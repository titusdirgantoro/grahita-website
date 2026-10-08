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
//   - Perubahan harga TIDAK menyentuh pesanan yang sudah ada: harga pada order
//     line adalah snapshot (PRD AP-KAT-05, BR-05). Paket ini hanya menulis
//     branch_prices, tidak pernah order_lines.
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
	"strings"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
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
	// ErrProductUsed dikembalikan bila produk sudah dipakai order line sehingga
	// tidak boleh dihapus.
	ErrProductUsed = errors.New("produk sudah dipakai pesanan")
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
	Satuan       string `json:"satuan"`
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
	Satuan    string `json:"satuan"`
	Deskripsi string `json:"deskripsi,omitempty"`
	Harga     int64  `json:"harga"`
	Urutan    int    `json:"urutan"`
}

// Branch adalah data cabang (master data) untuk /me/branch portal cabang.
type Branch struct {
	ID         string `json:"id"`
	Nama       string `json:"nama"`
	NamaKepala string `json:"nama_kepala"`
	Email      string `json:"email"`
	Telepon    string `json:"telepon,omitempty"`
	IsCNC      bool   `json:"is_cnc"`
	IsSpecial  bool   `json:"is_special"`
	PajakIn    bool   `json:"pajak_in"`
	IsActive   bool   `json:"is_active"`
}

// PriceCell adalah satu sel matriks harga: harga produk untuk satu cabang.
// Sel yang tidak dijual tidak punya PriceCell sama sekali.
type PriceCell struct {
	BranchID  string `json:"branch_id"`
	ProductID string `json:"product_id"`
	Harga     int64  `json:"harga"`
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
	Satuan       string
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
	Satuan       *string
	Deskripsi    *string
	HargaDefault *int64
	Urutan       *int
	IsActive     *bool
}

// PriceInput adalah satu sel yang dikirim pada batch edit matriks harga.
// Harga nil berarti sel dikosongkan (produk tidak dijual ke cabang itu).
type PriceInput struct {
	BranchID  string
	ProductID string
	Harga     *int64
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
	// Harga nil berarti baris dihapus (sel dikosongkan).
	Harga *int64
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
	// ApplyPriceChanges menerapkan seluruh perubahan sel dan entri auditnya
	// dalam SATU transaksi.
	ApplyPriceChanges(ctx context.Context, changes []PriceChange, actorID string, recs []audit.Entry) error
	// CopyPrices menggantikan seluruh matriks harga setiap cabang tujuan
	// dengan matriks cabang sumber, beserta satu entri audit per cabang.
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
		Satuan:       in.Satuan,
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
	if p.Satuan != nil {
		next.Satuan = strings.TrimSpace(*p.Satuan)
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
// Aturan: harga tidak boleh negatif; produk Sertifikat selalu 0; sel dengan
// harga sama dengan nilai sekarang diabaikan (tidak ada audit berisik); sel
// yang dikosongkan membuat baris terhapus sehingga produk tidak lagi dijual ke
// cabang itu.
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

	// Produk dimuat sekali per id supaya harga Sertifikat bisa dipaksa 0 dan
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
	ada := make(map[[2]string]int64, len(sekarang))
	for _, c := range sekarang {
		ada[[2]string{c.BranchID, c.ProductID}] = c.Harga
	}

	var (
		changes []PriceChange
		recs    []audit.Entry
		res     PriceResult
	)
	for _, it := range items {
		p := products[it.ProductID]
		if it.Harga != nil {
			if *it.Harga < 0 {
				return PriceResult{}, fmt.Errorf("%w: harga tidak boleh negatif", ErrInvalidData)
			}
			if p.Jenis == JenisSertifikat && *it.Harga != 0 {
				return PriceResult{}, fmt.Errorf("%w: produk Sertifikat selalu berharga 0", ErrInvalidData)
			}
		}
		key := [2]string{it.BranchID, it.ProductID}
		lama, punyaBaris := ada[key]

		switch {
		case it.Harga == nil:
			if !punyaBaris {
				res.Diabaikan++
				continue
			}
			changes = append(changes, PriceChange{BranchID: it.BranchID, ProductID: it.ProductID})
			recs = append(recs, audit.NewEntry(ctx, actor, audit.EntityBranchPrice, it.ProductID, audit.ActionDelete,
				map[string]any{"branch_id": it.BranchID, "harga": lama}, nil))
			res.Dihapus++
		case punyaBaris && lama == *it.Harga:
			res.Diabaikan++
		default:
			harga := *it.Harga
			changes = append(changes, PriceChange{BranchID: it.BranchID, ProductID: it.ProductID, Harga: &harga})
			aksi := audit.ActionCreate
			var before any
			if punyaBaris {
				aksi = audit.ActionUpdate
				before = map[string]any{"branch_id": it.BranchID, "harga": lama}
			}
			recs = append(recs, audit.NewEntry(ctx, actor, audit.EntityBranchPrice, it.ProductID, aksi,
				before, map[string]any{"branch_id": it.BranchID, "harga": harga}))
			res.Diubah++
		}
	}
	if len(changes) == 0 {
		return res, nil
	}
	if err := s.repo.ApplyPriceChanges(ctx, changes, actor.UserID, recs); err != nil {
		return PriceResult{}, err
	}
	return res, nil
}

// CopyPrices menggantikan matriks harga setiap cabang tujuan dengan matriks
// cabang sumber (PRD AP-KAT-04). Bersifat replace: sel yang tidak ada di
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
		lamaMap := make(map[string]int64, len(lama))
		for _, c := range lama {
			lamaMap[c.ProductID] = c.Harga
		}
		berubah := 0
		for _, c := range sumber {
			if v, ok := lamaMap[c.ProductID]; !ok || v != c.Harga {
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
	p.Satuan = strings.TrimSpace(p.Satuan)
	p.Deskripsi = strings.TrimSpace(p.Deskripsi)
	if p.Jenis == "" {
		p.Jenis = JenisReguler
	}
	if p.Satuan == "" {
		p.Satuan = "pcs"
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
	case len([]rune(p.Satuan)) < 1 || len([]rune(p.Satuan)) > 40:
		return fmt.Errorf("%w: satuan wajib 1–40 karakter", ErrInvalidData)
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
