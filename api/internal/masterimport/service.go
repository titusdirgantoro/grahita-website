package masterimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/grahita/portal/api/internal/auth"
)

type variantSpec struct {
	Code        string
	ProductCode string
	ParentCode  string
}

var variantSpecs = func() []variantSpec {
	out := make([]variantSpec, 0, len(productSpecs)+3)
	for _, p := range productSpecs {
		out = append(out, variantSpec{Code: p.Code, ProductCode: p.Code})
	}
	out = append(out,
		variantSpec{Code: "idv-upset-emosi", ProductCode: "idv", ParentCode: "idv"},
		variantSpec{Code: "idv-sales", ProductCode: "idv", ParentCode: "idv"},
		variantSpec{Code: "idv-aka", ProductCode: "idv", ParentCode: "idv"},
	)
	return out
}()

func ProductDefinitions() []ProductDefinition {
	out := make([]ProductDefinition, 0, len(productSpecs))
	for _, spec := range productSpecs {
		out = append(out, ProductDefinition{Code: spec.Code, Name: spec.Name})
	}
	return out
}

func VariantDefinitions() []VariantDefinition {
	out := make([]VariantDefinition, 0, len(variantSpecs))
	for _, spec := range variantSpecs {
		name := spec.Code
		for _, product := range productSpecs {
			if product.Code == spec.Code {
				name = product.Name
				break
			}
		}
		out = append(out, VariantDefinition{Code: spec.Code, ProductCode: spec.ProductCode, ParentCode: spec.ParentCode, Name: name})
	}
	return out
}

func ReconcileHash(parsed ParsedWorkbook, snapshot Snapshot) ([]PreviewItem, Counts, string, error) {
	items := reconcile(parsed, snapshot)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].SourceRow != items[j].SourceRow {
			return items[i].SourceRow < items[j].SourceRow
		}
		if items[i].EntityKind != items[j].EntityKind {
			return items[i].EntityKind < items[j].EntityKind
		}
		return items[i].EntityCode < items[j].EntityCode
	})
	counts := countItems(items)
	hash, err := hashPreview(parsed.SourceHash, items, counts)
	return items, counts, hash, err
}

func (s *Service) preview(ctx context.Context, actor auth.Actor, r io.Reader, size int64) (Preview, error) {
	if !actor.IsSuperadmin() {
		return Preview{}, ErrForbidden
	}
	parsed, err := s.parser.Parse(r, size)
	if err != nil {
		return Preview{}, err
	}
	snapshot, err := s.repo.Snapshot(ctx)
	if err != nil {
		return Preview{}, fmt.Errorf("baca snapshot master: %w", err)
	}
	items := reconcile(parsed, snapshot)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].SourceRow != items[j].SourceRow {
			return items[i].SourceRow < items[j].SourceRow
		}
		if items[i].EntityKind != items[j].EntityKind {
			return items[i].EntityKind < items[j].EntityKind
		}
		return items[i].EntityCode < items[j].EntityCode
	})
	counts := countItems(items)
	previewHash, err := hashPreview(parsed.SourceHash, items, counts)
	if err != nil {
		return Preview{}, err
	}
	saved, err := s.repo.SavePreview(ctx, SavePreviewInput{
		Environment: previewArtifactEnvironment(s.environment), ActorID: actor.UserID, SourceHash: parsed.SourceHash,
		PreviewHash: previewHash, Items: items, Counts: counts, Parsed: parsed,
	})
	if err != nil {
		return Preview{}, fmt.Errorf("simpan preview: %w", err)
	}
	return Preview{
		AttemptID: saved.AttemptID, ArtifactID: saved.ArtifactID,
		SourceHash: parsed.SourceHash, PreviewHash: previewHash,
		ContractVersion: ContractVersion, Sheet: SheetName, Counts: counts,
		Items: items, CreatedAt: saved.CreatedAt,
	}, nil
}

func previewArtifactEnvironment(environment string) string {
	if strings.EqualFold(strings.TrimSpace(environment), "production") {
		return ProductionPreviewEnvironment
	}
	return environment
}

func reconcile(parsed ParsedWorkbook, snapshot Snapshot) []PreviewItem {
	items := make([]PreviewItem, 0, ExpectedRows*(len(productSpecs)+2)+len(productSpecs)+len(variantSpecs))
	availableByProduct := make(map[string]bool, len(productSpecs))
	unmatchedMappings := unmatchedBranchMappings(parsed.Branches, snapshot.Branches)

	for _, source := range parsed.Branches {
		current, exists := snapshot.Branches[source.SourceKey]
		mapped := exists
		if !exists {
			if candidates := snapshot.BranchCandidates[source.SourceKey]; len(candidates) > 0 {
				current, exists = candidates[0], true
				current.MappingConflict = true
				current.Ambiguous = len(candidates) > 1
			}
		}
		if !exists && len(unmatchedMappings) > 0 {
			current = remapConflictState(unmatchedMappings)
			exists = true
		}
		branchStatus, branchReason := reconcileBranch(source, current, exists)
		items = append(items, PreviewItem{
			SourceRow: source.Row, EntityKind: EntityBranch, EntityCode: source.Fingerprint,
			Fingerprint: branchFingerprint(source), ExistingFingerprint: current.Fingerprint,
			Status: branchStatus, ReasonCode: branchReason,
		})
		for _, review := range source.ReviewCells {
			items = append(items, PreviewItem{
				SourceRow: source.Row, EntityKind: EntityNote, EntityCode: review.Column,
				Fingerprint: review.Fingerprint, Status: StatusNeedsReview, ReasonCode: "manual_review_required",
				PrivateEvidence: review.Value,
			})
		}
		for _, spec := range productSpecs {
			component := source.Tariffs[spec.Code]
			if component.Available {
				availableByProduct[spec.Code] = true
			}
			key := current.ID + "\x00" + spec.Code
			tariff, tariffExists := snapshot.Tariffs[key]
			status, reason := reconcileTariff(component, tariff, mapped && tariffExists)
			items = append(items, PreviewItem{
				SourceRow: source.Row, EntityKind: EntityTariff,
				EntityCode:  source.Fingerprint + ":" + spec.Code,
				Fingerprint: component.Fingerprint, ExistingFingerprint: tariff.Fingerprint,
				Status: status, ReasonCode: reason,
			})
		}
	}

	for _, spec := range productSpecs {
		active := availableByProduct[spec.Code]
		current, exists := snapshot.Products[spec.Code]
		status := StatusInsert
		if exists {
			if current.Name == spec.Name && current.Active == active {
				status = StatusSkip
			} else {
				status = StatusUpdate
			}
		}
		items = append(items, PreviewItem{
			EntityKind: EntityProduct, EntityCode: spec.Code,
			Fingerprint: fingerprint(spec.Code, spec.Name, fmt.Sprint(active)), ExistingFingerprint: current.Fingerprint, Status: status,
		})
	}
	for _, spec := range variantSpecs {
		active := availableByProduct[spec.ProductCode]
		current, exists := snapshot.Variants[spec.Code]
		status := StatusInsert
		if exists {
			if current.ProductCode == spec.ProductCode && current.ParentCode == spec.ParentCode && current.Active == active {
				status = StatusSkip
			} else {
				status = StatusUpdate
			}
		}
		items = append(items, PreviewItem{
			EntityKind: EntityVariant, EntityCode: spec.Code,
			Fingerprint: fingerprint(spec.Code, spec.ProductCode, spec.ParentCode, fmt.Sprint(active)), ExistingFingerprint: current.Fingerprint, Status: status,
		})
	}
	return items
}

func unmatchedBranchMappings(sources []SourceBranch, mapped map[string]BranchState) []BranchState {
	sourceKeys := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		sourceKeys[source.SourceKey] = struct{}{}
	}
	candidates := make([]BranchState, 0)
	for key, state := range mapped {
		if _, exists := sourceKeys[key]; exists {
			continue
		}
		state.SourceKey = key
		candidates = append(candidates, state)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].SourceKey != candidates[j].SourceKey {
			return candidates[i].SourceKey < candidates[j].SourceKey
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates
}

func remapConflictState(candidates []BranchState) BranchState {
	parts := make([]string, 0, 1+len(candidates)*3)
	parts = append(parts, "identity-remap-candidates")
	for _, candidate := range candidates {
		parts = append(parts, candidate.SourceKey, candidate.ID, candidate.Fingerprint)
	}
	return BranchState{
		MappingConflict: true,
		Ambiguous:       len(candidates) > 1,
		Fingerprint:     fingerprint(parts...),
	}
}

func reconcileBranch(source SourceBranch, current BranchState, exists bool) (Status, string) {
	if !exists {
		return StatusInsert, ""
	}
	if current.MappingConflict {
		return StatusConflict, "identity_remap_required"
	}
	if current.Ambiguous {
		return StatusConflict, "ambiguous_identity"
	}
	if !current.AccessConsistent || ((current.IdentityStatus == "ready" || current.IdentityStatus == "registered") && !current.IdentityComplete) {
		return StatusConflict, "inconsistent_existing_access"
	}
	if current.IdentityStatus == "identity_incomplete" && current.AccessUsable {
		return StatusConflict, "inconsistent_existing_access"
	}
	if !isTextComplete(source.HeadName) && (current.IdentityStatus == "ready" || current.IdentityStatus == "registered") {
		return StatusConflict, "existing_access_identity_blank"
	}
	if cleanText(current.Name) != source.Name || cleanText(current.Position) != source.Position || !sameOptionalText(current.HeadName, source.HeadName) {
		return StatusUpdate, ""
	}
	return StatusSkip, ""
}

func reconcileTariff(source MoneyComponent, current TariffState, exists bool) (Status, string) {
	if !source.Available {
		if !exists || !current.Available {
			return StatusSkip, "already_unavailable"
		}
		return StatusUpdate, "tariff_mark_unavailable"
	}
	if !exists {
		return StatusInsert, ""
	}
	if current.Available && current.ComponentHash == source.Fingerprint && current.ProjectionPresent && current.ProjectionPrice == source.FinalItem {
		return StatusSkip, ""
	}
	return StatusUpdate, ""
}

func sameOptionalText(a, b *string) bool {
	if !isTextComplete(a) && !isTextComplete(b) {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return cleanText(*a) == cleanText(*b)
}

func branchFingerprint(b SourceBranch) string {
	head := ""
	if b.HeadName != nil {
		head = maskedTextFingerprint(*b.HeadName)
	}
	return fingerprint(b.SourceKey, maskedTextFingerprint(b.Name), maskedTextFingerprint(b.Position), head)
}

func countItems(items []PreviewItem) Counts {
	var out Counts
	for _, item := range items {
		var target *Count
		switch item.EntityKind {
		case EntityBranch:
			target = &out.Branch
		case EntityProduct:
			target = &out.Product
		case EntityVariant:
			target = &out.Variant
		case EntityTariff:
			target = &out.Tariff
		case EntityNote:
			target = &out.Note
		}
		increment(target, item.Status)
		increment(&out.Total, item.Status)
	}
	return out
}

func increment(c *Count, status Status) {
	if c == nil {
		return
	}
	switch status {
	case StatusInsert:
		c.Insert++
	case StatusUpdate:
		c.Update++
	case StatusSkip:
		c.Skip++
	case StatusConflict:
		c.Conflict++
	case StatusNeedsReview:
		c.NeedsReview++
	}
}

func hashPreview(sourceHash string, items []PreviewItem, counts Counts) (string, error) {
	type hashItem struct {
		SourceRow           int        `json:"source_row"`
		EntityKind          EntityKind `json:"entity_kind"`
		EntityCode          string     `json:"entity_code"`
		Fingerprint         string     `json:"fingerprint"`
		ExistingFingerprint string     `json:"existing_fingerprint,omitempty"`
		Status              Status     `json:"status"`
		ReasonCode          string     `json:"reason_code,omitempty"`
	}
	hashItems := make([]hashItem, 0, len(items))
	for _, item := range items {
		hashItems = append(hashItems, hashItem{
			SourceRow: item.SourceRow, EntityKind: item.EntityKind, EntityCode: item.EntityCode,
			Fingerprint: item.Fingerprint, ExistingFingerprint: item.ExistingFingerprint,
			Status: item.Status, ReasonCode: item.ReasonCode,
		})
	}
	payload := struct {
		ContractVersion string     `json:"contract_version"`
		SourceHash      string     `json:"source_hash"`
		Counts          Counts     `json:"counts"`
		Items           []hashItem `json:"items"`
	}{ContractVersion, sourceHash, counts, hashItems}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode preview hash: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) Apply(ctx context.Context, actor auth.Actor, req ApplyRequest) (ApplyResult, error) {
	if !actor.IsSuperadmin() {
		return ApplyResult{}, ErrForbidden
	}
	if strings.EqualFold(strings.TrimSpace(s.environment), "production") {
		return ApplyResult{}, ErrProduction
	}
	if strings.TrimSpace(req.AttemptID) == "" || strings.TrimSpace(req.PreviewHash) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		return ApplyResult{}, ErrApplyBlocked
	}
	return s.repo.Apply(ctx, actor, req)
}

func (s *Service) Rollback(ctx context.Context, actor auth.Actor, applyRunID string) (RollbackResult, error) {
	if !actor.IsSuperadmin() {
		return RollbackResult{}, ErrForbidden
	}
	if strings.EqualFold(strings.TrimSpace(s.environment), "production") {
		return RollbackResult{}, ErrProduction
	}
	if strings.TrimSpace(applyRunID) == "" {
		return RollbackResult{}, ErrAttemptNotFound
	}
	return s.repo.Rollback(ctx, actor, applyRunID)
}
