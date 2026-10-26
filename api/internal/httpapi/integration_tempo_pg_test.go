package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	backuppg "github.com/grahita/portal/api/internal/backup/postgres"
	"github.com/grahita/portal/api/internal/receivable"
	"github.com/xuri/excelize/v2"
)

func tempoSubmitted(t *testing.T, f *pgFixture, day string, total int64) string {
	t.Helper()
	var id string
	err := f.pool.QueryRow(t.Context(), `INSERT INTO orders
 (branch_id,nomor,status,verif_status,tanggal,total,submitted_at,is_tempo,tanggal_bayar_snapshot)
 VALUES($1,'TEMPO-'||$2::text,'submitted','belum',$2::date,$3,now(),true,5)
 RETURNING id::text`, f.branchA, day, total).Scan(&id)
	if err != nil {
		t.Fatalf("buat pesanan Tempo: %v", err)
	}
	var productID string
	if err := f.pool.QueryRow(t.Context(), `SELECT product_id::text FROM branch_prices WHERE branch_id=$1 LIMIT 1`, f.branchA).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO order_lines
(order_id,line_no,jenis,product_id,nama_snapshot,qty,harga_satuan,serial_number)
VALUES($1,1,'product',$2,'Bio Magnetik',1,$3,$4)`, id, productID, total, strings.ReplaceAll(day, "-", "")+"001"); err != nil {
		t.Fatalf("baris Tempo: %v", err)
	}
	return id
}

func TestPGTempoApprovalFIFOdanKreditTerpisah(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5,tahan_bila_telat=true WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	superadmin := auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
	branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, BranchID: f.branchA, Nama: "Cabang A"}
	first := tempoSubmitted(t, f, "2026-08-10", 100000)
	second := tempoSubmitted(t, f, "2026-08-11", 200000)
	due, err := svc.Approve(ctx, manager, first)
	if err != nil {
		t.Fatalf("approve pertama: %v", err)
	}
	expected := receivable.NextPayDay(time.Now(), 5).Format("2006-01-02")
	if due != expected {
		t.Fatalf("jatuh tempo approval terlambat: %s, mau %s", due, expected)
	}
	if _, err := svc.Approve(ctx, manager, second); err != nil {
		t.Fatalf("approve kedua: %v", err)
	}
	proof := func(amount int64, tail byte) receivable.Repayment {
		t.Helper()
		r, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{Amount: amount, BankAccountID: f.bankDefault, Filename: "bukti.jpg", Body: []byte{0xff, 0xd8, 0xff, tail}})
		if err != nil {
			t.Fatalf("unggah bukti: %v", err)
		}
		return r
	}
	firstPay := proof(150000, 1)
	if _, err := svc.Decide(ctx, manager, firstPay.ID, "verify", 150000, ""); err != nil {
		t.Fatalf("verifikasi parsial: %v", err)
	}
	if _, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{
		Amount: 150000, BankAccountID: f.bankDefault, Filename: "ulang.jpg", Body: []byte{0xff, 0xd8, 0xff, 1},
	}); !errors.Is(err, receivable.ErrDuplicateProof) {
		t.Fatalf("bukti identik tidak boleh dihitung dua kali: %v", err)
	}
	var status1, status2 string
	var paid1, paid2 int64
	if err := f.pool.QueryRow(ctx, `SELECT status::text,tempo_paid_amount FROM orders WHERE id=$1`, first).Scan(&status1, &paid1); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT status::text,tempo_paid_amount FROM orders WHERE id=$1`, second).Scan(&status2, &paid2); err != nil {
		t.Fatal(err)
	}
	if status1 != "done" || paid1 != 100000 || status2 != "approved_unpaid" || paid2 != 50000 {
		t.Fatalf("FIFO salah: %s %d, %s %d", status1, paid1, status2, paid2)
	}
	balance, err := svc.Overview(ctx, branch)
	if err != nil {
		t.Fatal(err)
	}
	if balance.TotalReceivable != 150000 {
		t.Fatalf("sisa harus 150000: %+v", balance)
	}
	secondPay := proof(200000, 2)
	if _, err := svc.Decide(ctx, manager, secondPay.ID, "verify", 200000, ""); !errors.Is(err, receivable.ErrSuperadmin) {
		t.Fatalf("lebih bayar harus Superadmin: %v", err)
	}
	if _, err := svc.Decide(ctx, superadmin, secondPay.ID, "verify", 200000, ""); err != nil {
		t.Fatalf("verifikasi Superadmin: %v", err)
	}
	var credit int64
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(sum(remaining),0)::bigint FROM tempo_credit_entries WHERE branch_id=$1`, f.branchA).Scan(&credit); err != nil {
		t.Fatal(err)
	}
	if credit != 50000 {
		t.Fatalf("saldo Tempo seharusnya 50000, dapat %d", credit)
	}
	var depositBalance int64
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(sum(balance),0)::bigint FROM branch_deposit_accounts WHERE branch_id=$1`, f.branchA).Scan(&depositBalance); err != nil {
		t.Fatal(err)
	}
	if depositBalance != 0 {
		t.Fatalf("kredit Tempo tidak boleh masuk deposit C&C: %d", depositBalance)
	}
	third := tempoSubmitted(t, f, "2026-10-25", 100000)
	if _, err := svc.Approve(ctx, manager, third); err != nil {
		t.Fatalf("approve dengan kredit: %v", err)
	}
	var paid3 int64
	if err := f.pool.QueryRow(ctx, `SELECT tempo_paid_amount FROM orders WHERE id=$1`, third).Scan(&paid3); err != nil {
		t.Fatal(err)
	}
	if paid3 != 50000 {
		t.Fatalf("kredit tidak teralokasi ke invoice baru: %d", paid3)
	}
	xlsx, err := svc.ExportXLSX(ctx, superadmin)
	if err != nil {
		t.Fatalf("ekspor piutang: %v", err)
	}
	book, err := excelize.OpenReader(bytes.NewReader(xlsx))
	if err != nil {
		t.Fatalf("buka XLSX: %v", err)
	}
	defer book.Close()
	rows, err := book.GetRows("Piutang Tempo")
	if err != nil || len(rows) < 2 {
		t.Fatalf("isi XLSX: %d %v", len(rows), err)
	}
	id, err := svc.IssueMonth(ctx, f.branchA, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || id == "" {
		t.Fatalf("terbit tagihan: %q %v", id, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE admin_users SET notif_invoice=(id=$1) WHERE id IN ($1,$2)`, f.adminSA, f.adminMgr); err != nil {
		t.Fatal(err)
	}
	if err := svc.RenderStatement(ctx, f.objek, rendererUji{}, id); err != nil {
		t.Fatalf("render surat tagihan: %v", err)
	}
	statements, err := svc.Statements(ctx, branch)
	if err != nil || len(statements) != 1 || !statements[0].Ready {
		t.Fatalf("surat tagihan belum tersedia: %+v %v", statements, err)
	}
	var statementEmails int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE jenis='tempo_statement'`).Scan(&statementEmails); err != nil {
		t.Fatal(err)
	}
	if statementEmails != 2 {
		t.Fatalf("surat tagihan harus dikirim ke cabang dan admin penerima invoice: %d", statementEmails)
	}
	backupRepo := backuppg.New(f.pool)
	documentKeys, err := backupRepo.KunciDokumen(ctx)
	if err != nil || !slices.Contains(documentKeys, "tempo-statement/"+id+".pdf") {
		t.Fatalf("PDF Surat Tagihan tidak tercakup dalam cadangan objek: %v %v", documentKeys, err)
	}
	proofKeys, err := backupRepo.KunciProofs(ctx)
	if err != nil || !slices.ContainsFunc(proofKeys, func(key string) bool { return strings.HasPrefix(key, "tempo-proof/") }) {
		t.Fatalf("bukti pelunasan tidak tercakup dalam cadangan objek: %v %v", proofKeys, err)
	}
}

func TestPGRevisiTempoSetelahBayarSebagianMenghasilkanKredit(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, BranchID: f.branchA, Nama: "Cabang A"}
	id := tempoSubmitted(t, f, "2026-10-20", 100000)
	if _, err := svc.Approve(ctx, manager, id); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("dokumen awal: %v", err)
	}
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	statementV1, err := svc.IssueMonth(ctx, f.branchA, month)
	if err != nil || statementV1 == "" {
		t.Fatalf("tagihan awal: %q %v", statementV1, err)
	}
	if err := svc.RenderStatement(ctx, f.objek, rendererUji{}, statementV1); err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{Amount: 40000, BankAccountID: f.bankDefault, Filename: "bukti.jpg", Body: []byte{0xff, 0xd8, 0xff, 7}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(ctx, manager, receipt.ID, "verify", 40000, ""); err != nil {
		t.Fatal(err)
	}
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, result := reviseHTTP(t, f, cookiesMgr, csrfMgr, id, reviseBody("harga kurang", `{"line_no":1,"qty":1,"harga_satuan":130000}`))
	if status != 200 {
		t.Fatalf("revisi naik harus 200: %d %+v", status, result)
	}
	var total, paid int64
	var state string
	if err := f.pool.QueryRow(ctx, `SELECT total,tempo_paid_amount,status::text FROM orders WHERE id=$1`, id).Scan(&total, &paid, &state); err != nil {
		t.Fatal(err)
	}
	if total != 130000 || paid != 40000 || state != "approved_unpaid" {
		t.Fatalf("revisi naik tidak mempertahankan bayar: %d %d %s", total, paid, state)
	}
	if err := jalankanPekerjaanRevisi(t, f, id, 1); err != nil {
		t.Fatalf("invoice revisi R1: %v", err)
	}
	statementV2, err := svc.IssueMonth(ctx, f.branchA, month)
	if err != nil || statementV2 == statementV1 {
		t.Fatalf("koreksi harus membuat versi tagihan baru: %q %v", statementV2, err)
	}
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, result = reviseHTTP(t, f, cookiesSA, csrfSA, id, reviseBody("harga terlalu tinggi", `{"line_no":1,"qty":1,"harga_satuan":20000}`))
	if status != 200 {
		t.Fatalf("revisi turun harus 200: %d %+v", status, result)
	}
	if err := f.pool.QueryRow(ctx, `SELECT total,tempo_paid_amount,status::text FROM orders WHERE id=$1`, id).Scan(&total, &paid, &state); err != nil {
		t.Fatal(err)
	}
	if total != 20000 || paid != 20000 || state != "done" {
		t.Fatalf("revisi turun salah: %d %d %s", total, paid, state)
	}
	var credit int64
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(sum(remaining),0)::bigint FROM tempo_credit_entries WHERE branch_id=$1`, f.branchA).Scan(&credit); err != nil {
		t.Fatal(err)
	}
	if credit != 20000 {
		t.Fatalf("kredit koreksi harus 20000, dapat %d", credit)
	}
	if err := jalankanPekerjaanRevisi(t, f, id, 2); err != nil {
		t.Fatalf("invoice revisi R2: %v", err)
	}
	var docCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM documents WHERE order_id=$1 AND jenis='invoice_pdf'`, id).Scan(&docCount); err != nil {
		t.Fatal(err)
	}
	if docCount != 3 {
		t.Fatalf("invoice versi 0,R1,R2 harus ada: %d", docCount)
	}
	statementV3, err := svc.IssueMonth(ctx, f.branchA, month)
	if err != nil || statementV3 == statementV2 {
		t.Fatalf("koreksi kedua harus membuat versi tagihan baru: %q %v", statementV3, err)
	}
	var versions int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_statements WHERE branch_id=$1 AND period_month=$2::date`, f.branchA, month).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 3 {
		t.Fatalf("surat tagihan harus tiga versi, dapat %d", versions)
	}
	history, err := svc.Statements(ctx, branch)
	if err != nil || len(history) != 3 || !history[0].Latest || history[0].Version != 3 ||
		history[1].Latest || history[1].Status != "versi_lama" || history[2].Status != "versi_lama" {
		t.Fatalf("riwayat PDF tagihan harus dapat ditelusuri: %+v %v", history, err)
	}
}

func TestPGTempoSubmitSnapshotDanTahanPesanan(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5,tahan_bila_telat=true WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	cookies, csrf := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	product := produkKode(t, f, "P-01")
	makeDraft := func() string { t.Helper(); return buatDraftHTTP(t, f, cookies, csrf, product, 1)["id"].(string) }
	submit := func(id string) (int, map[string]any) {
		t.Helper()
		resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookies, csrf, nil, "")
		return resp.StatusCode, body
	}
	first := makeDraft()
	if status, body := submit(first); status != 200 {
		t.Fatalf("Tempo tanpa bukti harus terkirim: %d %+v", status, body)
	}
	var payDay int16
	var tempo bool
	if err := f.pool.QueryRow(ctx, `SELECT is_tempo,tanggal_bayar_snapshot FROM orders WHERE id=$1`, first).Scan(&tempo, &payDay); err != nil {
		t.Fatal(err)
	}
	if !tempo || payDay != 5 {
		t.Fatalf("snapshot salah: %v %d", tempo, payDay)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET tanggal_bayar=10 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	second := makeDraft()
	if status, body := submit(second); status != 200 {
		t.Fatalf("Tempo setelah ubah tanggal bayar: %d %+v", status, body)
	}
	if err := f.pool.QueryRow(ctx, `SELECT tanggal_bayar_snapshot FROM orders WHERE id=$1`, second).Scan(&payDay); err != nil {
		t.Fatal(err)
	}
	if payDay != 10 {
		t.Fatalf("perubahan tanggal bayar belum berlaku: %d", payDay)
	}
	svc := receivable.New(f.pool, f.jobs)
	mgr := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	if _, err := svc.Approve(ctx, mgr, first); err != nil {
		t.Fatal(err)
	}
	// Simulasikan hari sesudah jatuh tempo tanpa menggantung uji pada jam dinding.
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET tempo_due_date=CURRENT_DATE-1 WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tempo_charges SET due_date=CURRENT_DATE-1 WHERE order_id=$1`, first); err != nil {
		t.Fatal(err)
	}
	third := makeDraft()
	if status, body := submit(third); status != 409 || body["error"].(map[string]any)["code"] != "TEMPO_ORDER_HELD" {
		t.Fatalf("tunggakan harus menahan submit: %d %+v", status, body)
	}
	branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, BranchID: f.branchA, Nama: "Cabang A"}
	pending, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{Amount: 100000, BankAccountID: f.bankDefault, Filename: "bukti.jpg", Body: []byte{0xff, 0xd8, 0xff, 9}})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" {
		t.Fatalf("status bukti: %+v", pending)
	}
	if status, body := submit(third); status != 200 {
		t.Fatalf("bukti cukup harus memberi tenggang: %d %+v", status, body)
	}
	// Unggahan ulang tidak memperpanjang jendela yang sudah berakhir.
	if _, err := f.pool.Exec(ctx, `UPDATE tempo_hold_grace SET ends_on=CURRENT_DATE-1 WHERE branch_id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{Amount: 100000, BankAccountID: f.bankDefault, Filename: "bukti2.jpg", Body: []byte{0xff, 0xd8, 0xff, 10}}); err != nil {
		t.Fatal(err)
	}
	fourth := makeDraft()
	if status, body := submit(fourth); status != 409 {
		t.Fatalf("unggah ulang tidak boleh mereset tenggang: %d %+v", status, body)
	}
	// Admin dapat melepas hold melalui toggle cabang, dan tindakan tercatat audit.
	adminCookie, adminCSRF := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/branches/"+f.branchA, `{"tahan_bila_telat":false}`, adminCookie, adminCSRF)
	if status != 200 {
		t.Fatalf("matikan toggle: %d %+v", status, body)
	}
	if status, body := submit(fourth); status != 200 {
		t.Fatalf("toggle off harus melepas hold: %d %+v", status, body)
	}
}

func TestPGPilihanPembayaranCampuranDanTahanHanyaTempo(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET allow_transfer=true,is_cnc=true,is_tempo=true,tanggal_bayar=5,tahan_bila_telat=true WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	seedSaldoDeposit(t, f, f.branchA, 200000)
	cookies, csrf := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	product := produkKode(t, f, "P-01")
	makeDraft := func() string {
		t.Helper()
		return buatDraftHTTP(t, f, cookies, csrf, product, 1)["id"].(string)
	}
	submit := func(id, mode, version string, proof map[string][]byte) (int, map[string]any) {
		t.Helper()
		resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookies, csrf, proof, version, mode)
		return resp.StatusCode, body
	}
	first := makeDraft()
	if status, body := submit(first, "", "", nil); status != http.StatusBadRequest || kodeErrorVerifikasi(body) != "VALIDATION_FAILED" {
		t.Fatalf("beberapa pilihan wajib dipilih eksplisit: %d %+v", status, body)
	}
	if status, body := submit(first, "tempo", "", nil); status != http.StatusOK {
		t.Fatalf("Tempo pada cabang campuran gagal: %d %+v", status, body)
	}
	var tempo, cnc bool
	if err := f.pool.QueryRow(ctx, `SELECT is_tempo,is_cnc FROM orders WHERE id=$1`, first).Scan(&tempo, &cnc); err != nil || !tempo || cnc {
		t.Fatalf("snapshot Tempo: %v %v %v", tempo, cnc, err)
	}
	svc := receivable.New(f.pool, f.jobs)
	mgr := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	if _, err := svc.Approve(ctx, mgr, first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tempo_charges SET due_date=CURRENT_DATE-1 WHERE order_id=$1`, first); err != nil {
		t.Fatal(err)
	}
	blocked := makeDraft()
	if status, body := submit(blocked, "tempo", "", nil); status != http.StatusConflict || kodeErrorVerifikasi(body) != "TEMPO_ORDER_HELD" {
		t.Fatalf("Tempo harus ditahan: %d %+v", status, body)
	}
	if status, body := submit(blocked, "transfer", "", map[string][]byte{"transfer.jpg": berkasJPEGUji("multi-transfer")}); status != http.StatusOK {
		t.Fatalf("transfer tetap tersedia saat Tempo ditahan: %d %+v", status, body)
	}
	if err := f.pool.QueryRow(ctx, `SELECT is_tempo,is_cnc FROM orders WHERE id=$1`, blocked).Scan(&tempo, &cnc); err != nil || tempo || cnc {
		t.Fatalf("snapshot transfer: %v %v %v", tempo, cnc, err)
	}
	depositOrder := makeDraft()
	var version int64
	if err := f.pool.QueryRow(ctx, `SELECT journal_version FROM branch_deposit_accounts WHERE branch_id=$1`, f.branchA).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if status, body := submit(depositOrder, "deposit", strconv.FormatInt(version, 10), nil); status != http.StatusOK {
		t.Fatalf("deposit tetap tersedia saat Tempo ditahan: %d %+v", status, body)
	}
	if err := f.pool.QueryRow(ctx, `SELECT is_tempo,is_cnc FROM orders WHERE id=$1`, depositOrder).Scan(&tempo, &cnc); err != nil || tempo || !cnc {
		t.Fatalf("snapshot deposit: %v %v %v", tempo, cnc, err)
	}
}

func TestPGPengingatHMinus3TanggalAwalBulanTetapTerkirim(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=1 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	id := tempoSubmitted(t, f, "2026-10-15", 100000)
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET tanggal_bayar_snapshot=1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	if err := svc.SendReminders(ctx, now); err != nil {
		t.Fatalf("pengingat H-3: %v", err)
	}
	if err := svc.SendReminders(ctx, now); err != nil {
		t.Fatalf("ulang pengingat H-3: %v", err)
	}
	var count int
	var generic bool
	if err := f.pool.QueryRow(ctx, `SELECT count(*),COALESCE(bool_and(generic),false)
 FROM tempo_reminders WHERE branch_id=$1 AND due_date='2026-11-01' AND stage='h_minus_3'`, f.branchA).Scan(&count, &generic); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !generic {
		t.Fatalf("pengingat H-3 umum harus sekali: count=%d generic=%v", count, generic)
	}
}
