package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
)

// ordersServer adalah router lengkap untuk menguji draft, editor order line,
// dan daftar/detail pesanan cabang lewat HTTP: penyimpanan auth, katalog, dan
// pesanan semuanya in-memory.
type ordersServer struct {
	srv *httptest.Server
	cat *catalog.Memory
	ord *order.Memory
}

const (
	// id pesanan uji berbentuk UUID supaya melewati pemeriksaan format id.
	orderSubmittedID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	orderCabangBID   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func newOrdersServer(t *testing.T) *ordersServer {
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

	catRepo := catalog.NewMemory()
	catRepo.PutProduct(catalog.Product{ID: "p1", Kode: "P-01", Nama: "Bio Magnetik", Jenis: catalog.JenisReguler, Satuan: "pcs", HargaDefault: 350000, Urutan: 1, IsActive: true})
	catRepo.PutProduct(catalog.Product{ID: "p2", Kode: "P-02", Nama: "Tanpa Harga Cabang", Jenis: catalog.JenisReguler, Satuan: "pcs", HargaDefault: 999000, Urutan: 2, IsActive: true})
	catRepo.PutProduct(catalog.Product{ID: "p3", Kode: "P-03", Nama: "Sertifikat", Jenis: catalog.JenisSertifikat, Satuan: "pcs", HargaDefault: 0, Urutan: 3, IsActive: true})
	// Harga berbeda per cabang; p2 sengaja tanpa baris harga di cabang mana pun.
	catRepo.PutPrice("br-a", "p1", 100000)
	catRepo.PutPrice("br-b", "p1", 200000)
	catRepo.PutPrice("br-a", "p3", 0)

	ordRepo := order.NewMemory()
	// Pesanan yang sudah dikirim (milik cabang A) dan draft milik cabang B.
	ordRepo.Put(order.Order{
		ID: orderSubmittedID, BranchID: "br-a", Status: order.StatusSubmitted,
		Nomor: "2026-10-01", Tanggal: "2026-10-08", VerifStatus: "belum",
		Lines: []order.Line{{LineNo: 1, Jenis: order.JenisProduct, ProductID: "p1", Nama: "Bio Magnetik", Satuan: "pcs", Qty: 2, HargaSatuan: 100000}},
	})
	ordRepo.Put(order.Order{
		ID: orderCabangBID, BranchID: "br-b", Status: order.StatusDraft,
		Lines: []order.Line{{LineNo: 1, Jenis: order.JenisProduct, ProductID: "p1", Nama: "Bio Magnetik", Satuan: "pcs", Qty: 1, HargaSatuan: 200000}},
	})

	svc := auth.NewService(store, store, store, clock.System(), 8*time.Hour, 30*24*time.Hour)
	deps := Deps{
		Config:       testConfig(),
		Version:      "test-orders",
		Health:       fakeHealth{migrasi: 11},
		Auth:         svc,
		Settings:     settings.NewService(&fakeSettingsRepo{values: map[string]string{"biaya_packaging": "3000"}}),
		Catalog:      catalog.NewService(catRepo),
		Orders:       order.NewService(ordRepo, catalog.NewService(catRepo)),
		LoginLimiter: httpx.NewRateLimiter(50, 15*time.Minute),
	}
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	return &ordersServer{srv: srv, cat: catRepo, ord: ordRepo}
}

// TestApiDraftCRUDDanTotalServer menempuh siklus penuh draft: buat, daftar,
// detail, ubah, hapus — dengan harga cabang dan total hasil server.
func TestApiDraftCRUDDanTotalServer(t *testing.T) {
	os := newOrdersServer(t)
	cookies, csrf := login(t, os.srv, "a@example.test")

	// Tanpa CSRF ditolak.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, os.srv.URL+"/api/v1/orders",
		`{"lines":[{"product_id":"p1","qty":3}]}`, cookies, "")
	if status != http.StatusForbidden {
		t.Fatalf("POST tanpa CSRF harus 403, dapat %d", status)
	}
	assertErrorShape(t, body, "CSRF_INVALID")

	// Campuran Sertifikat + reguler ditolak (PRD CP-SER-03) sehingga tidak
	// pernah berakhir sebagai pelanggaran trigger database.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, os.srv.URL+"/api/v1/orders",
		`{"lines":[{"product_id":"p1","qty":3},{"product_id":"p3","qty":5}]}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("campuran sertifikat+reguler harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")

	// Buat draft: hanya product_id & qty yang dikirim.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, os.srv.URL+"/api/v1/orders",
		`{"catatan":"dikirim pagi","lines":[{"product_id":"p1","qty":3}]}`, cookies, csrf)
	if status != http.StatusCreated {
		t.Fatalf("buat draft harus 201, dapat %d (%v)", status, body)
	}
	draft := dataMap(t, body)
	if draft["status"] != "draft" {
		t.Errorf("status draft, dapat %v", draft["status"])
	}
	if _, ada := draft["nomor"]; ada {
		t.Errorf("draft tidak boleh punya nomor: %v", draft["nomor"])
	}
	if _, ada := draft["tanggal"]; ada {
		t.Errorf("draft tidak boleh punya tanggal: %v", draft["tanggal"])
	}
	// Harga baris datang dari branch_prices cabang sesi (100000), bukan
	// harga_default (350000), dan total dihitung server.
	if draft["total"] != float64(300000) {
		t.Errorf("total harus 3×100000 = 300000, dapat %v", draft["total"])
	}
	if draft["jumlah_item"] != float64(3) {
		t.Errorf("jumlah item harus 3, dapat %v", draft["jumlah_item"])
	}
	lines, _ := draft["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("harus 1 baris, dapat %v", draft["lines"])
	}
	baris := lines[0].(map[string]any)
	if baris["harga_satuan"] != float64(100000) || baris["subtotal"] != float64(300000) {
		t.Errorf("baris tidak memakai harga cabang: %v", baris)
	}
	if _, ada := baris["serial_number"]; ada {
		t.Errorf("draft tidak boleh punya serial number: %v", baris["serial_number"])
	}
	id, _ := draft["id"].(string)

	// Daftar pesanan cabang: draft + pesanan submitted milik cabang A.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, os.srv.URL+"/api/v1/orders", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("daftar pesanan harus 200, dapat %d", status)
	}
	daftar := dataList(t, body)
	if len(daftar) != 2 {
		t.Fatalf("cabang A harus melihat 2 pesanan, dapat %d", len(daftar))
	}
	for _, it := range daftar {
		if it.(map[string]any)["id"] == orderCabangBID {
			t.Error("pesanan cabang B tidak boleh muncul di daftar cabang A")
		}
	}

	// Filter status.
	_, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, os.srv.URL+"/api/v1/orders?status=draft", "", cookies, "")
	if got := dataList(t, body); len(got) != 1 || got[0].(map[string]any)["id"] != id {
		t.Fatalf("filter draft salah: %v", got)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, os.srv.URL+"/api/v1/orders?status=batal", "", cookies, "")
	if status != http.StatusBadRequest {
		t.Fatalf("status tak dikenal harus 400 (tidak ada status Batal), dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")

	// Detail.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, os.srv.URL+"/api/v1/orders/"+id, "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("detail pesanan harus 200, dapat %d (%v)", status, body)
	}
	if got := dataMap(t, body)["catatan"]; got != "dikirim pagi" {
		t.Errorf("catatan tidak tersimpan: %v", got)
	}

	// Ubah draft: baris diganti dan total dihitung ulang.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, os.srv.URL+"/api/v1/orders/"+id,
		`{"catatan":"dikirim sore","lines":[{"product_id":"p1","qty":4}]}`, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("ubah draft harus 200, dapat %d (%v)", status, body)
	}
	ubah := dataMap(t, body)
	if ubah["total"] != float64(400000) || ubah["catatan"] != "dikirim sore" || ubah["jumlah_item"] != float64(4) {
		t.Fatalf("hasil ubah salah: %v", ubah)
	}

	// Hapus draft.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodDelete, os.srv.URL+"/api/v1/orders/"+id, "", cookies, csrf)
	if status != http.StatusNoContent {
		t.Fatalf("hapus draft harus 204, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, os.srv.URL+"/api/v1/orders/"+id, "", cookies, "")
	if status != http.StatusNotFound {
		t.Fatalf("draft yang sudah dihapus harus 404, dapat %d", status)
	}
}

// TestApiHargaDanTotalDariKlienDitolak memverifikasi angka kiriman klien tidak
// pernah diterima: field harga/subtotal/total bukan bagian dari kontrak.
func TestApiHargaDanTotalDariKlienDitolak(t *testing.T) {
	os := newOrdersServer(t)
	cookies, csrf := login(t, os.srv, "a@example.test")

	for _, body := range []string{
		`{"lines":[{"product_id":"p1","qty":1,"harga_satuan":1}]}`,
		`{"total":1,"lines":[{"product_id":"p1","qty":1}]}`,
		`{"lines":[{"product_id":"p1","qty":1,"subtotal":1}]}`,
		`{"lines":[{"product_id":"p1","qty":1,"nama_snapshot":"Palsu"}]}`,
	} {
		status, resp, _ := apiReq(t, http.DefaultClient, http.MethodPost, os.srv.URL+"/api/v1/orders", body, cookies, csrf)
		if status != http.StatusBadRequest {
			t.Fatalf("body %s harus ditolak 400, dapat %d (%v)", body, status, resp)
		}
		assertErrorShape(t, resp, "VALIDATION_FAILED")
	}

	// Setelah itu tidak ada pesanan yang lahir dari percobaan di atas.
	_, resp, _ := apiReq(t, http.DefaultClient, http.MethodGet, os.srv.URL+"/api/v1/orders?status=draft", "", cookies, "")
	if got := dataList(t, resp); len(got) != 0 {
		t.Fatalf("tidak boleh ada draft baru, dapat %v", got)
	}
}

// TestApiProdukTanpaHargaCabangDitolak memenuhi kewajiban F1-11 dari QA F1-08:
// produk tanpa baris branch_prices tidak bisa dipesan lewat API order.
func TestApiProdukTanpaHargaCabangDitolak(t *testing.T) {
	os := newOrdersServer(t)
	cookies, csrf := login(t, os.srv, "a@example.test")

	// p2 punya harga_default 999000 tetapi tanpa baris harga cabang.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, os.srv.URL+"/api/v1/orders",
		`{"lines":[{"product_id":"p2","qty":1}]}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("produk tanpa harga cabang harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")

	// Produk tidak dikenal juga ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, os.srv.URL+"/api/v1/orders",
		`{"lines":[{"product_id":"p-hantu","qty":1}]}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("produk tak dikenal harus 400, dapat %d (%v)", status, body)
	}

	// Cabang B tidak menjual p3 (sertifikat) sehingga juga ditolak.
	cookiesB, csrfB := login(t, os.srv, "b@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, os.srv.URL+"/api/v1/orders",
		`{"lines":[{"product_id":"p3","qty":1}]}`, cookiesB, csrfB)
	if status != http.StatusBadRequest {
		t.Fatalf("produk tanpa harga di cabang B harus 400, dapat %d (%v)", status, body)
	}
}

// TestApiDraftCabangLainTidakTerlihat menegaskan isolasi lintas cabang lewat
// HTTP: baca, ubah, dan hapus draft cabang lain semuanya 404.
func TestApiDraftCabangLainTidakTerlihat(t *testing.T) {
	os := newOrdersServer(t)
	cookiesA, csrfA := login(t, os.srv, "a@example.test")
	cookiesB, csrfB := login(t, os.srv, "b@example.test")

	// Draft baru milik cabang A.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, os.srv.URL+"/api/v1/orders",
		`{"lines":[{"product_id":"p1","qty":1}]}`, cookiesA, csrfA)
	if status != http.StatusCreated {
		t.Fatalf("buat draft cabang A: %d (%v)", status, body)
	}
	id := dataMap(t, body)["id"].(string)

	kasus := []struct {
		method, url string
	}{
		{http.MethodGet, os.srv.URL + "/api/v1/orders/" + id},
		{http.MethodPut, os.srv.URL + "/api/v1/orders/" + id},
		{http.MethodDelete, os.srv.URL + "/api/v1/orders/" + id},
	}
	for _, k := range kasus {
		var payload string
		if k.method == http.MethodPut {
			payload = `{"lines":[{"product_id":"p1","qty":9}]}`
		}
		status, body, _ := apiReq(t, http.DefaultClient, k.method, k.url, payload, cookiesB, csrfB)
		if status != http.StatusNotFound {
			t.Errorf("cabang B %s draft cabang A harus 404, dapat %d (%v)", k.method, status, body)
		}
	}

	// Draft cabang B miliknya sendiri juga tidak bisa dibaca cabang A.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, os.srv.URL+"/api/v1/orders/"+orderCabangBID, "", cookiesA, "")
	if status != http.StatusNotFound {
		t.Errorf("cabang A membaca draft cabang B harus 404, dapat %d (%v)", status, body)
	}
	// Draft cabang A tetap utuh dengan harga cabang A.
	_, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, os.srv.URL+"/api/v1/orders/"+id, "", cookiesA, "")
	if got := dataMap(t, body)["lines"].([]any)[0].(map[string]any)["harga_satuan"]; got != float64(100000) {
		t.Errorf("draft cabang A harus tetap memakai harganya sendiri, dapat %v", got)
	}
}

// TestApiPesananSubmittedTerkunci memverifikasi PRD CP-ORD-09 lewat HTTP:
// pesanan yang sudah dikirim tidak bisa diubah maupun dihapus cabang.
func TestApiPesananSubmittedTerkunci(t *testing.T) {
	os := newOrdersServer(t)
	cookies, csrf := login(t, os.srv, "a@example.test")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, os.srv.URL+"/api/v1/orders/"+orderSubmittedID,
		`{"lines":[{"product_id":"p1","qty":9}]}`, cookies, csrf)
	if status != http.StatusConflict {
		t.Fatalf("ubah pesanan Submitted harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "ORDER_LOCKED")

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodDelete, os.srv.URL+"/api/v1/orders/"+orderSubmittedID, "", cookies, csrf)
	if status != http.StatusConflict {
		t.Fatalf("hapus pesanan Submitted harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "ORDER_LOCKED")

	if os.ord.Count() != 2 {
		t.Errorf("pesanan Submitted tidak boleh terhapus, sisa %d", os.ord.Count())
	}
}

// TestApiOrdersHanyaUntukAkunCabang menolak admin dan menolak id yang bukan
// UUID dengan 404 (bukan 500).
func TestApiOrdersHanyaUntukAkunCabang(t *testing.T) {
	os := newOrdersServer(t)
	cookiesMgr, _ := login(t, os.srv, "budi@example.test")

	kasus := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/orders", ""},
		{http.MethodPost, "/api/v1/orders", `{"lines":[{"product_id":"p1","qty":1}]}`},
		{http.MethodGet, "/api/v1/orders/" + orderSubmittedID, ""},
		{http.MethodPut, "/api/v1/orders/" + orderSubmittedID, `{"lines":[{"product_id":"p1","qty":1}]}`},
		{http.MethodDelete, "/api/v1/orders/" + orderSubmittedID, ""},
	}
	for _, k := range kasus {
		status, body, _ := apiReq(t, http.DefaultClient, k.method, os.srv.URL+k.path, k.body, cookiesMgr, csrfFromCookies(cookiesMgr))
		if status != http.StatusForbidden {
			t.Errorf("admin %s %s harus 403, dapat %d (%v)", k.method, k.path, status, body)
		}
	}

	cookies, _ := login(t, os.srv, "a@example.test")
	for _, id := range []string{"tidak-ada", "1"} {
		status, _, _ := apiReq(t, http.DefaultClient, http.MethodGet, os.srv.URL+"/api/v1/orders/"+id, "", cookies, "")
		if status != http.StatusNotFound {
			t.Errorf("id %q harus 404, dapat %d", id, status)
		}
	}
}

// TestApiValidasiBodyDraft menolak body yang tidak lengkap atau melanggar batas.
func TestApiValidasiBodyDraft(t *testing.T) {
	os := newOrdersServer(t)
	cookies, csrf := login(t, os.srv, "a@example.test")

	kasus := []string{
		`{}`,
		`{"lines":[]}`,
		`{"lines":[{"product_id":"p1","qty":0}]}`,
		`{"lines":[{"qty":1}]}`,
		`{"lines":[{"product_id":"p1","qty":1},{"product_id":"p1","qty":1}]}`,
		`{"catatan":"` + strings.Repeat("a", 501) + `","lines":[{"product_id":"p1","qty":1}]}`,
	}
	for _, k := range kasus {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, os.srv.URL+"/api/v1/orders", k, cookies, csrf)
		if status != http.StatusBadRequest {
			t.Errorf("body %.40q harus 400, dapat %d (%v)", k, status, body)
		}
	}
	if os.ord.Count() != 2 {
		t.Errorf("tidak boleh ada pesanan baru dari body tidak valid, dapat %d", os.ord.Count())
	}
}
