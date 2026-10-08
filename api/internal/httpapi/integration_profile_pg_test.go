package httpapi

// Uji profil sendiri & ganti kata sandi pada PostgreSQL nyata (F1-10).
//
// Yang benar-benar dieksekusi SQL di sini: nama profil admin tersimpan di
// admin_users, email/role tidak pernah ikut berubah, dan data cabang tetap
// read-only (tidak ada rute tulis, parameter cabang lain diabaikan).

import (
	"context"
	"net/http"
	"testing"
)

func TestPGProfilAdminUbahNamaTanpaMengubahEmailRole(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	cookies, csrf := pgLogin(t, f.srv, "budi@example.test", "rahasia123")

	// Field read-only ditolak lebih dulu: email, role, dan field asing.
	for _, body := range []string{
		`{"nama":"Budi Santoso","email":"hacker@example.test"}`,
		`{"nama":"Budi Santoso","role":"superadmin"}`,
		`{"nama":"Budi Santoso","is_active":false}`,
	} {
		status, resp, _ := apiReq(t, http.DefaultClient, http.MethodPut,
			f.srv.URL+"/api/v1/auth/me", body, cookies, csrf)
		if status != http.StatusBadRequest {
			t.Fatalf("body %s harus 400, dapat %d (%v)", body, status, resp)
		}
		assertErrorShape(t, resp, "VALIDATION_FAILED")
	}
	// Tidak ada yang berubah di database karena percobaan di atas.
	var nama, email, role string
	if err := f.pool.QueryRow(ctx,
		`SELECT nama, email::text, role::text FROM admin_users WHERE id = $1`, f.adminMgr).
		Scan(&nama, &email, &role); err != nil {
		t.Fatalf("baca admin: %v", err)
	}
	if nama != "Budi" || email != "budi@example.test" || role != "manager" {
		t.Fatalf("permintaan yang ditolak tidak boleh mengubah akun: nama=%q email=%q role=%q", nama, email, role)
	}

	// Perubahan sah.
	status, resp, _ := apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/auth/me", `{"nama":"  Budi   Santoso "}`, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("ubah nama harus 200, dapat %d (%v)", status, resp)
	}

	// Database: nama berubah, email & role TIDAK.
	if err := f.pool.QueryRow(ctx,
		`SELECT nama, email::text, role::text FROM admin_users WHERE id = $1`, f.adminMgr).
		Scan(&nama, &email, &role); err != nil {
		t.Fatalf("baca admin setelah ubah: %v", err)
	}
	if nama != "Budi Santoso" {
		t.Errorf("nama di database harus ternormalisasi, dapat %q", nama)
	}
	if email != "budi@example.test" || role != "manager" {
		t.Errorf("email/role tidak boleh berubah: email=%q role=%q", email, role)
	}

	// Sesi LAIN milik akun yang sama membaca nama baru (aktor dimuat dari DB
	// tiap request, bukan dari cookie).
	cookies2, _ := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, resp, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookies2, "")
	if status != http.StatusOK {
		t.Fatalf("me sesi lain harus 200, dapat %d", status)
	}
	if got := namaDariMe(t, resp); got != "Budi Santoso" {
		t.Errorf("sesi lain harus melihat nama baru, dapat %q", got)
	}

	// Akun cabang ditolak: profilnya data master Pusat (read-only).
	brCookies, brCsrf := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	status, resp, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/auth/me", `{"nama":"Cabang A Baru"}`, brCookies, brCsrf)
	if status != http.StatusForbidden {
		t.Fatalf("akun cabang harus 403, dapat %d (%v)", status, resp)
	}
	assertErrorShape(t, resp, "FORBIDDEN")
	var namaCabang string
	if err := f.pool.QueryRow(ctx, `SELECT nama FROM branches WHERE id = $1`, f.branchA).Scan(&namaCabang); err != nil {
		t.Fatalf("baca cabang: %v", err)
	}
	if namaCabang != "Grahita Cabang A" {
		t.Errorf("nama cabang tidak boleh berubah, dapat %q", namaCabang)
	}
}

func TestPGProfilCabangReadOnlyDanTerpisah(t *testing.T) {
	f := newPGFixture(t)

	cookiesA, csrfA := pgLogin(t, f.srv, "a@example.test", "rahasia123")

	// Profil cabang berasal dari sesi; parameter cabang lain diabaikan sama
	// sekali sehingga cabang A tidak bisa membaca profil cabang B.
	status, resp, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/me/branch?branch_id="+f.branchB, "", cookiesA, "")
	if status != http.StatusOK {
		t.Fatalf("me/branch harus 200, dapat %d", status)
	}
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("respons me/branch tidak memuat data: %v", resp)
	}
	if data["id"] != f.branchA {
		t.Errorf("me/branch harus cabang sesi (%s), dapat %v", f.branchA, data["id"])
	}
	for _, field := range []string{"nama", "nama_kepala", "email", "telepon"} {
		if _, ada := data[field]; !ada {
			t.Errorf("profil cabang harus memuat field %q: %v", field, data)
		}
	}

	// Tidak ada rute tulis untuk profil cabang: PUT dijawab 405 (dengan CSRF
	// yang sah, supaya yang diuji benar-benar rute, bukan penjaga CSRF).
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodPost} {
		status, body, _ := apiReq(t, http.DefaultClient, method,
			f.srv.URL+"/api/v1/me/branch", `{"nama":"Cabang Palsu","telepon":"0800"}`,
			cookiesA, csrfA)
		if status != http.StatusMethodNotAllowed {
			t.Errorf("%s /me/branch harus 405, dapat %d (%v)", method, status, body)
		}
	}

	// Akun admin tidak boleh memakai endpoint profil cabang.
	mgrCookies, _ := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/me/branch", "", mgrCookies, "")
	if status != http.StatusForbidden {
		t.Errorf("admin harus 403 pada me/branch, dapat %d (%v)", status, body)
	}
}
