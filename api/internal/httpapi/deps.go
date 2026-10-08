// Package httpapi adalah satu-satunya lapisan transport HTTP.
//
// Aturan ketergantungan (Architecture.md, SystemDesign.md §2):
//
//	httpapi -> service -> repository -> database
//
// Paket ini boleh mengimpor paket service (auth, branch, settings, catalog)
// dan platform, tetapi TIDAK boleh mengimpor repository (*/postgres) maupun
// paket query hasil sqlc. Aturan ini ditegakkan otomatis oleh pengujian di
// internal/arch.
package httpapi

import (
	"context"
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/branch"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/platform/mailer"
	"github.com/grahita/portal/api/internal/platform/pdf"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/grahita/portal/api/internal/settings"
	"github.com/grahita/portal/api/internal/users"
)

// HealthChecker adalah dependensi yang diperiksa /readyz. Implementasinya
// ada di cmd/server supaya paket ini tidak menyentuh pgx.
type HealthChecker interface {
	Ping(ctx context.Context) error
	MigrationVersion(ctx context.Context) (int64, error)
}

// Deps adalah seluruh dependensi router.
type Deps struct {
	Config  config.Config
	Version string

	Health   HealthChecker
	Auth     *auth.Service
	Settings *settings.Service
	Catalog  *catalog.Service
	Users    *users.Service
	Branches *branch.Service

	Storage   storage.Store
	Gotenberg pdf.Renderer
	Mailer    mailer.Mailer

	// LoginLimiter membatasi percobaan login GAGAL per akun + per IP
	// (PRD CP-AUT-05: 5 percobaan gagal / 15 menit).
	LoginLimiter *httpx.RateLimiter

	// ForgotLimiter membatasi permintaan tautan atur ulang per IP, baik untuk
	// forgot maupun reset (Architecture.md §6: lupa password dibatasi).
	ForgotLimiter *httpx.RateLimiter
}

// API memegang dependensi untuk handler.
type API struct {
	d Deps
}

// New membuat handler API.
func New(d Deps) *API {
	if d.Storage == nil {
		d.Storage = storage.Disabled{}
	}
	if d.Gotenberg == nil {
		d.Gotenberg = pdf.Disabled{}
	}
	if d.Mailer == nil {
		d.Mailer = mailer.Log{}
	}
	return &API{d: d}
}

// handler adalah bentuk fungsi handler tanpa pembungkus tambahan.
type handler func(http.ResponseWriter, *http.Request) error

// adapt mengubah handler yang mengembalikan error menjadi http.HandlerFunc
// sekaligus menerjemahkan error ke bentuk seragam.
func (a *API) adapt(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			httpx.Fail(w, r, err)
		}
	}
}
