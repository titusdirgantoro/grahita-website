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

// spyRecorder menangkap entri audit yang ditulis lewat audit.Recorder (mutasi
// yang transaksinya milik domain lain: tautan atur ulang & kirim ulang
// undangan, F2-28).
type spyRecorder struct {
	recs []audit.Entry
}

func (s *spyRecorder) Record(_ context.Context, e audit.Entry) error {
	s.recs = append(s.recs, e)
	return nil
}

// harnessAudit menyiapkan service dengan penyimpanan in-memory (yang mencatat
// entri audit transaksionalnya) dan Recorder palsu.
func harnessAudit(t *testing.T) (*harness, *spyRecorder) {
	t.Helper()
	repo := users.NewMemory()
	rec := &spyRecorder{}
	svc := users.NewService(repo, &fakeResetter{}, &fakeSessions{},
		users.WithEmailQueue(&fakeQueue{}),
		users.WithResetURLBuilder(func(auth.Kind, string) string { return "https://admin.example.test/lupa" }),
		users.WithAuditRecorder(rec),
	)
	return &harness{svc: svc, repo: repo, resetter: &fakeResetter{}, sessions: &fakeSessions{}}, rec
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
	h, _ := harnessAudit(t)
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
	h, _ := harnessAudit(t)
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
	h, _ := harnessAudit(t)
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
	h, _ := harnessAudit(t)
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

func TestAuditTautanResetDanKirimUlangUndanganLewatRecorder(t *testing.T) {
	h, rec := harnessAudit(t)
	aktif := time.Now().UTC()
	h.repo.PutBranch(users.BranchUser{BranchID: "br-a", UserID: "usr-a", Nama: "Grahita A", Email: "a@example.test", IsActive: true, BranchActive: true})
	h.repo.PutAdmin(users.AdminUser{ID: "adm-undang", Nama: "Dewi", Email: "dewi@example.test", Role: auth.RoleManager, IsActive: true})
	aktor := adminActor(auth.RoleSuperadmin)

	if err := h.svc.ResendBranchReset(context.Background(), aktor, "usr-a", time.Hour); err != nil {
		t.Fatalf("kirim tautan reset: %v", err)
	}
	if _, err := h.svc.ResendInviteAdmin(context.Background(), aktor, "adm-undang", 24*time.Hour); err != nil {
		t.Fatalf("kirim ulang undangan: %v", err)
	}

	if len(rec.recs) != 2 {
		t.Fatalf("dua aksi harus menghasilkan dua entri, dapat %d: %+v", len(rec.recs), rec.recs)
	}
	reset, undang := rec.recs[0], rec.recs[1]
	if reset.Entity != audit.EntityBranchUser || reset.Action != audit.ActionReset || reset.EntityID != "usr-a" {
		t.Errorf("entri tautan reset salah: %+v", reset)
	}
	if undang.Entity != audit.EntityAdminUser || undang.Action != audit.ActionResend || undang.EntityID != "adm-undang" {
		t.Errorf("entri kirim ulang undangan salah: %+v", undang)
	}
	// Tautan/token TIDAK pernah masuk snapshot — hanya id entitasnya.
	for _, e := range rec.recs {
		body := e.Before
		if body == nil {
			body = e.After
		}
		if strings.Contains(strings.ToLower(snapshotTeks(body)), "token") {
			t.Errorf("snapshot audit memuat nilai token: %v", body)
		}
		emailDiSnapshot(t, "aksi akun", body)
	}
	_ = aktif
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
