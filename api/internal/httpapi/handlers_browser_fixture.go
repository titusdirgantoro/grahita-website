package httpapi

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
)

const browserFixtureProxyHeader = "X-Browser-Fixture-Key"

// browserFixtureSession menerbitkan sesi melalui alur Login normal untuk akun
// seed sintetis. Route ini hanya didaftarkan saat APP_ENV=local dan fixture
// diaktifkan eksplisit; password internal tidak pernah diterima dari browser.
func (a *API) browserFixtureSession(w http.ResponseWriter, r *http.Request) error {
	if !a.browserFixtureRequestTrusted(r) {
		return notFoundErr()
	}

	profil := chi.URLParam(r, "profile")
	email, expectedRole, ok := a.browserFixtureIdentity(profil)
	if !ok {
		return notFoundErr()
	}

	issued, err := a.d.Auth.Login(r.Context(), email, a.d.Config.BrowserFixture.Password, requestMeta(r))
	if err != nil {
		return mapDomainErr(err)
	}
	if issued.Actor.Role != expectedRole {
		_ = a.d.Auth.Logout(r.Context(), issued.Token)
		return apierr.Internal(fmt.Errorf("profil fixture %s tidak cocok dengan role akun", profil))
	}

	http.SetCookie(w, sessionCookie(a.d.Config, issued.Token, issued.ExpiresAt))
	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}

// browserFixtureRequestTrusted mewajibkan tiga bukti sekaligus: request datang
// lewat proxy Vite yang mengetahui kunci sementara, host/referer adalah origin
// loopback yang diizinkan, dan Fetch Metadata menyatakan navigasi same-origin.
// RemoteAddr sengaja tidak dipakai sebagai bukti karena reverse proxy lokal
// juga akan terlihat berasal dari loopback.
func (a *API) browserFixtureRequestTrusted(r *http.Request) bool {
	expectedKey := a.d.Config.BrowserFixture.ProxyKey
	providedKey := r.Header.Get(browserFixtureProxyHeader)
	if len(expectedKey) < 32 || len(providedKey) != len(expectedKey) || subtle.ConstantTimeCompare([]byte(providedKey), []byte(expectedKey)) != 1 {
		return false
	}
	fetchSite := r.Header.Get("Sec-Fetch-Site")
	if fetchSite == "none" {
		// Navigasi eksplisit dari address bar/bookmark tidak membawa Origin atau
		// Referer. Kunci proxy + Host tepercaya tetap wajib diperiksa di bawah.
		return r.Header.Get("Origin") == "" && r.Referer() == "" && a.browserFixtureHostAllowed(r.Host)
	}
	if fetchSite != "same-origin" {
		return false
	}
	referer, err := url.Parse(r.Referer())
	if err != nil || referer.Scheme == "" || referer.Host == "" {
		return false
	}
	refererOrigin := referer.Scheme + "://" + referer.Host
	requestOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	for _, allowed := range a.d.Config.BrowserFixture.AllowedOrigins {
		u, parseErr := url.Parse(allowed)
		if parseErr != nil || !strings.EqualFold(r.Host, u.Host) || !strings.EqualFold(refererOrigin, allowed) {
			continue
		}
		return requestOrigin == "" || strings.EqualFold(requestOrigin, allowed)
	}
	return false
}

func (a *API) browserFixtureHostAllowed(host string) bool {
	for _, allowed := range a.d.Config.BrowserFixture.AllowedOrigins {
		u, err := url.Parse(allowed)
		if err == nil && strings.EqualFold(host, u.Host) {
			return true
		}
	}
	return false
}

func (a *API) browserFixtureIdentity(profile string) (string, auth.Role, bool) {
	switch profile {
	case "superadmin":
		return a.d.Config.BrowserFixture.SuperadminEmail, auth.RoleSuperadmin, true
	case "manager":
		return a.d.Config.BrowserFixture.ManagerEmail, auth.RoleManager, true
	case "branch":
		return a.d.Config.BrowserFixture.BranchEmail, auth.RoleBranch, true
	default:
		return "", "", false
	}
}
