package httpapi

import (
	"net/http"
	"testing"
)

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
		`{"values":{"biaya_packaging":4500,"cnc_hari":10,"manager_boleh_revisi":false}}`, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("batch valid harus 200, dapat %d (%v)", status, body)
	}
	data := body["data"].(map[string]any)
	values := data["values"].(map[string]any)
	if values["biaya_packaging"] != float64(4500) || values["cnc_hari"] != float64(10) {
		t.Errorf("nilai tersimpan tidak sesuai: %v", values)
	}
	if bs.settings.values["biaya_packaging"] != "4500" {
		t.Errorf("nilai kanonik harus tersimpan, dapat %q", bs.settings.values["biaya_packaging"])
	}

	// Batch dengan satu nilai tidak valid → 400, tidak ada yang tersimpan.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/settings",
		`{"values":{"biaya_packaging":-5,"cnc_hari":11}}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("nilai tidak valid harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
	errObj := body["error"].(map[string]any)
	fields := errObj["fields"].(map[string]any)
	if _, ok := fields["biaya_packaging"]; !ok {
		t.Errorf("fields harus memuat kunci biaya_packaging: %v", fields)
	}
	if bs.settings.values["cnc_hari"] != "10" {
		t.Errorf("nilai valid tidak boleh ikut berubah saat batch gagal: %q", bs.settings.values["cnc_hari"])
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
