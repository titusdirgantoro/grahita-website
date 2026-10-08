package main

// Perakitan dan penjalanan pekerjaan cadangan terenkripsi (kartu F1-21).
//
// Mode yang memakai berkas ini:
//
//	--mode=backup              menjalankan pekerjaan cadangan sekali lalu keluar
//	--mode=backup-rekonsiliasi membandingkan DB↔object storage (dry-run bawaan)
//	--mode=backup-restore      uji restore ke database anonim (staging)
//
// Worker (--mode=worker) memakai service yang sama lewat job terjadwal
// `backup_jadwal`; halaman Pengaturan admin membacanya lewat HTTP.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/backup"
	backuppg "github.com/grahita/portal/api/internal/backup/postgres"
	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/storage"
)

// buildBackupTarget menyusun tujuan cadangan dari konfigurasi.
func buildBackupTarget(cfg config.Config) (backup.Target, error) {
	switch strings.TrimSpace(cfg.BackupTarget) {
	case "":
		return backup.Nol{}, nil
	case config.BackupTargetDir:
		return backup.NewDir(cfg.BackupDir)
	case config.BackupTargetRclone:
		return &backup.Rclone{
			Biner:   cfg.BackupRcloneBin,
			Remote:  cfg.BackupRcloneRemote,
			Config:  cfg.BackupRcloneConfig,
			Timeout: 30 * time.Minute,
		}, nil
	default:
		return nil, fmt.Errorf("BACKUP_TARGET tidak dikenal: %q", cfg.BackupTarget)
	}
}

// buildBackup menyusun service cadangan lengkap.
func buildBackup(cfg config.Config, pool *database.Pool, store storage.Store, logger *slog.Logger) (*backup.Service, error) {
	target, err := buildBackupTarget(cfg)
	if err != nil {
		return nil, err
	}
	repo := backuppg.New(pool)
	notif := backup.NewPemberitahu(repo, buildMailer(cfg), cfg.AdminBaseURL())
	return backup.New(backup.Config{
		Kerja:          cfg.BackupWorkDir,
		Recipient:      cfg.BackupAgeRecipient,
		Identity:       cfg.BackupAgeIdentity,
		PgDump:         cfg.BackupPgDump,
		PgRestore:      cfg.BackupPgRestore,
		DSN:            cfg.DatabaseURL,
		WALDir:         cfg.BackupWALDir,
		RetensiHarian:  cfg.BackupRetensiHarian,
		RetensiBulanan: cfg.BackupRetensiBulanan,
		RetensiWALHari: cfg.BackupWALHari,
		SweepHapus:     cfg.BackupSweepHapus,
		SweepGrace:     cfg.BackupSweepGrace,
		JamHarian:      cfg.BackupJamWIB,
	}, repo,
		backup.WithTarget(target),
		backup.WithObjectStore(store),
		backup.WithNotifier(notif),
		backup.WithLogger(logger),
	), nil
}

// opsiCadangan menampung flag CLI mode cadangan.
type opsiCadangan struct {
	jenis     string
	sweep     bool
	grace     time.Duration
	dsnTujuan string
	objekDir  string
	berkas    string
	anonim    bool
	bersih    bool
	paksa     bool
	laporan   string
}

// jalankanModeCadangan menjalankan mode cadangan yang dipilih.
func jalankanModeCadangan(ctx context.Context, mode string, cfg config.Config, logger *slog.Logger, o opsiCadangan) error {
	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	store, err := buildStorage(cfg, logger)
	if err != nil {
		return err
	}
	svc, err := buildBackup(cfg, pool, storage.Store(store), logger)
	if err != nil {
		return err
	}

	switch mode {
	case "backup":
		return jalankanCadanganSekali(ctx, svc, o)
	case "backup-rekonsiliasi":
		return jalankanRekonsiliasi(ctx, svc, o)
	case "backup-restore":
		return jalankanUjiRestore(ctx, svc, o)
	default:
		return fmt.Errorf("mode cadangan tidak dikenal: %s", mode)
	}
}

// jenisTerpilih memetakan flag --jenis ke daftar pekerjaan.
func jenisTerpilih(jenis string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(jenis)) {
	case "", "semua", "all":
		return backup.SemuaJenis, nil
	case backup.JenisPostgres:
		return []string{backup.JenisPostgres}, nil
	case backup.JenisStorage:
		return []string{backup.JenisStorage}, nil
	case backup.JenisWAL:
		return []string{backup.JenisWAL}, nil
	default:
		return nil, fmt.Errorf("--jenis tidak dikenal: %q (postgres|storage|wal|semua)", jenis)
	}
}

// jalankanCadanganSekali menjalankan pekerjaan cadangan di luar jadwal
// (dipakai operator saat memulihkan cadangan yang gagal atau saat go-live).
func jalankanCadanganSekali(ctx context.Context, svc *backup.Service, o opsiCadangan) error {
	jenis, err := jenisTerpilih(o.jenis)
	if err != nil {
		return err
	}
	gagal := 0
	for _, j := range jenis {
		h := svc.Jalankan(ctx, j)
		cetakJSON(h)
		if !h.Sukses || h.Err != nil {
			gagal++
		}
	}
	if gagal > 0 {
		return fmt.Errorf("%d dari %d pekerjaan cadangan gagal", gagal, len(jenis))
	}
	return nil
}

// jalankanRekonsiliasi membandingkan DB↔object storage.
func jalankanRekonsiliasi(ctx context.Context, svc *backup.Service, o opsiCadangan) error {
	laporan, err := svc.Rekonsiliasi(ctx, backup.OpsiRekonsiliasi{
		Hapus: o.sweep,
		Grace: o.grace,
	})
	cetakJSON(laporan)
	if err != nil {
		return err
	}
	if laporan.DryRun && len(laporan.KandidatSapu) > 0 {
		logger := slog.Default()
		logger.Warn("ada objek yatim yang bisa disapu (mode lapor); jalankan ulang dengan --sweep untuk menghapus",
			"kandidat", len(laporan.KandidatSapu))
	}
	return nil
}

// jalankanUjiRestore menjalankan uji restore ke lingkungan uji.
func jalankanUjiRestore(ctx context.Context, svc *backup.Service, o opsiCadangan) error {
	opsi := backup.OpsiUjiRestore{
		Berkas:    o.berkas,
		DSNTujuan: o.dsnTujuan,
		ObjekDir:  o.objekDir,
		Anonim:    o.anonim,
		Bersih:    o.bersih,
		Paksa:     o.paksa,
	}
	laporan, err := svc.UjiRestore(ctx, opsi)
	cetakJSON(laporan)
	if o.laporan != "" {
		if errTulis := tulisLaporanJSON(o.laporan, laporan); errTulis != nil {
			return errTulis
		}
		slog.Default().Info("laporan uji restore ditulis", "berkas", o.laporan)
	}
	return err
}

// cetakJSON mencetak hasil dalam satu baris JSON (mudah dibaca operator/CI).
func cetakJSON(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gagal menyusun keluaran JSON:", err)
		return
	}
	fmt.Println(string(raw))
}

// tulisLaporanJSON menyimpan laporan uji restore ke berkas.
func tulisLaporanJSON(jalur string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("susun laporan uji restore: %w", err)
	}
	if err := os.WriteFile(jalur, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("tulis laporan uji restore: %w", err)
	}
	return nil
}
