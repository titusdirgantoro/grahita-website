package httpapi

// Uji integrasi bukti transfer (F1-13) pada PostgreSQL nyata.
//
// Berbeda dari router_proofs_test.go yang memakai penyimpanan in-memory, berkas
// ini menjalankan repository asli (payment/postgres) di atas database yang
// dimigrasikan, sehingga perilaku SQL benar-benar dieksekusi:
//
//   - indeks unik (order_id, sha256) menolak unggahan ganda;
//   - kolom uploaded_by_kind/id/nama benar-benar terisi dari sesi;
//   - timeline order_events ditulis beserta nama pengunggah;
//   - isolasi cabang ditegakkan lewat branch_id pesanan.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"context"
	"encoding/hex"
	"net/http"
	"testing"
)

// insertOrder membuat pesanan uji langsung di database: satu baris produk,
// status sesuai permintaan. Draft sengaja tanpa nomor/tanggal (constraint),
// dan pesanan done dibuat lewat UPDATE karena done_at wajib terisi bersamaan.
func insertOrder(t *testing.T, f *pgFixture, branchID, status, nomor string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	statusAwal := status
	var nomorArg, tanggalArg any
	if status == "done" {
		// Kolom done_at wajib terisi saat status menjadi done
		// (constraint done_punya_waktu), jadi barisnya lahir sebagai submitted.
		statusAwal = "submitted"
	}
	if statusAwal != "draft" {
		nomorArg = nomor
		tanggalArg = "2026-10-08"
	}
	err := f.pool.QueryRow(ctx,
		`INSERT INTO orders (branch_id, status, nomor, tanggal, total)
		 VALUES ($1, $2::order_status, $3, $4::date, 100000) RETURNING id`,
		branchID, statusAwal, nomorArg, tanggalArg).Scan(&id)
	if err != nil {
		t.Fatalf("seed pesanan %s: %v", status, err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot, satuan_snapshot, qty, harga_satuan)
		 VALUES ($1, 1, 'product', (SELECT id FROM products LIMIT 1), 'Bio Magnetik', 'pcs', 1, 100000)`,
		id); err != nil {
		t.Fatalf("seed baris pesanan: %v", err)
	}
	if status == "done" {
		if _, err := f.pool.Exec(ctx,
			`UPDATE orders SET status = 'done', done_at = now(), verif_status = 'cocok' WHERE id = $1`, id); err != nil {
			t.Fatalf("tandai pesanan done: %v", err)
		}
	}
	return id
}

func TestPGUnggahBuktiMenyimpanMetadataDanTimeline(t *testing.T) {
	f := newPGFixture(t)
	pesanan := insertOrder(t, f, f.branchA, "submitted", "2026-10-0011")

	cookies, csrf := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	resp, body := multipartUnggah(t, f.srv.URL+"/api/v1/orders/"+pesanan+"/proofs", cookies, csrf,
		map[string][]byte{"transfer-a.jpg": berkasJPEGUji("bukti-a"), "transfer-b.pdf": berkasPDFUji("bukti-b")})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("unggah harus 201, dapat %d (%v)", resp.StatusCode, body)
	}
	bukti := dataList(t, body)
	if len(bukti) != 2 {
		t.Fatalf("harus 2 bukti, dapat %d", len(bukti))
	}

	// Baris database: metadata lengkap, kind dari sesi, nama admin kosong.
	var jumlah int
	var kind string
	var nama *string
	var sha []byte
	var kunci string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM payment_proofs WHERE order_id = $1`, pesanan).Scan(&jumlah); err != nil {
		t.Fatalf("hitung bukti: %v", err)
	}
	if jumlah != 2 {
		t.Fatalf("payment_proofs harus 2 baris, dapat %d", jumlah)
	}
	if err := f.pool.QueryRow(context.Background(),
		`SELECT uploaded_by_kind::text, uploaded_by_nama, sha256, storage_key
		   FROM payment_proofs WHERE order_id = $1 ORDER BY created_at LIMIT 1`,
		pesanan).Scan(&kind, &nama, &sha, &kunci); err != nil {
		t.Fatalf("baca bukti: %v", err)
	}
	if kind != "branch" {
		t.Errorf("uploaded_by_kind = %q, mau branch", kind)
	}
	if nama != nil {
		t.Errorf("unggahan cabang tidak boleh mengisi uploaded_by_nama: %v", *nama)
	}
	if len(sha) != 32 {
		t.Errorf("sha256 harus 32 byte, dapat %d", len(sha))
	}
	if hex.EncodeToString(sha) == "" {
		t.Error("sha256 kosong")
	}
	if kunci == "" || kunci[:7] != "proofs/" {
		t.Errorf("storage_key tidak berawalan proofs/: %q", kunci)
	}

	// Timeline: satu event per bukti, ditandai datang dari cabang.
	var teks []string
	rows, err := f.pool.Query(context.Background(),
		`SELECT teks FROM order_events WHERE order_id = $1 ORDER BY at`, pesanan)
	if err != nil {
		t.Fatalf("baca order_events: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan order_events: %v", err)
		}
		teks = append(teks, s)
	}
	if len(teks) != 2 {
		t.Fatalf("order_events harus 2 baris, dapat %d (%v)", len(teks), teks)
	}
	for _, s := range teks {
		if s != "Bukti transfer diunggah oleh cabang" {
			t.Errorf("teks timeline cabang tidak sesuai: %q", s)
		}
	}

	// Daftar + tautan unduh lewat HTTP.
	resp, body = doJSON(t, http.MethodGet, f.srv.URL+"/api/v1/orders/"+pesanan+"/proofs", "", cookies, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("daftar bukti harus 200, dapat %d", resp.StatusCode)
	}
	if len(dataList(t, body)) != 2 {
		t.Errorf("daftar bukti harus 2")
	}
	id := dataList(t, body)[0].(map[string]any)["id"].(string)
	resp, body = doJSON(t, http.MethodGet,
		f.srv.URL+"/api/v1/orders/"+pesanan+"/proofs/"+id+"/download", "", cookies, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tautan unduh harus 200, dapat %d (%v)", resp.StatusCode, body)
	}

	// Isi berkas benar-benar tersimpan pada kunci acak itu.
	baca, err := f.objek.Get(context.Background(), kunci)
	if err != nil {
		t.Fatalf("baca objek dari storage: %v", err)
	}
	defer baca.Close()
	buf := make([]byte, 16)
	n, _ := baca.Read(buf)
	if n == 0 {
		t.Error("objek storage kosong")
	}
}

func TestPGDuplikatDitolakServiceDanDatabase(t *testing.T) {
	f := newPGFixture(t)
	pesanan := insertOrder(t, f, f.branchA, "submitted", "2026-10-0012")
	cookies, csrf := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	url := f.srv.URL + "/api/v1/orders/" + pesanan + "/proofs"

	if resp, body := multipartUnggah(t, url, cookies, csrf,
		map[string][]byte{"a.jpg": berkasJPEGUji("isi-identik")}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("unggah pertama: %d (%v)", resp.StatusCode, body)
	}
	resp, body := multipartUnggah(t, url, cookies, csrf,
		map[string][]byte{"b.jpg": berkasJPEGUji("isi-identik")})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplikat lewat HTTP harus 409, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "PROOF_DUPLICATE")

	// Benteng terakhir: indeks unik database menolak baris kembar walau
	// service dilewati.
	var sha []byte
	if err := f.pool.QueryRow(context.Background(),
		`SELECT sha256 FROM payment_proofs WHERE order_id = $1`, pesanan).Scan(&sha); err != nil {
		t.Fatalf("baca sha256: %v", err)
	}
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO payment_proofs (order_id, storage_key, nama_file, mime_type, ukuran_byte, sha256, uploaded_by_kind)
		 VALUES ($1, 'proofs/dup.jpg', 'dup.jpg', 'image/jpeg', 10, $2, 'branch')`,
		pesanan, sha)
	if err == nil {
		t.Fatal("INSERT duplikat (order_id, sha256) harus ditolak database")
	}
}

func TestPGIsolasiCabangLewatHTTP(t *testing.T) {
	f := newPGFixture(t)
	pesananA := insertOrder(t, f, f.branchA, "submitted", "2026-10-0013")

	cookiesB, csrfB := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	resp, body := multipartUnggah(t, f.srv.URL+"/api/v1/orders/"+pesananA+"/proofs", cookiesB, csrfB,
		map[string][]byte{"x.jpg": berkasJPEGUji("lintas-cabang")})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unggah lintas cabang harus 404, dapat %d (%v)", resp.StatusCode, body)
	}
	resp, _ = doJSON(t, http.MethodGet, f.srv.URL+"/api/v1/orders/"+pesananA+"/proofs", "", cookiesB, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("daftar lintas cabang harus 404, dapat %d", resp.StatusCode)
	}
	var jumlah int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM payment_proofs WHERE order_id = $1`, pesananA).Scan(&jumlah); err != nil {
		t.Fatalf("hitung bukti: %v", err)
	}
	if jumlah != 0 {
		t.Errorf("penolakan lintas cabang tidak boleh menyisakan baris, dapat %d", jumlah)
	}
}

func TestPGAdminUnggahMenandaiNamaDanStatusDone(t *testing.T) {
	f := newPGFixture(t)
	pesananDone := insertOrder(t, f, f.branchA, "done", "2026-10-0014")

	// Cabang tidak boleh menambah bukti pada pesanan Done.
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	resp, body := multipartUnggah(t, f.srv.URL+"/api/v1/orders/"+pesananDone+"/proofs", cookiesA, csrfA,
		map[string][]byte{"x.jpg": berkasJPEGUji("done")})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("cabang pada pesanan Done harus 409, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "ORDER_LOCKED")

	// Admin boleh, dan pengunggahnya ditandai nama admin di baris + timeline.
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	resp, body = multipartUnggah(t, f.srv.URL+"/api/v1/admin/orders/"+pesananDone+"/proofs", cookiesSA, csrfSA,
		map[string][]byte{"koreksi.pdf": berkasPDFUji("koreksi admin")})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin pada pesanan Done harus 201, dapat %d (%v)", resp.StatusCode, body)
	}
	hasil := dataList(t, body)
	p := hasil[0].(map[string]any)
	if p["uploaded_by_kind"] != "admin" || p["uploaded_by_nama"] != "Rina" {
		t.Errorf("bukti admin harus membawa nama pengunggah: %v", p)
	}

	var teks, kind string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT teks, actor_kind::text FROM order_events WHERE order_id = $1`, pesananDone).Scan(&teks, &kind); err != nil {
		t.Fatalf("baca order_events: %v", err)
	}
	if teks != "Bukti transfer diunggah oleh Rina (admin Pusat)" {
		t.Errorf("timeline harus menandai nama admin: %q", teks)
	}
	if kind != "admin" {
		t.Errorf("actor_kind timeline = %q, mau admin", kind)
	}
}

func TestPGBatasJumlahDariSetelanDatabase(t *testing.T) {
	f := newPGFixture(t)
	pesanan := insertOrder(t, f, f.branchA, "submitted", "2026-10-0015")

	// Superadmin menurunkan max_file menjadi 1 lewat setelan (PRD §4.9).
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	resp, body := doJSON(t, http.MethodPut, f.srv.URL+"/api/v1/admin/settings",
		`{"key":"max_file","value":1}`, cookiesSA, csrfSA)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ubah setelan max_file: %d (%v)", resp.StatusCode, body)
	}

	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	url := f.srv.URL + "/api/v1/orders/" + pesanan + "/proofs"
	if resp, body := multipartUnggah(t, url, cookiesA, csrfA,
		map[string][]byte{"a.jpg": berkasJPEGUji("satu")}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("unggah pertama: %d (%v)", resp.StatusCode, body)
	}
	resp, body = multipartUnggah(t, url, cookiesA, csrfA, map[string][]byte{"b.jpg": berkasJPEGUji("dua")})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("melebihi max_file harus 409, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "PROOF_LIMIT")

	// Setelan yang sama dipakai domain: batas permintaan ikut 1 × max_mb.
	batas, err := f.payments.MaxRequestBytes(context.Background())
	if err != nil {
		t.Fatalf("batas permintaan: %v", err)
	}
	if mau := int64(1*5*1024*1024) + (1 << 20); batas != mau {
		t.Errorf("batas permintaan %d, mau %d", batas, mau)
	}
}
