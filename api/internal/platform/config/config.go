// Package config membaca konfigurasi dari environment variable.
//
// Tidak ada nilai rahasia di dalam repo (Architecture.md §6): password,
// kunci S3, dan kredensial SMTP selalu berasal dari environment. Berkas
// .env.example hanya berisi nilai contoh untuk pengembangan lokal.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Domain keputusan user (tidak boleh diubah tanpa persetujuan):
// customer grahita-branch.tiusclaw.com, admin grahita-admin.tiusclaw.com.
const (
	DefaultCustomerHost = "grahita-branch.tiusclaw.com"
	DefaultAdminHost    = "grahita-admin.tiusclaw.com"
)

// Config adalah seluruh konfigurasi runtime satu binary.
type Config struct {
	AppEnv   string // local | staging | production
	HTTPAddr string
	LogLevel string

	DatabaseURL string

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

	// Object storage S3-compatible (MinIO saat lokal).
	S3Endpoint     string
	S3Region       string
	S3Bucket       string
	S3AccessKey    string
	S3SecretKey    string
	S3UsePathStyle bool

	// SMTP (Mailpit saat lokal, Gmail saat produksi).
	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	MailFrom string

	// Gotenberg (HTML -> PDF).
	GotenbergURL string

	// Worker River.
	WorkerQueues    map[string]int
	WorkerShutdown  time.Duration
	JobPollInterval time.Duration
}

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
		CustomerHost:      env("CUSTOMER_HOST", DefaultCustomerHost),
		AdminHost:         env("ADMIN_HOST", DefaultAdminHost),
		PublicScheme:      env("PUBLIC_SCHEME", "https"),
		CookieName:        env("SESSION_COOKIE_NAME", "grahita_session"),
		CSRFEnabled:       envBool("CSRF_ENABLED", true),
		PasswordResetPath: env("PASSWORD_RESET_PATH", "/lupa"),
		S3Endpoint:        env("S3_ENDPOINT", ""),
		S3Region:          env("S3_REGION", "us-east-1"),
		S3Bucket:          env("S3_BUCKET", ""),
		S3AccessKey:       env("S3_ACCESS_KEY", ""),
		S3SecretKey:       env("S3_SECRET_KEY", ""),
		S3UsePathStyle:    envBool("S3_USE_PATH_STYLE", true),
		SMTPHost:          env("SMTP_HOST", ""),
		SMTPPort:          envInt("SMTP_PORT", 1025),
		SMTPUser:          env("SMTP_USER", ""),
		SMTPPass:          env("SMTP_PASS", ""),
		MailFrom:          env("MAIL_FROM", "no-reply@grahita.id"),
		GotenbergURL:      env("GOTENBERG_URL", ""),
		WorkerShutdown:    20 * time.Second,
		JobPollInterval:   15 * time.Second,
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
