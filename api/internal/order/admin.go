// Riwayat & detail pesanan untuk admin Pusat (kartu F1-15, PRD §4.2).
//
// Bagian ini sengaja dipisah dari order.go (jalur cabang) karena perbedaan
// yang mendasar:
//
//   - otorisasinya admin Pusat ber-kapabilitas `admin.orders` (Manager dan
//     Superadmin, PRD §2.1) — bukan akun cabang, dan TIDAK dibatasi branch_id
//     sesi karena admin memang melihat seluruh cabang;
//   - daftarnya SELALU dipaginasi di server dan ringkasan grup dihitung
//     agregat, sehingga riwayat besar tidak pernah diunduh seluruhnya ke
//     browser (AP-ORD-03);
//   - catatan internal (`note_admin`) HANYA hidup di jalur ini: struct
//     customer (Order) tidak punya fieldnya, jadi catatan internal tidak
//     mungkin bocor ke portal cabang.
//
// Dua pembacaan lintas-domain yang dipakai di sini sengaja lewat interface
// kecil, bukan dengan menembus tabel domain lain:
//
//   - email gagal diserahkan domain notification lewat SumberEmailGagal;
//   - log verifikasi & dokumen dibaca repository pesanan apa adanya untuk
//     ditampilkan (read-only) karena domain pemiliknya (verifikasi F1-18 dan
//     dokumen F1-18/F2-25) belum ada; penulisannya tidak pernah dari sini.
package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
)

// ErrAdminFilter dikembalikan bila parameter daftar admin tidak valid
// (status/grup/tanggal/paginasi). Dibedakan dari ErrInvalidData supaya UI bisa
// menunjuk ke filter, bukan ke isi pesanan.
var ErrAdminFilter = errors.New("filter daftar pesanan tidak valid")

// Mode grouping daftar riwayat (PRD AP-ORD-02). GrupTanpa berarti daftar rata.
const (
	GrupTanpa  = ""
	GrupCabang = "cabang"
	GrupHari   = "hari"
	GrupBulan  = "bulan"
	GrupTahun  = "tahun"
)

// Parameter paginasi server (AP-ORD-03).
const (
	DefaultPerHalaman = 25
	MaxPerHalaman     = 100
	// MaxCatatanInternal membatasi panjang catatan internal admin.
	MaxCatatanInternal = 2000
)

// TeksEventNoteAdmin adalah timeline yang ditulis saat catatan internal
// disimpan; isi catatannya ikut supaya riwayat siapa-kapan-apa tetap terbaca
// walau kolom `note_admin` hanya menyimpan nilai terakhir.
const (
	IconEventNoteAdmin    = "note-pencil"
	TeksEventNoteAdmin    = "Note admin diperbarui"
	TeksEventNoteAdminIsi = "Catatan internal"
)

// AdminFilter adalah parameter daftar riwayat pemesanan dari klien (query
// string). Semua nilai sudah berupa teks mentah: normalisasi & validasi ada di
// service supaya aturannya satu tempat.
type AdminFilter struct {
	Cari        string
	BranchID    string
	Status      string
	VerifStatus string
	IsCNC       *bool
	Terlambat   *bool
	EmailGagal  *bool
	Dari        string
	Sampai      string
	Grup        string
	Halaman     int
	PerHalaman  int
}

// AdminQuery adalah filter yang sudah dinormalkan dan siap dieksekusi
// repository.
type AdminQuery struct {
	Cari          string
	BranchID      string
	Status        string
	VerifStatus   string
	IsCNC         *bool
	Terlambat     *bool
	EmailGagal    *bool
	Dari          *time.Time
	Sampai        *time.Time
	Grup          string
	Limit         int
	Offset        int
	EmailGagalIDs []string
}

// AdminRow adalah satu baris daftar riwayat.
type AdminRow struct {
	ID           string `json:"id"`
	Nomor        string `json:"nomor,omitempty"`
	Versi        int    `json:"versi"`
	BranchID     string `json:"branch_id"`
	BranchNama   string `json:"branch_nama"`
	Tanggal      string `json:"tanggal,omitempty"`
	JatuhTempo   string `json:"jatuh_tempo,omitempty"`
	Status       string `json:"status"`
	VerifStatus  string `json:"verif_status"`
	IsCNC        bool   `json:"is_cnc"`
	IsSertifikat bool   `json:"is_sertifikat"`
	Total        int64  `json:"total"`
	JumlahItem   int64  `json:"jumlah_item"`
	// Terlambat: C&C yang belum Done dan sudah melewati jatuh tempo (WIB).
	Terlambat bool `json:"terlambat"`
	// EmailGagal: pesanan punya email berstatus `failed` di outbox.
	EmailGagal  bool   `json:"email_gagal"`
	SubmittedAt string `json:"submitted_at,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// AdminGrup adalah ringkasan satu grup (AP-ORD-02): berapa pesanan dan berapa
// nilainya — dihitung agregat di database untuk SELURUH baris yang lolos
// filter, bukan hanya halaman yang sedang dilihat.
type AdminGrup struct {
	Kunci  string `json:"kunci"`
	Label  string `json:"label"`
	Jumlah int64  `json:"jumlah"`
	Total  int64  `json:"total"`
}

// AdminPage adalah satu halaman daftar riwayat beserta jumlah total dan
// ringkasan grupnya.
type AdminPage struct {
	Items      []AdminRow  `json:"items"`
	Total      int64       `json:"total"`
	Halaman    int         `json:"halaman"`
	PerHalaman int         `json:"per_halaman"`
	Grup       string      `json:"grup,omitempty"`
	Ringkasan  []AdminGrup `json:"ringkasan,omitempty"`
}

// Event adalah satu entri timeline pesanan.
type Event struct {
	At        string `json:"at"`
	Icon      string `json:"icon,omitempty"`
	Teks      string `json:"teks"`
	ActorKind string `json:"actor_kind"`
	ActorID   string `json:"actor_id,omitempty"`
}

// Dokumen adalah satu dokumen pesanan (invoice/surat jalan). Kunci storage
// tidak pernah disertakan.
type Dokumen struct {
	ID         string `json:"id"`
	Jenis      string `json:"jenis"`
	Versi      int    `json:"versi"`
	UkuranByte int64  `json:"ukuran_byte"`
	CreatedAt  string `json:"created_at"`
}

// VerifikasiLog adalah satu baris log verifikasi pembayaran.
type VerifikasiLog struct {
	ID                     string          `json:"id"`
	Metode                 string          `json:"metode"`
	TotalPesanan           int64           `json:"total_pesanan"`
	TotalTerbaca           int64           `json:"total_terbaca"`
	Selisih                int64           `json:"selisih"`
	Hasil                  string          `json:"hasil"`
	Alasan                 string          `json:"alasan,omitempty"`
	Catatan                string          `json:"catatan,omitempty"`
	AdminNama              string          `json:"admin_nama,omitempty"`
	BranchAllowlistVersion int64           `json:"branch_allowlist_version"`
	DestinationResults     json.RawMessage `json:"destination_results"`
	CreatedAt              string          `json:"created_at"`
}

// AdminDetail adalah detail lengkap satu pesanan untuk admin (AP-ORD-04).
// Bukti transfer dan riwayat email TIDAK diduplikasi di sini: keduanya sudah
// punya endpoint sendiri (F1-13/F1-16) dan dipanggil terpisah oleh UI, sehingga
// satu respons tetap kecil.
type AdminDetail struct {
	ID           string `json:"id"`
	Nomor        string `json:"nomor,omitempty"`
	Versi        int    `json:"versi"`
	BranchID     string `json:"branch_id"`
	BranchNama   string `json:"branch_nama"`
	BranchKepala string `json:"branch_kepala"`
	BranchEmail  string `json:"branch_email"`
	BranchCNC    bool   `json:"branch_cnc"`
	Status       string `json:"status"`
	VerifStatus  string `json:"verif_status"`
	AlasanReview string `json:"alasan_review,omitempty"`
	IsCNC        bool   `json:"is_cnc"`
	IsSertifikat bool   `json:"is_sertifikat"`
	Tanggal      string `json:"tanggal,omitempty"`
	JatuhTempo   string `json:"jatuh_tempo,omitempty"`
	SubmittedAt  string `json:"submitted_at,omitempty"`
	DoneAt       string `json:"done_at,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	Catatan      string `json:"catatan"`
	// NoteAdmin adalah catatan internal — hanya ada di respons admin.
	NoteAdmin  string          `json:"note_admin"`
	Total      int64           `json:"total"`
	JumlahItem int64           `json:"jumlah_item"`
	Terlambat  bool            `json:"terlambat"`
	EmailGagal bool            `json:"email_gagal"`
	Lines      []Line          `json:"lines"`
	Timeline   []Event         `json:"timeline"`
	Dokumen    []Dokumen       `json:"dokumen"`
	Verifikasi []VerifikasiLog `json:"verifikasi"`
}

// SumberEmailGagal menyajikan id pesanan yang punya email berstatus gagal
// (dimiliki domain notification). Dipakai untuk penanda & filter "email gagal"
// tanpa domain pesanan membaca tabel email_outbox langsung.
type SumberEmailGagal interface {
	PesananEmailGagal(ctx context.Context) ([]string, error)
}

// AdminRepository memuat riwayat & detail pesanan untuk admin. Tidak ada
// parameter branch_id sesi di sini: pembatasan cabang adalah pilihan filter,
// bukan isolasi (otorisasi ada di service).
type AdminRepository interface {
	// ListAdmin mengembalikan satu halaman beserta total dan ringkasan grup.
	ListAdmin(ctx context.Context, q AdminQuery) (AdminPage, error)
	// GetAdmin mengembalikan detail satu pesanan; ErrNotFound bila tidak ada.
	GetAdmin(ctx context.Context, id string, emailGagalIDs []string) (AdminDetail, error)
	// SetNoteAdmin menyimpan catatan internal dan mencatatnya di timeline
	// dalam SATU transaksi; ErrNotFound bila pesanannya tidak ada.
	SetNoteAdmin(ctx context.Context, id, note, actorID, actorKind, teksEvent string) error
}

// AdminService adalah logika bisnis riwayat & detail pesanan admin.
type AdminService struct {
	repo  AdminRepository
	gagal SumberEmailGagal
	clk   clock.Clock
}

// AdminOption mengubah perilaku AdminService saat dibuat.
type AdminOption func(*AdminService)

// WithEmailGagal memasang sumber id pesanan ber-email gagal. Tanpa sumber ini,
// penanda email gagal selalu false dan filter email_gagal=true tidak
// mengembalikan apa pun (fail closed, bukan seolah-olah semua pesanan normal).
func WithEmailGagal(s SumberEmailGagal) AdminOption {
	return func(a *AdminService) { a.gagal = s }
}

// WithAdminClock memasang sumber waktu (dipakai pengujian).
func WithAdminClock(c clock.Clock) AdminOption {
	return func(a *AdminService) {
		if c != nil {
			a.clk = c
		}
	}
}

// NewAdminService membuat service riwayat pesanan admin.
func NewAdminService(repo AdminRepository, opts ...AdminOption) *AdminService {
	s := &AdminService{repo: repo, clk: clock.System()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// List mengembalikan satu halaman riwayat pemesanan seluruh cabang
// (AP-ORD-01/02/03).
func (s *AdminService) List(ctx context.Context, actor auth.Actor, f AdminFilter) (AdminPage, error) {
	if err := adminActor(actor); err != nil {
		return AdminPage{}, err
	}
	q, err := s.query(f)
	if err != nil {
		return AdminPage{}, err
	}
	gagal, err := s.emailGagal(ctx)
	if err != nil {
		return AdminPage{}, err
	}
	q.EmailGagalIDs = gagal

	page, err := s.repo.ListAdmin(ctx, q)
	if err != nil {
		return AdminPage{}, err
	}
	if page.Items == nil {
		page.Items = []AdminRow{}
	}
	page.Grup = q.Grup
	page.Halaman = q.Offset/q.Limit + 1
	page.PerHalaman = q.Limit
	if q.Grup != "" {
		sortGrup(page.Ringkasan, q.Grup)
	}
	return page, nil
}

// Get mengembalikan detail lengkap satu pesanan mana pun (AP-ORD-04). Id yang
// bukan UUID dijawab ErrNotFound (404), bukan 500.
func (s *AdminService) Get(ctx context.Context, actor auth.Actor, id string) (AdminDetail, error) {
	if err := adminActor(actor); err != nil {
		return AdminDetail{}, err
	}
	id = strings.TrimSpace(id)
	if !uuidRe.MatchString(id) {
		return AdminDetail{}, ErrNotFound
	}
	gagal, err := s.emailGagal(ctx)
	if err != nil {
		return AdminDetail{}, err
	}
	d, err := s.repo.GetAdmin(ctx, id, gagal)
	if err != nil {
		return AdminDetail{}, err
	}
	fillDetailSlices(&d)
	return d, nil
}

// SetNote menyimpan catatan internal pesanan (AP-ORD-05) dan mengembalikan
// detail terbaru. Catatan internal tidak pernah muncul di endpoint cabang.
func (s *AdminService) SetNote(ctx context.Context, actor auth.Actor, id, note string) (AdminDetail, error) {
	if err := adminActor(actor); err != nil {
		return AdminDetail{}, err
	}
	id = strings.TrimSpace(id)
	if !uuidRe.MatchString(id) {
		return AdminDetail{}, ErrNotFound
	}
	note = strings.TrimSpace(note)
	switch {
	case note == "":
		return AdminDetail{}, fmt.Errorf("%w: catatan internal tidak boleh kosong", ErrInvalidData)
	case len([]rune(note)) > MaxCatatanInternal:
		return AdminDetail{}, fmt.Errorf("%w: catatan internal maksimal %d karakter", ErrInvalidData, MaxCatatanInternal)
	}
	teks := TeksEventNoteAdmin + " oleh " + strings.TrimSpace(actor.Nama) + " — " + TeksEventNoteAdminIsi + ": " + note
	if err := s.repo.SetNoteAdmin(ctx, id, note, actor.UserID, string(actor.Kind), teks); err != nil {
		return AdminDetail{}, err
	}
	return s.Get(ctx, actor, id)
}

// adminActor menegakkan PRD §2.1: hanya admin Pusat ber-kapabilitas
// `admin.orders` (Manager maupun Superadmin) yang boleh membaca riwayat;
// akun cabang dijawab 403.
func adminActor(actor auth.Actor) error {
	if !actor.IsAdmin() || !actor.Can(auth.CapAdminOrders) {
		return ErrForbidden
	}
	return nil
}

// query menormalkan AdminFilter menjadi AdminQuery atau mengembalikan
// ErrAdminFilter yang bisa ditampilkan UI.
func (s *AdminService) query(f AdminFilter) (AdminQuery, error) {
	q := AdminQuery{
		Cari:       strings.TrimSpace(f.Cari),
		BranchID:   strings.TrimSpace(f.BranchID),
		IsCNC:      f.IsCNC,
		Terlambat:  f.Terlambat,
		EmailGagal: f.EmailGagal,
		Grup:       strings.ToLower(strings.TrimSpace(f.Grup)),
	}
	if q.BranchID != "" && !uuidRe.MatchString(q.BranchID) {
		return AdminQuery{}, fmt.Errorf("%w: cabang tidak valid", ErrAdminFilter)
	}
	status, err := normStatus(f.Status)
	if err != nil {
		return AdminQuery{}, fmt.Errorf("%w: status %q tidak dikenal", ErrAdminFilter, f.Status)
	}
	q.Status = status

	verif := strings.ToLower(strings.TrimSpace(f.VerifStatus))
	switch verif {
	case "", "semua", "belum", "cocok", "perlu_review":
		if verif == "semua" {
			verif = ""
		}
	default:
		return AdminQuery{}, fmt.Errorf("%w: status verifikasi %q tidak dikenal", ErrAdminFilter, f.VerifStatus)
	}
	q.VerifStatus = verif

	switch q.Grup {
	case "", "tanpa", GrupCabang, GrupHari, GrupBulan, GrupTahun:
		if q.Grup == "tanpa" {
			q.Grup = GrupTanpa
		}
	default:
		return AdminQuery{}, fmt.Errorf("%w: grup %q tidak dikenal", ErrAdminFilter, f.Grup)
	}

	if q.Dari, err = s.tanggal(f.Dari, "dari"); err != nil {
		return AdminQuery{}, err
	}
	if q.Sampai, err = s.tanggal(f.Sampai, "sampai"); err != nil {
		return AdminQuery{}, err
	}
	if q.Dari != nil && q.Sampai != nil && q.Dari.After(*q.Sampai) {
		return AdminQuery{}, fmt.Errorf("%w: tanggal awal melewati tanggal akhir", ErrAdminFilter)
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
	q.Limit = per
	q.Offset = (halaman - 1) * per
	return q, nil
}

// tanggal mengubah YYYY-MM-DD menjadi tanggal bisnis WIB; kosong berarti tanpa
// batas.
func (s *AdminService) tanggal(v, nama string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation(TanggalLayout, v, clock.Jakarta())
	if err != nil {
		return nil, fmt.Errorf("%w: tanggal %s harus berformat YYYY-MM-DD", ErrAdminFilter, nama)
	}
	return &t, nil
}

// emailGagal mengambil id pesanan ber-email gagal dari domain notification.
// Tanpa sumber terpasang hasilnya kosong (fail closed).
func (s *AdminService) emailGagal(ctx context.Context) ([]string, error) {
	if s.gagal == nil {
		return []string{}, nil
	}
	ids, err := s.gagal.PesananEmailGagal(ctx)
	if err != nil {
		return nil, fmt.Errorf("baca email gagal: %w", err)
	}
	if ids == nil {
		return []string{}, nil
	}
	return ids, nil
}

// sortGrup mengurutkan ringkasan secara stabil: nama cabang A→Z untuk grouping
// per cabang, dan periode terbaru lebih dahulu untuk grouping tanggal. Urutan
// tetap sama di setiap halaman sehingga tabel tidak "melompat".
func sortGrup(g []AdminGrup, grup string) {
	sort.SliceStable(g, func(i, j int) bool {
		if grup == GrupCabang {
			return g[i].Label < g[j].Label
		}
		return g[i].Kunci > g[j].Kunci
	})
}

// fillDetailSlices memastikan koleksi detail selalu berupa array JSON (bukan
// null) supaya UI tidak perlu menangani dua bentuk.
func fillDetailSlices(d *AdminDetail) {
	if d.Lines == nil {
		d.Lines = []Line{}
	}
	if d.Timeline == nil {
		d.Timeline = []Event{}
	}
	if d.Dokumen == nil {
		d.Dokumen = []Dokumen{}
	}
	if d.Verifikasi == nil {
		d.Verifikasi = []VerifikasiLog{}
	}
}
