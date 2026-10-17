package masterimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/grahita/portal/api/internal/auth"
)

func fixtureWorkbook(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	defaultSheet := f.GetSheetName(0)
	if err := f.SetSheetName(defaultSheet, SheetName); err != nil {
		t.Fatal(err)
	}
	for c := 0; c < 48; c++ {
		cell1, _ := excelize.CoordinatesToCellName(c+1, 1)
		cell2, _ := excelize.CoordinatesToCellName(c+1, 2)
		_ = f.SetCellStr(SheetName, cell1, "")
		_ = f.SetCellStr(SheetName, cell2, "")
	}
	_ = f.SetCellStr(SheetName, "A1", "IDENTITAS CABANG")
	_ = f.SetCellStr(SheetName, "AR1", "CATATAN KHUSUS")
	for _, spec := range productSpecs {
		start, _ := excelize.CoordinatesToCellName(spec.Start+1, 1)
		hjm, _ := excelize.CoordinatesToCellName(spec.Start+1, 2)
		p1, _ := excelize.CoordinatesToCellName(spec.Start+2, 2)
		spv, _ := excelize.CoordinatesToCellName(spec.Start+3, 2)
		_ = f.SetCellStr(SheetName, start, spec.Name)
		_ = f.SetCellStr(SheetName, hjm, spec.FirstLabel)
		_ = f.SetCellStr(SheetName, p1, "P1")
		_ = f.SetCellStr(SheetName, spv, "SPV")
	}
	for i, v := range []string{"NO", "CABANG", "JABATAN", "KEPALA CABANG"} {
		cell, _ := excelize.CoordinatesToCellName(i+1, 2)
		_ = f.SetCellStr(SheetName, cell, v)
	}
	for i, v := range []string{"NOTED 1", "NOTED 2", "NOTED 3"} {
		cell, _ := excelize.CoordinatesToCellName(44+i, 2)
		_ = f.SetCellStr(SheetName, cell, v)
	}
	for i := 0; i < ExpectedRows; i++ {
		row := i + 3
		_ = f.SetCellInt(SheetName, "A"+itoa(row), int64(i+1))
		_ = f.SetCellStr(SheetName, "B"+itoa(row), "Cabang Uji "+itoa(i+1))
		_ = f.SetCellStr(SheetName, "C"+itoa(row), "Kepala")
		if i != 0 {
			_ = f.SetCellStr(SheetName, "D"+itoa(row), "Nama Uji "+itoa(i+1))
		}
		for _, spec := range productSpecs {
			cell, _ := excelize.CoordinatesToCellName(spec.Start+1, row)
			_ = f.SetCellInt(SheetName, cell, 10005)
			cell, _ = excelize.CoordinatesToCellName(spec.Start+2, row)
			_ = f.SetCellFloat(SheetName, cell, 0.1, 1, 64)
			cell, _ = excelize.CoordinatesToCellName(spec.Start+3, row)
			_ = f.SetCellInt(SheetName, cell, 0)
		}
	}
	_ = f.SetCellStr(SheetName, "AR3", "review")
	_ = f.SetCellStr(SheetName, "AV36", "")
	_ = f.SetSheetDimension(SheetName, "A1:AV36")
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return buf.Bytes()
}

func itoa(v int) string {
	const digits = "0123456789"
	if v < 10 {
		return string(digits[v])
	}
	return string([]byte{digits[v/10], digits[v%10]})
}

func parserFor(data []byte) *ExcelParser {
	sum := sha256.Sum256(data)
	return NewExcelParser(hex.EncodeToString(sum[:]))
}

func TestExcelParserHargaHalfUpDanReview(t *testing.T) {
	data := fixtureWorkbook(t)
	got, err := parserFor(data).Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Branches) != 34 {
		t.Fatalf("branches=%d", len(got.Branches))
	}
	if got.Branches[0].HeadName != nil {
		t.Fatal("kepala cabang blank harus nil")
	}
	component := got.Branches[0].Tariffs["idv"]
	if component.FinalItem != 11006 {
		t.Fatalf("half-up 10005 x 1.1 = %d, mau 11006", component.FinalItem)
	}
	if len(got.Branches[0].ReviewCells) != 1 || got.Branches[0].ReviewCells[0].Column != "AR" || got.Branches[0].ReviewCells[0].Value != "review" {
		t.Fatalf("review cells=%v", got.Branches[0].ReviewCells)
	}
}

func TestExcelParserMenolakHashDanHeaderBerubah(t *testing.T) {
	data := fixtureWorkbook(t)
	if _, err := NewExcelParser("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").Parse(bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err=%v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.SetCellStr(SheetName, "B2", "CABANG BERUBAH")
	var changed bytes.Buffer
	_ = f.Write(&changed)
	_ = f.Close()
	if _, err := parserFor(changed.Bytes()).Parse(bytes.NewReader(changed.Bytes()), int64(changed.Len())); !errors.Is(err, ErrStructure) {
		t.Fatalf("err=%v", err)
	}
}

func TestExcelParserSumberPrivat(t *testing.T) {
	path := os.Getenv("IMPACT_WORKBOOK_PATH")
	if path == "" {
		t.Skip("IMPACT_WORKBOOK_PATH tidak diisi")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewExcelParser(ExpectedHash).Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	blankHead, review := 0, 0
	for _, b := range got.Branches {
		if b.HeadName == nil {
			blankHead++
		}
		review += len(b.ReviewCells)
	}
	if len(got.Branches) != 34 || blankHead != 1 || review == 0 {
		t.Fatalf("fakta kontrak tidak cocok: branches=%d blank_head=%d review=%d", len(got.Branches), blankHead, review)
	}
}

type fakeParser struct{ parsed ParsedWorkbook }

func (f fakeParser) Parse(_ io.Reader, _ int64) (ParsedWorkbook, error) { return f.parsed, nil }

type fakeRepo struct {
	snapshot Snapshot
	saves    []SavePreviewInput
}

func (f *fakeRepo) Snapshot(context.Context) (Snapshot, error) { return f.snapshot, nil }
func (f *fakeRepo) SavePreview(_ context.Context, in SavePreviewInput) (SavedPreview, error) {
	f.saves = append(f.saves, in)
	return SavedPreview{AttemptID: "attempt-" + itoa(len(f.saves)), ArtifactID: "artifact-sama", CreatedAt: time.Unix(int64(len(f.saves)), 0)}, nil
}

func (f *fakeRepo) LoadEvidence(_ context.Context, attemptID string) ([]EvidenceItem, error) {
	for i, saved := range f.saves {
		if attemptID != "attempt-"+itoa(i+1) {
			continue
		}
		var out []EvidenceItem
		for _, item := range saved.Items {
			if item.PrivateEvidence != "" {
				out = append(out, EvidenceItem{SourceRow: item.SourceRow, EntityKind: item.EntityKind, EntityCode: item.EntityCode, Value: item.PrivateEvidence})
			}
		}
		return out, nil
	}
	return nil, nil
}

func (f *fakeRepo) Apply(context.Context, auth.Actor, ApplyRequest) (ApplyResult, error) {
	return ApplyResult{}, nil
}

func (f *fakeRepo) Rollback(context.Context, auth.Actor, string) (RollbackResult, error) {
	return RollbackResult{}, nil
}

func TestPreviewKonflikAksesFailClosedDanAttemptTerpisah(t *testing.T) {
	head := "Sumber"
	parsed := ParsedWorkbook{SourceHash: ExpectedHash, Branches: []SourceBranch{{
		Row: 3, SourceKey: "branch:uji", Fingerprint: fingerprint("branch:uji"), Name: "Uji", HeadName: &head,
		Tariffs: map[string]MoneyComponent{}, ReviewCells: []ReviewCell{{Column: "AR", Fingerprint: fingerprint("note"), Value: "evidence privat"}},
	}}}
	for _, p := range productSpecs {
		parsed.Branches[0].Tariffs[p.Code] = MoneyComponent{Fingerprint: fingerprint("unavailable")}
	}
	repo := &fakeRepo{snapshot: Snapshot{
		Branches: map[string]BranchState{"branch:uji": {ID: "b1", Name: "Uji", HeadName: &head, IdentityStatus: "identity_incomplete", AccessUsable: true}},
		Products: map[string]ProductState{}, Variants: map[string]VariantState{}, Tariffs: map[string]TariffState{},
	}}
	svc := NewService(repo, fakeParser{parsed}, "staging")
	actor := auth.Actor{UserID: "a1", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
	one, err := svc.Preview(context.Background(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	two, err := svc.Preview(context.Background(), actor, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	if one.AttemptID == two.AttemptID || one.ArtifactID != two.ArtifactID || one.PreviewHash != two.PreviewHash {
		t.Fatalf("attempt/artifact/hash salah: %#v %#v", one, two)
	}
	foundConflict, foundReview := false, false
	for _, item := range one.Items {
		if item.ReasonCode == "inconsistent_existing_access" && item.Status == StatusConflict {
			foundConflict = true
		}
		if item.Status == StatusNeedsReview {
			foundReview = true
		}
	}
	if !foundConflict || !foundReview {
		t.Fatalf("conflict=%v review=%v", foundConflict, foundReview)
	}
	encoded, err := json.Marshal(one)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("Sumber")) || bytes.Contains(encoded, []byte("Uji")) {
		t.Fatal("preview publik tidak boleh memuat nama cabang/kepala mentah")
	}
	if bytes.Contains(encoded, []byte("evidence privat")) {
		t.Fatal("preview publik tidak boleh memuat evidence privat")
	}
	evidence, err := svc.Evidence(context.Background(), actor, one.AttemptID)
	if err != nil || len(evidence) != 1 || evidence[0].Value != "evidence privat" {
		t.Fatalf("evidence=%v err=%v", evidence, err)
	}
	manager := auth.Actor{UserID: "m1", Kind: auth.KindAdmin, Role: auth.RoleManager}
	if _, err := svc.Evidence(context.Background(), manager, one.AttemptID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("manager evidence err=%v", err)
	}
}

func TestPreviewProductionDiizinkanHanyaUntukSuperadmin(t *testing.T) {
	repo := &fakeRepo{}
	parsed := ParsedWorkbook{SourceHash: ExpectedHash}
	manager := auth.Actor{UserID: "m1", Kind: auth.KindAdmin, Role: auth.RoleManager}
	service := NewService(repo, fakeParser{parsed}, "production")
	if _, err := service.Preview(context.Background(), manager, bytes.NewReader(nil), 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("manager err=%v", err)
	}
	sa := auth.Actor{UserID: "a1", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
	preview, err := service.Preview(context.Background(), sa, bytes.NewReader(nil), 0)
	if err != nil || preview.AttemptID == "" {
		t.Fatalf("preview production=%#v err=%v", preview, err)
	}
	if len(repo.saves) != 1 || repo.saves[0].Environment != ProductionPreviewEnvironment {
		t.Fatalf("save preview production=%#v", repo.saves)
	}
	if _, err := service.Apply(context.Background(), sa, ApplyRequest{AttemptID: preview.AttemptID, PreviewHash: preview.PreviewHash, IdempotencyKey: "unit-production"}); !errors.Is(err, ErrProduction) {
		t.Fatalf("apply production err=%v", err)
	}
	if _, err := service.Rollback(context.Background(), sa, "00000000-0000-0000-0000-000000000001"); !errors.Is(err, ErrProduction) {
		t.Fatalf("rollback production err=%v", err)
	}
}

// TestApplyProductionHanyaDenganIzinOperator: apply/rollback production hanya
// terbuka lewat WithProductionApply; Superadmin tetap wajib.
func TestApplyProductionHanyaDenganIzinOperator(t *testing.T) {
	sa := auth.Actor{UserID: "a1", Kind: auth.KindAdmin, Role: auth.RoleSuperadmin}
	manager := auth.Actor{UserID: "m1", Kind: auth.KindAdmin, Role: auth.RoleManager}
	req := ApplyRequest{AttemptID: "attempt-1", PreviewHash: "hash", IdempotencyKey: "unit-production"}
	service := NewService(&fakeRepo{}, fakeParser{}, "production", WithProductionApply(true))
	if _, err := service.Apply(context.Background(), sa, req); err != nil {
		t.Fatalf("apply production berizin err=%v", err)
	}
	if _, err := service.Rollback(context.Background(), sa, "00000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("rollback production berizin err=%v", err)
	}
	if _, err := service.Apply(context.Background(), manager, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("manager tetap ditolak, err=%v", err)
	}
	tertutup := NewService(&fakeRepo{}, fakeParser{}, "production", WithProductionApply(false))
	if _, err := tertutup.Apply(context.Background(), sa, req); !errors.Is(err, ErrProduction) {
		t.Fatalf("tanpa izin operator harus ErrProduction, err=%v", err)
	}
}

func TestReconcileBranchMatriksIdentitas(t *testing.T) {
	head := "Kepala Existing"
	blank := SourceBranch{Name: "Cabang", SourceKey: "branch:cabang"}
	filled := blank
	filled.HeadName = &head
	tests := []struct {
		name   string
		source SourceBranch
		state  BranchState
		want   Status
		reason string
	}{
		{"remap", filled, BranchState{MappingConflict: true}, StatusConflict, "identity_remap_required"},
		{"ambigu", filled, BranchState{Ambiguous: true}, StatusConflict, "ambiguous_identity"},
		{"incomplete akses", filled, BranchState{IdentityStatus: "identity_incomplete", AccessUsable: true}, StatusConflict, "inconsistent_existing_access"},
		{"ready identitas tak lengkap", filled, BranchState{IdentityStatus: "ready", AccessConsistent: true}, StatusConflict, "inconsistent_existing_access"},
		{"registered akses tak konsisten", filled, BranchState{IdentityStatus: "registered", IdentityComplete: true}, StatusConflict, "inconsistent_existing_access"},
		{"blank ready", blank, BranchState{IdentityStatus: "ready", HeadName: &head, IdentityComplete: true, AccessConsistent: true}, StatusConflict, "existing_access_identity_blank"},
		{"blank registered", blank, BranchState{IdentityStatus: "registered", HeadName: &head, IdentityComplete: true, AccessConsistent: true}, StatusConflict, "existing_access_identity_blank"},
		{"incomplete aman", blank, BranchState{Name: "Cabang", IdentityStatus: "identity_incomplete", AccessUsable: false, AccessConsistent: true}, StatusSkip, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := reconcileBranch(tt.source, tt.state, true)
			if got != tt.want || reason != tt.reason {
				t.Fatalf("got=%s/%s want=%s/%s", got, reason, tt.want, tt.reason)
			}
		})
	}
}

func TestPreviewHashMengikatSnapshotExisting(t *testing.T) {
	items := []PreviewItem{{EntityKind: EntityBranch, EntityCode: "c", Fingerprint: fingerprint("source"), ExistingFingerprint: fingerprint("state-a"), Status: StatusUpdate}}
	one, err := hashPreview(ExpectedHash, items, countItems(items))
	if err != nil {
		t.Fatal(err)
	}
	items[0].ExistingFingerprint = fingerprint("state-b")
	two, err := hashPreview(ExpectedHash, items, countItems(items))
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("preview hash harus berubah ketika fingerprint snapshot existing berubah")
	}
}

func TestReconcileNamaDanSourceKeyBerubahFailClosed(t *testing.T) {
	parsed := ParsedWorkbook{Branches: []SourceBranch{{
		Row: 3, SourceKey: "branch:nama-baru", Fingerprint: fingerprint("branch:nama-baru"), Name: "Nama Baru",
		Tariffs: map[string]MoneyComponent{},
	}}}
	snapshot := Snapshot{
		Branches: map[string]BranchState{
			"branch:nama-lama": {ID: "branch-1", SourceKey: "branch:nama-lama", Name: "Nama Lama", Fingerprint: fingerprint("state-lama")},
		},
		BranchCandidates: map[string][]BranchState{},
		Products:         map[string]ProductState{},
		Variants:         map[string]VariantState{},
		Tariffs:          map[string]TariffState{},
	}

	items := reconcile(parsed, snapshot)
	if len(items) == 0 {
		t.Fatal("preview tidak menghasilkan item")
	}
	branch := items[0]
	if branch.EntityKind != EntityBranch || branch.Status != StatusConflict || branch.ReasonCode != "identity_remap_required" {
		t.Fatalf("branch=%#v, harus conflict identity_remap_required", branch)
	}
	if branch.ExistingFingerprint == "" {
		t.Fatal("conflict remap harus mengikat fingerprint mapping existing")
	}
}

func TestReconcileCabangBaruTetapInsertJikaSemuaMappingTerwakili(t *testing.T) {
	parsed := ParsedWorkbook{Branches: []SourceBranch{
		{Row: 3, SourceKey: "branch:lama", Fingerprint: fingerprint("branch:lama"), Name: "Lama", Tariffs: map[string]MoneyComponent{}},
		{Row: 4, SourceKey: "branch:baru", Fingerprint: fingerprint("branch:baru"), Name: "Baru", Tariffs: map[string]MoneyComponent{}},
	}}
	snapshot := Snapshot{
		Branches: map[string]BranchState{
			"branch:lama": {ID: "branch-1", SourceKey: "branch:lama", Name: "Lama", IdentityStatus: "identity_incomplete", AccessConsistent: true, Fingerprint: fingerprint("state-lama")},
		},
		BranchCandidates: map[string][]BranchState{},
		Products:         map[string]ProductState{},
		Variants:         map[string]VariantState{},
		Tariffs:          map[string]TariffState{},
	}

	items := reconcile(parsed, snapshot)
	for _, item := range items {
		if item.EntityKind == EntityBranch && item.SourceRow == 4 {
			if item.Status != StatusInsert || item.ReasonCode != "" {
				t.Fatalf("cabang baru=%#v, harus insert", item)
			}
			return
		}
	}
	t.Fatal("item cabang baru tidak ditemukan")
}
