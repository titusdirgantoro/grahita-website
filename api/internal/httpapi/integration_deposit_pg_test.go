package httpapi

// Uji integrasi deposit cabang C2 (D3 §6, §12) terhadap PostgreSQL nyata.
// Dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"
	"sync"
	"testing"
)

// depositReq mengirim request JSON/kosong dengan header Idempotency-Key.
func depositReq(t *testing.T, method, url, body string, cookies []*http.Cookie, csrf, key string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("susun request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return kirimDeposit(t, req, cookies, csrf, key)
}

func kirimDeposit(t *testing.T, req *http.Request, cookies []*http.Cookie, csrf, key string) (int, map[string]any) {
	t.Helper()
	for _, c := range cookies {
		req.AddCookie(c)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kirim request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// topupHTTP mengajukan top-up multipart (proof, declared_amount, bank_account_id).
func topupHTTP(t *testing.T, f *pgFixture, cookies []*http.Cookie, csrf, key, bankID string, amount int64, isi []byte) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if isi != nil {
		fw, err := mw.CreateFormFile("proof", "bukti-topup.jpg")
		if err != nil {
			t.Fatalf("form file: %v", err)
		}
		_, _ = fw.Write(isi)
	}
	_ = mw.WriteField("declared_amount", strconv.FormatInt(amount, 10))
	_ = mw.WriteField("bank_account_id", bankID)
	_ = mw.Close()
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/api/v1/deposit/topups", &buf)
	if err != nil {
		t.Fatalf("susun request top-up: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return kirimDeposit(t, req, cookies, csrf, key)
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func dataObj(body map[string]any) map[string]any {
	d, _ := body["data"].(map[string]any)
	return d
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// saldoCabang membaca GET /deposit cabang sesi.
func saldoCabang(t *testing.T, f *pgFixture, cookies []*http.Cookie) (int64, int64, int64, int64) {
	t.Helper()
	status, body := depositReq(t, http.MethodGet, f.srv.URL+"/api/v1/deposit", "", cookies, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET /deposit harus 200, dapat %d (%v)", status, body)
	}
	d := dataObj(body)
	return num(d["available_balance"]), num(d["journal_version"]), num(d["pending_topup_count"]),
		num(d["pending_topup_amount_declared"])
}

// kreditCabang menambah saldo cabang lewat alur sah: top-up cabang lalu verify
// Superadmin. Mengembalikan versi journal sesudahnya.
func kreditCabang(t *testing.T, f *pgFixture, branchCookies []*http.Cookie, branchCSRF string, amount int64, tag string) int64 {
	t.Helper()
	status, body := topupHTTP(t, f, branchCookies, branchCSRF, "topup-"+tag, f.bankDefault, amount, berkasJPEGUji("kredit-"+tag))
	if status != http.StatusCreated {
		t.Fatalf("top-up %s harus 201, dapat %d (%v)", tag, status, body)
	}
	topupID := dataObj(body)["id"].(string)
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, body = depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/admin/deposit-topups/"+topupID+"/decision",
		fmt.Sprintf(`{"action":"verify","approved_amount":%d}`, amount), cookiesSA, csrfSA, "verify-"+tag)
	if status != http.StatusOK {
		t.Fatalf("verify %s harus 200, dapat %d (%v)", tag, status, body)
	}
	return num(dataObj(body)["balance"].(map[string]any)["journal_version"])
}

// jadikanCNC menandai cabang sebagai Cash & Carry: hanya cabang ini yang
// memakai deposit (top-up dan pembayaran pesanan dari saldo).
func jadikanCNC(t *testing.T, f *pgFixture, branchIDs ...string) {
	t.Helper()
	for _, id := range branchIDs {
		if _, err := f.pool.Exec(context.Background(), `UPDATE branches SET is_cnc = true, allow_transfer = false WHERE id = $1`, id); err != nil {
			t.Fatalf("tandai cabang C&C: %v", err)
		}
	}
}

func insertOrderSubmitted(t *testing.T, f *pgFixture, branchID, nomor string, total int64) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO orders
		(branch_id, nomor, versi, status, verif_status, total, submitted_at, tanggal)
		VALUES ($1,$2,0,'submitted','belum',$3,now(),current_date) RETURNING id`,
		branchID, nomor, total).Scan(&id); err != nil {
		t.Fatalf("semai pesanan submitted %s: %v", nomor, err)
	}
	return id
}

// TestPGDepositTopupPendingTidakMenambahSaldo: top-up selalu pending, saldo
// tidak berubah, bukti identik idempoten, bukti dipakai ulang untuk permintaan
// lain ditolak, dan rekening di luar allowlist D2 ditolak fail-closed.
func TestPGDepositTopupPendingTidakMenambahSaldo(t *testing.T) {
	f := newPGFixture(t)
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	isi := berkasJPEGUji("topup-a-1")

	// Deposit milik cabang Cash & Carry: cabang lain tidak bisa top-up.
	status, body := topupHTTP(t, f, cookiesA, csrfA, "k-0", f.bankDefault, 150000, berkasJPEGUji("topup-a-0"))
	if status != http.StatusForbidden || errCode(body) != "DEPOSIT_CNC_ONLY" {
		t.Fatalf("top-up cabang non-C&C harus 403 DEPOSIT_CNC_ONLY, dapat %d (%v)", status, body)
	}
	jadikanCNC(t, f, f.branchA)

	// DEP-03: berkas wajib, isi harus cocok dengan ekstensi, dan batas ukuran
	// per berkas mengikuti setting (default 10 MB). Semua kegagalan terjadi
	// tanpa baris top-up/payment_proof maupun objek yatim.
	objekSebelum := f.objek.jumlah()
	invalid := []struct {
		nama string
		key  string
		isi  []byte
	}{
		{nama: "tanpa bukti", key: "invalid-empty", isi: nil},
		{nama: "PDF berkedok JPG", key: "invalid-mime", isi: berkasPDFUji("palsu")},
		{nama: "lebih dari 10 MB", key: "invalid-large", isi: append(berkasJPEGUji("besar"), bytes.Repeat([]byte{'x'}, 10*1024*1024)...)},
	}
	for _, tc := range invalid {
		status, body = topupHTTP(t, f, cookiesA, csrfA, tc.key, f.bankDefault, 150000, tc.isi)
		if status < 400 || status >= 500 {
			t.Fatalf("%s harus ditolak 4xx, dapat %d (%v)", tc.nama, status, body)
		}
	}
	var invalidTopups, invalidProofs int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM deposit_topups`).Scan(&invalidTopups); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM payment_proofs WHERE purpose='deposit_topup'`).Scan(&invalidProofs); err != nil {
		t.Fatal(err)
	}
	if invalidTopups != 0 || invalidProofs != 0 || f.objek.jumlah() != objekSebelum {
		t.Fatalf("unggahan gagal meninggalkan data: topup=%d proof=%d objek=%d (sebelum=%d)",
			invalidTopups, invalidProofs, f.objek.jumlah(), objekSebelum)
	}

	status, body = topupHTTP(t, f, cookiesA, csrfA, "", f.bankDefault, 150000, isi)
	if status != http.StatusBadRequest {
		t.Fatalf("tanpa Idempotency-Key harus 400, dapat %d (%v)", status, body)
	}
	status, body = topupHTTP(t, f, cookiesA, csrfA, "k-1", f.bankDefault, 150000, isi)
	if status != http.StatusCreated || dataObj(body)["status"] != "pending_review" {
		t.Fatalf("top-up harus 201 pending_review, dapat %d (%v)", status, body)
	}
	topupID := dataObj(body)["id"].(string)
	saldo, versi, pending, pendingNominal := saldoCabang(t, f, cookiesA)
	if saldo != 0 || versi != 0 || pending != 1 || pendingNominal != 150000 {
		t.Fatalf("pending tidak boleh menambah saldo: saldo=%d versi=%d pending=%d nominal=%d",
			saldo, versi, pending, pendingNominal)
	}

	status, body = topupHTTP(t, f, cookiesA, csrfA, "k-1-retry", f.bankDefault, 150000, isi)
	if status != http.StatusOK || dataObj(body)["id"] != topupID {
		t.Fatalf("bukti identik untuk permintaan sama harus replay 200, dapat %d (%v)", status, body)
	}
	status, body = topupHTTP(t, f, cookiesA, csrfA, "k-2", f.bankDefault, 175000, isi)
	if status != http.StatusConflict || errCode(body) != "PAYMENT_PROOF_ALREADY_USED" {
		t.Fatalf("bukti untuk nominal lain harus 409 PAYMENT_PROOF_ALREADY_USED, dapat %d (%v)", status, body)
	}
	var lain string
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO bank_accounts (bank, nama_pemilik, nomor, is_active)
		VALUES ('BRI','PT Lain','1111222233',true) RETURNING id`).Scan(&lain); err != nil {
		t.Fatalf("seed rekening lain: %v", err)
	}
	status, body = topupHTTP(t, f, cookiesA, csrfA, "k-3", lain, 50000, berkasJPEGUji("topup-a-2"))
	if status != http.StatusConflict || errCode(body) != "BANK_ACCOUNT_NOT_ALLOWED" {
		t.Fatalf("rekening di luar allowlist harus 409 BANK_ACCOUNT_NOT_ALLOWED, dapat %d (%v)", status, body)
	}

	cookiesB, _ := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	status, _ = depositReq(t, http.MethodGet, f.srv.URL+"/api/v1/deposit/topups/"+topupID+"/proof", "", cookiesB, "", "")
	if status != http.StatusNotFound {
		t.Fatalf("cabang lain tidak boleh melihat bukti top-up, dapat %d", status)
	}
	if _, _, pending, _ := saldoCabang(t, f, cookiesB); pending != 0 {
		t.Fatalf("pending cabang A tidak boleh terlihat di cabang B, dapat %d", pending)
	}
}

// TestPGDepositKreditHanyaSuperadmin: Manager boleh review/reject tetapi tidak
// menciptakan kredit; verify Superadmin menambah saldo sekali (retry idempoten),
// dan keputusan kedua ditolak.
func TestPGDepositKreditHanyaSuperadmin(t *testing.T) {
	f := newPGFixture(t)
	jadikanCNC(t, f, f.branchA)
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	status, body := topupHTTP(t, f, cookiesA, csrfA, "k-1", f.bankDefault, 200000, berkasJPEGUji("kredit-1"))
	if status != http.StatusCreated {
		t.Fatalf("top-up harus 201, dapat %d (%v)", status, body)
	}
	topupID := dataObj(body)["id"].(string)
	url := f.srv.URL + "/api/v1/admin/deposit-topups/" + topupID + "/decision"

	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body = depositReq(t, http.MethodPost, url, `{"action":"verify","approved_amount":200000}`, cookiesMgr, csrfMgr, "mgr-v")
	if status != http.StatusForbidden {
		t.Fatalf("Manager tidak boleh menciptakan kredit, dapat %d (%v)", status, body)
	}
	status, body = depositReq(t, http.MethodPost, url, `{"action":"review"}`, cookiesMgr, csrfMgr, "")
	if status != http.StatusBadRequest {
		t.Fatalf("review tanpa alasan harus 400, dapat %d (%v)", status, body)
	}
	status, body = depositReq(t, http.MethodPost, url, `{"action":"review","reason":"Nominal perlu dicek"}`, cookiesMgr, csrfMgr, "")
	if status != http.StatusOK || dataObj(body)["topup"].(map[string]any)["status"] != "needs_review" {
		t.Fatalf("review Manager harus needs_review, dapat %d (%v)", status, body)
	}

	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, body = depositReq(t, http.MethodPost, url, `{"action":"verify","approved_amount":180000}`, cookiesSA, csrfSA, "sa-v")
	if status != http.StatusBadRequest {
		t.Fatalf("nominal final berbeda tanpa alasan harus 400, dapat %d (%v)", status, body)
	}
	verify := `{"action":"verify","approved_amount":200000,"reason":"Nominal dan rekening cocok"}`
	status, body = depositReq(t, http.MethodPost, url, verify, cookiesSA, csrfSA, "sa-v1")
	if status != http.StatusOK || dataObj(body)["topup"].(map[string]any)["status"] != "verified" {
		t.Fatalf("verify Superadmin harus verified, dapat %d (%v)", status, body)
	}
	if saldo, versi, pending, _ := saldoCabang(t, f, cookiesA); saldo != 200000 || versi != 1 || pending != 0 {
		t.Fatalf("saldo setelah kredit salah: saldo=%d versi=%d pending=%d", saldo, versi, pending)
	}
	status, body = depositReq(t, http.MethodPost, url, verify, cookiesSA, csrfSA, "sa-v1")
	if status != http.StatusOK || dataObj(body)["replayed"] != true {
		t.Fatalf("retry key sama harus replay, dapat %d (%v)", status, body)
	}
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != 200000 {
		t.Fatalf("retry tidak boleh mengkredit dua kali, saldo=%d", saldo)
	}
	berbeda := `{"action":"verify","approved_amount":190000,"reason":"Nominal berbeda"}`
	status, body = depositReq(t, http.MethodPost, url, berbeda, cookiesSA, csrfSA, "sa-v1")
	if status != http.StatusConflict || errCode(body) != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("key keputusan sama dengan isi berbeda harus 409, dapat %d (%v)", status, body)
	}
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != 200000 {
		t.Fatalf("key dipakai ulang tidak boleh mengubah saldo: %d", saldo)
	}
	status, body = depositReq(t, http.MethodPost, url, verify, cookiesSA, csrfSA, "sa-v2")
	if status != http.StatusConflict {
		t.Fatalf("keputusan kedua harus 409, dapat %d (%v)", status, body)
	}
	var audit int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs
		WHERE entity='deposit_topup' AND entity_id=$1 AND action='review'`, topupID).Scan(&audit); err != nil || audit != 1 {
		t.Fatalf("review harus tercatat sekali di audit, dapat %d (err %v)", audit, err)
	}

	// Manager boleh menolak dengan alasan, tetapi penolakan tidak menambah
	// saldo. Tanpa alasan tetap ditolak sebelum keputusan tersimpan.
	status, body = topupHTTP(t, f, cookiesA, csrfA, "k-reject", f.bankDefault, 125000, berkasJPEGUji("kredit-reject"))
	if status != http.StatusCreated {
		t.Fatalf("top-up untuk reject harus 201, dapat %d (%v)", status, body)
	}
	rejectID := dataObj(body)["id"].(string)
	rejectURL := f.srv.URL + "/api/v1/admin/deposit-topups/" + rejectID + "/decision"
	status, body = depositReq(t, http.MethodPost, rejectURL, `{"action":"reject"}`, cookiesMgr, csrfMgr, "")
	if status != http.StatusBadRequest {
		t.Fatalf("reject tanpa alasan harus 400, dapat %d (%v)", status, body)
	}
	status, body = depositReq(t, http.MethodPost, rejectURL,
		`{"action":"reject","reason":"Bukti tidak menunjukkan transfer"}`, cookiesMgr, csrfMgr, "")
	if status != http.StatusOK || dataObj(body)["topup"].(map[string]any)["status"] != "rejected" {
		t.Fatalf("reject Manager harus rejected, dapat %d (%v)", status, body)
	}
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != 200000 {
		t.Fatalf("reject tidak boleh menambah saldo, dapat %d", saldo)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs
		WHERE entity='deposit_topup' AND entity_id=$1 AND action='reject'`, rejectID).Scan(&audit); err != nil || audit != 1 {
		t.Fatalf("reject harus tercatat sekali di audit, dapat %d (err %v)", audit, err)
	}

	// Superadmin boleh menyetujui nominal berbeda hanya dengan alasan. Nilai
	// journal, saldo, audit, dan sumber bukti harus memakai nominal final.
	status, body = topupHTTP(t, f, cookiesA, csrfA, "k-different", f.bankDefault, 200000, berkasJPEGUji("kredit-different"))
	if status != http.StatusCreated {
		t.Fatalf("top-up nominal berbeda harus 201, dapat %d (%v)", status, body)
	}
	differentID := dataObj(body)["id"].(string)
	differentURL := f.srv.URL + "/api/v1/admin/deposit-topups/" + differentID + "/decision"
	status, body = depositReq(t, http.MethodPost, differentURL,
		`{"action":"verify","approved_amount":180000,"reason":"Mutasi bank yang sah hanya Rp180.000"}`,
		cookiesSA, csrfSA, "sa-different")
	if status != http.StatusOK || dataObj(body)["topup"].(map[string]any)["status"] != "verified" {
		t.Fatalf("verify nominal berbeda beralasan harus 200, dapat %d (%v)", status, body)
	}
	if saldo, versi, _, _ := saldoCabang(t, f, cookiesA); saldo != 380000 || versi != 2 {
		t.Fatalf("saldo harus bertambah tepat nominal final: saldo=%d versi=%d", saldo, versi)
	}
	var approvedAmount int64
	var proofLinked, auditLinked int
	if err := f.pool.QueryRow(context.Background(), `SELECT t.amount,
		(t.source_proof_id=dt.payment_proof_id)::int,
		EXISTS(SELECT 1 FROM audit_logs a WHERE a.entity='deposit_topup' AND a.entity_id=dt.id AND a.action='verify')::int
		FROM deposit_topups dt
		JOIN deposit_verification_logs v ON v.topup_id=dt.id
		JOIN branch_deposit_transactions t ON t.deposit_verification_log_id=v.id
		WHERE dt.id=$1 AND t.kind='topup_credit'`, differentID).Scan(&approvedAmount, &proofLinked, &auditLinked); err != nil {
		t.Fatalf("baca jurnal top-up nominal berbeda: %v", err)
	}
	if approvedAmount != 180000 || proofLinked != 1 || auditLinked != 1 {
		t.Fatalf("jurnal/audit/sumber bukti salah: amount=%d proof=%d audit=%d", approvedAmount, proofLinked, auditLinked)
	}
	status, body = depositReq(t, http.MethodPost, differentURL,
		`{"action":"verify","approved_amount":180000,"reason":"Mutasi bank yang sah hanya Rp180.000"}`,
		cookiesSA, csrfSA, "sa-different")
	if status != http.StatusOK || dataObj(body)["replayed"] != true {
		t.Fatalf("retry verify top-up harus replay, dapat %d (%v)", status, body)
	}
	var logs, journals, audits int
	if err := f.pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM deposit_verification_logs WHERE topup_id=$1 AND result='verified'),
		(SELECT count(*) FROM branch_deposit_transactions t JOIN deposit_verification_logs v ON v.id=t.deposit_verification_log_id WHERE v.topup_id=$1),
		(SELECT count(*) FROM audit_logs WHERE entity='deposit_topup' AND entity_id=$1 AND action='verify')`,
		differentID).Scan(&logs, &journals, &audits); err != nil {
		t.Fatalf("hitung replay top-up: %v", err)
	}
	if logs != 1 || journals != 1 || audits != 1 {
		t.Fatalf("retry tidak boleh menggandakan log/jurnal/audit: log=%d journal=%d audit=%d", logs, journals, audits)
	}
}

// TestPGDepositAlokasiParalelDanIsolasi: alokasi paralel ke beberapa order dari
// saldo yang sama tidak pernah melebihi saldo; cabang lain dan versi basi
// ditolak; saldo kurang ditolak tanpa pengurangan diam-diam.
func TestPGDepositAlokasiParalelDanIsolasi(t *testing.T) {
	f := newPGFixture(t)
	jadikanCNC(t, f, f.branchA)
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	versi := kreditCabang(t, f, cookiesA, csrfA, 100000, "a")

	cookiesB, csrfB := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	orderA := insertOrderSubmitted(t, f, f.branchA, "2026-10-1001", 80000)
	body := fmt.Sprintf(`{"amount":10000,"expected_journal_version":%d,"expected_order_version":0}`, versi)
	status, res := depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orderA+"/deposit-allocations", body, cookiesA, csrfA, "")
	if status != http.StatusBadRequest {
		t.Fatalf("alokasi tanpa Idempotency-Key harus 400, dapat %d (%v)", status, res)
	}
	melebihi := fmt.Sprintf(`{"amount":90000,"expected_journal_version":%d,"expected_order_version":0}`, versi)
	status, res = depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orderA+"/deposit-allocations", melebihi, cookiesA, csrfA, "a-over-order")
	if status != http.StatusConflict || errCode(res) != "DEPOSIT_ALLOCATION_EXCEEDS_ORDER" {
		t.Fatalf("alokasi melebihi sisa order harus 409 DEPOSIT_ALLOCATION_EXCEEDS_ORDER, dapat %d (%v)", status, res)
	}
	orderDone := insertOrder(t, f, f.branchA, "done", "2026-10-1002")
	status, res = depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orderDone+"/deposit-allocations", body, cookiesA, csrfA, "a-done")
	if status != http.StatusConflict || errCode(res) != "ORDER_ALREADY_DONE" {
		t.Fatalf("alokasi ke order Done harus 409 ORDER_ALREADY_DONE, dapat %d (%v)", status, res)
	}
	orderVersionStale := fmt.Sprintf(`{"amount":10000,"expected_journal_version":%d,"expected_order_version":99}`, versi)
	status, res = depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orderA+"/deposit-allocations", orderVersionStale, cookiesA, csrfA, "a-order-stale")
	if status != http.StatusConflict || errCode(res) != "DEPOSIT_VERSION_CONFLICT" {
		t.Fatalf("versi order basi harus 409 DEPOSIT_VERSION_CONFLICT, dapat %d (%v)", status, res)
	}
	if saldo, currentVersion, _, _ := saldoCabang(t, f, cookiesA); saldo != 100000 || currentVersion != versi {
		t.Fatalf("seluruh alokasi ditolak tidak boleh mendebit: saldo=%d versi=%d", saldo, currentVersion)
	}
	status, res = depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orderA+"/deposit-allocations", body, cookiesB, csrfB, "b-1")
	if status != http.StatusNotFound {
		t.Fatalf("cabang lain harus 404, dapat %d (%v)", status, res)
	}
	stale := `{"amount":10000,"expected_journal_version":0,"expected_order_version":0}`
	status, res = depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orderA+"/deposit-allocations", stale, cookiesA, csrfA, "a-stale")
	if status != http.StatusConflict || errCode(res) != "DEPOSIT_VERSION_CONFLICT" {
		t.Fatalf("versi basi harus 409 DEPOSIT_VERSION_CONFLICT, dapat %d (%v)", status, res)
	}

	const paralel = 5
	orders := make([]string, paralel)
	for i := range orders {
		orders[i] = insertOrderSubmitted(t, f, f.branchA, fmt.Sprintf("2026-10-20%02d", i), 80000)
	}
	var wg sync.WaitGroup
	statuses := make([]int, paralel)
	mulai := make(chan struct{})
	for i := range orders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-mulai
			b := fmt.Sprintf(`{"amount":60000,"expected_journal_version":%d,"expected_order_version":0}`, versi)
			statuses[i], _ = depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orders[i]+"/deposit-allocations",
				b, cookiesA, csrfA, fmt.Sprintf("par-%d", i))
		}(i)
	}
	close(mulai)
	wg.Wait()
	sukses := 0
	for _, s := range statuses {
		switch s {
		case http.StatusCreated:
			sukses++
		case http.StatusConflict:
		default:
			t.Fatalf("status alokasi paralel tak terduga: %v", statuses)
		}
	}
	if sukses != 1 {
		t.Fatalf("hanya satu alokasi 60000 dari saldo 100000 yang boleh lolos, dapat %d (%v)", sukses, statuses)
	}
	saldo, versi2, _, _ := saldoCabang(t, f, cookiesA)
	if saldo != 40000 || versi2 != versi+1 {
		t.Fatalf("saldo setelah alokasi paralel salah: saldo=%d versi=%d", saldo, versi2)
	}

	// Satu alokasi sah dan replay dengan key/payload yang sama hanya menulis
	// satu debit serta satu allocation.
	replayBody := fmt.Sprintf(`{"amount":10000,"expected_journal_version":%d,"expected_order_version":0}`, versi2)
	status, firstReplay := depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orderA+"/deposit-allocations",
		replayBody, cookiesA, csrfA, "a-replay")
	if status != http.StatusCreated {
		t.Fatalf("alokasi untuk replay harus 201, dapat %d (%v)", status, firstReplay)
	}
	status, res = depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orderA+"/deposit-allocations",
		replayBody, cookiesA, csrfA, "a-replay")
	if status != http.StatusOK || dataObj(res)["replayed"] != true {
		t.Fatalf("replay alokasi harus 200 replayed, dapat %d (%v)", status, res)
	}
	versi2 = num(dataObj(firstReplay)["balance"].(map[string]any)["journal_version"])
	if n := hitungSQL(t, f, `SELECT count(*) FROM deposit_allocations WHERE order_id=$1`, orderA); n != 1 {
		t.Fatalf("replay harus menyisakan satu allocation, dapat %d", n)
	}
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != 30000 {
		t.Fatalf("replay tidak boleh mendebit dua kali, saldo=%d", saldo)
	}
	var journal, cache int64
	if err := f.pool.QueryRow(context.Background(), `SELECT
		(SELECT COALESCE(sum(amount),0) FROM branch_deposit_transactions WHERE branch_id=$1),
		(SELECT balance FROM branch_deposit_accounts WHERE branch_id=$1)`, f.branchA).Scan(&journal, &cache); err != nil || journal != cache {
		t.Fatalf("cache saldo harus sama dengan journal: journal=%d cache=%d err=%v", journal, cache, err)
	}

	kurang := fmt.Sprintf(`{"amount":50000,"expected_journal_version":%d,"expected_order_version":0}`, versi2)
	status, res = depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orderA+"/deposit-allocations", kurang, cookiesA, csrfA, "a-kurang")
	if status != http.StatusConflict || errCode(res) != "DEPOSIT_BALANCE_INSUFFICIENT" {
		t.Fatalf("saldo kurang harus 409 DEPOSIT_BALANCE_INSUFFICIENT, dapat %d (%v)", status, res)
	}
}

// TestPGDepositReversalAdmin: reversal hanya oleh admin beralasan, parsial
// bertahap, tidak melebihi bagian aktif, dan ditolak untuk order Done.
func TestPGDepositReversalAdmin(t *testing.T) {
	f := newPGFixture(t)
	jadikanCNC(t, f, f.branchA)
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	versi := kreditCabang(t, f, cookiesA, csrfA, 100000, "rev")
	orderID := insertOrderSubmitted(t, f, f.branchA, "2026-10-3001", 90000)
	status, res := depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orderID+"/deposit-allocations",
		fmt.Sprintf(`{"amount":60000,"expected_journal_version":%d,"expected_order_version":0}`, versi), cookiesA, csrfA, "alok-1")
	if status != http.StatusCreated {
		t.Fatalf("alokasi harus 201, dapat %d (%v)", status, res)
	}
	d := dataObj(res)
	allocID := d["allocation"].(map[string]any)["id"].(string)
	if num(d["remaining_due"]) != 30000 {
		t.Fatalf("sisa tagihan harus 30000, dapat %v", d["remaining_due"])
	}
	versi = num(d["balance"].(map[string]any)["journal_version"])
	url := f.srv.URL + "/api/v1/admin/orders/" + orderID + "/deposit-allocations/" + allocID + "/reverse"

	status, _ = depositReq(t, http.MethodPost, url, `{"amount":10000,"expected_journal_version":1,"reason":"x"}`, cookiesA, csrfA, "cab-rev")
	if status != http.StatusForbidden {
		t.Fatalf("cabang tidak boleh reversal, dapat %d", status)
	}
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, res = depositReq(t, http.MethodPost, url, fmt.Sprintf(`{"amount":20000,"expected_journal_version":%d}`, versi), cookiesMgr, csrfMgr, "rev-0")
	if status != http.StatusBadRequest {
		t.Fatalf("reversal tanpa alasan harus 400, dapat %d (%v)", status, res)
	}
	status, res = depositReq(t, http.MethodPost, url,
		fmt.Sprintf(`{"amount":20000,"expected_journal_version":%d,"reason":"Koreksi sebagian"}`, versi), cookiesMgr, csrfMgr, "rev-1")
	if status != http.StatusOK || num(dataObj(res)["reversed_amount"]) != 20000 || num(dataObj(res)["active_amount"]) != 40000 {
		t.Fatalf("reversal parsial salah: %d (%v)", status, res)
	}
	versi = num(dataObj(res)["balance"].(map[string]any)["journal_version"])
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != 60000 {
		t.Fatalf("saldo setelah reversal 20000 harus 60000, dapat %d", saldo)
	}
	status, res = depositReq(t, http.MethodPost, url,
		fmt.Sprintf(`{"amount":50000,"expected_journal_version":%d,"reason":"Terlalu besar"}`, versi), cookiesMgr, csrfMgr, "rev-2")
	if status != http.StatusConflict || errCode(res) != "REVERSAL_EXCEEDS_AVAILABLE" {
		t.Fatalf("reversal melebihi bagian aktif harus 409 REVERSAL_EXCEEDS_AVAILABLE, dapat %d (%v)", status, res)
	}

	// Reverse seluruh sisa aktif. Retry dengan key/payload sama harus replay,
	// tanpa kredit kedua. Sumber transaksi dan audit tetap terhubung.
	fullBody := fmt.Sprintf(`{"amount":40000,"expected_journal_version":%d,"reason":"Batalkan seluruh sisa alokasi"}`, versi)
	status, full := depositReq(t, http.MethodPost, url, fullBody, cookiesMgr, csrfMgr, "rev-full")
	if status != http.StatusOK || num(dataObj(full)["reversed_amount"]) != 60000 || num(dataObj(full)["active_amount"]) != 0 {
		t.Fatalf("reversal seluruh sisa salah: %d (%v)", status, full)
	}
	status, res = depositReq(t, http.MethodPost, url, fullBody, cookiesMgr, csrfMgr, "rev-full")
	if status != http.StatusOK || dataObj(res)["replayed"] != true {
		t.Fatalf("retry reversal seluruh harus replay, dapat %d (%v)", status, res)
	}
	versi = num(dataObj(full)["balance"].(map[string]any)["journal_version"])
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != 100000 {
		t.Fatalf("reversal seluruh harus mengembalikan saldo ke 100000, dapat %d", saldo)
	}
	var reversalJournal, reversalAudit int
	if err := f.pool.QueryRow(context.Background(), `SELECT
		count(*) FILTER (WHERE t.kind='allocation_reversal_credit' AND t.reverses_transaction_id=a.debit_transaction_id),
		(SELECT count(*) FROM audit_logs l WHERE l.entity='deposit_allocation' AND l.entity_id=a.id AND l.action='reverse')
		FROM deposit_allocations a LEFT JOIN branch_deposit_transactions t ON t.reverses_transaction_id=a.debit_transaction_id
		WHERE a.id=$1 GROUP BY a.id`, allocID).Scan(&reversalJournal, &reversalAudit); err != nil {
		t.Fatalf("baca sumber reversal/audit: %v", err)
	}
	if reversalJournal != 2 || reversalAudit != 2 {
		t.Fatalf("dua reversal harus punya sumber dan audit: journal=%d audit=%d", reversalJournal, reversalAudit)
	}

	if _, err := f.pool.Exec(context.Background(), `UPDATE orders SET total=0 WHERE id=$1`, orderID); err != nil {
		t.Fatalf("siapkan order Done: %v", err)
	}
	pasangPointerVerifikasiDone(t, f.pool, f.adminSA, f.bankDefault, orderID, "2026-10-3001", 0)
	status, res = depositReq(t, http.MethodPost, url,
		fmt.Sprintf(`{"amount":10000,"expected_journal_version":%d,"reason":"Setelah Done"}`, versi), cookiesMgr, csrfMgr, "rev-3")
	if status != http.StatusConflict || errCode(res) != "ORDER_ALREADY_DONE" {
		t.Fatalf("reversal order Done harus 409 ORDER_ALREADY_DONE, dapat %d (%v)", status, res)
	}
}
