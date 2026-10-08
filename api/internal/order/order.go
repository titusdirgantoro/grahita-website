// Package order mengelola pesanan cabang: draft, editor order line, daftar dan
// detail pesanan (kartu F1-11).
//
// Aturan yang ditegakkan di sini — bukan hanya disembunyikan di UI:
//
//   - Setiap pembacaan/penulisan dibatasi branch_id dari SESI (Actor.BranchID),
//     tidak pernah dari parameter klien. Pesanan milik cabang lain berakhir 404
//     tanpa membocorkan keberadaannya (isolasi data cabang, Architecture.md §6).
//   - Harga order line SELALU diambil dari branch_prices cabang sesi (lewat
//     domain katalog). Produk tanpa baris harga cabang DITOLAK; harga_default
//     bukan cadangan (PRD CP-KAT-02, keputusan QA F1-08 untuk F1-11).
//   - Total dihitung server: service menjumlahkan qty × harga cabang, dan
//     trigger order_lines_recalc di PostgreSQL menyinkronkan orders.total saat
//     baris ditulis. Angka harga/subtotal/total dari klien tidak pernah dibaca
//     (SystemDesign.md §3, BR-10).
//   - Hanya draft milik cabang pemiliknya yang boleh diubah/dihapus
//     (PRD CP-ORD-03, CP-ORD-09). Status `draft` diperiksa ulang di klausa
//     WHERE update/delete sehingga submit bersamaan (F1-14) tidak bisa
//     ditembus dari jalur ini — sesuai catatan kartu F1-02 bahwa penguncian
//     pesanan Submitted/Done adalah tanggung jawab layer service.
//   - Belum ada status Batal dan tidak ada pembatalan pesanan Submitted
//     (keputusan kartu F1-11; PRD §8 no.4 masih terbuka).
//   - Draft tidak punya nomor, tanggal, serial number, maupun bukti transfer;
//     semuanya lahir saat submit (F1-14). Aturan Sertifikat (satu baris biaya
//     packaging) dan Cash & Carry datang di F1-12; di sini campuran Sertifikat
//     dengan produk reguler sudah ditolak dengan penjelasan supaya invariant
//     Database.md §8 (order_lines_guard_kind) tidak pernah dilanggar.
package order

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/catalog"
)

// Sentinel error domain. Pemetaan ke kode HTTP ada di lapisan transport.
var (
	// ErrForbidden dikembalikan bila aktor bukan akun cabang yang berhak.
	ErrForbidden = errors.New("tidak berhak mengakses pesanan")
	// ErrNotFound dikembalikan bila pesanan tidak ada ATAU bukan milik cabang
	// pemanggil — keduanya dijawab sama supaya keberadaan pesanan cabang lain
	// tidak bocor.
	ErrNotFound = errors.New("pesanan tidak ditemukan")
	// ErrLocked dikembalikan bila pesanan sudah dikirim (Submitted/Done) atau
	// baru saja berpindah status, sehingga cabang tidak boleh mengubahnya.
	ErrLocked = errors.New("pesanan sudah dikirim sehingga tidak dapat diubah atau dihapus")
	// ErrInvalidData dikembalikan bila isi draft tidak lolos validasi.
	ErrInvalidData = errors.New("data pesanan tidak valid")
	// ErrEmptyLines dikembalikan bila draft dikirim tanpa satu pun baris.
	ErrEmptyLines = errors.New("pesanan harus punya minimal satu baris produk")
	// ErrProductNotSold dikembalikan bila produk yang dipilih tidak punya baris
	// harga untuk cabang sesi (atau tidak aktif): produk itu tidak boleh
	// dipesan, dan harga_default tidak dipakai sebagai cadangan.
	ErrProductNotSold = errors.New("produk tidak dijual ke cabang ini")
	// ErrNoBranch dikembalikan bila akun cabang tidak terhubung ke cabang.
	ErrNoBranch = errors.New("akun tidak terhubung ke cabang")
)

// Status pesanan (Database.md §3, enum order_status).
//
// Sengaja TIDAK ada StatusBatal: pembatalan pesanan Submitted masih pertanyaan
// terbuka PRD §8 no.4 dan keputusan kartu F1-11 adalah belum menyediakannya.
const (
	StatusDraft     = "draft"
	StatusSubmitted = "submitted"
	StatusDone      = "done"
)

// Jenis baris pesanan (Database.md §3, enum line_kind).
const (
	JenisProduct   = "product"
	JenisPackaging = "packaging"
)

// Batas satu permintaan editor supaya transaksinya tetap pendek dan
// perkalian qty × harga tidak meluap.
const (
	MaxLines = 100
	maxQty   = 1_000_000
)

// uuidRe dipakai untuk menolak id yang jelas bukan UUID dengan jawaban 404
// (bukan 500 dari PostgreSQL). Format yang diterima adalah bentuk standar yang
// selalu diterbitkan sistem (uuidv7).
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// TanggalLayout adalah bentuk tanggal bisnis (WIB) yang dipakai API.
const TanggalLayout = "2006-01-02"

// Line adalah satu baris pesanan.
//
// Jenis "packaging" belum pernah dibuat di sini (F1-12), tetapi tipenya sudah
// disiapkan supaya bentuk jawabannya tidak berubah saat F1-12 mendarat.
type Line struct {
	LineNo       int    `json:"line_no"`
	Jenis        string `json:"jenis"`
	ProductID    string `json:"product_id,omitempty"`
	Nama         string `json:"nama"`
	Satuan       string `json:"satuan"`
	Qty          int    `json:"qty"`
	HargaSatuan  int64  `json:"harga_satuan"`
	Subtotal     int64  `json:"subtotal"`
	SerialNumber string `json:"serial_number,omitempty"`
}

// Order adalah pesanan satu cabang. Lines hanya diisi pada detail
// (daftar pesanan tidak memuat barisnya).
//
// BranchID dan CreatedBy tidak pernah dikirim ke klien: keduanya dipakai
// internal untuk menegakkan isolasi cabang dan jejak pembuat draft.
type Order struct {
	ID           string `json:"id"`
	BranchID     string `json:"-"`
	CreatedBy    string `json:"-"`
	Nomor        string `json:"nomor,omitempty"`
	Status       string `json:"status"`
	VerifStatus  string `json:"verif_status"`
	Versi        int    `json:"versi"`
	IsCNC        bool   `json:"is_cnc"`
	IsSertifikat bool   `json:"is_sertifikat"`
	// Tanggal adalah tanggal submit (WIB) dalam bentuk YYYY-MM-DD; kosong
	// selama masih draft (BR-04).
	Tanggal     string    `json:"tanggal,omitempty"`
	JatuhTempo  string    `json:"jatuh_tempo,omitempty"`
	Catatan     string    `json:"catatan"`
	Total       int64     `json:"total"`
	JumlahItem  int64     `json:"jumlah_item"`
	SubmittedAt string    `json:"submitted_at,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Lines       []Line    `json:"lines,omitempty"`
}

// LineInput adalah satu baris yang dikirim editor: HANYA product_id dan qty.
// Harga satuan, subtotal, dan total tidak pernah diterima dari klien.
type LineInput struct {
	ProductID string
	Qty       int
}

// DraftInput adalah isi draft yang dikirim klien.
type DraftInput struct {
	Catatan string
	Lines   []LineInput
}

// Filter adalah penyaring daftar pesanan. Status kosong berarti semua status.
type Filter struct {
	Status string
}

// Catalog adalah sudut pandang domain katalog yang dibutuhkan pesanan: produk
// yang benar-benar dijual ke satu cabang beserta harga khususnya. Produk tanpa
// baris branch_prices tidak pernah muncul di daftar ini (PRD CP-KAT-02).
type Catalog interface {
	BranchProducts(ctx context.Context, branchID string) ([]catalog.BranchProduct, error)
}

// Repository memuat dan menyimpan pesanan cabang. Setiap metode menerima
// branchID dari SESI dan wajib memakainya sebagai syarat query.
type Repository interface {
	// List mengembalikan pesanan satu cabang (status kosong = semua).
	List(ctx context.Context, branchID, status string) ([]Order, error)
	// Get mengembalikan satu pesanan milik cabang tersebut; ErrNotFound bila
	// tidak ada atau bukan miliknya.
	Get(ctx context.Context, id, branchID string) (Order, error)
	// CreateDraft menyimpan draft baru beserta barisnya dalam satu transaksi.
	CreateDraft(ctx context.Context, branchID, createdBy, catatan string, lines []Line) (Order, error)
	// UpdateDraft mengganti catatan dan seluruh baris draft (bukan diff).
	UpdateDraft(ctx context.Context, id, branchID, catatan string, lines []Line) (Order, error)
	// DeleteDraft menghapus draft milik cabang tersebut; ErrNotFound bila tidak
	// ada, bukan miliknya, atau sudah bukan draft.
	DeleteDraft(ctx context.Context, id, branchID string) error
}

// Service adalah logika bisnis pesanan cabang.
type Service struct {
	repo Repository
	cat  Catalog
}

// NewService membuat service pesanan.
func NewService(repo Repository, cat Catalog) *Service {
	return &Service{repo: repo, cat: cat}
}

// List mengembalikan daftar pesanan cabang sesuai filter status (PRD CP-ORD-01).
func (s *Service) List(ctx context.Context, actor auth.Actor, f Filter) ([]Order, error) {
	branchID, err := branchActor(actor)
	if err != nil {
		return nil, err
	}
	status, err := normStatus(f.Status)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.List(ctx, branchID, status)
	if err != nil {
		return nil, fmt.Errorf("daftar pesanan: %w", err)
	}
	return rows, nil
}

// Get mengembalikan detail satu pesanan milik cabang sesi, lengkap dengan
// barisnya. Pesanan cabang lain (atau id yang bukan UUID) dijawab 404.
func (s *Service) Get(ctx context.Context, actor auth.Actor, id string) (Order, error) {
	branchID, err := branchActor(actor)
	if err != nil {
		return Order{}, err
	}
	id = strings.TrimSpace(id)
	if !uuidRe.MatchString(id) {
		return Order{}, ErrNotFound
	}
	o, err := s.repo.Get(ctx, id, branchID)
	if err != nil {
		return Order{}, err
	}
	return o, nil
}

// CreateDraft membuat draft baru milik cabang sesi (PRD CP-ORD-03).
//
// Draft tidak punya nomor maupun tanggal: keduanya diberikan saat submit
// (F1-14), sehingga constraint draft_tanpa_nomor tetap terjaga.
func (s *Service) CreateDraft(ctx context.Context, actor auth.Actor, in DraftInput) (Order, error) {
	branchID, err := branchActor(actor)
	if err != nil {
		return Order{}, err
	}
	catatan, lines, err := s.buildLines(ctx, branchID, in)
	if err != nil {
		return Order{}, err
	}
	return s.repo.CreateDraft(ctx, branchID, actor.UserID, catatan, lines)
}

// UpdateDraft mengubah draft milik cabang sesi. Draft yang sudah dikirim tidak
// bisa diubah (PRD CP-ORD-09).
func (s *Service) UpdateDraft(ctx context.Context, actor auth.Actor, id string, in DraftInput) (Order, error) {
	cur, err := s.Get(ctx, actor, id)
	if err != nil {
		return Order{}, err
	}
	if cur.Status != StatusDraft {
		return Order{}, ErrLocked
	}
	// branchID dipakai lagi di klausa WHERE repository supaya status draft
	// diperiksa ulang di database.
	branchID, err := branchActor(actor)
	if err != nil {
		return Order{}, err
	}
	catatan, lines, err := s.buildLines(ctx, branchID, in)
	if err != nil {
		return Order{}, err
	}
	updated, err := s.repo.UpdateDraft(ctx, cur.ID, branchID, catatan, lines)
	if errors.Is(err, ErrNotFound) {
		// Berpindah status (mis. disubmit) setelah pemeriksaan di atas.
		return Order{}, ErrLocked
	}
	return updated, err
}

// DeleteDraft menghapus draft milik cabang sesi (PRD CP-ORD-03/09).
func (s *Service) DeleteDraft(ctx context.Context, actor auth.Actor, id string) error {
	cur, err := s.Get(ctx, actor, id)
	if err != nil {
		return err
	}
	if cur.Status != StatusDraft {
		return ErrLocked
	}
	branchID, err := branchActor(actor)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteDraft(ctx, cur.ID, branchID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrLocked
		}
		return err
	}
	return nil
}

// buildLines memvalidasi isi draft dan menyusun barisnya dengan harga dari
// branch_prices cabang SESI.
//
// Produk yang tidak ada di daftar "dijual ke cabang ini" ditolak — termasuk
// produk nonaktif dan produk yang hanya punya harga_default (PRD CP-KAT-02).
func (s *Service) buildLines(ctx context.Context, branchID string, in DraftInput) (string, []Line, error) {
	catatan := strings.TrimSpace(in.Catatan)
	if len([]rune(catatan)) > 500 {
		return "", nil, fmt.Errorf("%w: catatan maksimal 500 karakter", ErrInvalidData)
	}
	switch {
	case len(in.Lines) == 0:
		return "", nil, ErrEmptyLines
	case len(in.Lines) > MaxLines:
		return "", nil, fmt.Errorf("%w: maksimal %d baris per pesanan", ErrInvalidData, MaxLines)
	}

	produk, err := s.cat.BranchProducts(ctx, branchID)
	if err != nil {
		return "", nil, fmt.Errorf("baca harga cabang: %w", err)
	}
	dijual := make(map[string]catalog.BranchProduct, len(produk))
	for _, p := range produk {
		dijual[p.ID] = p
	}

	lines := make([]Line, 0, len(in.Lines))
	dipakai := map[string]bool{}
	var sertifikat, reguler int
	for i, baris := range in.Lines {
		pid := strings.TrimSpace(baris.ProductID)
		if pid == "" {
			return "", nil, fmt.Errorf("%w: baris %d belum memilih produk", ErrInvalidData, i+1)
		}
		if dipakai[pid] {
			return "", nil, fmt.Errorf("%w: produk yang sama tidak boleh dipesan dua kali", ErrInvalidData)
		}
		p, ok := dijual[pid]
		if !ok {
			return "", nil, fmt.Errorf("%w: baris %d", ErrProductNotSold, i+1)
		}
		if baris.Qty < 1 {
			return "", nil, fmt.Errorf("%w: qty baris %d minimal 1", ErrInvalidData, i+1)
		}
		if baris.Qty > maxQty {
			return "", nil, fmt.Errorf("%w: qty baris %d maksimal %d", ErrInvalidData, i+1, maxQty)
		}
		if p.Harga > 0 && int64(baris.Qty) > math.MaxInt64/p.Harga {
			return "", nil, fmt.Errorf("%w: nilai baris %d terlalu besar", ErrInvalidData, i+1)
		}
		if p.Jenis == catalog.JenisSertifikat {
			sertifikat++
		} else {
			reguler++
		}
		dipakai[pid] = true
		lines = append(lines, Line{
			LineNo:      i + 1,
			Jenis:       JenisProduct,
			ProductID:   pid,
			Nama:        p.Nama,
			Satuan:      p.Satuan,
			Qty:         baris.Qty,
			HargaSatuan: p.Harga,
			Subtotal:    int64(baris.Qty) * p.Harga,
		})
	}

	// PRD CP-SER-03 / Database.md §8: satu pesanan tidak boleh mencampur
	// Sertifikat dengan produk reguler. Penambahan baris biaya packaging dan
	// penandaan sertifikat dikerjakan F1-12.
	if sertifikat > 0 && reguler > 0 {
		return "", nil, fmt.Errorf("%w: Sertifikat dipesan terpisah dan tidak boleh dicampur produk reguler", ErrInvalidData)
	}

	return catatan, lines, nil
}

// branchActor memastikan aktor adalah akun cabang yang boleh menulis pesanan
// dan mengembalikan branch_id dari SESI.
func branchActor(actor auth.Actor) (string, error) {
	if !actor.IsBranch() || !actor.Can(auth.CapOrderWrite) {
		return "", ErrForbidden
	}
	if actor.BranchID == "" {
		return "", ErrNoBranch
	}
	return actor.BranchID, nil
}

// normStatus menyeragamkan filter status daftar pesanan.
func normStatus(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "semua", StatusDraft, StatusSubmitted, StatusDone:
		if s == "semua" {
			return "", nil
		}
		return s, nil
	default:
		return "", fmt.Errorf("%w: status %q tidak dikenal", ErrInvalidData, s)
	}
}
