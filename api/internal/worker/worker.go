// Package worker menjalankan pekerjaan latar belakang.
//
// Saat ini baru berisi dua tugas pemeliharaan yang sudah bisa dijalankan
// (pembersihan sesi & token kedaluwarsa) plus kerangka pemrosesan outbox email.
// Pada tahap berikutnya loop manual ini diganti River (lihat Architecture.md §1),
// sehingga job bisa di-enqueue di dalam transaksi bisnis yang sama.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/db"
)

func Run(ctx context.Context, pool *db.Pool, cfg config.Config) error {
	slog.Info("worker berjalan", "interval", cfg.WorkerInterval.String())

	ticker := time.NewTicker(cfg.WorkerInterval)
	defer ticker.Stop()

	tick(ctx, pool) // jalankan sekali di awal agar tidak menunggu interval pertama

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			tick(ctx, pool)
		}
	}
}

func tick(ctx context.Context, pool *db.Pool) {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if n, err := bersihkanKedaluwarsa(runCtx, pool); err != nil {
		slog.Error("pembersihan gagal", "err", err)
	} else if n > 0 {
		slog.Info("pembersihan selesai", "baris", n)
	}

	if n, err := hitungEmailTertunda(runCtx, pool); err != nil {
		slog.Error("cek outbox gagal", "err", err)
	} else if n > 0 {
		slog.Info("email menunggu dikirim", "jumlah", n)
		// TODO: kirim lewat SMTP, perbarui status & attempts (SystemDesign.md §5.1)
	}
}

// bersihkanKedaluwarsa menghapus sesi dan token yang sudah lewat masa berlakunya.
func bersihkanKedaluwarsa(ctx context.Context, pool *db.Pool) (int64, error) {
	var total int64

	for _, q := range []string{
		`DELETE FROM sessions WHERE expires_at < now() - interval '7 days'`,
		`DELETE FROM password_reset_tokens WHERE expires_at < now() - interval '7 days'`,
	} {
		tag, err := pool.Exec(ctx, q)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

func hitungEmailTertunda(ctx context.Context, pool *db.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM email_outbox
		 WHERE status IN ('queued','sending') AND next_try_at <= now()`).Scan(&n)
	return n, err
}
