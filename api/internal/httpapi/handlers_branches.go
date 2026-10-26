package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/branch"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// createBranchRequest adalah body POST /api/v1/admin/branches (PRD §4.4).
type createBranchRequest struct {
	Nama           string `json:"nama" validate:"required,min=2,max=120"`
	NamaKepala     string `json:"nama_kepala" validate:"omitempty,min=2,max=120"`
	Email          string `json:"email" validate:"omitempty,email,max=254"`
	JabatanKepala  string `json:"jabatan_kepala" validate:"omitempty,max=120"`
	Telepon        string `json:"telepon" validate:"omitempty,max=40"`
	IsCNC          bool   `json:"is_cnc"`
	IsTempo        bool   `json:"is_tempo"`
	TanggalBayar   int    `json:"tanggal_bayar"`
	TahanBilaTelat bool   `json:"tahan_bila_telat"`
	PajakIn        bool   `json:"pajak_in"`
	// IsActive kosong berarti aktif (default cabang baru).
	IsActive *bool `json:"is_active"`
}

// updateBranchRequest adalah body PUT /api/v1/admin/branches/{id}.
// Semua field opsional: yang dikirim saja yang diubah.
type updateBranchRequest struct {
	Nama           *string `json:"nama"`
	NamaKepala     *string `json:"nama_kepala"`
	Email          *string `json:"email"`
	JabatanKepala  *string `json:"jabatan_kepala"`
	Telepon        *string `json:"telepon"`
	IsCNC          *bool   `json:"is_cnc"`
	IsTempo        *bool   `json:"is_tempo"`
	TanggalBayar   *int    `json:"tanggal_bayar"`
	TahanBilaTelat *bool   `json:"tahan_bila_telat"`
	PajakIn        *bool   `json:"pajak_in"`
	IsActive       *bool   `json:"is_active"`
}

// adminBranches adalah master cabang: daftar + filter (PRD §4.4).
//
// Filter: q (cari nama/email), aktif, cnc, tempo — semuanya opsional.
func (a *API) adminBranches(w http.ResponseWriter, r *http.Request) error {
	if a.d.Branches == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan master cabang belum tersedia.")
	}
	f, err := branchFilter(r)
	if err != nil {
		return err
	}
	list, err := a.d.Branches.List(r.Context(), auth.ActorFrom(r.Context()), f)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}

// adminBranch mengambil satu cabang berdasarkan id.
func (a *API) adminBranch(w http.ResponseWriter, r *http.Request) error {
	if a.d.Branches == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan master cabang belum tersedia.")
	}
	b, err := a.d.Branches.Get(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(b))
	return nil
}

// adminBranchCreate menambahkan cabang baru.
func (a *API) adminBranchCreate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Branches == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan master cabang belum tersedia.")
	}
	var req createBranchRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	b, err := a.d.Branches.Create(r.Context(), auth.ActorFrom(r.Context()), branch.NewBranch{
		Nama:           req.Nama,
		NamaKepala:     req.NamaKepala,
		Email:          req.Email,
		JabatanKepala:  req.JabatanKepala,
		Telepon:        req.Telepon,
		IsCNC:          req.IsCNC,
		IsTempo:        req.IsTempo,
		TanggalBayar:   req.TanggalBayar,
		TahanBilaTelat: req.TahanBilaTelat,
		PajakIn:        req.PajakIn,
		IsActive:       req.IsActive,
	})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.Created(w, data(b))
	return nil
}

// adminBranchUpdate mengubah data cabang, termasuk mengaktifkan atau
// menonaktifkannya (PRD AP-CAB-03). Cabang nonaktif tidak bisa login dan
// tidak muncul di pilihan aktif.
func (a *API) adminBranchUpdate(w http.ResponseWriter, r *http.Request) error {
	if a.d.Branches == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan master cabang belum tersedia.")
	}
	var req updateBranchRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	b, err := a.d.Branches.Update(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"), branch.Patch{
		Nama:           req.Nama,
		NamaKepala:     req.NamaKepala,
		Email:          req.Email,
		JabatanKepala:  req.JabatanKepala,
		Telepon:        req.Telepon,
		IsCNC:          req.IsCNC,
		IsTempo:        req.IsTempo,
		TanggalBayar:   req.TanggalBayar,
		TahanBilaTelat: req.TahanBilaTelat,
		PajakIn:        req.PajakIn,
		IsActive:       req.IsActive,
	})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(b))
	return nil
}

// adminBranchDelete menghapus cabang yang belum punya pesanan; cabang yang
// sudah punya pesanan hanya boleh dinonaktifkan lewat PUT (PRD AP-CAB-04).
func (a *API) adminBranchDelete(w http.ResponseWriter, r *http.Request) error {
	if a.d.Branches == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan master cabang belum tersedia.")
	}
	if err := a.d.Branches.Delete(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id")); err != nil {
		return mapDomainErr(err)
	}
	httpx.NoContent(w)
	return nil
}

// adminBranchToken membuat token registrasi sekali pakai untuk cabang yang
// belum punya akun (PRD §3.1 CP-REG-01, §4.4 AP-CAB-02). Nilai token hanya
// ada di respons ini dan tidak bisa dibaca lagi setelahnya; token lama yang
// belum terpakai otomatis batal.
func (a *API) adminBranchToken(w http.ResponseWriter, r *http.Request) error {
	if a.d.Branches == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan master cabang belum tersedia.")
	}
	// Masa berlaku mengikuti pengaturan `token_hari` (PRD §4.9, default 14 hari).
	issued, err := a.d.Branches.GenerateToken(r.Context(), auth.ActorFrom(r.Context()),
		chi.URLParam(r, "id"), a.invitationTTL(r))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(issued))
	return nil
}

// branchFilter menyusun penyaring daftar cabang dari query string.
func branchFilter(r *http.Request) (branch.Filter, error) {
	q := r.URL.Query()
	f := branch.Filter{Q: q.Get("q")}
	var err error
	if f.Active, err = boolQuery(r, "aktif"); err != nil {
		return f, err
	}
	if f.CNC, err = boolQuery(r, "cnc"); err != nil {
		return f, err
	}
	if f.Tempo, err = boolQuery(r, "tempo"); err != nil {
		return f, err
	}
	return f, nil
}

// boolQuery membaca parameter boolean opsional; kosong berarti tidak disaring.
func boolQuery(r *http.Request, key string) (*bool, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, apierr.Validation("Parameter "+key+" harus bernilai true atau false.",
			map[string]string{key: "bukan nilai boolean"})
	}
	return &v, nil
}
