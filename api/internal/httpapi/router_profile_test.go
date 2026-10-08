package httpapi

// Uji profil sendiri (F1-10, PRD §3.8/§4.10) lewat HTTP dengan penyimpanan
// in-memory: hanya NAMA admin yang bisa diubah pemiliknya, email/role tetap
// read-only, dan akun cabang ditolak.

import (
	"net/http"
	"testing"
)

// namaDariMe membaca `data.user.nama` dari respons GET/PUT /auth/me.
func namaDariMe(t *testing.T, body map[string]any) string {
	t.Helper()
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("respons tidak memuat data: %v", body)
	}
	user, ok := data["user"].(map[string]any)
	if !ok {
		t.Fatalf("respons tidak memuat data.user: %v", body)
	}
	nama, _ := user["nama"].(string)
	return nama
}

func userDariMe(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	user, ok := body["data"].(map[string]any)["user"].(map[string]any)
	if !ok {
		t.Fatalf("respons tidak memuat data.user: %v", body)
	}
	return user
}

func TestApiUbahNamaProfilSendiriAdmin(t *testing.T) {
	as := newAuthServer(t, nil)
	cookies, csrf := login(t, as.srv, "rina@example.test")

	// Mutasi wajib CSRF (sudah login).
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
		as.srv.URL+"/api/v1/auth/me", `{"nama":"Rina Kartika"}`, cookies, "")
	if status != http.StatusForbidden {
		t.Fatalf("tanpa CSRF harus 403, dapat %d", status)
	}
	assertErrorShape(t, body, "CSRF_INVALID")

	// Perubahan sah; spasi berlebih dan spasi tepi dirapikan.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		as.srv.URL+"/api/v1/auth/me", `{"nama":"  Rina   Kartika  "}`, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("ubah nama harus 200, dapat %d (%v)", status, body)
	}
	user := userDariMe(t, body)
	if user["nama"] != "Rina Kartika" {
		t.Errorf("nama respons harus ternormalisasi, dapat %v", user["nama"])
	}
	// Email dan role TIDAK ikut berubah oleh permintaan ini (read-only).
	if user["email"] != "rina@example.test" {
		t.Errorf("email harus tetap, dapat %v", user["email"])
	}
	if user["role"] != "superadmin" {
		t.Errorf("role harus tetap, dapat %v", user["role"])
	}
	if user["id"] != "adm-sa" {
		t.Errorf("id akun harus tetap, dapat %v", user["id"])
	}

	// Sesi yang sama membaca nama baru pada request berikutnya.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/auth/me", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("me harus 200, dapat %d", status)
	}
	if got := namaDariMe(t, body); got != "Rina Kartika" {
		t.Errorf("me harus menampilkan nama baru, dapat %v", got)
	}
}

func TestApiUbahProfilTolakFieldReadOnlyDanNamaTidakValid(t *testing.T) {
	as := newAuthServer(t, nil)
	cookies, csrf := login(t, as.srv, "rina@example.test")

	kasus := []struct {
		nama  string
		body  string
		field string
	}{
		{"email read-only ditolak", `{"nama":"Rina","email":"hacker@example.test"}`, "email"},
		{"role read-only ditolak", `{"nama":"Rina","role":"superadmin"}`, "role"},
		{"field asing ditolak", `{"nama":"Rina","is_active":false}`, "is_active"},
		{"nama wajib ada", `{"email":"hacker@example.test"}`, "nama"},
		{"nama terlalu pendek", `{"nama":"R"}`, "nama"},
		{"nama hanya spasi", `{"nama":"   "}`, "nama"},
	}
	for _, k := range kasus {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
			as.srv.URL+"/api/v1/auth/me", k.body, cookies, csrf)
		if status != http.StatusBadRequest {
			t.Errorf("%s: harus 400, dapat %d (%v)", k.nama, status, body)
			continue
		}
		assertErrorShape(t, body, "VALIDATION_FAILED")
	}

	// Tidak ada satu pun percobaan di atas yang mengubah akun.
	_, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/auth/me", "", cookies, "")
	user := userDariMe(t, body)
	if user["nama"] != "Rina" || user["email"] != "rina@example.test" || user["role"] != "superadmin" {
		t.Errorf("akun tidak boleh berubah oleh permintaan yang ditolak: %v", user)
	}
}

func TestApiUbahProfilManagerBolehTapiCabangDitolak(t *testing.T) {
	as := newAuthServer(t, nil)

	// Manager juga boleh mengganti nama tampilannya sendiri (PRD §4.10) tanpa
	// mengubah role-nya.
	mgrCookies, mgrCsrf := login(t, as.srv, "budi@example.test")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
		as.srv.URL+"/api/v1/auth/me", `{"nama":"Budi Santoso"}`, mgrCookies, mgrCsrf)
	if status != http.StatusOK {
		t.Fatalf("manager ubah nama harus 200, dapat %d (%v)", status, body)
	}
	user := userDariMe(t, body)
	if user["nama"] != "Budi Santoso" || user["role"] != "manager" {
		t.Errorf("nama/role manager salah setelah ubah: %v", user)
	}

	// Akun cabang: nama/kepala/email/telepon adalah data master Pusat.
	brCookies, brCsrf := login(t, as.srv, "a@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		as.srv.URL+"/api/v1/auth/me", `{"nama":"Cabang A Baru"}`, brCookies, brCsrf)
	if status != http.StatusForbidden {
		t.Fatalf("akun cabang harus 403, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "FORBIDDEN")

	// Endpoint admin tetap berlaku untuk akun admin, dan akun cabang tetap
	// melihat data cabangnya seperti semula.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/me/branch", "", brCookies, "")
	if status != http.StatusOK {
		t.Fatalf("me/branch harus 200 untuk akun cabang, dapat %d", status)
	}
	if got := body["data"].(map[string]any)["nama"]; got != "Grahita br-a" {
		t.Errorf("nama cabang tidak boleh berubah, dapat %v", got)
	}
}

func TestApiUbahProfilTanpaSesi(t *testing.T) {
	as := newAuthServer(t, nil)

	// Ambil cookie CSRF tanpa sesi supaya penjaga CSRF terpenuhi dan yang
	// diuji benar-benar penjaga sesi.
	_, _, csrfCookies := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/meta", "", nil, "")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut,
		as.srv.URL+"/api/v1/auth/me", `{"nama":"Tanpa Sesi"}`, csrfCookies, csrfFromCookies(csrfCookies))
	if status != http.StatusUnauthorized {
		t.Fatalf("tanpa sesi harus 401, dapat %d", status)
	}
	assertErrorShape(t, body, "UNAUTHENTICATED")
}
