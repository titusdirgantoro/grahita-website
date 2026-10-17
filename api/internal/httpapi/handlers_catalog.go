package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/branch"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
)

// catalogUnavailable adalah jawaban bila layanan katalog belum terpasang.
func catalogUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan katalog belum tersedia.")
}

// branchesUnavailable adalah jawaban bila layanan master cabang belum terpasang.
func branchesUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan master cabang belum tersedia.")
}

// catalog mengembalikan katalog sesuai sudut pandang aktor. Akun cabang hanya
// melihat produk yang punya baris harga untuk cabangnya (PRD CP-KAT-01/02).
//
// Produk Sertifikat ditandai catatan biaya packaging-nya (CP-KAT-04): nilai
// `biaya_packaging` yang sedang berlaku diambil dari domain pengaturan di sini,
// di lapisan transport, lalu ditempelkan ke produk Sertifikat. Portal cabang
// memakainya untuk menampilkan catatan dan menghitung total editor; angka
// final tetap dihitung server saat draft disimpan dan saat submit.
func (a *API) catalog(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return catalogUnavailable()
	}
	list, err := a.d.Catalog.Products(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return mapDomainErr(err)
	}
	// Hanya katalog portal cabang ([]BranchProduct) yang membawa catatan biaya
	// packaging; katalog admin memakai daftar produk master.
	rows, ok := list.([]catalog.BranchProduct)
	if !ok {
		httpx.OK(w, data(list))
		return nil
	}
	if a.d.Settings != nil {
		biaya, err := a.d.Settings.Int(r.Context(), settings.BiayaPackaging)
		if err != nil {
			return fmt.Errorf("baca biaya packaging: %w", err)
		}
		for i := range rows {
			if rows[i].Jenis == catalog.JenisSertifikat {
				rows[i].BiayaPackaging = biaya
			}
		}
	}
	httpx.OK(w, data(rows))
	return nil
}

// myBranch mengembalikan data cabang milik akun cabang yang login.
func (a *API) myBranch(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return catalogUnavailable()
	}
	actor := auth.ActorFrom(r.Context())
	if !actor.IsBranch() || actor.BranchID == "" {
		return apierr.Forbidden("Endpoint ini hanya untuk akun cabang.")
	}
	b, err := a.d.Catalog.Branch(r.Context(), actor, actor.BranchID)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(b))
	return nil
}

// ---------- Master produk (PRD §4.5 AP-KAT-01) ----------

// createProductRequest adalah body POST /api/v1/admin/products.
type createProductRequest struct {
	Kode  string `json:"kode" validate:"omitempty,max=40"`
	Nama  string `json:"nama" validate:"required,min=2,max=160"`
	Jenis string `json:"jenis" validate:"omitempty,oneof=reguler sertifikat"`

	Deskripsi    string `json:"deskripsi" validate:"omitempty,max=500"`
	HargaDefault *int64 `json:"harga_default" validate:"omitempty,gte=0"`
	Urutan       *int   `json:"urutan" validate:"omitempty,gte=0"`
	IsActive     *bool  `json:"is_active"`
}

// updateProductRequest adalah body PUT /api/v1/admin/products/{id}. Semua field
// opsional: yang dikirim saja yang diubah.
type updateProductRequest struct {
	Kode  *string `json:"kode" validate:"omitempty,max=40"`
	Nama  *string `json:"nama" validate:"omitempty,min=2,max=160"`
	Jenis *string `json:"jenis" validate:"omitempty,oneof=reguler sertifikat"`

	Deskripsi    *string `json:"deskripsi" validate:"omitempty,max=500"`
	HargaDefault *int64  `json:"harga_default" validate:"omitempty,gte=0"`
	Urutan       *int    `json:"urutan" validate:"omitempty,gte=0"`
	IsActive     *bool   `json:"is_active"`
}

// adminProducts mengembalikan daftar produk (termasuk nonaktif) beserta
// filternya: q (nama/kode), jenis, aktif.
func (a *API) adminProducts(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return catalogUnavailable()
	}
	f, err := productFilter(r)
	if err != nil {
		return err
	}
	list, err := a.d.Catalog.ListProducts(r.Context(), auth.ActorFrom(r.Context()), f)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}

// adminProduct mengambil satu produk.
func (a *API) adminProduct(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return catalogUnavailable()
	}
	p, err := a.d.Catalog.GetProduct(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(p))
	return nil
}

// adminProductCreate menambahkan produk baru (PRD AP-KAT-01).
func (a *API) adminProductCreate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return catalogUnavailable()
	}
	var req createProductRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	in := catalog.NewProduct{
		Kode:  req.Kode,
		Nama:  req.Nama,
		Jenis: req.Jenis,

		Deskripsi: req.Deskripsi,
		IsActive:  req.IsActive,
	}
	if req.HargaDefault != nil {
		in.HargaDefault = *req.HargaDefault
	}
	if req.Urutan != nil {
		in.Urutan = *req.Urutan
	}
	p, err := a.d.Catalog.CreateProduct(r.Context(), auth.ActorFrom(r.Context()), in)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.Created(w, data(p))
	return nil
}

// adminProductUpdate mengubah produk, termasuk mengaktifkan/menonaktifkannya.
func (a *API) adminProductUpdate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return catalogUnavailable()
	}
	var req updateProductRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	p, err := a.d.Catalog.UpdateProduct(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"), catalog.ProductPatch{
		Kode:  req.Kode,
		Nama:  req.Nama,
		Jenis: req.Jenis,

		Deskripsi:    req.Deskripsi,
		HargaDefault: req.HargaDefault,
		Urutan:       req.Urutan,
		IsActive:     req.IsActive,
	})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(p))
	return nil
}

// adminProductDelete menghapus produk yang belum dipakai pesanan; produk yang
// sudah dipakai hanya boleh dinonaktifkan lewat PUT (order_lines.product_id
// memakai ON DELETE RESTRICT).
func (a *API) adminProductDelete(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return catalogUnavailable()
	}
	if err := a.d.Catalog.DeleteProduct(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id")); err != nil {
		return mapDomainErr(err)
	}
	httpx.NoContent(w)
	return nil
}

// ---------- Matriks harga (PRD §4.5 AP-KAT-02..04) ----------

// matrixResponse adalah jawaban GET /api/v1/admin/prices: baris produk, kolom
// cabang, dan sel yang berisi harga. Sel tanpa harga berarti produk tidak
// dijual ke cabang itu (sel kosong di grid).
type matrixResponse struct {
	Produk []catalog.Product   `json:"produk"`
	Cabang []matrixBranch      `json:"cabang"`
	Sel    []catalog.PriceCell `json:"sel"`
}

// matrixBranch adalah satu kolom cabang pada matriks harga.
type matrixBranch struct {
	ID       string `json:"id"`
	Nama     string `json:"nama"`
	IsActive bool   `json:"is_active"`
}

// matrixResponseDari menyusun jawaban matriks.
func matrixResponseDari(products []catalog.Product, branches []matrixBranch, sel []catalog.PriceCell) matrixResponse {
	if products == nil {
		products = []catalog.Product{}
	}
	if branches == nil {
		branches = []matrixBranch{}
	}
	if sel == nil {
		sel = []catalog.PriceCell{}
	}
	return matrixResponse{Produk: products, Cabang: branches, Sel: sel}
}

// adminPrices membalas matriks harga Produk × Cabang (PRD AP-KAT-02).
//
// Filter: q (nama/kode produk), jenis, aktif (produk), branch_q (nama cabang),
// branch_id (batasi sel ke satu cabang).
func (a *API) adminPrices(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return catalogUnavailable()
	}
	if a.d.Branches == nil {
		return branchesUnavailable()
	}
	actor := auth.ActorFrom(r.Context())
	f, err := productFilter(r)
	if err != nil {
		return err
	}
	products, err := a.d.Catalog.ListProducts(r.Context(), actor, f)
	if err != nil {
		return mapDomainErr(err)
	}
	branches, err := a.d.Branches.List(r.Context(), actor, branch.Filter{Q: strings.TrimSpace(r.URL.Query().Get("branch_q"))})
	if err != nil {
		return mapDomainErr(err)
	}
	kolom := make([]matrixBranch, 0, len(branches))
	for _, b := range branches {
		kolom = append(kolom, matrixBranch{ID: b.ID, Nama: b.Nama, IsActive: b.IsActive})
	}
	sel, err := a.d.Catalog.Prices(r.Context(), actor, catalog.PriceFilter{
		BranchID: strings.TrimSpace(r.URL.Query().Get("branch_id")),
	})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(matrixResponseDari(products, kolom, sel)))
	return nil
}

// setPricesRequest adalah body PUT /api/v1/admin/prices (batch edit).
type setPricesRequest struct {
	Items []priceItemRequest `json:"items" validate:"required,min=1,dive"`
}

// priceItemRequest adalah satu sel matriks yang diubah, berupa komponen tarif.
// Harga jual tidak diterima dari klien: server menghitungnya dari HJM, P1, dan
// SPV. HJM null berarti sel dikosongkan — produk tidak dijual ke cabang itu
// (PRD AP-KAT-03), sama seperti sel HJM kosong pada workbook Impact.
type priceItemRequest struct {
	BranchID  string `json:"branch_id" validate:"required"`
	ProductID string `json:"product_id" validate:"required"`
	HJM       *int64 `json:"hjm" validate:"omitempty,gte=0"`
	// P1 adalah rasio desimal 0–1 sebagai teks ("0.1" = 10%); kosong = 0.
	P1  string `json:"p1" validate:"max=20"`
	SPV *int64 `json:"spv" validate:"omitempty,gte=0"`
}

// adminPricesUpdate menerapkan batch edit matriks harga (PRD AP-KAT-02/03/04).
func (a *API) adminPricesUpdate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return catalogUnavailable()
	}
	var req setPricesRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	actor := auth.ActorFrom(r.Context())

	items := make([]catalog.PriceInput, 0, len(req.Items))
	ids := make([]string, 0, len(req.Items))
	for _, it := range req.Items {
		in := catalog.PriceInput{
			BranchID:  strings.TrimSpace(it.BranchID),
			ProductID: strings.TrimSpace(it.ProductID),
		}
		if it.HJM != nil {
			k := catalog.Komponen{HJM: *it.HJM, P1: strings.TrimSpace(it.P1)}
			if it.SPV != nil {
				k.SPV = *it.SPV
			}
			in.Komponen = &k
		}
		items = append(items, in)
		ids = append(ids, it.BranchID)
	}
	// Cabang master dimiliki domain internal/branch, jadi keberadaannya
	// diperiksa di sini (katalog tidak membaca tabel cabang).
	if err := a.ensureBranchesExist(r, actor, ids); err != nil {
		return err
	}
	hasil, err := a.d.Catalog.SetPrices(r.Context(), actor, items)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(hasil))
	return nil
}

// copyPricesRequest adalah body POST /api/v1/admin/prices/copy.
type copyPricesRequest struct {
	FromBranchID string   `json:"from_branch_id" validate:"required"`
	ToBranchIDs  []string `json:"to_branch_ids"`
	// Semua true berarti salin ke seluruh cabang lain (PRD AP-KAT-04).
	Semua bool `json:"semua"`
}

// adminPricesCopy menyalin matriks harga satu cabang ke cabang lain, atau ke
// seluruh cabang lain (PRD AP-KAT-04).
func (a *API) adminPricesCopy(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return catalogUnavailable()
	}
	var req copyPricesRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	actor := auth.ActorFrom(r.Context())
	from := strings.TrimSpace(req.FromBranchID)

	tujuan := req.ToBranchIDs
	if req.Semua {
		if a.d.Branches == nil {
			return branchesUnavailable()
		}
		rows, err := a.d.Branches.List(r.Context(), actor, branch.Filter{})
		if err != nil {
			return mapDomainErr(err)
		}
		tujuan = make([]string, 0, len(rows))
		for _, b := range rows {
			tujuan = append(tujuan, b.ID)
		}
	}
	if err := a.ensureBranchesExist(r, actor, append([]string{from}, tujuan...)); err != nil {
		return err
	}
	hasil, err := a.d.Catalog.CopyPrices(r.Context(), actor, catalog.CopyInput{
		FromBranchID: from,
		ToBranchIDs:  tujuan,
	})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(hasil))
	return nil
}

// ensureBranchesExist memastikan setiap id cabang yang disebut benar-benar ada.
//
// Daftar cabang diambil dari layanan master cabang (internal/branch) memakai
// aktor yang sama, sehingga otorisasi & filternya tetap satu sumber.
func (a *API) ensureBranchesExist(r *http.Request, actor auth.Actor, ids []string) error {
	butuh := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			butuh[id] = true
		}
	}
	if len(butuh) == 0 {
		return nil
	}
	if a.d.Branches == nil {
		return branchesUnavailable()
	}
	rows, err := a.d.Branches.List(r.Context(), actor, branch.Filter{})
	if err != nil {
		return mapDomainErr(err)
	}
	ada := make(map[string]bool, len(rows))
	for _, b := range rows {
		ada[b.ID] = true
	}
	for id := range butuh {
		if !ada[id] {
			return apierr.Validation("Cabang yang diminta tidak ditemukan.",
				map[string]string{"branch_id": "cabang tidak dikenal: " + id})
		}
	}
	return nil
}

// productFilter menyusun penyaring daftar produk dari query string:
// q, jenis, aktif.
func productFilter(r *http.Request) (catalog.ProductFilter, error) {
	q := r.URL.Query()
	f := catalog.ProductFilter{
		Q:     strings.TrimSpace(q.Get("q")),
		Jenis: strings.ToLower(strings.TrimSpace(q.Get("jenis"))),
	}
	aktif, err := boolQuery(r, "aktif")
	if err != nil {
		return f, err
	}
	f.Active = aktif
	return f, nil
}
