package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/mailer"
)

// setterClock adalah clock yang bisa dimajukan; dipakai untuk menguji masa
// berlaku tautan tanpa menunggu waktu nyata.
type setterClock interface {
	clock.Clock
	Set(time.Time)
}

// fakeResetMailer menangkap pesan atur ulang untuk diperiksa pengujian.
type fakeResetMailer struct {
	sent []ResetMessage
	err  error
}

func (f *fakeResetMailer) SendPasswordReset(_ context.Context, m ResetMessage) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

// fakeMailer memenuhi mailer.Mailer untuk menguji EmailResetMailer.
type fakeMailer struct{ msg mailer.Message }

func (f *fakeMailer) Send(_ context.Context, m mailer.Message) error { f.msg = m; return nil }

func (f *fakeMailer) Health(context.Context) error { return nil }

var waktuUji = time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)

func newResetFixture(t *testing.T) (*Memory, *Service, *fakeResetMailer, setterClock) {
	t.Helper()
	store := NewMemory()
	hash, err := HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}
	store.PutBranch(Credentials{
		UserID: "u-1", Kind: KindBranch, Role: RoleBranch, BranchID: "br-1",
		Nama: "Grahita Bandung", Email: "bandung@example.test", PasswordHash: hash, Active: true,
	})
	clk := clock.Fixed(waktuUji)
	svc := NewService(store, store, store, clk, 8*time.Hour, 30*24*time.Hour)
	mail := &fakeResetMailer{}
	svc.SetResetMailer(mail)
	return store, svc, mail, clk
}

func TestLupaKataSandiNonEnumeratif(t *testing.T) {
	store, svc, mail, _ := newResetFixture(t)
	ctx := context.Background()

	// Akun tidak dikenal: sukses, tanpa token, tanpa email.
	if err := svc.ForgotPassword(ctx, "tidak-ada@example.test", time.Hour, RequestMeta{}); err != nil {
		t.Fatalf("lupa kata sandi akun tak dikenal harus sukses, dapat %v", err)
	}
	if len(mail.sent) != 0 {
		t.Errorf("email tidak boleh dikirim untuk akun tak dikenal, dapat %d", len(mail.sent))
	}
	if n := store.ResetTokenCount(); n != 0 {
		t.Errorf("token tidak boleh dibuat untuk akun tak dikenal, dapat %d", n)
	}

	// Akun ada: sukses, satu email, satu token berlaku.
	if err := svc.ForgotPassword(ctx, "BANDUNG@example.test", time.Hour, RequestMeta{}); err != nil {
		t.Fatalf("lupa kata sandi: %v", err)
	}
	if len(mail.sent) != 1 {
		t.Fatalf("email atur ulang harus dikirim sekali, dapat %d", len(mail.sent))
	}
	if got := mail.sent[0].To; got != "bandung@example.test" {
		t.Errorf("email tujuan salah: %q", got)
	}
	if mail.sent[0].Token == "" {
		t.Error("token pada email tidak boleh kosong")
	}
	if n := store.ResetTokenCount(); n != 1 {
		t.Errorf("harus ada 1 token berlaku, dapat %d", n)
	}

	// Akun nonaktif juga tidak dikirimi email.
	nonaktif := NewMemory()
	hash, _ := HashPassword("rahasia123")
	nonaktif.PutBranch(Credentials{
		UserID: "u-9", Kind: KindBranch, Role: RoleBranch, BranchID: "br-9",
		Email: "nonaktif@example.test", PasswordHash: hash, Active: false,
	})
	svcNonaktif := NewService(nonaktif, nonaktif, nonaktif, clock.Fixed(waktuUji), time.Hour, time.Hour)
	mailNonaktif := &fakeResetMailer{}
	svcNonaktif.SetResetMailer(mailNonaktif)
	if err := svcNonaktif.ForgotPassword(ctx, "nonaktif@example.test", time.Hour, RequestMeta{}); err != nil {
		t.Fatalf("akun nonaktif harus tetap sukses di permukaan, dapat %v", err)
	}
	if len(mailNonaktif.sent) != 0 || nonaktif.ResetTokenCount() != 0 {
		t.Error("akun nonaktif tidak boleh menerima tautan atur ulang")
	}
}

func TestLupaKataSandiGagalKirimTetapSukses(t *testing.T) {
	_, svc, mail, _ := newResetFixture(t)
	mail.err = errors.New("smtp mati")

	// Kegagalan teknis pengiriman tidak boleh membedakan akun ada/tidak.
	if err := svc.ForgotPassword(context.Background(), "bandung@example.test", time.Hour, RequestMeta{}); err != nil {
		t.Fatalf("kegagalan SMTP tidak boleh terlihat oleh pemanggil, dapat %v", err)
	}
}

func TestAturUlangSekaliPakaiDanCabutSemuaSesi(t *testing.T) {
	store, svc, mail, _ := newResetFixture(t)
	ctx := context.Background()

	sesi1, err := svc.Login(ctx, "bandung@example.test", "rahasia123", RequestMeta{})
	if err != nil {
		t.Fatalf("login pertama: %v", err)
	}
	sesi2, err := svc.Login(ctx, "bandung@example.test", "rahasia123", RequestMeta{})
	if err != nil {
		t.Fatalf("login kedua: %v", err)
	}

	if err := svc.ForgotPassword(ctx, "bandung@example.test", time.Hour, RequestMeta{}); err != nil {
		t.Fatalf("lupa kata sandi: %v", err)
	}
	token := mail.sent[0].Token

	if err := svc.ResetPassword(ctx, token, "sandibaru123", RequestMeta{}); err != nil {
		t.Fatalf("atur ulang: %v", err)
	}

	// Sekali pakai.
	if err := svc.ResetPassword(ctx, token, "sandibaru123", RequestMeta{}); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("token yang sudah dipakai harus ErrResetInvalid, dapat %v", err)
	}
	// Seluruh sesi lama dicabut (PRD CP-AUT-07).
	for nama, s := range map[string]IssuedSession{"sesi1": sesi1, "sesi2": sesi2} {
		if _, err := svc.Authenticate(ctx, s.Token); !errors.Is(err, ErrSessionInvalid) {
			t.Errorf("%s harus sudah tidak valid, dapat %v", nama, err)
		}
	}
	if store.SessionCount() != 0 {
		t.Errorf("tidak boleh ada sesi aktif, dapat %d", store.SessionCount())
	}
	// Kata sandi baru berlaku, yang lama tidak.
	if _, err := svc.Login(ctx, "bandung@example.test", "sandibaru123", RequestMeta{}); err != nil {
		t.Errorf("login dengan kata sandi baru gagal: %v", err)
	}
	if _, err := svc.Login(ctx, "bandung@example.test", "rahasia123", RequestMeta{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("kata sandi lama harus ditolak, dapat %v", err)
	}
}

func TestAturUlangKedaluwarsa(t *testing.T) {
	_, svc, mail, clk := newResetFixture(t)
	ctx := context.Background()

	// Masih berlaku sebelum masa berlakunya habis.
	if err := svc.ForgotPassword(ctx, "bandung@example.test", 60*time.Minute, RequestMeta{}); err != nil {
		t.Fatalf("lupa kata sandi: %v", err)
	}
	clk.Set(waktuUji.Add(59 * time.Minute))
	if err := svc.ResetPassword(ctx, mail.sent[0].Token, "sandibaru123", RequestMeta{}); err != nil {
		t.Fatalf("tautan yang belum kedaluwarsa harus berlaku: %v", err)
	}

	// Token kedua diterbitkan pada menit ke-59, masa berlaku 60 menit:
	// lewat menit ke-120 token harus sudah ditolak.
	if err := svc.ForgotPassword(ctx, "bandung@example.test", 60*time.Minute, RequestMeta{}); err != nil {
		t.Fatalf("lupa kata sandi kedua: %v", err)
	}
	token2 := mail.sent[1].Token
	clk.Set(waktuUji.Add(120 * time.Minute))
	if err := svc.ResetPassword(ctx, token2, "sandibaru123", RequestMeta{}); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("token kedaluwarsa harus ErrResetInvalid, dapat %v", err)
	}
}

func TestAturUlangSandiLemahTidakMembakarToken(t *testing.T) {
	_, svc, mail, _ := newResetFixture(t)
	ctx := context.Background()

	if err := svc.ForgotPassword(ctx, "bandung@example.test", time.Hour, RequestMeta{}); err != nil {
		t.Fatalf("lupa kata sandi: %v", err)
	}
	token := mail.sent[0].Token

	if err := svc.ResetPassword(ctx, token, "pendek", RequestMeta{}); !errors.Is(err, ErrPasswordTooWeak) {
		t.Errorf("kata sandi lemah harus ErrPasswordTooWeak, dapat %v", err)
	}
	// Token belum terpakai: percobaan berikutnya dengan kata sandi sah berhasil.
	if err := svc.ResetPassword(ctx, token, "sandibaru123", RequestMeta{}); err != nil {
		t.Errorf("token tidak boleh terbakar oleh percobaan gagal: %v", err)
	}
}

func TestTautanBaruMembatalkanTautanLama(t *testing.T) {
	_, svc, mail, _ := newResetFixture(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := svc.ForgotPassword(ctx, "bandung@example.test", time.Hour, RequestMeta{}); err != nil {
			t.Fatalf("lupa kata sandi ke-%d: %v", i+1, err)
		}
	}
	if len(mail.sent) != 2 {
		t.Fatalf("harus ada 2 email, dapat %d", len(mail.sent))
	}
	lama, baru := mail.sent[0].Token, mail.sent[1].Token

	if err := svc.ResetPassword(ctx, lama, "sandibaru123", RequestMeta{}); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("tautan lama harus sudah tidak berlaku, dapat %v", err)
	}
	if err := svc.ResetPassword(ctx, baru, "sandibaru123", RequestMeta{}); err != nil {
		t.Errorf("tautan terbaru harus berlaku, dapat %v", err)
	}
}

func TestGantiKataSandiCabutSesiLainSaja(t *testing.T) {
	store, svc, _, _ := newResetFixture(t)
	ctx := context.Background()

	sesiAktif, err := svc.Login(ctx, "bandung@example.test", "rahasia123", RequestMeta{})
	if err != nil {
		t.Fatalf("login sesi aktif: %v", err)
	}
	sesiLain, err := svc.Login(ctx, "bandung@example.test", "rahasia123", RequestMeta{})
	if err != nil {
		t.Fatalf("login sesi lain: %v", err)
	}

	if err := svc.ChangePassword(ctx, sesiAktif.Actor, "salahsekali", "sandibaru123", RequestMeta{}); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("kata sandi lama salah harus ErrWrongPassword, dapat %v", err)
	}
	if err := svc.ChangePassword(ctx, sesiAktif.Actor, "rahasia123", "rahasia123", RequestMeta{}); !errors.Is(err, ErrPasswordTooWeak) {
		t.Errorf("kata sandi baru sama dengan lama harus ditolak, dapat %v", err)
	}
	if err := svc.ChangePassword(ctx, sesiAktif.Actor, "rahasia123", "sandibaru123", RequestMeta{}); err != nil {
		t.Fatalf("ganti kata sandi: %v", err)
	}

	if _, err := svc.Authenticate(ctx, sesiAktif.Token); err != nil {
		t.Errorf("sesi yang sedang dipakai harus tetap hidup, dapat %v", err)
	}
	if _, err := svc.Authenticate(ctx, sesiLain.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("sesi lain harus dicabut, dapat %v", err)
	}
	if store.SessionCount() != 1 {
		t.Errorf("harus tersisa tepat 1 sesi, dapat %d", store.SessionCount())
	}
	if _, err := svc.Login(ctx, "bandung@example.test", "sandibaru123", RequestMeta{}); err != nil {
		t.Errorf("login dengan kata sandi baru gagal: %v", err)
	}
}

func TestEmailResetMailerMenyusunTautanPortal(t *testing.T) {
	m := &fakeMailer{}
	rm := NewEmailResetMailer(m,
		"https://grahita-branch.tiusclaw.com",
		"https://grahita-admin.tiusclaw.com", "")
	ctx := context.Background()
	exp := waktuUji.Add(time.Hour)

	if err := rm.SendPasswordReset(ctx, ResetMessage{
		To: "bandung@example.test", Nama: "Grahita Bandung",
		Kind: KindBranch, Token: "tok en+1", ExpiresAt: exp, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("kirim email: %v", err)
	}
	if len(m.msg.To) != 1 || m.msg.To[0] != "bandung@example.test" {
		t.Errorf("tujuan email salah: %v", m.msg.To)
	}
	if !strings.Contains(m.msg.Text, "https://grahita-branch.tiusclaw.com/lupa?token=tok+en%2B1") {
		t.Errorf("tautan cabang salah: %s", m.msg.Text)
	}
	if !strings.Contains(m.msg.Text, "berlaku 60 menit") {
		t.Errorf("masa berlaku tidak dicantumkan: %s", m.msg.Text)
	}

	// Akun admin diarahkan ke portal admin.
	if got := rm.ResetURL(KindAdmin, "abc"); got != "https://grahita-admin.tiusclaw.com/lupa?token=abc" {
		t.Errorf("tautan admin salah: %s", got)
	}
}

func TestMatriksOtorisasiRekeningDanReporting(t *testing.T) {
	manager := Actor{UserID: "a1", Kind: KindAdmin, Role: RoleManager}
	superadmin := Actor{UserID: "a2", Kind: KindAdmin, Role: RoleSuperadmin}

	// Manager dilarang bank/report/settings/admin user (PRD §2.1).
	for _, cap := range []Capability{CapAdminBank, CapAdminReports, CapAdminSettings, CapAdminUsersAdmin} {
		if manager.Can(cap) {
			t.Errorf("manager tidak boleh punya kapabilitas %s", cap)
		}
		if !superadmin.Can(cap) {
			t.Errorf("superadmin harus punya kapabilitas %s", cap)
		}
	}
}

func TestOwnershipBranchDariSesi(t *testing.T) {
	cabangA := Actor{UserID: "b1", Kind: KindBranch, Role: RoleBranch, BranchID: "br-a"}
	cabangB := Actor{UserID: "b2", Kind: KindBranch, Role: RoleBranch, BranchID: "br-b"}
	manager := Actor{UserID: "a1", Kind: KindAdmin, Role: RoleManager}

	if err := cabangA.RequireBranch("br-a"); err != nil {
		t.Errorf("cabang A harus boleh atas namanya sendiri: %v", err)
	}
	if err := cabangA.RequireBranch("br-b"); !errors.Is(err, ErrBranchForbidden) {
		t.Errorf("cabang A dilarang menyentuh br-b, dapat %v", err)
	}
	if err := cabangB.RequireBranch(""); !errors.Is(err, ErrBranchForbidden) {
		t.Errorf("branch_id kosong harus ditolak, dapat %v", err)
	}
	if err := manager.RequireBranch("br-b"); err != nil {
		t.Errorf("manager boleh atas nama cabang mana pun: %v", err)
	}
	if Anonymous().OwnsBranch("br-a") {
		t.Error("anonim tidak boleh memiliki cabang")
	}
}
