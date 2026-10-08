package order

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/grahita/portal/api/internal/auth"
)

// adminRepoFake adalah AdminRepository untuk pengujian unit: ia merekam
// parameter yang diterima service dan mengembalikan jawaban yang bisa diatur.
type adminRepoFake struct {
	// terakhir adalah parameter daftar terakhir yang diterima.
	terakhir AdminQuery
	// panggilList menghitung berapa kali ListAdmin dipanggil.
	panggilList int
	// panggilGet menghitung berapa kali GetAdmin dipanggil.
	panggilGet int

	halaman AdminPage
	detail  AdminDetail
	getErr  error
	listErr error

	// catatan merekam panggilan SetNoteAdmin.
	noteID        string
	noteIsi       string
	noteActor     string
	noteKind      string
	noteTeks      string
	noteDipanggil bool
}

func (f *adminRepoFake) ListAdmin(_ context.Context, q AdminQuery) (AdminPage, error) {
	f.panggilList++
	f.terakhir = q
	return f.halaman, f.listErr
}

func (f *adminRepoFake) GetAdmin(_ context.Context, _ string, _ []string) (AdminDetail, error) {
	f.panggilGet++
	return f.detail, f.getErr
}

func (f *adminRepoFake) SetNoteAdmin(_ context.Context, id, note, actorID, actorKind, teksEvent string) error {
	f.noteDipanggil = true
	f.noteID = id
	f.noteIsi = note
	f.noteActor = actorID
	f.noteKind = actorKind
	f.noteTeks = teksEvent
	return f.getErr
}

// sumberGagalFake meniru domain notification.
type sumberGagalFake struct {
	ids []string
	err error
}

func (s sumberGagalFake) PesananEmailGagal(context.Context) ([]string, error) {
	return s.ids, s.err
}

func aktorAdmin(role auth.Role) auth.Actor {
	return auth.Actor{UserID: "adm-1", Kind: auth.KindAdmin, Role: role, Nama: "Rina Kartika"}
}

func TestAdminListOtorisasi(t *testing.T) {
	repo := &adminRepoFake{}
	svc := NewAdminService(repo)

	// Akun cabang tidak boleh melihat riwayat seluruh cabang (PRD §2.1).
	if _, err := svc.List(context.Background(), aktorCabang("br-a"), AdminFilter{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cabang harus ditolak 403, dapat %v", err)
	}
	if _, err := svc.Get(context.Background(), aktorCabang("br-a"), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cabang harus ditolak di detail, dapat %v", err)
	}
	if _, err := svc.SetNote(context.Background(), aktorCabang("br-a"), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cabang harus ditolak di catatan, dapat %v", err)
	}
	if repo.panggilList != 0 || repo.panggilGet != 0 || repo.noteDipanggil {
		t.Fatal("repository tidak boleh disentuh sebelum otorisasi lolos")
	}

	// Manager DAN Superadmin boleh (PRD §2.1: riwayat pemesanan semua cabang).
	for _, role := range []auth.Role{auth.RoleManager, auth.RoleSuperadmin} {
		if _, err := svc.List(context.Background(), aktorAdmin(role), AdminFilter{}); err != nil {
			t.Fatalf("admin %s harus boleh melihat riwayat, dapat %v", role, err)
		}
	}
}

func TestAdminListNormalisasiFilter(t *testing.T) {
	repo := &adminRepoFake{}
	svc := NewAdminService(repo)

	// Bawaan: halaman 1, 25 baris, offset 0, tanpa grup.
	if _, err := svc.List(context.Background(), aktorAdmin(auth.RoleManager), AdminFilter{}); err != nil {
		t.Fatalf("daftar bawaan: %v", err)
	}
	if repo.terakhir.Limit != DefaultPerHalaman || repo.terakhir.Offset != 0 || repo.terakhir.Grup != "" {
		t.Fatalf("bawaan paginasi salah: %+v", repo.terakhir)
	}

	// Halaman 3 dengan 50 baris -> offset 100; nilai "semua"/"tanpa" berarti
	// tanpa filter.
	page, err := svc.List(context.Background(), aktorAdmin(auth.RoleSuperadmin), AdminFilter{
		Status: "semua", VerifStatus: "semua", Grup: "tanpa", Halaman: 3, PerHalaman: 50,
	})
	if err != nil {
		t.Fatalf("daftar: %v", err)
	}
	if repo.terakhir.Offset != 100 || repo.terakhir.Limit != 50 || repo.terakhir.Grup != "" {
		t.Fatalf("paginasi/grup salah: %+v", repo.terakhir)
	}
	if repo.terakhir.Status != "" || repo.terakhir.VerifStatus != "" {
		t.Fatalf("nilai 'semua' harus berarti tanpa filter: %+v", repo.terakhir)
	}
	// Halaman yang dikembalikan dihitung dari offset.
	if page.Halaman != 3 || page.PerHalaman != 50 {
		t.Fatalf("metadata halaman salah: %+v", page)
	}

	// per_halaman dipagari 100 supaya riwayat besar tidak diunduh sekaligus.
	if _, err := svc.List(context.Background(), aktorAdmin(auth.RoleManager), AdminFilter{PerHalaman: 5000}); err != nil {
		t.Fatalf("daftar: %v", err)
	}
	if repo.terakhir.Limit != MaxPerHalaman {
		t.Fatalf("per_halaman harus dipagari %d, dapat %d", MaxPerHalaman, repo.terakhir.Limit)
	}

	// Filter tidak valid ditolak sebagai ErrAdminFilter (bukan diabaikan).
	kasus := []AdminFilter{
		{Status: "batal"},
		{VerifStatus: "entah"},
		{Grup: "kuartal"},
		{Dari: "08-10-2026"},
		{Sampai: "2026-13-01"},
		{Dari: "2026-10-08", Sampai: "2026-10-01"},
		{BranchID: "bukan-uuid"},
	}
	for i, f := range kasus {
		if _, err := svc.List(context.Background(), aktorAdmin(auth.RoleManager), f); !errors.Is(err, ErrAdminFilter) {
			t.Fatalf("kasus %d harus ErrAdminFilter, dapat %v", i, err)
		}
	}

	// Tanggal sah diteruskan sebagai batas inklusif.
	if _, err := svc.List(context.Background(), aktorAdmin(auth.RoleManager), AdminFilter{
		Dari: "2026-10-01", Sampai: "2026-10-31", Status: "done", VerifStatus: "cocok", Grup: "bulan",
	}); err != nil {
		t.Fatalf("daftar: %v", err)
	}
	if repo.terakhir.Dari == nil || repo.terakhir.Sampai == nil {
		t.Fatalf("batas tanggal tidak diteruskan: %+v", repo.terakhir)
	}
	if repo.terakhir.Dari.Format(TanggalLayout) != "2026-10-01" || repo.terakhir.Sampai.Format(TanggalLayout) != "2026-10-31" {
		t.Fatalf("batas tanggal salah: %+v", repo.terakhir)
	}
	if repo.terakhir.Grup != GrupBulan || repo.terakhir.Status != StatusDone || repo.terakhir.VerifStatus != "cocok" {
		t.Fatalf("filter salah: %+v", repo.terakhir)
	}
}

func TestAdminListEmailGagalDiserahkanDomainNotification(t *testing.T) {
	repo := &adminRepoFake{}
	svc := NewAdminService(repo, WithEmailGagal(sumberGagalFake{ids: []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}))
	if _, err := svc.List(context.Background(), aktorAdmin(auth.RoleManager), AdminFilter{EmailGagal: boolPtr(true)}); err != nil {
		t.Fatalf("daftar: %v", err)
	}
	if len(repo.terakhir.EmailGagalIDs) != 1 {
		t.Fatalf("id email gagal tidak diteruskan: %+v", repo.terakhir.EmailGagalIDs)
	}
	if repo.terakhir.EmailGagal == nil || !*repo.terakhir.EmailGagal {
		t.Fatal("filter email_gagal tidak diteruskan")
	}

	// Tanpa sumber terpasang hasilnya kosong (fail closed), bukan error.
	repo2 := &adminRepoFake{}
	svc2 := NewAdminService(repo2)
	if _, err := svc2.List(context.Background(), aktorAdmin(auth.RoleManager), AdminFilter{}); err != nil {
		t.Fatalf("daftar tanpa sumber: %v", err)
	}
	if repo2.terakhir.EmailGagalIDs == nil || len(repo2.terakhir.EmailGagalIDs) != 0 {
		t.Fatalf("tanpa sumber harus slice kosong: %+v", repo2.terakhir.EmailGagalIDs)
	}

	// Kegagalan sumber diteruskan sebagai error, bukan diam-diam diabaikan.
	svc3 := NewAdminService(&adminRepoFake{}, WithEmailGagal(sumberGagalFake{err: errors.New("db mati")}))
	if _, err := svc3.List(context.Background(), aktorAdmin(auth.RoleManager), AdminFilter{}); err == nil {
		t.Fatal("kegagalan sumber email gagal harus menggagalkan daftar")
	}
}

func TestAdminListRingkasanUrutStabil(t *testing.T) {
	repo := &adminRepoFake{halaman: AdminPage{
		Items: []AdminRow{{ID: "x"}},
		Total: 3,
		Ringkasan: []AdminGrup{
			{Kunci: "b", Label: "Bandung", Jumlah: 1},
			{Kunci: "a", Label: "Aceh", Jumlah: 1},
			{Kunci: "c", Label: "Cirebon", Jumlah: 1},
		},
	}}
	svc := NewAdminService(repo)
	page, err := svc.List(context.Background(), aktorAdmin(auth.RoleManager), AdminFilter{Grup: "cabang"})
	if err != nil {
		t.Fatalf("daftar: %v", err)
	}
	if page.Grup != "cabang" {
		t.Fatalf("grup harus dilaporkan: %+v", page)
	}
	if page.Ringkasan[0].Label != "Aceh" || page.Ringkasan[2].Label != "Cirebon" {
		t.Fatalf("grup cabang harus urut nama A→Z: %+v", page.Ringkasan)
	}

	repo.halaman.Ringkasan = []AdminGrup{
		{Kunci: "2026-08", Jumlah: 1},
		{Kunci: "2026-10", Jumlah: 1},
		{Kunci: "2026-09", Jumlah: 1},
	}
	page, err = svc.List(context.Background(), aktorAdmin(auth.RoleManager), AdminFilter{Grup: "bulan"})
	if err != nil {
		t.Fatalf("daftar: %v", err)
	}
	if page.Ringkasan[0].Kunci != "2026-10" || page.Ringkasan[2].Kunci != "2026-08" {
		t.Fatalf("grup bulan harus terbaru dahulu: %+v", page.Ringkasan)
	}
}

func TestAdminGetIdBukanUUIDDanKoleksiKosong(t *testing.T) {
	repo := &adminRepoFake{}
	svc := NewAdminService(repo)
	if _, err := svc.Get(context.Background(), aktorAdmin(auth.RoleManager), "tidak-ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("id bukan uuid harus 404, dapat %v", err)
	}
	if repo.panggilGet != 0 {
		t.Fatal("id bukan uuid tidak boleh menyentuh database")
	}

	repo.detail = AdminDetail{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
	d, err := svc.Get(context.Background(), aktorAdmin(auth.RoleManager), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	// Koleksi selalu array JSON, bukan null.
	if d.Lines == nil || d.Timeline == nil || d.Dokumen == nil || d.Verifikasi == nil {
		t.Fatalf("koleksi detail harus array kosong: %+v", d)
	}
}

func TestAdminSetNote(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	repo := &adminRepoFake{detail: AdminDetail{ID: id, NoteAdmin: "halo"}}
	svc := NewAdminService(repo)

	kasus := []struct {
		nama string
		id   string
		note string
		want error
	}{
		{"id bukan uuid", "x", "catatan", ErrNotFound},
		{"catatan kosong", id, "   ", ErrInvalidData},
		{"catatan terlalu panjang", id, strings.Repeat("a", MaxCatatanInternal+1), ErrInvalidData},
	}
	for _, k := range kasus {
		if _, err := svc.SetNote(context.Background(), aktorAdmin(auth.RoleManager), k.id, k.note); !errors.Is(err, k.want) {
			t.Fatalf("%s: harus %v, dapat %v", k.nama, k.want, err)
		}
	}
	if repo.noteDipanggil {
		t.Fatal("input tidak valid tidak boleh menyentuh repository")
	}

	d, err := svc.SetNote(context.Background(), aktorAdmin(auth.RoleManager), id, "  hubungi cabang  ")
	if err != nil {
		t.Fatalf("simpan catatan: %v", err)
	}
	if !repo.noteDipanggil || repo.noteID != id || repo.noteIsi != "hubungi cabang" {
		t.Fatalf("catatan tidak tersimpan benar: %+v", repo)
	}
	// Jejak siapa-kapan-apa ikut ditulis ke timeline.
	if !strings.Contains(repo.noteTeks, "hubungi cabang") || !strings.Contains(repo.noteTeks, "Rina Kartika") {
		t.Fatalf("teks timeline catatan tidak memuat aktor/isi: %q", repo.noteTeks)
	}
	if repo.noteActor != "adm-1" || repo.noteKind != string(auth.KindAdmin) {
		t.Fatalf("aktor catatan salah: %q %q", repo.noteActor, repo.noteKind)
	}
	// Respons adalah detail terbaru hasil baca ulang.
	if d.ID != id {
		t.Fatalf("respons harus detail terbaru: %+v", d)
	}
}

func boolPtr(b bool) *bool { return &b }
