package httpapi

// Uji integrasi riwayat invoice cabang (kartu F1-19) pada PostgreSQL nyata:
// filter periode, isolasi cabang, versi lama yang tetap tampil setelah revisi,
// dan invoice yang belum Done yang tidak bisa diunduh.

import (
	"context"
	"net/http"
	"testing"
)

// seedPesananSubmitted menyemai pesanan berstatus submitted: sudah punya nomor
// dan tanggal (BR-04) tetapi belum punya dokumen — bentuk "invoice belum Done"
// yang harus tetap tampil di riwayat tanpa PDF yang bisa diunduh.
func (f *pgFixture) seedPesananSubmitted(t *testing.T, branchID, nomor, tanggal string, total int64) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO orders (branch_id, nomor, status, tanggal, total, submitted_at, verif_status)
		 VALUES ($1,$2,'submitted',$3::date,$4, now(), 'belum')
		 RETURNING id`, branchID, nomor, tanggal, total).Scan(&id); err != nil {
		t.Fatalf("seed pesanan submitted %s: %v", nomor, err)
	}
	return id
}

// barisInvoice mencari satu baris riwayat invoice berdasarkan nomor pesanan.
func barisInvoice(t *testing.T, body map[string]any, nomor string) map[string]any {
	t.Helper()
	for _, item := range dataList(t, body) {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("baris invoice bukan objek: %v", item)
		}
		if row["nomor"] == nomor {
			return row
		}
	}
	t.Fatalf("invoice %s tidak ada di jawaban: %v", nomor, body["data"])
	return nil
}

// TestPGInvoicesRiwayatCabang menempuh janji kartu F1-19 lewat HTTP + PostgreSQL
// nyata: jumlah/status sesuai data DB, isolasi cabang, versi lama tetap tampil
// setelah revisi, invoice belum Done tak bisa diunduh, dan filter periode.
func TestPGInvoicesRiwayatCabang(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// Cabang A: dua pesanan Done (satu Oktober, satu September) + satu Submitted.
	doneOKt := f.seedPesananDone(t, f.branchA, "2026-10-0001", false)
	doneSep := f.seedPesananDone(t, f.branchA, "2026-09-0002", false)
	// Tanggal September untuk pesanan kedua (seedPesananDone selalu 8 Okt).
	if _, err := f.pool.Exec(ctx,
		`UPDATE orders SET tanggal = DATE '2026-09-30' WHERE id = $1`, doneSep.ID); err != nil {
		t.Fatalf("geser tanggal pesanan September: %v", err)
	}
	submitted := f.seedPesananSubmitted(t, f.branchA, "2026-10-0003", "2026-10-09", 150000)
	// Cabang B: satu pesanan Done yang TIDAK boleh muncul di riwayat cabang A.
	doneB := f.seedPesananDone(t, f.branchB, "2026-10-0004", false)

	for _, id := range []string{doneOKt.ID, doneSep.ID, doneB.ID} {
		if _, err := f.documents.Ensure(ctx, id); err != nil {
			t.Fatalf("Ensure dokumen %s: %v", id, err)
		}
	}

	cabangA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	cabangB, _ := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	sa, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	// 1) Riwayat cabang A: tiga pesanan ber-nomor, tanpa satu pun baris cabang B.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/invoices", "", cabangA, csrfA)
	if status != http.StatusOK {
		t.Fatalf("GET /invoices harus 200, dapat %d (%v)", status, body)
	}
	rows := dataList(t, body)
	if len(rows) != 3 {
		t.Fatalf("cabang A harus punya 3 baris invoice, dapat %d (%v)", len(rows), rows)
	}
	for _, item := range rows {
		if item.(map[string]any)["nomor"] == doneB.Nomor {
			t.Fatalf("invoice cabang lain bocor ke riwayat cabang A: %v", item)
		}
		if _, bocor := item.(map[string]any)["storage_key"]; bocor {
			t.Error("kunci storage tidak boleh dikirim ke klien")
		}
	}

	// 2) Pesanan Done: barisnya bisa diunduh dan menunjuk dokumen versi berjalan.
	listBody := body
	invoiceDone := barisInvoice(t, listBody, doneOKt.Nomor)
	if invoiceDone["status"] != "done" {
		t.Errorf("status invoice Done salah: %v", invoiceDone["status"])
	}
	if invoiceDone["dapat_diunduh"] != true {
		t.Errorf("invoice Done harus dapat diunduh: %v", invoiceDone)
	}
	if invoiceDone["versi_dokumen"] != float64(0) || invoiceDone["terbaru"] != true {
		t.Errorf("versi dokumen/terbaru invoice Done salah: %v", invoiceDone)
	}
	if invoiceDone["total"] != float64(doneOKt.Total) {
		t.Errorf("total = %v, mau %d", invoiceDone["total"], doneOKt.Total)
	}
	if invoiceDone["jumlah_item"] != float64(3) {
		t.Errorf("jumlah_item = %v, mau 3 (Σ qty baris produk)", invoiceDone["jumlah_item"])
	}
	docID, _ := invoiceDone["dokumen_id"].(string)
	if docID == "" {
		t.Fatal("invoice Done harus punya dokumen_id")
	}

	// Dokumen itu benar-benar milik pesanan cabang A dan bisa diunduh.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/documents/"+docID+"/download", "", cabangA, csrfA)
	if status != http.StatusOK {
		t.Fatalf("unduh invoice sendiri harus 200, dapat %d (%v)", status, body)
	}
	if url, _ := dataMap(t, body)["url"].(string); url == "" {
		t.Error("tautan unduh kosong")
	}
	// Cabang B tidak boleh mengunduh dokumen itu (riwayat lintas cabang ditolak).
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/documents/"+docID+"/download", "", cabangB, "")
	if status != http.StatusNotFound {
		t.Fatalf("cabang lain mengunduh invoice harus 404, dapat %d", status)
	}

	// 3) Pesanan Submitted: tampil, tanpa dokumen, dan tidak bisa diunduh.
	invoiceSubmitted := barisInvoice(t, listBody, "2026-10-0003")
	if invoiceSubmitted["status"] != "submitted" {
		t.Errorf("status invoice Submitted salah: %v", invoiceSubmitted["status"])
	}
	if invoiceSubmitted["versi_dokumen"] != nil {
		t.Errorf("invoice belum Done tidak boleh punya versi dokumen: %v", invoiceSubmitted)
	}
	if _, ada := invoiceSubmitted["dokumen_id"]; ada {
		t.Errorf("invoice belum Done tidak boleh punya dokumen_id: %v", invoiceSubmitted)
	}
	if invoiceSubmitted["dapat_diunduh"] != false {
		t.Errorf("invoice belum Done tidak boleh dapat diunduh: %v", invoiceSubmitted)
	}
	// Invoice-nya juga belum bisa dirender paksa: transisi Done belum terjadi.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+submitted+"/documents", "", sa, csrfSA)
	if status != http.StatusConflict {
		t.Fatalf("render dokumen pesanan belum Done harus 409, dapat %d (%v)", status, body)
	}
	if kode, _ := dataErrorKode(body); kode != "ORDER_NOT_DONE" {
		t.Errorf("kode error = %q, mau ORDER_NOT_DONE", kode)
	}

	// 4) Filter periode inklusif: hanya Oktober (0001 dan 0003).
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/invoices?dari=2026-10-01&sampai=2026-10-31", "", cabangA, csrfA)
	if status != http.StatusOK {
		t.Fatalf("filter periode harus 200, dapat %d (%v)", status, body)
	}
	rows = dataList(t, body)
	if len(rows) != 2 {
		t.Fatalf("periode Oktober harus 2 baris, dapat %d (%v)", len(rows), rows)
	}
	if adaInvoice(t, body, "2026-09-0002") {
		t.Error("invoice September tidak boleh masuk periode Oktober")
	}
	// Batas inklusif: satu hari tepat di tanggal pesanan September.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/invoices?dari=2026-09-30&sampai=2026-09-30", "", cabangA, csrfA)
	if status != http.StatusOK {
		t.Fatalf("filter batas inklusif harus 200, dapat %d (%v)", status, body)
	}
	if len(dataList(t, body)) != 1 {
		t.Errorf("batas inklusif harus memuat invoice 2026-09-30: %v", body["data"])
	}

	// 5) Parameter tidak terbaca dijawab 400, bukan daftar kosong.
	for _, q := range []string{"?dari=01-10-2026", "?sampai=kemarin", "?dari=2026-10-31&sampai=2026-10-01"} {
		status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
			f.srv.URL+"/api/v1/invoices"+q, "", cabangA, csrfA)
		if status != http.StatusBadRequest {
			t.Fatalf("filter %s harus 400, dapat %d (%v)", q, status, body)
		}
		if kode, _ := dataErrorKode(body); kode != "VALIDATION_FAILED" {
			t.Errorf("kode error filter %s = %q", q, kode)
		}
	}

	// 6) Versi lama tetap tampil (dan tetap bisa diunduh) setelah revisi.
	//    Revisi = versi pesanan naik lalu invoice versi baru dirender; berkas
	//    versi lama tidak disentuh (F1-17/F2-25).
	versiLama := docID
	if _, err := f.pool.Exec(ctx, `UPDATE orders SET versi = 1 WHERE id = $1`, doneOKt.ID); err != nil {
		t.Fatalf("naikkan versi pesanan: %v", err)
	}
	baru, err := f.documents.EnsureInvoice(ctx, doneOKt.ID)
	if err != nil {
		t.Fatalf("EnsureInvoice setelah revisi: %v", err)
	}
	versiBaru := baru.ID
	if baru.Versi != 1 {
		t.Fatalf("dokumen revisi harus versi 1, dapat %d", baru.Versi)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/invoices", "", cabangA, csrfA)
	if status != http.StatusOK {
		t.Fatalf("GET /invoices setelah revisi harus 200, dapat %d (%v)", status, body)
	}
	rows = dataList(t, body)
	if len(rows) != 4 {
		t.Fatalf("setelah revisi harus 4 baris (v1+v0+submitted+September), dapat %d (%v)", len(rows), rows)
	}
	terlihat := map[float64]bool{}
	for _, item := range rows {
		row := item.(map[string]any)
		if row["nomor"] != doneOKt.Nomor {
			continue
		}
		versi, _ := row["versi_dokumen"].(float64)
		terlihat[versi] = true
		if row["dapat_diunduh"] != true {
			t.Errorf("invoice versi %v harus tetap dapat diunduh: %v", versi, row)
		}
		// versi dokumen terbaru = versi pesanan sekarang (1).
		mauTerbaru := versi == 1
		if row["terbaru"] != mauTerbaru {
			t.Errorf("terbaru versi %v = %v, mau %v", versi, row["terbaru"], mauTerbaru)
		}
	}
	if !terlihat[0] || !terlihat[1] {
		t.Fatalf("versi lama hilang setelah revisi: %v", terlihat)
	}
	// Berkas versi lama masih ada dan masih boleh diunduh cabang pemiliknya.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/documents/"+versiLama+"/download", "", cabangA, csrfA)
	if status != http.StatusOK {
		t.Fatalf("unduh versi lama harus 200, dapat %d (%v)", status, body)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/documents/"+versiBaru+"/download", "", cabangA, csrfA)
	if status != http.StatusOK {
		t.Fatalf("unduh versi baru harus 200, dapat %d (%v)", status, body)
	}

	// 7) Admin Pusat tidak punya riwayat invoice cabang: 403, bukan daftar.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/invoices", "", sa, csrfSA)
	if status != http.StatusForbidden {
		t.Fatalf("akun Pusat pada /invoices harus 403, dapat %d", status)
	}
}

// dataErrorKode membaca kode error dari bentuk seragam {error:{code,...}}.
func dataErrorKode(body map[string]any) (string, bool) {
	raw, ok := body["error"].(map[string]any)
	if !ok {
		return "", false
	}
	kode, _ := raw["code"].(string)
	return kode, kode != ""
}

// adaInvoice melaporkan apakah riwayat memuat nomor pesanan tertentu.
func adaInvoice(t *testing.T, body map[string]any, nomor string) bool {
	t.Helper()
	for _, item := range dataList(t, body) {
		if row, ok := item.(map[string]any); ok && row["nomor"] == nomor {
			return true
		}
	}
	return false
}
