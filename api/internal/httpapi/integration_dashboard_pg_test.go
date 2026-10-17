package httpapi

// Uji integrasi dashboard admin (F1-20) pada PostgreSQL NYATA: metrik berbasis
// role, chip "perlu tindakan" yang KONSISTEN dengan daftar terfilternya,
// batas hari bisnis Asia/Jakarta, C&C lewat jatuh tempo yang tetap Submitted,
// dan email gagal setelah retry sungguhan lewat worker outbox.
//
// Data disemai langsung lewat SQL supaya keadaan yang jarang bisa dibuat
// deterministik; satu bagian (email gagal setelah retry) sengaja menempuh
// jalur produksi notification.Service + repository-nya sungguhan.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/notification"
	notificationpg "github.com/grahita/portal/api/internal/notification/postgres"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/mailer"
)

// tolakSender adalah mailer yang selalu gagal — dipakai untuk membuat email
// menempuh retry sampai status `failed`.
type tolakSender struct{}

var errTolakKirim = errors.New("smtp menolak (uji)")

func (tolakSender) Send(context.Context, mailer.Message) error {
	return errTolakKirim
}

func (tolakSender) Health(context.Context) error { return nil }

// dashboardGet melakukan GET satu endpoint dashboard dan mengembalikan status,
// badan mentah, dan bentuk JSON-nya. Badan mentah dipakai untuk memastikan
// kunci `nilai` benar-benar TIDAK ADA pada respons Manager.
func dashboardGet(t *testing.T, srv *httptest.Server, path string, cookies []*http.Cookie, csrf string) (int, []byte, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("menyusun permintaan %s: %v", path, err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("memanggil %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("membaca respons %s: %v", path, err)
	}
	var bentuk map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &bentuk)
	}
	return resp.StatusCode, raw, bentuk
}

// dashboardData mengambil bagian `data` dari respons dan memastikan ada.
func dashboardData(t *testing.T, bentuk map[string]any) map[string]any {
	t.Helper()
	data, ok := bentuk["data"].(map[string]any)
	if !ok {
		t.Fatalf("respons tanpa bagian data: %v", bentuk)
	}
	return data
}

// metrikMap mengambil satu metrik {jumlah,nilai?}.
func metrikMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("metrik bukan objek: %v", v)
	}
	return m
}

func angka(t *testing.T, m map[string]any, kunci string) float64 {
	t.Helper()
	v, ok := m[kunci]
	if !ok {
		return -1
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s bukan angka: %v", kunci, v)
	}
	return f
}

// chipDashboard memetakan kunci chip ke jumlahnya pada respons.
func chipDashboard(t *testing.T, d map[string]any) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	chips, ok := d["chip"].([]any)
	if !ok {
		t.Fatalf("respons tanpa chip: %v", d)
	}
	for _, c := range chips {
		m, _ := c.(map[string]any)
		kunci, _ := m["kunci"].(string)
		jumlah, _ := m["jumlah"].(float64)
		if _, ada := out[kunci]; ada {
			t.Fatalf("kunci chip %q muncul dua kali", kunci)
		}
		out[kunci] = int64(jumlah)
	}
	return out
}

func TestPGDashboardMetrikChipDanRole(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	hariIni := clock.TodayWIB(clock.System())
	awalBulan := time.Date(hariIni.Year(), hariIni.Month(), 1, 0, 0, 0, 0, hariIni.Location())
	produk := insertProdukAdmin(t, ctx, f.pool, "P-20", "Produk Dashboard")

	// Lima pesanan bulan berjalan (tiga cabang) + satu pesanan historis yang
	// TIDAK boleh masuk periode/tren/peringkat, tetapi tetap masuk chip.
	insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchA, nomor: "2026-10-11", tanggal: hariIni.Format("2006-01-02"),
		status: "submitted", verif: "belum", harga: 100000,
	}, produk, "20261011001")
	insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchA, nomor: "2026-10-12", tanggal: hariIni.Format("2006-01-02"),
		status: "done", verif: "cocok", harga: 300000,
	}, produk, "20261012001")
	b1 := insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchB, nomor: "2026-10-13", tanggal: hariIni.Format("2006-01-02"),
		status: "submitted", verif: "belum", cnc: true,
		jt: hariIni.AddDate(0, 0, -7).Format("2006-01-02"), harga: 150000,
	}, produk, "20261013001")
	insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchB, nomor: "2026-10-14", tanggal: hariIni.Format("2006-01-02"),
		status: "submitted", verif: "perlu_review", alasan: "kurang_bayar", harga: 200000,
	}, produk, "20261014001")
	insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchC, nomor: "2026-10-15", tanggal: hariIni.Format("2006-01-02"),
		status: "submitted", verif: "belum", harga: 50000,
	}, produk, "20261015001")
	insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchA, nomor: "2020-01-01", tanggal: "2020-01-15",
		status: "submitted", verif: "belum", harga: 999000,
	}, produk, "20200115001")

	acMgr, csrfMgr := login(t, f.srv, "budi@example.test")
	acSA, csrfSA := login(t, f.srv, "rina@example.test")
	acCabang, csrfCabang := login(t, f.srv, "a@example.test")

	// --- Otorisasi ----------------------------------------------------------
	st, body, _ := dashboardGet(t, f.srv, "/api/v1/admin/dashboard", acCabang, csrfCabang)
	if st != http.StatusForbidden {
		t.Fatalf("akun cabang harus 403, dapat %d (%v)", st, body)
	}
	for _, path := range []string{"/api/v1/admin/dashboard?periode=dekade", "/api/v1/admin/dashboard?cabang=bukan-uuid"} {
		st, body, _ = dashboardGet(t, f.srv, path, acMgr, csrfMgr)
		if st != http.StatusBadRequest {
			t.Fatalf("filter tak dikenal pada %s harus 400, dapat %d (%v)", path, st, body)
		}
	}

	// --- Manager: jumlah saja, tanpa rupiah ---------------------------------
	st, raw, bentuk := dashboardGet(t, f.srv, "/api/v1/admin/dashboard", acMgr, csrfMgr)
	if st != http.StatusOK {
		t.Fatalf("dashboard Manager harus 200, dapat %d (%v)", st, raw)
	}
	d := dashboardData(t, bentuk)
	if containsKey(raw, "nilai") {
		t.Fatalf("respons Manager TIDAK boleh memuat kunci nilai: %s", raw)
	}

	filter := d["filter"].(map[string]any)
	if filter["periode"] != "bulan" || filter["zona"] != "Asia/Jakarta" {
		t.Fatalf("filter bawaan salah: %v", filter)
	}
	if filter["dari"] != awalBulan.Format("2006-01-02") || filter["sampai"] != hariIni.Format("2006-01-02") ||
		filter["hari_ini"] != hariIni.Format("2006-01-02") {
		t.Fatalf("rentang bulan berjalan salah: %v", filter)
	}

	ringkasan := metrikMap(t, d["ringkasan"])
	if angka(t, ringkasan, "jumlah") != 5 {
		t.Fatalf("ringkasan jumlah bulan ini harus 5 (historis 2020 di luar periode), dapat %v", ringkasan)
	}
	if angka(t, ringkasan, "nilai") != -1 {
		t.Fatalf("Manager tidak boleh menerima nilai: %v", ringkasan)
	}

	pay := d["pembayaran"].(map[string]any)
	for nama, mau := range map[string]float64{"terverifikasi": 1, "menunggu": 3, "perlu_review": 1} {
		if got := angka(t, metrikMap(t, pay[nama]), "jumlah"); got != mau {
			t.Fatalf("pembayaran.%s harus %v, dapat %v", nama, mau, got)
		}
	}

	// --- Chip + konsistensi dengan daftar terfilter -------------------------
	chip := chipDashboard(t, d)
	mauChip := map[string]int64{
		"menunggu":          4, // a1, b1, c1, historis (chip TIDAK dibatasi periode)
		"perlu_review":      1, // b2
		"lewat_jatuh_tempo": 1, // b1 (C&C lewat tempo, masih submitted)
		"email_gagal":       0,
	}
	for kunci, mau := range mauChip {
		if chip[kunci] != mau {
			t.Fatalf("chip %s harus %d, dapat %d (%v)", kunci, mau, chip[kunci], chip)
		}
	}

	padanan := map[string]string{
		"menunggu":          "status=submitted&verif=belum",
		"perlu_review":      "verif=perlu_review",
		"lewat_jatuh_tempo": "terlambat=true",
		"email_gagal":       "email_gagal=true",
	}
	for kunci, query := range padanan {
		for _, cabang := range []string{"", "&cabang=" + f.branchA, "&cabang=" + f.branchB} {
			mau := chip[kunci]
			if cabang != "" {
				// Pembanding per cabang dihitung langsung dari SQL lewat
				// klausa yang sama dengan daftar riwayat — independen dari
				// endpoint dashboard.
				mau = chipCabang(t, ctx, f, kunci, cabang[len("&cabang="):])
			}
			st, _, bentukDaftarResp := dashboardGet(t, f.srv, "/api/v1/admin/orders?"+query+cabang, acMgr, csrfMgr)
			if st != http.StatusOK {
				t.Fatalf("daftar %s%s harus 200, dapat %d", query, cabang, st)
			}
			if total := int64(angka(t, bentukDaftar(t, bentukDaftarResp), "total")); total != mau {
				t.Fatalf("chip %s (%d) tidak sama dengan total daftar %q%s (%d)", kunci, mau, query, cabang, total)
			}
		}
	}

	// --- Chip tidak dibatasi periode ---------------------------------------
	for _, periode := range []string{"hari", "minggu", "tahun"} {
		_, _, bentuk := dashboardGet(t, f.srv, "/api/v1/admin/dashboard?periode="+periode, acMgr, csrfMgr)
		dc := dashboardData(t, bentuk)
		if got := chipDashboard(t, dc)["menunggu"]; got != 4 {
			t.Fatalf("chip menunggu pada periode %s harus 4 (tidak dibatasi periode), dapat %d", periode, got)
		}
	}

	// --- Tren 12 bulan & harian --------------------------------------------
	tren, _ := d["tren_bulanan"].([]any)
	if len(tren) != 12 {
		t.Fatalf("tren_bulanan harus 12 batang, dapat %d", len(tren))
	}
	bulanIni := tren[hariIni.Month()-1].(map[string]any)
	if bulanIni["kunci"] != hariIni.Format("2006-01") || angka(t, bulanIni, "jumlah") != 5 {
		t.Fatalf("batang bulan berjalan salah: %v", bulanIni)
	}
	var totalTahun float64
	for _, titik := range tren {
		totalTahun += angka(t, titik.(map[string]any), "jumlah")
	}
	if totalTahun != 5 {
		t.Fatalf("jumlah tren setahun harus 5 (historis 2020 di luar tahun), dapat %v", totalTahun)
	}
	harian, _ := d["harian"].([]any)
	if len(harian) != hariIni.Day() {
		t.Fatalf("harian harus %d titik (tanggal 1..hari ini), dapat %d", hariIni.Day(), len(harian))
	}
	hariIniTitik := harian[len(harian)-1].(map[string]any)
	if hariIniTitik["kunci"] != hariIni.Format("2006-01-02") || angka(t, hariIniTitik, "jumlah") != 5 {
		t.Fatalf("titik harian terakhir salah: %v", hariIniTitik)
	}

	// --- Peringkat cabang (urut jumlah lalu nilai) --------------------------
	peringkat, _ := d["peringkat_cabang"].([]any)
	if len(peringkat) != 3 {
		t.Fatalf("peringkat harus 3 cabang, dapat %d", len(peringkat))
	}
	pertama := peringkat[0].(map[string]any)
	kedua := peringkat[1].(map[string]any)
	if pertama["branch_id"] != f.branchA || angka(t, pertama, "jumlah") != 2 {
		t.Fatalf("peringkat pertama harus cabang A (2 pesanan, nilai terbesar): %v", pertama)
	}
	if kedua["branch_id"] != f.branchB || angka(t, kedua, "jumlah") != 2 {
		t.Fatalf("peringkat kedua harus cabang B (seri jumlah, nilai lebih kecil): %v", kedua)
	}

	// --- Kartu prototype: item, pembanding, komposisi, product terlaris -----
	// Lima pesanan bulan ini masing-masing satu baris produk qty 1.
	if got := angka(t, d, "item"); got != 5 {
		t.Fatalf("item periode harus 5 (Σ qty baris produk), dapat %v", got)
	}
	if got := angka(t, bulanIni, "terverifikasi"); got != 1 {
		t.Fatalf("batang bulan berjalan harus mencatat 1 terverifikasi, dapat %v", got)
	}
	pembanding := d["pembanding"].(map[string]any)
	awalBulanLalu := awalBulan.AddDate(0, -1, 0)
	if pembanding["dari"] != awalBulanLalu.Format("2006-01-02") ||
		angka(t, metrikMap(t, pembanding["ringkasan"]), "jumlah") != 0 || angka(t, pembanding, "item") != 0 {
		t.Fatalf("pembanding bulan lalu salah (historis 2020 di luar rentang): %v", pembanding)
	}
	if angka(t, metrikMap(t, d["bulan_lalu"]), "jumlah") != 0 || angka(t, d, "harian_pembanding") != 0 {
		t.Fatalf("bulan lalu harus kosong: bulan_lalu=%v harian_pembanding=%v", d["bulan_lalu"], d["harian_pembanding"])
	}
	komposisi := d["komposisi"].(map[string]any)
	for nama, mau := range map[string]float64{"reguler": 4, "cnc": 1, "sertifikat": 0, "cnc_lewat_tempo": 1} {
		if got := angka(t, komposisi, nama); got != mau {
			t.Fatalf("komposisi.%s harus %v, dapat %v", nama, mau, got)
		}
	}
	terlaris, _ := d["produk_terlaris"].([]any)
	if len(terlaris) != 1 || terlaris[0].(map[string]any)["product_id"] != produk || angka(t, terlaris[0].(map[string]any), "qty") != 5 {
		t.Fatalf("product terlaris harus Produk Dashboard qty 5, dapat %v", terlaris)
	}

	// --- Filter satu cabang -------------------------------------------------
	_, _, bentukA := dashboardGet(t, f.srv, "/api/v1/admin/dashboard?cabang="+f.branchA, acMgr, csrfMgr)
	dA := dashboardData(t, bentukA)
	if dA["filter"].(map[string]any)["cabang"] != f.branchA {
		t.Fatalf("filter cabang tidak diakui: %v", dA["filter"])
	}
	if angka(t, metrikMap(t, dA["ringkasan"]), "jumlah") != 2 {
		t.Fatalf("ringkasan cabang A harus 2, dapat %v", dA["ringkasan"])
	}
	chipA := chipDashboard(t, dA)
	if chipA["menunggu"] != 2 || chipA["perlu_review"] != 0 || chipA["lewat_jatuh_tempo"] != 0 {
		t.Fatalf("chip cabang A salah: %v", chipA)
	}

	// --- C&C lewat jatuh tempo TETAP Submitted ------------------------------
	var status string
	if err := f.pool.QueryRow(ctx, `SELECT status::text FROM orders WHERE id = $1`, b1).Scan(&status); err != nil {
		t.Fatalf("membaca status pesanan C&C: %v", err)
	}
	if status != "submitted" {
		t.Fatalf("C&C lewat jatuh tempo harus tetap submitted, dapat %q", status)
	}

	// --- Superadmin: nilai rupiah ada --------------------------------------
	st, rawSA, bentukSA := dashboardGet(t, f.srv, "/api/v1/admin/dashboard", acSA, csrfSA)
	if st != http.StatusOK {
		t.Fatalf("dashboard Superadmin harus 200, dapat %d (%v)", st, rawSA)
	}
	if !containsKey(rawSA, "nilai") {
		t.Fatalf("respons Superadmin harus memuat kunci nilai: %s", rawSA)
	}
	dSA := dashboardData(t, bentukSA)
	if got := angka(t, metrikMap(t, dSA["ringkasan"]), "nilai"); got != 800000 {
		t.Fatalf("nilai ringkasan Superadmin harus 800000, dapat %v", got)
	}
	paySA := dSA["pembayaran"].(map[string]any)
	if got := angka(t, metrikMap(t, paySA["terverifikasi"]), "nilai"); got != 300000 {
		t.Fatalf("nilai terverifikasi harus 300000, dapat %v", got)
	}
	if got := angka(t, metrikMap(t, paySA["menunggu"]), "nilai"); got != 300000 {
		t.Fatalf("nilai menunggu harus 300000, dapat %v", got)
	}
	perSA := dSA["peringkat_cabang"].([]any)
	if got := angka(t, perSA[0].(map[string]any), "nilai"); got != 400000 {
		t.Fatalf("nilai peringkat pertama harus 400000, dapat %v", got)
	}
}

// bentukDaftar mengekstrak peta respons daftar pesanan yang sudah di-parse.
func bentukDaftar(t *testing.T, bentuk map[string]any) map[string]any {
	t.Helper()
	data, ok := bentuk["data"].(map[string]any)
	if !ok {
		t.Fatalf("daftar tanpa bagian data: %v", bentuk)
	}
	return data
}

// chipCabang menghitung ulang angka chip satu cabang langsung lewat SQL,
// memakai klausa yang sama dengan daftar riwayat (bukan lewat endpoint
// dashboard) supaya pembandingnya benar-benar independen.
func chipCabang(t *testing.T, ctx context.Context, f *pgFixture, kunci, branchID string) int64 {
	t.Helper()
	kondisi := map[string]string{
		"menunggu":          "o.status = 'submitted' AND o.verif_status = 'belum'",
		"perlu_review":      "o.verif_status = 'perlu_review'",
		"lewat_jatuh_tempo": "o.is_cnc AND o.status <> 'done' AND o.jatuh_tempo < (now() AT TIME ZONE 'Asia/Jakarta')::date",
		// email_gagal dihitung dari outbox: satu-satunya pesanan cabang yang
		// punya email `failed` pada uji ini dibuat belakangan, sebelum itu nol.
		"email_gagal": "EXISTS (SELECT 1 FROM email_outbox e WHERE e.order_id = o.id AND e.status = 'failed')",
	}[kunci]
	var jumlah int64
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*)::bigint FROM orders o WHERE o.branch_id = $1 AND `+kondisi, branchID).Scan(&jumlah); err != nil {
		t.Fatalf("menghitung chip %s cabang %s: %v", kunci, branchID, err)
	}
	return jumlah
}

// containsKey melaporkan apakah JSON memuat kunci tertentu (bentuk `"kunci":`).
func containsKey(raw []byte, kunci string) bool {
	var bentuk map[string]any
	if err := json.Unmarshal(raw, &bentuk); err != nil {
		return false
	}
	return cariKunci(bentuk, kunci)
}

func cariKunci(v any, kunci string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, isi := range t {
			if k == kunci || cariKunci(isi, kunci) {
				return true
			}
		}
	case []any:
		for _, isi := range t {
			if cariKunci(isi, kunci) {
				return true
			}
		}
	}
	return false
}

// TestPGDashboardEmailGagalSetelahRetry menempuh jalur produksi outbox:
// email diantre, gagal, dijadwalkan ulang (retry), gagal lagi sampai jatahnya
// habis, lalu muncul sebagai chip "email gagal" di dashboard.
func TestPGDashboardEmailGagalSetelahRetry(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	hariIni := clock.TodayWIB(clock.System())
	produk := insertProdukAdmin(t, ctx, f.pool, "P-21", "Produk Email")
	order := insertOrderAdmin(t, ctx, f.pool, seedOrderAdmin{
		branchID: f.branchA, nomor: "2026-10-21", tanggal: hariIni.Format("2006-01-02"),
		status: "submitted", verif: "belum", harga: 120000,
	}, produk, "20261021001")

	// Jam uji dimulai sedikit di depan `now()` database: baris outbox baru
	// memakai next_try_at dari jam database, jadi tanpa selisih ini klaim
	// pertama bisa saja belum memenuhi syarat `next_try_at <= now`.
	jam := clock.Fixed(time.Now().UTC().Add(5 * time.Minute))
	kirim := notification.New(notificationpg.New(f.pool), tolakSender{},
		notification.WithClock(jam),
		notification.WithConfig(notification.Config{MaxAttempts: 2}),
	)
	email, err := kirim.Enqueue(ctx, notification.Envelope{
		OrderID: order,
		Jenis:   notification.JenisInvoiceCabang,
		To:      []string{"cabang-a@example.test"},
		Subject: "Invoice",
		Payload: map[string]any{"template": notification.JenisInvoiceCabang},
	})
	if err != nil {
		t.Fatalf("mengantre email: %v", err)
	}

	// Percobaan pertama: gagal -> dijadwalkan ulang (retry).
	hasil, err := kirim.ProsesAntrean(ctx, 10)
	if err != nil {
		t.Fatalf("pemindaian pertama: %v", err)
	}
	if hasil.Ditunda != 1 || hasil.Menyerah != 0 {
		t.Fatalf("percobaan pertama harus menunda (retry), dapat %+v", hasil)
	}

	// Waktu dimajukan melewati jeda retry, lalu percobaan kedua menghabiskan
	// jatah: status menjadi `failed`.
	jam.Set(time.Now().UTC().Add(30 * time.Minute))
	hasil, err = kirim.ProsesAntrean(ctx, 10)
	if err != nil {
		t.Fatalf("pemindaian kedua: %v", err)
	}
	if hasil.Menyerah != 1 {
		t.Fatalf("percobaan kedua harus menyerah, dapat %+v", hasil)
	}

	var status string
	var percobaan int
	if err := f.pool.QueryRow(ctx,
		`SELECT status::text, attempts FROM email_outbox WHERE id = $1`, email.ID).Scan(&status, &percobaan); err != nil {
		t.Fatalf("membaca baris email: %v", err)
	}
	if status != notification.StatusFailed || percobaan != 2 {
		t.Fatalf("email harus failed setelah %d percobaan, dapat %s/%d", 2, status, percobaan)
	}

	// Chip dashboard memuatnya, dan daftar terfilternya konsisten.
	acMgr, csrfMgr := login(t, f.srv, "budi@example.test")
	st, raw, bentuk := dashboardGet(t, f.srv, "/api/v1/admin/dashboard", acMgr, csrfMgr)
	if st != http.StatusOK {
		t.Fatalf("dashboard harus 200, dapat %d (%v)", st, raw)
	}
	if got := chipDashboard(t, dashboardData(t, bentuk))["email_gagal"]; got != 1 {
		t.Fatalf("chip email gagal harus 1, dapat %d", got)
	}
	st, body, bentukDaftarResp := dashboardGet(t, f.srv, "/api/v1/admin/orders?email_gagal=true", acMgr, csrfMgr)
	if st != http.StatusOK {
		t.Fatalf("daftar email gagal harus 200, dapat %d (%v)", st, body)
	}
	if total := angka(t, bentukDaftar(t, bentukDaftarResp), "total"); total != 1 {
		t.Fatalf("daftar email_gagal=true harus 1 baris, dapat %v", total)
	}
}
