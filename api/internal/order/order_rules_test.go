package order

// Uji aturan Sertifikat, baris biaya packaging, dan Cash & Carry (kartu F1-12;
// PRD §3.6–3.7, §7; Database.md §4.4, §8).
//
// Fokus berkas ini adalah invarian yang HARUS berlaku dari sisi server:
// harga Sertifikat selalu 0, tepat satu baris biaya packaging dari setelan,
// klien tidak punya cara menyisipkan/mengubah/menghapus baris itu, snapshot
// biaya tidak berubah diam-diam, dan aturan C&C (flag cabang + tenggat dari
// tanggal submit WIB) dihitung server.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/settings"
)

// packagingDari mengembalikan baris packaging satu pesanan (atau ok=false).
func packagingDari(lines []Line) (Line, bool) {
	for _, l := range lines {
		if l.Jenis == JenisPackaging {
			return l, true
		}
	}
	return Line{}, false
}

// TestSertifikatMenambahTepatSatuBarisPackaging memverifikasi CP-SER-01/02:
// harga baris sertifikat 0 berapa pun qty-nya dan pesanan sertifikat mendapat
// tepat satu baris biaya packaging qty 1 dari setelan server — tanpa product_id
// dan tanpa serial number.
func TestSertifikatMenambahTepatSatuBarisPackaging(t *testing.T) {
	svc, repo := newUjiService()
	ctx := context.Background()

	o, err := svc.CreateDraft(ctx, aktorCabang("br-a"), DraftInput{
		Lines: []LineInput{{ProductID: "p3", Qty: 7}},
	})
	if err != nil {
		t.Fatalf("buat draft sertifikat: %v", err)
	}
	if len(o.Lines) != 2 {
		t.Fatalf("pesanan sertifikat harus 2 baris (produk + packaging), dapat %d: %+v", len(o.Lines), o.Lines)
	}
	produk := o.Lines[0]
	if produk.Jenis != JenisProduct || produk.HargaSatuan != 0 || produk.Subtotal != 0 || produk.Qty != 7 {
		t.Errorf("baris sertifikat harus harga 0 berapa pun qty: %+v", produk)
	}
	pack, ok := packagingDari(o.Lines)
	if !ok {
		t.Fatalf("pesanan sertifikat wajib punya baris packaging: %+v", o.Lines)
	}
	if pack.Qty != 1 || pack.HargaSatuan != 3000 || pack.Subtotal != 3000 {
		t.Errorf("baris packaging harus qty 1 senilai biaya_packaging 3000: %+v", pack)
	}
	if pack.ProductID != "" || pack.Nama != NamaBiayaPackaging || pack.SerialNumber != "" {
		t.Errorf("baris packaging tidak boleh punya produk/serial: %+v", pack)
	}
	if o.Total != 3000 || o.JumlahItem != 7 {
		t.Errorf("total harus 0×7 + 3000 = 3000 dan jumlah item 7: total=%d item=%d", o.Total, o.JumlahItem)
	}
	if !o.IsSertifikat {
		t.Errorf("pesanan sertifikat harus bertanda is_sertifikat: %+v", o)
	}
	if pack.LineNo != 2 {
		t.Errorf("baris packaging harus baris terakhir (line_no 2), dapat %d", pack.LineNo)
	}

	// Mengubah draft (editor mengirim daftar baris final) TIDAK boleh
	// menggandakan baris packaging: baris itu selalu disusun ulang server.
	ubah, err := svc.UpdateDraft(ctx, aktorCabang("br-a"), o.ID, DraftInput{
		Lines: []LineInput{{ProductID: "p3", Qty: 9}},
	})
	if err != nil {
		t.Fatalf("ubah draft sertifikat: %v", err)
	}
	if len(ubah.Lines) != 2 {
		t.Fatalf("setelah diubah tetap 2 baris, dapat %d: %+v", len(ubah.Lines), ubah.Lines)
	}
	if got := ubah.Total; got != 3000 {
		t.Errorf("total setelah ubah harus 3000 (packaging saja), dapat %d", got)
	}
	semua := repo.Lines(o.ID)
	if len(semua) != 2 {
		t.Fatalf("database in-memory harus 2 baris, dapat %d", len(semua))
	}
}

// TestSertifikatHargaSelaluNol menjaga CP-SER-01 walau data harga cabang
// menyimpang: harga yang tersimpan di branch_prices tidak pernah dipakai untuk
// produk Sertifikat.
func TestSertifikatHargaSelaluNol(t *testing.T) {
	repo := NewMemory()
	cat := fakeCatalog{perBranch: map[string][]catalog.BranchProduct{
		"br-a": {{ID: "p3", Nama: "Sertifikat", Jenis: catalog.JenisSertifikat, Harga: 7500}},
	}}
	svc := NewService(repo, cat, cfgUji())

	o, err := svc.CreateDraft(context.Background(), aktorCabang("br-a"), DraftInput{
		Lines: []LineInput{{ProductID: "p3", Qty: 3}},
	})
	if err != nil {
		t.Fatalf("buat draft: %v", err)
	}
	if o.Lines[0].HargaSatuan != 0 || o.Lines[0].Subtotal != 0 {
		t.Errorf("harga sertifikat harus dipaksa 0, dapat %+v", o.Lines[0])
	}
	if o.Total != 3000 {
		t.Errorf("total harus hanya packaging (3000), dapat %d", o.Total)
	}
}

// TestBiayaPackagingSnapshotDanPerubahanSetelan memverifikasi CP-SER-05/BR-05:
// angka packaging adalah snapshot setelan saat baris dibuat. Mengubah setelan
// tidak menyentuh baris yang sudah tersimpan, sementara pesanan berikutnya
// memakai nilai baru.
func TestBiayaPackagingSnapshotDanPerubahanSetelan(t *testing.T) {
	svc, repo, cfg := newUjiServiceCfg()
	ctx := context.Background()
	aktor := aktorCabang("br-a")

	lama, err := svc.CreateDraft(ctx, aktor, DraftInput{Lines: []LineInput{{ProductID: "p3", Qty: 1}}})
	if err != nil {
		t.Fatalf("buat draft lama: %v", err)
	}
	if pack, _ := packagingDari(lama.Lines); pack.HargaSatuan != 3000 {
		t.Fatalf("snapshot awal harus 3000, dapat %+v", pack)
	}

	// Superadmin mengubah biaya packaging (setelan).
	cfg.values[settings.BiayaPackaging] = 5000

	// Pesanan lama tidak berubah: baris tersimpan tetap 3000 di database
	// in-memory dan pada pembacaan ulang.
	tersimpan := repo.Lines(lama.ID)
	if pack, ok := packagingDari(tersimpan); !ok || pack.HargaSatuan != 3000 {
		t.Fatalf("baris lama tidak boleh ikut berubah: %+v", tersimpan)
	}
	lagi, err := svc.Get(ctx, aktor, lama.ID)
	if err != nil {
		t.Fatalf("baca pesanan lama: %v", err)
	}
	if pack, _ := packagingDari(lagi.Lines); pack.HargaSatuan != 3000 || lagi.Total != 3000 {
		t.Errorf("pembacaan ulang harus tetap 3000: %+v", lagi)
	}

	// Pesanan berikutnya memakai nilai setelan yang baru.
	baru, err := svc.CreateDraft(ctx, aktor, DraftInput{Lines: []LineInput{{ProductID: "p3", Qty: 1}}})
	if err != nil {
		t.Fatalf("buat draft baru: %v", err)
	}
	if pack, _ := packagingDari(baru.Lines); pack.HargaSatuan != 5000 || baru.Total != 5000 {
		t.Errorf("pesanan baru harus memakai 5000: %+v", baru.Lines)
	}
}

// TestKlienTidakBisaMenyisipkanBarisPackaging menegaskan CP-SER-02: satu-satunya
// bentuk baris yang diterima editor adalah product_id yang benar-benar dijual
// ke cabang, sehingga baris packaging tidak bisa ditambah, diubah, atau
// diduplikasi klien.
func TestKlienTidakBisaMenyisipkanBarisPackaging(t *testing.T) {
	svc, repo := newUjiService()
	ctx := context.Background()
	aktor := aktorCabang("br-a")

	// product_id kosong (bentuk paling dekat dengan baris packaging tanpa
	// produk) ditolak.
	if _, err := svc.CreateDraft(ctx, aktor, DraftInput{
		Lines: []LineInput{{ProductID: "", Qty: 1}},
	}); !errors.Is(err, ErrInvalidData) {
		t.Errorf("baris tanpa produk harus ditolak, dapat %v", err)
	}
	// id yang bukan produk yang dijual cabang juga ditolak (mis. id karangan).
	if _, err := svc.CreateDraft(ctx, aktor, DraftInput{
		Lines: []LineInput{{ProductID: "packaging", Qty: 1}},
	}); !errors.Is(err, ErrProductNotSold) {
		t.Errorf("id karangan harus ditolak sebagai produk tak dijual, dapat %v", err)
	}
	// Dua baris produk Sertifikat yang sama tetap tidak boleh (baris packaging
	// pun tidak bisa dibuat dua kali lewat jalur mana pun).
	if _, err := svc.CreateDraft(ctx, aktor, DraftInput{
		Lines: []LineInput{{ProductID: "p3", Qty: 1}, {ProductID: "p3", Qty: 1}},
	}); !errors.Is(err, ErrInvalidData) {
		t.Errorf("produk sertifikat ganda harus ditolak, dapat %v", err)
	}
	if repo.Count() != 0 {
		t.Errorf("tidak boleh ada draft tersimpan, dapat %d", repo.Count())
	}
}

// TestBarisPackagingSatuUntukBeberapaProdukSertifikat menjaga CP-SER-02
// ("per pesanan", bukan per baris): beberapa produk Sertifikat tetap hanya
// mendapat satu baris biaya packaging.
func TestBarisPackagingSatuUntukBeberapaProdukSertifikat(t *testing.T) {
	repo := NewMemory()
	cat := fakeCatalog{perBranch: map[string][]catalog.BranchProduct{
		"br-a": {
			{ID: "p3", Nama: "Sertifikat A", Jenis: catalog.JenisSertifikat, Harga: 0},
			{ID: "p5", Nama: "Sertifikat B", Jenis: catalog.JenisSertifikat, Harga: 0},
		},
	}}
	svc := NewService(repo, cat, cfgUji())

	o, err := svc.CreateDraft(context.Background(), aktorCabang("br-a"), DraftInput{
		Lines: []LineInput{{ProductID: "p3", Qty: 2}, {ProductID: "p5", Qty: 4}},
	})
	if err != nil {
		t.Fatalf("buat draft: %v", err)
	}
	var jml int
	for _, l := range o.Lines {
		if l.Jenis == JenisPackaging {
			jml++
		}
	}
	if jml != 1 {
		t.Fatalf("harus tepat satu baris packaging, dapat %d: %+v", jml, o.Lines)
	}
	if o.Total != 3000 || o.JumlahItem != 6 {
		t.Errorf("total harus 3000 dan item 6, dapat %d/%d", o.Total, o.JumlahItem)
	}
}

// TestValidasiCNCFlagCabang memverifikasi CP-CNC-01: C&C hanya sah bila cabang
// berflag cash_and_carry. Flag dibaca server, jadi cabang tanpa flag tidak bisa
// memalsukan C&C.
func TestValidasiCNCFlagCabang(t *testing.T) {
	kasus := []struct {
		nama      string
		branchCNC bool
		diminta   bool
		mauErr    bool
	}{
		{"cabang C&C meminta C&C", true, true, false},
		{"cabang C&C tanpa C&C", true, false, false},
		{"cabang biasa tanpa C&C", false, false, false},
		{"cabang biasa memalsukan C&C", false, true, true},
	}
	for _, k := range kasus {
		err := ValidateCNC(k.branchCNC, k.diminta)
		if k.mauErr && !errors.Is(err, ErrCNCNotAllowed) {
			t.Errorf("%s: mau ErrCNCNotAllowed, dapat %v", k.nama, err)
		}
		if !k.mauErr && err != nil {
			t.Errorf("%s: tidak boleh gagal, dapat %v", k.nama, err)
		}
	}
}

// TestTanggalWIBLintasTengahMalam memverifikasi CP-ORD-06/BR-04: tanggal bisnis
// memakai zona Asia/Jakarta (UTC+7), bukan UTC — sehingga submit pukul 00:30
// WIB tercatat pada tanggal yang benar dan tenggat C&C-nya ikut benar.
func TestTanggalWIBLintasTengahMalam(t *testing.T) {
	kasus := []struct {
		nama string
		utc  string
		mau  string
	}{
		{"17:00 UTC sudah besok di WIB", "2026-10-08T17:00:00Z", "2026-10-09"},
		{"16:59:59 UTC masih hari ini di WIB", "2026-10-08T16:59:59Z", "2026-10-08"},
		{"tengah malam WIB", "2026-10-08T17:00:00Z", "2026-10-09"},
		{"23:30 WIB", "2026-10-08T16:30:00Z", "2026-10-08"},
		{"awal bulan", "2026-09-30T17:00:00Z", "2026-10-01"},
	}
	for _, k := range kasus {
		now, err := time.Parse(time.RFC3339, k.utc)
		if err != nil {
			t.Fatalf("waktu uji tidak valid: %v", err)
		}
		got := TanggalWIB(now)
		if got.Format(TanggalLayout) != k.mau {
			t.Errorf("%s: %s → %s, mau %s", k.nama, k.utc, got.Format(TanggalLayout), k.mau)
		}
		if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 {
			t.Errorf("%s: tanggal bisnis harus tengah malam, dapat %02d:%02d:%02d", k.nama, h, m, s)
		}
		if got.Location() != clock.Jakarta() {
			t.Errorf("%s: tanggal bisnis harus di zona WIB, dapat %v", k.nama, got.Location())
		}
		// Tenggat C&C = tanggal pesanan + cnc_hari.
		if jt := JatuhTempo(got, 7); jt.Format(TanggalLayout) != lebihHari(k.mau, 7) {
			t.Errorf("%s: jatuh tempo 7 hari salah: %s", k.nama, jt.Format(TanggalLayout))
		}
	}
}

// lebihHari menambahkan n hari pada tanggal YYYY-MM-DD (pembanding uji).
func lebihHari(tanggal string, n int) string {
	d, err := time.Parse(TanggalLayout, tanggal)
	if err != nil {
		return "tanggal-tidak-valid"
	}
	return d.AddDate(0, 0, n).Format(TanggalLayout)
}

// TestJatuhTempoLintasBulan memastikan penambahan cnc_hari tidak meleset saat
// melewati batas bulan/tahun (mis. 31 Desember + 7 hari).
func TestJatuhTempoLintasBulan(t *testing.T) {
	tanggal, err := time.Parse(TanggalLayout, "2026-12-31")
	if err != nil {
		t.Fatalf("tanggal uji: %v", err)
	}
	if got := JatuhTempo(tanggal, 7).Format(TanggalLayout); got != "2027-01-07" {
		t.Errorf("31 Des 2026 + 7 hari harus 2027-01-07, dapat %s", got)
	}
	if got := JatuhTempo(tanggal, 0).Format(TanggalLayout); got != "2026-12-31" {
		t.Errorf("cnc_hari 0 harus tetap tanggal pesanan, dapat %s", got)
	}
}
