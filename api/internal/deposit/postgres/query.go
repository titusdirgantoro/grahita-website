package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/deposit"
)

// Sisi baca dan pencatatan top-up deposit (C2) di atas ledger C1.

func (r *Repo) Summary(ctx context.Context, branchID string) (deposit.Summary, error) {
	out := deposit.Summary{BranchID: branchID}
	err := r.pool.QueryRow(ctx, `SELECT COALESCE(a.balance,0)::bigint, COALESCE(a.journal_version,0)::bigint,
  (SELECT count(*) FROM deposit_topups t
    WHERE t.branch_id=b.id AND t.status IN ('pending_review','needs_review'))::int,
  (SELECT COALESCE(sum(t.declared_amount),0) FROM deposit_topups t
    WHERE t.branch_id=b.id AND t.status IN ('pending_review','needs_review'))::bigint
FROM branches b LEFT JOIN branch_deposit_accounts a ON a.branch_id=b.id
WHERE b.id=$1`, branchID).Scan(&out.AvailableBalance, &out.JournalVersion,
		&out.PendingTopupCount, &out.PendingTopupAmountDeclared)
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.Summary{}, deposit.ErrNotFound
	}
	if err != nil {
		return deposit.Summary{}, fmt.Errorf("baca ringkasan deposit: %w", err)
	}
	return out, nil
}

func (r *Repo) Transactions(ctx context.Context, branchID string, limit, offset int) ([]deposit.TransactionView, int, error) {
	rows, err := r.pool.Query(ctx, `SELECT t.id, t.kind::text, t.amount, t.order_id::text,
  COALESCE(o.nomor,''), v.topup_id::text, t.reverses_transaction_id::text,
  COALESCE(al.reversed_amount,0), t.created_at, count(*) OVER()
FROM branch_deposit_transactions t
LEFT JOIN orders o ON o.id=t.order_id
LEFT JOIN deposit_verification_logs v ON v.id=t.deposit_verification_log_id
LEFT JOIN deposit_allocations al ON al.debit_transaction_id=t.id
WHERE t.branch_id=$1
ORDER BY t.created_at DESC, t.id DESC LIMIT $2 OFFSET $3`, branchID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("daftar journal deposit: %w", err)
	}
	defer rows.Close()
	out := []deposit.TransactionView{}
	total := 0
	for rows.Next() {
		var v deposit.TransactionView
		var orderID, topupID, reverses *string
		if err := rows.Scan(&v.ID, &v.Kind, &v.Amount, &orderID, &v.OrderNomor, &topupID,
			&reverses, &v.ReversedAmount, &v.CreatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("baca journal deposit: %w", err)
		}
		v.OrderID, v.TopupID, v.ReversesTransactionID = deref(orderID), deref(topupID), deref(reverses)
		out = append(out, v)
	}
	return out, total, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

const topupSelect = `SELECT t.id, t.branch_id, b.nama, t.bank_account_id, ba.bank::text, ba.nomor,
  t.status::text, t.declared_amount, t.verified_amount, COALESCE(t.rejection_reason,''),
  p.id, p.nama_file, p.mime_type, p.ocr_nominal, t.created_at, t.verified_at, p.storage_key
FROM deposit_topups t
JOIN branches b ON b.id=t.branch_id
JOIN bank_accounts ba ON ba.id=t.bank_account_id
JOIN payment_proofs p ON p.id=t.payment_proof_id`

func scanTopup(row pgx.Row) (deposit.Topup, error) {
	var t deposit.Topup
	var nomor string
	err := row.Scan(&t.ID, &t.BranchID, &t.BranchNama, &t.BankAccountID, &t.Bank, &nomor,
		&t.Status, &t.DeclaredAmount, &t.VerifiedAmount, &t.RejectionReason,
		&t.ProofID, &t.ProofNamaFile, &t.ProofMimeType, &t.OCRNominal, &t.CreatedAt, &t.VerifiedAt, &t.ProofKey)
	if err != nil {
		return deposit.Topup{}, err
	}
	t.DisplayNomor = bank.Account{Nomor: nomor}.DisplayNomor()
	return t, nil
}

func (r *Repo) Topup(ctx context.Context, id string) (deposit.Topup, error) {
	t, err := scanTopup(r.pool.QueryRow(ctx, topupSelect+` WHERE t.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.Topup{}, deposit.ErrTopupNotFound
	}
	if err != nil {
		return deposit.Topup{}, fmt.Errorf("baca top-up deposit: %w", err)
	}
	return t, nil
}

func (r *Repo) Topups(ctx context.Context, f deposit.TopupFilter) ([]deposit.Topup, int, error) {
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM deposit_topups t
WHERE ($1::uuid IS NULL OR t.branch_id=$1) AND ($2::text = '' OR t.status::text=$2)`,
		nullableUUID(f.BranchID), f.Status).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("hitung top-up deposit: %w", err)
	}
	rows, err := r.pool.Query(ctx, topupSelect+`
WHERE ($1::uuid IS NULL OR t.branch_id=$1) AND ($2::text = '' OR t.status::text=$2)
ORDER BY t.created_at DESC, t.id DESC LIMIT $3 OFFSET $4`,
		nullableUUID(f.BranchID), f.Status, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("daftar top-up deposit: %w", err)
	}
	defer rows.Close()
	out := []deposit.Topup{}
	for rows.Next() {
		t, err := scanTopup(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("baca top-up deposit: %w", err)
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// CreateTopup mencatat bukti mandiri dan top-up pending_review dalam satu
// transaksi. Bukti identik (hash sama per cabang) untuk permintaan yang sama
// adalah replay idempoten; untuk permintaan berbeda ditolak.
func (r *Repo) CreateTopup(ctx context.Context, in deposit.CreateTopupInput) (deposit.Topup, bool, error) {
	sum, err := hex.DecodeString(in.Proof.Sha256Hex)
	if err != nil || len(sum) != 32 {
		return deposit.Topup{}, false, fmt.Errorf("sidik jari bukti tidak sah")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return deposit.Topup{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var found string
	if err := tx.QueryRow(ctx, `SELECT id FROM branches WHERE id=$1 AND is_active FOR SHARE`, in.BranchID).Scan(&found); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deposit.Topup{}, false, deposit.ErrForbidden
		}
		return deposit.Topup{}, false, fmt.Errorf("kunci cabang top-up: %w", err)
	}
	// Assignment D2 dibaca ulang di dalam transaksi supaya pencabutan yang
	// terjadi setelah pemeriksaan service tidak lolos.
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM branch_bank_accounts x
  JOIN bank_accounts ba ON ba.id=x.bank_account_id
  WHERE x.branch_id=$1 AND x.bank_account_id=$2 AND x.is_allowed AND ba.is_active)`,
		in.BranchID, in.BankAccountID).Scan(&allowed); err != nil {
		return deposit.Topup{}, false, fmt.Errorf("periksa rekening efektif: %w", err)
	}
	if !allowed {
		return deposit.Topup{}, false, deposit.ErrBankNotAllowed
	}

	var existingTopup *string
	var existingBank *string
	var existingDeclared *int64
	err = tx.QueryRow(ctx, `SELECT t.id::text, t.bank_account_id::text, t.declared_amount
FROM payment_proofs p LEFT JOIN deposit_topups t ON t.payment_proof_id=p.id
WHERE p.purpose='deposit_topup' AND p.branch_id=$1 AND p.sha256=$2`, in.BranchID, sum).
		Scan(&existingTopup, &existingBank, &existingDeclared)
	switch {
	case err == nil:
		if existingTopup != nil && deref(existingBank) == in.BankAccountID &&
			existingDeclared != nil && *existingDeclared == in.DeclaredAmount {
			t, err := scanTopup(tx.QueryRow(ctx, topupSelect+` WHERE t.id=$1`, *existingTopup))
			if err != nil {
				return deposit.Topup{}, false, err
			}
			return t, true, tx.Commit(ctx)
		}
		return deposit.Topup{}, false, deposit.ErrProofUsed
	case !errors.Is(err, pgx.ErrNoRows):
		return deposit.Topup{}, false, fmt.Errorf("periksa bukti top-up: %w", err)
	}

	var proofID string
	if err := tx.QueryRow(ctx, `INSERT INTO payment_proofs
  (purpose, branch_id, order_id, storage_key, nama_file, mime_type, ukuran_byte, sha256,
   uploaded_by_kind, uploaded_by_id)
VALUES ('deposit_topup',$1,NULL,$2,$3,$4,$5,$6,$7::actor_kind,$8) RETURNING id`,
		in.BranchID, in.Proof.StorageKey, in.Proof.NamaFile, in.Proof.MimeType, in.Proof.UkuranByte,
		sum, in.ActorKind, nullableUUID(in.ActorID)).Scan(&proofID); err != nil {
		return deposit.Topup{}, false, fmt.Errorf("catat bukti top-up: %w", err)
	}
	var topupID string
	if err := tx.QueryRow(ctx, `INSERT INTO deposit_topups
  (branch_id, bank_account_id, declared_amount, payment_proof_id, created_by_kind, created_by_id)
VALUES ($1,$2,$3,$4,$5::actor_kind,$6) RETURNING id`,
		in.BranchID, in.BankAccountID, in.DeclaredAmount, proofID, in.ActorKind,
		nullableUUID(in.ActorID)).Scan(&topupID); err != nil {
		return deposit.Topup{}, false, fmt.Errorf("catat top-up: %w", err)
	}
	if in.Audit.Entity != "" {
		in.Audit.EntityID = topupID
		if err := auditpg.Insert(ctx, tx, in.Audit); err != nil {
			return deposit.Topup{}, false, err
		}
	}
	t, err := scanTopup(tx.QueryRow(ctx, topupSelect+` WHERE t.id=$1`, topupID))
	if err != nil {
		return deposit.Topup{}, false, err
	}
	return t, false, tx.Commit(ctx)
}

// MarkTopup mencatat keputusan review/reject beserta log verifikasi immutable.
func (r *Repo) MarkTopup(ctx context.Context, in deposit.MarkTopupInput) (deposit.Topup, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return deposit.Topup{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	var declared int64
	if err := tx.QueryRow(ctx, `SELECT status::text, declared_amount FROM deposit_topups
WHERE id=$1 FOR UPDATE`, in.TopupID).Scan(&status, &declared); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deposit.Topup{}, deposit.ErrTopupNotFound
		}
		return deposit.Topup{}, fmt.Errorf("kunci top-up: %w", err)
	}
	if status != deposit.TopupPendingReview && status != deposit.TopupNeedsReview {
		return deposit.Topup{}, deposit.ErrTopupDecided
	}
	next, result := deposit.TopupNeedsReview, "needs_review"
	var rejection any
	if in.Action == deposit.ActionReject {
		next, result, rejection = deposit.TopupRejected, "rejected", in.Reason
	}
	if _, err := tx.Exec(ctx, `UPDATE deposit_topups SET status=$2::deposit_topup_status,
  rejection_reason=$3, updated_at=now() WHERE id=$1`, in.TopupID, next, rejection); err != nil {
		return deposit.Topup{}, fmt.Errorf("ubah status top-up: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deposit_verification_logs
  (topup_id, method, result, declared_amount, branch_allowlist_version, destination_result,
   admin_id, reason)
VALUES ($1,'manual',$2,$3,$4,'{}'::jsonb,$5,$6)`,
		in.TopupID, result, declared, in.AllowlistVersion, nullableUUID(in.ActorID), in.Reason); err != nil {
		return deposit.Topup{}, fmt.Errorf("catat log keputusan top-up: %w", err)
	}
	if in.Audit.Entity != "" {
		if err := auditpg.Insert(ctx, tx, in.Audit); err != nil {
			return deposit.Topup{}, err
		}
	}
	t, err := scanTopup(tx.QueryRow(ctx, topupSelect+` WHERE t.id=$1`, in.TopupID))
	if err != nil {
		return deposit.Topup{}, err
	}
	return t, tx.Commit(ctx)
}

func (r *Repo) Overview(ctx context.Context) ([]deposit.BranchOverview, error) {
	// Deposit milik cabang Cash & Carry; cabang lain hanya muncul selama masih
	// bersaldo (mis. kredit lebih bayar) atau punya top-up yang tercatat.
	rows, err := r.pool.Query(ctx, `SELECT b.id, b.nama, b.is_cnc, COALESCE(a.balance,0)::bigint,
  COALESCE(a.journal_version,0)::bigint,
  count(t.id) FILTER (WHERE t.status IN ('pending_review','needs_review'))::int,
  COALESCE(sum(t.declared_amount) FILTER (WHERE t.status IN ('pending_review','needs_review')),0)::bigint
FROM branches b
LEFT JOIN branch_deposit_accounts a ON a.branch_id=b.id
LEFT JOIN deposit_topups t ON t.branch_id=b.id
WHERE b.is_active
GROUP BY b.id, b.nama, b.is_cnc, a.balance, a.journal_version
HAVING b.is_cnc OR COALESCE(a.balance,0) <> 0 OR count(t.id) > 0
ORDER BY 6 DESC, b.nama`)
	if err != nil {
		return nil, fmt.Errorf("ikhtisar deposit: %w", err)
	}
	defer rows.Close()
	out := []deposit.BranchOverview{}
	for rows.Next() {
		var o deposit.BranchOverview
		if err := rows.Scan(&o.BranchID, &o.BranchNama, &o.IsCNC, &o.AvailableBalance, &o.JournalVersion,
			&o.PendingTopupCount, &o.PendingTopupAmount); err != nil {
			return nil, fmt.Errorf("baca ikhtisar deposit: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (r *Repo) BranchNama(ctx context.Context, branchID string) (string, error) {
	var nama string
	err := r.pool.QueryRow(ctx, `SELECT nama FROM branches WHERE id=$1`, branchID).Scan(&nama)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", deposit.ErrNotFound
	}
	return nama, err
}

// BranchCNC membaca flag Cash & Carry cabang.
func (r *Repo) BranchCNC(ctx context.Context, branchID string) (bool, error) {
	var cnc bool
	err := r.pool.QueryRow(ctx, `SELECT is_cnc FROM branches WHERE id=$1`, branchID).Scan(&cnc)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, deposit.ErrNotFound
	}
	return cnc, err
}

// Reconciled membandingkan cache saldo dengan jumlah journal (D3 §10).
func (r *Repo) Reconciled(ctx context.Context, branchID string) (bool, error) {
	var ok bool
	err := r.pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM branch_deposit_accounts WHERE branch_id=$1),0)
  = (SELECT COALESCE(sum(amount),0) FROM branch_deposit_transactions WHERE branch_id=$1)`, branchID).Scan(&ok)
	return ok, err
}

func (r *Repo) OrderRef(ctx context.Context, orderID string) (deposit.OrderRef, error) {
	var o deposit.OrderRef
	err := r.pool.QueryRow(ctx, `SELECT id, branch_id, status::text, versi, total FROM orders WHERE id=$1`, orderID).
		Scan(&o.ID, &o.BranchID, &o.Status, &o.Versi, &o.Total)
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.OrderRef{}, deposit.ErrNotFound
	}
	if err != nil {
		return deposit.OrderRef{}, fmt.Errorf("baca order deposit: %w", err)
	}
	return o, nil
}

func (r *Repo) OrderSettlement(ctx context.Context, orderID string) (deposit.OrderPaymentState, error) {
	var out deposit.OrderPaymentState
	var logID *string
	var version *int32
	var dep, trf *int64
	err := r.pool.QueryRow(ctx, `SELECT COALESCE(revision_payment_state::text,''),done_verification_log_id::text,
done_order_version,done_deposit_amount,done_transfer_amount FROM orders WHERE id=$1`, orderID).
		Scan(&out.RevisionPaymentState, &logID, &version, &dep, &trf)
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.OrderPaymentState{}, deposit.ErrNotFound
	}
	if err != nil {
		return deposit.OrderPaymentState{}, fmt.Errorf("baca settlement order: %w", err)
	}
	if logID != nil && version != nil && dep != nil && trf != nil {
		out.Settlement = &deposit.OrderSettlement{VerificationLogID: *logID, OrderVersion: *version,
			DepositAmount: *dep, TransferAmount: *trf}
	}
	return out, nil
}

func (r *Repo) OrderAllocations(ctx context.Context, orderID string) ([]deposit.AllocationView, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, order_id, order_version, amount, reversed_amount, created_at
FROM deposit_allocations WHERE order_id=$1 ORDER BY created_at, id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("daftar alokasi deposit: %w", err)
	}
	defer rows.Close()
	out := []deposit.AllocationView{}
	for rows.Next() {
		var v deposit.AllocationView
		var created time.Time
		if err := rows.Scan(&v.ID, &v.OrderID, &v.OrderVersion, &v.Amount, &v.ReversedAmount, &created); err != nil {
			return nil, fmt.Errorf("baca alokasi deposit: %w", err)
		}
		v.ActiveAmount, v.CreatedAt = v.Amount-v.ReversedAmount, created
		out = append(out, v)
	}
	return out, rows.Err()
}
