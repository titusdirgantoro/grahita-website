// Package dashboard menyajikan metrik dan chip "perlu tindakan" untuk dashboard
// admin Pusat (kartu F1-20, PRD §4.1 AP-DSH-01..06).
//
// Aturan yang ditegakkan di sini:
//
//   - Otorisasi ditentukan KAPABILITAS, bukan tampilan: Manager maupun
//     Superadmin boleh membuka dashboard (`admin.dashboard`, PRD §2.1), tetapi
//     NILAI RUPIAH hanya keluar untuk pemegang `admin.dashboard.value`
//     (Superadmin). Field `nilai` dihilangkan dari respons untuk Manager —
//     bukan dikirim nol — supaya angka uang tidak pernah sampai ke browser
//     Manager (AP-DSH-02).
//   - Seluruh tanggal bisnis memakai zona Asia/Jakarta dan "hari ini" diambil
//     dari jam aplikasi (clock.Clock) pada zona itu, bukan UTC. Rentang periode
//     (hari/minggu/bulan/tahun) mengikuti perilaku prototype: minggu mulai
//     Senin, bulan berjalan sampai hari ini, tahun berjalan sampai hari ini.
//   - Angka chip dihitung dengan klausa yang sama dengan filter daftar riwayat
//     pemesanan (F1-15), jadi "3 menunggu verifikasi" selalu berarti 3 baris
//     yang benar-benar muncul di daftar terfilter.
//   - Filter yang tidak dikenal DITOLAK (400), bukan diam-diam diabaikan.
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
)

// Sentinel error domain; pemetaan ke kode HTTP ada di lapisan transport.
var (
	// ErrForbidden: aktor bukan admin Pusat ber-kapabilitas `admin.dashboard`.
	ErrForbidden = errors.New("tidak berhak mengakses dashboard admin")
	// ErrFilterInvalid: parameter filter dashboard tidak valid.
	ErrFilterInvalid = errors.New("filter dashboard tidak valid")
)

// Periode yang dikenal (AP-DSH-01). Kosong berarti periode bawaan.
const (
	PeriodeHari   = "hari"
	PeriodeMinggu = "minggu"
	PeriodeBulan  = "bulan"
	PeriodeTahun  = "tahun"

	// DefaultPeriode adalah periode bawaan bila klien tidak mengirimnya —
	// sama dengan keadaan awal prototype (`bulan ini`).
	DefaultPeriode = PeriodeBulan

	// MaxPeringkatCabang membatasi panjang peringkat cabang (AP-DSH-06).
	MaxPeringkatCabang = 5

	// ZonaBisnis adalah zona tanggal bisnis seluruh sistem (PRD §7.2).
	ZonaBisnis = "Asia/Jakarta"

	// LayoutTanggal adalah bentuk tanggal bisnis pada filter & respons.
	LayoutTanggal = "2006-01-02"
	// LayoutBulan adalah bentuk kunci tren bulanan.
	LayoutBulan = "2006-01"
)

// Kunci chip "perlu tindakan" (AP-DSH-03). Dipakai UI untuk memetakan chip ke
// daftar terfilter di riwayat pemesanan.
const (
	ChipMenunggu        = "menunggu"
	ChipPerluReview     = "perlu_review"
	ChipLewatJatuhTempo = "lewat_jatuh_tempo"
	ChipEmailGagal      = "email_gagal"
)

// uuidRe memeriksa format id cabang tanpa menyentuh database.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Filter adalah parameter dashboard dari klien (query string) dalam bentuk
// mentah; normalisasi & validasi ada di service supaya aturannya satu tempat.
type Filter struct {
	// Cabang: kosong/`semua` berarti seluruh cabang, selain itu id cabang.
	Cabang string
	// Periode: hari|minggu|bulan|tahun; kosong berarti DefaultPeriode.
	Periode string
}

// Query adalah filter yang sudah dinormalkan dan siap dieksekusi repository.
type Query struct {
	// BranchID kosong berarti seluruh cabang.
	BranchID string
	// Periode yang sudah ternormalkan (selalu salah satu konstanta Periode*).
	Periode string
	// Dari/Sampai adalah rentang tanggal bisnis periode terpilih (inklusif).
	Dari   time.Time
	Sampai time.Time
	// HariIni adalah tanggal bisnis hari ini (WIB), dasar chip lewat jatuh tempo.
	HariIni time.Time
	// BulanAwal/BulanAkhir adalah rentang bulan bisnis berjalan — dasar tren
	// harian, yang tidak terpengaruh periode terpilih (AP-DSH-04).
	BulanAwal  time.Time
	BulanAkhir time.Time
	// TahunAwal/TahunAkhir adalah rentang tahun bisnis berjalan — dasar tren
	// 12 bulan.
	TahunAwal  time.Time
	TahunAkhir time.Time
	// EmailGagalIDs adalah id pesanan yang punya email `failed`, diserahkan
	// domain notification (satu sumber kebenaran dengan daftar riwayat).
	EmailGagalIDs []string
}

// HitunganPeriode adalah hasil agregat periode terpilih.
type HitunganPeriode struct {
	Jumlah int64
	Nilai  int64

	TerverifikasiJumlah int64
	TerverifikasiNilai  int64
	MenungguJumlah      int64
	MenungguNilai       int64
	PerluReviewJumlah   int64
	PerluReviewNilai    int64
}

// HitunganChip adalah hasil agregat chip "perlu tindakan".
type HitunganChip struct {
	Menunggu        int64
	PerluReview     int64
	LewatJatuhTempo int64
	EmailGagal      int64
}

// Titik adalah satu titik deret waktu (bulan `YYYY-MM` atau hari `YYYY-MM-DD`).
type Titik struct {
	Kunci  string
	Jumlah int64
	Nilai  int64
}

// BarisPeringkat adalah satu baris peringkat cabang.
type BarisPeringkat struct {
	BranchID string
	Nama     string
	Jumlah   int64
	Nilai    int64
}

// Repository membaca metrik dashboard. Tidak ada parameter branch_id sesi di
// sini: pembatasan cabang hanyalah pilihan filter (otorisasi ada di service).
type Repository interface {
	Ringkasan(ctx context.Context, q Query) (HitunganPeriode, error)
	Chip(ctx context.Context, q Query) (HitunganChip, error)
	TrenBulanan(ctx context.Context, q Query) ([]Titik, error)
	Harian(ctx context.Context, q Query) ([]Titik, error)
	PeringkatCabang(ctx context.Context, q Query) ([]BarisPeringkat, error)
}

// SumberEmailGagal menyajikan id pesanan yang punya email berstatus gagal
// (dimiliki domain notification). Dipakai chip & filter "email gagal" tanpa
// domain dashboard membaca tabel email_outbox langsung.
type SumberEmailGagal interface {
	PesananEmailGagal(ctx context.Context) ([]string, error)
}

// --- Bentuk respons JSON -------------------------------------------------

// Metrik adalah satu angka metrik yang berbasis role: `jumlah` selalu ada,
// sedangkan `nilai` (rupiah) HANYA ada untuk Superadmin.
type Metrik struct {
	Jumlah int64  `json:"jumlah"`
	Nilai  *int64 `json:"nilai,omitempty"`
}

// RingkasanPembayaran adalah ringkasan status pembayaran periode terpilih
// (AP-DSH-05). Ketiga keranjangnya saling lepas.
type RingkasanPembayaran struct {
	Terverifikasi Metrik `json:"terverifikasi"`
	Menunggu      Metrik `json:"menunggu"`
	PerluReview   Metrik `json:"perlu_review"`
}

// TitikTren adalah satu batang grafik (bulan atau hari).
type TitikTren struct {
	Kunci  string `json:"kunci"`
	Label  string `json:"label"`
	Jumlah int64  `json:"jumlah"`
	Nilai  *int64 `json:"nilai,omitempty"`
}

// PeringkatCabang adalah satu baris peringkat cabang (AP-DSH-06).
type PeringkatCabang struct {
	BranchID string `json:"branch_id"`
	Nama     string `json:"nama"`
	Jumlah   int64  `json:"jumlah"`
	Nilai    *int64 `json:"nilai,omitempty"`
}

// Chip adalah satu chip "perlu tindakan" (AP-DSH-03).
type Chip struct {
	Kunci  string `json:"kunci"`
	Label  string `json:"label"`
	Jumlah int64  `json:"jumlah"`
}

// InfoFilter mengembalikan filter yang benar-benar dipakai server, termasuk
// rentang tanggal bisnis hasil normalisasi, supaya UI bisa menampilkannya.
type InfoFilter struct {
	Cabang  string `json:"cabang,omitempty"`
	Periode string `json:"periode"`
	Dari    string `json:"dari"`
	Sampai  string `json:"sampai"`
	HariIni string `json:"hari_ini"`
	Zona    string `json:"zona"`
}

// Dashboard adalah satu respons dashboard lengkap.
type Dashboard struct {
	Filter          InfoFilter          `json:"filter"`
	Ringkasan       Metrik              `json:"ringkasan"`
	Pembayaran      RingkasanPembayaran `json:"pembayaran"`
	Chip            []Chip              `json:"chip"`
	TrenBulanan     []TitikTren         `json:"tren_bulanan"`
	Harian          []TitikTren         `json:"harian"`
	PeringkatCabang []PeringkatCabang   `json:"peringkat_cabang"`
}

// Service adalah logika bisnis dashboard admin.
type Service struct {
	repo  Repository
	gagal SumberEmailGagal
	clk   clock.Clock
}

// Option menyesuaikan Service.
type Option func(*Service)

// WithEmailGagal memasang sumber id pesanan ber-email gagal. Tanpa sumber ini
// chip email gagal selalu nol dan daftar terfilternya kosong (fail closed),
// bukan seolah-olah semua pesanan normal.
func WithEmailGagal(s SumberEmailGagal) Option {
	return func(v *Service) { v.gagal = s }
}

// WithClock memasang sumber waktu (dipakai pengujian).
func WithClock(c clock.Clock) Option {
	return func(v *Service) {
		if c != nil {
			v.clk = c
		}
	}
}

// NewService membuat service dashboard.
func NewService(repo Repository, opts ...Option) *Service {
	s := &Service{repo: repo, clk: clock.System()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Get menyusun seluruh metrik dashboard untuk aktor & filter yang diberikan.
func (s *Service) Get(ctx context.Context, actor auth.Actor, f Filter) (Dashboard, error) {
	if err := dashboardActor(actor); err != nil {
		return Dashboard{}, err
	}
	q, err := s.query(f)
	if err != nil {
		return Dashboard{}, err
	}
	ids, err := s.emailGagal(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	q.EmailGagalIDs = ids

	agregat, err := s.repo.Ringkasan(ctx, q)
	if err != nil {
		return Dashboard{}, fmt.Errorf("ringkasan dashboard: %w", err)
	}
	chip, err := s.repo.Chip(ctx, q)
	if err != nil {
		return Dashboard{}, fmt.Errorf("chip dashboard: %w", err)
	}
	trenRows, err := s.repo.TrenBulanan(ctx, q)
	if err != nil {
		return Dashboard{}, fmt.Errorf("tren bulanan dashboard: %w", err)
	}
	harianRows, err := s.repo.Harian(ctx, q)
	if err != nil {
		return Dashboard{}, fmt.Errorf("tren harian dashboard: %w", err)
	}
	peringkatRows, err := s.repo.PeringkatCabang(ctx, q)
	if err != nil {
		return Dashboard{}, fmt.Errorf("peringkat cabang dashboard: %w", err)
	}

	// Nilai rupiah hanya untuk Superadmin (AP-DSH-02). Bila aktornya tidak
	// berhak, pointer nilai tetap nil sehingga field tidak pernah dikirim.
	uang := actor.Can(auth.CapAdminDashboardValue)
	metrik := func(jumlah, nilai int64) Metrik {
		m := Metrik{Jumlah: jumlah}
		if uang {
			v := nilai
			m.Nilai = &v
		}
		return m
	}
	titik := func(t Titik) TitikTren {
		out := TitikTren{Kunci: t.Kunci, Jumlah: t.Jumlah, Label: labelTitik(t.Kunci)}
		if uang {
			v := t.Nilai
			out.Nilai = &v
		}
		return out
	}

	d := Dashboard{
		Filter: InfoFilter{
			Cabang:  q.BranchID,
			Periode: q.Periode,
			Dari:    q.Dari.Format(LayoutTanggal),
			Sampai:  q.Sampai.Format(LayoutTanggal),
			HariIni: q.HariIni.Format(LayoutTanggal),
			Zona:    ZonaBisnis,
		},
		Ringkasan: metrik(agregat.Jumlah, agregat.Nilai),
		Pembayaran: RingkasanPembayaran{
			Terverifikasi: metrik(agregat.TerverifikasiJumlah, agregat.TerverifikasiNilai),
			Menunggu:      metrik(agregat.MenungguJumlah, agregat.MenungguNilai),
			PerluReview:   metrik(agregat.PerluReviewJumlah, agregat.PerluReviewNilai),
		},
		Chip: []Chip{
			{Kunci: ChipMenunggu, Label: "menunggu verifikasi", Jumlah: chip.Menunggu},
			{Kunci: ChipPerluReview, Label: "perlu review", Jumlah: chip.PerluReview},
			{Kunci: ChipLewatJatuhTempo, Label: "C&C lewat jatuh tempo", Jumlah: chip.LewatJatuhTempo},
			{Kunci: ChipEmailGagal, Label: "email gagal", Jumlah: chip.EmailGagal},
		},
	}

	d.TrenBulanan = isiBulanan(trenRows, q, titik)
	d.Harian = isiHarian(harianRows, q, titik)
	d.PeringkatCabang = make([]PeringkatCabang, 0, len(peringkatRows))
	for _, p := range peringkatRows {
		row := PeringkatCabang{BranchID: p.BranchID, Nama: p.Nama, Jumlah: p.Jumlah}
		if uang {
			v := p.Nilai
			row.Nilai = &v
		}
		d.PeringkatCabang = append(d.PeringkatCabang, row)
	}
	return d, nil
}

// dashboardActor menegakkan PRD §2.1: hanya admin Pusat ber-kapabilitas
// `admin.dashboard` (Manager maupun Superadmin) yang boleh membuka dashboard;
// akun cabang dijawab 403.
func dashboardActor(actor auth.Actor) error {
	if !actor.IsAdmin() || !actor.Can(auth.CapAdminDashboard) {
		return ErrForbidden
	}
	return nil
}

// query menormalkan Filter menjadi Query atau mengembalikan ErrFilterInvalid.
func (s *Service) query(f Filter) (Query, error) {
	cabang := strings.TrimSpace(f.Cabang)
	switch strings.ToLower(cabang) {
	case "", "semua", "all":
		cabang = ""
	}
	if cabang != "" && !uuidRe.MatchString(cabang) {
		return Query{}, fmt.Errorf("%w: cabang %q bukan id yang sah", ErrFilterInvalid, f.Cabang)
	}

	periode := strings.ToLower(strings.TrimSpace(f.Periode))
	if periode == "" {
		periode = DefaultPeriode
	}
	switch periode {
	case PeriodeHari, PeriodeMinggu, PeriodeBulan, PeriodeTahun:
	default:
		return Query{}, fmt.Errorf("%w: periode %q tidak dikenal", ErrFilterInvalid, f.Periode)
	}

	hari := clock.TodayWIB(s.clk)
	dari, sampai := rentang(periode, hari)
	bulanAwal := time.Date(hari.Year(), hari.Month(), 1, 0, 0, 0, 0, hari.Location())
	tahunAwal := time.Date(hari.Year(), time.January, 1, 0, 0, 0, 0, hari.Location())

	return Query{
		BranchID:   cabang,
		Periode:    periode,
		Dari:       dari,
		Sampai:     sampai,
		HariIni:    hari,
		BulanAwal:  bulanAwal,
		BulanAkhir: hari,
		TahunAwal:  tahunAwal,
		TahunAkhir: time.Date(hari.Year(), time.December, 31, 0, 0, 0, 0, hari.Location()),
	}, nil
}

// emailGagal mengambil id pesanan ber-email gagal dari domain notification.
func (s *Service) emailGagal(ctx context.Context) ([]string, error) {
	if s.gagal == nil {
		return []string{}, nil
	}
	ids, err := s.gagal.PesananEmailGagal(ctx)
	if err != nil {
		return nil, fmt.Errorf("baca email gagal: %w", err)
	}
	if ids == nil {
		return []string{}, nil
	}
	return ids, nil
}

// rentang menghitung rentang tanggal bisnis satu periode. Perilakunya
// mengikuti prototype (`range()` pada docs/prototype/admin_v2.html): minggu
// mulai Senin, bulan & tahun berjalan berakhir HARI INI (bukan akhir bulan).
func rentang(periode string, hari time.Time) (time.Time, time.Time) {
	switch periode {
	case PeriodeHari:
		return hari, hari
	case PeriodeMinggu:
		// Go: Sunday = 0; kita ingin Senin sebagai awal minggu.
		offset := (int(hari.Weekday()) + 6) % 7
		return hari.AddDate(0, 0, -offset), hari
	case PeriodeTahun:
		return time.Date(hari.Year(), time.January, 1, 0, 0, 0, 0, hari.Location()), hari
	default: // PeriodeBulan
		return time.Date(hari.Year(), hari.Month(), 1, 0, 0, 0, 0, hari.Location()), hari
	}
}

// isiBulanan menyusun 12 batang Januari..Desember tahun bisnis berjalan.
// Bulan yang tidak dipakai diisi nol supaya grafiknya selalu 12 batang
// (prototype menggambar seluruh bulan tahun berjalan).
func isiBulanan(rows []Titik, q Query, ubah func(Titik) TitikTren) []TitikTren {
	perKunci := make(map[string]Titik, len(rows))
	for _, r := range rows {
		perKunci[r.Kunci] = r
	}
	out := make([]TitikTren, 0, 12)
	for bulan := time.January; bulan <= time.December; bulan++ {
		kunci := time.Date(q.TahunAwal.Year(), bulan, 1, 0, 0, 0, 0, q.TahunAwal.Location()).Format(LayoutBulan)
		titik, ada := perKunci[kunci]
		if !ada {
			titik = Titik{Kunci: kunci}
		}
		out = append(out, ubah(titik))
	}
	return out
}

// isiHarian menyusun batang harian bulan bisnis berjalan dari tanggal 1 sampai
// HARI INI; hari tanpa pesanan diisi nol.
func isiHarian(rows []Titik, q Query, ubah func(Titik) TitikTren) []TitikTren {
	perKunci := make(map[string]Titik, len(rows))
	for _, r := range rows {
		perKunci[r.Kunci] = r
	}
	out := make([]TitikTren, 0, 31)
	for t := q.BulanAwal; !t.After(q.BulanAkhir); t = t.AddDate(0, 0, 1) {
		kunci := t.Format(LayoutTanggal)
		titik, ada := perKunci[kunci]
		if !ada {
			titik = Titik{Kunci: kunci}
		}
		out = append(out, ubah(titik))
	}
	return out
}

// labelTitik memberi label pendek yang bisa dibaca langsung oleh grafik:
// nama bulan singkat Indonesia untuk kunci `YYYY-MM`, dan nomor hari untuk
// kunci `YYYY-MM-DD`.
func labelTitik(kunci string) string {
	if len(kunci) == len(LayoutBulan) {
		if t, err := time.Parse(LayoutBulan, kunci); err == nil {
			return bulanSingkat(t.Month())
		}
	}
	if len(kunci) == len(LayoutTanggal) {
		if t, err := time.Parse(LayoutTanggal, kunci); err == nil {
			return fmt.Sprintf("%d", t.Day())
		}
	}
	return kunci
}

// bulanSingkat mengembalikan nama bulan Indonesia 3 huruf.
func bulanSingkat(m time.Month) string {
	nama := [...]string{"Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}
	if m < time.January || m > time.December {
		return ""
	}
	return nama[m-1]
}
