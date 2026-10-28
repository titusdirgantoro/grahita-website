package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGeminiMewajibkanMasterKey32Byte(t *testing.T) {
	base := Config{DatabaseURL: "postgres://example", CustomerHost: DefaultCustomerHost,
		AdminHost: DefaultAdminHost, SessionIdle: 8, SessionAbsolute: 24,
		OCRProvider: OCRProviderGemini}
	for _, tc := range []struct { key string; valid bool }{
		{"", false},
		{base64.StdEncoding.EncodeToString([]byte("terlalu-pendek")), false},
		{base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))), true},
	} {
		base.GeminiKeyEncryptionKey = tc.key
		if got := base.Validate() == nil; got != tc.valid {
			t.Fatalf("master key valid=%v, ingin %v", got, tc.valid)
		}
	}
}
