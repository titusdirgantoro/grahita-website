package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// kerjaWAL menyalin direktori arsip WAL ke tujuan bila PITR aktif (WALDir
// terisi). Bila PITR belum aktif, pekerjaan ini dicatat sebagai sukses tanpa
// tindakan dengan pesan yang jujur — bukan kegagalan palsu.
//
// Arsip WAL memuat SELURUH perubahan baris — termasuk hash kata sandi dan data
// pelanggan — karena itu setiap segmen DIENKRIPSI age sebelum meninggalkan host
// dan mendarat di tujuan sebagai `wal/<berkas WAL>.age`, persis seperti dump
// (Architecture.md §6: "Backup | Terenkripsi sebelum diunggah ke Google Drive").
// Tanpa kunci publik age pekerjaan ini menolak berjalan (fail closed): tidak ada
// jalur yang menulis WAL polos ke tujuan.
//
// Arsip WAL sendiri disimpan PostgreSQL lewat `archive_command`
// (lihat infra/backup/README.md §6); paket ini hanya mencadangkan hasilnya dan
// memberlakukan retensi 7 hari di tujuan (Architecture.md §7). Jalur pemulihan
// (drill/PITR) ada di PulihkanWAL.
func (s *Service) kerjaWAL(ctx context.Context) (int64, string, string, error) {
	if strings.TrimSpace(s.cfg.WALDir) == "" {
		return 0, "", "PITR tidak aktif (BACKUP_WAL_DIR kosong): arsip WAL tidak dicadangkan", nil
	}
	if err := s.targetSiap(); err != nil {
		return 0, "", "", err
	}
	if err := s.kripto.SiapEnkripsi(); err != nil {
		return 0, "", "", err
	}
	info, err := os.Stat(s.cfg.WALDir)
	if err != nil {
		return 0, "", "", fmt.Errorf("direktori arsip WAL tidak terbaca: %w", err)
	}
	if !info.IsDir() {
		return 0, "", "", fmt.Errorf("BACKUP_WAL_DIR bukan direktori: %s", s.cfg.WALDir)
	}
	entri, err := os.ReadDir(s.cfg.WALDir)
	if err != nil {
		return 0, "", "", fmt.Errorf("baca direktori arsip WAL: %w", err)
	}
	if err := os.MkdirAll(s.cfg.Kerja, 0o700); err != nil {
		return 0, "", "", fmt.Errorf("siapkan direktori kerja: %w", err)
	}
	ada, err := s.target.Daftar(ctx, PrefixWAL)
	if err != nil {
		return 0, "", "", fmt.Errorf("daftar arsip WAL di tujuan: %w", err)
	}
	sudahAda := map[string]bool{}
	polosDiTujuan := []string{}
	for _, b := range ada {
		nama := filepath.ToSlash(b.Nama)
		if strings.HasSuffix(nama, SufiksAge) {
			sudahAda[nama] = true
			continue
		}
		// Arsip WAL POLOS (tanpa `.age`) berasal dari jalur versi lama: ia
		// DILAPORKAN kepada operator, tidak pernah dipakai, ditimpa, maupun
		// ditambah oleh pekerjaan ini.
		polosDiTujuan = append(polosDiTujuan, nama)
	}

	var total int64
	baru, dilewati := 0, 0
	for _, e := range entri {
		if e.IsDir() {
			continue
		}
		lokal := filepath.Join(s.cfg.WALDir, e.Name())
		if _, err := os.Stat(lokal); err != nil {
			return total, "", "", fmt.Errorf("periksa berkas WAL %s: %w", e.Name(), err)
		}
		nama := NamaWALTerEnkripsi(e.Name())
		if sudahAda[nama] {
			// Ukuran berkas di tujuan TIDAK bisa dibandingkan dengan berkas
			// polos lokal (isinya hasil enkripsi), jadi keberadaannya dipakai
			// sebagai penanda segmen ini sudah terunggah utuh (Upload bersifat
			// atomik: berkas sementara lalu rename / satu aliran rclone rcat).
			dilewati++
			continue
		}
		ukuran, err := s.unggahWALTerEnkripsi(ctx, lokal, nama)
		if err != nil {
			return total, "", "", err
		}
		baru++
		total += ukuran
	}

	pesanPolos := ""
	if len(polosDiTujuan) > 0 {
		pesanPolos = fmt.Sprintf("; PERINGATAN %d berkas WAL POLOS (tanpa .age) di tujuan dari jalur versi lama: %s — unduh/enkripsi manual lalu hapus; pekerjaan ini tidak pernah memakai atau menimpanya",
			len(polosDiTujuan), strings.Join(batasi(polosDiTujuan, 5), ", "))
	}
	pesanRetensi := s.retensiWAL(ctx, s.clk.Now())
	return total, s.target.Nama() + ":" + PrefixWAL,
		fmt.Sprintf("arsip WAL: %d berkas terenkripsi baru (%d byte terenkripsi), %d sudah ada%s%s",
			baru, total, dilewati, pesanPolos, pesanRetensi), nil
}

// unggahWALTerEnkripsi mengenkripsi satu segmen WAL ke berkas sementara DI
// DIREKTORI KERJA (WAL polos tidak pernah ditulis ke tujuan), mengunggahnya,
// memverifikasi keberadaannya di tujuan, lalu menghapus berkas sementara itu.
func (s *Service) unggahWALTerEnkripsi(ctx context.Context, lokal, nama string) (int64, error) {
	// Nama sementara unik (sufiks waktu): dua pekerjaan cadangan yang berjalan
	// bersamaan (mis. worker + operator menjalankan ulang manual) tidak boleh
	// saling menimpa berkas sementara di direktori kerja.
	terenkripsi := filepath.Join(s.cfg.Kerja, "wal-"+filepath.Base(nama)+"."+strconvNano(s.clk.Now()))
	defer func() { _ = os.Remove(terenkripsi) }()
	if _, err := s.kripto.EnkripsiBerkas(terenkripsi, lokal); err != nil {
		return 0, fmt.Errorf("enkripsi berkas WAL %s: %w", filepath.Base(lokal), err)
	}
	info, err := os.Stat(terenkripsi)
	if err != nil {
		return 0, fmt.Errorf("periksa berkas WAL terenkripsi: %w", err)
	}
	f, err := os.Open(terenkripsi)
	if err != nil {
		return 0, fmt.Errorf("buka berkas WAL terenkripsi: %w", err)
	}
	errUnggah := s.target.Upload(ctx, nama, f, info.Size())
	_ = f.Close()
	if errUnggah != nil {
		return 0, fmt.Errorf("unggah berkas WAL %s: %w", filepath.Base(lokal), errUnggah)
	}
	if err := s.verifikasiTerenkripsi(ctx, nama, info.Size()); err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// LaporanPulihWAL merangkum pemulihan arsip WAL ke direktori lokal (jalur
// drill/PITR).
type LaporanPulihWAL struct {
	Mulai      time.Time `json:"mulai"`
	Selesai    time.Time `json:"selesai"`
	DirTujuan  string    `json:"dir_tujuan"`
	Diperiksa  int       `json:"diperiksa"`
	Dipulihkan int       `json:"dipulihkan"`
	Dilewati   []string  `json:"dilewati,omitempty"`
	Berkas     []string  `json:"berkas,omitempty"`
}

// PulihkanWAL mendekripsi seluruh arsip WAL di tujuan ke sebuah direktori
// lokal: inilah jalur drill/PITR (README §6) — `restore_command` pada server
// pemulihan menunjuk ke direktori hasil dekripsi ini.
//
// Setiap berkas WAJIB merupakan hasil enkripsi age. Berkas yang tidak
// berheader age DITOLAK (dekripsi gagal, seluruh pemulihan dibatalkan) dan
// berkas polos tanpa sufiks `.age` hanya dilaporkan di `dilewati` — tidak
// pernah disalin apa adanya ke direktori pemulihan.
func (s *Service) PulihkanWAL(ctx context.Context, dirTujuan string) (LaporanPulihWAL, error) {
	laporan := LaporanPulihWAL{Mulai: s.clk.Now().UTC()}
	if err := s.targetSiap(); err != nil {
		return laporan, err
	}
	if err := s.kripto.SiapDekripsi(); err != nil {
		return laporan, err
	}
	if strings.TrimSpace(dirTujuan) == "" {
		return laporan, errors.New("direktori tujuan pemulihan WAL wajib diisi (flag --dir-wal)")
	}
	if err := os.MkdirAll(dirTujuan, 0o700); err != nil {
		return laporan, fmt.Errorf("siapkan direktori pemulihan WAL: %w", err)
	}
	daftar, err := s.target.Daftar(ctx, PrefixWAL)
	if err != nil {
		return laporan, fmt.Errorf("daftar arsip WAL di tujuan: %w", err)
	}
	laporan.DirTujuan = dirTujuan
	for _, b := range daftar {
		nama := filepath.ToSlash(b.Nama)
		if !strings.HasSuffix(nama, SufiksAge) {
			laporan.Dilewati = append(laporan.Dilewati,
				fmt.Sprintf("%s: bukan arsip terenkripsi (.age) — WAL polos di tujuan butuh tindakan operator, tidak dipulihkan", nama))
			continue
		}
		laporan.Diperiksa++
		rc, err := s.target.Buka(ctx, nama)
		if err != nil {
			return laporan, fmt.Errorf("buka arsip WAL %s: %w", nama, err)
		}
		polos := filepath.Base(strings.TrimSuffix(nama, SufiksAge))
		keluaran := filepath.Join(dirTujuan, polos)
		n, err := s.kripto.DekripsiPembacaKeBerkas(rc, keluaran)
		_ = rc.Close()
		if err != nil {
			return laporan, fmt.Errorf("dekripsi arsip WAL %s: %w", nama, err)
		}
		laporan.Dipulihkan++
		laporan.Berkas = append(laporan.Berkas, fmt.Sprintf("%s (%d byte)", polos, n))
	}
	if laporan.Diperiksa == 0 {
		return laporan, errors.New("tidak ada arsip WAL terenkripsi di tujuan")
	}
	laporan.Selesai = s.clk.Now().UTC()
	return laporan, nil
}

// retensiWAL menghapus arsip WAL di tujuan yang lebih tua dari RetensiWALHari
// (Architecture.md §7: 7 hari). Yang dihapus adalah salinan di tujuan —
// sumbernya di server PostgreSQL tidak pernah disentuh pekerjaan ini.
//
// Berkas yang BUKAN arsip terenkripsi milik pekerjaan ini (mis. WAL polos dari
// jalur versi lama, atau berkas asing yang diletakkan operator) selalu
// disimpan, sesuai doktrin retensi paket ini: "retensi tidak pernah menghapus
// sesuatu yang bukan buatannya sendiri". Operator memutuskan sendiri nasibnya.
func (s *Service) retensiWAL(ctx context.Context, sekarang time.Time) string {
	ada, err := s.target.Daftar(ctx, PrefixWAL)
	if err != nil {
		return fmt.Sprintf("; retensi WAL dilewati: %v", err)
	}
	batas := sekarang.Add(-time.Duration(s.cfg.RetensiWALHari) * 24 * time.Hour)
	dihapus := 0
	bagian := ""
	for _, b := range ada {
		if !strings.HasSuffix(b.Nama, SufiksAge) {
			continue
		}
		if b.ModTime.IsZero() || !b.ModTime.Before(batas) {
			continue
		}
		if err := s.target.Hapus(ctx, b.Nama); err != nil {
			bagian += fmt.Sprintf("; gagal hapus WAL %s: %v", b.Nama, err)
			continue
		}
		dihapus++
	}
	return fmt.Sprintf("; retensi WAL: hapus %d berkas%s", dihapus, bagian)
}
