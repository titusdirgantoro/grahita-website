package config

import "testing"

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
	cfg := configValidMinimum()
	cfg.BrowserFixture = BrowserFixtureConfig{
		Enabled: true, Password: "rahasia-uji",
		SuperadminEmail: "rina@example.test", ManagerEmail: "budi@example.test", BranchEmail: "bandung@example.test",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("fixture browser lokal harus valid: %v", err)
	}
}

func configValidMinimum() Config {
	return Config{
		AppEnv:          "local",
		DatabaseURL:     "postgres://contoh",
		CustomerHost:    DefaultCustomerHost,
		AdminHost:       DefaultAdminHost,
		SessionIdle:     1,
		SessionAbsolute: 2,
	}
}
