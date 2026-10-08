package httpapi

import (
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// data membungkus payload sukses dengan amplop yang konsisten.
func data(v any) map[string]any { return map[string]any{"data": v} }

// userDTO adalah representasi akun yang boleh dilihat frontend.
type userDTO struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Role     string `json:"role"`
	BranchID string `json:"branch_id,omitempty"`
	Nama     string `json:"nama"`
	Email    string `json:"email"`
}

func actorDTO(a actor) userDTO {
	return userDTO{
		ID:       a.UserID,
		Kind:     string(a.Kind),
		Role:     string(a.Role),
		BranchID: a.BranchID,
		Nama:     a.Nama,
		Email:    a.Email,
	}
}

// loginRequest adalah body POST /api/v1/auth/login.
type loginRequest struct {
	Email    string `json:"email" validate:"required,email,max=254"`
	Password string `json:"password" validate:"required,min=8,max=200"`
}

// updateSettingRequest adalah body PUT /api/v1/admin/settings.
type updateSettingRequest struct {
	Key   string `json:"key" validate:"required,max=64"`
	Value any    `json:"value" validate:"required"`
}

// login menangani POST /api/v1/auth/login untuk kedua portal.
func (a *API) login(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}

	email := auth.NormalizeEmail(req.Email)
	if a.d.LoginLimiter != nil {
		key := httpx.ClientIP(r) + "|" + email
		if !a.d.LoginLimiter.Allow(key) {
			return apierr.RateLimited("Terlalu banyak percobaan masuk. Coba lagi beberapa saat lagi.")
		}
	}

	issued, err := a.d.Auth.Login(r.Context(), email, req.Password, auth.RequestMeta{
		IP:        httpx.ClientIP(r),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		return mapDomainErr(err)
	}
	if a.d.LoginLimiter != nil {
		a.d.LoginLimiter.Reset(httpx.ClientIP(r) + "|" + email)
	}

	http.SetCookie(w, sessionCookie(a.d.Config, issued.Token, issued.ExpiresAt))
	httpx.OK(w, data(map[string]any{"user": actorDTO(issued.Actor)}))
	return nil
}

// logout menangani POST /api/v1/auth/logout (idempoten).
func (a *API) logout(w http.ResponseWriter, r *http.Request) error {
	if token := sessionToken(a.d.Config, r); token != "" {
		_ = a.d.Auth.Logout(r.Context(), token)
	}
	http.SetCookie(w, clearSessionCookie(a.d.Config))
	httpx.NoContent(w)
	return nil
}

// me mengembalikan profil + role + cabang aktor yang sedang login.
func (a *API) me(w http.ResponseWriter, r *http.Request) error {
	actor := auth.ActorFrom(r.Context())
	httpx.OK(w, data(map[string]any{"user": actorDTO(actor)}))
	return nil
}
