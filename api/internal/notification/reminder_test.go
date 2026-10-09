package notification

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/platform/clock"
)

// ---------- tiruan penyimpanan pengingat C&C ----------

// fakeReminderStore meniru notification.ReminderStore: kandidat yang
// dikonfigurasi uji, penjaga satu-baris-per-(order,tahap) seperti PRIMARY KEY
// cnc_reminders, dan kemampuan menyuntikkan kegagalan penulisan.
type fakeReminderStore struct {
	kandidat []KandidatCNC
	dibuat   map[string]bool
	emails   []Envelope
	tahapan  []string
	// hariTerakhir merekam hari bisnis yang diterima KandidatCNC supaya uji
	// dapat memastikan service meneruskan tanggal bisnis WIB, bukan instant
	// mentah dari clock.
	hariTerakhir time.Time
	// gagal, bila diisi, membuat setiap penulisan gagal (meniru transaksi
	// database yang rollback).
	gagal error
}

func newFakeReminderStore(kandidat ...KandidatCNC) *fakeReminderStore {
	return &fakeReminderStore{kandidat: kandidat, dibuat: map[string]bool{}}
}

// KandidatCNC meniru postgres.Repo.KandidatCNC: menyaring kandidat ke jendela
// tanggal JendelaKandidatCNC(hari) sehingga tier unit hanya melihat kandidat
// yang akan dibaca kueri sungguhan (yang penyaringan SQL-nya diuji di tier
// integrasi).
func (f *fakeReminderStore) KandidatCNC(_ context.Context, hari time.Time) ([]KandidatCNC, error) {
	f.hariTerakhir = hari
	mulai, akhir := JendelaKandidatCNC(hari)
	out := []KandidatCNC{}
	for _, k := range f.kandidat {
		jt := tanggalWIB(k.JatuhTempo)
		if !jt.Before(mulai) && !jt.After(akhir) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (f *fakeReminderStore) AntreReminderCNC(_ context.Context, orderID, tahap string, e Envelope) (bool, error) {
	if f.gagal != nil {
		return false, f.gagal
	}
	kunci := orderID + "|" + tahap
	if f.dibuat[kunci] {
		return false, nil
	}
	f.dibuat[kunci] = true
	f.emails = append(f.emails, e)
	f.tahapan = append(f.tahapan, tahap)
	return true, nil
}

// kandidatCNC adalah kandidat ringkas untuk uji.
func kandidatCNC(id, nomor, hariJatuhTempo string) KandidatCNC {
	jt, err := time.ParseInLocation("2006-01-02", hariJatuhTempo, clock.Jakarta())
	if err != nil {
		panic(err)
	}
	return KandidatCNC{
		OrderID: id, Nomor: nomor, Cabang: "Cabang Uji", NamaKepala: "Kepala Uji",
		CabangEmail: "cabang-uji@example.test", Total: 350000, JatuhTempo: jt,
	}
}

func svcReminder(t *testing.T, clk clock.Clock, rs ReminderStore) *Service {
	t.Helper()
	return New(newFakeStore(), nil, WithClock(clk), WithReminderStore(rs))
}

// ---------- TahapReminder ----------

// TestTahapReminderEmpatTahap mengunci keempat tanggal pengingat dan
// memastikan tanggal lain tidak pernah menghasilkan tahap (PRD CP-CNC-05).
func TestTahapReminderEmpatTahap(t *testing.T) {
	jt := time.Date(2026, 10, 9, 0, 0, 0, 0, clock.Jakarta())
	for _, tc := range []struct {
		geser int
		mau   string
	}{
		{-2, TahapReminderH2},
		{-1, ""},
		{0, TahapReminderH0},
		{1, TahapReminderP1},
		{2, ""},
		{3, TahapReminderP3},
		{4, ""},
		{-3, ""},
	} {
		hari := jt.AddDate(0, 0, tc.geser)
		dapat, ok := TahapReminder(hari, jt)
		if tc.mau == "" {
			if ok {
				t.Errorf("geser %d tidak boleh punya tahap, dapat %q", tc.geser, dapat)
			}
			continue
		}
		if !ok || dapat != tc.mau {
			t.Errorf("geser %d harus tahap %q, dapat %q (ok=%v)", tc.geser, tc.mau, dapat, ok)
		}
	}
}

// TestTahapReminderMemakaiTanggalWIB membuktikan perbandingan memakai tanggal
// bisnis WIB (bukan tanggal UTC): pukul 00:30 WIB sudah masuk hari WIB
// berikutnya walau jam server (UTC) masih hari sebelumnya.
func TestTahapReminderMemakaiTanggalWIB(t *testing.T) {
	jt := time.Date(2026, 10, 7, 0, 0, 0, 0, clock.Jakarta())

	// 2026-10-06T17:30Z == 2026-10-07 00:30 WIB -> hari jatuh tempo (h-0).
	utcMalam := time.Date(2026, 10, 6, 17, 30, 0, 0, time.UTC)
	dapat, ok := TahapReminder(clock.TodayWIB(clock.Fixed(utcMalam)), jt)
	if !ok || dapat != TahapReminderH0 {
		t.Errorf("00:30 WIB harus h-0, dapat %q (ok=%v)", dapat, ok)
	}

	// 2026-10-06T16:59Z == 2026-10-06 23:59 WIB -> sehari sebelum h-0 (tanpa tahap).
	utcSebelum := time.Date(2026, 10, 6, 16, 59, 0, 0, time.UTC)
	if dapat, ok := TahapReminder(clock.TodayWIB(clock.Fixed(utcSebelum)), jt); ok {
		t.Errorf("23:59 WIB harus tanpa tahap, dapat %q", dapat)
	}
}

// TestJalankanReminderCNCMemakaiTanggalWIB membuktikan jadwal 07:00 WIB tetap
// benar bila jam server UTC: pukul 07:00 WIB = 00:00 UTC hari yang sama, dan
// pukul 00:30 WIB (masih UTC hari sebelumnya) sudah memicu tahap hari WIB itu.
func TestJalankanReminderCNCMemakaiTanggalWIB(t *testing.T) {
	// Pesanan jatuh tempo 2026-10-07 (WIB).
	rs := newFakeReminderStore(kandidatCNC("o-1", "2026-10-0001", "2026-10-07"))
	clk := clock.Fixed(time.Date(2026, 10, 6, 17, 30, 0, 0, time.UTC)) // 2026-10-07 00:30 WIB
	svc := svcReminder(t, clk, rs)

	hasil, err := svc.JalankanReminderCNC(context.Background())
	if err != nil {
		t.Fatalf("JalankanReminderCNC: %v", err)
	}
	if hasil.Diantre != 1 || hasil.Gagal != 0 {
		t.Fatalf("00:30 WIB harus mengantre tepat satu pengingat h-0, hasil %+v", hasil)
	}
	if len(rs.tahapan) != 1 || rs.tahapan[0] != TahapReminderH0 {
		t.Fatalf("tahap harus %s, dapat %v", TahapReminderH0, rs.tahapan)
	}
}

// TestJalankanReminderCNCDiteruskanHariBisnisWIB memastikan service menyerahkan
// HARI BISNIS WIB (bukan instant mentah clock) ke penyimpanan, dan bahwa
// kandidat di luar jendela tidak pernah dibaca — jadi hasil pemindaian hanya
// bergantung pada tanggal WIB, bukan tanggal/UTC server.
func TestJalankanReminderCNCDiteruskanHariBisnisWIB(t *testing.T) {
	rs := newFakeReminderStore(
		kandidatCNC("o-luar-lalu", "2026-10-0001", "2026-10-03"),  // hari-4: di luar jendela
		kandidatCNC("o-dalam", "2026-10-0002", "2026-10-09"),      // h-2
		kandidatCNC("o-luar-depan", "2026-10-0003", "2026-10-10"), // hari+3: di luar jendela
	)
	// 2026-10-06T17:30Z == 2026-10-07 00:30 WIB (jam UTC masih hari sebelumnya).
	clk := clock.Fixed(time.Date(2026, 10, 6, 17, 30, 0, 0, time.UTC))
	svc := svcReminder(t, clk, rs)

	hasil, err := svc.JalankanReminderCNC(context.Background())
	if err != nil {
		t.Fatalf("JalankanReminderCNC: %v", err)
	}
	if mau := time.Date(2026, 10, 7, 0, 0, 0, 0, clock.Jakarta()); !rs.hariTerakhir.Equal(mau) {
		t.Errorf("hari bisnis yang diteruskan harus %s (WIB), dapat %s", mau, rs.hariTerakhir)
	}
	if hasil.Diperiksa != 1 || hasil.Diantre != 1 || hasil.Dilewati != 0 {
		t.Errorf("hanya kandidat di dalam jendela yang boleh dibaca/diproses, hasil %+v", hasil)
	}
	if len(rs.tahapan) != 1 || rs.tahapan[0] != TahapReminderH2 {
		t.Errorf("tahap harus hanya %s, dapat %v", TahapReminderH2, rs.tahapan)
	}
}

// ---------- JendelaKandidatCNC ----------

// TestJendelaKandidatCNCMenutupSemuaTahap mengunci hubungan antara jadwal
// tahap dan jendela kandidat: SETIAP jatuh tempo yang menghasilkan tahap pada
// hari `hari` (menurut TahapReminder, satu-satunya penentu tahap) selalu berada
// di dalam JendelaKandidatCNC(hari). Karena itu menambahkan klausa jendela pada
// kueri kandidat tidak mungkin menjatuhkan pengingat — ia hanya membatasi baris
// yang dibaca. Sekaligus ditegaskan jendela memang lebih LUAS: tepi dalamnya
// (hari-2 dan hari+1) tidak ber-tahap.
func TestJendelaKandidatCNCMenutupSemuaTahap(t *testing.T) {
	hari := time.Date(2026, 10, 7, 7, 0, 0, 0, clock.Jakarta())
	mulai, akhir := JendelaKandidatCNC(hari)
	if mau := time.Date(2026, 10, 4, 0, 0, 0, 0, clock.Jakarta()); !mulai.Equal(mau) {
		t.Errorf("batas mundur harus %s (hari-3), dapat %s", mau, mulai)
	}
	if mau := time.Date(2026, 10, 9, 0, 0, 0, 0, clock.Jakarta()); !akhir.Equal(mau) {
		t.Errorf("batas maju harus %s (hari+2), dapat %s", mau, akhir)
	}
	if mulai.Location() != clock.Jakarta() || akhir.Location() != clock.Jakarta() {
		t.Fatalf("batas jendela harus bertanggal WIB, dapat %s .. %s", mulai.Location(), akhir.Location())
	}

	// Tidak boleh ada tahap yang jatuh di luar jendela — dijangkau jauh ke
	// belakang (tumpukan C&C lewat jatuh tempo bertahun-tahun) dan ke depan.
	jt := time.Date(2026, 10, 9, 0, 0, 0, 0, clock.Jakarta())
	for geser := -400; geser <= 400; geser++ {
		kandidatJt := jt.AddDate(0, 0, geser)
		if _, ok := TahapReminder(hari, kandidatJt); !ok {
			continue
		}
		if kandidatJt.Before(mulai) || kandidatJt.After(akhir) {
			t.Fatalf("jatuh tempo %s punya tahap pada %s tetapi di luar jendela %s..%s",
				kandidatJt.Format("2006-01-02"), hari.Format("2006-01-02"),
				mulai.Format("2006-01-02"), akhir.Format("2006-01-02"))
		}
	}

	for _, tanggal := range []string{"2026-10-05", "2026-10-08"} { // hari-2, hari+1
		d, err := time.ParseInLocation("2006-01-02", tanggal, clock.Jakarta())
		if err != nil {
			t.Fatalf("parse %s: %v", tanggal, err)
		}
		if _, ok := TahapReminder(hari, d); ok {
			t.Errorf("%s seharusnya tanpa tahap (jendela memang lebih luas dari tanggal ber-tahap)", tanggal)
		}
	}
}

// TestJendelaKandidatCNCMemakaiTanggalWIB membuktikan batas jendela dihitung
// pada tanggal bisnis WIB: instant yang jam UTC-nya masih hari sebelumnya tetapi
// sudah lewat tengah malam WIB memakai hari WIB berikutnya.
func TestJendelaKandidatCNCMemakaiTanggalWIB(t *testing.T) {
	// 2026-10-06T17:30Z == 2026-10-07 00:30 WIB.
	pagi := time.Date(2026, 10, 6, 17, 30, 0, 0, time.UTC)
	mulai, akhir := JendelaKandidatCNC(pagi)
	mauMulai := time.Date(2026, 10, 4, 0, 0, 0, 0, clock.Jakarta())
	mauAkhir := time.Date(2026, 10, 9, 0, 0, 0, 0, clock.Jakarta())
	if !mulai.Equal(mauMulai) || !akhir.Equal(mauAkhir) {
		t.Errorf("jendela 00:30 WIB harus %s..%s, dapat %s..%s", mauMulai, mauAkhir, mulai, akhir)
	}

	// Jendela sama baik dipanggil dengan instant waktu maupun hari WIB-nya.
	m, a := JendelaKandidatCNC(clock.TodayWIB(clock.Fixed(pagi)))
	if !m.Equal(mulai) || !a.Equal(akhir) {
		t.Errorf("jendela dari hari WIB (%s..%s) harus sama dengan dari instant (%s..%s)", m, a, mulai, akhir)
	}
}

// ---------- JalankanReminderCNC ----------

// TestJalankanReminderCNCIsiEmail memeriksa email yang benar-benar diantre:
// penerima email cabang, jenis reminder_cnc, dan payload yang dipakai template.
func TestJalankanReminderCNCIsiEmail(t *testing.T) {
	rs := newFakeReminderStore(kandidatCNC("o-1", "2026-10-0001", "2026-10-09"))
	clk := clock.Fixed(time.Date(2026, 10, 7, 7, 0, 0, 0, clock.Jakarta()))
	svc := svcReminder(t, clk, rs)

	if _, err := svc.JalankanReminderCNC(context.Background()); err != nil {
		t.Fatalf("JalankanReminderCNC: %v", err)
	}
	if len(rs.emails) != 1 {
		t.Fatalf("harus 1 email, dapat %d", len(rs.emails))
	}
	e := rs.emails[0]
	if e.Jenis != JenisReminderCNC || e.OrderID != "o-1" {
		t.Errorf("email salah jenis/pesanan: %+v", e)
	}
	if len(e.To) != 1 || e.To[0] != "cabang-uji@example.test" {
		t.Errorf("penerima harus email cabang, dapat %v", e.To)
	}
	for kunci, mau := range map[string]any{
		"template":    JenisReminderCNC,
		"nomor":       "2026-10-0001",
		"tahap":       "H-2",
		"jatuh_tempo": "2026-10-09",
		"total":       int64(350000),
		"nama":        "Kepala Uji",
	} {
		if e.Payload[kunci] != mau {
			t.Errorf("payload[%q] = %v, mau %v", kunci, e.Payload[kunci], mau)
		}
	}
	if e.Subject == "" {
		t.Error("subjek tidak boleh kosong")
	}
}

// TestJalankanReminderCNCTidakMenggandakanEmail menjalankan pemindaian dua
// kali pada hari yang sama: tahap yang sudah tercatat tidak pernah diantre
// ulang (PRIMARY KEY cnc_reminders).
func TestJalankanReminderCNCTidakMenggandakanEmail(t *testing.T) {
	rs := newFakeReminderStore(
		kandidatCNC("o-1", "2026-10-0001", "2026-10-09"), // h-2
		kandidatCNC("o-2", "2026-10-0002", "2026-10-07"), // h-0
	)
	clk := clock.Fixed(time.Date(2026, 10, 7, 7, 0, 0, 0, clock.Jakarta()))
	svc := svcReminder(t, clk, rs)
	ctx := context.Background()

	pertama, err := svc.JalankanReminderCNC(ctx)
	if err != nil {
		t.Fatalf("pemindaian pertama: %v", err)
	}
	if pertama.Diantre != 2 || pertama.Dilewati != 0 {
		t.Fatalf("pemindaian pertama harus mengantre 2, hasil %+v", pertama)
	}

	kedua, err := svc.JalankanReminderCNC(ctx)
	if err != nil {
		t.Fatalf("pemindaian kedua: %v", err)
	}
	if kedua.Diantre != 0 || kedua.Dilewati != 2 {
		t.Fatalf("pemindaian kedua harus melewati semua, hasil %+v", kedua)
	}
	if len(rs.emails) != 2 {
		t.Fatalf("total email harus tetap 2, dapat %d", len(rs.emails))
	}
}

// TestJalankanReminderCNCTahapBerlanjutMemakaiClockYangDipindah menempuh
// keempat tahap satu pesanan dengan memindahkan jam hari demi hari.
func TestJalankanReminderCNCTahapBerlanjutMemakaiClockYangDipindah(t *testing.T) {
	rs := newFakeReminderStore(kandidatCNC("o-1", "2026-10-0001", "2026-10-09"))
	clk := clock.Fixed(time.Date(2026, 10, 7, 7, 0, 0, 0, clock.Jakarta()))
	svc := svcReminder(t, clk, rs)
	ctx := context.Background()

	mau := []struct {
		hari  time.Time
		tahap string
	}{
		{time.Date(2026, 10, 7, 7, 0, 0, 0, clock.Jakarta()), TahapReminderH2},
		{time.Date(2026, 10, 8, 7, 0, 0, 0, clock.Jakarta()), ""}, // h-1: tidak ada
		{time.Date(2026, 10, 9, 7, 0, 0, 0, clock.Jakarta()), TahapReminderH0},
		{time.Date(2026, 10, 10, 7, 0, 0, 0, clock.Jakarta()), TahapReminderP1},
		{time.Date(2026, 10, 11, 7, 0, 0, 0, clock.Jakarta()), ""}, // h+2: tidak ada
		{time.Date(2026, 10, 12, 7, 0, 0, 0, clock.Jakarta()), TahapReminderP3},
	}
	for _, m := range mau {
		clk.Set(m.hari)
		hasil, err := svc.JalankanReminderCNC(ctx)
		if err != nil {
			t.Fatalf("JalankanReminderCNC %s: %v", m.hari, err)
		}
		if m.tahap == "" {
			if hasil.Diantre != 0 {
				t.Errorf("%s: tidak boleh ada pengingat, dapat %+v", m.hari, hasil)
			}
			continue
		}
		if hasil.Diantre != 1 {
			t.Errorf("%s: harus 1 pengingat, dapat %+v", m.hari, hasil)
		}
	}
	mauTahap := []string{TahapReminderH2, TahapReminderH0, TahapReminderP1, TahapReminderP3}
	if len(rs.tahapan) != len(mauTahap) {
		t.Fatalf("harus %d pengingat, dapat %v", len(mauTahap), rs.tahapan)
	}
	for i, th := range mauTahap {
		if rs.tahapan[i] != th {
			t.Errorf("pengingat ke-%d harus %s, dapat %s", i+1, th, rs.tahapan[i])
		}
	}
}

// TestJalankanReminderCNCGagalTidakDianggapTerkirim menutup kriteria kartu:
// penulisan yang gagal TIDAK meninggalkan baris cnc_reminders, sehingga tahap
// yang sama dicoba lagi (dan berhasil) pada pemindaian berikutnya.
func TestJalankanReminderCNCGagalTidakDianggapTerkirim(t *testing.T) {
	rs := newFakeReminderStore(kandidatCNC("o-1", "2026-10-0001", "2026-10-09"))
	rs.gagal = errors.New("database menolak")
	clk := clock.Fixed(time.Date(2026, 10, 7, 7, 0, 0, 0, clock.Jakarta()))
	svc := svcReminder(t, clk, rs)
	ctx := context.Background()

	pertama, err := svc.JalankanReminderCNC(ctx)
	if err != nil {
		t.Fatalf("kegagalan satu kandidat tidak boleh menggagalkan job: %v", err)
	}
	if pertama.Gagal != 1 || pertama.Diantre != 0 {
		t.Fatalf("penulisan gagal harus tercatat gagal, hasil %+v", pertama)
	}
	if len(rs.dibuat) != 0 {
		t.Fatalf("kegagalan harus tidak meninggalkan baris reminder, dapat %v", rs.dibuat)
	}

	rs.gagal = nil
	kedua, err := svc.JalankanReminderCNC(ctx)
	if err != nil {
		t.Fatalf("pemindaian kedua: %v", err)
	}
	if kedua.Diantre != 1 {
		t.Fatalf("tahap yang gagal harus dicoba lagi dan berhasil, hasil %+v", kedua)
	}
}

// TestJalankanReminderCNCTanpaPenerima memastikan kandidat tanpa email cabang
// tidak membuat baris outbox tanpa penerima (email tak berhak) dan tidak
// menggagalkan seluruh job.
func TestJalankanReminderCNCTanpaPenerima(t *testing.T) {
	k := kandidatCNC("o-1", "2026-10-0001", "2026-10-09")
	k.CabangEmail = ""
	rs := newFakeReminderStore(k)
	clk := clock.Fixed(time.Date(2026, 10, 7, 7, 0, 0, 0, clock.Jakarta()))
	svc := svcReminder(t, clk, rs)

	hasil, err := svc.JalankanReminderCNC(context.Background())
	if err != nil {
		t.Fatalf("JalankanReminderCNC: %v", err)
	}
	if hasil.Gagal != 1 || hasil.Diantre != 0 || len(rs.emails) != 0 {
		t.Fatalf("kandidat tanpa penerima harus dilewati, hasil %+v", hasil)
	}
}

// TestJalankanReminderCNCTanpaStore menolak berjalan tanpa penyimpanan.
func TestJalankanReminderCNCTanpaStore(t *testing.T) {
	svc := New(newFakeStore(), nil)
	if _, err := svc.JalankanReminderCNC(context.Background()); err == nil {
		t.Fatal("tanpa penyimpanan pengingat harus error")
	}
}
