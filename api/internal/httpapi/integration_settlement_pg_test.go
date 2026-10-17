package httpapi

// Uji integrasi settlement campuran deposit + transfer dan revisi berdeposit
// (C3; D3 §3.5–3.7, §5.4, §6.2, §8.1; delta C0 §3.4, §4.3.1, §4.4) terhadap
// PostgreSQL nyata. Dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// buktiOrder menyemai satu bukti transfer order dengan hash unik.
func buktiOrder(t *testing.T, f *pgFixture, orderID, tag string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO payment_proofs
(purpose,branch_id,order_id,storage_key,nama_file,mime_type,ukuran_byte,sha256,uploaded_by_kind,uploaded_by_id)
VALUES('order_payment',(SELECT branch_id FROM orders WHERE id=$1),$1,$2,'bukti.jpg','image/jpeg',4,digest($2,'sha256'),'branch',$3)
RETURNING id`, orderID, "proofs/"+orderID+"/"+tag, f.userA).Scan(&id); err != nil {
		t.Fatalf("seed bukti order %s: %v", tag, err)
	}
	return id
}

// alokasiHTTP mengalokasikan deposit cabang ke order lewat API cabang.
func alokasiHTTP(t *testing.T, f *pgFixture, cookies []*http.Cookie, csrf, orderID string, amount, journal int64, orderVersion int, key string) string {
	t.Helper()
	body := fmt.Sprintf(`{"amount":%d,"expected_journal_version":%d,"expected_order_version":%d}`, amount, journal, orderVersion)
	status, res := depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/orders/"+orderID+"/deposit-allocations", body, cookies, csrf, key)
	if status != http.StatusCreated {
		t.Fatalf("alokasi %d harus 201, dapat %d (%v)", amount, status, res)
	}
	return dataObj(res)["allocation"].(map[string]any)["id"].(string)
}

type splitUji struct {
	proof             string
	approved, apply   int64
	creditedKumulatif int64
}

func settlementBody(f *pgFixture, versi int, alokasi []string, splits []splitUji) string {
	type split struct {
		PaymentProofID         string `json:"payment_proof_id"`
		BankAccountID          string `json:"bank_account_id"`
		ApprovedNominal        int64  `json:"approved_nominal"`
		AppliedToOrder         int64  `json:"applied_to_order"`
		CreditedToDepositTotal int64  `json:"credited_to_deposit_total"`
	}
	out := []split{}
	for _, s := range splits {
		out = append(out, split{s.proof, f.bankDefault, s.approved, s.apply, s.creditedKumulatif})
	}
	if alokasi == nil {
		alokasi = []string{}
	}
	raw, _ := json.Marshal(map[string]any{"hasil": "cocok", "expected_order_version": versi,
		"deposit_allocation_ids": alokasi, "proof_splits": out})
	return string(raw)
}

func verifyKey(t *testing.T, f *pgFixture, cookies []*http.Cookie, csrf, orderID, body, key string) (int, map[string]any) {
	t.Helper()
	return depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/admin/orders/"+orderID+"/verify", body, cookies, csrf, key)
}

func hitungSQL(t *testing.T, f *pgFixture, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// TestPGSettlementCampuranTepatTotal: deposit + transfer harus tepat total;
// alokasi yang dilewati, kurang bayar, dan versi basi ditolak tanpa mutasi;
// Manager boleh menyelesaikan campuran tanpa kredit, dan Done memakai snapshot
// sumber deposit tanpa debit kedua.
func TestPGSettlementCampuranTepatTotal(t *testing.T) {
	f := newPGFixture(t)
	id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-501")
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	versi := kreditCabang(t, f, cookiesA, csrfA, 300000, "c3-tepat")
	alokasi := alokasiHTTP(t, f, cookiesA, csrfA, id, 100000, versi, 0, "c3-tepat-alok")
	proof := buktiOrder(t, f, id, "c3-tepat")
	transfer := total - 100000

	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	legacy := fmt.Sprintf(`{"hasil":"cocok","proof_destinations":[{"payment_proof_id":"%s","bank_account_id":"%s"}]}`, proof, f.bankDefault)
	cases := []struct {
		nama   string
		body   string
		status int
		code   string
	}{
		{"tanpa rincian", legacy, http.StatusUnprocessableEntity, "PAYMENT_BREAKDOWN_MISMATCH"},
		{"kurang bayar", settlementBody(f, 0, []string{alokasi}, []splitUji{{proof, transfer - 1, transfer - 1, 0}}), http.StatusUnprocessableEntity, "PAYMENT_BREAKDOWN_MISMATCH"},
		{"alokasi dilewati", settlementBody(f, 0, nil, []splitUji{{proof, total, total, 0}}), http.StatusUnprocessableEntity, "PAYMENT_BREAKDOWN_MISMATCH"},
		{"versi basi", settlementBody(f, 1, []string{alokasi}, []splitUji{{proof, transfer, transfer, 0}}), http.StatusConflict, "ORDER_VERSION_CONFLICT"},
	}
	for _, c := range cases {
		status, res := verifyKey(t, f, cookiesMgr, csrfMgr, id, c.body, "")
		if status != c.status || errCode(res) != c.code {
			t.Fatalf("%s: harus %d %s, dapat %d (%v)", c.nama, c.status, c.code, status, res)
		}
	}
	if n := hitungSQL(t, f, `SELECT count(*) FROM verification_logs WHERE order_id=$1`, id); n != 0 {
		t.Fatalf("penolakan tidak boleh menulis log verifikasi, dapat %d", n)
	}

	status, res := verifyKey(t, f, cookiesMgr, csrfMgr, id,
		settlementBody(f, 0, []string{alokasi}, []splitUji{{proof, transfer, transfer, 0}}), "")
	if status != http.StatusOK {
		t.Fatalf("settlement campuran Manager harus 200, dapat %d (%v)", status, res)
	}
	set := dataObj(res)["settlement"].(map[string]any)
	if num(set["deposit_amount"]) != 100000 || num(set["transfer_amount"]) != transfer || num(set["new_credit_to_deposit"]) != 0 {
		t.Fatalf("breakdown settlement salah: %v", set)
	}
	var st string
	var doneDeposit, doneTransfer int64
	if err := f.pool.QueryRow(context.Background(), `SELECT status::text,done_deposit_amount,done_transfer_amount
FROM orders WHERE id=$1`, id).Scan(&st, &doneDeposit, &doneTransfer); err != nil {
		t.Fatal(err)
	}
	if st != "done" || doneDeposit != 100000 || doneTransfer != transfer {
		t.Fatalf("pointer Done salah: %s %d/%d", st, doneDeposit, doneTransfer)
	}
	if n := hitungSQL(t, f, `SELECT count(*) FROM verification_deposit_sources s JOIN orders o
ON o.done_verification_log_id=s.verification_log_id WHERE o.id=$1 AND s.amount_snapshot=100000`, id); n != 1 {
		t.Fatalf("snapshot sumber deposit harus 1, dapat %d", n)
	}
	if n := hitungSQL(t, f, `SELECT count(*) FROM branch_deposit_transactions WHERE order_id=$1`, id); n != 1 {
		t.Fatalf("Done tidak boleh menulis journal deposit baru, dapat %d", n)
	}
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != 200000 {
		t.Fatalf("saldo tidak boleh berubah saat Done, dapat %d", saldo)
	}
	status, res = verifyKey(t, f, cookiesMgr, csrfMgr, id,
		settlementBody(f, 0, []string{alokasi}, []splitUji{{proof, transfer, transfer, 0}}), "")
	if status != http.StatusConflict || errCode(res) != "ORDER_ALREADY_DONE" {
		t.Fatalf("verifikasi kedua harus 409 ORDER_ALREADY_DONE, dapat %d (%v)", status, res)
	}
}

// TestPGSettlementLebihBayarHanyaSuperadmin: lebih bayar tidak pernah Done
// tanpa keputusan; kredit deposit hanya Superadmin ber-key, retry idempoten,
// dan key dipakai ulang untuk keputusan lain ditolak.
func TestPGSettlementLebihBayarHanyaSuperadmin(t *testing.T) {
	f := newPGFixture(t)
	id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-502")
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	versi := kreditCabang(t, f, cookiesA, csrfA, 100000, "c3-lebih")
	alokasi := alokasiHTTP(t, f, cookiesA, csrfA, id, 50000, versi, 0, "c3-lebih-alok")
	proof := buktiOrder(t, f, id, "c3-lebih")
	butuh := total - 50000
	lebih := int64(30000)
	approved := butuh + lebih

	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, res := verifyKey(t, f, cookiesMgr, csrfMgr, id,
		settlementBody(f, 0, []string{alokasi}, []splitUji{{proof, approved, approved, 0}}), "")
	if status != http.StatusConflict || errCode(res) != "OVERPAY_DECISION_REQUIRED" {
		t.Fatalf("lebih bayar tanpa keputusan harus 409 OVERPAY_DECISION_REQUIRED, dapat %d (%v)", status, res)
	}
	kredit := settlementBody(f, 0, []string{alokasi}, []splitUji{{proof, approved, butuh, lebih}})
	status, res = verifyKey(t, f, cookiesMgr, csrfMgr, id, kredit, "mgr-kredit")
	if status != http.StatusForbidden {
		t.Fatalf("Manager tidak boleh mengkredit lebih bayar, dapat %d (%v)", status, res)
	}
	if n := hitungSQL(t, f, `SELECT count(*) FROM payment_proof_economic_decisions WHERE order_id=$1`, id); n != 0 {
		t.Fatalf("penolakan tidak boleh menyimpan keputusan bukti, dapat %d", n)
	}

	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, _ = verifyKey(t, f, cookiesSA, csrfSA, id, kredit, "")
	if status != http.StatusBadRequest {
		t.Fatalf("kredit tanpa Idempotency-Key harus 400, dapat %d", status)
	}
	status, res = verifyKey(t, f, cookiesSA, csrfSA, id, kredit, "sa-kredit-1")
	if status != http.StatusOK || num(dataObj(res)["settlement"].(map[string]any)["new_credit_to_deposit"]) != lebih {
		t.Fatalf("kredit Superadmin harus 200 dengan kredit %d, dapat %d (%v)", lebih, status, res)
	}
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != 50000+lebih {
		t.Fatalf("saldo setelah kredit lebih bayar salah: %d", saldo)
	}
	status, res = verifyKey(t, f, cookiesSA, csrfSA, id, kredit, "sa-kredit-1")
	if status != http.StatusOK || dataObj(res)["settlement"].(map[string]any)["replayed"] != true {
		t.Fatalf("retry key sama harus replay 200, dapat %d (%v)", status, res)
	}
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != 50000+lebih {
		t.Fatalf("retry tidak boleh mengkredit dua kali, saldo %d", saldo)
	}
	lain := settlementBody(f, 0, []string{alokasi}, []splitUji{{proof, approved + 1, butuh, lebih + 1}})
	status, res = verifyKey(t, f, cookiesSA, csrfSA, id, lain, "sa-kredit-1")
	if status != http.StatusConflict || errCode(res) != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("key sama isi berbeda harus 409 IDEMPOTENCY_KEY_REUSED, dapat %d (%v)", status, res)
	}
	status, res = verifyKey(t, f, cookiesSA, csrfSA, id, kredit, "sa-kredit-2")
	if status != http.StatusConflict || errCode(res) != "ORDER_ALREADY_DONE" {
		t.Fatalf("key lain setelah Done harus 409 ORDER_ALREADY_DONE, dapat %d (%v)", status, res)
	}
	if n := hitungSQL(t, f, `SELECT count(*) FROM verification_proof_splits s JOIN branch_deposit_transactions t
ON t.id=s.new_credit_transaction_id WHERE s.order_id=$1 AND t.kind='overpay_credit' AND t.amount=$2`, id, lebih); n != 1 {
		t.Fatalf("split harus menunjuk tepat satu journal overpay_credit, dapat %d", n)
	}
}

// TestPGSettlementRevisiTurunReversalPlanDanCarryForward: revisi turun di
// bawah deposit committed wajib membawa reversal plan yang cukup (atomik);
// settlement versi baru membawa alokasi lama tanpa debit kedua, nominal bukti
// tetap, dan hanya delta kredit kumulatif yang dijurnal.
func TestPGSettlementRevisiTurunReversalPlanDanCarryForward(t *testing.T) {
	f := newPGFixture(t)
	id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-503")
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	versi := kreditCabang(t, f, cookiesA, csrfA, 200000, "c3-rev")
	alokasi := alokasiHTTP(t, f, cookiesA, csrfA, id, 150000, versi, 0, "c3-rev-alok")
	proof := buktiOrder(t, f, id, "c3-rev")
	transfer := total - 150000
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, res := verifyKey(t, f, cookiesSA, csrfSA, id,
		settlementBody(f, 0, []string{alokasi}, []splitUji{{proof, transfer, transfer, 0}}), "")
	if status != http.StatusOK {
		t.Fatalf("settlement awal harus 200, dapat %d (%v)", status, res)
	}
	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("render invoice v0: %v", err)
	}
	saldoAwal, _, _, _ := saldoCabang(t, f, cookiesA)

	revisi := func(plan, key string) (int, map[string]any) {
		body := `{"alasan":"UJI-C3 turun","lines":[{"line_no":1,"qty":1,"harga_satuan":80000}],"deposit_reversals":` + plan + `}`
		return depositReq(t, http.MethodPost, f.srv.URL+"/api/v1/admin/orders/"+id+"/revise", body, cookiesSA, csrfSA, key)
	}
	tolak := []struct {
		nama, plan, key string
		status          int
		code            string
	}{
		{"tanpa plan", `[]`, "", http.StatusConflict, "REVISION_REVERSAL_PLAN_REQUIRED"},
		{"plan kurang", `[{"allocation_id":"` + alokasi + `","amount":50000,"reason":"kurang"}]`, "rev-kurang", http.StatusConflict, "REVISION_REVERSAL_PLAN_REQUIRED"},
		{"alokasi asing", `[{"allocation_id":"01900000-0000-7000-8000-000000000001","amount":70000,"reason":"asing"}]`, "rev-asing", http.StatusConflict, "REVISION_REVERSAL_PLAN_INVALID"},
		{"tanpa key", `[{"allocation_id":"` + alokasi + `","amount":70000,"reason":"cukup"}]`, "", http.StatusBadRequest, "VALIDATION_FAILED"},
	}
	for _, c := range tolak {
		status, res := revisi(c.plan, c.key)
		if status != c.status || errCode(res) != c.code {
			t.Fatalf("%s: harus %d %s, dapat %d (%v)", c.nama, c.status, c.code, status, res)
		}
	}
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != saldoAwal {
		t.Fatalf("revisi gagal tidak boleh mengubah saldo: %d -> %d", saldoAwal, saldo)
	}
	if n := hitungSQL(t, f, `SELECT reversed_amount FROM deposit_allocations WHERE id=$1`, alokasi); n != 0 {
		t.Fatalf("revisi gagal tidak boleh menyisakan reversal, dapat %d", n)
	}

	status, res = revisi(`[{"allocation_id":"`+alokasi+`","amount":70000,"reason":"Total revisi turun"}]`, "rev-cukup")
	if status != http.StatusOK {
		t.Fatalf("revisi dengan plan cukup harus 200, dapat %d (%v)", status, res)
	}
	var st, sub string
	if err := f.pool.QueryRow(context.Background(), `SELECT status::text,COALESCE(revision_payment_state::text,'')
FROM orders WHERE id=$1`, id).Scan(&st, &sub); err != nil {
		t.Fatal(err)
	}
	if st != "submitted" || sub != "revision_overpayment_pending" {
		t.Fatalf("revisi turun harus pending lebih bayar, dapat %s/%s", st, sub)
	}
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != saldoAwal+70000 {
		t.Fatalf("reversal plan harus mengembalikan 70000, saldo %d -> %d", saldoAwal, saldo)
	}

	status, res = depositReq(t, http.MethodGet, f.srv.URL+"/api/v1/orders/"+id+"/payment", "", cookiesA, "", "")
	pay := dataObj(res)
	if status != http.StatusOK || pay["is_lunas"] != false || pay["settlement"] != nil ||
		pay["revision_payment_state"] != "revision_overpayment_pending" || num(pay["committed_deposit"]) != 80000 ||
		num(pay["current_total"]) != 80000 || num(pay["remaining_due"]) != 0 {
		t.Fatalf("rincian pembayaran revisi pending salah: %d %v", status, res)
	}
	cookiesB, _ := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	if status, _ := depositReq(t, http.MethodGet, f.srv.URL+"/api/v1/orders/"+id+"/payment", "", cookiesB, "", ""); status != http.StatusNotFound {
		t.Fatalf("cabang lain tidak boleh melihat rincian pembayaran, dapat %d", status)
	}

	// Settlement v1: alokasi lama (sisa 80000) menutup total; transfer bukti
	// lama tidak lagi diterapkan sehingga seluruhnya menjadi kredit kumulatif.
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	v1 := settlementBody(f, 1, []string{alokasi}, []splitUji{{proof, transfer, 0, transfer}})
	status, _ = verifyKey(t, f, cookiesMgr, csrfMgr, id, v1, "mgr-v1")
	if status != http.StatusForbidden {
		t.Fatalf("Manager tidak boleh memutuskan kredit revisi, dapat %d", status)
	}
	status, res = verifyKey(t, f, cookiesSA, csrfSA, id,
		settlementBody(f, 1, []string{alokasi}, []splitUji{{proof, transfer + 1, 0, transfer + 1}}), "sa-v1-salah")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("nominal bukti yang sudah diputuskan tidak boleh berubah, dapat %d (%v)", status, res)
	}
	status, res = verifyKey(t, f, cookiesSA, csrfSA, id, v1, "sa-v1")
	if status != http.StatusOK {
		t.Fatalf("settlement v1 harus 200, dapat %d (%v)", status, res)
	}
	set := dataObj(res)["settlement"].(map[string]any)
	if num(set["deposit_amount"]) != 80000 || num(set["transfer_amount"]) != 0 || num(set["new_credit_to_deposit"]) != transfer {
		t.Fatalf("breakdown v1 salah: %v", set)
	}
	if n := hitungSQL(t, f, `SELECT count(*) FROM branch_deposit_transactions
WHERE order_id=$1 AND kind='order_allocation_debit'`, id); n != 1 {
		t.Fatalf("carry-forward tidak boleh menulis debit kedua, dapat %d", n)
	}
	if n := hitungSQL(t, f, `SELECT count(*) FROM verification_deposit_sources s JOIN orders o
ON o.done_verification_log_id=s.verification_log_id
WHERE o.id=$1 AND s.allocation_order_version=0 AND s.settlement_order_version=1 AND s.amount_snapshot=80000`, id); n != 1 {
		t.Fatalf("snapshot v1 harus membawa alokasi v0 sebesar 80000, dapat %d", n)
	}
	if saldo, _, _, _ := saldoCabang(t, f, cookiesA); saldo != saldoAwal+70000+transfer {
		t.Fatalf("saldo akhir salah: %d", saldo)
	}
	var journal, cache int64
	if err := f.pool.QueryRow(context.Background(), `SELECT
(SELECT COALESCE(sum(amount),0) FROM branch_deposit_transactions WHERE branch_id=$1),
(SELECT balance FROM branch_deposit_accounts WHERE branch_id=$1)`, f.branchA).Scan(&journal, &cache); err != nil || journal != cache {
		t.Fatalf("journal harus sama dengan cache: %d/%d (%v)", journal, cache, err)
	}
	status, res = depositReq(t, http.MethodGet, f.srv.URL+"/api/v1/admin/orders/"+id+"/payment", "", cookiesSA, "", "")
	pay = dataObj(res)
	settled, _ := pay["settlement"].(map[string]any)
	if status != http.StatusOK || pay["is_lunas"] != true || settled == nil ||
		num(settled["deposit_amount"]) != 80000 || num(settled["transfer_amount"]) != 0 || num(settled["order_version"]) != 1 {
		t.Fatalf("rincian pembayaran Done v1 salah: %d %v", status, res)
	}
}
