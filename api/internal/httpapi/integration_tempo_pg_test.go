package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
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
	"github.com/grahita/portal/api/internal/document"
	"github.com/grahita/portal/api/internal/fulfillment"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/receivable"
	"github.com/grahita/portal/api/internal/report"
	reportpg "github.com/grahita/portal/api/internal/report/postgres"
	"github.com/xuri/excelize/v2"
)

// Bukti Transfer pesanan dan endpoint verifikasi biasa tidak boleh dipakai
// untuk menyelesaikan pesanan Tempo. Pelunasannya mempunyai receipt/ledger
// tersendiri di menu Tagihan; kesalahan harus 409, bukan error constraint 500.
func TestPGTempoMenolakBuktiPesananDanVerifikasiReguler(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	id := tempoSubmitted(t, f, "2026-10-20", 100000)
	branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: f.branchA, Nama: "Cabang A"}
	objectsBefore := f.objek.jumlah()
	branchCookies, branchCSRF := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	respHTTP, uploadBody := multipartUnggah(t, f.srv.URL+"/api/v1/orders/"+id+"/proofs",
		branchCookies, branchCSRF, map[string][]byte{"tempo.jpg": berkasJPEGUji("tempo-http")})
	if respHTTP.StatusCode != http.StatusConflict || kodeErrorVerifikasi(uploadBody) != "TEMPO_USE_TAGIHAN" {
		t.Fatalf("endpoint unggah pesanan Tempo harus 409: %d %+v", respHTTP.StatusCode, uploadBody)
	}
	otherCookies, otherCSRF := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	respHTTP, uploadBody = multipartUnggah(t, f.srv.URL+"/api/v1/orders/"+id+"/proofs",
		otherCookies, otherCSRF, map[string][]byte{"foreign.jpg": berkasJPEGUji("foreign-tempo")})
	if respHTTP.StatusCode != http.StatusNotFound {
		t.Fatalf("cabang lain tidak boleh mengetahui pesanan Tempo: %d %+v", respHTTP.StatusCode, uploadBody)
	}
	_, err := f.payments.Upload(ctx, branch, id, []payment.FileInput{{
		NamaFile: "bukti.jpg", Content: bytes.NewReader(berkasJPEGUji("tempo-order-proof")),
	}})
	if !errors.Is(err, payment.ErrTempoOrder) || f.objek.jumlah() != objectsBefore {
		t.Fatalf("unggah bukti ke pesanan Tempo harus ditolak sebelum storage: %v", err)
	}

	// Simulasi bukti yang telanjur dibuat oleh versi lama: verifikasi langsung
	// tetap harus ditolak dan tidak menulis log maupun mengubah status pesanan.
	var proofID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO payment_proofs
(order_id,branch_id,storage_key,nama_file,mime_type,ukuran_byte,sha256,uploaded_by_kind)
VALUES($1,$2,'qa/legacy-tempo.jpg','legacy.jpg','image/jpeg',4,'\x010203'::bytea,'branch') RETURNING id::text`,
		id, f.branchA).Scan(&proofID); err != nil {
		t.Fatal(err)
	}
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	respHTTP, uploadBody = multipartUnggah(t, f.srv.URL+"/api/v1/admin/orders/"+id+"/proofs",
		cookies, csrf, map[string][]byte{"tempo-admin.jpg": berkasJPEGUji("tempo-http-admin")})
	if respHTTP.StatusCode != http.StatusConflict || kodeErrorVerifikasi(uploadBody) != "TEMPO_USE_TAGIHAN" {
		t.Fatalf("endpoint unggah admin untuk Tempo harus 409: %d %+v", respHTTP.StatusCode, uploadBody)
	}
	request := fmt.Sprintf(`{"hasil":"cocok","total_terbaca":100000,"proof_destinations":[{"payment_proof_id":%q,"bank_account_id":%q}]}`,
		proofID, f.bankDefault)
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+id+"/verify", request, cookies, csrf)
	if status != http.StatusConflict || kodeErrorVerifikasi(body) != "TEMPO_USE_TAGIHAN" {
		t.Fatalf("verifikasi biasa atas Tempo harus 409 TEMPO_USE_TAGIHAN: %d %+v", status, body)
	}
	var orderStatus string
	var logCount int
	if err := f.pool.QueryRow(ctx, `SELECT status::text FROM orders WHERE id=$1`, id).Scan(&orderStatus); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM verification_logs WHERE order_id=$1`, id).Scan(&logCount); err != nil {
		t.Fatal(err)
	}
	if orderStatus != "submitted" || logCount != 0 {
		t.Fatalf("pesanan Tempo berubah karena verifikasi biasa: status=%s log=%d", orderStatus, logCount)
	}

	draftID := buatDraftHTTP(t, f, branchCookies, branchCSRF, produkKode(t, f, "P-01"), 1)["id"].(string)
	if _, err := f.payments.Upload(ctx, branch, draftID, []payment.FileInput{{
		NamaFile: "draft.jpg", Content: bytes.NewReader(berkasJPEGUji("draft-transfer")),
	}}); err != nil {
		t.Fatalf("siapkan bukti draft sebelum memilih mode: %v", err)
	}
	resp, submitBody := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+draftID+"/submit",
		branchCookies, branchCSRF, nil, "", "tempo")
	if resp.StatusCode != http.StatusConflict || kodeErrorVerifikasi(submitBody) != "TEMPO_PROOF_CONFLICT" {
		t.Fatalf("draft yang sudah punya bukti tidak boleh dikirim sebagai Tempo: %d %+v", resp.StatusCode, submitBody)
	}
	if err := f.pool.QueryRow(ctx, `SELECT status::text FROM orders WHERE id=$1`, draftID).Scan(&orderStatus); err != nil || orderStatus != "draft" {
		t.Fatalf("submit gagal harus tetap draft: status=%s err=%v", orderStatus, err)
	}
}

// TMP-27: Sertifikat Tempo dengan packaging Rp0 memang bernilai Rp0. Saat
// disetujui, status langsung lunas tanpa charge atau saldo negatif, tetapi
// dokumen tetap menjadi hak pesanan tersebut.
func TestPGTempoSertifikatNolLangsungLunasDanDokumenTerbit(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	setSetting(t, f, "biaya_packaging", "0")
	var product string
	if err := f.pool.QueryRow(ctx, `INSERT INTO products(kode,nama,jenis,harga_default,urutan)
VALUES('P-SER-TEMPO-0','Sertifikat Tempo Nol','sertifikat',0,99) RETURNING id::text`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO branch_prices(branch_id,product_id,harga) VALUES($1,$2,0)`, f.branchA, product); err != nil {
		t.Fatal(err)
	}
	branchCookies, branchCSRF := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	draft := buatDraftHTTP(t, f, branchCookies, branchCSRF, product, 1)
	id := draft["id"].(string)
	if draft["total"] != float64(0) {
		t.Fatalf("Sertifikat + packaging Rp0 harus total Rp0: %+v", draft)
	}
	resp, submitted := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", branchCookies, branchCSRF, nil, "", "tempo")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("kirim Sertifikat Tempo Rp0: %d %+v", resp.StatusCode, submitted)
	}
	adminCookies, adminCSRF := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	statusHTTP, approved, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+id+"/approve-tempo", `{}`, adminCookies, adminCSRF)
	if statusHTTP != http.StatusOK {
		t.Fatalf("approve Sertifikat Tempo Rp0: %d %+v", statusHTTP, approved)
	}
	var status string
	var chargeCount int
	var openAmount int64
	if err := f.pool.QueryRow(ctx, `SELECT status::text FROM orders WHERE id=$1`, id).Scan(&status); err != nil || status != "done" {
		t.Fatalf("pesanan Rp0 langsung done: %s %v", status, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(amount-paid_amount),0)::bigint FROM tempo_charges WHERE order_id=$1`, id).
		Scan(&chargeCount, &openAmount); err != nil || openAmount != 0 {
		t.Fatalf("pesanan Rp0 tidak boleh menyisakan piutang: charges=%d sisa=%d err=%v", chargeCount, openAmount, err)
	}
	var auditedStatus string
	if err := f.pool.QueryRow(ctx, `SELECT after->>'status' FROM audit_logs WHERE entity='order' AND entity_id=$1
ORDER BY at DESC LIMIT 1`, id).Scan(&auditedStatus); err != nil || auditedStatus != "done" {
		t.Fatalf("audit approval Rp0 harus mencatat status done: %q %v", auditedStatus, err)
	}
	if _, err := f.selesai.PesananSelesai(ctx, id); err != nil {
		t.Fatalf("dokumen pesanan Rp0 harus tetap terbit: %v", err)
	}
	var docs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM documents WHERE order_id=$1`, id).Scan(&docs); err != nil || docs != 3 {
		t.Fatalf("pesanan Rp0 harus punya invoice dan dua format surat jalan: %d %v", docs, err)
	}
}

// MAS-10 / ORD-19: produk yang dinonaktifkan setelah dipakai tidak mengubah
// snapshot pada pesanan, dokumen, dan laporan. Baris lama boleh tanpa instansi.
func TestPGPesananHistorisTetapTerbacaSesudahProdukNonaktif(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	id := tempoSubmitted(t, f, "2026-10-20", 100000)
	var productID string
	if err := f.pool.QueryRow(ctx, `SELECT product_id::text FROM order_lines WHERE order_id=$1`, id).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	if _, err := svc.Approve(ctx, manager, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE products SET is_active=false,nama='Produk Diubah Setelah Order' WHERE id=$1`, productID); err != nil {
		t.Fatal(err)
	}
	branchCookies, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/orders/"+id, "", branchCookies, "")
	if status != http.StatusOK {
		t.Fatalf("pesanan historis tetap dapat dibuka: %d %+v", status, body)
	}
	lines := dataMap(t, body)["lines"].([]any)
	line := lines[0].(map[string]any)
	if line["nama"] != "Bio Magnetik" || line["nama_instansi"] != "" {
		t.Fatalf("snapshot order berubah atau instansi lama diisi otomatis: %+v", line)
	}
	if _, err := f.selesai.PesananSelesai(ctx, id); err != nil {
		t.Fatalf("dokumen historis setelah produk nonaktif: %v", err)
	}
	var key string
	if err := f.pool.QueryRow(ctx, `SELECT storage_key FROM documents WHERE order_id=$1 AND jenis='invoice_pdf'`, id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	file, err := f.objek.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	pdfBody, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || !bytes.Contains(pdfBody, []byte("Bio Magnetik")) || !bytes.Contains(pdfBody, []byte("—")) ||
		bytes.Contains(pdfBody, []byte("Produk Diubah Setelah Order")) {
		t.Fatalf("invoice historis kehilangan snapshot atau placeholder instansi: err=%v", err)
	}
	var roadPDFKey, roadXLSXKey string
	if err := f.pool.QueryRow(ctx, `SELECT storage_key FROM documents WHERE order_id=$1 AND jenis='surat_jalan_pdf'`, id).Scan(&roadPDFKey); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT storage_key FROM documents WHERE order_id=$1 AND jenis='surat_jalan_xlsx'`, id).Scan(&roadXLSXKey); err != nil {
		t.Fatal(err)
	}
	roadPDF, err := f.objek.Get(ctx, roadPDFKey)
	if err != nil {
		t.Fatal(err)
	}
	roadPDFBytes, err := io.ReadAll(roadPDF)
	_ = roadPDF.Close()
	if err != nil || !bytes.Contains(roadPDFBytes, []byte("Bio Magnetik")) || !bytes.Contains(roadPDFBytes, []byte("—")) {
		t.Fatalf("Surat Jalan PDF historis kehilangan snapshot/placeholder: %v", err)
	}
	roadXLSX, err := f.objek.Get(ctx, roadXLSXKey)
	if err != nil {
		t.Fatal(err)
	}
	roadXLSXBytes, err := io.ReadAll(roadXLSX)
	_ = roadXLSX.Close()
	if err != nil {
		t.Fatal(err)
	}
	roadBook, err := excelize.OpenReader(bytes.NewReader(roadXLSXBytes))
	if err != nil {
		t.Fatal(err)
	}
	if instansi, err := roadBook.GetCellValue(document.SheetSuratJalan, "C9"); err != nil || instansi != "—" {
		t.Fatalf("Surat Jalan XLSX historis harus menampilkan dash: %q %v", instansi, err)
	}
	_ = roadBook.Close()
	reportSvc := report.NewService(reportpg.New(f.pool, reportpg.WithJobQueue(f.jobs)), f.objek)
	actor := auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
	page, err := reportSvc.List(ctx, actor, report.Filter{})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Produk != "Bio Magnetik" || page.Items[0].NamaInstansi != "" {
		t.Fatalf("laporan historis harus memakai snapshot produk dan instansi kosong: %+v %v", page, err)
	}
	reportJob, err := reportSvc.Request(ctx, actor, report.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reportSvc.Jalankan(ctx, reportJob.ID); err != nil {
		t.Fatalf("ekspor laporan historis: %v", err)
	}
	reportFile, err := f.objek.Get(ctx, report.StorageKey(report.JenisOrderLines, reportJob.ID))
	if err != nil {
		t.Fatal(err)
	}
	reportBytes, err := io.ReadAll(reportFile)
	_ = reportFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	reportBook, err := excelize.OpenReader(bytes.NewReader(reportBytes))
	if err != nil {
		t.Fatal(err)
	}
	if instansi, err := reportBook.GetCellValue(report.SheetLaporan, "I2"); err != nil || instansi != "—" {
		t.Fatalf("ekspor laporan historis harus menampilkan dash: %q %v", instansi, err)
	}
	_ = reportBook.Close()
}

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

// TMP-03: seluruh guard persetujuan Tempo diuji dalam satu fixture. Setiap
// penolakan harus terjadi sebelum charge maupun pekerjaan dokumen terbentuk;
// dua persetujuan serentak hanya boleh menghasilkan satu transisi ekonomi.
func TestPGApproveTempoGuardStatusRekeningDanKlikGanda(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM branch_bank_accounts WHERE branch_id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	managerCookies, managerCSRF := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	approveURL := func(id string) string { return f.srv.URL + "/api/v1/admin/orders/" + id + "/approve-tempo" }
	assertKosong := func(id string) {
		t.Helper()
		var charges, jobsCount int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_charges WHERE order_id=$1`, id).Scan(&charges); err != nil {
			t.Fatal(err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='render_dokumen_pesanan' AND args->>'order_id'=$1`, id).Scan(&jobsCount); err != nil {
			t.Fatal(err)
		}
		if charges != 0 || jobsCount != 0 {
			t.Fatalf("penolakan tidak boleh membuat charge/job: charge=%d job=%d", charges, jobsCount)
		}
	}

	// Pesanan Tempo yang sah tetapi belum mempunyai rekening efektif.
	tanpaRekening := tempoSubmitted(t, f, "2026-10-22", 110000)
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, approveURL(tanpaRekening), `{}`, managerCookies, managerCSRF)
	if status != http.StatusConflict || kodeErrorVerifikasi(body) != "BRANCH_BANK_ACCOUNT_UNAVAILABLE" {
		t.Fatalf("Tempo tanpa rekening harus 409 BRANCH_BANK_ACCOUNT_UNAVAILABLE: %d %+v", status, body)
	}
	assertKosong(tanpaRekening)

	// Order non-Tempo yang sudah submitted dan draft yang belum memilih mode
	// pembayaran tidak boleh dikenali sebagai order Tempo.
	nonTempo := insertOrder(t, f, f.branchA, "submitted", "2026-10-NONTEMPO")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, approveURL(nonTempo), `{}`, managerCookies, managerCSRF)
	if status != http.StatusNotFound {
		t.Fatalf("order non-Tempo harus 404, dapat %d (%v)", status, body)
	}
	assertKosong(nonTempo)
	branchCookies, branchCSRF := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	draft := buatDraftHTTP(t, f, branchCookies, branchCSRF, produkKode(t, f, "P-01"), 1)["id"].(string)
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, approveURL(draft), `{}`, managerCookies, managerCSRF)
	if status != http.StatusNotFound {
		t.Fatalf("draft belum memilih Tempo harus 404, dapat %d (%v)", status, body)
	}
	assertKosong(draft)

	// Setelah rekening efektif dipasang, persetujuan pertama berhasil dan
	// pengulangan atas order yang sudah approved konflik tanpa duplikasi.
	if _, err := f.pool.Exec(ctx, `INSERT INTO branch_bank_accounts(branch_id,bank_account_id,is_allowed,assigned_by)
		VALUES($1,$2,true,$3)`, f.branchA, f.bankDefault, f.adminSA); err != nil {
		t.Fatal(err)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, approveURL(tanpaRekening), `{}`, managerCookies, managerCSRF)
	if status != http.StatusOK {
		t.Fatalf("approve Tempo sah harus 200, dapat %d (%v)", status, body)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, approveURL(tanpaRekening), `{}`, managerCookies, managerCSRF)
	if status != http.StatusConflict || kodeErrorVerifikasi(body) != "TEMPO_CONFLICT" {
		t.Fatalf("approve ulang harus 409 TEMPO_CONFLICT, dapat %d (%v)", status, body)
	}

	// Klik ganda serentak pada order lain: transaksi dan lock cabang menjamin
	// tepat satu sukses dan satu konflik.
	dobel := tempoSubmitted(t, f, "2026-10-23", 120000)
	svc := receivable.New(f.pool, f.jobs)
	actor := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	mulai := make(chan struct{})
	hasil := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-mulai
			_, err := svc.Approve(ctx, actor, dobel)
			hasil <- err
		}()
	}
	close(mulai)
	wg.Wait()
	close(hasil)
	var sukses, konflik int
	for err := range hasil {
		switch {
		case err == nil:
			sukses++
		case errors.Is(err, receivable.ErrConflict):
			konflik++
		default:
			t.Fatalf("hasil klik ganda tidak terduga: %v", err)
		}
	}
	if sukses != 1 || konflik != 1 {
		t.Fatalf("klik ganda harus satu sukses/satu konflik: sukses=%d konflik=%d", sukses, konflik)
	}
	for _, id := range []string{tanpaRekening, dobel} {
		var charges, jobsCount int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_charges WHERE order_id=$1`, id).Scan(&charges); err != nil {
			t.Fatal(err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='render_dokumen_pesanan' AND args->>'order_id'=$1`, id).Scan(&jobsCount); err != nil {
			t.Fatal(err)
		}
		if charges != 1 || jobsCount != 1 {
			t.Fatalf("order %s harus tepat satu charge/job: charge=%d job=%d", id, charges, jobsCount)
		}
	}
}

// TMP-11: endpoint pelunasan Tempo menolak seluruh batas nominal dan berkas
// sebelum membentuk receipt aktif. Bukti sah tersimpan sekali; byte identik
// tidak dapat dipakai ulang dan objek kedua dibersihkan.
func TestPGPelunasanTempoValidasiNominalBerkasDanDuplikat(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	cookies, csrf := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	kirim := func(amount, bankID, filename string, isi []byte) (int, map[string]any) {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("amount", amount)
		_ = mw.WriteField("bank_account_id", bankID)
		if isi != nil {
			fw, err := mw.CreateFormFile("file", filename)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fw.Write(isi)
		}
		_ = mw.Close()
		req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/api/v1/tempo/repayments", &buf)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		return kirimDeposit(t, req, cookies, csrf, "")
	}
	validJPEG := berkasJPEGUji("pelunasan-tempo-valid")
	objekAwal := f.objek.jumlah()
	invalid := []struct {
		nama, amount, bankID, filename string
		isi                            []byte
	}{
		{"nominal nol", "0", f.bankDefault, "nol.jpg", validJPEG},
		{"nominal negatif", "-1", f.bankDefault, "negatif.jpg", validJPEG},
		{"nominal desimal", "12.5", f.bankDefault, "desimal.jpg", validJPEG},
		{"nominal di atas satu triliun", "1000000000001", f.bankDefault, "besar.jpg", validJPEG},
		{"tanpa rekening", "100000", "", "rekening.jpg", validJPEG},
		{"tanpa berkas", "100000", f.bankDefault, "", nil},
		{"MIME tidak didukung", "100000", f.bankDefault, "palsu.jpg", []byte("bukan gambar atau PDF")},
		{"lebih dari 10 MB", "100000", f.bankDefault, "terlalu-besar.jpg", append(berkasJPEGUji("besar"), bytes.Repeat([]byte{'x'}, 10*1024*1024)...)},
	}
	for _, tc := range invalid {
		status, body := kirim(tc.amount, tc.bankID, tc.filename, tc.isi)
		if status < 400 || status >= 500 {
			t.Fatalf("%s harus ditolak 4xx, dapat %d (%v)", tc.nama, status, body)
		}
	}
	var receipts, proofs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM payment_proofs WHERE purpose='tempo_repayment'`).Scan(&proofs); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || proofs != 0 || f.objek.jumlah() != objekAwal {
		t.Fatalf("input tidak sah meninggalkan data: receipt=%d proof=%d objek=%d awal=%d",
			receipts, proofs, f.objek.jumlah(), objekAwal)
	}

	status, body := kirim("100000", f.bankDefault, "valid.jpg", validJPEG)
	if status != http.StatusCreated || dataMap(t, body)["status"] != "pending" {
		t.Fatalf("bukti sah harus 201 pending, dapat %d (%v)", status, body)
	}
	status, body = kirim("100000", f.bankDefault, "duplikat.jpg", validJPEG)
	if status != http.StatusConflict || kodeErrorVerifikasi(body) != "TEMPO_PROOF_DUPLICATE" {
		t.Fatalf("bukti identik harus 409 TEMPO_PROOF_DUPLICATE, dapat %d (%v)", status, body)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM payment_proofs WHERE purpose='tempo_repayment'`).Scan(&proofs); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || proofs != 1 || f.objek.jumlah() != objekAwal+1 {
		t.Fatalf("duplikat harus menyisakan tepat satu bukti aktif: receipt=%d proof=%d objek=%d", receipts, proofs, f.objek.jumlah())
	}
}

func TestPGApproveTempoSebelumPadaDanSesudahJatuhTempo(t *testing.T) {
	wib := time.FixedZone("WIB", 7*60*60)
	for _, tc := range []struct {
		name, approval, wantDue string
	}{
		{name: "sebelum", approval: "2026-11-04T10:00:00+07:00", wantDue: "2026-11-05"},
		{name: "pada_hari_h", approval: "2026-11-05T23:59:59+07:00", wantDue: "2026-11-05"},
		{name: "sesudah", approval: "2026-11-06T00:00:00+07:00", wantDue: "2026-12-05"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			approval, err := time.Parse(time.RFC3339, tc.approval)
			if err != nil {
				t.Fatal(err)
			}
			clk := clock.Fixed(approval.In(wib))
			f := newPGFixture(t, pgFixtureClock{receivables: clk})
			if _, err := f.pool.Exec(t.Context(),
				`UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
				t.Fatal(err)
			}
			id := tempoSubmitted(t, f, "2026-10-15", 100000)
			cookies, csrf := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
			status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
				f.srv.URL+"/api/v1/admin/orders/"+id+"/approve-tempo", `{}`, cookies, csrf)
			if status != http.StatusOK || dataMap(t, body)["tempo_due_date"] != tc.wantDue {
				t.Fatalf("approve %s: HTTP %d, body=%v, mau due %s", tc.name, status, body, tc.wantDue)
			}
			var orderDue, chargeDue, orderStatus string
			var chargeCount int
			if err := f.pool.QueryRow(t.Context(), `SELECT tempo_due_date::text,status::text FROM orders WHERE id=$1`, id).
				Scan(&orderDue, &orderStatus); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(t.Context(), `SELECT due_date::text,count(*) OVER () FROM tempo_charges WHERE order_id=$1`, id).
				Scan(&chargeDue, &chargeCount); err != nil {
				t.Fatal(err)
			}
			if orderDue != tc.wantDue || chargeDue != tc.wantDue || orderStatus != "approved_unpaid" || chargeCount != 1 {
				t.Fatalf("approve %s: order due=%s, charge due=%s, status=%s, charges=%d", tc.name, orderDue, chargeDue, orderStatus, chargeCount)
			}
		})
	}
}

func TestPGTempoTanggalBayarSnapshotSaatMasterBerubah(t *testing.T) {
	clk := clock.Fixed(time.Date(2026, time.October, 20, 10, 0, 0, 0, time.FixedZone("WIB", 7*60*60)))
	f := newPGFixture(t, pgFixtureClock{orders: clk, receivables: clk})
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx,
		`UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	branchCookies, branchCSRF := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	adminCookies, adminCSRF := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	product := produkKode(t, f, "P-01")
	submit := func() string {
		t.Helper()
		id := buatDraftHTTP(t, f, branchCookies, branchCSRF, product, 1)["id"].(string)
		resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit",
			branchCookies, branchCSRF, nil, "", "tempo")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("submit Tempo: %d %+v", resp.StatusCode, body)
		}
		return id
	}
	first := submit()
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/branches/"+f.branchA,
		`{"tanggal_bayar":10}`, adminCookies, adminCSRF)
	if status != http.StatusOK {
		t.Fatalf("ubah tanggal bayar melalui API Admin: %d %+v", status, body)
	}
	second := submit()
	for _, tc := range []struct {
		id      string
		payDay  int16
		wantDue string
	}{
		{id: first, payDay: 5, wantDue: "2026-11-05"},
		{id: second, payDay: 10, wantDue: "2026-11-10"},
	} {
		var snapshot int16
		if err := f.pool.QueryRow(ctx, `SELECT tanggal_bayar_snapshot FROM orders WHERE id=$1`, tc.id).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot != tc.payDay {
			t.Fatalf("snapshot pesanan %s: %d, mau %d", tc.id, snapshot, tc.payDay)
		}
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
			f.srv.URL+"/api/v1/admin/orders/"+tc.id+"/approve-tempo", `{}`, adminCookies, adminCSRF)
		if status != http.StatusOK || dataMap(t, body)["tempo_due_date"] != tc.wantDue {
			t.Fatalf("approve pesanan %s: HTTP %d body=%+v, mau due %s", tc.id, status, body, tc.wantDue)
		}
		var orderDue, chargeDue string
		if err := f.pool.QueryRow(ctx, `SELECT tempo_due_date::text FROM orders WHERE id=$1`, tc.id).Scan(&orderDue); err != nil {
			t.Fatal(err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT due_date::text FROM tempo_charges WHERE order_id=$1`, tc.id).Scan(&chargeDue); err != nil {
			t.Fatal(err)
		}
		if orderDue != tc.wantDue || chargeDue != tc.wantDue {
			t.Fatalf("due pesanan %s berubah setelah master diubah: order=%s charge=%s", tc.id, orderDue, chargeDue)
		}
	}
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
	managerCookies, _ := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	for _, tc := range []struct {
		role    string
		cookies []*http.Cookie
	}{
		{role: "manager", cookies: managerCookies},
		{role: "branch", cookies: branchCookies},
	} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
			f.srv.URL+"/api/v1/admin/tempo/export", "", tc.cookies, "")
		if status != http.StatusForbidden {
			t.Fatalf("ekspor piutang oleh %s harus 403, dapat %d (%v)", tc.role, status, body)
		}
	}
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
	dimensi, err := book.GetSheetDimension("Piutang Tempo")
	if err != nil || dimensi != fmt.Sprintf("A1:H%d", len(rows)) {
		t.Fatalf("rentang XLSX wajib memuat semua baris untuk pembaca streaming: %q, %v", dimensi, err)
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

// TMP-17: bukti yang menutup tunggakan pada Jumat memberi grace pada Senin
// dan Selasa saja. Unggahan kedua tidak boleh menggeser akhir grace.
func TestPGTempoGraceJumatDuaHariKerjaTanpaPerpanjangan(t *testing.T) {
	friday := time.Date(2026, time.October, 23, 3, 0, 0, 0, time.UTC)
	if friday.In(clock.Jakarta()).Weekday() != time.Friday {
		t.Fatal("fixture harus dimulai pada Jumat WIB")
	}
	clk := clock.Fixed(friday)
	f := newPGFixture(t, pgFixtureClock{orders: clk, receivables: clk})
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5,tahan_bila_telat=true WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	id := tempoSubmitted(t, f, "2026-09-20", 100000)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	svc := receivable.New(f.pool, f.jobs, receivable.WithClock(clk))
	clk.Set(time.Date(2026, time.October, 1, 3, 0, 0, 0, time.UTC))
	if _, err := svc.Approve(ctx, manager, id); err != nil {
		t.Fatal(err)
	}
	clk.Set(friday)
	var due string
	if err := f.pool.QueryRow(ctx, `SELECT due_date::text FROM tempo_charges WHERE order_id=$1`, id).Scan(&due); err != nil || due != "2026-10-05" {
		t.Fatalf("utang harus sudah lewat jatuh tempo pada Jumat: due=%s err=%v", due, err)
	}
	branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, BranchID: f.branchA, Nama: "Cabang A"}
	first, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{
		Amount: 100000, BankAccountID: f.bankDefault, Filename: "jumat.jpg", Body: []byte{0xff, 0xd8, 0xff, 91},
	})
	if err != nil {
		t.Fatal(err)
	}
	var started time.Time
	var ends string
	if err := f.pool.QueryRow(ctx, `SELECT started_at,ends_on::text FROM tempo_hold_grace WHERE branch_id=$1`, f.branchA).Scan(&started, &ends); err != nil {
		t.Fatal(err)
	}
	if !first.SubmittedAt.Equal(friday) || !started.Equal(friday) || ends != "2026-10-27" {
		t.Fatalf("grace Jumat harus berakhir Selasa: receipt=%v started=%v ends=%s", first.SubmittedAt, started, ends)
	}
	cookies, csrf := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	product := produkKode(t, f, "P-01")
	submitAt := func(day int, wantStatus int) {
		t.Helper()
		clk.Set(time.Date(2026, time.October, day, 3, 0, 0, 0, time.UTC))
		draft := buatDraftHTTP(t, f, cookies, csrf, product, 1)["id"].(string)
		resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+draft+"/submit", cookies, csrf, nil, "", "tempo")
		if resp.StatusCode != wantStatus {
			t.Fatalf("submit %d Oktober: dapat %d %+v, mau %d", day, resp.StatusCode, body, wantStatus)
		}
		if wantStatus == http.StatusConflict && kodeErrorVerifikasi(body) != "TEMPO_ORDER_HELD" {
			t.Fatalf("akhir grace harus mengembalikan hold: %+v", body)
		}
	}
	submitAt(26, http.StatusOK) // Senin, hari kerja pertama.
	_, err = svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{
		Amount: 100000, BankAccountID: f.bankDefault, Filename: "senin.jpg", Body: []byte{0xff, 0xd8, 0xff, 92},
	})
	if err != nil {
		t.Fatal(err)
	}
	var after time.Time
	var endsAfter string
	if err := f.pool.QueryRow(ctx, `SELECT started_at,ends_on::text FROM tempo_hold_grace WHERE branch_id=$1`, f.branchA).Scan(&after, &endsAfter); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(started) || endsAfter != ends {
		t.Fatalf("unggah Senin tidak boleh memperpanjang grace Jumat: %v %s", after, endsAfter)
	}
	submitAt(27, http.StatusOK)       // Selasa, hari kerja kedua.
	submitAt(28, http.StatusConflict) // Rabu, hold kembali.
}

// OPS-04 (Surat Tagihan): proses mati saat PDF hendak diunggah. Transaksi
// dibatalkan; proses baru melanjutkan versi yang sama dan email tidak ganda.
func TestPGSuratTagihanPulihSesudahWorkerBerhentiSaatUnggah(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	id := tempoSubmitted(t, f, "2026-09-20", 100000)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	if _, err := svc.Approve(ctx, manager, id); err != nil {
		t.Fatal(err)
	}
	statementID, err := svc.IssueMonth(ctx, f.branchA, time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || statementID == "" {
		t.Fatalf("terbitkan versi tagihan: %s %v", statementID, err)
	}
	blocked := storageTerhenti{Store: f.objek, mulai: make(chan struct{})}
	workerCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { result <- svc.RenderStatement(workerCtx, blocked, rendererUji{}, statementID) }()
	select {
	case <-blocked.mulai:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("render tagihan tidak mencapai unggah storage")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("worker yang terhenti harus mengembalikan error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker yang terhenti tidak melepaskan transaksi")
	}
	var ready bool
	if err := f.pool.QueryRow(ctx, `SELECT storage_key IS NOT NULL FROM tempo_statements WHERE id=$1`, statementID).Scan(&ready); err != nil || ready {
		t.Fatalf("unggah terhenti tidak boleh menandai PDF siap: %v %v", ready, err)
	}
	var emails int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE jenis='tempo_statement'`).Scan(&emails); err != nil || emails != 0 {
		t.Fatalf("email tidak boleh antre sebelum PDF siap: %d %v", emails, err)
	}
	if err := svc.RenderStatement(ctx, f.objek, rendererUji{}, statementID); err != nil {
		t.Fatalf("proses baru harus menyelesaikan tagihan: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT storage_key IS NOT NULL FROM tempo_statements WHERE id=$1`, statementID).Scan(&ready); err != nil || !ready {
		t.Fatalf("PDF harus siap setelah restart: %v %v", ready, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE jenis='tempo_statement'`).Scan(&emails); err != nil || emails < 1 {
		t.Fatalf("email tagihan harus diantre setelah PDF siap: %d %v", emails, err)
	}
	if err := svc.RenderStatement(ctx, f.objek, rendererUji{}, statementID); err != nil {
		t.Fatal(err)
	}
	var versions, emailsAgain int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_statements WHERE branch_id=$1 AND period_month='2026-09-01'`, f.branchA).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("restart tidak boleh membuat versi tambahan: %d %v", versions, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE jenis='tempo_statement'`).Scan(&emailsAgain); err != nil || emailsAgain != emails {
		t.Fatalf("restart tidak boleh membuat email ganda: %d -> %d (%v)", emails, emailsAgain, err)
	}
}

// OPS-04 (dokumen): kegagalan tepat saat upload PDF tidak boleh mengantre
// email prematur, dan restart menghasilkan satu set versi dokumen yang sama.
func TestPGDokumenPulihSesudahWorkerBerhentiSaatUnggah(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	id := tempoSubmitted(t, f, "2026-10-20", 100000)
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	if _, err := svc.Approve(ctx, manager, id); err != nil {
		t.Fatal(err)
	}
	blocked := storageTerhenti{Store: f.objek, mulai: make(chan struct{})}
	interrupted := fulfillment.NewService(document.NewService(f.docRepo, blocked, rendererUji{}), f.notif)
	workerCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { _, runErr := interrupted.PesananSelesai(workerCtx, id); result <- runErr }()
	select {
	case <-blocked.mulai:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("render dokumen tidak mencapai unggah storage")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("worker yang terhenti harus mengembalikan error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker yang terhenti tidak melepaskan pekerjaan")
	}
	var docs, emails int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM documents WHERE order_id=$1`, id).Scan(&docs); err != nil || docs != 0 {
		t.Fatalf("dokumen terhenti tidak boleh dinyatakan siap: %d %v", docs, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE order_id=$1`, id).Scan(&emails); err != nil || emails != 0 {
		t.Fatalf("email sebelum dokumen siap: %d %v", emails, err)
	}
	if _, err := f.selesai.PesananSelesai(ctx, id); err != nil {
		t.Fatalf("proses baru harus menyelesaikan dokumen: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM documents WHERE order_id=$1`, id).Scan(&docs); err != nil || docs != 3 {
		t.Fatalf("restart harus menghasilkan 3 dokumen: %d %v", docs, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE order_id=$1`, id).Scan(&emails); err != nil || emails < 1 {
		t.Fatalf("email dokumen harus diantre setelah siap: %d %v", emails, err)
	}
	if _, err := f.selesai.PesananSelesai(ctx, id); err != nil {
		t.Fatal(err)
	}
	var docsAgain, emailsAgain int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM documents WHERE order_id=$1`, id).Scan(&docsAgain); err != nil || docsAgain != docs {
		t.Fatalf("restart ulang menggandakan dokumen: %d -> %d (%v)", docs, docsAgain, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE order_id=$1`, id).Scan(&emailsAgain); err != nil || emailsAgain != emails {
		t.Fatalf("restart ulang menggandakan email: %d -> %d (%v)", emails, emailsAgain, err)
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
	var nomor string
	var amount, paid int64
	var due time.Time
	if err := f.pool.QueryRow(ctx, `SELECT o.nomor,sl.amount,sl.paid_amount,sl.due_date
		FROM tempo_statement_lines sl JOIN orders o ON o.id=sl.order_id
		JOIN tempo_statements st ON st.id=sl.statement_id
		WHERE st.branch_id=$1 AND st.period_month='2099-10-01'`, f.branchA).
		Scan(&nomor, &amount, &paid, &due); err != nil {
		t.Fatalf("isi tagihan Oktober: %v", err)
	}
	if nomor != "TEMPO-2099-10-20" || amount != 110000 || paid != 0 || due.Format("2006-01-02") != "2099-11-05" {
		t.Fatalf("invoice/due/nominal tagihan salah: nomor=%s amount=%d paid=%d due=%s", nomor, amount, paid, due.Format("2006-01-02"))
	}
	file, err := f.objek.Get(ctx, key)
	if err != nil {
		t.Fatalf("PDF Surat Tagihan tidak tersimpan: %v", err)
	}
	pdfBytes, readErr := io.ReadAll(file)
	_ = file.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, isi := range []string{"TEMPO-2099-10-20", "05-11-2099", "Rp 110000", "Rp 0"} {
		if !bytes.Contains(pdfBytes, []byte(isi)) {
			t.Fatalf("PDF Surat Tagihan tidak memuat %q", isi)
		}
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
	if hasil := f.kirimAntrean(t); hasil.Terkirim != 2 || f.kirim.jumlah() != 2 {
		t.Fatalf("email Surat Tagihan harus benar-benar dikirim ke dua penerima: %+v", hasil)
	}
	var sent, branchMail, adminMail int
	if err := f.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE status='sent'),
		count(*) FILTER (WHERE 'cabang-a@example.test'=ANY(to_addr)),
		count(*) FILTER (WHERE 'rina@example.test'=ANY(to_addr))
		FROM email_outbox WHERE jenis='tempo_statement'`).
		Scan(&sent, &branchMail, &adminMail); err != nil {
		t.Fatal(err)
	}
	if sent != 2 || branchMail != 1 || adminMail != 1 {
		t.Fatalf("status/penerima email tagihan salah: sent=%d cabang=%d admin=%d", sent, branchMail, adminMail)
	}
}

// TMP-07: pekerjaan harian yang diulang harus menjaga PDF lama ketika order
// periode tertutup baru disetujui dan ketika invoice kemudian dikoreksi.
func TestPGTempoDailyMemversikanPersetujuanLamaDanKoreksi(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE admin_users SET notif_invoice=(id=$1) WHERE id IN ($1,$2)`, f.adminSA, f.adminMgr); err != nil {
		t.Fatal(err)
	}
	wib := time.FixedZone("WIB", 7*3600)
	jam := clock.Fixed(time.Date(2099, 10, 25, 9, 0, 0, 0, wib))
	svc := receivable.New(f.pool, f.jobs, receivable.WithClock(jam))
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	pertama := tempoSubmitted(t, f, "2099-10-20", 100000)
	if _, err := svc.Approve(ctx, manager, pertama); err != nil {
		t.Fatalf("persetujuan pertama: %v", err)
	}
	if err := jalankanPekerjaanDone(t, f, pertama); err != nil {
		t.Fatalf("dokumen pesanan pertama: %v", err)
	}

	terbit := func(waktu time.Time, versi int, total int64, baris int) (string, []byte) {
		t.Helper()
		if err := svc.Daily(ctx, f.objek, rendererUji{}, waktu); err != nil {
			t.Fatalf("pekerjaan harian v%d: %v", versi, err)
		}
		var id, key string
		var gotVersion, gotLines int
		var gotTotal int64
		if err := f.pool.QueryRow(ctx, `SELECT id::text,version,total_amount,storage_key,
			(SELECT count(*) FROM tempo_statement_lines WHERE statement_id=st.id)
			FROM tempo_statements st WHERE branch_id=$1 AND period_month='2099-10-01'
			ORDER BY version DESC LIMIT 1`, f.branchA).
			Scan(&id, &gotVersion, &gotTotal, &key, &gotLines); err != nil {
			t.Fatalf("baca Surat Tagihan v%d: %v", versi, err)
		}
		if gotVersion != versi || gotTotal != total || gotLines != baris || key == "" {
			t.Fatalf("Surat Tagihan v%d: versi=%d total=%d baris=%d key=%q", versi, gotVersion, gotTotal, gotLines, key)
		}
		file, err := f.objek.Get(ctx, key)
		if err != nil {
			t.Fatalf("baca PDF v%d: %v", versi, err)
		}
		defer file.Close()
		pdf, err := io.ReadAll(file)
		if err != nil || len(pdf) == 0 {
			t.Fatalf("PDF v%d kosong/gagal dibaca: %v", versi, err)
		}
		return id, pdf
	}

	v1, pdfV1 := terbit(time.Date(2099, 11, 1, 9, 0, 0, 0, wib), 1, 100000, 1)
	// Persetujuan setelah tanggal 1 tetap masuk periode submit Oktober.
	jam.Set(time.Date(2099, 11, 2, 9, 0, 0, 0, wib))
	kedua := tempoSubmitted(t, f, "2099-10-21", 200000)
	if _, err := svc.Approve(ctx, manager, kedua); err != nil {
		t.Fatalf("persetujuan terlambat: %v", err)
	}
	v2, pdfV2 := terbit(time.Date(2099, 11, 2, 10, 0, 0, 0, wib), 2, 300000, 2)
	if v1 == v2 || bytes.Equal(pdfV1, pdfV2) {
		t.Fatal("approval terlambat harus membuat versi/PDF baru")
	}

	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, body := reviseHTTP(t, f, cookies, csrf, pertama,
		reviseBody("koreksi harga", `{"line_no":1,"qty":1,"harga_satuan":150000}`))
	if status != http.StatusOK {
		t.Fatalf("revisi pesanan pertama: %d %+v", status, body)
	}
	if err := jalankanPekerjaanRevisi(t, f, pertama, 1); err != nil {
		t.Fatalf("dokumen revisi: %v", err)
	}
	// Koreksi positif disimpan sebagai charge tambahan, sehingga dua invoice
	// menghasilkan tiga baris charge dalam versi Surat Tagihan berikutnya.
	v3, pdfV3 := terbit(time.Date(2099, 11, 3, 9, 0, 0, 0, wib), 3, 350000, 3)
	if v3 == v2 || bytes.Equal(pdfV2, pdfV3) {
		t.Fatal("koreksi harus membuat versi/PDF baru")
	}
	// Ulangi job; fingerprint sama tidak boleh menambah versi atau outbox.
	if err := svc.Daily(ctx, f.objek, rendererUji{}, time.Date(2099, 11, 3, 10, 0, 0, 0, wib)); err != nil {
		t.Fatalf("pekerjaan harian berulang: %v", err)
	}
	var versions, emails int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_statements WHERE branch_id=$1 AND period_month='2099-10-01'`, f.branchA).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE jenis='tempo_statement'`).Scan(&emails); err != nil {
		t.Fatal(err)
	}
	if versions != 3 || emails != 6 {
		t.Fatalf("job berulang menggandakan versi/email: versi=%d email=%d", versions, emails)
	}
	branchCookies, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	statusUnduh, download, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/tempo/statements/"+v1+"/download", "", branchCookies, "")
	if statusUnduh != http.StatusOK {
		t.Fatalf("PDF historis v1 harus tetap dapat diunduh: %d %+v", statusUnduh, download)
	}
	link, _ := dataMap(t, download)["url"].(string)
	if !strings.Contains(link, "tempo-statement/"+v1+".pdf") {
		t.Fatalf("tautan unduh v1 menunjuk objek salah: %q", link)
	}
	fileV1, err := f.objek.Get(ctx, "tempo-statement/"+v1+".pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer fileV1.Close()
	oldPDF, err := io.ReadAll(fileV1)
	if err != nil || !bytes.Equal(oldPDF, pdfV1) {
		t.Fatalf("PDF historis v1 berubah sesudah koreksi: %v", err)
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

func TestPGPengingatTempoMengikutiKoreksiDanBerhentiSesudahLunas(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	id := tempoSubmitted(t, f, "2099-10-20", 100000)
	svc := receivable.New(f.pool, f.jobs)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	branch := auth.Actor{UserID: f.userA, Kind: auth.KindBranch, BranchID: f.branchA, Nama: "Cabang A"}
	if _, err := svc.Approve(ctx, manager, id); err != nil {
		t.Fatal(err)
	}
	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatal(err)
	}
	day := func(s string) time.Time {
		t.Helper()
		parsed, err := time.ParseInLocation("2006-01-02", s, time.FixedZone("WIB", 7*3600))
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	for i := 0; i < 2; i++ {
		if err := svc.SendReminders(ctx, day("2099-11-02")); err != nil {
			t.Fatal(err)
		}
	}
	cookies, csrf := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body := reviseHTTP(t, f, cookies, csrf, id, reviseBody("koreksi harga sebelum jatuh tempo", `{"line_no":1,"qty":1,"harga_satuan":70000}`))
	if status != http.StatusOK {
		t.Fatalf("koreksi harus 200: %d %v", status, body)
	}
	for i := 0; i < 2; i++ {
		if err := svc.SendReminders(ctx, day("2099-11-05")); err != nil {
			t.Fatal(err)
		}
	}
	var first, due int64
	if err := f.pool.QueryRow(ctx, `SELECT
 COALESCE(max(amount) FILTER (WHERE stage='h_minus_3'),0),
 COALESCE(max(amount) FILTER (WHERE stage='due'),0)
 FROM tempo_reminders WHERE branch_id=$1 AND due_date='2099-11-05'`, f.branchA).Scan(&first, &due); err != nil {
		t.Fatal(err)
	}
	if first != 100000 || due != 70000 {
		t.Fatalf("pengingat harus mengikuti koreksi: H-3=%d H=%d", first, due)
	}
	receipt, err := svc.UploadProof(ctx, branch, f.objek, receivable.UploadInput{
		Amount: 70000, BankAccountID: f.bankDefault, Filename: "lunas.jpg", Body: []byte{0xff, 0xd8, 0xff, 76},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(ctx, manager, receipt.ID, "verify", 70000, ""); err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2099-11-05", "2099-11-06", "2099-11-12"} {
		for i := 0; i < 2; i++ {
			if err := svc.SendReminders(ctx, day(date)); err != nil {
				t.Fatal(err)
			}
		}
	}
	var reminders, emails int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM tempo_reminders WHERE branch_id=$1 AND due_date='2099-11-05'`, f.branchA).Scan(&reminders); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE jenis='tempo_reminder' AND payload->>'branch_id'=$1`, f.branchA).Scan(&emails); err != nil {
		t.Fatal(err)
	}
	if reminders != 2 || emails != 2 {
		t.Fatalf("setelah lunas dan retry hanya H-3 serta H yang boleh ada: reminder=%d email=%d", reminders, emails)
	}
}

func TestPGSuratTagihanCabangLainTidakBocor(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	id := tempoSubmitted(t, f, "2026-10-20", 100000)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	if _, err := svc.Approve(ctx, manager, id); err != nil {
		t.Fatal(err)
	}
	statementID, err := svc.IssueMonth(ctx, f.branchA, time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || statementID == "" {
		t.Fatalf("terbitkan tagihan: %s %v", statementID, err)
	}
	if err := svc.RenderStatement(ctx, f.objek, rendererUji{}, statementID); err != nil {
		t.Fatal(err)
	}
	owner, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	other, _ := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	path := f.srv.URL + "/api/v1/tempo/statements/" + statementID + "/download"
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, path, "", owner, "")
	if status != http.StatusOK || dataMap(t, body)["url"] == "" {
		t.Fatalf("pemilik harus dapat tautan Surat Tagihan: %d %v", status, body)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, path, "", other, "")
	if status != http.StatusNotFound {
		t.Fatalf("cabang lain tidak boleh mengetahui Surat Tagihan: %d %v", status, body)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/tempo/statements", "", other, "")
	if status != http.StatusOK || len(dataList(t, body)) != 0 {
		t.Fatalf("daftar Surat Tagihan cabang lain harus kosong: %d %v", status, body)
	}
}
