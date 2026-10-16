package order

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/settings"
)

// fakeCatalog adalah sudut pandang katalog untuk pengujian: hanya produk yang
// punya baris branch_prices yang muncul, persis seperti query katalog cabang.
type fakeCatalog struct {
	perBranch map[string][]catalog.BranchProduct
	err       error
}

func (f fakeCatalog) BranchProducts(_ context.Context, branchID string) ([]catalog.BranchProduct, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.perBranch[branchID], nil
}

// fakeConfig adalah sudut pandang domain pengaturan untuk pengujian: nilai
// bilangan bulat per kunci yang bisa diubah di tengah uji untuk membuktikan
// snapshot biaya packaging.
type fakeConfig struct {
	values map[string]int64
}

func (f *fakeConfig) Int(_ context.Context, key string) (int64, error) {
	v, ok := f.values[key]
	if !ok {
		return 0, fmt.Errorf("kunci pengaturan %q tidak ada", key)
	}
	return v, nil
}

// cfgUji menyiapkan setelan uji dengan nilai bawaan PRD §4.9.
func cfgUji() *fakeConfig {
	return &fakeConfig{values: map[string]int64{
		settings.BiayaPackaging: 3000,
		settings.CNCHari:        7,
		settings.DigitInvoice:   2,
	}}
}

// cabangA menyiapkan katalog uji: p1 & p4 dijual ke cabang A (100000/5000), p2
// hanya punya harga_default sehingga tidak dijual ke cabang mana pun, dan p3
// adalah produk Sertifikat yang dijual ke cabang A seharga 0.
func katalogUji() fakeCatalog {
	return fakeCatalog{perBranch: map[string][]catalog.BranchProduct{
		"br-a": {
			{ID: "p1", Nama: "Bio Magnetik", Jenis: catalog.JenisReguler, Harga: 100000, Urutan: 1},
			{ID: "p3", Nama: "Sertifikat", Jenis: catalog.JenisSertifikat, Harga: 0, Urutan: 2},
			{ID: "p4", Nama: "Kertas HVS", Jenis: catalog.JenisReguler, Harga: 5000, Urutan: 3},
		},
		"br-b": {
			{ID: "p1", Nama: "Bio Magnetik", Jenis: catalog.JenisReguler, Harga: 200000, Urutan: 1},
		},
	}}
}

func aktorCabang(branchID string) auth.Actor {
	return auth.Actor{UserID: "usr-" + branchID, Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: branchID, Nama: "Cabang " + branchID}
}

func newUjiService() (*Service, *Memory) {
	repo := NewMemory()
	return NewService(repo, katalogUji(), cfgUji()), repo
}

// newUjiServiceCfg sama seperti newUjiService, tetapi juga mengembalikan
// setelannya supaya uji snapshot biaya packaging bisa mengubah nilainya.
func newUjiServiceCfg() (*Service, *Memory, *fakeConfig) {
	repo := NewMemory()
	cfg := cfgUji()
	return NewService(repo, katalogUji(), cfg), repo, cfg
}

// TestBuatDraftMemakaiHargaCabangDanTotalServer memverifikasi invarian inti
// F1-11: harga baris datang dari branch_prices cabang sesi, total dan jumlah
// item dihitung server, dan draft tidak punya nomor/tanggal/serial.
func TestBuatDraftMemakaiHargaCabangDanTotalServer(t *testing.T) {
	svc, repo := newUjiService()
	ctx := context.Background()

	o, err := svc.CreateDraft(ctx, aktorCabang("br-a"), DraftInput{
		Catatan: " dikirim pagi ",
		Lines: []LineInput{
			{ProductID: "p1", Qty: 3},
			{ProductID: "p4", Qty: 2},
		},
	})
	if err != nil {
		t.Fatalf("buat draft: %v", err)
	}
	if o.Status != StatusDraft {
		t.Errorf("status draft, dapat %q", o.Status)
	}
	if o.Nomor != "" || o.Tanggal != "" {
		t.Errorf("draft tidak boleh punya nomor/tanggal: %q %q", o.Nomor, o.Tanggal)
	}
	if o.Catatan != "dikirim pagi" {
		t.Errorf("catatan tidak dirapikan: %q", o.Catatan)
	}
	if o.Total != 310000 {
		t.Errorf("total harus 3×100000 + 2×5000 = 310000, dapat %d", o.Total)
	}
	if o.JumlahItem != 5 {
		t.Errorf("jumlah item = Σ qty baris produk = 5, dapat %d", o.JumlahItem)
	}
	if len(o.Lines) != 2 {
		t.Fatalf("harus 2 baris, dapat %d", len(o.Lines))
	}
	if o.Lines[0].HargaSatuan != 100000 || o.Lines[0].Subtotal != 300000 {
		t.Errorf("baris pertama tidak memakai harga cabang: %+v", o.Lines[0])
	}
	if o.Lines[1].HargaSatuan != 5000 || o.Lines[1].Subtotal != 10000 {
		t.Errorf("baris kedua tidak memakai harga cabang: %+v", o.Lines[1])
	}
	for _, l := range o.Lines {
		if l.SerialNumber != "" {
			t.Errorf("draft tidak boleh punya serial number: %+v", l)
		}
	}
	if repo.Count() != 1 {
		t.Errorf("harus tersimpan 1 pesanan, dapat %d", repo.Count())
	}
}

// TestHargaDariCabangSesiBukanCabangLain memastikan branch_id pemilik data
// diambil dari sesi: cabang B tidak melihat harga cabang A.
func TestHargaDariCabangSesiBukanCabangLain(t *testing.T) {
	svc, _ := newUjiService()
	ctx := context.Background()

	o, err := svc.CreateDraft(ctx, aktorCabang("br-b"), DraftInput{Lines: []LineInput{{ProductID: "p1", Qty: 2}}})
	if err != nil {
		t.Fatalf("buat draft cabang B: %v", err)
	}
	if o.Lines[0].HargaSatuan != 200000 || o.Total != 400000 {
		t.Fatalf("cabang B harus memakai harganya sendiri: %+v", o.Lines[0])
	}
}

// TestProdukTanpaHargaCabangDitolak memverifikasi kewajiban yang diwariskan QA
// F1-08: produk yang tidak punya baris branch_prices tidak bisa dipesan, dan
// harga_default tidak pernah menjadi cadangan (PRD CP-KAT-02).
func TestProdukTanpaHargaCabangDitolak(t *testing.T) {
	svc, _ := newUjiService()
	ctx := context.Background()

	// p2 hanya ada di master produk (harga_default) — tidak dijual ke cabang.
	_, err := svc.CreateDraft(ctx, aktorCabang("br-a"), DraftInput{Lines: []LineInput{{ProductID: "p2", Qty: 1}}})
	if !errors.Is(err, ErrProductNotSold) {
		t.Fatalf("produk tanpa baris harga cabang harus ditolak, dapat %v", err)
	}

	// Produk yang tidak dikenal sama saja tidak dijual.
	_, err = svc.CreateDraft(ctx, aktorCabang("br-a"), DraftInput{Lines: []LineInput{{ProductID: "p-hantu", Qty: 1}}})
	if !errors.Is(err, ErrProductNotSold) {
		t.Fatalf("produk tak dikenal harus ditolak, dapat %v", err)
	}

	// p1 dijual ke cabang A tetapi TIDAK ke cabang tanpa harga (cabang C).
	_, err = svc.CreateDraft(ctx, aktorCabang("br-c"), DraftInput{Lines: []LineInput{{ProductID: "p1", Qty: 1}}})
	if !errors.Is(err, ErrProductNotSold) {
		t.Fatalf("cabang tanpa baris harga harus ditolak, dapat %v", err)
	}
}

// TestDraftHanyaMilikPemiliknya memverifikasi isolasi cabang pada baca, ubah,
// dan hapus: semuanya 404, bukan 403, supaya keberadaan pesanan tidak bocor.
func TestDraftHanyaMilikPemiliknya(t *testing.T) {
	svc, _ := newUjiService()
	ctx := context.Background()

	draft, err := svc.CreateDraft(ctx, aktorCabang("br-a"), DraftInput{Lines: []LineInput{{ProductID: "p1", Qty: 1}}})
	if err != nil {
		t.Fatalf("buat draft: %v", err)
	}

	ubah := DraftInput{Lines: []LineInput{{ProductID: "p1", Qty: 9}}}
	if _, err := svc.Get(ctx, aktorCabang("br-b"), draft.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("baca draft cabang lain harus 404, dapat %v", err)
	}
	if _, err := svc.UpdateDraft(ctx, aktorCabang("br-b"), draft.ID, ubah); !errors.Is(err, ErrNotFound) {
		t.Errorf("ubah draft cabang lain harus 404, dapat %v", err)
	}
	if err := svc.DeleteDraft(ctx, aktorCabang("br-b"), draft.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("hapus draft cabang lain harus 404, dapat %v", err)
	}
	// Draft pemiliknya tetap utuh.
	sesudah, err := svc.Get(ctx, aktorCabang("br-a"), draft.ID)
	if err != nil {
		t.Fatalf("draft pemilik harus tetap ada: %v", err)
	}
	if sesudah.Lines[0].Qty != 1 {
		t.Errorf("draft pemilik tidak boleh ikut berubah: %+v", sesudah.Lines[0])
	}
}

// TestPesananSubmittedTidakBisaDiubahAtauDihapus menegaskan PRD CP-ORD-09 dan
// catatan kartu F1-02: penguncian pesanan non-draft ada di layer service.
func TestPesananSubmittedTidakBisaDiubahAtauDihapus(t *testing.T) {
	repo := NewMemory()
	svc := NewService(repo, katalogUji(), cfgUji())
	ctx := context.Background()

	kirim := repo.Put(Order{
		BranchID: "br-a", Status: StatusSubmitted, Nomor: "2026-10-01", Tanggal: "2026-10-08",
		Total: 100000, Lines: []Line{{LineNo: 1, Jenis: JenisProduct, ProductID: "p1", Nama: "Bio Magnetik", Qty: 1, HargaSatuan: 100000}},
	})
	selesai := repo.Put(Order{BranchID: "br-a", Status: StatusDone, Nomor: "2026-10-02", Tanggal: "2026-10-08"})

	ubah := DraftInput{Lines: []LineInput{{ProductID: "p1", Qty: 5}}}
	for _, id := range []string{kirim.ID, selesai.ID} {
		if _, err := svc.UpdateDraft(ctx, aktorCabang("br-a"), id, ubah); !errors.Is(err, ErrLocked) {
			t.Errorf("ubah pesanan %s harus ORDER_LOCKED, dapat %v", id, err)
		}
		if err := svc.DeleteDraft(ctx, aktorCabang("br-a"), id); !errors.Is(err, ErrLocked) {
			t.Errorf("hapus pesanan %s harus ORDER_LOCKED, dapat %v", id, err)
		}
	}
	if repo.Count() != 2 {
		t.Errorf("pesanan Submitted/Done tidak boleh terhapus, sisa %d", repo.Count())
	}
}

// TestDraftHanyaUntukAkunCabang menolak aktor admin dan akun tanpa cabang.
func TestDraftHanyaUntukAkunCabang(t *testing.T) {
	svc, _ := newUjiService()
	ctx := context.Background()

	admin := auth.Actor{UserID: "adm", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
	in := DraftInput{Lines: []LineInput{{ProductID: "p1", Qty: 1}}}
	if _, err := svc.CreateDraft(ctx, admin, in); !errors.Is(err, ErrForbidden) {
		t.Errorf("admin tidak boleh membuat pesanan cabang, dapat %v", err)
	}
	if _, err := svc.List(ctx, admin, Filter{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("admin tidak boleh membaca daftar pesanan cabang, dapat %v", err)
	}
	tanpaCabang := auth.Actor{UserID: "usr-x", Kind: auth.KindBranch, Role: auth.RoleBranch, Nama: "X"}
	if _, err := svc.CreateDraft(ctx, tanpaCabang, in); !errors.Is(err, ErrNoBranch) {
		t.Errorf("akun tanpa cabang dijawab BRANCH_MISSING, dapat %v", err)
	}
}

// TestSertifikatTidakBolehDicampurReguler menjaga invariant Database.md §8
// (order_lines_guard_kind) supaya tidak pernah muncul sebagai 500.
func TestSertifikatTidakBolehDicampurReguler(t *testing.T) {
	svc, _ := newUjiService()
	_, err := svc.CreateDraft(context.Background(), aktorCabang("br-a"), DraftInput{
		Lines: []LineInput{{ProductID: "p1", Qty: 1}, {ProductID: "p3", Qty: 1}},
	})
	if !errors.Is(err, ErrInvalidData) {
		t.Fatalf("campuran sertifikat + reguler harus ditolak, dapat %v", err)
	}
	if !strings.Contains(err.Error(), "terpisah") {
		t.Errorf("alasan harus menjelaskan pemisahan pesanan: %v", err)
	}

	// Sertifikat sendirian tetap boleh dan mendapat baris biaya packaging.
	if _, err := svc.CreateDraft(context.Background(), aktorCabang("br-a"), DraftInput{
		Lines: []LineInput{{ProductID: "p3", Qty: 10}},
	}); err != nil {
		t.Fatalf("sertifikat sendirian harus diterima, dapat %v", err)
	}
}

// TestValidasiIsiDraft memeriksa batas qty, catatan, jumlah baris, dan produk
// ganda yang semuanya ditolak sebelum menyentuh database.
func TestValidasiIsiDraft(t *testing.T) {
	svc, repo := newUjiService()
	ctx := context.Background()
	aktor := aktorCabang("br-a")

	kasus := []struct {
		nama string
		in   DraftInput
		mau  error
	}{
		{"tanpa baris", DraftInput{}, ErrEmptyLines},
		{"qty nol", DraftInput{Lines: []LineInput{{ProductID: "p1", Qty: 0}}}, ErrInvalidData},
		{"qty negatif", DraftInput{Lines: []LineInput{{ProductID: "p1", Qty: -2}}}, ErrInvalidData},
		{"produk ganda", DraftInput{Lines: []LineInput{{ProductID: "p1", Qty: 1}, {ProductID: "p1", Qty: 2}}}, ErrInvalidData},
		{"produk kosong", DraftInput{Lines: []LineInput{{ProductID: "  ", Qty: 1}}}, ErrInvalidData},
		{"catatan terlalu panjang", DraftInput{Catatan: strings.Repeat("a", 501), Lines: []LineInput{{ProductID: "p1", Qty: 1}}}, ErrInvalidData},
		{"terlalu banyak baris", DraftInput{Lines: banyakBaris(MaxLines + 1)}, ErrInvalidData},
	}
	for _, k := range kasus {
		if _, err := svc.CreateDraft(ctx, aktor, k.in); !errors.Is(err, k.mau) {
			t.Errorf("%s: mau %v, dapat %v", k.nama, k.mau, err)
		}
	}
	if repo.Count() != 0 {
		t.Errorf("tidak boleh ada draft tersimpan saat validasi gagal, dapat %d", repo.Count())
	}
}

// banyakBaris menyusun n baris produk unik (dipakai menguji batas MaxLines).
func banyakBaris(n int) []LineInput {
	out := make([]LineInput, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, LineInput{ProductID: "p1", Qty: 1})
	}
	return out
}

// TestUbahDraftMenggantiBarisDanMenghitungUlangTotal memverifikasi editor
// mengirim daftar baris final, bukan diff.
func TestUbahDraftMenggantiBarisDanMenghitungUlangTotal(t *testing.T) {
	svc, _ := newUjiService()
	ctx := context.Background()
	aktor := aktorCabang("br-a")

	draft, err := svc.CreateDraft(ctx, aktor, DraftInput{
		Catatan: "awal",
		Lines:   []LineInput{{ProductID: "p1", Qty: 1}},
	})
	if err != nil {
		t.Fatalf("buat draft: %v", err)
	}

	ubah, err := svc.UpdateDraft(ctx, aktor, draft.ID, DraftInput{
		Catatan: "akhir",
		Lines:   []LineInput{{ProductID: "p1", Qty: 4}},
	})
	if err != nil {
		t.Fatalf("ubah draft: %v", err)
	}
	if ubah.Catatan != "akhir" || ubah.Total != 400000 || ubah.JumlahItem != 4 {
		t.Fatalf("hasil ubah salah: %+v", ubah)
	}
	if len(ubah.Lines) != 1 || ubah.Lines[0].Qty != 4 {
		t.Fatalf("baris harus digantikan: %+v", ubah.Lines)
	}
	if ubah.Nomor != "" || ubah.Tanggal != "" {
		t.Errorf("draft tetap tanpa nomor/tanggal setelah diubah: %+v", ubah)
	}
}

// TestHapusDraftMilikSendiri memverifikasi hapus draft berhasil dan hilang.
func TestHapusDraftMilikSendiri(t *testing.T) {
	svc, repo := newUjiService()
	ctx := context.Background()
	aktor := aktorCabang("br-a")

	draft, err := svc.CreateDraft(ctx, aktor, DraftInput{Lines: []LineInput{{ProductID: "p1", Qty: 1}}})
	if err != nil {
		t.Fatalf("buat draft: %v", err)
	}
	if err := svc.DeleteDraft(ctx, aktor, draft.ID); err != nil {
		t.Fatalf("hapus draft: %v", err)
	}
	if _, err := svc.Get(ctx, aktor, draft.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("draft harus hilang setelah dihapus, dapat %v", err)
	}
	if repo.Count() != 0 {
		t.Errorf("harus 0 pesanan tersisa, dapat %d", repo.Count())
	}
}

// TestListMemfilterStatusDanCabang memeriksa daftar pesanan cabang sesi.
func TestListMemfilterStatusDanCabang(t *testing.T) {
	repo := NewMemory()
	svc := NewService(repo, katalogUji(), cfgUji())
	ctx := context.Background()

	repo.Put(Order{ID: "d-a", BranchID: "br-a", Status: StatusDraft, Lines: []Line{{Jenis: JenisProduct, Qty: 2, HargaSatuan: 1000}}})
	repo.Put(Order{ID: "s-a", BranchID: "br-a", Status: StatusSubmitted, Nomor: "2026-10-01", Tanggal: "2026-10-08"})
	repo.Put(Order{ID: "d-b", BranchID: "br-b", Status: StatusDraft})

	semua, err := svc.List(ctx, aktorCabang("br-a"), Filter{})
	if err != nil {
		t.Fatalf("daftar semua: %v", err)
	}
	if len(semua) != 2 {
		t.Fatalf("cabang A harus punya 2 pesanan, dapat %d", len(semua))
	}
	for _, o := range semua {
		if o.ID == "d-b" {
			t.Error("pesanan cabang B tidak boleh muncul di daftar cabang A")
		}
	}

	draft, err := svc.List(ctx, aktorCabang("br-a"), Filter{Status: "draft"})
	if err != nil || len(draft) != 1 || draft[0].ID != "d-a" {
		t.Fatalf("filter draft salah: %v %+v", err, draft)
	}
	if got, err := svc.List(ctx, aktorCabang("br-a"), Filter{Status: "semua"}); err != nil || len(got) != 2 {
		t.Fatalf("status 'semua' harus sama dengan tanpa filter: %v %d", err, len(got))
	}
	if _, err := svc.List(ctx, aktorCabang("br-a"), Filter{Status: "batal"}); !errors.Is(err, ErrInvalidData) {
		t.Errorf("status tak dikenal harus ditolak (tidak ada status Batal), dapat %v", err)
	}
}

// TestIdBukanUUIDDijawab404 menjaga kontrak API: id yang jelas bukan UUID
// tidak boleh berakhir sebagai kesalahan 500 dari PostgreSQL.
func TestIdBukanUUIDDijawab404(t *testing.T) {
	svc, _ := newUjiService()
	ctx := context.Background()

	for _, id := range []string{"tidak-ada", "1", "", "12ab"} {
		if _, err := svc.Get(ctx, aktorCabang("br-a"), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("id %q harus 404, dapat %v", id, err)
		}
	}
}
