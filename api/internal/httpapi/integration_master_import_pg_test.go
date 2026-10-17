package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/masterimport"
	masterimportpg "github.com/grahita/portal/api/internal/masterimport/postgres"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

type fixedImpactParser struct {
	parsed masterimport.ParsedWorkbook
}

const impactSnapshotTestKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
const impactSnapshotOtherTestKey = "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="

func (p fixedImpactParser) Parse(io.Reader, int64) (masterimport.ParsedWorkbook, error) {
	return p.parsed, nil
}

func impactTestFingerprint(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func impactParsed(branches ...masterimport.SourceBranch) masterimport.ParsedWorkbook {
	for i := range branches {
		if branches[i].Row == 0 {
			branches[i].Row = i + 3
		}
		if branches[i].Fingerprint == "" {
			branches[i].Fingerprint = impactTestFingerprint(branches[i].SourceKey)
		}
		if branches[i].Tariffs == nil {
			branches[i].Tariffs = map[string]masterimport.MoneyComponent{}
		}
		for _, def := range masterimport.ProductDefinitions() {
			if _, ok := branches[i].Tariffs[def.Code]; !ok {
				branches[i].Tariffs[def.Code] = masterimport.MoneyComponent{Fingerprint: impactTestFingerprint("unavailable", def.Code)}
			}
		}
	}
	return masterimport.ParsedWorkbook{SourceHash: masterimport.ExpectedHash, Branches: branches}
}

func impactDecisionFor(preview masterimport.Preview, reasonCode, decision string) masterimport.Decision {
	for _, item := range preview.Items {
		if item.ReasonCode == reasonCode {
			return masterimport.Decision{SourceRow: item.SourceRow, EntityKind: item.EntityKind, EntityCode: item.EntityCode, Decision: decision, Reason: "keputusan uji eksplisit"}
		}
	}
	return masterimport.Decision{}
}

func impactPreviewRequest(t *testing.T, url string, cookies []*http.Cookie, csrf string, workbook []byte) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "impact.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(workbook); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(httpx.CSRFHeader, csrf)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, decoded
}

func impactJSONRequest(t *testing.T, url string, cookies []*http.Cookie, csrf string, body any) (int, map[string]any) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(httpx.CSRFHeader, csrf)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, decoded
}

func impactTableFingerprints(t *testing.T, f *pgFixture, tables []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(tables))
	for _, table := range tables {
		query := `SELECT md5(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text)::text, '[]')) FROM ` + table + ` AS t`
		var fingerprint string
		if err := f.pool.QueryRow(t.Context(), query).Scan(&fingerprint); err != nil {
			t.Fatalf("fingerprint tabel %s: %v", table, err)
		}
		out[table] = fingerprint
	}
	return out
}

func TestPGImpactPreviewProductionHanyaMenulisAuditPrivat(t *testing.T) {
	f := newPGFixture(t)
	parsed := impactParsed(masterimport.SourceBranch{
		SourceKey: "branch:production-preview-uji", Name: "Production Preview Uji", Position: "Kepala", HeadName: impactStringPtr("Kepala Uji"),
		ReviewCells: []masterimport.ReviewCell{{Column: "AR", Fingerprint: impactTestFingerprint("production-note"), Value: "evidence privat uji"}},
	})
	deps := f.deps
	deps.MasterImport = masterimport.NewService(masterimportpg.New(f.pool, impactSnapshotTestKey), fixedImpactParser{parsed: parsed}, "production")
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)

	saCookies, saCSRF := pgLogin(t, srv, "rina@example.test", "rahasia123")
	masterTables := []string{
		"branches", "products", "product_variants", "branch_prices", "branch_tariff_versions",
		"master_import_identities", "branch_users", "registration_tokens", "password_reset_tokens",
	}
	before := impactTableFingerprints(t, f, masterTables)

	status, body := impactPreviewRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/preview", saCookies, saCSRF, []byte("workbook sintetis parser uji"))
	if status != http.StatusCreated {
		t.Fatalf("preview production status=%d body=%v", status, body)
	}
	preview := body["data"].(map[string]any)
	attemptID := preview["attempt_id"].(string)
	previewHash := preview["preview_hash"].(string)

	applyBody := map[string]any{"preview_hash": previewHash, "idempotency_key": "production-denied-uji"}
	if status, _ := impactJSONRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/"+attemptID+"/apply", saCookies, saCSRF, applyBody); status != http.StatusConflict {
		t.Fatalf("apply production status=%d, ingin 409", status)
	}
	if status, _ := impactJSONRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/apply-runs/"+attemptID+"/rollback", saCookies, saCSRF, map[string]any{}); status != http.StatusConflict {
		t.Fatalf("rollback production status=%d, ingin 409", status)
	}

	after := impactTableFingerprints(t, f, masterTables)
	for _, table := range masterTables {
		if after[table] != before[table] {
			t.Fatalf("preview production memutasi tabel %s", table)
		}
	}

	var artifacts, attempts, items, evidence, applyRuns int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM master_import_artifacts WHERE environment=$1`, masterimport.ProductionPreviewEnvironment).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM master_import_attempts WHERE id=$1`, attemptID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE private_evidence IS NOT NULL) FROM master_import_preview_items WHERE attempt_id=$1`, attemptID).Scan(&items, &evidence); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM master_import_apply_runs`).Scan(&applyRuns); err != nil {
		t.Fatal(err)
	}
	if artifacts != 1 || attempts != 1 || items == 0 || evidence != 1 || applyRuns != 0 {
		t.Fatalf("audit preview production artifacts=%d attempts=%d items=%d evidence=%d apply_runs=%d", artifacts, attempts, items, evidence, applyRuns)
	}
}

func TestPGImpactPreviewDryRunRepeatAttemptDanOtorisasi(t *testing.T) {
	path := os.Getenv("IMPACT_WORKBOOK_PATH")
	if path == "" {
		t.Skip("IMPACT_WORKBOOK_PATH tidak diisi")
	}
	workbook, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := masterimport.NewExcelParser(masterimport.ExpectedHash).Parse(bytes.NewReader(workbook), int64(len(workbook)))
	if err != nil {
		t.Fatal(err)
	}
	var blankHeadSource masterimport.SourceBranch
	for _, branch := range parsed.Branches {
		if branch.HeadName == nil {
			blankHeadSource = branch
			break
		}
	}
	if blankHeadSource.SourceKey == "" {
		t.Fatal("sumber harus mempunyai satu kepala cabang blank")
	}
	var unmappedSource masterimport.SourceBranch
	for _, branch := range parsed.Branches {
		if branch.SourceKey != parsed.Branches[0].SourceKey && branch.SourceKey != blankHeadSource.SourceKey {
			unmappedSource = branch
			break
		}
	}
	var renamedMappedSource masterimport.SourceBranch
	for _, branch := range parsed.Branches {
		if branch.SourceKey != parsed.Branches[0].SourceKey && branch.SourceKey != blankHeadSource.SourceKey && branch.SourceKey != unmappedSource.SourceKey {
			renamedMappedSource = branch
			break
		}
	}
	f := newPGFixture(t)
	// Dua cabang existing dipetakan lewat nama sumber untuk membuktikan urutan
	// fail-closed: incomplete+akses usable selalu inconsistent; ready/registered
	// konsisten dengan kepala sumber blank meminta resolusi preservasi/revokasi.
	if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET nama=$1, identity_status='identity_incomplete' WHERE id=$2`, parsed.Branches[0].Name, f.branchA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET nama=$1, identity_status='registered' WHERE id=$2`, blankHeadSource.Name, f.branchB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET nama=$1, identity_status='registered' WHERE id=$2`, unmappedSource.Name, f.branchC); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO master_import_identities (source_system, entity_kind, source_key, entity_id)
		VALUES ('impact','branch',$1,$2), ('impact','branch',$3,$4)`, parsed.Branches[0].SourceKey, f.branchA, blankHeadSource.SourceKey, f.branchB); err != nil {
		t.Fatal(err)
	}
	var renamedBranchID string
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO branches (nama,nama_kepala,email,jabatan_kepala,identity_status)
		VALUES ('Nama Display Diubah','Kepala Existing','renamed-map@example.test','Kepala','ready') RETURNING id`).Scan(&renamedBranchID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO master_import_identities (source_system,entity_kind,source_key,entity_id)
		VALUES ('impact','branch',$1,$2)`, renamedMappedSource.SourceKey, renamedBranchID); err != nil {
		t.Fatal(err)
	}
	deps := f.deps
	importService := masterimport.NewService(masterimportpg.New(f.pool, impactSnapshotTestKey), masterimport.NewExcelParser(masterimport.ExpectedHash), "staging")
	deps.MasterImport = importService
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)

	var branchesBefore, productsBefore, pricesBefore int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branches`).Scan(&branchesBefore); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM products`).Scan(&productsBefore); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branch_prices`).Scan(&pricesBefore); err != nil {
		t.Fatal(err)
	}

	managerCookies, managerCSRF := pgLogin(t, srv, "budi@example.test", "rahasia123")
	status, _ := impactPreviewRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/preview", managerCookies, managerCSRF, workbook)
	if status != http.StatusForbidden {
		t.Fatalf("manager status=%d", status)
	}

	saCookies, saCSRF := pgLogin(t, srv, "rina@example.test", "rahasia123")
	status, _ = impactPreviewRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/preview", saCookies, "", workbook)
	if status != http.StatusForbidden {
		t.Fatalf("preview tanpa CSRF status=%d", status)
	}
	status, first := impactPreviewRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/preview", saCookies, saCSRF, workbook)
	if status != http.StatusCreated {
		t.Fatalf("preview pertama status=%d body=%v", status, first)
	}
	status, second := impactPreviewRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/preview", saCookies, saCSRF, workbook)
	if status != http.StatusCreated {
		t.Fatalf("preview kedua status=%d body=%v", status, second)
	}
	one := first["data"].(map[string]any)
	two := second["data"].(map[string]any)
	if one["attempt_id"] == two["attempt_id"] || one["artifact_id"] != two["artifact_id"] || one["preview_hash"] != two["preview_hash"] {
		t.Fatalf("attempt/artifact/preview hash tidak deterministik")
	}
	counts := one["counts"].(map[string]any)
	branchCounts := counts["branch"].(map[string]any)
	if int(branchCounts["insert"].(float64)) != 30 || int(branchCounts["update"].(float64)) != 1 || int(branchCounts["conflict"].(float64)) != 3 {
		t.Fatalf("branch counts=%v", branchCounts)
	}
	noteCounts := counts["note"].(map[string]any)
	if noteCounts["needs_review"].(float64) < 1 {
		t.Fatalf("needs_review note=%v", noteCounts)
	}
	foundInconsistent, foundBlank, foundRemap := false, false, false
	for _, raw := range one["items"].([]any) {
		item := raw.(map[string]any)
		if item["reason_code"] == "inconsistent_existing_access" {
			foundInconsistent = true
		}
		if item["reason_code"] == "existing_access_identity_blank" {
			foundBlank = true
		}
		if item["reason_code"] == "identity_remap_required" {
			foundRemap = true
		}
	}
	if !foundInconsistent || !foundBlank || !foundRemap {
		t.Fatalf("reason code tidak lengkap: inconsistent=%v blank=%v remap=%v", foundInconsistent, foundBlank, foundRemap)
	}
	evidence, err := importService.Evidence(t.Context(), auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}, one["attempt_id"].(string))
	if err != nil || len(evidence) == 0 || evidence[0].Value == "" {
		t.Fatalf("evidence privat tidak dapat ditelusuri superadmin: count=%d err=%v", len(evidence), err)
	}

	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_users SET updated_at=updated_at + interval '1 second' WHERE branch_id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	status, third := impactPreviewRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/preview", saCookies, saCSRF, workbook)
	if status != http.StatusCreated || third["data"].(map[string]any)["preview_hash"] == two["preview_hash"] {
		t.Fatal("perubahan relasi akses harus mengubah preview hash walau klasifikasi tetap")
	}

	var productID, variantID string
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO products (kode,nama,is_active) VALUES ('idv','IDV Existing',true) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO product_variants (product_id,code,nama,is_active) VALUES ($1,'idv','IDV Existing',true) RETURNING id`, productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO branch_tariff_versions
		(branch_id,variant_id,hjm_rupiah,p1_ratio,spv_rupiah,final_item_rupiah,is_available,source_import_attempt_id,source_sheet,source_row,source_columns,source_component_hash)
		VALUES ($1,$2,1,0,0,1,true,$3,$4,3,'N:P',$5)`, f.branchA, variantID, one["attempt_id"].(string), masterimport.SheetName, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	status, fourth := impactPreviewRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/preview", saCookies, saCSRF, workbook)
	if status != http.StatusCreated {
		t.Fatalf("preview bertarif status=%d", status)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_tariff_versions SET valid_from=valid_from + interval '1 second' WHERE branch_id=$1 AND variant_id=$2`, f.branchA, variantID); err != nil {
		t.Fatal(err)
	}
	status, fifth := impactPreviewRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/preview", saCookies, saCSRF, workbook)
	if status != http.StatusCreated || fifth["data"].(map[string]any)["preview_hash"] == fourth["data"].(map[string]any)["preview_hash"] {
		t.Fatal("perubahan versi tarif harus mengubah preview hash walau klasifikasi tetap")
	}
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM branch_tariff_versions WHERE variant_id=$1`, variantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM product_variants WHERE id=$1`, variantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM products WHERE id=$1`, productID); err != nil {
		t.Fatal(err)
	}

	var branchesAfter, productsAfter, pricesAfter, artifacts, attempts int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branches`).Scan(&branchesAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM products`).Scan(&productsAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branch_prices`).Scan(&pricesAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM master_import_artifacts`).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM master_import_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if branchesAfter != branchesBefore || productsAfter != productsBefore || pricesAfter != pricesBefore {
		t.Fatalf("preview mengubah master: branches %d/%d products %d/%d prices %d/%d", branchesBefore, branchesAfter, productsBefore, productsAfter, pricesBefore, pricesAfter)
	}
	if artifacts != 1 || attempts != 5 {
		t.Fatalf("artifacts=%d attempts=%d", artifacts, attempts)
	}
}

func TestPGImpactPreviewSourceKeyDanNamaBerubahMemerlukanRemap(t *testing.T) {
	actorFor := func(f *pgFixture) auth.Actor {
		return auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
	}
	previewFor := func(t *testing.T, f *pgFixture, parsed masterimport.ParsedWorkbook) masterimport.Preview {
		t.Helper()
		service := masterimport.NewService(masterimportpg.New(f.pool, impactSnapshotTestKey), fixedImpactParser{parsed: parsed}, "staging")
		preview, err := service.Preview(t.Context(), actorFor(f), bytes.NewReader(nil), 0)
		if err != nil {
			t.Fatal(err)
		}
		return preview
	}
	branchItem := func(t *testing.T, preview masterimport.Preview, row int) masterimport.PreviewItem {
		t.Helper()
		for _, item := range preview.Items {
			if item.EntityKind == masterimport.EntityBranch && item.SourceRow == row {
				return item
			}
		}
		t.Fatalf("item branch baris %d tidak ditemukan", row)
		return masterimport.PreviewItem{}
	}
	assertMasterUnchanged := func(t *testing.T, f *pgFixture, before int) {
		t.Helper()
		var after int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branches`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("preview membuat duplikasi cabang: before=%d after=%d", before, after)
		}
	}

	t.Run("key dan display berubah wajib remap tanpa overwrite", func(t *testing.T) {
		f := newPGFixture(t)
		if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET nama='Nama Lama' WHERE id=$1`, f.branchA); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO master_import_identities (source_system,entity_kind,source_key,entity_id)
			VALUES ('impact','branch','branch:nama-lama',$1)`, f.branchA); err != nil {
			t.Fatal(err)
		}
		var branchesBefore int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branches`).Scan(&branchesBefore); err != nil {
			t.Fatal(err)
		}

		parsed := impactParsed(masterimport.SourceBranch{SourceKey: "branch:nama-baru", Name: "Nama Baru", HeadName: impactStringPtr("Kepala A")})
		item := branchItem(t, previewFor(t, f, parsed), 3)
		if item.Status != masterimport.StatusConflict || item.ReasonCode != "identity_remap_required" {
			t.Fatalf("branch=%#v, harus conflict identity_remap_required", item)
		}
		if item.ExistingFingerprint == "" {
			t.Fatal("preview remap harus menyimpan fingerprint snapshot mapping lama")
		}

		var name string
		if err := f.pool.QueryRow(t.Context(), `SELECT nama FROM branches WHERE id=$1`, f.branchA).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "Nama Lama" {
			t.Fatalf("preview menimpa cabang existing: nama=%q", name)
		}
		var oldMappings, newMappings int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE source_key='branch:nama-lama'), count(*) FILTER (WHERE source_key='branch:nama-baru') FROM master_import_identities WHERE entity_id=$1`, f.branchA).Scan(&oldMappings, &newMappings); err != nil {
			t.Fatal(err)
		}
		if oldMappings != 1 || newMappings != 0 {
			t.Fatalf("mapping berubah saat preview: lama=%d baru=%d", oldMappings, newMappings)
		}
		assertMasterUnchanged(t, f, branchesBefore)
	})

	t.Run("source key persisted tetap mengendalikan update display", func(t *testing.T) {
		f := newPGFixture(t)
		if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET nama='Nama Lama' WHERE id=$1`, f.branchA); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO master_import_identities (source_system,entity_kind,source_key,entity_id)
			VALUES ('impact','branch','branch:identitas-tetap',$1)`, f.branchA); err != nil {
			t.Fatal(err)
		}
		var branchesBefore int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branches`).Scan(&branchesBefore); err != nil {
			t.Fatal(err)
		}

		parsed := impactParsed(masterimport.SourceBranch{SourceKey: "branch:identitas-tetap", Name: "Nama Baru", HeadName: impactStringPtr("Kepala A")})
		item := branchItem(t, previewFor(t, f, parsed), 3)
		if item.Status != masterimport.StatusUpdate || item.ReasonCode != "" {
			t.Fatalf("branch=%#v, mapping persisted harus menghasilkan update", item)
		}
		assertMasterUnchanged(t, f, branchesBefore)
	})

	t.Run("display sama tanpa mapping tidak dianggap identitas", func(t *testing.T) {
		f := newPGFixture(t)
		if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET nama='Nama Sama' WHERE id=$1`, f.branchA); err != nil {
			t.Fatal(err)
		}
		var branchesBefore int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branches`).Scan(&branchesBefore); err != nil {
			t.Fatal(err)
		}

		parsed := impactParsed(masterimport.SourceBranch{SourceKey: "branch:nama-sama", Name: "Nama Sama", HeadName: impactStringPtr("Kepala A")})
		item := branchItem(t, previewFor(t, f, parsed), 3)
		if item.Status != masterimport.StatusConflict || item.ReasonCode != "identity_remap_required" {
			t.Fatalf("branch=%#v, kecocokan display tanpa mapping harus meminta remap", item)
		}
		assertMasterUnchanged(t, f, branchesBefore)
	})

	t.Run("cabang baru insert bila seluruh mapping lama terwakili", func(t *testing.T) {
		f := newPGFixture(t)
		if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET nama='Nama Lama' WHERE id=$1`, f.branchA); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO master_import_identities (source_system,entity_kind,source_key,entity_id)
			VALUES ('impact','branch','branch:nama-lama',$1)`, f.branchA); err != nil {
			t.Fatal(err)
		}
		var branchesBefore int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branches`).Scan(&branchesBefore); err != nil {
			t.Fatal(err)
		}

		parsed := impactParsed(
			masterimport.SourceBranch{SourceKey: "branch:nama-lama", Name: "Nama Lama", HeadName: impactStringPtr("Kepala A")},
			masterimport.SourceBranch{SourceKey: "branch:cabang-benar-benar-baru", Name: "Cabang Benar-benar Baru", HeadName: impactStringPtr("Kepala Baru")},
		)
		preview := previewFor(t, f, parsed)
		mapped := branchItem(t, preview, 3)
		if mapped.Status != masterimport.StatusSkip || mapped.ReasonCode != "" {
			t.Fatalf("mapping lama terwakili=%#v, harus skip", mapped)
		}
		newBranch := branchItem(t, preview, 4)
		if newBranch.Status != masterimport.StatusInsert || newBranch.ReasonCode != "" {
			t.Fatalf("cabang baru=%#v, harus insert", newBranch)
		}
		assertMasterUnchanged(t, f, branchesBefore)
	})
}

func TestPGImpactApplyIdempotenTombstoneRollbackDanStale(t *testing.T) {
	f := newPGFixture(t)
	actor := auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
	available := masterimport.MoneyComponent{Available: true, HJM: 100000, P1: "1/10", SPV: 2500, FinalItem: 112500, Fingerprint: impactTestFingerprint("idv-v1")}
	parsed := impactParsed(masterimport.SourceBranch{
		SourceKey: "branch:impact-apply-uji", Name: "Impact Apply Uji", Position: "Kepala", HeadName: impactStringPtr("Kepala Uji"),
		Tariffs:     map[string]masterimport.MoneyComponent{"idv": available},
		ReviewCells: []masterimport.ReviewCell{{Column: "AR", Fingerprint: impactTestFingerprint("note-1"), Value: "evidence privat"}},
	})
	repo := masterimportpg.New(f.pool, impactSnapshotTestKey)
	service := masterimport.NewService(repo, fixedImpactParser{parsed: parsed}, "staging")

	preview, err := service.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	deps := f.deps
	deps.MasterImport = service
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	managerCookies, managerCSRF := pgLogin(t, srv, "budi@example.test", "rahasia123")
	saCookies, _ := pgLogin(t, srv, "rina@example.test", "rahasia123")
	httpBody := map[string]any{"preview_hash": preview.PreviewHash, "idempotency_key": "http-auth-check"}
	if status, _ := impactJSONRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/"+preview.AttemptID+"/apply", managerCookies, managerCSRF, httpBody); status != http.StatusForbidden {
		t.Fatalf("manager apply status=%d", status)
	}
	if status, _ := impactJSONRequest(t, srv.URL+"/api/v1/admin/master-imports/impact/"+preview.AttemptID+"/apply", saCookies, "", httpBody); status != http.StatusForbidden {
		t.Fatalf("apply tanpa CSRF status=%d", status)
	}
	if _, err := service.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: preview.AttemptID, PreviewHash: preview.PreviewHash, IdempotencyKey: "impact-apply-unresolved"}); !errors.Is(err, masterimport.ErrApplyBlocked) {
		t.Fatalf("apply tanpa keputusan err=%v", err)
	}
	noteDecision := impactDecisionFor(preview, "manual_review_required", "ignored_with_reason")
	applyReq := masterimport.ApplyRequest{AttemptID: preview.AttemptID, PreviewHash: preview.PreviewHash, IdempotencyKey: "impact-apply-key-0001", Decisions: []masterimport.Decision{noteDecision}}
	type applyOutcome struct {
		result masterimport.ApplyResult
		err    error
	}
	outcomes := make(chan applyOutcome, 2)
	for range 2 {
		go func() {
			got, applyErr := service.Apply(t.Context(), actor, applyReq)
			outcomes <- applyOutcome{got, applyErr}
		}()
	}
	one, two := <-outcomes, <-outcomes
	if one.err != nil || two.err != nil {
		t.Fatalf("concurrent apply err1=%v err2=%v", one.err, two.err)
	}
	result := one.result
	if result.Idempotent {
		result = two.result
	}
	if one.result.ApplyRunID != two.result.ApplyRunID || one.result.Idempotent == two.result.Idempotent {
		t.Fatalf("hasil race one=%+v two=%+v", one.result, two.result)
	}
	if result.Mutations.Branch != 1 || result.Mutations.Tariff != 1 {
		t.Fatalf("mutations=%+v", result.Mutations)
	}

	retry, err := service.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: preview.AttemptID, PreviewHash: preview.PreviewHash, IdempotencyKey: "impact-apply-key-0001", Decisions: []masterimport.Decision{noteDecision}})
	if err != nil || !retry.Idempotent || retry.ApplyRunID != result.ApplyRunID {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	var applyRuns int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM master_import_apply_runs WHERE attempt_id=$1`, preview.AttemptID).Scan(&applyRuns); err != nil || applyRuns != 1 {
		t.Fatalf("apply_runs=%d err=%v", applyRuns, err)
	}

	second, err := service.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	secondDecision := impactDecisionFor(second, "manual_review_required", "ignored_with_reason")
	secondResult, err := service.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: second.AttemptID, PreviewHash: second.PreviewHash, IdempotencyKey: "impact-apply-key-0002", Decisions: []masterimport.Decision{secondDecision}})
	if err != nil {
		t.Fatal(err)
	}
	if secondResult.Mutations != (masterimport.MutationCounts{}) {
		t.Fatalf("attempt identik membuat mutasi: %+v", secondResult.Mutations)
	}

	stale, err := service.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE products SET updated_at=updated_at+interval '1 second' WHERE kode='idv'`); err != nil {
		t.Fatal(err)
	}
	staleDecision := impactDecisionFor(stale, "manual_review_required", "ignored_with_reason")
	if _, err := service.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: stale.AttemptID, PreviewHash: stale.PreviewHash, IdempotencyKey: "impact-apply-key-stale", Decisions: []masterimport.Decision{staleDecision}}); !errors.Is(err, masterimport.ErrPreviewStale) {
		t.Fatalf("perubahan pascapreview err=%v", err)
	}

	var branchID, productID string
	if err := f.pool.QueryRow(t.Context(), `SELECT entity_id::text FROM master_import_identities WHERE source_system='impact' AND source_key='branch:impact-apply-uji'`).Scan(&branchID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT id::text FROM products WHERE kode='idv'`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	var orderID string
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO orders (branch_id,nomor,status,tanggal,total) VALUES ($1,'HIST-IMPACT-1','submitted',CURRENT_DATE,112500) RETURNING id::text`, branchID).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO order_lines (order_id,line_no,jenis,product_id,nama_snapshot,qty,harga_satuan) VALUES ($1,1,'product',$2,'IDV Historis',1,112500)`, orderID, productID); err != nil {
		t.Fatal(err)
	}

	unavailableParsed := parsed
	unavailableParsed.Branches = append([]masterimport.SourceBranch(nil), parsed.Branches...)
	unavailableParsed.Branches[0].Tariffs = cloneTariffs(parsed.Branches[0].Tariffs)
	unavailableParsed.Branches[0].Tariffs["idv"] = masterimport.MoneyComponent{Fingerprint: impactTestFingerprint("idv-unavailable")}
	unavailableService := masterimport.NewService(repo, fixedImpactParser{parsed: unavailableParsed}, "staging")
	tombstonePreview, err := unavailableService.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	tombstoneDecision := impactDecisionFor(tombstonePreview, "manual_review_required", "ignored_with_reason")
	tombstoneResult, err := unavailableService.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: tombstonePreview.AttemptID, PreviewHash: tombstonePreview.PreviewHash, IdempotencyKey: "impact-tombstone-key", Decisions: []masterimport.Decision{tombstoneDecision}})
	if err != nil {
		t.Fatal(err)
	}
	var availableNow bool
	if err := f.pool.QueryRow(t.Context(), `SELECT t.is_available FROM branch_tariff_versions t JOIN product_variants v ON v.id=t.variant_id WHERE t.branch_id=$1 AND v.code='idv' AND t.superseded_at IS NULL`, branchID).Scan(&availableNow); err != nil || availableNow {
		t.Fatalf("tombstone available=%v err=%v", availableNow, err)
	}
	var projection int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branch_prices WHERE branch_id=$1 AND product_id=$2`, branchID, productID).Scan(&projection); err != nil || projection != 0 {
		t.Fatalf("projection=%d err=%v", projection, err)
	}

	var tombstoneID string
	if err := f.pool.QueryRow(t.Context(), `SELECT t.id::text FROM branch_tariff_versions t JOIN product_variants v ON v.id=t.variant_id WHERE t.branch_id=$1 AND v.code='idv' AND t.superseded_at IS NULL`, branchID).Scan(&tombstoneID); err != nil {
		t.Fatal(err)
	}
	var tombstoneValidFrom string
	if err := f.pool.QueryRow(t.Context(), `SELECT valid_from::text FROM branch_tariff_versions WHERE id=$1`, tombstoneID).Scan(&tombstoneValidFrom); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_tariff_versions SET valid_from=valid_from+interval '1 second' WHERE id=$1`, tombstoneID); err != nil {
		t.Fatal(err)
	}
	if _, err := unavailableService.Rollback(t.Context(), actor, tombstoneResult.ApplyRunID); !errors.Is(err, masterimport.ErrRollbackConflict) {
		t.Fatalf("rollback perubahan material tombstone pascaapply err=%v", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_tariff_versions SET valid_from=$2::timestamptz WHERE id=$1`, tombstoneID, tombstoneValidFrom); err != nil {
		t.Fatal(err)
	}
	rolled, err := unavailableService.Rollback(t.Context(), actor, tombstoneResult.ApplyRunID)
	if err != nil || rolled.Restored == 0 {
		t.Fatalf("rollback=%+v err=%v", rolled, err)
	}
	retryAfterRollback, err := unavailableService.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: tombstonePreview.AttemptID, PreviewHash: tombstonePreview.PreviewHash, IdempotencyKey: "impact-tombstone-key", Decisions: []masterimport.Decision{tombstoneDecision}})
	if err != nil || retryAfterRollback.Status != "rolled_back" || !retryAfterRollback.Idempotent {
		t.Fatalf("retry sesudah rollback=%+v err=%v", retryAfterRollback, err)
	}
	if _, err := unavailableService.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: tombstonePreview.AttemptID, PreviewHash: tombstonePreview.PreviewHash, IdempotencyKey: "impact-tombstone-key-baru", Decisions: []masterimport.Decision{tombstoneDecision}}); !errors.Is(err, masterimport.ErrAlreadyApplied) {
		t.Fatalf("reapply attempt lama err=%v", err)
	}
	var historicalPrice int64
	if err := f.pool.QueryRow(t.Context(), `SELECT harga_satuan FROM order_lines WHERE order_id=$1`, orderID).Scan(&historicalPrice); err != nil || historicalPrice != 112500 {
		t.Fatalf("harga historis=%d err=%v", historicalPrice, err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branch_prices WHERE branch_id=$1 AND product_id=$2 AND harga=112500`, branchID, productID).Scan(&projection); err != nil || projection != 1 {
		t.Fatalf("projection restored=%d err=%v", projection, err)
	}
}

// TestPGImpactBersamaEditManualMatriks memverifikasi interaksi edit komponen
// matriks harga dengan impor master: edit manual identik dengan sel workbook
// tidak membuat versi dan dikenali impor sebagai skip; edit berbeda membuat
// rollback impor lama ditolak (versi aktif bukan lagi milik run itu) dan
// impor berikutnya menjadi update yang memulihkan nilai workbook.
func TestPGImpactBersamaEditManualMatriks(t *testing.T) {
	f := newPGFixture(t)
	actor := auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
	tarifWorkbook, err := catalog.HitungTarif(catalog.Komponen{HJM: 100000, P1: "0.1", SPV: 2500}, catalog.JenisReguler)
	if err != nil {
		t.Fatal(err)
	}
	parsed := impactParsed(masterimport.SourceBranch{
		SourceKey: "branch:impact-manual-uji", Name: "Impact Manual Uji", Position: "Kepala", HeadName: impactStringPtr("Kepala Uji"),
		Tariffs: map[string]masterimport.MoneyComponent{"idv": {
			Available: true, HJM: 100000, P1: "1/10", SPV: 2500, FinalItem: tarifWorkbook.Final, Fingerprint: tarifWorkbook.Sidik,
		}},
	})
	service := masterimport.NewService(masterimportpg.New(f.pool, impactSnapshotTestKey), fixedImpactParser{parsed: parsed}, "staging")
	preview := func() masterimport.Preview {
		t.Helper()
		got, err := service.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	statusTarifIDV := func(p masterimport.Preview) masterimport.Status {
		t.Helper()
		for _, item := range p.Items {
			if item.EntityKind == masterimport.EntityTariff && len(item.EntityCode) > 4 && item.EntityCode[len(item.EntityCode)-4:] == ":idv" {
				return item.Status
			}
		}
		t.Fatal("preview tidak memuat tarif idv")
		return ""
	}
	apply := func(p masterimport.Preview, key string) masterimport.ApplyResult {
		t.Helper()
		res, err := service.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: p.AttemptID, PreviewHash: p.PreviewHash, IdempotencyKey: key})
		if err != nil {
			t.Fatalf("apply %s: %v", key, err)
		}
		return res
	}
	pertama := apply(preview(), "impact-manual-0001")

	var branchID, productID string
	if err := f.pool.QueryRow(t.Context(), `SELECT entity_id::text FROM master_import_identities WHERE source_system='impact' AND source_key='branch:impact-manual-uji'`).Scan(&branchID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT id::text FROM products WHERE kode='idv'`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	cookies, csrf := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	putIDV := func(hjm int64, p1 string, spv int64) map[string]any {
		t.Helper()
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/prices",
			fmt.Sprintf(`{"items":[{"branch_id":%q,"product_id":%q,"hjm":%d,"p1":%q,"spv":%d}]}`, branchID, productID, hjm, p1, spv), cookies, csrf)
		if status != http.StatusOK {
			t.Fatalf("PUT harga harus 200, dapat %d (%v)", status, body)
		}
		return dataMap(t, body)
	}
	aktif := func() (string, int64) {
		t.Helper()
		var sumber string
		var harga int64
		if err := f.pool.QueryRow(t.Context(), `
			SELECT tv.source_kind, bp.harga
			  FROM branch_tariff_versions tv
			  JOIN product_variants v ON v.id = tv.variant_id AND v.code = 'idv'
			  JOIN branch_prices bp ON bp.branch_id = tv.branch_id AND bp.product_id = v.product_id
			 WHERE tv.branch_id = $1 AND tv.superseded_at IS NULL`, branchID).Scan(&sumber, &harga); err != nil {
			t.Fatal(err)
		}
		return sumber, harga
	}

	// Matriks menampilkan komponen hasil impor; menulis nilai yang sama diabaikan.
	if hasil := putIDV(100000, "0.1", 2500); hasil["diabaikan"] != float64(1) {
		t.Fatalf("komponen identik dengan impor harus diabaikan, dapat %v", hasil)
	}
	if sumber, harga := aktif(); sumber != "import" || harga != 112500 {
		t.Fatalf("versi impor harus tetap aktif, dapat %s/%d", sumber, harga)
	}

	// Edit berbeda: versi manual aktif, rollback impor pertama ditolak.
	putIDV(120000, "0.05", 0)
	if sumber, harga := aktif(); sumber != "manual" || harga != 126000 {
		t.Fatalf("edit manual harus aktif dengan tarif 126.000, dapat %s/%d", sumber, harga)
	}
	if _, err := service.Rollback(t.Context(), actor, pertama.ApplyRunID); !errors.Is(err, masterimport.ErrRollbackConflict) {
		t.Fatalf("rollback impor setelah edit manual harus conflict, dapat %v", err)
	}

	// Impor berikutnya melihat perbedaan sebagai update dan memulihkan workbook.
	kedua := preview()
	if st := statusTarifIDV(kedua); st != masterimport.StatusUpdate {
		t.Fatalf("tarif yang diubah manual harus update pada impor berikutnya, dapat %s", st)
	}
	apply(kedua, "impact-manual-0002")
	if sumber, harga := aktif(); sumber != "import" || harga != 112500 {
		t.Fatalf("impor harus memulihkan nilai workbook, dapat %s/%d", sumber, harga)
	}

	// Edit manual yang kembali persis ke nilai workbook dikenali sebagai skip.
	putIDV(120000, "0.05", 0)
	putIDV(100000, "0.10", 2500)
	if sumber, harga := aktif(); sumber != "manual" || harga != 112500 {
		t.Fatalf("edit manual kembali harus aktif dengan tarif 112.500, dapat %s/%d", sumber, harga)
	}
	if st := statusTarifIDV(preview()); st != masterimport.StatusSkip {
		t.Fatalf("komponen manual identik dengan workbook harus skip, dapat %s", st)
	}
}

func TestPGImpactProjectionLegacyFingerprintRollbackDanSnapshotTerenkripsi(t *testing.T) {
	f := newPGFixture(t)
	actor := auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET identity_status='registered' WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO master_import_identities (source_system,entity_kind,source_key,entity_id) VALUES ('impact','branch','branch:legacy-projection',$1)`, f.branchA); err != nil {
		t.Fatal(err)
	}
	var productID, variantID string
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO products (kode,nama,is_active) VALUES ('idv','IDV Legacy',true) RETURNING id::text`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO product_variants (product_id,code,nama,is_active) VALUES ($1,'idv','IDV Legacy',true) RETURNING id::text`, productID).Scan(&variantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO branch_prices (branch_id,product_id,harga,updated_by) VALUES ($1,$2,777,$3)`, f.branchA, productID, f.adminMgr); err != nil {
		t.Fatal(err)
	}
	parsed := impactParsed(masterimport.SourceBranch{
		SourceKey: "branch:legacy-projection", Name: "Impact Legacy Baru", HeadName: impactStringPtr("Kepala A"),
		Tariffs: map[string]masterimport.MoneyComponent{"idv": {Available: true, HJM: 90, P1: "0", SPV: 10, FinalItem: 100, Fingerprint: impactTestFingerprint("legacy-v1")}},
	})
	service := masterimport.NewService(masterimportpg.New(f.pool, impactSnapshotTestKey), fixedImpactParser{parsed: parsed}, "staging")
	stalePreview, err := service.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_prices SET harga=778,updated_at=now()+interval '1 second' WHERE branch_id=$1 AND product_id=$2`, f.branchA, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: stalePreview.AttemptID, PreviewHash: stalePreview.PreviewHash, IdempotencyKey: "legacy-stale"}); !errors.Is(err, masterimport.ErrPreviewStale) {
		t.Fatalf("perubahan projection pascapreview err=%v", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_prices SET harga=777,updated_at=now()+interval '2 second' WHERE branch_id=$1 AND product_id=$2`, f.branchA, productID); err != nil {
		t.Fatal(err)
	}
	preview, err := service.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: preview.AttemptID, PreviewHash: preview.PreviewHash, IdempotencyKey: "legacy-apply"})
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	var keyID, storageType string
	if err := f.pool.QueryRow(t.Context(), `SELECT before_state,encryption_key_id,pg_typeof(before_state)::text FROM master_import_apply_changes WHERE apply_run_id=$1 AND entity_kind='branch'`, result.ApplyRunID).Scan(&ciphertext, &keyID, &storageType); err != nil {
		t.Fatal(err)
	}
	if storageType != "bytea" || keyID == "" || bytes.Contains(ciphertext, []byte("Grahita Cabang A")) {
		t.Fatalf("snapshot tidak terenkripsi: type=%s key=%q ciphertext=%q", storageType, keyID, ciphertext)
	}
	var appliedUpdatedBy *string
	var appliedUpdatedAt string
	if err := f.pool.QueryRow(t.Context(), `SELECT updated_by::text,updated_at::text FROM branch_prices WHERE branch_id=$1 AND product_id=$2 AND harga=100`, f.branchA, productID).Scan(&appliedUpdatedBy, &appliedUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_prices SET harga=101,updated_at=now()+interval '3 second' WHERE branch_id=$1 AND product_id=$2`, f.branchA, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rollback(t.Context(), actor, result.ApplyRunID); !errors.Is(err, masterimport.ErrRollbackConflict) {
		t.Fatalf("rollback harus menolak projection berubah, err=%v", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_prices SET harga=100,updated_by=$3,updated_at=$4::timestamptz WHERE branch_id=$1 AND product_id=$2`, f.branchA, productID, appliedUpdatedBy, appliedUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rollback(t.Context(), actor, result.ApplyRunID); err != nil {
		t.Fatal(err)
	}
	var restoredPrice int64
	var restoredName string
	if err := f.pool.QueryRow(t.Context(), `SELECT harga FROM branch_prices WHERE branch_id=$1 AND product_id=$2`, f.branchA, productID).Scan(&restoredPrice); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT nama FROM branches WHERE id=$1`, f.branchA).Scan(&restoredName); err != nil {
		t.Fatal(err)
	}
	if restoredPrice != 777 || restoredName != "Grahita Cabang A" {
		t.Fatalf("rollback legacy price=%d name=%q", restoredPrice, restoredName)
	}
}

func TestPGImpactRollbackMenolakPerubahanVersiBaruDanLama(t *testing.T) {
	f := newPGFixture(t)
	actor := auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET identity_status='registered' WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO master_import_identities (source_system,entity_kind,source_key,entity_id) VALUES ('impact','branch','branch:version-cas',$1)`, f.branchA); err != nil {
		t.Fatal(err)
	}
	baseParsed := impactParsed(masterimport.SourceBranch{SourceKey: "branch:version-cas", Name: "Grahita Cabang A", HeadName: impactStringPtr("Kepala A"), Tariffs: map[string]masterimport.MoneyComponent{
		"idv": {Available: true, HJM: 90, P1: "0", SPV: 10, FinalItem: 100, Fingerprint: impactTestFingerprint("version-v1")},
	}})
	repo := masterimportpg.New(f.pool, impactSnapshotTestKey)
	baseService := masterimport.NewService(repo, fixedImpactParser{parsed: baseParsed}, "staging")
	basePreview, err := baseService.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := baseService.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: basePreview.AttemptID, PreviewHash: basePreview.PreviewHash, IdempotencyKey: "version-base"}); err != nil {
		t.Fatal(err)
	}
	nextParsed := impactParsed(masterimport.SourceBranch{SourceKey: "branch:version-cas", Name: "Grahita Cabang A", HeadName: impactStringPtr("Kepala A"), Tariffs: map[string]masterimport.MoneyComponent{
		"idv": {Available: true, HJM: 180, P1: "0", SPV: 20, FinalItem: 200, Fingerprint: impactTestFingerprint("version-v2")},
	}})
	nextService := masterimport.NewService(repo, fixedImpactParser{parsed: nextParsed}, "staging")
	nextPreview, err := nextService.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := nextService.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: nextPreview.AttemptID, PreviewHash: nextPreview.PreviewHash, IdempotencyKey: "version-next"})
	if err != nil {
		t.Fatal(err)
	}
	var oldID, newID string
	if err := f.pool.QueryRow(t.Context(), `SELECT id::text FROM branch_tariff_versions WHERE branch_id=$1 AND superseded_at IS NOT NULL ORDER BY valid_from DESC LIMIT 1`, f.branchA).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT id::text FROM branch_tariff_versions WHERE branch_id=$1 AND superseded_at IS NULL ORDER BY valid_from DESC LIMIT 1`, f.branchA).Scan(&newID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_tariff_versions SET hjm_rupiah=181 WHERE id=$1`, newID); err != nil {
		t.Fatal(err)
	}
	if _, err := nextService.Rollback(t.Context(), actor, result.ApplyRunID); !errors.Is(err, masterimport.ErrRollbackConflict) {
		t.Fatalf("mutasi versi baru harus conflict, err=%v", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_tariff_versions SET hjm_rupiah=180 WHERE id=$1`, newID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_tariff_versions SET final_item_rupiah=101 WHERE id=$1`, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := nextService.Rollback(t.Context(), actor, result.ApplyRunID); !errors.Is(err, masterimport.ErrRollbackConflict) {
		t.Fatalf("mutasi versi lama harus conflict, err=%v", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branch_tariff_versions SET final_item_rupiah=100 WHERE id=$1`, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := nextService.Rollback(t.Context(), actor, result.ApplyRunID); err != nil {
		t.Fatal(err)
	}
	var activeID string
	var price int64
	if err := f.pool.QueryRow(t.Context(), `SELECT id::text FROM branch_tariff_versions WHERE branch_id=$1 AND superseded_at IS NULL`, f.branchA).Scan(&activeID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT bp.harga FROM branch_prices bp JOIN products p ON p.id=bp.product_id WHERE bp.branch_id=$1 AND p.kode='idv'`, f.branchA).Scan(&price); err != nil {
		t.Fatal(err)
	}
	if activeID != oldID || price != 100 {
		t.Fatalf("rollback active=%s want=%s price=%d", activeID, oldID, price)
	}
}

func TestPGImpactRevocationRollbackTetapFailClosedDanAuditMultiEntitas(t *testing.T) {
	f := newPGFixture(t)
	actor := auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
	sourceKeyA := "branch:revocation-uji-a"
	sourceKeyB := "branch:revocation-uji-b"
	operatorReason := "hubungi pii.sintetis@example.test atas nama Kepala Sintetis sebelum pemulihan"
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO master_import_identities (source_system,entity_kind,source_key,entity_id) VALUES ('impact','branch',$1,$2),('impact','branch',$3,$4)`, sourceKeyA, f.branchA, sourceKeyB, f.branchB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET identity_status='registered' WHERE id IN ($1,$2)`, f.branchA, f.branchB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO sessions (actor_kind,user_id,branch_id,token_hash,expires_at)
		VALUES ('branch',$1,$2,digest(gen_random_uuid()::text,'sha256'),now()+interval '1 hour'),
		       ('branch',$3,$4,digest(gen_random_uuid()::text,'sha256'),now()+interval '1 hour')`, f.userA, f.branchA, f.userB, f.branchB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO password_reset_tokens (actor_kind,user_id,token_hash,expires_at)
		VALUES ('branch',$1,digest(gen_random_uuid()::text,'sha256'),now()+interval '1 hour'),
		       ('branch',$2,digest(gen_random_uuid()::text,'sha256'),now()+interval '1 hour')`, f.userA, f.userB); err != nil {
		t.Fatal(err)
	}
	parsed := impactParsed(
		masterimport.SourceBranch{SourceKey: sourceKeyA, Name: "Grahita Cabang A", Position: "Kepala", HeadName: nil},
		masterimport.SourceBranch{SourceKey: sourceKeyB, Name: "Grahita Cabang B", Position: "Kepala", HeadName: nil},
	)
	service := masterimport.NewService(masterimportpg.New(f.pool, impactSnapshotTestKey), fixedImpactParser{parsed: parsed}, "staging")
	preview, err := service.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	decisions := make([]masterimport.Decision, 0, 2)
	for _, item := range preview.Items {
		if item.ReasonCode == "existing_access_identity_blank" {
			decisions = append(decisions, masterimport.Decision{SourceRow: item.SourceRow, EntityKind: item.EntityKind, EntityCode: item.EntityCode, Decision: "revoke_access_and_mark_incomplete", Reason: operatorReason})
		}
	}
	if len(decisions) != 2 {
		t.Fatalf("keputusan revokasi=%d, ingin 2", len(decisions))
	}
	result, err := service.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: preview.AttemptID, PreviewHash: preview.PreviewHash, IdempotencyKey: "impact-revoke-key", Decisions: decisions})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mutations.Revoked != 2 {
		t.Fatalf("revoked=%d", result.Mutations.Revoked)
	}
	rolled, err := service.Rollback(t.Context(), actor, result.ApplyRunID)
	if err != nil || rolled.Irreversible != 2 {
		t.Fatalf("rollback=%+v err=%v", rolled, err)
	}
	var incomplete, heads int
	var activeUsers, activeSessions, activeResetTokens int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE identity_status='identity_incomplete'),count(*) FILTER (WHERE nama_kepala IS NOT NULL) FROM branches WHERE id IN ($1,$2)`, f.branchA, f.branchB).Scan(&incomplete, &heads); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branch_users WHERE branch_id IN ($1,$2) AND is_active`, f.branchA, f.branchB).Scan(&activeUsers); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM sessions WHERE branch_id IN ($1,$2) AND revoked_at IS NULL AND expires_at>now()`, f.branchA, f.branchB).Scan(&activeSessions); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM password_reset_tokens WHERE actor_kind='branch' AND user_id IN ($1,$2) AND revoked_at IS NULL AND used_at IS NULL AND expires_at>now()`, f.userA, f.userB).Scan(&activeResetTokens); err != nil {
		t.Fatal(err)
	}
	if incomplete != 2 || heads != 0 || activeUsers != 0 || activeSessions != 0 || activeResetTokens != 0 {
		t.Fatalf("state rollback revocation incomplete=%d heads=%d users=%d sessions=%d reset=%d", incomplete, heads, activeUsers, activeSessions, activeResetTokens)
	}
	var auditCount int
	var auditActor, sourceRun string
	var entityIDs, resolutions []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) OVER (),actor_id::text,after->>'source_apply_run_id',after->'entity_ids',after->'resolutions'
		FROM audit_logs WHERE entity_id=$1 AND action='identity_revocation_not_reversed'`, result.ApplyRunID).Scan(&auditCount, &auditActor, &sourceRun, &entityIDs, &resolutions); err != nil {
		t.Fatal(err)
	}
	var gotEntityIDs []string
	var gotResolutions []map[string]any
	if err := json.Unmarshal(entityIDs, &gotEntityIDs); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(resolutions, &gotResolutions); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, id := range gotEntityIDs {
		seen[id] = true
	}
	if auditCount != 1 || auditActor != actor.UserID || sourceRun != result.ApplyRunID || len(gotEntityIDs) != 2 || !seen[f.branchA] || !seen[f.branchB] || len(gotResolutions) != 2 {
		t.Fatalf("audit count=%d actor=%s run=%s entity_ids=%v resolutions=%v", auditCount, auditActor, sourceRun, gotEntityIDs, gotResolutions)
	}
	for _, resolution := range gotResolutions {
		if resolution["decision"] != "revoke_access_and_mark_incomplete" || resolution["reason"] != "source_identity_blank_access_revoked" {
			t.Fatalf("metadata resolusi audit=%v", resolution)
		}
	}
	var leakedAuditCount int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs
		WHERE after::text ILIKE '%' || $1 || '%' OR after::text ILIKE '%' || $2 || '%'`,
		"pii.sintetis@example.test", "Kepala Sintetis").Scan(&leakedAuditCount); err != nil {
		t.Fatal(err)
	}
	if leakedAuditCount != 0 {
		t.Fatalf("alasan operator dengan PII sintetis bocor ke audit: %d baris", leakedAuditCount)
	}
	if _, err := service.Rollback(t.Context(), actor, result.ApplyRunID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE entity_id=$1 AND action='identity_revocation_not_reversed'`, result.ApplyRunID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit retry=%d err=%v", auditCount, err)
	}
}

func TestPGImpactRollbackMenolakKunciBerbedaAtauHilangTanpaMutasi(t *testing.T) {
	f := newPGFixture(t)
	actor := auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
	parsed := impactParsed(masterimport.SourceBranch{SourceKey: "branch:key-rotation", Name: "Cabang Key Rotation", Position: "Kepala"})
	serviceA := masterimport.NewService(masterimportpg.New(f.pool, impactSnapshotTestKey), fixedImpactParser{parsed: parsed}, "staging")
	preview, err := serviceA.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := serviceA.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: preview.AttemptID, PreviewHash: preview.PreviewHash, IdempotencyKey: "impact-key-boundary"})
	if err != nil {
		t.Fatal(err)
	}
	var insertedBranchID string
	if err := f.pool.QueryRow(t.Context(), `SELECT entity_id::text FROM master_import_identities WHERE source_system='impact' AND source_key='branch:key-rotation'`).Scan(&insertedBranchID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		key  string
	}{{"berbeda", impactSnapshotOtherTestKey}, {"hilang", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			service := masterimport.NewService(masterimportpg.New(f.pool, tc.key), fixedImpactParser{parsed: parsed}, "staging")
			if _, err := service.Rollback(t.Context(), actor, result.ApplyRunID); err == nil {
				t.Fatal("rollback dengan kunci salah harus gagal")
			}
			var status string
			var branchCount, rollbackAuditCount int
			if err := f.pool.QueryRow(t.Context(), `SELECT status FROM master_import_apply_runs WHERE id=$1`, result.ApplyRunID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branches WHERE id=$1`, insertedBranchID).Scan(&branchCount); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE entity_id=$1 AND action IN ('rollback','identity_revocation_not_reversed')`, result.ApplyRunID).Scan(&rollbackAuditCount); err != nil {
				t.Fatal(err)
			}
			if status != "applied" || branchCount != 1 || rollbackAuditCount != 0 {
				t.Fatalf("rollback salah key status=%s branch=%d audit=%d", status, branchCount, rollbackAuditCount)
			}
		})
	}
	if _, err := serviceA.Rollback(t.Context(), actor, result.ApplyRunID); err != nil {
		t.Fatal(err)
	}
	var branchCount, rollbackAuditCount int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM branches WHERE id=$1`, insertedBranchID).Scan(&branchCount); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_logs WHERE entity_id=$1 AND action='rollback'`, result.ApplyRunID).Scan(&rollbackAuditCount); err != nil {
		t.Fatal(err)
	}
	if branchCount != 0 || rollbackAuditCount != 1 {
		t.Fatalf("rollback key benar branch=%d audit=%d", branchCount, rollbackAuditCount)
	}
}

func TestPGImpactApplyMenolakPerubahanPascapreview(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *pgFixture, string)
	}{
		{"mapping", func(t *testing.T, f *pgFixture, _ string) {
			_, err := f.pool.Exec(t.Context(), `UPDATE master_import_identities SET source_key='branch:berubah-setelah-preview' WHERE source_key='branch:stale-uji'`)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"email", func(t *testing.T, f *pgFixture, _ string) {
			_, err := f.pool.Exec(t.Context(), `UPDATE branches SET email='berubah@example.test' WHERE id=$1`, f.branchA)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"account", func(t *testing.T, f *pgFixture, _ string) {
			_, err := f.pool.Exec(t.Context(), `UPDATE branch_users SET updated_at=updated_at+interval '1 second' WHERE branch_id=$1`, f.branchA)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"registration_token", func(t *testing.T, f *pgFixture, _ string) {
			_, err := f.pool.Exec(t.Context(), `INSERT INTO registration_tokens (branch_id,token_hash,token_prefix,expires_at,created_by) VALUES ($1,digest(gen_random_uuid()::text,'sha256'),'stale',now()+interval '1 hour',$2)`, f.branchA, f.adminSA)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"session", func(t *testing.T, f *pgFixture, _ string) {
			_, err := f.pool.Exec(t.Context(), `INSERT INTO sessions (actor_kind,user_id,branch_id,token_hash,expires_at) VALUES ('branch',$1,$2,digest(gen_random_uuid()::text,'sha256'),now()+interval '1 hour')`, f.userA, f.branchA)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"tariff", func(t *testing.T, f *pgFixture, branchID string) {
			_, err := f.pool.Exec(t.Context(), `UPDATE branch_tariff_versions SET valid_from=valid_from+interval '1 second' WHERE branch_id=$1 AND superseded_at IS NULL`, branchID)
			if err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPGFixture(t)
			actor := auth.Actor{UserID: f.adminSA, Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
			if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET nama='Stale Uji',identity_status='registered' WHERE id=$1`, f.branchA); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `INSERT INTO master_import_identities (source_system,entity_kind,source_key,entity_id) VALUES ('impact','branch','branch:stale-uji',$1)`, f.branchA); err != nil {
				t.Fatal(err)
			}
			parsed := impactParsed(masterimport.SourceBranch{SourceKey: "branch:stale-uji", Name: "Stale Uji", HeadName: impactStringPtr("Kepala A"), Tariffs: map[string]masterimport.MoneyComponent{
				"idv": {Available: true, HJM: 100, P1: "0", SPV: 0, FinalItem: 100, Fingerprint: impactTestFingerprint("stale-idv")},
			}})
			service := masterimport.NewService(masterimportpg.New(f.pool, impactSnapshotTestKey), fixedImpactParser{parsed: parsed}, "staging")
			baseline, err := service.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: baseline.AttemptID, PreviewHash: baseline.PreviewHash, IdempotencyKey: "baseline-" + tc.name}); err != nil {
				t.Fatal(err)
			}
			preview, err := service.Preview(t.Context(), actor, bytes.NewReader(nil), 0)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, f, f.branchA)
			if _, err := service.Apply(t.Context(), actor, masterimport.ApplyRequest{AttemptID: preview.AttemptID, PreviewHash: preview.PreviewHash, IdempotencyKey: "stale-" + tc.name}); !errors.Is(err, masterimport.ErrPreviewStale) {
				t.Fatalf("mutasi %s err=%v", tc.name, err)
			}
		})
	}
}

func impactStringPtr(v string) *string { return &v }

func cloneTariffs(in map[string]masterimport.MoneyComponent) map[string]masterimport.MoneyComponent {
	out := make(map[string]masterimport.MoneyComponent, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
