package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/ocrsecret"
	"github.com/grahita/portal/api/internal/payment"
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
	ready, keyStatus, err := a.ocrProviderStatus(r.Context())
	if err != nil {
		return apierr.Wrap(err, http.StatusServiceUnavailable, apierr.CodeUnavailable, "Status kunci AI belum dapat diperiksa.")
	}
	result := map[string]any{
		"ai_active":      ai.Aktif,
		"provider_ready": ready,
		"provider":       a.d.Config.OCRProvider,
		"model":          "",
	}
	if a.d.Config.OCRProvider == config.OCRProviderGemini {
		result["model"] = payment.GeminiModel
		result["key_configured"] = keyStatus.Configured
		result["key_verified"] = keyStatus.Verified
		result["key_updated_at"] = keyStatus.UpdatedAt
	}
	httpx.OK(w, data(result))
	return nil
}

func (a *API) ocrProviderStatus(ctx context.Context) (bool, ocrsecret.Status, error) {
	if a.d.Config.OCRProvider == config.OCRProviderGemini {
		if a.d.OCRSecrets == nil {
			return false, ocrsecret.Status{}, nil
		}
		status, err := a.d.OCRSecrets.Status(ctx)
		return err == nil && status.Verified, status, err
	}
	return ocrProviderReady(a.d.Config), ocrsecret.Status{}, nil
}

func (a *API) geminiSecrets() (*ocrsecret.Service, error) {
	if a.d.Config.OCRProvider != config.OCRProviderGemini || a.d.OCRSecrets == nil {
		return nil, apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Provider Gemini belum dikonfigurasi pada server.")
	}
	return a.d.OCRSecrets, nil
}

func ocrKeyError(err error) error {
	switch {
	case errors.Is(err, ocrsecret.ErrForbidden):
		return apierr.Forbidden("Hanya Superadmin yang boleh mengelola kunci AI.")
	case errors.Is(err, ocrsecret.ErrInvalidKey):
		return apierr.Validation("Format API key Gemini tidak valid.", map[string]string{"api_key": "periksa API key"})
	case errors.Is(err, ocrsecret.ErrAIActive):
		return apierr.Conflict("AI_ACTIVE", "Matikan verifikasi otomatis sebelum mengganti atau menghapus API key.")
	case errors.Is(err, ocrsecret.ErrNotConfigured):
		return apierr.Validation("Simpan API key Gemini sebelum menguji koneksi.", nil)
	case errors.Is(err, ocrsecret.ErrStale):
		return apierr.Conflict("OCR_KEY_CHANGED", "API key berubah saat diuji. Uji ulang kunci terbaru.")
	default:
		return apierr.Wrap(err, http.StatusServiceUnavailable, apierr.CodeUnavailable, "Pengaturan API key Gemini belum dapat diproses.")
	}
}

func (a *API) adminOCRKeySave(w http.ResponseWriter, r *http.Request) error {
	secrets, err := a.geminiSecrets()
	if err != nil {
		return err
	}
	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := secrets.Save(r.Context(), auth.ActorFrom(r.Context()), req.APIKey); err != nil {
		return ocrKeyError(err)
	}
	httpx.OK(w, data(map[string]any{"key_configured": true, "key_verified": false}))
	return nil
}

func (a *API) adminOCRKeyTest(w http.ResponseWriter, r *http.Request) error {
	secrets, err := a.geminiSecrets()
	if err != nil {
		return err
	}
	if a.d.OCRTester == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Penguji Gemini belum tersedia.")
	}
	key, version, err := secrets.KeyForTest(r.Context())
	if err != nil {
		return ocrKeyError(err)
	}
	if err := a.d.OCRTester.TestKey(r.Context(), key); err != nil {
		return apierr.Validation("Uji koneksi Gemini 2.5 Flash-Lite gagal. Periksa API key, akses model, kuota, dan jaringan server.", nil)
	}
	if err := secrets.MarkVerified(r.Context(), auth.ActorFrom(r.Context()), version); err != nil {
		return ocrKeyError(err)
	}
	httpx.OK(w, data(map[string]any{"key_configured": true, "key_verified": true}))
	return nil
}

func (a *API) adminOCRKeyDelete(w http.ResponseWriter, r *http.Request) error {
	secrets, err := a.geminiSecrets()
	if err != nil {
		return err
	}
	if err := secrets.Delete(r.Context(), auth.ActorFrom(r.Context())); err != nil {
		return ocrKeyError(err)
	}
	httpx.OK(w, data(map[string]any{"key_configured": false, "key_verified": false}))
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
			ready, _, statusErr := a.ocrProviderStatus(r.Context())
			if statusErr != nil {
				return apierr.Wrap(statusErr, http.StatusServiceUnavailable, apierr.CodeUnavailable, "Status provider AI belum dapat diperiksa.")
			}
			if err := json.Unmarshal(canonical, &pilihan); err == nil && pilihan.Aktif && !ready {
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
