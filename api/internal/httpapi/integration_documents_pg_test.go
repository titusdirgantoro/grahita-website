package httpapi

// Uji integrasi dokumen pesanan (kartu F1-17) pada PostgreSQL nyata:
// render + penyimpanan Invoice PDF dan Surat Jalan XLSX/PDF, idempotensi,
// berkas versi lama yang tidak tertimpa, dan akses unduh per peran/cabang.
//
// Renderer di fixture ini adalah renderer uji yang MENYISIPKAN HTML template ke
// dalam berkas "PDF"-nya, sehingga pengujian HTTP bisa memeriksa isi dokumen
// (nomor, serial, total) tanpa mesin PDF. Validitas PDF sungguhan diuji di
// integration_documents_pdf_test.go yang memakai Chromium nyata.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/document"
	documentpg "github.com/grahita/portal/api/internal/document/postgres"
	"github.com/grahita/portal/api/internal/platform/pdf"
)

// rendererUji meniru Gotenberg tanpa mesin PDF: HTML yang dirender ikut
// disimpan di dalam "berkas PDF" supaya isinya bisa diperiksa uji.
type rendererUji struct{}

// HTMLToPDF membungkus HTML menjadi berkas berawalan %PDF.
func (rendererUji) HTMLToPDF(_ context.Context, html []byte) ([]byte, error) {
	out := make([]byte, 0, len(html)+32)
	out = append(out, []byte("%PDF-1.4\n%")...)
	out = append(out, html...)
	out = append(out, []byte("\n%%EOF\n")...)
	return out, nil
}

// Health melengkapi kontrak pdf.Renderer.
func (rendererUji) Health(context.Context) error { return nil }

// pesananUji adalah pesanan Done yang disemai langsung di database.
type pesananUji struct {
	ID      string
	Nomor   string
	Serials []string
	Total   int64
}

// seedPesananDone menyemai satu pesanan berstatus done lengkap dengan dua baris
// produk berserial dan snapshot verifikasi final yang sah. Pesanan dibuat lewat
// SQL langsung karena transisi Done adalah kartu F1-18, tetapi tetap harus
// memenuhi invariant Done yang sama dengan jalur service.
func (f *pgFixture) seedPesananDone(t *testing.T, branchID, nomor string, isCNC bool) pesananUji {
	t.Helper()
	ctx := context.Background()

	var produkID string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM products WHERE kode = 'P-01'`).Scan(&produkID); err != nil {
		t.Fatalf("baca produk: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO bank_accounts (bank, nama_pemilik, nomor, is_active)
		 VALUES ('BCA','PT Grahita Indonesia','1234567890',true),
		        ('BRI','PT Grahita Indonesia','0987654321',false)
		 ON CONFLICT (bank, nomor) DO NOTHING`); err != nil {
		t.Fatalf("seed rekening: %v", err)
	}

	// Serial resmi: YYYYMMDD + urut harian 3 digit (BR-02); basis nomor supaya
	// tiap pesanan unik dalam satu uji.
	basis := nomor[len(nomor)-2:]
	serial1 := "20261008" + basis + "1"
	serial2 := "20261008" + basis + "2"

	var id string
	var jatuhTempo any
	if isCNC {
		jt := time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC)
		jatuhTempo = jt
	}
	total := int64(2*100000 + 50000)
	err := f.pool.QueryRow(ctx,
		`INSERT INTO orders (branch_id, nomor, status, tanggal, total, is_cnc, jatuh_tempo,
		                     submitted_at, done_at, verif_status)
		 VALUES ($1,$2,'done',DATE '2026-10-08',$3,$4,$5, now(), now(), 'cocok')
		 RETURNING id`,
		branchID, nomor, total, isCNC, jatuhTempo).Scan(&id)
	if err != nil {
		t.Fatalf("seed pesanan done %s: %v", nomor, err)
	}

	baris := []struct {
		no     int
		nama   string
		qty    int
		harga  int64
		serial string
	}{
		{1, "Bio Magnetik", 2, 100000, serial1},
		{2, "Bio Listrik", 1, 50000, serial2},
	}
	for _, b := range baris {
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO order_lines (order_id, line_no, product_id, nama_snapshot,
			                          qty, harga_satuan, serial_number)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			id, b.no, produkID, b.nama, b.qty, b.harga, b.serial); err != nil {
			t.Fatalf("seed baris pesanan %s: %v", nomor, err)
		}
	}
	f.seedVerifikasiDokumenDone(t, id, branchID, nomor, total, isCNC)
	return pesananUji{ID: id, Nomor: nomor, Serials: []string{serial1, serial2}, Total: total}
}

// seedVerifikasiDokumenDone melengkapi fixture SQL dengan attempt sukses yang
// ditunjuk orders.done_verification_log_id. Transfer membawa satu bukti dan
// snapshot rekening tujuan; C&C tanpa bukti tetap membawa attempt sukses dengan
// destination_results kosong sesuai kontrak dokumen historis.
func (f *pgFixture) seedVerifikasiDokumenDone(
	t *testing.T, orderID, branchID, nomor string, total int64, isCNC bool,
) {
	t.Helper()
	ctx := context.Background()

	var allowlistVersion int64
	var verificationID string
	if isCNC {
		if err := f.pool.QueryRow(ctx,
			`SELECT bank_allowlist_version FROM branches WHERE id=$1`, branchID).Scan(&allowlistVersion); err != nil {
			t.Fatalf("baca versi allowlist %s: %v", nomor, err)
		}
		if err := f.pool.QueryRow(ctx,
			`INSERT INTO verification_logs
			   (order_id, metode, total_pesanan, total_terbaca, hasil, admin_id, catatan,
			    branch_allowlist_version, destination_results)
			 VALUES ($1,'manual',$2,$2,'cocok',$3,'fixture C&C tanpa bukti',$4,'[]'::jsonb)
			 RETURNING id`, orderID, total, f.adminSA, allowlistVersion).Scan(&verificationID); err != nil {
			t.Fatalf("seed verifikasi C&C %s: %v", nomor, err)
		}
	} else {
		var bankID, bankCode, accountOwner, accountNumber string
		if err := f.pool.QueryRow(ctx,
			`SELECT id::text, bank::text, nama_pemilik, nomor
			   FROM bank_accounts WHERE bank='BCA' AND nomor='1234567890'`).Scan(
			&bankID, &bankCode, &accountOwner, &accountNumber); err != nil {
			t.Fatalf("baca rekening snapshot %s: %v", nomor, err)
		}
		tag, err := f.pool.Exec(ctx,
			`INSERT INTO branch_bank_accounts (branch_id, bank_account_id, is_allowed, assigned_by)
			 VALUES ($1,$2,true,$3) ON CONFLICT (branch_id, bank_account_id) DO NOTHING`,
			branchID, bankID, f.adminSA)
		if err != nil {
			t.Fatalf("seed assignment rekening %s: %v", nomor, err)
		}
		if tag.RowsAffected() > 0 {
			if err := f.pool.QueryRow(ctx,
				`UPDATE branches SET bank_allowlist_version=bank_allowlist_version+1
				  WHERE id=$1 RETURNING bank_allowlist_version`, branchID).Scan(&allowlistVersion); err != nil {
				t.Fatalf("naikkan versi allowlist %s: %v", nomor, err)
			}
		} else if err := f.pool.QueryRow(ctx,
			`SELECT bank_allowlist_version FROM branches WHERE id=$1`, branchID).Scan(&allowlistVersion); err != nil {
			t.Fatalf("baca versi allowlist %s: %v", nomor, err)
		}
		var proofID string
		if err := f.pool.QueryRow(ctx,
			`INSERT INTO payment_proofs
			   (order_id, storage_key, nama_file, mime_type, ukuran_byte, sha256,
			    uploaded_by_kind, uploaded_by_id)
			 VALUES ($1,$2,'bukti-dokumen.jpg','image/jpeg',4,convert_to($3,'UTF8'),'admin',$4)
			 RETURNING id`, orderID, "proofs/"+orderID+"/bukti-dokumen.jpg", nomor, f.adminSA).Scan(&proofID); err != nil {
			t.Fatalf("seed bukti pembayaran %s: %v", nomor, err)
		}
		if err := f.pool.QueryRow(ctx,
			`INSERT INTO verification_logs
			   (order_id, metode, total_pesanan, total_terbaca, hasil, admin_id, catatan,
			    branch_allowlist_version, destination_results)
			 VALUES ($1,'manual',$2,$2,'cocok',$3,'fixture dokumen dengan snapshot tujuan',$4,
			   jsonb_build_array(jsonb_build_object(
			     'payment_proof_id',$5::text,
			     'status','allowed',
			     'source','manual',
			     'bank_account_id',$6::text,
			     'bank_snapshot',$7::text,
			     'account_owner_snapshot',$8::text,
			     'account_last_digits_snapshot',right($9::text,4))))
			 RETURNING id`, orderID, total, f.adminSA, allowlistVersion, proofID, bankID,
			bankCode, accountOwner, accountNumber).Scan(&verificationID); err != nil {
			t.Fatalf("seed verifikasi transfer %s: %v", nomor, err)
		}
	}

	if _, err := f.pool.Exec(ctx,
		`UPDATE orders SET done_verification_log_id=$2 WHERE id=$1`, orderID, verificationID); err != nil {
		t.Fatalf("pasang pointer verifikasi %s: %v", nomor, err)
	}
}

// barisDokumen menghitung baris `documents` satu pesanan beserta kuncinya.
func (f *pgFixture) barisDokumen(t *testing.T, orderID string) []document.Document {
	t.Helper()
	rows, err := f.docRepo.ListByOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("baca baris dokumen: %v", err)
	}
	return rows
}

// isiBerkas membaca isi satu objek dokumen dari object storage uji.
func (f *pgFixture) isiBerkas(t *testing.T, key string) []byte {
	t.Helper()
	rc, err := f.objek.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("objek %s tidak ada: %v", key, err)
	}
	defer func() { _ = rc.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		t.Fatalf("baca objek %s: %v", key, err)
	}
	return buf.Bytes()
}

// TestPGDokumenGenerateIdempotenDanIsolasi menempuh seluruh janji kartu lewat
// HTTP + PostgreSQL nyata: tiga berkas valid dengan kunci sesuai, angka/serial
// cocok snapshot, render ulang tidak menggandakan, dan isolasi cabang.
func TestPGDokumenGenerateIdempotenDanIsolasi(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	pesananA := f.seedPesananDone(t, f.branchA, "2026-10-0001", false)
	pesananB := f.seedPesananDone(t, f.branchB, "2026-10-0002", true)

	cabangA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	cabangB, _ := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	sa, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	// 1) Cabang tidak boleh memicu render dokumen.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+pesananA.ID+"/documents", "", cabangA, csrfA)
	if status != http.StatusForbidden {
		t.Fatalf("cabang pada generate harus 403, dapat %d (%v)", status, body)
	}
	if len(f.barisDokumen(t, pesananA.ID)) != 0 {
		t.Fatal("permintaan yang ditolak tidak boleh menghasilkan dokumen")
	}

	// 2) Staf Pusat membuat dokumen; hasilnya tiga berkas dengan kunci tetap.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+pesananA.ID+"/documents", "", sa, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("generate harus 200, dapat %d (%v)", status, body)
	}
	daftar := dataList(t, body)
	if len(daftar) != 3 {
		t.Fatalf("harus 3 dokumen, dapat %v", body["data"])
	}
	idPerJenis := map[string]string{}
	for _, item := range daftar {
		d := item.(map[string]any)
		jenis, _ := d["jenis"].(string)
		idPerJenis[jenis], _ = d["id"].(string)
		if d["order_id"] != pesananA.ID {
			t.Errorf("order_id dokumen salah: %v", d["order_id"])
		}
		if d["versi"] != float64(0) {
			t.Errorf("versi dokumen harus mengikuti versi pesanan: %v", d["versi"])
		}
		if ukuran, _ := d["ukuran_byte"].(float64); ukuran <= 0 {
			t.Errorf("ukuran dokumen harus terisi: %v", d)
		}
		if _, bocor := d["storage_key"]; bocor {
			t.Error("kunci storage tidak boleh dikirim ke klien")
		}
	}
	for _, jenis := range []string{"invoice_pdf", "surat_jalan_xlsx", "surat_jalan_pdf"} {
		if idPerJenis[jenis] == "" {
			t.Fatalf("dokumen %s tidak ada: %v", jenis, idPerJenis)
		}
	}

	// Baris database + objek storage memakai kunci documents/{order}/{jenis}-{versi}.{ext}.
	rows := f.barisDokumen(t, pesananA.ID)
	if len(rows) != 3 {
		t.Fatalf("harus 3 baris dokumen di database, dapat %d", len(rows))
	}
	for _, d := range rows {
		mau := document.StorageKey(pesananA.ID, d.Jenis, 0)
		if d.StorageKey != mau {
			t.Errorf("kunci %s = %q, mau %q", d.Jenis, d.StorageKey, mau)
		}
		if _, err := f.objek.Get(ctx, mau); err != nil {
			t.Errorf("objek %s tidak ada di storage: %v", mau, err)
		}
	}

	// 3) Isi dokumen cocok dengan snapshot pesanan (nomor, serial, total, rekening, penanda).
	invoice := string(f.isiBerkas(t, document.StorageKey(pesananA.ID, "invoice_pdf", 0)))
	for _, wajib := range []string{
		"INVOICE", pesananA.Nomor, "08 Oktober 2026", "Grahita Cabang A", "Kepala A",
		pesananA.Serials[0], pesananA.Serials[1], "Rp 250.000", "LUNAS",
		"BCA — 1234567890",
	} {
		if !strings.Contains(invoice, wajib) {
			t.Errorf("isi invoice tidak memuat %q", wajib)
		}
	}
	// Rekening nonaktif tidak pernah tercetak sebagai instruksi pembayaran.
	if strings.Contains(invoice, "0987654321") {
		t.Error("rekening nonaktif tidak boleh muncul di invoice")
	}
	// Pesanan B adalah Cash & Carry.
	if _, err := f.documents.Ensure(ctx, pesananB.ID); err != nil {
		t.Fatalf("Ensure pesanan B: %v", err)
	}
	invoiceB := string(f.isiBerkas(t, document.StorageKey(pesananB.ID, "invoice_pdf", 0)))
	if !strings.Contains(invoiceB, "CASH &amp; CARRY") {
		t.Error("pesanan C&C harus ditandai CASH & CARRY")
	}

	// 4) Surat jalan XLSX valid: satu baris per order line + serial.
	xlsx := f.isiBerkas(t, document.StorageKey(pesananA.ID, "surat_jalan_xlsx", 0))
	if !bytes.HasPrefix(xlsx, []byte("PK")) {
		t.Fatal("surat jalan XLSX bukan berkas xlsx")
	}
	wb, err := excelize.OpenReader(bytes.NewReader(xlsx))
	if err != nil {
		t.Fatalf("surat jalan XLSX tidak dapat dibuka: %v", err)
	}
	defer func() { _ = wb.Close() }()
	if v, _ := wb.GetCellValue(document.SheetSuratJalan, "B3"); v != pesananA.Nomor {
		t.Errorf("nomor di surat jalan = %q", v)
	}
	for i, s := range pesananA.Serials {
		sel := fmt.Sprintf("D%d", 9+i)
		if v, _ := wb.GetCellValue(document.SheetSuratJalan, sel); v != s {
			t.Errorf("%s = %q, mau %q", sel, v, s)
		}
	}
	if v, _ := wb.GetCellValue(document.SheetSuratJalan, fmt.Sprintf("A%d", 9+len(pesananA.Serials))); v != "" {
		t.Errorf("baris data berlebih: %q", v)
	}

	// 5) Idempoten: render ulang tidak menggandakan baris/berkas.
	sebelum := f.isiBerkas(t, document.StorageKey(pesananA.ID, "invoice_pdf", 0))
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+pesananA.ID+"/documents", "", sa, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("generate ulang harus 200, dapat %d (%v)", status, body)
	}
	for _, item := range dataList(t, body) {
		d := item.(map[string]any)
		jenis, _ := d["jenis"].(string)
		if d["id"] != idPerJenis[jenis] {
			t.Errorf("render ulang mengubah id dokumen %s: %v", jenis, d["id"])
		}
	}
	if len(f.barisDokumen(t, pesananA.ID)) != 3 {
		t.Error("render ulang tidak boleh menggandakan baris dokumen")
	}
	if !bytes.Equal(sebelum, f.isiBerkas(t, document.StorageKey(pesananA.ID, "invoice_pdf", 0))) {
		t.Error("berkas dokumen versi 0 ditulis ulang padahal sudah ada")
	}

	// 6) Daftar dokumen per peran: cabang hanya invoice miliknya.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/orders/"+pesananA.ID+"/documents", "", cabangA, "")
	if status != http.StatusOK {
		t.Fatalf("daftar dokumen cabang harus 200, dapat %d (%v)", status, body)
	}
	listCabang := dataList(t, body)
	if len(listCabang) != 1 || listCabang[0].(map[string]any)["jenis"] != "invoice_pdf" {
		t.Errorf("cabang hanya boleh melihat invoice-nya: %v", body["data"])
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/orders/"+pesananA.ID+"/documents", "", cabangB, "")
	if status != http.StatusNotFound {
		t.Errorf("pesanan cabang lain harus 404, dapat %d (%v)", status, body)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/orders/"+pesananA.ID+"/documents", "", sa, "")
	if status != http.StatusOK {
		t.Errorf("staf Pusat harus boleh daftar dokumen, dapat %d", status)
	}
	// Endpoint admin: Manager boleh, cabang ditolak.
	mgr, _ := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/orders/"+pesananA.ID+"/documents", "", mgr, "")
	if status != http.StatusOK || len(dataList(t, body)) != 3 {
		t.Errorf("manager harus melihat 3 dokumen, dapat %d (%v)", status, body)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/orders/"+pesananA.ID+"/documents", "", cabangA, "")
	if status != http.StatusForbidden {
		t.Errorf("cabang pada endpoint admin harus 403, dapat %d", status)
	}

	// 7) Unduhan: presigned pendek, dibatasi peran/cabang.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/documents/"+idPerJenis["invoice_pdf"]+"/download", "", cabangA, "")
	if status != http.StatusOK {
		t.Fatalf("cabang harus boleh unduh invoice-nya, dapat %d (%v)", status, body)
	}
	dl := dataMap(t, body)
	if dl["expires_in"] != float64(300) {
		t.Errorf("umur tautan harus pendek (300 detik), dapat %v", dl["expires_in"])
	}
	if nama, _ := dl["nama_file"].(string); nama != "Invoice-"+pesananA.Nomor+".pdf" {
		t.Errorf("nama berkas unduhan = %v", dl["nama_file"])
	}
	if url, _ := dl["url"].(string); !strings.Contains(url, document.StorageKey(pesananA.ID, "invoice_pdf", 0)) {
		t.Errorf("tautan unduh tidak menunjuk objek yang benar: %q", url)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/documents/"+idPerJenis["surat_jalan_xlsx"]+"/download", "", cabangA, "")
	if status != http.StatusNotFound {
		t.Errorf("surat jalan bukan dokumen cabang, harus 404, dapat %d (%v)", status, body)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/documents/"+idPerJenis["invoice_pdf"]+"/download", "", cabangB, "")
	if status != http.StatusNotFound {
		t.Errorf("dokumen cabang lain harus 404, dapat %d", status)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/documents/"+idPerJenis["surat_jalan_pdf"]+"/download", "", sa, "")
	if status != http.StatusOK {
		t.Errorf("staf Pusat harus boleh unduh surat jalan, dapat %d (%v)", status, body)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/documents/"+idPerJenis["invoice_pdf"]+"/download", "", nil, "")
	if status != http.StatusUnauthorized {
		t.Errorf("tanpa sesi harus 401, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/documents/bukan-uuid/download", "", sa, "")
	if status != http.StatusNotFound {
		t.Errorf("id bukan UUID harus 404, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/documents/11111111-2222-3333-4444-555555555555/download", "", sa, "")
	if status != http.StatusNotFound {
		t.Errorf("dokumen tak dikenal harus 404, dapat %d", status)
	}
}

// TestPGDokumenPesananBelumDone menjaga PRD §6.1 di jalur HTTP: dokumen hanya
// untuk pesanan Done.
func TestPGDokumenPesananBelumDone(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	var orderID string
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO orders (branch_id, nomor, status, tanggal, total, submitted_at)
		 VALUES ($1,'2026-10-0007','submitted',DATE '2026-10-08',150000, now()) RETURNING id`,
		f.branchA).Scan(&orderID); err != nil {
		t.Fatalf("seed pesanan submitted: %v", err)
	}

	sa, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+orderID+"/documents", "", sa, csrfSA)
	if status != http.StatusConflict {
		t.Fatalf("pesanan belum Done harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "ORDER_NOT_DONE")
	if len(f.barisDokumen(t, orderID)) != 0 {
		t.Error("tidak boleh ada dokumen untuk pesanan yang belum Done")
	}

	// Pesanan tidak dikenal & id bukan UUID juga tidak menghasilkan apa pun.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/11111111-2222-3333-4444-555555555555/documents", "", sa, csrfSA)
	if status != http.StatusNotFound {
		t.Errorf("pesanan tak dikenal harus 404, dapat %d", status)
	}
}

// TestPGDokumenVersiBaruTidakMenimpaBerkasLama menempuh revisi invoice
// (versi +1): berkas versi lama tetap utuh, dokumen versi baru memakai kunci
// baru, dan kedua versi tercatat (PRD §9 no.8).
func TestPGDokumenVersiBaruTidakMenimpaBerkasLama(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	pesanan := f.seedPesananDone(t, f.branchA, "2026-10-0003", false)
	sa, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+pesanan.ID+"/documents", "", sa, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("generate versi 0 harus 200, dapat %d (%v)", status, body)
	}
	kunciV0 := document.StorageKey(pesanan.ID, "invoice_pdf", 0)
	isiV0 := f.isiBerkas(t, kunciV0)

	// Revisi invoice: versi pesanan naik dan isinya berubah (kartu F2-25).
	if _, err := f.pool.Exec(ctx,
		`UPDATE orders SET versi = 1, total = 300000 WHERE id = $1`, pesanan.ID); err != nil {
		t.Fatalf("naikkan versi pesanan: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE order_lines SET qty = 3 WHERE order_id = $1 AND line_no = 1`, pesanan.ID); err != nil {
		t.Fatalf("ubah qty baris: %v", err)
	}

	// Revisi hanya merender ulang INVOICE (surat jalan tidak ikut direvisi).
	doc, err := f.documents.EnsureInvoice(ctx, pesanan.ID)
	if err != nil {
		t.Fatalf("EnsureInvoice versi 1: %v", err)
	}
	if doc.Versi != 1 || doc.StorageKey != document.StorageKey(pesanan.ID, "invoice_pdf", 1) {
		t.Fatalf("dokumen revisi salah: %+v", doc)
	}
	if !bytes.Equal(isiV0, f.isiBerkas(t, kunciV0)) {
		t.Error("berkas invoice versi 0 tertimpa oleh versi 1")
	}
	isiV1 := string(f.isiBerkas(t, document.StorageKey(pesanan.ID, "invoice_pdf", 1)))
	if !strings.Contains(isiV1, "Revisi v1") {
		t.Error("invoice versi 1 harus menandai revisinya")
	}
	if !strings.Contains(isiV1, "Rp 350.000") {
		t.Error("invoice versi 1 harus memakai total pesanan terbaru (qty 3 x 100.000 + 50.000)")
	}

	rows := f.barisDokumen(t, pesanan.ID)
	if len(rows) != 4 {
		t.Fatalf("harus 4 baris (3 versi 0 + 1 versi 1), dapat %d", len(rows))
	}
	versi := map[string]bool{}
	for _, d := range rows {
		versi[fmt.Sprintf("%s-v%d", d.Jenis, d.Versi)] = true
	}
	for _, mau := range []string{"invoice_pdf-v0", "invoice_pdf-v1", "surat_jalan_xlsx-v0", "surat_jalan_pdf-v0"} {
		if !versi[mau] {
			t.Errorf("dokumen %s tidak tercatat: %v", mau, versi)
		}
	}

	// Generate ulang (Ensure penuh) setelah revisi tidak menyentuh versi lama
	// dan tidak membuat versi ganda.
	if _, err := f.documents.Ensure(ctx, pesanan.ID); err != nil {
		t.Fatalf("Ensure setelah revisi: %v", err)
	}
	if len(f.barisDokumen(t, pesanan.ID)) != 6 {
		t.Errorf("harus 6 baris (3 versi 0 + 3 versi 1), dapat %d", len(f.barisDokumen(t, pesanan.ID)))
	}
	if !bytes.Equal(isiV0, f.isiBerkas(t, kunciV0)) {
		t.Error("berkas versi 0 berubah setelah Ensure penuh")
	}
	// Unduhan versi lama/baru tetap bisa dibuat (berkasnya masih ada).
	actorSA := auth.Actor{UserID: "u-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
	if _, err := f.documents.DownloadURL(ctx, actorSA, doc.ID); err != nil {
		t.Errorf("unduh dokumen versi 1: %v", err)
	}
}

// TestPGDokumenRekeningNonAktifTidakTercetak menjaga instruksi pembayaran hanya
// memuat rekening aktif (rekening nonaktif tersembunyi dari cabang).
func TestPGDokumenRekeningNonAktifTidakTercetak(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	pesanan := f.seedPesananDone(t, f.branchA, "2026-10-0004", false)
	if _, err := f.documents.Ensure(ctx, pesanan.ID); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	invoice := string(f.isiBerkas(t, document.StorageKey(pesanan.ID, "invoice_pdf", 0)))
	if !strings.Contains(invoice, "BCA") {
		t.Error("rekening aktif harus tercetak di invoice")
	}
	if strings.Contains(invoice, "BRI") || strings.Contains(invoice, "0987654321") {
		t.Error("rekening nonaktif tidak boleh tercetak di invoice")
	}

	// Rekening yang dinonaktifkan setelah render tidak mengubah dokumen lama
	// (dokumen adalah snapshot; revisilah yang membuat versi baru).
	if _, err := f.pool.Exec(ctx, `UPDATE bank_accounts SET is_active = false WHERE nomor = '1234567890'`); err != nil {
		t.Fatalf("nonaktifkan rekening: %v", err)
	}
	if _, err := f.documents.Ensure(ctx, pesanan.ID); err != nil {
		t.Fatalf("Ensure ulang: %v", err)
	}
	if got := string(f.isiBerkas(t, document.StorageKey(pesanan.ID, "invoice_pdf", 0))); got != invoice {
		t.Error("dokumen versi yang sudah ada tidak boleh dirender ulang")
	}
}

// repoBalapanPG membungkus repository dokumen PostgreSQL nyata untuk meniru
// balapan dua permintaan render atas dokumen yang sama: panggilan SaveAll
// PERTAMA lebih dulu benar-benar commit barisnya (seolah permintaan pesaing
// menang balapan), lalu mengembalikan kegagalan — persis keadaan yang membuat
// permintaan ini menjalankan pembersihan objek. Panggilan berikutnya diteruskan
// ke repository asli.
type repoBalapanPG struct {
	*documentpg.Repo
	mu      sync.Mutex
	panggil int
}

func (r *repoBalapanPG) SaveAll(ctx context.Context, req document.SaveRequest) ([]document.Document, error) {
	r.mu.Lock()
	r.panggil++
	ke := r.panggil
	r.mu.Unlock()
	if ke == 1 {
		if _, err := r.Repo.SaveAll(ctx, req); err != nil {
			return nil, err
		}
		return nil, errors.New("simulasi: SaveAll gagal setelah pesaing commit")
	}
	return r.Repo.SaveAll(ctx, req)
}

// TestPGDokumenKegagalanCommitTidakMencabutBerkasBarisLain adalah uji regresi
// HTTP + PostgreSQL nyata untuk temuan pemblokir QA F1-17: bila SaveAll gagal
// setelah baris (order_id, jenis, versi) ter-commit, objek storage yang sudah
// dirujuk baris itu TIDAK boleh dihapus — kalau dihapus, Invoice/Surat Jalan
// pesanan hilang permanen dari sisi unduhan dan render ulang tidak
// menyembuhkannya (barisnya dianggap sudah ada).
func TestPGDokumenKegagalanCommitTidakMencabutBerkasBarisLain(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	pesanan := f.seedPesananDone(t, f.branchA, "2026-10-0009", false)

	deps := f.deps
	deps.Documents = document.NewService(&repoBalapanPG{Repo: f.docRepo}, f.objek, rendererUji{})
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)

	sa, csrfSA := pgLogin(t, srv, "rina@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		srv.URL+"/api/v1/admin/orders/"+pesanan.ID+"/documents", "", sa, csrfSA)
	if status != http.StatusInternalServerError {
		t.Fatalf("SaveAll yang gagal harus dijawab 500, dapat %d (%v)", status, body)
	}

	rows := f.barisDokumen(t, pesanan.ID)
	if len(rows) != 3 {
		t.Fatalf("baris dokumen yang sudah commit harus tetap ada (3), dapat %d", len(rows))
	}
	for _, d := range rows {
		if _, err := f.objek.Get(ctx, d.StorageKey); err != nil {
			t.Errorf("baris %s kehilangan berkasnya di storage: %v", d.Jenis, err)
		}
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
			srv.URL+"/api/v1/documents/"+d.ID+"/download", "", sa, "")
		if status != http.StatusOK {
			t.Fatalf("unduh %s harus 200 karena objeknya masih ada, dapat %d (%v)", d.Jenis, status, body)
		}
		dl := dataMap(t, body)
		if url, _ := dl["url"].(string); !strings.Contains(url, d.StorageKey) {
			t.Errorf("tautan unduh %s tidak menunjuk objeknya: %q", d.Jenis, url)
		}
	}

	// Permintaan berikutnya menemukan baris yang sudah ada: idempoten 200 tanpa
	// merender ulang dan tanpa menggandakan baris.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		srv.URL+"/api/v1/admin/orders/"+pesanan.ID+"/documents", "", sa, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("generate ulang harus 200, dapat %d (%v)", status, body)
	}
	if len(f.barisDokumen(t, pesanan.ID)) != 3 {
		t.Error("generate ulang tidak boleh menggandakan baris dokumen")
	}
}

// storePGMenang membungkus object storage bersama (objekHitung) dan menutup
// `siap` begitu pemiliknya selesai menulis `butuh` objek — supaya permintaan
// pesaing yang gagal menjalankan pembersihannya SETELAH objek pemenang tertulis
// tetapi SEBELUM pemenang commit (jendela yang dilaporkan QA putaran 2).
type storePGMenang struct {
	*objekHitung
	mu     sync.Mutex
	n      int
	butuh  int
	siap   chan struct{}
	sekali sync.Once
}

// storePGMenangBaru menyiapkan pembungkus storage pemenang.
func storePGMenangBaru(m *objekHitung, butuh int) *storePGMenang {
	return &storePGMenang{objekHitung: m, butuh: butuh, siap: make(chan struct{})}
}

// Put menyimpan objek lalu menandai gerbang bila seluruh objek sudah ditulis.
func (s *storePGMenang) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if err := s.objekHitung.Put(ctx, key, body, size, contentType); err != nil {
		return err
	}
	s.mu.Lock()
	s.n++
	penuh := s.n >= s.butuh
	s.mu.Unlock()
	if penuh {
		s.sekali.Do(func() { close(s.siap) })
	}
	return nil
}

// storePGKalah meniru permintaan dokumen yang memakai object storage yang SAMA
// tetapi selalu gagal menulis satu kunci (Surat Jalan XLSX = dokumen kedua).
type storePGKalah struct {
	*objekHitung
	gagalKey string
	tunggu   <-chan struct{}
}

// Put gagal untuk kunci yang ditandai, setelah menunggu gerbang pemenang.
func (s *storePGKalah) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if key == s.gagalKey {
		if s.tunggu != nil {
			select {
			case <-s.tunggu:
			case <-time.After(60 * time.Second):
				return errors.New("uji buntu: pemenang tidak menyelesaikan penulisan objeknya")
			}
		}
		return errors.New("simulasi: storage mati saat menulis dokumen kedua")
	}
	return s.objekHitung.Put(ctx, key, body, size, contentType)
}

// repoPGTahan menahan SaveAll sampai gerbangnya dibuka: meniru pemenang yang
// sudah menulis seluruh objeknya tetapi belum commit ketika pesaing gagal.
type repoPGTahan struct {
	*documentpg.Repo
	siap <-chan struct{}
}

// SaveAll menunggu gerbang lalu meneruskan ke repository PostgreSQL nyata.
func (r *repoPGTahan) SaveAll(ctx context.Context, req document.SaveRequest) ([]document.Document, error) {
	select {
	case <-r.siap:
	case <-time.After(60 * time.Second):
		return nil, errors.New("uji buntu: gerbang commit tidak pernah dibuka")
	}
	return r.Repo.SaveAll(ctx, req)
}

// TestPGDokumenKegagalanPutTidakMencabutBerkasPemenang adalah uji regresi QA
// putaran 2 butir 1/2(ii) pada PostgreSQL NYATA + service produksi: dua
// permintaan render paralel atas dokumen yang sama, satu gagal di Put dokumen
// kedua, sementara SaveAll pemenang ditahan sampai pembersihan yang kalah
// selesai. Pada 8f91c3a urutan ini menghapus objek yang kemudian dirujuk baris
// pemenang (berkas hilang permanen); aturan putaran 3 (jalur render tidak
// pernah menghapus objek) harus menjaga setiap baris tetap punya objeknya dan
// tautan unduhnya menunjuk objek yang ada.
func TestPGDokumenKegagalanPutTidakMencabutBerkasPemenang(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	pesanan := f.seedPesananDone(t, f.branchA, "2026-10-0011", false)

	menang := storePGMenangBaru(f.objek, 3)
	kalah := &storePGKalah{
		objekHitung: f.objek,
		gagalKey:    document.StorageKey(pesanan.ID, document.JenisSuratJalanXLSX, 0),
		tunggu:      menang.siap,
	}
	gerbangCommit := make(chan struct{})
	repo := &repoPGTahan{Repo: f.docRepo, siap: gerbangCommit}

	svcMenang := document.NewService(repo, menang, rendererUji{})
	svcKalah := document.NewService(repo, kalah, rendererUji{})

	var wg sync.WaitGroup
	var errMenang, errKalah error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errMenang = svcMenang.Ensure(ctx, pesanan.ID)
	}()
	go func() {
		defer wg.Done()
		_, errKalah = svcKalah.Ensure(ctx, pesanan.ID)
		close(gerbangCommit)
	}()
	wg.Wait()

	if errKalah == nil {
		t.Fatal("permintaan yang Put-nya gagal harus mengembalikan error")
	}
	if errMenang != nil {
		t.Fatalf("permintaan pemenang harus sukses, dapat %v", errMenang)
	}

	// Baris pemenang ada di PostgreSQL dan SETIAP baris wajib masih punya
	// objeknya di storage (bukan hanya HTTP 200: presign S3 tidak memeriksa
	// keberadaan objek).
	rows := f.barisDokumen(t, pesanan.ID)
	if len(rows) != 3 {
		t.Fatalf("harus 3 baris dokumen pemenang, dapat %d", len(rows))
	}

	deps := f.deps
	deps.Documents = svcMenang
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)

	sa, _ := pgLogin(t, srv, "rina@example.test", "rahasia123")
	for _, d := range rows {
		if _, err := f.objek.Get(ctx, d.StorageKey); err != nil {
			t.Errorf("baris %s kehilangan berkasnya di storage: %v", d.Jenis, err)
			continue
		}
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
			srv.URL+"/api/v1/documents/"+d.ID+"/download", "", sa, "")
		if status != http.StatusOK {
			t.Fatalf("unduh %s harus 200, dapat %d (%v)", d.Jenis, status, body)
		}
		dl := dataMap(t, body)
		url, _ := dl["url"].(string)
		if !strings.Contains(url, d.StorageKey) {
			t.Errorf("tautan unduh %s tidak menunjuk objeknya: %q", d.Jenis, url)
		}
	}
}

// rendererMati meniru Gotenberg yang belum dikonfigurasi.
type rendererMati struct{}

// HTMLToPDF selalu menolak dengan ErrNotConfigured.
func (rendererMati) HTMLToPDF(context.Context, []byte) ([]byte, error) {
	return nil, pdf.ErrNotConfigured
}

// Health melaporkan mesin PDF memang belum aktif.
func (rendererMati) Health(context.Context) error { return pdf.ErrNotConfigured }

// TestPGDokumenRendererTidakSiapMenjawab503 menjaga kegagalan mesin PDF
// dijawab jelas (bukan 500) dan tidak menyisakan baris/berkas.
func TestPGDokumenRendererTidakSiapMenjawab503(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	pesanan := f.seedPesananDone(t, f.branchA, "2026-10-0005", false)

	// Router kedua memakai renderer yang menolak karena belum dikonfigurasi.
	deps := f.deps
	deps.Documents = document.NewService(f.docRepo, f.objek, rendererMati{})
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)

	sa, csrfSA := pgLogin(t, srv, "rina@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		srv.URL+"/api/v1/admin/orders/"+pesanan.ID+"/documents", "", sa, csrfSA)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("renderer tidak siap harus 503, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "DOCUMENT_RENDER_UNAVAILABLE")
	if len(f.barisDokumen(t, pesanan.ID)) != 0 {
		t.Error("gagal render tidak boleh menyisakan baris dokumen")
	}
	if _, err := f.objek.Get(ctx, document.StorageKey(pesanan.ID, "invoice_pdf", 0)); err == nil {
		t.Error("gagal render tidak boleh menyisakan berkas")
	}
}
