package httpapi

import (
	"errors"
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
)

// authenticate memuat aktor dari cookie sesi dan menitipkannya ke context.
//
// Sesi yang tidak valid TIDAK langsung menolak request: routing berikutnya
// yang memutuskan. Ini membuat endpoint publik tetap bisa melaporkan status
// "belum login" dengan 200, sementara endpoint terproteksi memberi 401.
func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := sessionToken(a.d.Config, r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		actor, err := a.d.Auth.Authenticate(r.Context(), token)
		if err != nil {
			// Termasuk sesi kedaluwarsa/dicabut maupun kesalahan teknis:
			// request lanjut sebagai anonim dan endpoint terproteksi yang
			// akan menolaknya dengan 401.
			if errors.Is(err, auth.ErrSessionInvalid) {
				http.SetCookie(w, clearSessionCookie(a.d.Config))
			}
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
	})
}

// requireAuth menolak request dari pengguna yang belum login.
func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.ActorFrom(r.Context()).IsAuthenticated() {
			writeUnauthenticated(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireCap menolak request yang role-nya tidak punya kapabilitas tertentu.
//
// Inilah yang membuat otorisasi benar-benar ditegakkan di backend:
// menyembunyikan menu di UI saja tidak pernah cukup (Architecture.md §6).
func (a *API) requireCap(cap auth.Capability) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := auth.ActorFrom(r.Context())
			if !actor.IsAuthenticated() {
				writeUnauthenticated(w, r)
				return
			}
			if !actor.Can(cap) {
				httpErr(w, r, apierr.Forbidden("Anda tidak memiliki akses ke tindakan ini."))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeUnauthenticated(w http.ResponseWriter, r *http.Request) {
	httpErr(w, r, apierr.Unauthenticated("Anda harus masuk terlebih dahulu."))
}
