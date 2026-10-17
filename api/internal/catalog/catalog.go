// Package catalog menyediakan katalog produk (harga khusus cabang), master
// produk, dan matriks harga Produk × Cabang untuk kedua portal.
//
// Aturan yang ditegakkan di sini — bukan hanya disembunyikan di UI:
//   - Harga default BUKAN cadangan katalog: produk hanya dijual ke cabang yang
//     punya baris branch_prices. Sel kosong = tidak dijual, sehingga produknya
//     tidak tampil di portal cabang dan tidak bisa dipesan (PRD CP-KAT-01/02,
//     AP-KAT-03). Query katalog cabang memakai INNER JOIN, bukan COALESCE.
//   - Produk Sertifikat selalu berharga 0 — baik harga_default maupun harga
//     per cabangnya (PRD CP-SER-01, BR-06).
//   - Produk yang sudah dipakai order line tidak boleh dihapus, hanya
//     dinonaktifkan (order_lines.product_id ON DELETE RESTRICT).
//   - Setiap mutasi produk/harga dicatat ke audit_logs DI DALAM transaksi yang
//     sama dengan perubahannya (PRD AP-KAT-06, SystemDesign.md §5.4).
//   - Harga sel matriks SELALU dihitung server dari komponen HJM, P1, dan SPV
//     (tarif = HJM + HJM×P1 + SPV, lihat paket tarif). Setiap perubahan sel
//     menjadi versi tarif append-only (branch_tariff_versions) dan tarif
//     finalnya diproyeksikan ke branch_prices yang dibaca katalog dan pesanan.
//   - Perubahan harga TIDAK menyentuh pesanan yang sudah ada: harga pada order
//     line adalah snapshot (PRD AP-KAT-05, BR-05). Paket ini tidak pernah
//     menulis order_lines.
//
// Master cabang untuk Admin Portal TIDAK ada di sini: sejak F1-06 CRUD cabang
// beserta token registrasinya dimiliki paket internal/branch. Yang tersisa di
// sini hanya pembacaan satu cabang untuk /me/branch (portal cabang). Daftar
// cabang untuk matriks harga diambil lapisan transport dari layanan master
// cabang (lihat handlers_catalog.go) supaya katalog tidak membaca tabel milik
// domain lain.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/tarif"
)

// Sentinel error domain. Pemetaan ke kode HTTP ada di lapisan transport.
var (
	// ErrNotFound dikembalikan bila data tidak ada.
	ErrNotFound = errors.New("data tidak ditemukan")
	// ErrForbidden dikembalikan bila aktor tidak berhak melihat data tersebut.
	ErrForbidden = errors.New("tidak berhak mengakses data ini")
	// ErrNoBranch dikembalikan bila akun cabang tidak terhubung ke cabang mana pun.
	ErrNoBranch = errors.New("akun tidak terhubung ke cabang")
	// ErrInvalidData dikembalikan bila data produk/harga tidak lolos validasi.
	ErrInvalidData = errors.New("data katalog tidak valid")
	// ErrKodeTaken dikembalikan bila kode produk sudah dipakai produk lain.
	ErrKodeTaken = errors.New("kode produk sudah dipakai")
	// ErrProductUsed dikembalikan bila produk sudah dipakai order line atau
	// punya riwayat tarif berversi sehingga tidak boleh dihapus.
	ErrProductUsed = errors.New("produk sudah dipakai pesanan atau punya riwayat tarif")
)

// Jenis produk (Database.md §3, tipe enum product_kind).
const (
	JenisReguler    = "reguler"
	JenisSertifikat = "sertifikat"
)

// Batas ukuran batch perubahan harga; satu permintaan tidak boleh memuat
// ribuan sel sekaligus supaya transaksinya tetap pendek.
const maxPriceBatch = 1000

// Product adalah produk pada katalog (PRD §4.5 AP-KAT-01).
type Product struct {
	ID           string `json:"id"`
	Kode         string `json:"kode,omitempty"`
	Nama         string `json:"nama"`
	Jenis        string `json:"jenis"`
	Deskripsi    string `json:"deskripsi,omitempty"`
	HargaDefault int64  `json:"harga_default"`
	Urutan       int    `json:"urutan"`
	IsActive     bool   `json:"is_active"`
}

// BranchProduct adalah produk yang benar-benar dijual ke satu cabang beserta
// harga khususnya. Produk tanpa baris harga tidak pernah muncul di sini
// (PRD CP-KAT-02).
type BranchProduct struct {
	ID        string `json:"id"`
	Nama      string `json:"nama"`
	Jenis     string `json:"jenis"`
	Deskripsi string `json:"deskripsi,omitempty"`
	Harga     int64  `json:"harga"`
	Urutan    int    `json:"urutan"`
	// BiayaPackaging hanya terisi untuk produk Sertifikat: biaya packaging per
	// pesanan yang ditambahkan otomatis (PRD CP-KAT-04, CP-SER-02). Nilainya
	// diisi lapisan transport dari setelan server yang berlaku supaya portal
	// cabang bisa menampilkan catatan biaya dan menghitung total editor sebelum
	// draft disimpan — cabang tidak pernah membaca setelan langsung.
	BiayaPackaging int64 `json:"biaya_packaging,omitempty"`
}

// Branch adalah data cabang (master data) untuk /me/branch portal cabang.
type Branch struct {
	ID         string `json:"id"`
	Nama       string `json:"nama"`
	NamaKepala string `json:"nama_kepala"`
	Email      string `json:"email"`
	Telepon    string `json:"telepon"`
	IsCNC      bool   `json:"is_cnc"`
	IsSpecial  bool   `json:"is_special"`
	PajakIn    bool   `json:"pajak_in"`
	IsActive   bool   `json:"is_active"`
}

// Sumber rincian sel matriks harga.
const (
	// SumberImpor: versi tarif hasil impor master Impact.
	SumberImpor = "import"
	// SumberManual: versi tarif hasil edit matriks harga oleh admin.
	SumberManual = "manual"
	// SumberLama: harga tanpa rincian komponen (dibuat sebelum tarif
	// berversi, atau tarif finalnya tidak lagi sama dengan harga sel).
	SumberLama = "legacy"
)

// PriceCell adalah satu sel matriks harga: tarif produk untuk satu cabang.
// Sel yang tidak dijual tidak punya PriceCell sama sekali.
type PriceCell struct {
	BranchID  string `json:"branch_id"`
	ProductID string `json:"product_id"`
	// Harga adalah tarif final satu item yang dibaca katalog cabang & pesanan.
	Harga int64 `json:"harga"`
	// HJM, P1 (rasio desimal 0–1, "0.1" = 10%), dan SPV pembentuk Harga; nil
	// untuk sel tanpa rincian (Sumber SumberLama).
	HJM    *int64  `json:"hjm"`
	P1     *string `json:"p1"`
	SPV    *int64  `json:"spv"`
	Sumber string  `json:"sumber"`
}

// Komponen adalah masukan tarif satu sel: HJM dan SPV dalam Rupiah, P1 rasio
// desimal 0–1 (kosong = 0).
type Komponen struct {
	HJM int64  `json:"hjm"`
	P1  string `json:"p1"`
	SPV int64  `json:"spv"`
}

// TarifSel adalah komponen yang sudah divalidasi beserta tarif final dan
// sidiknya — bentuk yang disimpan sebagai versi tarif.
type TarifSel struct {
	Komponen
	Final int64
	Sidik string
}

// maxDesimalP1 membatasi P1 ke enam digit desimal rasio (0,0001%), cukup untuk
// persentase berpecahan tanpa menyimpan pecahan berulang yang tidak eksak.
var maxDesimalP1 = big.NewRat(1_000_000, 1)

// HitungTarif memvalidasi komponen dan menghitung tarif finalnya. Produk
// Sertifikat hanya boleh berkomponen nol (PRD CP-SER-01).
func HitungTarif(k Komponen, jenis string) (TarifSel, error) {
	if k.HJM < 0 || k.SPV < 0 {
		return TarifSel{}, fmt.Errorf("%w: HJM dan SPV tidak boleh negatif", ErrInvalidData)
	}
	p1, err := tarif.ParseRasio(k.P1)
	if err != nil {
		return TarifSel{}, fmt.Errorf("%w: P1 harus 0 sampai 100%%", ErrInvalidData)
	}
	if !new(big.Rat).Mul(p1, maxDesimalP1).IsInt() {
		return TarifSel{}, fmt.Errorf("%w: P1 maksimal empat angka di belakang koma persen", ErrInvalidData)
	}
	if jenis == JenisSertifikat && (k.HJM != 0 || k.SPV != 0 || p1.Sign() != 0) {
		return TarifSel{}, fmt.Errorf("%w: produk Sertifikat selalu berharga 0", ErrInvalidData)
	}
	final, ok := tarif.Final(k.HJM, p1, k.SPV)
	if !ok {
		return TarifSel{}, fmt.Errorf("%w: tarif terlalu besar", ErrInvalidData)
	}
	return TarifSel{
		Komponen: Komponen{HJM: k.HJM, P1: tarif.Desimal(p1), SPV: k.SPV},
		Final:    final,
		Sidik:    tarif.SidikTersedia(k.HJM, p1, k.SPV, final),
	}, nil
}

// TarifDariSel membentuk tarif dari sel yang sudah ada (dipakai salin harga).
// Sel tanpa rincian dibaca sebagai HJM = harga, P1 = 0, SPV = 0 — tarif finalnya
// identik dengan harga lama, sehingga salinannya tetap berharga sama.
func TarifDariSel(c PriceCell) (TarifSel, error) {
	k := Komponen{HJM: c.Harga}
	if c.HJM != nil && c.P1 != nil && c.SPV != nil {
		k = Komponen{HJM: *c.HJM, P1: *c.P1, SPV: *c.SPV}
	}
	return HitungTarif(k, JenisReguler)
}

// SamaDengan melaporkan apakah sel sudah memuat tarif t persis (komponen dan
// harga), sehingga menulisnya lagi tidak perlu membuat versi baru.
func (c PriceCell) SamaDengan(t TarifSel) bool {
	if c.Sumber == SumberLama || c.HJM == nil || c.P1 == nil || c.SPV == nil {
		return false
	}
	p1Sel, err1 := tarif.ParseRasio(*c.P1)
	p1Baru, err2 := tarif.ParseRasio(t.P1)
	return err1 == nil && err2 == nil && p1Sel.Cmp(p1Baru) == 0 &&
		*c.HJM == t.HJM && *c.SPV == t.SPV && c.Harga == t.Final
}

// auditSel adalah isi before/after audit untuk satu sel harga.
func auditSel(branchID string, c *PriceCell, t *TarifSel) map[string]any {
	switch {
	case t != nil:
		return map[string]any{"branch_id": branchID, "harga": t.Final, "hjm": t.HJM, "p1": t.P1, "spv": t.SPV}
	case c != nil && c.HJM != nil && c.P1 != nil && c.SPV != nil:
		return map[string]any{"branch_id": branchID, "harga": c.Harga, "hjm": *c.HJM, "p1": *c.P1, "spv": *c.SPV}
	case c != nil:
		return map[string]any{"branch_id": branchID, "harga": c.Harga}
	}
	return nil
}

// ProductFilter adalah penyaring daftar produk admin. Field nil/kosong berarti
// tidak disaring.
type ProductFilter struct {
	// Q mencari pada nama dan kode (ILIKE).
	Q string
	// Jenis membatasi ke "reguler" atau "sertifikat".
	Jenis  string
	Active *bool
}

// PriceFilter adalah penyaring sel matriks harga.
type PriceFilter struct {
	BranchID  string
	ProductID string
}

// NewProduct adalah permintaan pembuatan produk.
type NewProduct struct {
	Kode         string
	Nama         string
	Jenis        string
	Deskripsi    string
	HargaDefault int64
	Urutan       int
	// IsActive nil berarti produk baru aktif (default kolom).
	IsActive *bool
}

// ProductPatch adalah perubahan sebagian produk. Field nil berarti tidak diubah.
type ProductPatch struct {
	Kode         *string
	Nama         *string
	Jenis        *string
	Deskripsi    *string
	HargaDefault *int64
	Urutan       *int
	IsActive     *bool
}

// PriceInput adalah satu sel yang dikirim pada batch edit matriks harga.
// Komponen nil berarti sel dikosongkan (produk tidak dijual ke cabang itu).
// Harga jual tidak pernah dikirim klien: selalu dihitung dari komponen.
type PriceInput struct {
	BranchID  string
	ProductID string
	Komponen  *Komponen
}

// PriceResult merangkum hasil batch edit matriks harga.
type PriceResult struct {
	Diubah    int `json:"diubah"`
	Dihapus   int `json:"dihapus"`
	Diabaikan int `json:"diabaikan"`
}

// CopyInput adalah permintaan "salin harga" dari satu cabang ke cabang lain.
// Daftar cabang tujuan sudah diselesaikan pemanggil (termasuk saat memilih
// "semua cabang") karena katalog tidak memiliki master cabang.
type CopyInput struct {
	FromBranchID string
	ToBranchIDs  []string
}

// CopiedBranch merangkum hasil salin ke satu cabang tujuan.
type CopiedBranch struct {
	BranchID string `json:"branch_id"`
	Jumlah   int    `json:"jumlah"`
	Diubah   int    `json:"diubah"`
}

// CopyResult merangkum hasil aksi salin harga.
type CopyResult struct {
	FromBranchID string         `json:"from_branch_id"`
	Jumlah       int            `json:"jumlah"`
	Salinan      []CopiedBranch `json:"salinan"`
}

// PriceChange adalah satu perubahan sel yang sudah divalidasi service dan
// siap dieksekusi repository di dalam satu transaksi.
type PriceChange struct {
	BranchID  string
	ProductID string
	// Tarif nil berarti sel dikosongkan: versi tarif aktifnya ditutup dan
	// baris harganya dihapus.
	Tarif *TarifSel
}

// Repository memuat dan mengubah data katalog.
//
// Master cabang untuk Admin Portal TIDAK ada di sini (lihat komentar paket).
// Setiap metode yang mengubah data menerima audit.Entry dan WAJIB menyimpannya
// di dalam transaksi yang sama dengan perubahannya (SystemDesign.md §5.4).
type Repository interface {
	// Pembacaan katalog.
	ListProducts(ctx context.Context) ([]Product, error)
	ListActiveProducts(ctx context.Context) ([]Product, error)
	ListProductsForBranch(ctx context.Context, branchID string) ([]BranchProduct, error)
	GetBranch(ctx context.Context, id string) (Branch, error)

	// Master produk (admin).
	ListProductsForAdmin(ctx context.Context, f ProductFilter) ([]Product, error)
	GetProduct(ctx context.Context, id string) (Product, error)
	CreateProduct(ctx context.Context, in Product, rec audit.Entry) (Product, error)
	UpdateProduct(ctx context.Context, in Product, rec audit.Entry) (Product, error)
	DeleteProduct(ctx context.Context, id string, rec audit.Entry) error
	ProductKodeTaken(ctx context.Context, kode, exceptID string) (bool, error)
	ProductUsed(ctx context.Context, id string) (bool, error)

	// Matriks harga per cabang.
	ListPrices(ctx context.Context, f PriceFilter) ([]PriceCell, error)
	// ApplyPriceChanges menerapkan seluruh perubahan sel — versi tarif manual
	// baru, proyeksi harganya, dan entri audit — dalam SATU transaksi.
	ApplyPriceChanges(ctx context.Context, changes []PriceChange, actorID string, recs []audit.Entry) error
	// CopyPrices menggantikan seluruh matriks harga setiap cabang tujuan
	// dengan komponen tarif cabang sumber (TarifDariSel), beserta satu entri
	// audit per cabang; sel yang sudah sama tidak membuat versi baru.
	CopyPrices(ctx context.Context, fromBranchID string, toBranchIDs []string, actorID string, recs []audit.Entry) error
}

// Service adalah logika bisnis katalog.
type Service struct {
	repo Repository
}

// NewService membuat service katalog.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// Products mengembalikan katalog sesuai sudut pandang aktor:
// cabang hanya melihat produk yang dijual ke dirinya (harga khusus cabang),
// admin melihat semua produk aktif dengan harga default (PRD §3.4).
func (s *Service) Products(ctx context.Context, actor auth.Actor) (any, error) {
	switch {
	case actor.IsBranch():
		if actor.BranchID == "" {
			return nil, ErrNoBranch
		}
		rows, err := s.repo.ListProductsForBranch(ctx, actor.BranchID)
		if err != nil {
			return nil, fmt.Errorf("katalog cabang: %w", err)
		}
		return rows, nil
	case actor.IsAdmin():
		rows, err := s.repo.ListActiveProducts(ctx)
		if err != nil {
			return nil, fmt.Errorf("katalog admin: %w", err)
		}
		return rows, nil
	default:
		return nil, ErrForbidden
	}
}

// BranchProducts mengembalikan produk yang benar-benar dijual ke satu cabang
// beserta harga khususnya (PRD CP-KAT-01/02).
//
// Metode ini dipakai domain pesanan (F1-11) untuk mengambil harga order line:
// daftarnya adalah INNER JOIN products × branch_prices, jadi produk tanpa baris
// harga cabang tidak pernah ada di sini dan harga_default tidak pernah menjadi
// cadangan. branchID berasal dari SESI pemanggil, bukan dari permintaan klien.
func (s *Service) BranchProducts(ctx context.Context, branchID string) ([]BranchProduct, error) {
	if strings.TrimSpace(branchID) == "" {
		return nil, ErrNoBranch
	}
	rows, err := s.repo.ListProductsForBranch(ctx, branchID)
	if err != nil {
		return nil, fmt.Errorf("produk cabang: %w", err)
	}
	return rows, nil
}

// Branch mengembalikan satu cabang. Kepemilikan cabang diperiksa di sini
// lewat Actor (bukan dari parameter klien): akun cabang hanya boleh membaca
// cabangnya sendiri (isolasi data cabang, Architecture.md §6).
func (s *Service) Branch(ctx context.Context, actor auth.Actor, id string) (Branch, error) {
	if err := actor.RequireBranch(id); err != nil {
		return Branch{}, err
	}
	row, err := s.repo.GetBranch(ctx, id)
	if err != nil {
		return Branch{}, err
	}
	return row, nil
}

// ListProducts mengembalikan daftar produk untuk halaman katalog admin,
// termasuk produk nonaktif (PRD AP-KAT-01). Manager & Superadmin boleh
// (CapAdminCatalog, PRD §2.1).
func (s *Service) ListProducts(ctx context.Context, actor auth.Actor, f ProductFilter) ([]Product, error) {
	if !actor.Can(auth.CapAdminCatalog) {
		return nil, ErrForbidden
	}
	f.Q = strings.TrimSpace(f.Q)
	f.Jenis = strings.ToLower(strings.TrimSpace(f.Jenis))
	if f.Jenis != "" && !knownJenis(f.Jenis) {
		return nil, fmt.Errorf("%w: jenis produk harus %s atau %s", ErrInvalidData, JenisReguler, JenisSertifikat)
	}
	rows, err := s.repo.ListProductsForAdmin(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("daftar produk: %w", err)
	}
	return rows, nil
}

// GetProduct mengembalikan satu produk.
func (s *Service) GetProduct(ctx context.Context, actor auth.Actor, id string) (Product, error) {
	if !actor.Can(auth.CapAdminCatalog) {
		return Product{}, ErrForbidden
	}
	if id == "" {
		return Product{}, ErrNotFound
	}
	return s.repo.GetProduct(ctx, id)
}

// CreateProduct menambahkan produk baru (PRD AP-KAT-01).
func (s *Service) CreateProduct(ctx context.Context, actor auth.Actor, in NewProduct) (Product, error) {
	if !actor.Can(auth.CapAdminCatalog) {
		return Product{}, ErrForbidden
	}
	next := normProduct(Product{
		Kode:         in.Kode,
		Nama:         in.Nama,
		Jenis:        in.Jenis,
		Deskripsi:    in.Deskripsi,
		HargaDefault: in.HargaDefault,
		Urutan:       in.Urutan,
		IsActive:     true,
	})
	if in.IsActive != nil {
		next.IsActive = *in.IsActive
	}
	if err := validateProduct(next); err != nil {
		return Product{}, err
	}
	if err := s.ensureKodeFree(ctx, next.Kode, ""); err != nil {
		return Product{}, err
	}
	rec := audit.NewEntry(ctx, actor, audit.EntityProduct, "", audit.ActionCreate, nil, next)
	saved, err := s.repo.CreateProduct(ctx, next, rec)
	if err != nil {
		return Product{}, err
	}
	return saved, nil
}

// UpdateProduct mengubah produk, termasuk mengaktifkan/menonaktifkannya.
func (s *Service) UpdateProduct(ctx context.Context, actor auth.Actor, id string, p ProductPatch) (Product, error) {
	if !actor.Can(auth.CapAdminCatalog) {
		return Product{}, ErrForbidden
	}
	if id == "" {
		return Product{}, ErrNotFound
	}
	cur, err := s.repo.GetProduct(ctx, id)
	if err != nil {
		return Product{}, err
	}
	next := cur
	if p.Kode != nil {
		next.Kode = strings.TrimSpace(*p.Kode)
	}
	if p.Nama != nil {
		next.Nama = strings.TrimSpace(*p.Nama)
	}
	if p.Jenis != nil {
		next.Jenis = strings.ToLower(strings.TrimSpace(*p.Jenis))
	}

	if p.Deskripsi != nil {
		next.Deskripsi = strings.TrimSpace(*p.Deskripsi)
	}
	if p.HargaDefault != nil {
		next.HargaDefault = *p.HargaDefault
	}
	if p.Urutan != nil {
		next.Urutan = *p.Urutan
	}
	if p.IsActive != nil {
		next.IsActive = *p.IsActive
	}
	next = normProduct(next)
	if err := validateProduct(next); err != nil {
		return Product{}, err
	}
	if next.Kode != cur.Kode {
		if err := s.ensureKodeFree(ctx, next.Kode, id); err != nil {
			return Product{}, err
		}
	}
	// Mengubah produk menjadi Sertifikat ditolak bila masih ada harga per
	// cabang yang bukan 0: matriksnya akan melanggar aturan "Sertifikat selalu
	// berharga 0" (PRD CP-SER-01). Admin harus mengosongkan/menolkan selnya
	// lebih dulu.
	if next.Jenis == JenisSertifikat && cur.Jenis != JenisSertifikat {
		sel, err := s.repo.ListPrices(ctx, PriceFilter{ProductID: id})
		if err != nil {
			return Product{}, fmt.Errorf("periksa harga cabang produk: %w", err)
		}
		for _, c := range sel {
			if c.Harga != 0 {
				return Product{}, fmt.Errorf("%w: kosongkan dulu harga cabang produk ini sebelum dijadikan Sertifikat", ErrInvalidData)
			}
		}
	}
	rec := audit.NewEntry(ctx, actor, audit.EntityProduct, id, audit.ActionUpdate, cur, next)
	saved, err := s.repo.UpdateProduct(ctx, next, rec)
	if err != nil {
		return Product{}, err
	}
	return saved, nil
}

// DeleteProduct menghapus produk yang belum dipakai order line. Produk yang
// sudah dipakai hanya boleh dinonaktifkan (PRD AP-KAT-01, order_lines
// .product_id ON DELETE RESTRICT).
func (s *Service) DeleteProduct(ctx context.Context, actor auth.Actor, id string) error {
	if !actor.Can(auth.CapAdminCatalog) {
		return ErrForbidden
	}
	if id == "" {
		return ErrNotFound
	}
	cur, err := s.repo.GetProduct(ctx, id)
	if err != nil {
		return err
	}
	dipakai, err := s.repo.ProductUsed(ctx, id)
	if err != nil {
		return err
	}
	if dipakai {
		return ErrProductUsed
	}
	rec := audit.NewEntry(ctx, actor, audit.EntityProduct, id, audit.ActionDelete, cur, nil)
	return s.repo.DeleteProduct(ctx, id, rec)
}

// Prices mengembalikan sel matriks harga (hanya sel yang berisi).
func (s *Service) Prices(ctx context.Context, actor auth.Actor, f PriceFilter) ([]PriceCell, error) {
	if !actor.Can(auth.CapAdminCatalog) {
		return nil, ErrForbidden
	}
	rows, err := s.repo.ListPrices(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("matriks harga: %w", err)
	}
	return rows, nil
}

// SetPrices menerapkan batch edit matriks harga (PRD AP-KAT-02/03).
//
// Aturan: setiap sel diisi komponen HJM, P1, dan SPV; harga jualnya dihitung
// server (HitungTarif) dan tidak pernah diterima dari klien. Produk Sertifikat
// hanya boleh berkomponen nol. Sel yang komponennya sama dengan versi aktif
// diabaikan (tidak ada versi/audit berisik); sel yang dikosongkan membuat
// produk tidak lagi dijual ke cabang itu.
func (s *Service) SetPrices(ctx context.Context, actor auth.Actor, items []PriceInput) (PriceResult, error) {
	if !actor.Can(auth.CapAdminCatalog) {
		return PriceResult{}, ErrForbidden
	}
	if len(items) == 0 {
		return PriceResult{}, fmt.Errorf("%w: tidak ada sel yang dikirim", ErrInvalidData)
	}
	if len(items) > maxPriceBatch {
		return PriceResult{}, fmt.Errorf("%w: maksimal %d sel per permintaan", ErrInvalidData, maxPriceBatch)
	}

	// Produk dimuat sekali per id supaya aturan Sertifikat bisa ditegakkan dan
	// produk tak dikenal ditolak dengan pesan jelas sebelum menyentuh DB.
	products := map[string]Product{}
	for _, it := range items {
		if strings.TrimSpace(it.BranchID) == "" || strings.TrimSpace(it.ProductID) == "" {
			return PriceResult{}, fmt.Errorf("%w: branch_id dan product_id wajib diisi", ErrInvalidData)
		}
		if _, ok := products[it.ProductID]; ok {
			continue
		}
		p, err := s.repo.GetProduct(ctx, it.ProductID)
		if err != nil {
			return PriceResult{}, fmt.Errorf("%w: produk %s tidak dikenal", ErrInvalidData, it.ProductID)
		}
		products[it.ProductID] = p
	}

	// Nilai sekarang dipakai untuk menghitung diff (dan jumlah audit yang
	// benar-benar berubah).
	sekarang, err := s.repo.ListPrices(ctx, PriceFilter{})
	if err != nil {
		return PriceResult{}, fmt.Errorf("baca matriks harga saat ini: %w", err)
	}
	ada := make(map[[2]string]PriceCell, len(sekarang))
	for _, c := range sekarang {
		ada[[2]string{c.BranchID, c.ProductID}] = c
	}

	var (
		changes []PriceChange
		recs    []audit.Entry
		res     PriceResult
	)
	for _, it := range items {
		lama, punyaBaris := ada[[2]string{it.BranchID, it.ProductID}]
		var before any
		if punyaBaris {
			before = auditSel(it.BranchID, &lama, nil)
		}
		if it.Komponen == nil {
			if !punyaBaris {
				res.Diabaikan++
				continue
			}
			changes = append(changes, PriceChange{BranchID: it.BranchID, ProductID: it.ProductID})
			recs = append(recs, audit.NewEntry(ctx, actor, audit.EntityBranchPrice, it.ProductID, audit.ActionDelete, before, nil))
			res.Dihapus++
			continue
		}
		t, err := HitungTarif(*it.Komponen, products[it.ProductID].Jenis)
		if err != nil {
			return PriceResult{}, err
		}
		if punyaBaris && lama.SamaDengan(t) {
			res.Diabaikan++
			continue
		}
		changes = append(changes, PriceChange{BranchID: it.BranchID, ProductID: it.ProductID, Tarif: &t})
		aksi := audit.ActionCreate
		if punyaBaris {
			aksi = audit.ActionUpdate
		}
		recs = append(recs, audit.NewEntry(ctx, actor, audit.EntityBranchPrice, it.ProductID, aksi, before, auditSel(it.BranchID, nil, &t)))
		res.Diubah++
	}
	if len(changes) == 0 {
		return res, nil
	}
	if err := s.repo.ApplyPriceChanges(ctx, changes, actor.UserID, recs); err != nil {
		return PriceResult{}, err
	}
	return res, nil
}

// CopyPrices menggantikan matriks harga setiap cabang tujuan dengan komponen
// tarif cabang sumber (PRD AP-KAT-04). Bersifat replace: sel yang tidak ada di
// cabang sumber menjadi kosong di cabang tujuan.
//
// Daftar cabang tujuan sudah diperiksa lapisan transport (katalog tidak
// memiliki master cabang); service memastikan cabang sumber punya isi dan
// menyiapkan satu entri audit per cabang tujuan.
func (s *Service) CopyPrices(ctx context.Context, actor auth.Actor, in CopyInput) (CopyResult, error) {
	if !actor.Can(auth.CapAdminCatalog) {
		return CopyResult{}, ErrForbidden
	}
	from := strings.TrimSpace(in.FromBranchID)
	if from == "" {
		return CopyResult{}, fmt.Errorf("%w: cabang sumber wajib diisi", ErrInvalidData)
	}
	sumber, err := s.repo.ListPrices(ctx, PriceFilter{BranchID: from})
	if err != nil {
		return CopyResult{}, fmt.Errorf("baca harga cabang sumber: %w", err)
	}
	if len(sumber) == 0 {
		return CopyResult{}, fmt.Errorf("%w: cabang sumber belum punya harga untuk disalin", ErrInvalidData)
	}

	tujuan := make([]string, 0, len(in.ToBranchIDs))
	seen := map[string]bool{}
	for _, t := range in.ToBranchIDs {
		t = strings.TrimSpace(t)
		if t == "" || t == from || seen[t] {
			continue
		}
		seen[t] = true
		tujuan = append(tujuan, t)
	}
	if len(tujuan) == 0 {
		return CopyResult{}, fmt.Errorf("%w: tidak ada cabang tujuan yang berbeda dari cabang sumber", ErrInvalidData)
	}

	// Nilai sekarang setiap cabang tujuan dipakai untuk menghitung berapa sel
	// yang benar-benar berubah (dan tercermin di audit).
	hasil := CopyResult{FromBranchID: from, Jumlah: len(sumber)}
	recs := make([]audit.Entry, 0, len(tujuan))
	for _, t := range tujuan {
		lama, err := s.repo.ListPrices(ctx, PriceFilter{BranchID: t})
		if err != nil {
			return CopyResult{}, fmt.Errorf("baca harga cabang %s: %w", t, err)
		}
		lamaMap := make(map[string]PriceCell, len(lama))
		for _, c := range lama {
			lamaMap[c.ProductID] = c
		}
		berubah := 0
		for _, c := range sumber {
			t, err := TarifDariSel(c)
			if err != nil {
				return CopyResult{}, err
			}
			if v, ok := lamaMap[c.ProductID]; !ok || !v.SamaDengan(t) {
				berubah++
			}
		}
		// Sel di cabang tujuan yang tidak ada di sumber ikut terhapus.
		terhapus := 0
		for pid := range lamaMap {
			if !hasProduct(sumber, pid) {
				terhapus++
			}
		}
		after := map[string]any{"sumber": from, "jumlah": len(sumber), "diubah": berubah, "dihapus": terhapus}
		before := map[string]any{"jumlah": len(lama)}
		recs = append(recs, audit.NewEntry(ctx, actor, audit.EntityBranchPrice, t, audit.ActionCopy, before, after))
		hasil.Salinan = append(hasil.Salinan, CopiedBranch{BranchID: t, Jumlah: len(sumber), Diubah: berubah})
	}
	if err := s.repo.CopyPrices(ctx, from, tujuan, actor.UserID, recs); err != nil {
		return CopyResult{}, err
	}
	return hasil, nil
}

// ensureKodeFree menolak kode produk yang sudah dipakai produk lain. Kode
// kosong boleh berulang (kolom kode nullable).
func (s *Service) ensureKodeFree(ctx context.Context, kode, exceptID string) error {
	if kode == "" {
		return nil
	}
	taken, err := s.repo.ProductKodeTaken(ctx, kode, exceptID)
	if err != nil {
		return err
	}
	if taken {
		return ErrKodeTaken
	}
	return nil
}

// normProduct menyeragamkan masukan produk.
func normProduct(p Product) Product {
	p.Kode = strings.TrimSpace(p.Kode)
	p.Nama = strings.TrimSpace(p.Nama)
	p.Jenis = strings.ToLower(strings.TrimSpace(p.Jenis))
	p.Deskripsi = strings.TrimSpace(p.Deskripsi)
	if p.Jenis == "" {
		p.Jenis = JenisReguler
	}

	return p
}

// validateProduct memeriksa invarian produk yang tidak dijaga database.
func validateProduct(p Product) error {
	switch {
	case len([]rune(p.Nama)) < 2:
		return fmt.Errorf("%w: nama produk minimal 2 karakter", ErrInvalidData)
	case len([]rune(p.Nama)) > 160:
		return fmt.Errorf("%w: nama produk maksimal 160 karakter", ErrInvalidData)
	case !knownJenis(p.Jenis):
		return fmt.Errorf("%w: jenis produk harus %s atau %s", ErrInvalidData, JenisReguler, JenisSertifikat)

	case len([]rune(p.Kode)) > 40:
		return fmt.Errorf("%w: kode produk maksimal 40 karakter", ErrInvalidData)
	case len([]rune(p.Deskripsi)) > 500:
		return fmt.Errorf("%w: deskripsi maksimal 500 karakter", ErrInvalidData)
	case p.HargaDefault < 0:
		return fmt.Errorf("%w: harga default tidak boleh negatif", ErrInvalidData)
	case p.Urutan < 0:
		return fmt.Errorf("%w: urutan tidak boleh negatif", ErrInvalidData)
	case p.Jenis == JenisSertifikat && p.HargaDefault != 0:
		return fmt.Errorf("%w: produk Sertifikat selalu berharga 0", ErrInvalidData)
	}
	return nil
}

// knownJenis melaporkan apakah jenis produk dikenal sistem.
func knownJenis(j string) bool {
	return j == JenisReguler || j == JenisSertifikat
}

// hasProduct melaporkan apakah salah satu sel sumber memakai productID.
func hasProduct(cells []PriceCell, productID string) bool {
	for _, c := range cells {
		if c.ProductID == productID {
			return true
		}
	}
	return false
}
