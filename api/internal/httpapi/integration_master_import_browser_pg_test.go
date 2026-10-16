package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/masterimport"
	masterimportpg "github.com/grahita/portal/api/internal/masterimport/postgres"
)

// browserImpactParser adalah parser khusus test yang memilih fixture sintetis
// dari isi berkas unggahan. Parser produksi dan ExpectedHash tidak diubah.
type browserImpactParser struct{}

func (browserImpactParser) Parse(r io.Reader, _ int64) (masterimport.ParsedWorkbook, error) {
	payload, err := io.ReadAll(r)
	if err != nil {
		return masterimport.ParsedWorkbook{}, err
	}
	switch strings.TrimSpace(string(payload)) {
	case "apply", "stale":
		return browserImpactParsed(false), nil
	case "conflict":
		return browserImpactParsed(true), nil
	default:
		return masterimport.ParsedWorkbook{}, masterimport.ErrInvalidWorkbook
	}
}

func browserImpactParsed(conflict bool) masterimport.ParsedWorkbook {
	definitions := masterimport.ProductDefinitions()
	branches := make([]masterimport.SourceBranch, masterimport.ExpectedRows)
	for i := range branches {
		row := i + 3
		sourceKey := fmt.Sprintf("branch:e2e-%02d", i+1)
		name := fmt.Sprintf("Cabang Sintetis %02d", i+1)
		fingerprint := impactTestFingerprint("browser-e2e", sourceKey)
		tariffs := make(map[string]masterimport.MoneyComponent, len(definitions))
		for productIndex, definition := range definitions {
			hjm := int64(100_000 + i*1_000 + productIndex*100)
			tariffs[definition.Code] = masterimport.MoneyComponent{
				Available:   true,
				HJM:         hjm,
				P1:          "1/10",
				SPV:         2_500,
				FinalItem:   hjm + hjm/10 + 2_500,
				Fingerprint: impactTestFingerprint("browser-e2e", sourceKey, definition.Code),
			}
		}
		branches[i] = masterimport.SourceBranch{
			Row:         row,
			SourceKey:   sourceKey,
			Fingerprint: fingerprint,
			Name:        name,
			Position:    "Kepala Cabang",
			HeadName:    impactStringPtr(fmt.Sprintf("Kepala Sintetis %02d", i+1)),
			Tariffs:     tariffs,
		}
	}
	branches[0].ReviewCells = []masterimport.ReviewCell{{
		Column: "AR", Fingerprint: impactTestFingerprint("browser-e2e-note"), Value: "catatan sintetis privat untuk verifikasi",
	}}
	sourceHash := strings.Repeat("a", 64)
	if conflict {
		branches[0].SourceKey = "branch:e2e-01-remap"
		branches[0].Name = "Cabang Sintetis 01 Remap"
		branches[0].Fingerprint = impactTestFingerprint("browser-e2e", branches[0].SourceKey)
		sourceHash = strings.Repeat("b", 64)
	}
	return masterimport.ParsedWorkbook{SourceHash: sourceHash, Branches: branches}
}

type browserE2ECookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	HTTPOnly bool   `json:"http_only"`
}

type browserE2EControl struct {
	BackendURL    string             `json:"backend_url"`
	FrontendURL   string             `json:"frontend_url"`
	ApplyFile     string             `json:"apply_file"`
	ConflictFile  string             `json:"conflict_file"`
	StaleFile     string             `json:"stale_file"`
	Superadmin    []browserE2ECookie `json:"superadmin_cookies"`
	Manager       []browserE2ECookie `json:"manager_cookies"`
	PreviewMarker string             `json:"preview_marker"`
	PreviewAck    string             `json:"preview_ack"`
	ApplyMarker   string             `json:"apply_marker"`
	ApplyAck      string             `json:"apply_ack"`
	StaleMarker   string             `json:"stale_marker"`
	StaleAck      string             `json:"stale_ack"`
	ResultFile    string             `json:"result_file"`
}

type browserE2EResult struct {
	ManagerStatus       int  `json:"manager_status"`
	CSRFStatus          int  `json:"csrf_status"`
	PreviewBranchTotal  int  `json:"preview_branch_total"`
	PreviewTariffTotal  int  `json:"preview_tariff_total"`
	EvidenceConfirmed   bool `json:"evidence_confirmed"`
	ApplySucceeded      bool `json:"apply_succeeded"`
	ConflictBlocked     bool `json:"conflict_blocked"`
	StaleObserved       bool `json:"stale_observed"`
	RepreviewNewAttempt bool `json:"repreview_new_attempt"`
	NoProductionButton  bool `json:"no_production_button"`
}

type browserE2ECounts struct {
	branches int
	products int
	variants int
	prices   int
	tariffs  int
}

func browserE2EReadCounts(t *testing.T, f *pgFixture) browserE2ECounts {
	t.Helper()
	var out browserE2ECounts
	queries := []struct {
		query string
		value *int
	}{
		{"SELECT count(*) FROM branches", &out.branches},
		{"SELECT count(*) FROM products", &out.products},
		{"SELECT count(*) FROM product_variants", &out.variants},
		{"SELECT count(*) FROM branch_prices", &out.prices},
		{"SELECT count(*) FROM branch_tariff_versions", &out.tariffs},
	}
	for _, query := range queries {
		if err := f.pool.QueryRow(t.Context(), query.query).Scan(query.value); err != nil {
			t.Fatalf("menghitung fixture browser E2E: %v", err)
		}
	}
	return out
}

func browserE2EWriteJSON(t *testing.T, path string, value any, mode os.FileMode) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, mode); err != nil {
		t.Fatal(err)
	}
}

func browserE2ECookies(cookies []*http.Cookie) []browserE2ECookie {
	out := make([]browserE2ECookie, 0, len(cookies))
	for _, cookie := range cookies {
		out = append(out, browserE2ECookie{Name: cookie.Name, Value: cookie.Value, HTTPOnly: cookie.HttpOnly})
	}
	return out
}

func TestBrowserImpactParserMemilihFixtureSintetis(t *testing.T) {
	parser := browserImpactParser{}
	valid, err := parser.Parse(strings.NewReader("apply"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(valid.Branches) != masterimport.ExpectedRows || len(valid.Branches[0].Tariffs) != len(masterimport.ProductDefinitions()) {
		t.Fatalf("fixture apply tidak lengkap: branches=%d tariffs=%d", len(valid.Branches), len(valid.Branches[0].Tariffs))
	}
	conflict, err := parser.Parse(strings.NewReader("conflict"), 8)
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Branches[0].SourceKey == valid.Branches[0].SourceKey || conflict.SourceHash == valid.SourceHash {
		t.Fatal("fixture conflict harus memakai source key dan hash terpisah")
	}
	if _, err := parser.Parse(strings.NewReader("bukan-fixture"), 13); err == nil {
		t.Fatal("payload selain fixture sintetis harus ditolak")
	}
}

// TestPGImpactBrowserE2E menahan server router nyata selama browser eksternal
// menggerakkan UI Vite. Aktifkan eksplisit dengan IMPACT_BROWSER_E2E_DIR;
// berkas kontrol berizin 0600 memuat cookie sintetis dan tidak boleh dilampirkan.
func TestPGImpactBrowserE2E(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("IMPACT_BROWSER_E2E_DIR"))
	if dir == "" {
		t.Skip("IMPACT_BROWSER_E2E_DIR tidak diisi")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"preview.request", "preview.ack", "apply.request", "apply.ack", "stale.request", "stale.ack", "browser-result.json", "control.json"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	applyFile := filepath.Join(dir, "apply.xlsx")
	conflictFile := filepath.Join(dir, "conflict.xlsx")
	staleFile := filepath.Join(dir, "stale.xlsx")
	if err := os.WriteFile(applyFile, []byte("apply"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflictFile, []byte("conflict"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleFile, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := newPGFixture(t)
	before := browserE2EReadCounts(t, f)
	deps := f.deps
	deps.MasterImport = masterimport.NewService(masterimportpg.New(f.pool, impactSnapshotTestKey), browserImpactParser{}, "staging")
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)
	saCookies, _ := pgLogin(t, srv, "rina@example.test", "rahasia123")
	managerCookies, _ := pgLogin(t, srv, "budi@example.test", "rahasia123")

	frontendURL := strings.TrimSpace(os.Getenv("IMPACT_BROWSER_FRONTEND_URL"))
	if frontendURL == "" {
		frontendURL = "http://localhost:5174"
	}
	control := browserE2EControl{
		BackendURL: srv.URL, FrontendURL: frontendURL,
		ApplyFile: applyFile, ConflictFile: conflictFile, StaleFile: staleFile,
		Superadmin: browserE2ECookies(saCookies), Manager: browserE2ECookies(managerCookies),
		PreviewMarker: filepath.Join(dir, "preview.request"), PreviewAck: filepath.Join(dir, "preview.ack"),
		ApplyMarker: filepath.Join(dir, "apply.request"), ApplyAck: filepath.Join(dir, "apply.ack"),
		StaleMarker: filepath.Join(dir, "stale.request"), StaleAck: filepath.Join(dir, "stale.ack"),
		ResultFile: filepath.Join(dir, "browser-result.json"),
	}
	browserE2EWriteJSON(t, filepath.Join(dir, "control.json"), control, 0o600)
	t.Logf("browser E2E siap; kontrol privat: %s", filepath.Join(dir, "control.json"))

	previewChecked := false
	applyChecked := false
	staleMutated := false
	afterApply := browserE2ECounts{}
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		if !previewChecked {
			if _, err := os.Stat(control.PreviewMarker); err == nil {
				if got := browserE2EReadCounts(t, f); got != before {
					t.Fatalf("preview browser memutasi master: before=%+v after=%+v", before, got)
				}
				if err := os.WriteFile(control.PreviewAck, []byte("ok"), 0o600); err != nil {
					t.Fatal(err)
				}
				previewChecked = true
			}
		}
		if !applyChecked {
			if _, err := os.Stat(control.ApplyMarker); err == nil {
				got := browserE2EReadCounts(t, f)
				wantProducts := len(masterimport.ProductDefinitions())
				wantVariants := len(masterimport.VariantDefinitions())
				wantTariffs := masterimport.ExpectedRows * wantProducts
				if got.branches-before.branches != masterimport.ExpectedRows || got.products-before.products != wantProducts ||
					got.variants-before.variants != wantVariants || got.prices-before.prices != wantTariffs || got.tariffs-before.tariffs != wantTariffs {
					t.Fatalf("mutasi apply browser tidak sesuai: before=%+v after=%+v", before, got)
				}
				afterApply = got
				if err := os.WriteFile(control.ApplyAck, []byte("ok"), 0o600); err != nil {
					t.Fatal(err)
				}
				applyChecked = true
			}
		}
		if !staleMutated {
			if _, err := os.Stat(control.StaleMarker); err == nil {
				if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET nama=nama || ' berubah' WHERE id=(SELECT entity_id FROM master_import_identities WHERE source_system='impact' AND entity_kind='branch' AND source_key='branch:e2e-01')`); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(control.StaleAck, []byte("ok"), 0o600); err != nil {
					t.Fatal(err)
				}
				staleMutated = true
			}
		}
		payload, err := os.ReadFile(control.ResultFile)
		if err == nil {
			var result browserE2EResult
			if err := json.Unmarshal(payload, &result); err != nil {
				t.Fatal(err)
			}
			if !previewChecked || !applyChecked || !staleMutated {
				t.Fatalf("browser selesai sebelum checkpoint backend: preview=%v apply=%v stale=%v", previewChecked, applyChecked, staleMutated)
			}
			if result.ManagerStatus != 403 || result.CSRFStatus != 403 || result.PreviewBranchTotal != masterimport.ExpectedRows ||
				result.PreviewTariffTotal != masterimport.ExpectedRows*len(masterimport.ProductDefinitions()) || !result.EvidenceConfirmed ||
				!result.ApplySucceeded || !result.ConflictBlocked || !result.StaleObserved || !result.RepreviewNewAttempt || !result.NoProductionButton {
				t.Fatalf("hasil browser E2E tidak lengkap: %+v", result)
			}
			var applyRuns int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM master_import_apply_runs`).Scan(&applyRuns); err != nil {
				t.Fatal(err)
			}
			if applyRuns != 1 {
				t.Fatalf("apply run=%d, mau 1", applyRuns)
			}
			t.Logf("browser E2E lulus: DB before=%+v after_apply=%+v; 34 cabang, %d tarif, role/CSRF 403, conflict dan stale terkunci", before, afterApply, masterimport.ExpectedRows*len(masterimport.ProductDefinitions()))
			return
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("timeout menunggu browser E2E")
}
