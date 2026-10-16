package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/platform/clock"
)

type gantiKredensialSebelumCreate struct {
	SessionStore
	store   *Memory
	current Credentials
}

func (g gantiKredensialSebelumCreate) Create(ctx context.Context, s Session, hash []byte, meta RequestMeta) (Session, error) {
	g.store.PutBranch(g.current)
	return g.SessionStore.Create(ctx, s, hash, meta)
}

type gantiKredensialSebelumChange struct {
	UserStore
	store   *Memory
	current Credentials
}

func (g gantiKredensialSebelumChange) ChangePassword(ctx context.Context, kind Kind, id, sessionID, expected, next string) error {
	g.store.PutBranch(g.current)
	_ = g.store.RevokeForUser(ctx, id)
	return g.UserStore.ChangePassword(ctx, kind, id, sessionID, expected, next)
}

func TestMatriksKapabilitas(t *testing.T) {
	cabang := Actor{UserID: "b1", Kind: KindBranch, Role: RoleBranch, BranchID: "br1"}
	manager := Actor{UserID: "a1", Kind: KindAdmin, Role: RoleManager}
	superadmin := Actor{UserID: "a2", Kind: KindAdmin, Role: RoleSuperadmin}
	anonim := Anonymous()

	kasus := []struct {
		nama  string
		actor Actor
		cap   Capability
		mau   bool
	}{
		{"cabang lihat katalog sendiri", cabang, CapCatalogSelf, true},
		{"cabang buat pesanan", cabang, CapOrderWrite, true},
		{"cabang tidak boleh dashboard admin", cabang, CapAdminDashboard, false},
		{"cabang tidak boleh pengaturan", cabang, CapAdminSettings, false},
		{"manager boleh dashboard", manager, CapAdminDashboard, true},
		{"manager boleh verifikasi", manager, CapAdminVerify, true},
		{"manager TIDAK boleh nilai rupiah", manager, CapAdminDashboardValue, false},
		{"manager TIDAK boleh rekening bank", manager, CapAdminBank, false},
		{"manager TIDAK boleh reporting", manager, CapAdminReports, false},
		{"manager TIDAK boleh pengaturan", manager, CapAdminSettings, false},
		{"manager TIDAK boleh user admin", manager, CapAdminUsersAdmin, false},
		{"manager TIDAK boleh konten tutorial", manager, CapAdminTutorial, false},
		{"cabang TIDAK boleh konten tutorial", cabang, CapAdminTutorial, false},
		{"superadmin punya semua", superadmin, CapAdminSettings, true},
		{"superadmin konten tutorial", superadmin, CapAdminTutorial, true},
		{"superadmin rekening bank", superadmin, CapAdminBank, true},
		{"superadmin reporting", superadmin, CapAdminReports, true},
		{"superadmin user admin", superadmin, CapAdminUsersAdmin, true},
		{"anonim tidak punya apa pun", anonim, CapCatalogSelf, false},
	}
	for _, k := range kasus {
		if got := k.actor.Can(k.cap); got != k.mau {
			t.Errorf("%s: dapat %v, mau %v", k.nama, got, k.mau)
		}
	}
}

func TestKataSandi(t *testing.T) {
	hash, err := HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !VerifyPassword(hash, "rahasia123") {
		t.Error("kata sandi benar harus cocok")
	}
	if VerifyPassword(hash, "salah1234") {
		t.Error("kata sandi salah tidak boleh cocok")
	}
	if VerifyPassword("", "rahasia123") {
		t.Error("hash kosong tidak boleh cocok")
	}
	if VerifyPassword("bukan-hash-argon2", "rahasia123") {
		t.Error("hash rusak tidak boleh cocok")
	}

	lemah := []string{"pendek1", "tanpaangka", "12345678", " spasi123"}
	for _, p := range lemah {
		if _, err := HashPassword(p); err == nil {
			t.Errorf("kata sandi %q seharusnya ditolak", p)
		}
	}
}

func TestLoginDanSesi(t *testing.T) {
	store := NewMemory()
	hash, err := HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	store.PutBranch(Credentials{
		UserID: "u-cabang", Kind: KindBranch, Role: RoleBranch, BranchID: "br-1",
		Nama: "Grahita Bandung", Email: "bandung@example.test", PasswordHash: hash, Active: true,
	})

	clk := clock.Fixed(time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC))
	svc := NewService(store, store, store, clk, 8*time.Hour, 30*24*time.Hour)

	issued, err := svc.Login(context.Background(), "BANDUNG@example.test", "rahasia123", RequestMeta{IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if issued.Token == "" {
		t.Fatal("token sesi tidak boleh kosong")
	}
	if issued.Actor.BranchID != "br-1" || issued.Actor.Role != RoleBranch {
		t.Errorf("aktor cabang tidak lengkap: %+v", issued.Actor)
	}

	actor, err := svc.Authenticate(context.Background(), issued.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if actor.UserID != "u-cabang" {
		t.Errorf("aktor tidak sesuai: %+v", actor)
	}

	if _, err := svc.Authenticate(context.Background(), "token-palsu"); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("token palsu harus ErrSessionInvalid, dapat %v", err)
	}

	if err := svc.Logout(context.Background(), issued.Token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := svc.Authenticate(context.Background(), issued.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("sesi setelah logout harus tidak valid, dapat %v", err)
	}
	if store.SessionCount() != 0 {
		t.Errorf("tidak boleh ada sesi aktif, dapat %d", store.SessionCount())
	}
}

func TestKredensialSalahTidakMembocorkanAkun(t *testing.T) {
	store := NewMemory()
	hash, _ := HashPassword("rahasia123")
	store.PutAdmin(Credentials{
		UserID: "a-1", Kind: KindAdmin, Role: RoleManager,
		Nama: "Budi", Email: "budi@example.test", PasswordHash: hash, Active: true,
	})
	svc := NewService(store, store, store, clock.System(), time.Hour, time.Hour)

	if _, err := svc.Login(context.Background(), "budi@example.test", "salah1234", RequestMeta{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("password salah harus ErrInvalidCredentials, dapat %v", err)
	}
	if _, err := svc.Login(context.Background(), "tidak-ada@example.test", "rahasia123", RequestMeta{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("email tak dikenal harus ErrInvalidCredentials (pesan sama), dapat %v", err)
	}
}

func TestLoginMenolakHashYangSudahDigantiSetelahVerifikasi(t *testing.T) {
	store := NewMemory()
	hashLama, _ := HashPassword("rahasia123")
	hashBaru, _ := HashPassword("sandibaru123")
	creds := Credentials{
		UserID: "u-1", Kind: KindBranch, Role: RoleBranch, BranchID: "br-1",
		Nama: "Cabang", Email: "cabang@example.test", PasswordHash: hashLama, Active: true,
	}
	store.PutBranch(creds)
	creds.PasswordHash = hashBaru
	sessions := gantiKredensialSebelumCreate{SessionStore: store, store: store, current: creds}
	svc := NewService(store, sessions, store, clock.System(), time.Hour, time.Hour)

	if _, err := svc.Login(context.Background(), "cabang@example.test", "rahasia123", RequestMeta{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("password lama yang sudah diverifikasi sebelum pergantian harus ditolak, dapat %v", err)
	}
	if store.SessionCount() != 0 {
		t.Fatal("hash kredensial usang tidak boleh menghasilkan sesi")
	}
}

func TestAkunNonaktifDitolak(t *testing.T) {
	store := NewMemory()
	hash, _ := HashPassword("rahasia123")
	store.PutAdmin(Credentials{
		UserID: "a-1", Kind: KindAdmin, Role: RoleManager,
		Nama: "Budi", Email: "budi@example.test", PasswordHash: hash, Active: false,
	})
	svc := NewService(store, store, store, clock.System(), time.Hour, time.Hour)

	if _, err := svc.Login(context.Background(), "budi@example.test", "rahasia123", RequestMeta{}); !errors.Is(err, ErrInactiveAccount) {
		t.Errorf("akun nonaktif harus ErrInactiveAccount, dapat %v", err)
	}
}

func TestSesiKedaluwarsa(t *testing.T) {
	now := time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)
	s := Session{ExpiresAt: now.Add(time.Hour), LastSeenAt: now}

	if s.Expired(now.Add(30*time.Minute), 8*time.Hour) {
		t.Error("sesi baru tidak boleh dianggap kedaluwarsa")
	}
	if !s.Expired(now.Add(2*time.Hour), 8*time.Hour) {
		t.Error("sesi melewati batas absolut harus kedaluwarsa")
	}
	if !s.Expired(now.Add(9*time.Hour), 8*time.Hour) {
		t.Error("sesi melewati batas diam harus kedaluwarsa")
	}
}

func TestSesiDicabutSetelahGantiSandi(t *testing.T) {
	store := NewMemory()
	hash, _ := HashPassword("rahasia123")
	store.PutBranch(Credentials{
		UserID: "u-1", Kind: KindBranch, Role: RoleBranch, BranchID: "br-1",
		Nama: "Cabang", Email: "cabang@example.test", PasswordHash: hash, Active: true,
	})
	svc := NewService(store, store, store, clock.System(), time.Hour, time.Hour)

	issued, err := svc.Login(context.Background(), "cabang@example.test", "rahasia123", RequestMeta{})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := svc.RevokeAllForUser(context.Background(), "u-1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.Authenticate(context.Background(), issued.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("sesi harus dicabut setelah ganti sandi, dapat %v", err)
	}
}

func TestGantiSandiMenolakRequestDariGenerasiSebelumPemulihan(t *testing.T) {
	store := NewMemory()
	hashLama, _ := HashPassword("rahasia123")
	hashPemulihan, _ := HashPassword("credentialbaru123")
	creds := Credentials{
		UserID: "u-1", Kind: KindBranch, Role: RoleBranch, BranchID: "br-1",
		Nama: "Cabang", Email: "cabang@example.test", PasswordHash: hashLama, Active: true,
	}
	store.PutBranch(creds)
	svcLogin := NewService(store, store, store, clock.System(), time.Hour, time.Hour)
	issued, err := svcLogin.Login(context.Background(), creds.Email, "rahasia123", RequestMeta{})
	if err != nil {
		t.Fatalf("login fixture: %v", err)
	}
	creds.PasswordHash = hashPemulihan
	guarded := gantiKredensialSebelumChange{UserStore: store, store: store, current: creds}
	svc := NewService(guarded, store, store, clock.System(), time.Hour, time.Hour)
	if err := svc.ChangePassword(context.Background(), issued.Actor, "rahasia123", "pilihanlama123", RequestMeta{}); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("request sesi pra-revokasi harus ditolak, dapat %v", err)
	}
	akhir, err := store.BranchByID(context.Background(), creds.UserID)
	if err != nil {
		t.Fatalf("baca credential akhir: %v", err)
	}
	if !VerifyPassword(akhir.PasswordHash, "credentialbaru123") || VerifyPassword(akhir.PasswordHash, "pilihanlama123") {
		t.Fatal("request lama tidak boleh menimpa credential pemulihan")
	}
}
