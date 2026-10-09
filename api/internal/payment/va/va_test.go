package va

// Uji unit kontrak VA (kartu F3-29): potongan-potongan kontrak yang tidak
// bergantung provider — bentuk kanonik tanda tangan, verifikasi waktu-konstan,
// jendela replay, kunci idempotensi peristiwa, dan pencocokan nominal.
// Uji yang memerlukan "bank tiruan" ada di `va/vatest`.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func rahasiaUjiUnit() []byte { return []byte("rahasia-unit-va-0123456789") }

func TestKanonikBentukDanSidikBadan(t *testing.T) {
	badan := []byte(`{"nominal":150000}`)
	k := Kanonik("post", "/api/v1/va/callback", "2026-10-09T03:30:00Z", badan)
	// Metode dinormalkan ke huruf besar; sidik badan (bukan badan itu sendiri)
	// yang ditandatangani supaya perbedaan spasi/urutan kunci JSON tidak
	// mematahkan verifikasi, tetapi setiap PERUBAHAN ISI tetap terdeteksi.
	if !strings.HasPrefix(k, "POST:/api/v1/va/callback:") {
		t.Fatalf("kanonik tidak memuat metode/path: %q", k)
	}
	if strings.Contains(k, string(badan)) {
		t.Fatal("kanonik memuat badan mentah; seharusnya sidik SHA-256")
	}
	k2 := Kanonik("POST", "/api/v1/va/callback", "2026-10-09T03:30:00Z", []byte(`{"nominal":150001}`))
	if k == k2 {
		t.Fatal("kanonik tidak sensitif terhadap perubahan isi badan")
	}
	k3 := Kanonik("POST", "/api/v1/va/callback", "2026-10-09T03:30:01Z", badan)
	if k == k3 {
		t.Fatal("kanonik tidak sensitif terhadap perubahan stempel waktu")
	}
}

func TestVerifikasiTandaTanganMenolakYangTidakCocok(t *testing.T) {
	k := Kanonik("POST", "/cb", "2026-10-09T03:30:00Z", []byte(`{}`))
	sah := TandaTangan(rahasiaUjiUnit(), k)
	if err := VerifikasiTandaTangan(rahasiaUjiUnit(), k, sah); err != nil {
		t.Fatalf("tanda tangan sah ditolak: %v", err)
	}
	// Huruf besar/kecil dan spasi tepi ditoleransi (provider berbeda-beda).
	if err := VerifikasiTandaTangan(rahasiaUjiUnit(), k, "  "+strings.ToUpper(sah)+"  "); err != nil {
		t.Fatalf("normalisasi tanda tangan gagal: %v", err)
	}
	for nama, terkirim := range map[string]string{
		"kosong":       "",
		"nilai salah":  strings.Repeat("0", len(sah)),
		"lebih pendek": sah[:len(sah)-2],
		"non-hex":      strings.Repeat("z", len(sah)),
	} {
		if err := VerifikasiTandaTangan(rahasiaUjiUnit(), k, terkirim); !errors.Is(err, ErrTandaTanganTidakSah) {
			t.Fatalf("%s: ingin ErrTandaTanganTidakSah, dapat %v", nama, err)
		}
	}
	// Rahasia berbeda → ditolak (klaim: tidak ada jalur "kunci kosong=lolos").
	if err := VerifikasiTandaTangan(nil, k, sah); !errors.Is(err, ErrTandaTanganTidakSah) {
		t.Fatalf("rahasia nil: ingin ErrTandaTanganTidakSah, dapat %v", err)
	}
}

func TestVerifikasiKanonikMemerlukanHeaderLengkap(t *testing.T) {
	in := PermintaanCallback{
		Method: "POST", Path: "/cb",
		Header:       http.Header{},
		Body:         []byte(`{"nominal":1}`),
		DiterimaPada: time.Now(),
	}
	if err := VerifikasiKanonik(rahasiaUjiUnit(), in, "X-VA-Signature"); !errors.Is(err, ErrTandaTanganTidakSah) {
		t.Fatalf("tanpa header: ingin ErrTandaTanganTidakSah, dapat %v", err)
	}
	if err := VerifikasiKanonik(rahasiaUjiUnit(), in, ""); err == nil {
		t.Fatal("nama header kosong harus ditolak")
	}
	// Hanya tanda tangan tanpa stempel waktu → ditolak.
	in.Header.Set("X-VA-Signature", TandaTangan(rahasiaUjiUnit(), Kanonik("POST", "/cb", "", in.Body)))
	if err := VerifikasiKanonik(rahasiaUjiUnit(), in, "X-VA-Signature"); !errors.Is(err, ErrTandaTanganTidakSah) {
		t.Fatalf("tanpa stempel waktu: ingin ErrTandaTanganTidakSah, dapat %v", err)
	}
}

func TestPeriksaUmur(t *testing.T) {
	diterima := time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC)
	kasus := []struct {
		nama      string
		ts        string
		jendela   time.Duration
		toleransi time.Duration
		inginErr  bool
	}{
		{"tepat sekarang", diterima.Format(time.RFC3339), time.Hour, 5 * time.Minute, false},
		{"tua di dalam jendela", diterima.Add(-30 * time.Minute).Format(time.RFC3339), time.Hour, 5 * time.Minute, false},
		{"tepat di batas jendela", diterima.Add(-time.Hour).Format(time.RFC3339), time.Hour, 5 * time.Minute, false},
		{"lebih tua dari jendela", diterima.Add(-time.Hour - time.Second).Format(time.RFC3339), time.Hour, 5 * time.Minute, true},
		{"sedikit dari masa depan", diterima.Add(4 * time.Minute).Format(time.RFC3339), time.Hour, 5 * time.Minute, false},
		{"terlalu jauh dari masa depan", diterima.Add(6 * time.Minute).Format(time.RFC3339), time.Hour, 5 * time.Minute, true},
		{"rusak", "kemarin", time.Hour, 5 * time.Minute, true},
		{"tanpa waktu terima", diterima.Format(time.RFC3339), time.Hour, 5 * time.Minute, true},
	}
	for _, k := range kasus {
		pada := diterima
		if k.nama == "tanpa waktu terima" {
			pada = time.Time{}
		}
		err := PeriksaUmur(k.ts, pada, k.jendela, k.toleransi)
		if k.inginErr && !errors.Is(err, ErrCallbackKedaluwarsa) {
			t.Fatalf("%s: ingin ErrCallbackKedaluwarsa, dapat %v", k.nama, err)
		}
		if !k.inginErr && err != nil {
			t.Fatalf("%s: tidak diharapkan galat, dapat %v", k.nama, err)
		}
	}
	// Jendela 0 berarti memakai bawaan.
	if err := PeriksaUmur(time.Now().UTC().Add(-JendelaReplayBawaan-time.Minute).Format(time.RFC3339), time.Now().UTC(), 0, 0); !errors.Is(err, ErrCallbackKedaluwarsa) {
		t.Fatalf("jendela bawaan tidak dipakai: %v", err)
	}
}

func TestKunciPeristiwaStabilDanSensitif(t *testing.T) {
	waktu := time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC)
	dasar := PeristiwaBayar{IDProvider: "EV-1", Bank: BankBCA, NomorVA: "8808000000000001", Nominal: 150000, WaktuBayar: waktu}
	if dasar.Kunci() != "va:EV-1" {
		t.Fatalf("kunci memakai id provider: %q", dasar.Kunci())
	}
	if dasar.Kunci() != dasar.Kunci() {
		t.Fatal("kunci tidak stabil")
	}
	// Tanpa id provider, kunci diturunkan dari isi pembayaran.
	tanpaID := dasar
	tanpaID.IDProvider = ""
	k1 := tanpaID.Kunci()
	if k1 == "" || k1 == "va:" {
		t.Fatalf("kunci turunan kosong: %q", k1)
	}
	if tanpaID.Kunci() != k1 {
		t.Fatal("kunci turunan tidak stabil")
	}
	berbeda := tanpaID
	berbeda.Nominal = 150001
	if berbeda.Kunci() == k1 {
		t.Fatal("kunci turunan tidak sensitif terhadap nominal")
	}
	berbeda = tanpaID
	berbeda.NomorVA = "8808000000000002"
	if berbeda.Kunci() == k1 {
		t.Fatal("kunci turunan tidak sensitif terhadap nomor VA")
	}
	berbeda = tanpaID
	berbeda.WaktuBayar = waktu.Add(time.Second)
	if berbeda.Kunci() == k1 {
		t.Fatal("kunci turunan tidak sensitif terhadap waktu bayar")
	}
	// Offset zona waktu yang sama tidak mengubah kunci.
	berbeda = tanpaID
	berbeda.WaktuBayar = waktu.In(time.FixedZone("WIB", 7*3600))
	if berbeda.Kunci() != k1 {
		t.Fatal("kunci bergantung pada zona waktu")
	}
}

func TestCocokNominal(t *testing.T) {
	kasus := []struct {
		total, bayar, tolK, tolL int64
		ingin                    HasilNominal
		selisih                  int64
	}{
		{100_000, 100_000, 0, 0, NominalCocok, 0},
		{100_000, 95_000, 5_000, 0, NominalCocok, -5_000},
		{100_000, 94_999, 5_000, 0, NominalKurangBayar, -5_001},
		{100_000, 99_999, 0, 0, NominalKurangBayar, -1},
		{100_000, 100_001, 0, 0, NominalLebihBayar, 1},
		{100_000, 105_000, 0, 5_000, NominalCocok, 5_000},
		{100_000, 105_001, 0, 5_000, NominalLebihBayar, 5_001},
		{0, 0, 0, 0, NominalCocok, 0},
		// Toleransi negatif diperlakukan sebagai 0 (fail closed).
		{100_000, 99_999, -5_000, -5_000, NominalKurangBayar, -1},
	}
	for _, k := range kasus {
		hasil, selisih := CocokNominal(k.total, k.bayar, k.tolK, k.tolL)
		if hasil != k.ingin || selisih != k.selisih {
			t.Fatalf("total=%d bayar=%d tol=%d/%d: dapat %q/%d, ingin %q/%d",
				k.total, k.bayar, k.tolK, k.tolL, hasil, selisih, k.ingin, k.selisih)
		}
	}
}

func TestMemoriIdempotensiKlaimSekali(t *testing.T) {
	p := NewMemoriIdempotensi()
	ctx := context.Background()
	baru, err := p.Klaim(ctx, "va:EV-1")
	if err != nil || !baru {
		t.Fatalf("klaim pertama: baru=%v err=%v", baru, err)
	}
	baru, err = p.Klaim(ctx, "va:EV-1")
	if err != nil || baru {
		t.Fatalf("klaim kedua: baru=%v err=%v", baru, err)
	}
	// Kunci berbeda tetap boleh.
	if baru, err = p.Klaim(ctx, "va:EV-2"); err != nil || !baru {
		t.Fatalf("klaim kunci lain: baru=%v err=%v", baru, err)
	}
	// Kunci kosong adalah galat, bukan "diterima".
	if _, err = p.Klaim(ctx, ""); !errors.Is(err, ErrPayloadTidakSah) {
		t.Fatalf("kunci kosong: ingin ErrPayloadTidakSah, dapat %v", err)
	}
}

func TestMockProviderMenolakTanpaRahasia(t *testing.T) {
	if _, err := NewMockProvider(MockConfig{}); !errors.Is(err, ErrProviderTidakTersedia) {
		t.Fatalf("tanpa rahasia: ingin ErrProviderTidakTersedia, dapat %v", err)
	}
}

func TestMockProviderTidakMembocorkanRahasia(t *testing.T) {
	rahasia := rahasiaUjiUnit()
	p, err := NewMockProvider(MockConfig{Rahasia: rahasia})
	if err != nil {
		t.Fatalf("NewMockProvider: %v", err)
	}
	hasil, err := p.DaftarVA(context.Background(), PermintaanVA{ReffID: "order:1", Nominal: 10_000})
	if err != nil {
		t.Fatalf("DaftarVA: %v", err)
	}
	if len(hasil.Raw) == 0 || !json.Valid(hasil.Raw) {
		t.Fatalf("Raw bukan JSON sah: %q", hasil.Raw)
	}
	if strings.Contains(string(hasil.Raw), string(rahasia)) {
		t.Fatal("jawaban provider memuat rahasia HMAC")
	}
	if p.Nama() == "" {
		t.Fatal("nama provider kosong")
	}
}

func TestNominalDariString(t *testing.T) {
	for s, ingin := range map[string]int64{
		"150000":    150_000,
		"150.000":   150_000,
		"Rp150.000": 150_000,
		" 1 000 ":   1_000,
		"1,000":     1_000,
	} {
		v, err := NominalDariString(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if v != ingin {
			t.Fatalf("%q: dapat %d, ingin %d", s, v, ingin)
		}
	}
	if _, err := NominalDariString("seratus ribu"); !errors.Is(err, ErrPayloadTidakSah) {
		t.Fatalf("teks: ingin ErrPayloadTidakSah, dapat %v", err)
	}
}
