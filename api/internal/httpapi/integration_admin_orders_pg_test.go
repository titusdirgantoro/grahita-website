package httpapi

// Uji integrasi riwayat & detail pesanan admin (F1-15) pada PostgreSQL NYATA:
// filter, grouping, paginasi server, catatan internal, dokumen, log verifikasi,
// dan isolasi catatan internal dari portal cabang benar-benar dieksekusi SQL.
//
// Data disemai langsung lewat SQL supaya keadaan yang jarang (C&C lewat jatuh
// tempo, email gagal, dokumen + log verifikasi) bisa dibuat deterministik tanpa
// harus menempuh seluruh alur submit/verifikasi yang pemiliknya kartu lain.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/database"
)

// seedOrderAdmin adalah satu pesanan yang disemai untuk uji riwayat admin.
type seedOrderAdmin struct {
	branchID string
	nomor    string
	tanggal  string // YYYY-MM-DD
	status   string // draft|submitted|done
	verif    string // belum|cocok|perlu_review
	alasan   string // review_reason atau kosong
	cnc      bool
	jt       string // jatuh_tempo YYYY-MM-DD atau kosong
	catatan  string
	note     string
	harga    int64
}

func insertProdukAdmin(t *testing.T, ctx context.Context, pool *database.Pool, kode, nama string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx,
		`INSERT INTO products (kode, nama, harga_default) VALUES ($1,$2,1000) RETURNING id`,
		kode, nama).Scan(&id); err != nil {
		t.Fatalf("semai produk %s: %v", kode, err)
	}
	return id
}

// insertOrderAdmin menyemai satu pesanan non-draft beserta satu baris produk
// berserial. Total ditentukan trigger order_lines_recalc dari subtotal baris.
func insertOrderAdmin(t *testing.T, ctx context.Context, pool *database.Pool, s seedOrderAdmin, productID, serial string) string {
	t.Helper()
	var jt, alasan any
	if s.jt != "" {
		jt = s.jt
	}
	if s.alasan != "" {
		alasan = s.alasan
	}
	// Pesanan Done wajib memiliki pointer verifikasi (orders_settlement_state_consistent):
	// semai sebagai submitted dulu, lalu naikkan lewat pasangPointerVerifikasiDone.
	status, verif := s.status, s.verif
	if s.status == "done" {
		status, verif = "submitted", "belum"
	}
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO orders (branch_id, nomor, versi, status, verif_status, alasan_review, is_cnc,
		                    is_sertifikat, tanggal, jatuh_tempo, catatan, note_admin, total, submitted_at)
		VALUES ($1, $2, 0, $3::order_status, $4::verif_status, $5::review_reason, $6,
		        false, $7::date, $8::date, $9, $10, 0, now())
		RETURNING id`,
		s.branchID, s.nomor, status, verif, alasan, s.cnc, s.tanggal, jt, s.catatan, s.note).Scan(&id)
	if err != nil {
		t.Fatalf("semai pesanan %s: %v", s.nomor, err)
	}
	if productID != "" {
		if _, err := pool.Exec(ctx, `
			INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot, qty, harga_satuan, serial_number)
			VALUES ($1, 1, 'product', $2, 'Produk Uji', 1, $3, $4)`,
			id, productID, s.harga, serial); err != nil {
			t.Fatalf("semai baris pesanan %s: %v", s.nomor, err)
		}
	}
	if s.status == "done" {
		var adminID, bankID string
		var total int64
		if err := pool.QueryRow(ctx, `SELECT
			  (SELECT id::text FROM admin_users WHERE role = 'superadmin' ORDER BY created_at LIMIT 1),
			  (SELECT id::text FROM bank_accounts ORDER BY created_at LIMIT 1),
			  (SELECT total FROM orders WHERE id = $1)`, id).Scan(&adminID, &bankID, &total); err != nil {
			t.Fatalf("baca admin/rekening untuk pesanan Done %s: %v", s.nomor, err)
		}
		pasangPointerVerifikasiDone(t, pool, adminID, bankID, id, s.nomor, total)
	}
	return id
}

func insertEventAdmin(t *testing.T, ctx context.Context, pool *database.Pool, orderID, teks string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO order_events (order_id, icon, teks, actor_kind, actor_id)
		 VALUES ($1, 'kirim', $2, 'branch', $3)`, orderID, teks, nil); err != nil {
		t.Fatalf("semai timeline: %v", err)
	}
}

func insertDokumenAdmin(t *testing.T, ctx context.Context, pool *database.Pool, orderID, jenis string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO documents (order_id, jenis, versi, storage_key, ukuran_byte)
		 VALUES ($1, $2::doc_kind, 0, $3, 1234)`,
		orderID, jenis, "documents/"+orderID+"/0"); err != nil {
		t.Fatalf("semai dokumen: %v", err)
	}
}

func insertVerifikasiAdmin(t *testing.T, ctx context.Context, pool *database.Pool, orderID, adminID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO verification_logs (order_id, metode, total_pesanan, total_terbaca, hasil, admin_id, catatan)
		VALUES ($1, 'manual', 200000, 200000, 'cocok', $2, 'cocok dengan mutasi')`,
		orderID, adminID); err != nil {
		t.Fatalf("semai log verifikasi: %v", err)
	}
}

func insertEmailGagalAdmin(t *testing.T, ctx context.Context, pool *database.Pool, orderID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO email_outbox (order_id, jenis, to_addr, subject, payload, status, attempts, last_error)
		VALUES ($1, 'invoice_cabang', ARRAY['cabang-a@example.test'], 'Invoice', '{}'::jsonb, 'failed', 6, 'smtp menolak')`,
		orderID); err != nil {
		t.Fatalf("semai email gagal: %v", err)
	}
}

// TestPGAdminRiwayatDetailDanCatatan menempuh seluruh permukaan F1-15 di atas
// PostgreSQL nyata.
func TestPGAdminRiwayatDetailDanCatatan(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	hariIni := clock.TodayWIB(clock.System())
	tglLama := hariIni.AddDate(0, -1, -5)
	tglBaru := hariIni.AddDate(0, 0, -1)

	produk := insertProdukAdmin(t, ctx, f.pool, "P-15", "Produk Riwayat")

	o1 := insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchA, nomor: "2026-09-01", tanggal: tglLama.Format("2006-01-02"),
		status: "submitted", verif: "belum", catatan: "dikirim pagi", harga: 200000,
	}, produk, "20260901001")
	o2 := insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchA, nomor: "2026-10-01", tanggal: tglBaru.Format("2006-01-02"),
		status: "done", verif: "cocok", harga: 300000,
	}, produk, "20261001001")
	o3 := insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchB, nomor: "2026-10-02", tanggal: tglBaru.Format("2006-01-02"),
		status: "submitted", verif: "belum", cnc: true,
		jt: tglBaru.AddDate(0, 0, -7).Format("2006-01-02"), harga: 150000,
	}, produk, "20261002001")
	_ = insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchC, nomor: "2026-09-02", tanggal: tglLama.Format("2006-01-02"),
		status: "done", verif: "cocok", harga: 50000,
	}, produk, "20260902001")
	o4 := insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchB, nomor: "2026-10-03", tanggal: tglBaru.Format("2006-01-02"),
		status: "submitted", verif: "perlu_review", alasan: "kurang_bayar", harga: 100000,
	}, produk, "20261003001")

	insertEventAdmin(t, ctx, f.pool, o1, "Pesanan dikirim oleh Grahita Cabang A")
	insertDokumenAdmin(t, ctx, f.pool, o1, "invoice_pdf")
	insertVerifikasiAdmin(t, ctx, f.pool, o2, f.adminSA)
	insertEmailGagalAdmin(t, ctx, f.pool, o4)

	ac, csrf := login(t, f.srv, "budi@example.test")

	// --- Batas akses: akun cabang tidak boleh melihat riwayat admin ---------
	cc, csrfC := login(t, f.srv, "a@example.test")
	for _, path := range []string{"/api/v1/admin/orders", "/api/v1/admin/orders/" + o1,
		"/api/v1/admin/orders/" + o1 + "/proofs"} {
		st, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+path, "", cc, csrfC)
		if st != http.StatusForbidden {
			t.Fatalf("cabang pada %s harus 403, dapat %d (%v)", path, st, body)
		}
	}
	st, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/orders/"+o1+"/notes",
		`{"catatan":"x"}`, cc, csrfC)
	if st != http.StatusForbidden {
		t.Fatalf("cabang tidak boleh menulis catatan admin, dapat %d (%v)", st, body)
	}

	// --- Daftar: semua cabang, paginasi server ------------------------------
	st, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/orders", "", ac, csrf)
	if st != http.StatusOK {
		t.Fatalf("daftar riwayat harus 200, dapat %d (%v)", st, body)
	}
	hal := dataMap(t, body)
	if hal["total"] != float64(5) {
		t.Fatalf("total harus 5, dapat %v (%v)", hal["total"], body)
	}
	items, _ := hal["items"].([]any)
	if len(items) != 5 {
		t.Fatalf("halaman pertama harus 5 baris, dapat %d", len(items))
	}
	// Pesanan dari ketiga cabang muncul (admin melihat semua cabang).
	cabangTerlihat := map[string]bool{}
	for _, it := range items {
		cabangTerlihat[it.(map[string]any)["branch_id"].(string)] = true
	}
	if !cabangTerlihat[f.branchA] || !cabangTerlihat[f.branchB] || !cabangTerlihat[f.branchC] {
		t.Fatalf("daftar harus memuat seluruh cabang: %v", cabangTerlihat)
	}

	// Paginasi: 3 halaman × 2 baris, tanpa baris berulang/terlewat.
	terkumpul := map[string]bool{}
	for halaman := 1; halaman <= 3; halaman++ {
		url := fmt.Sprintf("%s/api/v1/admin/orders?per_halaman=2&halaman=%d", f.srv.URL, halaman)
		st, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, url, "", ac, csrf)
		if st != http.StatusOK {
			t.Fatalf("halaman %d harus 200, dapat %d (%v)", halaman, st, body)
		}
		h := dataMap(t, body)
		if h["total"] != float64(5) || h["halaman"] != float64(halaman) || h["per_halaman"] != float64(2) {
			t.Fatalf("metadata halaman %d salah: %v", halaman, h)
		}
		its, _ := h["items"].([]any)
		mau := 2
		if halaman == 3 {
			mau = 1
		}
		if len(its) != mau {
			t.Fatalf("halaman %d harus %d baris, dapat %d", halaman, mau, len(its))
		}
		for _, it := range its {
			id := it.(map[string]any)["id"].(string)
			if terkumpul[id] {
				t.Fatalf("baris %s muncul lebih dari sekali antar halaman", id)
			}
			terkumpul[id] = true
		}
	}
	if len(terkumpul) != 5 {
		t.Fatalf("paginasi harus menutup 5 pesanan, dapat %d", len(terkumpul))
	}

	// --- Filter -------------------------------------------------------------
	kasusFilter := []struct {
		nama  string
		query string
		mau   int
	}{
		{"per cabang", "?cabang=" + f.branchA, 2},
		{"status done", "?status=done", 2},
		{"verifikasi perlu review", "?verif=perlu_review", 1},
		{"C&C", "?cnc=true", 1},
		{"lewat jatuh tempo", "?terlambat=true", 1},
		{"tidak lewat jatuh tempo", "?terlambat=false", 4},
		{"email gagal", "?email_gagal=true", 1},
		{"email tidak gagal", "?email_gagal=false", 4},
		{"rentang tanggal", "?dari=" + tglLama.Format("2006-01-02") + "&sampai=" + tglLama.Format("2006-01-02"), 2},
		{"cari nomor", "?cari=2026-10-02", 1},
		{"cari cabang", "?cari=" + "Cabang%20B", 2},
		{"cari serial", "?cari=20260901001", 1},
		{"kombinasi", "?cabang=" + f.branchB + "&status=submitted&cnc=true", 1},
	}
	for _, k := range kasusFilter {
		st, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/orders"+k.query, "", ac, csrf)
		if st != http.StatusOK {
			t.Fatalf("filter %s harus 200, dapat %d (%v)", k.nama, st, body)
		}
		h := dataMap(t, body)
		if h["total"] != float64(k.mau) {
			t.Fatalf("filter %s harus %d baris, dapat %v (%v)", k.nama, k.mau, h["total"], body)
		}
		its, _ := h["items"].([]any)
		if len(its) != k.mau {
			t.Fatalf("filter %s: items %d != total %v", k.nama, len(its), h["total"])
		}
		// Penanda terlambat/email gagal ikut terhitung benar di baris.
		for _, it := range its {
			m := it.(map[string]any)
			if m["id"] == o3 && m["terlambat"] != true {
				t.Fatalf("pesanan C&C lewat tempo harus bertanda terlambat: %v", m)
			}
			if m["id"] == o4 && m["email_gagal"] != true {
				t.Fatalf("pesanan ber-email gagal harus bertanda email_gagal: %v", m)
			}
		}
	}

	// --- Grouping -----------------------------------------------------------
	st, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/orders?grup=bulan", "", ac, csrf)
	if st != http.StatusOK {
		t.Fatalf("grup bulan harus 200, dapat %d (%v)", st, body)
	}
	gb := dataMap(t, body)
	if gb["grup"] != "bulan" {
		t.Fatalf("grup harus dilaporkan: %v", gb)
	}
	ring, _ := gb["ringkasan"].([]any)
	if len(ring) != 2 {
		t.Fatalf("harus ada 2 grup bulan, dapat %d (%v)", len(ring), gb["ringkasan"])
	}
	var jumlahTotal float64
	for i, g := range ring {
		gr := g.(map[string]any)
		jumlahTotal += gr["jumlah"].(float64)
		if i > 0 {
			// Terbaru dahulu: kunci periode menurun.
			if ring[i-1].(map[string]any)["kunci"].(string) < gr["kunci"].(string) {
				t.Fatalf("grup bulan harus urut terbaru dahulu: %v", gb["ringkasan"])
			}
		}
	}
	if jumlahTotal != 5 {
		t.Fatalf("jumlah antar grup harus 5, dapat %v (%v)", jumlahTotal, gb["ringkasan"])
	}

	st, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/orders?grup=cabang", "", ac, csrf)
	if st != http.StatusOK {
		t.Fatalf("grup cabang harus 200, dapat %d (%v)", st, body)
	}
	gc := dataMap(t, body)
	ringC, _ := gc["ringkasan"].([]any)
	if len(ringC) != 3 {
		t.Fatalf("harus ada 3 grup cabang, dapat %d (%v)", len(ringC), gc["ringkasan"])
	}
	for i, g := range ringC {
		gr := g.(map[string]any)
		if strings.TrimSpace(gr["label"].(string)) == "" {
			t.Fatalf("grup cabang harus berlabel nama cabang: %v", gr)
		}
		if i > 0 {
			if ringC[i-1].(map[string]any)["label"].(string) > gr["label"].(string) {
				t.Fatalf("grup cabang harus urut A→Z: %v", gc["ringkasan"])
			}
		}
	}

	// --- Detail lengkap -----------------------------------------------------
	st, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/orders/"+o1, "", ac, csrf)
	if st != http.StatusOK {
		t.Fatalf("detail admin harus 200, dapat %d (%v)", st, body)
	}
	d := dataMap(t, body)
	if d["branch_nama"] != "Grahita Cabang A" || d["branch_kepala"] != "Kepala A" {
		t.Fatalf("header cabang salah: %v", d)
	}
	lines, _ := d["lines"].([]any)
	if len(lines) != 1 || lines[0].(map[string]any)["serial_number"] != "20260901001" {
		t.Fatalf("baris + serial salah: %v", d["lines"])
	}
	timeline, _ := d["timeline"].([]any)
	if len(timeline) != 1 || !strings.Contains(timeline[0].(map[string]any)["teks"].(string), "Pesanan dikirim") {
		t.Fatalf("timeline salah: %v", d["timeline"])
	}
	dok, _ := d["dokumen"].([]any)
	if len(dok) != 1 || dok[0].(map[string]any)["jenis"] != "invoice_pdf" {
		t.Fatalf("dokumen pesanan tidak tampil: %v", d["dokumen"])
	}
	if _, ada := dok[0].(map[string]any)["storage_key"]; ada {
		t.Fatal("kunci storage tidak boleh keluar ke klien")
	}
	if d["note_admin"] != "" {
		t.Fatalf("catatan internal harus kosong di awal: %v", d["note_admin"])
	}

	// Log verifikasi tampil beserta nama admin pemeriksa.
	st, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/orders/"+o2, "", ac, csrf)
	if st != http.StatusOK {
		t.Fatalf("detail o2 harus 200, dapat %d (%v)", st, body)
	}
	d2 := dataMap(t, body)
	verif, _ := d2["verifikasi"].([]any)
	if len(verif) != 1 {
		t.Fatalf("log verifikasi tidak tampil: %v", d2["verifikasi"])
	}
	if verif[0].(map[string]any)["admin_nama"] != "Rina" {
		t.Fatalf("nama admin verifikator salah: %v", verif[0])
	}

	// Id bukan UUID dijawab 404 (bukan 500).
	st, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/orders/bukan-uuid", "", ac, csrf)
	if st != http.StatusNotFound {
		t.Fatalf("id bukan uuid harus 404, dapat %d (%v)", st, body)
	}

	// --- Catatan internal + isolasi dari portal cabang ----------------------
	st, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/admin/orders/"+o1+"/notes",
		`{"catatan":"rahasia internal pusat"}`, ac, csrf)
	if st != http.StatusOK {
		t.Fatalf("simpan catatan harus 200, dapat %d (%v)", st, body)
	}
	dc := dataMap(t, body)
	if dc["note_admin"] != "rahasia internal pusat" {
		t.Fatalf("catatan internal tidak tersimpan: %v", dc["note_admin"])
	}
	timeline2, _ := dc["timeline"].([]any)
	if len(timeline2) != 2 {
		t.Fatalf("catatan harus menambah satu entri timeline: %v", dc["timeline"])
	}
	teksCatatan, _ := timeline2[1].(map[string]any)["teks"].(string)
	if !strings.Contains(teksCatatan, "Budi") || !strings.Contains(teksCatatan, "rahasia internal pusat") {
		t.Fatalf("timeline catatan tidak memuat aktor/isi: %q", teksCatatan)
	}

	// Nilai catatan tersimpan di kolom note_admin.
	var noteDB string
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(note_admin,'') FROM orders WHERE id = $1`, o1).Scan(&noteDB); err != nil {
		t.Fatalf("baca note_admin: %v", err)
	}
	if noteDB != "rahasia internal pusat" {
		t.Fatalf("note_admin tidak tersimpan di database: %q", noteDB)
	}

	// Portal cabang TIDAK boleh menerima catatan internal.
	st, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/orders/"+o1, "", cc, csrfC)
	if st != http.StatusOK {
		t.Fatalf("detail cabang harus 200, dapat %d (%v)", st, body)
	}
	if _, ada := dataMap(t, body)["note_admin"]; ada {
		t.Fatalf("catatan internal bocor ke portal cabang: %v", body)
	}
	if strings.Contains(fmt.Sprint(body), "rahasia internal pusat") {
		t.Fatalf("isi catatan internal bocor ke portal cabang: %v", body)
	}

	// --- Manager & Superadmin sama-sama boleh -------------------------------
	sc, csrfSA := login(t, f.srv, "rina@example.test")
	st, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/orders?grup=cabang&per_halaman=1", "", sc, csrfSA)
	if st != http.StatusOK {
		t.Fatalf("Superadmin harus boleh melihat riwayat, dapat %d (%v)", st, body)
	}

	// Bukti hanya untuk admin yang sah: admin boleh (200), cabang 403
	// (sudah diuji di atas).
	st, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/orders/"+o1+"/proofs", "", ac, csrf)
	if st != http.StatusOK {
		t.Fatalf("admin harus boleh melihat bukti, dapat %d (%v)", st, body)
	}
}
