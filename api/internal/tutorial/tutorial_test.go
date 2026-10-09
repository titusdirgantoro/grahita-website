package tutorial_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/tutorial"
)

// repoUji adalah repository in-memory: cukup untuk menguji orkestrasi,
// validasi, sanitasi, dan audit tanpa PostgreSQL (integrasi nyata ada di
// internal/httpapi/integration_tutorial_pg_test.go).
type repoUji struct {
	topik    []tutorial.Topic
	urutID   int
	panggil  int
	aktorID  string
	tambah   []tutorial.Topic
	ubah     []tutorial.Topic
	hapus    []string
	recs     []audit.Entry
	listErr  error
	applyErr error
}

func (r *repoUji) List(context.Context) ([]tutorial.Topic, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]tutorial.Topic(nil), r.topik...), nil
}

func (r *repoUji) Apply(_ context.Context, actorID string, tambah, ubah []tutorial.Topic, hapus []string, recs []audit.Entry) error {
	if r.applyErr != nil {
		return r.applyErr
	}
	r.panggil++
	r.aktorID = actorID
	r.tambah, r.ubah, r.hapus, r.recs = tambah, ubah, hapus, recs

	buang := map[string]bool{}
	for _, id := range hapus {
		buang[id] = true
	}
	ganti := map[string]tutorial.Topic{}
	for _, t := range ubah {
		ganti[t.ID] = t
	}
	akhir := make([]tutorial.Topic, 0, len(r.topik)+len(tambah))
	for _, t := range r.topik {
		if buang[t.ID] {
			continue
		}
		if g, ok := ganti[t.ID]; ok {
			akhir = append(akhir, g)
			continue
		}
		akhir = append(akhir, t)
	}
	for _, t := range tambah {
		r.urutID++
		t.ID = fmt.Sprintf("id-baru-%d", r.urutID)
		akhir = append(akhir, t)
	}
	sort.SliceStable(akhir, func(i, j int) bool { return akhir[i].Urutan < akhir[j].Urutan })
	r.topik = akhir
	return nil
}

var (
	cabang     = auth.Actor{UserID: "u-cabang", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "b1"}
	manager    = auth.Actor{UserID: "u-manager", Kind: auth.KindAdmin, Role: auth.RoleManager}
	superadmin = auth.Actor{UserID: "u-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
)

func topikAwal() []tutorial.Topic {
	return []tutorial.Topic{
		{ID: "11111111-1111-1111-1111-111111111111", Ikon: tutorial.IkonMemesan, Judul: "Cara memesan", Ringkas: "Ringkas memesan.", Langkah: []string{"Langkah satu."}, Urutan: 10},
		{ID: "22222222-2222-2222-2222-222222222222", Ikon: tutorial.IkonBukti, Judul: "Cara unggah bukti transfer", Ringkas: "Ringkas bukti.", Langkah: []string{"Langkah bukti."}, Urutan: 20},
	}
}

// ---------- sanitasi ----------

func TestSanitizeMembuangMarkup(t *testing.T) {
	kasus := []struct {
		nama    string
		masukan string
		ingin   string
	}{
		{"tag script beserta isinya", "<script>alert('xss')</script>Halo cabang", "Halo cabang"},
		{"tag style beserta isinya", "<style>body{display:none}</style>Konten", "Konten"},
		{"atribut event", "Halo <img src=x onerror=alert(1)> dunia", "Halo dunia"},
		{"penanda format", "Kirim <b>pesanan</b> hari ini", "Kirim pesanan hari ini"},
		{"komentar html", "Halo <!-- sembunyi --> cabang", "Halo cabang"},
		{"tautan javascript", `<a href="javascript:alert(1)">klik</a> di sini`, "klik di sini"},
		{"tag bersarang", "<<script>>alert(1)<<//script>>", ""},
		{"script huruf besar", "<SCRIPT>alert(1)</SCRIPT>aman", "aman"},
		{"huruf besar tanpa penutup", "aman<SCRIPT>alert(1)", "aman"},
		{"svg inline", "<svg onload=alert(1)></svg>Hati-hati", "Hati-hati"},
		{"bukan tag", "harga kurang dari 5000", "harga kurang dari 5000"},
		{"karakter kendali", "Halo\x00\x07Dunia", "Halo Dunia"},
		{"baris baru jadi spasi", "Kirim\npesanan\tsekarang", "Kirim pesanan sekarang"},
		{"spasi berlebih", "  Halo    cabang  ", "Halo cabang"},
		{"teks biasa utuh", "Pilih “Buat pesanan” — lalu isi qty.", "Pilih “Buat pesanan” — lalu isi qty."},
		{"angka dan tanda baca", "Maksimal 5 MB (JPG/PNG/PDF); Rp0.", "Maksimal 5 MB (JPG/PNG/PDF); Rp0."},
		{"kosong", "   ", ""},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			if dapat := tutorial.Sanitize(k.masukan); dapat != k.ingin {
				t.Errorf("Sanitize(%q) = %q, ingin %q", k.masukan, dapat, k.ingin)
			}
		})
	}
}

// TestSanitizeHasilBebasMarkup memastikan invarian utama: keluaran sanitasi
// tidak pernah memuat karakter markup, apa pun masukannya.
func TestSanitizeHasilBebasMarkup(t *testing.T) {
	masukan := []string{
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"a < b > c",
		"<svg/onload=alert(1)>",
		"<iframe src='//jahat'></iframe>aman",
		"&lt;script&gt;alert(1)&lt;/script&gt;",
		"<scr<script>ipt>alert(1)",
	}
	for _, in := range masukan {
		dapat := tutorial.Sanitize(in)
		if strings.ContainsAny(dapat, "<>") {
			t.Errorf("Sanitize(%q) = %q masih memuat karakter markup", in, dapat)
		}
	}
}

// ---------- validasi ----------

func TestCheckTopik(t *testing.T) {
	panjangJudul := strings.Repeat("a", tutorial.MaxJudul+1)
	panjangLangkah := strings.Repeat("b", tutorial.MaxLangkahTeks+1)
	banyakLangkah := make([]string, tutorial.MaxLangkah+1)
	for i := range banyakLangkah {
		banyakLangkah[i] = fmt.Sprintf("langkah %d", i)
	}

	kasus := []struct {
		nama    string
		masukan tutorial.Input
		fields  []string
	}{
		{
			nama: "valid dan tersanitasi",
			masukan: tutorial.Input{
				Ikon: tutorial.IkonCNC, Judul: " <b>Pesanan</b> Cash & Carry ",
				Ringkas: "Ringkas.", Langkah: []string{" Satu ", "<script>x</script>Dua", ""},
			},
			fields: nil,
		},
		{
			nama:    "ikon tidak dikenal",
			masukan: tutorial.Input{Ikon: "<svg>", Judul: "Judul", Ringkas: "Ringkas.", Langkah: []string{"Satu"}},
			fields:  []string{"ikon"},
		},
		{
			nama:    "judul kosong setelah sanitasi",
			masukan: tutorial.Input{Ikon: tutorial.IkonUmum, Judul: "<br>", Ringkas: "Ringkas.", Langkah: []string{"Satu"}},
			fields:  []string{"judul"},
		},
		{
			nama:    "judul terlalu panjang",
			masukan: tutorial.Input{Ikon: tutorial.IkonUmum, Judul: panjangJudul, Ringkas: "Ringkas.", Langkah: []string{"Satu"}},
			fields:  []string{"judul"},
		},
		{
			nama:    "ringkasan kosong",
			masukan: tutorial.Input{Ikon: tutorial.IkonUmum, Judul: "Judul", Langkah: []string{"Satu"}},
			fields:  []string{"ringkas"},
		},
		{
			nama:    "langkah kosong",
			masukan: tutorial.Input{Ikon: tutorial.IkonUmum, Judul: "Judul", Ringkas: "Ringkas.", Langkah: []string{"  ", "<i></i>"}},
			fields:  []string{"langkah"},
		},
		{
			nama:    "langkah terlalu panjang",
			masukan: tutorial.Input{Ikon: tutorial.IkonUmum, Judul: "Judul", Ringkas: "Ringkas.", Langkah: []string{panjangLangkah}},
			fields:  []string{"langkah[0]"},
		},
		{
			nama:    "terlalu banyak langkah",
			masukan: tutorial.Input{Ikon: tutorial.IkonUmum, Judul: "Judul", Ringkas: "Ringkas.", Langkah: banyakLangkah},
			fields:  []string{"langkah"},
		},
		{
			nama:    "id bukan uuid",
			masukan: tutorial.Input{ID: "bukan-uuid", Ikon: tutorial.IkonUmum, Judul: "Judul", Ringkas: "Ringkas.", Langkah: []string{"Satu"}},
			fields:  []string{"id"},
		},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			topik, fields := tutorial.Check(k.masukan)
			if len(k.fields) == 0 {
				if len(fields) != 0 {
					t.Fatalf("topik seharusnya valid, dapat %v", fields)
				}
				if topik.Ikon != k.masukan.Ikon {
					t.Errorf("ikon = %q, ingin %q", topik.Ikon, k.masukan.Ikon)
				}
				return
			}
			for _, f := range k.fields {
				if _, ok := fields[f]; !ok {
					t.Errorf("field %q tidak dilaporkan; dapat %v", f, fields)
				}
			}
		})
	}

	// Bentuk kanonik: langkah kosong dibuang, markup hilang.
	topik, fields := tutorial.Check(tutorial.Input{
		Ikon: tutorial.IkonCNC, Judul: " <b>Pesanan</b> Cash & Carry ",
		Ringkas: "Ringkas.", Langkah: []string{" Satu ", "<script>x</script>Dua", ""},
	})
	if len(fields) != 0 {
		t.Fatalf("topik seharusnya valid, dapat %v", fields)
	}
	if topik.Judul != "Pesanan Cash & Carry" {
		t.Errorf("judul kanonik = %q", topik.Judul)
	}
	if len(topik.Langkah) != 2 || topik.Langkah[0] != "Satu" || topik.Langkah[1] != "Dua" {
		t.Errorf("langkah kanonik = %v", topik.Langkah)
	}
}

// ---------- otorisasi ----------

func TestListMenuntutSesiTidakMembukaTulis(t *testing.T) {
	repo := &repoUji{topik: topikAwal()}
	svc := tutorial.NewService(repo)

	if _, err := svc.List(context.Background(), auth.Anonymous()); !errors.Is(err, tutorial.ErrForbidden) {
		t.Fatalf("tanpa sesi harus ErrForbidden, dapat %v", err)
	}
	topik, err := svc.List(context.Background(), cabang)
	if err != nil {
		t.Fatalf("cabang membaca tutorial: %v", err)
	}
	if len(topik) != 2 {
		t.Fatalf("cabang harus melihat 2 topik, dapat %d", len(topik))
	}
	if repo.panggil != 0 {
		t.Fatalf("pembacaan tidak boleh menyentuh repo.Apply (panggil=%d)", repo.panggil)
	}
}

func TestReplaceHanyaSuperadmin(t *testing.T) {
	in := []tutorial.Input{{Ikon: tutorial.IkonUmum, Judul: "Baru", Ringkas: "Ringkas.", Langkah: []string{"Satu"}}}
	for _, aktor := range []struct {
		nama string
		a    auth.Actor
	}{{"cabang", cabang}, {"manager", manager}, {"anonim", auth.Anonymous()}} {
		repo := &repoUji{}
		svc := tutorial.NewService(repo)
		if _, err := svc.Replace(context.Background(), aktor.a, in); !errors.Is(err, tutorial.ErrForbidden) {
			t.Fatalf("%s: harus ErrForbidden, dapat %v", aktor.nama, err)
		}
		if repo.panggil != 0 {
			t.Fatalf("%s: tidak boleh ada penulisan (panggil=%d)", aktor.nama, repo.panggil)
		}
	}
}

// ---------- penggantian konten ----------

func TestReplaceTambahUbahHapusBesertaAudit(t *testing.T) {
	lama := topikAwal()
	repo := &repoUji{topik: lama}
	svc := tutorial.NewService(repo)

	in := []tutorial.Input{
		{ID: lama[1].ID, Ikon: tutorial.IkonBukti, Judul: "Cara unggah bukti transfer",
			Ringkas: "Ringkas bukti baru.", Langkah: []string{"Langkah bukti."}}, // berubah (ringkas)
		{Ikon: tutorial.IkonCNC, Judul: "Pesanan Cash & Carry", Ringkas: "Ringkas cnc.",
			Langkah: []string{"Langkah cnc."}}, // baru
	}
	topik, err := svc.Replace(context.Background(), superadmin, in)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if repo.aktorID != superadmin.UserID {
		t.Errorf("aktor audit = %q, ingin %q", repo.aktorID, superadmin.UserID)
	}
	if len(repo.tambah) != 1 || len(repo.ubah) != 1 || len(repo.hapus) != 1 {
		t.Fatalf("args salah: tambah=%d ubah=%d hapus=%d", len(repo.tambah), len(repo.ubah), len(repo.hapus))
	}
	if repo.hapus[0] != lama[0].ID {
		t.Errorf("topik yang hilang dari daftar harus dihapus: %v", repo.hapus)
	}
	if repo.tambah[0].Judul != "Pesanan Cash & Carry" || repo.ubah[0].Ringkas != "Ringkas bukti baru." {
		t.Errorf("isi args salah: %+v / %+v", repo.tambah, repo.ubah)
	}
	// Urutan tampil ditentukan server dari posisi array.
	if repo.ubah[0].Urutan != 10 || repo.tambah[0].Urutan != 20 {
		t.Errorf("urutan = %d/%d, ingin 10/20", repo.ubah[0].Urutan, repo.tambah[0].Urutan)
	}
	if len(repo.recs) != 3 {
		t.Fatalf("audit harus 3 entri (ubah, tambah, hapus), dapat %d", len(repo.recs))
	}
	var aksi []string
	for _, r := range repo.recs {
		if r.Entity != audit.EntityTutorial {
			t.Errorf("entity audit = %q", r.Entity)
		}
		if r.ActorID != superadmin.UserID || r.ActorKind != auth.KindAdmin {
			t.Errorf("aktor audit salah: %+v", r)
		}
		aksi = append(aksi, r.Action)
	}
	sort.Strings(aksi)
	if strings.Join(aksi, ",") != "create,delete,update" {
		t.Errorf("aksi audit = %v", aksi)
	}
	// Snapshot tidak boleh memuat field rahasia apa pun — hanya konten.
	for _, r := range repo.recs {
		if r.Action == audit.ActionUpdate {
			before, _ := r.Before.(map[string]any)
			after, _ := r.After.(map[string]any)
			if before["ringkas"] != "Ringkas bukti." || after["ringkas"] != "Ringkas bukti baru." {
				t.Errorf("snapshot before/after salah: %v -> %v", r.Before, r.After)
			}
		}
	}
	// Hasil akhir terurut sesuai array yang dikirim.
	if len(topik) != 2 || topik[0].Judul != "Cara unggah bukti transfer" || topik[1].Judul != "Pesanan Cash & Carry" {
		t.Fatalf("hasil Replace salah: %+v", topik)
	}
}

func TestReplaceTanpaPerubahanTidakMenulis(t *testing.T) {
	lama := topikAwal()
	repo := &repoUji{topik: lama}
	svc := tutorial.NewService(repo)

	in := []tutorial.Input{
		{ID: lama[0].ID, Ikon: lama[0].Ikon, Judul: lama[0].Judul, Ringkas: lama[0].Ringkas, Langkah: lama[0].Langkah},
		{ID: lama[1].ID, Ikon: lama[1].Ikon, Judul: lama[1].Judul, Ringkas: lama[1].Ringkas, Langkah: lama[1].Langkah},
	}
	if _, err := svc.Replace(context.Background(), superadmin, in); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if repo.panggil != 0 {
		t.Fatalf("mengirim ulang konten yang sama tidak boleh menulis (panggil=%d)", repo.panggil)
	}
}

func TestReplaceValidasiAllOrNothing(t *testing.T) {
	repo := &repoUji{topik: topikAwal()}
	svc := tutorial.NewService(repo)

	in := []tutorial.Input{
		{Ikon: tutorial.IkonUmum, Judul: "Judul sah", Ringkas: "Ringkas.", Langkah: []string{"Satu"}},
		{Ikon: "tidak-ada", Judul: "", Ringkas: "", Langkah: nil},
	}
	_, err := svc.Replace(context.Background(), superadmin, in)
	if !errors.Is(err, tutorial.ErrInvalidContent) {
		t.Fatalf("harus ErrInvalidContent, dapat %v", err)
	}
	var ve *tutorial.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error harus *ValidationError, dapat %T", err)
	}
	for _, f := range []string{"topik[1].ikon", "topik[1].judul", "topik[1].ringkas", "topik[1].langkah"} {
		if _, ok := ve.Fields[f]; !ok {
			t.Errorf("field %q tidak dilaporkan; dapat %v", f, ve.Fields)
		}
	}
	if repo.panggil != 0 {
		t.Fatalf("konten tidak valid tidak boleh disimpan (panggil=%d)", repo.panggil)
	}
}

func TestReplaceMenolakTopikDenganIdTidakDikenal(t *testing.T) {
	repo := &repoUji{topik: topikAwal()}
	svc := tutorial.NewService(repo)

	in := []tutorial.Input{
		{ID: "33333333-3333-3333-3333-333333333333", Ikon: tutorial.IkonUmum, Judul: "Judul", Ringkas: "Ringkas.", Langkah: []string{"Satu"}},
	}
	_, err := svc.Replace(context.Background(), superadmin, in)
	if !errors.Is(err, tutorial.ErrInvalidContent) {
		t.Fatalf("harus ErrInvalidContent, dapat %v", err)
	}
	if repo.panggil != 0 {
		t.Fatalf("id tidak dikenal tidak boleh menulis apa pun (panggil=%d)", repo.panggil)
	}
}

func TestReplaceBatasJumlahTopik(t *testing.T) {
	repo := &repoUji{}
	svc := tutorial.NewService(repo)

	if _, err := svc.Replace(context.Background(), superadmin, nil); !errors.Is(err, tutorial.ErrInvalidContent) {
		t.Fatalf("daftar kosong harus ditolak, dapat %v", err)
	}
	banyak := make([]tutorial.Input, tutorial.MaxTopik+1)
	for i := range banyak {
		banyak[i] = tutorial.Input{Ikon: tutorial.IkonUmum, Judul: fmt.Sprintf("Topik %d", i), Ringkas: "Ringkas.", Langkah: []string{"Satu"}}
	}
	if _, err := svc.Replace(context.Background(), superadmin, banyak); !errors.Is(err, tutorial.ErrInvalidContent) {
		t.Fatalf("melebihi batas topik harus ditolak, dapat %v", err)
	}
	if repo.panggil != 0 {
		t.Fatalf("tidak boleh ada penulisan (panggil=%d)", repo.panggil)
	}
}

// TestReplaceMenyanitasiKonten memastikan yang tersimpan adalah bentuk
// kanonik: markup hilang, ikon tetap salah satu kunci bawaan.
func TestReplaceMenyanitasiKonten(t *testing.T) {
	repo := &repoUji{}
	svc := tutorial.NewService(repo)

	_, err := svc.Replace(context.Background(), superadmin, []tutorial.Input{{
		Ikon:    tutorial.IkonMemesan,
		Judul:   "Cara <b>memesan</b><script>alert(1)</script>",
		Ringkas: "<img src=x onerror=alert(1)>Panduan singkat.",
		Langkah: []string{"Klik <a href='javascript:alert(1)'>di sini</a>", "<style>p{}</style>Selesai"},
	}})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if len(repo.tambah) != 1 {
		t.Fatalf("harus ada 1 topik baru, dapat %d", len(repo.tambah))
	}
	t0 := repo.tambah[0]
	if t0.Judul != "Cara memesan" {
		t.Errorf("judul tersimpan = %q", t0.Judul)
	}
	if t0.Ringkas != "Panduan singkat." {
		t.Errorf("ringkasan tersimpan = %q", t0.Ringkas)
	}
	for _, l := range t0.Langkah {
		if strings.ContainsAny(l, "<>") {
			t.Errorf("langkah tersimpan masih memuat markup: %q", l)
		}
	}
	if t0.Langkah[0] != "Klik di sini" || t0.Langkah[1] != "Selesai" {
		t.Errorf("langkah tersimpan = %v", t0.Langkah)
	}
}

// TestLangkahJSONPutarBalik menjaga bentuk jsonb kolom langkah.
func TestLangkahJSONPutarBalik(t *testing.T) {
	b, err := tutorial.LangkahJSON([]string{"Satu", "Dua"})
	if err != nil {
		t.Fatalf("LangkahJSON: %v", err)
	}
	if string(b) != `["Satu","Dua"]` {
		t.Fatalf("JSON = %s", b)
	}
	kembali, err := tutorial.LangkahDariJSON(b)
	if err != nil {
		t.Fatalf("LangkahDariJSON: %v", err)
	}
	if len(kembali) != 2 || kembali[0] != "Satu" {
		t.Fatalf("hasil putar balik = %v", kembali)
	}
	if _, err := tutorial.LangkahDariJSON([]byte(`{"bukan":"array"}`)); !errors.Is(err, tutorial.ErrInvalidContent) {
		t.Fatalf("bukan array harus error domain, dapat %v", err)
	}
}
