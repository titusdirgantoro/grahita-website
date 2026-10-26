package httpapi

// Uji integrasi submit pesanan (F1-14) pada PostgreSQL nyata.
//
// Yang dibuktikan berkas ini adalah perilaku yang hanya ada di database:
// penguncian baris pesanan, validasi ulang harga/produk dari master,
// kewajiban bukti non-C&C, penomoran dari tabel counter (invoice bulanan
// global + serial harian, tanpa celah dan tanpa bentrok saat 100 submit
// paralel), rollback yang tidak memakai nomor, trigger orders_guard_submit,
// dan tanggal bisnis WIB.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// multipartSubmit mengirim POST /orders/{id}/submit sebagai multipart:
// berkas bukti (boleh kosong) dan versi saldo deposit yang dilihat cabang C&C
// (kosong = tidak dikirim).
func multipartSubmit(t *testing.T, url string, cookies []*http.Cookie, csrf string, berkas map[string][]byte, versiSaldo string, paymentMode ...string) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for nama, isi := range berkas {
		fw, err := w.CreateFormFile(payment.FieldBerkas, nama)
		if err != nil {
			t.Fatalf("buat bagian multipart: %v", err)
		}
		if _, err := fw.Write(isi); err != nil {
			t.Fatalf("tulis isi berkas: %v", err)
		}
	}
	if versiSaldo != "" {
		if err := w.WriteField("expected_journal_version", versiSaldo); err != nil {
			t.Fatalf("tulis field expected_journal_version: %v", err)
		}
	}
	if len(paymentMode) > 0 {
		if err := w.WriteField("payment_mode", paymentMode[0]); err != nil {
			t.Fatalf("tulis field payment_mode: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("tutup multipart: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("buat request submit: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if csrf != "" {
		req.Header.Set(httpx.CSRFHeader, csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kirim submit: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("body submit bukan JSON (%d): %v", resp.StatusCode, err)
	}
	return resp, decoded
}

// submitParalel adalah submit yang aman dipanggil dari banyak goroutine: tanpa
// t.Fatal, hanya mengembalikan status dan nomor invoice (bila berhasil).
func submitParalel(url string, cookies []*http.Cookie, csrf, versiSaldo string) (int, string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if versiSaldo != "" {
		if err := w.WriteField("expected_journal_version", versiSaldo); err != nil {
			return 0, ""
		}
	}
	if err := w.Close(); err != nil {
		return 0, ""
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		return 0, ""
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if csrf != "" {
		req.Header.Set(httpx.CSRFHeader, csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ""
	}
	var body struct {
		Data struct {
			Nomor string `json:"nomor"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, ""
	}
	return resp.StatusCode, body.Data.Nomor
}

// wibHariIni mengembalikan tanggal bisnis WIB yang dihitung DATABASE — sumber
// kebenaran yang sama dengan yang dipakai penomoran.
func wibHariIni(t *testing.T, f *pgFixture) (tanggal, periodeInvoice, periodeSerial string) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT wib_today()::text, invoice_periode(wib_today()), serial_periode(wib_today())`).
		Scan(&tanggal, &periodeInvoice, &periodeSerial); err != nil {
		t.Fatalf("baca tanggal bisnis WIB: %v", err)
	}
	return tanggal, periodeInvoice, periodeSerial
}

// hitungCounter membaca nilai satu counter penomoran.
func hitungCounter(t *testing.T, f *pgFixture, scope, periode string) int {
	t.Helper()
	var n int
	err := f.pool.QueryRow(context.Background(),
		`SELECT COALESCE((SELECT last_value FROM numbering_counters WHERE scope=$1 AND periode=$2), 0)`,
		scope, periode).Scan(&n)
	if err != nil {
		t.Fatalf("baca counter %s/%s: %v", scope, periode, err)
	}
	return n
}

// buatDraftHTTP membuat draft lewat API untuk cabang sesi.
func buatDraftHTTP(t *testing.T, f *pgFixture, cookies []*http.Cookie, csrf, productID string, qty int) map[string]any {
	t.Helper()
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/orders",
		fmt.Sprintf(`{"lines":[{"product_id":%q,"qty":%d}]}`, productID, qty), cookies, csrf)
	if status != http.StatusCreated {
		t.Fatalf("buat draft harus 201, dapat %d (%v)", status, body)
	}
	return dataMap(t, body)
}

// produkKode mengembalikan id produk berdasarkan kode.
func produkKode(t *testing.T, f *pgFixture, kode string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM products WHERE kode = $1`, kode).Scan(&id); err != nil {
		t.Fatalf("baca produk %s: %v", kode, err)
	}
	return id
}

// setSetting menulis satu setelan sistem (jsonb) apa pun keadaan tabelnya.
//
// Catatan penting: fixture uji ini mengosongkan tabel domain lewat TRUNCATE
// CASCADE, dan `settings` ikut terpotong karena `settings.updated_by`
// mereferensikan `admin_users`. Jadi baris setelan bawaan migrasi TIDAK ada di
// dalam uji; nilai yang dibutuhkan harus ditulis eksplisit — dan bila tidak
// ada, domain memakai nilai bawaan Go (settings.Default), seperti semestinya.
func setSetting(t *testing.T, f *pgFixture, key, nilaiJSON string) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO settings (key, value) VALUES ($1, $2::jsonb)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = now()`, key, nilaiJSON)
	if err != nil {
		t.Fatalf("tulis setelan %s: %v", key, err)
	}
}

// TestPGSubmitEndToEndMultipart menjalankan alur submit lengkap lewat HTTP:
// draft -> submit multipart dengan bukti -> pesanan bernomor, berserial,
// bertanggal WIB, bercatatan timeline, dan terkunci.
func TestPGSubmitEndToEndMultipart(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")

	draft := buatDraftHTTP(t, f, cookiesA, csrfA, produk, 2)
	id, _ := draft["id"].(string)
	if draft["status"] != "draft" || draft["total"] != float64(200000) {
		t.Fatalf("draft tidak sesuai: %v", draft)
	}

	resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA,
		map[string][]byte{"bukti.jpg": berkasJPEGUji("bukti-submit-f114")}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit harus 200, dapat %d (%v)", resp.StatusCode, body)
	}
	hasil := dataMap(t, body)

	tanggal, periode, periodeSerial := wibHariIni(t, f)
	if hasil["status"] != "submitted" {
		t.Errorf("status harus submitted, dapat %v", hasil["status"])
	}
	if hasil["nomor"] != periode+"-01" {
		t.Errorf("nomor harus %s-01, dapat %v", periode, hasil["nomor"])
	}
	if hasil["tanggal"] != tanggal {
		t.Errorf("tanggal harus tanggal bisnis WIB %s, dapat %v", tanggal, hasil["tanggal"])
	}
	if hasil["is_cnc"] != false {
		t.Errorf("non-C&C tidak boleh ditandai C&C: %v", hasil["is_cnc"])
	}
	if hasil["total"] != float64(200000) {
		t.Errorf("total harus 200000, dapat %v", hasil["total"])
	}
	if hasil["submitted_at"] == nil || hasil["submitted_at"] == "" {
		t.Error("submitted_at harus terisi")
	}
	if at, _ := hasil["submitted_at"].(string); !strings.HasSuffix(at, "Z") {
		t.Errorf("submitted_at harus disimpan/dikirim dalam UTC (berakhiran Z), dapat %q", at)
	}
	lines, _ := hasil["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("harus 1 baris, dapat %d", len(lines))
	}
	baris := lines[0].(map[string]any)
	if baris["serial_number"] != periodeSerial+"001" {
		t.Errorf("serial harus %s001, dapat %v", periodeSerial, baris["serial_number"])
	}

	// Keadaan di database: nomor/tanggal/serial/timeline/bukti.
	var statusDB, nomorDB, tanggalDB string
	var jatuhTempoDB *string
	var submittedAt *string
	if err := f.pool.QueryRow(ctx,
		`SELECT status::text, nomor, tanggal::text, jatuh_tempo::text, submitted_at::text
		   FROM orders WHERE id = $1`, id).Scan(&statusDB, &nomorDB, &tanggalDB, &jatuhTempoDB, &submittedAt); err != nil {
		t.Fatalf("baca orders: %v", err)
	}
	if statusDB != "submitted" || nomorDB != periode+"-01" || tanggalDB != tanggal {
		t.Fatalf("orders di database tidak sesuai: %s %s %s", statusDB, nomorDB, tanggalDB)
	}
	if jatuhTempoDB != nil {
		t.Errorf("jatuh_tempo non-C&C harus NULL, dapat %v", *jatuhTempoDB)
	}
	if submittedAt == nil {
		t.Error("submitted_at harus tersimpan (UTC)")
	}

	var serialDB *string
	if err := f.pool.QueryRow(ctx, `SELECT serial_number FROM order_lines WHERE order_id = $1`, id).Scan(&serialDB); err != nil {
		t.Fatalf("baca serial: %v", err)
	}
	if serialDB == nil || *serialDB != periodeSerial+"001" {
		t.Fatalf("serial di database tidak sesuai: %v", serialDB)
	}

	var events int
	var teks string
	if err := f.pool.QueryRow(ctx, `SELECT count(*), COALESCE(max(teks),'') FROM order_events WHERE order_id = $1`, id).Scan(&events, &teks); err != nil {
		t.Fatalf("baca timeline: %v", err)
	}
	if events != 1 || teks != "Pesanan dikirim oleh cabang" {
		t.Errorf("timeline submit tidak tercatat: %d %q", events, teks)
	}

	var bukti int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM payment_proofs WHERE order_id = $1`, id).Scan(&bukti); err != nil {
		t.Fatalf("baca bukti: %v", err)
	}
	if bukti != 1 {
		t.Errorf("bukti yang diunggah bersama submit harus tersimpan, dapat %d", bukti)
	}

	// Submit ulang: 409 ORDER_LOCKED, dan counter tidak bertambah.
	resp2, body2 := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA, nil, "")
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("submit ulang harus 409, dapat %d (%v)", resp2.StatusCode, body2)
	}
	assertErrorShape(t, body2, "ORDER_LOCKED")
	if got := hitungCounter(t, f, "invoice", periode); got != 1 {
		t.Errorf("submit ulang tidak boleh memakai nomor, counter=%d", got)
	}

	// Pesanan Submitted terkunci untuk cabang: ubah dan hapus ditolak 409.
	status, body3, _ := apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/orders/"+id,
		fmt.Sprintf(`{"lines":[{"product_id":%q,"qty":1}]}`, produk), cookiesA, csrfA)
	if status != http.StatusConflict {
		t.Errorf("ubah pesanan submitted harus 409, dapat %d (%v)", status, body3)
	}
	status, body4, _ := apiReq(t, http.DefaultClient, http.MethodDelete, f.srv.URL+"/api/v1/orders/"+id, "", cookiesA, csrfA)
	if status != http.StatusConflict {
		t.Errorf("hapus pesanan submitted harus 409, dapat %d (%v)", status, body4)
	}
}

// TestPGSubmitNonCNCTanpaBuktiDitolak memverifikasi CP-ORD-04: submit non-C&C
// tanpa bukti ditolak TANPA memakai nomor dan tanpa mengubah pesanan; bukti
// yang sudah diunggah pada draft boleh dipakai submit.
func TestPGSubmitNonCNCTanpaBuktiDitolak(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")
	draft := buatDraftHTTP(t, f, cookiesA, csrfA, produk, 1)
	id, _ := draft["id"].(string)

	resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA, nil, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("submit tanpa bukti harus 400, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
	fields, _ := body["error"].(map[string]any)["fields"].(map[string]any)
	if fields == nil || fields["bukti"] == nil {
		t.Errorf("galat harus menjelaskan kekurangan bukti: %v", body)
	}

	var jumlahCounter int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM numbering_counters`).Scan(&jumlahCounter); err != nil {
		t.Fatalf("baca counter: %v", err)
	}
	if jumlahCounter != 0 {
		t.Errorf("submit gagal tidak boleh menggerakkan counter, baris=%d", jumlahCounter)
	}
	var statusDB string
	var nomorDB, serialDB *string
	if err := f.pool.QueryRow(ctx, `SELECT status::text, nomor FROM orders WHERE id = $1`, id).Scan(&statusDB, &nomorDB); err != nil {
		t.Fatalf("baca orders: %v", err)
	}
	if statusDB != "draft" || nomorDB != nil {
		t.Errorf("pesanan harus tetap draft tanpa nomor: %s %v", statusDB, nomorDB)
	}
	if err := f.pool.QueryRow(ctx, `SELECT serial_number FROM order_lines WHERE order_id = $1`, id).Scan(&serialDB); err != nil {
		t.Fatalf("baca serial: %v", err)
	}
	if serialDB != nil {
		t.Errorf("serial tidak boleh terisi saat submit gagal: %v", serialDB)
	}
	var events int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1`, id).Scan(&events); err != nil {
		t.Fatalf("baca timeline: %v", err)
	}
	if events != 0 {
		t.Errorf("submit gagal tidak boleh menulis timeline, dapat %d", events)
	}

	// Bukti diunggah lebih dulu pada draft (F1-13), lalu submit tanpa berkas.
	respBukti, bodyBukti := multipartUnggah(t, f.srv.URL+"/api/v1/orders/"+id+"/proofs", cookiesA, csrfA,
		map[string][]byte{"bukti.jpg": berkasJPEGUji("bukti-draft-f114")})
	if respBukti.StatusCode != http.StatusCreated {
		t.Fatalf("unggah bukti pada draft harus 201, dapat %d (%v)", respBukti.StatusCode, bodyBukti)
	}
	resp2, body2 := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA, nil, "")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("submit dengan bukti tersimpan harus 200, dapat %d (%v)", resp2.StatusCode, body2)
	}
	hasil := dataMap(t, body2)
	_, periode, periodeSerial := wibHariIni(t, f)
	if hasil["nomor"] != periode+"-01" {
		t.Errorf("nomor harus mulai dari 01 setelah percobaan gagal, dapat %v", hasil["nomor"])
	}
	if got := hitungCounter(t, f, "serial", periodeSerial); got != 1 {
		t.Errorf("counter serial harus 1, dapat %d", got)
	}
}

// TestPGSubmitHargaDanNamaMasterBerubah memverifikasi BR-05: harga dan nama
// yang di-snapshot saat submit adalah nilai master yang berlaku SAAT
// ITU — bukan angka yang tersimpan di draft.
func TestPGSubmitHargaDanNamaMasterBerubah(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")

	draft := buatDraftHTTP(t, f, cookiesA, csrfA, produk, 2)
	id, _ := draft["id"].(string)
	if draft["total"] != float64(200000) {
		t.Fatalf("harga draft harus 2×100000, dapat %v", draft["total"])
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO payment_proofs (order_id, branch_id, storage_key, nama_file, mime_type, ukuran_byte, sha256, uploaded_by_kind)
		 VALUES ($1,(SELECT branch_id FROM orders WHERE id=$1), 'k', 'b.jpg', 'image/jpeg', 10, '\\x01', 'branch')`, id); err != nil {
		t.Fatalf("seed bukti: %v", err)
	}

	// Master berubah SETELAH draft disimpan.
	if _, err := f.pool.Exec(ctx,
		`UPDATE branch_prices SET harga = 150000 WHERE branch_id = $1 AND product_id = $2`, f.branchA, produk); err != nil {
		t.Fatalf("ubah harga cabang: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE products SET nama = 'Bio Magnetik Edisi Baru' WHERE id = $1`, produk); err != nil {
		t.Fatalf("ubah master produk: %v", err)
	}

	resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit harus 200, dapat %d (%v)", resp.StatusCode, body)
	}
	hasil := dataMap(t, body)
	lines, _ := hasil["lines"].([]any)
	baris := lines[0].(map[string]any)
	if baris["harga_satuan"] != float64(150000) || baris["subtotal"] != float64(300000) {
		t.Errorf("snapshot harus memakai harga terbaru 150000: %v", baris)
	}
	if baris["nama"] != "Bio Magnetik Edisi Baru" {
		t.Errorf("snapshot nama harus dari master terbaru: %v", baris)
	}
	if _, ada := baris["satuan"]; ada {
		t.Errorf("snapshot tidak boleh memuat satuan: %v", baris)
	}
	if hasil["total"] != float64(300000) {
		t.Errorf("total harus dihitung ulang 2×150000, dapat %v", hasil["total"])
	}
	var hargaDB, totalDB int64
	if err := f.pool.QueryRow(ctx,
		`SELECT (SELECT harga_satuan FROM order_lines WHERE order_id = $1), total FROM orders WHERE id = $1`,
		id).Scan(&hargaDB, &totalDB); err != nil {
		t.Fatalf("baca harga/total: %v", err)
	}
	if hargaDB != 150000 || totalDB != 300000 {
		t.Errorf("database harus menyimpan snapshot terbaru: harga=%d total=%d", hargaDB, totalDB)
	}
}

// TestPGSubmitCNCMemotongDepositAtomik memverifikasi CP-CNC-02 di PostgreSQL:
// pesanan cabang C&C dibayar dari saldo deposit DI DALAM transaksi submit
// (alokasi, journal, dan saldo tetap konsisten), saldo yang berubah sejak
// dilihat cabang ditolak tanpa memakai nomor maupun saldo, sisa yang tidak
// tertutup deposit wajib berbukti, dan dua submit yang berebut saldo yang sama
// tidak pernah membuat saldo negatif.
func TestPGSubmitCNCMemotongDepositAtomik(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_cnc = true, allow_transfer = false WHERE id = $1`, f.branchA); err != nil {
		t.Fatalf("aktifkan flag C&C cabang A: %v", err)
	}
	seedSaldoDeposit(t, f, f.branchA, 300000)
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")
	urlSubmit := func(id string) string { return f.srv.URL + "/api/v1/orders/" + id + "/submit" }
	// saldo membaca saldo + versi journal yang dilihat cabang, dan memastikan
	// cache saldo selalu sama dengan journal serta tidak pernah negatif.
	saldo := func() (int64, string) {
		t.Helper()
		var balance, versi int64
		var cocok bool
		if err := f.pool.QueryRow(ctx, `SELECT a.balance, a.journal_version,
			a.balance = (SELECT COALESCE(sum(amount),0) FROM branch_deposit_transactions WHERE branch_id=a.branch_id)
			FROM branch_deposit_accounts a WHERE a.branch_id=$1`, f.branchA).Scan(&balance, &versi, &cocok); err != nil {
			t.Fatalf("baca saldo: %v", err)
		}
		if !cocok || balance < 0 {
			t.Fatalf("saldo %d tidak rekonsiliasi dengan journal atau negatif", balance)
		}
		return balance, strconv.FormatInt(versi, 10)
	}
	terpakai := func(orderID string) int64 {
		t.Helper()
		var n int64
		if err := f.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount-reversed_amount),0)::bigint
			FROM deposit_allocations WHERE order_id=$1`, orderID).Scan(&n); err != nil {
			t.Fatalf("baca alokasi: %v", err)
		}
		return n
	}

	// Versi saldo yang dilihat cabang wajib dikirim dan harus masih berlaku;
	// penolakan tidak memakai nomor maupun saldo.
	_, dilihat := saldo()
	d1 := buatDraftHTTP(t, f, cookiesA, csrfA, produk, 2)
	id1, _ := d1["id"].(string)
	if resp, body := multipartSubmit(t, urlSubmit(id1), cookiesA, csrfA, nil, ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("tanpa versi saldo harus 400, dapat %d (%v)", resp.StatusCode, body)
	}
	seedSaldoDeposit(t, f, f.branchA, 1)
	resp, body := multipartSubmit(t, urlSubmit(id1), cookiesA, csrfA, nil, dilihat)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("saldo berubah harus 409, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "DEPOSIT_CHANGED")
	var nomor *string
	if err := f.pool.QueryRow(ctx, `SELECT nomor FROM orders WHERE id=$1`, id1).Scan(&nomor); err != nil || nomor != nil {
		t.Fatalf("pesanan yang ditolak harus tetap tanpa nomor: %v %v", nomor, err)
	}
	if s, _ := saldo(); s != 300001 {
		t.Fatalf("submit yang ditolak tidak boleh memakai saldo, sisa %d", s)
	}

	// Saldo cukup: dibayar penuh dari deposit tanpa bukti transfer.
	_, dilihat = saldo()
	resp, body = multipartSubmit(t, urlSubmit(id1), cookiesA, csrfA, nil, dilihat)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit C&C bersaldo cukup harus 200, dapat %d (%v)", resp.StatusCode, body)
	}
	hasil := dataMap(t, body)
	if hasil["is_cnc"] != true || hasil["status"] != "submitted" {
		t.Fatalf("pesanan harus submitted bertanda C&C: %v", hasil)
	}
	total := int64(hasil["total"].(float64))
	if s, _ := saldo(); terpakai(id1) != total || s != 300001-total {
		t.Fatalf("deposit terpakai %d / saldo %d tidak sesuai total %d", terpakai(id1), s, total)
	}
	var jatuhTempo *string
	if err := f.pool.QueryRow(ctx, `SELECT jatuh_tempo::text FROM orders WHERE id=$1`, id1).Scan(&jatuhTempo); err != nil || jatuhTempo != nil {
		t.Fatalf("C&C berdeposit tidak punya jatuh tempo: %v %v", jatuhTempo, err)
	}

	// Saldo kurang: seluruh saldo terpakai dan sisanya wajib berbukti.
	sisaSaldo, dilihat := saldo()
	d2 := buatDraftHTTP(t, f, cookiesA, csrfA, produk, 2)
	id2, _ := d2["id"].(string)
	if resp, body := multipartSubmit(t, urlSubmit(id2), cookiesA, csrfA, nil, dilihat); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("sisa tanpa bukti harus 400, dapat %d (%v)", resp.StatusCode, body)
	}
	if s, _ := saldo(); s != sisaSaldo {
		t.Fatal("submit tanpa bukti tidak boleh memakai saldo")
	}
	resp, body = multipartSubmit(t, urlSubmit(id2), cookiesA, csrfA,
		map[string][]byte{"bukti.jpg": berkasJPEGUji("bukti-sisa-cnc")}, dilihat)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit campuran harus 200, dapat %d (%v)", resp.StatusCode, body)
	}
	if s, _ := saldo(); terpakai(id2) != sisaSaldo || s != 0 {
		t.Fatalf("seluruh saldo %d harus terpakai pesanan campuran: terpakai %d saldo %d", sisaSaldo, terpakai(id2), s)
	}

	// Dua submit berebut saldo yang sama: tepat satu menang, yang lain melihat
	// saldo berubah, dan saldo tidak pernah negatif.
	seedSaldoDeposit(t, f, f.branchA, 150000)
	_, dilihat = saldo()
	var ids []string
	for i := 0; i < 2; i++ {
		d := buatDraftHTTP(t, f, cookiesA, csrfA, produk, 1)
		id, _ := d["id"].(string)
		ids = append(ids, id)
	}
	kode := make([]int, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			kode[i], _ = submitParalel(urlSubmit(id), cookiesA, csrfA, dilihat)
		}(i, id)
	}
	wg.Wait()
	sukses, konflik := 0, 0
	for _, k := range kode {
		switch k {
		case http.StatusOK:
			sukses++
		case http.StatusConflict:
			konflik++
		}
	}
	if sukses != 1 || konflik != 1 {
		t.Fatalf("balapan saldo harus tepat satu 200 dan satu 409, dapat %v", kode)
	}
	if s, _ := saldo(); s != 150000-terpakai(ids[0])-terpakai(ids[1]) {
		t.Fatalf("saldo setelah balapan harus 150000 dikurangi satu pesanan, dapat %d", s)
	}
}

// TestPGSubmitSerialPackagingNonSerial memverifikasi BR-02/CP-SER-02: baris
// packaging tidak pernah mendapat serial, sedangkan setiap baris produk
// mendapat serial berurutan dari satu rentang.
func TestPGSubmitPackagingTanpaSerial(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")

	// Produk Sertifikat baru yang dijual ke cabang A (harga 0, constraint).
	var sertifikat string
	err := f.pool.QueryRow(ctx,
		`INSERT INTO products (kode, nama, jenis, harga_default, urutan)
		 VALUES ('P-SER-F114', 'Sertifikat F114', 'sertifikat', 0, 9) RETURNING id`).Scan(&sertifikat)
	if err != nil {
		t.Fatalf("seed produk sertifikat: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO branch_prices (branch_id, product_id, harga) VALUES ($1, $2, 0)`, f.branchA, sertifikat); err != nil {
		t.Fatalf("seed harga sertifikat: %v", err)
	}

	draft := buatDraftHTTP(t, f, cookiesA, csrfA, sertifikat, 1)
	id, _ := draft["id"].(string)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO payment_proofs (order_id, branch_id, storage_key, nama_file, mime_type, ukuran_byte, sha256, uploaded_by_kind)
		 VALUES ($1,(SELECT branch_id FROM orders WHERE id=$1), 'k', 'b.jpg', 'image/jpeg', 10, '\\x03', 'branch')`, id); err != nil {
		t.Fatalf("seed bukti: %v", err)
	}
	// Setelan biaya packaging berubah sebelum submit: nilai final harus 5000.
	setSetting(t, f, "biaya_packaging", "5000")

	resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit sertifikat harus 200, dapat %d (%v)", resp.StatusCode, body)
	}
	hasil := dataMap(t, body)
	if hasil["is_sertifikat"] != true || hasil["total"] != float64(5000) {
		t.Errorf("sertifikat: is_sertifikat=true total=5000, dapat %v %v", hasil["is_sertifikat"], hasil["total"])
	}
	lines, _ := hasil["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("harus 2 baris (produk + packaging), dapat %d", len(lines))
	}
	var produk, packaging map[string]any
	for _, l := range lines {
		baris := l.(map[string]any)
		if baris["jenis"] == "packaging" {
			packaging = baris
		} else {
			produk = baris
		}
	}
	if produk == nil || packaging == nil {
		t.Fatalf("baris tidak lengkap: %v", lines)
	}
	if produk["harga_satuan"] != float64(0) || produk["serial_number"] == "" {
		t.Errorf("baris sertifikat harus 0 berserial: %v", produk)
	}
	if packaging["harga_satuan"] != float64(5000) || packaging["qty"] != float64(1) {
		t.Errorf("baris packaging harus qty 1 @5000: %v", packaging)
	}
	if _, ada := packaging["serial_number"]; ada {
		t.Errorf("baris packaging tidak boleh berserial: %v", packaging)
	}

	var serialPackaging *string
	var hargaPackaging int64
	if err := f.pool.QueryRow(ctx,
		`SELECT serial_number, harga_satuan FROM order_lines WHERE order_id = $1 AND jenis = 'packaging'`, id).
		Scan(&serialPackaging, &hargaPackaging); err != nil {
		t.Fatalf("baca baris packaging: %v", err)
	}
	if serialPackaging != nil || hargaPackaging != 5000 {
		t.Errorf("packaging di database: serial=%v harga=%d", serialPackaging, hargaPackaging)
	}
}

// TestPGSubmit100ParalelNomorDanSerialUnik memenuhi kriteria penerimaan
// PRD §9(3): 100 submit paralel menghasilkan 100 nomor invoice dan serial yang
// unik, berurutan, dan tanpa bolong.
func TestPGSubmit100ParalelNomorDanSerialUnik(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")

	const jumlah = 100
	pesanan := make([]string, jumlah)
	for i := 0; i < jumlah; i++ {
		var id string
		err := f.pool.QueryRow(ctx,
			`INSERT INTO orders (branch_id, status, total) VALUES ($1, 'draft', 100000) RETURNING id`, f.branchA).Scan(&id)
		if err != nil {
			t.Fatalf("seed draft ke-%d: %v", i, err)
		}
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot, qty, harga_satuan)
			 VALUES ($1, 1, 'product', $2, 'Bio Magnetik', 1, 100000)`, id, produk); err != nil {
			t.Fatalf("seed baris ke-%d: %v", i, err)
		}
		// Bukti transfer unik per pesanan (submit non-C&C wajib punya bukti).
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO payment_proofs (order_id, branch_id, storage_key, nama_file, mime_type, ukuran_byte, sha256, uploaded_by_kind)
			 VALUES ($1,(SELECT branch_id FROM orders WHERE id=$1), 'k', 'b.jpg', 'image/jpeg', 10, digest($2::text, 'sha256'), 'branch')`, id, fmt.Sprintf("bukti-%d", i)); err != nil {
			t.Fatalf("seed bukti ke-%d: %v", i, err)
		}
		pesanan[i] = id
	}

	kode := make([]int, jumlah)
	nomor := make([]string, jumlah)
	var wg sync.WaitGroup
	for i := 0; i < jumlah; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kode[i], nomor[i] = submitParalel(f.srv.URL+"/api/v1/orders/"+pesanan[i]+"/submit", cookiesA, csrfA, "")
		}(i)
	}
	wg.Wait()

	_, periode, periodeSerial := wibHariIni(t, f)
	urut := make([]int, 0, jumlah)
	serialUrut := make([]int, 0, jumlah)
	pola := regexp.MustCompile(`^\d{4}-\d{2}-(\d+)$`)
	for i, k := range kode {
		if k != http.StatusOK {
			t.Errorf("submit ke-%d menjawab %d, mau 200", i, k)
			continue
		}
		cocok := pola.FindStringSubmatch(nomor[i])
		if cocok == nil || !strings.HasPrefix(nomor[i], periode) {
			t.Errorf("nomor ke-%d tidak sesuai format periode %s: %q", i, periode, nomor[i])
			continue
		}
		n, _ := strconv.Atoi(cocok[1])
		urut = append(urut, n)
	}
	if len(urut) != jumlah {
		t.Fatalf("harus %d submit berhasil, dapat %d", jumlah, len(urut))
	}
	sort.Ints(urut)
	for i, n := range urut {
		if n != i+1 {
			t.Fatalf("nomor invoice harus 1..%d tanpa bolong, urut ke-%d = %d", jumlah, i+1, n)
		}
	}

	// Serial: satu nomor per baris produk, juga tanpa bolong.
	rows, err := f.pool.Query(ctx,
		`SELECT l.serial_number FROM order_lines l JOIN orders o ON o.id = l.order_id
		  WHERE o.branch_id = $1 AND o.nomor IS NOT NULL AND l.jenis = 'product'`, f.branchA)
	if err != nil {
		t.Fatalf("baca serial: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan serial: %v", err)
		}
		if !strings.HasPrefix(s, periodeSerial) {
			t.Fatalf("serial %q harus berawalan periode hari %s", s, periodeSerial)
		}
		n, err := strconv.Atoi(strings.TrimPrefix(s, periodeSerial))
		if err != nil {
			t.Fatalf("serial %q tidak dapat diurai: %v", s, err)
		}
		serialUrut = append(serialUrut, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterasi serial: %v", err)
	}
	if len(serialUrut) != jumlah {
		t.Fatalf("harus %d serial, dapat %d", jumlah, len(serialUrut))
	}
	sort.Ints(serialUrut)
	for i, n := range serialUrut {
		if n != i+1 {
			t.Fatalf("serial harus 1..%d tanpa bolong, urut ke-%d = %d", jumlah, i+1, n)
		}
	}

	// Counter tabel adalah sumber nomor: nilai akhirnya tepat jumlah.
	if got := hitungCounter(t, f, "invoice", periode); got != jumlah {
		t.Errorf("counter invoice harus %d, dapat %d", jumlah, got)
	}
	if got := hitungCounter(t, f, "serial", periodeSerial); got != jumlah {
		t.Errorf("counter serial harus %d, dapat %d", jumlah, got)
	}
	// Tidak ada nomor duplikat di database.
	var distinctNomor int
	if err := f.pool.QueryRow(ctx, `SELECT count(DISTINCT nomor) FROM orders WHERE branch_id = $1`, f.branchA).Scan(&distinctNomor); err != nil {
		t.Fatalf("hitung nomor unik: %v", err)
	}
	if distinctNomor != jumlah {
		t.Errorf("nomor invoice harus %d nilai unik, dapat %d", jumlah, distinctNomor)
	}
}

// TestPGRollbackSubmitTidakMemakaiNomor memverifikasi BR-09 pada jalur nyata:
// submit yang gagal setelah validasi (produk tidak lagi dijual) tidak memakai
// nomor, tidak mengubah baris, dan tidak menulis timeline; submit berikutnya
// tetap memakai nomor pertama.
func TestPGRollbackSubmitTidakMemakaiNomor(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")

	draft := buatDraftHTTP(t, f, cookiesA, csrfA, produk, 3)
	id, _ := draft["id"].(string)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO payment_proofs (order_id, branch_id, storage_key, nama_file, mime_type, ukuran_byte, sha256, uploaded_by_kind)
		 VALUES ($1,(SELECT branch_id FROM orders WHERE id=$1), 'k', 'b.jpg', 'image/jpeg', 10, '\\x04', 'branch')`, id); err != nil {
		t.Fatalf("seed bukti: %v", err)
	}

	// Produk tidak lagi dijual ke cabang A: baris harga cabang dihapus.
	if _, err := f.pool.Exec(ctx,
		`DELETE FROM branch_prices WHERE branch_id = $1 AND product_id = $2`, f.branchA, produk); err != nil {
		t.Fatalf("hapus harga cabang: %v", err)
	}
	resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA, nil, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("produk tak dijual harus 400, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")

	var counter int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM numbering_counters`).Scan(&counter); err != nil {
		t.Fatalf("baca counter: %v", err)
	}
	if counter != 0 {
		t.Errorf("rollback tidak boleh menyisakan counter, baris=%d", counter)
	}
	var serial *string
	var harga int64
	var statusDB string
	var nomor *string
	if err := f.pool.QueryRow(ctx,
		`SELECT status::text, nomor FROM orders WHERE id = $1`, id).Scan(&statusDB, &nomor); err != nil {
		t.Fatalf("baca orders: %v", err)
	}
	if statusDB != "draft" || nomor != nil {
		t.Errorf("pesanan harus tetap draft tanpa nomor: %s %v", statusDB, nomor)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT serial_number, harga_satuan FROM order_lines WHERE order_id = $1`, id).Scan(&serial, &harga); err != nil {
		t.Fatalf("baca baris: %v", err)
	}
	if serial != nil || harga != 100000 {
		t.Errorf("baris tidak boleh berubah setelah rollback: serial=%v harga=%d", serial, harga)
	}
	var events int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1`, id).Scan(&events); err != nil {
		t.Fatalf("baca timeline: %v", err)
	}
	if events != 0 {
		t.Errorf("rollback tidak boleh menulis timeline, dapat %d", events)
	}

	// Pulihkan harga cabang: submit berikutnya memakai nomor PERTAMA (tanpa
	// bolong akibat percobaan yang gagal).
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO branch_prices (branch_id, product_id, harga) VALUES ($1, $2, 100000)`, f.branchA, produk); err != nil {
		t.Fatalf("pulihkan harga cabang: %v", err)
	}
	resp2, body2 := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA, nil, "")
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("submit setelah pulih harus 200, dapat %d (%v)", resp2.StatusCode, body2)
	}
	_, periode, _ := wibHariIni(t, f)
	if got := dataMap(t, body2)["nomor"]; got != periode+"-01" {
		t.Errorf("nomor pertama setelah rollback harus %s-01, dapat %v", periode, got)
	}
}

// TestPGSubmitBalapanSatuPesananSatuNomor menuntut dua submit bersamaan atas
// SATU pesanan hanya menghasilkan satu nomor: satu permintaan dijawab 200 dan
// yang lain 409 ORDER_LOCKED (penguncian FOR UPDATE + status diperiksa ulang).
func TestPGSubmitBalapanSatuPesananSatuNomor(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")
	draft := buatDraftHTTP(t, f, cookiesA, csrfA, produk, 1)
	id, _ := draft["id"].(string)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO payment_proofs (order_id, branch_id, storage_key, nama_file, mime_type, ukuran_byte, sha256, uploaded_by_kind)
		 VALUES ($1,(SELECT branch_id FROM orders WHERE id=$1), 'k', 'b.jpg', 'image/jpeg', 10, '\\x06', 'branch')`, id); err != nil {
		t.Fatalf("seed bukti: %v", err)
	}

	const percobaan = 4
	kode := make([]int, percobaan)
	nomor := make([]string, percobaan)
	var wg sync.WaitGroup
	for i := 0; i < percobaan; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kode[i], nomor[i] = submitParalel(f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA, "")
		}(i)
	}
	wg.Wait()

	sukses, ditolak := 0, 0
	var nomorSukses string
	for i, k := range kode {
		switch k {
		case http.StatusOK:
			sukses++
			nomorSukses = nomor[i]
		case http.StatusConflict:
			ditolak++
		default:
			t.Errorf("percobaan ke-%d menjawab %d, mau 200 atau 409", i, k)
		}
	}
	if sukses != 1 || ditolak != percobaan-1 {
		t.Fatalf("harus tepat 1 sukses dan %d ditolak, dapat %d/%d", percobaan-1, sukses, ditolak)
	}
	_, periode, _ := wibHariIni(t, f)
	if nomorSukses != periode+"-01" || hitungCounter(t, f, "invoice", periode) != 1 {
		t.Errorf("hanya satu nomor boleh terpakai: nomor=%q counter=%d", nomorSukses, hitungCounter(t, f, "invoice", periode))
	}
	var events int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id = $1`, id).Scan(&events); err != nil {
		t.Fatalf("baca timeline: %v", err)
	}
	if events != 1 {
		t.Errorf("timeline submit harus tercatat sekali, dapat %d", events)
	}
}

// TestPGSubmitProdukNonaktifDitolak memverifikasi validasi ulang master saat
// submit: produk yang dinonaktifkan setelah draft disimpan ditolak, dan pesanan
// tetap draft tanpa memakai nomor.
func TestPGSubmitProdukNonaktifDitolak(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")
	draft := buatDraftHTTP(t, f, cookiesA, csrfA, produk, 1)
	id, _ := draft["id"].(string)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO payment_proofs (order_id, branch_id, storage_key, nama_file, mime_type, ukuran_byte, sha256, uploaded_by_kind)
		 VALUES ($1,(SELECT branch_id FROM orders WHERE id=$1), 'k', 'b.jpg', 'image/jpeg', 10, '\\x07', 'branch')`, id); err != nil {
		t.Fatalf("seed bukti: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE products SET is_active = false WHERE id = $1`, produk); err != nil {
		t.Fatalf("nonaktifkan produk: %v", err)
	}

	resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA, nil, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("produk nonaktif harus 400, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
	var counter int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM numbering_counters`).Scan(&counter); err != nil {
		t.Fatalf("baca counter: %v", err)
	}
	if counter != 0 {
		t.Errorf("submit yang ditolak tidak boleh memakai nomor, counter=%d", counter)
	}
}

// TestPGSubmitDigitInvoiceMelebar memverifikasi BR-03 lewat fungsi database
// yang sama dengan transaksi submit: angka 10 tetap menjadi dua digit meski
// `digit_invoice` disetel 1 (nomor melebar, tidak gagal dan tidak bertabrakan).
func TestPGSubmitDigitInvoiceMelebar(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")
	setSetting(t, f, "digit_invoice", "1")

	_, periode, _ := wibHariIni(t, f)
	var terakhir string
	for i := 1; i <= 10; i++ {
		draft := buatDraftHTTP(t, f, cookiesA, csrfA, produk, 1)
		id, _ := draft["id"].(string)
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO payment_proofs (order_id, branch_id, storage_key, nama_file, mime_type, ukuran_byte, sha256, uploaded_by_kind)
			 VALUES ($1,(SELECT branch_id FROM orders WHERE id=$1), 'k', 'b.jpg', 'image/jpeg', 10, digest($2::text, 'sha256'), 'branch')`, id, fmt.Sprintf("melebar-%d", i)); err != nil {
			t.Fatalf("seed bukti ke-%d: %v", i, err)
		}
		resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA, nil, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("submit ke-%d harus 200, dapat %d (%v)", i, resp.StatusCode, body)
		}
		terakhir, _ = dataMap(t, body)["nomor"].(string)
	}
	if terakhir != periode+"-10" {
		t.Errorf("nomor ke-10 dengan digit_invoice=1 harus melebar %s-10, dapat %q", periode, terakhir)
	}
	if got := hitungCounter(t, f, "invoice", periode); got != 10 {
		t.Errorf("counter invoice harus 10, dapat %d", got)
	}
}

// TestPGSubmitIsolasiCabangDanAdmin memverifikasi submit tidak bisa dipakai
// cabang lain (404, bukan 403) dan bukan hak admin.
func TestPGSubmitIsolasiCabangDanAdmin(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	produk := produkKode(t, f, "P-01")
	draft := buatDraftHTTP(t, f, cookiesA, csrfA, produk, 1)
	id, _ := draft["id"].(string)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO payment_proofs (order_id, branch_id, storage_key, nama_file, mime_type, ukuran_byte, sha256, uploaded_by_kind)
		 VALUES ($1,(SELECT branch_id FROM orders WHERE id=$1), 'k', 'b.jpg', 'image/jpeg', 10, '\\x05', 'branch')`, id); err != nil {
		t.Fatalf("seed bukti: %v", err)
	}

	cookiesB, csrfB := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesB, csrfB, nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cabang lain harus 404, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "NOT_FOUND")

	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	respSA, bodySA := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesSA, csrfSA, nil, "")
	if respSA.StatusCode != http.StatusForbidden {
		t.Fatalf("admin harus 403 pada submit, dapat %d (%v)", respSA.StatusCode, bodySA)
	}

	var statusDB string
	var nomor *string
	if err := f.pool.QueryRow(ctx, `SELECT status::text, nomor FROM orders WHERE id = $1`, id).Scan(&statusDB, &nomor); err != nil {
		t.Fatalf("baca orders: %v", err)
	}
	if statusDB != "draft" || nomor != nil {
		t.Errorf("pesanan harus tetap draft: %s %v", statusDB, nomor)
	}

	// Pemiliknya tetap bisa submit setelah percobaan orang lain.
	respA, bodyA := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookiesA, csrfA, nil, "")
	if respA.StatusCode != http.StatusOK {
		t.Fatalf("pemilik harus bisa submit, dapat %d (%v)", respA.StatusCode, bodyA)
	}
}
