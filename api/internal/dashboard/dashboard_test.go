package dashboard

// Uji unit domain dashboard (F1-20) memakai repository palsu: yang diuji adalah
// logika yang tidak boleh salah — rentang periode menurut zona Asia/Jakarta,
// penjagaan role (Manager tidak pernah menerima rupiah), validasi filter, dan
// pengisian nol pada deret waktu.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
)

// fakeRepo mencatat Query terakhir supaya uji bisa memeriksa rentang yang
// benar-benar dikirim ke database.
type fakeRepo struct {
	last      Query
	panggilan int

	periode   HitunganPeriode
	chip      HitunganChip
	tren      []Titik
	harian    []Titik
	peringkat []BarisPeringkat
	item      int64
	komposisi HitunganKomposisi
	produk    []BarisProduk
	// rentang mencatat setiap rentang Ringkasan yang diminta, berurutan.
	rentang [][2]string
}

func (f *fakeRepo) Ringkasan(_ context.Context, q Query) (HitunganPeriode, error) {
	f.last = q
	f.panggilan++
	f.rentang = append(f.rentang, [2]string{q.Dari.Format(LayoutTanggal), q.Sampai.Format(LayoutTanggal)})
	return f.periode, nil
}

func (f *fakeRepo) Item(_ context.Context, q Query) (int64, error) {
	f.last = q
	f.panggilan++
	return f.item, nil
}

func (f *fakeRepo) Komposisi(_ context.Context, q Query) (HitunganKomposisi, error) {
	f.last = q
	f.panggilan++
	return f.komposisi, nil
}

func (f *fakeRepo) ProdukTerlaris(_ context.Context, q Query) ([]BarisProduk, error) {
	f.last = q
	f.panggilan++
	return f.produk, nil
}

func (f *fakeRepo) Chip(_ context.Context, q Query) (HitunganChip, error) {
	f.last = q
	f.panggilan++
	return f.chip, nil
}

func (f *fakeRepo) TrenBulanan(_ context.Context, q Query) ([]Titik, error) {
	f.last = q
	f.panggilan++
	return f.tren, nil
}

func (f *fakeRepo) Harian(_ context.Context, q Query) ([]Titik, error) {
	f.last = q
	f.panggilan++
	return f.harian, nil
}

func (f *fakeRepo) PeringkatCabang(_ context.Context, q Query) ([]BarisPeringkat, error) {
	f.last = q
	f.panggilan++
	return f.peringkat, nil
}

// sumberEmailGagal adalah SumberEmailGagal palsu.
type sumberEmailGagal struct {
	ids []string
	err error
}

func (s sumberEmailGagal) PesananEmailGagal(context.Context) ([]string, error) {
	return s.ids, s.err
}

// menitWIB memudahkan menulis jam lokal WIB pada uji.
var wib = time.FixedZone("WIB", 7*60*60)

func manager() auth.Actor {
	return auth.Actor{UserID: "u-mgr", Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
}

func superadmin() auth.Actor {
	return auth.Actor{UserID: "u-sa", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin, Nama: "Rina"}
}

func branch() auth.Actor {
	return auth.Actor{UserID: "u-cab", Kind: auth.KindBranch, Role: auth.RoleBranch, BranchID: "b-1"}
}

func newSvc(repo Repository, opts ...Option) *Service {
	var c clock.Clock = clock.Fixed(time.Date(2026, time.October, 7, 10, 0, 0, 0, wib))
	opts = append(opts, WithClock(c))
	return NewService(repo, opts...)
}

func tanggalHarus(t *testing.T, asal, mau string, v time.Time) {
	t.Helper()
	if got := v.Format(LayoutTanggal); got != mau {
		t.Errorf("%s: mau %s, dapat %s", asal, mau, got)
	}
}

func TestRentangPeriodeMengikutiPrototypeDanZonaWIB(t *testing.T) {
	kasus := []struct {
		periode      string
		dari, sampai string
	}{
		{PeriodeHari, "2026-10-07", "2026-10-07"},
		// Rabu 7 Okt 2026 -> minggu mulai Senin 5 Okt.
		{PeriodeMinggu, "2026-10-05", "2026-10-07"},
		{PeriodeBulan, "2026-10-01", "2026-10-07"},
		{PeriodeTahun, "2026-01-01", "2026-10-07"},
	}
	for _, k := range kasus {
		repo := &fakeRepo{}
		svc := newSvc(repo)
		d, err := svc.Get(context.Background(), manager(), Filter{Periode: k.periode})
		if err != nil {
			t.Fatalf("periode %s: %v", k.periode, err)
		}
		tanggalHarus(t, "filter.dari "+k.periode, k.dari, mustParse(t, d.Filter.Dari))
		tanggalHarus(t, "filter.sampai "+k.periode, k.sampai, mustParse(t, d.Filter.Sampai))
		tanggalHarus(t, "repo.dari "+k.periode, k.dari, repo.last.Dari)
		tanggalHarus(t, "repo.sampai "+k.periode, k.sampai, repo.last.Sampai)
		if d.Filter.Periode != k.periode {
			t.Errorf("periode ternormalkan salah: %q", d.Filter.Periode)
		}
	}
}

func TestBatasHariBisnisAsiaJakarta(t *testing.T) {
	// 16:59:59Z masih 23:59:59 WIB tanggal 7; 17:00:00Z sudah 8 Oktober WIB.
	kasus := []struct {
		utc   time.Time
		harus string
	}{
		{time.Date(2026, time.October, 7, 16, 59, 59, 0, time.UTC), "2026-10-07"},
		{time.Date(2026, time.October, 7, 17, 0, 0, 0, time.UTC), "2026-10-08"},
	}
	for _, k := range kasus {
		repo := &fakeRepo{}
		var c clock.Clock = clock.Fixed(k.utc)
		svc := NewService(repo, WithClock(c))
		d, err := svc.Get(context.Background(), manager(), Filter{Periode: PeriodeHari})
		if err != nil {
			t.Fatalf("jam %s: %v", k.utc, err)
		}
		if d.Filter.HariIni != k.harus {
			t.Errorf("jam %s: hari_ini mau %s, dapat %s", k.utc, k.harus, d.Filter.HariIni)
		}
		tanggalHarus(t, "repo.hari_ini", k.harus, repo.last.HariIni)
	}
}

func TestManagerTidakMenerimaNilaiRupiah(t *testing.T) {
	repo := &fakeRepo{
		periode: HitunganPeriode{
			Jumlah: 4, Nilai: 900000,
			TerverifikasiJumlah: 2, TerverifikasiNilai: 400000,
			MenungguJumlah: 1, MenungguNilai: 250000,
			PerluReviewJumlah: 1, PerluReviewNilai: 250000,
		},
		tren:      []Titik{{Kunci: "2026-10", Jumlah: 4, Nilai: 900000}},
		harian:    []Titik{{Kunci: "2026-10-07", Jumlah: 4, Nilai: 900000}},
		peringkat: []BarisPeringkat{{BranchID: "b-1", Nama: "Bandung", Jumlah: 4, Nilai: 900000}},
	}
	svc := newSvc(repo)
	d, err := svc.Get(context.Background(), manager(), Filter{})
	if err != nil {
		t.Fatal(err)
	}

	if d.Ringkasan.Jumlah != 4 {
		t.Errorf("jumlah manager: %d", d.Ringkasan.Jumlah)
	}
	for nama, m := range map[string]Metrik{
		"ringkasan":     d.Ringkasan,
		"terverifikasi": d.Pembayaran.Terverifikasi,
		"menunggu":      d.Pembayaran.Menunggu,
		"perlu_review":  d.Pembayaran.PerluReview,
	} {
		if m.Nilai != nil {
			t.Errorf("%s: Manager tidak boleh menerima nilai rupiah", nama)
		}
	}
	for _, titik := range append(append([]TitikTren{}, d.TrenBulanan...), d.Harian...) {
		if titik.Nilai != nil {
			t.Errorf("titik %s: Manager tidak boleh menerima nilai rupiah", titik.Kunci)
		}
	}
	for _, p := range d.PeringkatCabang {
		if p.Nilai != nil {
			t.Errorf("peringkat %s: Manager tidak boleh menerima nilai rupiah", p.Nama)
		}
	}

	// Kunci `nilai` tidak boleh ada sama sekali pada JSON Manager.
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"nilai"`) {
		t.Errorf("respons Manager masih memuat kunci nilai: %s", raw)
	}
}

func TestSuperadminMenerimaNilaiRupiah(t *testing.T) {
	repo := &fakeRepo{
		periode:   HitunganPeriode{Jumlah: 2, Nilai: 500000},
		tren:      []Titik{{Kunci: "2026-10", Jumlah: 2, Nilai: 500000}},
		harian:    []Titik{{Kunci: "2026-10-07", Jumlah: 2, Nilai: 500000}},
		peringkat: []BarisPeringkat{{BranchID: "b-1", Nama: "Bandung", Jumlah: 2, Nilai: 500000}},
	}
	svc := newSvc(repo)
	d, err := svc.Get(context.Background(), superadmin(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Ringkasan.Nilai == nil || *d.Ringkasan.Nilai != 500000 {
		t.Errorf("Superadmin harus menerima nilai: %+v", d.Ringkasan.Nilai)
	}
	if len(d.PeringkatCabang) != 1 || d.PeringkatCabang[0].Nilai == nil || *d.PeringkatCabang[0].Nilai != 500000 {
		t.Errorf("peringkat Superadmin tanpa nilai: %+v", d.PeringkatCabang)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"nilai"`) {
		t.Error("respons Superadmin seharusnya memuat kunci nilai")
	}
}

func TestCabangDitolakDanFilterTidakDikenalDitolak(t *testing.T) {
	repo := &fakeRepo{}
	svc := newSvc(repo)

	if _, err := svc.Get(context.Background(), branch(), Filter{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("akun cabang harus 403, dapat %v", err)
	}
	if _, err := svc.Get(context.Background(), auth.Anonymous(), Filter{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("anonim harus 403, dapat %v", err)
	}

	kasus := []Filter{
		{Periode: "dekade"},
		{Periode: "HARI INI"},
		{Cabang: "bukan-uuid"},
	}
	for _, f := range kasus {
		_, err := svc.Get(context.Background(), manager(), f)
		if !errors.Is(err, ErrFilterInvalid) {
			t.Errorf("filter %+v harus ditolak, dapat %v", f, err)
		}
	}
	if repo.panggilan != 0 {
		t.Errorf("repository tidak boleh dipanggil pada permintaan yang ditolak (%d panggilan)", repo.panggilan)
	}
}

func TestFilterCabangSemuaDianggapTanpaFilter(t *testing.T) {
	for _, nilai := range []string{"", " ", "semua", "SEMUA", "all"} {
		repo := &fakeRepo{}
		svc := newSvc(repo)
		if _, err := svc.Get(context.Background(), manager(), Filter{Cabang: nilai}); err != nil {
			t.Fatalf("cabang %q: %v", nilai, err)
		}
		if repo.last.BranchID != "" {
			t.Errorf("cabang %q seharusnya tanpa filter, dapat %q", nilai, repo.last.BranchID)
		}
	}
	repo := &fakeRepo{}
	svc := newSvc(repo)
	id := "3f2a1c9e-1111-4222-8333-444455556666"
	if _, err := svc.Get(context.Background(), manager(), Filter{Cabang: id}); err != nil {
		t.Fatal(err)
	}
	if repo.last.BranchID != id {
		t.Errorf("id cabang tidak diteruskan: %q", repo.last.BranchID)
	}
}

func TestChipDenganKunciStabil(t *testing.T) {
	repo := &fakeRepo{chip: HitunganChip{Menunggu: 3, PerluReview: 1, EmailGagal: 4}}
	svc := newSvc(repo, WithEmailGagal(sumberEmailGagal{ids: []string{"a", "b"}}))
	d, err := svc.Get(context.Background(), manager(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	mau := []Chip{
		{Kunci: ChipMenunggu, Label: "menunggu verifikasi", Jumlah: 3},
		{Kunci: ChipPerluReview, Label: "perlu review", Jumlah: 1},
		{Kunci: ChipEmailGagal, Label: "email gagal", Jumlah: 4},
	}
	if len(d.Chip) != len(mau) {
		t.Fatalf("chip mau %d, dapat %d", len(mau), len(d.Chip))
	}
	for i := range mau {
		if d.Chip[i] != mau[i] {
			t.Errorf("chip[%d]: mau %+v, dapat %+v", i, mau[i], d.Chip[i])
		}
	}
	// Id email gagal dari domain notification diteruskan apa adanya.
	if len(repo.last.EmailGagalIDs) != 2 {
		t.Errorf("id email gagal tidak diteruskan: %+v", repo.last.EmailGagalIDs)
	}
}

func TestEmailGagalGagalTertutupTanpaSumber(t *testing.T) {
	repo := &fakeRepo{}
	svc := newSvc(repo)
	if _, err := svc.Get(context.Background(), manager(), Filter{}); err != nil {
		t.Fatal(err)
	}
	if repo.last.EmailGagalIDs == nil || len(repo.last.EmailGagalIDs) != 0 {
		t.Errorf("tanpa sumber, daftar email gagal harus kosong: %+v", repo.last.EmailGagalIDs)
	}
}

func TestTrenBulananDuaBelasBatangTerisiNol(t *testing.T) {
	repo := &fakeRepo{
		tren: []Titik{
			{Kunci: "2026-01", Jumlah: 2},
			{Kunci: "2026-10", Jumlah: 5},
		},
	}
	svc := newSvc(repo)
	d, err := svc.Get(context.Background(), superadmin(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.TrenBulanan) != 12 {
		t.Fatalf("tren bulanan mau 12, dapat %d", len(d.TrenBulanan))
	}
	if d.TrenBulanan[0].Kunci != "2026-01" || d.TrenBulanan[0].Jumlah != 2 {
		t.Errorf("Januari salah: %+v", d.TrenBulanan[0])
	}
	if d.TrenBulanan[0].Label != "Jan" {
		t.Errorf("label Januari: %q", d.TrenBulanan[0].Label)
	}
	if d.TrenBulanan[9].Kunci != "2026-10" || d.TrenBulanan[9].Jumlah != 5 {
		t.Errorf("Oktober salah: %+v", d.TrenBulanan[9])
	}
	if d.TrenBulanan[9].Label != "Okt" {
		t.Errorf("label Oktober: %q", d.TrenBulanan[9].Label)
	}
	if d.TrenBulanan[11].Jumlah != 0 || d.TrenBulanan[11].Kunci != "2026-12" {
		t.Errorf("Desember harus nol: %+v", d.TrenBulanan[11])
	}
	// Rentang tahun bisnis penuh dikirim ke repository.
	tanggalHarus(t, "tahun_awal", "2026-01-01", repo.last.TahunAwal)
	tanggalHarus(t, "tahun_akhir", "2026-12-31", repo.last.TahunAkhir)
}

func TestHarianSampaiHariIniTerisiNol(t *testing.T) {
	repo := &fakeRepo{harian: []Titik{{Kunci: "2026-10-07", Jumlah: 3}}}
	svc := newSvc(repo)
	d, err := svc.Get(context.Background(), manager(), Filter{Periode: PeriodeTahun})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Harian) != 7 {
		t.Fatalf("harian 1..7 Oktober mau 7 titik, dapat %d", len(d.Harian))
	}
	if d.Harian[0].Kunci != "2026-10-01" || d.Harian[0].Jumlah != 0 || d.Harian[0].Label != "1" {
		t.Errorf("titik pertama salah: %+v", d.Harian[0])
	}
	if d.Harian[6].Jumlah != 3 || d.Harian[6].Label != "7" {
		t.Errorf("titik terakhir salah: %+v", d.Harian[6])
	}
	// Harian selalu bulan berjalan, walaupun periode yang dipilih tahun.
	tanggalHarus(t, "bulan_awal", "2026-10-01", repo.last.BulanAwal)
	tanggalHarus(t, "bulan_akhir", "2026-10-07", repo.last.BulanAkhir)
}

func TestInfoFilterMengembalikanKondisiYangDipakai(t *testing.T) {
	repo := &fakeRepo{}
	svc := newSvc(repo)
	id := "3f2a1c9e-1111-4222-8333-444455556666"
	d, err := svc.Get(context.Background(), superadmin(), Filter{Cabang: id, Periode: PeriodeMinggu})
	if err != nil {
		t.Fatal(err)
	}
	if d.Filter.Cabang != id || d.Filter.Periode != PeriodeMinggu || d.Filter.Zona != "Asia/Jakarta" {
		t.Errorf("info filter salah: %+v", d.Filter)
	}
	if d.Filter.Dari != "2026-10-05" || d.Filter.Sampai != "2026-10-07" || d.Filter.HariIni != "2026-10-07" {
		t.Errorf("rentang info filter salah: %+v", d.Filter)
	}
}

func mustParse(t *testing.T, v string) time.Time {
	t.Helper()
	parsed, err := time.Parse(LayoutTanggal, v)
	if err != nil {
		t.Fatalf("tanggal %q: %v", v, err)
	}
	return parsed
}

// TestPembandingMengikutiPrevRangePrototype mengunci rentang pembanding tiap
// periode (prototype `prevRange`) beserta pembanding grafik: bulan lalu penuh
// dan bulan lalu sampai tanggal yang sama — termasuk tanggal 31 yang tidak ada
// di bulan sebelumnya dan pergantian tahun.
func TestPembandingMengikutiPrevRangePrototype(t *testing.T) {
	kasus := []struct {
		nama, hari, periode         string
		pembanding                  [2]string
		bulanLaluPenuh, bulanLaluSD [2]string
	}{
		{"hari", "2026-10-07", PeriodeHari, [2]string{"2026-10-06", "2026-10-06"}, [2]string{"2026-09-01", "2026-09-30"}, [2]string{"2026-09-01", "2026-09-07"}},
		{"minggu", "2026-10-07", PeriodeMinggu, [2]string{"2026-09-28", "2026-09-30"}, [2]string{"2026-09-01", "2026-09-30"}, [2]string{"2026-09-01", "2026-09-07"}},
		{"bulan 31 ke Februari", "2026-03-31", PeriodeBulan, [2]string{"2026-02-01", "2026-02-28"}, [2]string{"2026-02-01", "2026-02-28"}, [2]string{"2026-02-01", "2026-02-28"}},
		{"tahun", "2026-10-07", PeriodeTahun, [2]string{"2025-01-01", "2025-10-07"}, [2]string{"2026-09-01", "2026-09-30"}, [2]string{"2026-09-01", "2026-09-07"}},
		{"Januari ke Desember tahun lalu", "2026-01-15", PeriodeBulan, [2]string{"2025-12-01", "2025-12-15"}, [2]string{"2025-12-01", "2025-12-31"}, [2]string{"2025-12-01", "2025-12-15"}},
	}
	for _, k := range kasus {
		hari, err := time.ParseInLocation(LayoutTanggal, k.hari, wib)
		if err != nil {
			t.Fatal(err)
		}
		repo := &fakeRepo{}
		svc := NewService(repo, WithClock(clock.Fixed(hari.Add(10*time.Hour))))
		d, err := svc.Get(context.Background(), superadmin(), Filter{Periode: k.periode})
		if err != nil {
			t.Fatalf("%s: %v", k.nama, err)
		}
		if got := [2]string{d.Pembanding.Dari, d.Pembanding.Sampai}; got != k.pembanding {
			t.Errorf("%s: pembanding mau %v, dapat %v", k.nama, k.pembanding, got)
		}
		// Urutan Ringkasan: periode terpilih, pembanding, bulan lalu penuh,
		// bulan lalu sampai tanggal yang sama.
		if len(repo.rentang) != 4 || repo.rentang[1] != k.pembanding || repo.rentang[2] != k.bulanLaluPenuh || repo.rentang[3] != k.bulanLaluSD {
			t.Errorf("%s: rentang ringkasan %v", k.nama, repo.rentang)
		}
	}
}
