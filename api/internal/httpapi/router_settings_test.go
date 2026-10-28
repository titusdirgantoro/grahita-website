package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/ocrsecret"
	"github.com/grahita/portal/api/internal/platform/config"
)

type testOCRRepo struct {
	rec        ocrsecret.Record
	configured bool
}

func (r *testOCRRepo) Load(context.Context) (ocrsecret.Record, error) {
	if !r.configured {
		return ocrsecret.Record{}, ocrsecret.ErrNotConfigured
	}
	return r.rec, nil
}
func (r *testOCRRepo) Save(_ context.Context, _ auth.Actor, rec ocrsecret.Record) error {
	if r.configured {
		rec.Version = r.rec.Version + 1
	} else {
		rec.Version = 1
	}
	rec.UpdatedAt = time.Now()
	r.rec = rec
	r.configured = true
	return nil
}
func (r *testOCRRepo) MarkVerified(_ context.Context, _ auth.Actor, version int64) error {
	if version != r.rec.Version {
		return ocrsecret.ErrStale
	}
	now := time.Now()
	r.rec.VerifiedAt = &now
	return nil
}
func (r *testOCRRepo) Delete(context.Context, auth.Actor) error { r.configured = false; return nil }

type testOCRTester struct{}

func (testOCRTester) TestKey(context.Context, string) error { return nil }

// TestApiPengaturanOtorisasiSuperadmin memastikan pengelolaan pengaturan
// (baca lengkap & ubah) hanya untuk Superadmin, baik lewat UI maupun API.
func TestApiPengaturanOtorisasiSuperadmin(t *testing.T) {
	bs := newBankServer(t)
	base := bs.srv.URL + "/api/v1"

	// GET /admin/settings tanpa sesi → 401.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/settings", "", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("tanpa sesi harus 401, dapat %d", status)
	}
	assertErrorShape(t, body, "UNAUTHENTICATED")

	// Manager & akun cabang ditolak pada baca dan ubah.
	cabang, cabangCsrf := login(t, bs.srv, "a@example.test")
	mgr, mgrCsrf := login(t, bs.srv, "budi@example.test")
	for _, aktor := range []struct {
		nama    string
		cookies []*http.Cookie
		csrf    string
	}{{"cabang", cabang, cabangCsrf}, {"manager", mgr, mgrCsrf}} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/settings", "", aktor.cookies, "")
		if status != http.StatusForbidden {
			t.Errorf("%s baca pengaturan harus 403, dapat %d", aktor.nama, status)
		}
		assertErrorShape(t, body, "FORBIDDEN")
		status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
			`{"values":{"biaya_packaging":4000}}`, aktor.cookies, aktor.csrf)
		if status != http.StatusForbidden {
			t.Errorf("%s ubah pengaturan harus 403, dapat %d", aktor.nama, status)
		}
	}

	// Superadmin boleh membaca lengkap, termasuk ai.
	sa, saCsrf := login(t, bs.srv, "rina@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/settings", "", sa, "")
	if status != http.StatusOK {
		t.Fatalf("superadmin baca harus 200, dapat %d", status)
	}
	data := body["data"].(map[string]any)
	if _, ok := data["ai"]; !ok {
		t.Error("pengaturan superadmin harus memuat ai")
	}
	if _, ok := data["biaya_packaging"]; !ok {
		t.Error("pengaturan superadmin harus memuat biaya_packaging")
	}
	if saCsrf == "" {
		t.Fatal("cookie CSRF superadmin tidak ditemukan")
	}
}

// TestApiPengaturanBatchValidasi memastikan simpan banyak kunci sekaligus
// divalidasi all-or-nothing dan nilai uang harus bilangan bulat.
func TestApiPengaturanBatchValidasi(t *testing.T) {
	bs := newBankServer(t)
	base := bs.srv.URL + "/api/v1"
	cookies, csrf := login(t, bs.srv, "rina@example.test")

	// Batch valid → 200 dan tersimpan.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"biaya_packaging":4500,"reset_menit":10,"manager_boleh_revisi":false}}`, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("batch valid harus 200, dapat %d (%v)", status, body)
	}
	data := body["data"].(map[string]any)
	values := data["values"].(map[string]any)
	if values["biaya_packaging"] != float64(4500) || values["reset_menit"] != float64(10) {
		t.Errorf("nilai tersimpan tidak sesuai: %v", values)
	}
	if bs.settings.values["biaya_packaging"] != "4500" {
		t.Errorf("nilai kanonik harus tersimpan, dapat %q", bs.settings.values["biaya_packaging"])
	}

	// Batch dengan satu nilai tidak valid → 400, tidak ada yang tersimpan.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"biaya_packaging":-5,"reset_menit":11}}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("nilai tidak valid harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
	errObj := body["error"].(map[string]any)
	fields := errObj["fields"].(map[string]any)
	if _, ok := fields["biaya_packaging"]; !ok {
		t.Errorf("fields harus memuat kunci biaya_packaging: %v", fields)
	}
	if bs.settings.values["reset_menit"] != "10" {
		t.Errorf("nilai valid tidak boleh ikut berubah saat batch gagal: %q", bs.settings.values["reset_menit"])
	}

	// Nilai uang pecahan ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"biaya_packaging":3000.5}}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("pecahan harus 400, dapat %d (%v)", status, body)
	}

	// Kunci tak dikenal ditolak.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"kunci_karangan":1}}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("kunci tak dikenal harus 400, dapat %d", status)
	}
}

// TestApiSettingsLintasRoleTidakAda memastikan permukaan pengaturan lintas-role
// benar-benar GONE (keputusan CEO atas review QA putaran 1): pengelolaan
// pengaturan hanya lewat /api/v1/admin/settings untuk Superadmin, sehingga
// /api/v1/settings dijawab 404 untuk siapa pun.
func TestApiSettingsLintasRoleTidakAda(t *testing.T) {
	bs := newBankServer(t)
	base := bs.srv.URL + "/api/v1"

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/settings", "", nil, "")
	if status != http.StatusNotFound {
		t.Fatalf("GET /api/v1/settings tanpa sesi harus 404, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "NOT_FOUND")

	for _, email := range []string{"a@example.test", "budi@example.test", "rina@example.test"} {
		cookies, _ := login(t, bs.srv, email)
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/settings", "", cookies, "")
		if status != http.StatusNotFound {
			t.Errorf("%s GET /api/v1/settings harus 404, dapat %d (%v)", email, status, body)
		}
	}
}

// Sakelar AI tidak boleh terlihat siap atau dapat diaktifkan di produksi
// ketika konfigurasi provider API belum ada. Opsi manual tetap dapat disimpan.
func TestApiOCRStatusDanGuardProviderProduksi(t *testing.T) {
	cfg := testConfig()
	cfg.AppEnv = "production"
	bs := newBankServerWithConfig(t, cfg)
	base := bs.srv.URL + "/api/v1/admin/settings/ocr-status"

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base, "", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("tanpa sesi harus 401, dapat %d (%v)", status, body)
	}
	mgr, _ := login(t, bs.srv, "budi@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base, "", mgr, "")
	if status != http.StatusForbidden {
		t.Fatalf("Manager harus 403, dapat %d (%v)", status, body)
	}
	sa, csrf := login(t, bs.srv, "rina@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base, "", sa, "")
	if status != http.StatusOK {
		t.Fatalf("Superadmin harus 200, dapat %d (%v)", status, body)
	}
	data := body["data"].(map[string]any)
	if data["provider_ready"] != false || data["ai_active"] != false {
		t.Fatalf("tanpa kunci AI harus nonaktif/tidak siap: %v", data)
	}
	if _, leaked := data["api_key"]; leaked {
		t.Fatal("status OCR tidak boleh mengembalikan kunci")
	}

	settingsURL := bs.srv.URL + "/api/v1/admin/settings"
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, settingsURL,
		`{"values":{"ai":{"aktif":true}}}`, sa, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("AI tanpa provider harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
	if _, saved := bs.settings.values["ai"]; saved {
		t.Fatal("AI tanpa provider tidak boleh tersimpan")
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, settingsURL,
		`{"values":{"ai":{"aktif":false}}}`, sa, csrf)
	if status != http.StatusOK {
		t.Fatalf("manual harus tetap diizinkan, dapat %d (%v)", status, body)
	}
}

func TestApiOCRProviderSiapMengizinkanAI(t *testing.T) {
	cfg := testConfig()
	cfg.AppEnv = "production"
	cfg.OCRProvider = config.OCRProviderClaude
	cfg.AnthropicAPIKey = "kunci-qa-tidak-asli"
	bs := newBankServerWithConfig(t, cfg)
	sa, csrf := login(t, bs.srv, "rina@example.test")
	settingsURL := bs.srv.URL + "/api/v1/admin/settings"
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, settingsURL,
		`{"values":{"ai":{"aktif":true}}}`, sa, csrf)
	if status != http.StatusOK {
		t.Fatalf("AI dengan provider siap harus 200, dapat %d (%v)", status, body)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, settingsURL+"/ocr-status", "", sa, "")
	if status != http.StatusOK {
		t.Fatalf("status OCR harus 200, dapat %d (%v)", status, body)
	}
	data := body["data"].(map[string]any)
	if data["provider_ready"] != true || data["ai_active"] != true {
		t.Fatalf("status AI setelah diaktifkan salah: %v", data)
	}
}

func TestApiGeminiKunciHanyaSuperadminDanHarusTeruji(t *testing.T) {
	cfg := testConfig()
	cfg.AppEnv = "production"
	cfg.OCRProvider = config.OCRProviderGemini
	cfg.GeminiKeyEncryptionKey = base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	repo := &testOCRRepo{}
	secret, err := ocrsecret.New(repo, cfg.GeminiKeyEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	bs := newBankServerWithConfig(t, cfg, func(d *Deps) { d.OCRSecrets = secret; d.OCRTester = testOCRTester{} })
	base := bs.srv.URL + "/api/v1/admin/settings"
	manager, managerCSRF := login(t, bs.srv, "budi@example.test")
	status, _, _ := apiReq(t, http.DefaultClient, http.MethodPut, base+"/ocr-key",
		`{"api_key":"AIza-kunci-uji-rahasia-123456"}`, manager, managerCSRF)
	if status != http.StatusForbidden {
		t.Fatalf("manager simpan kunci: %d", status)
	}
	sa, csrf := login(t, bs.srv, "rina@example.test")
	key := "AIza-kunci-uji-rahasia-123456"
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, base+"/ocr-key",
		`{"api_key":"`+key+`"}`, sa, csrf)
	if status != http.StatusOK || strings.Contains(strings.Join([]string{bodyString(body), string(repo.rec.Ciphertext)}, " "), key) {
		t.Fatalf("simpan kunci gagal atau bocor: %d", status)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/ocr-status", "", sa, "")
	if status != http.StatusOK || body["data"].(map[string]any)["provider_ready"] != false {
		t.Fatalf("kunci belum diuji tidak boleh siap: %d %v", status, body)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base,
		`{"values":{"ai":{"aktif":true}}}`, sa, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("AI aktif sebelum uji: %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, base+"/ocr-key/test", "", manager, managerCSRF)
	if status != http.StatusForbidden {
		t.Fatalf("manager uji kunci: %d", status)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, base+"/ocr-key/test", "", sa, csrf)
	if status != http.StatusOK || body["data"].(map[string]any)["key_verified"] != true {
		t.Fatalf("uji kunci: %d %v", status, body)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base,
		`{"values":{"ai":{"aktif":true}}}`, sa, csrf)
	if status != http.StatusOK {
		t.Fatalf("AI setelah kunci teruji: %d", status)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base, "", sa, "")
	if status != http.StatusOK || strings.Contains(bodyString(body), key) {
		t.Fatal("settings umum membocorkan API key")
	}
}

func bodyString(value any) string { return fmt.Sprint(value) }
