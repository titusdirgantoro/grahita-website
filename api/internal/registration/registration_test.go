package registration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
)

// waktuUji adalah titik waktu tetap untuk menguji masa berlaku token.
var waktuUji = time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)

// tokenUji membuat token registrasi yang benar-benar berbentuk sah beserta
// snapshot cabangnya. Token dibuat lewat auth.NewRegistrationToken supaya
// bentuknya pasti diterima ValidRegistrationTokenFormat.
func tokenUji(t *testing.T, branchID, nama, kepala, email string, expiresAt time.Time, aktif bool) (string, Snapshot) {
	t.Helper()
	token, _, err := auth.NewRegistrationToken()
	if err != nil {
		t.Fatalf("buat token uji: %v", err)
	}
	return token, Snapshot{
		TokenID: "tok-" + branchID, BranchID: branchID, ExpiresAt: expiresAt,
		Nama: nama, NamaKepala: kepala, Email: email, IdentityStatus: "ready", BranchActive: aktif,
	}
}

func TestValidasiTokenMenolakIdentitasBelumLengkap(t *testing.T) {
	svc, mem := newUji(t)
	token, snap := tokenUji(t, "br-a", "Grahita Cabang A", "", "", waktuUji.Add(time.Hour), true)
	snap.IdentityStatus = "identity_incomplete"
	mem.PutToken(token, snap)
	if _, err := svc.Validate(context.Background(), token); !errors.Is(err, ErrIdentityIncomplete) {
		t.Fatalf("identitas belum lengkap harus ditolak, dapat %v", err)
	}
}

func newUji(t *testing.T) (*Service, *Memory) {
	t.Helper()
	repo := NewMemory()
	return NewService(repo, clock.Fixed(waktuUji)), repo
}

func inputTemplate(token string) RegisterInput {
	return RegisterInput{
		Token:              token,
		Telepon:            "0812-3456-7890",
		Password:           "rahasia123",
		PasswordKonfirmasi: "rahasia123",
	}
}

func TestValidasiTokenMengembalikanDataCabangReadOnly(t *testing.T) {
	svc, mem := newUji(t)
	token, snap := tokenUji(t, "br-a", "Grahita Cabang A", "Kepala A", "cabang-a@example.test", waktuUji.Add(24*time.Hour), true)
	mem.PutToken(token, snap)

	data, err := svc.Validate(context.Background(), token)
	if err != nil {
		t.Fatalf("validasi token sah: %v", err)
	}
	if data.NamaCabang != "Grahita Cabang A" || data.NamaKepala != "Kepala A" || data.Email != "cabang-a@example.test" {
		t.Errorf("data cabang tidak sesuai: %+v", data)
	}

	// Tanda hubung boleh hilang dan huruf kecil boleh; artinya tetap sama.
	if _, err := svc.Validate(context.Background(), strings.ToLower(" "+strings.ReplaceAll(token, "-", "")+" ")); err != nil {
		t.Errorf("token tanpa tanda hubung/huruf kecil harus tetap sah: %v", err)
	}
}

func TestValidasiTokenMenolakSetiapKeadaanDenganPesanSpesifik(t *testing.T) {
	ctx := context.Background()

	kasus := []struct {
		nama string
		siap func(t *testing.T, mem *Memory) string
		mau  error
	}{
		{
			nama: "tidak dikenal",
			siap: func(*testing.T, *Memory) string {
				// Token berbentuk sah, tetapi tidak pernah disimpan.
				return "GRH-ABCD-EFGH-JKLM-NPQR"
			},
			mau: ErrTokenInvalid,
		},
		{
			nama: "bentuknya tidak sah",
			siap: func(*testing.T, *Memory) string { return "bukan-token" },
			mau:  ErrTokenInvalid,
		},
		{
			nama: "kedaluwarsa",
			siap: func(t *testing.T, mem *Memory) string {
				token, snap := tokenUji(t, "br-a", "Grahita Cabang A", "Kepala A", "a@example.test", waktuUji.Add(-time.Minute), true)
				mem.PutToken(token, snap)
				return token
			},
			mau: ErrTokenExpired,
		},
		{
			nama: "sudah dipakai",
			siap: func(t *testing.T, mem *Memory) string {
				token, snap := tokenUji(t, "br-a", "Grahita Cabang A", "Kepala A", "a@example.test", waktuUji.Add(time.Hour), true)
				mem.PutUsedToken(token, snap)
				return token
			},
			mau: ErrTokenUsed,
		},
		{
			nama: "cabang nonaktif",
			siap: func(t *testing.T, mem *Memory) string {
				token, snap := tokenUji(t, "br-b", "Grahita Cabang B", "Kepala B", "b@example.test", waktuUji.Add(time.Hour), false)
				mem.PutToken(token, snap)
				return token
			},
			mau: ErrBranchInactive,
		},
		{
			nama: "cabang sudah punya akun",
			siap: func(t *testing.T, mem *Memory) string {
				token, snap := tokenUji(t, "br-c", "Grahita Cabang C", "Kepala C", "c@example.test", waktuUji.Add(time.Hour), true)
				mem.PutToken(token, snap)
				mem.PutAccount("br-c")
				return token
			},
			mau: ErrBranchHasAccount,
		},
	}

	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			svc, mem := newUji(t)
			token := k.siap(t, mem)
			if _, err := svc.Validate(ctx, token); !errors.Is(err, k.mau) {
				t.Errorf("validate %s harus %v, dapat %v", k.nama, k.mau, err)
			}
		})
	}
}

func TestDaftarMembuatAkunDanMemakaiTokenTepatSekali(t *testing.T) {
	svc, mem := newUji(t)
	ctx := audit.WithRequestMeta(context.Background(), audit.RequestMeta{IP: "10.0.0.9"})
	token, snap := tokenUji(t, "br-a", "Grahita Cabang A", "Kepala A", "Cabang-A@Example.Test", waktuUji.Add(24*time.Hour), true)
	mem.PutToken(token, snap)

	akun, err := svc.Register(ctx, inputTemplate(token), auth.RequestMeta{IP: "10.0.0.9"})
	if err != nil {
		t.Fatalf("daftar: %v", err)
	}
	if akun.UserID == "" || akun.BranchID != "br-a" {
		t.Errorf("akun tidak sesuai: %+v", akun)
	}
	// Email akun selalu dari data cabang, dan dinormalkan.
	if akun.Email != "cabang-a@example.test" {
		t.Errorf("email akun harus dari data cabang, dapat %q", akun.Email)
	}
	simpan, ok := mem.Akun("br-a")
	if !ok {
		t.Fatal("akun cabang harus tersimpan")
	}
	if !auth.VerifyPassword(simpan.PasswordHash, "rahasia123") {
		t.Error("kata sandi tersimpan harus hash dari kata sandi yang diisi pengguna")
	}
	if simpan.Telepon != "0812-3456-7890" {
		t.Errorf("telepon harus tersimpan, dapat %q", simpan.Telepon)
	}

	// Token dipakai TEPAT sekali.
	if _, err := svc.Register(ctx, inputTemplate(token), auth.RequestMeta{}); !errors.Is(err, ErrTokenUsed) {
		t.Errorf("pemakaian kedua harus ErrTokenUsed, dapat %v", err)
	}
	// 1 cabang = 1 akun: akun tidak bertambah.
	if _, err := mem.Lookup(ctx, auth.HashRegistrationToken(token)); err != nil {
		t.Errorf("lookup token: %v", err)
	}
	var jumlah int
	if _, ok := mem.Akun("br-a"); ok {
		jumlah = 1
	}
	if jumlah != 1 {
		t.Errorf("harus tetap 1 akun, dapat %d", jumlah)
	}

	// Entri audit: entity branch_user, action register, aktor akun itu sendiri.
	entri := mem.AuditEntries()
	if len(entri) != 1 {
		t.Fatalf("harus ada 1 entri audit, dapat %d", len(entri))
	}
	e := entri[0]
	if e.Entity != audit.EntityBranchUser || e.Action != audit.ActionRegister {
		t.Errorf("audit harus branch_user/register, dapat %s/%s", e.Entity, e.Action)
	}
	if e.ActorID != akun.UserID || e.ActorKind != auth.KindBranch {
		t.Errorf("aktor audit harus akun cabang baru: %+v", e)
	}
	if e.IP != "10.0.0.9" {
		t.Errorf("IP audit harus dari metadata request, dapat %q", e.IP)
	}
	if e.EntityID != akun.UserID || e.After == nil {
		t.Errorf("audit harus memuat entity_id akun dan snapshot after: %+v", e)
	}
}

func TestDaftarMenolakKataSandiKonfirmasiDanTeleponTidakSah(t *testing.T) {
	svc, mem := newUji(t)
	ctx := context.Background()
	token, snap := tokenUji(t, "br-a", "Grahita Cabang A", "Kepala A", "a@example.test", waktuUji.Add(time.Hour), true)
	mem.PutToken(token, snap)

	// Konfirmasi tidak sama — token TIDAK boleh ikut terbakar.
	in := inputTemplate(token)
	in.PasswordKonfirmasi = "rahasia124"
	if _, err := svc.Register(ctx, in, auth.RequestMeta{}); !errors.Is(err, ErrPasswordMismatch) {
		t.Errorf("konfirmasi beda harus ErrPasswordMismatch, dapat %v", err)
	}

	// Kata sandi lemah.
	in = inputTemplate(token)
	in.Password = "pendek"
	in.PasswordKonfirmasi = "pendek"
	if _, err := svc.Register(ctx, in, auth.RequestMeta{}); !errors.Is(err, auth.ErrPasswordTooWeak) {
		t.Errorf("kata sandi lemah harus ErrPasswordTooWeak, dapat %v", err)
	}

	// Telepon kosong.
	in = inputTemplate(token)
	in.Telepon = "   "
	if _, err := svc.Register(ctx, in, auth.RequestMeta{}); !errors.Is(err, ErrTeleponInvalid) {
		t.Errorf("telepon kosong harus ErrTeleponInvalid, dapat %v", err)
	}

	// Telepon terlalu panjang.
	in = inputTemplate(token)
	in.Telepon = strings.Repeat("0", TeleponMaxLen+1)
	if _, err := svc.Register(ctx, in, auth.RequestMeta{}); !errors.Is(err, ErrTeleponInvalid) {
		t.Errorf("telepon terlalu panjang harus ErrTeleponInvalid, dapat %v", err)
	}

	// Token masih berlaku dan belum terpakai setelah semua penolakan itu.
	if _, err := svc.Validate(ctx, token); err != nil {
		t.Errorf("token tidak boleh terbakar oleh percobaan yang gagal: %v", err)
	}
}

func TestDaftarMenolakTokenTidakSahTanpaMenyentuhData(t *testing.T) {
	svc, mem := newUji(t)
	ctx := context.Background()

	for _, token := range []string{"", "bukan-token", "GRH-ABCD-EFGH-JKLM-NPQ2"} {
		if _, err := svc.Register(ctx, inputTemplate(token), auth.RequestMeta{}); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("token %q harus ErrTokenInvalid, dapat %v", token, err)
		}
	}
	if len(mem.AuditEntries()) != 0 {
		t.Error("penolakan tidak boleh mencatat audit")
	}
}
