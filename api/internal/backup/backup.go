// Package backup adalah domain cadangan terenkripsi: dump PostgreSQL, arsip
// WAL (bila PITR aktif), dan sinkronisasi bukti/dokumen ke tujuan luar
// (Google Drive lewat rclone) — Architecture.md §7, PRD §9 butir 11,
// Database.md §4.7 (tabel `backup_runs`).
//
// Aturan yang ditegakkan paket ini:
//
//   - Dump database SELALU `pg_dump -Fc` yang dienkripsi age SEBELUM
//     meninggalkan host. Tidak ada jalur yang menulis dump polos ke tujuan,
//     dan target tidak boleh dikonfigurasi tanpa kunci publik age.
//   - Hal yang sama berlaku untuk arsip WAL (bila PITR aktif): setiap segmen
//     dienkripsi age sebelum diunggah (`wal/<segmen>.age`), dan jalur
//     pemulihannya (PulihkanWAL) menolak berkas yang bukan hasil age.
//   - Setiap pekerjaan meninggalkan jejak di tabel `backup_runs`
//     (jenis, mulai, selesai, sukses, ukuran_byte, tujuan, pesan).
//   - Kegagalan memicu pemberitahuan: email ke seluruh Superadmin aktif +
//     entri audit. (Architecture.md §7: "kegagalan memunculkan notifikasi di
//     dashboard dan email ke Superadmin".)
//   - Sinkronisasi isi object storage bersifat ADITIF dan idempoten
//     (manifest), tidak pernah menghapus berkas asli. Bukti transfer
//     beretensi TANPA BATAS (keputusan pemilik): tidak ada satu pun aturan
//     di paket ini yang boleh menghapus `proofs/...` — baik di storage asli
//     maupun di salinan.
//   - Rekonsiliasi DB↔object storage dilaporkan apa adanya; sapu objek yatim
//     bersifat konservatif: bawaannya hanya MELAPORKAN (dry-run), hanya
//     menyentuh `documents/...` yang tidak dirujuk baris `documents`, harus
//     melewati grace period (berkas yang mungkin sedang ditulis render aktif
//     tidak pernah dihapus), dan setiap tindakan dicatat di audit.
package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/storage"
)

// Jenis pekerjaan cadangan (kolom backup_runs.jenis, Database.md §4.7).
const (
	JenisPostgres = "postgres"
	JenisStorage  = "storage"
	JenisWAL      = "wal"
)

// SemuaJenis adalah urutan pekerjaan yang dijalankan jadwal harian.
var SemuaJenis = []string{JenisPostgres, JenisStorage, JenisWAL}

// Prefix kunci di tujuan cadangan.
const (
	PrefixPostgres = "postgres/"
	PrefixStorage  = "storage/"
	PrefixWAL      = "wal/"
	// NamaManifest adalah berkas manifest sinkronisasi objek di tujuan.
	NamaManifest = PrefixStorage + "_manifest.json"
)

// SufiksAge adalah sufiks WAJIB berkas terenkripsi age di tujuan cadangan:
// setiap byte yang meninggalkan host (dump, salinan bukti/dokumen, segmen WAL)
// berakhiran `.age` (Architecture.md §6).
const SufiksAge = ".age"

// NamaWALTerEnkripsi menyusun nama arsip satu segmen WAL di tujuan:
// `wal/<berkas WAL>.age`. Sufiks `.age` menandai bahwa berkas DI TUJUAN selalu
// terenkripsi — tidak ada nama yang menjanjikan WAL polos.
func NamaWALTerEnkripsi(namaBerkasWAL string) string {
	return PrefixWAL + namaBerkasWAL + SufiksAge
}

// AktorSistem adalah actor_id untuk entri audit dari pekerjaan latar (backup
// bukan tindakan manusia): actor_kind 'system', actor_id UUID nol.
const AktorSistem = "00000000-0000-0000-0000-000000000000"

// Entity/aksi audit untuk pekerjaan cadangan.
const (
	EntityBackupRun   = "backup_run"
	EntityBackupSweep = "backup_sweep"
	ActionBackupGagal = "gagal"
	ActionSweepKering = "sweep_kering"
	ActionSweepHapus  = "sweep_hapus"
)

// Sentinel error domain. Pemetaan ke kode HTTP dilakukan lapisan transport.
var (
	// ErrTujuanBelumDiatur: target cadangan (dir/rclone) belum dikonfigurasi.
	ErrTujuanBelumDiatur = errors.New("tujuan cadangan belum dikonfigurasi")
	// ErrKunciEnkripsi: kunci age belum diisi sehingga cadangan tidak boleh jalan.
	ErrKunciEnkripsi = errors.New("kunci enkripsi cadangan (age) belum dikonfigurasi")
	// ErrCadanganKosong: belum ada berkas cadangan di tujuan.
	ErrCadanganKosong = errors.New("belum ada cadangan di tujuan")
	// ErrBentukKunci: kunci objek tidak mengikuti pola yang dikenal.
	ErrBentukKunci = errors.New("bentuk kunci objek tidak dikenal")
	// ErrTanpaDatabase: pekerjaan berkas murni (mis. dekripsi arsip WAL untuk
	// drill/PITR) tidak boleh menyentuh database sama sekali.
	ErrTanpaDatabase = errors.New("pekerjaan ini tidak memakai database")
)

// TanpaRepo adalah Repo kosong untuk pekerjaan yang MURNI berkas.
//
// Dipakai jalur pemulihan arsip WAL: drill/PITR sering justru dijalankan ketika
// database sumber tidak tersedia, jadi langkah dekripsi tidak boleh bergantung
// pada koneksi database. Seluruh metode menolak dengan jelas supaya tidak ada
// pekerjaan yang diam-diam "berhasil" tanpa data.
type TanpaRepo struct{}

// Mulai selalu menolak.
func (TanpaRepo) Mulai(context.Context, string, time.Time) (Run, error) {
	return Run{}, ErrTanpaDatabase
}

// Selesai selalu menolak.
func (TanpaRepo) Selesai(context.Context, string, time.Time, bool, int64, string, string) error {
	return ErrTanpaDatabase
}

// Terakhir selalu menolak.
func (TanpaRepo) Terakhir(context.Context, int) ([]Run, error) { return nil, ErrTanpaDatabase }

// Sejak selalu menolak.
func (TanpaRepo) Sejak(context.Context, string, time.Time) (Run, bool, error) {
	return Run{}, false, ErrTanpaDatabase
}

// SuperadminEmails selalu menolak.
func (TanpaRepo) SuperadminEmails(context.Context) ([]string, error) { return nil, ErrTanpaDatabase }

// KunciProofs selalu menolak.
func (TanpaRepo) KunciProofs(context.Context) ([]string, error) { return nil, ErrTanpaDatabase }

// KunciDokumen selalu menolak.
func (TanpaRepo) KunciDokumen(context.Context) ([]string, error) { return nil, ErrTanpaDatabase }

// Audit selalu menolak.
func (TanpaRepo) Audit(context.Context, audit.Entry) error { return ErrTanpaDatabase }

// Run adalah satu baris tabel backup_runs.
type Run struct {
	ID         string     `json:"id"`
	Jenis      string     `json:"jenis"`
	Mulai      time.Time  `json:"mulai"`
	Selesai    *time.Time `json:"selesai,omitempty"`
	Sukses     *bool      `json:"sukses,omitempty"`
	UkuranByte int64      `json:"ukuran_byte"`
	Tujuan     string     `json:"tujuan,omitempty"`
	Pesan      string     `json:"pesan,omitempty"`
}

// Repo menyimpan jejak pekerjaan cadangan dan menyediakan data pendukungnya.
type Repo interface {
	// Mulai menyisipkan baris backup_runs (selesai/sukses masih NULL).
	Mulai(ctx context.Context, jenis string, mulai time.Time) (Run, error)
	// Selesai menutup baris backup_runs.
	Selesai(ctx context.Context, id string, selesai time.Time, sukses bool, ukuranByte int64, tujuan, pesan string) error
	// Terakhir mengembalikan run terbaru (semua jenis), terbaru dulu.
	Terakhir(ctx context.Context, limit int) ([]Run, error)
	// Sejak melaporkan apakah sudah ada run satu jenis yang MULAI sejak
	// `sejak` (dipakai penjaga jadwal harian supaya tidak dobel).
	Sejak(ctx context.Context, jenis string, sejak time.Time) (Run, bool, error)
	// SuperadminEmails mengembalikan email superadmin aktif yang sudah
	// diaktivasi (penerima alarm kegagalan cadangan).
	SuperadminEmails(ctx context.Context) ([]string, error)
	// KunciProofs mengembalikan seluruh storage_key baris payment_proofs.
	KunciProofs(ctx context.Context) ([]string, error)
	// KunciDokumen mengembalikan seluruh storage_key baris documents.
	KunciDokumen(ctx context.Context) ([]string, error)
	// Audit menyimpan satu entri audit (SystemDesign.md §5.4).
	Audit(ctx context.Context, e audit.Entry) error
}

// Hasil merangkum satu pekerjaan cadangan (log, CLI, dan pengujian).
type Hasil struct {
	Jenis      string `json:"jenis"`
	Sukses     bool   `json:"sukses"`
	UkuranByte int64  `json:"ukuran_byte"`
	Tujuan     string `json:"tujuan,omitempty"`
	Pesan      string `json:"pesan,omitempty"`
	Err        error  `json:"-"`
}

// Config adalah parameter operasional cadangan.
type Config struct {
	// Kerja adalah direktori kerja sementara (dump → enkripsi → unggah).
	Kerja string
	// Recipient adalah kunci publik age (age1...). WAJIB: tanpa ini cadangan
	// tidak boleh berjalan sama sekali.
	Recipient string
	// Identity adalah berkas identitas age untuk dekripsi (uji restore).
	Identity string
	// PgDump/PgRestore adalah biner klien PostgreSQL.
	PgDump    string
	PgRestore string
	// DSN adalah DSN database sumber (dipakai pg_dump; password dikirim lewat
	// environment anak, bukan argumen proses).
	DSN string
	// WALDir adalah direktori arsip WAL (kosong = PITR tidak aktif).
	WALDir string
	// Retensi.
	RetensiHarian  int
	RetensiBulanan int
	RetensiWALHari int
	// SweepGrace adalah umur minimum objek yatim sebelum boleh disapu.
	SweepGrace time.Duration
	// JamHarian adalah jam (WIB) jadwal harian, default 02:00 (Architecture.md §7).
	JamHarian int
	// Zona adalah zona waktu jadwal (default WIB).
	Zona *time.Location
}

// denganBawaan mengisi nilai bawaan parameter.
func (c Config) denganBawaan() Config {
	if c.Kerja == "" {
		c.Kerja = "."
	}
	if c.PgDump == "" {
		c.PgDump = "pg_dump"
	}
	if c.PgRestore == "" {
		c.PgRestore = "pg_restore"
	}
	if c.RetensiHarian <= 0 {
		c.RetensiHarian = 30
	}
	if c.RetensiBulanan <= 0 {
		c.RetensiBulanan = 12
	}
	if c.RetensiWALHari <= 0 {
		c.RetensiWALHari = 7
	}
	if c.SweepGrace <= 0 {
		c.SweepGrace = 24 * time.Hour
	}
	if c.JamHarian <= 0 || c.JamHarian > 23 {
		// 02:00 WIB adalah jadwal yang diminta (Architecture.md §7). Jam 0
		// tidak dibedakan dari "belum diisi" secara sengaja: jadwal tengah
		// malam tidak dipakai sistem ini.
		c.JamHarian = 2
	}
	if c.Zona == nil {
		c.Zona = clock.Jakarta()
	}
	return c
}

// Service menjalankan seluruh pekerjaan cadangan.
type Service struct {
	cfg    Config
	repo   Repo
	target Target
	objek  storage.Store
	kripto *Kripto
	dumper *Dumper
	notif  Notifier
	clk    clock.Clock
	log    *slog.Logger
}

// Option menyesuaikan Service.
type Option func(*Service)

// WithTarget memasang tujuan cadangan (Dir atau Rclone).
func WithTarget(t Target) Option { return func(s *Service) { s.target = t } }

// WithObjectStore memasang pembaca object storage (bukti & dokumen).
func WithObjectStore(o storage.Store) Option { return func(s *Service) { s.objek = o } }

// WithNotifier memasang pemberi tahu kegagalan (email Superadmin + audit).
func WithNotifier(n Notifier) Option { return func(s *Service) { s.notif = n } }

// WithClock memasang sumber waktu (pengujian deterministik).
func WithClock(c clock.Clock) Option { return func(s *Service) { s.clk = c } }

// WithLogger memasang logger.
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.log = l } }

// New membuat service cadangan.
//
// Kunci enkripsi dibaca sekali di sini: bila `Recipient` kosong, seluruh
// pekerjaan cadangan akan menolak berjalan (fail closed) — lebih baik tidak
// ada cadangan daripada cadangan polos yang tak terenkripsi.
func New(cfg Config, repo Repo, opts ...Option) *Service {
	cfg = cfg.denganBawaan()
	s := &Service{
		cfg:   cfg,
		repo:  repo,
		clk:   clock.System(),
		log:   slog.Default(),
		objek: storage.Disabled{},
	}
	for _, o := range opts {
		o(s)
	}
	if s.target == nil {
		s.target = Nol{}
	}
	s.dumper = &Dumper{BinerDump: cfg.PgDump, BinerRestore: cfg.PgRestore, Log: s.log}
	s.kripto = NewKripto(cfg.Recipient, cfg.Identity)
	return s
}

// Config mengembalikan konfigurasi efektif (dipakai CLI dan pengujian).
func (s *Service) Config() Config { return s.cfg }

// Target mengembalikan tujuan cadangan efektif.
func (s *Service) Target() Target { return s.target }

// Jadwal mengembalikan deskripsi jadwal harian (untuk halaman Pengaturan).
func (s *Service) Jadwal() string {
	return fmt.Sprintf("setiap hari %02d:00 WIB", s.cfg.JamHarian)
}

// PerluJalan melaporkan apakah satu jenis pekerjaan harus dijalankan sekarang:
// jam WIB sudah melewati jam jadwal DAN belum ada run jenis itu yang mulai
// pada hari WIB yang sama. Pekerjaan yang terlewat (worker mati) mengejar di
// tick berikutnya — RPO tetap terjaga tanpa dobel.
func (s *Service) PerluJalan(ctx context.Context, jenis string, sekarang time.Time) (bool, error) {
	lokal := sekarang.In(s.cfg.Zona)
	if lokal.Hour() < s.cfg.JamHarian {
		return false, nil
	}
	awal := time.Date(lokal.Year(), lokal.Month(), lokal.Day(), 0, 0, 0, 0, s.cfg.Zona)
	if _, ada, err := s.repo.Sejak(ctx, jenis, awal.UTC()); err != nil {
		return false, err
	} else if ada {
		return false, nil
	}
	return true, nil
}

// JalankanHarian menjalankan pekerjaan yang memang sudah waktunya.
func (s *Service) JalankanHarian(ctx context.Context, sekarang time.Time) []Hasil {
	hasil := make([]Hasil, 0, len(SemuaJenis))
	for _, jenis := range SemuaJenis {
		perlu, err := s.PerluJalan(ctx, jenis, sekarang)
		if err != nil {
			hasil = append(hasil, Hasil{Jenis: jenis, Err: err, Pesan: "penjaga jadwal gagal dibaca"})
			continue
		}
		if !perlu {
			continue
		}
		hasil = append(hasil, s.Jalankan(ctx, jenis))
	}
	return hasil
}

// Jalankan menjalankan satu jenis pekerjaan cadangan dan mencatat hasilnya.
func (s *Service) Jalankan(ctx context.Context, jenis string) Hasil {
	switch jenis {
	case JenisPostgres:
		return s.catat(ctx, jenis, s.kerjaPostgres)
	case JenisStorage:
		return s.catat(ctx, jenis, s.kerjaStorage)
	case JenisWAL:
		return s.catat(ctx, jenis, s.kerjaWAL)
	default:
		return Hasil{Jenis: jenis, Err: fmt.Errorf("jenis cadangan tidak dikenal: %q", jenis)}
	}
}

// catat membungkus satu pekerjaan dengan pencatatan backup_runs + alarm.
func (s *Service) catat(ctx context.Context, jenis string, kerja func(context.Context) (ukuran int64, tujuan, pesan string, err error)) Hasil {
	mulai := s.clk.Now().UTC()
	run, err := s.repo.Mulai(ctx, jenis, mulai)
	if err != nil {
		return Hasil{Jenis: jenis, Err: fmt.Errorf("catat mulai cadangan %s: %w", jenis, err)}
	}

	ukuran, tujuan, pesan, errKerja := kerja(ctx)

	sukses := errKerja == nil
	if !sukses {
		pesan = gabungPesan(pesan, errKerja.Error())
	}
	selesai := s.clk.Now().UTC()
	if err := s.repo.Selesai(ctx, run.ID, selesai, sukses, ukuran, tujuan, pesan); err != nil {
		s.log.Error("gagal menutup catatan backup_runs", "jenis", jenis, "err", err)
		if errKerja == nil {
			errKerja = fmt.Errorf("tutup catatan backup_runs: %w", err)
			sukses = false
		}
	}

	hasil := Hasil{Jenis: jenis, Sukses: sukses, UkuranByte: ukuran, Tujuan: tujuan, Pesan: pesan, Err: errKerja}
	if !sukses {
		s.log.Error("cadangan gagal", "jenis", jenis, "pesan", pesan)
		s.beritahuGagal(ctx, run, pesan)
		return hasil
	}
	s.log.Info("cadangan selesai", "jenis", jenis, "ukuran_byte", ukuran, "tujuan", tujuan, "pesan", pesan)
	return hasil
}

// beritahuGagal menjalankan pemberi tahu (email Superadmin + audit) tanpa
// pernah menutupi kegagalan aslinya.
func (s *Service) beritahuGagal(ctx context.Context, run Run, sebab string) {
	if s.notif == nil {
		return
	}
	if err := s.notif.BackupGagal(ctx, run, sebab); err != nil {
		s.log.Error("pemberitahuan kegagalan cadangan gagal dikirim", "jenis", run.Jenis, "err", err)
	}
}

// Ringkasan adalah data yang ditampilkan di halaman Pengaturan admin
// (Architecture.md §7) sekaligus sumber "perlu tindakan" untuk dashboard.
type Ringkasan struct {
	Terakhir       []Run                `json:"terakhir"`
	PerluTindakan  bool                 `json:"perlu_tindakan"`
	Pesan          string               `json:"pesan,omitempty"`
	Tujuan         string               `json:"tujuan"`
	Jadwal         string               `json:"jadwal"`
	Retensi        Retensi              `json:"retensi"`
	SuksesTerakhir map[string]time.Time `json:"sukses_terakhir,omitempty"`
}

// Retensi merangkum kebijakan retensi yang berlaku.
type Retensi struct {
	Harian          int  `json:"harian"`
	Bulanan         int  `json:"bulanan"`
	WALHari         int  `json:"wal_hari"`
	BuktiTanpaBatas bool `json:"bukti_tanpa_batas"`
}

// Ringkasan menyusun ringkasan status cadangan terakhir.
func (s *Service) Ringkasan(ctx context.Context, limit int) (Ringkasan, error) {
	if limit <= 0 {
		limit = 20
	}
	runs, err := s.repo.Terakhir(ctx, limit)
	if err != nil {
		return Ringkasan{}, err
	}
	r := Ringkasan{
		Terakhir: runs,
		Tujuan:   s.target.Nama(),
		Jadwal:   s.Jadwal(),
		Retensi: Retensi{
			Harian:          s.cfg.RetensiHarian,
			Bulanan:         s.cfg.RetensiBulanan,
			WALHari:         s.cfg.RetensiWALHari,
			BuktiTanpaBatas: true,
		},
		SuksesTerakhir: map[string]time.Time{},
	}
	for _, run := range runs {
		if run.Sukses != nil && *run.Sukses {
			if _, ada := r.SuksesTerakhir[run.Jenis]; !ada {
				r.SuksesTerakhir[run.Jenis] = run.Mulai
			}
		}
	}
	// Baris paling baru yang GAGAL per jenis (belum ditutup sukses setelahnya)
	// berarti masih perlu tindakan.
	//
	// `runs` datang TERBARU-DULU dari repository (ORDER BY mulai DESC), jadi
	// kegagalan PERTAMA yang ditemui untuk satu jenis adalah kegagalan yang
	// terbaru. Nilai itu tidak boleh ditimpa kegagalan yang lebih tua: putaran
	// QA-1 menemukan loop yang menimpa terus sehingga nilai akhirnya adalah
	// kegagalan TERTUA di jendela, dan status bisa berbunyi "aman" tepat
	// setelah cadangan terakhir gagal (pola gagal → sukses → gagal).
	gagalTerakhir := map[string]time.Time{}
	for _, run := range runs {
		if run.Sukses == nil || *run.Sukses {
			continue
		}
		if _, sudah := gagalTerakhir[run.Jenis]; !sudah {
			gagalTerakhir[run.Jenis] = run.Mulai
		}
	}
	// Urutan tetap (jenis pekerjaan harian lebih dahulu, sisanya alfabetis)
	// supaya pesan ringkasan tidak berubah-ubah antar pemanggilan.
	for _, jenis := range urutanJenis(gagalTerakhir) {
		waktuGagal := gagalTerakhir[jenis]
		sukses, ada := r.SuksesTerakhir[jenis]
		if !ada || sukses.Before(waktuGagal) {
			r.PerluTindakan = true
			r.Pesan = fmt.Sprintf("Cadangan %s gagal pada %s dan belum ada cadangan sukses sesudahnya.",
				jenis, waktuGagal.In(s.cfg.Zona).Format("02 Jan 2006 15:04 WIB"))
			break
		}
	}
	return r, nil
}

// urutanJenis mengurutkan jenis yang muncul di peta: urutan pekerjaan harian
// (SemuaJenis) lebih dahulu, sisa jenis lain menyusul secara alfabetis.
func urutanJenis(peta map[string]time.Time) []string {
	out := make([]string, 0, len(peta))
	terpakai := map[string]bool{}
	for _, jenis := range SemuaJenis {
		if _, ada := peta[jenis]; ada {
			out = append(out, jenis)
			terpakai[jenis] = true
		}
	}
	sisa := make([]string, 0, len(peta))
	for jenis := range peta {
		if !terpakai[jenis] {
			sisa = append(sisa, jenis)
		}
	}
	sort.Strings(sisa)
	return append(out, sisa...)
}

// gabungPesan menggabungkan pesan dan sebab kegagalan.
func gabungPesan(pesan, sebab string) string {
	if pesan == "" {
		return sebab
	}
	if strings.Contains(pesan, sebab) {
		return pesan
	}
	return pesan + ": " + sebab
}

// urutkanKunci menyalin dan mengurutkan daftar kunci supaya hasil cadangan
// deterministik (penting untuk pembandingan di pengujian dan manifest).
func urutkanKunci(kunci []string) []string {
	out := append([]string(nil), kunci...)
	sort.Strings(out)
	return out
}
