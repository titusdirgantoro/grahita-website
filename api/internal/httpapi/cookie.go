package httpapi

import (
	"net/http"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/config"
)

// Cookie sesi.
//
// Cookie ini SENGAJA tanpa atribut Domain sehingga browser memperlakukannya
// sebagai host-only: cookie untuk grahita-branch.tiusclaw.com tidak pernah
// terkirim ke grahita-admin.tiusclaw.com, dan sebaliknya. Kedua portal tetap
// memakai API same-origin /api/v1 di host masing-masing, sementara otorisasi
// ditentukan role akun di server — bukan hostname (keputusan domain user).
func sessionCookie(cfg config.Config, token string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     cfg.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   cfg.SessionSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
	}
}

// clearSessionCookie menghapus cookie sesi (MaxAge < 0).
func clearSessionCookie(cfg config.Config) *http.Cookie {
	return &http.Cookie{
		Name:     cfg.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   cfg.SessionSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	}
}

// sessionToken mengambil token sesi dari cookie request.
func sessionToken(cfg config.Config, r *http.Request) string {
	c, err := r.Cookie(cfg.CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// actor mewarisi nama pendek untuk dipakai handler.
type actor = auth.Actor
