package httpapi

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/branch"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
)

// branchesServer adalah router lengkap untuk menguji master cabang lewat HTTP:
// penyimpanan auth in-memory dan penyimpanan cabang in-memory (yang sekaligus
// merekam entri audit).
type branchesServer struct {
	srv  *httptest.Server
	repo *branch.Memory
}

func newBranchesServer(t *testing.T, tune func(*Deps)) *branchesServer {
	t.Helper()

	hash, err := auth.HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}
	store := auth.NewMemory()
	store.PutBranch(auth.Credentials{
		UserID: "usr-a", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-a",
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

	repo := branch.NewMemory()
	repo.Put(branch.Branch{ID: "br-a", Nama: "Grahita Cabang A", NamaKepala: "Kepala A", Email: "a@example.test", Telepon: "0221", IsActive: true, IsCNC: true})
	repo.Put(branch.Branch{ID: "br-b", Nama: "Grahita Cabang B", NamaKepala: "Kepala B", Email: "b@example.test", IsActive: false, IsSpecial: true})
	repo.PutOrder("br-a") // cabang A sudah punya pesanan → tidak boleh dihapus
	repo.PutAccount("br-b")
	repo.PutAdminEmail("pusat@example.test")

	svc := auth.NewService(store, store, store, clock.System(), 8*time.Hour, 30*24*time.Hour)
	branchSvc := branch.NewService(repo, clock.System())

	deps := Deps{
		Config:        testConfig(),
		Version:       "test-branches",
		Health:        fakeHealth{migrasi: 9},
		Auth:          svc,
		Settings:      settings.NewService(&fakeSettingsRepo{values: map[string]string{"token_hari": "14"}}),
		Branches:      branchSvc,
		LoginLimiter:  httpx.NewRateLimiter(5, 15*time.Minute),
		ForgotLimiter: httpx.NewRateLimiter(5, 15*time.Minute),
	}
	if tune != nil {
		tune(&deps)
	}

	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	return &branchesServer{srv: srv, repo: repo}
}

func TestApiMasterCabangButuhSesiDanKapabilitas(t *testing.T) {
	bs := newBranchesServer(t, nil)
	cookiesCabang, csrfCabang := login(t, bs.srv, "a@example.test")

	// Tanpa sesi → 401.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, bs.srv.URL+"/api/v1/admin/branches", "", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("tanpa sesi harus 401, dapat %d", status)
	}
	assertErrorShape(t, body, "UNAUTHENTICATED")

	// Akun cabang tidak punya kapabilitas master cabang (PRD §2.1).
	kasus := []struct {
		method, path, req string
	}{
		{http.MethodGet, "/api/v1/admin/branches", ""},
		{http.MethodPost, "/api/v1/admin/branches", `{"nama":"X","nama_kepala":"Y","email":"x@example.test"}`},
		{http.MethodGet, "/api/v1/admin/branches/br-a", ""},
		{http.MethodPut, "/api/v1/admin/branches/br-a", `{"telepon":"0222"}`},
		{http.MethodDelete, "/api/v1/admin/branches/br-a", ""},
		{http.MethodPost, "/api/v1/admin/branches/br-b/token", ""},
	}
	for _, k := range kasus {
		status, body, _ := apiReq(t, http.DefaultClient, k.method, bs.srv.URL+k.path, k.req, cookiesCabang, csrfCabang)
		if status != http.StatusForbidden {
			t.Errorf("%s %s oleh akun cabang harus 403, dapat %d (%v)", k.method, k.path, status, body)
		}
	}

	// Mutasi tanpa CSRF ditolak walau rolenya benar.
	cookiesMgr, _ := login(t, bs.srv, "budi@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, bs.srv.URL+"/api/v1/admin/branches",
		`{"nama":"Tanpa CSRF","nama_kepala":"Kepala","email":"cs@example.test"}`, cookiesMgr, "")
	if status != http.StatusForbidden {
		t.Fatalf("POST tanpa CSRF harus 403, dapat %d", status)
	}
	assertErrorShape(t, body, "CSRF_INVALID")
	if len(bs.repo.AuditEntries()) != 0 {
		t.Error("permintaan tanpa CSRF tidak boleh mencatat audit")
	}
}

func TestApiDaftardanFilterCabang(t *testing.T) {
	bs := newBranchesServer(t, nil)
	cookies, _ := login(t, bs.srv, "budi@example.test")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, bs.srv.URL+"/api/v1/admin/branches", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("daftar cabang harus 200, dapat %d (%v)", status, body)
	}
	list := body["data"].([]any)
	if len(list) != 2 {
		t.Fatalf("harus ada 2 cabang, dapat %d", len(list))
	}
	pertama := list[0].(map[string]any)
	if pertama["nama"] != "Grahita Cabang A" || pertama["is_cnc"] != true {
		t.Errorf("baris cabang tidak sesuai: %v", pertama)
	}
	if _, ada := pertama["token_prefix"]; ada {
		t.Errorf("cabang tanpa token aktif tidak boleh menyertakan token_prefix: %v", pertama)
	}

	// Filter aktif, special, dan pencarian nama/email.
	for _, k := range []struct {
		query string
		mau   int
	}{
		{"?aktif=true", 1},
		{"?aktif=false", 1},
		{"?cnc=true", 1},
		{"?special=true", 1},
		{"?q=CABANG+B", 1},
		{"?q=b%40example.test", 1},
		{"?q=tidak-ada", 0},
	} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, bs.srv.URL+"/api/v1/admin/branches"+k.query, "", cookies, "")
		if status != http.StatusOK {
			t.Fatalf("filter %s harus 200, dapat %d (%v)", k.query, status, body)
		}
		if got := len(body["data"].([]any)); got != k.mau {
			t.Errorf("filter %s harus %d baris, dapat %d", k.query, k.mau, got)
		}
	}

	// Parameter boolean yang bukan boolean ditolak sebagai validasi.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, bs.srv.URL+"/api/v1/admin/branches?aktif=mungkin", "", cookies, "")
	if status != http.StatusBadRequest {
		t.Fatalf("aktif=mungkin harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")
}

func TestApiCrudCabang(t *testing.T) {
	bs := newBranchesServer(t, nil)
	cookies, csrf := login(t, bs.srv, "budi@example.test")

	// ---- create ----
	// Email dikirim huruf besar untuk memastikan service menormalkannya;
	// spasi tepi ditolak validator transport (sama seperti endpoint lain).
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, bs.srv.URL+"/api/v1/admin/branches",
		`{"nama":"Grahita Cabang C","nama_kepala":"Kepala C","email":"Cabang-C@Example.Test","telepon":"0811","is_cnc":true}`,
		cookies, csrf)
	if status != http.StatusCreated {
		t.Fatalf("buat cabang harus 201, dapat %d (%v)", status, body)
	}
	dibuat := body["data"].(map[string]any)
	id, _ := dibuat["id"].(string)
	if id == "" {
		t.Fatal("respons buat cabang harus memuat id")
	}
	if dibuat["email"] != "cabang-c@example.test" {
		t.Errorf("email harus dinormalkan, dapat %v", dibuat["email"])
	}
	if dibuat["is_active"] != true || dibuat["is_cnc"] != true {
		t.Errorf("flag cabang baru tidak sesuai: %v", dibuat)
	}

	// Email ganda (beda huruf besar/kecil) → 409 EMAIL_TAKEN.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, bs.srv.URL+"/api/v1/admin/branches",
		`{"nama":"Duplikat","nama_kepala":"Kepala","email":"A@EXAMPLE.TEST"}`, cookies, csrf)
	if status != http.StatusConflict {
		t.Fatalf("email ganda harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "EMAIL_TAKEN")

	// Email milik akun admin juga ditolak.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, bs.srv.URL+"/api/v1/admin/branches",
		`{"nama":"Pusat","nama_kepala":"Kepala","email":"pusat@example.test"}`, cookies, csrf)
	if status != http.StatusConflict {
		t.Fatalf("email admin harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "EMAIL_TAKEN")

	// Body tidak valid → 400 dengan field yang menunjuk.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, bs.srv.URL+"/api/v1/admin/branches",
		`{"nama":"X","nama_kepala":"Y","email":"bukan-email"}`, cookies, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("email salah format harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")

	// ---- get ----
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, bs.srv.URL+"/api/v1/admin/branches/"+id, "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("detail cabang harus 200, dapat %d (%v)", status, body)
	}
	if body["data"].(map[string]any)["nama"] != "Grahita Cabang C" {
		t.Errorf("detail cabang tidak sesuai: %v", body["data"])
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, bs.srv.URL+"/api/v1/admin/branches/tidak-ada", "", cookies, "")
	if status != http.StatusNotFound {
		t.Fatalf("cabang tak dikenal harus 404, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "NOT_FOUND")

	// ---- update ----
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, bs.srv.URL+"/api/v1/admin/branches/"+id,
		`{"telepon":"0899","is_active":false,"is_special":true}`, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("ubah cabang harus 200, dapat %d (%v)", status, body)
	}
	diubah := body["data"].(map[string]any)
	if diubah["telepon"] != "0899" || diubah["is_active"] != false || diubah["is_special"] != true {
		t.Errorf("perubahan tidak diterapkan: %v", diubah)
	}
	// Field yang tidak dikirim tetap.
	if diubah["nama"] != "Grahita Cabang C" || diubah["is_cnc"] != true {
		t.Errorf("field lain tidak boleh berubah: %v", diubah)
	}
	// Body kosong = tidak ada perubahan (semua field opsional).
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, bs.srv.URL+"/api/v1/admin/branches/"+id, `{}`, cookies, csrf)
	if status != http.StatusOK || body["data"].(map[string]any)["telepon"] != "0899" {
		t.Errorf("body kosong harus 200 tanpa perubahan, dapat %d (%v)", status, body)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, bs.srv.URL+"/api/v1/admin/branches/tidak-ada",
		`{"telepon":"0222"}`, cookies, csrf)
	if status != http.StatusNotFound {
		t.Errorf("ubah cabang tak dikenal harus 404, dapat %d (%v)", status, body)
	}

	// ---- delete ----
	// Cabang A punya pesanan pada simulasi ini.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodDelete, bs.srv.URL+"/api/v1/admin/branches/br-a", "", cookies, csrf)
	if status != http.StatusConflict {
		t.Fatalf("hapus cabang berp Pesanan harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_HAS_ORDERS")

	status, _, _ = apiReq(t, http.DefaultClient, http.MethodDelete, bs.srv.URL+"/api/v1/admin/branches/"+id, "", cookies, csrf)
	if status != http.StatusNoContent {
		t.Fatalf("hapus cabang tanpa pesanan harus 204, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, bs.srv.URL+"/api/v1/admin/branches/"+id, "", cookies, "")
	if status != http.StatusNotFound {
		t.Errorf("cabang terhapus harus 404, dapat %d", status)
	}

	// Semua mutasi yang BERHASIL tercatat audit (create, 2 update, delete);
	// permintaan yang ditolak tidak.
	entri := bs.repo.AuditEntries()
	aksi := make([]string, 0, len(entri))
	for _, e := range entri {
		aksi = append(aksi, e.Action)
	}
	mau := []string{"create", "update", "update", "delete"}
	if strings.Join(aksi, ",") != strings.Join(mau, ",") {
		t.Errorf("urutan aksi audit tidak sesuai: %v", aksi)
	}
	for _, e := range entri {
		if e.Entity != "branch" || e.ActorID != "adm-mgr" || e.ActorKind != auth.KindAdmin {
			t.Errorf("entri audit tidak lengkap: %+v", e)
		}
	}
}

func TestApiGenerateTokenRegistrasiSekaliPakai(t *testing.T) {
	bs := newBranchesServer(t, nil)
	cookies, csrf := login(t, bs.srv, "budi@example.test")

	// Cabang D baru, belum punya akun dan belum punya token.
	bs.repo.Put(branch.Branch{ID: "br-d", Nama: "Grahita Cabang D", NamaKepala: "Kepala D", Email: "d@example.test", IsActive: true})

	sebelum := time.Now().UTC()
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, bs.srv.URL+"/api/v1/admin/branches/br-d/token", "", cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("generate token harus 200, dapat %d (%v)", status, body)
	}
	data := body["data"].(map[string]any)
	token, _ := data["token"].(string)
	prefix, _ := data["prefix"].(string)
	pola := regexp.MustCompile(`^GRH-[A-Z0-9]{4}-[A-Z0-9]{4}-[A-Z0-9]{4}-[A-Z0-9]{4}$`)
	if !pola.MatchString(token) {
		t.Fatalf("format token registrasi tidak sesuai: %q", token)
	}
	if prefix != token[:8] {
		t.Errorf("prefix harus 8 karakter pertama token, dapat %q", prefix)
	}
	// Masa berlaku = token_hari dari pengaturan (14 hari).
	expStr, _ := data["expires_at"].(string)
	exp, err := time.Parse(time.RFC3339, expStr)
	if err != nil {
		t.Fatalf("expires_at bukan RFC3339: %q (%v)", expStr, err)
	}
	if d := exp.Sub(sebelum); d < 13*24*time.Hour || d > 15*24*time.Hour {
		t.Errorf("masa berlaku harus ~14 hari, dapat %v", d)
	}

	// Yang tersimpan di penyimpanan hanya hash-nya.
	hash, pfx, _, ok := bs.repo.ActiveToken("br-d")
	if !ok {
		t.Fatal("token aktif harus tersimpan")
	}
	if string(hash) != string(auth.HashRegistrationToken(token)) {
		t.Error("hash tersimpan harus sha256 dari token")
	}
	if pfx != prefix {
		t.Errorf("prefix tersimpan tidak sesuai: %q", pfx)
	}
	// Daftar cabang menampilkan prefix (bukan token aslinya).
	_, list, _ := apiReq(t, http.DefaultClient, http.MethodGet, bs.srv.URL+"/api/v1/admin/branches?q=Grahita+Cabang+D", "", cookies, "")
	baris := list["data"].([]any)[0].(map[string]any)
	if baris["token_prefix"] != prefix {
		t.Errorf("daftar cabang harus menampilkan prefix token, dapat %v", baris["token_prefix"])
	}
	if _, ada := baris["token"]; ada {
		t.Error("daftar cabang tidak boleh memuat token asli")
	}

	// Generate ulang: token lama batal, hanya token baru yang sah.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, bs.srv.URL+"/api/v1/admin/branches/br-d/token", "", cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("generate ulang harus 200, dapat %d (%v)", status, body)
	}
	tokenBaru := body["data"].(map[string]any)["token"].(string)
	if tokenBaru == token {
		t.Error("generate ulang harus menghasilkan token berbeda")
	}
	hashBaru, _, _, _ := bs.repo.ActiveToken("br-d")
	if string(hashBaru) != string(auth.HashRegistrationToken(tokenBaru)) {
		t.Error("hash tersimpan harus milik token baru")
	}
	if string(hashBaru) == string(auth.HashRegistrationToken(token)) {
		t.Error("token lama harus tidak berlaku lagi")
	}

	// Cabang yang sudah punya akun tidak butuh token.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, bs.srv.URL+"/api/v1/admin/branches/br-b/token", "", cookies, csrf)
	if status != http.StatusConflict {
		t.Fatalf("cabang berakun harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_HAS_ACCOUNT")

	// Cabang nonaktif tidak boleh memegang token.
	bs.repo.Put(branch.Branch{ID: "br-e", Nama: "Grahita Cabang E", NamaKepala: "Kepala E", Email: "e@example.test", IsActive: false})
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, bs.srv.URL+"/api/v1/admin/branches/br-e/token", "", cookies, csrf)
	if status != http.StatusConflict {
		t.Fatalf("cabang nonaktif harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BRANCH_INACTIVE")

	// Cabang tak dikenal → 404.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, bs.srv.URL+"/api/v1/admin/branches/tidak-ada/token", "", cookies, csrf)
	if status != http.StatusNotFound {
		t.Errorf("cabang tak dikenal harus 404, dapat %d (%v)", status, body)
	}

	// Dua generate yang berhasil tercatat audit; penolakan tidak.
	entri := bs.repo.AuditEntries()
	if len(entri) != 2 {
		t.Fatalf("harus ada 2 entri audit token_generate, dapat %d", len(entri))
	}
	for i, e := range entri {
		if e.Action != "token_generate" || e.EntityID != "br-d" {
			t.Errorf("entri audit %d tidak sesuai: %+v", i, e)
		}
	}
	if entri[0].Before != nil || entri[1].Before == nil {
		t.Errorf("generate kedua harus mencatat token lama di before: %+v / %+v", entri[0].Before, entri[1].Before)
	}
}

// TestApiKontrakPathMasterCabang memastikan endpoint master cabang dan kode
// error barunya benar-benar tercatat di kontrak OpenAPI.
func TestApiKontrakPathMasterCabang(t *testing.T) {
	raw, err := readOpenAPI(t)
	if err != nil {
		t.Fatalf("baca kontrak: %v", err)
	}
	spec := parsePathMetode(raw)
	wajib := map[string][]string{
		"/api/v1/admin/branches":            {"GET", "POST"},
		"/api/v1/admin/branches/{id}":       {"GET", "PUT", "DELETE"},
		"/api/v1/admin/branches/{id}/token": {"POST"},
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
	for _, kode := range []string{"BRANCH_HAS_ORDERS", "BRANCH_HAS_ACCOUNT", "BRANCH_INACTIVE", "RegistrationToken"} {
		if !strings.Contains(raw, kode) {
			t.Errorf("%s belum tercantum di kontrak OpenAPI", kode)
		}
	}
}
