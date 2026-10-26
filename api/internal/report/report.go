// Package report menyediakan laporan order line untuk Superadmin beserta
// ekspor XLSX asinkron (kartu F2-24; PRD §4.7 AP-REP-01..05, §9 no.10,
// §10; SystemDesign.md §6, §8–§9; Database.md §7.3).
//
// Keputusan desain yang ditegakkan di paket ini:
//
//   - SATU BARIS LAPORAN = SATU order line (AP-REP-01), termasuk baris
//     packaging bila ada. Pesanan `draft` dikecualikan: laporan hanya berisi
//     pesanan yang sudah dikirim (punya nomor/tanggal).
//   - Daftar (paginasi) dan ekspor memakai DEFINISI FILTER YANG SAMA, sehingga
//     "jumlah baris ekspor = jumlah baris query terfilter" (PRD §9 no.10) bukan
//     kebetulan, melainkan konsekuensi satu query yang dipakai dua jalur.
//   - PI adalah atribut cabang (`b.pajak_in`): hanya
//     tampil sebagai kolom dan bisa difilter. Keduanya TIDAK pernah mengubah
//     harga, subtotal, atau pajak.
//   - Ekspor berjalan ASINKRON lewat antrean pekerjaan (River). Permintaan
//     hanya membuat baris pekerjaan + job River dalam SATU transaksi, sehingga
//     tidak mungkin ada job tanpa barisnya (SystemDesign.md §4.2, §5.1).
//   - Hanya Superadmin (CapAdminReports) yang boleh melihat daftar, meminta
//     ekspor, membaca status, dan mengunduh hasilnya (PRD §2.1). Manager
//     ditolak di LAYER SERVICE, bukan hanya disembunyikan di UI.
//   - Hasil ekspor disimpan di object storage dan hanya bisa diunduh lewat
//     presigned URL berumur pendek pada jalur reporting; laporan SENGAJA tidak
//     dicatat di tabel `documents` supaya tidak bisa diunduh lewat endpoint
//     dokumen yang boleh dipakai Manager.
package report

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/storage"
)

// Jenis laporan yang tersedia. `payments` dipakai kartu lanjutan F3-32 dan
// sengaja disediakan di kontrak repository sejak sekarang.
const (
	JenisOrderLines = "order_lines"
)

// Status pekerjaan ekspor (enum report_job_status, migrasi 0005).
const (
	StatusQueued  = "queued"
	StatusProses  = "proses"
	StatusSelesai = "selesai"
	StatusGagal   = "gagal"
)

// Parameter paginasi daftar (AP-REP-03 memakai tabel panjang → paginasi server).
const (
	DefaultPerHalaman = 25
	MaxPerHalaman     = 500
	// BatchEkspor adalah jumlah baris per pengambilan saat ekspor. Nilainya
	// kecil dengan sengaja: baris ditulis ke XLSX streaming lalu dilepas,
	// sehingga ekspor 50.000 baris tidak pernah menahan seluruh data di memori
	// (SystemDesign.md §8, Database.md §7.3).
	BatchEkspor = 500
)

// LayoutTanggal adalah bentuk tanggal bisnis (WIB) pada API.
const LayoutTanggal = "2006-01-02"

// MimeXLSX adalah MIME type berkas ekspor.
const MimeXLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// DirStorage adalah awalan kunci object storage hasil ekspor.
const DirStorage = "reports"

// DefaultPresignTTL adalah umur tautan unduh hasil ekspor. Sengaja pendek:
// tautan hanya seumur satu sesi mengunduh (Architecture.md §6).
const DefaultPresignTTL = 5 * time.Minute

// Sentinel error domain. Pemetaan ke HTTP ada di lapisan transport.
var (
	// ErrForbidden dikembalikan bila aktor bukan Superadmin.
	ErrForbidden = errors.New("hanya Superadmin yang boleh mengakses laporan")
	// ErrNotFound dikembalikan bila pekerjaan ekspor tidak ada (atau id bukan
	// UUID — dijawab sama supaya bukan 500).
	ErrNotFound = errors.New("pekerjaan ekspor tidak ditemukan")
	// ErrFilterInvalid dikembalikan bila parameter filter laporan tidak valid.
	ErrFilterInvalid = errors.New("filter laporan tidak valid")
	// ErrStorageUnavailable dikembalikan bila object storage belum/tidak siap
	// sehingga hasil ekspor tidak mungkin disimpan dengan aman.
	ErrStorageUnavailable = errors.New("penyimpanan laporan belum tersedia")
	// ErrQueueUnavailable dikembalikan bila penjadwal pekerjaan belum siap
	// sehingga ekspor tidak bisa dijalankan asinkron.
	ErrQueueUnavailable = errors.New("penjadwal pekerjaan ekspor belum siap")
	// ErrJobNotReady dikembalikan bila hasil ekspor diminta sebelum selesai.
	ErrJobNotReady = errors.New("ekspor belum selesai")
	// ErrDataInvalid dikembalikan bila data laporan tidak konsisten (mis.
	// pesanan non-draft tanpa tanggal) sehingga ekspor tidak dapat dilanjutkan.
	ErrDataInvalid = errors.New("data laporan tidak konsisten")
)

// Filter adalah filter laporan dari klien (query string atau body JSON).
// Tanggal berbentuk YYYY-MM-DD; Cabang menerima banyak id.
type Filter struct {
	Dari       string   `json:"dari,omitempty"`
	Sampai     string   `json:"sampai,omitempty"`
	Cabang     []string `json:"cabang,omitempty"`
	Status     string   `json:"status,omitempty"`
	Jenis      string   `json:"jenis,omitempty"`
	Cnc        *bool    `json:"cnc,omitempty"`
		Pi         *bool    `json:"pi,omitempty"`
	Halaman    int      `json:"halaman,omitempty"`
	PerHalaman int      `json:"per_halaman,omitempty"`
}

// Query adalah filter yang sudah dinormalkan dan siap dieksekusi repository.
type Query struct {
	Dari           *time.Time
	Sampai         *time.Time
	Cabang         []string
	Status         string
	Jenis          string
	IsCNC          *bool
		PajakIn        *bool
	Batch          int
	Offset         int
	SetelahTanggal *time.Time
	SetelahNomor   string
	SetelahLineNo  int
}

// Line adalah satu baris laporan (satu order line).
type Line struct {
	Tanggal           string `json:"tanggal"`
	Nomor             string `json:"nomor"`
	Versi             int    `json:"versi"`
	BranchID          string `json:"branch_id"`
	Cabang            string `json:"cabang"`
	KepalaCabang      string `json:"kepala_cabang"`
	IsCNC             bool   `json:"is_cnc"`
		PajakIn           bool   `json:"pajak_in"`
	LineNo            int    `json:"line_no"`
	Produk            string `json:"produk"`
	Jenis             string `json:"jenis"`
	SerialNumber      string `json:"serial_number"`
	Qty               int    `json:"qty"`
	HargaSatuan       int64  `json:"harga_satuan"`
	Subtotal          int64  `json:"subtotal"`
	Status            string `json:"status"`
	TanggalVerifikasi string `json:"tanggal_verifikasi,omitempty"`
	MetodeVerifikasi  string `json:"metode_verifikasi,omitempty"`
	// TanggalWaktu hanya hidup di dalam proses: dipakai untuk kursor ekspor
	// dan pengelompokan rekap bulanan. Tidak pernah dikirim ke klien.
	TanggalWaktu time.Time `json:"-"`
}

// Page adalah satu halaman laporan beserta totalnya.
type Page struct {
	Items         []Line `json:"items"`
	Total         int64  `json:"total"`
	TotalSubtotal int64  `json:"total_subtotal"`
	Halaman       int    `json:"halaman"`
	PerHalaman    int    `json:"per_halaman"`
	// Filter adalah filter yang benar-benar dipakai (bentuk normal), supaya UI
	// bisa menampilkan/membagikan filter yang sama dengan yang diekspor.
	Filter Filter `json:"filter"`
}

// Job adalah satu pekerjaan ekspor XLSX.
type Job struct {
	ID          string     `json:"id"`
	Jenis       string     `json:"jenis"`
	Status      string     `json:"status"`
	DimintaNama string     `json:"diminta_nama"`
	Filter      Filter     `json:"filter"`
	NamaFile    string     `json:"nama_file,omitempty"`
	JumlahBaris int64      `json:"jumlah_baris"`
	Pesan       string     `json:"pesan,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	// StorageKey hanya hidup di dalam proses (repository <-> service).
	StorageKey string `json:"-"`
}

// SiapDiunduh melaporkan apakah hasil ekspor sudah bisa diunduh.
func (j Job) SiapDiunduh() bool { return j.Status == StatusSelesai && j.StorageKey != "" }

// Download adalah hasil permintaan tautan unduh hasil ekspor.
type Download struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
	NamaFile  string `json:"nama_file"`
	MimeType  string `json:"mime_type"`
}

// JobRequest adalah permintaan pembuatan pekerjaan ekspor.
type JobRequest struct {
	Jenis       string
	DimintaOleh string
	DimintaNama string
	Filter      Filter
}

// Repository memuat baris laporan dan menyimpan pekerjaan ekspor.
type Repository interface {
	// ListLines mengembalikan satu batch baris laporan sesuai Query (memakai
	// kursor bila SetelahTanggal diisi, atau Offset untuk paginasi daftar).
	ListLines(ctx context.Context, q Query) ([]Line, error)
	// CountLines mengembalikan jumlah baris dan total subtotal filter yang SAMA.
	CountLines(ctx context.Context, q Query) (jumlahBaris, totalSubtotal int64, err error)
	// InsertJob membuat baris pekerjaan ekspor DAN menitipkan job River-nya
	// dalam satu transaksi.
	InsertJob(ctx context.Context, req JobRequest) (Job, error)
	// GetJob mengembalikan satu pekerjaan (ErrNotFound bila tidak ada).
	GetJob(ctx context.Context, id string) (Job, error)
	MarkJobProses(ctx context.Context, id string) error
	MarkJobSelesai(ctx context.Context, id, storageKey, namaFile string, jumlahBaris int64) error
	MarkJobGagal(ctx context.Context, id, pesan string) error
}

// Service adalah logika bisnis laporan.
type Service struct {
	repo    Repository
	store   storage.Store
	ttl     time.Duration
	batch   int
	now     func() time.Time
	tempDir string
}

// Option mengubah perilaku service.
type Option func(*Service)

// WithPresignTTL mengubah umur tautan unduh hasil ekspor.
func WithPresignTTL(d time.Duration) Option { return func(s *Service) { s.ttl = d } }

// WithBatchSize mengubah jumlah baris per batch ekspor (≥1).
func WithBatchSize(n int) Option {
	return func(s *Service) {
		if n > 0 {
			s.batch = n
		}
	}
}

// WithClock mengganti sumber waktu (dipakai penamaan berkas hasil).
func WithClock(c clock.Clock) Option {
	return func(s *Service) {
		if c != nil {
			s.now = c.Now
		}
	}
}

// WithTempDir mengarahkan berkas XLSX sementara ke direktori tertentu
// (dipakai pengujian agar tidak menulis ke direktori sistem).
func WithTempDir(dir string) Option { return func(s *Service) { s.tempDir = dir } }

// NewService membuat service laporan. repo dan store tidak boleh nil.
func NewService(repo Repository, store storage.Store, opts ...Option) *Service {
	s := &Service{
		repo:  repo,
		store: store,
		ttl:   DefaultPresignTTL,
		batch: BatchEkspor,
		now:   func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// List mengembalikan satu halaman laporan untuk Superadmin.
func (s *Service) List(ctx context.Context, actor auth.Actor, f Filter) (Page, error) {
	if err := bolehSA(actor); err != nil {
		return Page{}, err
	}
	norm, q, err := s.query(f)
	if err != nil {
		return Page{}, err
	}
	items, err := s.repo.ListLines(ctx, q)
	if err != nil {
		return Page{}, err
	}
	total, subtotal, err := s.repo.CountLines(ctx, q)
	if err != nil {
		return Page{}, err
	}
	if items == nil {
		items = []Line{}
	}
	return Page{
		Items:         items,
		Total:         total,
		TotalSubtotal: subtotal,
		Halaman:       q.Offset/q.Batch + 1,
		PerHalaman:    q.Batch,
		Filter:        norm,
	}, nil
}

// Request membuat pekerjaan ekspor XLSX asinkron dan mengembalikan job-nya
// beserta filter yang benar-benar dipakai (AP-REP-04: "diminta Superadmin").
func (s *Service) Request(ctx context.Context, actor auth.Actor, f Filter) (Job, error) {
	if err := bolehSA(actor); err != nil {
		return Job{}, err
	}
	norm, _, err := s.query(f)
	if err != nil {
		return Job{}, err
	}
	if err := s.storageSiap(ctx); err != nil {
		return Job{}, err
	}
	job, err := s.repo.InsertJob(ctx, JobRequest{
		Jenis:       JenisOrderLines,
		DimintaOleh: actor.UserID,
		DimintaNama: strings.TrimSpace(actor.Nama),
		Filter:      norm,
	})
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

// Job mengembalikan status satu pekerjaan ekspor (Superadmin saja).
func (s *Service) Job(ctx context.Context, actor auth.Actor, id string) (Job, error) {
	if err := bolehSA(actor); err != nil {
		return Job{}, err
	}
	jobID := strings.TrimSpace(id)
	if !uuidRe.MatchString(jobID) {
		return Job{}, ErrNotFound
	}
	return s.repo.GetJob(ctx, jobID)
}

// Download membuat tautan unduh berumur pendek untuk hasil ekspor.
func (s *Service) Download(ctx context.Context, actor auth.Actor, id string) (Download, error) {
	if err := bolehSA(actor); err != nil {
		return Download{}, err
	}
	jobID := strings.TrimSpace(id)
	if !uuidRe.MatchString(jobID) {
		return Download{}, ErrNotFound
	}
	job, err := s.repo.GetJob(ctx, jobID)
	if err != nil {
		return Download{}, err
	}
	if !job.SiapDiunduh() {
		return Download{}, fmt.Errorf("%w: pekerjaan berstatus %s", ErrJobNotReady, job.Status)
	}
	url, err := s.presign(ctx, job.StorageKey, job.NamaFile)
	if err != nil {
		return Download{}, err
	}
	return Download{
		URL:       url,
		ExpiresIn: int(s.ttl.Seconds()),
		NamaFile:  job.NamaFile,
		MimeType:  MimeXLSX,
	}, nil
}

// Jalankan mengerjakan satu pekerjaan ekspor: membaca baris laporan secara
// BERTAHAP, menulisnya streaming ke XLSX, menyimpan berkas ke object storage,
// lalu menandai pekerjaan selesai. Dipanggil worker (bukan handler HTTP).
//
// Idempoten terhadap pengulangan River (at-least-once): pekerjaan yang sudah
// selesai dilewati tanpa efek samping, dan kunci storage deterministik per
// pekerjaan sehingga percobaan ulang menimpa berkas yang sama — tidak pernah
// menggandakan hasil.
func (s *Service) Jalankan(ctx context.Context, jobID string) (Job, error) {
	id := strings.TrimSpace(jobID)
	if !uuidRe.MatchString(id) {
		return Job{}, ErrNotFound
	}
	job, err := s.repo.GetJob(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if job.Status == StatusSelesai {
		return job, nil
	}
	if s.store == nil {
		return Job{}, ErrStorageUnavailable
	}
	if err := s.repo.MarkJobProses(ctx, id); err != nil {
		return Job{}, err
	}
	q, err := queryDariFilter(job.Filter, s.batch)
	if err != nil {
		s.gagal(ctx, id, err)
		return Job{}, err
	}

	berkas, err := s.tulisXLSX(ctx, q)
	if err != nil {
		s.gagal(ctx, id, err)
		return Job{}, err
	}
	defer func() { _ = os.Remove(berkas.path) }()

	if err := s.simpan(ctx, id, berkas, namaBerkas(job, s.now())); err != nil {
		s.gagal(ctx, id, err)
		return Job{}, err
	}
	return s.repo.GetJob(ctx, id)
}

// simpan menaruh berkas hasil di object storage lalu menandai pekerjaan selesai.
func (s *Service) simpan(ctx context.Context, id string, berkas hasilXLSX, namaFile string) error {
	key := StorageKey(JenisOrderLines, id)
	f, err := os.Open(berkas.path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := s.store.Put(ctx, key, f, berkas.size, MimeXLSX); err != nil {
		return fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
	}
	if err := s.repo.MarkJobSelesai(ctx, id, key, namaFile, berkas.baris); err != nil {
		return err
	}
	return nil
}

// presign membuat tautan unduh; bila storage mendukung paksaan nama berkas
// (Content-Disposition attachment), nama berkas yang benar ikut dibawa.
func (s *Service) presign(ctx context.Context, key, nama string) (string, error) {
	if ds, ok := s.store.(interface {
		PresignDownload(context.Context, string, string, time.Duration) (string, error)
	}); ok {
		url, err := ds.PresignDownload(ctx, key, nama, s.ttl)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
		}
		return url, nil
	}
	url, err := s.store.PresignGet(ctx, key, s.ttl)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
	}
	return url, nil
}

// gagal menandai pekerjaan gagal; kegagalannya sendiri tidak menghentikan
// pelaporan error aslinya ke pemanggil (worker tetap mengembalikan error supaya
// River mencoba ulang).
func (s *Service) gagal(ctx context.Context, id string, sebab error) {
	pesan := sebab.Error()
	if r := []rune(pesan); len(r) > 500 {
		pesan = string(r[:500])
	}
	if err := s.repo.MarkJobGagal(ctx, id, pesan); err != nil {
		// Tidak ada yang bisa dilakukan lagi di sini: error aslinya tetap
		// dikembalikan ke worker dan tercatat di log River.
		slog.ErrorContext(ctx, "gagal menandai pekerjaan ekspor gagal",
			"job_id", id, "err", err)
	}
}

// storageSiap memeriksa object storage SEBELUM pekerjaan dibuat. Tanpa
// pemeriksaan ini, permintaan ekspor akan "berhasil" di mata Superadmin tetapi
// pekerjaannya hanya bisa gagal (storage belum dikonfigurasi).
func (s *Service) storageSiap(ctx context.Context) error {
	if s.store == nil {
		return ErrStorageUnavailable
	}
	type pemeriksa interface{ Health(context.Context) error }
	if h, ok := s.store.(pemeriksa); ok {
		if err := h.Health(ctx); err != nil {
			return fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
		}
	}
	return nil
}

// query menormalkan Filter menjadi Filter tersimpan + Query siap eksekusi.
//
// Nilai kembali pertama adalah bentuk NORMAL yang disimpan di pekerjaan ekspor,
// sehingga filter yang dipakai saat job berjalan selalu sama dengan yang
// diminta Superadmin (tidak pernah dibaca ulang dari klien saat unduh).
func (s *Service) query(f Filter) (Filter, Query, error) {
	norm := Filter{
		Cabang:  normCabang(f.Cabang),
		Status:  strings.ToLower(strings.TrimSpace(f.Status)),
		Jenis:   strings.ToLower(strings.TrimSpace(f.Jenis)),
		Cnc:     f.Cnc,
		Pi:      f.Pi,
	}
	if err := normCabangID(norm.Cabang); err != nil {
		return Filter{}, Query{}, err
	}
	switch norm.Status {
	case "", "semua":
		norm.Status = ""
	case "submitted", "approved_unpaid", "done":
	case "draft":
		return Filter{}, Query{}, fmt.Errorf("%w: pesanan draft tidak pernah masuk laporan", ErrFilterInvalid)
	default:
		return Filter{}, Query{}, fmt.Errorf("%w: status %q tidak dikenal (submitted|approved_unpaid|done)", ErrFilterInvalid, f.Status)
	}
	switch norm.Jenis {
	case "", "semua":
		norm.Jenis = ""
	case "product", "packaging":
	default:
		return Filter{}, Query{}, fmt.Errorf("%w: jenis %q tidak dikenal (product|packaging)", ErrFilterInvalid, f.Jenis)
	}

	dari, err := s.tanggal(f.Dari, "dari")
	if err != nil {
		return Filter{}, Query{}, err
	}
	sampai, err := s.tanggal(f.Sampai, "sampai")
	if err != nil {
		return Filter{}, Query{}, err
	}
	if dari != nil && sampai != nil && dari.After(*sampai) {
		return Filter{}, Query{}, fmt.Errorf("%w: tanggal awal melewati tanggal akhir", ErrFilterInvalid)
	}
	if dari != nil {
		norm.Dari = dari.Format(LayoutTanggal)
	}
	if sampai != nil {
		norm.Sampai = sampai.Format(LayoutTanggal)
	}

	per := f.PerHalaman
	if per <= 0 {
		per = DefaultPerHalaman
	}
	if per > MaxPerHalaman {
		per = MaxPerHalaman
	}
	halaman := f.Halaman
	if halaman <= 0 {
		halaman = 1
	}

	q := Query{
		Dari:      dari,
		Sampai:    sampai,
		Cabang:    norm.Cabang,
		Status:    norm.Status,
		Jenis:     norm.Jenis,
		IsCNC:     f.Cnc,
				PajakIn:   f.Pi,
		Batch:     per,
		Offset:    (halaman - 1) * per,
	}
	return norm, q, nil
}

// tanggal mengubah YYYY-MM-DD menjadi tanggal bisnis WIB; kosong berarti tanpa
// batas.
func (s *Service) tanggal(v, nama string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation(LayoutTanggal, v, clock.Jakarta())
	if err != nil {
		return nil, fmt.Errorf("%w: tanggal %s harus berformat YYYY-MM-DD", ErrFilterInvalid, nama)
	}
	return &t, nil
}

// queryDariFilter membangun Query ekspor dari FILTER TERSIMPAN pada pekerjaan
// (bukan dari permintaan klien saat unduh). Batch selalu BatchEkspor.
func queryDariFilter(f Filter, batch int) (Query, error) {
	if batch <= 0 {
		batch = BatchEkspor
	}
	loc := clock.Jakarta()
	parse := func(v string) (*time.Time, error) {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, nil
		}
		t, err := time.ParseInLocation(LayoutTanggal, v, loc)
		if err != nil {
			return nil, fmt.Errorf("%w: tanggal %q tersimpan tidak valid", ErrDataInvalid, v)
		}
		return &t, nil
	}
	dari, err := parse(f.Dari)
	if err != nil {
		return Query{}, err
	}
	sampai, err := parse(f.Sampai)
	if err != nil {
		return Query{}, err
	}
	return Query{
		Dari:      dari,
		Sampai:    sampai,
		Cabang:    f.Cabang,
		Status:    f.Status,
		Jenis:     f.Jenis,
		IsCNC:     f.Cnc,
				PajakIn:   f.Pi,
		Batch:     batch,
	}, nil
}

// normCabang membersihkan daftar cabang: buang yang kosong, rapikan, buang
// duplikat, lalu urutkan supaya filter tersimpan deterministik.
func normCabang(in []string) []string {
	out := make([]string, 0, len(in))
	lihat := map[string]bool{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || lihat[v] {
			continue
		}
		lihat[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// normCabangID memastikan setiap id cabang berbentuk UUID.
func normCabangID(ids []string) error {
	for _, id := range ids {
		if !uuidRe.MatchString(id) {
			return fmt.Errorf("%w: cabang %q bukan id yang sah", ErrFilterInvalid, id)
		}
	}
	return nil
}

// bolehSA menegakkan PRD §2.1: hanya Superadmin (CapAdminReports) yang boleh
// menyentuh laporan/ekspor. Manager ditolak FORBIDDEN.
func bolehSA(actor auth.Actor) error {
	if !actor.IsAdmin() || !actor.Can(auth.CapAdminReports) {
		return ErrForbidden
	}
	return nil
}

// StorageKey menyusun kunci object storage hasil ekspor:
// `reports/{jenis}/{job_id}.xlsx`.
func StorageKey(jenis, jobID string) string {
	return DirStorage + "/" + jenis + "/" + jobID + ".xlsx"
}

// namaBerkas menyusun nama berkas unduhan hasil ekspor.
func namaBerkas(job Job, now time.Time) string {
	bagian := []string{"Laporan-Order-Line"}
	if job.Filter.Dari != "" {
		bagian = append(bagian, "dari-"+job.Filter.Dari)
	}
	if job.Filter.Sampai != "" {
		bagian = append(bagian, "sampai-"+job.Filter.Sampai)
	}
	bagian = append(bagian, now.In(clock.Jakarta()).Format("20060102"))
	return strings.Join(bagian, "-") + ".xlsx"
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
