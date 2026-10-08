package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// settings mengembalikan konstanta operasional untuk pengguna yang sudah
// login. Kedua portal memakainya untuk konstanta UI (mis. biaya packaging).
// Setelan internal `ai` TIDAK termasuk — hanya Superadmin yang membacanya
// lewat /admin/settings (PRD §2.1, §4.9).
func (a *API) settings(w http.ResponseWriter, r *http.Request) error {
	if a.d.Settings == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengaturan belum tersedia.")
	}
	vals, err := a.d.Settings.Runtime(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, data(vals))
	return nil
}

// adminSettings adalah pembacaan pengaturan lengkap khusus Superadmin.
func (a *API) adminSettings(w http.ResponseWriter, r *http.Request) error {
	if a.d.Settings == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengaturan belum tersedia.")
	}
	vals, err := a.d.Settings.All(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(vals))
	return nil
}

// updateSettingRequest adalah body PUT /api/v1/admin/settings.
//
// Dua bentuk diterima:
//   - {"key":"biaya_packaging","value":4000} — satu kunci (kompatibel).
//   - {"values":{"biaya_packaging":4000,"cnc_hari":7}} — banyak kunci sekaligus.
type updateSettingRequest struct {
	Key    *string                    `json:"key"`
	Value  json.RawMessage            `json:"value"`
	Values map[string]json.RawMessage `json:"values"`
}

// changes menyusun peta perubahan dari body yang dikirim.
func (q updateSettingRequest) changes() (map[string]json.RawMessage, error) {
	switch {
	case len(q.Values) > 0:
		if q.Key != nil {
			return nil, apierr.Validation("Kirim salah satu: key/value atau values, bukan keduanya.", nil)
		}
		return q.Values, nil
	case q.Key != nil:
		if len(q.Value) == 0 {
			return nil, apierr.Validation("Nilai pengaturan wajib diisi.", map[string]string{"value": "kosong"})
		}
		return map[string]json.RawMessage{*q.Key: q.Value}, nil
	default:
		return nil, apierr.Validation("Body harus memuat key/value atau values.", nil)
	}
}

// adminSettingsUpdate menyimpan satu atau beberapa pengaturan (Superadmin saja).
func (a *API) adminSettingsUpdate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Settings == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengaturan belum tersedia.")
	}
	var req updateSettingRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	changes, err := req.changes()
	if err != nil {
		return err
	}
	actor := auth.ActorFrom(r.Context())
	if err := a.d.Settings.Update(r.Context(), actor, changes); err != nil {
		return mapDomainErr(err)
	}

	// Respons: satu kunci mengembalikan {key,value} (kompatibel), banyak kunci
	// mengembalikan {values:{...}} berisi nilai kanonik yang tersimpan.
	if req.Key != nil {
		vals, err := a.d.Settings.All(r.Context(), actor)
		if err != nil {
			return mapDomainErr(err)
		}
		httpx.OK(w, data(map[string]any{"key": *req.Key, "value": vals[*req.Key]}))
		return nil
	}
	vals, err := a.d.Settings.All(r.Context(), actor)
	if err != nil {
		return mapDomainErr(err)
	}
	saved := make(map[string]json.RawMessage, len(changes))
	for k := range changes {
		saved[k] = vals[k]
	}
	httpx.OK(w, data(map[string]any{"values": saved}))
	return nil
}
