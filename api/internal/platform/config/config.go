// Package config membaca konfigurasi dari environment variable.
//
// Tidak ada nilai rahasia di dalam repo (Architecture.md §6): password,
// kunci S3, dan kredensial SMTP selalu berasal dari environment. Berkas
// .env.example hanya berisi nilai contoh untuk pengembangan lokal.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/platform/observability"
)

// Domain keputusan user (tidak boleh diubah tanpa persetujuan):
// customer grahita-branch.tiusclaw.com, admin grahita-admin.tiusclaw.com.
const (
	DefaultCustomerHost = "grahita-branch.tiusclaw.com"
	DefaultAdminHost    = "grahita-admin.tiusclaw.com"
)

// Cara menjangkau tujuan cadangan (BACKUP_TARGET, F1-21).
const (
	// BackupTargetDir menulis cadangan terenkripsi ke direktori lokal
	// (termasuk mount Google Drive yang disinkronkan alat lain).
	BackupTargetDir = "dir"
	// BackupTargetRclone menulis cadangan terenkripsi ke remote rclone
	// (Google Drive di produksi, Architecture.md §7).
	BackupTargetRclone = "rclone"
)

// Config adalah seluruh konfigurasi runtime satu binary.
type Config struct {
	AppEnv   string // local | staging | production
	HTTPAddr string
	LogLevel string

	DatabaseURL string
	// ImportSnapshotKey adalah kunci AES-256 berformat base64 untuk
	// before-snapshot rollback impor staging. Kunci berasal dari secret store
	// runtime dan tidak pernah disimpan bersama ciphertext di database.
	ImportSnapshotKey string

	// Host publik per portal. Kedua host melayani API same-origin di
	// /api/v1 (Architecture.md §5.1).
	CustomerHost string
	AdminHost    string
	PublicScheme string

	// Sesi: cookie selalu host-only (tanpa atribut Domain) dan disimpan di DB.
	CookieName      string
	SessionSecure   bool
	SessionIdle     time.Duration
	SessionAbsolute time.Duration

	// PasswordResetPath adalah rute halaman atur ulang kata sandi pada portal
	// (tautan email `${portal}/<path>?token=...`).
	PasswordResetPath string

	// CSRF double-submit untuk semua request non-GET.
	CSRFEnabled bool

	// CORS hanya dipakai saat pengembangan (Vite dev server di port lain).
	AllowedOrigins []string

	// BrowserFixture menyediakan pintu masuk sesi untuk uji browser sintetis
	// lokal tanpa mengetik kata sandi ke automation tool. Handler tetap memakai
	// Login normal dengan password yang hanya hidup di environment proses.
	// Validate menolak fitur ini di luar APP_ENV=local.
	BrowserFixture BrowserFixtureConfig

	// Object storage S3-compatible (MinIO saat lokal).
	S3Endpoint     string
	S3Region       string
	S3Bucket       string
	S3AccessKey    string
	S3SecretKey    string
	S3UsePathStyle bool

	// SMTP (Mailpit saat lokal, Gmail saat produksi). Timeout adalah batas
	// waktu TOTAL satu operasi kirim supaya peer SMTP yang diam tidak
	// menggantung worker (temuan QA F1-05).
	SMTPHost    string
	SMTPPort    int
	SMTPUser    string
	SMTPPass    string
	MailFrom    string
	SMTPTimeout time.Duration

	// Email outbox (F1-16): jeda pemindaian, batas percobaan kirim per email,
	// dan ukuran batch worker pengirim email.
	OutboxInterval    time.Duration
	OutboxMaxAttempts int
	OutboxBatch       int

	// Gotenberg (HTML -> PDF).
	GotenbergURL string

	// Cadangan terenkripsi (F1-21, Architecture.md §7).
	//
	// BackupTarget: cara menjangkau tujuan — "" (nonaktif), "dir" (direktori
	// lokal, termasuk mount Drive), atau "rclone" (Google Drive lewat biner
	// rclone). Kredensial rclone SELALU berada di berkas milik operator di
	// luar repo (BackupRcloneConfig).
	BackupTarget         string
	BackupDir            string
	BackupRcloneBin      string
	BackupRcloneRemote   string
	BackupRcloneConfig   string
	BackupAgeRecipient   string
	BackupAgeIdentity    string
	BackupPgDump         string
	BackupPgRestore      string
	BackupWorkDir        string
	BackupRetensiHarian  int
	BackupRetensiBulanan int
	BackupWALHari        int
	BackupWALDir         string
	BackupJamWIB         int
	BackupInterval       time.Duration
	BackupSweepGrace     time.Duration
	BackupRestoreDSN     string

	// Worker River.
	WorkerQueues    map[string]int
	WorkerShutdown  time.Duration
	JobPollInterval time.Duration

	// ReminderJamWIB adalah jam (WIB) pekerjaan pengingat pembayaran C&C
	// dijalankan setiap hari (F2-23, SystemDesign.md §4.6: 07:00 Asia/Jakarta).
	ReminderJamWIB int

	// Observability (Architecture.md §8). METRICS_ENABLED menyalakan endpoint
	// /metrics beserta penghitung permintaan; endpoint itu hanya dijangkau
	// Prometheus di jaringan internal (Caddy tidak mem-proxy /metrics dan
	// port API tidak dipublikasikan di produksi). SENTRY_DSN kosong berarti
	// pelaporan error dimatikan (no-op).
	MetricsEnabled    bool
	MetricsInterval   time.Duration
	DiskMounts        []string
	SentryDSN         string
	SentryEnvironment string
	SentrySampleRate  float64
	// WorkerMetricsAddr adalah alamat listener /metrics proses worker. Worker
	// tidak melayani HTTP lain, jadi metrik pekerjaan (River) hanya bisa
	// dipanen dari sini (Architecture.md §8).
	WorkerMetricsAddr string

	// Pembacaan bukti transfer AI/OCR (F2-26; Architecture.md §1/§11).
	//
	// OCRProvider memilih implementasi `payment.PaymentReader`: "claude"
	// (bawaan) memakai Claude vision, "mock" memakai pembaca deterministik
	// untuk pengembangan lokal/pengujian (Architecture.md §1: "OCR mode mock").
	OCRProvider string
	// AnthropicAPIKey adalah kunci Claude API. Kosong berarti provider claude
	// BELUM bisa dipakai: pekerjaan pembacaan bukti gagal dan diulang (River),
	// bukan menyetujui pembayaran atas dasar tebakan. Kunci TIDAK pernah
	// dicatat ke log maupun entri audit.
	AnthropicAPIKey string
	// AnthropicBaseURL/AnthropicModel mengatur endpoint + model vision;
	// kosong berarti bawaan pembaca (api.anthropic.com, model bawaan).
	AnthropicBaseURL string
	AnthropicModel   string
	// OCRTimeout membatasi satu panggilan provider.
	OCRTimeout time.Duration
}

// BrowserFixtureConfig adalah konfigurasi pintu sesi untuk browser lokal.
type BrowserFixtureConfig struct {
	Enabled         bool
	Password        string
	ProxyKey        string
	AllowedOrigins  []string
	SuperadminEmail string
	ManagerEmail    string
	BranchEmail     string
}

// Nilai OCR_PROVIDER yang dikenal.
const (
	// OCRProviderClaude memakai Claude vision (bawaan).
	OCRProviderClaude = "claude"
	// OCRProviderMock memakai pembaca deterministik (lokal/pengujian).
	OCRProviderMock = "mock"
)

// IsProduction menandai lingkungan produksi.
func (c Config) IsProduction() bool { return c.AppEnv == "production" }

// IsLocal menandai lingkungan pengembangan lokal (Docker Compose).
func (c Config) IsLocal() bool { return c.AppEnv == "local" }

// LogLevelSlog memetakan string konfigurasi ke slog.Level.
func (c Config) LogLevelSlog() slog.Level {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// CustomerBaseURL adalah akar URL portal cabang.
func (c Config) CustomerBaseURL() string {
	return fmt.Sprintf("%s://%s", c.PublicScheme, c.CustomerHost)
}

// AdminBaseURL adalah akar URL portal admin.
func (c Config) AdminBaseURL() string {
	return fmt.Sprintf("%s://%s", c.PublicScheme, c.AdminHost)
}

// Load membaca dan memvalidasi konfigurasi.
func Load() (Config, error) {
	c := Config{
		AppEnv:            env("APP_ENV", "local"),
		HTTPAddr:          env("HTTP_ADDR", ":8080"),
		LogLevel:          env("LOG_LEVEL", "info"),
		DatabaseURL:       env("DATABASE_URL", ""),
		ImportSnapshotKey: strings.TrimSpace(env("MASTER_IMPORT_SNAPSHOT_KEY", "")),
		CustomerHost:      env("CUSTOMER_HOST", DefaultCustomerHost),
		AdminHost:         env("ADMIN_HOST", DefaultAdminHost),
		PublicScheme:      env("PUBLIC_SCHEME", "https"),
		CookieName:        env("SESSION_COOKIE_NAME", "grahita_session"),
		CSRFEnabled:       envBool("CSRF_ENABLED", true),
		PasswordResetPath: env("PASSWORD_RESET_PATH", "/lupa"),
		BrowserFixture: BrowserFixtureConfig{
			Enabled:         envBool("BROWSER_FIXTURE_ENABLED", false),
			Password:        strings.TrimSpace(env("BROWSER_FIXTURE_PASSWORD", "")),
			ProxyKey:        strings.TrimSpace(env("BROWSER_FIXTURE_PROXY_KEY", "")),
			AllowedOrigins:  splitNonEmpty(env("BROWSER_FIXTURE_ORIGINS", "")),
			SuperadminEmail: strings.TrimSpace(env("BROWSER_FIXTURE_SUPERADMIN_EMAIL", "rina@grahitaindonesia.com")),
			ManagerEmail:    strings.TrimSpace(env("BROWSER_FIXTURE_MANAGER_EMAIL", "budi@grahitaindonesia.com")),
			BranchEmail:     strings.TrimSpace(env("BROWSER_FIXTURE_BRANCH_EMAIL", "bandung@grahitaindonesia.com")),
		},
		S3Endpoint:           env("S3_ENDPOINT", ""),
		S3Region:             env("S3_REGION", "us-east-1"),
		S3Bucket:             env("S3_BUCKET", ""),
		S3AccessKey:          env("S3_ACCESS_KEY", ""),
		S3SecretKey:          env("S3_SECRET_KEY", ""),
		S3UsePathStyle:       envBool("S3_USE_PATH_STYLE", true),
		SMTPHost:             env("SMTP_HOST", ""),
		SMTPPort:             envInt("SMTP_PORT", 1025),
		SMTPUser:             env("SMTP_USER", ""),
		SMTPPass:             env("SMTP_PASS", ""),
		MailFrom:             env("MAIL_FROM", "no-reply@grahita.id"),
		GotenbergURL:         env("GOTENBERG_URL", ""),
		OutboxMaxAttempts:    envInt("OUTBOX_MAX_ATTEMPTS", 6),
		OutboxBatch:          envInt("OUTBOX_BATCH", 50),
		BackupDir:            env("BACKUP_TARGET_DIR", ""),
		BackupRcloneBin:      env("BACKUP_RCLONE_BIN", "rclone"),
		BackupRcloneRemote:   env("BACKUP_RCLONE_REMOTE", ""),
		BackupRcloneConfig:   env("BACKUP_RCLONE_CONFIG", ""),
		BackupAgeRecipient:   env("BACKUP_AGE_RECIPIENT", ""),
		BackupAgeIdentity:    env("BACKUP_AGE_IDENTITY_FILE", ""),
		BackupPgDump:         env("BACKUP_PG_DUMP", "pg_dump"),
		BackupPgRestore:      env("BACKUP_PG_RESTORE", "pg_restore"),
		BackupWorkDir:        env("BACKUP_WORK_DIR", os.TempDir()),
		BackupRetensiHarian:  envInt("BACKUP_RETENSI_HARIAN", 30),
		BackupRetensiBulanan: envInt("BACKUP_RETENSI_BULANAN", 12),
		BackupWALHari:        envInt("BACKUP_WAL_HARI", 7),
		BackupWALDir:         env("BACKUP_WAL_DIR", ""),
		BackupJamWIB:         envInt("BACKUP_JAM_WIB", 2),
		BackupRestoreDSN:     env("BACKUP_RESTORE_DSN", ""),
		WorkerShutdown:       20 * time.Second,
		JobPollInterval:      15 * time.Second,
		MetricsEnabled:       envBool("METRICS_ENABLED", true),
		DiskMounts:           observability.MountsDariEnv(env("DISK_MOUNTS", "/")),
		SentryDSN:            env("SENTRY_DSN", ""),
		SentryEnvironment:    env("SENTRY_ENVIRONMENT", ""),
		SentrySampleRate:     envFloat("SENTRY_TRACES_SAMPLE_RATE", 0),
		WorkerMetricsAddr:    env("WORKER_METRICS_ADDR", ":9091"),
		ReminderJamWIB:       envInt("REMINDER_JAM_WIB", 7),
		OCRProvider:          strings.ToLower(strings.TrimSpace(env("OCR_PROVIDER", OCRProviderClaude))),
		AnthropicAPIKey:      strings.TrimSpace(env("ANTHROPIC_API_KEY", "")),
		AnthropicBaseURL:     strings.TrimSpace(env("ANTHROPIC_BASE_URL", "")),
		AnthropicModel:       strings.TrimSpace(env("ANTHROPIC_MODEL", "")),
	}

	// Pilihan pembaca bukti divalidasi di sini: nilai yang tidak dikenal
	// menghentikan start alih-alih diam-diam memakai provider lain (F2-26).
	switch c.OCRProvider {
	case OCRProviderClaude, OCRProviderMock:
	default:
		return c, fmt.Errorf("OCR_PROVIDER tidak dikenal: %q (pilih %s atau %s)",
			c.OCRProvider, OCRProviderClaude, OCRProviderMock)
	}

	// Cara menjangkau tujuan cadangan ditentukan otomatis bila tidak disebut
	// eksplisit: remote rclone lebih dahulu, lalu direktori.
	c.BackupTarget = env("BACKUP_TARGET", "")
	if c.BackupTarget == "" {
		switch {
		case c.BackupRcloneRemote != "":
			c.BackupTarget = BackupTargetRclone
		case c.BackupDir != "":
			c.BackupTarget = BackupTargetDir
		}
	}

	// Cookie Secure hanya dimatikan di lingkungan lokal supaya bisa diakses
	// lewat http://localhost. Semua lingkungan lain wajib Secure.
	c.SessionSecure = envBool("SESSION_SECURE", !c.IsLocal())

	var err error
	if c.SessionIdle, err = envDuration("SESSION_IDLE", 8*time.Hour); err != nil {
		return c, err
	}
	if c.SessionAbsolute, err = envDuration("SESSION_ABSOLUTE", 30*24*time.Hour); err != nil {
		return c, err
	}
	if c.WorkerShutdown, err = envDuration("WORKER_SHUTDOWN", c.WorkerShutdown); err != nil {
		return c, err
	}
	if c.JobPollInterval, err = envDuration("WORKER_INTERVAL", c.JobPollInterval); err != nil {
		return c, err
	}
	if c.SMTPTimeout, err = envDuration("SMTP_TIMEOUT", 30*time.Second); err != nil {
		return c, err
	}
	if c.OutboxInterval, err = envDuration("OUTBOX_INTERVAL", time.Minute); err != nil {
		return c, err
	}
	if c.BackupInterval, err = envDuration("BACKUP_INTERVAL", 15*time.Minute); err != nil {
		return c, err
	}
	if c.OCRTimeout, err = envDuration("OCR_TIMEOUT", 60*time.Second); err != nil {
		return c, err
	}
	if c.BackupSweepGrace, err = envDuration("BACKUP_SWEEP_GRACE", 24*time.Hour); err != nil {
		return c, err
	}
	if c.MetricsInterval, err = envDuration("METRICS_INTERVAL", 30*time.Second); err != nil {
		return c, err
	}

	// Lingkungan Sentry mengikuti APP_ENV bila tidak disebut eksplisit.
	if c.SentryEnvironment = strings.TrimSpace(c.SentryEnvironment); c.SentryEnvironment == "" {
		c.SentryEnvironment = c.AppEnv
	}

	c.WorkerQueues = map[string]int{"default": envInt("WORKER_CONCURRENCY", 4)}

	if origins := env("CORS_ORIGINS", ""); origins != "" {
		for _, o := range strings.Split(origins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				c.AllowedOrigins = append(c.AllowedOrigins, o)
			}
		}
	}

	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// Validate memastikan konfigurasi wajib terisi dan tidak saling bertabrakan.
func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL wajib diisi")
	}
	if c.CustomerHost == "" || c.AdminHost == "" {
		return errors.New("CUSTOMER_HOST dan ADMIN_HOST wajib diisi")
	}
	if c.CustomerHost == c.AdminHost {
		return errors.New("CUSTOMER_HOST dan ADMIN_HOST tidak boleh sama")
	}
	if c.SessionIdle <= 0 || c.SessionAbsolute <= 0 {
		return errors.New("SESSION_IDLE dan SESSION_ABSOLUTE harus positif")
	}
	if c.SessionIdle > c.SessionAbsolute {
		return errors.New("SESSION_IDLE tidak boleh lebih besar dari SESSION_ABSOLUTE")
	}
	if c.IsProduction() && !c.SessionSecure {
		return errors.New("SESSION_SECURE wajib true di produksi")
	}
	if c.BrowserFixture.Enabled {
		if !c.IsLocal() {
			return errors.New("BROWSER_FIXTURE_ENABLED hanya boleh true pada APP_ENV=local")
		}
		if c.SessionSecure {
			return errors.New("BROWSER_FIXTURE_ENABLED membutuhkan SESSION_SECURE=false untuk browser HTTP lokal")
		}
		if strings.TrimSpace(c.BrowserFixture.Password) == "" {
			return errors.New("BROWSER_FIXTURE_PASSWORD wajib diisi saat fixture browser aktif")
		}
		if len(c.BrowserFixture.ProxyKey) < 32 {
			return errors.New("BROWSER_FIXTURE_PROXY_KEY wajib minimal 32 karakter")
		}
		if err := validateBrowserFixtureHTTPAddr(c.HTTPAddr); err != nil {
			return err
		}
		if err := validateBrowserFixtureDatabaseURL(c.DatabaseURL); err != nil {
			return err
		}
		if err := validateBrowserFixtureOrigins(c.BrowserFixture.AllowedOrigins); err != nil {
			return err
		}
		if strings.TrimSpace(c.BrowserFixture.SuperadminEmail) == "" ||
			strings.TrimSpace(c.BrowserFixture.ManagerEmail) == "" ||
			strings.TrimSpace(c.BrowserFixture.BranchEmail) == "" {
			return errors.New("email ketiga profil BROWSER_FIXTURE wajib diisi")
		}
	}
	if !validImportSnapshotKey(c.AppEnv, c.ImportSnapshotKey) {
		return errors.New("MASTER_IMPORT_SNAPSHOT_KEY wajib berupa base64 32 byte di staging")
	}
	if err := c.ValidasiCadangan(); err != nil {
		return err
	}
	return nil
}

func splitNonEmpty(value string) []string {
	var hasil []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			hasil = append(hasil, item)
		}
	}
	return hasil
}

func validateBrowserFixtureHTTPAddr(addr string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || port == "" || !literalLoopback(host) {
		return errors.New("fixture browser mewajibkan HTTP_ADDR dengan IP loopback eksplisit")
	}
	return nil
}

func validateBrowserFixtureDatabaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.RawQuery != "" || u.Fragment != "" || !literalLoopback(u.Hostname()) {
		return errors.New("fixture browser mewajibkan PostgreSQL pada IP loopback eksplisit")
	}
	databaseName := strings.TrimPrefix(path.Clean(u.EscapedPath()), "/")
	if unescaped, unescapeErr := url.PathUnescape(databaseName); unescapeErr == nil {
		databaseName = unescaped
	}
	if !strings.HasPrefix(databaseName, "grahita_browser_fixture_") {
		return errors.New("fixture browser mewajibkan database bernama grahita_browser_fixture_*")
	}
	return nil
}

func validateBrowserFixtureOrigins(origins []string) error {
	if len(origins) == 0 {
		return errors.New("BROWSER_FIXTURE_ORIGINS wajib diisi")
	}
	for _, raw := range origins {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Port() == "" || !literalLoopback(u.Hostname()) {
			return fmt.Errorf("origin fixture browser tidak aman: %q", raw)
		}
	}
	return nil
}

func literalLoopback(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func validImportSnapshotKey(appEnv, encoded string) bool {
	if !strings.EqualFold(strings.TrimSpace(appEnv), "staging") {
		return true
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	return err == nil && len(key) == 32
}

// CadanganAktif melaporkan apakah tujuan cadangan dikonfigurasi.
func (c Config) CadanganAktif() bool { return strings.TrimSpace(c.BackupTarget) != "" }

// ValidasiCadangan menolak konfigurasi cadangan yang SETENGAH JADI.
//
// Aturan kerasnya: bila tujuan cadangan aktif, kunci publik age wajib ada.
// Tanpa kunci, cadangan hanya bisa ditulis polos — dan cadangan polos berisi
// seluruh data bisnis (termasuk hash kata sandi) di Drive. Lebih baik gagal
// saat start daripada mengirim rahasia dalam bentuk terbuka.
func (c Config) ValidasiCadangan() error {
	switch strings.TrimSpace(c.BackupTarget) {
	case "":
		return nil
	case BackupTargetDir:
		if strings.TrimSpace(c.BackupDir) == "" {
			return errors.New("BACKUP_TARGET=dir wajib disertai BACKUP_TARGET_DIR")
		}
	case BackupTargetRclone:
		if strings.TrimSpace(c.BackupRcloneRemote) == "" {
			return errors.New("BACKUP_TARGET=rclone wajib disertai BACKUP_RCLONE_REMOTE")
		}
	default:
		return fmt.Errorf("BACKUP_TARGET tidak dikenal: %q (pakai dir atau rclone)", c.BackupTarget)
	}
	if strings.TrimSpace(c.BackupAgeRecipient) == "" {
		return errors.New("BACKUP_AGE_RECIPIENT wajib diisi bila tujuan cadangan aktif: cadangan tidak boleh ditulis tanpa enkripsi age")
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// envFloat membaca angka pecahan; nilai tidak sah diabaikan (kembali ke
// bawaan) supaya konfigurasi pengamatan tidak menjatuhkan start server.
func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback, fmt.Errorf("%s bukan durasi yang valid: %w", key, err)
	}
	return d, nil
}
