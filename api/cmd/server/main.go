// Command server adalah satu binary dengan beberapa mode jalan:
//
//	--mode=api               menjalankan HTTP API
//	--mode=worker            menjalankan pemroses job latar belakang (River)
//	--mode=migrate           menjalankan migrasi database lalu keluar
//	--mode=migrate-down      menurunkan seluruh migrasi (dipakai uji CI)
//	--mode=seed               mengisi data contoh untuk pengembangan lalu keluar
//	--mode=backup             menjalankan cadangan terenkripsi sekali lalu keluar
//	--mode=backup-rekonsiliasi memeriksa DB↔object storage (dry-run bawaan)
//	--mode=backup-restore     uji restore ke database uji (staging anonim)
//	--mode=backup-pulih-wal   dekripsi arsip WAL ke direktori lokal (PITR/drill)
//	--mode=healthcheck        memanggil /healthz lokal (dipakai Docker healthcheck)
//	--mode=version            mencetak versi build
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

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/grahita/portal/api/internal/auth"
	authpg "github.com/grahita/portal/api/internal/auth/postgres"
	"github.com/grahita/portal/api/internal/backup"
	"github.com/grahita/portal/api/internal/bank"
	bankpg "github.com/grahita/portal/api/internal/bank/postgres"
	"github.com/grahita/portal/api/internal/branch"
	branchpg "github.com/grahita/portal/api/internal/branch/postgres"
	"github.com/grahita/portal/api/internal/catalog"
	catalogpg "github.com/grahita/portal/api/internal/catalog/postgres"
	"github.com/grahita/portal/api/internal/dashboard"
	dashboardpg "github.com/grahita/portal/api/internal/dashboard/postgres"
	"github.com/grahita/portal/api/internal/document"
	documentpg "github.com/grahita/portal/api/internal/document/postgres"
	"github.com/grahita/portal/api/internal/fulfillment"
	"github.com/grahita/portal/api/internal/httpapi"
	"github.com/grahita/portal/api/internal/notification"
	notificationpg "github.com/grahita/portal/api/internal/notification/postgres"
	"github.com/grahita/portal/api/internal/order"
	orderpg "github.com/grahita/portal/api/internal/order/postgres"
	"github.com/grahita/portal/api/internal/payment"
	paymentpg "github.com/grahita/portal/api/internal/payment/postgres"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/platform/mailer"
	"github.com/grahita/portal/api/internal/platform/observability"
	"github.com/grahita/portal/api/internal/platform/pdf"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/grahita/portal/api/internal/registration"
	registrationpg "github.com/grahita/portal/api/internal/registration/postgres"
	"github.com/grahita/portal/api/internal/report"
	reportpg "github.com/grahita/portal/api/internal/report/postgres"
	"github.com/grahita/portal/api/internal/seed"
	"github.com/grahita/portal/api/internal/settings"
	settingspg "github.com/grahita/portal/api/internal/settings/postgres"
	"github.com/grahita/portal/api/internal/tutorial"
	tutorialpg "github.com/grahita/portal/api/internal/tutorial/postgres"
	"github.com/grahita/portal/api/internal/users"
	userspg "github.com/grahita/portal/api/internal/users/postgres"
	"github.com/grahita/portal/api/internal/worker"
)

// version diisi saat build: -ldflags "-X main.version=<sha>".
var version = "dev"

func main() {
	mode := flag.String("mode", "api", "api | worker | migrate | migrate-down | seed | backup | backup-rekonsiliasi | backup-restore | backup-pulih-wal | healthcheck | version")
	// Flag mode cadangan (F1-21).
	jenis := flag.String("jenis", "semua", "backup: postgres | storage | wal | semua")
	sweep := flag.Bool("sweep", false, "backup-rekonsiliasi: hapus objek yatim (bawaan: hanya melaporkan)")
	grace := flag.Duration("grace", 0, "backup-rekonsiliasi: umur minimum objek yatim (bawaan BACKUP_SWEEP_GRACE)")
	dsnTujuan := flag.String("dsn-tujuan", "", "backup-restore: DSN database uji (bawaan BACKUP_RESTORE_DSN)")
	objekDir := flag.String("objek-dir", "", "backup-restore: direktori pemulihan bukti/dokumen")
	berkas := flag.String("berkas", "", "backup-restore: nama cadangan yang dipakai (bawaan: terbaru)")
	anonim := flag.Bool("anonim", false, "backup-restore: samarkan data pribadi (staging anonim)")
	bersih := flag.Bool("bersih", false, "backup-restore: hapus & buat ulang database tujuan")
	paksa := flag.Bool("paksa", false, "backup-restore: izinkan --bersih pada nama database non-uji")
	laporan := flag.String("laporan", "", "backup-restore|backup-pulih-wal: berkas JSON laporan bukti")
	dirWal := flag.String("dir-wal", "", "backup-pulih-wal: direktori lokal hasil dekripsi arsip WAL (restore_command PITR)")
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

	// Pelaporan error ke Sentry (Architecture.md §8). Opsional: tanpa
	// SENTRY_DSN seluruh pelaporan menjadi no-op, jadi pengembangan lokal dan
	// pengujian tidak mengirim apa pun.
	tutupSentry := func() {}
	if terpasang, err := observability.InitSentry(observability.SentryOptions{
		DSN:              cfg.SentryDSN,
		Environment:      cfg.SentryEnvironment,
		Release:          version,
		TracesSampleRate: cfg.SentrySampleRate,
		Logger:           logger,
	}); err != nil {
		logger.Warn("Sentry tidak terpasang", "err", err)
	} else {
		tutupSentry = terpasang
	}
	defer tutupSentry()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opsi := opsiCadangan{
		jenis:     *jenis,
		sweep:     *sweep,
		grace:     *grace,
		dsnTujuan: pilihDSN(*dsnTujuan, cfg.BackupRestoreDSN),
		objekDir:  *objekDir,
		berkas:    *berkas,
		anonim:    *anonim,
		bersih:    *bersih,
		paksa:     *paksa,
		laporan:   *laporan,
		dirWal:    *dirWal,
	}

	if err := run(ctx, *mode, cfg, logger, opsi); err != nil {
		logger.Error("berhenti karena error", "mode", *mode, "err", err)
		os.Exit(1)
	}
	logger.Info("berhenti dengan rapi", "mode", *mode)
}

// pilihDSN memilih nilai pertama yang terisi (flag lebih diutamakan).
func pilihDSN(flag, env string) string {
	if flag != "" {
		return flag
	}
	return env
}

func run(ctx context.Context, mode string, cfg config.Config, logger *slog.Logger, opsi opsiCadangan) error {
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
	case "backup-pulih-wal":
		// Pemulihan arsip WAL hanya butuh tujuan + kunci age: tidak menyentuh
		// database sama sekali supaya drill/PITR tetap bisa dijalankan saat
		// database sumber tidak tersedia.
		if !cfg.CadanganAktif() {
			return fmt.Errorf("mode %s butuh tujuan cadangan: isi BACKUP_TARGET_DIR atau BACKUP_RCLONE_REMOTE beserta BACKUP_AGE_RECIPIENT", mode)
		}
		return jalankanPulihWALMandiri(ctx, cfg, logger, opsi)
	case "backup", "backup-rekonsiliasi", "backup-restore":
		if !cfg.CadanganAktif() {
			return fmt.Errorf("mode %s butuh tujuan cadangan: isi BACKUP_TARGET_DIR atau BACKUP_RCLONE_REMOTE beserta BACKUP_AGE_RECIPIENT", mode)
		}
		return jalankanModeCadangan(ctx, mode, cfg, logger, opsi)
	case "api", "worker":
	default:
		return fmt.Errorf("mode tidak dikenal: %s", mode)
	}

	pool, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Metrik Prometheus (Architecture.md §8). Dinyalakan secara bawaan dan
	// dapat dimatikan dengan METRICS_ENABLED=false. Sumber angka database
	// diserahkan sebagai interface supaya paket platform tetap bebas pgx.
	var metrik *observability.Metrics
	if cfg.MetricsEnabled {
		metrik = observability.New(observability.Options{
			Version: version,
			Env:     cfg.AppEnv,
			Sumber:  sumberStatistik{pool: pool},
			Mounts:  cfg.DiskMounts,
			Logger:  logger,
		})
		go metrik.JalankanPemantauan(ctx, cfg.MetricsInterval)
	}

	if mode == "worker" {
		// Object storage juga dipasang di worker: pengiriman lampiran dokumen
		// (F1-16), render Invoice/Surat Jalan setelah pesanan selesai (F1-18),
		// dan sinkronisasi cadangan (F1-21) membacanya di sini.
		store, err := buildStorage(cfg, logger)
		if err != nil {
			return err
		}
		notif := buildNotification(pool, cfg, store, buildMailer(cfg))

		// Pekerjaan lanjutan Done (F1-18): render dokumen lalu antre email
		// dokumen. Renderer yang sama dengan mode api (Gotenberg; Disabled
		// berarti job gagal dan diulang sampai mesinnya siap — bukan mengirim
		// email tanpa lampiran).
		renderer := pdf.Renderer(pdf.Disabled{})
		if cfg.GotenbergURL != "" {
			renderer = pdf.NewGotenberg(cfg.GotenbergURL)
		}
		documentSvc := document.NewService(documentpg.New(pool), store, renderer)
		selesai := fulfillment.NewService(documentSvc, notif)

		// Ekspor laporan XLSX (F2-24) dijalankan di worker: berkas ditulis
		// streaming lalu disimpan ke object storage yang sama.
		reportSvc := report.NewService(reportpg.New(pool), store)

		// Service cadangan hanya dijalankan bila tujuan dikonfigurasi; tanpa
		// itu tidak ada job jadwal cadangan yang didaftarkan sama sekali
		// (bukan job yang selalu gagal).
		var bk *backup.Service
		if cfg.CadanganAktif() {
			if bk, err = buildBackup(cfg, pool, store, logger); err != nil {
				return err
			}
			logger.Info("jadwal cadangan aktif", "tujuan", bk.Target().Nama(), "jadwal", bk.Jadwal())
		}

		client, err := worker.New(pool, cfg, notif, bk, selesai, reportSvc, worker.DenganPencatatJob(metrik))
		if err != nil {
			return err
		}
		// Worker tidak melayani HTTP selain /metrics: Prometheus menjangkaunya
		// di WORKER_METRICS_ADDR (tidak dipublikasikan ke host).
		if metrik != nil {
			hentikan, err := jalankanMetrikWorker(ctx, cfg, logger, metrik)
			if err != nil {
				return err
			}
			defer hentikan()
		}
		if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}

	return runAPI(ctx, pool, cfg, logger, metrik)
}

// buildStorage menyusun object storage S3-compatible dari konfigurasi.
//
// Storage belum dikonfigurasi berarti unggah bukti/dokumen akan ditolak
// (503) — bukan diam-diam disimpan ke memori yang hilang saat proses
// berhenti. Konfigurasi yang diisi SETENGAH (endpoint ada, kredensial tidak)
// justru menghentikan start: lebih baik gagal terang-terangan daripada
// kehilangan berkas pengguna.
func buildStorage(cfg config.Config, logger *slog.Logger) (storage.Store, error) {
	if cfg.S3Endpoint == "" || cfg.S3Bucket == "" {
		logger.Warn("object storage belum dikonfigurasi (S3_ENDPOINT/S3_BUCKET kosong); unggah bukti akan ditolak")
		return storage.Disabled{}, nil
	}
	s3store, err := storage.NewS3(storage.S3Config{
		Endpoint:     cfg.S3Endpoint,
		Region:       cfg.S3Region,
		Bucket:       cfg.S3Bucket,
		AccessKey:    cfg.S3AccessKey,
		SecretKey:    cfg.S3SecretKey,
		UsePathStyle: cfg.S3UsePathStyle,
	})
	if err != nil {
		return nil, fmt.Errorf("konfigurasi object storage S3: %w", err)
	}
	logger.Info("object storage S3 aktif", "endpoint", cfg.S3Endpoint, "bucket", cfg.S3Bucket)
	return s3store, nil
}

// buildMailer menyusun pengirim email dari konfigurasi.
//
// SMTP tidak dikonfigurasi berarti pengirim Log (hanya mencatat) — dipakai di
// lingkungan lokal tanpa Mailpit dan pada pengujian.
func buildMailer(cfg config.Config) mailer.Mailer {
	if cfg.SMTPHost == "" {
		return mailer.Log{}
	}
	smtp := mailer.NewSMTP(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, cfg.MailFrom)
	smtp.SetTimeout(cfg.SMTPTimeout)
	return smtp
}

// buildNotification menyusun service email_outbox (F1-16).
//
// Penerima dokumen admin diambil langsung dari repository pengguna
// (DocumentRecipients) supaya tidak ada ketergantungan melingkar antara
// users.Service dan service outbox.
func buildNotification(pool *database.Pool, cfg config.Config, store storage.Store, mail mailer.Mailer) *notification.Service {
	repo := notificationpg.New(pool)
	return notification.New(repo, mail,
		notification.WithObjectStore(store),
		notification.WithOrderReader(repo),
		notification.WithAdminRecipients(userspg.New(pool)),
		// Pengingat pembayaran C&C terjadwal (F2-23): repository yang sama
		// menulis baris cnc_reminders + email_outbox + audit dalam satu
		// transaksi.
		notification.WithReminderStore(repo),
		notification.WithConfig(notification.Config{
			MaxAttempts: cfg.OutboxMaxAttempts,
			SendTimeout: cfg.SMTPTimeout,
			Batch:       cfg.OutboxBatch,
		}),
	)
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
func runAPI(ctx context.Context, pool *database.Pool, cfg config.Config, logger *slog.Logger, metrik *observability.Metrics) error {
	authRepo := authpg.New(pool)
	authSvc := auth.NewService(authRepo, authRepo, authRepo, clock.System(), cfg.SessionIdle, cfg.SessionAbsolute)

	store, err := buildStorage(cfg, logger)
	if err != nil {
		return err
	}

	renderer := pdf.Renderer(pdf.Disabled{})
	if cfg.GotenbergURL != "" {
		renderer = pdf.NewGotenberg(cfg.GotenbergURL)
	}

	mail := buildMailer(cfg)
	// Tautan atur ulang menunjuk portal asal akun: cabang ke portal cabang,
	// admin ke portal admin (keduanya same-origin dengan API-nya).
	resetURL := auth.NewResetURLBuilder(cfg.CustomerBaseURL(), cfg.AdminBaseURL(), cfg.PasswordResetPath)

	// Email transaksional lewat email_outbox (F1-16). Sejak kartu ini tidak
	// ada lagi email yang dikirim langsung dari service: lupa kata sandi dan
	// undangan admin menulis baris outbox, worker yang mengirim.
	notifSvc := buildNotification(pool, cfg, store, mail)
	authSvc.SetResetMailer(notification.NewResetMailer(notifSvc, cfg.CustomerBaseURL(), cfg.AdminBaseURL(), cfg.PasswordResetPath))

	// Pengelolaan akun pengguna. Undangan admin diantre lewat email_outbox.
	usersSvc := users.NewService(userspg.New(pool), authSvc, authSvc,
		users.WithEmailQueue(notification.NewQueue(notifSvc)),
		users.WithResetURLBuilder(resetURL),
	)

	// Master cabang + token registrasi sekali pakai (F1-06).
	branchSvc := branch.NewService(branchpg.New(pool), clock.System())

	// Pendaftaran akun cabang lewat token registrasi (F1-07).
	registrationSvc := registration.NewService(registrationpg.New(pool), clock.System())

	// Rekening bank tujuan transfer (F1-09).
	bankSvc := bank.NewService(bankpg.New(pool))

	// Konten halaman Tutorial portal cabang (F2-27, PRD §3.8) tersimpan di
	// database: Superadmin mengubahnya lewat API dan portal cabang yang
	// di-refresh langsung menampilkan versi barunya tanpa build/deploy.
	tutorialSvc := tutorial.NewService(tutorialpg.New(pool))

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
	// Setelan sistem (F1-09) sekaligus sumber angka server untuk pesanan:
	// biaya_packaging pesanan Sertifikat dan cnc_hari tenggat C&C (F1-12).
	settingsSvc := settings.NewService(settingspg.New(pool))
	// Pesanan cabang: draft, editor order line, aturan Sertifikat/packaging,
	// daftar dan detail (F1-11 + F1-12).
	orderRepo := orderpg.New(pool)
	orderSvc := order.NewService(orderRepo, catalogSvc, settingsSvc,
		order.WithBranchFlags(branchSvc), order.WithClock(clock.System()))

	// Riwayat & detail pesanan seluruh cabang untuk admin Pusat (F1-15).
	// Penanda "email gagal" diserahkan domain notification lewat interface,
	// jadi domain pesanan tidak pernah membaca email_outbox langsung.
	adminOrderSvc := order.NewAdminService(orderRepo, order.WithEmailGagal(notifSvc))

	// Dashboard admin Pusat (F1-20, PRD §4.1): metrik periode + chip "perlu
	// tindakan". Sumber "hari ini" adalah jam aplikasi pada zona Asia/Jakarta,
	// dan daftar pesanan ber-email gagal diserahkan domain notification lewat
	// interface — sama seperti riwayat pesanan (F1-15) sehingga angka chip dan
	// daftar terfilternya tidak pernah berbeda.
	dashboardSvc := dashboard.NewService(dashboardpg.New(pool),
		dashboard.WithEmailGagal(notifSvc), dashboard.WithClock(clock.System()))

	// Bukti transfer pesanan (F1-13). Satu repository melayani dua peran:
	// metadata bukti (termasuk komit atomik + timeline) dan konteks pesanan
	// untuk otorisasi. Batas unggah (max_file/max_mb) dibaca dari setelan yang
	// sama dengan pengaturan sistem, jadi angka di UI dan server tidak pernah
	// berbeda.
	//
	// Repository yang sama juga menyimpan transaksi VERIFIKASI pembayaran
	// (F1-18): status, log, timeline, audit, dan penjadwalan pekerjaan dokumen
	// dalam satu transaksi. Klien River di bawah hanya dipakai untuk
	// MENITIPKAN job (insert-only); worker-lah yang menjalankannya.
	jobQueue, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		return fmt.Errorf("siapkan penjadwal pekerjaan: %w", err)
	}
	paymentRepo := paymentpg.New(pool, paymentpg.WithJobQueue(jobQueue))
	paymentSvc := payment.NewService(paymentRepo, paymentRepo, store, settingsSvc)
	verifySvc := payment.NewVerificationService(paymentRepo)

	// Revisi invoice berversi (F2-25): repository pesanan dengan penjadwal
	// River supaya transaksi revisi menitipkan pekerjaan "render invoice versi
	// baru + email invoice" pada transaksi yang sama. Setelan sistem dipakai
	// untuk izin Manager (`manager_boleh_revisi`) dan biaya packaging pesanan
	// Sertifikat.
	reviseSvc := order.NewRevisionService(orderpg.New(pool, orderpg.WithJobQueue(jobQueue)), settingsSvc)

	// Dokumen pesanan (F1-17): render Invoice PDF + Surat Jalan XLSX/PDF ke
	// object storage, catat di tabel `documents`, dan sediakan tautan unduh
	// berumur pendek. Service yang sama dipakai transisi Done (F1-18) dan
	// revisi invoice (F2-25).
	documentRepo := documentpg.New(pool)
	documentSvc := document.NewService(documentRepo, store, renderer)

	// Laporan order line + ekspor XLSX asinkron (F2-24). Job River dititipkan
	// di dalam transaksi pembuatan baris `report_jobs`, jadi tidak mungkin ada
	// pekerjaan yang dijadwalkan tanpa barisnya.
	reportSvc := report.NewService(reportpg.New(pool, reportpg.WithJobQueue(jobQueue)), store)

	// Cadangan terenkripsi (F1-21). Service yang sama dengan worker; di mode
	// API ia dipakai untuk MENAMPILKAN status di halaman Pengaturan
	// (Architecture.md §7) dan tidak pernah menjalankan cadangan sendiri.
	backupSvc, err := buildBackup(cfg, pool, store, logger)
	if err != nil {
		return err
	}
	if !cfg.CadanganAktif() {
		logger.Warn("tujuan cadangan belum dikonfigurasi: status cadangan hanya bisa dibaca, cadangan tidak akan berjalan")
	}

	handler := httpapi.NewRouter(httpapi.Deps{
		Config:              cfg,
		Version:             version,
		Health:              health{pool: pool, dsn: cfg.DatabaseURL},
		Auth:                authSvc,
		Settings:            settingsSvc,
		Catalog:             catalogSvc,
		Orders:              orderSvc,
		AdminOrders:         adminOrderSvc,
		Revise:              reviseSvc,
		Dashboard:           dashboardSvc,
		Payments:            paymentSvc,
		Verify:              verifySvc,
		Documents:           documentSvc,
		Reports:             reportSvc,
		Users:               usersSvc,
		Branches:            branchSvc,
		Bank:                bankSvc,
		Registration:        registrationSvc,
		Tutorial:            tutorialSvc,
		Emails:              notifSvc,
		Backups:             backupSvc,
		Storage:             store,
		Gotenberg:           renderer,
		Mailer:              mail,
		LoginLimiter:        loginLimiter,
		ForgotLimiter:       forgotLimiter,
		RegistrationLimiter: registrationLimiter,
		ProofLimiter:        httpx.NewRateLimiter(30, 15*time.Minute),
		Metrics:             metrik,
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
