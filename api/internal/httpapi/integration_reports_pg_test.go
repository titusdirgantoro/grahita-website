package httpapi

// Uji integrasi laporan order line & ekspor XLSX asinkron (F2-24) pada
// PostgreSQL nyata.
//
// Berbeda dari router_reports_test.go (repository in-memory), berkas ini
// menggerakkan router lengkap dengan repository PostgreSQL asli: filter benar
// dieksekusi sebagai SQL, jumlah baris ekspor dibandingkan dengan query
// terfilter, dan ekspor 50.000 baris dijalankan sungguhan untuk membuktikan
// pembacaan bertahap (bukan menahan seluruh data di memori).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/report"
	reportpg "github.com/grahita/portal/api/internal/report/postgres"
	"github.com/grahita/portal/api/internal/worker"
)

// aktorSAUji adalah aktor Superadmin untuk memanggil service langsung (peran
// worker/pengujian); jalur HTTP-nya diuji terpisah dengan sesi sungguhan.
func aktorSAUji() auth.Actor {
	return auth.Actor{UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
}

// hitungRepo membungkus repository laporan PostgreSQL dan mencatat pola
// pembacaan batch: berapa kali ListLines dipanggil dan seberapa besar batch
// terbesar yang pernah diminta. Pola inilah bukti "dibaca bertahap".
type hitungRepo struct {
	*reportpg.Repo

	mu        sync.Mutex
	panggilan int
	maksBatch int
}

func (r *hitungRepo) ListLines(ctx context.Context, q report.Query) ([]report.Line, error) {
	r.mu.Lock()
	r.panggilan++
	if int(q.Batch) > r.maksBatch {
		r.maksBatch = int(q.Batch)
	}
	r.mu.Unlock()
	return r.Repo.ListLines(ctx, q)
}

func (r *hitungRepo) pola() (panggilan, maksBatch int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.panggilan, r.maksBatch
}

// laporanPG adalah dunia uji laporan di atas PostgreSQL nyata.
type laporanPG struct {
	*pgFixture
	srv   *httptest.Server
	repo  *hitungRepo
	store *objekHitung
	svc   *report.Service
	seq   int
}

func newLaporanPG(t *testing.T) *laporanPG {
	t.Helper()
	f := newPGFixture(t)

	repo := &hitungRepo{Repo: reportpg.New(f.pool, reportpg.WithJobQueue(f.jobs))}
	svc := report.NewService(repo, f.objek)
	f.deps.Reports = svc

	srv := httptest.NewServer(NewRouter(f.deps))
	t.Cleanup(srv.Close)
	return &laporanPG{pgFixture: f, srv: srv, repo: repo, store: f.objek, svc: svc}
}

// serialBaru menghasilkan serial resmi (YYYYMMDD + urut) yang unik dalam uji.
func (l *laporanPG) serialBaru() string {
	l.seq++
	return fmt.Sprintf("20261001%05d", l.seq)
}

// barisLaporan adalah satu baris yang ditulis saat menyiapkan pesanan uji.
type barisLaporan struct {
	Jenis     string // product | packaging
	Nama      string
	Qty       int
	Harga     int64
	ProductID string // kosong untuk packaging
	Serial    string // kosong untuk packaging
}

// pesananLaporan adalah satu pesanan yang disiapkan langsung di database: dibuat
// sebagai draft, diisi barisnya, lalu dipindahkan ke submitted/done persis
// seperti hasil submit sungguhan (trigger orders_guard_submit tetap berlaku).
type pesananLaporan struct {
	BranchID string
	Nomor    string
	Tanggal  string // YYYY-MM-DD
	Status   string // submitted | done
	IsCNC    bool
	Baris    []barisLaporan
	Metode   string // diisi bila Status = done
}

// seedPesanan menulis satu pesanan lengkap dan mengembalikan id-nya.
func (l *laporanPG) seedPesanan(t *testing.T, o pesananLaporan) string {
	t.Helper()
	ctx := context.Background()

	var id string
	if err := l.pool.QueryRow(ctx,
		`INSERT INTO orders (branch_id, is_cnc) VALUES ($1,$2) RETURNING id`,
		o.BranchID, o.IsCNC).Scan(&id); err != nil {
		t.Fatalf("seed pesanan (draft): %v", err)
	}

	for i, b := range o.Baris {
		var produk any
		if b.ProductID != "" {
			produk = b.ProductID
		}
		var serial any
		if b.Serial != "" {
			serial = b.Serial
		}
		if _, err := l.pool.Exec(ctx,
			`INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot, qty, harga_satuan, serial_number)
			 VALUES ($1,$2,$3::line_kind,$4,$5,$6,$7,$8)`,
			id, i+1, b.Jenis, produk, b.Nama, b.Qty, b.Harga, serial); err != nil {
			t.Fatalf("seed baris %d pesanan %s: %v", i+1, o.Nomor, err)
		}
	}

	if o.Status != "draft" {
		status := o.Status
		if status == "done" {
			status = "submitted"
		}
		if _, err := l.pool.Exec(ctx,
			`UPDATE orders
			    SET status = $2::order_status,
			        nomor = $3,
			        tanggal = $4::date,
			        submitted_at = now()
			  WHERE id = $1`,
			id, status, o.Nomor, o.Tanggal); err != nil {
			t.Fatalf("pindahkan pesanan %s ke %s: %v", o.Nomor, o.Status, err)
		}
	}

	if o.Status == "done" {
		var total int64
		if err := l.pool.QueryRow(ctx, `SELECT total FROM orders WHERE id=$1`, id).Scan(&total); err != nil {
			t.Fatal(err)
		}
		pasangPointerVerifikasiDone(t, l.pool, l.adminSA, l.bankDefault, id, o.Nomor, total)
	}
	return id
}

// seedProdukSertifikat menambahkan satu produk sertifikat (jenis 'sertifikat')
// beserta harganya untuk cabang tertentu. Produk sertifikat gratis
// (harga_default = 0, constraint `sertifikat_gratis`); yang dibayar adalah
// baris biaya packaging.
func (l *laporanPG) seedProdukSertifikat(t *testing.T, branchID string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := l.pool.QueryRow(ctx,
		`INSERT INTO products (kode, nama, jenis, harga_default, urutan)
		 VALUES ('S-01','Sertifikat Halal','sertifikat',0,2) RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seed produk sertifikat: %v", err)
	}
	if _, err := l.pool.Exec(ctx,
		`INSERT INTO branch_prices (branch_id, product_id, harga) VALUES ($1,$2,0)`,
		branchID, id); err != nil {
		t.Fatalf("seed harga produk sertifikat: %v", err)
	}
	return id
}

// produkA mengembalikan id produk reguler hasil seed fixture (kode P-01).
func (l *laporanPG) produkA(t *testing.T) string {
	t.Helper()
	var id string
	if err := l.pool.QueryRow(context.Background(),
		`SELECT id FROM products WHERE kode = 'P-01'`).Scan(&id); err != nil {
		t.Fatalf("cari produk P-01: %v", err)
	}
	return id
}

// tandaiCabang mengatur flag cabang (C&C/PI) yang dipakai filter
// laporan (b.pajak_in).
func (l *laporanPG) tandaiCabang(t *testing.T, branchID string, cnc, pajakIn bool) {
	t.Helper()
	if _, err := l.pool.Exec(context.Background(),
		`UPDATE branches SET is_cnc=$2, pajak_in=$3 WHERE id=$1`,
		branchID, cnc, pajakIn); err != nil {
		t.Fatalf("tandai cabang: %v", err)
	}
}

// saLogin masuk sebagai Superadmin dan mengembalikan cookie + token CSRF.
func (l *laporanPG) saLogin(t *testing.T) ([]*http.Cookie, string) {
	t.Helper()
	return pgLogin(t, l.srv, "rina@example.test", "rahasia123")
}

// lajur mengambil satu halaman laporan lewat API.
func (l *laporanPG) lajur(t *testing.T, cookies []*http.Cookie, query string) map[string]any {
	t.Helper()
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		l.srv.URL+"/api/v1/admin/reports/order-lines"+query, "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("daftar laporan %q harus 200, dapat %d (%v)", query, status, body)
	}
	return body["data"].(map[string]any)
}

// TestPGReportLaporanOrderLine menjalankan seluruh perilaku laporan order line
// di atas PostgreSQL nyata: kolom, pengecualian draft, filter (tanggal, cabang
// multi, status, jenis, C&C, PI), otorisasi Superadmin, dan ekspor
// asinkron yang jumlah barisnya sama dengan query terfilter.
func TestPGReportLaporanOrderLine(t *testing.T) {
	l := newLaporanPG(t)
	ctx := context.Background()

	produkID := l.produkA(t)
	sertifikatID := l.seedProdukSertifikat(t, l.branchA)

	// Cabang A: PI. Cabang B: tanpa flag.
	l.tandaiCabang(t, l.branchA, true, true)
	l.tandaiCabang(t, l.branchB, false, false)

	// Pesanan submitted cabang A (2 baris produk, C&C).
	l.seedPesanan(t, pesananLaporan{
		BranchID: l.branchA, Nomor: "2026-10-01", Tanggal: "2026-10-01", Status: "submitted", IsCNC: true,
		Baris: []barisLaporan{
			{Jenis: "product", Nama: "Bio Magnetik", Qty: 2, Harga: 100000, ProductID: produkID, Serial: l.serialBaru()},
			{Jenis: "product", Nama: "Bio Magnetik", Qty: 1, Harga: 100000, ProductID: produkID, Serial: l.serialBaru()},
		},
	})
	// Pesanan done cabang B (1 baris produk) — verifikasi manual tercatat.
	l.seedPesanan(t, pesananLaporan{
		BranchID: l.branchB, Nomor: "2026-10-02", Tanggal: "2026-10-02", Status: "done", Metode: "manual",
		Baris: []barisLaporan{
			{Jenis: "product", Nama: "Bio Magnetik", Qty: 3, Harga: 200000, ProductID: produkID, Serial: l.serialBaru()},
		},
	})
	// Pesanan done SERTIFIKAT cabang B: baris produk + baris packaging.
	l.seedPesanan(t, pesananLaporan{
		BranchID: l.branchB, Nomor: "2026-10-03", Tanggal: "2026-10-03", Status: "done", Metode: "manual",
		Baris: []barisLaporan{
			{Jenis: "product", Nama: "Sertifikat Halal", Qty: 1, Harga: 0, ProductID: sertifikatID, Serial: l.serialBaru()},
			{Jenis: "packaging", Nama: "Biaya Packaging", Qty: 1, Harga: 3000},
		},
	})
	// Pesanan DRAFT (harus dikecualikan) — punya baris produk juga.
	l.seedPesanan(t, pesananLaporan{
		BranchID: l.branchA, Nomor: "", Tanggal: "2026-10-04", Status: "draft",
		Baris: []barisLaporan{
			{Jenis: "product", Nama: "Bio Magnetik", Qty: 9, Harga: 100000, ProductID: produkID, Serial: l.serialBaru()},
		},
	})

	cookies, csrf := l.saLogin(t)

	// ---------- Daftar dasar: satu baris per order line, draft dikecualikan ----
	page := l.lajur(t, cookies, "")
	if page["total"] != float64(5) {
		t.Fatalf("total baris harus 5 (draft dikecualikan), dapat %v", page["total"])
	}
	if len(page["items"].([]any)) != 5 {
		t.Fatalf("items harus 5, dapat %v", page["items"])
	}
	// total_subtotal = (2+1)×100000 (cabang A) + 3×200000 (B done)
	//                  + 0 + 3000 (B sertifikat: produk gratis + packaging) = 903.000
	if page["total_subtotal"] != float64(903000) {
		t.Errorf("total subtotal harus 903000, dapat %v", page["total_subtotal"])
	}

	baris := map[string]map[string]any{}
	for _, it := range page["items"].([]any) {
		m := it.(map[string]any)
		baris[m["nomor"].(string)+"#"+fmt.Sprint(m["line_no"])] = m
	}
	if len(baris) != 5 {
		t.Fatalf("kunci baris tidak unik: %v", baris)
	}
	// Kolom PRD §4.7 lengkap, termasuk serial, versi, dan verifikasi.
	satu := baris["2026-10-01#1"]
	if satu["serial_number"] == "" || satu["versi"] != float64(0) {
		t.Errorf("baris pertama harus punya serial + versi: %v", satu)
	}
	if satu["cabang"] != "Grahita Cabang A" || satu["kepala_cabang"] != "Kepala A" {
		t.Errorf("identitas cabang salah: %v", satu)
	}
	if satu["is_cnc"] != true || satu["pajak_in"] != true {
		t.Errorf("atribut C&C/PI cabang A harus true: %v", satu)
	}
	if satu["produk"] != "Bio Magnetik" || satu["jenis"] != "product" || satu["qty"] != float64(2) ||
		satu["harga_satuan"] != float64(100000) || satu["subtotal"] != float64(200000) {
		t.Errorf("angka baris salah: %v", satu)
	}
	if satu["status"] != "submitted" {
		t.Errorf("status baris pertama harus submitted: %v", satu)
	}
	// PI hanya atribut: subtotal tetap qty × harga cabang, tanpa efek pajak/harga.
	if got := satu["subtotal"].(float64); got != 2*100000 {
		t.Errorf("PI tidak boleh mengubah harga: subtotal %v", got)
	}

	done := baris["2026-10-02#1"]
	if done["status"] != "done" || done["metode_verifikasi"] != "manual" || done["tanggal_verifikasi"] == "" {
		t.Errorf("kolom verifikasi baris done salah: %v", done)
	}
	kemas := baris["2026-10-03#2"]
	if kemas["jenis"] != "packaging" || kemas["serial_number"] != "" {
		t.Errorf("baris packaging harus ikut dan tanpa serial: %v", kemas)
	}
	if _, ada := baris["2026-10-04#1"]; ada {
		t.Error("baris pesanan draft tidak boleh muncul di laporan")
	}

	// ---------- Filter -------------------------------------------------------
	kasus := []struct {
		nama  string
		query string
		total float64
	}{
		{"cabang A", "?cabang=" + l.branchA, 2},
		{"cabang multi (A+B)", "?cabang=" + l.branchA + "," + l.branchB, 5},
		{"cabang berulang", "?cabang=" + l.branchA + "&cabang=" + l.branchB, 5},
		{"status done", "?status=done", 3},
		{"status submitted", "?status=submitted", 2},
		{"jenis packaging", "?jenis=packaging", 1},
		{"jenis product", "?jenis=product", 4},
		{"C&C", "?cnc=true", 2},
		{"PI", "?pi=true", 2},
		{"PI cabang B (false)", "?pi=false", 3},
		{"tanggal satu hari", "?dari=2026-10-02&sampai=2026-10-02", 1},
		{"rentang dua hari", "?dari=2026-10-02&sampai=2026-10-03", 3},
		{"gabungan cabang+status+jenis", "?cabang=" + l.branchB + "&status=done&jenis=product", 2},
	}
	for _, k := range kasus {
		got := l.lajur(t, cookies, k.query)
		if got["total"] != k.total {
			t.Errorf("filter %s: total harus %v, dapat %v", k.nama, k.total, got["total"])
		}
	}

	// ---------- Otorisasi: hanya Superadmin (PRD §2.1) -----------------------
	akhir := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/admin/reports/order-lines", ""},
		{http.MethodPost, "/admin/reports/export", `{}`},
		{http.MethodGet, "/admin/reports/export/00000000-0000-4000-8000-000000000001", ""},
		{http.MethodGet, "/admin/reports/export/00000000-0000-4000-8000-000000000001/download", ""},
	}
	mgrCookies, mgrCsrf := pgLogin(t, l.srv, "budi@example.test", "rahasia123")
	userCookies, userCsrf := pgLogin(t, l.srv, "a@example.test", "rahasia123")
	for _, aktor := range []struct {
		nama    string
		cookies []*http.Cookie
		csrf    string
	}{{"manager", mgrCookies, mgrCsrf}, {"cabang", userCookies, userCsrf}} {
		for _, e := range akhir {
			status, body, _ := apiReq(t, http.DefaultClient, e.method,
				l.srv.URL+"/api/v1"+e.path, e.body, aktor.cookies, aktor.csrf)
			if status != http.StatusForbidden {
				t.Errorf("%s %s %s harus 403, dapat %d (%v)", aktor.nama, e.method, e.path, status, body)
			}
		}
	}

	// ---------- Ekspor asinkron: jumlah baris = query terfilter --------------
	permintaan := `{"cabang":["` + l.branchB + `"],"status":"done"}`
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		l.srv.URL+"/api/v1/admin/reports/export", permintaan, cookies, csrf)
	if status != http.StatusAccepted {
		t.Fatalf("permintaan ekspor harus 202, dapat %d (%v)", status, body)
	}
	job := body["data"].(map[string]any)
	jobID := job["id"].(string)
	if job["status"] != "queued" {
		t.Errorf("status awal harus queued, dapat %v", job["status"])
	}
	// Filter tersimpan = filter yang diminta (dalam bentuk normal).
	tersimpan := job["filter"].(map[string]any)
	if fmt.Sprint(tersimpan["cabang"]) != fmt.Sprint([]any{l.branchB}) || tersimpan["status"] != "done" {
		t.Errorf("filter tersimpan tidak sesuai permintaan: %v", tersimpan)
	}
	// Job River dititipkan DI DALAM transaksi yang sama (SystemDesign §4.2):
	// barisnya benar-benar ada di tabel antrean dengan kind ekspor laporan.
	var jobAntrean int
	if err := l.pool.QueryRow(ctx,
		`SELECT count(*) FROM river_job WHERE kind = 'report_xlsx' AND args->>'job_id' = $1`,
		jobID).Scan(&jobAntrean); err != nil {
		t.Fatalf("periksa job River: %v", err)
	}
	if jobAntrean != 1 {
		t.Errorf("harus ada tepat 1 job River report_xlsx untuk pekerjaan ini, dapat %d", jobAntrean)
	}

	if _, err := l.svc.Jalankan(ctx, jobID); err != nil {
		t.Fatalf("jalankan ekspor: %v", err)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		l.srv.URL+"/api/v1/admin/reports/export/"+jobID, "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("status pekerjaan harus 200, dapat %d (%v)", status, body)
	}
	selesai := body["data"].(map[string]any)
	if selesai["status"] != "selesai" {
		t.Fatalf("pekerjaan harus selesai, dapat %v", selesai)
	}
	if selesai["jumlah_baris"] != float64(3) {
		t.Fatalf("jumlah baris ekspor harus 3, dapat %v", selesai["jumlah_baris"])
	}
	// Angka ekspor harus sama dengan query terfilter yang sama.
	banding := l.lajur(t, cookies, "?cabang="+l.branchB+"&status=done")
	if banding["total"] != selesai["jumlah_baris"] {
		t.Errorf("jumlah baris ekspor %v harus sama dengan query terfilter %v", selesai["jumlah_baris"], banding["total"])
	}

	// Unduhan hanya setelah selesai, dan tautannya menunjuk objek yang ada.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		l.srv.URL+"/api/v1/admin/reports/export/"+jobID+"/download", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("unduh harus 200, dapat %d (%v)", status, body)
	}
	unduh := body["data"].(map[string]any)
	if unduh["url"] == "" || unduh["expires_in"] != float64(300) {
		t.Errorf("tautan unduh tidak sesuai: %v", unduh)
	}

	// Isi berkas: 3 baris data, header ter-freeze, sheet rekap ada, versi
	// pesanan ikut dan kolom uang berformat Rupiah.
	hasil, err := l.svc.Job(ctx, aktorSAUji(), jobID)
	if err != nil {
		t.Fatalf("baca pekerjaan: %v", err)
	}
	raw, err := l.store.Get(ctx, hasil.StorageKey)
	if err != nil {
		t.Fatalf("ambil berkas ekspor: %v", err)
	}
	defer func() { _ = raw.Close() }()
	isi, err := io.ReadAll(raw)
	if err != nil {
		t.Fatalf("baca berkas ekspor: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(isi))
	if err != nil {
		t.Fatalf("buka ekspor: %v", err)
	}
	defer func() { _ = f.Close() }()
	if sheet := f.GetSheetList(); len(sheet) != 2 || sheet[0] != report.SheetLaporan || sheet[1] != report.SheetRekap {
		t.Fatalf("sheet ekspor harus [Laporan Rekap Bulanan], dapat %v", sheet)
	}
	barisEkspor, err := f.GetRows(report.SheetLaporan, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatalf("baca sheet laporan: %v", err)
	}
	if len(barisEkspor) != 4 {
		t.Fatalf("sheet laporan harus 4 baris (header+3), dapat %d", len(barisEkspor))
	}
	// Kolom status sesuai filter; baris packaging milik pesanan sertifikat ikut
	// (satu baris per order line, AP-REP-01).
	jenisTampil := map[string]int{}
	for _, b := range barisEkspor[1:] {
		if b[13] != "done" {
			t.Errorf("baris ekspor harus berstatus done (sesuai filter): %v", b)
		}
		if b[3] != "Grahita Cabang B" {
			t.Errorf("baris ekspor harus milik cabang B (sesuai filter): %v", b)
		}
		jenisTampil[b[8]]++
	}
	if jenisTampil["product"] != 2 || jenisTampil["packaging"] != 1 {
		t.Errorf("ekspor cabang B harus memuat 2 baris produk + 1 packaging, dapat %v", jenisTampil)
	}
	// Format Rupiah pada kolom harga satuan (L).
	idGaya, err := f.GetCellStyle(report.SheetLaporan, "L2")
	if err != nil {
		t.Fatalf("gaya sel harga: %v", err)
	}
	gaya, err := f.GetStyle(idGaya)
	if err != nil {
		t.Fatalf("baca gaya harga: %v", err)
	}
	if gaya.CustomNumFmt == nil || !strings.Contains(*gaya.CustomNumFmt, "Rp") {
		t.Errorf("kolom harga harus berformat Rupiah, dapat %v", gaya.CustomNumFmt)
	}
	panes, err := f.GetPanes(report.SheetLaporan)
	if err != nil {
		t.Fatalf("baca panes: %v", err)
	}
	if !panes.Freeze || panes.YSplit != 1 {
		t.Errorf("header ekspor harus ter-freeze: %+v", panes)
	}
	rekap, err := f.GetRows(report.SheetRekap, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatalf("baca rekap: %v", err)
	}
	if len(rekap) != 2 {
		t.Fatalf("rekap harus 1 baris data + header, dapat %v", rekap)
	}
	// 3 baris cabang B: 600000 + 0 (sertifikat gratis) + 3000 = 603.000.
	if rekap[1][2] != "3" || rekap[1][4] != "603000" {
		t.Errorf("rekap bulanan salah: %v", rekap[1])
	}
}

// TestPGReportEksporDijalankanWorkerRiver membuktikan rantai asinkron utuh di
// atas PostgreSQL nyata: permintaan HTTP menitipkan job River di dalam
// transaksinya, worker River yang sesungguhnya (kelas yang dipakai
// `--mode=worker`) mengambil job itu, dan pekerjaan berakhir `selesai` tanpa
// ada pemanggilan langsung dari pengujian.
func TestPGReportEksporDijalankanWorkerRiver(t *testing.T) {
	l := newLaporanPG(t)
	ctx := context.Background()
	produkID := l.produkA(t)

	for i := 1; i <= 3; i++ {
		l.seedPesanan(t, pesananLaporan{
			BranchID: l.branchA, Nomor: fmt.Sprintf("2026-10-1%d", i), Tanggal: "2026-10-10", Status: "submitted",
			Baris: []barisLaporan{
				{Jenis: "product", Nama: "Bio Magnetik", Qty: i, Harga: 100000, ProductID: produkID, Serial: l.serialBaru()},
			},
		})
	}

	cookies, csrf := l.saLogin(t)
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		l.srv.URL+"/api/v1/admin/reports/export", `{"status":"submitted"}`, cookies, csrf)
	if status != http.StatusAccepted {
		t.Fatalf("permintaan ekspor harus 202, dapat %d (%v)", status, body)
	}
	jobID := body["data"].(map[string]any)["id"].(string)

	// Worker River sungguhan (peran `--mode=worker` di produksi).
	cfg := config.Config{
		WorkerQueues:    map[string]int{"default": 1},
		JobPollInterval: 100 * time.Millisecond,
		WorkerShutdown:  5 * time.Second,
	}
	wc, err := worker.New(l.pool, cfg, nil, nil, nil, l.svc)
	if err != nil {
		t.Fatalf("buat worker: %v", err)
	}
	runCtx, stop := context.WithCancel(ctx)
	berhenti := make(chan error, 1)
	go func() { berhenti <- wc.Run(runCtx) }()

	var st string
	tenggat := time.Now().Add(60 * time.Second)
	for time.Now().Before(tenggat) {
		if err := l.pool.QueryRow(ctx,
			`SELECT status::text FROM report_jobs WHERE id = $1`, jobID).Scan(&st); err != nil {
			t.Fatalf("baca status pekerjaan: %v", err)
		}
		if st == report.StatusSelesai || st == report.StatusGagal {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if st != report.StatusSelesai {
		t.Fatalf("worker harus menyelesaikan pekerjaan, status terakhir %q", st)
	}

	var jumlahBaris int64
	var namaFile string
	if err := l.pool.QueryRow(ctx,
		`SELECT jumlah_baris, COALESCE(nama_file,'') FROM report_jobs WHERE id = $1`, jobID).Scan(&jumlahBaris, &namaFile); err != nil {
		t.Fatalf("baca hasil pekerjaan: %v", err)
	}
	if jumlahBaris != 3 || namaFile == "" {
		t.Errorf("hasil pekerjaan tidak sesuai: baris=%d nama_file=%q", jumlahBaris, namaFile)
	}

	// River menandai job selesai SESUDAH work dikembalikan, jadi keadaannya
	// dipantau sesaat (bukan dibaca sekali).
	var state string
	tenggatRiver := time.Now().Add(15 * time.Second)
	for time.Now().Before(tenggatRiver) {
		if err := l.pool.QueryRow(ctx,
			`SELECT state FROM river_job WHERE kind = 'report_xlsx' AND args->>'job_id' = $1 ORDER BY id DESC LIMIT 1`,
			jobID).Scan(&state); err != nil {
			t.Fatalf("baca state job River: %v", err)
		}
		if state == "completed" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if state != "completed" {
		t.Errorf("job River harus completed, dapat %q", state)
	}

	// Berkasnya benar-benar bisa diunduh lewat API.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		l.srv.URL+"/api/v1/admin/reports/export/"+jobID+"/download", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("unduh harus 200, dapat %d (%v)", status, body)
	}
	if url := body["data"].(map[string]any)["url"]; url == "" || url == nil {
		t.Errorf("tautan unduh harus terisi: %v", url)
	}

	stop()
	select {
	case err := <-berhenti:
		if err != nil {
			t.Fatalf("worker berhenti dengan error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("worker tidak berhenti setelah context dibatalkan")
	}
}

// TestPGReportEkspor50kBarisTidakOOM mengekspor 50.000 order line sungguhan
// (PRD §9 beban) dan membuktikan pembacaan bertahap: repository dipanggil
// berkali-kali dengan batch kecil, berkas lengkap sampai baris terakhir, dan
// kenaikan heap selama ekspor tetap terbatas (bukan menahan seluruh data).
func TestPGReportEkspor50kBarisTidakOOM(t *testing.T) {
	if testing.Short() {
		t.Skip("uji beban 50.000 baris dilewati pada mode -short")
	}
	l := newLaporanPG(t)
	ctx := context.Background()
	produkID := l.produkA(t)

	const (
		jumlahPesanan   = 200
		barisPerPesanan = 250
		totalBaris      = jumlahPesanan * barisPerPesanan // 50.000
	)

	// Pesanan + baris dibuat massal dalam dua pernyataan supaya uji beban tetap
	// wajar waktunya (trigger recalc per pesanan hanya memindai 250 baris).
	if _, err := l.pool.Exec(ctx, `
		WITH baru AS (
		    INSERT INTO orders (branch_id, is_cnc)
		    SELECT $1, false FROM generate_series(1,$2)
		    RETURNING id
		), bernomor AS (
		    SELECT id, row_number() OVER () AS n FROM baru
		)
		INSERT INTO order_lines (order_id, line_no, jenis, product_id, nama_snapshot, qty, harga_satuan, serial_number)
		SELECT b.id, g.i, 'product', $3, 'Bio Magnetik', 2, 100000,
		       '20261001' || lpad((((b.n - 1) * $4) + g.i)::text, 5, '0')
		  FROM bernomor b, generate_series(1,$4) g(i)`,
		l.branchA, jumlahPesanan, produkID, barisPerPesanan); err != nil {
		t.Fatalf("seed 50.000 baris: %v", err)
	}
	if _, err := l.pool.Exec(ctx, `
		UPDATE orders
		   SET status = 'submitted',
		       nomor = 'MASS-' || replace(id::text, '-', ''),
		       tanggal = '2026-10-01',
		       submitted_at = now()
		 WHERE status = 'draft'`); err != nil {
		t.Fatalf("submit massal: %v", err)
	}

	var dihitung int64
	if err := l.pool.QueryRow(ctx,
		`SELECT count(*) FROM order_lines l JOIN orders o ON o.id = l.order_id WHERE o.status <> 'draft'`).Scan(&dihitung); err != nil {
		t.Fatalf("hitung baris seed: %v", err)
	}
	if dihitung != totalBaris {
		t.Fatalf("seed harus %d baris, dapat %d", totalBaris, dihitung)
	}

	cookies, csrf := l.saLogin(t)
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		l.srv.URL+"/api/v1/admin/reports/export", `{}`, cookies, csrf)
	if status != http.StatusAccepted {
		t.Fatalf("permintaan ekspor harus 202, dapat %d (%v)", status, body)
	}
	jobID := body["data"].(map[string]any)["id"].(string)

	// Heap dipantau selama ekspor: pembacaan bertahap tidak boleh menumpuk
	// seluruh 50.000 baris (apalagi berkasnya) di memori.
	runtime.GC()
	var awal runtime.MemStats
	runtime.ReadMemStats(&awal)
	puncak := awal.HeapAlloc
	selesaiPantau := make(chan struct{})
	var pantau sync.WaitGroup
	pantau.Add(1)
	go func() {
		defer pantau.Done()
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-selesaiPantau:
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > puncak {
					puncak = m.HeapAlloc
				}
			}
		}
	}()

	mulai := time.Now()
	hasil, err := l.svc.Jalankan(ctx, jobID)
	close(selesaiPantau)
	pantau.Wait()
	if err != nil {
		t.Fatalf("ekspor 50.000 baris gagal: %v", err)
	}
	durasi := time.Since(mulai)

	if hasil.Status != report.StatusSelesai || hasil.JumlahBaris != totalBaris {
		t.Fatalf("ekspor harus selesai %d baris, dapat status=%s baris=%d pesan=%s",
			totalBaris, hasil.Status, hasil.JumlahBaris, hasil.Pesan)
	}

	panggilan, maksBatch := l.repo.pola()
	if panggilan < totalBaris/report.BatchEkspor {
		t.Errorf("repository harus dipanggil bertahap minimal %d kali, dapat %d", totalBaris/report.BatchEkspor, panggilan)
	}
	if maksBatch > report.BatchEkspor {
		t.Errorf("batch terbesar tidak boleh melebihi %d, dapat %d", report.BatchEkspor, maksBatch)
	}

	kenaikan := int64(puncak) - int64(awal.HeapAlloc)
	t.Logf("ekspor %d baris: %v, panggilan batch=%d, batch maks=%d, heap awal=%d puncak=%d kenaikan=%d byte",
		totalBaris, durasi, panggilan, maksBatch, awal.HeapAlloc, puncak, kenaikan)
	// Bukti UTAMA bahwa ekspor tidak menahan seluruh data adalah pola pembacaan
	// di atas (101 permintaan × 500 baris) ditambah excelize StreamWriter yang
	// menumpahkan chunk >16 MB ke berkas sementara. Angka heap di bawah adalah
	// pengaman terhadap pemakaian yang benar-benar boros (mis. menahan seluruh
	// berkas di memori); batasnya sengaja longgar karena HeapAlloc ikut
	// menghitung sampah yang belum dikumpulkan GC saat ekspor berjalan.
	const batas = 256 << 20 // 256 MiB
	if kenaikan > batas {
		t.Errorf("kenaikan heap selama ekspor %d byte melebihi batas %d byte", kenaikan, batas)
	}

	// Berkas lengkap sampai baris terakhir (dibaca sebagian saja supaya uji
	// tidak menahan 50.000 baris di memori pengujian).
	raw, err := l.store.Get(ctx, hasil.StorageKey)
	if err != nil {
		t.Fatalf("ambil berkas ekspor: %v", err)
	}
	defer func() { _ = raw.Close() }()
	isi, err := io.ReadAll(raw)
	if err != nil {
		t.Fatalf("baca berkas ekspor: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(isi))
	if err != nil {
		t.Fatalf("buka ekspor: %v", err)
	}
	defer func() { _ = f.Close() }()
	if sheet := f.GetSheetList(); len(sheet) != 2 {
		t.Fatalf("ekspor harus 2 sheet, dapat %v", sheet)
	}
	pertama, err := f.GetCellValue(report.SheetLaporan, "B2")
	if err != nil || pertama == "" {
		t.Errorf("baris data pertama harus terisi: %q (%v)", pertama, err)
	}
	terakhir := fmt.Sprintf("B%d", totalBaris+1)
	isiTerakhir, err := f.GetCellValue(report.SheetLaporan, terakhir)
	if err != nil {
		t.Fatalf("baca baris terakhir %s: %v", terakhir, err)
	}
	if isiTerakhir == "" {
		t.Errorf("baris terakhir %s harus terisi, berarti berkas terpotong", terakhir)
	}
	kosong, err := f.GetCellValue(report.SheetLaporan, fmt.Sprintf("B%d", totalBaris+2))
	if err != nil {
		t.Fatalf("baca baris setelah terakhir: %v", err)
	}
	if kosong != "" {
		t.Errorf("tidak boleh ada baris melebihi %d, dapat %q", totalBaris, kosong)
	}

	// Rekap bulanan satu bulan/ satu cabang menjumlahkan seluruh baris.
	rekap, err := f.GetRows(report.SheetRekap, excelize.Options{RawCellValue: true})
	if err != nil {
		t.Fatalf("baca rekap: %v", err)
	}
	if len(rekap) != 2 {
		t.Fatalf("rekap harus 2 baris (header + 1 bulan/cabang), dapat %v", rekap)
	}
	if rekap[1][2] != fmt.Sprint(totalBaris) {
		t.Errorf("rekap jumlah baris harus %d, dapat %q", totalBaris, rekap[1][2])
	}
	mauTotal := int64(totalBaris) * 2 * 100000
	if rekap[1][4] != fmt.Sprint(mauTotal) {
		t.Errorf("rekap total subtotal harus %d, dapat %q", mauTotal, rekap[1][4])
	}
}
