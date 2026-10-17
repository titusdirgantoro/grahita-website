package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/masterimport"
	"github.com/grahita/portal/api/internal/tarif"
)

type storedPreviewItem struct {
	SourceRow           int
	EntityKind          masterimport.EntityKind
	EntityCode          string
	Fingerprint         string
	ExistingFingerprint string
	Status              masterimport.Status
	ReasonCode          string
}

type changeEnvelope struct {
	Before any `json:"before,omitempty"`
	After  any `json:"after,omitempty"`
	Meta   any `json:"meta,omitempty"`
}

type branchChangeState struct {
	Name           string  `json:"name"`
	Position       string  `json:"position"`
	HeadName       *string `json:"head_name"`
	IdentityStatus string  `json:"identity_status"`
}

type productChangeState struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

type variantChangeState struct {
	Code      string `json:"code"`
	ProductID string `json:"product_id"`
	ParentID  string `json:"parent_id"`
	Name      string `json:"name"`
	Active    bool   `json:"active"`
}

type tariffChangeMeta struct {
	NewVersion       tariffVersionState   `json:"new_version"`
	OldVersionBefore *tariffVersionState  `json:"old_version_before,omitempty"`
	OldVersionAfter  *tariffVersionState  `json:"old_version_after,omitempty"`
	ProjectionBefore priceProjectionState `json:"projection_before"`
	ProjectionAfter  priceProjectionState `json:"projection_after"`
	BranchID         string               `json:"branch_id"`
	VariantID        string               `json:"variant_id"`
	ProductID        string               `json:"product_id"`
}

type identityRevocationMeta struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type identityRevocationAuditResolution struct {
	EntityID string `json:"entity_id"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func identityRevocationAuditReason(decision string) (string, error) {
	switch decision {
	case "revoke_access_and_mark_incomplete":
		return "source_identity_blank_access_revoked", nil
	case "revoke_inconsistent_access":
		return "inconsistent_existing_access_revoked", nil
	default:
		return "", fmt.Errorf("keputusan revokasi identitas tidak dikenal: %s", decision)
	}
}

type tariffVersionState struct {
	ID            string     `json:"id"`
	BranchID      string     `json:"branch_id"`
	VariantID     string     `json:"variant_id"`
	HJM           *int64     `json:"hjm,omitempty"`
	P1            *string    `json:"p1,omitempty"`
	SPV           *int64     `json:"spv,omitempty"`
	FinalItem     *int64     `json:"final_item,omitempty"`
	Available     bool       `json:"available"`
	AttemptID     string     `json:"attempt_id"`
	SourceSheet   string     `json:"source_sheet"`
	SourceRow     int        `json:"source_row"`
	SourceColumns string     `json:"source_columns"`
	ComponentHash string     `json:"component_hash"`
	ValidFrom     time.Time  `json:"valid_from"`
	SupersededAt  *time.Time `json:"superseded_at,omitempty"`
}

type priceProjectionState struct {
	Present   bool      `json:"present"`
	BranchID  string    `json:"branch_id,omitempty"`
	ProductID string    `json:"product_id,omitempty"`
	Price     int64     `json:"price,omitempty"`
	UpdatedBy *string   `json:"updated_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type applyChange struct {
	Sequence    int
	Kind        string
	EntityID    string
	Operation   string
	Envelope    changeEnvelope
	Fingerprint string
	Reversible  bool
}

type rollbackRow struct {
	kind, id, operation string
	payload             []byte
	keyID               string
	fp                  string
	reversible          bool
}

func (r *Repo) Apply(ctx context.Context, actor auth.Actor, req masterimport.ApplyRequest) (masterimport.ApplyResult, error) {
	if r.snapshotCipher == nil || r.snapshotCipher.err != nil {
		return masterimport.ApplyResult{}, errSnapshotKeyInvalid
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return masterimport.ApplyResult{}, fmt.Errorf("mulai transaksi apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if result, found, err := loadApplyByKey(ctx, tx, req.IdempotencyKey); err != nil {
		return masterimport.ApplyResult{}, err
	} else if found {
		if result.AttemptID != req.AttemptID {
			return masterimport.ApplyResult{}, masterimport.ErrAlreadyApplied
		}
		result.Idempotent = true
		return result, nil
	}

	var storedHash, status string
	var normalized []byte
	err = tx.QueryRow(ctx, `SELECT preview_hash,status,normalized_source FROM master_import_attempts WHERE id=$1 FOR UPDATE`, req.AttemptID).
		Scan(&storedHash, &status, &normalized)
	if errors.Is(err, pgx.ErrNoRows) {
		return masterimport.ApplyResult{}, masterimport.ErrAttemptNotFound
	}
	if err != nil {
		return masterimport.ApplyResult{}, fmt.Errorf("kunci attempt: %w", err)
	}
	if status == "applied" {
		if result, found, loadErr := loadApplyByKey(ctx, tx, req.IdempotencyKey); loadErr != nil {
			return masterimport.ApplyResult{}, loadErr
		} else if found && result.AttemptID == req.AttemptID {
			result.Idempotent = true
			return result, nil
		}
		return masterimport.ApplyResult{}, masterimport.ErrAlreadyApplied
	}
	if storedHash != req.PreviewHash {
		return masterimport.ApplyResult{}, masterimport.ErrPreviewStale
	}

	// Import master jarang dilakukan. Lock tabel eksplisit menutup race antara
	// fingerprint ulang dengan mutasi identitas/tarif lain di transaksi berbeda.
	if _, err := tx.Exec(ctx, `LOCK TABLE branches, branch_users, registration_tokens, password_reset_tokens, sessions,
		products, product_variants, branch_tariff_versions, branch_prices, master_import_identities IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return masterimport.ApplyResult{}, fmt.Errorf("kunci master import: %w", err)
	}

	var parsed masterimport.ParsedWorkbook
	if err := json.Unmarshal(normalized, &parsed); err != nil {
		return masterimport.ApplyResult{}, fmt.Errorf("decode sumber ternormalisasi: %w", err)
	}
	snapshot, err := snapshotFrom(ctx, tx)
	if err != nil {
		return masterimport.ApplyResult{}, err
	}
	currentItems, _, currentHash, err := masterimport.ReconcileHash(parsed, snapshot)
	if err != nil {
		return masterimport.ApplyResult{}, err
	}
	if currentHash != storedHash {
		return masterimport.ApplyResult{}, masterimport.ErrPreviewStale
	}
	storedItems, err := loadPreviewItems(ctx, tx, req.AttemptID)
	if err != nil {
		return masterimport.ApplyResult{}, err
	}
	if !samePreviewItems(storedItems, currentItems) {
		return masterimport.ApplyResult{}, masterimport.ErrPreviewStale
	}
	decisions, err := validateDecisions(storedItems, req.Decisions)
	if err != nil {
		return masterimport.ApplyResult{}, err
	}

	var applyRunID string
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	emptyResult, _ := json.Marshal(masterimport.ApplyResult{})
	err = tx.QueryRow(ctx, `INSERT INTO master_import_apply_runs
		(attempt_id,actor_id,preview_hash,idempotency_key,result,applied_at)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text`, req.AttemptID, actor.UserID, storedHash,
		req.IdempotencyKey, emptyResult, appliedAt).Scan(&applyRunID)
	if err != nil {
		return masterimport.ApplyResult{}, fmt.Errorf("buat apply run: %w", err)
	}

	changes := make([]applyChange, 0)
	counts := masterimport.MutationCounts{}
	seq := 0
	nextChange := func(kind, entityID, operation string, before, after, meta any, reversible bool) error {
		seq++
		env := changeEnvelope{Before: before, After: after, Meta: meta}
		fp, err := fingerprintJSON(after)
		if err != nil {
			return err
		}
		changes = append(changes, applyChange{seq, kind, entityID, operation, env, fp, reversible})
		return nil
	}

	available := availableProducts(parsed)
	productIDs := make(map[string]string)
	for _, def := range masterimport.ProductDefinitions() {
		current, exists := snapshot.Products[def.Code]
		want := productChangeState{Code: def.Code, Name: def.Name, Active: available[def.Code]}
		if !exists {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO products (kode,nama,is_active) VALUES ($1,$2,$3) RETURNING id::text`, def.Code, def.Name, want.Active).Scan(&id); err != nil {
				return masterimport.ApplyResult{}, fmt.Errorf("insert product %s: %w", def.Code, err)
			}
			productIDs[def.Code] = id
			if err := nextChange("product", id, "insert", nil, want, nil, true); err != nil {
				return masterimport.ApplyResult{}, err
			}
			counts.Product++
			if err := insertMutationAudit(ctx, tx, actor, "product", id, "create", def.Code, applyRunID); err != nil {
				return masterimport.ApplyResult{}, err
			}
			continue
		}
		productIDs[def.Code] = current.ID
		if current.Name == want.Name && current.Active == want.Active {
			continue
		}
		before := productChangeState{Code: current.Code, Name: current.Name, Active: current.Active}
		if _, err := tx.Exec(ctx, `UPDATE products SET nama=$2,is_active=$3,updated_at=now() WHERE id=$1`, current.ID, want.Name, want.Active); err != nil {
			return masterimport.ApplyResult{}, fmt.Errorf("update product %s: %w", def.Code, err)
		}
		if err := nextChange("product", current.ID, "update", before, want, nil, true); err != nil {
			return masterimport.ApplyResult{}, err
		}
		counts.Product++
		if err := insertMutationAudit(ctx, tx, actor, "product", current.ID, "update", def.Code, applyRunID); err != nil {
			return masterimport.ApplyResult{}, err
		}
	}

	variantIDs := make(map[string]string)
	for _, def := range masterimport.VariantDefinitions() {
		productID := productIDs[def.ProductCode]
		parentID := ""
		if def.ParentCode != "" {
			parentID = variantIDs[def.ParentCode]
		}
		want := variantChangeState{Code: def.Code, ProductID: productID, ParentID: parentID, Name: def.Name, Active: available[def.ProductCode]}
		current, exists := snapshot.Variants[def.Code]
		if !exists {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO product_variants (product_id,code,nama,tariff_parent_variant_id,is_active)
				VALUES ($1,$2,$3,NULLIF($4,'')::uuid,$5) RETURNING id::text`, productID, def.Code, def.Name, parentID, want.Active).Scan(&id); err != nil {
				return masterimport.ApplyResult{}, fmt.Errorf("insert variant %s: %w", def.Code, err)
			}
			variantIDs[def.Code] = id
			if err := nextChange("variant", id, "insert", nil, want, nil, true); err != nil {
				return masterimport.ApplyResult{}, err
			}
			counts.Variant++
			if err := insertMutationAudit(ctx, tx, actor, "product_variant", id, "create", def.Code, applyRunID); err != nil {
				return masterimport.ApplyResult{}, err
			}
			continue
		}
		variantIDs[def.Code] = current.ID
		currentProductID := productIDs[current.ProductCode]
		currentParentID := ""
		if current.ParentCode != "" {
			currentParentID = snapshot.Variants[current.ParentCode].ID
		}
		before := variantChangeState{Code: current.Code, ProductID: currentProductID, ParentID: currentParentID, Name: def.Name, Active: current.Active}
		if current.ProductCode == def.ProductCode && current.ParentCode == def.ParentCode && current.Active == want.Active {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE product_variants SET product_id=$2,nama=$3,tariff_parent_variant_id=NULLIF($4,'')::uuid,is_active=$5,updated_at=now() WHERE id=$1`,
			current.ID, productID, def.Name, parentID, want.Active); err != nil {
			return masterimport.ApplyResult{}, fmt.Errorf("update variant %s: %w", def.Code, err)
		}
		if err := nextChange("variant", current.ID, "update", before, want, nil, true); err != nil {
			return masterimport.ApplyResult{}, err
		}
		counts.Variant++
		if err := insertMutationAudit(ctx, tx, actor, "product_variant", current.ID, "update", def.Code, applyRunID); err != nil {
			return masterimport.ApplyResult{}, err
		}
	}

	branchIDs := make(map[string]string)
	for _, source := range parsed.Branches {
		item := findStoredItem(storedItems, source.Row, masterimport.EntityBranch, source.Fingerprint)
		decision := decisions[itemKey(item.SourceRow, item.EntityKind, item.EntityCode)]
		current, exists := snapshot.Branches[source.SourceKey]
		if item.Status == masterimport.StatusConflict {
			if decision.Decision == "preserve_existing_verified_identity" {
				branchIDs[source.SourceKey] = current.ID
				continue
			}
			if decision.Decision == "revoke_access_and_mark_incomplete" || decision.Decision == "revoke_inconsistent_access" {
				clearName := decision.Decision == "revoke_access_and_mark_incomplete"
				if err := revokeBranchAccess(ctx, tx, current.ID, clearName); err != nil {
					return masterimport.ApplyResult{}, err
				}
				after := branchChangeState{Name: current.Name, Position: current.Position, HeadName: current.HeadName, IdentityStatus: "identity_incomplete"}
				if clearName {
					after.HeadName = nil
				}
				if err := nextChange("identity_revocation", current.ID, "revoke", nil, after, identityRevocationMeta{Decision: decision.Decision, Reason: decision.Reason}, false); err != nil {
					return masterimport.ApplyResult{}, err
				}
				counts.Revoked++
				branchIDs[source.SourceKey] = current.ID
				if err := insertMutationAudit(ctx, tx, actor, "branch", current.ID, "identity_revoke", source.Fingerprint, applyRunID); err != nil {
					return masterimport.ApplyResult{}, err
				}
				continue
			}
		}
		wantStatus := "identity_incomplete"
		if exists {
			wantStatus = current.IdentityStatus
		}
		want := branchChangeState{Name: source.Name, Position: source.Position, HeadName: source.HeadName, IdentityStatus: wantStatus}
		if !exists {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO branches (nama,nama_kepala,email,jabatan_kepala,identity_status)
				VALUES ($1,$2,NULL,NULLIF($3,''),'identity_incomplete') RETURNING id::text`, source.Name, source.HeadName, source.Position).Scan(&id); err != nil {
				return masterimport.ApplyResult{}, fmt.Errorf("insert branch row %d: %w", source.Row, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO master_import_identities (source_system,entity_kind,source_key,entity_id) VALUES ('impact','branch',$1,$2)`, source.SourceKey, id); err != nil {
				return masterimport.ApplyResult{}, fmt.Errorf("map branch row %d: %w", source.Row, err)
			}
			branchIDs[source.SourceKey] = id
			if err := nextChange("branch", id, "insert", nil, want, map[string]any{"source_key": source.SourceKey}, true); err != nil {
				return masterimport.ApplyResult{}, err
			}
			counts.Branch++
			if err := insertMutationAudit(ctx, tx, actor, "branch", id, "create", source.Fingerprint, applyRunID); err != nil {
				return masterimport.ApplyResult{}, err
			}
			continue
		}
		branchIDs[source.SourceKey] = current.ID
		if item.Status == masterimport.StatusSkip {
			continue
		}
		before := branchChangeState{Name: current.Name, Position: current.Position, HeadName: current.HeadName, IdentityStatus: current.IdentityStatus}
		if _, err := tx.Exec(ctx, `UPDATE branches SET nama=$2,jabatan_kepala=NULLIF($3,''),nama_kepala=$4,updated_at=now() WHERE id=$1`, current.ID, source.Name, source.Position, source.HeadName); err != nil {
			return masterimport.ApplyResult{}, fmt.Errorf("update branch row %d: %w", source.Row, err)
		}
		if err := nextChange("branch", current.ID, "update", before, want, nil, true); err != nil {
			return masterimport.ApplyResult{}, err
		}
		counts.Branch++
		if err := insertMutationAudit(ctx, tx, actor, "branch", current.ID, "update", source.Fingerprint, applyRunID); err != nil {
			return masterimport.ApplyResult{}, err
		}
	}

	for _, source := range parsed.Branches {
		branchID := branchIDs[source.SourceKey]
		for _, def := range masterimport.ProductDefinitions() {
			component := source.Tariffs[def.Code]
			variantID, productID := variantIDs[def.Code], productIDs[def.Code]
			oldVersion, err := loadActiveTariffVersion(ctx, tx, branchID, variantID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return masterimport.ApplyResult{}, fmt.Errorf("baca tarif aktif: %w", err)
			}
			exists := err == nil
			projectionBefore, projectionErr := loadPriceProjection(ctx, tx, branchID, productID)
			projectionExists := projectionErr == nil
			if projectionErr != nil && !errors.Is(projectionErr, pgx.ErrNoRows) {
				return masterimport.ApplyResult{}, projectionErr
			}
			if !projectionExists {
				projectionBefore = priceProjectionState{BranchID: branchID, ProductID: productID}
			}
			if component.Available && exists && oldVersion.Available && oldVersion.ComponentHash == component.Fingerprint && projectionExists && projectionBefore.Price == component.FinalItem {
				continue
			}
			if !component.Available && (!exists || !oldVersion.Available) && !projectionExists {
				continue
			}
			if exists {
				if _, err := tx.Exec(ctx, `UPDATE branch_tariff_versions SET superseded_at=$2 WHERE id=$1`, oldVersion.ID, appliedAt); err != nil {
					return masterimport.ApplyResult{}, err
				}
			}
			var newID string
			var newVersion tariffVersionState
			projectionAfter := priceProjectionState{BranchID: branchID, ProductID: productID}
			if component.Available {
				err = tx.QueryRow(ctx, `INSERT INTO branch_tariff_versions
					(branch_id,variant_id,hjm_rupiah,p1_ratio,spv_rupiah,final_item_rupiah,is_available,source_import_attempt_id,source_sheet,source_row,source_columns,source_component_hash,valid_from)
					VALUES ($1,$2,$3,$4::numeric,$5,$6,true,$7,$8,$9,$10,$11,$12) RETURNING id::text`,
					branchID, variantID, component.HJM, ratioNumeric(component.P1), component.SPV, component.FinalItem,
					req.AttemptID, masterimport.SheetName, source.Row, tariffColumns(def.Code), component.Fingerprint, appliedAt).Scan(&newID)
				if err == nil {
					_, err = tx.Exec(ctx, `INSERT INTO branch_prices (branch_id,product_id,harga,updated_by,updated_at)
						VALUES ($1,$2,$3,$4,$5) ON CONFLICT (branch_id,product_id) DO UPDATE SET harga=EXCLUDED.harga,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`,
						branchID, productID, component.FinalItem, actor.UserID, appliedAt)
				}
				actorID := actor.UserID
				projectionAfter = priceProjectionState{Present: true, BranchID: branchID, ProductID: productID, Price: component.FinalItem, UpdatedBy: &actorID, UpdatedAt: appliedAt}
				hjm, p1, spv, final := component.HJM, ratioNumeric(component.P1), component.SPV, component.FinalItem
				newVersion = tariffVersionState{ID: newID, BranchID: branchID, VariantID: variantID, HJM: &hjm, P1: &p1, SPV: &spv, FinalItem: &final, Available: true, AttemptID: req.AttemptID, SourceSheet: masterimport.SheetName, SourceRow: source.Row, SourceColumns: tariffColumns(def.Code), ComponentHash: component.Fingerprint, ValidFrom: appliedAt}
			} else {
				err = tx.QueryRow(ctx, `INSERT INTO branch_tariff_versions
					(branch_id,variant_id,is_available,source_import_attempt_id,source_sheet,source_row,source_columns,source_component_hash,valid_from)
					VALUES ($1,$2,false,$3,$4,$5,$6,$7,$8) RETURNING id::text`, branchID, variantID, req.AttemptID,
					masterimport.SheetName, source.Row, tariffColumns(def.Code), component.Fingerprint, appliedAt).Scan(&newID)
				if err == nil {
					_, err = tx.Exec(ctx, `DELETE FROM branch_prices WHERE branch_id=$1 AND product_id=$2`, branchID, productID)
				}
				newVersion = tariffVersionState{ID: newID, BranchID: branchID, VariantID: variantID, Available: false, AttemptID: req.AttemptID, SourceSheet: masterimport.SheetName, SourceRow: source.Row, SourceColumns: tariffColumns(def.Code), ComponentHash: component.Fingerprint, ValidFrom: appliedAt}
			}
			if err != nil {
				return masterimport.ApplyResult{}, fmt.Errorf("tulis versi tarif row %d code %s: %w", source.Row, def.Code, err)
			}
			meta := tariffChangeMeta{NewVersion: newVersion, ProjectionBefore: projectionBefore, ProjectionAfter: projectionAfter, BranchID: branchID, VariantID: variantID, ProductID: productID}
			if exists {
				oldBefore := oldVersion
				oldAfter := oldVersion
				oldAfter.SupersededAt = &appliedAt
				meta.OldVersionBefore = &oldBefore
				meta.OldVersionAfter = &oldAfter
			}
			after := struct {
				Version    tariffVersionState   `json:"version"`
				Projection priceProjectionState `json:"projection"`
			}{newVersion, projectionAfter}
			if err := nextChange("tariff", newID, "insert", nil, after, meta, true); err != nil {
				return masterimport.ApplyResult{}, err
			}
			counts.Tariff++
			if err := insertMutationAudit(ctx, tx, actor, "branch_tariff", newID, "create", source.Fingerprint+":"+def.Code, applyRunID); err != nil {
				return masterimport.ApplyResult{}, err
			}
		}
	}

	for key, decision := range decisions {
		parts := strings.SplitN(key, "\x00", 3)
		if _, err := tx.Exec(ctx, `UPDATE master_import_preview_items SET decision=$5,decision_reason=$6,decided_by=$7,decided_at=now()
			WHERE attempt_id=$1 AND source_row=$2 AND entity_kind=$3 AND entity_code=$4`, req.AttemptID, decision.SourceRow, parts[1], decision.EntityCode,
			decision.Decision, decision.Reason, actor.UserID); err != nil {
			return masterimport.ApplyResult{}, fmt.Errorf("simpan keputusan item: %w", err)
		}
		if err := insertMutationAudit(ctx, tx, actor, "master_import_decision", applyRunID, "resolve", decision.EntityCode, applyRunID); err != nil {
			return masterimport.ApplyResult{}, err
		}
	}

	for _, change := range changes {
		payload, err := json.Marshal(change.Envelope)
		if err != nil {
			return masterimport.ApplyResult{}, err
		}
		ciphertext, keyID, err := r.snapshotCipher.encrypt(payload, snapshotAAD(applyRunID, change.Sequence))
		if err != nil {
			return masterimport.ApplyResult{}, fmt.Errorf("enkripsi before snapshot: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO master_import_apply_changes
			(apply_run_id,sequence_no,entity_kind,entity_id,operation,before_state,encryption_key_id,after_fingerprint,reversible)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, applyRunID, change.Sequence, change.Kind, change.EntityID,
			change.Operation, ciphertext, keyID, change.Fingerprint, change.Reversible); err != nil {
			return masterimport.ApplyResult{}, fmt.Errorf("simpan before snapshot: %w", err)
		}
	}
	result := masterimport.ApplyResult{ApplyRunID: applyRunID, AttemptID: req.AttemptID, Status: "applied", Mutations: counts, AppliedAt: appliedAt}
	resultJSON, _ := json.Marshal(result)
	if _, err := tx.Exec(ctx, `UPDATE master_import_apply_runs SET result=$2,contains_revocation=$3 WHERE id=$1`, applyRunID, resultJSON, counts.Revoked > 0); err != nil {
		return masterimport.ApplyResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE master_import_attempts SET status='applied' WHERE id=$1`, req.AttemptID); err != nil {
		return masterimport.ApplyResult{}, err
	}
	if err := insertMutationAudit(ctx, tx, actor, "master_import", applyRunID, "apply", storedHash, applyRunID); err != nil {
		return masterimport.ApplyResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return masterimport.ApplyResult{}, fmt.Errorf("commit apply: %w", err)
	}
	return result, nil
}

func (r *Repo) Rollback(ctx context.Context, actor auth.Actor, applyRunID string) (masterimport.RollbackResult, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return masterimport.RollbackResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE branches, products, product_variants, branch_tariff_versions, branch_prices, master_import_identities IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return masterimport.RollbackResult{}, err
	}
	var status string
	var rolledBackAt *time.Time
	err = tx.QueryRow(ctx, `SELECT status,rolled_back_at FROM master_import_apply_runs WHERE id=$1 FOR UPDATE`, applyRunID).Scan(&status, &rolledBackAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return masterimport.RollbackResult{}, masterimport.ErrAttemptNotFound
	}
	if err != nil {
		return masterimport.RollbackResult{}, err
	}
	if status == "rolled_back" && rolledBackAt != nil {
		return masterimport.RollbackResult{ApplyRunID: applyRunID, Status: status, RolledBackAt: *rolledBackAt}, nil
	}
	rows, err := tx.Query(ctx, `SELECT sequence_no,entity_kind,entity_id::text,operation,before_state,encryption_key_id,after_fingerprint,reversible
		FROM master_import_apply_changes WHERE apply_run_id=$1 ORDER BY sequence_no DESC`, applyRunID)
	if err != nil {
		return masterimport.RollbackResult{}, err
	}
	var list []rollbackRow
	for rows.Next() {
		var c rollbackRow
		var sequence int
		if err := rows.Scan(&sequence, &c.kind, &c.id, &c.operation, &c.payload, &c.keyID, &c.fp, &c.reversible); err != nil {
			rows.Close()
			return masterimport.RollbackResult{}, err
		}
		plaintext, err := r.snapshotCipher.decrypt(c.payload, c.keyID, snapshotAAD(applyRunID, sequence))
		if err != nil {
			rows.Close()
			return masterimport.RollbackResult{}, fmt.Errorf("dekripsi before snapshot: %w", err)
		}
		c.payload = plaintext
		list = append(list, c)
	}
	rows.Close()
	result := masterimport.RollbackResult{ApplyRunID: applyRunID, Status: "rolled_back"}
	irreversibleResolutions := make([]identityRevocationAuditResolution, 0)
	for _, change := range list {
		if !change.reversible {
			if change.kind != "identity_revocation" {
				return masterimport.RollbackResult{}, fmt.Errorf("perubahan irreversible tidak dikenal: %s", change.kind)
			}
			var env changeEnvelope
			if err := json.Unmarshal(change.payload, &env); err != nil {
				return masterimport.RollbackResult{}, err
			}
			metaBytes, err := json.Marshal(env.Meta)
			if err != nil {
				return masterimport.RollbackResult{}, err
			}
			var meta identityRevocationMeta
			if err := json.Unmarshal(metaBytes, &meta); err != nil {
				return masterimport.RollbackResult{}, err
			}
			if strings.TrimSpace(meta.Decision) == "" || strings.TrimSpace(meta.Reason) == "" {
				return masterimport.RollbackResult{}, errors.New("metadata resolusi revokasi identitas tidak lengkap")
			}
			auditReason, err := identityRevocationAuditReason(meta.Decision)
			if err != nil {
				return masterimport.RollbackResult{}, err
			}
			irreversibleResolutions = append(irreversibleResolutions, identityRevocationAuditResolution{
				EntityID: change.id,
				Decision: meta.Decision,
				Reason:   auditReason,
			})
			result.Irreversible++
			continue
		}
		var env changeEnvelope
		if err := json.Unmarshal(change.payload, &env); err != nil {
			return masterimport.RollbackResult{}, err
		}
		if err := rollbackChange(ctx, tx, change, env); err != nil {
			return masterimport.RollbackResult{}, err
		}
		result.Restored++
	}
	result.RolledBackAt = time.Now().UTC()
	if _, err := tx.Exec(ctx, `UPDATE master_import_apply_runs
		SET status='rolled_back',rolled_back_at=$2,rolled_back_by=$3,
		    result=jsonb_set(jsonb_set(result,'{status}','"rolled_back"'::jsonb),'{rolled_back_at}',to_jsonb($2::timestamptz),true)
		WHERE id=$1`, applyRunID, result.RolledBackAt, actor.UserID); err != nil {
		return masterimport.RollbackResult{}, err
	}
	if result.Irreversible > 0 {
		if err := insertIdentityRevocationRollbackAudit(ctx, tx, actor, applyRunID, irreversibleResolutions); err != nil {
			return masterimport.RollbackResult{}, err
		}
	}
	if err := insertMutationAudit(ctx, tx, actor, "master_import", applyRunID, "rollback", fmt.Sprint(result.Restored), applyRunID); err != nil {
		return masterimport.RollbackResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return masterimport.RollbackResult{}, err
	}
	return result, nil
}

func rollbackChange(ctx context.Context, tx pgx.Tx, change rollbackRow, env changeEnvelope) error {
	switch change.kind {
	case "tariff":
		metaBytes, _ := json.Marshal(env.Meta)
		var meta tariffChangeMeta
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			return err
		}
		if err := verifyTariffChangeAfter(ctx, tx, meta, change.fp); err != nil {
			return err
		}
		var activeID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM branch_tariff_versions WHERE branch_id=$1 AND variant_id=$2 AND superseded_at IS NULL FOR UPDATE`, meta.BranchID, meta.VariantID).Scan(&activeID); err != nil || activeID != meta.NewVersion.ID {
			return masterimport.ErrRollbackConflict
		}
		if err := restorePriceProjection(ctx, tx, meta.ProjectionBefore); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM branch_tariff_versions WHERE id=$1`, meta.NewVersion.ID); err != nil {
			return err
		}
		if meta.OldVersionBefore != nil {
			if _, err := tx.Exec(ctx, `UPDATE branch_tariff_versions SET superseded_at=$2 WHERE id=$1`, meta.OldVersionBefore.ID, meta.OldVersionBefore.SupersededAt); err != nil {
				return err
			}
		}
	case "branch":
		if change.operation == "insert" {
			if err := verifyBranchAfter(ctx, tx, change.id, change.fp); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM master_import_identities WHERE source_system='impact' AND entity_kind='branch' AND entity_id=$1`, change.id); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `DELETE FROM branches WHERE id=$1`, change.id)
			if err != nil || tag.RowsAffected() != 1 {
				return masterimport.ErrRollbackConflict
			}
			return nil
		}
		beforeBytes, _ := json.Marshal(env.Before)
		var before branchChangeState
		if err := json.Unmarshal(beforeBytes, &before); err != nil {
			return err
		}
		if err := verifyBranchAfter(ctx, tx, change.id, change.fp); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE branches SET nama=$2,jabatan_kepala=NULLIF($3,''),nama_kepala=$4,identity_status=$5::branch_identity_status,updated_at=now() WHERE id=$1`, change.id, before.Name, before.Position, before.HeadName, before.IdentityStatus)
		return err
	case "variant":
		if change.operation == "insert" {
			if err := verifyVariantAfter(ctx, tx, change.id, change.fp); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `DELETE FROM product_variants WHERE id=$1`, change.id)
			if err != nil || tag.RowsAffected() != 1 {
				return masterimport.ErrRollbackConflict
			}
			return nil
		}
		beforeBytes, _ := json.Marshal(env.Before)
		var before variantChangeState
		if err := json.Unmarshal(beforeBytes, &before); err != nil {
			return err
		}
		if err := verifyVariantAfter(ctx, tx, change.id, change.fp); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE product_variants SET product_id=$2,nama=$3,tariff_parent_variant_id=NULLIF($4,'')::uuid,is_active=$5,updated_at=now() WHERE id=$1`, change.id, before.ProductID, before.Name, before.ParentID, before.Active)
		return err
	case "product":
		if change.operation == "insert" {
			if err := verifyProductAfter(ctx, tx, change.id, change.fp); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `DELETE FROM products WHERE id=$1`, change.id)
			if err != nil || tag.RowsAffected() != 1 {
				return masterimport.ErrRollbackConflict
			}
			return nil
		}
		beforeBytes, _ := json.Marshal(env.Before)
		var before productChangeState
		if err := json.Unmarshal(beforeBytes, &before); err != nil {
			return err
		}
		if err := verifyProductAfter(ctx, tx, change.id, change.fp); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE products SET nama=$2,is_active=$3,updated_at=now() WHERE id=$1`, change.id, before.Name, before.Active)
		return err
	}
	return nil
}

func validateDecisions(items []storedPreviewItem, input []masterimport.Decision) (map[string]masterimport.Decision, error) {
	provided := make(map[string]masterimport.Decision, len(input))
	for _, d := range input {
		if strings.TrimSpace(d.Reason) == "" {
			return nil, masterimport.ErrApplyBlocked
		}
		provided[itemKey(d.SourceRow, d.EntityKind, d.EntityCode)] = d
	}
	resolved := make(map[string]masterimport.Decision)
	for _, item := range items {
		if item.Status != masterimport.StatusConflict && item.Status != masterimport.StatusNeedsReview {
			continue
		}
		key := itemKey(item.SourceRow, item.EntityKind, item.EntityCode)
		d, ok := provided[key]
		if !ok {
			return nil, masterimport.ErrApplyBlocked
		}
		switch item.Status {
		case masterimport.StatusNeedsReview:
			if d.Decision != "ignored_with_reason" && d.Decision != "mapped_by_explicit_rule" && d.Decision != "source_corrected" {
				return nil, masterimport.ErrApplyBlocked
			}
			if d.Decision == "source_corrected" {
				return nil, masterimport.ErrApplyBlocked
			}
		case masterimport.StatusConflict:
			switch item.ReasonCode {
			case "existing_access_identity_blank":
				if d.Decision != "preserve_existing_verified_identity" && d.Decision != "revoke_access_and_mark_incomplete" {
					return nil, masterimport.ErrApplyBlocked
				}
			case "inconsistent_existing_access":
				if d.Decision != "revoke_inconsistent_access" {
					return nil, masterimport.ErrApplyBlocked
				}
			default:
				return nil, masterimport.ErrApplyBlocked
			}
		}
		resolved[key] = d
	}
	return resolved, nil
}

func loadPreviewItems(ctx context.Context, tx pgx.Tx, attemptID string) ([]storedPreviewItem, error) {
	rows, err := tx.Query(ctx, `SELECT source_row,entity_kind,entity_code,fingerprint,COALESCE(existing_fingerprint,''),preview_status,COALESCE(reason_code,'')
		FROM master_import_preview_items WHERE attempt_id=$1 ORDER BY source_row,entity_kind,entity_code FOR UPDATE`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedPreviewItem
	for rows.Next() {
		var item storedPreviewItem
		if err := rows.Scan(&item.SourceRow, &item.EntityKind, &item.EntityCode, &item.Fingerprint, &item.ExistingFingerprint, &item.Status, &item.ReasonCode); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func samePreviewItems(stored []storedPreviewItem, current []masterimport.PreviewItem) bool {
	if len(stored) != len(current) {
		return false
	}
	for i := range stored {
		a, b := stored[i], current[i]
		if a.SourceRow != b.SourceRow || a.EntityKind != b.EntityKind || a.EntityCode != b.EntityCode || a.Fingerprint != b.Fingerprint || a.ExistingFingerprint != b.ExistingFingerprint || a.Status != b.Status || a.ReasonCode != b.ReasonCode {
			return false
		}
	}
	return true
}

func loadApplyByKey(ctx context.Context, tx pgx.Tx, key string) (masterimport.ApplyResult, bool, error) {
	var payload []byte
	var attemptID string
	err := tx.QueryRow(ctx, `SELECT attempt_id::text,result FROM master_import_apply_runs WHERE idempotency_key=$1`, key).Scan(&attemptID, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return masterimport.ApplyResult{}, false, nil
	}
	if err != nil {
		return masterimport.ApplyResult{}, false, err
	}
	var out masterimport.ApplyResult
	if err := json.Unmarshal(payload, &out); err != nil {
		return out, false, err
	}
	out.AttemptID = attemptID
	return out, true, nil
}

func availableProducts(parsed masterimport.ParsedWorkbook) map[string]bool {
	out := make(map[string]bool)
	for _, branch := range parsed.Branches {
		for code, component := range branch.Tariffs {
			if component.Available {
				out[code] = true
			}
		}
	}
	return out
}

func revokeBranchAccess(ctx context.Context, tx pgx.Tx, branchID string, clearName bool) error {
	if _, err := tx.Exec(ctx, `UPDATE branch_users SET is_active=false,password_hash='!identity-revoked!',updated_at=now() WHERE branch_id=$1`, branchID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE registration_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE branch_id=$1 AND used_at IS NULL`, branchID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE password_reset_tokens p SET revoked_at=COALESCE(p.revoked_at,now()) FROM branch_users u WHERE p.actor_kind='branch' AND p.user_id=u.id AND u.branch_id=$1 AND p.used_at IS NULL`, branchID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE branch_id=$1`, branchID); err != nil {
		return err
	}
	if clearName {
		_, err := tx.Exec(ctx, `UPDATE branches SET nama_kepala=NULL,identity_status='identity_incomplete',updated_at=now() WHERE id=$1`, branchID)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE branches SET identity_status='identity_incomplete',updated_at=now() WHERE id=$1`, branchID)
	return err
}

func insertMutationAudit(ctx context.Context, tx pgx.Tx, actor auth.Actor, entity, entityID, action, fingerprint, runID string) error {
	return auditpg.Insert(ctx, tx, audit.NewEntry(ctx, actor, entity, entityID, action, nil, map[string]any{
		"apply_run_id": runID, "fingerprint": fingerprint,
	}))
}

func insertIdentityRevocationRollbackAudit(ctx context.Context, tx pgx.Tx, actor auth.Actor, applyRunID string, resolutions []identityRevocationAuditResolution) error {
	entityIDs := make([]string, 0, len(resolutions))
	for _, resolution := range resolutions {
		entityIDs = append(entityIDs, resolution.EntityID)
	}
	return auditpg.Insert(ctx, tx, audit.NewEntry(ctx, actor, "master_import", applyRunID, "identity_revocation_not_reversed", nil, map[string]any{
		"apply_run_id":        applyRunID,
		"source_apply_run_id": applyRunID,
		"entity_ids":          entityIDs,
		"resolutions":         resolutions,
		"reason":              "automatic_rollback_does_not_restore_revoked_identity_access",
	}))
}

func fingerprintJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func itemKey(row int, kind masterimport.EntityKind, code string) string {
	return fmt.Sprintf("%d\x00%s\x00%s", row, kind, code)
}

func findStoredItem(items []storedPreviewItem, row int, kind masterimport.EntityKind, code string) storedPreviewItem {
	for _, item := range items {
		if item.SourceRow == row && item.EntityKind == kind && item.EntityCode == code {
			return item
		}
	}
	return storedPreviewItem{SourceRow: row, EntityKind: kind, EntityCode: code}
}

func tariffColumns(code string) string {
	columns := map[string]string{"setoran-klasikal-lengkap": "E:G", "iq12": "H:J", "ppm": "K:M", "idv": "N:P", "mukpb": "Q:S", "au": "T:V", "mgrplus": "W:Y", "csr": "Z:AB", "expres-klasikal": "AC:AE", "expres-idv": "AF:AH", "s-expres": "AI:AK", "ptsd": "AL:AN", "klinis": "AO:AQ"}
	return columns[code]
}

func ratioNumeric(value string) string {
	r := new(big.Rat)
	if _, ok := r.SetString(value); !ok {
		return value
	}
	return tarif.Desimal(r)
}

func verifyBranchAfter(ctx context.Context, tx pgx.Tx, id, expected string) error {
	var state branchChangeState
	if err := tx.QueryRow(ctx, `SELECT nama,COALESCE(jabatan_kepala,''),nama_kepala,identity_status::text FROM branches WHERE id=$1 FOR UPDATE`, id).Scan(&state.Name, &state.Position, &state.HeadName, &state.IdentityStatus); err != nil {
		return masterimport.ErrRollbackConflict
	}
	got, _ := fingerprintJSON(state)
	if got != expected {
		return masterimport.ErrRollbackConflict
	}
	return nil
}
func verifyProductAfter(ctx context.Context, tx pgx.Tx, id, expected string) error {
	var state productChangeState
	if err := tx.QueryRow(ctx, `SELECT COALESCE(kode,''),nama,is_active FROM products WHERE id=$1 FOR UPDATE`, id).Scan(&state.Code, &state.Name, &state.Active); err != nil {
		return masterimport.ErrRollbackConflict
	}
	got, _ := fingerprintJSON(state)
	if got != expected {
		return masterimport.ErrRollbackConflict
	}
	return nil
}
func verifyVariantAfter(ctx context.Context, tx pgx.Tx, id, expected string) error {
	var parent *string
	var state variantChangeState
	if err := tx.QueryRow(ctx, `SELECT code,product_id::text,tariff_parent_variant_id::text,nama,is_active FROM product_variants WHERE id=$1 FOR UPDATE`, id).Scan(&state.Code, &state.ProductID, &parent, &state.Name, &state.Active); err != nil {
		return masterimport.ErrRollbackConflict
	}
	if parent != nil {
		state.ParentID = *parent
	}
	got, _ := fingerprintJSON(state)
	if got != expected {
		return masterimport.ErrRollbackConflict
	}
	return nil
}

func snapshotAAD(applyRunID string, sequence int) string {
	return fmt.Sprintf("master-impact:%s:%d", applyRunID, sequence)
}

// Versi hasil edit manual matriks harga tidak punya attempt/sel sumber; kolom
// itu dibaca sebagai nilai kosong agar reconcile dan rollback impor tetap bisa
// membandingkan dan menolak (conflict) versi tersebut.
func loadActiveTariffVersion(ctx context.Context, tx pgx.Tx, branchID, variantID string) (tariffVersionState, error) {
	return loadTariffVersion(ctx, tx, `SELECT id::text,branch_id::text,variant_id::text,hjm_rupiah,p1_ratio::text,spv_rupiah,final_item_rupiah,
		is_available,COALESCE(source_import_attempt_id::text,''),COALESCE(source_sheet,''),COALESCE(source_row,0),COALESCE(source_columns,''),source_component_hash,valid_from,superseded_at
		FROM branch_tariff_versions WHERE branch_id=$1 AND variant_id=$2 AND superseded_at IS NULL FOR UPDATE`, branchID, variantID)
}

func loadTariffVersionByID(ctx context.Context, tx pgx.Tx, id string) (tariffVersionState, error) {
	return loadTariffVersion(ctx, tx, `SELECT id::text,branch_id::text,variant_id::text,hjm_rupiah,p1_ratio::text,spv_rupiah,final_item_rupiah,
		is_available,COALESCE(source_import_attempt_id::text,''),COALESCE(source_sheet,''),COALESCE(source_row,0),COALESCE(source_columns,''),source_component_hash,valid_from,superseded_at
		FROM branch_tariff_versions WHERE id=$1 FOR UPDATE`, id)
}

func loadTariffVersion(ctx context.Context, tx pgx.Tx, query string, args ...any) (tariffVersionState, error) {
	var state tariffVersionState
	err := tx.QueryRow(ctx, query, args...).Scan(&state.ID, &state.BranchID, &state.VariantID, &state.HJM, &state.P1, &state.SPV, &state.FinalItem,
		&state.Available, &state.AttemptID, &state.SourceSheet, &state.SourceRow, &state.SourceColumns, &state.ComponentHash, &state.ValidFrom, &state.SupersededAt)
	return state, err
}

func loadPriceProjection(ctx context.Context, tx pgx.Tx, branchID, productID string) (priceProjectionState, error) {
	state := priceProjectionState{BranchID: branchID, ProductID: productID}
	err := tx.QueryRow(ctx, `SELECT harga,updated_by::text,updated_at FROM branch_prices WHERE branch_id=$1 AND product_id=$2 FOR UPDATE`, branchID, productID).
		Scan(&state.Price, &state.UpdatedBy, &state.UpdatedAt)
	if err == nil {
		state.Present = true
	}
	return state, err
}

func restorePriceProjection(ctx context.Context, tx pgx.Tx, state priceProjectionState) error {
	if !state.Present {
		_, err := tx.Exec(ctx, `DELETE FROM branch_prices WHERE branch_id=$1 AND product_id=$2`, state.BranchID, state.ProductID)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO branch_prices (branch_id,product_id,harga,updated_by,updated_at)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (branch_id,product_id) DO UPDATE
		SET harga=EXCLUDED.harga,updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at`,
		state.BranchID, state.ProductID, state.Price, state.UpdatedBy, state.UpdatedAt)
	return err
}

func verifyTariffChangeAfter(ctx context.Context, tx pgx.Tx, meta tariffChangeMeta, expected string) error {
	newVersion, err := loadTariffVersionByID(ctx, tx, meta.NewVersion.ID)
	if err != nil {
		return masterimport.ErrRollbackConflict
	}
	projection, projectionErr := loadPriceProjection(ctx, tx, meta.BranchID, meta.ProductID)
	if errors.Is(projectionErr, pgx.ErrNoRows) {
		projection = priceProjectionState{BranchID: meta.BranchID, ProductID: meta.ProductID}
	} else if projectionErr != nil {
		return masterimport.ErrRollbackConflict
	}
	after := struct {
		Version    tariffVersionState   `json:"version"`
		Projection priceProjectionState `json:"projection"`
	}{newVersion, projection}
	got, _ := fingerprintJSON(after)
	if got != expected {
		return masterimport.ErrRollbackConflict
	}
	if meta.OldVersionAfter != nil {
		oldVersion, oldErr := loadTariffVersionByID(ctx, tx, meta.OldVersionAfter.ID)
		if oldErr != nil {
			return masterimport.ErrRollbackConflict
		}
		gotOld, _ := fingerprintJSON(oldVersion)
		wantOld, _ := fingerprintJSON(*meta.OldVersionAfter)
		if gotOld != wantOld {
			return masterimport.ErrRollbackConflict
		}
	}
	return nil
}
