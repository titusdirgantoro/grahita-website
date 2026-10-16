package httpapi

import (
	"errors"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/masterimport"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

const masterImportMultipartOverhead = 1 << 20

func masterImportUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan preview impor master belum tersedia.")
}

func (a *API) adminMasterImpactPreview(w http.ResponseWriter, r *http.Request) error {
	if a.d.MasterImport == nil {
		return masterImportUnavailable()
	}
	r.Body = http.MaxBytesReader(w, r.Body, masterimport.MaxWorkbookSize+masterImportMultipartOverhead)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return apierr.New(http.StatusRequestEntityTooLarge, "IMPORT_FILE_TOO_LARGE", "Workbook melebihi batas 5 MiB.")
		}
		return apierr.Validation("Form upload tidak valid.", map[string]string{"file": "kirim satu workbook XLSX pada field file"})
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	file, header, err := r.FormFile("file")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return apierr.Validation("Workbook wajib diunggah.", map[string]string{"file": "wajib"})
		}
		return apierr.Validation("Workbook tidak dapat dibaca.", map[string]string{"file": "tidak valid"})
	}
	defer func() { _ = file.Close() }()
	if !xlsxFilename(header) {
		return apierr.New(http.StatusUnsupportedMediaType, "IMPORT_FILE_UNSUPPORTED", "Berkas harus berformat .xlsx.")
	}
	preview, err := a.d.MasterImport.Preview(r.Context(), auth.ActorFrom(r.Context()), file, header.Size)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.Created(w, data(preview))
	return nil
}

func xlsxFilename(h *multipart.FileHeader) bool {
	return strings.HasSuffix(strings.ToLower(h.Filename), ".xlsx")
}

func (a *API) adminMasterImpactEvidence(w http.ResponseWriter, r *http.Request) error {
	if a.d.MasterImport == nil {
		return masterImportUnavailable()
	}
	evidence, err := a.d.MasterImport.Evidence(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(evidence))
	return nil
}

type masterImpactApplyRequest struct {
	PreviewHash    string                  `json:"preview_hash" validate:"required,len=64"`
	IdempotencyKey string                  `json:"idempotency_key" validate:"required,min=8,max=200"`
	Decisions      []masterimport.Decision `json:"decisions"`
}

func (a *API) adminMasterImpactApply(w http.ResponseWriter, r *http.Request) error {
	if a.d.MasterImport == nil {
		return masterImportUnavailable()
	}
	var req masterImpactApplyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	result, err := a.d.MasterImport.Apply(r.Context(), auth.ActorFrom(r.Context()), masterimport.ApplyRequest{
		AttemptID: chi.URLParam(r, "id"), PreviewHash: req.PreviewHash,
		IdempotencyKey: req.IdempotencyKey, Decisions: req.Decisions,
	})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(result))
	return nil
}

func (a *API) adminMasterImpactRollback(w http.ResponseWriter, r *http.Request) error {
	if a.d.MasterImport == nil {
		return masterImportUnavailable()
	}
	result, err := a.d.MasterImport.Rollback(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(result))
	return nil
}
