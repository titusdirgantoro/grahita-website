package config

import (
	"strings"
	"testing"
)

func TestBrowserFixtureHanyaBolehLokal(t *testing.T) {
	cfg := configValidMinimum()
	cfg.AppEnv = "production"
	cfg.SessionSecure = true
	cfg.BrowserFixture.Enabled = true
	cfg.BrowserFixture.Password = "rahasia-uji"

	if err := cfg.Validate(); err == nil {
		t.Fatal("fixture browser harus ditolak di production")
	}
}

func TestBrowserFixtureLokalWajibPasswordInternal(t *testing.T) {
	cfg := configValidMinimum()
	cfg.BrowserFixture.Enabled = true
	cfg.BrowserFixture.Password = ""

	if err := cfg.Validate(); err == nil {
		t.Fatal("fixture browser tanpa password internal harus ditolak")
	}
}

func TestBrowserFixtureLokalValid(t *testing.T) {
	cfg := configBrowserFixtureValid()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("fixture browser lokal harus valid: %v", err)
	}
}

func TestBrowserFixtureMenolakBindNonLoopback(t *testing.T) {
	for _, addr := range []string{":8080", "0.0.0.0:8080", "192.168.1.10:8080", "localhost:8080"} {
		t.Run(addr, func(t *testing.T) {
			cfg := configBrowserFixtureValid()
			cfg.HTTPAddr = addr
			if err := cfg.Validate(); err == nil {
				t.Fatalf("fixture browser harus menolak bind %q", addr)
			}
		})
	}
}

func TestBrowserFixtureMenolakDatabaseBukanSintetisLokal(t *testing.T) {
	for _, dsn := range []string{
		"postgres://uji@db.internal:5432/grahita_browser_fixture_1",
		"postgres://uji@127.0.0.1:5432/grahita",
		"postgres://uji@192.168.1.10:5432/grahita_browser_fixture_1",
		"postgres://uji@127.0.0.1:5432/grahita_browser_fixture_1?host=db.internal",
	} {
		t.Run(dsn, func(t *testing.T) {
			cfg := configBrowserFixtureValid()
			cfg.DatabaseURL = dsn
			if err := cfg.Validate(); err == nil {
				t.Fatalf("fixture browser harus menolak DSN %q", dsn)
			}
		})
	}
}

func TestBrowserFixtureMenolakOriginAtauKunciProxyTidakAman(t *testing.T) {
	kasus := []struct {
		nama string
		ubah func(*Config)
	}{
		{"tanpa kunci proxy", func(c *Config) { c.BrowserFixture.ProxyKey = "" }},
		{"kunci proxy pendek", func(c *Config) { c.BrowserFixture.ProxyKey = "pendek" }},
		{"tanpa origin", func(c *Config) { c.BrowserFixture.AllowedOrigins = nil }},
		{"origin remote", func(c *Config) { c.BrowserFixture.AllowedOrigins = []string{"http://192.168.1.10:5174"} }},
		{"origin https", func(c *Config) { c.BrowserFixture.AllowedOrigins = []string{"https://127.0.0.1:5174"} }},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			cfg := configBrowserFixtureValid()
			k.ubah(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("konfigurasi fixture tidak aman harus ditolak")
			}
		})
	}
}

func configBrowserFixtureValid() Config {
	cfg := configValidMinimum()
	cfg.BrowserFixture = BrowserFixtureConfig{
		Enabled: true, Password: "rahasia-uji", ProxyKey: strings.Repeat("k", 32),
		AllowedOrigins:  []string{"http://127.0.0.1:5173", "http://127.0.0.1:5174"},
		SuperadminEmail: "rina@example.test", ManagerEmail: "budi@example.test", BranchEmail: "bandung@example.test",
	}
	return cfg
}

func configValidMinimum() Config {
	return Config{
		AppEnv:          "local",
		HTTPAddr:        "127.0.0.1:8088",
		DatabaseURL:     "postgres://uji@127.0.0.1:5432/grahita_browser_fixture_1",
		CustomerHost:    DefaultCustomerHost,
		AdminHost:       DefaultAdminHost,
		SessionIdle:     1,
		SessionAbsolute: 2,
	}
}
