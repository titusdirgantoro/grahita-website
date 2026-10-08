// Command server adalah satu binary dengan beberapa mode jalan:
//
//	--mode=api            menjalankan HTTP API
//	--mode=worker         menjalankan pemroses job latar belakang (River)
//	--mode=migrate        menjalankan migrasi database lalu keluar
//	--mode=migrate-down   menurunkan seluruh migrasi (dipakai uji CI)
//	--mode=seed           mengisi data contoh untuk pengembangan lalu keluar
//	--mode=healthcheck    memanggil /healthz lokal (dipakai Docker healthcheck)
//	--mode=version        mencetak versi build
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

	"github.com/grahita/portal/api/internal/auth"
	authpg "github.com/grahita/portal/api/internal/auth/postgres"
	"github.com/grahita/portal/api/internal/bank"
	bankpg "github.com/grahita/portal/api/internal/bank/postgres"
	"github.com/grahita/portal/api/internal/branch"
	branchpg "github.com/grahita/portal/api/internal/branch/postgres"
	"github.com/grahita/portal/api/internal/catalog"
	catalogpg "github.com/grahita/portal/api/internal/catalog/postgres"
	"github.com/grahita/portal/api/internal/httpapi"
	"github.com/grahita/portal/api/internal/order"
	orderpg "github.com/grahita/portal/api/internal/order/postgres"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/platform/mailer"
	"github.com/grahita/portal/api/internal/platform/pdf"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/grahita/portal/api/internal/registration"
	registrationpg "github.com/grahita/portal/api/internal/registration/postgres"
	"github.com/grahita/portal/api/internal/seed"
	"github.com/grahita/portal/api/internal/settings"
	settingspg "github.com/grahita/portal/api/internal/settings/postgres"
	"github.com/grahita/portal/api/internal/users"
	userspg "github.com/grahita/portal/api/internal/users/postgres"
	"github.com/grahita/portal/api/internal/worker"
)

// version diisi saat build: -ldflags "-X main.version=<sha>".
var version = "dev"

func main() {
	mode := flag.String("mode", "api", "api | worker | migrate | migrate-down | seed | healthcheck | version")
	flag.Parse()

	switch *mode {
	case "version":
		fmt.Println(version)
		return
	case "healthcheck":
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

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevelSlog()}))
	slog.SetDefault(logger)
	logger.Info("menjalankan server", "mode", *mode, "env", cfg.AppEnv, "version", version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *mode, cfg, logger); err != nil {
		logger.Error("berhenti karena error", "mode", *mode, "err", err)
		os.Exit(1)
	}
	logger.Info("berhenti dengan rapi", "mode", *mode)
}

func run(ctx context.Context, mode string, cfg config.Config, logger *slog.Logger) error {
	// Mode migrasi memakai koneksi database/sql sendiri (goose) dan tidak
	// perlu pool pgx.
	switch mode {
	case "migrate":
		if err := database.Up(ctx, cfg.DatabaseURL); err != nil {
			return err
		}
		logger.Info("migrasi selesai")
		return migrateRiver(ctx, cfg, logger)
	case "migrate-down":
		return database.DownTo(ctx, cfg.DatabaseURL, 0)
	case "seed":
		pool, err := database.Connect(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		return seed.Run(ctx, pool)
	case "api", "worker":
	default:
		return fmt.Errorf("mode tidak dikenal: %s", mode)
	}

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if mode == "worker" {
		client, err := worker.New(pool, cfg)
		if err != nil {
			return err
		}
		if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}

	return runAPI(ctx, pool, cfg, logger)
}

// health adalah adapter dependensi /readyz. Ia hidup di composition root
// supaya paket httpapi tidak pernah menyentuh pgx atau goose.
type health struct {
	pool *database.Pool
	dsn  string
}

func (h health) Ping(ctx context.Context) error { return h.pool.Ping(ctx) }

func (h health) MigrationVersion(ctx context.Context) (int64, error) {
	return database.Version(ctx, h.dsn)
}

// runAPI menyusun seluruh dependensi dan menjalankan HTTP API.
func runAPI(ctx context.Context, pool *database.Pool, cfg config.Config, logger *slog.Logger) error {
	authRepo := authpg.New(pool)
	authSvc := auth.NewService(authRepo, authRepo, authRepo, clock.System(), cfg.SessionIdle, cfg.SessionAbsolute)

	store := storage.Store(storage.Disabled{})
	if cfg.S3Endpoint != "" && cfg.S3Bucket != "" {
		// Implementasi S3 asli menyusul bersama domain dokumen; sampai itu,
		// storage dilaporkan "tidak dikonfigurasi" di /readyz.
		logger.Warn("S3_ENDPOINT diisi tetapi implementasi S3 belum tersedia pada tahap ini")
	}

	renderer := pdf.Renderer(pdf.Disabled{})
	if cfg.GotenbergURL != "" {
		renderer = pdf.NewGotenberg(cfg.GotenbergURL)
	}

	mail := mailer.Mailer(mailer.Log{})
	if cfg.SMTPHost != "" {
		mail = mailer.NewSMTP(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, cfg.MailFrom)
	}
	// Tautan atur ulang menunjuk portal asal akun: cabang ke portal cabang,
	// admin ke portal admin (keduanya same-origin dengan API-nya).
	resetURL := auth.NewResetURLBuilder(cfg.CustomerBaseURL(), cfg.AdminBaseURL(), cfg.PasswordResetPath)
	authSvc.SetResetMailer(auth.NewEmailResetMailer(mail, cfg.CustomerBaseURL(), cfg.AdminBaseURL(), cfg.PasswordResetPath))

	// Pengelolaan akun pengguna. Undangan admin TIDAK dikirim langsung: ia
	// diantre lewat EmailQueue yang penulis email_outbox-nya menyusul di F1-16
	// (users.LogQueue hanya mencatat antrean).
	usersSvc := users.NewService(userspg.New(pool), authSvc, authSvc,
		users.WithEmailQueue(users.LogQueue{}),
		users.WithResetURLBuilder(resetURL),
	)

	// Master cabang + token registrasi sekali pakai (F1-06).
	branchSvc := branch.NewService(branchpg.New(pool), clock.System())

	// Pendaftaran akun cabang lewat token registrasi (F1-07).
	registrationSvc := registration.NewService(registrationpg.New(pool), clock.System())

	// Rekening bank tujuan transfer (F1-09).
	bankSvc := bank.NewService(bankpg.New(pool))

	// Pembatas percobaan login selalu aktif dan tidak digantungkan pada
	// CSRF_ENABLED: keduanya tidak berhubungan (PRD CP-AUT-05).
	// 5 percobaan GAGAL per 15 menit, dihitung terpisah per akun dan per IP.
	loginLimiter := httpx.NewRateLimiter(5, 15*time.Minute)
	// Permintaan tautan atur ulang dibatasi per IP (Architecture.md §6).
	forgotLimiter := httpx.NewRateLimiter(5, 15*time.Minute)
	// Validasi token registrasi & pendaftaran akun juga endpoint publik:
	// keduanya dibatasi per IP (Architecture.md §6).
	registrationLimiter := httpx.NewRateLimiter(10, 15*time.Minute)

	// Katalog dipakai kedua portal sekaligus menjadi sumber harga order line
	// domain pesanan: harga selalu diambil dari branch_prices cabang sesi.
	catalogSvc := catalog.NewService(catalogpg.New(pool))
	// Pesanan cabang: draft, editor order line, daftar dan detail (F1-11).
	orderSvc := order.NewService(orderpg.New(pool), catalogSvc)

	handler := httpapi.NewRouter(httpapi.Deps{
		Config:              cfg,
		Version:             version,
		Health:              health{pool: pool, dsn: cfg.DatabaseURL},
		Auth:                authSvc,
		Settings:            settings.NewService(settingspg.New(pool)),
		Catalog:             catalogSvc,
		Orders:              orderSvc,
		Users:               usersSvc,
		Branches:            branchSvc,
		Bank:                bankSvc,
		Registration:        registrationSvc,
		Storage:             store,
		Gotenberg:           renderer,
		Mailer:              mail,
		LoginLimiter:        loginLimiter,
		ForgotLimiter:       forgotLimiter,
		RegistrationLimiter: registrationLimiter,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http listening", "addr", cfg.HTTPAddr, "customer", cfg.CustomerHost, "admin", cfg.AdminHost)
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

// healthcheck dipakai Docker HEALTHCHECK: tidak butuh curl di dalam image.
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
