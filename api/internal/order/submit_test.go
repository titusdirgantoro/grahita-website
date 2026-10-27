package order

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/settings"
)

// Berkas ini menguji submit pesanan (kartu F1-14) di tingkat domain:
// aturan snapshot/harga (SiapkanSubmit), kewajiban bukti, potongan deposit
// cabang C&C, tanggal bisnis WIB, dan penomoran. Penomoran di sini memakai Memory (cermin
// tabel counter); perilaku SQL/trigger sebenarnya diuji di uji integrasi
// PostgreSQL (internal/httpapi/integration_submit_pg_test.go).

// fakeFlags adalah sumber flag Cash & Carry cabang untuk pengujian.
type fakeFlags struct {
	cnc map[string]bool
	err error
}

func (f fakeFlags) IsCNC(_ context.Context, branchID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.cnc[branchID], nil
}

// waktuSubmit adalah waktu submit uji: 8 Oktober 2026 pukul 14:00 WIB
// (07:00 UTC) — jauh dari batas tengah malam supaya tanggalnya jelas.
var waktuSubmit = time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

// newSubmitService menyiapkan service submit dengan Memory, flag cabang
// (hanya br-cnc berflag C&C), setelan uji, dan clock tetap.
func newSubmitService() (*Service, *Memory, *fakeConfig) {
	repo := NewMemory()
	cfg := cfgUji()
	svc := NewService(repo, katalogUji(), cfg,
		WithBranchFlags(fakeFlags{cnc: map[string]bool{"br-cnc": true}}),
		WithClock(clock.Fixed(waktuSubmit)))
	return svc, repo, cfg
}

// hargaA adalah keadaan master+harga cabang A yang disuntikkan ke Memory:
// sama dengan katalog uji ditambah produk reguler tambahan.
func hargaA() map[string]MemoryProduct {
	return map[string]MemoryProduct{
		"p1": {Nama: "Bio Magnetik", Jenis: catalog.JenisReguler, Aktif: true, Harga: 100000},
		"p3": {Nama: "Sertifikat", Jenis: catalog.JenisSertifikat, Aktif: true, Harga: 0},
		"p4": {Nama: "Kertas HVS", Jenis: catalog.JenisReguler, Aktif: true, Harga: 5000},
	}
}

// draftReguler membuat draft satu baris produk reguler p1 sebanyak qty.
func draftReguler(t *testing.T, svc *Service, branchID string, qty int) Order {
	t.Helper()
	o, err := svc.CreateDraft(context.Background(), aktorCabang(branchID), DraftInput{
		Lines: []LineInput{{NamaInstansi: "Instansi Uji", ProductID: "p1", Qty: qty}},
	})
	if err != nil {
		t.Fatalf("buat draft: %v", err)
	}
	return o
}

// TestSiapkanSubmitMemakaiHargaTerbaru memverifikasi BR-05 di tingkat aturan:
// snapshot yang ditulis saat submit memakai nama/harga yang berlaku
// SAAT SUBMIT, bukan angka yang tersimpan di draft.
func TestSiapkanSubmitMemakaiHargaTerbaru(t *testing.T) {
	lines := []SubmitLine{{
		LineNo: 1, Jenis: JenisProduct, NamaInstansi: "Instansi Uji", Qty: 3,
		NamaLama:  "Nama Lama",
		ProdukAda: true, ProdukAktif: true,
		ProdukNama: "Nama Baru", ProdukJenis: catalog.JenisReguler,
		HargaCabang: 120000, AdaHargaCabang: true,
	}}

	got, err := SiapkanSubmit(lines, 3000)
	if err != nil {
		t.Fatalf("SiapkanSubmit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("harus 1 snapshot, dapat %d", len(got))
	}
	if got[0].Nama != "Nama Baru" || got[0].HargaSatuan != 120000 {
		t.Errorf("snapshot harus dari master terbaru, dapat %+v", got[0])
	}
}

// TestSiapkanSubmitMenolakProdukTakLagiDijual memverifikasi produk nonaktif
// maupun produk yang baris harga cabangnya hilang ditolak saat submit
// (harga_default bukan cadangan, PRD CP-KAT-02).
func TestSiapkanSubmitMenolakProdukTakLagiDijual(t *testing.T) {
	kasus := map[string]SubmitLine{
		"nonaktif": {
			LineNo: 1, Jenis: JenisProduct, NamaInstansi: "Instansi Uji", Qty: 1, ProdukAda: true, ProdukAktif: false,
			ProdukJenis: catalog.JenisReguler, HargaCabang: 100, AdaHargaCabang: true,
		},
		"tanpa harga cabang": {
			LineNo: 1, Jenis: JenisProduct, NamaInstansi: "Instansi Uji", Qty: 1, ProdukAda: true, ProdukAktif: true,
			ProdukJenis: catalog.JenisReguler, AdaHargaCabang: false,
		},
		"produk hilang": {
			LineNo: 1, Jenis: JenisProduct, NamaInstansi: "Instansi Uji", Qty: 1, ProdukAda: false, AdaHargaCabang: false,
		},
	}
	for nama, line := range kasus {
		t.Run(nama, func(t *testing.T) {
			if _, err := SiapkanSubmit([]SubmitLine{line}, 3000); !errors.Is(err, ErrProductNotSold) {
				t.Fatalf("harus ErrProductNotSold, dapat %v", err)
			}
		})
	}
}

// TestSiapkanSubmitAturanSertifikatDanPackaging memverifikasi aturan CP-SER-01
// sampai CP-SER-03 di tingkat snapshot: harga Sertifikat 0, tepat satu baris
// packaging senilai setelan yang berlaku, dan campuran/packaging liar ditolak.
func TestSiapkanSubmitAturanSertifikatDanPackaging(t *testing.T) {
	sertifikat := SubmitLine{
		LineNo: 1, Jenis: JenisProduct, NamaInstansi: "Instansi Uji", Qty: 5, ProdukAda: true, ProdukAktif: true,
		ProdukNama: "Sertifikat", ProdukJenis: catalog.JenisSertifikat,
		HargaCabang: 25000, AdaHargaCabang: true,
	}
	reguler := SubmitLine{
		LineNo: 1, Jenis: JenisProduct, NamaInstansi: "Instansi Uji", Qty: 1, ProdukAda: true, ProdukAktif: true,
		ProdukNama: "Bio", ProdukJenis: catalog.JenisReguler,
		HargaCabang: 100000, AdaHargaCabang: true,
	}
	packaging := SubmitLine{LineNo: 2, Jenis: JenisPackaging, Qty: 1, NamaLama: NamaBiayaPackaging}

	// Sertifikat + packaging: harga produk 0 berapa pun qty, harga packaging
	// memakai setelan saat submit (5000), bukan angka draft (3000).
	got, err := SiapkanSubmit([]SubmitLine{sertifikat, packaging}, 5000)
	if err != nil {
		t.Fatalf("SiapkanSubmit sertifikat: %v", err)
	}
	if len(got) != 2 || got[0].HargaSatuan != 0 || got[1].HargaSatuan != 5000 {
		t.Fatalf("snapshot sertifikat salah: %+v", got)
	}
	if got[1].Nama != NamaBiayaPackaging {
		t.Errorf("nama packaging harus teks baku server, dapat %+v", got[1])
	}

	kasus := map[string]struct {
		lines []SubmitLine
	}{
		"campuran sertifikat dan reguler": {[]SubmitLine{sertifikat, {LineNo: 2, Jenis: JenisProduct, NamaInstansi: "Instansi Uji", Qty: 1, ProdukAda: true, ProdukAktif: true, ProdukJenis: catalog.JenisReguler, HargaCabang: 1, AdaHargaCabang: true}}},
		"sertifikat tanpa packaging":      {[]SubmitLine{sertifikat}},
		"reguler dengan packaging":        {[]SubmitLine{reguler, packaging}},
		"dua baris packaging":             {[]SubmitLine{sertifikat, packaging, {LineNo: 3, Jenis: JenisPackaging, Qty: 1}}},
		"packaging qty bukan 1":           {[]SubmitLine{sertifikat, {LineNo: 2, Jenis: JenisPackaging, Qty: 2}}},
		"tanpa baris produk":              {[]SubmitLine{packaging}},
		"tanpa baris sama sekali":         {nil},
	}
	for nama, k := range kasus {
		t.Run(nama, func(t *testing.T) {
			if _, err := SiapkanSubmit(k.lines, 3000); err == nil {
				t.Fatal("harus ditolak, tetapi diterima")
			}
		})
	}
}

// TestSubmitWajibBuktiUntukNonCNC memverifikasi CP-ORD-04: pesanan non-C&C
// tanpa bukti ditolak dan TIDAK memakai nomor (counter tetap 0, status tetap
// draft), lalu berhasil setelah bukti tersedia.
func TestSubmitWajibBuktiUntukNonCNC(t *testing.T) {
	svc, repo, _ := newSubmitService()
	ctx := context.Background()
	repo.SetHargaCabang("br-a", hargaA())

	draft := draftReguler(t, svc, "br-a", 2)

	if _, err := svc.Submit(ctx, aktorCabang("br-a"), draft.ID, SubmitInput{}); !errors.Is(err, ErrProofRequired) {
		t.Fatalf("submit tanpa bukti harus ErrProofRequired, dapat %v", err)
	}
	if got := repo.Counter("invoice", "2026-10"); got != 0 {
		t.Errorf("submit gagal tidak boleh memakai nomor, counter=%d", got)
	}
	if got := repo.Counter("serial", "20261008"); got != 0 {
		t.Errorf("submit gagal tidak boleh memakai serial, counter=%d", got)
	}
	if len(repo.Events(draft.ID)) != 0 {
		t.Error("submit gagal tidak boleh menulis timeline")
	}
	after, err := svc.Get(ctx, aktorCabang("br-a"), draft.ID)
	if err != nil {
		t.Fatalf("baca ulang draft: %v", err)
	}
	if after.Status != StatusDraft || after.Nomor != "" {
		t.Fatalf("pesanan harus tetap draft tanpa nomor, dapat %+v", after)
	}

	repo.SetProofs(draft.ID, 1)
	out, err := svc.Submit(ctx, aktorCabang("br-a"), draft.ID, SubmitInput{})
	if err != nil {
		t.Fatalf("submit dengan bukti: %v", err)
	}
	if out.Status != StatusSubmitted || out.Nomor != "2026-10-01" {
		t.Fatalf("pesanan harus submitted bernomor 2026-10-01, dapat %+v", out)
	}
	if out.Tanggal != "2026-10-08" {
		t.Errorf("tanggal pesanan harus tanggal submit WIB (2026-10-08), dapat %q", out.Tanggal)
	}
	if out.SubmittedAt == "" {
		t.Error("submitted_at harus terisi (penyimpanan UTC)")
	}
	if out.IsCNC {
		t.Errorf("pesanan cabang non-C&C tidak boleh bertanda C&C: %+v", out)
	}
	if len(out.Lines) != 1 || out.Lines[0].SerialNumber != "20261008001" {
		t.Errorf("serial baris produk harus 20261008001, dapat %+v", out.Lines)
	}
	if got := repo.Events(draft.ID); len(got) != 1 || got[0] != TeksEventSubmit {
		t.Errorf("timeline submit tidak tercatat: %v", got)
	}
	// Harga dan total dihitung ulang server (2 × 100000).
	if out.Total != 200000 || out.Lines[0].HargaSatuan != 100000 {
		t.Errorf("total/harga tidak sesuai: total=%d baris=%+v", out.Total, out.Lines[0])
	}
}

// TestSubmitPenomoranBerurutanTanpaCelah memverifikasi BR-09: nomor invoice
// naik berurutan per bulan dan serial harian naik per baris produk, tanpa
// celah meski ada submit yang gagal di antaranya.
func TestSubmitPenomoranBerurutanTanpaCelah(t *testing.T) {
	svc, repo, _ := newSubmitService()
	ctx := context.Background()
	repo.SetHargaCabang("br-a", hargaA())

	var nomor []string
	var serial []string
	for i := 1; i <= 3; i++ {
		d := draftReguler(t, svc, "br-a", i)
		repo.SetProofs(d.ID, 1)
		out, err := svc.Submit(ctx, aktorCabang("br-a"), d.ID, SubmitInput{})
		if err != nil {
			t.Fatalf("submit ke-%d: %v", i, err)
		}
		nomor = append(nomor, out.Nomor)
		serial = append(serial, out.Lines[0].SerialNumber)
	}

	wantNomor := []string{"2026-10-01", "2026-10-02", "2026-10-03"}
	for i := range wantNomor {
		if nomor[i] != wantNomor[i] {
			t.Errorf("nomor ke-%d harus %s, dapat %s", i+1, wantNomor[i], nomor[i])
		}
	}
	// Serial: baris pertama memakai 001; qty mempengaruhi hanya total, jadi
	// setiap submit memakai satu nomor serial.
	wantSerial := []string{"20261008001", "20261008002", "20261008003"}
	for i := range wantSerial {
		if serial[i] != wantSerial[i] {
			t.Errorf("serial ke-%d harus %s, dapat %s", i+1, wantSerial[i], serial[i])
		}
	}

	// Submit ulang pesanan yang sudah submitted: 409 (ErrLocked), tanpa
	// menambah nomor.
	d := draftReguler(t, svc, "br-a", 1)
	repo.SetProofs(d.ID, 1)
	if _, err := svc.Submit(ctx, aktorCabang("br-a"), d.ID, SubmitInput{}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := svc.Submit(ctx, aktorCabang("br-a"), d.ID, SubmitInput{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("submit ulang harus ErrLocked, dapat %v", err)
	}
	if got := repo.Counter("invoice", "2026-10"); got != 4 {
		t.Errorf("submit ulang tidak boleh memakai nomor, counter=%d", got)
	}
}

// versi adalah alamat versi saldo deposit yang dilihat cabang (SubmitInput).
func versi(v int64) *int64 { return &v }

// TestSubmitCNCDibayarDeposit memverifikasi CP-CNC-02: pesanan cabang C&C yang
// saldonya cukup dibayar penuh dari deposit saat dikirim, tanpa bukti
// transfer, dan saldo turun tepat sebesar total.
func TestSubmitCNCDibayarDeposit(t *testing.T) {
	svc, repo, _ := newSubmitService()
	ctx := context.Background()
	repo.SetHargaCabang("br-cnc", hargaA())
	repo.SetSaldoDeposit("br-cnc", 500000)

	d := draftReguler(t, svc, "br-cnc", 2)
	out, err := svc.Submit(ctx, aktorCabang("br-cnc"), d.ID, SubmitInput{ExpectedJournalVersion: versi(repo.VersiSaldoDeposit("br-cnc"))})
	if err != nil {
		t.Fatalf("submit C&C bersaldo cukup: %v", err)
	}
	if !out.IsCNC || out.Status != StatusSubmitted {
		t.Fatalf("pesanan harus Submitted bertanda C&C, dapat %+v", out)
	}
	if got := repo.PotonganDepositPesanan(d.ID); got != out.Total {
		t.Errorf("deposit terpakai harus sama dengan total %d, dapat %d", out.Total, got)
	}
	if got := repo.SaldoDeposit("br-cnc"); got != 500000-out.Total {
		t.Errorf("saldo harus berkurang sebesar total, sisa %d", got)
	}
	if ev := repo.Events(d.ID); len(ev) != 2 || ev[1] != TeksEventDeposit(out.Total) {
		t.Errorf("timeline harus mencatat pembayaran deposit, dapat %v", ev)
	}
}

// TestSubmitCNCSisaSetelahDepositWajibBukti memverifikasi bahwa bila saldo
// kurang, seluruh saldo terpakai dan sisanya wajib disertai bukti transfer;
// penolakan tidak memakai nomor maupun saldo.
func TestSubmitCNCSisaSetelahDepositWajibBukti(t *testing.T) {
	svc, repo, _ := newSubmitService()
	ctx := context.Background()
	repo.SetHargaCabang("br-cnc", hargaA())
	repo.SetSaldoDeposit("br-cnc", 30000)

	d := draftReguler(t, svc, "br-cnc", 1)
	dilihat := SubmitInput{ExpectedJournalVersion: versi(repo.VersiSaldoDeposit("br-cnc"))}
	if _, err := svc.Submit(ctx, aktorCabang("br-cnc"), d.ID, dilihat); !errors.Is(err, ErrProofRequired) {
		t.Fatalf("sisa tanpa bukti harus ErrProofRequired, dapat %v", err)
	}
	if repo.SaldoDeposit("br-cnc") != 30000 || repo.Counter("invoice", "2026-10") != 0 {
		t.Fatal("submit yang ditolak tidak boleh memakai saldo maupun nomor")
	}

	repo.SetProofs(d.ID, 1)
	out, err := svc.Submit(ctx, aktorCabang("br-cnc"), d.ID, dilihat)
	if err != nil {
		t.Fatalf("submit campuran: %v", err)
	}
	if got := repo.PotonganDepositPesanan(d.ID); got != 30000 || got >= out.Total {
		t.Errorf("seluruh saldo (30000) harus terpakai dan kurang dari total %d, dapat %d", out.Total, got)
	}
	if repo.SaldoDeposit("br-cnc") != 0 {
		t.Errorf("saldo harus habis, sisa %d", repo.SaldoDeposit("br-cnc"))
	}
}

// TestSubmitCNCMenolakSaldoYangBerubah memverifikasi bahwa potongan deposit
// tidak pernah berubah diam-diam: cabang C&C wajib menyebut versi saldo yang ia
// lihat, dan saldo yang sudah berubah sejak itu membatalkan submit.
func TestSubmitCNCMenolakSaldoYangBerubah(t *testing.T) {
	svc, repo, _ := newSubmitService()
	ctx := context.Background()
	repo.SetHargaCabang("br-cnc", hargaA())
	repo.SetSaldoDeposit("br-cnc", 500000)
	dilihat := repo.VersiSaldoDeposit("br-cnc")
	d := draftReguler(t, svc, "br-cnc", 1)

	if _, err := svc.Submit(ctx, aktorCabang("br-cnc"), d.ID, SubmitInput{}); !errors.Is(err, ErrDepositVersionRequired) {
		t.Fatalf("tanpa versi saldo yang dilihat harus ErrDepositVersionRequired, dapat %v", err)
	}
	// Saldo berubah setelah modal kirim dibuka (mis. top-up baru diverifikasi).
	repo.SetSaldoDeposit("br-cnc", 400000)
	if _, err := svc.Submit(ctx, aktorCabang("br-cnc"), d.ID, SubmitInput{ExpectedJournalVersion: versi(dilihat)}); !errors.Is(err, ErrDepositChanged) {
		t.Fatalf("saldo berubah harus ErrDepositChanged, dapat %v", err)
	}
	if repo.SaldoDeposit("br-cnc") != 400000 || repo.Counter("invoice", "2026-10") != 0 {
		t.Fatal("submit yang ditolak tidak boleh memakai saldo maupun nomor")
	}
}

// TestSubmitCabangNonCNCTidakMemakaiDeposit memverifikasi CP-CNC-01: status
// C&C dibaca server, jadi cabang tanpa flag (atau service tanpa sumber flag)
// tetap wajib bukti dan tidak bisa memakai jalur deposit lewat masukan klien.
func TestSubmitCabangNonCNCTidakMemakaiDeposit(t *testing.T) {
	svc, repo, _ := newSubmitService()
	ctx := context.Background()
	repo.SetHargaCabang("br-b", map[string]MemoryProduct{
		"p1": {Nama: "Bio Magnetik", Jenis: catalog.JenisReguler, Aktif: true, Harga: 200000},
	})
	repo.SetSaldoDeposit("br-b", 1_000_000)
	d := draftReguler(t, svc, "br-b", 1)
	if _, err := svc.Submit(ctx, aktorCabang("br-b"), d.ID, SubmitInput{ExpectedJournalVersion: versi(repo.VersiSaldoDeposit("br-b"))}); !errors.Is(err, ErrProofRequired) {
		t.Fatalf("cabang non-C&C tanpa bukti harus ErrProofRequired, dapat %v", err)
	}
	repo.SetProofs(d.ID, 1)
	out, err := svc.Submit(ctx, aktorCabang("br-b"), d.ID, SubmitInput{ExpectedJournalVersion: versi(repo.VersiSaldoDeposit("br-b"))})
	if err != nil {
		t.Fatalf("submit non-C&C berbukti: %v", err)
	}
	if out.IsCNC || repo.SaldoDeposit("br-b") != 1_000_000 {
		t.Errorf("cabang non-C&C tidak boleh bertanda C&C maupun memakai saldo: %+v", out)
	}

	// Service tanpa sumber flag: semua cabang diperlakukan non-C&C.
	polosRepo := NewMemory()
	polos := NewService(polosRepo, katalogUji(), cfgUji(), WithClock(clock.Fixed(waktuSubmit)))
	polosRepo.SetHargaCabang("br-cnc", hargaA())
	polosRepo.SetSaldoDeposit("br-cnc", 500000)
	dp := draftReguler(t, polos, "br-cnc", 1)
	if _, err := polos.Submit(ctx, aktorCabang("br-cnc"), dp.ID, SubmitInput{ExpectedJournalVersion: versi(polosRepo.VersiSaldoDeposit("br-cnc"))}); !errors.Is(err, ErrProofRequired) {
		t.Fatalf("tanpa sumber flag harus wajib bukti, dapat %v", err)
	}

	// Error membaca flag dilaporkan apa adanya, bukan dianggap non-C&C.
	rusak := NewService(NewMemory(), katalogUji(), cfgUji(), WithBranchFlags(fakeFlags{err: errors.New("db mati")}))
	if _, err := rusak.Submit(ctx, aktorCabang("br-cnc"), dp.ID, SubmitInput{}); err == nil || errors.Is(err, ErrProofRequired) {
		t.Fatalf("kegagalan baca flag harus dilaporkan apa adanya, dapat %v", err)
	}
}

// TestSubmitMenolakAktorDanPesananYangSalah memverifikasi isolasi cabang dan
// batas aktor: hanya akun cabang pemilik pesanan yang boleh submit.
func TestSubmitMenolakAktorDanPesananYangSalah(t *testing.T) {
	svc, repo, _ := newSubmitService()
	ctx := context.Background()
	repo.SetHargaCabang("br-a", hargaA())
	d := draftReguler(t, svc, "br-a", 1)
	repo.SetProofs(d.ID, 1)

	// Cabang lain: pesanan tidak ditemukan (tidak membocorkan keberadaannya).
	if _, err := svc.Submit(ctx, aktorCabang("br-b"), d.ID, SubmitInput{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cabang lain harus ErrNotFound, dapat %v", err)
	}
	// Aktor admin: submit adalah hak cabang.
	admin := auth.Actor{UserID: "adm-1", Kind: auth.KindAdmin, Role: auth.RoleManager}
	if _, err := svc.Submit(ctx, admin, d.ID, SubmitInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin harus ErrForbidden, dapat %v", err)
	}
	// Id yang bukan UUID: 404, bukan 500.
	if _, err := svc.Submit(ctx, aktorCabang("br-a"), "bukan-uuid", SubmitInput{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("id bukan UUID harus ErrNotFound, dapat %v", err)
	}
	if got := repo.Counter("invoice", "2026-10"); got != 0 {
		t.Errorf("percobaan yang ditolak tidak boleh memakai nomor, counter=%d", got)
	}
}

// TestSubmitTanggalWIBDekatTengahMalam memverifikasi BR-04/BR-11: tanggal
// pesanan mengikuti zona Asia/Jakarta, bukan UTC.
func TestSubmitTanggalWIBDekatTengahMalam(t *testing.T) {
	kasus := []struct {
		nama     string
		sekarang time.Time
		tanggal  string
		periode  string
	}{
		// 08 Oktober 2026 16:59:59 UTC = 23:59:59 WIB → masih 8 Oktober WIB.
		{"23:59:59 WIB", time.Date(2026, 10, 8, 16, 59, 59, 0, time.UTC), "2026-10-08", "2026-10"},
		// 08 Oktober 2026 17:00:00 UTC = 09 Oktober 2026 00:00 WIB → pindah hari.
		{"00:00:00 WIB", time.Date(2026, 10, 8, 17, 0, 0, 0, time.UTC), "2026-10-09", "2026-10"},
		// Awal bulan di WIB: 31 Oktober 2026 18:00 UTC = 01 November WIB.
		{"awal bulan WIB", time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC), "2026-11-01", "2026-11"},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			repo := NewMemory()
			svc := NewService(repo, katalogUji(), cfgUji(),
				WithBranchFlags(fakeFlags{}),
				WithClock(clock.Fixed(k.sekarang)))
			repo.SetHargaCabang("br-a", hargaA())
			d := draftReguler(t, svc, "br-a", 1)
			repo.SetProofs(d.ID, 1)
			out, err := svc.Submit(context.Background(), aktorCabang("br-a"), d.ID, SubmitInput{})
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if out.Tanggal != k.tanggal {
				t.Errorf("tanggal harus %s, dapat %s", k.tanggal, out.Tanggal)
			}
			if got := repo.Counter("invoice", k.periode); got != 1 {
				t.Errorf("counter periode %s harus 1, dapat %d", k.periode, got)
			}
			// Serial memakai periode hari WIB pula.
			mulai, _ := time.Parse(TanggalLayout, k.tanggal)
			if !strings.HasPrefix(out.Lines[0].SerialNumber, mulai.Format("20060102")) {
				t.Errorf("serial harus berawalan periode hari WIB %s, dapat %q",
					mulai.Format("20060102"), out.Lines[0].SerialNumber)
			}
			if len(out.Lines[0].SerialNumber) != 11 {
				t.Errorf("serial harus YYYYMMDDXXX (11 karakter), dapat %q", out.Lines[0].SerialNumber)
			}
		})
	}
}

// TestSubmitSertifikatSnapshotPackagingFinal memverifikasi bahwa pesanan
// Sertifikat disubmit dengan harga produk 0, TEPAT satu baris packaging, tanpa
// serial, dan harga packaging memakai setelan yang berlaku saat submit.
func TestSubmitSertifikatSnapshotPackagingFinal(t *testing.T) {
	svc, repo, cfg := newSubmitService()
	ctx := context.Background()
	repo.SetHargaCabang("br-a", hargaA())

	d, err := svc.CreateDraft(ctx, aktorCabang("br-a"), DraftInput{
		Lines: []LineInput{{NamaInstansi: "Instansi Uji", ProductID: "p3", Qty: 4}},
	})
	if err != nil {
		t.Fatalf("buat draft sertifikat: %v", err)
	}
	repo.SetProofs(d.ID, 1)

	// Setelan berubah antara draft dan submit: biaya final = 7000.
	cfg.values[settings.BiayaPackaging] = 7000
	out, err := svc.Submit(ctx, aktorCabang("br-a"), d.ID, SubmitInput{})
	if err != nil {
		t.Fatalf("submit sertifikat: %v", err)
	}
	if !out.IsSertifikat {
		t.Error("pesanan sertifikat harus ditandai is_sertifikat")
	}
	if out.Total != 7000 {
		t.Errorf("total sertifikat = 0×4 + 7000 packaging, dapat %d", out.Total)
	}
	var produk, packaging int
	for _, l := range out.Lines {
		switch l.Jenis {
		case JenisProduct:
			produk++
			if l.HargaSatuan != 0 || l.SerialNumber == "" {
				t.Errorf("baris sertifikat harus berharga 0 berserial: %+v", l)
			}
		case JenisPackaging:
			packaging++
			if l.HargaSatuan != 7000 || l.Qty != 1 || l.SerialNumber != "" {
				t.Errorf("baris packaging salah: %+v", l)
			}
		}
	}
	if produk != 1 || packaging != 1 {
		t.Errorf("harus 1 baris produk + 1 packaging, dapat %d/%d", produk, packaging)
	}
}

// TestSubmitDigitInvoiceMelebar memverifikasi BR-03: bila urutan melewati
// jumlah digit `digit_invoice`, nomor bertambah digit — bukan gagal atau
// memakai nomor yang sama.
func TestSubmitDigitInvoiceMelebar(t *testing.T) {
	svc, repo, cfg := newSubmitService()
	ctx := context.Background()
	repo.SetHargaCabang("br-a", hargaA())
	cfg.values[settings.DigitInvoice] = 1

	var terakhir string
	for i := 1; i <= 10; i++ {
		d := draftReguler(t, svc, "br-a", 1)
		repo.SetProofs(d.ID, 1)
		out, err := svc.Submit(ctx, aktorCabang("br-a"), d.ID, SubmitInput{})
		if err != nil {
			t.Fatalf("submit ke-%d: %v", i, err)
		}
		terakhir = out.Nomor
	}
	if terakhir != "2026-10-10" {
		t.Errorf("urutan ke-10 dengan digit=1 harus melebar menjadi 2026-10-10, dapat %q", terakhir)
	}
	if got := repo.Counter("invoice", "2026-10"); got != 10 {
		t.Errorf("counter harus 10, dapat %d", got)
	}
}

// TestSubmitSerialSatuRentangPerBarisProduk memverifikasi BR-02: seluruh baris
// produk satu pesanan memakai rentang serial berurutan, dan baris packaging
// tidak pernah mendapat serial.
func TestSubmitSerialSatuRentangPerBarisProduk(t *testing.T) {
	svc, repo, _ := newSubmitService()
	ctx := context.Background()
	repo.SetHargaCabang("br-a", hargaA())

	d, err := svc.CreateDraft(ctx, aktorCabang("br-a"), DraftInput{
		Lines: []LineInput{{NamaInstansi: "Instansi Uji", ProductID: "p1", Qty: 1}, {NamaInstansi: "Instansi Uji", ProductID: "p4", Qty: 2}},
	})
	if err != nil {
		t.Fatalf("buat draft: %v", err)
	}
	repo.SetProofs(d.ID, 1)
	out, err := svc.Submit(ctx, aktorCabang("br-a"), d.ID, SubmitInput{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	want := []string{"20261008001", "20261008002"}
	for i, l := range out.Lines {
		if l.SerialNumber != want[i] {
			t.Errorf("baris %d serial harus %s, dapat %s", i+1, want[i], l.SerialNumber)
		}
	}
	if got := repo.Counter("serial", "20261008"); got != 2 {
		t.Errorf("counter serial harus 2, dapat %d", got)
	}
}
