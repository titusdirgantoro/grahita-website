// Package postgres adalah implementasi repository rekening bank di PostgreSQL.
//
// Setiap metode yang mengubah data menuliskan entri audit di dalam transaksi
// yang sama, sehingga perubahan dan catatannya selalu sejalan
// (SystemDesign.md §5.4).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
)

// Repo mengimplementasikan bank.Repository di atas PostgreSQL.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository rekening bank.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// List memuat seluruh rekening (aktif maupun nonaktif).
func (r *Repo) List(ctx context.Context) ([]bank.Account, error) {
	rows, err := r.q.ListBankAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListBankAccounts: %w", err)
	}
	out := make([]bank.Account, 0, len(rows))
	for _, row := range rows {
		out = append(out, bank.Account{
			ID: row.ID, Bank: row.Bank, NamaPemilik: row.NamaPemilik,
			Nomor: row.Nomor, IsActive: row.IsActive,
		})
	}
	return out, nil
}

// ListActive memuat hanya rekening aktif (PRD §4.6).
func (r *Repo) ListActive(ctx context.Context) ([]bank.Account, error) {
	rows, err := r.q.ListActiveBankAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListActiveBankAccounts: %w", err)
	}
	out := make([]bank.Account, 0, len(rows))
	for _, row := range rows {
		out = append(out, bank.Account{
			ID: row.ID, Bank: row.Bank, NamaPemilik: row.NamaPemilik,
			Nomor: row.Nomor, IsActive: row.IsActive,
		})
	}
	return out, nil
}

// Get memuat satu rekening.
func (r *Repo) Get(ctx context.Context, id string) (bank.Account, error) {
	row, err := r.q.GetBankAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return bank.Account{}, bank.ErrNotFound
	}
	if err != nil {
		return bank.Account{}, fmt.Errorf("query GetBankAccount: %w", err)
	}
	return bank.Account{
		ID: row.ID, Bank: row.Bank, NamaPemilik: row.NamaPemilik,
		Nomor: row.Nomor, IsActive: row.IsActive,
	}, nil
}

// Create menyimpan rekening baru beserta catatan auditnya.
func (r *Repo) Create(ctx context.Context, in bank.Account, rec audit.Entry) (bank.Account, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return bank.Account{}, fmt.Errorf("mulai transaksi buat rekening: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := r.q.WithTx(tx).CreateBankAccount(ctx, sqlc.CreateBankAccountParams{
		Bank:        sqlc.BankCode(in.Bank),
		NamaPemilik: in.NamaPemilik,
		Nomor:       in.Nomor,
		IsActive:    in.IsActive,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return bank.Account{}, bank.ErrDuplicate
		}
		return bank.Account{}, fmt.Errorf("query CreateBankAccount: %w", err)
	}
	saved := bank.Account{
		ID: row.ID, Bank: row.Bank, NamaPemilik: row.NamaPemilik,
		Nomor: row.Nomor, IsActive: row.IsActive,
	}
	rec.EntityID = saved.ID
	// Snapshot umum hanya memuat nomor masked, bukan nomor rekening lengkap.
	rec.After = map[string]any{
		"id": saved.ID, "bank": saved.Bank, "nama_pemilik": saved.NamaPemilik,
		"display_nomor": saved.DisplayNomor(), "is_active": saved.IsActive,
		"affected_branch_count": 0,
	}
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return bank.Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return bank.Account{}, fmt.Errorf("commit buat rekening: %w", err)
	}
	return saved, nil
}

// Update menyimpan perubahan penuh satu rekening beserta catatan auditnya.
func (r *Repo) Update(ctx context.Context, next bank.Account, rec audit.Entry) (bank.Account, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return bank.Account{}, fmt.Errorf("mulai transaksi ubah rekening: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(22102, 2)`); err != nil {
		return bank.Account{}, fmt.Errorf("kunci kebijakan rekening: %w", err)
	}
	var currentActive bool
	if err := tx.QueryRow(ctx, `SELECT is_active FROM bank_accounts WHERE id = $1 FOR UPDATE`, next.ID).Scan(&currentActive); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bank.Account{}, bank.ErrNotFound
		}
		return bank.Account{}, fmt.Errorf("baca rekening saat ini: %w", err)
	}
	statusChanged := currentActive != next.IsActive
	affectedBranches := []string{}
	if statusChanged {
		rows, err := tx.Query(ctx, `SELECT b.id FROM branches b
 JOIN branch_bank_accounts x ON x.branch_id=b.id
 WHERE x.bank_account_id=$1 AND x.is_allowed ORDER BY b.id FOR UPDATE`, next.ID)
		if err != nil {
			return bank.Account{}, fmt.Errorf("kunci cabang terdampak: %w", err)
		}
		for rows.Next() {
			var branchID string
			if err := rows.Scan(&branchID); err != nil {
				rows.Close()
				return bank.Account{}, err
			}
			affectedBranches = append(affectedBranches, branchID)
		}
		rows.Close()
	}

	row, err := r.q.WithTx(tx).UpdateBankAccount(ctx, sqlc.UpdateBankAccountParams{
		Bank:        sqlc.BankCode(next.Bank),
		NamaPemilik: next.NamaPemilik,
		Nomor:       next.Nomor,
		IsActive:    next.IsActive,
		ID:          next.ID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return bank.Account{}, bank.ErrDuplicate
		}
		return bank.Account{}, fmt.Errorf("query UpdateBankAccount: %w", err)
	}
	if statusChanged {
		if _, err := tx.Exec(ctx, `UPDATE branches b SET bank_allowlist_version=bank_allowlist_version+1
 WHERE EXISTS (SELECT 1 FROM branch_bank_accounts x
               WHERE x.branch_id=b.id AND x.bank_account_id=$1 AND x.is_allowed)`, next.ID); err != nil {
			return bank.Account{}, fmt.Errorf("naikkan versi cabang terdampak: %w", err)
		}
	}
	saved := bank.Account{
		ID: row.ID, Bank: row.Bank, NamaPemilik: row.NamaPemilik,
		Nomor: row.Nomor, IsActive: row.IsActive,
	}
	// Tambahkan dampak cabang tanpa pernah menyalin nomor lengkap ke audit.
	if before, ok := rec.Before.(map[string]any); ok {
		before["affected_branch_count"] = len(affectedBranches)
	}
	if after, ok := rec.After.(map[string]any); ok {
		after["affected_branch_count"] = len(affectedBranches)
	}
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return bank.Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return bank.Account{}, fmt.Errorf("commit ubah rekening: %w", err)
	}
	return saved, nil
}

func (r *Repo) EffectiveForBranch(ctx context.Context, branchID string) (bank.EffectiveSet, error) {
	var version int64
	var active bool
	if err := r.pool.QueryRow(ctx, `SELECT bank_allowlist_version, is_active FROM branches WHERE id=$1`, branchID).Scan(&version, &active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bank.EffectiveSet{}, bank.ErrBranchNotFound
		}
		return bank.EffectiveSet{}, fmt.Errorf("baca versi allowlist: %w", err)
	}
	out := bank.EffectiveSet{Accounts: []bank.Account{}, Version: version}
	if !active {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT ba.id, ba.bank::text, ba.nama_pemilik, ba.nomor, ba.is_active
 FROM branch_bank_accounts bba JOIN bank_accounts ba ON ba.id=bba.bank_account_id
 WHERE bba.branch_id=$1 AND bba.is_allowed AND ba.is_active
 ORDER BY ba.bank, ba.id`, branchID)
	if err != nil {
		return bank.EffectiveSet{}, fmt.Errorf("baca rekening efektif: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a bank.Account
		if err := rows.Scan(&a.ID, &a.Bank, &a.NamaPemilik, &a.Nomor, &a.IsActive); err != nil {
			return bank.EffectiveSet{}, err
		}
		out.Accounts = append(out.Accounts, a)
	}
	return out, rows.Err()
}

func (r *Repo) ListAssignments(ctx context.Context, branchID string) (bank.AssignmentList, error) {
	var version int64
	var branchActive bool
	if err := r.pool.QueryRow(ctx, `SELECT bank_allowlist_version, is_active FROM branches WHERE id=$1`, branchID).Scan(&version, &branchActive); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bank.AssignmentList{}, bank.ErrBranchNotFound
		}
		return bank.AssignmentList{}, err
	}
	rows, err := r.pool.Query(ctx, `SELECT ba.id, ba.bank::text, ba.nama_pemilik, ba.nomor, ba.is_active,
 COALESCE(bba.is_allowed,false)
 FROM bank_accounts ba LEFT JOIN branch_bank_accounts bba
   ON bba.bank_account_id=ba.id AND bba.branch_id=$1
 ORDER BY ba.bank, ba.id`, branchID)
	if err != nil {
		return bank.AssignmentList{}, err
	}
	defer rows.Close()
	out := bank.AssignmentList{BranchID: branchID, Version: version, Items: []bank.AssignmentItem{}}
	for rows.Next() {
		var a bank.Account
		var allowed bool
		if err := rows.Scan(&a.ID, &a.Bank, &a.NamaPemilik, &a.Nomor, &a.IsActive, &allowed); err != nil {
			return bank.AssignmentList{}, err
		}
		out.Items = append(out.Items, bank.AssignmentItem{ID: a.ID, Bank: a.Bank, NamaPemilik: a.NamaPemilik,
			DisplayNomor: a.DisplayNomor(), IsActive: a.IsActive, IsAllowed: allowed,
			Dormant: allowed && (!a.IsActive || !branchActive), Effective: allowed && a.IsActive && branchActive})
	}
	return out, rows.Err()
}

func (r *Repo) ReplaceAssignments(ctx context.Context, branchID string, in bank.ReplaceInput, actor auth.Actor) (bank.ReplaceResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return bank.ReplaceResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(22102, 2)`); err != nil {
		return bank.ReplaceResult{}, err
	}
	rows, err := tx.Query(ctx, `SELECT bank_account_id FROM branch_bank_accounts WHERE branch_id=$1 AND is_allowed`, branchID)
	if err != nil {
		return bank.ReplaceResult{}, err
	}
	current := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return bank.ReplaceResult{}, err
		}
		current[id] = true
	}
	rows.Close()
	lockIDs := append([]string(nil), in.BankAccountIDs...)
	for id := range current {
		if !contains(lockIDs, id) {
			lockIDs = append(lockIDs, id)
		}
	}
	sort.Strings(lockIDs)
	accountActive := map[string]bool{}
	for _, id := range lockIDs {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT is_active FROM bank_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&active); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return bank.ReplaceResult{}, bank.ErrNotFound
			}
			return bank.ReplaceResult{}, err
		}
		accountActive[id] = active
	}
	var version int64
	var branchActive bool
	if err := tx.QueryRow(ctx, `SELECT bank_allowlist_version,is_active FROM branches WHERE id=$1 FOR UPDATE`, branchID).Scan(&version, &branchActive); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bank.ReplaceResult{}, bank.ErrBranchNotFound
		}
		return bank.ReplaceResult{}, err
	}
	if version != in.ExpectedVersion {
		return bank.ReplaceResult{}, bank.ErrVersionConflict
	}
	for _, id := range in.BankAccountIDs {
		if !accountActive[id] && !current[id] {
			return bank.ReplaceResult{}, bank.ErrAccountInactive
		}
	}
	desired := map[string]bool{}
	for _, id := range in.BankAccountIDs {
		desired[id] = true
	}
	changed := len(desired) != len(current)
	if !changed {
		for id := range desired {
			if !current[id] {
				changed = true
				break
			}
		}
	}
	if changed {
		for _, id := range in.BankAccountIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO branch_bank_accounts
 (branch_id,bank_account_id,is_allowed,assigned_by,assigned_at,revoked_by,revoked_at,updated_at)
 VALUES ($1,$2,true,$3,now(),NULL,NULL,now())
 ON CONFLICT (branch_id,bank_account_id) DO UPDATE SET is_allowed=true,assigned_by=EXCLUDED.assigned_by,
 assigned_at=now(),revoked_by=NULL,revoked_at=NULL,updated_at=now()`, branchID, id, actor.UserID); err != nil {
				return bank.ReplaceResult{}, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE branch_bank_accounts
 SET is_allowed=false,revoked_by=$2,revoked_at=now(),updated_at=now()
 WHERE branch_id=$1 AND is_allowed AND NOT (bank_account_id = ANY($3::uuid[]))`, branchID, actor.UserID, in.BankAccountIDs); err != nil {
			return bank.ReplaceResult{}, err
		}
		version++
		if _, err := tx.Exec(ctx, `UPDATE branches SET bank_allowlist_version=$2 WHERE id=$1`, branchID, version); err != nil {
			return bank.ReplaceResult{}, err
		}
		rec := audit.NewEntry(ctx, actor, "branch_bank_allowlist", branchID, "replace",
			map[string]any{"version": in.ExpectedVersion, "bank_account_ids": sortedMapKeys(current)},
			map[string]any{"version": version, "bank_account_ids": in.BankAccountIDs})
		if err := auditpg.Insert(ctx, tx, rec); err != nil {
			return bank.ReplaceResult{}, err
		}
	}
	available := false
	for id := range desired {
		if branchActive && accountActive[id] {
			available = true
			break
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return bank.ReplaceResult{}, err
	}
	return bank.ReplaceResult{BranchID: branchID, Version: version, BankAccountIDs: in.BankAccountIDs,
		PaymentAvailable: available, Changed: changed}, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedMapKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for id := range values {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
