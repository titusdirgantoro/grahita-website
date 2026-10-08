package backup

import (
	"context"
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
// Arsip WAL sendiri disimpan PostgreSQL lewat `archive_command`
// (lihat infra/backup/README.md); paket ini hanya mencadangkan hasilnya dan
// memberlakukan retensi 7 hari di tujuan (Architecture.md §7).
func (s *Service) kerjaWAL(ctx context.Context) (int64, string, string, error) {
	if strings.TrimSpace(s.cfg.WALDir) == "" {
		return 0, "", "PITR tidak aktif (BACKUP_WAL_DIR kosong): arsip WAL tidak dicadangkan", nil
	}
	if err := s.targetSiap(); err != nil {
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
	ada, err := s.target.Daftar(ctx, PrefixWAL)
	if err != nil {
		return 0, "", "", fmt.Errorf("daftar arsip WAL di tujuan: %w", err)
	}
	ukuranDiTujuan := map[string]int64{}
	for _, b := range ada {
		ukuranDiTujuan[filepath.ToSlash(b.Nama)] = b.Ukuran
	}

	var total int64
	baru := 0
	for _, e := range entri {
		if e.IsDir() {
			continue
		}
		lokal := filepath.Join(s.cfg.WALDir, e.Name())
		st, err := os.Stat(lokal)
		if err != nil {
			return total, "", "", fmt.Errorf("periksa berkas WAL %s: %w", e.Name(), err)
		}
		nama := PrefixWAL + e.Name()
		if ukuran, sudah := ukuranDiTujuan[nama]; sudah && ukuran == st.Size() {
			continue
		}
		f, err := os.Open(lokal)
		if err != nil {
			return total, "", "", fmt.Errorf("buka berkas WAL %s: %w", e.Name(), err)
		}
		errUnggah := s.target.Upload(ctx, nama, f, st.Size())
		_ = f.Close()
		if errUnggah != nil {
			return total, "", "", fmt.Errorf("unggah berkas WAL %s: %w", e.Name(), errUnggah)
		}
		baru++
		total += st.Size()
	}

	pesanRetensi := s.retensiWAL(ctx, s.clk.Now())
	return total, s.target.Nama() + ":" + PrefixWAL,
		fmt.Sprintf("arsip WAL: %d berkas baru (%d byte), %d sudah ada%s", baru, total, len(entri)-baru, pesanRetensi), nil
}

// retensiWAL menghapus arsip WAL di tujuan yang lebih tua dari RetensiWALHari
// (Architecture.md §7: 7 hari).
func (s *Service) retensiWAL(ctx context.Context, sekarang time.Time) string {
	ada, err := s.target.Daftar(ctx, PrefixWAL)
	if err != nil {
		return fmt.Sprintf("; retensi WAL dilewati: %v", err)
	}
	batas := sekarang.Add(-time.Duration(s.cfg.RetensiWALHari) * 24 * time.Hour)
	dihapus := 0
	bagian := ""
	for _, b := range ada {
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
