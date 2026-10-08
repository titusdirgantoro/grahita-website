// Command server adalah satu binary dengan beberapa mode jalan:
//
//	--mode=api          menjalankan HTTP API
//	--mode=worker       menjalankan pemroses job latar belakang
//	--mode=migrate      menjalankan migrasi database lalu keluar
//	--mode=seed         mengisi data contoh untuk development lalu keluar
//	--mode=healthcheck  memanggil /healthz lokal (dipakai Docker healthcheck)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grahita/portal/api/internal/httpapi"
	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/db"
	"github.com/grahita/portal/api/internal/seed"
	"github.com/grahita/portal/api/internal/worker"
)

// Diisi saat build: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	mode := flag.String("mode", "api", "api | worker | migrate | seed | healthcheck")
	flag.Parse()

	if *mode == "healthcheck" {
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck gagal:", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "konfigurasi tidak valid:", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel()}))
	slog.SetDefault(logger)
	logger.Info("starting", "mode", *mode, "env", cfg.AppEnv, "version", version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("tidak bisa terhubung ke database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	switch *mode {
	case "migrate":
		if err := db.Migrate(ctx, pool); err != nil {
			logger.Error("migrasi gagal", "err", err)
			os.Exit(1)
		}
		logger.Info("migrasi selesai")

	case "seed":
		if err := seed.Run(ctx, pool); err != nil {
			logger.Error("seed gagal", "err", err)
			os.Exit(1)
		}
		logger.Info("seed selesai")

	case "worker":
		if err := worker.Run(ctx, pool, cfg); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("worker berhenti karena error", "err", err)
			os.Exit(1)
		}
		logger.Info("worker berhenti dengan rapi")

	case "api":
		if err := runAPI(ctx, pool, cfg, logger); err != nil {
			logger.Error("api berhenti karena error", "err", err)
			os.Exit(1)
		}
		logger.Info("api berhenti dengan rapi")

	default:
		fmt.Fprintf(os.Stderr, "mode tidak dikenal: %s\n", *mode)
		os.Exit(2)
	}
}

func runAPI(ctx context.Context, pool *db.Pool, cfg config.Config, logger *slog.Logger) error {
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewRouter(pool, cfg, version),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// healthcheck dipakai oleh Docker HEALTHCHECK: tidak butuh curl di image.
func healthcheck() error {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1" + addr + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
