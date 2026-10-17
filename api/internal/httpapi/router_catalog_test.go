package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/branch"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
)

// catalogServer adalah router lengkap untuk menguji master produk, matriks
// harga, dan katalog cabang lewat HTTP: penyimpanan auth, katalog, dan master
// cabang semuanya in-memory (katalog & cabang sekaligus merekam audit).
type catalogServer struct {
	srv *httptest.Server
	cat *catalog.Memory
	br  *branch.Memory
}

func newCatalogServer(t *testing.T) *catalogServer {
	t.Helper()

	hash, err := auth.HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}
	store := auth.NewMemory()
	store.PutBranch(auth.Credentials{
		UserID: "usr-a", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-a",
		Nama: "Grahita Cabang A", Email: "a@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	store.PutBranch(auth.Credentials{
		UserID: "usr-b", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-b",
		Nama: "Grahita Cabang B", Email: "b@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	store.PutAdmin(auth.Credentials{
		UserID: "adm-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager,
		Nama: "Budi", Email: "budi@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	store.PutAdmin(auth.Credentials{
		UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin,
		Nama: "Rina", Email: "rina@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})

	// Cabang: dua aktif (br-a, br-b) dan satu nonaktif (br-c) supaya kolom
	// matriks serta filter pencarian cabang bisa diuji.
	brRepo := branch.NewMemory()
	brRepo.Put(branch.Branch{ID: "br-a", Nama: "Grahita Cabang A", NamaKepala: "Kepala A", Email: "a@example.test", IsActive: true})
	brRepo.Put(branch.Branch{ID: "br-b", Nama: "Grahita Cabang B", NamaKepala: "Kepala B", Email: "b@example.test", IsActive: true})
	brRepo.Put(branch.Branch{ID: "br-c", Nama: "Grahita Cabang C", NamaKepala: "Kepala C", Email: "c@example.test", IsActive: false})

	catRepo := catalog.NewMemory()
	catRepo.PutProduct(catalog.Product{ID: "p1", Kode: "P-01", Nama: "Bio Magnetik", Jenis: catalog.JenisReguler, HargaDefault: 350000, Urutan: 1, IsActive: true})
	catRepo.PutProduct(catalog.Product{ID: "p2", Kode: "P-02", Nama: "Sertifikat", Jenis: catalog.JenisSertifikat, HargaDefault: 0, Urutan: 2, IsActive: true})
	catRepo.PutProduct(catalog.Product{ID: "p3", Kode: "P-03", Nama: "Produk Lama", Jenis: catalog.JenisReguler, HargaDefault: 10000, Urutan: 3, IsActive: false})
	// p1 punya harga di dua cabang (berbeda) dan TIDAK punya harga di br-c;
	// p3 sengaja tidak punya harga di cabang mana pun.
	catRepo.PutPrice("br-a", "p1", 100000)
	catRepo.PutPrice("br-b", "p1", 200000)

	svc := auth.NewService(store, store, store, clock.System(), 8*time.Hour, 30*24*time.Hour)
	deps := Deps{
		Config:        testConfig(),
		Version:       "test-catalog",
		Health:        fakeHealth{migrasi: 9},
		Auth:          svc,
		Settings:      settings.NewService(&fakeSettingsRepo{values: map[string]string{"biaya_packaging": "3000"}}),
		Catalog:       catalog.NewService(catRepo),
		Branches:      branch.NewService(brRepo, clock.System()),
		LoginLimiter:  httpx.NewRateLimiter(50, 15*time.Minute),
		ForgotLimiter: httpx.NewRateLimiter(50, 15*time.Minute),
	}
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	return &catalogServer{srv: srv, cat: catRepo, br: brRepo}
}

// dataList mengambil array `data` dari jawaban JSON.
func dataList(t *testing.T, body map[string]any) []any {
	t.Helper()
	raw, ok := body["data"].([]any)
	if !ok {
		t.Fatalf("jawaban tidak punya array data: %v", body)
	}
	return raw
}

// dataMap mengambil objek `data` dari jawaban JSON.
func dataMap(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	raw, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("jawaban tidak punya objek data: %v", body)
	}
	return raw
}

func TestApiProdukCRUDDanFilter(t *testing.T) {
	cs := newCatalogServer(t)
	cookies, csrf := login(t, cs.srv, "budi@example.test")

	// Daftar produk admin memuat produk nonaktif juga (PRD AP-KAT-01).
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/admin/products", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("daftar produk harus 200, dapat %d (%v)", status, body)
	}
	if got := len(dataList(t, body)); got != 3 {
		t.Fatalf("harus 3 produk (termasuk nonaktif), dapat %d", got)
	}

	// Filter aktif + jenis + q.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/admin/products?aktif=true&jenis=sertifikat", "", cookies, "")
	if status != http.StatusOK || len(dataList(t, body)) != 1 {
		t.Fatalf("filter aktif+jenis salah: %d %v", status, body)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/admin/products?q=magnetik", "", cookies, "")
	if status != http.StatusOK || len(dataList(t, body)) != 1 {
		t.Fatalf("filter q salah: %d %v", status, body)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/admin/products?aktif=mungkin", "", cookies, "")
	if status != http.StatusBadRequest {
		t.Fatalf("filter boolean tidak valid harus 400, dapat %d", status)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")

	// Mutasi tanpa CSRF ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, cs.srv.URL+"/api/v1/admin/products",
		`{"nama":"Produk Baru","harga_default":5000}`, cookies, "")
	if status != http.StatusForbidden {
		t.Fatalf("POST tanpa CSRF harus 403, dapat %d", status)
	}
	assertErrorShape(t, body, "CSRF_INVALID")

	// Buat produk baru.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, cs.srv.URL+"/api/v1/admin/products",
		`{"kode":"P-99","nama":"Produk Baru","jenis":"reguler","harga_default":5000,"urutan":9}`, cookies, csrf)
	if status != http.StatusCreated {
		t.Fatalf("buat produk harus 201, dapat %d (%v)", status, body)
	}
	baru := dataMap(t, body)
	if baru["nama"] != "Produk Baru" || baru["is_active"] != true {
		t.Fatalf("produk baru tidak sesuai: %v", baru)
	}
	newID, _ := baru["id"].(string)

	// Kode duplikat ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, cs.srv.URL+"/api/v1/admin/products",
		`{"kode":"P-99","nama":"Duplikat"}`, cookies, csrf)
	if status != http.StatusConflict {
		t.Fatalf("kode duplikat harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "KODE_TAKEN")

	// Sertifikat berharga selain 0 ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, cs.srv.URL+"/api/v1/admin/products",
		`{"nama":"Sertifikat Mahal","jenis":"sertifikat","harga_default":1000}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("sertifikat berharga harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")

	// Baca satu produk, termasuk yang tidak ada.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/admin/products/"+newID, "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("baca produk harus 200, dapat %d", status)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/admin/products/tidak-ada", "", cookies, "")
	if status != http.StatusNotFound {
		t.Fatalf("produk tidak ada harus 404, dapat %d (%v)", status, body)
	}

	// Ubah produk (nonaktifkan).
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, cs.srv.URL+"/api/v1/admin/products/"+newID,
		`{"nama":"Produk Baru v2","is_active":false}`, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("ubah produk harus 200, dapat %d (%v)", status, body)
	}
	if ubah := dataMap(t, body); ubah["nama"] != "Produk Baru v2" || ubah["is_active"] != false {
		t.Fatalf("hasil ubah tidak sesuai: %v", ubah)
	}

	// Hapus produk yang belum dipakai pesanan.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodDelete, cs.srv.URL+"/api/v1/admin/products/"+newID, "", cookies, csrf)
	if status != http.StatusNoContent {
		t.Fatalf("hapus produk harus 204, dapat %d", status)
	}
	// Produk yang sudah dipakai order line hanya boleh dinonaktifkan.
	cs.cat.PutUsed("p1")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodDelete, cs.srv.URL+"/api/v1/admin/products/p1", "", cookies, csrf)
	if status != http.StatusConflict {
		t.Fatalf("hapus produk terpakai harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "PRODUCT_IN_USE")
}

func TestApiProdukTolakAkunCabang(t *testing.T) {
	cs := newCatalogServer(t)
	cookies, csrf := login(t, cs.srv, "a@example.test")

	kasus := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/admin/products", ""},
		{http.MethodPost, "/api/v1/admin/products", `{"nama":"X"}`},
		{http.MethodPut, "/api/v1/admin/products/p1", `{"nama":"X"}`},
		{http.MethodDelete, "/api/v1/admin/products/p1", ""},
		{http.MethodGet, "/api/v1/admin/prices", ""},
		{http.MethodPut, "/api/v1/admin/prices", `{"items":[{"branch_id":"br-a","product_id":"p1","hjm":1}]}`},
		{http.MethodPost, "/api/v1/admin/prices/copy", `{"from_branch_id":"br-a","semua":true}`},
	}
	for _, k := range kasus {
		status, body, _ := apiReq(t, http.DefaultClient, k.method, cs.srv.URL+k.path, k.body, cookies, csrf)
		if status != http.StatusForbidden {
			t.Errorf("akun cabang %s %s harus 403, dapat %d (%v)", k.method, k.path, status, body)
		}
	}
}

func TestApiMatriksHargaBatchDanFilter(t *testing.T) {
	cs := newCatalogServer(t)
	cookies, csrf := login(t, cs.srv, "budi@example.test")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/admin/prices", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("matriks harga harus 200, dapat %d (%v)", status, body)
	}
	matriks := dataMap(t, body)
	if got := len(matriks["produk"].([]any)); got != 3 {
		t.Fatalf("matriks harus memuat 3 baris produk, dapat %d", got)
	}
	if got := len(matriks["cabang"].([]any)); got != 3 {
		t.Fatalf("matriks harus memuat 3 kolom cabang, dapat %d", got)
	}
	// Hanya sel yang berisi yang dikirim: p1 punya harga di br-a & br-b.
	if got := len(matriks["sel"].([]any)); got != 2 {
		t.Fatalf("harus 2 sel berisi, dapat %d (%v)", got, matriks["sel"])
	}

	// Pencarian cabang memfilter kolom.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/admin/prices?branch_q=cabang+b", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("filter cabang harus 200, dapat %d", status)
	}
	if got := len(dataMap(t, body)["cabang"].([]any)); got != 1 {
		t.Fatalf("filter branch_q harus menyisakan 1 kolom, dapat %d", got)
	}

	// Filter produk pada matriks.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/admin/prices?aktif=true&q=magnetik", "", cookies, "")
	if status != http.StatusOK || len(dataMap(t, body)["produk"].([]any)) != 1 {
		t.Fatalf("filter produk pada matriks salah: %d %v", status, body)
	}

	// Batch edit berkomponen: ubah p1 di br-b dan isi sel baru p3 untuk br-a.
	// Harga jual dihitung server: 150.000 + 10% + 2.500 = 167.500.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, cs.srv.URL+"/api/v1/admin/prices",
		`{"items":[{"branch_id":"br-b","product_id":"p1","hjm":150000,"p1":"0.1","spv":2500},{"branch_id":"br-a","product_id":"p3","hjm":9000}]}`, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("batch edit harus 200, dapat %d (%v)", status, body)
	}
	hasil := dataMap(t, body)
	if hasil["diubah"] != float64(2) || hasil["diabaikan"] != float64(0) {
		t.Fatalf("harus 2 sel berubah (1 diubah + 1 baru), dapat %v", hasil)
	}
	if v, ok := cs.cat.PriceOf("br-b", "p1"); !ok || v != 167500 {
		t.Fatalf("harga br-b tidak tersimpan: %d %v", v, ok)
	}
	if v, ok := cs.cat.PriceOf("br-a", "p3"); !ok || v != 9000 {
		t.Fatalf("sel baru p3/br-a tidak tersimpan: %d %v", v, ok)
	}

	// Kosongkan sel = produk tidak dijual lagi ke cabang itu.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, cs.srv.URL+"/api/v1/admin/prices",
		`{"items":[{"branch_id":"br-a","product_id":"p3","hjm":null}]}`, cookies, csrf)
	if status != http.StatusOK || dataMap(t, body)["dihapus"] != float64(1) {
		t.Fatalf("kosongkan sel harus 1 terhapus: %d %v", status, body)
	}
	if _, ok := cs.cat.PriceOf("br-a", "p3"); ok {
		t.Fatal("sel yang dikosongkan harus hilang")
	}

	// Cabang tidak dikenal ditolak sebelum menyentuh harga.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, cs.srv.URL+"/api/v1/admin/prices",
		`{"items":[{"branch_id":"br-hantu","product_id":"p1","hjm":1000}]}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("cabang tidak dikenal harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")

	// Harga Sertifikat selain 0 ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, cs.srv.URL+"/api/v1/admin/prices",
		`{"items":[{"branch_id":"br-a","product_id":"p2","hjm":5000}]}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("harga sertifikat harus 400, dapat %d (%v)", status, body)
	}

	// HJM negatif ditolak validasi transport.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, cs.srv.URL+"/api/v1/admin/prices",
		`{"items":[{"branch_id":"br-a","product_id":"p1","hjm":-1}]}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("HJM negatif harus 400, dapat %d (%v)", status, body)
	}

	// Harga jual tidak bisa dikirim langsung: selalu dihitung dari komponen.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, cs.srv.URL+"/api/v1/admin/prices",
		`{"items":[{"branch_id":"br-a","product_id":"p1","harga":1000}]}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("field harga harus ditolak, dapat %d (%v)", status, body)
	}
}

func TestApiSalinHargaAntarCabang(t *testing.T) {
	cs := newCatalogServer(t)
	cookies, csrf := login(t, cs.srv, "budi@example.test")

	// Salin harga cabang A ke seluruh cabang lain (br-b & br-c). Matriks br-c
	// sebelumnya kosong, jadi aksi ini juga membuktikan cabang tanpa harga
	// tidak menghalangi salin.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, cs.srv.URL+"/api/v1/admin/prices/copy",
		`{"from_branch_id":"br-a","semua":true}`, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("salin harga harus 200, dapat %d (%v)", status, body)
	}
	hasil := dataMap(t, body)
	if hasil["jumlah"] != float64(1) || len(hasil["salinan"].([]any)) != 2 {
		t.Fatalf("ringkasan salin tidak sesuai: %v", hasil)
	}
	if v, ok := cs.cat.PriceOf("br-b", "p1"); !ok || v != 100000 {
		t.Fatalf("br-b harus ikut harga cabang sumber: %d %v", v, ok)
	}
	if v, ok := cs.cat.PriceOf("br-c", "p1"); !ok || v != 100000 {
		t.Fatalf("br-c harus ikut harga cabang sumber: %d %v", v, ok)
	}

	// Daftar tujuan eksplisit.
	cs.cat.PutPrice("br-c", "p1", 777)
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, cs.srv.URL+"/api/v1/admin/prices/copy",
		`{"from_branch_id":"br-a","to_branch_ids":["br-c"]}`, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("salin ke cabang tertentu harus 200, dapat %d", status)
	}
	if v, _ := cs.cat.PriceOf("br-c", "p1"); v != 100000 {
		t.Fatalf("br-c harus kembali ke harga sumber, dapat %d", v)
	}

	// Cabang sumber kosong ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, cs.srv.URL+"/api/v1/admin/prices/copy",
		`{"from_branch_id":"br-a","to_branch_ids":["br-a"]}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("tujuan sama dengan sumber harus 400, dapat %d (%v)", status, body)
	}
}

// TestApiKatalogCabangHanyaHargaSendiri memverifikasi kriteria kartu: dua
// cabang melihat harga berbeda, dan produk tanpa harga cabang tidak tampil
// (PRD CP-KAT-01/02).
func TestApiKatalogCabangHanyaHargaSendiri(t *testing.T) {
	cs := newCatalogServer(t)

	cookiesA, _ := login(t, cs.srv, "a@example.test")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/catalog", "", cookiesA, "")
	if status != http.StatusOK {
		t.Fatalf("katalog cabang A harus 200, dapat %d", status)
	}
	listA := dataList(t, body)
	if len(listA) != 1 {
		t.Fatalf("cabang A hanya menjual p1, dapat %v", listA)
	}
	if item := listA[0].(map[string]any); item["harga"] != float64(100000) {
		t.Fatalf("cabang A harus melihat harganya sendiri, dapat %v", item["harga"])
	}

	cookiesB, _ := login(t, cs.srv, "b@example.test")
	_, bodyB, _ := apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/catalog", "", cookiesB, "")
	listB := dataList(t, bodyB)
	if len(listB) != 1 || listB[0].(map[string]any)["harga"] != float64(200000) {
		t.Fatalf("cabang B harus melihat harga berbeda, dapat %v", listB)
	}

	// Setelah harga cabang A diubah, katalog cabang A ikut berubah — tetapi
	// pesanan yang sudah ada tidak tersentuh (diuji di level PostgreSQL).
	cookiesMgr, csrfMgr := login(t, cs.srv, "budi@example.test")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, cs.srv.URL+"/api/v1/admin/prices",
		`{"items":[{"branch_id":"br-a","product_id":"p1","hjm":111000}]}`, cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("ubah harga harus 200, dapat %d", status)
	}
	_, bodyA2, _ := apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/catalog", "", cookiesA, "")
	if got := dataList(t, bodyA2)[0].(map[string]any)["harga"]; got != float64(111000) {
		t.Fatalf("katalog cabang A harus memakai harga baru, dapat %v", got)
	}
	_, bodyB2, _ := apiReq(t, http.DefaultClient, http.MethodGet, cs.srv.URL+"/api/v1/catalog", "", cookiesB, "")
	if got := dataList(t, bodyB2)[0].(map[string]any)["harga"]; got != float64(200000) {
		t.Fatalf("katalog cabang B tidak boleh ikut berubah, dapat %v", got)
	}
}
