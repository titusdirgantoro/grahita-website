// Package worker menjalankan pekerjaan latar belakang dengan River.
//
// River menyimpan job di PostgreSQL yang sama dengan data bisnis, sehingga
// service bisa men-enqueue job di dalam transaksi yang sama (SystemDesign.md
// §4.2 dan §5.1) — tidak mungkin terjadi "pesanan tersimpan tapi job hilang".
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/grahita/portal/api/internal/backup"
	"github.com/grahita/portal/api/internal/fulfillment"
	"github.com/grahita/portal/api/internal/notification"
	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/report"
)

// PingArgs adalah job contoh yang dipakai untuk memverifikasi rantai
// enqueue → work berjalan. Job bisnis (email, PDF, OCR, reminder) menyusul di
// domain masing-masing dengan pola yang sama.
type PingArgs struct {
	Pesan string `json:"pesan"`
}

// Kind mengidentifikasi jenis job di tabel River.
func (PingArgs) Kind() string { return "ping" }

// PingWorker mencatat job ping ke log.
type PingWorker struct {
	river.WorkerDefaults[PingArgs]
}

// Work dijalankan River untuk setiap job ping.
func (PingWorker) Work(ctx context.Context, job *river.Job[PingArgs]) error {
	slog.InfoContext(ctx, "job ping diterima",
		"id", job.ID, "pesan", job.Args.Pesan, "attempt", job.Attempt)
	return nil
}

// EmailScanArgs adalah job pemindai email_outbox (F1-16). Job ini tidak
// membawa data apa pun: seluruh keadaan ada di tabel email_outbox, sehingga
// job yang terlewat/tergandakan tidak pernah membuat email terkirim dua kali
// (klaim baris memakai FOR UPDATE SKIP LOCKED).
type EmailScanArgs struct{}

// Kind mengidentifikasi job pemindai email di tabel River.
func (EmailScanArgs) Kind() string { return "email_outbox_pindai" }

// EmailScanWorker memindai email_outbox dan mengirim yang sudah waktunya.
type EmailScanWorker struct {
	river.WorkerDefaults[EmailScanArgs]
	notif *notification.Service
}

// Work menjalankan satu siklus pengiriman email.
func (w *EmailScanWorker) Work(ctx context.Context, job *river.Job[EmailScanArgs]) error {
	if w.notif == nil {
		return nil
	}
	hasil, err := w.notif.ProsesAntrean(ctx, 0)
	slog.InfoContext(ctx, "pemindaian email outbox",
		"diklaim", hasil.Diklaim, "terkirim", hasil.Terkirim,
		"ditunda", hasil.Ditunda, "menyerah", hasil.Menyerah)
	return err
}

// BackupArgs adalah job jadwal cadangan harian (F1-21). Satu job = satu
// pemeriksaan jadwal: pekerjaan yang benar-benar dijalankan ditentukan
// penjaga jadwal di domain backup (02:00 WIB, sekali per hari per jenis),
// sehingga job yang terlewat/tergandakan tidak pernah membuat cadangan dobel.
type BackupArgs struct{}

// Kind mengidentifikasi jenis job cadangan di tabel River.
func (BackupArgs) Kind() string { return "backup_jadwal" }

// BackupWorker menjalankan pekerjaan cadangan yang sudah waktunya.
type BackupWorker struct {
	river.WorkerDefaults[BackupArgs]
	svc *backup.Service
}

// Work menjalankan satu pemeriksaan jadwal cadangan.
func (w *BackupWorker) Work(ctx context.Context, job *river.Job[BackupArgs]) error {
	if w.svc == nil {
		return nil
	}
	hasil := w.svc.JalankanHarian(ctx, time.Now().UTC())
	for _, h := range hasil {
		if h.Sukses {
			slog.InfoContext(ctx, "cadangan selesai", "jenis", h.Jenis, "ukuran_byte", h.UkuranByte, "tujuan", h.Tujuan)
			continue
		}
		slog.ErrorContext(ctx, "cadangan gagal", "jenis", h.Jenis, "pesan", h.Pesan, "err", h.Err)
	}
	// Kegagalan cadangan sudah dicatat di backup_runs dan diberitahukan ke
	// Superadmin; job TIDAK dikembalikan sebagai error supaya River tidak
	// mencoba ulang tanpa henti (retry tidak akan memperbaiki kredensial
	// tujuan atau kunci yang salah, dan justru menutupi alarm).
	return nil
}

// ServerSelesai adalah pekerjaan lanjutan setelah pesanan Done: render dokumen
// lalu antre email dokumen (fulfillment.Service, kartu F1-18), dan pekerjaan
// setelah revisi invoice: render invoice versi baru lalu antre email invoice
// (kartu F2-25). Keduanya dipenuhi *fulfillment.Service.
type ServerSelesai interface {
	PesananSelesai(ctx context.Context, orderID string) (fulfillment.Hasil, error)
	PesananDirevisi(ctx context.Context, orderID string) (fulfillment.Hasil, error)
}

// Laporan adalah pelaksana ekspor XLSX (report.Service, kartu F2-24).
type Laporan interface {
	Jalankan(ctx context.Context, jobID string) (report.Job, error)
}

// ReportXLSXWorker menjalankan job "report_xlsx": menulis satu laporan XLSX
// bertahap (streaming) lalu menyimpannya ke object storage (PRD §4.7 AP-REP-04).
//
// Seluruh keadaan pekerjaan ada di tabel `report_jobs`, jadi job yang terlewat
// atau tergandakan tidak pernah menghasilkan berkas ganda: pekerjaan yang sudah
// selesai dilewati tanpa efek samping.
type ReportXLSXWorker struct {
	river.WorkerDefaults[jobs.ReportXLSXArgs]
	laporan Laporan
}

// NewReportXLSXWorker membuat worker ekspor laporan.
func NewReportXLSXWorker(l Laporan) *ReportXLSXWorker {
	return &ReportXLSXWorker{laporan: l}
}

// Kind adalah Kind job di tabel River.
func (ReportXLSXWorker) Kind() string { return jobs.KindReportXLSX }

// Timeout memberi ekspor besar ruang lebih dari batas bawaan klien (2 menit):
// 50.000 baris tetap harus selesai tanpa dipotong di tengah jalan.
func (w *ReportXLSXWorker) Timeout(*river.Job[jobs.ReportXLSXArgs]) time.Duration {
	return 30 * time.Minute
}

// Work menjalankan satu pekerjaan ekspor.
func (w *ReportXLSXWorker) Work(ctx context.Context, job *river.Job[jobs.ReportXLSXArgs]) error {
	if w.laporan == nil {
		// Tanpa service, pekerjaan tidak bisa dikerjakan — jangan dilepas
		// begitu saja (Superadmin akan menunggu berkas yang tak pernah ada).
		return fmt.Errorf("job %s: layanan laporan belum terpasang", w.Kind())
	}
	hasil, err := w.laporan.Jalankan(ctx, job.Args.JobID)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "ekspor laporan selesai",
		"job_id", hasil.ID, "jenis", hasil.Jenis, "baris", hasil.JumlahBaris)
	return nil
}

// RenderDokumenWorker menjalankan job "render_dokumen_pesanan": memastikan
// Invoice/Surat Jalan pesanan ada, lalu mengantre email dokumennya
// (SystemDesign.md §4.4). Job ini dititipkan transaksi verifikasi (F1-18),
// jadi pesanan yang baru selesai tidak mungkin kehilangan pekerjaan dokumennya.
//
// Kegagalan render (mesin PDF/object storage belum siap) dikembalikan supaya
// River mengulanginya; email baru diantre setelah dokumennya benar-benar ada.
type RenderDokumenWorker struct {
	river.WorkerDefaults[jobs.RenderDokumenArgs]
	selesai ServerSelesai
}

// NewRenderDokumenWorker membuat worker pemenuhan pesanan Done. Dipakai
// composition root (mode worker) dan pengujian yang menjalankan satu pekerjaan
// secara langsung.
func NewRenderDokumenWorker(selesai ServerSelesai) *RenderDokumenWorker {
	return &RenderDokumenWorker{selesai: selesai}
}

// Kind adalah Kind job di tabel River.
func (RenderDokumenWorker) Kind() string { return jobs.KindRenderDokumen }

// Work menjalankan satu penyelesaian pesanan.
func (w *RenderDokumenWorker) Work(ctx context.Context, job *river.Job[jobs.RenderDokumenArgs]) error {
	if w.selesai == nil {
		// Tanpa service, job tidak bisa dikerjakan — jangan lepas begitu saja
		// (pesanan Done akan kehilangan dokumen & email).
		return fmt.Errorf("job %s: layanan pemenuhan pesanan belum terpasang", w.Kind())
	}
	hasil, err := w.selesai.PesananSelesai(ctx, job.Args.OrderID)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "pesanan selesai dipenuhi",
		"order_id", job.Args.OrderID, "dokumen", hasil.Dokumen,
		"email_dibuat", hasil.EmailDibuat, "email_sudah_ada", hasil.EmailSudahAda,
		"email_tanpa_penerima", hasil.EmailTanpaPenerima)
	return nil
}

// RevisiInvoiceWorker menjalankan job "revisi_invoice": memastikan Invoice PDF
// VERSI BARU ada, lalu mengantre email invoice versi itu ke cabang dan admin
// ber-flag Invoice (SystemDesign.md §4.5). Job ini dititipkan transaksi revisi
// (F2-25), jadi versi pesanan yang naik tidak pernah kehilangan invoice baru.
//
// Surat Jalan TIDAK disentuh/dikirim ulang: revisi hanya menyentuh invoice.
// Kegagalan render dikembalikan supaya River mengulanginya; email baru diantre
// setelah invoice versi baru benar-benar ada.
type RevisiInvoiceWorker struct {
	river.WorkerDefaults[jobs.RevisiInvoiceArgs]
	revisi ServerSelesai
}

// NewRevisiInvoiceWorker membuat worker pemenuhan revisi invoice.
func NewRevisiInvoiceWorker(revisi ServerSelesai) *RevisiInvoiceWorker {
	return &RevisiInvoiceWorker{revisi: revisi}
}

// Kind adalah Kind job di tabel River.
func (RevisiInvoiceWorker) Kind() string { return jobs.KindRevisiInvoice }

// Work menjalankan satu pemenuhan revisi invoice.
func (w *RevisiInvoiceWorker) Work(ctx context.Context, job *river.Job[jobs.RevisiInvoiceArgs]) error {
	if w.revisi == nil {
		// Tanpa service, job tidak bisa dikerjakan — jangan dilepas begitu saja
		// (pesanan yang direvisi akan kehilangan invoice versi barunya).
		return fmt.Errorf("job %s: layanan pemenuhan revisi belum terpasang", w.Kind())
	}
	hasil, err := w.revisi.PesananDirevisi(ctx, job.Args.OrderID)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "revisi invoice dipenuhi",
		"order_id", job.Args.OrderID, "dokumen", hasil.Dokumen,
		"email_dibuat", hasil.EmailDibuat, "email_sudah_ada", hasil.EmailSudahAda,
		"email_tanpa_penerima", hasil.EmailTanpaPenerima)
	return nil
}

// Client membungkus River client beserta pool-nya.
type Client struct {
	river *river.Client[pgx.Tx]
}

// PencatatJob menerima hasil satu pekerjaan River yang selesai dikerjakan.
//
// Interface ini sengaja kecil dan hidup di paket worker (bukan di paket
// observability) supaya paket worker tidak bergantung pada Prometheus:
// composition root menyerahkan implementasinya (observability.Metrics).
type PencatatJob interface {
	CatatJob(kind string, durasi time.Duration, err error)
}

// Opsi menyesuaikan pembuatan worker tanpa mengubah pemanggil yang sudah ada.
type Opsi func(*konfigurasiWorker)

type konfigurasiWorker struct {
	pencatat PencatatJob
}

// DenganPencatatJob memasang pencatat metrik pekerjaan (Architecture.md §8).
func DenganPencatatJob(p PencatatJob) Opsi {
	return func(k *konfigurasiWorker) { k.pencatat = p }
}

// HookMetrikJob mengembalikan hook River yang mencatat durasi dan hasil setiap
// pekerjaan ke pencatat. Mengembalikan nil bila pencatat nil.
//
// HookWorkBegin/WorkEnd tidak berbagi context (didokumentasikan River), jadi
// waktu mulai disimpan pada peta berkunci keyed oleh id job.
func HookMetrikJob(pencatat PencatatJob) []rivertype.Hook {
	if pencatat == nil {
		return nil
	}
	var mu sync.Mutex
	mulai := map[int64]time.Time{}
	return []rivertype.Hook{
		river.HookWorkBeginFunc(func(_ context.Context, job *rivertype.JobRow) error {
			mu.Lock()
			mulai[job.ID] = time.Now()
			mu.Unlock()
			return nil
		}),
		river.HookWorkEndFunc(func(_ context.Context, job *rivertype.JobRow, err error) error {
			mu.Lock()
			t, ada := mulai[job.ID]
			delete(mulai, job.ID)
			mu.Unlock()
			var durasi time.Duration
			if ada {
				durasi = time.Since(t)
			}
			pencatat.CatatJob(job.Kind, durasi, err)
			return err
		}),
	}
}

// New membuat River client dengan queue sesuai konfigurasi.
//
// notif boleh nil (mis. pengujian unit worker tanpa database domain); bila
// diisi, worker mengirim email_outbox secara berkala. bk boleh nil: tanpa
// service cadangan (BACKUP_TARGET kosong) tidak ada job cadangan yang
// dijadwalkan sama sekali. selesai boleh nil (mis. mode lokal tanpa dokumen);
// bila diisi, worker menjalankan job render dokumen + antre email (F1-18).
// laporan boleh nil: tanpa service laporan, job ekspor XLSX tidak didaftarkan
// (permintaan ekspor sudah ditolak lebih dulu di API — tidak ada pekerjaan
// menggantung).
func New(pool *database.Pool, cfg config.Config, notif *notification.Service, bk *backup.Service, selesai ServerSelesai, laporan Laporan, opts ...Opsi) (*Client, error) {
	var konf konfigurasiWorker
	for _, o := range opts {
		o(&konf)
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, &PingWorker{})
	if notif != nil {
		river.AddWorker(workers, &EmailScanWorker{notif: notif})
	}
	if bk != nil {
		river.AddWorker(workers, &BackupWorker{svc: bk})
	}
	if selesai != nil {
		river.AddWorker(workers, NewRenderDokumenWorker(selesai))
		river.AddWorker(workers, NewRevisiInvoiceWorker(selesai))
	}
	if laporan != nil {
		river.AddWorker(workers, NewReportXLSXWorker(laporan))
	}

	queues := map[string]river.QueueConfig{}
	for name, max := range cfg.WorkerQueues {
		if max <= 0 {
			max = 1
		}
		queues[name] = river.QueueConfig{MaxWorkers: max}
	}
	if len(queues) == 0 {
		queues["default"] = river.QueueConfig{MaxWorkers: 4}
	}

	// River mensyaratkan FetchCooldown tidak lebih besar dari FetchPollInterval.
	cooldown := 200 * time.Millisecond
	if cfg.JobPollInterval > 0 && cfg.JobPollInterval < cooldown {
		cooldown = cfg.JobPollInterval / 2
	}

	periodic := []*river.PeriodicJob{}
	if notif != nil {
		interval := cfg.OutboxInterval
		if interval < time.Second {
			interval = time.Minute
		}
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(interval),
			func() (river.JobArgs, *river.InsertOpts) { return EmailScanArgs{}, nil },
			&river.PeriodicJobOpts{ID: "email-outbox", RunOnStart: true},
		))
	}
	if bk != nil {
		interval := cfg.BackupInterval
		if interval < time.Minute {
			interval = 15 * time.Minute
		}
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(interval),
			func() (river.JobArgs, *river.InsertOpts) { return BackupArgs{}, nil },
			&river.PeriodicJobOpts{ID: "backup-harian", RunOnStart: true},
		))
	}

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:            queues,
		Workers:           workers,
		PeriodicJobs:      periodic,
		JobTimeout:        2 * time.Minute,
		FetchCooldown:     cooldown,
		FetchPollInterval: cfg.JobPollInterval,
		SoftStopTimeout:   cfg.WorkerShutdown,
		Hooks:             HookMetrikJob(konf.pencatat),
	})
	if err != nil {
		return nil, fmt.Errorf("buat river client: %w", err)
	}
	return &Client{river: client}, nil
}

// River mengembalikan client River yang mendasarinya (dipakai untuk enqueue).
func (c *Client) River() *river.Client[pgx.Tx] { return c.river }

// Run menjalankan worker sampai context dibatalkan.
func (c *Client) Run(ctx context.Context) error {
	if err := c.river.Start(ctx); err != nil {
		return fmt.Errorf("jalankan river: %w", err)
	}
	slog.Info("worker river berjalan")

	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.river.Stop(stopCtx); err != nil {
		return fmt.Errorf("hentikan river: %w", err)
	}
	return nil
}

// EnqueuePing memasukkan job ping — dipakai /healthz manual dan pengujian.
func (c *Client) EnqueuePing(ctx context.Context, pesan string) (int64, error) {
	res, err := c.river.Insert(ctx, PingArgs{Pesan: pesan}, nil)
	if err != nil {
		return 0, err
	}
	return res.Job.ID, nil
}

// JobCount mengembalikan jumlah job pada satu state (untuk /readyz dan uji).
func JobCount(ctx context.Context, pool *database.Pool, state string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE state = $1`, state).Scan(&n)
	return n, err
}
