package httpapi

// Uji integrasi rekening bank & pengaturan sistem pada PostgreSQL nyata.
//
// Berbeda dari router_bank_test.go/router_settings_test.go yang memakai
// penyimpanan in-memory, berkas ini menjalankan migrasi sungguhan lalu
// memakai repository asli (bank/postgres, settings/postgres) sehingga
// perilaku SQL — constraint unik (bank,nomor), audit transaksional, dan
// snapshot harga order line — benar-benar dieksekusi.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi (CI "migrasi").

import (
	"context"
	"net/http"
	"testing"
)

func TestPGBankRekeningTransaksionalDanNonaktif(t *testing.T) {
	f := newPGFixture(t)
	base := f.srv.URL + "/api/v1"
	ctx := context.Background()

	cabangCookies, cabangCsrf := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	mgrCookies, mgrCsrf := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	saCookies, saCsrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	// Otorisasi: cabang & Manager ditolak (PRD §2.1).
	for _, aktor := range []struct {
		nama    string
		cookies []*http.Cookie
		csrf    string
	}{{"cabang", cabangCookies, cabangCsrf}, {"manager", mgrCookies, mgrCsrf}} {
		status, _, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/bank-accounts", "", aktor.cookies, "")
		if status != http.StatusForbidden {
			t.Errorf("%s daftar rekening admin harus 403, dapat %d", aktor.nama, status)
		}
		status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/bank-accounts/apa-saja",
			`{"is_active":false}`, aktor.cookies, aktor.csrf)
		if status != http.StatusForbidden {
			t.Errorf("%s ubah rekening harus 403, dapat %d", aktor.nama, status)
		}
	}

	// Superadmin membuat dua rekening (satu langsung nonaktif).
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, base+"/admin/bank-accounts",
		`{"bank":"BCA","nama_pemilik":"PT Grahita Indonesia","nomor":"1234567890"}`, saCookies, saCsrf)
	if status != http.StatusCreated {
		t.Fatalf("buat rekening BCA harus 201, dapat %d (%v)", status, body)
	}
	bcaID := body["data"].(map[string]any)["id"].(string)

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, base+"/admin/bank-accounts",
		`{"bank":"BRI","nama_pemilik":"PT Grahita Indonesia","nomor":"0987654321","is_active":false}`, saCookies, saCsrf)
	if status != http.StatusCreated {
		t.Fatalf("buat rekening BRI harus 201, dapat %d (%v)", status, body)
	}

	// Duplikat (bank,nomor) ditolak oleh constraint unik → 409 BANK_DUPLICATE.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, base+"/admin/bank-accounts",
		`{"bank":"BCA","nama_pemilik":"PT Lain","nomor":"1234567890"}`, saCookies, saCsrf)
	if status != http.StatusConflict {
		t.Fatalf("duplikat harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BANK_DUPLICATE")

	// Rekening nonaktif tidak muncul di daftar publik; yang aktif muncul.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/bank-accounts", "", cabangCookies, "")
	if status != http.StatusOK {
		t.Fatalf("rekening publik harus 200, dapat %d", status)
	}
	pub := body["data"].([]any)
	if len(pub) != 1 || pub[0].(map[string]any)["id"] != bcaID {
		t.Fatalf("hanya rekening aktif yang boleh tampil, dapat %v", pub)
	}

	// Nonaktifkan rekening BCA, lalu daftar publik kosong.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/bank-accounts/"+bcaID,
		`{"is_active":false}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("nonaktifkan rekening harus 200, dapat %d", status)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/bank-accounts", "", cabangCookies, "")
	if status != http.StatusOK {
		t.Fatalf("rekening publik harus 200, dapat %d", status)
	}
	if pub = body["data"].([]any); len(pub) != 0 {
		t.Fatalf("tidak ada rekening aktif yang tersisa, dapat %v", pub)
	}

	// Audit: 2 create + 1 update tercatat; percobaan duplikat TIDAK meninggalkan
	// event (rollback transaksi yang sama dengan mutasinya).
	var total, creates, updates int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE entity = 'bank_account'`).Scan(&total); err != nil {
		t.Fatalf("hitung audit rekening: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE entity='bank_account' AND action='create'`).Scan(&creates); err != nil {
		t.Fatalf("hitung audit create: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE entity='bank_account' AND action='update'`).Scan(&updates); err != nil {
		t.Fatalf("hitung audit update: %v", err)
	}
	if total != 3 || creates != 2 || updates != 1 {
		t.Errorf("audit rekening salah: total=%d create=%d update=%d (mau 3/2/1)", total, creates, updates)
	}
	// Aktor & snapshot audit terisi.
	var actorID, before, after string
	if err := f.pool.QueryRow(ctx,
		`SELECT actor_id::text, before::text, after::text FROM audit_logs
		  WHERE entity='bank_account' AND action='update' ORDER BY at DESC LIMIT 1`).Scan(&actorID, &before, &after); err != nil {
		t.Fatalf("baca audit update: %v", err)
	}
	if actorID != f.adminSA {
		t.Errorf("aktor audit harus superadmin (%s), dapat %s", f.adminSA, actorID)
	}
	if before == "" || after == "" {
		t.Errorf("snapshot before/after audit harus terisi: %q %q", before, after)
	}
}

func TestPGSettingsValidasiAuditDanSnapshotPackaging(t *testing.T) {
	f := newPGFixture(t)
	base := f.srv.URL + "/api/v1"
	ctx := context.Background()

	cabangCookies, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	mgrCookies, _ := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	saCookies, saCsrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	// Pengelolaan hanya Superadmin; konstanta kedua portal tanpa ai.
	status, _, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/settings", "", mgrCookies, "")
	if status != http.StatusForbidden {
		t.Errorf("manager baca pengaturan harus 403, dapat %d", status)
	}
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/settings", "", saCookies, "")
	if status != http.StatusOK {
		t.Fatalf("superadmin baca pengaturan harus 200, dapat %d", status)
	}
	data := body["data"].(map[string]any)
	if _, ok := data["ai"]; !ok {
		t.Error("pengaturan Superadmin harus memuat ai")
	}
	if data["biaya_packaging"] != float64(3000) {
		t.Errorf("default biaya_packaging harus 3000, dapat %v", data["biaya_packaging"])
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/settings", "", cabangCookies, "")
	if status != http.StatusOK {
		t.Fatalf("cabang baca konstanta harus 200, dapat %d", status)
	}
	if _, ok := body["data"].(map[string]any)["ai"]; ok {
		t.Error("konstanta kedua portal tidak boleh memuat ai")
	}

	// Simpan pesanan lama yang memakai biaya packaging 3000 (snapshot).
	var orderID string
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO orders (branch_id, status) VALUES ($1,'draft') RETURNING id`, f.branchA).Scan(&orderID); err != nil {
		t.Fatalf("seed pesanan: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO order_lines (order_id, line_no, jenis, nama_snapshot, qty, harga_satuan)
		 VALUES ($1, 1, 'packaging', 'Biaya packaging', 1, 3000)`, orderID); err != nil {
		t.Fatalf("seed baris packaging: %v", err)
	}

	// Fixture mengosongkan tabel settings, jadi nilai awal ditulis lewat API.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"biaya_packaging":3000}}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("set biaya_packaging awal harus 200, dapat %d", status)
	}

	// Nilai tidak valid → 400, nilai lama tidak berubah, tanpa event audit baru.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"biaya_packaging":-1,"cnc_hari":10}}`, saCookies, saCsrf)
	if status != http.StatusBadRequest {
		t.Fatalf("nilai tidak valid harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
	var stored string
	if err := f.pool.QueryRow(ctx, `SELECT value::text FROM settings WHERE key='biaya_packaging'`).Scan(&stored); err != nil {
		t.Fatalf("baca pengaturan: %v", err)
	}
	if stored != "3000" {
		t.Errorf("nilai lama tidak boleh berubah saat validasi gagal, dapat %s", stored)
	}
	var auditCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE entity='setting'`).Scan(&auditCount); err != nil {
		t.Fatalf("hitung audit pengaturan: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("validasi gagal tidak boleh mencatat audit baru, dapat %d (mau 1)", auditCount)
	}

	// Batch valid → tersimpan sekaligus + satu audit per kunci.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"biaya_packaging":5000,"manager_boleh_revisi":false}}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("batch valid harus 200, dapat %d (%v)", status, body)
	}
	if err := f.pool.QueryRow(ctx, `SELECT value::text FROM settings WHERE key='biaya_packaging'`).Scan(&stored); err != nil {
		t.Fatalf("baca pengaturan setelah simpan: %v", err)
	}
	if stored != "5000" {
		t.Errorf("biaya_packaging harus tersimpan 5000, dapat %s", stored)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE entity='setting'`).Scan(&auditCount); err != nil {
		t.Fatalf("hitung audit pengaturan: %v", err)
	}
	if auditCount != 3 {
		t.Errorf("harus 3 entri audit (1 + 2), dapat %d", auditCount)
	}

	// Perubahan biaya packaging TIDAK memengaruhi pesanan lama (BR-05/CP-SER-05):
	// harga di-snapshot pada order line saat submit.
	var harga, subtotal, total int64
	if err := f.pool.QueryRow(ctx, `SELECT harga_satuan, subtotal FROM order_lines WHERE order_id=$1`, orderID).Scan(&harga, &subtotal); err != nil {
		t.Fatalf("baca order line: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT total FROM orders WHERE id=$1`, orderID).Scan(&total); err != nil {
		t.Fatalf("baca total pesanan: %v", err)
	}
	if harga != 3000 || subtotal != 3000 || total != 3000 {
		t.Errorf("pesanan lama harus tetap 3000 (harga=%d subtotal=%d total=%d)", harga, subtotal, total)
	}

	// Setelan ai dapat disimpan dan divalidasi rentangnya.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"ai":{"aktif":true,"min_keyakinan":1.5}}}`, saCookies, saCsrf)
	if status != http.StatusBadRequest {
		t.Errorf("ai dengan keyakinan di luar 0..1 harus 400, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"ai":{"aktif":true,"min_keyakinan":0.85,"tol_kurang":1000,"tol_lebih":500,"digit_rek":4,"mundur":2}}}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Errorf("ai valid harus 200, dapat %d", status)
	}
}
