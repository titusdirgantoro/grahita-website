package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
)

func testConfig() config.Config {
	return config.Config{
		AppEnv:          "local",
		CookieName:      "grahita_session",
		SessionSecure:   false,
		SessionIdle:     8 * time.Hour,
		SessionAbsolute: 30 * 24 * time.Hour,
		CSRFEnabled:     true,
		CustomerHost:    config.DefaultCustomerHost,
		AdminHost:       config.DefaultAdminHost,
		PublicScheme:    "https",
	}
}

type fakeHealth struct {
	pingErr  error
	migrasi  int64
	migrasiE error
}

func (f fakeHealth) Ping(context.Context) error { return f.pingErr }

func (f fakeHealth) MigrationVersion(context.Context) (int64, error) { return f.migrasi, f.migrasiE }

type fakeSettingsRepo struct {
	values map[string]string
}

func (r *fakeSettingsRepo) List(context.Context) ([]settings.Value, error) {
	out := make([]settings.Value, 0, len(r.values))
	for k, v := range r.values {
		out = append(out, settings.Value{Key: k, Value: []byte(v)})
	}
	return out, nil
}

func (r *fakeSettingsRepo) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := r.values[key]
	if !ok {
		return nil, settings.ErrNotFound
	}
	return []byte(v), nil
}

func (r *fakeSettingsRepo) Upsert(_ context.Context, key string, value []byte, _ string) error {
	r.values[key] = string(value)
	return nil
}

type fakeCatalogRepo struct{}

func (fakeCatalogRepo) ListProducts(context.Context) ([]catalog.Product, error) {
	return []catalog.Product{{ID: "p1", Nama: "Bio Magnetik", HargaDefault: 350000, IsActive: true}}, nil
}

func (fakeCatalogRepo) ListActiveProducts(context.Context) ([]catalog.Product, error) {
	return []catalog.Product{{ID: "p1", Nama: "Bio Magnetik", HargaDefault: 350000, IsActive: true}}, nil
}

func (fakeCatalogRepo) ListProductsForBranch(context.Context, string) ([]catalog.BranchProduct, error) {
	return []catalog.BranchProduct{{ID: "p1", Nama: "Bio Magnetik", Harga: 340000}}, nil
}

func (fakeCatalogRepo) ListBranches(context.Context) ([]catalog.Branch, error) {
	return []catalog.Branch{{ID: "br-1", Nama: "Grahita Bandung", Email: "bandung@example.test", IsActive: true}}, nil
}

func (fakeCatalogRepo) GetBranch(_ context.Context, id string) (catalog.Branch, error) {
	if id != "br-1" {
		return catalog.Branch{}, catalog.ErrNotFound
	}
	return catalog.Branch{ID: "br-1", Nama: "Grahita Bandung", Email: "bandung@example.test", IsActive: true}, nil
}

// newTestServer menyiapkan router lengkap dengan penyimpanan in-memory.
func newTestServer(t *testing.T, health fakeHealth) (*httptest.Server, *auth.Memory) {
	t.Helper()

	store := auth.NewMemory()
	hash, err := auth.HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}
	store.PutAdmin(auth.Credentials{
		UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin,
		Nama: "Rina", Email: "rina@example.test", PasswordHash: hash, Active: true,
	})
	store.PutAdmin(auth.Credentials{
		UserID: "adm-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager,
		Nama: "Budi", Email: "budi@example.test", PasswordHash: hash, Active: true,
	})
	store.PutBranch(auth.Credentials{
		UserID: "usr-br", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-1",
		Nama: "Grahita Bandung", Email: "bandung@example.test", PasswordHash: hash, Active: true,
	})

	svc := auth.NewService(store, store, clock.System(), testConfig().SessionIdle, testConfig().SessionAbsolute)

	router := NewRouter(Deps{
		Config:   testConfig(),
		Version:  "test",
		Health:   health,
		Auth:     svc,
		Settings: settings.NewService(&fakeSettingsRepo{values: map[string]string{"biaya_packaging": "3000"}}),
		Catalog:  catalog.NewService(fakeCatalogRepo{}),
	})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, store
}

func doJSON(t *testing.T, method, url, body string, cookies []*http.Cookie, csrf string) (*http.Response, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("buat request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set(httpx.CSRFHeader, csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kirim request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var decoded map[string]any
	if resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			t.Fatalf("body bukan JSON pada %s %s: %v", method, url, err)
		}
	}
	return resp, decoded
}

func TestHealthzSelaluOK(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{pingErr: context.DeadlineExceeded, migrasi: 0})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/healthz", "", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz harus 200 walau DB mati, dapat %d", resp.StatusCode)
	}
	if body["status"] != "ok" {
		t.Errorf("status healthz tidak sesuai: %v", body["status"])
	}
}

func TestReadyzSiap(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{migrasi: 2})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/readyz", "", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz harus 200, dapat %d", resp.StatusCode)
	}
	if body["status"] != "ready" {
		t.Errorf("status readyz tidak sesuai: %v", body["status"])
	}
	checks, ok := body["checks"].(map[string]any)
	if !ok {
		t.Fatalf("checks tidak ada: %v", body)
	}
	if checks["database"] != "ok" {
		t.Errorf("database harus ok: %v", checks["database"])
	}
	if !strings.HasPrefix(checks["migrations"].(string), "ok") {
		t.Errorf("migrasi harus ok: %v", checks["migrations"])
	}
}

func TestReadyzTidakSiapSaatDatabaseGagal(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{pingErr: context.DeadlineExceeded, migrasi: 1})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/readyz", "", nil, "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz harus 503 saat DB gagal, dapat %d", resp.StatusCode)
	}
	if body["status"] != "not ready" {
		t.Errorf("status readyz tidak sesuai: %v", body["status"])
	}
}

func TestReadyzTidakSiapTanpaMigrasi(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{migrasi: 0})

	resp, _ := doJSON(t, http.MethodGet, srv.URL+"/readyz", "", nil, "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz harus 503 saat belum ada migrasi, dapat %d", resp.StatusCode)
	}
}

func TestNotFoundBerbentukErrorAPI(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{migrasi: 1})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/tidak-ada", "", nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("harus 404, dapat %d", resp.StatusCode)
	}
	assertErrorShape(t, body, "NOT_FOUND")
}

func TestEndpointTerproteksiButuhLogin(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{migrasi: 1})

	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/catalog", "", nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("harus 401, dapat %d", resp.StatusCode)
	}
	assertErrorShape(t, body, "UNAUTHENTICATED")
}

func TestLoginBodyTidakValidMenghasilkanFields(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{migrasi: 1})

	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login", `{"email":"bukan-email","password":"pendek"}`, nil, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("harus 400, dapat %d", resp.StatusCode)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
	fields := body["error"].(map[string]any)["fields"].(map[string]any)
	if fields["email"] == nil || fields["password"] == nil {
		t.Errorf("fields harus memuat email dan password: %v", fields)
	}
}

func TestLoginKredensialSalah(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{migrasi: 1})

	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login",
		`{"email":"rina@example.test","password":"salahsekali"}`, nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("harus 401, dapat %d", resp.StatusCode)
	}
	assertErrorShape(t, body, "UNAUTHENTICATED")
	if strings.Contains(strings.ToLower(body["error"].(map[string]any)["message"].(string)), "tidak terdaftar") {
		t.Error("pesan tidak boleh membocorkan keberadaan akun")
	}
}

// login mengembalikan cookie sesi + token CSRF untuk request berikutnya.
func login(t *testing.T, srv *httptest.Server, email string) ([]*http.Cookie, string) {
	t.Helper()
	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login",
		`{"email":"`+email+`","password":"rahasia123"}`, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s gagal: %d %v", email, resp.StatusCode, body)
	}
	var csrf string
	for _, c := range resp.Cookies() {
		if c.Name == httpx.CSRFCookieName {
			csrf = c.Value
		}
	}
	if csrf == "" {
		t.Fatal("cookie CSRF tidak dipasang saat login")
	}
	return resp.Cookies(), csrf
}

func TestLoginCookieSesiHostOnly(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{migrasi: 1})

	resp, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/login",
		`{"email":"bandung@example.test","password":"rahasia123"}`, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login gagal: %d", resp.StatusCode)
	}

	var sesi *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "grahita_session" {
			sesi = c
		}
	}
	if sesi == nil {
		t.Fatal("cookie sesi tidak dipasang")
	}
	if sesi.Domain != "" {
		t.Errorf("cookie harus host-only, Domain harus kosong, dapat %q", sesi.Domain)
	}
	if !sesi.HttpOnly {
		t.Error("cookie sesi wajib HttpOnly")
	}
	if sesi.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite harus Lax, dapat %v", sesi.SameSite)
	}
	if sesi.Path != "/" {
		t.Errorf("Path harus /, dapat %q", sesi.Path)
	}
	if sesi.Value == "" {
		t.Error("nilai cookie sesi tidak boleh kosong")
	}
}

func TestLoginCookieSesiSecureDiProduksi(t *testing.T) {
	cfg := config.Config{
		AppEnv:          "production",
		CookieName:      "grahita_session",
		SessionSecure:   true,
		SessionIdle:     8 * time.Hour,
		SessionAbsolute: 30 * 24 * time.Hour,
		CSRFEnabled:     true,
		CustomerHost:    config.DefaultCustomerHost,
		AdminHost:       config.DefaultAdminHost,
		PublicScheme:    "https",
	}
	c := sessionCookie(cfg, "token-uji", time.Now().Add(time.Hour))
	if !c.Secure {
		t.Error("cookie sesi wajib Secure di produksi")
	}
	if c.Domain != "" {
		t.Errorf("cookie harus host-only, dapat %q", c.Domain)
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Error("atribut HttpOnly/SameSite tidak sesuai")
	}
}

func TestOtorisasiRoleDitegakkanBackend(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{migrasi: 1})

	kasus := []struct {
		nama   string
		email  string
		status int
	}{
		{"superadmin boleh membaca pengaturan admin", "rina@example.test", http.StatusOK},
		{"manager ditolak (FORBIDDEN)", "budi@example.test", http.StatusForbidden},
		{"cabang ditolak (FORBIDDEN)", "bandung@example.test", http.StatusForbidden},
	}

	for _, k := range kasus {
		cookies, csrf := login(t, srv, k.email)
		resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/admin/settings", "", cookies, csrf)
		if resp.StatusCode != k.status {
			t.Errorf("%s: dapat %d, mau %d (%v)", k.nama, resp.StatusCode, k.status, body)
		}
		if k.status == http.StatusForbidden {
			assertErrorShape(t, body, "FORBIDDEN")
		}
	}
}

func TestCsrfWajibUntukRequestNonGET(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{migrasi: 1})

	cookies, _ := login(t, srv, "rina@example.test")
	// Tanpa header X-CSRF-Token.
	resp, body := doJSON(t, http.MethodPut, srv.URL+"/api/v1/admin/settings",
		`{"key":"biaya_packaging","value":4000}`, cookies, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tanpa CSRF harus 403, dapat %d", resp.StatusCode)
	}
	assertErrorShape(t, body, "CSRF_INVALID")

	// Dengan token yang benar.
	resp2, body2 := doJSON(t, http.MethodPut, srv.URL+"/api/v1/admin/settings",
		`{"key":"biaya_packaging","value":4000}`, cookies, csrfFromCookies(cookies))
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("dengan CSRF harus 200, dapat %d (%v)", resp2.StatusCode, body2)
	}
	dataObj, ok := body2["data"].(map[string]any)
	if !ok || dataObj["key"] != "biaya_packaging" {
		t.Errorf("respons tidak sesuai: %v", body2)
	}
}

func TestKatalogCabangHanyaProdukSendiri(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{migrasi: 1})

	cookies, csrf := login(t, srv, "bandung@example.test")
	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/catalog", "", cookies, csrf)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("katalog harus 200, dapat %d", resp.StatusCode)
	}
	list, ok := body["data"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("katalog tidak sesuai: %v", body)
	}
	item := list[0].(map[string]any)
	if item["harga"] != float64(340000) {
		t.Errorf("harga harus harga khusus cabang, dapat %v", item["harga"])
	}
}

func TestKunciPengaturanTidakDikenalDitolak(t *testing.T) {
	srv, _ := newTestServer(t, fakeHealth{migrasi: 1})

	cookies, _ := login(t, srv, "rina@example.test")
	resp, body := doJSON(t, http.MethodPut, srv.URL+"/api/v1/admin/settings",
		`{"key":"kunci_karangan","value":1}`, cookies, csrfFromCookies(cookies))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("harus 400, dapat %d", resp.StatusCode)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
}

func TestLogoutMencabutSesiDanMenghapusCookie(t *testing.T) {
	srv, store := newTestServer(t, fakeHealth{migrasi: 1})

	cookies, _ := login(t, srv, "bandung@example.test")
	if store.SessionCount() != 1 {
		t.Fatalf("harus ada 1 sesi aktif, dapat %d", store.SessionCount())
	}

	resp, _ := doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/logout", "", cookies, csrfFromCookies(cookies))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout harus 204, dapat %d", resp.StatusCode)
	}
	var dihapus bool
	for _, c := range resp.Cookies() {
		if c.Name == "grahita_session" && c.MaxAge < 0 {
			dihapus = true
		}
	}
	if !dihapus {
		t.Error("cookie sesi harus dihapus saat logout")
	}
	if store.SessionCount() != 0 {
		t.Errorf("sesi harus dicabut, sisa %d", store.SessionCount())
	}
}

func csrfFromCookies(cookies []*http.Cookie) string {
	for _, c := range cookies {
		if c.Name == httpx.CSRFCookieName {
			return c.Value
		}
	}
	return ""
}

func assertErrorShape(t *testing.T, body map[string]any, code string) {
	t.Helper()
	raw, ok := body["error"]
	if !ok {
		t.Fatalf("respons tidak memuat objek error: %v", body)
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("error bukan objek: %T", raw)
	}
	if obj["code"] != code {
		t.Errorf("kode error %v, mau %q", obj["code"], code)
	}
	msg, ok := obj["message"].(string)
	if !ok || msg == "" {
		t.Errorf("message wajib ada dan tidak kosong: %v", obj["message"])
	}
}
