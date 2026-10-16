package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
)

// adminOrderRepoUji adalah menyimpan riwayat admin in-memory untuk uji HTTP:
// ia merekam filter yang diterima handler dan mengembalikan jawaban tetap.
type adminOrderRepoUji struct {
	terakhir order.AdminQuery
	panggil  int

	halaman order.AdminPage
	detail  order.AdminDetail
	err     error

	noteCatatan string
	noteId      string
}

func (f *adminOrderRepoUji) ListAdmin(_ context.Context, q order.AdminQuery) (order.AdminPage, error) {
	f.panggil++
	f.terakhir = q
	if f.err != nil {
		return order.AdminPage{}, f.err
	}
	return f.halaman, nil
}

func (f *adminOrderRepoUji) GetAdmin(_ context.Context, id string, _ []string) (order.AdminDetail, error) {
	if f.err != nil {
		return order.AdminDetail{}, f.err
	}
	d := f.detail
	d.ID = id
	return d, nil
}

func (f *adminOrderRepoUji) SetNoteAdmin(_ context.Context, id, note, _, _, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.noteId = id
	f.noteCatatan = note
	return nil
}

// adminOrdersServer adalah router lengkap dengan riwayat admin di atas
// penyimpanan semu.
type adminOrdersServer struct {
	srv  *httptest.Server
	repo *adminOrderRepoUji
	ord  *order.Memory
}

func newAdminOrdersServer(t *testing.T) *adminOrdersServer {
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
		UserID: "adm-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager,
		Nama: "Budi", Email: "budi@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})

	settingsSvc := settings.NewService(&fakeSettingsRepo{values: map[string]string{
		"biaya_packaging": "3000",
		"cnc_hari":        "7",
	}})

	ordRepo := order.NewMemory()
	ordRepo.Put(order.Order{
		ID: orderSubmittedID, BranchID: "br-a", Status: order.StatusSubmitted,
		Nomor: "2026-10-01", Tanggal: "2026-10-08", VerifStatus: "belum",
		Lines: []order.Line{{LineNo: 1, Jenis: order.JenisProduct, ProductID: "p1", Nama: "Bio Magnetik", Qty: 2, HargaSatuan: 100000}},
	})

	repo := &adminOrderRepoUji{
		halaman: order.AdminPage{
			Items: []order.AdminRow{{
				ID: orderSubmittedID, Nomor: "2026-10-01", Versi: 0, BranchID: "br-a",
				BranchNama: "Grahita Cabang A", Tanggal: "2026-10-08", Status: "submitted",
				VerifStatus: "belum", Total: 200000, JumlahItem: 2, CreatedAt: "2026-10-08T03:00:00Z",
				UpdatedAt: "2026-10-08T03:00:00Z",
			}},
			Total: 1,
		},
		detail: order.AdminDetail{
			Nomor: "2026-10-01", BranchID: "br-a", BranchNama: "Grahita Cabang A",
			BranchKepala: "Kepala A", BranchEmail: "cabang-a@example.test",
			Status: "submitted", VerifStatus: "belum", Catatan: "dikirim pagi",
			NoteAdmin: "hubungi cabang", Total: 200000, JumlahItem: 2,
			CreatedAt: "2026-10-08T03:00:00Z", UpdatedAt: "2026-10-08T03:00:00Z",
			Lines: []order.Line{{LineNo: 1, Jenis: order.JenisProduct, Nama: "Bio Magnetik", Qty: 2, HargaSatuan: 100000, Subtotal: 200000, SerialNumber: "20261008001"}},
		},
	}

	svc := auth.NewService(store, store, store, clock.System(), 8*time.Hour, 30*24*time.Hour)
	deps := Deps{
		Config:       testConfig(),
		Version:      "test-admin-orders",
		Health:       fakeHealth{migrasi: 11},
		Auth:         svc,
		Settings:     settingsSvc,
		Orders:       order.NewService(ordRepo, nil, settingsSvc),
		AdminOrders:  order.NewAdminService(repo),
		LoginLimiter: httpx.NewRateLimiter(50, 15*time.Minute),
	}
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	return &adminOrdersServer{srv: srv, repo: repo, ord: ordRepo}
}

// TestAdminRiwayatOtorisasiDanFilter menempuh daftar riwayat admin lewat HTTP:
// akses Manager, penolakan cabang, dan filter yang diteruskan ke domain.
func TestAdminRiwayatOtorisasiDanFilter(t *testing.T) {
	as := newAdminOrdersServer(t)

	// Akun cabang ditolak (PRD §2.1).
	cc, csrfC := login(t, as.srv, "a@example.test")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/admin/orders", "", cc, csrfC)
	if status != http.StatusForbidden {
		t.Fatalf("cabang tidak boleh melihat riwayat admin, dapat %d (%v)", status, body)
	}

	ac, csrf := login(t, as.srv, "budi@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/admin/orders", "", ac, csrf)
	if status != http.StatusOK {
		t.Fatalf("daftar riwayat harus 200, dapat %d (%v)", status, body)
	}
	data := dataMap(t, body)
	if data["total"] != float64(1) || data["per_halaman"] != float64(order.DefaultPerHalaman) {
		t.Fatalf("metadata paginasi salah: %v", data)
	}
	items, _ := data["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("harus 1 baris, dapat %v", data["items"])
	}
	baris := items[0].(map[string]any)
	if baris["branch_nama"] != "Grahita Cabang A" || baris["nomor"] != "2026-10-01" {
		t.Fatalf("baris riwayat salah: %v", baris)
	}

	// Filter lengkap diteruskan ke domain (bukan diabaikan handler).
	cari := as.srv.URL + "/api/v1/admin/orders?cari=bio&cabang=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" +
		"&status=submitted&verif=belum&cnc=true&terlambat=true&email_gagal=true" +
		"&dari=2026-10-01&sampai=2026-10-31&grup=bulan&halaman=2&per_halaman=10"
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, cari, "", ac, csrf)
	if status != http.StatusOK {
		t.Fatalf("daftar dengan filter harus 200, dapat %d (%v)", status, body)
	}
	q := as.repo.terakhir
	if q.Cari != "bio" || q.Status != "submitted" || q.VerifStatus != "belum" || q.Grup != "bulan" {
		t.Fatalf("filter teks/status salah: %+v", q)
	}
	if q.IsCNC == nil || !*q.IsCNC || q.Terlambat == nil || !*q.Terlambat || q.EmailGagal == nil || !*q.EmailGagal {
		t.Fatalf("filter boolean tidak diteruskan: %+v", q)
	}
	if q.Limit != 10 || q.Offset != 10 {
		t.Fatalf("paginasi salah: %+v", q)
	}
	if q.Dari == nil || q.Sampai == nil {
		t.Fatalf("rentang tanggal tidak diteruskan: %+v", q)
	}
}

// TestAdminRiwayatFilterTidakValid menjamin parameter salah tidak diam-diam
// diabaikan.
func TestAdminRiwayatFilterTidakValid(t *testing.T) {
	as := newAdminOrdersServer(t)
	ac, csrf := login(t, as.srv, "budi@example.test")

	for _, q := range []string{
		"?status=batal", "?verif=entah", "?grup=kuartal", "?dari=08-10-2026",
		"?cnc=mungkin", "?halaman=abc", "?per_halaman=abc",
		"?per_halaman=5000x", "?cabang=bukan-uuid", "?dari=2026-10-31&sampai=2026-10-01",
	} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/admin/orders"+q, "", ac, csrf)
		if status != http.StatusBadRequest {
			t.Fatalf("filter %s harus 400, dapat %d (%v)", q, status, body)
		}
		assertErrorShape(t, body, "VALIDATION_FAILED")
	}
}

// TestAdminDetailDanCatatanInternal menempuh detail + catatan internal admin.
func TestAdminDetailDanCatatanInternal(t *testing.T) {
	as := newAdminOrdersServer(t)
	ac, csrf := login(t, as.srv, "budi@example.test")

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/admin/orders/"+orderSubmittedID, "", ac, csrf)
	if status != http.StatusOK {
		t.Fatalf("detail admin harus 200, dapat %d (%v)", status, body)
	}
	d := dataMap(t, body)
	if d["branch_kepala"] != "Kepala A" || d["note_admin"] != "hubungi cabang" {
		t.Fatalf("detail tidak lengkap: %v", d)
	}
	if _, ada := d["lines"].([]any); !ada {
		t.Fatalf("detail harus memuat baris: %v", d)
	}

	// Id yang bukan UUID dijawab 404 (bukan 500).
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/admin/orders/tidak-ada", "", ac, csrf)
	if status != http.StatusNotFound {
		t.Fatalf("id bukan uuid harus 404, dapat %d (%v)", status, body)
	}

	// Catatan internal: CSRF wajib.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, as.srv.URL+"/api/v1/admin/orders/"+orderSubmittedID+"/notes",
		`{"catatan":"tindak lanjut"}`, ac, "")
	if status != http.StatusForbidden {
		t.Fatalf("tanpa CSRF harus 403, dapat %d (%v)", status, body)
	}

	// Catatan kosong ditolak 400.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, as.srv.URL+"/api/v1/admin/orders/"+orderSubmittedID+"/notes",
		`{"catatan":"   "}`, ac, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("catatan kosong harus 400, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "VALIDATION_FAILED")

	// Field tak dikenal ditolak (bukan diam-diam diterima).
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, as.srv.URL+"/api/v1/admin/orders/"+orderSubmittedID+"/notes",
		`{"catatan":"x","admin_id":"palsu"}`, ac, csrf)
	if status != http.StatusBadRequest {
		t.Fatalf("field tak dikenal harus 400, dapat %d (%v)", status, body)
	}

	// Simpan catatan yang sah.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, as.srv.URL+"/api/v1/admin/orders/"+orderSubmittedID+"/notes",
		`{"catatan":"  hubungi cabang  "}`, ac, csrf)
	if status != http.StatusOK {
		t.Fatalf("simpan catatan harus 200, dapat %d (%v)", status, body)
	}
	if as.repo.noteCatatan != "hubungi cabang" || as.repo.noteId != orderSubmittedID {
		t.Fatalf("catatan tidak tersimpan: %+v", as.repo)
	}

	// Catatan internal TIDAK pernah muncul di portal cabang.
	cc, csrfC := login(t, as.srv, "a@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet, as.srv.URL+"/api/v1/orders/"+orderSubmittedID, "", cc, csrfC)
	if status != http.StatusOK {
		t.Fatalf("detail cabang harus 200, dapat %d (%v)", status, body)
	}
	if _, ada := dataMap(t, body)["note_admin"]; ada {
		t.Fatalf("catatan internal bocor ke portal cabang: %v", body)
	}
	mentah, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	if json.Valid(mentah) == false {
		t.Fatalf("body cabang bukan JSON valid: %s", mentah)
	}
}
