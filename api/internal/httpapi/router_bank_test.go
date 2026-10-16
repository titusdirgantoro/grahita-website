package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
)

type bankOrderRepo struct{}

const bankOrderID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func (bankOrderRepo) ListAdmin(context.Context, order.AdminQuery) (order.AdminPage, error) {
	return order.AdminPage{}, nil
}

func (bankOrderRepo) GetAdmin(_ context.Context, id string, _ []string) (order.AdminDetail, error) {
	if id != bankOrderID {
		return order.AdminDetail{}, order.ErrNotFound
	}
	return order.AdminDetail{ID: id, BranchID: "br-a", Status: order.StatusSubmitted}, nil
}

func (bankOrderRepo) SetNoteAdmin(context.Context, string, string, string, string, string) error {
	return nil
}

// bankServer adalah router lengkap untuk menguji rekening bank lewat HTTP:
// penyimpanan auth in-memory, penyimpanan bank in-memory (yang merekam audit),
// dan penyimpanan pengaturan in-memory.
type bankServer struct {
	srv      *httptest.Server
	repo     *bank.Memory
	settings *fakeSettingsRepo
}

func newBankServer(t *testing.T) *bankServer {
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

	repo := bank.NewMemory()
	repo.Put(bank.Account{ID: "bk-aktif", Bank: bank.BankBCA, NamaPemilik: "PT Grahita Indonesia", Nomor: "1234567890", IsActive: true})
	repo.Put(bank.Account{ID: "bk-nonaktif", Bank: bank.BankBRI, NamaPemilik: "PT Grahita Indonesia", Nomor: "0987654321", IsActive: false})
	repo.PutBranch("br-a", true)
	repo.Assign("br-a", "bk-aktif")

	setRepo := &fakeSettingsRepo{values: map[string]string{"biaya_packaging": "3000", "cnc_hari": "7"}}

	svc := auth.NewService(store, store, store, clock.System(), 8*time.Hour, 30*24*time.Hour)
	deps := Deps{
		Config:        testConfig(),
		Version:       "test-bank",
		Health:        fakeHealth{migrasi: 9},
		Auth:          svc,
		Settings:      settings.NewService(setRepo),
		Bank:          bank.NewService(repo),
		AdminOrders:   order.NewAdminService(bankOrderRepo{}),
		LoginLimiter:  httpx.NewRateLimiter(5, 15*time.Minute),
		ForgotLimiter: httpx.NewRateLimiter(5, 15*time.Minute),
	}

	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	return &bankServer{srv: srv, repo: repo, settings: setRepo}
}

func TestApiRekeningEfektifPesananUntukVerifikasi(t *testing.T) {
	bs := newBankServer(t)
	base := bs.srv.URL + "/api/v1/admin/orders/" + bankOrderID + "/bank-accounts"

	mgrCookies, _ := login(t, bs.srv, "budi@example.test")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base, "", mgrCookies, "")
	if status != http.StatusOK {
		t.Fatalf("manager harus dapat membaca rekening verifikasi: %d (%v)", status, body)
	}
	data := body["data"].(map[string]any)
	accounts := data["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("harus ada satu rekening efektif, dapat %v", accounts)
	}
	account := accounts[0].(map[string]any)
	if account["display_nomor"] != "••••7890" {
		t.Errorf("nomor harus termasking, dapat %v", account["display_nomor"])
	}
	if _, bocor := account["nomor"]; bocor {
		t.Error("nomor rekening penuh tidak boleh bocor ke endpoint verifikasi")
	}
	if _, ada := data["allowlist_version"]; !ada {
		t.Error("versi allowlist harus disertakan")
	}

	cabangCookies, _ := login(t, bs.srv, "a@example.test")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, base, "", cabangCookies, "")
	if status != http.StatusForbidden {
		t.Fatalf("akun cabang harus ditolak dari endpoint admin, dapat %d", status)
	}
}

func TestApiBankOtorisasiSuperadmin(t *testing.T) {
	bs := newBankServer(t)
	base := bs.srv.URL + "/api/v1"

	adminEndpoints := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/admin/bank-accounts", ""},
		{http.MethodPost, "/admin/bank-accounts", `{"bank":"BCA","nama_pemilik":"PT Grahita","nomor":"111222333"}`},
		{http.MethodPut, "/admin/bank-accounts/bk-aktif", `{"is_active":false}`},
	}

	// Tanpa sesi → 401; mutasi tanpa CSRF ditolak lebih dulu oleh middleware
	// CSRF (403), jadi yang diperiksa per metode.
	for _, e := range adminEndpoints {
		status, body, _ := apiReq(t, http.DefaultClient, e.method, base+e.path, e.body, nil, "")
		if e.method == http.MethodGet {
			if status != http.StatusUnauthorized {
				t.Errorf("tanpa sesi %s %s harus 401, dapat %d", e.method, e.path, status)
			}
			assertErrorShape(t, body, "UNAUTHENTICATED")
			continue
		}
		if status != http.StatusForbidden {
			t.Errorf("tanpa sesi %s %s harus 403 CSRF, dapat %d", e.method, e.path, status)
		}
		assertErrorShape(t, body, "CSRF_INVALID")
	}

	// Akun cabang & Manager tidak punya kapabilitas rekening bank (PRD §2.1).
	cabangCookies, cabangCsrf := login(t, bs.srv, "a@example.test")
	mgrCookies, mgrCsrf := login(t, bs.srv, "budi@example.test")
	for _, aktor := range []struct {
		nama    string
		cookies []*http.Cookie
		csrf    string
	}{{"cabang", cabangCookies, cabangCsrf}, {"manager", mgrCookies, mgrCsrf}} {
		for _, e := range adminEndpoints {
			status, body, _ := apiReq(t, http.DefaultClient, e.method, base+e.path, e.body, aktor.cookies, aktor.csrf)
			if status != http.StatusForbidden {
				t.Errorf("%s %s %s harus 403, dapat %d (%v)", aktor.nama, e.method, e.path, status, body)
			}
		}
	}

	// Mutasi tanpa CSRF ditolak walau rolenya benar.
	saCookies, saCsrf := login(t, bs.srv, "rina@example.test")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, base+"/admin/bank-accounts",
		`{"bank":"BCA","nama_pemilik":"PT Grahita","nomor":"444555666"}`, saCookies, "")
	if status != http.StatusForbidden {
		t.Fatalf("POST tanpa CSRF harus 403, dapat %d", status)
	}
	assertErrorShape(t, body, "CSRF_INVALID")
	for _, rec := range bs.repo.AuditEntries() {
		if rec.Action == auditActionCreate {
			t.Error("permintaan tanpa CSRF tidak boleh mencatat audit")
		}
	}
	if saCsrf == "" {
		t.Fatal("cookie CSRF superadmin tidak ditemukan")
	}
}

func TestApiBankCRUDDanAudit(t *testing.T) {
	bs := newBankServer(t)
	base := bs.srv.URL + "/api/v1"
	cookies, csrf := login(t, bs.srv, "rina@example.test")
	cabangCookies, _ := login(t, bs.srv, "a@example.test")

	// Daftar admin memuat rekening aktif DAN nonaktif (untuk dikelola SA).
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/bank-accounts", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("daftar rekening harus 200, dapat %d", status)
	}
	if list := body["data"].([]any); len(list) != 2 {
		t.Fatalf("harus 2 rekening (aktif+nonaktif), dapat %d", len(list))
	}

	// Buat rekening baru.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, base+"/admin/bank-accounts",
		`{"bank":"BCA","nama_pemilik":"PT Grahita Baru","nomor":"555 666 777"}`, cookies, csrf)
	if status != http.StatusCreated {
		t.Fatalf("buat rekening harus 201, dapat %d (%v)", status, body)
	}
	baru := body["data"].(map[string]any)
	if baru["nomor"] != "555666777" {
		t.Errorf("nomor harus dinormalisasi tanpa spasi, dapat %v", baru["nomor"])
	}
	if baru["is_active"] != true {
		t.Errorf("rekening baru harus aktif, dapat %v", baru["is_active"])
	}

	// Duplikat (bank+nomor) → 409.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, base+"/admin/bank-accounts",
		`{"bank":"BCA","nama_pemilik":"PT Grahita Lain","nomor":"555666777"}`, cookies, csrf)
	if status != http.StatusConflict {
		t.Fatalf("duplikat harus 409, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "BANK_DUPLICATE")

	// Validasi domain: bank tak dikenal & nomor bukan angka → 400 per-field.
	for _, k := range []struct{ nama, body string }{
		{"bank tak dikenal", `{"bank":"BNI","nama_pemilik":"PT Grahita","nomor":"123456789"}`},
		{"nomor bukan angka", `{"bank":"BCA","nama_pemilik":"PT Grahita","nomor":"abcdefghij"}`},
	} {
		status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, base+"/admin/bank-accounts", k.body, cookies, csrf)
		if status != http.StatusBadRequest {
			t.Errorf("%s harus 400, dapat %d (%v)", k.nama, status, body)
		}
		assertErrorShape(t, body, "VALIDATION_FAILED")
	}

	// Daftar customer hanya memuat rekening efektif cabang sesi. Rekening baru
	// yang aktif global tetapi belum assigned tidak boleh bocor sebagai fallback.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/bank-accounts", "", cabangCookies, "")
	if status != http.StatusOK {
		t.Fatalf("rekening publik harus 200, dapat %d", status)
	}
	ids := map[string]bool{}
	for _, row := range body["data"].([]any) {
		ids[row.(map[string]any)["id"].(string)] = true
	}
	if len(ids) != 1 || !ids["bk-aktif"] {
		t.Fatalf("hanya rekening efektif cabang yang boleh tampil, dapat %v", ids)
	}
	if ids["bk-nonaktif"] {
		t.Error("rekening nonaktif tidak boleh muncul di daftar publik")
	}

	// Nonaktifkan rekening assigned: daftar menjadi kosong, tanpa fallback bk-1.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/bank-accounts/bk-aktif",
		`{"is_active":false}`, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("nonaktifkan harus 200, dapat %d (%v)", status, body)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/bank-accounts", "", cabangCookies, "")
	if status != http.StatusOK {
		t.Fatalf("rekening publik harus 200, dapat %d", status)
	}
	pubList := body["data"].([]any)
	if len(pubList) != 0 {
		t.Fatalf("rekening assigned nonaktif harus hilang tanpa fallback, dapat %v", pubList)
	}

	// Daftar admin tetap memuat keduanya.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, base+"/admin/bank-accounts", "", cookies, "")
	if status != http.StatusOK {
		t.Fatalf("daftar admin harus 200, dapat %d", status)
	}
	if list := body["data"].([]any); len(list) != 3 {
		t.Errorf("daftar admin harus memuat semua rekening (3), dapat %d", len(list))
	}

	// Update tak ditemukan → 404.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, base+"/admin/bank-accounts/tidak-ada",
		`{"is_active":true}`, cookies, csrf)
	if status != http.StatusNotFound {
		t.Errorf("update rekening tak ada harus 404, dapat %d", status)
	}

	// Audit: create + update tercatat, aktor superadmin.
	kinds := map[string]int{}
	for _, rec := range bs.repo.AuditEntries() {
		kinds[rec.Entity+"/"+rec.Action]++
		if rec.ActorID != "adm-sa" {
			t.Errorf("aktor audit harus superadmin, dapat %s", rec.ActorID)
		}
	}
	if kinds["bank_account/create"] != 1 || kinds["bank_account/update"] != 1 {
		t.Errorf("audit tidak sesuai: %v", kinds)
	}
}

// auditActionCreate dipakai agar uji tidak bergantung literal string.
const auditActionCreate = "create"
