package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/masterimport"
)

type masterImportEvidenceRepo struct {
	evidence []masterimport.EvidenceItem
}

func (r masterImportEvidenceRepo) Snapshot(context.Context) (masterimport.Snapshot, error) {
	return masterimport.Snapshot{}, nil
}
func (r masterImportEvidenceRepo) SavePreview(context.Context, masterimport.SavePreviewInput) (masterimport.SavedPreview, error) {
	return masterimport.SavedPreview{}, nil
}
func (r masterImportEvidenceRepo) LoadEvidence(_ context.Context, attemptID string) ([]masterimport.EvidenceItem, error) {
	if strings.TrimSpace(attemptID) == "" {
		return nil, masterimport.ErrAttemptNotFound
	}
	return r.evidence, nil
}
func (r masterImportEvidenceRepo) Apply(context.Context, auth.Actor, masterimport.ApplyRequest) (masterimport.ApplyResult, error) {
	return masterimport.ApplyResult{}, nil
}
func (r masterImportEvidenceRepo) Rollback(context.Context, auth.Actor, string) (masterimport.RollbackResult, error) {
	return masterimport.RollbackResult{}, nil
}

func masterImportEvidenceRequest(actor auth.Actor) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/master-imports/impact/attempt-1/evidence", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "attempt-1")
	ctx := context.WithValue(auth.WithActor(req.Context(), actor), chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func TestAdminMasterImpactEvidenceHanyaSuperadminDanJSONTersanitasi(t *testing.T) {
	repo := masterImportEvidenceRepo{evidence: []masterimport.EvidenceItem{{
		SourceRow: 3, EntityKind: masterimport.EntityNote, EntityCode: "AR", Value: "catatan sintetis privat",
	}}}
	service := masterimport.NewService(repo, nil, "staging")
	api := New(Deps{MasterImport: service})

	t.Run("superadmin", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := masterImportEvidenceRequest(auth.Actor{UserID: "sa-1", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin})
		api.adapt(api.adminMasterImpactEvidence).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		for _, want := range []string{`"source_row":3`, `"entity_kind":"note"`, `"entity_code":"AR"`, `"value":"catatan sintetis privat"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("response tidak memuat %s: %s", want, body)
			}
		}
		if strings.Contains(body, "SourceRow") || strings.Contains(body, "EntityCode") {
			t.Fatalf("kontrak JSON harus snake_case: %s", body)
		}
	})

	t.Run("manager", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := masterImportEvidenceRequest(auth.Actor{UserID: "m-1", Kind: auth.KindAdmin, Role: auth.RoleManager})
		api.adapt(api.adminMasterImpactEvidence).ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "catatan sintetis privat") {
			t.Fatalf("evidence privat bocor ke respons manager: %s", rr.Body.String())
		}
	})
}
