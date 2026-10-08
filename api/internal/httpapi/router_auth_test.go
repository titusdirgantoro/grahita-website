package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/branch"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
)

// fakeResetMailer menangkap tautan atur ulang yang dikirim service.
type fakeResetMailer struct {
	mu   sync.Mutex
	sent []auth.ResetMessage
}

func (f *fakeResetMailer) SendPasswordReset(_ context.Context, m auth.ResetMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeResetMailer) jumlah() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeResetMailer) tokenTerakhir(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("tidak ada email atur ulang yang dikirim")
	}
	return f.sent[len(f.sent)-1].Token
}

// fakeBranchCatalog mengembalikan data yang berbeda per cabang sehingga
// kebocoran lintas cabang langsung terlihat di respons. Sisa metode
// catalog.Repository (master produk & matriks harga) diambil dari penyimpanan
// in-memory katalog supaya perubahan interface tidak perlu disalin ke sini.
type fakeBranchCatalog struct{ *catalog.Memory }

var hargaPerCabang = map[string]int64{"br-a": 100000, "br-b": 200000, "br-c": 300000}

func (fakeBranchCatalog) ListProducts(context.Context) ([]catalog.Product, error) {
	return []catalog.Product{{ID: "p1", Nama: "Bio Magnetik", HargaDefault: 350000, IsActive: true}}, nil
}

func (fakeBranchCatalog) ListActiveProducts(context.Context) ([]catalog.Product, error) {
	return []catalog.Product{{ID: "p1", Nama: "Bio Magnetik", HargaDefault: 350000, IsActive: true}}, nil
}

func (fakeBranchCatalog) ListProductsForBranch(_ context.Context, branchID string) ([]catalog.BranchProduct, error) {
	return []catalog.BranchProduct{{ID: "p1", Nama: "Bio Magnetik", Harga: hargaPerCabang[branchID]}}, nil
}

func (fakeBranchCatalog) ListBranches(context.Context) ([]catalog.Branch, error) {
	return []catalog.Branch{
		{ID: "br-a", Nama: "Grahita Cabang A", Email: "a@example.test", IsActive: true},
		{ID: "br-b", Nama: "Grahita Cabang B", Email: "b@example.test", IsActive: true},
	}, nil
}

func (fakeBranchCatalog) GetBranch(_ context.Context, id string) (catalog.Branch, error) {
	harga := hargaPerCabang[id]
	if harga == 0 {
		return catalog.Branch{}, catalog.ErrNotFound
	}
	return catalog.Branch{ID: id, Nama: "Grahita " + id, Email: id + "@example.test", IsActive: true}, nil
}

// authServer adalah router lengkap dengan penyimpanan in-memory + pencatat
// email atur ulang, khusus untuk pengujian alur autentikasi lewat HTTP.
type authServer struct {
	srv        *httptest.Server
	router     http.Handler
	store      *auth.Memory
	mail       *fakeResetMailer
	loginLimit *httpx.RateLimiter
}

func newAuthServer(t *testing.T, tune func(*Deps)) *authServer {
	t.Helper()

	store := auth.NewMemory()
	hash, err := auth.HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}
	store.PutBranch(auth.Credentials{
		UserID: "usr-a", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-a",
		Nama: "Grahita Cabang A", Email: "a@example.test", PasswordHash: hash, Active: true,
	})
	store.PutBranch(auth.Credentials{
		UserID: "usr-b", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-b",
		Nama: "Grahita Cabang B", Email: "b@example.test", PasswordHash: hash, Active: true,
	})
	// Cabang nonaktif: repository memetakan cabang nonaktif menjadi Active=false.
	store.PutBranch(auth.Credentials{
		UserID: "usr-c", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-c",
		Nama: "Grahita Cabang C (nonaktif)", Email: "c@example.test", PasswordHash: hash, Active: false,
	})
	store.PutAdmin(auth.Credentials{
		UserID: "adm-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin,
		Nama: "Rina", Email: "rina@example.test", PasswordHash: hash, Active: true,
	})
	store.PutAdmin(auth.Credentials{
		UserID: "adm-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager,
		Nama: "Budi", Email: "budi@example.test", PasswordHash: hash, Active: true,
	})
	// Admin yang belum boleh masuk walau kata sandinya sudah terisi (mis.
	// aktivasi dicabut / activated_at dihapus) → 403, bukan 401, karena
	// kredensialnya memang terverifikasi. Varian "undangan murni" (tanpa kata
	// sandi sama sekali) diuji di integration_pg_test.go dan dijawab 401.
	store.PutAdmin(auth.Credentials{
		UserID: "adm-undang", Kind: auth.KindAdmin, Role: auth.RoleManager,
		Nama: "Undangan", Email: "undang@example.test", PasswordHash: hash, Active: false,
	})

	svc := auth.NewService(store, store, store, clock.System(), 8*time.Hour, 30*24*time.Hour)
	mail := &fakeResetMailer{}
	svc.SetResetMailer(mail)

	limit := httpx.NewRateLimiter(5, 15*time.Minute)
	// Master cabang (F1-06) memakai penyimpanan in-memory berisi cabang yang
	// sama dengan yang dipakai authStore & fakeBranchCatalog.
	branchRepo := branch.NewMemory()
	branchRepo.Put(branch.Branch{ID: "br-a", Nama: "Grahita Cabang A", NamaKepala: "Kepala A", Email: "a@example.test", IsActive: true})
	branchRepo.Put(branch.Branch{ID: "br-b", Nama: "Grahita Cabang B", NamaKepala: "Kepala B", Email: "b@example.test", IsActive: true})
	deps := Deps{
		Config:        testConfig(),
		Version:       "test",
		Health:        fakeHealth{migrasi: 1},
		Auth:          svc,
		Settings:      settings.NewService(&fakeSettingsRepo{values: map[string]string{"biaya_packaging": "3000", "reset_menit": "60"}}),
		Catalog:       catalog.NewService(fakeBranchCatalog{Memory: catalog.NewMemory()}),
		Branches:      branch.NewService(branchRepo, clock.System()),
		LoginLimiter:  limit,
		ForgotLimiter: httpx.NewRateLimiter(5, 15*time.Minute),
	}
	if tune != nil {
		tune(&deps)
	}

	router := NewRouter(deps)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &authServer{srv: srv, router: router, store: store, mail: mail, loginLimit: deps.LoginLimiter}
}

// apiReq mengirim satu request JSON dan mengembalikan status, body, dan
// cookie pada respons.
func apiReq(t *testing.T, client *http.Client, method, url, body string, cookies []*http.Cookie, csrf string) (int, map[string]any, []*http.Cookie) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("buat request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set(httpx.CSRFHeader, csrf)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("kirim request %s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var decoded map[string]any
	if resp.StatusCode != http.StatusNoContent {
		_ = json.NewDecoder(resp.Body).Decode(&decoded)
	}
	return resp.StatusCode, decoded, resp.Cookies()
}

func TestApiLupaPasswordSelaluSuksesDanNonEnumeratif(t *testing.T) {
	as := newAuthServer(t, nil)

	statusTakDikenal, bodyTakDikenal, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		as.srv.URL+"/api/v1/auth/password/forgot", `{"email":"tidak-ada@example.test"}`, nil, "")
	if statusTakDikenal != http.StatusOK {
		t.Fatalf("lupa kata sandi akun tak dikenal harus 200, dapat %d", statusTakDikenal)
	}
	if as.mail.jumlah() != 0 {
		t.Errorf("email tidak boleh dikirim ke akun tak dikenal")
	}

	statusAda, bodyAda, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		as.srv.URL+"/api/v1/auth/password/forgot", `{"email":"a@example.test"}`, nil, "")
	if statusAda != http.StatusOK {
		t.Fatalf("lupa kata sandi harus 200, dapat %d", statusAda)
	}
	if as.mail.jumlah() != 1 {
		t.Fatalf("email atur ulang harus dikirim sekali, dapat %d", as.mail.jumlah())
	}
	// Bentuk respons identik: klien tidak bisa membedakan akun ada/tidak.
	if bodyTakDikenal["data"].(map[string]any)["pesan"] != bodyAda["data"].(map[string]any)["pesan"] {
		t.Errorf("pesan respons berbeda antar akun: %v vs %v", bodyTakDikenal, bodyAda)
	}

	// Email tidak valid tetap ditolak sebagai kesalahan bentuk.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		as.srv.URL+"/api/v1/auth/password/forgot", `{"email":"bukan-email"}`, nil, "")
	if status != http.StatusBadRequest {
		t.Fatalf("email tidak valid harus 400, dapat %d", status)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
}

func TestApiAturUlangPasswordSekaliPakaiDanCabutSesi(t *testing.T) {
	as := newAuthServer(t, nil)

	cookie1, _ := login(t, as.srv, "a@example.test")
	cookie2, _ := login(t, as.srv, "a@example.test")
	if as.store.SessionCount() != 2 {
		t.Fatalf("harus ada 2 sesi aktif, dapat %d", as.store.SessionCount())
	}

	status, _, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		as.srv.URL+"/api/v1/auth/password/forgot", `{"email":"a@example.test"}`, nil, "")
	if status != http.StatusOK {
		t.Fatalf("lupa kata sandi harus 200, dapat %d", status)
	}
	token := as.mail.tokenTerakhir(t)

	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		as.srv.URL+"/api/v1/auth/password/reset", `{"token":"`+token+`","password":"sandibaru123"}`, nil, "")
	if status != http.StatusNoContent {
		t.Fatalf("atur ulang harus 204, dapat %d", status)
	}

	// Sekali pakai.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		as.srv.URL+"/api/v1/auth/password/reset", `{"token":"`+token+`","password":"sandibaru123"}`, nil, "")
	if status != http.StatusBadRequest {
		t.Fatalf("token terpakai harus 400, dapat %d", status)
	}
	assertErrorShape(t, body, "RESET_INVALID")

	// Seluruh sesi lama mati.
	for nama, cookies := range map[string][]*http.Cookie{"sesi1": cookie1, "sesi2": cookie2} {
		status, _, _ := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/auth/me", "", cookies, "")
		if status != http.StatusUnauthorized {
			t.Errorf("%s harus 401 setelah atur ulang, dapat %d", nama, status)
		}
	}

	// Kata sandi baru berlaku, yang lama tidak.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		as.srv.URL+"/api/v1/auth/login", `{"email":"a@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusUnauthorized {
		t.Errorf("kata sandi lama harus ditolak (401), dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		as.srv.URL+"/api/v1/auth/login", `{"email":"a@example.test","password":"sandibaru123"}`, nil, "")
	if status != http.StatusOK {
		t.Errorf("login dengan kata sandi baru harus 200, dapat %d", status)
	}
}

func TestApiGantiPasswordCabutSesiLain(t *testing.T) {
	as := newAuthServer(t, nil)

	cookie1, csrf1 := login(t, as.srv, "a@example.test")
	cookie2, _ := login(t, as.srv, "a@example.test")

	// Wajib CSRF (sudah login).
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, as.srv.URL+"/api/v1/auth/password/change",
		`{"password_lama":"rahasia123","password_baru":"sandibaru123"}`, cookie1, "")
	if status != http.StatusForbidden {
		t.Fatalf("tanpa CSRF harus 403, dapat %d", status)
	}
	assertErrorShape(t, body, "CSRF_INVALID")

	// Kata sandi lama salah.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, as.srv.URL+"/api/v1/auth/password/change",
		`{"password_lama":"salahsekali","password_baru":"sandibaru123"}`, cookie1, csrf1)
	if status != http.StatusBadRequest {
		t.Fatalf("kata sandi lama salah harus 400, dapat %d", status)
	}
	fields := body["error"].(map[string]any)["fields"].(map[string]any)
	if fields["password_lama"] == nil {
		t.Errorf("field password_lama harus dilaporkan: %v", fields)
	}

	// Kata sandi baru terlalu lemah.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, as.srv.URL+"/api/v1/auth/password/change",
		`{"password_lama":"rahasia123","password_baru":"pendek"}`, cookie1, csrf1)
	if status != http.StatusBadRequest {
		t.Fatalf("kata sandi lemah harus 400, dapat %d", status)
	}
	fields = body["error"].(map[string]any)["fields"].(map[string]any)
	if fields["password_baru"] == nil {
		t.Errorf("field password_baru harus dilaporkan: %v", fields)
	}

	// Perubahan sah.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, as.srv.URL+"/api/v1/auth/password/change",
		`{"password_lama":"rahasia123","password_baru":"sandibaru123"}`, cookie1, csrf1)
	if status != http.StatusNoContent {
		t.Fatalf("ganti kata sandi harus 204, dapat %d", status)
	}

	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/auth/me", "", cookie1, "")
	if status != http.StatusOK {
		t.Errorf("sesi yang dipakai harus tetap hidup, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/auth/me", "", cookie2, "")
	if status != http.StatusUnauthorized {
		t.Errorf("sesi lain harus dicabut, dapat %d", status)
	}
}

func TestApiForgotDanResetTidakButuhSesiMaupunCsrf(t *testing.T) {
	as := newAuthServer(t, nil)

	// Tanpa cookie CSRF sama sekali: forgot wajib 200, reset wajib 400
	// (bukan 403) karena keduanya endpoint publik.
	status, _, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		as.srv.URL+"/api/v1/auth/password/forgot", `{"email":"a@example.test"}`, nil, "")
	if status != http.StatusOK {
		t.Errorf("forgot tanpa CSRF harus 200, dapat %d", status)
	}
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		as.srv.URL+"/api/v1/auth/password/reset", `{"token":"tidak-ada","password":"sandibaru123"}`, nil, "")
	if status != http.StatusBadRequest {
		t.Fatalf("reset tanpa CSRF harus 400 (dikecualikan CSRF), dapat %d", status)
	}
	assertErrorShape(t, body, "RESET_INVALID")

	// Ganti kata sandi TANPA sesi tetap 401 (CSRF dipenuhi lebih dulu supaya
	// yang diuji benar-benar penjaga sesi, bukan penjaga CSRF).
	_, _, csrfCookies := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/meta", "", nil, "")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		as.srv.URL+"/api/v1/auth/password/change", `{"password_lama":"rahasia123","password_baru":"sandibaru123"}`,
		csrfCookies, csrfFromCookies(csrfCookies))
	if status != http.StatusUnauthorized {
		t.Errorf("ganti kata sandi tanpa sesi harus 401, dapat %d", status)
	}
}

func TestApiRateLimitLoginGagalPerAkuDanPerIP(t *testing.T) {
	as := newAuthServer(t, func(d *Deps) { d.LoginLimiter = httpx.NewRateLimiter(5, 15*time.Minute) })

	salah := func(catatan string, headers map[string]string) int {
		req, _ := http.NewRequest(http.MethodPost, as.srv.URL+"/api/v1/auth/login",
			strings.NewReader(`{"email":"`+catatan+`","password":"salahsekali"}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("kirim login: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	// Per akun: 5 percobaan gagal dari IP berbeda, percobaan ke-6 ditolak
	// walau datang dari IP baru.
	for i := 0; i < 5; i++ {
		ip := "203.0.113." + string(rune('1'+i))
		if status := salah("a@example.test", map[string]string{"X-Forwarded-For": ip}); status != http.StatusUnauthorized {
			t.Fatalf("percobaan gagal ke-%d harus 401, dapat %d", i+1, status)
		}
	}
	if status := salah("a@example.test", map[string]string{"X-Forwarded-For": "203.0.113.99"}); status != http.StatusTooManyRequests {
		t.Errorf("percobaan ke-6 per akun harus 429, dapat %d", status)
	}
	// Kata sandi yang benar pun tidak lagi dilayani selama jendela berjalan.
	req, _ := http.NewRequest(http.MethodPost, as.srv.URL+"/api/v1/auth/login",
		strings.NewReader(`{"email":"a@example.test","password":"rahasia123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kirim login: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("login benar saat terkunci harus 429, dapat %d", resp.StatusCode)
	}
}

func TestApiRateLimitLoginPerIPLintasAkun(t *testing.T) {
	as := newAuthServer(t, func(d *Deps) { d.LoginLimiter = httpx.NewRateLimiter(5, 15*time.Minute) })
	ip := "198.51.100.7"

	for i := 0; i < 5; i++ {
		email := "orang" + string(rune('a'+i)) + "@example.test"
		req, _ := http.NewRequest(http.MethodPost, as.srv.URL+"/api/v1/auth/login",
			strings.NewReader(`{"email":"`+email+`","password":"salahsekali"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", ip)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("kirim login: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("percobaan ke-%d harus 401, dapat %d", i+1, resp.StatusCode)
		}
	}

	// Akun sah dari IP yang sama ikut tertahan selama jendela berjalan.
	req, _ := http.NewRequest(http.MethodPost, as.srv.URL+"/api/v1/auth/login",
		strings.NewReader(`{"email":"b@example.test","password":"rahasia123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", ip)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kirim login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("IP dengan 5 kegagalan harus 429, dapat %d", resp.StatusCode)
	}
}

func TestApiSesiTidakBocorAntarSubdomain(t *testing.T) {
	as := newAuthServer(t, nil)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("buat cookie jar: %v", err)
	}
	addr := as.srv.Listener.Addr().String()
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			// Semua nama host diarahkan ke server uji, tetapi cookie jar
			// tetap memakai nama host pada URL — persis perilaku browser.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
		Timeout: 10 * time.Second,
	}

	const customerHost = "http://grahita-branch.tiusclaw.com"
	const adminHost = "http://grahita-admin.tiusclaw.com"

	status, _, _ := apiReq(t, client, http.MethodPost, customerHost+"/api/v1/auth/login",
		`{"email":"a@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusOK {
		t.Fatalf("login di host cabang harus 200, dapat %d", status)
	}

	status, _, _ = apiReq(t, client, http.MethodGet, customerHost+"/api/v1/auth/me", "", nil, "")
	if status != http.StatusOK {
		t.Errorf("sesi harus berlaku di host yang memasangnya, dapat %d", status)
	}

	// Cookie host-only tidak pernah dikirim ke subdomain lain.
	status, _, _ = apiReq(t, client, http.MethodGet, adminHost+"/api/v1/auth/me", "", nil, "")
	if status != http.StatusUnauthorized {
		t.Errorf("sesi TIDAK boleh bocor ke host admin, dapat %d", status)
	}

	// Header Set-Cookie memang tidak memuat atribut Domain.
	req, _ := http.NewRequest(http.MethodPost, customerHost+"/api/v1/auth/login",
		strings.NewReader(`{"email":"b@example.test","password":"rahasia123"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	for _, sc := range resp.Header.Values("Set-Cookie") {
		if strings.Contains(strings.ToLower(sc), "domain=") {
			t.Errorf("cookie sesi/CSRF tidak boleh punya atribut Domain: %q", sc)
		}
	}
}

func TestApiCabangNonaktifDanAdminBelumAktifDitolak(t *testing.T) {
	as := newAuthServer(t, nil)

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, as.srv.URL+"/api/v1/auth/login",
		`{"email":"c@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusForbidden {
		t.Fatalf("cabang nonaktif harus 403, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "FORBIDDEN")

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, as.srv.URL+"/api/v1/auth/login",
		`{"email":"undang@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusForbidden {
		t.Fatalf("admin belum diaktivasi harus 403, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "FORBIDDEN")

	// Cabang aktif tetap bisa masuk sebagai pembanding.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, as.srv.URL+"/api/v1/auth/login",
		`{"email":"b@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusOK {
		t.Errorf("cabang aktif harus 200, dapat %d", status)
	}
}

func TestApiAksesLintasCabangDanRole(t *testing.T) {
	as := newAuthServer(t, nil)

	// Cabang A hanya melihat harga cabangnya sendiri.
	cookieA, _ := login(t, as.srv, "a@example.test")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/catalog", "", cookieA, "")
	if status != http.StatusOK {
		t.Fatalf("katalog cabang harus 200, dapat %d", status)
	}
	item := body["data"].([]any)[0].(map[string]any)
	if item["harga"] != float64(hargaPerCabang["br-a"]) {
		t.Errorf("cabang A harus melihat harganya sendiri, dapat %v", item["harga"])
	}

	// Cabang B melihat harga B — tidak ada kebocoran antar cabang.
	cookieB, _ := login(t, as.srv, "b@example.test")
	_, bodyB, _ := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/catalog", "", cookieB, "")
	itemB := bodyB["data"].([]any)[0].(map[string]any)
	if itemB["harga"] != float64(hargaPerCabang["br-b"]) {
		t.Errorf("cabang B harus melihat harganya sendiri, dapat %v", itemB["harga"])
	}

	// /me/branch selalu memakai branch_id dari sesi.
	_, me, _ := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/me/branch", "", cookieA, "")
	if me["data"].(map[string]any)["id"] != "br-a" {
		t.Errorf("me/branch harus cabang sesi, dapat %v", me)
	}

	// Cabang tidak boleh menyentuh master cabang maupun pengaturan.
	for _, path := range []string{"/api/v1/admin/branches", "/api/v1/admin/branches/br-b", "/api/v1/admin/settings"} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+path, "", cookieA, "")
		if status != http.StatusForbidden {
			t.Errorf("cabang pada %s harus 403, dapat %d (%v)", path, status, body)
		}
	}

	// Manager: boleh master cabang, dilarang pengaturan (bank/report/user admin
	// memakai kapabilitas yang sama-sama tidak dimiliki Manager).
	cookieMgr, _ := login(t, as.srv, "budi@example.test")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/admin/branches", "", cookieMgr, "")
	if status != http.StatusOK {
		t.Errorf("manager harus boleh master cabang, dapat %d", status)
	}
	for _, path := range []string{"/api/v1/admin/settings"} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+path, "", cookieMgr, "")
		if status != http.StatusForbidden {
			t.Errorf("manager pada %s harus 403, dapat %d (%v)", path, status, body)
		}
	}

	// Superadmin boleh pengaturan.
	cookieSA, _ := login(t, as.srv, "rina@example.test")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/admin/settings", "", cookieSA, "")
	if status != http.StatusOK {
		t.Errorf("superadmin harus boleh pengaturan, dapat %d", status)
	}
}
