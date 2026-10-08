package httpapi

import (
	"net/http"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// catalog mengembalikan katalog sesuai sudut pandang aktor.
func (a *API) catalog(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan katalog belum tersedia.")
	}
	list, err := a.d.Catalog.Products(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}

// myBranch mengembalikan data cabang milik akun cabang yang login.
func (a *API) myBranch(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan katalog belum tersedia.")
	}
	actor := auth.ActorFrom(r.Context())
	if !actor.IsBranch() || actor.BranchID == "" {
		return apierr.Forbidden("Endpoint ini hanya untuk akun cabang.")
	}
	branch, err := a.d.Catalog.Branch(r.Context(), actor, actor.BranchID)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(branch))
	return nil
}

// adminProducts adalah daftar produk untuk katalog admin.
func (a *API) adminProducts(w http.ResponseWriter, r *http.Request) error {
	if a.d.Catalog == nil {
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan katalog belum tersedia.")
	}
	list, err := a.d.Catalog.Products(r.Context(), auth.ActorFrom(r.Context()))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}
