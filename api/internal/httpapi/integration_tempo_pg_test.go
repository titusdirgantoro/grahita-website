package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
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

func TestPGTagihanHistorisSesudahIzinTempoDicabut(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=true,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	branchCookies, branchCSRF := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	profil := func() map[string]any {
		t.Helper()
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/me/branch", "", branchCookies, "")
		if status != http.StatusOK {
			t.Fatalf("profil cabang: %d %+v", status, body)
		}
		return dataMap(t, body)
	}
	if profil()["has_tempo_history"] != false {
		t.Fatal("cabang tanpa pesanan Tempo tidak memiliki riwayat")
	}
	id := tempoSubmitted(t, f, "2026-10-20", 70000)
	if profil()["has_tempo_history"] != true {
		t.Fatal("pesanan Tempo yang dikirim harus tampak dalam riwayat")
	}
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	if _, err := svc.Approve(ctx, manager, id); err != nil {
		t.Fatalf("approve Tempo: %v", err)
	}
	adminCookies, adminCSRF := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/branches/"+f.branchA, `{"is_tempo":false}`, adminCookies, adminCSRF)
	if status != http.StatusOK {
		t.Fatalf("cabut izin Tempo: %d %+v", status, body)
	}
	if me := profil(); me["is_tempo"] != false || me["has_tempo_history"] != true {
		t.Fatalf("izin baru dan riwayat harus terpisah: %+v", me)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/tempo/overview", "", branchCookies, "")
	if status != http.StatusOK || dataMap(t, body)["total_receivable"] != float64(70000) {
		t.Fatalf("piutang lama tetap dapat dibaca: %d %+v", status, body)
	}
	branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, BranchID: f.branchA, Nama: "Cabang A"}
	receipt, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{
		Amount: 70000, BankAccountID: f.bankDefault, Filename: "bukti.jpg", Body: []byte{0xff, 0xd8, 0xff, 43},
	})
	if err != nil {
		t.Fatalf("unggah pembayaran setelah cabut izin: %v", err)
	}
	if _, err := svc.Decide(ctx, manager, receipt.ID, "verify", 70000, ""); err != nil {
		t.Fatalf("verifikasi pelunasan lama: %v", err)
	}
	var orderStatus string
	if err := f.pool.QueryRow(ctx, `SELECT status::text FROM orders WHERE id=$1`, id).Scan(&orderStatus); err != nil || orderStatus != "done" {
		t.Fatalf("pesanan lama harus lunas: %s %v", orderStatus, err)
	}
	if profil()["has_tempo_history"] != true {
		t.Fatal("riwayat Tagihan tetap tersedia setelah lunas")
	}
	product := produkKode(t, f, "P-01")
	draft := buatDraftHTTP(t, f, branchCookies, branchCSRF, product, 1)["id"].(string)
	resp, submitBody := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+draft+"/submit", branchCookies, branchCSRF, nil, "", "tempo")
	if resp.StatusCode != http.StatusConflict || kodeErrorVerifikasi(submitBody) != "PAYMENT_MODE_CHANGED" {
		t.Fatalf("izin dicabut harus menolak pesanan Tempo baru: %d %+v", resp.StatusCode, submitBody)
	}
}

func TestPGPiutangCabangAdminDanEksporSelaras(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, BranchID: f.branchA, Nama: "Cabang A"}
	first := tempoSubmitted(t, f, "2026-08-10", 100000)
	second := tempoSubmitted(t, f, "2026-09-10", 200000)
	for _, id := range []string{first, second} {
		if _, err := svc.Approve(ctx, manager, id); err != nil {
			t.Fatal(err)
		}
	}
	paid, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{
		Amount: 50000, BankAccountID: f.bankDefault, Filename: "bayar.jpg", Body: []byte{0xff, 0xd8, 0xff, 61},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(ctx, manager, paid.ID, "verify", 50000, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{
		Amount: 20000, BankAccountID: f.bankDefault, Filename: "menunggu.jpg", Body: []byte{0xff, 0xd8, 0xff, 62},
	}); err != nil {
		t.Fatal(err)
	}
	branchCookies, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	adminCookies, _ := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	getOverview := func(path string, cookies []*http.Cookie) map[string]any {
		t.Helper()
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+path, "", cookies, "")
		if status != http.StatusOK {
			t.Fatalf("overview %s: %d %+v", path, status, body)
		}
		return dataMap(t, body)
	}
	branchView := getOverview("/api/v1/tempo/overview", branchCookies)
	adminView := getOverview("/api/v1/admin/tempo/overview", adminCookies)
	if !reflect.DeepEqual(branchView["items"], adminView["items"]) {
		t.Fatalf("rincian piutang cabang dan admin berbeda: %v vs %v", branchView["items"], adminView["items"])
	}
	if branchView["total_receivable"] != float64(250000) || adminView["total_receivable"] != float64(250000) ||
		branchView["pending_proof"] != float64(20000) {
		t.Fatalf("bukti pending tidak boleh mengurangi piutang: cabang=%v admin=%v", branchView, adminView)
	}
	var dbRemaining int64
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount-paid_amount),0)::bigint FROM tempo_charges WHERE branch_id=$1`, f.branchA).Scan(&dbRemaining); err != nil || dbRemaining != 250000 {
		t.Fatalf("sisa buku besar: %d %v", dbRemaining, err)
	}
	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/api/v1/admin/tempo/export", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range adminCookies {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ekspor piutang: %d", resp.StatusCode)
	}
	book, err := excelize.OpenReader(resp.Body)
	if err != nil {
		t.Fatalf("buka ekspor piutang: %v", err)
	}
	defer book.Close()
	rows, err := book.GetRows("Piutang Tempo")
	items := adminView["items"].([]any)
	if err != nil || len(rows) != len(items)+1 || len(items) != 2 {
		t.Fatalf("jumlah baris ekspor dan Tagihan tidak sama: rows=%d items=%d err=%v", len(rows), len(items), err)
	}
	for _, raw := range items {
		item := raw.(map[string]any)
		period := item["period_month"].(string)
		var found []string
		for _, row := range rows[1:] {
			if len(row) >= 8 && row[1] == period {
				found = row
				break
			}
		}
		if found == nil {
			t.Fatalf("periode %s hilang dari ekspor", period)
		}
		want := []string{
			item["branch_name"].(string), period, item["due_date"].(string),
			strconv.FormatInt(int64(item["total"].(float64)), 10),
			strconv.FormatInt(int64(item["paid"].(float64)), 10),
			strconv.FormatInt(int64(item["remaining"].(float64)), 10),
			item["status"].(string), strconv.Itoa(int(item["age_days"].(float64))),
		}
		if !slices.Equal(found[:8], want) {
			t.Fatalf("ekspor periode %s tidak sama dengan Tagihan: %v vs %v", period, found[:8], want)
		}
	}
}

func TestPGStatusTagihanBerubahPadaBatasHariWIB(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	var today time.Time
	if err := f.pool.QueryRow(ctx, `SELECT (now() AT TIME ZONE 'Asia/Jakarta')::date`).Scan(&today); err != nil {
		t.Fatal(err)
	}
	id := tempoSubmitted(t, f, today.Format("2006-01-02"), 80000)
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, BranchID: f.branchA, Nama: "Cabang A"}
	if _, err := svc.Approve(ctx, manager, id); err != nil {
		t.Fatal(err)
	}
	check := func(want string, age int) {
		t.Helper()
		view, err := svc.Overview(ctx, branch)
		if err != nil || len(view.Items) != 1 || view.Items[0].Status != want || view.Items[0].AgeDays != age {
			t.Fatalf("status/umur tagihan = %+v, mau %s/%d, err=%v", view.Items, want, age, err)
		}
	}
	initial, err := svc.Overview(ctx, branch)
	if err != nil || len(initial.Items) != 1 || initial.Items[0].Status != "berjalan" {
		t.Fatalf("periode berjalan: %+v %v", initial.Items, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET tanggal=date_trunc('month',(now() AT TIME ZONE 'Asia/Jakarta')::date)::date-1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		offset int
		status string
		age    int
	}{{1, "menunggu_bayar", -1}, {0, "menunggu_bayar", 0}, {-1, "lewat_jatuh_tempo", 1}} {
		if _, err := f.pool.Exec(ctx, `UPDATE tempo_charges SET due_date=(now() AT TIME ZONE 'Asia/Jakarta')::date+$2::int WHERE order_id=$1`, id, tc.offset); err != nil {
			t.Fatal(err)
		}
		check(tc.status, tc.age)
	}
	paid, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{
		Amount: 80000, BankAccountID: f.bankDefault, Filename: "lunas.jpg", Body: []byte{0xff, 0xd8, 0xff, 63},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(ctx, manager, paid.ID, "verify", 80000, ""); err != nil {
		t.Fatal(err)
	}
	check("lunas", 1)
}

func TestPGKeputusanPelunasanTempoBersamaanHanyaSekali(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, BranchID: f.branchA, Nama: "Cabang A"}
	id := tempoSubmitted(t, f, "2026-10-21", 90000)
	if _, err := svc.Approve(ctx, manager, id); err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{
		Amount: 90000, BankAccountID: f.bankDefault, Filename: "bukti.jpg", Body: []byte{0xff, 0xd8, 0xff, 45},
	})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Decide(ctx, manager, receipt.ID, "verify", 90000, "")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, receivable.ErrConflict):
			conflict++
		default:
			t.Fatalf("keputusan paralel memberi galat tak terduga: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("keputusan ganda: berhasil=%d konflik=%d", success, conflict)
	}
	var allocations int
	var applied, paid int64
	var state string
	if err := f.pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(amount),0)::bigint
 FROM tempo_allocations WHERE receipt_id=$1`, receipt.ID).Scan(&allocations, &applied); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT status::text,tempo_paid_amount FROM orders WHERE id=$1`, id).Scan(&state, &paid); err != nil {
		t.Fatal(err)
	}
	if allocations != 1 || applied != 90000 || state != "done" || paid != 90000 {
		t.Fatalf("alokasi ganda: count=%d applied=%d state=%s paid=%d", allocations, applied, state, paid)
	}
	if _, err := svc.Decide(ctx, manager, receipt.ID, "verify", 90000, ""); !errors.Is(err, receivable.ErrConflict) {
		t.Fatalf("keputusan setelah selesai harus konflik: %v", err)
	}
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
	// Bukti tunggal harus mengalir dari tagihan Agustus ke September.
	second := tempoSubmitted(t, f, "2026-09-11", 200000)
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
	partial, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{Amount: 40000, BankAccountID: f.bankDefault, Filename: "bukti.jpg", Body: []byte{0xff, 0xd8, 0xff, 9}})
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != "pending" {
		t.Fatalf("status bukti: %+v", partial)
	}
	if status, body := submit(third); status != 409 || kodeErrorVerifikasi(body) != "TEMPO_ORDER_HELD" {
		t.Fatalf("bukti sebagian tidak boleh melepas hold: %d %+v", status, body)
	}
	var graceCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_hold_grace WHERE branch_id=$1`, f.branchA).Scan(&graceCount); err != nil || graceCount != 0 {
		t.Fatalf("grace tidak boleh dimulai oleh bukti kurang: %d %v", graceCount, err)
	}
	pending, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{Amount: 60000, BankAccountID: f.bankDefault, Filename: "bukti2.jpg", Body: []byte{0xff, 0xd8, 0xff, 10}})
	if err != nil {
		t.Fatal(err)
	}
	var graceStarted time.Time
	if err := f.pool.QueryRow(ctx, `SELECT started_at FROM tempo_hold_grace WHERE branch_id=$1`, f.branchA).Scan(&graceStarted); err != nil || !graceStarted.Equal(pending.SubmittedAt) {
		t.Fatalf("grace harus mulai dari bukti yang menutup tunggakan: %v %v %v", graceStarted, pending.SubmittedAt, err)
	}
	if status, body := submit(third); status != 200 {
		t.Fatalf("bukti cukup harus memberi tenggang: %d %+v", status, body)
	}
	// Unggahan ulang tidak memperpanjang jendela yang sudah berakhir.
	if _, err := f.pool.Exec(ctx, `UPDATE tempo_hold_grace SET ends_on=CURRENT_DATE-1 WHERE branch_id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{Amount: 100000, BankAccountID: f.bankDefault, Filename: "bukti3.jpg", Body: []byte{0xff, 0xd8, 0xff, 11}}); err != nil {
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

func TestPGHoldTempoDihitungUlangSesudahTolakDanBayarSebagian(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5,tahan_bila_telat=true WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, BranchID: f.branchA, Nama: "Cabang A"}
	id := tempoSubmitted(t, f, "2026-10-22", 100000)
	if _, err := svc.Approve(ctx, manager, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE tempo_charges SET due_date=(now() AT TIME ZONE 'Asia/Jakarta')::date-1 WHERE order_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	proof := func(amount int64, tail byte) receivable.Repayment {
		t.Helper()
		r, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{
			Amount: amount, BankAccountID: f.bankDefault, Filename: "bukti.jpg", Body: []byte{0xff, 0xd8, 0xff, tail},
		})
		if err != nil {
			t.Fatalf("unggah bukti: %v", err)
		}
		return r
	}
	checkHold := func(want bool) {
		t.Helper()
		o, err := svc.Overview(ctx, branch)
		if err != nil || o.HoldActive != want {
			t.Fatalf("hold=%v, mau %v, piutang=%+v, err=%v", o.HoldActive, want, o, err)
		}
	}
	checkHold(true)
	first := proof(100000, 51)
	checkHold(false)
	if _, err := svc.Decide(ctx, manager, first.ID, "reject", 0, "rekening tidak cocok"); err != nil {
		t.Fatal(err)
	}
	checkHold(true)
	second := proof(100000, 52)
	checkHold(false)
	if _, err := svc.Decide(ctx, manager, second.ID, "verify", 40000, ""); err != nil {
		t.Fatal(err)
	}
	checkHold(true)
	var remaining int64
	if err := f.pool.QueryRow(ctx, `SELECT sum(amount-paid_amount)::bigint FROM tempo_charges WHERE order_id=$1`, id).Scan(&remaining); err != nil || remaining != 60000 {
		t.Fatalf("setelah verifikasi kurang, sisa harus 60000: %d %v", remaining, err)
	}
	third := proof(60000, 53)
	checkHold(false)
	if _, err := svc.Decide(ctx, manager, third.ID, "verify", 60000, ""); err != nil {
		t.Fatal(err)
	}
	checkHold(false)
	var graceCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_hold_grace WHERE branch_id=$1`, f.branchA).Scan(&graceCount); err != nil || graceCount != 0 {
		t.Fatalf("grace harus terhapus setelah lunas: %d %v", graceCount, err)
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

func TestPGTempoDailyMenerbitkanSuratTanggalSatuTepatSekali(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	id := tempoSubmitted(t, f, "2099-10-20", 110000)
	if _, err := f.pool.Exec(ctx, `UPDATE admin_users SET notif_invoice=(id=$1) WHERE id IN ($1,$2)`, f.adminSA, f.adminMgr); err != nil {
		t.Fatal(err)
	}
	if due, err := svc.Approve(ctx, manager, id); err != nil || due != "2099-11-05" {
		t.Fatalf("approve Oktober: due=%s err=%v", due, err)
	}
	before := time.Date(2099, 10, 31, 12, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	if err := svc.Daily(ctx, f.objek, rendererUji{}, before); err != nil {
		t.Fatalf("job sebelum tanggal 1: %v", err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_statements WHERE branch_id=$1`, f.branchA).Scan(&count); err != nil || count != 0 {
		t.Fatalf("Surat Tagihan belum boleh terbit: count=%d err=%v", count, err)
	}
	onFirst := time.Date(2099, 11, 1, 9, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	for i := 0; i < 2; i++ {
		if err := svc.Daily(ctx, f.objek, rendererUji{}, onFirst); err != nil {
			t.Fatalf("job tanggal 1 ke-%d: %v", i+1, err)
		}
	}
	var version int
	var total int64
	var key string
	if err := f.pool.QueryRow(ctx, `SELECT version,total_amount,storage_key FROM tempo_statements
 WHERE branch_id=$1 AND period_month='2099-10-01'`, f.branchA).Scan(&version, &total, &key); err != nil {
		t.Fatalf("Surat Tagihan Oktober: %v", err)
	}
	if version != 1 || total != 110000 || key == "" {
		t.Fatalf("versi/total/PDF tagihan salah: %d %d %q", version, total, key)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_statements WHERE branch_id=$1`, f.branchA).Scan(&count); err != nil || count != 1 {
		t.Fatalf("pengulangan job menggandakan surat: %d %v", count, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE jenis='tempo_statement'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("tagihan perlu satu email cabang dan satu admin, dapat %d", count)
	}
}

func TestPGPengingatHMinus3TanggalAwalBulanTetapTerkirim(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=1 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	id := tempoSubmitted(t, f, "2099-10-15", 100000)
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET tanggal_bayar_snapshot=1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	now := time.Date(2099, 10, 29, 9, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	if err := svc.SendReminders(ctx, now); err != nil {
		t.Fatalf("pengingat H-3: %v", err)
	}
	if err := svc.SendReminders(ctx, now); err != nil {
		t.Fatalf("ulang pengingat H-3: %v", err)
	}
	var count int
	var generic bool
	if err := f.pool.QueryRow(ctx, `SELECT count(*),COALESCE(bool_and(generic),false)
 FROM tempo_reminders WHERE branch_id=$1 AND due_date='2099-11-01' AND stage='h_minus_3'`, f.branchA).Scan(&count, &generic); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !generic {
		t.Fatalf("pengingat H-3 umum harus sekali: count=%d generic=%v", count, generic)
	}
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	if due, err := svc.Approve(ctx, manager, id); err != nil || due != "2099-11-01" {
		t.Fatalf("approve sebelum jatuh tempo: %s %v", due, err)
	}
	for _, tc := range []struct {
		day, stage string
		amount     int64
	}{
		{"2099-11-01", "due", 100000},
		{"2099-11-02", "h_plus_1", 60000},
		{"2099-11-08", "h_plus_7", 60000},
	} {
		if tc.stage == "h_plus_1" {
			branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, BranchID: f.branchA, Nama: "Cabang A"}
			receipt, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{
				Amount: 40000, BankAccountID: f.bankDefault, Filename: "bukti.jpg", Body: []byte{0xff, 0xd8, 0xff, 44},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Decide(ctx, manager, receipt.ID, "verify", 40000, ""); err != nil {
				t.Fatal(err)
			}
		}
		day, err := time.ParseInLocation("2006-01-02", tc.day, now.Location())
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := svc.SendReminders(ctx, day); err != nil {
				t.Fatalf("pengingat %s: %v", tc.stage, err)
			}
		}
		var gotAmount int64
		if err := f.pool.QueryRow(ctx, `SELECT count(*),COALESCE(max(amount),0)::bigint
 FROM tempo_reminders WHERE branch_id=$1 AND due_date='2099-11-01' AND stage=$2`,
			f.branchA, tc.stage).Scan(&count, &gotAmount); err != nil {
			t.Fatal(err)
		}
		if count != 1 || gotAmount != tc.amount {
			t.Fatalf("%s harus sekali dengan sisa %d: count=%d amount=%d", tc.stage, tc.amount, count, gotAmount)
		}
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE jenis='tempo_reminder'
 AND payload->>'branch_id'=$1`, f.branchA).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("empat tahap harus menghasilkan tepat empat email, dapat %d", count)
	}
}

func TestPGPengingatTempoTanggalBayarDuaTigaDanLima(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	for i, payDay := range []int{2, 3, 5} {
		id := tempoSubmitted(t, f, time.Date(2099, 10, 11+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), 100000)
		if _, err := f.pool.Exec(ctx, `UPDATE orders SET tanggal_bayar_snapshot=$2 WHERE id=$1`, id, payDay); err != nil {
			t.Fatal(err)
		}
		if due, err := svc.Approve(ctx, manager, id); err != nil || due != time.Date(2099, 11, payDay, 0, 0, 0, 0, time.UTC).Format("2006-01-02") {
			t.Fatalf("approve tanggal bayar %d: %s %v", payDay, due, err)
		}
	}
	for _, day := range []int{30, 31} {
		date := time.Date(2099, 10, day, 9, 0, 0, 0, time.FixedZone("WIB", 7*3600))
		if err := svc.SendReminders(ctx, date); err != nil {
			t.Fatal(err)
		}
	}
	for _, day := range []int{2, 3, 4, 5, 6, 9, 10, 12} {
		date := time.Date(2099, 11, day, 9, 0, 0, 0, time.FixedZone("WIB", 7*3600))
		for i := 0; i < 2; i++ {
			if err := svc.SendReminders(ctx, date); err != nil {
				t.Fatal(err)
			}
		}
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_reminders WHERE branch_id=$1`, f.branchA).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 12 {
		t.Fatalf("tiga jatuh tempo × empat tahap harus 12 pengingat, dapat %d", count)
	}
	for _, dueDay := range []int{2, 3, 5} {
		for _, stage := range []string{"h_minus_3", "due", "h_plus_1", "h_plus_7"} {
			var generic bool
			var amount int64
			if err := f.pool.QueryRow(ctx, `SELECT generic,amount FROM tempo_reminders
 WHERE branch_id=$1 AND due_date=$2::date AND stage=$3`, f.branchA,
				time.Date(2099, 11, dueDay, 0, 0, 0, 0, time.UTC), stage).Scan(&generic, &amount); err != nil {
				t.Fatalf("pengingat due %d stage %s tidak ada: %v", dueDay, stage, err)
			}
			if generic != (stage == "h_minus_3" && dueDay <= 3) || amount != 100000 {
				t.Fatalf("pengingat due %d stage %s: generic=%v amount=%d", dueDay, stage, generic, amount)
			}
		}
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE jenis='tempo_reminder'
 AND payload->>'branch_id'=$1`, f.branchA).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 12 {
		t.Fatalf("setiap pengingat harus tepat satu email, dapat %d", count)
	}
}
