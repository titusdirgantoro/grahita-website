package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
)

// settings mengembalikan seluruh pengaturan untuk pengguna yang sudah login.
// Kedua portal memakainya untuk konstanta UI (mis. biaya packaging).
func (a *API) settings(w http.ResponseWriter, r *http.Request) error {
	if a.d.Settings == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengaturan belum tersedia.")
	}
	all, err := a.d.Settings.All(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, data(all))
	return nil
}

// adminSettings adalah pembacaan pengaturan khusus Superadmin.
func (a *API) adminSettings(w http.ResponseWriter, r *http.Request) error {
	return a.settings(w, r)
}

// adminSettingsUpdate menyimpan satu pengaturan (Superadmin saja).
func (a *API) adminSettingsUpdate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Settings == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengaturan belum tersedia.")
	}
	var req updateSettingRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if !isKnownSetting(req.Key) {
		return apierr.Validation("Kunci pengaturan tidak dikenal.",
			map[string]string{"key": "bukan kunci pengaturan yang dikenal"})
	}
	raw, err := json.Marshal(req.Value)
	if err != nil {
		return apierr.Validation("Nilai pengaturan tidak bisa disimpan.",
			map[string]string{"value": "bukan JSON yang valid"})
	}

	actor := auth.ActorFrom(r.Context())
	if err := a.d.Settings.Update(r.Context(), actor, req.Key, raw); err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(map[string]any{"key": req.Key, "value": json.RawMessage(raw)}))
	return nil
}

func isKnownSetting(key string) bool {
	for _, k := range settings.KnownKeys {
		if k == key {
			return true
		}
	}
	return false
}
