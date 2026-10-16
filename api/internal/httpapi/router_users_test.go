package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
	"github.com/grahita/portal/api/internal/users"
)

// fakeUsersQueue menangkap email yang diantre pengelolaan pengguna sehingga
// tautan undangan bisa diperiksa pengujian tanpa mengirim email sungguhan.
type fakeUsersQueue struct {
	mu     sync.Mutex
	queued []users.QueuedEmail
}

func (f *fakeUsersQueue) Enqueue(_ context.Context, m users.QueuedEmail) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued = append(f.queued, m)
	return nil
}

func (f *fakeUsersQueue) terakhir(t *testing.T) users.QueuedEmail {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queued) == 0 {
		t.Fatal("tidak ada email yang diantre")
	}
	return f.queued[len(f.queued)-1]
}

// usersServer adalah router lengkap untuk menguji User management lewat HTTP:
// penyimpanan pengguna in-memory, penyimpanan auth in-memory, antrean email
// palsu, dan mailer tautan reset palsu.
type usersServer struct {
	srv       *httptest.Server
	authStore *auth.Memory
	repo      *users.Memory
	queue     *fakeUsersQueue
	mail      *fakeResetMailer
}

func newUsersServer(t *testing.T, tune func(*Deps)) *usersServer {
	t.Helper()

	hash, err := auth.HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}

	authStore := auth.NewMemory()
	authStore.PutBranch(auth.Credentials{
		UserID: "usr-a", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-a",
		Nama: "Grahita Cabang A", Email: "a@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	authStore.PutBranch(auth.Credentials{
		UserID: "usr-b", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-b",
		Nama: "Grahita Cabang B", Email: "b@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	// Cabang C nonaktif: akunnya ada tetapi tidak boleh login.
	authStore.PutBranch(auth.Credentials{
		UserID: "usr-c", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-c",
		Nama: "Grahita Cabang C", Email: "c@example.test", PasswordHash: hash, Active: false, IsActive: true,
	})
	authStore.PutAdmin(auth.Credentials{
		UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin,
		Nama: "Rina", Email: "rina@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	authStore.PutAdmin(auth.Credentials{
		UserID: "adm-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager,
		Nama: "Budi", Email: "budi@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})

	aktifSejak := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := users.NewMemory()
	repo.PutBranch(users.BranchUser{BranchID: "br-a", UserID: "usr-a", Nama: "Grahita Cabang A", NamaKepala: "Kepala A", Email: "a@example.test", IdentityStatus: "registered", IsActive: true, BranchActive: true})
	repo.PutBranch(users.BranchUser{BranchID: "br-b", UserID: "usr-b", Nama: "Grahita Cabang B", NamaKepala: "Kepala B", Email: "b@example.test", IdentityStatus: "registered", IsActive: true, BranchActive: true})
	repo.PutBranch(users.BranchUser{BranchID: "br-c", UserID: "usr-c", Nama: "Grahita Cabang C", NamaKepala: "Kepala C", Email: "c@example.test", IdentityStatus: "registered", IsActive: true, BranchActive: false})
	repo.PutBranch(users.BranchUser{BranchID: "br-d", Nama: "Grahita Cabang D", NamaKepala: "Kepala D", Email: "d@example.test", BranchActive: true, TokenPrefix: "GRH-1234", TokenExpiresAt: ptr(waktuUjiTambah(12))})
	repo.PutAdmin(users.AdminUser{ID: "adm-sa", Nama: "Rina", Email: "rina@example.test", Role: auth.RoleSuperadmin, IsActive: true, ActivatedAt: &aktifSejak, NotifInvoice: true})
	repo.PutAdmin(users.AdminUser{ID: "adm-mgr", Nama: "Budi", Email: "budi@example.test", Role: auth.RoleManager, IsActive: true, ActivatedAt: &aktifSejak, NotifSuratJalan: true})

	// Admin hasil undangan harus ada di KEDUA penyimpanan: daftar akun dibaca
	// dari repo pengguna, sedangkan aktivasi/login lewat paket auth.
	repo.OnAdminCreated = func(a users.AdminUser) {
		authStore.PutAdmin(auth.Credentials{
			UserID: a.ID, Kind: auth.KindAdmin, Role: a.Role, Nama: a.Nama, Email: a.Email,
			PasswordHash: "", Active: false, IsActive: true,
		})
	}

	svc := auth.NewService(authStore, authStore, authStore, clock.System(), 8*time.Hour, 30*24*time.Hour)
	mail := &fakeResetMailer{}
	svc.SetResetMailer(mail)
	queue := &fakeUsersQueue{}
	usersSvc := users.NewService(repo, svc, svc,
		users.WithEmailQueue(queue),
		users.WithResetURLBuilder(func(kind auth.Kind, token string) string {
			return "https://admin.example.test/lupa?token=" + token
		}),
	)

	deps := Deps{
		Config:        testConfig(),
		Version:       "test-users",
		Health:        fakeHealth{migrasi: 7},
		Auth:          svc,
		Settings:      settings.NewService(&fakeSettingsRepo{values: map[string]string{"reset_menit": "60", "token_hari": "14"}}),
		Catalog:       catalog.NewService(fakeBranchCatalog{Memory: catalog.NewMemory()}),
		Users:         usersSvc,
		LoginLimiter:  httpx.NewRateLimiter(5, 15*time.Minute),
		ForgotLimiter: httpx.NewRateLimiter(5, 15*time.Minute),
	}
	if tune != nil {
		tune(&deps)
	}

	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	return &usersServer{srv: srv, authStore: authStore, repo: repo, queue: queue, mail: mail}
}

func ptr[T any](v T) *T { return &v }

func waktuUjiTambah(hari int) time.Time {
	return time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC).AddDate(0, 0, hari)
}

// tokenDariTautan mengambil token dari tautan aktivasi pada email undangan.
func tokenDariTautan(t *testing.T, tautan string) string {
	t.Helper()
	idx := strings.Index(tautan, "token=")
	if idx < 0 {
		t.Fatalf("tautan tidak memuat token: %q", tautan)
	}
	return tautan[idx+len("token="):]
}

func TestApiUserManagementOtorisasiPeran(t *testing.T) {
	us := newUsersServer(t, nil)

	kasus := []struct {
		nama   string
		email  string
		path   string
		status int
	}{
		{"manager boleh melihat akun cabang", "budi@example.test", "/api/v1/admin/users/branch", http.StatusOK},
		{"manager ditolak pada akun admin", "budi@example.test", "/api/v1/admin/users/admin", http.StatusForbidden},
		{"superadmin boleh melihat akun admin", "rina@example.test", "/api/v1/admin/users/admin", http.StatusOK},
		{"superadmin boleh melihat akun cabang", "rina@example.test", "/api/v1/admin/users/branch", http.StatusOK},
		{"akun cabang ditolak pada akun cabang", "a@example.test", "/api/v1/admin/users/branch", http.StatusForbidden},
		{"akun cabang ditolak pada akun admin", "a@example.test", "/api/v1/admin/users/admin", http.StatusForbidden},
	}
	for _, k := range kasus {
		cookies, _ := login(t, us.srv, k.email)
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, us.srv.URL+k.path, "", cookies, "")
		if status != k.status {
			t.Errorf("%s: dapat %d, mau %d (%v)", k.nama, status, k.status, body)
		}
		if k.status == http.StatusForbidden {
			assertErrorShape(t, body, "FORBIDDEN")
		}
	}

	// Tanpa sesi: 401.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, us.srv.URL+"/api/v1/admin/users/branch", "", nil, "")
	if status != http.StatusUnauthorized {
		t.Errorf("tanpa sesi harus 401, dapat %d", status)
	}
	assertErrorShape(t, body, "UNAUTHENTICATED")

	// Manager tidak boleh mengundang admin (mutasi ikut dijaga kapabilitas).
	cookiesMgr, csrfMgr := login(t, us.srv, "budi@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, us.srv.URL+"/api/v1/admin/users/admin",
		`{"nama":"X","email":"x@example.test","role":"manager"}`, cookiesMgr, csrfMgr)
	if status != http.StatusForbidden {
		t.Errorf("manager tidak boleh mengundang admin, dapat %d (%v)", status, body)
	}
}

func TestApiDaftarAkunCabangStatusDanLoginTerakhir(t *testing.T) {
	us := newUsersServer(t, nil)
	cookies, _ := login(t, us.srv, "budi@example.test")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, us.srv.URL+"/api/v1/admin/users/branch", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("daftar akun cabang harus 200, dapat %d (%v)", status, body)
	}
	list, ok := body["data"].([]any)
	if !ok || len(list) != 4 {
		t.Fatalf("harus ada 4 baris cabang, dapat %v", body["data"])
	}
	perBranch := map[string]map[string]any{}
	for _, item := range list {
		row := item.(map[string]any)
		perBranch[row["branch_id"].(string)] = row
	}
	if perBranch["br-a"]["status"] != "aktif" {
		t.Errorf("br-a harus aktif, dapat %v", perBranch["br-a"]["status"])
	}
	if perBranch["br-c"]["status"] != "nonaktif" {
		t.Errorf("br-c (cabang nonaktif) harus nonaktif, dapat %v", perBranch["br-c"]["status"])
	}
	if perBranch["br-d"]["status"] != "belum_registrasi" || perBranch["br-d"]["has_account"] != false {
		t.Errorf("br-d harus belum registrasi, dapat %v", perBranch["br-d"])
	}
	if perBranch["br-d"]["token_prefix"] != "GRH-1234" {
		t.Errorf("token prefix cabang belum registrasi harus tampil, dapat %v", perBranch["br-d"]["token_prefix"])
	}
	if _, ada := perBranch["br-a"]["last_login_at"]; !ada {
		t.Errorf("baris akun harus memuat kolom last_login_at (boleh null): %v", perBranch["br-a"])
	}
}

func TestApiNonaktifkanAkunCabangMencabutSesi(t *testing.T) {
	us := newUsersServer(t, nil)

	// Sesi cabang A aktif lebih dulu.
	cookiesA, _ := login(t, us.srv, "a@example.test")
	status, _, _ := apiReq(t, http.DefaultClient, http.MethodGet, us.srv.URL+"/api/v1/auth/me", "", cookiesA, "")
	if status != http.StatusOK {
		t.Fatalf("prasyarat: sesi cabang A harus hidup, dapat %d", status)
	}

	cookiesSA, csrfSA := login(t, us.srv, "rina@example.test")

	// Tanpa CSRF ditolak.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, us.srv.URL+"/api/v1/admin/users/branch/usr-a",
		`{"is_active":false}`, cookiesSA, "")
	if status != http.StatusForbidden {
		t.Fatalf("tanpa CSRF harus 403, dapat %d", status)
	}
	assertErrorShape(t, body, "CSRF_INVALID")

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, us.srv.URL+"/api/v1/admin/users/branch/usr-a",
		`{"is_active":false}`, cookiesSA, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("nonaktifkan akun harus 200, dapat %d (%v)", status, body)
	}
	dataRow := body["data"].(map[string]any)
	if dataRow["is_active"] != false || dataRow["status"] != "nonaktif" {
		t.Errorf("respons harus menunjukkan akun nonaktif, dapat %v", dataRow)
	}

	// Sesi cabang A langsung gugur (dicabut saat dinonaktifkan).
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, us.srv.URL+"/api/v1/auth/me", "", cookiesA, "")
	if status != http.StatusUnauthorized {
		t.Errorf("sesi akun yang dinonaktifkan harus 401, dapat %d", status)
	}

	// Akun tak dikenal → 404.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, us.srv.URL+"/api/v1/admin/users/branch/tidak-ada",
		`{"is_active":true}`, cookiesSA, csrfSA)
	if status != http.StatusNotFound {
		t.Errorf("akun tak dikenal harus 404, dapat %d (%v)", status, body)
	}

	// Body tanpa is_active ditolak sebagai validasi.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, us.srv.URL+"/api/v1/admin/users/branch/usr-a",
		`{}`, cookiesSA, csrfSA)
	if status != http.StatusBadRequest {
		t.Errorf("body tanpa is_active harus 400, dapat %d (%v)", status, body)
	}
}

func TestApiKirimUlangResetCabang(t *testing.T) {
	us := newUsersServer(t, nil)
	cookiesMgr, csrfMgr := login(t, us.srv, "budi@example.test")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, us.srv.URL+"/api/v1/admin/users/branch/usr-b/reset",
		"", cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("kirim ulang reset harus 200, dapat %d (%v)", status, body)
	}
	if us.mail.jumlah() != 1 {
		t.Fatalf("harus ada 1 email reset terkirim, dapat %d", us.mail.jumlah())
	}

	// Cabang belum registrasi tidak punya akun → 404 untuk id yang tak dikenal.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, us.srv.URL+"/api/v1/admin/users/branch/tidak-ada/reset",
		"", cookiesMgr, csrfMgr)
	if status != http.StatusNotFound {
		t.Errorf("akun tak dikenal harus 404, dapat %d (%v)", status, body)
	}

	// Akun/cabang nonaktif tidak boleh memegang tautan → 409 ACCOUNT_INACTIVE.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, us.srv.URL+"/api/v1/admin/users/branch/usr-c/reset",
		"", cookiesMgr, csrfMgr)
	if status != http.StatusConflict {
		t.Fatalf("reset akun nonaktif harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "ACCOUNT_INACTIVE")
}

func TestApiUndangAdminLaluAktivasi(t *testing.T) {
	us := newUsersServer(t, nil)
	cookiesSA, csrfSA := login(t, us.srv, "rina@example.test")

	// Undang admin baru (flag invoice menyala sejak awal).
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, us.srv.URL+"/api/v1/admin/users/admin",
		`{"nama":"Dewi Lestari","email":"dewi@example.test","role":"manager","notif_invoice":true}`,
		cookiesSA, csrfSA)
	if status != http.StatusCreated {
		t.Fatalf("undang admin harus 201, dapat %d (%v)", status, body)
	}
	admin := body["data"].(map[string]any)
	if admin["status"] != "undangan" {
		t.Errorf("admin baru harus berstatus undangan, dapat %v", admin["status"])
	}
	if admin["notif_invoice"] != true {
		t.Errorf("flag invoice harus tersimpan, dapat %v", admin["notif_invoice"])
	}

	// Email undangan diantre (bukan dikirim langsung) dan memuat tautan.
	antre := us.queue.terakhir(t)
	if antre.Jenis != users.JenisUndanganAdmin {
		t.Fatalf("jenis email harus undangan_admin, dapat %q", antre.Jenis)
	}
	tautan, _ := antre.Payload["tautan"].(string)
	token := tokenDariTautan(t, tautan)

	// Admin undangan belum bisa login (belum punya kata sandi).
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, us.srv.URL+"/api/v1/auth/login",
		`{"email":"dewi@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("admin undangan harus 401, dapat %d (%v)", status, body)
	}

	// Aktivasi lewat tautan undangan.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, us.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+token+`","password":"sandibaru123"}`, nil, "")
	if status != http.StatusNoContent {
		t.Fatalf("aktivasi harus 204, dapat %d (%v)", status, body)
	}

	// Sekarang admin bisa login.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, us.srv.URL+"/api/v1/auth/login",
		`{"email":"dewi@example.test","password":"sandibaru123"}`, nil, "")
	if status != http.StatusOK {
		t.Errorf("admin teraktivasi harus bisa login, dapat %d", status)
	}
	// Tautan aktivasi sekali pakai: percobaan kedua ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, us.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+token+`","password":"sandilain123"}`, nil, "")
	if status != http.StatusBadRequest {
		t.Errorf("tautan aktivasi terpakai harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "RESET_INVALID")
	// Status "undangan" → "aktif" pada daftar diperiksa pada uji integrasi
	// PostgreSQL (integration_users_pg_test.go) yang membaca satu tabel nyata.

	// Email ganda ditolak 409 EMAIL_TAKEN.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, us.srv.URL+"/api/v1/admin/users/admin",
		`{"nama":"Dewi Lain","email":"dewi@example.test","role":"manager"}`, cookiesSA, csrfSA)
	if status != http.StatusConflict {
		t.Fatalf("email ganda harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "EMAIL_TAKEN")
}

// TestApiUndangAdminEmailCabangDitolak memastikan POST /admin/users/admin
// menolak email yang sudah dipakai cabang — email login akun cabang maupun email
// cabang yang belum registrasi — bukan hanya email admin lain (kebalikan dari
// pemeriksaan F1-06 pada master cabang).
func TestApiUndangAdminEmailCabangDitolak(t *testing.T) {
	us := newUsersServer(t, nil)
	cookiesSA, csrfSA := login(t, us.srv, "rina@example.test")

	// "a@example.test" = email akun cabang A, "d@example.test" = email cabang D
	// yang belum registrasi.
	for _, email := range []string{"a@example.test", "d@example.test"} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, us.srv.URL+"/api/v1/admin/users/admin",
			`{"nama":"Bentrok","email":"`+email+`","role":"manager"}`, cookiesSA, csrfSA)
		if status != http.StatusConflict {
			t.Fatalf("email cabang %s harus 409, dapat %d (%v)", email, status, body)
		}
		assertErrorShape(t, body, "EMAIL_TAKEN")
	}
	// Penolakan terjadi sebelum undangan dibuat: tidak ada email yang diantre.
	if len(us.queue.queued) != 0 {
		t.Errorf("email undangan tidak boleh diantre saat email ditolak, dapat %d", len(us.queue.queued))
	}
}

func TestApiSuperadminTerakhirDilindungiDiApi(t *testing.T) {
	us := newUsersServer(t, nil)
	cookiesSA, csrfSA := login(t, us.srv, "rina@example.test")

	// adm-sa satu-satunya superadmin aktif → penurunan role ditolak.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, us.srv.URL+"/api/v1/admin/users/admin/adm-sa",
		`{"role":"manager"}`, cookiesSA, csrfSA)
	if status != http.StatusConflict {
		t.Fatalf("menurunkan superadmin terakhir harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "LAST_SUPERADMIN")

	// Nonaktifkan juga ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, us.srv.URL+"/api/v1/admin/users/admin/adm-sa",
		`{"is_active":false}`, cookiesSA, csrfSA)
	if status != http.StatusConflict {
		t.Fatalf("menonaktifkan superadmin terakhir harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "LAST_SUPERADMIN")

	// Flag notifikasi boleh diubah pada superadmin terakhir.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, us.srv.URL+"/api/v1/admin/users/admin/adm-sa",
		`{"notif_surat_jalan":true}`, cookiesSA, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("mengubah flag harus 200, dapat %d (%v)", status, body)
	}
	if body["data"].(map[string]any)["notif_surat_jalan"] != true {
		t.Errorf("flag surat jalan harus tersimpan: %v", body["data"])
	}

	// Role tidak dikenal ditolak validasi.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, us.srv.URL+"/api/v1/admin/users/admin/adm-mgr",
		`{"role":"branch"}`, cookiesSA, csrfSA)
	if status != http.StatusBadRequest {
		t.Errorf("role tak dikenal harus 400, dapat %d", status)
	}

	// admin tak dikenal → 404.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, us.srv.URL+"/api/v1/admin/users/admin/tidak-ada",
		`{"nama":"X"}`, cookiesSA, csrfSA)
	if status != http.StatusNotFound {
		t.Errorf("admin tak dikenal harus 404, dapat %d", status)
	}
}

// TestApiNonaktifkanAdminMencabutSesi memastikan admin yang dinonaktifkan
// langsung kehilangan sesinya (bukan hanya ditolak saat login berikutnya).
func TestApiNonaktifkanAdminMencabutSesi(t *testing.T) {
	us := newUsersServer(t, nil)

	cookiesMgr, _ := login(t, us.srv, "budi@example.test")
	cookiesSA, csrfSA := login(t, us.srv, "rina@example.test")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, us.srv.URL+"/api/v1/admin/users/admin/adm-mgr",
		`{"is_active":false}`, cookiesSA, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("nonaktifkan admin harus 200, dapat %d (%v)", status, body)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, us.srv.URL+"/api/v1/auth/me", "", cookiesMgr, "")
	if status != http.StatusUnauthorized {
		t.Errorf("sesi admin nonaktif harus 401, dapat %d", status)
	}
}

func readOpenAPI(t *testing.T) (string, error) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "openapi", "openapi.yaml"))
	return string(raw), err
}

// TestApiKontrakPathUserManagement memastikan endpoint pengelolaan pengguna
// benar-benar tercatat di kontrak OpenAPI.
func TestApiKontrakPathUserManagement(t *testing.T) {
	raw, err := readOpenAPI(t)
	if err != nil {
		t.Fatalf("baca kontrak: %v", err)
	}
	spec := parsePathMetode(raw)
	wajib := map[string][]string{
		"/api/v1/admin/users/branch":      {"GET"},
		"/api/v1/admin/users/branch/{id}": {"PUT"},
		"/api/v1/admin/users/admin":       {"GET", "POST"},
		"/api/v1/admin/users/admin/{id}":  {"PUT"},
	}
	for path, metode := range wajib {
		if spec[path] == nil {
			t.Errorf("path %s belum ada di kontrak OpenAPI", path)
			continue
		}
		for _, m := range metode {
			if !spec[path][m] {
				t.Errorf("%s %s belum ada di kontrak OpenAPI", m, path)
			}
		}
	}
	if !strings.Contains(raw, "LAST_SUPERADMIN") || !strings.Contains(raw, "EMAIL_TAKEN") {
		t.Error("kode error LAST_SUPERADMIN/EMAIL_TAKEN belum tercantum di kontrak")
	}
}
