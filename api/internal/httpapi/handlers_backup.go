package httpapi

import (
	"net/http"
	"strconv"

	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// adminBackupRuns adalah pembacaan status cadangan terenkripsi (F1-21).
//
// Hanya Superadmin: halaman Pengaturan memang halaman Superadmin (PRD §2.1)
// dan Architecture.md §7 menempatkan hasil backup di sana. Endpoint ini
// membaca catatan backup_runs apa adanya — kegagalan muncul sebagai baris
// sukses=false berikut ringkasan "perlu tindakan", sehingga dashboard admin
// (F1-20) bisa memakai `ringkasan.perlu_tindakan` sebagai chip.
func (a *API) adminBackupRuns(w http.ResponseWriter, r *http.Request) error {
	if a.d.Backups == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan cadangan belum tersedia.")
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return apierr.Validation("Parameter limit harus bilangan positif.", map[string]string{"limit": "bukan bilangan positif"})
		}
		if n > 200 {
			n = 200
		}
		limit = n
	}
	ringkasan, err := a.d.Backups.Ringkasan(r.Context(), limit)
	if err != nil {
		return err
	}
	httpx.OK(w, data(ringkasan))
	return nil
}
