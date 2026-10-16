package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/masterimport"
	"github.com/grahita/portal/api/internal/platform/database"
)

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type Repo struct {
	pool           *database.Pool
	snapshotCipher *snapshotCipher
}

func New(pool *database.Pool, snapshotKey string) *Repo {
	return &Repo{pool: pool, snapshotCipher: newSnapshotCipher(snapshotKey)}
}

func (r *Repo) Snapshot(ctx context.Context) (masterimport.Snapshot, error) {
	return snapshotFrom(ctx, r.pool)
}

func snapshotFrom(ctx context.Context, q querier) (masterimport.Snapshot, error) {
	out := masterimport.Snapshot{
		Branches: map[string]masterimport.BranchState{}, BranchCandidates: map[string][]masterimport.BranchState{},
		Products: map[string]masterimport.ProductState{}, Variants: map[string]masterimport.VariantState{},
		Tariffs: map[string]masterimport.TariffState{},
	}
	rows, err := q.Query(ctx, `
SELECT b.id::text, b.nama, COALESCE(b.jabatan_kepala, ''), b.nama_kepala,
       b.email IS NOT NULL, b.identity_status::text, m.source_key,
       (b.nama_kepala IS NOT NULL AND btrim(b.nama_kepala) <> '' AND b.email IS NOT NULL AND btrim(b.email::text) <> ''),
       access.active_users > 0 OR access.registration_tokens > 0 OR access.sessions > 0 OR access.reset_tokens > 0,
       CASE b.identity_status::text
         WHEN 'identity_incomplete' THEN access.active_users = 0 AND access.registration_tokens = 0 AND access.sessions = 0 AND access.reset_tokens = 0
         WHEN 'ready' THEN access.active_users = 0 AND access.sessions = 0 AND access.reset_tokens = 0
              AND b.nama_kepala IS NOT NULL AND btrim(b.nama_kepala) <> '' AND b.email IS NOT NULL AND btrim(b.email::text) <> ''
         WHEN 'registered' THEN access.active_users = 1 AND access.registration_tokens = 0
              AND b.nama_kepala IS NOT NULL AND btrim(b.nama_kepala) <> '' AND b.email IS NOT NULL AND btrim(b.email::text) <> ''
         ELSE false
       END,
       encode(digest(jsonb_build_object(
         'branch', jsonb_build_array(b.id, b.nama, b.jabatan_kepala, b.nama_kepala, b.email, b.identity_status, b.updated_at),
         'users', COALESCE((SELECT jsonb_agg(jsonb_build_array(u.id,u.email,u.password_hash,u.is_active,u.updated_at) ORDER BY u.id) FROM branch_users u WHERE u.branch_id=b.id), '[]'::jsonb),
         'registration_tokens', COALESCE((SELECT jsonb_agg(jsonb_build_array(t.id,encode(t.token_hash,'hex'),t.expires_at,t.used_at,t.revoked_at) ORDER BY t.id) FROM registration_tokens t WHERE t.branch_id=b.id), '[]'::jsonb),
         'sessions', COALESCE((SELECT jsonb_agg(jsonb_build_array(s.id,encode(s.token_hash,'hex'),s.expires_at,s.revoked_at,s.last_seen_at) ORDER BY s.id) FROM sessions s WHERE s.branch_id=b.id), '[]'::jsonb),
         'reset_tokens', COALESCE((SELECT jsonb_agg(jsonb_build_array(p.id,encode(p.token_hash,'hex'),p.expires_at,p.used_at,p.revoked_at) ORDER BY p.id) FROM password_reset_tokens p JOIN branch_users u ON u.id=p.user_id WHERE p.actor_kind='branch' AND u.branch_id=b.id), '[]'::jsonb)
       )::text, 'sha256'), 'hex')
  FROM branches b
  LEFT JOIN master_import_identities m ON m.source_system='impact' AND m.entity_kind='branch' AND m.entity_id=b.id
  CROSS JOIN LATERAL (
    SELECT
      (SELECT count(*) FROM branch_users u WHERE u.branch_id=b.id AND u.is_active) AS active_users,
      (SELECT count(*) FROM registration_tokens t WHERE t.branch_id=b.id AND t.used_at IS NULL AND t.revoked_at IS NULL AND t.expires_at > now()) AS registration_tokens,
      (SELECT count(*) FROM sessions s WHERE s.branch_id=b.id AND s.revoked_at IS NULL AND s.expires_at > now()) AS sessions,
      (SELECT count(*) FROM password_reset_tokens p JOIN branch_users u ON u.id=p.user_id WHERE p.actor_kind='branch' AND u.branch_id=b.id AND p.used_at IS NULL AND p.revoked_at IS NULL AND p.expires_at > now()) AS reset_tokens
  ) access`)
	if err != nil {
		return out, fmt.Errorf("query branches: %w", err)
	}
	for rows.Next() {
		var state masterimport.BranchState
		var head, mapped *string
		if err := rows.Scan(&state.ID, &state.Name, &state.Position, &head, &state.EmailPresent,
			&state.IdentityStatus, &mapped, &state.IdentityComplete, &state.AccessUsable,
			&state.AccessConsistent, &state.Fingerprint); err != nil {
			rows.Close()
			return out, fmt.Errorf("scan branch: %w", err)
		}
		state.HeadName = head
		candidateKey := "branch:" + normalizeKey(state.Name)
		out.BranchCandidates[candidateKey] = append(out.BranchCandidates[candidateKey], state)
		if mapped == nil {
			continue
		}
		state.SourceKey = *mapped
		if existing, duplicate := out.Branches[state.SourceKey]; duplicate {
			existing.Ambiguous = true
			out.Branches[state.SourceKey] = existing
			continue
		}
		out.Branches[state.SourceKey] = state
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, fmt.Errorf("iterasi branches: %w", err)
	}
	rows.Close()
	for key, candidates := range out.BranchCandidates {
		mapped, ok := out.Branches[key]
		if !ok {
			continue
		}
		for _, candidate := range candidates {
			if candidate.ID != mapped.ID {
				mapped.Ambiguous = true
				out.Branches[key] = mapped
				break
			}
		}
	}

	rows, err = q.Query(ctx, `SELECT id::text, COALESCE(kode,''), nama, is_active, updated_at FROM products WHERE kode IS NOT NULL`)
	if err != nil {
		return out, fmt.Errorf("query products: %w", err)
	}
	for rows.Next() {
		var p masterimport.ProductState
		var updated time.Time
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Active, &updated); err != nil {
			rows.Close()
			return out, fmt.Errorf("scan product: %w", err)
		}
		p.Fingerprint = stateFingerprint(p.ID, p.Code, p.Name, strconv.FormatBool(p.Active), updated.UTC().Format(time.RFC3339Nano))
		out.Products[p.Code] = p
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, fmt.Errorf("iterasi products: %w", err)
	}
	rows.Close()

	rows, err = q.Query(ctx, `
SELECT v.id::text, v.code, p.kode, COALESCE(parent.code,''), v.is_active, v.updated_at
  FROM product_variants v
  JOIN products p ON p.id=v.product_id
  LEFT JOIN product_variants parent ON parent.id=v.tariff_parent_variant_id`)
	if err != nil {
		return out, fmt.Errorf("query variants: %w", err)
	}
	for rows.Next() {
		var v masterimport.VariantState
		var updated time.Time
		if err := rows.Scan(&v.ID, &v.Code, &v.ProductCode, &v.ParentCode, &v.Active, &updated); err != nil {
			rows.Close()
			return out, fmt.Errorf("scan variant: %w", err)
		}
		v.Fingerprint = stateFingerprint(v.ID, v.Code, v.ProductCode, v.ParentCode, strconv.FormatBool(v.Active), updated.UTC().Format(time.RFC3339Nano))
		out.Variants[v.Code] = v
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, fmt.Errorf("iterasi variants: %w", err)
	}
	rows.Close()

	rows, err = q.Query(ctx, `
SELECT t.branch_id::text, v.code, t.is_available, t.source_component_hash, t.id::text,
       t.valid_from, t.superseded_at, t.hjm_rupiah::text, t.p1_ratio::text, t.spv_rupiah::text, t.final_item_rupiah::text
  FROM branch_tariff_versions t
  JOIN product_variants v ON v.id=t.variant_id
 WHERE t.superseded_at IS NULL`)
	if err != nil {
		return out, fmt.Errorf("query tariffs: %w", err)
	}
	for rows.Next() {
		var t masterimport.TariffState
		var validFrom time.Time
		var superseded *time.Time
		var hjm, p1, spv, final *string
		if err := rows.Scan(&t.BranchID, &t.VariantCode, &t.Available, &t.ComponentHash, &t.VersionID,
			&validFrom, &superseded, &hjm, &p1, &spv, &final); err != nil {
			rows.Close()
			return out, fmt.Errorf("scan tariff: %w", err)
		}
		t.Fingerprint = stateFingerprint(t.BranchID, t.VariantCode, strconv.FormatBool(t.Available), t.ComponentHash,
			t.VersionID, validFrom.UTC().Format(time.RFC3339Nano), optionalTime(superseded), optional(hjm), optional(p1), optional(spv), optional(final))
		out.Tariffs[t.BranchID+"\x00"+t.VariantCode] = t
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, fmt.Errorf("iterasi tariffs: %w", err)
	}
	rows.Close()

	rows, err = q.Query(ctx, `
SELECT bp.branch_id::text, p.kode, bp.harga, bp.updated_by::text, bp.updated_at
  FROM branch_prices bp
  JOIN products p ON p.id=bp.product_id
 WHERE p.kode IS NOT NULL`)
	if err != nil {
		return out, fmt.Errorf("query projection tariff: %w", err)
	}
	for rows.Next() {
		var branchID, code string
		var price int64
		var updatedBy *string
		var updatedAt time.Time
		if err := rows.Scan(&branchID, &code, &price, &updatedBy, &updatedAt); err != nil {
			rows.Close()
			return out, fmt.Errorf("scan projection tariff: %w", err)
		}
		key := branchID + "\x00" + code
		state := out.Tariffs[key]
		state.BranchID = branchID
		state.VariantCode = code
		state.ProjectionPresent = true
		state.ProjectionPrice = price
		if state.VersionID == "" {
			// Projection legacy tanpa versi tetap merupakan state harga aktif yang
			// harus ikut preview, apply, dan rollback.
			state.Available = true
			state.Fingerprint = stateFingerprint("no-version", branchID, code)
		}
		state.Fingerprint = stateFingerprint(state.Fingerprint, "projection", strconv.FormatInt(price, 10), optional(updatedBy), updatedAt.UTC().Format(time.RFC3339Nano))
		out.Tariffs[key] = state
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, fmt.Errorf("iterasi projection tariff: %w", err)
	}
	rows.Close()
	for key, state := range out.Tariffs {
		if !state.ProjectionPresent {
			state.Fingerprint = stateFingerprint(state.Fingerprint, "projection-absent")
			out.Tariffs[key] = state
		}
	}
	return out, nil
}

func (r *Repo) SavePreview(ctx context.Context, in masterimport.SavePreviewInput) (masterimport.SavedPreview, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return masterimport.SavedPreview{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var out masterimport.SavedPreview
	err = tx.QueryRow(ctx, `
INSERT INTO master_import_artifacts (environment, source_system, source_hash, contract_version, source_sheet)
VALUES ($1, 'impact', $2, $3, $4)
ON CONFLICT (environment, source_hash, contract_version)
DO UPDATE SET source_sheet=EXCLUDED.source_sheet
RETURNING id::text`, in.Environment, in.SourceHash, masterimport.ContractVersion, masterimport.SheetName).Scan(&out.ArtifactID)
	if err != nil {
		return out, fmt.Errorf("upsert artifact: %w", err)
	}
	counts, err := json.Marshal(in.Counts)
	if err != nil {
		return out, err
	}
	normalizedSource, err := json.Marshal(in.Parsed)
	if err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `
INSERT INTO master_import_attempts (artifact_id, actor_id, preview_hash, counts, normalized_source)
VALUES ($1, $2, $3, $4, $5)
RETURNING id::text, created_at`, out.ArtifactID, in.ActorID, in.PreviewHash, counts, normalizedSource).Scan(&out.AttemptID, &out.CreatedAt)
	if err != nil {
		return out, fmt.Errorf("insert attempt: %w", err)
	}
	batch := &pgx.Batch{}
	for _, item := range in.Items {
		batch.Queue(`INSERT INTO master_import_preview_items
(attempt_id, source_row, entity_kind, entity_code, fingerprint, existing_fingerprint, preview_status, reason_code, private_evidence)
VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,NULLIF($8,''),NULLIF($9,''))`, out.AttemptID, item.SourceRow, item.EntityKind,
			item.EntityCode, item.Fingerprint, item.ExistingFingerprint, item.Status, item.ReasonCode, item.PrivateEvidence)
	}
	results := tx.SendBatch(ctx, batch)
	for range in.Items {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return out, fmt.Errorf("insert preview item: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return out, fmt.Errorf("close preview batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return out, err
	}
	return out, nil
}

func (r *Repo) LoadEvidence(ctx context.Context, attemptID string) ([]masterimport.EvidenceItem, error) {
	rows, err := r.pool.Query(ctx, `
SELECT source_row, entity_kind, entity_code, private_evidence
  FROM master_import_preview_items
 WHERE attempt_id=$1 AND private_evidence IS NOT NULL
 ORDER BY source_row, entity_kind, entity_code`, attemptID)
	if err != nil {
		return nil, fmt.Errorf("query private evidence: %w", err)
	}
	defer rows.Close()
	var out []masterimport.EvidenceItem
	for rows.Next() {
		var item masterimport.EvidenceItem
		if err := rows.Scan(&item.SourceRow, &item.EntityKind, &item.EntityCode, &item.Value); err != nil {
			return nil, fmt.Errorf("scan private evidence: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate private evidence: %w", err)
	}
	return out, nil
}

func stateFingerprint(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func optional(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func normalizeKey(v string) string {
	return masterimport.NormalizeSourceName(v)
}
