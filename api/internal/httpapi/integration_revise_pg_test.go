package httpapi

// Uji integrasi revisi invoice berversi dan audit perubahannya (kartu F2-25)
// pada PostgreSQL nyata.
//
// Yang dibuktikan berkas ini adalah perilaku yang hanya ada di database dan
// rantai antar-domain:
//
//   - SATU transaksi revisi menaikkan versi (nomor tetap), menyimpan snapshot
//     versi lama + delta nilai di `invoice_revisions`, menulis timeline +
//     audit, dan MENITIPKAN pekerjaan invoice versi baru pada tabel river_job;
//   - berkas versi lama TIDAK pernah ditimpa; invoice versi baru dirender
//     terpisah dan email invoice dikirim ulang (Surat Jalan tidak);
//   - serial baris lama tidak berubah; baris produk baru mendapat serial baru
//     bertanggal pesanan ASLI;
//   - dua revisi bersamaan berbaris rapi (baris pesanan dikunci);
//   - Manager ditolak saat `manager_boleh_revisi` nonaktif (ditegakkan API);
//   - rollback TIDAK menyisakan nomor serial, versi, timeline, audit, atau job.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/grahita/portal/api/internal/document"
	"github.com/grahita/portal/api/internal/fulfillment"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/platform/pdf"
	"github.com/grahita/portal/api/internal/settings"
	"github.com/grahita/portal/api/internal/worker"
)

// barisDone adalah satu baris pesanan Done yang disemai uji.
type barisDone struct {
	lineNo    int
	product   string
	nama      string
	qty       int
	harga     int64
	serial    string
	packaging bool
}

// seedPesananDone menyemai satu pesanan berstatus Done versi 0 lengkap dengan
// barisnya (dan serial number tetap), supaya revisi punya invoice untuk
// direvisi. Nomor/tanggal dipilih uji supaya periode serial dapat diprediksi.
func seedPesananDone(t *testing.T, f *pgFixture, branchID, nomor, tanggal string, baris []barisDone) (string, int64) {
	t.Helper()
	ctx := context.Background()

	if _, err := f.pool.Exec(ctx,
		`INSERT INTO bank_accounts (bank, nama_pemilik, nomor, is_active)
		 VALUES ('BCA','PT Grahita Indonesia','1234567890',true)
		 ON CONFLICT (bank, nomor) DO NOTHING`); err != nil {
		t.Fatalf("seed rekening: %v", err)
	}

	var total int64
	for _, b := range baris {
		total += int64(b.qty) * b.harga
	}
	var id string
	err := f.pool.QueryRow(ctx,
		`INSERT INTO orders (branch_id, nomor, versi, status, verif_status, tanggal, total,
		                     done_at, submitted_at)
		 VALUES ($1,$2,0,'done','cocok',$3::date,$4,now(),now()) RETURNING id`,
		branchID, nomor, tanggal, total).Scan(&id)
	if err != nil {
		t.Fatalf("seed pesanan done %s: %v", nomor, err)
	}
	for _, b := range baris {
		if b.packaging {
			if _, err := f.pool.Exec(ctx,
				`INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot,
				                          satuan_snapshot, qty, harga_satuan)
				 VALUES ($1,$2,'packaging',NULL,$3,'paket',1,$4)`,
				id, b.lineNo, b.nama, b.harga); err != nil {
				t.Fatalf("seed baris packaging %s: %v", nomor, err)
			}
			continue
		}
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot,
			                          satuan_snapshot, qty, harga_satuan, serial_number)
			 VALUES ($1,$2,'product',$3,$4,'pcs',$5,$6,$7)`,
			id, b.lineNo, b.product, b.nama, b.qty, b.harga, b.serial); err != nil {
			t.Fatalf("seed baris pesanan %s: %v", nomor, err)
		}
	}
	// Satu entri timeline dasar supaya uji dapat membuktikan revisi MENAMBAH
	// entri (dan rollback tidak menambah apa pun).
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO order_events (order_id, icon, teks, actor_kind)
		 VALUES ($1,'selesai','Pembayaran diverifikasi dan pesanan selesai','admin')`, id); err != nil {
		t.Fatalf("seed timeline %s: %v", nomor, err)
	}
	return id, total
}

// reviseBody menyusun body JSON permintaan revisi dari potongan baris mentah.
func reviseBody(alasan, lines string) string {
	return `{"alasan":"` + alasan + `","lines":[` + lines + `]}`
}

// reviseHTTP mengirim permintaan revisi dan mengembalikan status + body.
func reviseHTTP(t *testing.T, f *pgFixture, cookies []*http.Cookie, csrf, id, body string) (int, map[string]any) {
	t.Helper()
	status, decoded, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+id+"/revise", body, cookies, csrf)
	return status, decoded
}

// jalankanPekerjaanRevisi menjalankan pekerjaan worker "revisi_invoice" untuk
// satu pesanan — peran worker River di produksi.
func jalankanPekerjaanRevisi(t *testing.T, f *pgFixture, orderID string) error {
	t.Helper()
	w := worker.NewRevisiInvoiceWorker(f.selesai)
	job := &river.Job[jobs.RevisiInvoiceArgs]{
		JobRow: &rivertype.JobRow{ID: 1},
		Args:   jobs.RevisiInvoiceArgs{OrderID: orderID},
	}
	return w.Work(context.Background(), job)
}

// dokumenPesanans mengembalikan kunci storage per (jenis, versi) milik pesanan.
func dokumenPesanans(t *testing.T, f *pgFixture, orderID string) map[string]string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT jenis::text, versi, storage_key FROM documents WHERE order_id = $1`, orderID)
	if err != nil {
		t.Fatalf("baca dokumen: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var jenis, key string
		var versi int
		if err := rows.Scan(&jenis, &versi, &key); err != nil {
			t.Fatalf("scan dokumen: %v", err)
		}
		out[fmt.Sprintf("%s-%d", jenis, versi)] = key
	}
	return out
}

// isiObjek membaca isi satu objek storage sebagai teks.
func isiObjek(t *testing.T, f *pgFixture, key string) string {
	t.Helper()
	rc, err := f.objek.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("baca objek %s: %v", key, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("baca isi objek %s: %v", key, err)
	}
	return string(b)
}

// hitungEmail menghitung baris email_outbox satu pesanan + jenis + versi.
func hitungEmail(t *testing.T, f *pgFixture, orderID, jenis string, versi int) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM email_outbox
		  WHERE order_id = $1::uuid AND jenis = $2::email_kind
		    AND COALESCE((payload->>'versi')::int, 0) = $3`, orderID, jenis, versi).Scan(&n); err != nil {
		t.Fatalf("hitung email %s v%d: %v", jenis, versi, err)
	}
	return n
}

// hitungJobRevisi menghitung job revisi_invoice milik satu pesanan. Tabel
// river_job di skema uji bisa dipakai bersama uji lain, jadi hitungannya
// SELALU dipersempit ke id pesanan.
func hitungJobRevisi(t *testing.T, f *pgFixture, orderID string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'order_id' = $2`,
		jobs.KindRevisiInvoice, orderID).Scan(&n); err != nil {
		t.Fatalf("hitung job revisi: %v", err)
	}
	return n
}

// hitungTimeline menghitung entri timeline satu pesanan.
func hitungTimeline(t *testing.T, f *pgFixture, orderID string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM order_events WHERE order_id = $1`, orderID).Scan(&n); err != nil {
		t.Fatalf("hitung timeline: %v", err)
	}
	return n
}

// counterSerial membaca last_value counter serial satu periode (0 bila belum ada).
func counterSerial(t *testing.T, f *pgFixture, periode string) int {
	t.Helper()
	var v int
	err := f.pool.QueryRow(context.Background(),
		`SELECT COALESCE((SELECT last_value FROM numbering_counters
		                   WHERE scope = 'serial' AND periode = $1), 0)`, periode).Scan(&v)
	if err != nil {
		t.Fatalf("baca counter serial %s: %v", periode, err)
	}
	return v
}

// serialBaris membaca serial satu baris pesanan.
func serialBaris(t *testing.T, f *pgFixture, orderID string, lineNo int) string {
	t.Helper()
	var s *string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT serial_number FROM order_lines WHERE order_id = $1 AND line_no = $2`,
		orderID, lineNo).Scan(&s); err != nil {
		t.Fatalf("baca serial baris %d: %v", lineNo, err)
	}
	if s == nil {
		return ""
	}
	return *s
}

// versiPesanan membaca kolom versi + total satu pesanan.
func versiPesanan(t *testing.T, f *pgFixture, orderID string) (int, int64) {
	t.Helper()
	var versi int
	var total int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT versi, total FROM orders WHERE id = $1`, orderID).Scan(&versi, &total); err != nil {
		t.Fatalf("baca versi pesanan: %v", err)
	}
	return versi, total
}

// tanggalDanPeriode mengembalikan tanggal bisnis (WIB) N hari lalu beserta
// periode serialnya (YYYYMMDD) — dipakai menyemai serial yang dapat diprediksi.
func tanggalDanPeriode(daysAgo int) (string, string) {
	d := time.Now().In(jakartaTZ()).AddDate(0, 0, -daysAgo).Format("2006-01-02")
	return d, strings.ReplaceAll(d, "-", "")
}

// jalurUtamaRevisi menyiapkan fixture + pesanan Done + sesi Superadmin untuk
// sebagian besar uji di berkas ini.
func jalurUtamaRevisi(t *testing.T) (*pgFixture, string, int64, []*http.Cookie, string) {
	t.Helper()
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx,
		`UPDATE admin_users SET notif_invoice = true, notif_surat_jalan = true WHERE id = $1`, f.adminSA); err != nil {
		t.Fatalf("pasang flag email admin: %v", err)
	}
	produk := produkKode(t, f, "P-01")
	tanggal, periode := tanggalDanPeriode(3)
	id, total := seedPesananDone(t, f, f.branchA, "2026-10-900", tanggal, []barisDone{
		{lineNo: 1, product: produk, nama: "Bio Magnetik", qty: 2, harga: 100000, serial: periode + "901"},
		{lineNo: 2, product: produk, nama: "Bio Magnetik B", qty: 1, harga: 50000, serial: periode + "902"},
	})
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	return f, id, total, cookies, csrf
}

// TestPGRevisiMenaikkanVersiSimpanSnapshotDanInvoiceBaru menjalankan alur
// utama: revisi -> versi +1 (nomor tetap) -> snapshot versi lama + delta ->
// timeline + audit + job -> invoice versi baru terunduh, versi lama utuh, dan
// email invoice dikirim ulang tanpa Surat Jalan.
func TestPGRevisiMenaikkanVersiSimpanSnapshotDanInvoiceBaru(t *testing.T) {
	f, id, totalLama, cookies, csrf := jalurUtamaRevisi(t)
	ctx := context.Background()
	_, periode := tanggalDanPeriode(3)

	// Dokumen versi 0 (pesanan Done) dirender lebih dulu — peran worker.
	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("pekerjaan Done: %v", err)
	}
	docAwal := dokumenPesanans(t, f, id)
	if _, ada := docAwal["invoice_pdf-0"]; !ada {
		t.Fatalf("invoice versi 0 harus ada sebelum revisi: %v", docAwal)
	}
	htmlInvoiceV0 := isiObjek(t, f, docAwal["invoice_pdf-0"])
	if strings.Contains(htmlInvoiceV0, "Revisi v") {
		t.Errorf("invoice versi 0 tidak boleh mencantumkan versi revisi")
	}

	// Revisi: baris 1 dinaikkan qty-nya, harga baris 1 naik, baris 2 tetap,
	// ditambah satu baris produk BARU.
	produk2 := insertProduct(t, ctx, f.pool, "P-99", "Kertas HVS", 7000)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO branch_prices (branch_id, product_id, harga) VALUES ($1,$2,$3)`,
		f.branchA, produk2, 7000); err != nil {
		t.Fatalf("seed harga produk baru: %v", err)
	}
	body := reviseBody("harga baris 1 salah input", `{"line_no":1,"qty":3,"harga_satuan":120000},`+
		`{"line_no":2,"qty":1,"harga_satuan":50000},`+
		`{"product_id":"`+produk2+`","qty":4,"harga_satuan":7000}`)
	status, hasil := reviseHTTP(t, f, cookies, csrf, id, body)
	if status != http.StatusOK {
		t.Fatalf("revisi harus 200, dapat %d (%v)", status, hasil)
	}
	data := dataMap(t, hasil)
	if data["versi"] != float64(1) {
		t.Errorf("versi harus 1, dapat %v", data["versi"])
	}
	if data["nomor"] != "2026-10-900" {
		t.Errorf("nomor invoice harus tetap, dapat %v", data["nomor"])
	}
	// Total baru = 3*120000 + 1*50000 + 4*7000 = 438000.
	totalBaru := int64(438000)
	if data["total"] != float64(totalBaru) {
		t.Errorf("total versi baru harus %d, dapat %v", totalBaru, data["total"])
	}
	if data["delta_total"] != float64(totalBaru-totalLama) {
		t.Errorf("delta harus %d, dapat %v", totalBaru-totalLama, data["delta_total"])
	}

	versi, totalDB := versiPesanan(t, f, id)
	if versi != 1 || totalDB != totalBaru {
		t.Errorf("DB harus versi 1 total %d, dapat versi %d total %d", totalBaru, versi, totalDB)
	}

	// Snapshot versi lama + delta tersimpan.
	var (
		versiLama, versiBaru int
		alasan               string
		delta                int64
		snapshot             string
	)
	err := f.pool.QueryRow(ctx,
		`SELECT versi_lama, versi_baru, alasan, delta_total, snapshot::text
		   FROM invoice_revisions WHERE order_id = $1`, id).
		Scan(&versiLama, &versiBaru, &alasan, &delta, &snapshot)
	if err != nil {
		t.Fatalf("baca invoice_revisions: %v", err)
	}
	if versiLama != 0 || versiBaru != 1 || alasan != "harga baris 1 salah input" || delta != totalBaru-totalLama {
		t.Errorf("baris revisi tidak sesuai: lama=%d baru=%d alasan=%q delta=%d", versiLama, versiBaru, alasan, delta)
	}
	if !strings.Contains(snapshot, `"total": `+fmt.Sprint(totalLama)) {
		t.Errorf("snapshot versi lama harus memuat total lama %d: %s", totalLama, snapshot)
	}

	// Serial baris lama tidak berubah; baris baru berserial periode TANGGAL ASLI.
	if got := serialBaris(t, f, id, 1); got != periode+"901" {
		t.Errorf("serial baris 1 harus tetap %s, dapat %s", periode+"901", got)
	}
	serialBaru := serialBaris(t, f, id, 3)
	if !strings.HasPrefix(serialBaru, periode) {
		t.Errorf("serial baris baru harus bertanggal pesanan asli %s, dapat %s", periode, serialBaru)
	}

	// Timeline + audit mencatat aktor/alasan/delta.
	var teks string
	if err := f.pool.QueryRow(ctx,
		`SELECT teks FROM order_events WHERE order_id = $1 ORDER BY at DESC, id DESC LIMIT 1`, id).Scan(&teks); err != nil {
		t.Fatalf("baca timeline: %v", err)
	}
	if !strings.Contains(teks, "revisi") || !strings.Contains(teks, "harga baris 1 salah input") {
		t.Errorf("timeline harus memuat alasan revisi: %q", teks)
	}
	if n := hitungBaris(t, f, "audit_logs", "action", "revise"); n != 1 {
		t.Errorf("audit revisi harus tepat satu, dapat %d", n)
	}

	// Pekerjaan render invoice versi baru dititipkan pada transaksi yang sama.
	if n := hitungJobRevisi(t, f, id); n != 1 {
		t.Errorf("job revisi_invoice harus tepat satu, dapat %d", n)
	}

	// Jalankan pekerjaan worker: invoice versi baru dirender, versi lama utuh.
	if err := jalankanPekerjaanRevisi(t, f, id); err != nil {
		t.Fatalf("pekerjaan revisi: %v", err)
	}
	docAkhir := dokumenPesanans(t, f, id)
	if _, ada := docAkhir["invoice_pdf-1"]; !ada {
		t.Fatalf("invoice versi 1 harus tercipta: %v", docAkhir)
	}
	if docAkhir["invoice_pdf-0"] != docAwal["invoice_pdf-0"] {
		t.Errorf("kunci berkas versi lama tidak boleh berubah: %s -> %s", docAwal["invoice_pdf-0"], docAkhir["invoice_pdf-0"])
	}
	if isiObjek(t, f, docAkhir["invoice_pdf-0"]) != htmlInvoiceV0 {
		t.Error("berkas invoice versi lama tidak boleh ditimpa")
	}
	htmlV1 := isiObjek(t, f, docAkhir["invoice_pdf-1"])
	if !strings.Contains(htmlV1, "Revisi v1") {
		t.Errorf("invoice versi baru harus mencantumkan Revisi v1: %s", htmlV1)
	}
	// Surat Jalan TIDAK dirender ulang untuk versi revisi.
	if _, ada := docAkhir["surat_jalan_pdf-1"]; ada {
		t.Error("surat jalan versi revisi tidak boleh dibuat")
	}

	// Email: invoice cabang + admin versi 1 dikirim ulang; surat jalan tidak.
	if n := hitungEmail(t, f, id, "invoice_cabang", 1); n != 1 {
		t.Errorf("email invoice cabang v1 harus 1, dapat %d", n)
	}
	if n := hitungEmail(t, f, id, "invoice_admin", 1); n != 1 {
		t.Errorf("email invoice admin v1 harus 1, dapat %d", n)
	}
	if n := hitungEmail(t, f, id, "surat_jalan_admin", 1); n != 0 {
		t.Errorf("email surat jalan tidak boleh dikirim ulang saat revisi, dapat %d", n)
	}

	// Pekerjaan dijalankan ulang: idempoten (tidak menggandakan email).
	if err := jalankanPekerjaanRevisi(t, f, id); err != nil {
		t.Fatalf("pekerjaan revisi ulang: %v", err)
	}
	if n := hitungEmail(t, f, id, "invoice_cabang", 1); n != 1 {
		t.Errorf("ulangan pekerjaan tidak boleh menggandakan email: %d", n)
	}
}

// TestPGRevisiUnduhVersiLamaDanBaru membuktikan kedua versi invoice dapat
// diunduh lewat API (presigned) setelah revisi.
func TestPGRevisiUnduhVersiLamaDanBaru(t *testing.T) {
	f, id, _, cookies, csrf := jalurUtamaRevisi(t)

	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("pekerjaan Done: %v", err)
	}
	body := reviseBody("koreksi qty", `{"line_no":1,"qty":5,"harga_satuan":100000},{"line_no":2,"qty":1,"harga_satuan":50000}`)
	if status, hasil := reviseHTTP(t, f, cookies, csrf, id, body); status != http.StatusOK {
		t.Fatalf("revisi harus 200, dapat %d (%v)", status, hasil)
	}
	if err := jalankanPekerjaanRevisi(t, f, id); err != nil {
		t.Fatalf("pekerjaan revisi: %v", err)
	}

	// Sesi cabang: daftar dokumennya hanya invoice, termasuk kedua versi.
	cabang, csrfCabang := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	status, list, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/orders/"+id+"/documents", "", cabang, csrfCabang)
	if status != http.StatusOK {
		t.Fatalf("daftar dokumen cabang harus 200, dapat %d (%v)", status, list)
	}
	items, _ := list["data"].([]any)
	if len(items) != 2 {
		t.Fatalf("cabang harus melihat DUA versi invoice, dapat %d (%v)", len(items), items)
	}
	for _, it := range items {
		doc, _ := it.(map[string]any)
		docID, _ := doc["id"].(string)
		st, dl, _ := apiReq(t, http.DefaultClient, http.MethodGet,
			f.srv.URL+"/api/v1/documents/"+docID+"/download", "", cabang, csrfCabang)
		if st != http.StatusOK {
			t.Fatalf("unduh invoice versi %v harus 200, dapat %d (%v)", doc["versi"], st, dl)
		}
		data := dataMap(t, dl)
		if url, _ := data["url"].(string); !strings.Contains(url, "invoice_pdf-") {
			t.Errorf("tautan unduh harus menunjuk berkas invoice: %v", data)
		}
	}
}

// TestPGRevisiManagerDitolakSaatToggleMati membuktikan larangan Manager
// ditegakkan API saat setelan `manager_boleh_revisi` nonaktif.
func TestPGRevisiManagerDitolakSaatToggleMati(t *testing.T) {
	f := newPGFixture(t)
	produk := produkKode(t, f, "P-01")
	tgl, periode := tanggalDanPeriode(1)
	id, _ := seedPesananDone(t, f, f.branchA, "2026-10-901", tgl, []barisDone{
		{lineNo: 1, product: produk, nama: "Bio Magnetik", qty: 1, harga: 100000, serial: periode + "901"},
	})

	setSetting(t, f, settings.ManagerBolehRevisi, "false")
	cookies, csrf := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	body := reviseBody("coba revisi", `{"line_no":1,"qty":2,"harga_satuan":100000}`)
	status, hasil := reviseHTTP(t, f, cookies, csrf, id, body)
	if status != http.StatusForbidden {
		t.Fatalf("Manager saat toggle mati harus 403, dapat %d (%v)", status, hasil)
	}
	if versi, _ := versiPesanan(t, f, id); versi != 0 {
		t.Errorf("versi tidak boleh berubah saat Manager ditolak: %d", versi)
	}
	if n := hitungBaris(t, f, "invoice_revisions", "order_id", id); n != 0 {
		t.Errorf("tidak boleh ada baris revisi saat Manager ditolak: %d", n)
	}

	// Toggle dihidupkan: Manager boleh merevisi.
	setSetting(t, f, settings.ManagerBolehRevisi, "true")
	status, hasil = reviseHTTP(t, f, cookies, csrf, id, body)
	if status != http.StatusOK {
		t.Fatalf("Manager saat toggle hidup harus 200, dapat %d (%v)", status, hasil)
	}
	if versi, _ := versiPesanan(t, f, id); versi != 1 {
		t.Errorf("versi harus 1 sesudah revisi Manager: %d", versi)
	}
}

// TestPGRevisiConcurrentBerurutan membuktikan dua revisi bersamaan berbaris
// rapi: keduanya sukses dengan versi berurutan (1 lalu 2), bukan dua versi 1.
func TestPGRevisiConcurrentBerurutan(t *testing.T) {
	f, id, _, cookies, csrf := jalurUtamaRevisi(t)
	body := reviseBody("ganti qty", `{"line_no":1,"qty":2,"harga_satuan":110000},{"line_no":2,"qty":1,"harga_satuan":50000}`)

	const n = 2
	var wg sync.WaitGroup
	statuses := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := append([]*http.Cookie{}, cookies...)
			st, _ := reviseHTTP(t, f, c, csrf, id, body)
			statuses[i] = st
		}(i)
	}
	wg.Wait()

	for i, st := range statuses {
		if st != http.StatusOK {
			t.Fatalf("revisi ke-%d harus 200 (berurutan), dapat %d", i+1, st)
		}
	}
	versi, _ := versiPesanan(t, f, id)
	if versi != 2 {
		t.Errorf("dua revisi harus menghasilkan versi 2, dapat %d", versi)
	}
	var jumlah int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM invoice_revisions WHERE order_id = $1`, id).Scan(&jumlah); err != nil {
		t.Fatalf("hitung revisi: %v", err)
	}
	if jumlah != 2 {
		t.Errorf("harus ada dua baris revisi (versi 1 dan 2), dapat %d", jumlah)
	}
}

// TestPGRevisiRollbackTidakMenyisakanSerialVersiDanJob memaksa kegagalan di
// akhir transaksi revisi (baris revisi bentrok) dan membuktikan rollback tidak
// menyisakan versi, serial, timeline, audit, dokumen, atau job palsu.
func TestPGRevisiRollbackTidakMenyisakanSerialVersiDanJob(t *testing.T) {
	f, id, totalLama, cookies, csrf := jalurUtamaRevisi(t)
	ctx := context.Background()
	_, periode := tanggalDanPeriode(3)

	if err := jalankanPekerjaanDone(t, f, id); err != nil {
		t.Fatalf("pekerjaan Done: %v", err)
	}
	counterAwal := counterSerial(t, f, periode)
	docAwal := dokumenPesanans(t, f, id)

	// Paksa InsertInvoiceRevision gagal: baris (order_id, versi_baru=1) sudah
	// ada (unique constraint), padahal transaksi revisi menghitung versi_baru 1.
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO invoice_revisions (order_id, versi_lama, versi_baru, alasan, snapshot, delta_total, admin_id)
		 VALUES ($1, 0, 1, 'pembuka', '{}'::jsonb, 0, $2)`, id, f.adminSA); err != nil {
		t.Fatalf("seed revisi pembuka: %v", err)
	}

	body := reviseBody("harus gagal", `{"line_no":1,"qty":9,"harga_satuan":120000}`)
	status, _ := reviseHTTP(t, f, cookies, csrf, id, body)
	if status == http.StatusOK {
		t.Fatalf("revisi harus GAGAL karena bentrok snapshot, tetapi sukses")
	}

	// Rollback: versi, total, baris, serial, counter tidak berubah.
	versi, totalDB := versiPesanan(t, f, id)
	if versi != 0 || totalDB != totalLama {
		t.Errorf("rollback harus mempertahankan versi 0 total %d, dapat versi %d total %d", totalLama, versi, totalDB)
	}
	if got := serialBaris(t, f, id, 1); got != periode+"901" {
		t.Errorf("serial baris lama tidak boleh berubah: %s", got)
	}
	var qtyBaris1 int
	if err := f.pool.QueryRow(ctx,
		`SELECT qty FROM order_lines WHERE order_id = $1 AND line_no = 1`, id).Scan(&qtyBaris1); err != nil {
		t.Fatalf("baca qty baris 1: %v", err)
	}
	if qtyBaris1 != 2 {
		t.Errorf("baris pesanan tidak boleh berubah setelah rollback: qty=%d", qtyBaris1)
	}
	if n := hitungBaris(t, f, "order_lines", "order_id", id); n != 2 {
		t.Errorf("jumlah baris harus tetap 2, dapat %d", n)
	}
	if c := counterSerial(t, f, periode); c != counterAwal {
		t.Errorf("counter serial tidak boleh terpakai saat rollback: %d -> %d", counterAwal, c)
	}
	if docAkhir := dokumenPesanans(t, f, id); docAkhir["invoice_pdf-0"] != docAwal["invoice_pdf-0"] {
		t.Error("dokumen versi lama tidak boleh tersentuh rollback")
	}
	if n := hitungJobRevisi(t, f, id); n != 0 {
		t.Errorf("tidak boleh ada job revisi setelah rollback, dapat %d", n)
	}
	if n := hitungTimeline(t, f, id); n != 1 {
		t.Errorf("timeline harus tetap satu entri dasar setelah rollback, dapat %d", n)
	}
	var teks string
	if err := f.pool.QueryRow(ctx,
		`SELECT teks FROM order_events WHERE order_id = $1 ORDER BY at DESC, id DESC LIMIT 1`, id).Scan(&teks); err != nil {
		t.Fatalf("baca timeline: %v", err)
	}
	if strings.Contains(teks, "Revisi") || strings.Contains(teks, "direvisi") {
		t.Errorf("tidak boleh ada timeline revisi setelah rollback: %q", teks)
	}
}

// TestPGRevisiMenolakPesananBelumDoneDanIdTakSah menutup gerbang dasar:
// revisi hanya untuk pesanan Done, dan id/CSRF/aktor tidak sah dijawab rapi.
func TestPGRevisiMenolakPesananBelumDoneDanIdTakSah(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	produk := produkKode(t, f, "P-01")

	// Pesanan Submitted (belum Done) tidak bisa direvisi.
	var idSubmitted string
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO orders (branch_id, nomor, status, verif_status, tanggal, total, submitted_at)
		 VALUES ($1,'2026-10-902','submitted','belum',(now() AT TIME ZONE 'Asia/Jakarta')::date,100000, now())
		 RETURNING id`, f.branchA).Scan(&idSubmitted); err != nil {
		t.Fatalf("seed pesanan submitted: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO order_lines (order_id, line_no, product_id, nama_snapshot, satuan_snapshot, qty, harga_satuan, serial_number)
		 VALUES ($1,1,$2,'Bio Magnetik','pcs',1,100000,'20261009901')`, idSubmitted, produk); err != nil {
		t.Fatalf("seed baris submitted: %v", err)
	}

	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	body := reviseBody("coba", `{"line_no":1,"qty":2,"harga_satuan":100000}`)

	status, hasil := reviseHTTP(t, f, cookies, csrf, idSubmitted, body)
	if status != http.StatusConflict {
		t.Fatalf("pesanan belum Done harus 409, dapat %d (%v)", status, hasil)
	}
	if kode := errorKode(t, hasil); kode != "ORDER_NOT_DONE" {
		t.Errorf("kode harus ORDER_NOT_DONE, dapat %q", kode)
	}

	// Id bukan UUID -> 404 (bukan 500).
	status, _ = reviseHTTP(t, f, cookies, csrf, "bukan-uuid", body)
	if status != http.StatusNotFound {
		t.Errorf("id bukan UUID harus 404, dapat %d", status)
	}

	// Tanpa CSRF -> 403.
	st, _, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+idSubmitted+"/revise", body, cookies, "")
	if st != http.StatusForbidden {
		t.Errorf("tanpa CSRF harus 403, dapat %d", st)
	}

	// Akun cabang tidak punya kapabilitas admin.revise -> 403.
	cabang, csrfCabang := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	status, _ = reviseHTTP(t, f, cabang, csrfCabang, idSubmitted, body)
	if status != http.StatusForbidden {
		t.Errorf("akun cabang harus 403, dapat %d", status)
	}
}

// TestPGRevisiSertifikatPertahankanPackagingDanHargaNol membuktikan aturan
// Sertifikat tetap terjaga saat revisi: harga produk Sertifikat dipaksa 0 dan
// baris packaging dipertahankan (biaya dari setelan server), serta pencampuran
// Sertifikat dengan produk reguler tetap ditolak.
func TestPGRevisiSertifikatPertahankanPackagingDanHargaNol(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx,
		`UPDATE admin_users SET notif_invoice = true WHERE id = $1`, f.adminSA); err != nil {
		t.Fatalf("pasang flag email admin: %v", err)
	}

	var sertifikat string
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO products (kode, nama, jenis, satuan, harga_default, urutan)
		 VALUES ('S-01','Sertifikat Halal','sertifikat','pcs',0,1) RETURNING id`).Scan(&sertifikat); err != nil {
		t.Fatalf("seed produk sertifikat: %v", err)
	}
	tgl, periode := tanggalDanPeriode(2)
	id, _ := seedPesananDone(t, f, f.branchA, "2026-10-903", tgl, []barisDone{
		{lineNo: 1, product: sertifikat, nama: "Sertifikat Halal", qty: 2, harga: 0, serial: periode + "901"},
		{lineNo: 2, packaging: true, nama: "Biaya packaging", harga: 3000},
	})
	cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")

	// Coba naikkan harga Sertifikat: server harus memaksanya 0.
	body := reviseBody("koreksi qty sertifikat", `{"line_no":1,"qty":5,"harga_satuan":999999}`)
	status, hasil := reviseHTTP(t, f, cookies, csrf, id, body)
	if status != http.StatusOK {
		t.Fatalf("revisi sertifikat harus 200, dapat %d (%v)", status, hasil)
	}
	data := dataMap(t, hasil)
	if data["total"] != float64(3000) {
		t.Errorf("total sertifikat harus tetap 3000 (harga 0 + packaging), dapat %v", data["total"])
	}

	var (
		jenis      string
		qty        int
		harga      int64
		nPackaging int
	)
	rows, err := f.pool.Query(ctx,
		`SELECT jenis::text, qty, harga_satuan FROM order_lines WHERE order_id = $1 ORDER BY line_no`, id)
	if err != nil {
		t.Fatalf("baca baris: %v", err)
	}
	for rows.Next() {
		if err := rows.Scan(&jenis, &qty, &harga); err != nil {
			t.Fatalf("scan baris: %v", err)
		}
		if jenis == "packaging" {
			nPackaging++
			if harga != 3000 {
				t.Errorf("baris packaging harus tetap 3000, dapat %d", harga)
			}
		}
		if jenis == "product" && harga != 0 {
			t.Errorf("harga produk Sertifikat harus dipaksa 0, dapat %d", harga)
		}
	}
	rows.Close()
	if nPackaging != 1 {
		t.Errorf("pesanan Sertifikat harus punya tepat satu baris packaging, dapat %d", nPackaging)
	}

	// Mencampur produk reguler ke pesanan sertifikat ditolak.
	reguler := produkKode(t, f, "P-01")
	bodyCampur := reviseBody("coba campur", `{"line_no":1,"qty":1,"harga_satuan":0},`+
		`{"product_id":"`+reguler+`","qty":1,"harga_satuan":1000}`)
	status, hasil = reviseHTTP(t, f, cookies, csrf, id, bodyCampur)
	if status != http.StatusBadRequest {
		t.Fatalf("campuran sertifikat+reguler harus 400, dapat %d (%v)", status, hasil)
	}
	if versi, _ := versiPesanan(t, f, id); versi != 1 {
		t.Errorf("percobaan gagal tidak boleh menaikkan versi: %d", versi)
	}
}

// TestPGRevisiPDFNyataVersiLamaDanBaru menjalankan revisi dengan mesin PDF
// SUNGGUHAN (tiruan protokol Gotenberg di atas Chromium lokal): invoice versi
// lama tetap utuh byte-per-byte, invoice versi baru dirender sebagai berkas
// PDF A4 yang sah, dan keduanya dapat diunduh. Dilewati bila Chromium tidak
// tersedia.
func TestPGRevisiPDFNyataVersiLamaDanBaru(t *testing.T) {
	f := newPGFixture(t)
	shim := serverGotenbergChromium(t)

	// Router kedua memakai klien Gotenberg sungguhan (Chromium lokal) dengan
	// repository + storage yang sama seperti fixture.
	deps := f.deps
	deps.Documents = document.NewService(f.docRepo, f.objek, pdf.NewGotenberg(shim.URL))
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)

	pesanan := f.seedPesananDone(t, f.branchA, "2026-10-780", false)
	sa, csrfSA := pgLogin(t, srv, "rina@example.test", "rahasia123")

	// Versi 0: render tiga dokumen (Invoice PDF nyata) lewat endpoint admin.
	st, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		srv.URL+"/api/v1/admin/orders/"+pesanan.ID+"/documents", "", sa, csrfSA)
	if st != http.StatusOK {
		t.Fatalf("generate dokumen v0 harus 200, dapat %d (%v)", st, body)
	}
	kunciV0 := document.StorageKey(pesanan.ID, document.JenisInvoicePDF, 0)
	v0 := f.isiBerkas(t, kunciV0)
	if !bytes.HasPrefix(v0, []byte("%PDF-")) || !bytes.Contains(v0, []byte("%%EOF")) {
		t.Fatalf("invoice v0 bukan PDF yang utuh (%d byte)", len(v0))
	}

	// Revisi: qty baris 1 naik, baris 2 tetap.
	reqBody := reviseBody("koreksi qty versi nyata",
		`{"line_no":1,"qty":3,"harga_satuan":100000},{"line_no":2,"qty":1,"harga_satuan":50000}`)
	st, hasil, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		srv.URL+"/api/v1/admin/orders/"+pesanan.ID+"/revise", reqBody, sa, csrfSA)
	if st != http.StatusOK {
		t.Fatalf("revisi harus 200, dapat %d (%v)", st, hasil)
	}

	// Worker revisi dengan renderer NYATA: invoice versi 1 dirender.
	selesaiNyata := fulfillment.NewService(deps.Documents, f.notif)
	w := worker.NewRevisiInvoiceWorker(selesaiNyata)
	job := &river.Job[jobs.RevisiInvoiceArgs]{
		JobRow: &rivertype.JobRow{ID: 1},
		Args:   jobs.RevisiInvoiceArgs{OrderID: pesanan.ID},
	}
	if err := w.Work(context.Background(), job); err != nil {
		t.Fatalf("pekerjaan revisi (PDF nyata): %v", err)
	}

	// Versi lama tetap byte-identik; versi baru PDF sah dan BERBEDA.
	if lagi := f.isiBerkas(t, kunciV0); !bytes.Equal(lagi, v0) {
		t.Error("berkas invoice versi lama tidak boleh berubah setelah revisi")
	}
	kunciV1 := document.StorageKey(pesanan.ID, document.JenisInvoicePDF, 1)
	v1 := f.isiBerkas(t, kunciV1)
	if !bytes.HasPrefix(v1, []byte("%PDF-")) || !bytes.Contains(v1, []byte("%%EOF")) {
		t.Fatalf("invoice v1 bukan PDF yang utuh (%d byte)", len(v1))
	}
	if !bytes.Contains(v1, []byte("/Type /Page")) {
		t.Error("invoice v1 tidak memuat halaman")
	}
	if len(v1) < 3000 {
		t.Errorf("invoice v1 terlalu kecil untuk A4 berisi tabel: %d byte", len(v1))
	}
	if bytes.Equal(v1, v0) {
		t.Error("invoice versi baru harus berkas yang berbeda dari versi lama")
	}

	// Kedua versi terdaftar dan dapat diunduh lewat API (presigned).
	cabang, csrfCabang := pgLogin(t, srv, "a@example.test", "rahasia123")
	st, list, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		srv.URL+"/api/v1/orders/"+pesanan.ID+"/documents", "", cabang, csrfCabang)
	if st != http.StatusOK {
		t.Fatalf("daftar dokumen cabang harus 200, dapat %d (%v)", st, list)
	}
	if items := dataList(t, list); len(items) != 2 {
		t.Fatalf("cabang harus melihat dua versi invoice, dapat %d (%v)", len(items), items)
	}
}

// jakartaTZ mengembalikan zona waktu bisnis (Asia/Jakarta).
func jakartaTZ() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		return time.FixedZone("WIB", 7*3600)
	}
	return loc
}

// errorKode membaca kode error dari amplop {error:{code,message}}.
func errorKode(t *testing.T, body map[string]any) string {
	t.Helper()
	env, _ := body["error"].(map[string]any)
	if env == nil {
		t.Fatalf("amplop error tidak ditemukan: %v", body)
	}
	kode, _ := env["code"].(string)
	return kode
}
