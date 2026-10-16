package httpapi

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
)

// browserFixtureSession menerbitkan sesi melalui alur Login normal untuk akun
// seed sintetis. Route ini hanya didaftarkan saat APP_ENV=local dan fixture
// diaktifkan eksplisit; password internal tidak pernah diterima dari browser.
func (a *API) browserFixtureSession(w http.ResponseWriter, r *http.Request) error {
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
