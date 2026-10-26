package httpapi

// Uji integrasi verifikasi pembayaran manual dan transisi Done (kartu F1-18)
// pada PostgreSQL nyata.
//
// Yang dibuktikan berkas ini adalah perilaku yang hanya ada di database dan di
// rantai antar-domain:
//
//   - SATU transaksi verifikasi menulis status/done_at/verif_status, log
//     verifikasi, timeline, audit, dan MENITIPKAN pekerjaan render dokumen
//     pada tabel river_job — tidak ada pesanan Done tanpa pekerjaan dokumen;
//   - dua verifikasi bersamaan berbaris rapi: satu menang, sisanya 409, dan
//     efek sampingnya tetap satu set;
//   - pekerjaan worker menjalankan rantai Done -> dokumen -> email (dokumen
//     DULU, email kemudian) dan aman dijalankan ulang (idempoten);
//   - keputusan "perlu review" TIDAK menyelesaikan pesanan;
//   - C&C berdeposit tetap Submitted sampai admin menyetujui rinciannya, dan
//     pesanan bernilai tanpa sumber dana tidak bisa disetujui;
//   - otorisasi CapAdminVerify (Manager & Superadmin) dan bentuk id bukan UUID.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/document"
	documentpg "github.com/grahita/portal/api/internal/document/postgres"
	"github.com/grahita/portal/api/internal/fulfillment"
	"github.com/grahita/portal/api/internal/payment"
	paymentpg "github.com/grahita/portal/api/internal/payment/postgres"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/platform/pdf"
	"github.com/grahita/portal/api/internal/worker"
)

// seedPesananSubmitted menyemai satu pesanan berstatus Submitted lengkap dengan
// baris berserial dan rekening aktif, supaya rantai dokumen setelah verifikasi
// bisa dirender. Pesanannya bukan hasil submit HTTP (itu kartu F1-14 dan sudah
// diuji di integration_submit_pg_test.go).
func seedPesananSubmitted(t *testing.T, f *pgFixture, branchID, nomor string) (string, int64) {
	t.Helper()
	ctx := context.Background()
	produk := produkKode(t, f, "P-01")

	if _, err := f.pool.Exec(ctx,
		`INSERT INTO bank_accounts (bank, nama_pemilik, nomor, is_active)
		 VALUES ('BCA','PT Grahita Indonesia','1234567890',true)
		 ON CONFLICT (bank, nomor) DO NOTHING`); err != nil {
		t.Fatalf("seed rekening: %v", err)
	}

	basis := nomor[len(nomor)-3:]
	baris := []struct {
		no     int
		qty    int
		harga  int64
		serial string
	}{
		{1, 2, 100000, "20261009" + basis + "1"},
		{2, 1, 50000, "20261009" + basis + "2"},
	}
	var total int64
	for _, b := range baris {
		total += int64(b.qty) * b.harga
	}

	var id string
	err := f.pool.QueryRow(ctx,
		`INSERT INTO orders (branch_id, nomor, status, verif_status, tanggal, total, is_cnc, submitted_at)
		 VALUES ($1, $2, 'submitted', 'belum', (now() AT TIME ZONE 'Asia/Jakarta')::date, $3, $4, now())
		 RETURNING id`,
		branchID, nomor, total, strings.Contains(nomor, "cnc")).Scan(&id)
	if err != nil {
		t.Fatalf("seed pesanan submitted %s: %v", nomor, err)
	}
	for _, b := range baris {
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO order_lines (order_id, line_no, product_id, nama_snapshot,
			                          qty, harga_satuan, serial_number)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			id, b.no, produk, "Bio Magnetik", b.qty, b.harga, b.serial); err != nil {
			t.Fatalf("seed baris pesanan %s: %v", nomor, err)
		}
	}
	return id, total
}

func bodyVerifikasiCocok(t *testing.T, f *pgFixture, orderID string, total *int64, catatan string) string {
	return bodyVerifikasiCocokKe(t, f, orderID, total, catatan, f.bankDefault)
}

func bodyVerifikasiCocokKe(t *testing.T, f *pgFixture, orderID string, total *int64, catatan, bankAccountID string) string {
	t.Helper()
	ctx := context.Background()
	rows, err := f.pool.Query(ctx, `SELECT id::text FROM payment_proofs WHERE order_id=$1 ORDER BY id`, orderID)
	if err != nil {
		t.Fatalf("baca bukti untuk body verifikasi: %v", err)
	}
	destinations := []payment.ProofDestination{}
	for rows.Next() {
		var proofID string
		if err := rows.Scan(&proofID); err != nil {
			t.Fatal(err)
		}
		destinations = append(destinations, payment.ProofDestination{PaymentProofID: proofID, BankAccountID: bankAccountID})
	}
	rows.Close()
	if len(destinations) == 0 {
		// Persetujuan butuh sumber dana: tanpa alokasi deposit, satu bukti.
		var proofID string
		if err := f.pool.QueryRow(ctx, `INSERT INTO payment_proofs
			(purpose,branch_id,order_id, storage_key, nama_file, mime_type, ukuran_byte, sha256, uploaded_by_kind, uploaded_by_id)
			VALUES ('order_payment',(SELECT branch_id FROM orders WHERE id=$1),$1,$2,'bukti.jpg','image/jpeg',4,decode('01020304','hex'),'branch',$3)
			RETURNING id`, orderID, "proofs/"+orderID, f.userA).Scan(&proofID); err != nil {
			t.Fatalf("seed bukti verifikasi manual: %v", err)
		}
		destinations = append(destinations, payment.ProofDestination{PaymentProofID: proofID, BankAccountID: bankAccountID})
	}
	payload := map[string]any{"hasil": "cocok", "proof_destinations": destinations}
	if total != nil {
		payload["total_terbaca"] = *total
	}
	if catatan != "" {
		payload["catatan"] = catatan
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// verifyHTTP mengirim permintaan verifikasi dan mengembalikan status + body.
func verifyHTTP(t *testing.T, f *pgFixture, cookies []*http.Cookie, csrf, id, body string) (int, map[string]any) {
	t.Helper()
	status, decoded, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+id+"/verify", body, cookies, csrf)
	return status, decoded
}

// seedAlokasiDepositAktif menyemai alokasi aktif lewat SQL dengan ledger yang
// tetap konsisten: saldo dikreditkan dulu (seedSaldoDeposit), lalu debit +
// alokasi + cache akun, supaya rekonsiliasi journal==cache tetap benar.
func seedAlokasiDepositAktif(t *testing.T, f *pgFixture, orderID, branchID string, amount int64) {
	t.Helper()
	ctx := context.Background()
	seedSaldoDeposit(t, f, branchID, amount)
	var transactionID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO branch_deposit_transactions(
branch_id,kind,amount,order_id,order_version,idempotency_key_hash,request_fingerprint,actor_kind)
VALUES($1,'order_allocation_debit',$2,$3,0,digest($4,'sha256'),digest($4,'sha256'),'system') RETURNING id`,
		branchID, -amount, orderID, "UJI-active-allocation-"+orderID).Scan(&transactionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO deposit_allocations(
branch_id,order_id,order_version,debit_transaction_id,amount,created_by_kind)
VALUES($1,$2,0,$3,$4,'system')`, branchID, orderID, transactionID, amount); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE branch_deposit_accounts
SET balance=balance-$2, journal_version=journal_version+1 WHERE branch_id=$1`, branchID, amount); err != nil {
		t.Fatal(err)
	}
}

// seedSaldoDeposit menyemai satu top-up terverifikasi Superadmin lengkap
// (bukti, top-up, log verifikasi, kredit journal, cache akun).
func seedSaldoDeposit(t *testing.T, f *pgFixture, branchID string, amount int64) {
	t.Helper()
	ctx := context.Background()
	tag := fmt.Sprintf("UJI-saldo-%s-%d", branchID, time.Now().UnixNano())
	var proofID, topupID, logID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO payment_proofs
(purpose,branch_id,storage_key,nama_file,mime_type,ukuran_byte,sha256,uploaded_by_kind,uploaded_by_id)
VALUES('deposit_topup',$1,$2,'topup.jpg','image/jpeg',4,digest($2,'sha256'),'admin',$3) RETURNING id`,
		branchID, "proofs/"+tag, f.adminSA).Scan(&proofID); err != nil {
		t.Fatalf("seed bukti top-up: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO deposit_topups
(branch_id,bank_account_id,status,declared_amount,verified_amount,payment_proof_id,verified_by,verified_at,created_by_kind)
VALUES($1,$2,'verified',$3,$3,$4,$5,now(),'admin') RETURNING id`,
		branchID, f.bankDefault, amount, proofID, f.adminSA).Scan(&topupID); err != nil {
		t.Fatalf("seed top-up: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO deposit_verification_logs
(topup_id,method,result,declared_amount,approved_amount,bank_account_id,branch_allowlist_version,destination_result,admin_id)
VALUES($1,'manual','verified',$2,$2,$3,1,'{}'::jsonb,$4) RETURNING id`,
		topupID, amount, f.bankDefault, f.adminSA).Scan(&logID); err != nil {
		t.Fatalf("seed log top-up: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO branch_deposit_transactions
(branch_id,kind,amount,source_proof_id,deposit_verification_log_id,idempotency_key_hash,request_fingerprint,actor_kind,actor_id)
VALUES($1,'topup_credit',$2,$3,$4,digest($5,'sha256'),digest($5,'sha256'),'admin',$6)`,
		branchID, amount, proofID, logID, tag, f.adminSA); err != nil {
		t.Fatalf("seed kredit top-up: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO branch_deposit_accounts(branch_id,balance,journal_version)
VALUES($1,$2,1) ON CONFLICT(branch_id) DO UPDATE
SET balance=branch_deposit_accounts.balance+$2, journal_version=branch_deposit_accounts.journal_version+1`,
		branchID, amount); err != nil {
		t.Fatalf("seed akun deposit: %v", err)
	}
}

func kodeErrorVerifikasi(body map[string]any) string {
	errObj, _ := body["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	return code
}

// jalankanPekerjaanDone menjalankan pekerjaan worker "render dokumen pesanan"
// untuk satu pesanan — peran worker River di produksi.
func jalankanPekerjaanDone(t *testing.T, f *pgFixture, orderID string) error {
	t.Helper()
	w := worker.NewRenderDokumenWorker(f.selesai)
	job := &river.Job[jobs.RenderDokumenArgs]{
		JobRow: &rivertype.JobRow{ID: 1},
		Args:   jobs.RenderDokumenArgs{OrderID: orderID},
	}
	return w.Work(context.Background(), job)
}

// hitungBaris menghitung baris tabel domain dengan satu syarat kolom = nilai.
func hitungBaris(t *testing.T, f *pgFixture, tabel, kolom, nilai string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", tabel, kolom), nilai).Scan(&n); err != nil {
		t.Fatalf("hitung %s: %v", tabel, err)
	}
	return n
}

// statusPesanan membaca kolom keadaan verifikasi satu pesanan.
func statusPesanan(t *testing.T, f *pgFixture, id string) (status, verif, alasan string, doneAt *string) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT status::text, verif_status::text, COALESCE(alasan_review::text,''), done_at::text
		   FROM orders WHERE id = $1`, id).Scan(&status, &verif, &alasan, &doneAt); err != nil {
		t.Fatalf("baca status pesanan: %v", err)
	}
	return
}

// pekerjaanDokumen mengembalikan jumlah job render dokumen milik satu pesanan.
func pekerjaanDokumen(t *testing.T, f *pgFixture, orderID string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'order_id' = $2`,
		jobs.KindRenderDokumen, orderID).Scan(&n); err != nil {
		t.Fatalf("hitung job dokumen: %v", err)
	}
	return n
}

// TestPGVerifikasiSetujuMenghasilkanDokumenDanEmailSekali menjalankan alur
// utama: Setujui -> Done -> dokumen + email sesuai flag, masing-masing SATU set.
func TestPGVerifikasiSetujuMenghasilkanDokumenDanEmailSekali(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// Admin Pusat ber-flag penerima invoice + surat jalan (PRD AP-USR-01).
	if _, err := f.pool.Exec(ctx,
		`UPDATE admin_users SET notif_invoice = true, notif_surat_jalan = true WHERE id = $1`, f.adminSA); err != nil {
		t.Fatalf("pasang flag email admin: %v", err)
	}

	id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-118")
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	status, body := verifyHTTP(t, f, cookies, csrf, id,
		bodyVerifikasiCocok(t, f, id, &total, "transfer sesuai"))
	if status != http.StatusOK {
		t.Fatalf("verifikasi setuju harus 200, dapat %d (%v)", status, body)
	}
	hasil := dataMap(t, body)
	order, _ := hasil["order"].(map[string]any)
	if order["status"] != "done" || order["verif_status"] != "cocok" {
		t.Errorf("pesanan harus done/cocok, dapat %v", order)
	}
	log, _ := hasil["verifikasi"].(map[string]any)
	if log["metode"] != "manual" || log["total_terbaca"] != float64(total) || log["selisih"] != float64(0) {
		t.Errorf("log verifikasi tidak sesuai: %v", log)
	}
	if log["admin_nama"] != "Rina" {
		t.Errorf("log harus memuat nama pemeriksa, dapat %v", log["admin_nama"])
	}

	// Database: status + done_at + log + timeline + audit, semuanya tepat satu.
	st, verif, _, doneAt := statusPesanan(t, f, id)
	if st != "done" || verif != "cocok" {
		t.Errorf("status pesanan harus done/cocok, dapat %s/%s", st, verif)
	}
	if doneAt == nil || *doneAt == "" {
		t.Error("done_at harus terisi saat status done (constraint done_punya_waktu)")
	}
	if n := hitungBaris(t, f, "verification_logs", "order_id", id); n != 1 {
		t.Errorf("harus ada tepat 1 log verifikasi, dapat %d", n)
	}
	var logCount int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM verification_logs WHERE order_id=$1 AND total_pesanan=$2 AND total_terbaca=$3 AND selisih=0 AND hasil='cocok' AND metode='manual'`,
		id, total, total).Scan(&logCount); err != nil {
		t.Fatalf("baca log verifikasi: %v", err)
	}
	if logCount != 1 {
		t.Errorf("log verifikasi harus memuat nominal + selisih yang benar, cocok=%d", logCount)
	}
	// Histori order done tidak boleh berubah saat rekening kemudian diganti nama
	// pemilik, dinonaktifkan, dan dicabut dari cabang.
	if _, err := f.pool.Exec(ctx, `UPDATE bank_accounts SET nama_pemilik='Pemilik Baru', is_active=false WHERE id=$1`, f.bankDefault); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE branch_bank_accounts SET is_allowed=false, revoked_by=$3, revoked_at=now() WHERE branch_id=$1 AND bank_account_id=$2`, f.branchA, f.bankDefault, f.adminSA); err != nil {
		t.Fatal(err)
	}
	snapshot, err := documentpg.New(f.pool).OrderForDocument(ctx, id)
	if err != nil {
		t.Fatalf("baca snapshot dokumen selesai: %v", err)
	}
	if len(snapshot.Rekening) != 1 || snapshot.Rekening[0].AtasNama != "PT Fixture Sintetis" || snapshot.Rekening[0].Nomor != "9999000011" {
		t.Fatalf("snapshot rekening historis berubah: %+v", snapshot.Rekening)
	}
	var teks string
	if err := f.pool.QueryRow(ctx,
		`SELECT teks FROM order_events WHERE order_id=$1 ORDER BY at DESC, id DESC LIMIT 1`, id).Scan(&teks); err != nil {
		t.Fatalf("baca timeline: %v", err)
	}
	if !strings.Contains(teks, "Rina") {
		t.Errorf("timeline harus memuat pelaku verifikasi, dapat %q", teks)
	}
	var entitas, aksi string
	var after []byte
	if err := f.pool.QueryRow(ctx,
		`SELECT entity, action, after FROM audit_logs WHERE entity_id=$1`, id).Scan(&entitas, &aksi, &after); err != nil {
		t.Fatalf("baca audit: %v", err)
	}
	if entitas != "order" || aksi != "verify" {
		t.Errorf("audit harus entity=order action=verify, dapat %s/%s", entitas, aksi)
	}
	var afterMap map[string]any
	if err := json.Unmarshal(after, &afterMap); err != nil {
		t.Fatalf("after bukan JSON objek: %v", err)
	}
	if afterMap["status"] != "done" || afterMap["selisih"] != float64(0) {
		t.Errorf("audit after harus memuat keadaan sesudahnya, dapat %v", afterMap)
	}

	// Pekerjaan dokumen DITITIPKAN pada transaksi yang sama, dan belum
	// dikerjakan: belum ada dokumen/email sama sekali.
	if n := pekerjaanDokumen(t, f, id); n != 1 {
		t.Fatalf("harus ada tepat 1 job render dokumen, dapat %d", n)
	}
	if n := hitungBaris(t, f, "documents", "order_id", id); n != 0 {
		t.Fatalf("dokumen belum boleh ada sebelum pekerjaan dijalankan, dapat %d", n)
	}
	if n := hitungBaris(t, f, "email_outbox", "order_id", id); n != 0 {
		t.Fatalf("email belum boleh diantre sebelum dokumen ada, dapat %d", n)
	}

	// Worker menjalankan pekerjaannya: dokumen dirender, lalu email diantre.
	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("pekerjaan done harus berhasil: %v", err)
	}
	jenisDokumen := []string{}
	rows, err := f.pool.Query(ctx, `SELECT jenis::text FROM documents WHERE order_id=$1 ORDER BY jenis`, id)
	if err != nil {
		t.Fatalf("baca dokumen: %v", err)
	}
	for rows.Next() {
		var jenis string
		if err := rows.Scan(&jenis); err != nil {
			t.Fatalf("scan dokumen: %v", err)
		}
		jenisDokumen = append(jenisDokumen, jenis)
	}
	rows.Close()
	if strings.Join(jenisDokumen, ",") != "invoice_pdf,surat_jalan_pdf,surat_jalan_xlsx" {
		t.Errorf("harus ada satu set Invoice + Surat Jalan, dapat %v", jenisDokumen)
	}
	// Objeknya benar-benar ada di storage (bukan hanya barisnya).
	objek, err := f.objek.List(ctx, "documents/"+id+"/")
	if err != nil {
		t.Fatalf("daftar objek dokumen: %v", err)
	}
	if len(objek) != 3 {
		t.Errorf("harus ada 3 berkas dokumen di storage, dapat %d", len(objek))
	}

	emailJenis := map[string][]string{}
	rows, err = f.pool.Query(ctx, `SELECT jenis::text, to_addr FROM email_outbox WHERE order_id=$1`, id)
	if err != nil {
		t.Fatalf("baca email: %v", err)
	}
	for rows.Next() {
		var jenis string
		var to []string
		if err := rows.Scan(&jenis, &to); err != nil {
			t.Fatalf("scan email: %v", err)
		}
		emailJenis[jenis] = to
	}
	rows.Close()
	if len(emailJenis) != 3 {
		t.Fatalf("harus ada 3 email dokumen, dapat %v", emailJenis)
	}
	if got := emailJenis["invoice_cabang"]; len(got) != 1 || got[0] != "cabang-a@example.test" {
		t.Errorf("invoice cabang harus ke email cabang, dapat %v", got)
	}
	if got := emailJenis["invoice_admin"]; len(got) != 1 || got[0] != "rina@example.test" {
		t.Errorf("invoice admin harus ke admin ber-flag, dapat %v", got)
	}
	if got := emailJenis["surat_jalan_admin"]; len(got) != 1 || got[0] != "rina@example.test" {
		t.Errorf("surat jalan harus ke admin ber-flag, dapat %v", got)
	}

	// Pekerjaan dijalankan ULANG (River at-least-once): tidak menggandakan
	// dokumen maupun email.
	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("pekerjaan ulang harus berhasil: %v", err)
	}
	if n := hitungBaris(t, f, "documents", "order_id", id); n != 3 {
		t.Errorf("dokumen harus tetap 3 sesudah dijalankan ulang, dapat %d", n)
	}
	if n := hitungBaris(t, f, "email_outbox", "order_id", id); n != 3 {
		t.Errorf("email harus tetap 3 sesudah dijalankan ulang, dapat %d", n)
	}
	if f.objek.jumlah() != 3 {
		t.Errorf("storage harus tetap 3 objek, dapat %d", f.objek.jumlah())
	}
}

// TestPGVerifikasiCocokWajibTujuanEfektifCabang menegakkan pemetaan tepat satu
// bukti ke rekening efektif dari cabang order.
func TestPGVerifikasiCocokWajibTujuanEfektifCabang(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-121")
	// Helper ini sekaligus membuat fixture bukti sintetis, tetapi body sengaja
	// tidak dipakai untuk menguji kontrak exact-one destination.
	_ = bodyVerifikasiCocok(t, f, id, &total, "")
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	status, body := verifyHTTP(t, f, cookies, csrf, id,
		fmt.Sprintf(`{"hasil":"cocok","total_terbaca":%d}`, total))
	if status != http.StatusUnprocessableEntity || kodeErrorVerifikasi(body) != "DESTINATION_ACCOUNT_REQUIRED" {
		t.Fatalf("tujuan kosong harus ditolak fail-closed, dapat %d %v", status, body)
	}

	var accountB string
	if err := f.pool.QueryRow(ctx, `INSERT INTO bank_accounts (bank,nama_pemilik,nomor,is_active)
		VALUES ('BRI','Cabang B','9876543210',true) RETURNING id`).Scan(&accountB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO branch_bank_accounts
		(branch_id,bank_account_id,is_allowed,assigned_by) VALUES ($1,$2,true,$3)`, f.branchB, accountB, f.adminSA); err != nil {
		t.Fatal(err)
	}
	status, body = verifyHTTP(t, f, cookies, csrf, id,
		bodyVerifikasiCocokKe(t, f, id, &total, "", accountB))
	if status != http.StatusConflict || kodeErrorVerifikasi(body) != "BANK_ACCOUNT_NOT_ALLOWED" {
		t.Fatalf("rekening cabang lain harus ditolak, dapat %d %v", status, body)
	}
	if st, _, _, _ := statusPesanan(t, f, id); st != "submitted" {
		t.Fatalf("order harus tetap submitted setelah tujuan ditolak, dapat %s", st)
	}
}

func TestPGDokumenDoneFailClosedUntukSnapshotParsialDanPointerHilang(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	repo := documentpg.New(f.pool)

	transferID, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-918")
	status, body := verifyHTTP(t, f, cookies, csrf, transferID, bodyVerifikasiCocok(t, f, transferID, &total, ""))
	if status != http.StatusOK {
		t.Fatalf("prasyarat transfer Done gagal: %d %v", status, body)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE verification_logs SET destination_results='[]'::jsonb
		WHERE id=(SELECT done_verification_log_id FROM orders WHERE id=$1)`, transferID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.OrderForDocument(ctx, transferID); err == nil || !strings.Contains(err.Error(), "snapshot tujuan tidak lengkap") {
		t.Fatalf("snapshot parsial harus fail closed, dapat %v", err)
	}

	cncID, cncTotal := seedPesananSubmitted(t, f, f.branchA, "2026-10-919")
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET is_cnc=true WHERE id=$1`, cncID); err != nil {
		t.Fatal(err)
	}
	status, body = verifyHTTP(t, f, cookies, csrf, cncID, bodyVerifikasiCocok(t, f, cncID, &cncTotal, ""))
	if status != http.StatusOK {
		t.Fatalf("prasyarat C&C Done gagal: %d %v", status, body)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET done_verification_log_id=NULL WHERE id=$1`, cncID); err == nil {
		t.Fatal("constraint database harus menolak Done tanpa pointer verifikasi")
	}

	// Pointer ke attempt sukses milik order lain tidak boleh diterima.
	leftID, leftTotal := seedPesananSubmitted(t, f, f.branchA, "2026-10-920")
	rightID, rightTotal := seedPesananSubmitted(t, f, f.branchA, "2026-10-921")
	if status, body = verifyHTTP(t, f, cookies, csrf, leftID, bodyVerifikasiCocok(t, f, leftID, &leftTotal, "")); status != http.StatusOK {
		t.Fatalf("prasyarat left Done gagal: %d %v", status, body)
	}
	if status, body = verifyHTTP(t, f, cookies, csrf, rightID, bodyVerifikasiCocok(t, f, rightID, &rightTotal, "")); status != http.StatusOK {
		t.Fatalf("prasyarat right Done gagal: %d %v", status, body)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET done_verification_log_id=(SELECT done_verification_log_id FROM orders WHERE id=$2) WHERE id=$1`, leftID, rightID); err == nil {
		t.Fatal("foreign key database harus menolak pointer verifikasi lintas pesanan")
	}

	// Jumlah snapshot sama tidak cukup bila UUID bukti berasal dari order lain.
	foreignID, foreignTotal := seedPesananSubmitted(t, f, f.branchA, "2026-10-922")
	foreignProof := seedBuktiOCR(t, f, foreignID, "asing.jpg", berkasJPEGUji("asing"))
	if status, body = verifyHTTP(t, f, cookies, csrf, foreignID, bodyVerifikasiCocok(t, f, foreignID, &foreignTotal, "")); status != http.StatusOK {
		t.Fatalf("prasyarat foreign Done gagal: %d %v", status, body)
	}
	targetID, targetTotal := seedPesananSubmitted(t, f, f.branchA, "2026-10-923")
	seedBuktiOCR(t, f, targetID, "target.jpg", berkasJPEGUji("target"))
	if status, body = verifyHTTP(t, f, cookies, csrf, targetID, bodyVerifikasiCocok(t, f, targetID, &targetTotal, "")); status != http.StatusOK {
		t.Fatalf("prasyarat target Done gagal: %d %v", status, body)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE verification_logs SET destination_results=jsonb_set(destination_results,'{0,payment_proof_id}',to_jsonb($2::text))
		WHERE id=(SELECT done_verification_log_id FROM orders WHERE id=$1)`, targetID, foreignProof); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.OrderForDocument(ctx, targetID); err == nil || !strings.Contains(err.Error(), "snapshot tujuan tidak valid") {
		t.Fatalf("UUID bukti asing harus fail closed, dapat %v", err)
	}

	// Semua snapshot divalidasi sebelum dedup rekening yang sama.
	multiID, multiTotal := seedPesananSubmitted(t, f, f.branchA, "2026-10-924")
	seedBuktiOCR(t, f, multiID, "satu.jpg", berkasJPEGUji("satu"))
	seedBuktiOCR(t, f, multiID, "dua.jpg", berkasJPEGUji("dua"))
	if status, body = verifyHTTP(t, f, cookies, csrf, multiID, bodyVerifikasiCocok(t, f, multiID, &multiTotal, "")); status != http.StatusOK {
		t.Fatalf("prasyarat multi Done gagal: %d %v", status, body)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE verification_logs SET destination_results=jsonb_set(destination_results,'{1,bank_snapshot}','"BRI"'::jsonb)
		WHERE id=(SELECT done_verification_log_id FROM orders WHERE id=$1)`, multiID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.OrderForDocument(ctx, multiID); err == nil || !strings.Contains(err.Error(), "identitas rekening tidak cocok") {
		t.Fatalf("snapshot kedua rusak pada rekening sama harus fail closed, dapat %v", err)
	}
}

// TestPGVerifikasiPerluReviewTidakMenyelesaikan menegakkan PRD §7.1: perlu
// review adalah sub-status verifikasi, bukan status pesanan — dan tidak
// melahirkan dokumen/email.
func TestPGVerifikasiPerluReviewTidakMenyelesaikan(t *testing.T) {
	f := newPGFixture(t)
	id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-119")
	seedBuktiOCR(t, f, id, "manual-review.jpg", berkasJPEGUji("manual-review"))
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	status, body := verifyHTTP(t, f, cookies, csrf, id,
		fmt.Sprintf(`{"hasil":"perlu_review","total_terbaca":%d,"alasan":"kurang_bayar","catatan":"kurang 50rb"}`, total-50000))
	if status != http.StatusOK {
		t.Fatalf("penandaan review harus 200, dapat %d (%v)", status, body)
	}
	hasil := dataMap(t, body)
	order, _ := hasil["order"].(map[string]any)
	if order["status"] != "submitted" || order["verif_status"] != "perlu_review" {
		t.Errorf("pesanan harus tetap submitted/perlu_review, dapat %v", order)
	}
	log, _ := hasil["verifikasi"].(map[string]any)
	if log["selisih"] != float64(-50000) {
		t.Errorf("selisih harus total_terbaca - total_pesanan, dapat %v", log["selisih"])
	}

	st, verif, alasan, doneAt := statusPesanan(t, f, id)
	if st != "submitted" || verif != "perlu_review" || alasan != "kurang_bayar" {
		t.Errorf("keadaan pesanan salah: %s/%s/%s", st, verif, alasan)
	}
	if doneAt != nil && *doneAt != "" {
		t.Errorf("pesanan yang perlu review tidak boleh punya done_at: %v", *doneAt)
	}
	if n := pekerjaanDokumen(t, f, id); n != 0 {
		t.Errorf("perlu review tidak boleh menitipkan pekerjaan dokumen, dapat %d", n)
	}
	if n := hitungBaris(t, f, "documents", "order_id", id); n != 0 {
		t.Errorf("perlu review tidak boleh menghasilkan dokumen, dapat %d", n)
	}
	if n := hitungBaris(t, f, "email_outbox", "order_id", id); n != 0 {
		t.Errorf("perlu review tidak boleh mengantre email, dapat %d", n)
	}
	var destinationJSON []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT destination_results FROM verification_logs WHERE order_id=$1`, id).Scan(&destinationJSON); err != nil {
		t.Fatal(err)
	}
	var destinations []payment.DestinationResult
	if err := json.Unmarshal(destinationJSON, &destinations); err != nil {
		t.Fatal(err)
	}
	if len(destinations) != 1 || destinations[0].Source != payment.MetodeManual || destinations[0].Status != "unreadable" {
		t.Fatalf("review manual harus menyimpan hasil per bukti: %+v", destinations)
	}

	// Admin boleh menimpa keputusan review dengan persetujuan (PRD §5).
	status, body = verifyHTTP(t, f, cookies, csrf, id, bodyVerifikasiCocok(t, f, id, &total, ""))
	if status != http.StatusOK {
		t.Fatalf("verifikasi ulang sebagai setuju harus 200, dapat %d (%v)", status, body)
	}
	st, verif, alasan, _ = statusPesanan(t, f, id)
	if st != "done" || verif != "cocok" || alasan != "" {
		t.Errorf("sesudah disetujui harus done/cocok tanpa alasan review, dapat %s/%s/%s", st, verif, alasan)
	}
	if n := hitungBaris(t, f, "verification_logs", "order_id", id); n != 2 {
		t.Errorf("dua keputusan harus meninggalkan dua baris log (histori), dapat %d", n)
	}
}

// TestPGVerifikasiBentukDataDitolak menegakkan validasi bentuk pada endpoint.
func TestPGVerifikasiBentukDataDitolak(t *testing.T) {
	f := newPGFixture(t)
	id, _ := seedPesananSubmitted(t, f, f.branchA, "2026-10-120")
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	kasus := []struct {
		nama string
		body string
	}{
		{"review tanpa alasan", `{"hasil":"perlu_review"}`},
		{"review alasan tak dikenal", `{"hasil":"perlu_review","alasan":"entahlah"}`},
		{"setuju membawa alasan", `{"hasil":"cocok","alasan":"kurang_bayar"}`},
		{"hasil kosong", `{}`},
		{"nominal negatif", `{"hasil":"cocok","total_terbaca":-1}`},
	}
	for _, k := range kasus {
		status, body := verifyHTTP(t, f, cookies, csrf, id, k.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: harus 400, dapat %d (%v)", k.nama, status, body)
		}
	}
	// Tidak satu pun keputusan yang tersimpan.
	if n := hitungBaris(t, f, "verification_logs", "order_id", id); n != 0 {
		t.Errorf("permintaan tak sah tidak boleh menulis log, dapat %d", n)
	}
	st, verif, _, _ := statusPesanan(t, f, id)
	if st != "submitted" || verif != "belum" {
		t.Errorf("permintaan tak sah tidak boleh mengubah keadaan: %s/%s", st, verif)
	}

	// Nominal terbaca boleh kosong (nominal belum terbaca) — tetap boleh setuju.
	if status, body := verifyHTTP(t, f, cookies, csrf, id, bodyVerifikasiCocok(t, f, id, nil, "")); status != http.StatusOK {
		t.Errorf("setuju tanpa nominal harus 200, dapat %d (%v)", status, body)
	}
}

// TestPGVerifikasiBersamaanTepatSekali membuktikan persetujuan ganda paralel
// menghasilkan TEPAT satu set efek samping.
func TestPGVerifikasiBersamaanTepatSekali(t *testing.T) {
	f := newPGFixture(t)
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE admin_users SET notif_invoice = true, notif_surat_jalan = true WHERE id = $1`, f.adminSA); err != nil {
		t.Fatalf("pasang flag email admin: %v", err)
	}
	id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-121")
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	const percobaan = 8
	var (
		mu      sync.Mutex
		sukses  int
		konflik int
		other   []int
		wg      sync.WaitGroup
	)
	url := f.srv.URL + "/api/v1/admin/orders/" + id + "/verify"
	body := bodyVerifikasiCocok(t, f, id, &total, "")
	siap := make(chan struct{})
	for i := 0; i < percobaan; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-siap
			req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
			if err != nil {
				mu.Lock()
				other = append(other, -1)
				mu.Unlock()
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(httpx.CSRFHeader, csrf)
			for _, c := range cookies {
				req.AddCookie(c)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				mu.Lock()
				other = append(other, -1)
				mu.Unlock()
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			mu.Lock()
			defer mu.Unlock()
			switch resp.StatusCode {
			case http.StatusOK:
				sukses++
			case http.StatusConflict:
				konflik++
			default:
				other = append(other, resp.StatusCode)
			}
		}()
	}
	close(siap)
	wg.Wait()

	if sukses != 1 || konflik != percobaan-1 || len(other) != 0 {
		t.Fatalf("harus tepat 1 sukses dan %d konflik, dapat sukses=%d konflik=%d lain=%v",
			percobaan-1, sukses, konflik, other)
	}
	if n := hitungBaris(t, f, "verification_logs", "order_id", id); n != 1 {
		t.Errorf("harus tepat 1 log verifikasi, dapat %d", n)
	}
	if n := hitungBaris(t, f, "audit_logs", "entity_id", id); n != 1 {
		t.Errorf("harus tepat 1 entri audit, dapat %d", n)
	}
	if n := pekerjaanDokumen(t, f, id); n != 1 {
		t.Errorf("harus tepat 1 job render dokumen, dapat %d", n)
	}
	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("pekerjaan done harus berhasil: %v", err)
	}
	if n := hitungBaris(t, f, "documents", "order_id", id); n != 3 {
		t.Errorf("harus tetap satu set dokumen (3), dapat %d", n)
	}
	if n := hitungBaris(t, f, "email_outbox", "order_id", id); n != 3 {
		t.Errorf("harus tetap satu set email (3), dapat %d", n)
	}
}

// TestPGVerifikasiOtorisasiDanBentukId menegakkan CapAdminVerify (Manager &
// Superadmin), penolakan cabang, dan jawaban 404 untuk id bukan UUID.
func TestPGVerifikasiOtorisasiDanBentukId(t *testing.T) {
	f := newPGFixture(t)
	id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-122")
	body := bodyVerifikasiCocok(t, f, id, &total, "")

	// Cabang: 403 (bukan 404 — pesanannya miliknya sendiri).
	cookiesCabang, csrfCabang := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	if status, _ := verifyHTTP(t, f, cookiesCabang, csrfCabang, id, body); status != http.StatusForbidden {
		t.Errorf("cabang harus 403, dapat %d", status)
	}

	// Id yang bukan UUID dan id yang tidak ada dijawab 404 (bukan 500).
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	if status, _ := verifyHTTP(t, f, cookiesSA, csrfSA, "bukan-uuid", body); status != http.StatusNotFound {
		t.Errorf("id bukan UUID harus 404, dapat %d", status)
	}
	if status, _ := verifyHTTP(t, f, cookiesSA, csrfSA, "0199a1b2-0000-7000-8000-00000000dead", body); status != http.StatusNotFound {
		t.Errorf("id tidak dikenal harus 404, dapat %d", status)
	}

	// Draft belum bisa diselesaikan.
	var draftID string
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO orders (branch_id, status, total) VALUES ($1,'draft',0) RETURNING id`, f.branchA).Scan(&draftID); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	status, resp := verifyHTTP(t, f, cookiesSA, csrfSA, draftID, body)
	if status != http.StatusConflict {
		t.Errorf("draft harus 409, dapat %d (%v)", status, resp)
	} else {
		assertErrorShape(t, resp, "ORDER_NOT_SUBMITTED")
	}

	// Manager juga boleh memverifikasi (PRD §2.1).
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	if status, resp := verifyHTTP(t, f, cookiesMgr, csrfMgr, id, body); status != http.StatusOK {
		t.Fatalf("manager harus boleh verifikasi, dapat %d (%v)", status, resp)
	}
	// Verifikasi kedua: 409 tanpa efek samping tambahan.
	status, resp = verifyHTTP(t, f, cookiesSA, csrfSA, id, body)
	if status != http.StatusConflict {
		t.Fatalf("verifikasi kedua harus 409, dapat %d (%v)", status, resp)
	}
	assertErrorShape(t, resp, "ORDER_ALREADY_DONE")
	st, verif, _, _ := statusPesanan(t, f, id)
	if st != "done" || verif != "cocok" {
		t.Errorf("keadaan harus tetap done/cocok, dapat %s/%s", st, verif)
	}
	if n := hitungBaris(t, f, "verification_logs", "order_id", id); n != 1 {
		t.Errorf("verifikasi kedua tidak boleh menambah log, dapat %d", n)
	}
	if n := pekerjaanDokumen(t, f, id); n != 1 {
		t.Errorf("verifikasi kedua tidak boleh menambah pekerjaan, dapat %d", n)
	}
}

// TestPGPekerjaanMenungguDokumenSebelumEmail membuktikan urutan yang diminta
// kartu: selama mesin PDF belum siap, pekerjaan GAGAL dan tidak ada satu pun
// email yang diantre; setelah mesin siap, pengulangan pekerjaan menghasilkan
// dokumen sekaligus emailnya.
func TestPGPekerjaanMenungguDokumenSebelumEmail(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx,
		`UPDATE admin_users SET notif_invoice = true, notif_surat_jalan = true WHERE id = $1`, f.adminSA); err != nil {
		t.Fatalf("pasang flag email admin: %v", err)
	}
	id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-123")
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	if status, body := verifyHTTP(t, f, cookies, csrf, id,
		bodyVerifikasiCocok(t, f, id, &total, "")); status != http.StatusOK {
		t.Fatalf("verifikasi harus 200, dapat %d (%v)", status, body)
	}

	// Mesin PDF belum tersedia (pdf.Disabled): pekerjaan menunggu.
	belumSiap := fulfillment.NewService(document.NewService(f.docRepo, f.objek, pdf.Disabled{}), f.notif)
	if _, err := belumSiap.PesananSelesai(ctx, id); err == nil {
		t.Fatal("pekerjaan harus gagal saat mesin PDF belum siap")
	}
	if n := hitungBaris(t, f, "documents", "order_id", id); n != 0 {
		t.Errorf("tanpa mesin PDF tidak boleh ada dokumen, dapat %d", n)
	}
	if n := hitungBaris(t, f, "email_outbox", "order_id", id); n != 0 {
		t.Errorf("email tidak boleh diantre sebelum dokumen ada, dapat %d", n)
	}

	// Mesin siap: pekerjaan yang sama diulang menghasilkan dokumen + email.
	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("pekerjaan ulang harus berhasil: %v", err)
	}
	if n := hitungBaris(t, f, "documents", "order_id", id); n != 3 {
		t.Errorf("dokumen harus lengkap sesudah mesin siap, dapat %d", n)
	}
	if n := hitungBaris(t, f, "email_outbox", "order_id", id); n != 3 {
		t.Errorf("email harus diantre sesudah dokumen ada, dapat %d", n)
	}
}

// TestPGVerifikasiTanpaPenjadwalDitolak membuktikan sikap fail-closed: tanpa
// penjadwal pekerjaan dokumen, pesanan TIDAK diselesaikan (tidak pernah ada
// pesanan Done tanpa dokumen dan email).
func TestPGVerifikasiTanpaPenjadwalDitolak(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-124")

	// Repository tanpa WithJobQueue (konfigurasi salah).
	tanpaPenjadwal := payment.NewVerificationService(paymentpg.New(f.pool))
	actor := auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
	_, err := tanpaPenjadwal.MarkVerified(ctx, actor, id, payment.VerifyInput{Hasil: payment.VerifCocok, TotalTerbaca: &total})
	if !errors.Is(err, payment.ErrQueueUnavailable) {
		t.Fatalf("tanpa penjadwal harus ErrQueueUnavailable, dapat %v", err)
	}
	st, verif, _, _ := statusPesanan(t, f, id)
	if st != "submitted" || verif != "belum" {
		t.Errorf("pesanan tidak boleh berubah, dapat %s/%s", st, verif)
	}
	if n := hitungBaris(t, f, "verification_logs", "order_id", id); n != 0 {
		t.Errorf("tidak boleh ada log verifikasi, dapat %d", n)
	}
}

func TestPGVerifikasiTransferOnlyMenolakAlokasiDepositAktifManualDanAI(t *testing.T) {
	t.Run("manual", func(t *testing.T) {
		f := newPGFixture(t)
		id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-991")
		seedAlokasiDepositAktif(t, f, id, f.branchA, 400)
		cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
		status, body := verifyHTTP(t, f, cookies, csrf, id, bodyVerifikasiCocok(t, f, id, &total, ""))
		if status != http.StatusUnprocessableEntity || kodeErrorVerifikasi(body) != "PAYMENT_BREAKDOWN_MISMATCH" {
			t.Fatalf("manual tanpa rincian settlement harus 422, dapat %d %v", status, body)
		}
		st, verif, _, _ := statusPesanan(t, f, id)
		if st != "submitted" || verif != "belum" || hitungBaris(t, f, "verification_logs", "order_id", id) != 0 {
			t.Fatalf("manual mengubah state: %s/%s", st, verif)
		}
	})

	t.Run("AI", func(t *testing.T) {
		f := newPGFixture(t)
		ctx := context.Background()
		id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-992")
		seedAlokasiDepositAktif(t, f, id, f.branchA, 400)
		_ = bodyVerifikasiCocok(t, f, id, &total, "")
		var proofID string
		if err := f.pool.QueryRow(ctx, `SELECT id FROM payment_proofs WHERE order_id=$1 ORDER BY created_at LIMIT 1`, id).Scan(&proofID); err != nil {
			t.Fatal(err)
		}
		_, err := payment.NewVerificationService(f.paymentsRepo).MarkVerified(ctx, payment.SystemActor(), id, payment.VerifyInput{
			Hasil: payment.VerifCocok, Metode: payment.MetodeAI, TotalTerbaca: &total,
			ProofDestinations: []payment.ProofDestination{{PaymentProofID: proofID, BankAccountID: f.bankDefault}},
		})
		if !errors.Is(err, payment.ErrInvalidData) {
			t.Fatalf("AI harus fail-closed ErrInvalidData, dapat %v", err)
		}
		st, verif, _, _ := statusPesanan(t, f, id)
		if st != "submitted" || verif != "belum" || hitungBaris(t, f, "verification_logs", "order_id", id) != 0 {
			t.Fatalf("AI mengubah state: %s/%s", st, verif)
		}
	})
}

// TestPGCnCDibayarDepositSampaiDone menjalankan alur C&C berdeposit lewat HTTP:
// submit tanpa bukti memotong saldo, pesanan tetap Submitted (tidak ada
// auto-Done) sampai admin menyetujui rincian deposit, lalu Done dengan deposit
// = total dan transfer = 0. Pesanan bernilai tanpa sumber dana sama sekali
// tidak bisa disetujui.
func TestPGCnCDibayarDepositSampaiDone(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_cnc = true WHERE id = $1`, f.branchA); err != nil {
		t.Fatalf("tandai cabang C&C: %v", err)
	}
	seedSaldoDeposit(t, f, f.branchA, 500000)
	cookiesCabang, csrfCabang := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")
	draft := buatDraftHTTP(t, f, cookiesCabang, csrfCabang, produk, 2)
	id, _ := draft["id"].(string)
	total := int64(draft["total"].(float64))

	var versiSaldo int64
	if err := f.pool.QueryRow(ctx, `SELECT journal_version FROM branch_deposit_accounts WHERE branch_id=$1`, f.branchA).Scan(&versiSaldo); err != nil {
		t.Fatal(err)
	}
	resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesCabang, csrfCabang,
		nil, strconv.FormatInt(versiSaldo, 10))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit C&C berdeposit harus 200, dapat %d (%v)", resp.StatusCode, body)
	}
	if st, _, _, doneAt := statusPesanan(t, f, id); st != "submitted" || (doneAt != nil && *doneAt != "") {
		t.Fatalf("dibayar deposit belum berarti Done tanpa verifikasi: %s / %v", st, doneAt)
	}

	var alokasi string
	var versi int
	if err := f.pool.QueryRow(ctx, `SELECT a.id::text, o.versi FROM deposit_allocations a
		JOIN orders o ON o.id = a.order_id WHERE a.order_id=$1`, id).Scan(&alokasi, &versi); err != nil {
		t.Fatalf("submit harus menulis satu alokasi deposit: %v", err)
	}
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	if status, res := verifyHTTP(t, f, cookiesSA, csrfSA, id, `{"hasil":"cocok"}`); status != http.StatusUnprocessableEntity ||
		kodeErrorVerifikasi(res) != "PAYMENT_BREAKDOWN_MISMATCH" {
		t.Fatalf("persetujuan tanpa rincian deposit harus 422, dapat %d (%v)", status, res)
	}
	if status, res := verifyKey(t, f, cookiesSA, csrfSA, id, settlementBody(f, versi, []string{alokasi}, nil), ""); status != http.StatusOK {
		t.Fatalf("persetujuan rincian deposit harus 200, dapat %d (%v)", status, res)
	}
	var st, verif string
	var depositDone, transferDone int64
	if err := f.pool.QueryRow(ctx, `SELECT status::text, verif_status::text, done_deposit_amount, done_transfer_amount
		FROM orders WHERE id=$1`, id).Scan(&st, &verif, &depositDone, &transferDone); err != nil {
		t.Fatal(err)
	}
	if st != "done" || verif != "cocok" || depositDone != total || transferDone != 0 {
		t.Fatalf("Done harus tercatat deposit=%d transfer=0, dapat %s/%s deposit=%d transfer=%d",
			total, st, verif, depositDone, transferDone)
	}

	// Pesanan C&C bernilai tanpa alokasi deposit maupun bukti: tidak ada sumber
	// dana yang bisa disetujui.
	kosong, _ := seedPesananSubmitted(t, f, f.branchA, "2026-10-cnc981")
	if status, res := verifyHTTP(t, f, cookiesSA, csrfSA, kosong, `{"hasil":"cocok","proof_destinations":[]}`); status != http.StatusUnprocessableEntity ||
		kodeErrorVerifikasi(res) != "PAYMENT_PROOF_REQUIRED" {
		t.Fatalf("pesanan tanpa sumber dana harus 422 PAYMENT_PROOF_REQUIRED, dapat %d (%v)", status, res)
	}
	if st, _, _, _ := statusPesanan(t, f, kosong); st != "submitted" {
		t.Fatalf("pesanan tanpa sumber dana harus tetap submitted, dapat %s", st)
	}
}
