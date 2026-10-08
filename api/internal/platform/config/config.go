// Package config membaca konfigurasi dari environment variable.
// Tidak ada nilai rahasia yang di-hardcode; semua lewat environment.
package config

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"
)

type Config struct {
	AppEnv      string // local | staging | production
	HTTPAddr    string
	DatabaseURL string
	Level       string

	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string

	SMTPHost string
	SMTPPort string
	MailFrom string

	GotenbergURL   string
	CORSOrigins    []string
	WorkerInterval time.Duration
}

func Load() (Config, error) {
	c := Config{
		AppEnv:       env("APP_ENV", "local"),
		HTTPAddr:     env("HTTP_ADDR", ":8080"),
		DatabaseURL:  env("DATABASE_URL", ""),
		Level:        env("LOG_LEVEL", "info"),
		S3Endpoint:   env("S3_ENDPOINT", ""),
		S3Bucket:     env("S3_BUCKET", "grahita"),
		S3AccessKey:  env("S3_ACCESS_KEY", ""),
		S3SecretKey:  env("S3_SECRET_KEY", ""),
		SMTPHost:     env("SMTP_HOST", ""),
		SMTPPort:     env("SMTP_PORT", "1025"),
		MailFrom:     env("MAIL_FROM", "no-reply@grahitaindonesia.com"),
		GotenbergURL: env("GOTENBERG_URL", ""),
	}

	if origins := env("CORS_ORIGINS", ""); origins != "" {
		for _, o := range strings.Split(origins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				c.CORSOrigins = append(c.CORSOrigins, o)
			}
		}
	}

	d, err := time.ParseDuration(env("WORKER_INTERVAL", "15s"))
	if err != nil {
		return c, errors.New("WORKER_INTERVAL bukan durasi yang valid")
	}
	c.WorkerInterval = d

	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL wajib diisi")
	}
	return c, nil
}

func (c Config) IsProduction() bool { return c.AppEnv == "production" }

func (c Config) LogLevel() slog.Level {
	switch strings.ToLower(c.Level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
