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
//     semuanya lahir saat submit (F1-14).
//
// Submit (kartu F1-14) ada di submit.go: satu transaksi Draft -> Submitted
// yang mengunci baris pesanan, menyegarkan snapshot harga/nama dari
// master (BR-05), memastikan bukti transfer non-C&C, menerbitkan nomor invoice
// dan serial number dari tabel counter (BR-02/BR-03/BR-09), menetapkan tanggal
// bisnis WIB + tenggat C&C, dan mencatat timeline.
//
// Aturan Sertifikat dan Cash & Carry (kartu F1-12, PRD §3.6–3.7):
//
//   - Produk Sertifikat selalu berharga 0 berapa pun qty-nya (CP-SER-01). Harga
//     dari branch_prices tidak dipakai untuk jenis ini, jadi data harga yang
//     menyimpang tidak bisa membuat sertifikat berbayar.
//   - Pesanan yang memuat Sertifikat mendapat TEPAT SATU baris `packaging`
//     (qty 1, tanpa produk, tanpa serial) senilai `biaya_packaging` yang
//     berlaku di setelan server saat baris itu disusun (CP-SER-02, CP-SER-05).
//     Baris ini dibuat server dari daftar produk kiriman editor, sehingga
//     klien tidak punya cara menambah, mengubah, menghapus, atau menduplikasi
//     baris packaging — setiap simpan draft menyusunnya ulang dari nol.
//   - Pesanan Sertifikat tidak boleh dicampur produk reguler (CP-SER-03).
//   - Cash & Carry adalah cabang berdeposit (keputusan user 2026-10-18):
//     setiap pesanan cabang berflag `cash_and_carry` dibayar dari saldo
//     deposit saat dikirim, sebanyak PotonganDeposit; sisanya wajib ditutup
//     transfer berbukti. Flag dibaca server dari master cabang, bukan dari
//     klien, dan tidak ada lagi jatuh tempo maupun utang C&C.
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
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/settings"
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
	// ErrDepositChanged dikembalikan bila saldo deposit cabang berubah (versi
	// journal berbeda) sejak modal kirim dibuka. Submit dibatalkan supaya
	// potongan deposit tidak pernah berubah diam-diam (D3 §3.4).
	ErrDepositChanged = errors.New("saldo deposit berubah sejak ditampilkan")
	// ErrDepositVersionRequired dikembalikan bila cabang Cash & Carry mengirim
	// pesanan tanpa menyebut versi saldo deposit yang ia lihat.
	ErrDepositVersionRequired = fmt.Errorf("%w: versi saldo deposit yang ditampilkan wajib dikirim", ErrInvalidData)
	ErrOrderHeld              = errors.New("pembayaran Tempo ditahan karena tagihan lewat jatuh tempo")
	ErrPaymentModeRequired    = fmt.Errorf("%w: pilih cara pembayaran pesanan", ErrInvalidData)
	ErrPaymentModeChanged     = errors.New("cara pembayaran cabang berubah; muat ulang pesanan")
	ErrTempoCreditSuperadmin  = errors.New("revisi Tempo yang menghasilkan kredit membutuhkan Superadmin")
)

// Status pesanan (Database.md §3, enum order_status).
//
// Sengaja TIDAK ada StatusBatal: pembatalan pesanan Submitted masih pertanyaan
// terbuka PRD §8 no.4 dan keputusan kartu F1-11 adalah belum menyediakannya.
const (
	StatusDraft          = "draft"
	StatusSubmitted      = "submitted"
	StatusApprovedUnpaid = "approved_unpaid"
	StatusDone           = "done"
)

// Jenis baris pesanan (Database.md §3, enum line_kind).
const (
	JenisProduct   = "product"
	JenisPackaging = "packaging"
)

// Nama baris biaya packaging pesanan Sertifikat (PRD CP-SER-02).
const NamaBiayaPackaging = "Biaya packaging"

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
// Jenis "packaging" dipakai untuk baris biaya packaging pesanan Sertifikat:
// qty 1, tanpa product_id, dan tanpa serial number (PRD CP-SER-02).
type Line struct {
	LineNo    int    `json:"line_no"`
	Jenis     string `json:"jenis"`
	ProductID string `json:"product_id,omitempty"`
	Nama      string `json:"nama"`

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
	ID              string `json:"id"`
	BranchID        string `json:"-"`
	CreatedBy       string `json:"-"`
	Nomor           string `json:"nomor,omitempty"`
	Status          string `json:"status"`
	VerifStatus     string `json:"verif_status"`
	Versi           int    `json:"versi"`
	IsCNC           bool   `json:"is_cnc"`
	IsTempo         bool   `json:"is_tempo"`
	TempoDueDate    string `json:"tempo_due_date,omitempty"`
	TempoPaidAmount int64  `json:"tempo_paid_amount"`
	IsSertifikat    bool   `json:"is_sertifikat"`
	// Tanggal adalah tanggal submit (WIB) dalam bentuk YYYY-MM-DD; kosong
	// selama masih draft (BR-04).
	Tanggal     string    `json:"tanggal,omitempty"`
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

// Config adalah sudut pandang domain pengaturan yang dibutuhkan aturan
// pesanan: nilai bilangan bulat yang sedang berlaku untuk satu kunci
// (biaya_packaging, digit_invoice). Implementasinya adalah settings.Service,
// dan nilainya dibaca SERVER — bukan dari klien — sehingga snapshot biaya
// packaging dan penomoran selalu mengikuti setelan yang berlaku
// (PRD CP-SER-05, BR-03).
type Config interface {
	Int(ctx context.Context, key string) (int64, error)
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
	// Submit menjalankan transisi draft -> submitted dalam SATU transaksi:
	// mengunci baris pesanan, memvalidasi ulang isinya terhadap master dan
	// harga cabang yang berlaku saat itu (SiapkanSubmit), memastikan bukti
	// transfer non-C&C, mengambil nomor invoice + serial dari tabel counter,
	// lalu menulis snapshot, serial, status, tanggal, tenggat, total, dan
	// timeline. ErrLocked bila pesanan sudah bukan draft; ErrNotFound bila
	// tidak ada atau bukan milik cabang tersebut; ErrProofRequired bila bukan
	// C&C dan belum ada bukti.
	Submit(ctx context.Context, req SubmitRequest) (Order, error)
}

// Service adalah logika bisnis pesanan cabang.
type Service struct {
	repo  Repository
	cat   Catalog
	cfg   Config
	flags BranchFlags
	banks PaymentAccountSource
	clk   clock.Clock
	// ai membaca setelan AI (PRD §4.9). Boleh nil: tanpa sumber setelan,
	// submit tidak pernah menitipkan pekerjaan pembacaan bukti (F2-26).
	ai AISettingsSource
}

// NewService membuat service pesanan. cfg menyajikan setelan operasional
// server (biaya packaging pesanan sertifikat); ia tidak boleh nil karena biaya
// packaging tidak pernah diambil dari klien.
//
// Sumber flag cabang (C&C) dan clock dipasang lewat Option: tanpa
// WithBranchFlags, permintaan C&C ditolak (fail closed); tanpa WithClock
// dipakai clock.System().
func NewService(repo Repository, cat Catalog, cfg Config, opts ...Option) *Service {
	s := &Service{repo: repo, cat: cat, cfg: cfg, clk: clock.System()}
	for _, o := range opts {
		o(s)
	}
	return s
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
//
// Aturan Sertifikat (PRD §3.6) diterapkan di sini, di server:
//
//   - baris produk Sertifikat berharga 0 berapa pun qty-nya (CP-SER-01);
//   - Sertifikat tidak boleh dicampur produk reguler (CP-SER-03);
//   - pesanan Sertifikat mendapat tepat satu baris `packaging` qty 1 senilai
//     `biaya_packaging` yang berlaku (CP-SER-02/05). Baris itu SELALU disusun
//     server dari daftar produk kiriman editor, sehingga klien tidak bisa
//     menambah, mengubah, menghapus, atau menduplikasinya: mengirim ulang
//     draft menyusunnya ulang dari nol, dan satu-satunya bentuk baris yang
//     diterima klien adalah product_id yang benar-benar dijual ke cabangnya.
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

	lines := make([]Line, 0, len(in.Lines)+1)
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

		harga := p.Harga
		if p.Jenis == catalog.JenisSertifikat {
			// CP-SER-01/BR-06: harga Sertifikat SELALU 0 berapa pun qty.
			harga = 0
			sertifikat++
		} else {
			reguler++
		}
		dipakai[pid] = true
		lines = append(lines, Line{
			LineNo:    i + 1,
			Jenis:     JenisProduct,
			ProductID: pid,
			Nama:      p.Nama,

			Qty:         baris.Qty,
			HargaSatuan: harga,
			Subtotal:    int64(baris.Qty) * harga,
		})
	}

	// PRD CP-SER-03 / Database.md §8: satu pesanan tidak boleh mencampur
	// Sertifikat dengan produk reguler (trigger order_lines_guard_kind juga
	// menolaknya di database).
	if sertifikat > 0 && reguler > 0 {
		return "", nil, fmt.Errorf("%w: Sertifikat dipesan terpisah dan tidak boleh dicampur produk reguler", ErrInvalidData)
	}

	// CP-SER-02: tepat satu baris biaya packaging untuk pesanan Sertifikat,
	// senilai setelan `biaya_packaging` yang berlaku saat draft disimpan.
	if sertifikat > 0 {
		biaya, err := s.cfg.Int(ctx, settings.BiayaPackaging)
		if err != nil {
			return "", nil, fmt.Errorf("baca biaya packaging: %w", err)
		}
		if biaya < 0 {
			return "", nil, fmt.Errorf("%w: biaya packaging tidak valid", ErrInvalidData)
		}
		lines = append(lines, packagingLine(len(lines)+1, biaya))
	}

	return catatan, lines, nil
}

// packagingLine menyusun baris biaya packaging pesanan Sertifikat: qty 1,
// tanpa product_id, tanpa serial number (Database.md §6), harga snapshot
// setelan server saat baris itu dibuat.
func packagingLine(lineNo int, biaya int64) Line {
	return Line{
		LineNo: lineNo,
		Jenis:  JenisPackaging,
		Nama:   NamaBiayaPackaging,

		Qty:         1,
		HargaSatuan: biaya,
		Subtotal:    biaya,
	}
}

// TanggalWIB mengembalikan tanggal bisnis (tengah malam WIB) dari sebuah waktu.
// Batas harinya adalah zona Asia/Jakarta (UTC+7, tanpa DST) — bukan UTC —
// sehingga submit pukul 00:30 WIB tercatat pada hari yang benar, bukan pada
// hari UTC sebelumnya (PRD CP-ORD-06, BR-04).
func TanggalWIB(t time.Time) time.Time {
	y, m, d := t.In(clock.Jakarta()).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, clock.Jakarta())
}

// PotonganDeposit adalah bagian total pesanan Cash & Carry yang dibayar dari
// saldo deposit saat submit: seluruh saldo bila kurang dari total, atau tepat
// sebesar total bila saldo cukup. Sisanya (total - potongan) wajib ditutup
// transfer berbukti.
func PotonganDeposit(saldo, total int64) int64 {
	if saldo <= 0 || total <= 0 {
		return 0
	}
	return min(saldo, total)
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
	case "", "semua", StatusDraft, StatusSubmitted, StatusApprovedUnpaid, StatusDone:
		if s == "semua" {
			return "", nil
		}
		return s, nil
	default:
		return "", fmt.Errorf("%w: status %q tidak dikenal", ErrInvalidData, s)
	}
}
