package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/order"
	orderpg "github.com/grahita/portal/api/internal/order/postgres"
	"github.com/grahita/portal/api/internal/receivable"
)

type configRevisiRusak struct{}

func (configRevisiRusak) Bool(context.Context, string) (bool, error) {
	return false, errors.New("config sengaja tidak tersedia")
}

func (configRevisiRusak) Int(context.Context, string) (int64, error) {
	return 0, errors.New("config sengaja tidak tersedia")
}

type hasilRevisiKonkuren struct {
	status int
	body   map[string]any
	err    error
}

// revisiKonkuren sengaja tidak memakai helper yang memanggil t.Fatal supaya
// aman dijalankan dari goroutine. Kedua request dilepas oleh barrier yang
// sama untuk benar-benar menguji serialisasi transaksi, bukan dua panggilan
// berurutan.
func revisiKonkuren(url, body string, cookies []*http.Cookie, csrf, key string, mulai <-chan struct{}) hasilRevisiKonkuren {
	<-mulai
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		return hasilRevisiKonkuren{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Idempotency-Key", key)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return hasilRevisiKonkuren{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return hasilRevisiKonkuren{status: resp.StatusCode, err: err}
	}
	return hasilRevisiKonkuren{status: resp.StatusCode, body: decoded}
}

func duaRevisiBersamaan(t *testing.T, url, body string, cookies []*http.Cookie, csrf, key string) [2]hasilRevisiKonkuren {
	t.Helper()
	mulai := make(chan struct{})
	hasil := make(chan hasilRevisiKonkuren, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hasil <- revisiKonkuren(url, body, cookies, csrf, key, mulai)
		}()
	}
	close(mulai)
	wg.Wait()
	close(hasil)
	var out [2]hasilRevisiKonkuren
	i := 0
	for item := range hasil {
		out[i] = item
		i++
	}
	return out
}

func pastikanSatuRevisiDanSatuReplay(t *testing.T, hasil [2]hasilRevisiKonkuren) {
	t.Helper()
	replayed := 0
	var versi any
	for i, item := range hasil {
		if item.err != nil || item.status != http.StatusOK {
			t.Fatalf("request konkuren %d gagal: status=%d err=%v body=%v", i+1, item.status, item.err, item.body)
		}
		data := dataMap(t, item.body)
		if data["replayed"] == true {
			replayed++
		}
		if versi == nil {
			versi = data["versi"]
		} else if versi != data["versi"] {
			t.Fatalf("hasil konkuren tidak menunjuk versi sama: %+v", hasil)
		}
	}
	if replayed != 1 {
		t.Fatalf("harus tepat satu eksekusi dan satu replay, replay=%d hasil=%+v", replayed, hasil)
	}
}

// Retry dengan key dan payload identik harus mengembalikan hasil revisi
// pertama walau invoice versi barunya belum selesai dirender. Payload berbeda
// dengan key yang sama harus ditolak tanpa side effect tambahan.
func TestPGRevisiIdempotencyMendahuluiPenjagaRender(t *testing.T) {
	f, id, _, cookies, csrf := jalurUtamaRevisi(t)
	ctx := context.Background()
	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("render dokumen prasyarat: %v", err)
	}
	body := reviseBody("retry revisi identik",
		`{"line_no":1,"nama_instansi":"Instansi Retry","qty":2,"harga_satuan":100000},`+
			`{"line_no":2,"nama_instansi":"Instansi Retry","qty":1,"harga_satuan":50000}`)
	url := f.srv.URL + "/api/v1/admin/orders/" + id + "/revise"

	status1, first := depositReq(t, http.MethodPost, url, body, cookies, csrf, "revision-retry-key")
	if status1 != http.StatusOK {
		t.Fatalf("revisi pertama harus 200, dapat %d (%v)", status1, first)
	}
	status2, replay := depositReq(t, http.MethodPost, url, body, cookies, csrf, "revision-retry-key")
	if status2 != http.StatusOK {
		t.Fatalf("retry sebelum render harus replay 200, dapat %d (%v)", status2, replay)
	}
	data1, data2 := dataMap(t, first), dataMap(t, replay)
	if data1["replayed"] != false || data2["replayed"] != true || data1["versi"] != data2["versi"] || data1["revisi"] == nil {
		t.Fatalf("hasil replay tidak sama: first=%v replay=%v", data1, data2)
	}
	bedaKet := reviseBody("retry revisi identik",
		`{"line_no":1,"nama_instansi":"Instansi Retry","keterangan":"berbeda","qty":2,"harga_satuan":100000},`+
			`{"line_no":2,"nama_instansi":"Instansi Retry","qty":1,"harga_satuan":50000}`)
	statusKet, conflictKet := depositReq(t, http.MethodPost, url, bedaKet, cookies, csrf, "revision-retry-key")
	if statusKet != http.StatusConflict || errCode(conflictKet) != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("key sama dengan payload hanya beda Ket harus konflik: %d %v", statusKet, conflictKet)
	}
	// Replay committed tetap tersedia ketika queue dan config sudah tidak
	// tersedia. Repo baru sengaja tidak dipasangi job queue; config selalu error.
	instansi := "Instansi Retry"
	tanpaQueue := order.NewRevisionService(orderpg.New(f.pool), configRevisiRusak{})
	serviceReplay, err := tanpaQueue.Revise(ctx,
		auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"},
		strings.ToUpper(id), order.ReviseInput{
			Alasan: "retry revisi identik", IdempotencyKey: "revision-retry-key",
			Lines: []order.ReviseLineInput{
				{LineNo: 1, NamaInstansi: &instansi, Qty: 2, HargaSatuan: 100000},
				{LineNo: 2, NamaInstansi: &instansi, Qty: 1, HargaSatuan: 50000},
			},
		})
	if err != nil || !serviceReplay.Replayed || serviceReplay.Versi != 1 {
		t.Fatalf("replay harus bebas queue/config: out=%+v err=%v", serviceReplay, err)
	}
	statusUpper, upper := depositReq(t, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+strings.ToUpper(id)+"/revise",
		body, cookies, csrf, "revision-retry-key")
	if statusUpper != http.StatusOK || dataMap(t, upper)["replayed"] != true {
		t.Fatalf("retry UUID uppercase harus replay transaksi yang sama: %d %v", statusUpper, upper)
	}

	beda := reviseBody("payload berbeda",
		`{"line_no":1,"nama_instansi":"Instansi Retry","qty":1,"harga_satuan":100000},`+
			`{"line_no":2,"nama_instansi":"Instansi Retry","qty":1,"harga_satuan":50000}`)
	status3, conflict := depositReq(t, http.MethodPost, url, beda, cookies, csrf, "revision-retry-key")
	errBody, _ := conflict["error"].(map[string]any)
	if status3 != http.StatusConflict || errBody["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("key sama payload beda harus 409 IDEMPOTENCY_KEY_REUSED, dapat %d (%v)", status3, conflict)
	}

	var revisions, events, audits, jobs int
	if err := f.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM invoice_revisions WHERE order_id=$1),
 (SELECT count(*) FROM order_events WHERE order_id=$1 AND icon='revisi'),
 (SELECT count(*) FROM audit_logs WHERE entity_id=$1 AND action='revise'),
 (SELECT count(*) FROM river_job WHERE kind='revisi_invoice' AND args->>'order_id'=$1::text)`, id).
		Scan(&revisions, &events, &audits, &jobs); err != nil {
		t.Fatalf("hitung side effect revisi: %v", err)
	}
	if revisions != 1 || events != 1 || audits != 1 || jobs != 1 {
		t.Fatalf("retry menggandakan side effect: revisions=%d events=%d audits=%d jobs=%d",
			revisions, events, audits, jobs)
	}

	// Sesudah worker membuat invoice versi baru, retry tetap harus mengembalikan
	// hasil transaksi pertama dan tidak membuat versi/side effect berikutnya.
	if err := jalankanPekerjaanRevisi(t, f, id, 1); err != nil {
		t.Fatalf("render invoice revisi: %v", err)
	}
	status4, afterRender := depositReq(t, http.MethodPost, url, body, cookies, csrf, "revision-retry-key")
	if status4 != http.StatusOK || dataMap(t, afterRender)["replayed"] != true {
		t.Fatalf("retry sesudah render harus replay 200: %d %v", status4, afterRender)
	}
	var versi int
	if err := f.pool.QueryRow(ctx, `SELECT versi FROM orders WHERE id=$1`, id).Scan(&versi); err != nil {
		t.Fatal(err)
	}
	if versi != 1 {
		t.Fatalf("retry sesudah render menaikkan versi menjadi %d", versi)
	}
}

func TestPGRevisiIdempotencyWajibDanScope(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	produk := produkKode(t, f, "P-01")
	tanggal, periode := tanggalDanPeriode(3)
	id1, _ := seedPesananDone(t, f, f.branchA, "2026-10-996", tanggal, []barisDone{{
		lineNo: 1, product: produk, nama: "Bio Magnetik", qty: 1, harga: 100000, serial: periode + "996",
	}})
	id2, _ := seedPesananDone(t, f, f.branchA, "2026-10-997", tanggal, []barisDone{{
		lineNo: 1, product: produk, nama: "Bio Magnetik", qty: 1, harga: 100000, serial: periode + "997",
	}})
	if err := jalankanPekerjaanDone(t, f, id1); err != nil {
		t.Fatal(err)
	}
	if err := jalankanPekerjaanDone(t, f, id2); err != nil {
		t.Fatal(err)
	}
	body := reviseBody("scope idempotensi", `{"line_no":1,"nama_instansi":"Scope","qty":1,"harga_satuan":100000}`)
	saCookies, saCSRF := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	url1 := f.srv.URL + "/api/v1/admin/orders/" + id1 + "/revise"
	url2 := f.srv.URL + "/api/v1/admin/orders/" + id2 + "/revise"

	statusMissing, missing, _ := apiReq(t, http.DefaultClient, http.MethodPost, url1, body, saCookies, saCSRF)
	if statusMissing != http.StatusBadRequest || errCode(missing) != "VALIDATION_FAILED" {
		t.Fatalf("semua revisi tanpa key harus 400: %d %v", statusMissing, missing)
	}
	missingErr, _ := missing["error"].(map[string]any)
	if missingErr["message"] != "Permintaan revisi tidak valid." {
		t.Fatalf("pesan validasi revisi tidak boleh menyebut deposit: %v", missing)
	}
	for _, invalidKey := range []string{"x", strings.Repeat("x", 7), strings.Repeat("x", 201)} {
		statusInvalid, invalid := depositReq(t, http.MethodPost, url1, body, saCookies, saCSRF, invalidKey)
		if statusInvalid != http.StatusBadRequest || errCode(invalid) != "VALIDATION_FAILED" {
			t.Fatalf("key panjang %d harus 400: %d %v", len(invalidKey), statusInvalid, invalid)
		}
	}
	const sharedKey = "12345678"
	status1, first := depositReq(t, http.MethodPost, url1, body, saCookies, saCSRF, sharedKey)
	if status1 != http.StatusOK {
		t.Fatalf("revisi scope pertama: %d %v", status1, first)
	}
	if dataMap(t, first)["replayed"] != false {
		t.Fatalf("respons pertama wajib memuat replayed=false: %v", first)
	}
	statusCrossOrder, crossOrder := depositReq(t, http.MethodPost, url2, body, saCookies, saCSRF, sharedKey)
	if statusCrossOrder != http.StatusConflict || errCode(crossOrder) != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("key admin sama lintas order harus konflik: %d %v", statusCrossOrder, crossOrder)
	}
	statusMax, maxKey := depositReq(t, http.MethodPost, url2, body, saCookies, saCSRF, strings.Repeat("m", 200))
	if statusMax != http.StatusOK || dataMap(t, maxKey)["replayed"] != false {
		t.Fatalf("key 200 karakter harus diterima: %d %v", statusMax, maxKey)
	}

	// Scope key per admin: sesudah invoice v1 siap, Manager boleh memakai text
	// key yang sama sebagai niat miliknya sendiri dan menghasilkan versi v2.
	if err := jalankanPekerjaanRevisi(t, f, id1, 1); err != nil {
		t.Fatal(err)
	}
	setSetting(t, f, "manager_boleh_revisi", "true")
	mgrCookies, mgrCSRF := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	statusAdminLain, adminLain := depositReq(t, http.MethodPost, url1, body, mgrCookies, mgrCSRF, sharedKey)
	if statusAdminLain != http.StatusOK || dataMap(t, adminLain)["versi"] != float64(2) {
		t.Fatalf("key sama lintas admin harus independen: %d %v", statusAdminLain, adminLain)
	}
	var versi, revisions int
	if err := f.pool.QueryRow(ctx, `SELECT versi,(SELECT count(*) FROM invoice_revisions WHERE order_id=orders.id) FROM orders WHERE id=$1`, id1).
		Scan(&versi, &revisions); err != nil {
		t.Fatal(err)
	}
	if versi != 2 || revisions != 2 {
		t.Fatalf("scope admin menghasilkan keadaan salah: versi=%d revisions=%d", versi, revisions)
	}
}

// Kegagalan sebelum transaksi ekonomi berhasil tidak boleh "memakan" key.
// Klien dapat mengulang key yang sama setelah invoice awal selesai dirender.
func TestPGRevisiIdempotencyRetrySetelahInvoiceAwalSiap(t *testing.T) {
	f, id, _, cookies, csrf := jalurUtamaRevisi(t)
	body := reviseBody("retry setelah invoice siap",
		`{"line_no":1,"nama_instansi":"Instansi Siap","qty":2,"harga_satuan":100000},`+
			`{"line_no":2,"nama_instansi":"Instansi Siap","qty":1,"harga_satuan":50000}`)
	url := f.srv.URL + "/api/v1/admin/orders/" + id + "/revise"
	statusBelum, belum := depositReq(t, http.MethodPost, url, body, cookies, csrf, "revision-wait-render")
	errBody, _ := belum["error"].(map[string]any)
	if statusBelum != http.StatusConflict || errBody["code"] != "INVOICE_NOT_READY" {
		t.Fatalf("sebelum invoice siap harus 409 INVOICE_NOT_READY: %d %v", statusBelum, belum)
	}
	var requests int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM invoice_revision_requests WHERE order_id=$1`, id).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("request gagal tidak boleh menyimpan tombstone idempotensi: %d", requests)
	}
	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("render invoice awal: %v", err)
	}
	statusSiap, siap := depositReq(t, http.MethodPost, url, body, cookies, csrf, "revision-wait-render")
	if statusSiap != http.StatusOK || dataMap(t, siap)["versi"] != float64(1) {
		t.Fatalf("retry key sama setelah invoice siap harus berhasil sekali: %d %v", statusSiap, siap)
	}
}

func TestPGRevisiIdempotencyKonkurenNormal(t *testing.T) {
	f, id, _, cookies, csrf := jalurUtamaRevisi(t)
	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("render dokumen prasyarat: %v", err)
	}
	body := reviseBody("revisi normal konkuren",
		`{"line_no":1,"nama_instansi":"Instansi Konkuren","qty":2,"harga_satuan":100000},`+
			`{"line_no":2,"nama_instansi":"Instansi Konkuren","qty":1,"harga_satuan":50000}`)
	hasil := duaRevisiBersamaan(t, f.srv.URL+"/api/v1/admin/orders/"+id+"/revise", body,
		cookies, csrf, "revision-concurrent-normal")
	pastikanSatuRevisiDanSatuReplay(t, hasil)

	var versi, revisions, events, audits, jobs int
	if err := f.pool.QueryRow(t.Context(), `SELECT
 o.versi,
 (SELECT count(*) FROM invoice_revisions WHERE order_id=o.id),
 (SELECT count(*) FROM order_events WHERE order_id=o.id AND icon='revisi'),
 (SELECT count(*) FROM audit_logs WHERE entity_id=o.id AND action='revise'),
 (SELECT count(*) FROM river_job WHERE kind='revisi_invoice' AND args->>'order_id'=o.id::text)
 FROM orders o WHERE o.id=$1`, id).Scan(&versi, &revisions, &events, &audits, &jobs); err != nil {
		t.Fatal(err)
	}
	if versi != 1 || revisions != 1 || events != 1 || audits != 1 || jobs != 1 {
		t.Fatalf("konkurensi menggandakan side effect: versi=%d revisions=%d events=%d audits=%d jobs=%d",
			versi, revisions, events, audits, jobs)
	}
}

func TestPGRevisiIdempotencyTempoReplayKonflikDanKonkuren(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	id := tempoSubmitted(t, f, "2026-10-20", 100000)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	if _, err := receivable.New(f.pool, f.jobs).Approve(ctx, manager, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.selesai.PesananSelesai(ctx, id); err != nil {
		t.Fatalf("render invoice awal Tempo: %v", err)
	}
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	body := reviseBody("revisi Tempo konkuren", `{"line_no":1,"nama_instansi":"Tempo Konkuren","qty":2,"harga_satuan":100000}`)
	url := f.srv.URL + "/api/v1/admin/orders/" + id + "/revise"
	hasil := duaRevisiBersamaan(t, url, body, cookies, csrf, "revision-concurrent-tempo")
	pastikanSatuRevisiDanSatuReplay(t, hasil)

	statusReplay, replay := depositReq(t, http.MethodPost, url, body, cookies, csrf, "revision-concurrent-tempo")
	if statusReplay != http.StatusOK || dataMap(t, replay)["replayed"] != true {
		t.Fatalf("retry Tempo sebelum render harus replay 200: %d %v", statusReplay, replay)
	}
	beda := reviseBody("revisi Tempo payload beda", `{"line_no":1,"nama_instansi":"Tempo Konkuren","qty":3,"harga_satuan":100000}`)
	statusBeda, conflict := depositReq(t, http.MethodPost, url, beda, cookies, csrf, "revision-concurrent-tempo")
	errBody, _ := conflict["error"].(map[string]any)
	if statusBeda != http.StatusConflict || errBody["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("key Tempo sama payload beda harus konflik: %d %v", statusBeda, conflict)
	}

	var versi, revisions, adjustments, charges, events, audits, jobs int
	var total int64
	if err := f.pool.QueryRow(ctx, `SELECT
 o.versi,o.total,
 (SELECT count(*) FROM invoice_revisions WHERE order_id=o.id),
 (SELECT count(*) FROM tempo_adjustments WHERE order_id=o.id),
 (SELECT count(*) FROM tempo_charges WHERE order_id=o.id),
 (SELECT count(*) FROM order_events WHERE order_id=o.id AND icon='revisi'),
 (SELECT count(*) FROM audit_logs WHERE entity_id=o.id AND action='revise'),
 (SELECT count(*) FROM river_job WHERE kind='revisi_invoice' AND args->>'order_id'=o.id::text)
 FROM orders o WHERE o.id=$1`, id).
		Scan(&versi, &total, &revisions, &adjustments, &charges, &events, &audits, &jobs); err != nil {
		t.Fatal(err)
	}
	// Satu charge berasal dari approval awal dan satu dari delta revisi.
	if versi != 1 || total != 200000 || revisions != 1 || adjustments != 1 || charges != 2 || events != 1 || audits != 1 || jobs != 1 {
		t.Fatalf("side effect Tempo tidak tunggal: versi=%d total=%d revisions=%d adjustments=%d charges=%d events=%d audits=%d jobs=%d",
			versi, total, revisions, adjustments, charges, events, audits, jobs)
	}
	if err := jalankanPekerjaanRevisi(t, f, id, 1); err != nil {
		t.Fatalf("render revisi Tempo: %v", err)
	}
	statusAfter, after := depositReq(t, http.MethodPost, url, body, cookies, csrf, "revision-concurrent-tempo")
	if statusAfter != http.StatusOK || dataMap(t, after)["replayed"] != true {
		t.Fatalf("retry Tempo sesudah render harus replay 200: %d %v", statusAfter, after)
	}
}
