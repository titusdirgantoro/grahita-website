package httpx

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/grahita/portal/api/internal/platform/apierr"
)

// CSRFCookieName adalah cookie double-submit yang dibaca JavaScript SPA.
//
// Cookie ini sengaja TIDAK HttpOnly (klien harus membacanya untuk dikirim
// kembali sebagai header) dan berisi nilai acak tanpa makna bila dicuri.
const CSRFCookieName = "grahita_csrf"

// CSRFHeader adalah header tempat SPA mengirim token.
const CSRFHeader = "X-CSRF-Token"

// CSRFConfig mengatur middleware double-submit (Architecture.md §6).
type CSRFConfig struct {
	Enabled  bool
	Secure   bool
	Exempt   map[string]bool
	TokenAge time.Duration
}

// CSRF menolak request非-GET yang tidak membawa token yang cocok.
func CSRF(cfg CSRFConfig) func(http.Handler) http.Handler {
	if cfg.TokenAge <= 0 {
		cfg.TokenAge = 12 * time.Hour
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			token := ""
			if c, err := r.Cookie(CSRFCookieName); err == nil {
				token = c.Value
			}
			// Cookie selalu disegarkan bila kosong/kedaluwarsa supaya SPA
			// bisa memulai sesi tanpa endpoint khusus.
			if token == "" {
				token = newCSRFToken()
				http.SetCookie(w, &http.Cookie{
					Name:     CSRFCookieName,
					Value:    token,
					Path:     "/",
					Secure:   cfg.Secure,
					HttpOnly: false,
					SameSite: http.SameSiteLaxMode,
					MaxAge:   int(cfg.TokenAge.Seconds()),
				})
			}

			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			if cfg.Exempt[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			sent := r.Header.Get(CSRFHeader)
			if sent == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(token)) != 1 {
				Fail(w, r, apierr.New(http.StatusForbidden, "CSRF_INVALID", "Token CSRF tidak valid atau tidak ada. Muat ulang halaman lalu coba lagi."))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func newCSRFToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}
