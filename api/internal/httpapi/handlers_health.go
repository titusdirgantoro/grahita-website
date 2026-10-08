package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/platform/mailer"
	"github.com/grahita/portal/api/internal/platform/pdf"
	"github.com/grahita/portal/api/internal/platform/storage"
)

// jakarta mengembalikan zona waktu bisnis untuk tampilan meta.
func jakarta() *time.Location { return clock.Jakarta() }

// healthz adalah liveness: proses hidup. Tidak menyentuh dependensi apa pun
// sehingga tidak ikut gagal saat database sedang sibuk.
func (a *API) healthz(w http.ResponseWriter, _ *http.Request) error {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": a.d.Version,
	})
	return nil
}

// readyz adalah readiness: dependensi wajib benar-benar siap melayani
// (Architecture.md §8).
//
// Database dan migrasi bersifat WAJIB. Storage, Gotenberg, dan SMTP diperiksa
// hanya bila dikonfigurasi; yang belum dikonfigurasi dilaporkan apa adanya
// dan tidak menjatuhkan status.
func (a *API) readyz(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	checks := map[string]string{}
	ready := true

	if a.d.Health == nil {
		checks["database"] = "tidak dikonfigurasi"
		ready = false
	} else {
		if err := a.d.Health.Ping(ctx); err != nil {
			checks["database"] = "gagal: " + err.Error()
			ready = false
		} else {
			checks["database"] = "ok"
		}

		version, err := a.d.Health.MigrationVersion(ctx)
		switch {
		case err != nil:
			checks["migrations"] = "gagal: " + err.Error()
			ready = false
		case version == 0:
			checks["migrations"] = "belum ada migrasi diterapkan"
			ready = false
		default:
			checks["migrations"] = fmt.Sprintf("ok (versi %d)", version)
		}
	}

	optional := []struct {
		name   string
		health func(context.Context) error
	}{
		{"storage", a.d.Storage.Health},
		{"gotenberg", a.d.Gotenberg.Health},
		{"smtp", a.d.Mailer.Health},
	}
	for _, dep := range optional {
		err := dep.health(ctx)
		switch {
		case err == nil:
			checks[dep.name] = "ok"
		case errors.Is(err, storage.ErrNotConfigured),
			errors.Is(err, pdf.ErrNotConfigured),
			errors.Is(err, mailer.ErrNotConfigured):
			checks[dep.name] = "tidak dikonfigurasi"
		default:
			checks[dep.name] = "gagal: " + err.Error()
			ready = false
		}
	}

	status := http.StatusOK
	state := "ready"
	if !ready {
		status = http.StatusServiceUnavailable
		state = "not ready"
	}
	httpx.JSON(w, status, map[string]any{
		"status":  state,
		"version": a.d.Version,
		"checks":  checks,
	})
	return nil
}

// meta adalah informasi runtime yang dipakai SPA saat memuat halaman.
func (a *API) meta(w http.ResponseWriter, _ *http.Request) error {
	httpx.OK(w, data(map[string]any{
		"app":           "grahita-portal",
		"version":       a.d.Version,
		"env":           a.d.Config.AppEnv,
		"api_base":      "/api/v1",
		"customer_host": a.d.Config.CustomerHost,
		"admin_host":    a.d.Config.AdminHost,
		"waktu_wib":     time.Now().In(jakarta()).Format(time.RFC3339),
	}))
	return nil
}
