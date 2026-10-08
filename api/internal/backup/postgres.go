package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// NamaCadanganPostgres menyusun nama berkas cadangan harian:
// `postgres/grahita-YYYY-MM-DD.dump.age` (tanggal bisnis WIB).
func NamaCadanganPostgres(t time.Time) string {
	return PrefixPostgres + "grahita-" + t.Format("2006-01-02") + ".dump.age"
}

// NamaRingkasanPostgres menyusun nama berkas ringkasan (potret jumlah baris)
// yang menemani satu dump.
func NamaRingkasanPostgres(t time.Time) string {
	return PrefixPostgres + "grahita-" + t.Format("2006-01-02") + ".ringkasan.json"
}

// RingkasanCadangan adalah potret jumlah baris tabel inti pada saat dump
// dibuat: penghitungan SEBELUM dan SESUDAH dump mengambil snapshot. Dump
// konsisten ada di antara keduanya, sehingga uji restore bisa memverifikasi
// jumlah baris hasil restore dengan tepat — tanpa keliru menyalahkan data yang
// memang berubah setelah dump (pesanan/audit baru masuk setiap jam).
type RingkasanCadangan struct {
	Versi   int              `json:"versi"`
	Berkas  string           `json:"berkas"`
	Dibuat  time.Time        `json:"dibuat"`
	Sebelum map[string]int64 `json:"sebelum"`
	Sesudah map[string]int64 `json:"sesudah"`
}

// SimpanRingkasan mengunggah ringkasan satu dump ke tujuan cadangan.
func SimpanRingkasan(ctx context.Context, t Target, r RingkasanCadangan) error {
	r.Versi = 1
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("susun ringkasan cadangan: %w", err)
	}
	nama := NamaRingkasanPostgres(r.BerkasWaktu())
	if err := t.Upload(ctx, nama, strings.NewReader(string(raw)), int64(len(raw))); err != nil {
		return fmt.Errorf("unggah ringkasan cadangan: %w", err)
	}
	return nil
}

// BerkasWaktu mengambil tanggal bisnis dari nama berkas dump pasangan.
func (r RingkasanCadangan) BerkasWaktu() time.Time {
	if tanggal, ok := tanggalDariNamaCadangan(r.Berkas); ok {
		return tanggal
	}
	return r.Dibuat
}

// MuatRingkasan membaca ringkasan pasangan satu dump. Dikembalikan (ringkasan,
// true) bila ada; (kosong, false) bila tujuan belum memilikinya (cadangan dari
// versi lama) — bukan kegagalan.
func MuatRingkasan(ctx context.Context, t Target, namaDump string) (RingkasanCadangan, bool, error) {
	tanggal, ok := tanggalDariNamaCadangan(namaDump)
	if !ok {
		return RingkasanCadangan{}, false, nil
	}
	rc, err := t.Buka(ctx, NamaRingkasanPostgres(tanggal))
	if err != nil {
		return RingkasanCadangan{}, false, nil
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return RingkasanCadangan{}, false, fmt.Errorf("baca ringkasan cadangan: %w", err)
	}
	var r RingkasanCadangan
	if err := json.Unmarshal(raw, &r); err != nil {
		return RingkasanCadangan{}, false, fmt.Errorf("urai ringkasan cadangan: %w", err)
	}
	return r, true, nil
}

// targetSiap memastikan tujuan cadangan sudah dikonfigurasi.
func (s *Service) targetSiap() error {
	if s.target == nil {
		return ErrTujuanBelumDiatur
	}
	if _, kosong := s.target.(Nol); kosong {
		return ErrTujuanBelumDiatur
	}
	if s.target.Nama() == "" {
		return ErrTujuanBelumDiatur
	}
	return nil
}

// kerjaPostgres menjalankan satu siklus cadangan database:
// pg_dump -Fc → enkripsi age → unggah → retensi harian/bulanan.
func (s *Service) kerjaPostgres(ctx context.Context) (int64, string, string, error) {
	if err := s.targetSiap(); err != nil {
		return 0, "", "", err
	}
	if err := s.kripto.SiapEnkripsi(); err != nil {
		return 0, "", "", err
	}
	koneksi, err := KoneksiDariDSN(s.cfg.DSN)
	if err != nil {
		return 0, "", "", err
	}
	if err := os.MkdirAll(s.cfg.Kerja, 0o700); err != nil {
		return 0, "", "", fmt.Errorf("siapkan direktori kerja: %w", err)
	}

	polos := filepath.Join(s.cfg.Kerja, "grahita-"+strconvNano(time.Now())+".dump")
	terenkripsi := polos + ".age"
	defer func() {
		_ = os.Remove(polos)
		_ = os.Remove(terenkripsi)
	}()

	// Potret jumlah baris SEBELUM dump: dipakai uji restore untuk memverifikasi
	// hasil pemulihan terhadap dump ini, bukan terhadap database yang terus
	// berubah.
	sebelum, errSebelum := statistikDatabase(ctx, s.cfg.DSN, TabelInti)
	if errSebelum != nil {
		s.log.Warn("potret jumlah baris sebelum dump gagal diambil", "err", errSebelum)
	}

	if err := s.dumper.Dump(ctx, koneksi, polos); err != nil {
		return 0, "", "", err
	}

	sesudah, errSesudah := statistikDatabase(ctx, s.cfg.DSN, TabelInti)
	if errSesudah != nil {
		s.log.Warn("potret jumlah baris sesudah dump gagal diambil", "err", errSesudah)
	}
	ukuranPolos, err := s.kripto.EnkripsiBerkas(terenkripsi, polos)
	if err != nil {
		return 0, "", "", err
	}
	info, err := os.Stat(terenkripsi)
	if err != nil {
		return 0, "", "", fmt.Errorf("periksa berkas cadangan terenkripsi: %w", err)
	}

	nama := NamaCadanganPostgres(s.clk.Now().In(s.cfg.Zona))
	f, err := os.Open(terenkripsi)
	if err != nil {
		return 0, "", "", fmt.Errorf("buka berkas cadangan terenkripsi: %w", err)
	}
	errUnggah := s.target.Upload(ctx, nama, f, info.Size())
	_ = f.Close()
	if errUnggah != nil {
		return 0, "", "", fmt.Errorf("unggah cadangan %s: %w", nama, errUnggah)
	}
	if err := s.verifikasiTerenkripsi(ctx, nama, info.Size()); err != nil {
		return 0, "", "", err
	}

	// Ringkasan potret jumlah baris ikut disimpan bersama dump; tanpa itu uji
	// restore hanya bisa membandingkan dengan database hidup (yang terus
	// berubah) sehingga selisih wajar pun tampak seperti kegagalan.
	pesanRingkasan := ""
	if errSebelum == nil && errSesudah == nil {
		ringkasan := RingkasanCadangan{
			Berkas:  nama,
			Dibuat:  s.clk.Now().UTC(),
			Sebelum: sebelum,
			Sesudah: sesudah,
		}
		if err := SimpanRingkasan(ctx, s.target, ringkasan); err != nil {
			pesanRingkasan = "; PERINGATAN ringkasan potret gagal disimpan: " + err.Error()
		} else {
			pesanRingkasan = "; ringkasan potret disimpan"
		}
	} else {
		pesanRingkasan = "; PERINGATAN potret jumlah baris tidak lengkap (uji restore akan membandingkan dengan database hidup)"
	}

	// Retensi tidak boleh menggagalkan cadangan yang sudah berhasil: kegagalan
	// pembersihan dilaporkan di pesan, bukan membuat cadangan tampak gagal.
	pesanRetensi := s.berlakukanRetensi(ctx, s.clk.Now())

	pesan := fmt.Sprintf("dump %s: %d byte polos, %d byte terenkripsi -> %s%s%s",
		koneksi.Samarkan(), ukuranPolos, info.Size(), nama, pesanRingkasan, pesanRetensi)
	return info.Size(), s.target.Nama() + ":" + nama, pesan, nil
}

// verifikasiTerenkripsi memastikan berkas benar-benar ada di tujuan dengan
// ukuran yang sama. Backup yang tidak bisa dibaca kembali bukan backup.
func (s *Service) verifikasiTerenkripsi(ctx context.Context, nama string, ukuran int64) error {
	daftar, err := s.target.Daftar(ctx, path.Dir(nama)+"/")
	if err != nil {
		return fmt.Errorf("daftar tujuan cadangan: %w", err)
	}
	for _, b := range daftar {
		if b.Nama != nama {
			continue
		}
		if b.Ukuran != ukuran {
			return fmt.Errorf("berkas cadangan %s di tujuan berukuran %d byte, seharusnya %d", nama, b.Ukuran, ukuran)
		}
		return nil
	}
	return fmt.Errorf("berkas cadangan %s tidak ditemukan di tujuan setelah diunggah", nama)
}

// berlakukanRetensi membersihkan cadangan lama sesuai kebijakan
// (Architecture.md §7: 30 harian, 12 bulanan) dan mengembalikan ringkasannya.
func (s *Service) berlakukanRetensi(ctx context.Context, sekarang time.Time) string {
	daftar, err := s.target.Daftar(ctx, PrefixPostgres)
	if err != nil {
		return fmt.Sprintf("; retensi dilewati: %v", err)
	}
	simpan, hapus := SaringRetensi(daftar, s.cfg.RetensiHarian, s.cfg.RetensiBulanan, sekarang)
	bagian := fmt.Sprintf("; retensi: simpan %d, hapus %d", len(simpan), len(hapus))
	for _, b := range hapus {
		if err := s.target.Hapus(ctx, b.Nama); err != nil {
			bagian += fmt.Sprintf("; gagal hapus %s: %v", b.Nama, err)
			continue
		}
		bagian += "; hapus " + b.Nama
	}

	// Berkas ringkasan potret dibersihkan bersama dump pasangannya: ringkasan
	// yang dump-nya sudah di luar jendela retensi tidak berguna lagi.
	simpanTanggal := map[string]bool{}
	for _, b := range simpan {
		if !strings.HasSuffix(b.Nama, ".dump.age") {
			continue
		}
		if tanggal, ok := tanggalDariNamaCadangan(b.Nama); ok {
			simpanTanggal[tanggal.Format("2006-01-02")] = true
		}
	}
	for _, b := range daftar {
		if !strings.HasSuffix(b.Nama, ".ringkasan.json") {
			continue
		}
		tanggal, ok := tanggalDariNamaCadangan(b.Nama)
		if !ok || simpanTanggal[tanggal.Format("2006-01-02")] {
			continue
		}
		if err := s.target.Hapus(ctx, b.Nama); err != nil {
			bagian += fmt.Sprintf("; gagal hapus %s: %v", b.Nama, err)
			continue
		}
		bagian += "; hapus " + b.Nama
	}
	return bagian
}

// SaringRetensi membagi berkas cadangan harian menjadi yang DISIMPAN dan yang
// DIHAPUS.
//
// Aturan (Architecture.md §7): simpan 30 cadangan harian terbaru DAN satu
// cadangan terbaru dari setiap bulan selama 12 bulan. Berkas yang namanya
// tidak dikenali SELALU disimpan — retensi tidak pernah menghapus sesuatu yang
// bukan buatannya sendiri.
func SaringRetensi(daftar []Berkas, retensiHarian, retensiBulanan int, _ time.Time) ([]Berkas, []Berkas) {
	if retensiHarian <= 0 {
		retensiHarian = 30
	}
	if retensiBulanan <= 0 {
		retensiBulanan = 12
	}

	type bertanggal struct {
		berkas  Berkas
		tanggal time.Time
	}
	bertanggalList := make([]bertanggal, 0, len(daftar))
	lindungi := []Berkas{}
	for _, b := range daftar {
		// Berkas ringkasan potret bukan dump: ia dikelola bersama dump
		// pasangannya (lihat berlakukanRetensi), jadi di sini selalu aman.
		if strings.HasSuffix(b.Nama, ".ringkasan.json") {
			lindungi = append(lindungi, b)
			continue
		}
		tanggal, ok := tanggalDariNamaCadangan(b.Nama)
		if !ok {
			lindungi = append(lindungi, b)
			continue
		}
		bertanggalList = append(bertanggalList, bertanggal{berkas: b, tanggal: tanggal})
	}
	sort.Slice(bertanggalList, func(i, j int) bool {
		if bertanggalList[i].tanggal.Equal(bertanggalList[j].tanggal) {
			return bertanggalList[i].berkas.Nama > bertanggalList[j].berkas.Nama
		}
		return bertanggalList[i].tanggal.After(bertanggalList[j].tanggal)
	})

	simpan := append([]Berkas{}, lindungi...)
	terpilih := map[string]bool{}
	for i, b := range bertanggalList {
		if i < retensiHarian {
			terpilih[b.berkas.Nama] = true
		}
	}
	bulan := map[string]bool{}
	for _, b := range bertanggalList {
		kunci := b.tanggal.Format("2006-01")
		if bulan[kunci] {
			continue
		}
		if len(bulan) >= retensiBulanan {
			break
		}
		bulan[kunci] = true
		terpilih[b.berkas.Nama] = true
	}

	hapus := []Berkas{}
	for _, b := range bertanggalList {
		if terpilih[b.berkas.Nama] {
			simpan = append(simpan, b.berkas)
			continue
		}
		hapus = append(hapus, b.berkas)
	}
	sort.Slice(simpan, func(i, j int) bool { return simpan[i].Nama < simpan[j].Nama })
	sort.Slice(hapus, func(i, j int) bool { return hapus[i].Nama < hapus[j].Nama })
	return simpan, hapus
}

// tanggalDariNamaCadangan mengambil tanggal dari nama berkas cadangan
// `postgres/grahita-YYYY-MM-DD.dump.age` atau ringkasan pasangannya
// `postgres/grahita-YYYY-MM-DD.ringkasan.json`.
func tanggalDariNamaCadangan(nama string) (time.Time, bool) {
	dasar := path.Base(nama)
	dasar = strings.TrimSuffix(dasar, ".dump.age")
	dasar = strings.TrimSuffix(dasar, ".ringkasan.json")
	dasar = strings.TrimPrefix(dasar, "grahita-")
	t, err := time.Parse("2006-01-02", dasar)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// BukaCadanganPostgres membuka cadangan harian yang DIMINTA (atau yang
// terbaru bila nama kosong). Dipakai uji restore dan verifikasi.
func (s *Service) BukaCadanganPostgres(ctx context.Context, nama string) (io.ReadCloser, string, error) {
	if err := s.targetSiap(); err != nil {
		return nil, "", err
	}
	if nama == "" {
		daftar, err := s.target.Daftar(ctx, PrefixPostgres)
		if err != nil {
			return nil, "", err
		}
		nama = cadanganTerbaru(daftar)
		if nama == "" {
			return nil, "", ErrCadanganKosong
		}
	}
	rc, err := s.target.Buka(ctx, nama)
	if err != nil {
		return nil, "", err
	}
	return rc, nama, nil
}

// cadanganTerbaru memilih nama berkas cadangan dengan tanggal terbaru.
func cadanganTerbaru(daftar []Berkas) string {
	terbaik := ""
	var terbaikTanggal time.Time
	for _, b := range daftar {
		tanggal, ok := tanggalDariNamaCadangan(b.Nama)
		if !ok {
			continue
		}
		if terbaik == "" || tanggal.After(terbaikTanggal) {
			terbaik, terbaikTanggal = b.Nama, tanggal
		}
	}
	return terbaik
}

// strconvNano adalah sufiks unik untuk berkas kerja sementara.
func strconvNano(t time.Time) string {
	return fmt.Sprintf("%d", t.UnixNano())
}

// errCadanganTanpaObjek menandai pekerjaan objek yang tidak bisa dijalankan.
var errCadanganTanpaObjek = errors.New("object storage belum dikonfigurasi: sinkronisasi bukti/dokumen tidak bisa dijalankan")
