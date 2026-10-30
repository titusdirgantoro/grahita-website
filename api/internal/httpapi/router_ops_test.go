package httpapi

import (
	"net/http"
	"testing"
)

// TestOPS01PermukaanPublikCSRFOriginSesiRoleDanHeader menutup seluruh variasi
// OPS-01 pada router yang sama dengan aplikasi. Origin asing diuji melalui
// preflight seperti yang dilakukan browser: tanpa ACAO browser tidak pernah
// mengirim mutasi sebenarnya.
func TestOPS01PermukaanPublikCSRFOriginSesiRoleDanHeader(t *testing.T) {
	cfg := testConfig()
	cfg.AllowedOrigins = []string{"http://127.0.0.1:5174"}
	srv, _ := newTestServerWithConfig(t, fakeHealth{migrasi: 20}, cfg)

	// Hanya health/meta yang publik. Endpoint bisnis tetap meminta sesi.
	for _, path := range []string{"/healthz", "/readyz", "/api/v1/meta"} {
		resp, _ := doJSON(t, http.MethodGet, srv.URL+path, "", nil, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("endpoint publik %s harus 200, dapat %d", path, resp.StatusCode)
		}
		for header, want := range map[string]string{
			"X-Content-Type-Options":     "nosniff",
			"X-Frame-Options":            "DENY",
			"Referrer-Policy":            "strict-origin-when-cross-origin",
			"Cross-Origin-Opener-Policy": "same-origin",
			"Permissions-Policy":         "geolocation=(), microphone=(), camera=()",
		} {
			if got := resp.Header.Get(header); got != want {
				t.Errorf("%s pada %s: dapat %q, mau %q", header, path, got, want)
			}
		}
		if got := resp.Header.Get("Content-Security-Policy"); got == "" {
			t.Errorf("CSP wajib ada pada %s", path)
		}
	}
	resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/v1/catalog", "", nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("endpoint bisnis tanpa sesi harus 401, dapat %d (%v)", resp.StatusCode, body)
	}

	// Browser hanya boleh melakukan preflight dari origin development yang
	// dikonfigurasi. Origin asing tidak memperoleh ACAO, jadi mutasi diblokir
	// browser sebelum PUT dikirim.
	preflight := func(origin string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodOptions, srv.URL+"/api/v1/admin/settings", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPut)
		req.Header.Set("Access-Control-Request-Headers", "Content-Type, X-CSRF-Token")
		got, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = got.Body.Close() })
		return got
	}
	known := preflight("http://127.0.0.1:5174")
	if known.StatusCode != http.StatusNoContent || known.Header.Get("Access-Control-Allow-Origin") != "http://127.0.0.1:5174" || known.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("origin dikenal harus mendapat preflight 204 berizin: %d %+v", known.StatusCode, known.Header)
	}
	unknown := preflight("https://evil.example")
	if unknown.Header.Get("Access-Control-Allow-Origin") != "" || unknown.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("origin asing tidak boleh memperoleh izin CORS: %+v", unknown.Header)
	}

	// Superadmin tanpa CSRF ditolak sebelum mutasi.
	saCookies, saCSRF := login(t, srv, "rina@example.test")
	resp, body = doJSON(t, http.MethodPut, srv.URL+"/api/v1/admin/settings",
		`{"key":"biaya_packaging","value":4000}`, saCookies, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mutasi tanpa CSRF harus 403, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "CSRF_INVALID")

	// Manager membawa sesi+CSRF sah tetapi role-nya salah.
	mgrCookies, mgrCSRF := login(t, srv, "budi@example.test")
	resp, body = doJSON(t, http.MethodPut, srv.URL+"/api/v1/admin/settings",
		`{"key":"biaya_packaging","value":4000}`, mgrCookies, mgrCSRF)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("role salah harus 403, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "FORBIDDEN")

	// Setelah logout, cookie dan CSRF lama tidak boleh menghidupkan sesi lagi.
	resp, body = doJSON(t, http.MethodPost, srv.URL+"/api/v1/auth/logout", `{}`, saCookies, saCSRF)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout harus 204, dapat %d (%v)", resp.StatusCode, body)
	}
	resp, body = doJSON(t, http.MethodPut, srv.URL+"/api/v1/admin/settings",
		`{"key":"biaya_packaging","value":4000}`, saCookies, saCSRF)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sesi berakhir harus 401, dapat %d (%v)", resp.StatusCode, body)
	}
	assertErrorShape(t, body, "UNAUTHENTICATED")
}
