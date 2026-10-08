package httpapi

// Uji integrasi pesanan cabang (draft, editor order line, daftar/detail) pada
// PostgreSQL nyata (kartu F1-11).
//
// Berbeda dari router_orders_test.go yang memakai penyimpanan in-memory, berkas
// ini menjalankan migrasi sungguhan lalu menggerakkan router lengkap dengan
// repository asli (order/postgres, catalog/postgres) sehingga perilaku SQL —
// harga dari branch_prices cabang sesi, trigger order_lines_recalc, klausa
// branch_id/status di setiap query, dan cascade hapus draft — benar-benar
// dieksekusi.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// TestPGDraftOrderSiklusLengkap menempuh siklus draft di PostgreSQL: buat,
// daftar, detail, ubah, lalu hapus — sambil memeriksa barisnya di database.
func TestPGDraftOrderSiklusLengkap(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")

	var produkA string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM products WHERE kode = 'P-01'`).Scan(&produkA); err != nil {
		t.Fatalf("baca produk: %v", err)
	}

	// Buat draft: harga HARUS dari branch_prices cabang A (100000), bukan
	// harga_default 350000, dan total dihitung server (3×100000).
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/orders",
		fmt.Sprintf(`{"catatan":"dikirim pagi","lines":[{"product_id":%q,"qty":3}]}`, produkA), cookiesA, csrfA)
	if status != http.StatusCreated {
		t.Fatalf("buat draft harus 201, dapat %d (%v)", status, body)
	}
	draft := dataMap(t, body)
	id, _ := draft["id"].(string)
	if id == "" {
		t.Fatalf("jawaban tanpa id: %v", draft)
	}
	if draft["status"] != "draft" || draft["total"] != float64(300000) || draft["jumlah_item"] != float64(3) {
		t.Fatalf("draft tidak sesuai: %v", draft)
	}

	// Keadaan baris di database: nomor & tanggal NULL, harga cabang, tanpa serial.
	var nomor, tanggal, serial *string
	var statusTeks string
	var total, hargaSatuan, subtotal int64
	if err := f.pool.QueryRow(ctx,
		`SELECT status::text, total FROM orders WHERE id = $1 AND branch_id = $2`, id, f.branchA).
		Scan(&statusTeks, &total); err != nil {
		t.Fatalf("baca orders: %v", err)
	}
	if statusTeks != "draft" || total != 300000 {
		t.Fatalf("orders di database: status=%s total=%d", statusTeks, total)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT nomor, tanggal::text FROM orders WHERE id = $1`, id).Scan(&nomor, &tanggal); err != nil {
		t.Fatalf("baca nomor/tanggal: %v", err)
	}
	if nomor != nil || tanggal != nil {
		t.Fatalf("draft tidak boleh punya nomor/tanggal di database: %v %v", nomor, tanggal)
	}
	var baris int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM order_lines WHERE order_id = $1`, id).Scan(&baris); err != nil {
		t.Fatalf("hitung baris: %v", err)
	}
	if baris != 1 {
		t.Fatalf("harus 1 baris, dapat %d", baris)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT harga_satuan, subtotal, serial_number FROM order_lines WHERE order_id = $1`, id).
		Scan(&hargaSatuan, &subtotal, &serial); err != nil {
		t.Fatalf("baca baris: %v", err)
	}
	if hargaSatuan != 100000 || subtotal != 300000 || serial != nil {
		t.Fatalf("baris draft salah: harga=%d subtotal=%d serial=%v", hargaSatuan, subtotal, serial)
	}

	// Daftar & filter status.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/orders?status=draft", "", cookiesA, "")
	if status != http.StatusOK {
		t.Fatalf("daftar draft harus 200, dapat %d", status)
	}
	daftar := dataList(t, body)
	if len(daftar) != 1 || daftar[0].(map[string]any)["id"] != id {
		t.Fatalf("daftar draft tidak sesuai: %v", daftar)
	}

	// Ubah draft: baris diganti seluruhnya dan total ikut dihitung ulang —
	// baik oleh service maupun trigger order_lines_recalc di database.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/orders/"+id,
		fmt.Sprintf(`{"catatan":"dikirim sore","lines":[{"product_id":%q,"qty":5}]}`, produkA), cookiesA, csrfA)
	if status != http.StatusOK {
		t.Fatalf("ubah draft harus 200, dapat %d (%v)", status, body)
	}
	if ubah := dataMap(t, body); ubah["total"] != float64(500000) || ubah["catatan"] != "dikirim sore" {
		t.Fatalf("hasil ubah salah: %v", ubah)
	}
	if err := f.pool.QueryRow(ctx, `SELECT total FROM orders WHERE id = $1`, id).Scan(&total); err != nil {
		t.Fatalf("baca total setelah ubah: %v", err)
	}
	if total != 500000 {
		t.Fatalf("total di database harus 500000, dapat %d", total)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM order_lines WHERE order_id = $1`, id).Scan(&baris); err != nil {
		t.Fatalf("hitung baris setelah ubah: %v", err)
	}
	if baris != 1 {
		t.Fatalf("baris harus digantikan (bukan ditambah), dapat %d", baris)
	}

	// Detail.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/orders/"+id, "", cookiesA, "")
	if status != http.StatusOK || len(dataMap(t, body)["lines"].([]any)) != 1 {
		t.Fatalf("detail pesanan salah: %d %v", status, body)
	}

	// Hapus draft: baris ikut terhapus lewat ON DELETE CASCADE.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodDelete, f.srv.URL+"/api/v1/orders/"+id, "", cookiesA, csrfA)
	if status != http.StatusNoContent {
		t.Fatalf("hapus draft harus 204, dapat %d", status)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE id = $1`, id).Scan(&baris); err != nil {
		t.Fatalf("hitung orders setelah hapus: %v", err)
	}
	if baris != 0 {
		t.Fatalf("pesanan harus terhapus, sisa %d", baris)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM order_lines WHERE order_id = $1`, id).Scan(&baris); err != nil {
		t.Fatalf("hitung order_lines setelah hapus: %v", err)
	}
	if baris != 0 {
		t.Fatalf("baris harus ikut terhapus, sisa %d", baris)
	}
}

// TestPGHargaOrderLineDariBranchPrices memverifikasi kewajiban F1-11 yang
// diwariskan QA F1-08: harga diambil dari branch_prices cabang SESI, produk
// tanpa baris harga ditolak, dan angka kiriman klien tidak pernah dipakai.
func TestPGHargaOrderLineDariBranchPrices(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")

	// Produk dengan harga_default besar tetapi TANPA baris harga cabang.
	tanpaHarga := insertProdukFull(t, ctx, f.pool, "P-90", "Tanpa Harga Cabang", "reguler", 999000, 9)
	// Produk yang hanya dijual ke cabang B.
	hanyaB := insertProdukFull(t, ctx, f.pool, "P-91", "Hanya Cabang B", "reguler", 7000, 10)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO branch_prices (branch_id, product_id, harga) VALUES ($1,$2,7000)`, f.branchB, hanyaB); err != nil {
		t.Fatalf("seed harga cabang B: %v", err)
	}

	var produkA string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM products WHERE kode = 'P-01'`).Scan(&produkA); err != nil {
		t.Fatalf("baca produk A: %v", err)
	}

	// 1) Produk tanpa baris harga cabang A ditolak (bukan memakai harga_default).
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/orders",
		fmt.Sprintf(`{"lines":[{"product_id":%q,"qty":1}]}`, tanpaHarga), cookiesA, csrfA)
	if status != http.StatusBadRequest {
		t.Fatalf("produk tanpa harga cabang harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")

	// 2) Produk yang hanya dijual ke cabang lain juga ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/orders",
		fmt.Sprintf(`{"lines":[{"product_id":%q,"qty":1}]}`, hanyaB), cookiesA, csrfA)
	if status != http.StatusBadRequest {
		t.Fatalf("produk milik cabang lain harus 400, dapat %d (%v)", status, body)
	}

	// 3) Angka harga/subtotal/total dari klien ditolak dekoder JSON.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/orders",
		fmt.Sprintf(`{"total":1,"lines":[{"product_id":%q,"qty":1,"harga_satuan":1}]}`, produkA), cookiesA, csrfA)
	if status != http.StatusBadRequest {
		t.Fatalf("harga/total kiriman klien harus 400, dapat %d (%v)", status, body)
	}

	// 4) Percobaan memalsukan branch_id lewat body juga ditolak: branch_id
	// hanya datang dari SESI.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/orders",
		fmt.Sprintf(`{"branch_id":%q,"lines":[{"product_id":%q,"qty":1}]}`, f.branchB, produkA), cookiesA, csrfA)
	if status != http.StatusBadRequest {
		t.Fatalf("branch_id kiriman klien harus 400, dapat %d (%v)", status, body)
	}

	// Tidak ada satu pun orders yang lahir dari percobaan di atas.
	var jumlah int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&jumlah); err != nil {
		t.Fatalf("hitung orders: %v", err)
	}
	if jumlah != 0 {
		t.Fatalf("tidak boleh ada pesanan dari permintaan yang ditolak, dapat %d", jumlah)
	}

	// 5) Setelah harga cabang A diisi, produk yang sama bisa dipesan dan harga
	// yang dipakai adalah harga cabang A — bukan harga_default 999000.
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO branch_prices (branch_id, product_id, harga) VALUES ($1,$2,123000)`, f.branchA, tanpaHarga); err != nil {
		t.Fatalf("isi harga cabang A: %v", err)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/orders",
		fmt.Sprintf(`{"lines":[{"product_id":%q,"qty":2}]}`, tanpaHarga), cookiesA, csrfA)
	if status != http.StatusCreated {
		t.Fatalf("produk dengan harga cabang harus 201, dapat %d (%v)", status, body)
	}
	baru := dataMap(t, body)
	if baru["total"] != float64(246000) {
		t.Fatalf("total harus 2×123000 = 246000 (harga cabang), dapat %v", baru["total"])
	}
	baris := baru["lines"].([]any)[0].(map[string]any)
	if baris["harga_satuan"] != float64(123000) {
		t.Fatalf("harga satuan harus dari branch_prices cabang A, dapat %v", baris["harga_satuan"])
	}
	var hargaDB int64
	if err := f.pool.QueryRow(ctx,
		`SELECT l.harga_satuan FROM order_lines l WHERE l.order_id = $1`, baru["id"]).Scan(&hargaDB); err != nil {
		t.Fatalf("baca harga baris: %v", err)
	}
	if hargaDB != 123000 {
		t.Fatalf("harga di database harus 123000, dapat %d", hargaDB)
	}
}

// TestPGIsolasiCabangPesanan memverifikasi IDOR lintas cabang lewat HTTP+SQL:
// baca, ubah, dan hapus pesanan cabang lain semuanya 404, dan datanya tidak
// tersentuh. Dua cabang juga melihat pesanannya masing-masing saja.
func TestPGIsolasiCabangPesanan(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	cookiesB, csrfB := pgLogin(t, f.srv, "b@example.test", "rahasia123")

	var produkA string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM products WHERE kode = 'P-01'`).Scan(&produkA); err != nil {
		t.Fatalf("baca produk: %v", err)
	}

	// Draft milik cabang A.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/orders",
		fmt.Sprintf(`{"lines":[{"product_id":%q,"qty":1}]}`, produkA), cookiesA, csrfA)
	if status != http.StatusCreated {
		t.Fatalf("buat draft cabang A: %d (%v)", status, body)
	}
	id := dataMap(t, body)["id"].(string)

	// Cabang B tidak bisa membaca, mengubah, atau menghapus draft cabang A.
	for _, k := range []struct {
		method string
		body   string
	}{
		{http.MethodGet, ""},
		{http.MethodPut, fmt.Sprintf(`{"lines":[{"product_id":%q,"qty":9}]}`, produkA)},
		{http.MethodDelete, ""},
	} {
		status, body, _ := apiReq(t, http.DefaultClient, k.method, f.srv.URL+"/api/v1/orders/"+id, k.body, cookiesB, csrfB)
		if status != http.StatusNotFound {
			t.Errorf("cabang B %s pesanan cabang A harus 404, dapat %d (%v)", k.method, status, body)
		}
	}

	// Data cabang A tidak berubah dan tetap milik cabang A.
	var branchID string
	var qty int
	if err := f.pool.QueryRow(ctx, `SELECT branch_id FROM orders WHERE id = $1`, id).Scan(&branchID); err != nil {
		t.Fatalf("baca branch_id: %v", err)
	}
	if branchID != f.branchA {
		t.Fatalf("pesanan harus milik cabang A, dapat %s", branchID)
	}
	if err := f.pool.QueryRow(ctx, `SELECT qty FROM order_lines WHERE order_id = $1`, id).Scan(&qty); err != nil {
		t.Fatalf("baca qty: %v", err)
	}
	if qty != 1 {
		t.Fatalf("qty tidak boleh berubah, dapat %d", qty)
	}

	// Daftar pesanan tetap terpisah per cabang.
	_, bodyA, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/orders", "", cookiesA, "")
	_, bodyB, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/orders", "", cookiesB, "")
	if got := len(dataList(t, bodyA)); got != 1 {
		t.Errorf("cabang A harus melihat 1 pesanan, dapat %d", got)
	}
	if got := len(dataList(t, bodyB)); got != 0 {
		t.Errorf("cabang B tidak boleh melihat pesanan cabang A, dapat %d", got)
	}
}

// TestPGPesananSubmittedTidakBisaDiubahCabang menegaskan PRD CP-ORD-09 di
// database: cabang tidak bisa mengubah atau menghapus pesanan yang sudah
// dikirim, walau memanggil endpointnya langsung.
func TestPGPesananSubmittedTidakBisaDiubahCabang(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")

	var produkA string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM products WHERE kode = 'P-01'`).Scan(&produkA); err != nil {
		t.Fatalf("baca produk: %v", err)
	}

	// Pesanan Submitted yang sudah punya nomor & tanggal, dibuat langsung di DB
	// (submit sebenarnya adalah kartu F1-14).
	var orderID string
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO orders (branch_id, nomor, status, tanggal, total)
		 VALUES ($1,'2026-10-01','submitted',current_date,0) RETURNING id`, f.branchA).Scan(&orderID); err != nil {
		t.Fatalf("seed pesanan submitted: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO order_lines (order_id, line_no, product_id, nama_snapshot, satuan_snapshot, qty, harga_satuan)
		 VALUES ($1,1,$2,'Bio Magnetik','pcs',2,100000)`, orderID, produkA); err != nil {
		t.Fatalf("seed baris pesanan: %v", err)
	}

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/orders/"+orderID,
		fmt.Sprintf(`{"lines":[{"product_id":%q,"qty":9}]}`, produkA), cookiesA, csrfA)
	if status != http.StatusConflict {
		t.Fatalf("ubah pesanan Submitted harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "ORDER_LOCKED")

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodDelete, f.srv.URL+"/api/v1/orders/"+orderID, "", cookiesA, csrfA)
	if status != http.StatusConflict {
		t.Fatalf("hapus pesanan Submitted harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "ORDER_LOCKED")

	// Data pesanan tetap utuh: nomor, total, dan qty barisnya.
	var nomor string
	var total int64
	var qty int
	if err := f.pool.QueryRow(ctx, `SELECT nomor, total FROM orders WHERE id = $1`, orderID).Scan(&nomor, &total); err != nil {
		t.Fatalf("baca pesanan: %v", err)
	}
	if nomor != "2026-10-01" || total != 200000 {
		t.Fatalf("pesanan Submitted berubah: nomor=%s total=%d", nomor, total)
	}
	if err := f.pool.QueryRow(ctx, `SELECT qty FROM order_lines WHERE order_id = $1`, orderID).Scan(&qty); err != nil {
		t.Fatalf("baca qty: %v", err)
	}
	if qty != 2 {
		t.Fatalf("qty pesanan Submitted tidak boleh berubah, dapat %d", qty)
	}
}
