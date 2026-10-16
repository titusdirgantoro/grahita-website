package httpapi

// Uji integrasi katalog, master produk, dan matriks harga pada PostgreSQL
// nyata (F1-08).
//
// Berbeda dari router_catalog_test.go yang memakai penyimpanan in-memory,
// berkas ini menjalankan migrasi sungguhan lalu menggerakkan router lengkap
// dengan repository asli (catalog/postgres, branch/postgres) sehingga perilaku
// SQL — INNER JOIN harga cabang, ON DELETE RESTRICT produk, transaksi audit,
// dan snapshot harga order line — benar-benar dieksekusi.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/grahita/portal/api/internal/platform/database"
)

// insertProdukFull menyisipkan produk dengan jenis dan harga default bebas.
func insertProdukFull(t *testing.T, ctx context.Context, pool *database.Pool, kode, nama, jenis string, hargaDefault int64, urutan int) string {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx,
		`INSERT INTO products (kode, nama, jenis, harga_default, urutan)
		 VALUES ($1,$2,$3::product_kind,$4,$5) RETURNING id`,
		kode, nama, jenis, hargaDefault, urutan).Scan(&id)
	if err != nil {
		t.Fatalf("seed produk %s: %v", kode, err)
	}
	return id
}

// TestPGKatalogHargaBerbedaDanProdukTanpaHarga memverifikasi kriteria kartu:
// dua cabang melihat harga berbeda dan produk tanpa baris harga tidak muncul
// di portal cabang walau harga_default-nya terisi (PRD CP-KAT-01/02, AP-KAT-03).
func TestPGKatalogHargaBerbedaDanProdukTanpaHarga(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// Produk dengan harga default besar tetapi TANPA baris harga cabang:
	// tidak boleh muncul di katalog cabang mana pun.
	tanpaHarga := insertProdukFull(t, ctx, f.pool, "P-02", "Tanpa Harga", "reguler", 999000, 2)

	cookiesA, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/catalog", "", cookiesA, "")
	if status != http.StatusOK {
		t.Fatalf("katalog cabang A harus 200, dapat %d", status)
	}
	listA := dataList(t, body)
	if len(listA) != 1 {
		t.Fatalf("cabang A hanya punya harga untuk satu produk, dapat %d baris", len(listA))
	}
	if item := listA[0].(map[string]any); item["harga"] != float64(100000) {
		t.Fatalf("cabang A harus melihat harganya sendiri, dapat %v", item["harga"])
	}
	for _, it := range listA {
		if it.(map[string]any)["id"] == tanpaHarga {
			t.Fatal("produk tanpa baris harga cabang tidak boleh tampil di katalog cabang")
		}
	}

	cookiesB, _ := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	_, bodyB, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/catalog", "", cookiesB, "")
	if got := dataList(t, bodyB)[0].(map[string]any)["harga"]; got != float64(200000) {
		t.Fatalf("cabang B harus melihat harga berbeda, dapat %v", got)
	}

	// Admin melihat seluruh produk (termasuk yang belum punya harga).
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/products", "", cookiesMgr, "")
	if status != http.StatusOK || len(dataList(t, body)) != 2 {
		t.Fatalf("admin harus melihat 2 produk, dapat %d (%v)", status, body)
	}

	// Matriks harga: 2 baris produk × 3 kolom cabang, hanya 3 sel berisi.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/prices", "", cookiesMgr, "")
	if status != http.StatusOK {
		t.Fatalf("matriks harga harus 200, dapat %d", status)
	}
	matriks := dataMap(t, body)
	if len(matriks["produk"].([]any)) != 2 || len(matriks["cabang"].([]any)) != 3 || len(matriks["sel"].([]any)) != 3 {
		t.Fatalf("bentuk matriks tidak sesuai: %d baris, %d kolom, %d sel",
			len(matriks["produk"].([]any)), len(matriks["cabang"].([]any)), len(matriks["sel"].([]any)))
	}

	// Isi sel lewat batch edit → katalog cabang A bertambah.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/prices",
		fmt.Sprintf(`{"items":[{"branch_id":%q,"product_id":%q,"harga":50000}]}`, f.branchA, tanpaHarga), cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("isi sel harus 200, dapat %d (%v)", status, body)
	}
	_, bodyA2, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/catalog", "", cookiesA, "")
	if got := len(dataList(t, bodyA2)); got != 2 {
		t.Fatalf("setelah sel diisi, katalog cabang A harus 2 produk, dapat %d", got)
	}

	// Kosongkan sel → produk hilang lagi dari katalog cabang A.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/prices",
		fmt.Sprintf(`{"items":[{"branch_id":%q,"product_id":%q,"harga":null}]}`, f.branchA, tanpaHarga), cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("kosongkan sel harus 200, dapat %d", status)
	}
	_, bodyA3, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/catalog", "", cookiesA, "")
	if got := len(dataList(t, bodyA3)); got != 1 {
		t.Fatalf("sel yang dikosongkan harus menghilangkan produk dari katalog, dapat %d", got)
	}
	var baris int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM branch_prices WHERE branch_id = $1 AND product_id = $2`, f.branchA, tanpaHarga).Scan(&baris); err != nil {
		t.Fatalf("hitung baris harga: %v", err)
	}
	if baris != 0 {
		t.Fatalf("baris branch_prices harus terhapus, sisa %d", baris)
	}
}

// TestPGPerubahanHargaTidakMengubahPesananLama memverifikasi PRD AP-KAT-05 /
// BR-05: harga order line adalah snapshot, jadi mengubah master harga tidak
// menyentuh pesanan yang sudah ada.
func TestPGPerubahanHargaTidakMengubahPesananLama(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// Pesanan draft cabang A memakai harga cabang saat itu (100000).
	var orderID string
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO orders (branch_id, status, total) VALUES ($1,'draft',300000) RETURNING id`,
		f.branchA).Scan(&orderID); err != nil {
		t.Fatalf("seed pesanan: %v", err)
	}
	var produkID string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM products LIMIT 1`).Scan(&produkID); err != nil {
		t.Fatalf("baca produk: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO order_lines (order_id, line_no, product_id, nama_snapshot, qty, harga_satuan)
		 VALUES ($1, 1, $2, 'Bio Magnetik', 3, 100000)`, orderID, produkID); err != nil {
		t.Fatalf("seed order line: %v", err)
	}

	// Ubah harga master cabang A.
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/prices",
		fmt.Sprintf(`{"items":[{"branch_id":%q,"product_id":%q,"harga":222000}]}`, f.branchA, produkID), cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("ubah harga harus 200, dapat %d (%v)", status, body)
	}

	// Pesanan lama tidak boleh berubah sedikit pun.
	var hargaSatuan, subtotal, total int64
	if err := f.pool.QueryRow(ctx,
		`SELECT harga_satuan, subtotal FROM order_lines WHERE order_id = $1`, orderID).Scan(&hargaSatuan, &subtotal); err != nil {
		t.Fatalf("baca order line: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT total FROM orders WHERE id = $1`, orderID).Scan(&total); err != nil {
		t.Fatalf("baca pesanan: %v", err)
	}
	if hargaSatuan != 100000 || subtotal != 300000 || total != 300000 {
		t.Fatalf("snapshot pesanan berubah: harga=%d subtotal=%d total=%d", hargaSatuan, subtotal, total)
	}

	// Harga master yang baru tetap terpakai untuk pesanan berikutnya.
	var hargaMaster int64
	if err := f.pool.QueryRow(ctx,
		`SELECT harga FROM branch_prices WHERE branch_id = $1 AND product_id = $2`, f.branchA, produkID).Scan(&hargaMaster); err != nil {
		t.Fatalf("baca harga cabang: %v", err)
	}
	if hargaMaster != 222000 {
		t.Fatalf("harga master harus terbaru, dapat %d", hargaMaster)
	}

	// Produk yang sudah dipakai pesanan tidak boleh dihapus (ON DELETE RESTRICT).
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodDelete, f.srv.URL+"/api/v1/admin/products/"+produkID, "", cookiesMgr, csrfMgr)
	if status != http.StatusConflict {
		t.Fatalf("hapus produk terpakai harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "PRODUCT_IN_USE")
}

// TestPGPenyalinanHargaMenggantikanCabangTujuan memverifikasi PRD AP-KAT-04
// beserta auditnya.
func TestPGPenyalinanHargaMenggantikanCabangTujuan(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// Produk kedua hanya dijual ke cabang B; produk itu harus hilang dari
	// cabang B setelah cabang B disalin dari cabang A.
	ekstra := insertProdukFull(t, ctx, f.pool, "P-05", "Hanya Cabang B", "reguler", 7000, 5)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO branch_prices (branch_id, product_id, harga) VALUES ($1,$2,7000)`, f.branchB, ekstra); err != nil {
		t.Fatalf("seed harga cabang B: %v", err)
	}

	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/prices/copy",
		fmt.Sprintf(`{"from_branch_id":%q,"semua":true}`, f.branchA), cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("salin harga harus 200, dapat %d (%v)", status, body)
	}
	hasil := dataMap(t, body)
	if len(hasil["salinan"].([]any)) != 2 {
		t.Fatalf("harus menyalin ke 2 cabang lain, dapat %v", hasil["salinan"])
	}

	// Cabang B harus identik dengan cabang A (dibandingkan dua arah lewat
	// EXCEPT, jadi sel yang hanya ada di salah satu sisi pun terhitung).
	var beda int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			(SELECT product_id, harga FROM branch_prices WHERE branch_id = $1
			 EXCEPT
			 SELECT product_id, harga FROM branch_prices WHERE branch_id = $2)
			UNION ALL
			(SELECT product_id, harga FROM branch_prices WHERE branch_id = $2
			 EXCEPT
			 SELECT product_id, harga FROM branch_prices WHERE branch_id = $1)
		) t`, f.branchA, f.branchB).Scan(&beda); err != nil {
		t.Fatalf("bandingkan matriks: %v", err)
	}
	if beda != 0 {
		t.Fatalf("cabang B harus identik dengan cabang A, %d sel berbeda", beda)
	}
	var ekstraDiB int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM branch_prices WHERE branch_id = $1 AND product_id = $2`, f.branchB, ekstra).Scan(&ekstraDiB); err != nil {
		t.Fatalf("hitung baris ekstra: %v", err)
	}
	if ekstraDiB != 0 {
		t.Fatal("harga yang tidak ada di cabang sumber harus terhapus di cabang tujuan")
	}

	// Satu entri audit per cabang tujuan, lengkap dengan aktor & IP.
	var audit int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE entity = 'branch_price' AND action = 'copy'
		   AND actor_nama = 'Budi' AND ip IS NOT NULL AND after IS NOT NULL`).Scan(&audit); err != nil {
		t.Fatalf("hitung audit salin: %v", err)
	}
	if audit != 2 {
		t.Fatalf("harus 2 entri audit copy, dapat %d", audit)
	}
}

// TestPGMasterProdukValidasiDanAudit memverifikasi CRUD produk, aturan
// Sertifikat, kode unik, dan catatan auditnya.
func TestPGMasterProdukValidasiDanAudit(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	var produkA string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM products WHERE kode = 'P-01'`).Scan(&produkA); err != nil {
		t.Fatalf("baca produk unggulan: %v", err)
	}

	// Buat produk reguler.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/products",
		`{"kode":"P-QA","nama":"Produk QA","jenis":"reguler","harga_default":12345,"urutan":9}`, cookiesMgr, csrfMgr)
	if status != http.StatusCreated {
		t.Fatalf("buat produk harus 201, dapat %d (%v)", status, body)
	}
	baru := dataMap(t, body)
	id, _ := baru["id"].(string)

	// Kode duplikat ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/products",
		`{"kode":"P-QA","nama":"Duplikat"}`, cookiesMgr, csrfMgr)
	if status != http.StatusConflict {
		t.Fatalf("kode duplikat harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "KODE_TAKEN")

	// Sertifikat berbayar ditolak; sertifikat 0 diterima.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/products",
		`{"nama":"Sertifikat Mahal","jenis":"sertifikat","harga_default":1000}`, cookiesMgr, csrfMgr)
	if status != http.StatusBadRequest {
		t.Fatalf("sertifikat berbayar harus 400, dapat %d", status)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/products",
		`{"nama":"Sertifikat","jenis":"sertifikat","harga_default":0}`, cookiesMgr, csrfMgr)
	if status != http.StatusCreated {
		t.Fatalf("sertifikat harga 0 harus 201, dapat %d (%v)", status, body)
	}
	serID, _ := dataMap(t, body)["id"].(string)

	// Sertifikat tidak boleh diberi harga cabang selain 0.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/prices",
		fmt.Sprintf(`{"items":[{"branch_id":%q,"product_id":%q,"harga":5000}]}`, f.branchA, serID), cookiesMgr, csrfMgr)
	if status != http.StatusBadRequest {
		t.Fatalf("harga cabang sertifikat harus 400, dapat %d (%v)", status, body)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/prices",
		fmt.Sprintf(`{"items":[{"branch_id":%q,"product_id":%q,"harga":0}]}`, f.branchA, serID), cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("harga cabang sertifikat 0 harus 200, dapat %d", status)
	}

	// Ubah produk, lalu nonaktifkan; hapus hanya boleh saat belum dipakai.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/products/"+id,
		`{"nama":"Produk QA v2","harga_default":15000}`, cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("ubah produk harus 200, dapat %d (%v)", status, body)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodDelete, f.srv.URL+"/api/v1/admin/products/"+id, "", cookiesMgr, csrfMgr)
	if status != http.StatusNoContent {
		t.Fatalf("hapus produk harus 204, dapat %d", status)
	}

	// Ubah harga sel yang sudah ada → entri audit update dengan before/after.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/prices",
		fmt.Sprintf(`{"items":[{"branch_id":%q,"product_id":%q,"harga":111000}]}`, f.branchA, produkA), cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("ubah harga sel lama harus 200, dapat %d", status)
	}

	// Audit: create, update, delete produk + perubahan harga, dengan aktor & IP.
	var audit int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_logs
		 WHERE entity = 'product' AND actor_nama = 'Budi' AND ip IS NOT NULL
		   AND action IN ('create','update','delete')`).Scan(&audit); err != nil {
		t.Fatalf("hitung audit produk: %v", err)
	}
	if audit != 4 {
		t.Fatalf("harus 4 entri audit produk (P-QA create/update/delete + sertifikat create), dapat %d", audit)
	}
	var before, after string
	if err := f.pool.QueryRow(ctx, `
		SELECT before->>'harga', after->>'harga' FROM audit_logs
		 WHERE entity = 'branch_price' AND action = 'update' AND actor_nama = 'Budi'
		   AND before IS NOT NULL AND after IS NOT NULL
		 ORDER BY at DESC LIMIT 1`).Scan(&before, &after); err != nil {
		t.Fatalf("baca audit harga: %v", err)
	}
	if before != "100000" || after != "111000" {
		t.Fatalf("snapshot audit harga tidak sesuai: before=%s after=%s", before, after)
	}

	// Constraint database untuk harga default sertifikat tetap berlaku walau
	// jalur service dilewati.
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO products (nama, jenis, harga_default) VALUES ('Curang','sertifikat',5000)`); err == nil {
		t.Fatal("constraint sertifikat_gratis harus menolak harga default sertifikat != 0")
	}
}
