package httpapi

// Uji HTTP alur pendaftaran akun cabang lewat token (F1-07) memakai
// penyimpanan in-memory: status, kode error spesifik, penolakan field
// read-only dari payload, pengecualian CSRF, dan pembatas laju per IP.
//
// Sisi database (kunci baris FOR UPDATE, token sekali pakai, akun bisa login)
// diuji di integration_registration_pg_test.go.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/registration"
)

// waktuUjiRegistrasi adalah titik waktu tetap untuk clock pendaftaran.
var waktuUjiRegistrasi = time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)

// registrationServer menyusun router dengan penyimpanan pendaftaran in-memory
// yang bisa diisi token oleh pengujian.
type registrationServer struct {
	*authServer
	repo *registration.Memory
}

func newRegistrationServer(t *testing.T, limit int) *registrationServer {
	t.Helper()
	repo := registration.NewMemory()
	as := newAuthServer(t, func(d *Deps) {
		d.Registration = registration.NewService(repo, clock.Fixed(waktuUjiRegistrasi))
		d.RegistrationLimiter = httpx.NewRateLimiter(limit, 15*time.Minute)
	})
	return &registrationServer{authServer: as, repo: repo}
}

// tokenRegistrasi membuat token berbentuk sah beserta cabangnya lalu
// menyimpannya sebagai token yang masih berlaku.
func (rs *registrationServer) tokenRegistrasi(t *testing.T, branchID, nama, kepala, email string, aktif bool) string {
	t.Helper()
	token, _, err := auth.NewRegistrationToken()
	if err != nil {
		t.Fatalf("buat token uji: %v", err)
	}
	rs.repo.PutToken(token, registration.Snapshot{
		TokenID: "tok-" + branchID, BranchID: branchID,
		ExpiresAt: waktuUjiRegistrasi.Add(24 * time.Hour),
		Nama:      nama, NamaKepala: kepala, Email: email, BranchActive: aktif,
	})
	return token
}

func registerBody(token string) string {
	return `{"token":"` + token + `","telepon":"0812-3456-7890","password":"rahasia123","password_konfirmasi":"rahasia123"}`
}

func TestApiRegistrasiDuaLangkahMembuatAkun(t *testing.T) {
	rs := newRegistrationServer(t, 50)
	token := rs.tokenRegistrasi(t, "br-x", "Grahita Cabang X", "Kepala X", "cabang-x@example.test", true)

	// Langkah 1: data cabang tampil apa adanya.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		rs.srv.URL+"/api/v1/auth/registration/validate", `{"token":"`+token+`"}`, nil, "")
	if status != http.StatusOK {
		t.Fatalf("validasi token harus 200, dapat %d (%v)", status, body)
	}
	data := body["data"].(map[string]any)
	if data["nama_cabang"] != "Grahita Cabang X" || data["nama_kepala"] != "Kepala X" || data["email"] != "cabang-x@example.test" {
		t.Errorf("data cabang tidak sesuai: %v", data)
	}

	// Langkah 2: akun terbentuk (201) tanpa CSRF karena endpoint publik.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		rs.srv.URL+"/api/v1/auth/register", registerBody(token), nil, "")
	if status != http.StatusCreated {
		t.Fatalf("daftar harus 201, dapat %d (%v)", status, body)
	}
	user := body["data"].(map[string]any)["user"].(map[string]any)
	if user["email"] != "cabang-x@example.test" || user["branch_id"] != "br-x" || user["role"] != "branch" {
		t.Errorf("akun yang dikembalikan tidak sesuai: %v", user)
	}
	if simpan, ok := rs.repo.Akun("br-x"); !ok || simpan.Email != "cabang-x@example.test" {
		t.Errorf("akun harus tersimpan dengan email cabang: %+v", simpan)
	}

	// Token dipakai tepat sekali.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		rs.srv.URL+"/api/v1/auth/register", registerBody(token), nil, "")
	if status != http.StatusConflict {
		t.Fatalf("pemakaian kedua harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "TOKEN_USED")
}

func TestApiRegistrasiPesanSpesifikPerKeadaanToken(t *testing.T) {
	rs := newRegistrationServer(t, 50)

	kedaluwarsa, _, _ := auth.NewRegistrationToken()
	rs.repo.PutToken(kedaluwarsa, registration.Snapshot{
		BranchID: "br-kadaluwarsa", ExpiresAt: waktuUjiRegistrasi.Add(-time.Minute),
		Nama: "Cabang Kadaluwarsa", NamaKepala: "Kepala", Email: "k@example.test", BranchActive: true,
	})
	terpakai, _, _ := auth.NewRegistrationToken()
	rs.repo.PutUsedToken(terpakai, registration.Snapshot{
		BranchID: "br-terpakai", ExpiresAt: waktuUjiRegistrasi.Add(time.Hour),
		Nama: "Cabang Terpakai", NamaKepala: "Kepala", Email: "t@example.test", BranchActive: true,
	})
	nonaktif, _, _ := auth.NewRegistrationToken()
	rs.repo.PutToken(nonaktif, registration.Snapshot{
		BranchID: "br-nonaktif", ExpiresAt: waktuUjiRegistrasi.Add(time.Hour),
		Nama: "Cabang Nonaktif", NamaKepala: "Kepala", Email: "n@example.test", BranchActive: false,
	})
	berakun, _, _ := auth.NewRegistrationToken()
	rs.repo.PutToken(berakun, registration.Snapshot{
		BranchID: "br-berakun", ExpiresAt: waktuUjiRegistrasi.Add(time.Hour),
		Nama: "Cabang Berakun", NamaKepala: "Kepala", Email: "a@example.test", BranchActive: true,
	})
	rs.repo.PutAccount("br-berakun")

	kasus := []struct {
		nama   string
		token  string
		status int
		kode   string
	}{
		{"tidak dikenal", "GRH-ABCD-EFGH-JKLM-NPQR", http.StatusBadRequest, "TOKEN_INVALID"},
		{"bentuk tidak sah", "bukan-token-sama-sekali", http.StatusBadRequest, "TOKEN_INVALID"},
		{"kedaluwarsa", kedaluwarsa, http.StatusBadRequest, "TOKEN_EXPIRED"},
		{"sudah dipakai", terpakai, http.StatusConflict, "TOKEN_USED"},
		{"cabang nonaktif", nonaktif, http.StatusConflict, "BRANCH_INACTIVE"},
		{"sudah punya akun", berakun, http.StatusConflict, "BRANCH_HAS_ACCOUNT"},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
				rs.srv.URL+"/api/v1/auth/registration/validate", `{"token":"`+k.token+`"}`, nil, "")
			if status != k.status {
				t.Fatalf("validate %s harus %d, dapat %d (%v)", k.nama, k.status, status, body)
			}
			assertErrorShape(t, body, k.kode)

			// Pesan spesifik juga berlaku di endpoint daftar.
			status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
				rs.srv.URL+"/api/v1/auth/register", registerBody(k.token), nil, "")
			if status != k.status {
				t.Errorf("daftar %s harus %d, dapat %d (%v)", k.nama, k.status, status, body)
			}
			assertErrorShape(t, body, k.kode)
		})
	}
	// Tidak satu pun percobaan di atas boleh menghasilkan akun.
	for _, branch := range []string{"br-kadaluwarsa", "br-terpakai", "br-nonaktif", "br-berakun"} {
		if _, ok := rs.repo.Akun(branch); ok && branch != "br-berakun" {
			t.Errorf("tidak boleh ada akun baru untuk %s", branch)
		}
	}
}

func TestApiRegistrasiMenolakDataReadOnlyDariPayload(t *testing.T) {
	rs := newRegistrationServer(t, 50)
	token := rs.tokenRegistrasi(t, "br-y", "Grahita Cabang Y", "Kepala Y", "cabang-y@example.test", true)

	// Nama cabang, nama kepala, dan email adalah data server: mengirimnya di
	// payload ditolak, bukan dipakai (PRD CP-REG-03).
	palsu := []string{
		`{"token":"` + token + `","telepon":"0812","password":"rahasia123","password_konfirmasi":"rahasia123","nama_cabang":"Cabang Palsu"}`,
		`{"token":"` + token + `","telepon":"0812","password":"rahasia123","password_konfirmasi":"rahasia123","email":"palsu@example.test"}`,
		`{"token":"` + token + `","telepon":"0812","password":"rahasia123","password_konfirmasi":"rahasia123","branch_id":"br-lain"}`,
	}
	for _, body := range palsu {
		status, resp, _ := apiReq(t, http.DefaultClient, http.MethodPost,
			rs.srv.URL+"/api/v1/auth/register", body, nil, "")
		if status != http.StatusBadRequest {
			t.Fatalf("payload dengan field read-only harus 400, dapat %d (%v)", status, resp)
		}
		assertErrorShape(t, resp, "VALIDATION_FAILED")
	}
	// Tidak ada akun yang lahir dari percobaan itu, dan tokennya masih utuh.
	if _, ok := rs.repo.Akun("br-y"); ok {
		t.Error("permintaan yang ditolak tidak boleh membuat akun")
	}
	status, _, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		rs.srv.URL+"/api/v1/auth/registration/validate", `{"token":"`+token+`"}`, nil, "")
	if status != http.StatusOK {
		t.Errorf("token tidak boleh terbakar oleh payload yang ditolak, dapat %d", status)
	}
}

func TestApiRegistrasiMenolakKonfirmasiDanKataSandiLemah(t *testing.T) {
	rs := newRegistrationServer(t, 50)
	token := rs.tokenRegistrasi(t, "br-z", "Grahita Cabang Z", "Kepala Z", "cabang-z@example.test", true)

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, rs.srv.URL+"/api/v1/auth/register",
		`{"token":"`+token+`","telepon":"0812","password":"rahasia123","password_konfirmasi":"rahasia124"}`, nil, "")
	if status != http.StatusBadRequest {
		t.Fatalf("konfirmasi beda harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
	if fields := body["error"].(map[string]any)["fields"].(map[string]any); fields["password_konfirmasi"] == nil {
		t.Errorf("field password_konfirmasi harus dilaporkan: %v", fields)
	}

	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, rs.srv.URL+"/api/v1/auth/register",
		`{"token":"`+token+`","telepon":"0812","password":"pendek","password_konfirmasi":"pendek"}`, nil, "")
	if status != http.StatusBadRequest {
		t.Fatalf("kata sandi lemah harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
	if fields := body["error"].(map[string]any)["fields"].(map[string]any); fields["password"] == nil {
		t.Errorf("field password harus dilaporkan: %v", fields)
	}
}

func TestApiRegistrasiDibatasiPerIP(t *testing.T) {
	rs := newRegistrationServer(t, 3)

	salah := func(i int) int {
		req, err := http.NewRequest(http.MethodPost, rs.srv.URL+"/api/v1/auth/registration/validate",
			strings.NewReader(`{"token":"GRH-ABCD-EFGH-JKLM-NPQR"}`))
		if err != nil {
			t.Fatalf("buat request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "203.0.113.10")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("kirim request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	for i := 1; i <= 3; i++ {
		if status := salah(i); status != http.StatusBadRequest {
			t.Fatalf("percobaan ke-%d harus 400, dapat %d", i, status)
		}
	}
	if status := salah(4); status != http.StatusTooManyRequests {
		t.Errorf("percobaan ke-4 per IP harus 429, dapat %d", status)
	}
	// IP lain tidak ikut tertahan.
	req, _ := http.NewRequest(http.MethodPost, rs.srv.URL+"/api/v1/auth/registration/validate",
		strings.NewReader(`{"token":"GRH-ABCD-EFGH-JKLM-NPQR"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.11")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("kirim request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("IP lain harus tetap dilayani (400), dapat %d", resp.StatusCode)
	}
}
