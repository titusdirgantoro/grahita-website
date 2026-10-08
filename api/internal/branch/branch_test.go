package branch

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
)

// waktuUji adalah titik waktu tetap untuk menguji masa berlaku token.
var waktuUji = time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC)

func adminAktor() auth.Actor {
	return auth.Actor{
		UserID: "adm-1", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin,
		Nama: "Rina", Email: "rina@example.test",
	}
}

func managerAktor() auth.Actor {
	return auth.Actor{
		UserID: "adm-2", Kind: auth.KindAdmin, Role: auth.RoleManager,
		Nama: "Budi", Email: "budi@example.test",
	}
}

func cabangAktor() auth.Actor {
	return auth.Actor{
		UserID: "usr-a", Kind: auth.KindBranch, Role: auth.RoleBranch,
		BranchID: "br-a", Nama: "Grahita Cabang A", Email: "a@example.test",
	}
}

// ctxUji menitipkan IP request seperti middleware router, supaya entri audit
// yang dihasilkan bisa diperiksa lengkap.
func ctxUji(ip string) context.Context {
	return audit.WithRequestMeta(context.Background(), audit.RequestMeta{IP: ip})
}

func newUji(t *testing.T) (*Service, *Memory) {
	t.Helper()
	repo := NewMemory()
	return NewService(repo, clock.Fixed(waktuUji)), repo
}

func seedDuaCabang(m *Memory) {
	m.Put(Branch{ID: "br-a", Nama: "Grahita Cabang A", NamaKepala: "Kepala A", Email: "a@example.test", IsActive: true, IsCNC: true})
	m.Put(Branch{ID: "br-b", Nama: "Grahita Cabang B", NamaKepala: "Kepala B", Email: "b@example.test", IsActive: false, IsSpecial: true})
}

// TestIsCNCFlagCabang memverifikasi pembacaan flag Cash & Carry lintas-domain
// (dipakai aturan C&C domain pesanan, F1-12): flag dibaca dari master cabang
// tanpa menuntut aktor, dan cabang tak dikenal menghasilkan ErrNotFound.
func TestIsCNCFlagCabang(t *testing.T) {
	svc, repo := newUji(t)
	seedDuaCabang(repo)
	ctx := context.Background()

	if cnc, err := svc.IsCNC(ctx, "br-a"); err != nil || !cnc {
		t.Errorf("cabang A harus berflag C&C, dapat %v (%v)", cnc, err)
	}
	if cnc, err := svc.IsCNC(ctx, "br-b"); err != nil || cnc {
		t.Errorf("cabang B tidak berflag C&C, dapat %v (%v)", cnc, err)
	}
	if _, err := svc.IsCNC(ctx, "br-tidak-ada"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cabang tak dikenal harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.IsCNC(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("branch_id kosong harus ErrNotFound, dapat %v", err)
	}
}

func TestLayananMasterCabangDibatasiKapabilitas(t *testing.T) {
	svc, mem := newUji(t)
	seedDuaCabang(mem)
	ctx := ctxUji("10.0.0.1")

	kasus := map[string]func(auth.Actor) error{
		"daftar": func(a auth.Actor) error {
			_, err := svc.List(ctx, a, Filter{})
			return err
		},
		"detail": func(a auth.Actor) error {
			_, err := svc.Get(ctx, a, "br-a")
			return err
		},
		"buat": func(a auth.Actor) error {
			_, err := svc.Create(ctx, a, NewBranch{Nama: "Cabang Baru", NamaKepala: "Kepala", Email: "baru@example.test"})
			return err
		},
		"ubah": func(a auth.Actor) error {
			_, err := svc.Update(ctx, a, "br-a", Patch{})
			return err
		},
		"hapus": func(a auth.Actor) error {
			return svc.Delete(ctx, a, "br-a")
		},
		"token": func(a auth.Actor) error {
			_, err := svc.GenerateToken(ctx, a, "br-a", 14*24*time.Hour)
			return err
		},
	}
	for nama, jalankan := range kasus {
		for _, aktor := range []auth.Actor{auth.Anonymous(), cabangAktor()} {
			if err := jalankan(aktor); !errors.Is(err, ErrForbidden) {
				t.Errorf("%s oleh %q harus ErrForbidden, dapat %v", nama, aktor.Kind, err)
			}
		}
		// Manager punya kapabilitas master cabang (PRD §2.1).
		if err := jalankan(managerAktor()); errors.Is(err, ErrForbidden) {
			t.Errorf("%s oleh manager tidak boleh ditolak kapabilitas", nama)
		}
	}
}

func TestBuatCabangMenormalkanEmailDanMencatatAudit(t *testing.T) {
	svc, mem := newUji(t)
	ctx := ctxUji("10.0.0.5")

	b, err := svc.Create(ctx, adminAktor(), NewBranch{
		Nama: "  Grahita Cabang C  ", NamaKepala: "  Kepala C ", Email: "  Cabang-C@Example.Test ",
		IsActive: nil,
	})
	if err != nil {
		t.Fatalf("buat cabang: %v", err)
	}
	if b.Email != "cabang-c@example.test" {
		t.Errorf("email harus dinormalkan, dapat %q", b.Email)
	}
	if b.Nama != "Grahita Cabang C" || b.NamaKepala != "Kepala C" {
		t.Errorf("spasi tepi harus dipangkas, dapat %q / %q", b.Nama, b.NamaKepala)
	}
	if !b.IsActive {
		t.Error("cabang baru tanpa is_active harus aktif")
	}
	if b.ID == "" {
		t.Error("cabang baru harus punya id")
	}

	// Tersimpan di penyimpanan dengan id yang sama.
	if got, err := mem.Get(ctx, b.ID); err != nil || got.Email != b.Email {
		t.Errorf("cabang tersimpan tidak sesuai: %+v (%v)", got, err)
	}

	entri := mem.AuditEntries()
	if len(entri) != 1 {
		t.Fatalf("harus ada tepat 1 entri audit, dapat %d", len(entri))
	}
	e := entri[0]
	if e.Entity != audit.EntityBranch || e.Action != audit.ActionCreate {
		t.Errorf("entri audit harus branch/create, dapat %s/%s", e.Entity, e.Action)
	}
	if e.ActorID != "adm-1" || e.ActorKind != auth.KindAdmin || e.ActorNama != "Rina" {
		t.Errorf("aktor audit tidak sesuai: %+v", e)
	}
	if e.IP != "10.0.0.5" {
		t.Errorf("IP audit harus dari context, dapat %q", e.IP)
	}
	if e.EntityID != b.ID {
		t.Errorf("entity_id audit harus id cabang, dapat %q", e.EntityID)
	}
	if e.Before != nil || e.After == nil {
		t.Errorf("create harus Before=nil dan After terisi: %+v", e)
	}
}

func TestBuatCabangMenolakDataTidakValid(t *testing.T) {
	svc, _ := newUji(t)
	ctx := ctxUji("10.0.0.1")

	kasus := []struct {
		nama string
		in   NewBranch
	}{
		{"nama terlalu pendek", NewBranch{Nama: "A", NamaKepala: "Kepala", Email: "x@example.test"}},
		{"kepala kosong", NewBranch{Nama: "Cabang X", NamaKepala: " ", Email: "x@example.test"}},
		{"email tanpa domain", NewBranch{Nama: "Cabang X", NamaKepala: "Kepala", Email: "bukan-email"}},
		{"email kosong", NewBranch{Nama: "Cabang X", NamaKepala: "Kepala"}},
	}
	for _, k := range kasus {
		if _, err := svc.Create(ctx, adminAktor(), k.in); !errors.Is(err, ErrInvalidData) {
			t.Errorf("%s harus ErrInvalidData, dapat %v", k.nama, err)
		}
	}
}

func TestBuatDanUbahCabangMenolakEmailTerpakai(t *testing.T) {
	svc, mem := newUji(t)
	seedDuaCabang(mem)
	ctx := ctxUji("10.0.0.1")

	// Konflik dengan cabang lain, termasuk beda huruf besar/kecil (citext).
	if _, err := svc.Create(ctx, adminAktor(), NewBranch{
		Nama: "Cabang Duplikat", NamaKepala: "Kepala", Email: "A@EXAMPLE.TEST",
	}); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("email ganda harus ErrEmailTaken, dapat %v", err)
	}

	// Konflik dengan email akun admin: login mencari admin lebih dulu, jadi
	// email itu tidak boleh dipakai cabang.
	mem.PutAdminEmail("rina@example.test")
	if _, err := svc.Create(ctx, adminAktor(), NewBranch{
		Nama: "Cabang Admin", NamaKepala: "Kepala", Email: "rina@example.test",
	}); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("email milik admin harus ErrEmailTaken, dapat %v", err)
	}

	// Mengubah ke email cabang lain ditolak; ke email sendiri boleh.
	if _, err := svc.Update(ctx, adminAktor(), "br-a", Patch{Email: ptr("b@example.test")}); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("ubah ke email cabang lain harus ErrEmailTaken, dapat %v", err)
	}
	if _, err := svc.Update(ctx, adminAktor(), "br-a", Patch{Email: ptr("a@example.test")}); err != nil {
		t.Errorf("email sendiri tidak boleh dianggap konflik: %v", err)
	}
}

func TestUbahCabangMenonaktifkanDanMencatatPerubahan(t *testing.T) {
	svc, mem := newUji(t)
	seedDuaCabang(mem)
	ctx := ctxUji("10.0.0.9")

	b, err := svc.Update(ctx, adminAktor(), "br-a", Patch{
		IsActive: ptr(false), IsSpecial: ptr(true), Telepon: ptr("0812"),
	})
	if err != nil {
		t.Fatalf("ubah cabang: %v", err)
	}
	if b.IsActive || !b.IsSpecial || b.Telepon != "0812" {
		t.Errorf("perubahan tidak diterapkan: %+v", b)
	}
	// Field yang tidak dikirim tidak berubah.
	if b.Nama != "Grahita Cabang A" || !b.IsCNC {
		t.Errorf("field lain harus tetap: %+v", b)
	}

	entri := mem.AuditEntries()
	if len(entri) != 1 || entri[0].Action != audit.ActionUpdate {
		t.Fatalf("harus ada 1 entri audit update, dapat %+v", entri)
	}
	if entri[0].Before == nil || entri[0].After == nil {
		t.Error("update harus mencatat snapshot sebelum dan sesudah")
	}
	before, ok := entri[0].Before.(Branch)
	if !ok || before.IsActive != true {
		t.Errorf("snapshot sebelum harus cabang aktif: %+v", entri[0].Before)
	}
}

func TestHapusCabangDitolakBilaSudahPunyaPesanan(t *testing.T) {
	svc, mem := newUji(t)
	seedDuaCabang(mem)
	ctx := ctxUji("10.0.0.1")

	mem.PutOrder("br-a")
	if err := svc.Delete(ctx, adminAktor(), "br-a"); !errors.Is(err, ErrHasOrders) {
		t.Fatalf("cabang berp Pesanan harus ErrHasOrders, dapat %v", err)
	}
	if len(mem.AuditEntries()) != 0 {
		t.Error("penolakan hapus tidak boleh mencatat audit")
	}

	// Cabang tanpa pesanan boleh dihapus, dan penghapusannya tercatat.
	if err := svc.Delete(ctx, adminAktor(), "br-b"); err != nil {
		t.Fatalf("hapus cabang tanpa pesanan: %v", err)
	}
	if _, err := mem.Get(ctx, "br-b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cabang harus hilang setelah dihapus, dapat %v", err)
	}
	entri := mem.AuditEntries()
	if len(entri) != 1 || entri[0].Action != audit.ActionDelete || entri[0].Before == nil {
		t.Fatalf("hapus harus mencatat audit dengan snapshot sebelum: %+v", entri)
	}
}

func TestGenerateTokenSekaliPakaiMenggantikanTokenLama(t *testing.T) {
	svc, mem := newUji(t)
	seedDuaCabang(mem)
	ctx := ctxUji("10.0.0.7")

	ttl := 14 * 24 * time.Hour
	pertama, err := svc.GenerateToken(ctx, adminAktor(), "br-a", ttl)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	pola := regexp.MustCompile(`^GRH-[A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4}$`)
	if !pola.MatchString(pertama.Token) {
		t.Fatalf("format token tidak sesuai: %q", pertama.Token)
	}
	if pertama.Prefix != pertama.Token[:8] || len(pertama.Prefix) != 8 {
		t.Errorf("prefix harus 8 karakter pertama token, dapat %q", pertama.Prefix)
	}
	if !pertama.ExpiresAt.Equal(waktuUji.Add(ttl)) {
		t.Errorf("masa berlaku harus clock+ttl, dapat %v", pertama.ExpiresAt)
	}

	// Yang tersimpan HANYA hash SHA-256 dari bentuk normalisasi token.
	hash, prefix, expires, ok := mem.ActiveToken("br-a")
	if !ok {
		t.Fatal("token aktif harus tersimpan")
	}
	if string(hash) == pertama.Token {
		t.Error("nilai token mentah tidak boleh tersimpan")
	}
	if string(hash) != string(auth.HashRegistrationToken(pertama.Token)) {
		t.Error("hash tersimpan harus sha256 dari token ternormalisasi")
	}
	if prefix != pertama.Prefix || !expires.Equal(pertama.ExpiresAt) {
		t.Errorf("prefix/kedaluwarsa tersimpan tidak sesuai: %q %v", prefix, expires)
	}
	// Admin Portal bisa melihat token aktifnya lewat daftar cabang.
	if got, err := mem.Get(ctx, "br-a"); err != nil || got.TokenPrefix != pertama.Prefix {
		t.Errorf("prefix harus tampil di data cabang: %+v (%v)", got, err)
	}

	// Generate kedua menggantikan token lama: hanya token baru yang sah.
	kedua, err := svc.GenerateToken(ctx, adminAktor(), "br-a", ttl)
	if err != nil {
		t.Fatalf("generate token kedua: %v", err)
	}
	if kedua.Token == pertama.Token {
		t.Error("generate baru harus menghasilkan token berbeda")
	}
	hashBaru, _, _, _ := mem.ActiveToken("br-a")
	if string(hashBaru) != string(auth.HashRegistrationToken(kedua.Token)) {
		t.Error("hash tersimpan harus milik token baru")
	}
	if string(hashBaru) == string(auth.HashRegistrationToken(pertama.Token)) {
		t.Error("token lama harus batal setelah generate baru")
	}

	// Dua entri audit, keduanya token_generate; yang kedua mencatat token lama
	// di "before".
	entri := mem.AuditEntries()
	if len(entri) != 2 || entri[0].Action != audit.ActionTokenGenerate || entri[1].Action != audit.ActionTokenGenerate {
		t.Fatalf("harus ada 2 entri audit token_generate, dapat %+v", entri)
	}
	if entri[0].Before != nil {
		t.Errorf("generate pertama tidak punya token sebelumnya: %+v", entri[0].Before)
	}
	if entri[1].Before == nil {
		t.Error("generate kedua harus mencatat token lama di before")
	}
	after, ok := entri[1].After.(map[string]any)
	if !ok || after["token_prefix"] != kedua.Prefix {
		t.Errorf("after harus memuat prefix token baru: %+v", entri[1].After)
	}
}

func TestGenerateTokenDitolakSaatPunyaAkunAtauCabangNonaktif(t *testing.T) {
	svc, mem := newUji(t)
	seedDuaCabang(mem)
	ctx := ctxUji("10.0.0.1")
	ttl := 14 * 24 * time.Hour

	// br-b nonaktif.
	if _, err := svc.GenerateToken(ctx, adminAktor(), "br-b", ttl); !errors.Is(err, ErrInactive) {
		t.Errorf("cabang nonaktif harus ErrInactive, dapat %v", err)
	}
	// br-a aktif tetapi sudah punya akun (1 cabang = 1 akun).
	mem.PutAccount("br-a")
	if _, err := svc.GenerateToken(ctx, adminAktor(), "br-a", ttl); !errors.Is(err, ErrHasAccount) {
		t.Errorf("cabang berakun harus ErrHasAccount, dapat %v", err)
	}
	// TTL tidak masuk akal ditolak.
	if _, err := svc.GenerateToken(ctx, adminAktor(), "br-a", 0); !errors.Is(err, ErrInvalidData) {
		t.Errorf("ttl 0 harus ErrInvalidData, dapat %v", err)
	}
	if len(mem.AuditEntries()) != 0 {
		t.Error("penolakan token tidak boleh mencatat audit")
	}
}

func TestDaftarCabangDapatDisaring(t *testing.T) {
	svc, mem := newUji(t)
	seedDuaCabang(mem)
	ctx := ctxUji("10.0.0.1")

	semua, err := svc.List(ctx, managerAktor(), Filter{})
	if err != nil || len(semua) != 2 {
		t.Fatalf("tanpa filter harus 2 cabang, dapat %d (%v)", len(semua), err)
	}
	if semua[0].Nama != "Grahita Cabang A" {
		t.Errorf("daftar harus urut nama, dapat %q", semua[0].Nama)
	}

	if aktif, _ := svc.List(ctx, managerAktor(), Filter{Active: ptr(true)}); len(aktif) != 1 || aktif[0].ID != "br-a" {
		t.Errorf("filter aktif=true harus menyisakan br-a: %+v", aktif)
	}
	if cnc, _ := svc.List(ctx, managerAktor(), Filter{CNC: ptr(true)}); len(cnc) != 1 || cnc[0].ID != "br-a" {
		t.Errorf("filter cnc=true harus menyisakan br-a: %+v", cnc)
	}
	if sp, _ := svc.List(ctx, managerAktor(), Filter{Special: ptr(true)}); len(sp) != 1 || sp[0].ID != "br-b" {
		t.Errorf("filter special=true harus menyisakan br-b: %+v", sp)
	}
	if q, _ := svc.List(ctx, managerAktor(), Filter{Q: "B@EXAMPLE"}); len(q) != 1 || q[0].ID != "br-b" {
		t.Errorf("pencarian harus mencakup email tanpa peka huruf besar: %+v", q)
	}
	if kosong, _ := svc.List(ctx, managerAktor(), Filter{Q: "tidak ada"}); len(kosong) != 0 {
		t.Errorf("pencarian tanpa hasil harus kosong: %+v", kosong)
	}
}

func TestCabangTidakDikenalMenghasilkanNotFound(t *testing.T) {
	svc, _ := newUji(t)
	ctx := ctxUji("10.0.0.1")

	if _, err := svc.Get(ctx, adminAktor(), "tidak-ada"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.Update(ctx, adminAktor(), "tidak-ada", Patch{Nama: ptr("X")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update harus ErrNotFound, dapat %v", err)
	}
	if err := svc.Delete(ctx, adminAktor(), "tidak-ada"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.GenerateToken(ctx, adminAktor(), "tidak-ada", time.Hour); !errors.Is(err, ErrNotFound) {
		t.Errorf("GenerateToken harus ErrNotFound, dapat %v", err)
	}
	if _, err := svc.Get(ctx, adminAktor(), ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("id kosong harus ErrNotFound, dapat %v", err)
	}
}

func TestTokenRegistrasiNormalisasiDanHash(t *testing.T) {
	token, prefix, err := auth.NewRegistrationToken()
	if err != nil {
		t.Fatalf("buat token: %v", err)
	}
	// Tanda hubung boleh hilang saat pengguna menempelkannya.
	if got, want := auth.HashRegistrationToken(strings.ReplaceAll(token, "-", "")), auth.HashRegistrationToken(token); string(got) != string(want) {
		t.Error("token dengan/tanpa tanda hubung harus menghasilkan hash yang sama")
	}
	// Huruf besar/kecil dan spasi tidak mengubah arti token.
	if got, want := auth.HashRegistrationToken(strings.ToLower(" "+token+" ")), auth.HashRegistrationToken(token); string(got) != string(want) {
		t.Error("token harus ternormalisasi terhadap huruf besar/kecil dan spasi")
	}
	if prefix != token[:8] {
		t.Errorf("prefix harus 8 karakter pertama, dapat %q", prefix)
	}
}

func ptr[T any](v T) *T { return &v }
