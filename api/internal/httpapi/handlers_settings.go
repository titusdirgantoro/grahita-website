package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/settings"
)

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

// adminOCRStatus memberi Superadmin informasi kesiapan konfigurasi tanpa
// mengekspos kunci provider. Opsi manual selalu tersedia.
func (a *API) adminOCRStatus(w http.ResponseWriter, r *http.Request) error {
	if a.d.Settings == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan pengaturan belum tersedia.")
	}
	ai, err := a.d.Settings.AISettings(r.Context())
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(map[string]any{
		"ai_active":      ai.Aktif,
		"provider_ready": ocrProviderReady(a.d.Config),
	}))
	return nil
}

// ocrProviderReady hanya berarti konfigurasi provider tersedia pada proses
// API. Kunci tidak pernah dikembalikan; pengujian provider dan konfigurasi
// worker tetap wajib sebelum otomatisasi dinyalakan di produksi.
func ocrProviderReady(cfg config.Config) bool {
	switch cfg.OCRProvider {
	case config.OCRProviderMock:
		return cfg.IsLocal()
	case "", config.OCRProviderClaude:
		return strings.TrimSpace(cfg.AnthropicAPIKey) != ""
	default:
		return false
	}
}

// updateSettingRequest adalah body PUT /api/v1/admin/settings.
//
// Dua bentuk diterima:
//   - {"key":"biaya_packaging","value":4000} — satu kunci (kompatibel).
//   - {"values":{"biaya_packaging":4000,"token_hari":14}} — banyak kunci sekaligus.
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
	// Produksi/staging tidak boleh mengaktifkan AI saat provider belum siap.
	// Validasi bentuk ai tetap milik settings.Service; pemeriksaan ini hanya
	// dilakukan pada objek valid agar pesan galat input tidak tertutup.
	if raw, ada := changes[settings.AI]; ada && !a.d.Config.IsLocal() {
		if canonical, message := settings.Check(settings.AI, raw); message == "" {
			var pilihan struct {
				Aktif bool `json:"aktif"`
			}
			if err := json.Unmarshal(canonical, &pilihan); err == nil && pilihan.Aktif && !ocrProviderReady(a.d.Config) {
				return apierr.Validation("Verifikasi AI belum tersedia. Minta operator menyiapkan provider terlebih dahulu.",
					map[string]string{"ai": "provider AI belum dikonfigurasi"})
			}
		}
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
