package httpapi

import (
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/tutorial"
)

// tutorialTopik adalah bentuk satu topik pada respons API. Bentuknya sengaja
// eksplisit (bukan struct domain) supaya kontrak OpenAPI tidak pernah berubah
// diam-diam saat field internal bertambah.
type tutorialTopik struct {
	ID      string   `json:"id"`
	Ikon    string   `json:"ikon"`
	Judul   string   `json:"judul"`
	Ringkas string   `json:"ringkas"`
	Langkah []string `json:"langkah"`
}

// tutorialResponse adalah bentuk data respons baca/tulis konten tutorial.
type tutorialResponse struct {
	Topik []tutorialTopik `json:"topik"`
}

func tutorialDTO(topik []tutorial.Topic) tutorialResponse {
	out := tutorialResponse{Topik: make([]tutorialTopik, 0, len(topik))}
	for _, t := range topik {
		langkah := t.Langkah
		if langkah == nil {
			langkah = []string{}
		}
		out.Topik = append(out.Topik, tutorialTopik{
			ID: t.ID, Ikon: t.Ikon, Judul: t.Judul, Ringkas: t.Ringkas, Langkah: langkah,
		})
	}
	return out
}

// tutorialRead melayani GET /api/v1/tutorial (portal cabang).
//
// Pembacaan hanya menuntut sesi yang sah: halaman tutorial adalah halaman
// informasi, dan konten yang dilayani tidak bergantung pada cabang mana pun —
// tidak ada parameter cabang yang bisa dipakai untuk membocorkan data lain.
func (a *API) tutorialRead(w http.ResponseWriter, r *http.Request) error {
	if a.d.Tutorial == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan tutorial belum tersedia.")
	}
	topik, err := a.d.Tutorial.List(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(tutorialDTO(topik)))
	return nil
}

// adminTutorial melayani GET /api/v1/admin/tutorial (Superadmin).
//
// Isinya sama dengan pembacaan portal cabang; kapabilitasnya ditegakkan router
// (`admin.tutorial`) supaya portal admin punya satu jalur baca yang eksplisit
// tanpa membuka permukaan lintas-role baru.
func (a *API) adminTutorial(w http.ResponseWriter, r *http.Request) error {
	return a.tutorialRead(w, r)
}

// updateTutorialRequest adalah body PUT /api/v1/admin/tutorial.
//
// Bentuknya "ganti seluruh isi": urutan array = urutan tampil, topik yang
// tidak disertakan dihapus. `id` dikosongkan untuk topik baru.
type updateTutorialRequest struct {
	Topik []tutorial.Input `json:"topik"`
}

// adminTutorialUpdate melayani PUT /api/v1/admin/tutorial (Superadmin):
// memvalidasi + menyamitasi konten, menyimpannya dalam satu transaksi beserta
// audit per topik, lalu mengembalikan bentuk kanonik yang tersimpan.
func (a *API) adminTutorialUpdate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Tutorial == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan tutorial belum tersedia.")
	}
	var req updateTutorialRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.Topik == nil {
		return apierr.Validation("Konten tutorial tidak valid.",
			map[string]string{"topik": "daftar topik wajib dikirim"})
	}
	saved, err := a.d.Tutorial.Replace(r.Context(), auth.ActorFrom(r.Context()), req.Topik)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(tutorialDTO(saved)))
	return nil
}
