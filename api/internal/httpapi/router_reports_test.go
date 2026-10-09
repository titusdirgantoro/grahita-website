package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/grahita/portal/api/internal/report"
)

// reportServer adalah router lengkap untuk menguji laporan lewat HTTP:
// penyimpanan auth in-memory, repository laporan in-memory, dan object storage
// in-memory.
type reportServer struct {
	srv  *httptest.Server
	repo *report.Memory
	svc  *report.Service
}

const (
	reportBr1 = "11111111-1111-4111-8111-000000000001"
	reportBr2 = "22222222-2222-4222-8222-000000000002"
)

func newReportServer(t *testing.T) *reportServer {
	t.Helper()

	hash, err := auth.HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}
	store := auth.NewMemory()
	store.PutBranch(auth.Credentials{
		UserID: "usr-a", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: reportBr1,
		Nama: "Grahita Cabang A", Email: "a@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	store.PutAdmin(auth.Credentials{
		UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin,
		Nama: "Rina", Email: "rina@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	store.PutAdmin(auth.Credentials{
		UserID: "adm-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager,
		Nama: "Budi", Email: "budi@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})

	repo := report.NewMemory()
	repo.TambahLines(
		report.Line{
			Tanggal: "2026-10-01", TanggalWaktu: jakartaDate(t, "2026-10-01"), Nomor: "2026-10-01", Versi: 0,
			BranchID: reportBr1, Cabang: "Cabang A", KepalaCabang: "Kepala A", LineNo: 1,
			Produk: "Bio Magnetik", Jenis: "product", SerialNumber: "20261001001",
			Qty: 2, HargaSatuan: 100000, Subtotal: 200000, Status: "submitted",
		},
		report.Line{
			Tanggal: "2026-10-02", TanggalWaktu: jakartaDate(t, "2026-10-02"), Nomor: "2026-10-02", Versi: 1,
			BranchID: reportBr2, Cabang: "Cabang B", KepalaCabang: "Kepala B", LineNo: 1,
			Produk: "Bio Magnetik", Jenis: "product", SerialNumber: "20261002001",
			Qty: 1, HargaSatuan: 200000, Subtotal: 200000, Status: "done",
			TanggalVerifikasi: "2026-10-02 09:30", MetodeVerifikasi: "manual",
		},
	)

	svc := report.NewService(repo, storage.NewMemory("/files"))
	authSvc := auth.NewService(store, store, store, clock.System(), 8*time.Hour, 30*24*time.Hour)
	deps := Deps{
		Config:        testConfig(),
		Version:       "test-report",
		Health:        fakeHealth{migrasi: 9},
		Auth:          authSvc,
		Reports:       svc,
		LoginLimiter:  httpx.NewRateLimiter(50, 15*time.Minute),
		ForgotLimiter: httpx.NewRateLimiter(50, 15*time.Minute),
	}

	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	return &reportServer{srv: srv, repo: repo, svc: svc}
}

func jakartaDate(t *testing.T, v string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation(report.LayoutTanggal, v, clock.Jakarta())
	if err != nil {
		t.Fatalf("tanggal uji %q: %v", v, err)
	}
	return parsed
}

// TestApiReportsOtorisasiSuperadmin memastikan seluruh endpoint laporan
// menolak akun tanpa kapabilitas (PRD §2.1: Manager ditolak, bukan hanya
// menunya disembunyikan) dan tetap 401 tanpa sesi.
func TestApiReportsOtorisasiSuperadmin(t *testing.T) {
	rs := newReportServer(t)
	base := rs.srv.URL + "/api/v1"

	endpoints := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/admin/reports/order-lines", ""},
		{http.MethodPost, "/admin/reports/export", `{}`},
		{http.MethodGet, "/admin/reports/export/00000000-0000-4000-8000-000000000001", ""},
		{http.MethodGet, "/admin/reports/export/00000000-0000-4000-8000-000000000001/download", ""},
	}

	// Tanpa sesi: GET 401; POST ditolak CSRF lebih dahulu (403 CSRF_INVALID).
	for _, e := range endpoints {
		status, body, _ := apiReq(t, http.DefaultClient, e.method, base+e.path, e.body, nil, "")
		if e.method == http.MethodGet {
			if status != http.StatusUnauthorized {
				t.Errorf("tanpa sesi %s %s harus 401, dapat %d", e.method, e.path, status)
			}
			assertErrorShape(t, body, "UNAUTHENTICATED")
			continue
		}
		if status != http.StatusForbidden {
			t.Errorf("tanpa sesi %s %s harus 403 CSRF, dapat %d", e.method, e.path, status)
		}
		assertErrorShape(t, body, "CSRF_INVALID")
	}

	// Cabang & Manager: 403 di SEMUA endpoint laporan.
	cabangCookies, cabangCsrf := login(t, rs.srv, "a@example.test")
	mgrCookies, mgrCsrf := login(t, rs.srv, "budi@example.test")
	for _, aktor := range []struct {
		nama    string
		cookies []*http.Cookie
		csrf    string
	}{{"cabang", cabangCookies, cabangCsrf}, {"manager", mgrCookies, mgrCsrf}} {
		for _, e := range endpoints {
			status, body, _ := apiReq(t, http.DefaultClient, e.method, base+e.path, e.body, aktor.cookies, aktor.csrf)
			if status != http.StatusForbidden {
				t.Errorf("%s %s %s harus 403, dapat %d (%v)", aktor.nama, e.method, e.path, status, body)
			}
		}
	}

	// Superadmin: daftar 200 (dengan isi), ekspor 202.
	saCookies, saCsrf := login(t, rs.srv, "rina@example.test")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/reports/order-lines", "", saCookies, "")
	if status != http.StatusOK {
		t.Fatalf("daftar laporan superadmin harus 200, dapat %d (%v)", status, body)
	}
	page := body["data"].(map[string]any)
	if page["total"] != float64(2) {
		t.Errorf("total baris harus 2, dapat %v", page["total"])
	}
	if len(page["items"].([]any)) != 2 {
		t.Errorf("items harus 2, dapat %v", page["items"])
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, base+"/admin/reports/export", `{}`, saCookies, saCsrf)
	if status != http.StatusAccepted {
		t.Fatalf("permintaan ekspor harus 202, dapat %d (%v)", status, body)
	}
	if id := body["data"].(map[string]any)["id"]; id == nil || id == "" {
		t.Error("jawaban ekspor harus memuat id pekerjaan")
	}
}

// TestApiReportsAlurEksporSampaiUnduh menelusuri alur lengkap: minta ekspor →
// status queued → unduh ditolak (409 REPORT_NOT_READY) → worker mengerjakan →
// status selesai → tautan unduh tersedia.
func TestApiReportsAlurEksporSampaiUnduh(t *testing.T) {
	rs := newReportServer(t)
	base := rs.srv.URL + "/api/v1"
	cookies, csrf := login(t, rs.srv, "rina@example.test")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, base+"/admin/reports/export",
		`{"cabang":["`+reportBr2+`"],"status":"done","cnc":false}`, cookies, csrf)
	if status != http.StatusAccepted {
		t.Fatalf("permintaan ekspor harus 202, dapat %d (%v)", status, body)
	}
	job := body["data"].(map[string]any)
	id := job["id"].(string)
	if job["status"] != "queued" {
		t.Errorf("status awal harus queued, dapat %v", job["status"])
	}
	// Filter tersimpan apa adanya (bentuk normal) dan ikut dikembalikan.
	filter := job["filter"].(map[string]any)
	if filter["status"] != "done" || filter["cnc"] != false {
		t.Errorf("filter tersimpan tidak sesuai: %v", filter)
	}

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/reports/export/"+id, "", cookies, "")
	if status != http.StatusOK || body["data"].(map[string]any)["status"] != "queued" {
		t.Fatalf("status pekerjaan harus queued, dapat %d (%v)", status, body)
	}

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/reports/export/"+id+"/download", "", cookies, "")
	if status != http.StatusConflict {
		t.Fatalf("unduh sebelum selesai harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "REPORT_NOT_READY")

	// Peran worker: kerjakan pekerjaannya di luar HTTP.
	if _, err := rs.svc.Jalankan(t.Context(), id); err != nil {
		t.Fatalf("jalankan ekspor: %v", err)
	}

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/reports/export/"+id, "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("status pekerjaan harus 200, dapat %d", status)
	}
	selesai := body["data"].(map[string]any)
	if selesai["status"] != "selesai" || selesai["jumlah_baris"] != float64(1) {
		t.Fatalf("pekerjaan harus selesai dengan 1 baris, dapat %v", selesai)
	}
	if selesai["nama_file"] == "" || selesai["nama_file"] == nil {
		t.Errorf("nama berkas hasil harus terisi: %v", selesai["nama_file"])
	}

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/reports/export/"+id+"/download", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("unduh setelah selesai harus 200, dapat %d (%v)", status, body)
	}
	dl := body["data"].(map[string]any)
	if dl["url"] == "" || dl["url"] == nil {
		t.Errorf("tautan unduh harus terisi: %v", dl)
	}
	if dl["expires_in"] != float64(300) {
		t.Errorf("masa berlaku tautan harus 300 detik, dapat %v", dl["expires_in"])
	}

	// Jumlah baris ekspor harus sama dengan query terfilter (PRD §9 no.10).
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/reports/order-lines?cabang="+reportBr2+"&status=done", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("daftar laporan harus 200, dapat %d", status)
	}
	if total := body["data"].(map[string]any)["total"]; total != float64(selesai["jumlah_baris"].(float64)) {
		t.Errorf("total daftar %v harus sama dengan jumlah baris ekspor %v", total, selesai["jumlah_baris"])
	}
}

// TestApiReportsFilterTidakValidDitolak memastikan salah ketik filter dijawab
// 400 VALIDATION_FAILED, bukan diam-diam mengabaikan filter.
func TestApiReportsFilterTidakValidDitolak(t *testing.T) {
	rs := newReportServer(t)
	base := rs.srv.URL + "/api/v1"
	cookies, csrf := login(t, rs.srv, "rina@example.test")

	kasus := []string{
		"?dari=08-10-2026",
		"?status=lunas",
		"?jenis=jasa",
		"?cabang=bukan-uuid",
		"?halaman=dua",
		"?cnc=mungkin",
		"?dari=2026-10-31&sampai=2026-10-01",
	}
	for _, q := range kasus {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/reports/order-lines"+q, "", cookies, "")
		if status != http.StatusBadRequest {
			t.Errorf("filter %s harus 400, dapat %d (%v)", q, status, body)
		}
		assertErrorShape(t, body, "VALIDATION_FAILED")
	}

	// Body ekspor dengan tanggal salah juga ditolak.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, base+"/admin/reports/export",
		`{"dari":"kemarin"}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Errorf("body ekspor salah harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
}

// TestApiReportsTanpaLayanan menjawab 503 bila layanan laporan belum terpasang
// (bukan 500 atau diam-diam sukses).
func TestApiReportsTanpaLayanan(t *testing.T) {
	hash, err := auth.HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}
	store := auth.NewMemory()
	store.PutAdmin(auth.Credentials{
		UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin,
		Nama: "Rina", Email: "rina@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	authSvc := auth.NewService(store, store, store, clock.System(), 8*time.Hour, 30*24*time.Hour)
	srv := httptest.NewServer(NewRouter(Deps{
		Config:       testConfig(),
		Version:      "test-tanpa-laporan",
		Health:       fakeHealth{migrasi: 9},
		Auth:         authSvc,
		LoginLimiter: httpx.NewRateLimiter(50, 15*time.Minute),
	}))
	t.Cleanup(srv.Close)

	cookies, _ := login(t, srv, "rina@example.test")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		srv.URL+"/api/v1/admin/reports/order-lines", "", cookies, "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("tanpa layanan laporan harus 503, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "UNAVAILABLE")
}
