package users_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/users"
)

// harnessAudit menyiapkan service dengan penyimpanan in-memory yang mencatat
// entri audit transaksionalnya, plus resetter palsu yang menangkap fakta audit
// yang ikut ditulis bersama token atur ulang (F2-28).
func harnessAudit(t *testing.T) *harness {
	t.Helper()
	repo := users.NewMemory()
	resetter := &fakeResetter{}
	sessions := &fakeSessions{}
	queue := &fakeQueue{}
	svc := users.NewService(repo, resetter, sessions,
		users.WithEmailQueue(queue),
		users.WithResetURLBuilder(func(auth.Kind, string) string { return "https://admin.example.test/lupa" }),
	)
	return &harness{svc: svc, repo: repo, resetter: resetter, sessions: sessions, queue: queue}
}

// emailDiSnapshot memastikan tidak ada kunci `email` di mana pun pada snapshot.
func emailDiSnapshot(t *testing.T, nama string, v any) {
	t.Helper()
	switch val := v.(type) {
	case map[string]any:
		for k, sub := range val {
			if strings.Contains(strings.ToLower(k), "email") {
				t.Errorf("%s: snapshot memuat kunci %q (PII tidak boleh masuk audit)", nama, k)
			}
			emailDiSnapshot(t, nama, sub)
		}
	case []any:
		for _, sub := range val {
			emailDiSnapshot(t, nama, sub)
		}
	}
}

func TestAuditUndanganAdminSatuEventAman(t *testing.T) {
	h := harnessAudit(t)
	aktor := adminActor(auth.RoleSuperadmin)

	admin, err := h.svc.InviteAdmin(context.Background(), aktor, users.NewAdmin{
		Nama: "Dewi", Email: "Dewi@Example.Test", Role: auth.RoleManager, NotifInvoice: true,
	}, 24*time.Hour)
	if err != nil {
		t.Fatalf("undang admin: %v", err)
	}

	recs := h.repo.AuditEntries()
	if len(recs) != 1 {
		t.Fatalf("undangan harus menghasilkan TEPAT satu entri audit, dapat %d: %+v", len(recs), recs)
	}
	e := recs[0]
	if e.Entity != audit.EntityAdminUser || e.Action != audit.ActionCreate {
		t.Errorf("entitas/aksi salah: %s/%s", e.Entity, e.Action)
	}
	if e.EntityID != admin.ID {
		t.Errorf("entity_id harus id akun baru (%s), dapat %q", admin.ID, e.EntityID)
	}
	if e.ActorID != aktor.UserID || e.ActorNama != aktor.Nama {
		t.Errorf("aktor tidak tercatat: %+v", e)
	}
	if e.Before != nil {
		t.Errorf("create tidak boleh punya before, dapat %v", e.Before)
	}
	after := e.After.(map[string]any)
	if after["nama"] != "Dewi" || after["role"] != "manager" || after["is_active"] != true {
		t.Errorf("snapshot after salah: %v", after)
	}
	emailDiSnapshot(t, "undangan admin", e.After)
}

func TestAuditUbahAdminTepatSatuEventDanNoOpTanpaEvent(t *testing.T) {
	h := harnessAudit(t)
	aktif := time.Now().UTC()
	h.repo.PutAdmin(users.AdminUser{ID: "adm-sa", Nama: "Rina", Email: "rina@example.test", Role: auth.RoleSuperadmin, IsActive: true, ActivatedAt: &aktif})
	h.repo.PutAdmin(users.AdminUser{ID: "adm-mgr", Nama: "Budi", Email: "budi@example.test", Role: auth.RoleManager, IsActive: true, ActivatedAt: &aktif})
	aktor := adminActor(auth.RoleSuperadmin)
	naik := auth.RoleSuperadmin

	if _, err := h.svc.UpdateAdmin(context.Background(), aktor, "adm-mgr", users.AdminPatch{Role: &naik}); err != nil {
		t.Fatalf("ubah role: %v", err)
	}
	recs := h.repo.AuditEntries()
	if len(recs) != 1 {
		t.Fatalf("satu perubahan harus satu entri audit, dapat %d", len(recs))
	}
	e := recs[0]
	if e.Entity != audit.EntityAdminUser || e.Action != audit.ActionUpdate || e.EntityID != "adm-mgr" {
		t.Errorf("entri audit salah: %+v", e)
	}
	before := e.Before.(map[string]any)
	after := e.After.(map[string]any)
	if before["role"] != "manager" || after["role"] != "superadmin" {
		t.Errorf("before/after tidak menunjukkan delta role: %v → %v", before, after)
	}
	emailDiSnapshot(t, "ubah admin", e.Before)
	emailDiSnapshot(t, "ubah admin", e.After)

	// Kirim ulang nilai yang sama = no-op: tidak ada entri baru.
	sama := "Budi Santoso"
	if _, err := h.svc.UpdateAdmin(context.Background(), aktor, "adm-mgr", users.AdminPatch{Nama: &sama}); err != nil {
		t.Fatalf("ubah nama: %v", err)
	}
	if _, err := h.svc.UpdateAdmin(context.Background(), aktor, "adm-mgr", users.AdminPatch{Nama: &sama}); err != nil {
		t.Fatalf("ubah nama (no-op): %v", err)
	}
	if got := len(h.repo.AuditEntries()); got != 2 {
		t.Errorf("no-op tidak boleh menambah entri audit: %d entri", got)
	}
}

func TestAuditUbahAdminYangDitolakTidakMeninggalkanEvent(t *testing.T) {
	h := harnessAudit(t)
	aktif := time.Now().UTC()
	h.repo.PutAdmin(users.AdminUser{ID: "adm-sa", Nama: "Rina", Email: "rina@example.test", Role: auth.RoleSuperadmin, IsActive: true, ActivatedAt: &aktif})
	aktor := adminActor(auth.RoleSuperadmin)
	turun := auth.RoleManager

	if _, err := h.svc.UpdateAdmin(context.Background(), aktor, "adm-sa", users.AdminPatch{Role: &turun}); err == nil {
		t.Fatal("menurunkan superadmin terakhir harus ditolak")
	}
	if got := len(h.repo.AuditEntries()); got != 0 {
		t.Errorf("permintaan yang ditolak tidak boleh meninggalkan entri audit: %d", got)
	}
}

func TestAuditStatusAkunCabangSatuEventDanNoOp(t *testing.T) {
	h := harnessAudit(t)
	h.repo.PutBranch(users.BranchUser{BranchID: "br-a", UserID: "usr-a", Nama: "Grahita A", Email: "a@example.test", IsActive: true, BranchActive: true})
	aktor := adminActor(auth.RoleManager)

	if _, err := h.svc.SetBranchUserActive(context.Background(), aktor, "usr-a", false); err != nil {
		t.Fatalf("nonaktifkan: %v", err)
	}
	recs := h.repo.AuditEntries()
	if len(recs) != 1 {
		t.Fatalf("satu perubahan status harus satu entri, dapat %d", len(recs))
	}
	e := recs[0]
	if e.Entity != audit.EntityBranchUser || e.Action != audit.ActionUpdate || e.EntityID != "usr-a" {
		t.Errorf("entri audit salah: %+v", e)
	}
	if e.Before.(map[string]any)["is_active"] != true || e.After.(map[string]any)["is_active"] != false {
		t.Errorf("before/after status salah: %v → %v", e.Before, e.After)
	}
	if e.ActorKind != auth.KindAdmin || e.ActorID != aktor.UserID {
		t.Errorf("aktor admin tidak tercatat: %+v", e)
	}

	// Mengirim status yang sama = no-op.
	if _, err := h.svc.SetBranchUserActive(context.Background(), aktor, "usr-a", false); err != nil {
		t.Fatalf("nonaktifkan ulang: %v", err)
	}
	if got := len(h.repo.AuditEntries()); got != 1 {
		t.Errorf("no-op tidak boleh menambah entri audit: %d", got)
	}

	// Akun tak dikenal: galat, tanpa entri.
	if _, err := h.svc.SetBranchUserActive(context.Background(), aktor, "tidak-ada", true); err == nil {
		t.Fatal("akun tak dikenal harus galat")
	}
	if got := len(h.repo.AuditEntries()); got != 1 {
		t.Errorf("galat tidak boleh menambah entri audit: %d", got)
	}
}

// TestAuditTautanResetDanKirimUlangUndanganLewatJalurToken membuktikan kedua
// aksi yang perubahannya hidup di repositori auth (penerbitan tautan atur ulang
// akun cabang dan kirim ulang undangan admin) MENITIPKAN fakta auditnya ke
// jalur token — bukan menulisnya sendiri setelah aksi berhasil. Penulisan
// sesungguhnya terjadi di auth/postgres bersama tokennya (diuji di httpapi,
// PostgreSQL nyata); di sini yang diperiksa adalah apa yang diteruskan service.
func TestAuditTautanResetDanKirimUlangUndanganLewatJalurToken(t *testing.T) {
	h := harnessAudit(t)
	h.repo.PutBranch(users.BranchUser{BranchID: "br-a", UserID: "usr-a", Nama: "Grahita A", Email: "a@example.test", IsActive: true, BranchActive: true})
	h.repo.PutAdmin(users.AdminUser{ID: "adm-undang", Nama: "Dewi", Email: "dewi@example.test", Role: auth.RoleManager, IsActive: true})
	aktor := adminActor(auth.RoleSuperadmin)

	if err := h.svc.ResendBranchReset(context.Background(), aktor, "usr-a", time.Hour); err != nil {
		t.Fatalf("kirim tautan reset: %v", err)
	}
	if _, err := h.svc.ResendInviteAdmin(context.Background(), aktor, "adm-undang", 24*time.Hour); err != nil {
		t.Fatalf("kirim ulang undangan: %v", err)
	}

	if len(h.resetter.sent) != 1 || len(h.resetter.issued) != 1 {
		t.Fatalf("kedua aksi harus lewat jalur token: sent=%d issued=%d", len(h.resetter.sent), len(h.resetter.issued))
	}
	reset := h.resetter.sent[0].audit
	undang := h.resetter.issued[0].audit
	if reset == nil || undang == nil {
		t.Fatalf("fakta audit tidak diteruskan ke jalur token: reset=%+v undang=%+v", reset, undang)
	}
	if reset.Entity != audit.EntityBranchUser || reset.Action != audit.ActionReset || reset.EntityID != "usr-a" {
		t.Errorf("entri tautan reset salah: %+v", reset)
	}
	if undang.Entity != audit.EntityAdminUser || undang.Action != audit.ActionResend || undang.EntityID != "adm-undang" {
		t.Errorf("entri kirim ulang undangan salah: %+v", undang)
	}
	if reset.ActorKind != auth.KindAdmin || reset.ActorID != aktor.UserID || reset.ActorNama != aktor.Nama || undang.ActorID != aktor.UserID {
		t.Errorf("aktor tidak ikut diteruskan: reset=%+v undang=%+v", reset, undang)
	}
	// Tautan/token TIDAK pernah masuk snapshot — hanya id entitasnya.
	for _, e := range []*auth.ResetAudit{reset, undang} {
		body := e.Before
		if body == nil {
			body = e.After
		}
		if strings.Contains(strings.ToLower(snapshotTeks(body)), "token") {
			t.Errorf("snapshot audit memuat nilai token: %v", body)
		}
		emailDiSnapshot(t, "aksi akun", body)
	}
	// Tidak ada entri yang ditulis di luar jalur token (tidak ada jalur ganda).
	if got := len(h.repo.AuditEntries()); got != 0 {
		t.Errorf("jalur token tidak boleh menulis lewat repository pengguna: %d entri", got)
	}

	// Undangan admin BARU tidak menitipkan audit ke jalur token: entri
	// `create` sudah ditulis CreateAdmin di transaksi yang sama.
	if _, err := h.svc.InviteAdmin(context.Background(), aktor, users.NewAdmin{
		Nama: "Bagus", Email: "bagus@example.test", Role: auth.RoleManager,
	}, 24*time.Hour); err != nil {
		t.Fatalf("undang admin baru: %v", err)
	}
	if got := len(h.repo.AuditEntries()); got != 1 {
		t.Errorf("undangan baru harus menulis tepat satu entri create, dapat %d", got)
	}
	if len(h.resetter.issued) != 2 || h.resetter.issued[1].audit != nil {
		t.Errorf("undangan baru tidak boleh menitipkan audit ke jalur token: %+v", h.resetter.issued[1].audit)
	}
}

// snapshotTeks mengubah snapshot menjadi teks untuk pemeriksaan sederhana.
func snapshotTeks(v any) string {
	switch t := v.(type) {
	case map[string]any:
		bagian := make([]string, 0, len(t))
		for k, val := range t {
			bagian = append(bagian, k+"="+snapshotTeks(val))
		}
		return strings.Join(bagian, " ")
	case []any:
		bagian := make([]string, 0, len(t))
		for _, val := range t {
			bagian = append(bagian, snapshotTeks(val))
		}
		return strings.Join(bagian, " ")
	default:
		return ""
	}
}
