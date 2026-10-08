package users_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/users"
)

// fakeResetter mencatat permintaan tautan atur ulang / aktivasi.
type fakeResetter struct {
	mu     sync.Mutex
	sent   []kirimanReset
	issued []kirimanReset
	seq    int
}

type kirimanReset struct {
	kind   auth.Kind
	userID string
	ttl    time.Duration
}

func (f *fakeResetter) SendPasswordResetFor(_ context.Context, kind auth.Kind, userID string, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, kirimanReset{kind: kind, userID: userID, ttl: ttl})
	return nil
}

func (f *fakeResetter) IssuePasswordResetFor(_ context.Context, kind auth.Kind, userID string, ttl time.Duration) (auth.IssuedReset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.issued = append(f.issued, kirimanReset{kind: kind, userID: userID, ttl: ttl})
	return auth.IssuedReset{
		Token:     fmt.Sprintf("token-aktivasi-%d", f.seq),
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

// fakeSessions mencatat pencabutan sesi.
type fakeSessions struct {
	mu      sync.Mutex
	revoked []string
}

func (f *fakeSessions) RevokeAllForUser(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, userID)
	return nil
}

// fakeQueue menangkap email yang diantre (email_outbox belum ada di kartu ini).
type fakeQueue struct {
	mu     sync.Mutex
	queued []users.QueuedEmail
}

func (f *fakeQueue) Enqueue(_ context.Context, m users.QueuedEmail) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued = append(f.queued, m)
	return nil
}

type harness struct {
	svc      *users.Service
	repo     *users.Memory
	resetter *fakeResetter
	sessions *fakeSessions
	queue    *fakeQueue
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	repo := users.NewMemory()
	resetter := &fakeResetter{}
	sessions := &fakeSessions{}
	queue := &fakeQueue{}
	svc := users.NewService(repo, resetter, sessions,
		users.WithEmailQueue(queue),
		users.WithResetURLBuilder(func(kind auth.Kind, token string) string {
			return "https://admin.example.test/lupa?token=" + token
		}),
	)
	return &harness{svc: svc, repo: repo, resetter: resetter, sessions: sessions, queue: queue}
}

func adminActor(role auth.Role) auth.Actor {
	return auth.Actor{UserID: "adm-aktor", Kind: auth.KindAdmin, Role: role, Nama: "Aktor", Email: "aktor@example.test"}
}

func branchActor() auth.Actor {
	return auth.Actor{UserID: "usr-a", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "br-a"}
}

func TestBranchStatusMengikutiAturanBolehLogin(t *testing.T) {
	kasus := []struct {
		nama                                 string
		hasAccount, userActive, branchActive bool
		mau                                  string
	}{
		{"belum registrasi", false, false, true, users.StatusBelumRegistrasi},
		{"akun dan cabang aktif", true, true, true, users.StatusAktif},
		{"akun nonaktif", true, false, true, users.StatusNonaktif},
		{"cabang nonaktif", true, true, false, users.StatusNonaktif},
	}
	for _, k := range kasus {
		if got := users.BranchStatus(k.hasAccount, k.userActive, k.branchActive); got != k.mau {
			t.Errorf("%s: status %q, mau %q", k.nama, got, k.mau)
		}
	}
}

func TestDaftarAkunCabangStatusDanLoginTerakhir(t *testing.T) {
	h := newHarness(t)
	login := time.Date(2026, 10, 7, 3, 4, 5, 0, time.UTC)
	h.repo.PutBranch(users.BranchUser{
		BranchID: "br-a", UserID: "usr-a", Nama: "Grahita A", NamaKepala: "Kepala A",
		Email: "a@example.test", IsActive: true, BranchActive: true, LastLoginAt: &login,
	})
	h.repo.PutBranch(users.BranchUser{
		BranchID: "br-b", Nama: "Grahita B", NamaKepala: "Kepala B",
		Email: "b@example.test", BranchActive: true, TokenPrefix: "GRH-7K4Q",
	})
	h.repo.PutBranch(users.BranchUser{
		BranchID: "br-c", UserID: "usr-c", Nama: "Grahita C", NamaKepala: "Kepala C",
		Email: "c@example.test", IsActive: true, BranchActive: false,
	})

	rows, err := h.svc.BranchUsers(context.Background(), adminActor(auth.RoleManager))
	if err != nil {
		t.Fatalf("daftar akun cabang: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("mau 3 baris, dapat %d", len(rows))
	}
	perID := map[string]users.BranchUser{}
	for _, r := range rows {
		perID[r.BranchID] = r
	}
	if perID["br-a"].Status != users.StatusAktif || perID["br-a"].LastLoginAt == nil {
		t.Errorf("br-a harus aktif dengan login terakhir, dapat %+v", perID["br-a"])
	}
	if perID["br-b"].Status != users.StatusBelumRegistrasi || perID["br-b"].HasAccount {
		t.Errorf("br-b harus belum registrasi, dapat %+v", perID["br-b"])
	}
	if perID["br-b"].TokenPrefix != "GRH-7K4Q" {
		t.Errorf("token prefix belum registrasi harus tampil, dapat %q", perID["br-b"].TokenPrefix)
	}
	if perID["br-c"].Status != users.StatusNonaktif {
		t.Errorf("br-c (cabang nonaktif) harus nonaktif, dapat %+v", perID["br-c"])
	}
}

func TestKapabilitasUserManagementDitegakkanService(t *testing.T) {
	h := newHarness(t)

	if _, err := h.svc.Admins(context.Background(), adminActor(auth.RoleManager)); !errors.Is(err, users.ErrForbidden) {
		t.Errorf("manager tidak boleh melihat akun admin, dapat %v", err)
	}
	if _, err := h.svc.Admins(context.Background(), branchActor()); !errors.Is(err, users.ErrForbidden) {
		t.Errorf("akun cabang tidak boleh melihat akun admin, dapat %v", err)
	}
	_, err := h.svc.InviteAdmin(context.Background(), adminActor(auth.RoleManager),
		users.NewAdmin{Nama: "X", Email: "x@example.test", Role: auth.RoleManager}, time.Hour)
	if !errors.Is(err, users.ErrForbidden) {
		t.Errorf("manager tidak boleh mengundang admin, dapat %v", err)
	}
	if _, err := h.svc.UpdateAdmin(context.Background(), branchActor(), "adm-1", users.AdminPatch{}); !errors.Is(err, users.ErrForbidden) {
		t.Errorf("akun cabang tidak boleh mengubah admin, dapat %v", err)
	}
	// Manager boleh melihat akun cabang.
	if _, err := h.svc.BranchUsers(context.Background(), adminActor(auth.RoleManager)); err != nil {
		t.Errorf("manager harus boleh melihat akun cabang: %v", err)
	}
}

func TestNonaktifkanAkunCabangMencabutSesi(t *testing.T) {
	h := newHarness(t)
	h.repo.PutBranch(users.BranchUser{BranchID: "br-a", UserID: "usr-a", Nama: "Grahita A", Email: "a@example.test", IsActive: true, BranchActive: true})

	u, err := h.svc.SetBranchUserActive(context.Background(), adminActor(auth.RoleManager), "usr-a", false)
	if err != nil {
		t.Fatalf("nonaktifkan akun: %v", err)
	}
	if u.IsActive || u.Status != users.StatusNonaktif {
		t.Errorf("akun harus nonaktif, dapat %+v", u)
	}
	if len(h.sessions.revoked) != 1 || h.sessions.revoked[0] != "usr-a" {
		t.Errorf("sesi akun yang dinonaktifkan harus dicabut, dapat %v", h.sessions.revoked)
	}

	// Mengaktifkan kembali tidak mencabut sesi (tidak ada sesi untuk dicabut).
	if _, err := h.svc.SetBranchUserActive(context.Background(), adminActor(auth.RoleManager), "usr-a", true); err != nil {
		t.Fatalf("aktifkan akun: %v", err)
	}
	if len(h.sessions.revoked) != 1 {
		t.Errorf("mengaktifkan akun tidak boleh mencabut sesi, dapat %v", h.sessions.revoked)
	}

	if _, err := h.svc.SetBranchUserActive(context.Background(), adminActor(auth.RoleManager), "tidak-ada", false); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("akun tak dikenal harus ErrNotFound, dapat %v", err)
	}
}

func TestKirimUlangResetCabang(t *testing.T) {
	h := newHarness(t)
	h.repo.PutBranch(users.BranchUser{BranchID: "br-a", UserID: "usr-a", Nama: "Grahita A", Email: "a@example.test", IsActive: true, BranchActive: true})
	h.repo.PutBranch(users.BranchUser{BranchID: "br-b", Nama: "Grahita B", Email: "b@example.test", BranchActive: true})

	if err := h.svc.ResendBranchReset(context.Background(), adminActor(auth.RoleManager), "usr-a", 45*time.Minute); err != nil {
		t.Fatalf("kirim ulang reset: %v", err)
	}
	if len(h.resetter.sent) != 1 {
		t.Fatalf("harus ada 1 permintaan tautan, dapat %d", len(h.resetter.sent))
	}
	k := h.resetter.sent[0]
	if k.kind != auth.KindBranch || k.userID != "usr-a" || k.ttl != 45*time.Minute {
		t.Errorf("permintaan tautan tidak sesuai: %+v", k)
	}

	// Cabang belum registrasi tidak punya akun untuk direset.
	if err := h.svc.ResendBranchReset(context.Background(), adminActor(auth.RoleManager), "tidak-ada", time.Minute); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("id tak dikenal harus ErrNotFound, dapat %v", err)
	}
}

func TestUndangAdminMengantreEmailTautanAktivasi(t *testing.T) {
	h := newHarness(t)
	ttl := 14 * 24 * time.Hour

	admin, err := h.svc.InviteAdmin(context.Background(), adminActor(auth.RoleSuperadmin), users.NewAdmin{
		Nama: "Dewi Lestari", Email: "Dewi@Example.test", Role: auth.RoleManager,
		NotifInvoice: true,
	}, ttl)
	if err != nil {
		t.Fatalf("undang admin: %v", err)
	}
	if admin.Status != users.StatusUndangan {
		t.Errorf("admin baru harus berstatus undangan, dapat %q", admin.Status)
	}
	if !admin.IsActive {
		t.Error("admin undangan tetap is_active=true; yang menahan login adalah aktivasi")
	}
	if admin.Email != "dewi@example.test" {
		t.Errorf("email harus dinormalkan, dapat %q", admin.Email)
	}
	if len(h.resetter.issued) != 1 || h.resetter.issued[0].kind != auth.KindAdmin || h.resetter.issued[0].userID != admin.ID {
		t.Fatalf("tautan aktivasi harus dibuat untuk admin baru, dapat %+v", h.resetter.issued)
	}
	if len(h.queue.queued) != 1 {
		t.Fatalf("harus ada 1 email diantre, dapat %d", len(h.queue.queued))
	}
	m := h.queue.queued[0]
	if m.Jenis != users.JenisUndanganAdmin {
		t.Errorf("jenis email harus %q, dapat %q", users.JenisUndanganAdmin, m.Jenis)
	}
	if len(m.To) != 1 || m.To[0] != "dewi@example.test" {
		t.Errorf("penerima undangan salah: %v", m.To)
	}
	tautan, _ := m.Payload["tautan"].(string)
	if !strings.HasPrefix(tautan, "https://admin.example.test/lupa?token=") {
		t.Errorf("payload undangan harus memuat tautan aktivasi, dapat %q", tautan)
	}

	// Email yang sudah dipakai ditolak.
	if _, err := h.svc.InviteAdmin(context.Background(), adminActor(auth.RoleSuperadmin), users.NewAdmin{
		Nama: "Lain", Email: "dewi@example.test", Role: auth.RoleManager,
	}, ttl); !errors.Is(err, users.ErrEmailTaken) {
		t.Errorf("email ganda harus ErrEmailTaken, dapat %v", err)
	}
	// Role tak dikenal ditolak di domain.
	if _, err := h.svc.InviteAdmin(context.Background(), adminActor(auth.RoleSuperadmin), users.NewAdmin{
		Nama: "Lain", Email: "lain@example.test", Role: auth.RoleBranch,
	}, ttl); !errors.Is(err, users.ErrInvalidData) {
		t.Errorf("role tak dikenal harus ErrInvalidData, dapat %v", err)
	}
}

func TestAdminFlagSajaMenerimaDokumen(t *testing.T) {
	h := newHarness(t)
	aktif := time.Now().UTC().Add(-time.Hour)
	h.repo.PutAdmin(users.AdminUser{ID: "adm-sa", Nama: "Rina", Email: "rina@example.test", Role: auth.RoleSuperadmin, IsActive: true, ActivatedAt: &aktif, NotifInvoice: true})
	h.repo.PutAdmin(users.AdminUser{ID: "adm-mgr", Nama: "Budi", Email: "budi@example.test", Role: auth.RoleManager, IsActive: true, ActivatedAt: &aktif, NotifSuratJalan: true})
	h.repo.PutAdmin(users.AdminUser{ID: "adm-undang", Nama: "Dewi", Email: "dewi@example.test", Role: auth.RoleManager, IsActive: true, NotifInvoice: true, NotifSuratJalan: true})
	h.repo.PutAdmin(users.AdminUser{ID: "adm-mati", Nama: "Mantan", Email: "mantan@example.test", Role: auth.RoleManager, IsActive: false, ActivatedAt: &aktif, NotifInvoice: true, NotifSuratJalan: true})

	invoice, err := h.svc.DocumentRecipients(context.Background(), users.DocInvoice)
	if err != nil {
		t.Fatalf("penerima invoice: %v", err)
	}
	if len(invoice) != 1 || invoice[0] != "rina@example.test" {
		t.Errorf("hanya admin aktif ber-flag invoice yang berhak, dapat %v", invoice)
	}
	surat, err := h.svc.DocumentRecipients(context.Background(), users.DocSuratJalan)
	if err != nil {
		t.Fatalf("penerima surat jalan: %v", err)
	}
	if len(surat) != 1 || surat[0] != "budi@example.test" {
		t.Errorf("hanya admin aktif ber-flag surat jalan yang berhak, dapat %v", surat)
	}
	if _, err := h.svc.DocumentRecipients(context.Background(), "karangan"); !errors.Is(err, users.ErrInvalidData) {
		t.Errorf("jenis dokumen tak dikenal harus ErrInvalidData, dapat %v", err)
	}
}

func TestSuperadminAktifTerakhirDilindungi(t *testing.T) {
	h := newHarness(t)
	aktif := time.Now().UTC()
	h.repo.PutAdmin(users.AdminUser{ID: "adm-sa", Nama: "Rina", Email: "rina@example.test", Role: auth.RoleSuperadmin, IsActive: true, ActivatedAt: &aktif})
	h.repo.PutAdmin(users.AdminUser{ID: "adm-mgr", Nama: "Budi", Email: "budi@example.test", Role: auth.RoleManager, IsActive: true, ActivatedAt: &aktif})
	demote := auth.RoleManager

	if _, err := h.svc.UpdateAdmin(context.Background(), adminActor(auth.RoleSuperadmin), "adm-sa", users.AdminPatch{Role: &demote}); !errors.Is(err, users.ErrLastSuperadmin) {
		t.Errorf("menurunkan superadmin terakhir harus ditolak, dapat %v", err)
	}
	mati := false
	if _, err := h.svc.UpdateAdmin(context.Background(), adminActor(auth.RoleSuperadmin), "adm-sa", users.AdminPatch{IsActive: &mati}); !errors.Is(err, users.ErrLastSuperadmin) {
		t.Errorf("menonaktifkan superadmin terakhir harus ditolak, dapat %v", err)
	}

	// Flag notifikasi tetap boleh diubah pada superadmin terakhir.
	ya := true
	if _, err := h.svc.UpdateAdmin(context.Background(), adminActor(auth.RoleSuperadmin), "adm-sa", users.AdminPatch{NotifInvoice: &ya}); err != nil {
		t.Errorf("mengubah flag tidak boleh tertahan perlindungan: %v", err)
	}

	// Setelah ada superadmin aktif kedua, penurunan boleh.
	h.repo.PutAdmin(users.AdminUser{ID: "adm-sa2", Nama: "Andi", Email: "andi@example.test", Role: auth.RoleSuperadmin, IsActive: true, ActivatedAt: &aktif})
	if _, err := h.svc.UpdateAdmin(context.Background(), adminActor(auth.RoleSuperadmin), "adm-sa", users.AdminPatch{Role: &demote}); err != nil {
		t.Errorf("penurunan harus boleh saat masih ada superadmin lain: %v", err)
	}
}

func TestSuperadminTerakhirAmanMeskiPermintaanBersamaan(t *testing.T) {
	h := newHarness(t)
	aktif := time.Now().UTC()
	h.repo.PutAdmin(users.AdminUser{ID: "adm-a", Nama: "Rina", Email: "rina@example.test", Role: auth.RoleSuperadmin, IsActive: true, ActivatedAt: &aktif})
	h.repo.PutAdmin(users.AdminUser{ID: "adm-b", Nama: "Andi", Email: "andi@example.test", Role: auth.RoleSuperadmin, IsActive: true, ActivatedAt: &aktif})
	demote := auth.RoleManager

	var wg sync.WaitGroup
	hasil := make([]error, 2)
	for i, id := range []string{"adm-a", "adm-b"} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			_, hasil[i] = h.svc.UpdateAdmin(context.Background(), adminActor(auth.RoleSuperadmin), id, users.AdminPatch{Role: &demote})
		}(i, id)
	}
	wg.Wait()

	sukses, ditolak := 0, 0
	for _, err := range hasil {
		switch {
		case err == nil:
			sukses++
		case errors.Is(err, users.ErrLastSuperadmin):
			ditolak++
		default:
			t.Fatalf("error tak terduga: %v", err)
		}
	}
	if sukses != 1 || ditolak != 1 {
		t.Fatalf("harus tepat satu berhasil dan satu ditolak, dapat sukses=%d ditolak=%d", sukses, ditolak)
	}

	rows, err := h.repo.ListAdmins(context.Background())
	if err != nil {
		t.Fatalf("daftar admin: %v", err)
	}
	sisa := 0
	for _, a := range rows {
		if a.Role == auth.RoleSuperadmin && a.IsActive {
			sisa++
		}
	}
	if sisa != 1 {
		t.Fatalf("harus tersisa tepat 1 superadmin aktif, dapat %d", sisa)
	}
}

// TestSuperadminUndanganTidakMenjadiPelindung memastikan superadmin yang masih
// berstatus UNDANGAN (is_active=true tetapi belum diaktivasi sehingga tidak bisa
// login) TIDAK dihitung sebagai penjaga: menurunkan/menonaktifkan superadmin
// satu-satunya yang benar-benar bisa login harus tetap ditolak, supaya sistem
// tidak pernah ditinggalkan tanpa superadmin yang bisa masuk (temuan QA F1-05).
func TestSuperadminUndanganTidakMenjadiPelindung(t *testing.T) {
	h := newHarness(t)
	aktif := time.Now().UTC()
	h.repo.PutAdmin(users.AdminUser{ID: "adm-sa", Nama: "Rina", Email: "rina@example.test", Role: auth.RoleSuperadmin, IsActive: true, ActivatedAt: &aktif})
	// Superadmin UNDANGAN: is_active=true, activated_at masih kosong.
	h.repo.PutAdmin(users.AdminUser{ID: "adm-undang", Nama: "Bagus", Email: "bagus@example.test", Role: auth.RoleSuperadmin, IsActive: true})

	demote := auth.RoleManager
	if _, err := h.svc.UpdateAdmin(context.Background(), adminActor(auth.RoleSuperadmin), "adm-sa", users.AdminPatch{Role: &demote}); !errors.Is(err, users.ErrLastSuperadmin) {
		t.Errorf("superadmin undangan tidak boleh dihitung sebagai pelindung; mau ErrLastSuperadmin, dapat %v", err)
	}
	mati := false
	if _, err := h.svc.UpdateAdmin(context.Background(), adminActor(auth.RoleSuperadmin), "adm-sa", users.AdminPatch{IsActive: &mati}); !errors.Is(err, users.ErrLastSuperadmin) {
		t.Errorf("menonaktifkan superadmin terakhir yang bisa login harus ditolak, dapat %v", err)
	}

	// Superadmin KEDUA yang sudah diaktivasi (bisa login) barulah pelindung.
	h.repo.PutAdmin(users.AdminUser{ID: "adm-sa2", Nama: "Andi", Email: "andi@example.test", Role: auth.RoleSuperadmin, IsActive: true, ActivatedAt: &aktif})
	if _, err := h.svc.UpdateAdmin(context.Background(), adminActor(auth.RoleSuperadmin), "adm-sa", users.AdminPatch{Role: &demote}); err != nil {
		t.Fatalf("penurunan harus boleh saat ada superadmin teraktivasi lain: %v", err)
	}

	// Sisa superadmin yang bisa login: adm-sa2 (adm-undang masih undangan),
	// jadi adm-sa2 pun masih dilindungi.
	matiAtas := false
	if _, err := h.svc.UpdateAdmin(context.Background(), adminActor(auth.RoleSuperadmin), "adm-sa2", users.AdminPatch{IsActive: &matiAtas}); !errors.Is(err, users.ErrLastSuperadmin) {
		t.Errorf("superadmin undangan tidak boleh menjadi satu-satunya penjaga; mau ErrLastSuperadmin, dapat %v", err)
	}
}

// TestUndangAdminMenolakEmailCabang memastikan email yang sudah dipakai cabang
// (email login akun cabang maupun email cabang yang belum registrasi) ditolak
// saat mengundang admin — kebalikan dari pemeriksaan F1-06.
func TestUndangAdminMenolakEmailCabang(t *testing.T) {
	h := newHarness(t)
	h.repo.PutBranch(users.BranchUser{BranchID: "br-a", UserID: "usr-a", Nama: "Grahita A", Email: "a@example.test", IsActive: true, BranchActive: true})
	h.repo.PutBranch(users.BranchUser{BranchID: "br-b", Nama: "Grahita B", Email: "b@example.test", BranchActive: true})

	for _, email := range []string{"a@example.test", "A@Example.Test", "b@example.test"} {
		_, err := h.svc.InviteAdmin(context.Background(), adminActor(auth.RoleSuperadmin),
			users.NewAdmin{Nama: "Bentrok", Email: email, Role: auth.RoleManager}, time.Hour)
		if !errors.Is(err, users.ErrEmailTaken) {
			t.Errorf("email cabang %q harus ErrEmailTaken, dapat %v", email, err)
		}
	}
	// Tidak ada admin yang terbuat dari percobaan yang ditolak.
	rows, err := h.repo.ListAdmins(context.Background())
	if err != nil {
		t.Fatalf("daftar admin: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("tidak boleh ada admin terbuat dari email cabang, dapat %d baris", len(rows))
	}
	if len(h.resetter.issued) != 0 || len(h.queue.queued) != 0 {
		t.Errorf("penolakan harus terjadi sebelum tautan/email dibuat: issued=%d antre=%d", len(h.resetter.issued), len(h.queue.queued))
	}

	// Email yang benar-benar bebas tetap boleh diundang.
	if _, err := h.svc.InviteAdmin(context.Background(), adminActor(auth.RoleSuperadmin),
		users.NewAdmin{Nama: "Bebas", Email: "bebas@example.test", Role: auth.RoleManager}, time.Hour); err != nil {
		t.Fatalf("email bebas harus boleh diundang: %v", err)
	}
}
