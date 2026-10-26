// Package postgres adalah implementasi repository laporan di PostgreSQL.
//
// Paket ini memegang SATU-SATUNYA jalur tulis ke tabel `report_jobs`
// (migrasi 0005) dan membaca `order_lines`/`orders`/`branches`/
// `verification_logs` hanya untuk MEMBACA laporan (Database.md §7.3) — tidak
// ada penulisan ke tabel domain lain.
//
// Penitipan job River dan pembuatan baris `report_jobs` terjadi di dalam SATU
// transaksi (SystemDesign.md §4.2): tidak mungkin ada pekerjaan yang
// dijadwalkan tanpa barisnya, atau sebaliknya.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
	"github.com/grahita/portal/api/internal/report"
)

// JobInserter menitipkan pekerjaan River DI DALAM transaksi yang sedang
// berjalan. Dipenuhi oleh *river.Client[pgx.Tx]; dipisah menjadi interface
// supaya paket ini tidak bergantung pada kelas betonnya.
type JobInserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// Repo adalah repository laporan berbasis sqlc.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
	jobs JobInserter
}

// Option mengubah perilaku repository.
type Option func(*Repo)

// WithJobQueue memasang penjadwal pekerjaan (River) yang dipakai saat pekerjaan
// ekspor dibuat.
func WithJobQueue(q JobInserter) Option { return func(r *Repo) { r.jobs = q } }

// New membuat repository laporan. Tanpa WithJobQueue, permintaan ekspor ditolak
// dengan ErrQueueUnavailable — bukan membuat baris pekerjaan yang tak akan
// pernah dijalankan.
func New(pool *database.Pool, opts ...Option) *Repo {
	r := &Repo{pool: pool, q: sqlc.New(pool)}
	for _, o := range opts {
		o(r)
	}
	return r
}

// ListLines mengembalikan satu batch baris laporan sesuai Query: paginasi
// (Offset) untuk daftar, atau kursor (SetelahTanggal/SetelahNomor/
// SetelahLineNo) untuk ekspor bertahap.
func (r *Repo) ListLines(ctx context.Context, q report.Query) ([]report.Line, error) {
	rows, err := r.q.ListReportOrderLines(ctx, paramLines(q))
	if err != nil {
		return nil, fmt.Errorf("query ListReportOrderLines: %w", err)
	}
	out := make([]report.Line, 0, len(rows))
	for _, row := range rows {
		out = append(out, keLine(row))
	}
	return out, nil
}

// CountLines mengembalikan jumlah baris dan total subtotal untuk filter yang
// SAMA dengan ListLines.
func (r *Repo) CountLines(ctx context.Context, q report.Query) (int64, int64, error) {
	p := paramLines(q)
	row, err := r.q.CountReportOrderLines(ctx, sqlc.CountReportOrderLinesParams{
		Dari:      p.Dari,
		Sampai:    p.Sampai,
		CabangIds: p.CabangIds,
		Status:    p.Status,
		Jenis:     p.Jenis,
		IsCnc:     p.IsCnc,
		PajakIn:   p.PajakIn,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("query CountReportOrderLines: %w", err)
	}
	return row.JumlahBaris, row.TotalSubtotal, nil
}

// InsertJob membuat baris pekerjaan ekspor dan menitipkan job River-nya dalam
// satu transaksi.
func (r *Repo) InsertJob(ctx context.Context, req report.JobRequest) (report.Job, error) {
	if r.jobs == nil {
		return report.Job{}, report.ErrQueueUnavailable
	}
	filter, err := json.Marshal(req.Filter)
	if err != nil {
		return report.Job{}, fmt.Errorf("marshal filter laporan: %w", err)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return report.Job{}, fmt.Errorf("mulai transaksi pekerjaan ekspor: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := r.q.WithTx(tx).InsertReportJob(ctx, sqlc.InsertReportJobParams{
		Jenis:       req.Jenis,
		DimintaOleh: pgtypes.UUID(req.DimintaOleh),
		DimintaNama: req.DimintaNama,
		Filter:      filter,
	})
	if err != nil {
		return report.Job{}, fmt.Errorf("query InsertReportJob: %w", err)
	}
	if _, err := r.jobs.InsertTx(ctx, tx, jobs.ReportXLSXArgs{JobID: row.ID, Jenis: req.Jenis}, nil); err != nil {
		return report.Job{}, fmt.Errorf("%w: %v", report.ErrQueueUnavailable, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return report.Job{}, fmt.Errorf("commit pekerjaan ekspor: %w", err)
	}
	return jobDariInsert(row), nil
}

// GetJob mengembalikan satu pekerjaan ekspor; id yang tidak ada dijawab
// ErrNotFound.
func (r *Repo) GetJob(ctx context.Context, id string) (report.Job, error) {
	row, err := r.q.GetReportJob(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return report.Job{}, report.ErrNotFound
	}
	if err != nil {
		return report.Job{}, fmt.Errorf("query GetReportJob: %w", err)
	}
	return jobDariGet(row), nil
}

// MarkJobProses menandai pekerjaan sedang dikerjakan (started_at sekali saja).
func (r *Repo) MarkJobProses(ctx context.Context, id string) error {
	n, err := r.q.MarkReportJobProses(ctx, id)
	if err != nil {
		return fmt.Errorf("query MarkReportJobProses: %w", err)
	}
	if n == 0 {
		return report.ErrNotFound
	}
	return nil
}

// MarkJobSelesai menyimpan metadata berkas hasil.
func (r *Repo) MarkJobSelesai(ctx context.Context, id, storageKey, namaFile string, jumlahBaris int64) error {
	n, err := r.q.MarkReportJobSelesai(ctx, sqlc.MarkReportJobSelesaiParams{
		ID:          id,
		StorageKey:  storageKey,
		NamaFile:    namaFile,
		JumlahBaris: jumlahBaris,
	})
	if err != nil {
		return fmt.Errorf("query MarkReportJobSelesai: %w", err)
	}
	if n == 0 {
		return report.ErrNotFound
	}
	return nil
}

// MarkJobGagal menandai pekerjaan gagal beserta pesannya. Pekerjaan yang sudah
// selesai tidak pernah diturunkan menjadi gagal (percobaan ulang River tidak
// boleh membatalkan hasil yang sudah tersimpan) — 0 baris bukan kegagalan.
func (r *Repo) MarkJobGagal(ctx context.Context, id, pesan string) error {
	if _, err := r.q.MarkReportJobGagal(ctx, sqlc.MarkReportJobGagalParams{ID: id, Pesan: pesan}); err != nil {
		return fmt.Errorf("query MarkReportJobGagal: %w", err)
	}
	return nil
}

// paramLines menyusun parameter SQL dari Query. Daftar cabang SELALU dikirim
// sebagai irisan (bukan NULL): filter "tanpa cabang" berarti irisan kosong,
// sesuai `cardinality(...) = 0` pada query. Tanggal dikirim sebagai TEKS
// `YYYY-MM-DD` supaya konversinya ke `date` tidak bergantung zona waktu sesi
// database (lihat catatan di db/queries/report.sql).
func paramLines(q report.Query) sqlc.ListReportOrderLinesParams {
	cabang := q.Cabang
	if cabang == nil {
		cabang = []string{}
	}
	return sqlc.ListReportOrderLinesParams{
		Dari:           tanggalParam(q.Dari),
		Sampai:         tanggalParam(q.Sampai),
		CabangIds:      cabang,
		Status:         q.Status,
		Jenis:          q.Jenis,
		IsCnc:          pgtypes.BoolPtr(q.IsCNC),
		PajakIn:        pgtypes.BoolPtr(q.PajakIn),
		SetelahTanggal: tanggalParam(q.SetelahTanggal),
		SetelahNomor:   q.SetelahNomor,
		SetelahLineNo:  int32(q.SetelahLineNo),
		OffsetN:        int32(q.Offset),
		Batch:          int32(q.Batch),
	}
}

// tanggalParam mengubah tanggal filter/kursor menjadi teks `YYYY-MM-DD`;
// nil berarti tanpa batas.
func tanggalParam(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(report.LayoutTanggal)
}

// keLine memetakan satu baris SQL ke baris laporan.
func keLine(row sqlc.ListReportOrderLinesRow) report.Line {
	l := report.Line{
		Nomor:            row.Nomor,
		Versi:            int(row.Versi),
		BranchID:         row.BranchID,
		Cabang:           row.Cabang,
		KepalaCabang:     row.NamaKepala,
		IsCNC:            row.IsCnc,
		PajakIn:          row.PajakIn,
		LineNo:           int(row.LineNo),
		Produk:           row.Produk,
		Jenis:            row.Jenis,
		SerialNumber:     row.SerialNumber,
		Qty:              int(row.Qty),
		HargaSatuan:      row.HargaSatuan,
		Subtotal:         row.Subtotal,
		Status:           row.Status,
		MetodeVerifikasi: row.MetodeVerifikasi,
	}
	if row.Tanggal != nil {
		l.TanggalWaktu = *row.Tanggal
		l.Tanggal = row.Tanggal.Format(report.LayoutTanggal)
	}
	if row.DoneAt != nil {
		l.TanggalVerifikasi = row.DoneAt.In(clock.Jakarta()).Format(report.LayoutVerifikasi)
	}
	return l
}

// jobDariInsert memetakan baris hasil INSERT.
func jobDariInsert(row sqlc.InsertReportJobRow) report.Job {
	return report.Job{
		ID:          row.ID,
		Jenis:       row.Jenis,
		Status:      row.Status,
		DimintaNama: row.DimintaNama,
		Filter:      keFilter(row.Filter),
		NamaFile:    pgtypes.TextOr(row.NamaFile),
		JumlahBaris: row.JumlahBaris.Int64,
		Pesan:       pgtypes.TextOr(row.Pesan),
		CreatedAt:   row.CreatedAt,
		StartedAt:   row.StartedAt,
		FinishedAt:  row.FinishedAt,
	}
}

// jobDariGet memetakan baris hasil SELECT.
func jobDariGet(row sqlc.GetReportJobRow) report.Job {
	return report.Job{
		ID:          row.ID,
		Jenis:       row.Jenis,
		Status:      row.Status,
		DimintaNama: row.DimintaNama,
		Filter:      keFilter(row.Filter),
		NamaFile:    pgtypes.TextOr(row.NamaFile),
		JumlahBaris: row.JumlahBaris.Int64,
		Pesan:       pgtypes.TextOr(row.Pesan),
		CreatedAt:   row.CreatedAt,
		StartedAt:   row.StartedAt,
		FinishedAt:  row.FinishedAt,
		StorageKey:  pgtypes.TextOr(row.StorageKey),
	}
}

// keFilter membaca filter tersimpan. Filter yang tidak terbaca (data rusak)
// dianggap kosong: ekspor seluruh laporan, bukan gagal total.
func keFilter(raw []byte) report.Filter {
	var f report.Filter
	if len(raw) == 0 {
		return f
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return report.Filter{}
	}
	return f
}

var _ report.Repository = (*Repo)(nil)
