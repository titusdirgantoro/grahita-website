package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/audit"
)

// auditStub menggantikan pembaca audit sungguhan pada uji HTTP in-memory; ia
// merekam Query yang diterima sehingga filter/paginasi bisa diperiksa tanpa
// database.
type auditStub struct {
	items []audit.Log
	total int64
	query audit.Query
}

func (a *auditStub) List(_ context.Context, q audit.Query) ([]audit.Log, error) {
	a.query = q
	return a.items, nil
}

func (a *auditStub) Count(_ context.Context, q audit.Query) (int64, error) { return a.total, nil }

// auditSample adalah satu entri contoh; before-nya sengaja memuat kunci
// sensitif untuk membuktikan layanan tetap meredaksinya saat menampilkan.
func auditSample() []audit.Log {
	return []audit.Log{{
		ID:        "11111111-1111-4111-8111-111111111111",
		At:        time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC),
		ActorKind: "admin",
		ActorID:   "22222222-2222-4222-8222-222222222222",
		ActorNama: "Rina",
		IP:        "203.0.113.9",
		Entity:    "order",
		EntityID:  "33333333-3333-4333-8333-333333333333",
		Action:    "revise",
		Before:    map[string]any{"total": 100000, "token": "rahasia"},
		After:     map[string]any{"total": 120000, "alasan": "harga naik", "delta_total": 20000},
	}}
}

func TestApiAuditLogHanyaSuperadmin(t *testing.T) {
	stub := &auditStub{items: auditSample(), total: 42}
	as := newAuthServer(t, func(d *Deps) { d.Audit = audit.NewService(stub) })

	// Akun cabang dan Manager ditolak 403 — bukan hanya menunya disembunyikan.
	cabang, _ := login(t, as.srv, "a@example.test")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		as.srv.URL+"/api/v1/admin/audit-logs", "", cabang, "")
	if status != http.StatusForbidden {
		t.Fatalf("akun cabang harus 403, dapat %d %v", status, body)
	}
	assertErrorShape(t, body, "FORBIDDEN")

	mgr, _ := login(t, as.srv, "budi@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		as.srv.URL+"/api/v1/admin/audit-logs", "", mgr, "")
	if status != http.StatusForbidden {
		t.Fatalf("Manager harus 403, dapat %d %v", status, body)
	}
	assertErrorShape(t, body, "FORBIDDEN")

	// Tanpa sesi: 401.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		as.srv.URL+"/api/v1/admin/audit-logs", "", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("tanpa sesi harus 401, dapat %d %v", status, body)
	}

	// Superadmin: 200 dan bentuk halaman lengkap.
	sa, _ := login(t, as.srv, "rina@example.test")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		as.srv.URL+"/api/v1/admin/audit-logs?entitas=order,branch&aksi=REVISE&aktor=Rina&halaman=2&per_halaman=5",
		"", sa, "")
	if status != http.StatusOK {
		t.Fatalf("Superadmin harus 200, dapat %d %v", status, body)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("respons tanpa data: %v", body)
	}
	if data["total"].(float64) != 42 || data["halaman"].(float64) != 2 ||
		data["per_halaman"].(float64) != 5 || data["total_halaman"].(float64) != 9 {
		t.Errorf("ringkasan halaman salah: %v", data)
	}
	// Filter yang diteruskan ke repository sudah ternormalkan.
	if got := stub.query.Entitas; len(got) != 2 || got[0] != "branch" || got[1] != "order" {
		t.Errorf("entitas tidak ternormalkan: %v", got)
	}
	if stub.query.Aksi != "revise" || stub.query.ActorNama != "Rina" || stub.query.ActorID != "" {
		t.Errorf("filter aktor/aksi salah: %+v", stub.query)
	}
	if stub.query.Offset != 5 || stub.query.Batch != 5 {
		t.Errorf("paginasi salah: %+v", stub.query)
	}
	// Entri yang dikirim sudah bersih dari kunci sensitif.
	items := data["items"].([]any)
	entry := items[0].(map[string]any)
	before := entry["before"].(map[string]any)
	if before["token"] != audit.NilaiRedaksi {
		t.Errorf("snapshot before harus diredaksi saat ditampilkan: %v", before)
	}
	if entry["action"] != "revise" || entry["actor_nama"] != "Rina" {
		t.Errorf("entri audit tidak lengkap: %v", entry)
	}
}

func TestApiAuditLogFilterTidakValidDitolak(t *testing.T) {
	as := newAuthServer(t, func(d *Deps) { d.Audit = audit.NewService(&auditStub{}) })
	sa, _ := login(t, as.srv, "rina@example.test")

	kasus := []string{
		"?entitas=pesanan",
		"?aksi=hapus_semua",
		"?dari=01-10-2026",
		"?dari=2026-10-09&sampai=2026-10-01",
		"?halaman=abc",
		"?per_halaman=dua",
	}
	for _, q := range kasus {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
			as.srv.URL+"/api/v1/admin/audit-logs"+q, "", sa, "")
		if status != http.StatusBadRequest {
			t.Errorf("%s harus 400, dapat %d %v", q, status, body)
			continue
		}
		assertErrorShape(t, body, "VALIDATION_FAILED")
	}
}

func TestApiAuditLogTanpaLayanan(t *testing.T) {
	as := newAuthServer(t, nil)
	sa, _ := login(t, as.srv, "rina@example.test")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
		as.srv.URL+"/api/v1/admin/audit-logs", "", sa, "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("tanpa layanan audit harus 503, dapat %d %v", status, body)
	}
	assertErrorShape(t, body, "UNAVAILABLE")
}
