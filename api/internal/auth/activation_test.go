package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/platform/clock"
)

// fixtureAktivasi menyiapkan service dengan satu admin undangan dan satu admin
// nonaktif, di atas penyimpanan in-memory.
type fixtureAktivasi struct {
	store *Memory
	svc   *Service
	mail  *fakeResetMailer
	clk   setterClock
}

func newFixtureAktivasi(t *testing.T) *fixtureAktivasi {
	t.Helper()
	store := NewMemory()
	hash, err := HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}
	// Admin undangan: is_active true, kata sandi & aktivasi belum ada.
	store.PutAdmin(Credentials{
		UserID: "adm-undang", Kind: KindAdmin, Role: RoleManager, Nama: "Dewi",
		Email: "dewi@example.test", PasswordHash: "", Active: false, IsActive: true,
	})
	// Admin aktif biasa (pembanding).
	store.PutAdmin(Credentials{
		UserID: "adm-sa", Kind: KindAdmin, Role: RoleSuperadmin, Nama: "Rina",
		Email: "rina@example.test", PasswordHash: hash, Active: true, IsActive: true,
	})
	// Admin yang dinonaktifkan: is_active false, kata sandi sudah ada.
	store.PutAdmin(Credentials{
		UserID: "adm-mati", Kind: KindAdmin, Role: RoleManager, Nama: "Mantan",
		Email: "mantan@example.test", PasswordHash: hash, Active: false, IsActive: false,
	})
	// Akun cabang aktif.
	store.PutBranch(Credentials{
		UserID: "u-1", Kind: KindBranch, Role: RoleBranch, BranchID: "br-1",
		Nama: "Grahita Bandung", Email: "bandung@example.test", PasswordHash: hash,
		Active: true, IsActive: true,
	})
	// Akun cabang yang cabangnya nonaktif.
	store.PutBranch(Credentials{
		UserID: "u-2", Kind: KindBranch, Role: RoleBranch, BranchID: "br-2",
		Nama: "Grahita Bekasi", Email: "bekasi@example.test", PasswordHash: hash,
		Active: false, IsActive: true,
	})

	clk := clock.Fixed(waktuUji)
	svc := NewService(store, store, store, clk, 8*time.Hour, 30*24*time.Hour)
	mail := &fakeResetMailer{}
	svc.SetResetMailer(mail)
	return &fixtureAktivasi{store: store, svc: svc, mail: mail, clk: clk}
}

func TestAktivasiAdminUndanganMengisiPasswordDanAktivasi(t *testing.T) {
	f := newFixtureAktivasi(t)
	ctx := context.Background()

	// Membuat tautan aktivasi TIDAK mengirim email apa pun.
	issued, err := f.svc.IssuePasswordResetFor(ctx, KindAdmin, "adm-undang", 14*24*time.Hour, nil)
	if err != nil {
		t.Fatalf("buat tautan aktivasi: %v", err)
	}
	if issued.Token == "" {
		t.Fatal("token aktivasi tidak boleh kosong")
	}
	if len(f.mail.sent) != 0 {
		t.Errorf("IssuePasswordResetFor tidak boleh mengirim email, dapat %d", len(f.mail.sent))
	}

	// Admin undangan belum bisa login.
	if _, err := f.svc.Login(ctx, "dewi@example.test", "rahasia123", RequestMeta{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("admin undangan harus ditolak sebagai kredensial salah, dapat %v", err)
	}

	// Aktivasi lewat tautan.
	if err := f.svc.ResetPassword(ctx, issued.Token, "sandibaru123", RequestMeta{}); err != nil {
		t.Fatalf("aktivasi admin: %v", err)
	}
	creds, err := f.store.AdminByID(ctx, "adm-undang")
	if err != nil {
		t.Fatalf("baca admin: %v", err)
	}
	if creds.PasswordHash == "" {
		t.Error("aktivasi WAJIB mengisi password_hash")
	}
	if !creds.Active {
		t.Error("aktivasi WAJIB membuat akun boleh login (activated_at terisi)")
	}
	if !VerifyPassword(creds.PasswordHash, "sandibaru123") {
		t.Error("kata sandi aktivasi tidak tersimpan benar")
	}

	// Sesudah aktivasi, admin bisa login dan menerima email dokumen.
	if _, err := f.svc.Login(ctx, "dewi@example.test", "sandibaru123", RequestMeta{}); err != nil {
		t.Fatalf("admin teraktivasi harus bisa login: %v", err)
	}
}

func TestTautanAktivasiSekaliPakaiDanKedaluwarsa(t *testing.T) {
	f := newFixtureAktivasi(t)
	ctx := context.Background()

	issued, err := f.svc.IssuePasswordResetFor(ctx, KindAdmin, "adm-undang", 14*24*time.Hour, nil)
	if err != nil {
		t.Fatalf("buat tautan aktivasi: %v", err)
	}
	if err := f.svc.ResetPassword(ctx, issued.Token, "sandibaru123", RequestMeta{}); err != nil {
		t.Fatalf("aktivasi: %v", err)
	}
	if err := f.svc.ResetPassword(ctx, issued.Token, "sandilain123", RequestMeta{}); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("tautan terpakai harus ditolak, dapat %v", err)
	}

	// Tautan baru untuk admin yang sama, lalu dimajukan melewati masa berlaku.
	lagi, err := f.svc.IssuePasswordResetFor(ctx, KindAdmin, "adm-undang", time.Hour, nil)
	if err != nil {
		t.Fatalf("buat tautan kedua: %v", err)
	}
	f.clk.Set(waktuUji.Add(2 * time.Hour))
	if err := f.svc.ResetPassword(ctx, lagi.Token, "sandilain123", RequestMeta{}); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("tautan kedaluwarsa harus ditolak, dapat %v", err)
	}
}

func TestAdminNonaktifTidakBolehMemegangTautan(t *testing.T) {
	f := newFixtureAktivasi(t)
	ctx := context.Background()

	if _, err := f.svc.IssuePasswordResetFor(ctx, KindAdmin, "adm-mati", time.Hour, nil); !errors.Is(err, ErrInactiveAccount) {
		t.Errorf("admin nonaktif tidak boleh memegang tautan, dapat %v", err)
	}
	if err := f.svc.SendPasswordResetFor(ctx, KindAdmin, "adm-mati", time.Hour, nil); !errors.Is(err, ErrInactiveAccount) {
		t.Errorf("kirim tautan ke admin nonaktif harus ditolak, dapat %v", err)
	}
	if len(f.mail.sent) != 0 {
		t.Errorf("tidak boleh ada email untuk admin nonaktif, dapat %d", len(f.mail.sent))
	}
}

func TestKirimTautanResetCabangHanyaUntukAkunYangBolehLogin(t *testing.T) {
	f := newFixtureAktivasi(t)
	ctx := context.Background()

	if err := f.svc.SendPasswordResetFor(ctx, KindBranch, "u-1", 45*time.Minute, nil); err != nil {
		t.Fatalf("kirim tautan reset cabang: %v", err)
	}
	if len(f.mail.sent) != 1 {
		t.Fatalf("harus ada 1 email reset, dapat %d", len(f.mail.sent))
	}
	pesan := f.mail.sent[0]
	if pesan.To != "bandung@example.test" || pesan.Kind != KindBranch || pesan.TTL != 45*time.Minute {
		t.Errorf("email reset tidak sesuai: %+v", pesan)
	}
	if err := f.svc.ResetPassword(ctx, pesan.Token, "sandibaru123", RequestMeta{}); err != nil {
		t.Fatalf("pakai tautan reset: %v", err)
	}
	if _, err := f.svc.Login(ctx, "bandung@example.test", "sandibaru123", RequestMeta{}); err != nil {
		t.Errorf("kata sandi baru harus berlaku: %v", err)
	}

	// Cabang nonaktif: akunnya tidak boleh memegang tautan.
	if err := f.svc.SendPasswordResetFor(ctx, KindBranch, "u-2", time.Hour, nil); !errors.Is(err, ErrInactiveAccount) {
		t.Errorf("akun cabang nonaktif harus ditolak, dapat %v", err)
	}
}
