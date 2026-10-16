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
	"fmt"
	"net/http"
	"sync"
	"testing"
)

func TestPGBankRekeningTransaksionalDanNonaktif(t *testing.T) {
	f := newPGFixture(t)
	base := f.srv.URL + "/api/v1"
	ctx := context.Background()

	cabangCookies, cabangCsrf := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	mgrCookies, mgrCsrf := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	saCookies, saCsrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	// Cabang fixture mempunyai assignment sintetis agar suite submit lama tetap
	// dapat berjalan. Kosongkan secara eksplisit untuk membuktikan fail-closed.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
		base+"/admin/branches/"+f.branchA+"/bank-accounts",
		`{"expected_version":1,"bank_account_ids":[]}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("kosongkan assignment fixture harus 200, dapat %d (%v)", status, body)
	}

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
		status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut,
			base+"/admin/branches/"+f.branchA+"/bank-accounts",
			`{"expected_version":2,"bank_account_ids":[]}`, aktor.cookies, aktor.csrf)
		if status != http.StatusForbidden {
			t.Errorf("%s mutasi assignment harus 403, dapat %d", aktor.nama, status)
		}
	}

	// Superadmin membuat dua rekening (satu langsung nonaktif).
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, base+"/admin/bank-accounts",
		`{"bank":"BCA","nama_pemilik":"PT Grahita Indonesia","nomor":"1234567890"}`, saCookies, saCsrf)
	if status != http.StatusCreated {
		t.Fatalf("buat rekening BCA harus 201, dapat %d (%v)", status, body)
	}
	bcaID := body["data"].(map[string]any)["id"].(string)
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		base+"/admin/branches/"+f.branchA+"/bank-accounts",
		`{"expected_version":2,"bank_account_ids":["`+bcaID+`"]}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("assign rekening BCA ke cabang A harus 200, dapat %d (%v)", status, body)
	}

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
	ocrOrderID, ocrTotal := seedPesananSubmitted(t, f, f.branchA, "2026-10-122")
	_ = bodyVerifikasiCocok(t, f, ocrOrderID, &ocrTotal, "")

	// Nonaktifkan rekening BCA, lalu daftar publik kosong.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/bank-accounts/"+bcaID,
		`{"is_active":false}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("nonaktifkan rekening harus 200, dapat %d", status)
	}
	if n := jobOCR(t, f, ocrOrderID); n != 0 {
		t.Fatalf("perubahan policy hanya menandai stale dan tidak boleh auto-enqueue, dapat %d job", n)
	}
	if n := hitungKuery(t, f, `SELECT count(*) FROM verification_logs WHERE order_id=$1`, ocrOrderID); n != 0 {
		t.Fatalf("perubahan policy tidak boleh membuat attempt otomatis, dapat %d", n)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/bank-accounts", "", cabangCookies, "")
	if status != http.StatusOK {
		t.Fatalf("rekening publik harus 200, dapat %d", status)
	}
	if pub = body["data"].([]any); len(pub) != 0 {
		t.Fatalf("tidak ada rekening aktif yang tersisa, dapat %v", pub)
	}

	// Deaktivasi rekening mempertahankan assignment sebagai dormant dan menaikkan
	// versi. Replacement identik boleh mempertahankannya, versi basi konflik,
	// dan rekening dormant yang sudah dicabut tidak boleh ditambahkan lagi.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		base+"/admin/branches/"+f.branchA+"/bank-accounts", "", saCookies, "")
	if status != http.StatusOK {
		t.Fatalf("baca assignment dormant harus 200, dapat %d (%v)", status, body)
	}
	assignment := body["data"].(map[string]any)
	if assignment["version"] != float64(4) {
		t.Fatalf("deaktivasi rekening harus menaikkan versi ke 4, dapat %v", assignment["version"])
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		base+"/admin/branches/"+f.branchA+"/bank-accounts",
		`{"expected_version":4,"bank_account_ids":["`+bcaID+`"]}`, saCookies, saCsrf)
	if status != http.StatusOK || body["meta"].(map[string]any)["changed"] != false {
		t.Fatalf("mempertahankan dormant harus no-op 200, dapat %d (%v)", status, body)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		base+"/admin/branches/"+f.branchA+"/bank-accounts",
		`{"expected_version":3,"bank_account_ids":[]}`, saCookies, saCsrf)
	if status != http.StatusConflict {
		t.Fatalf("versi assignment basi harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "ALLOWLIST_VERSION_CONFLICT")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		base+"/admin/branches/"+f.branchA+"/bank-accounts",
		`{"expected_version":4,"bank_account_ids":[]}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("cabut dormant harus 200, dapat %d (%v)", status, body)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		base+"/admin/branches/"+f.branchA+"/bank-accounts",
		`{"expected_version":5,"bank_account_ids":["`+bcaID+`"]}`, saCookies, saCsrf)
	if status != http.StatusConflict {
		t.Fatalf("rekening nonaktif yang dicabut tak boleh ditambah lagi, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BANK_ACCOUNT_INACTIVE")

	// Tanpa rekening efektif, backend menolak unggah bukti dan submit non-C&C.
	var orderID, productID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO orders (branch_id, status) VALUES ($1,'draft') RETURNING id`, f.branchA).Scan(&orderID); err != nil {
		t.Fatalf("seed order fail-closed: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT id FROM products WHERE kode='P-01'`).Scan(&productID); err != nil {
		t.Fatalf("baca produk fixture: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO order_lines
		(order_id, line_no, jenis, product_id, nama_snapshot, qty, harga_satuan)
		VALUES ($1,1,'product',$2,'Bio Magnetik',1,100000)`, orderID, productID); err != nil {
		t.Fatalf("seed baris order fail-closed: %v", err)
	}
	status = unggahBuktiHTTP(t, base+"/orders/"+orderID+"/proofs", cabangCookies, cabangCsrf,
		map[string][]byte{"bukti.jpg": berkasJPEGUji("allowlist-kosong")})
	if status != http.StatusConflict {
		t.Fatalf("unggah bukti tanpa rekening efektif harus 409, dapat %d", status)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO payment_proofs
		(order_id, storage_key, nama_file, mime_type, ukuran_byte, sha256, uploaded_by_kind, uploaded_by_id)
		VALUES ($1,'proofs/sintetis','bukti.jpg','image/jpeg',4,decode('01020304','hex'),'branch',$2)`, orderID, f.userA); err != nil {
		t.Fatalf("seed bukti untuk uji submit fail-closed: %v", err)
	}
	resp, submitBody := multipartSubmit(t, base+"/orders/"+orderID+"/submit", cabangCookies, cabangCsrf, nil, "false")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("submit tanpa rekening efektif harus 409, dapat %d (%v)", resp.StatusCode, submitBody)
	}
	assertErrorShape(t, submitBody, "BRANCH_BANK_ACCOUNT_UNAVAILABLE")

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

	// Pengelolaan hanya Superadmin; tidak ada lagi permukaan pengaturan
	// lintas-role (GET /api/v1/settings dihapus atas keputusan CEO).
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
	for _, aktor := range []struct {
		nama    string
		cookies []*http.Cookie
	}{{"cabang", cabangCookies}, {"manager", mgrCookies}, {"superadmin", saCookies}} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/settings", "", aktor.cookies, "")
		if status != http.StatusNotFound {
			t.Errorf("%s GET /api/v1/settings harus 404, dapat %d (%v)", aktor.nama, status, body)
		}
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

	var stored string
	var auditCount, settingRows int
	hitungPengaturan := func() (int, int) {
		t.Helper()
		var audit, baris int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE entity='setting'`).Scan(&audit); err != nil {
			t.Fatalf("hitung audit pengaturan: %v", err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM settings`).Scan(&baris); err != nil {
			t.Fatalf("hitung baris pengaturan: %v", err)
		}
		return audit, baris
	}

	// Skema uji mulai kosong (nilai berlaku = default). Mengirim nilai yang
	// SAMA dengan default adalah no-op: tidak ada baris ditulis, tanpa audit.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"biaya_packaging":3000}}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("PUT nilai sama dengan default harus 200, dapat %d", status)
	}
	if auditCount, settingRows = hitungPengaturan(); auditCount != 0 || settingRows != 0 {
		t.Errorf("PUT nilai yang tidak berubah harus no-op, dapat audit=%d baris=%d", auditCount, settingRows)
	}

	// Nilai tidak valid → 400, tidak ada baris tersimpan, tanpa event audit.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"biaya_packaging":-1,"cnc_hari":10}}`, saCookies, saCsrf)
	if status != http.StatusBadRequest {
		t.Fatalf("nilai tidak valid harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
	if auditCount, settingRows = hitungPengaturan(); auditCount != 0 || settingRows != 0 {
		t.Errorf("validasi gagal tidak boleh menyimpan/mencatat apa pun, dapat audit=%d baris=%d", auditCount, settingRows)
	}

	// Batch valid → tersimpan sekaligus + satu audit per kunci yang berubah.
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
	if auditCount, _ = hitungPengaturan(); auditCount != 2 {
		t.Errorf("harus 2 entri audit (dua kunci berubah), dapat %d", auditCount)
	}

	// Mengirim ulang nilai yang sama → tetap 200 tetapi TIDAK menambah audit.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"biaya_packaging":5000,"manager_boleh_revisi":false}}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("PUT nilai sama harus tetap 200, dapat %d", status)
	}
	if auditCount, _ = hitungPengaturan(); auditCount != 2 {
		t.Errorf("PUT nilai sama tidak boleh menambah audit, dapat %d (mau 2)", auditCount)
	}

	// Batch campuran: hanya kunci yang benar-benar berubah yang dicatat.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"biaya_packaging":5000,"cnc_hari":10}}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Fatalf("batch campuran harus 200, dapat %d", status)
	}
	if err := f.pool.QueryRow(ctx, `SELECT value::text FROM settings WHERE key='cnc_hari'`).Scan(&stored); err != nil {
		t.Fatalf("baca cnc_hari: %v", err)
	}
	if stored != "10" {
		t.Errorf("cnc_hari harus tersimpan 10, dapat %s", stored)
	}
	if auditCount, _ = hitungPengaturan(); auditCount != 3 {
		t.Errorf("hanya kunci yang berubah dicatat: mau 3, dapat %d", auditCount)
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

	// Setelan ai: nilai tidak valid ditolak tanpa audit, nilai valid tersimpan
	// dan menambah tepat satu entri audit.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"ai":{"aktif":true,"min_keyakinan":1.5}}}`, saCookies, saCsrf)
	if status != http.StatusBadRequest {
		t.Errorf("ai dengan keyakinan di luar 0..1 harus 400, dapat %d", status)
	}
	if auditCount, _ = hitungPengaturan(); auditCount != 3 {
		t.Errorf("ai tidak valid tidak boleh menambah audit, dapat %d (mau 3)", auditCount)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"ai":{"aktif":true,"min_keyakinan":0.85,"tol_kurang":1000,"tol_lebih":500,"digit_rek":4,"mundur":2}}}`, saCookies, saCsrf)
	if status != http.StatusOK {
		t.Errorf("ai valid harus 200, dapat %d", status)
	}
	if auditCount, _ = hitungPengaturan(); auditCount != 4 {
		t.Errorf("ai valid harus menambah 1 audit, dapat %d (mau 4)", auditCount)
	}
}

func TestPGBankRaceVersionDanDeactivateVsPUT(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	base := f.srv.URL + "/api/v1"
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	insertAccount := func(bankCode, owner, number string) string {
		t.Helper()
		var id string
		if err := f.pool.QueryRow(ctx, `INSERT INTO bank_accounts(bank,nama_pemilik,nomor,is_active)
			VALUES ($1,$2,$3,true) RETURNING id`, bankCode, owner, number).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	accountA := insertAccount("BCA", "Race A", "7000000001")
	accountB := insertAccount("BRI", "Race B", "7000000002")
	var version int64
	if err := f.pool.QueryRow(ctx, `SELECT bank_allowlist_version FROM branches WHERE id=$1`, f.branchA).Scan(&version); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, accountID := range []string{accountA, accountB} {
		accountID := accountID
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, _, _ := apiReq(t, http.DefaultClient, http.MethodPut,
				base+"/admin/branches/"+f.branchA+"/bank-accounts",
				fmt.Sprintf(`{"expected_version":%d,"bank_account_ids":["%s"]}`, version, accountID), cookies, csrf)
			statuses <- status
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	ok, conflict := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Fatalf("status race PUT tak terduga: %d", status)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("dua PUT versi sama harus 1 sukses/1 konflik, dapat %d/%d", ok, conflict)
	}

	raceAccount := insertAccount("BCA", "Race Deactivate", "7000000003")
	if err := f.pool.QueryRow(ctx, `SELECT bank_allowlist_version FROM branches WHERE id=$1`, f.branchA).Scan(&version); err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	statuses = make(chan int, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		status, _, _ := apiReq(t, http.DefaultClient, http.MethodPut,
			base+"/admin/branches/"+f.branchA+"/bank-accounts",
			fmt.Sprintf(`{"expected_version":%d,"bank_account_ids":["%s"]}`, version, raceAccount), cookies, csrf)
		statuses <- status
	}()
	go func() {
		defer wg.Done()
		<-start
		status, _, _ := apiReq(t, http.DefaultClient, http.MethodPut,
			base+"/admin/bank-accounts/"+raceAccount, `{"is_active":false}`, cookies, csrf)
		statuses <- status
	}()
	close(start)
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK && status != http.StatusConflict {
			t.Fatalf("race deactivate/PUT status tak terduga: %d", status)
		}
	}
	var effective bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM branches b
	 JOIN branch_bank_accounts x ON x.branch_id=b.id AND x.is_allowed
	 JOIN bank_accounts a ON a.id=x.bank_account_id AND a.is_active
	 WHERE b.id=$1 AND b.is_active AND a.id=$2)`, f.branchA, raceAccount).Scan(&effective); err != nil {
		t.Fatal(err)
	}
	if effective {
		t.Fatal("race deactivate/PUT tidak boleh meninggalkan rekening efektif nonaktif")
	}
}
