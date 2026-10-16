package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grahita/portal/api/internal/deposit"
	"github.com/grahita/portal/api/internal/platform/database"
)

type Repo struct{ pool *database.Pool }

func New(pool *database.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) Account(ctx context.Context, branchID string) (deposit.Account, error) {
	var out deposit.Account
	err := r.pool.QueryRow(ctx, `SELECT branch_id,balance,journal_version
FROM branch_deposit_accounts WHERE branch_id=$1`, branchID).
		Scan(&out.BranchID, &out.Balance, &out.JournalVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.Account{}, deposit.ErrNotFound
	}
	if err != nil {
		return deposit.Account{}, fmt.Errorf("baca akun deposit: %w", err)
	}
	return out, nil
}

func keyHash(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func lockBranchAndAccount(ctx context.Context, tx pgx.Tx, branchID string) (deposit.Account, error) {
	var found string
	if err := tx.QueryRow(ctx, `SELECT id FROM branches WHERE id=$1 FOR SHARE`, branchID).Scan(&found); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deposit.Account{}, deposit.ErrNotFound
		}
		return deposit.Account{}, fmt.Errorf("kunci cabang: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO branch_deposit_accounts(branch_id)
VALUES($1) ON CONFLICT(branch_id) DO NOTHING`, branchID); err != nil {
		return deposit.Account{}, fmt.Errorf("buat akun deposit: %w", err)
	}
	var account deposit.Account
	if err := tx.QueryRow(ctx, `SELECT branch_id,balance,journal_version
FROM branch_deposit_accounts WHERE branch_id=$1 FOR UPDATE`, branchID).
		Scan(&account.BranchID, &account.Balance, &account.JournalVersion); err != nil {
		return deposit.Account{}, fmt.Errorf("kunci akun deposit: %w", err)
	}
	var journal int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount),0)::bigint
FROM branch_deposit_transactions WHERE branch_id=$1`, branchID).Scan(&journal); err != nil {
		return deposit.Account{}, fmt.Errorf("rekonsiliasi journal: %w", err)
	}
	if journal != account.Balance {
		return deposit.Account{}, deposit.ErrReconciliation
	}
	return account, nil
}

func loadReplay(ctx context.Context, tx pgx.Tx, branchID, key string, fingerprint []byte) (deposit.Transaction, bool, error) {
	var out deposit.Transaction
	var orderID, reversedID, depositLogID, orderLogID, proofID *string
	var orderVersion *int32
	var stored []byte
	err := tx.QueryRow(ctx, `SELECT id,branch_id,kind::text,amount,order_id,order_version,
reverses_transaction_id,deposit_verification_log_id,order_verification_log_id,
source_proof_id,request_fingerprint,created_at
FROM branch_deposit_transactions
WHERE branch_id=$1 AND idempotency_key_hash=$2`, branchID, keyHash(key)).
		Scan(&out.ID, &out.BranchID, &out.Kind, &out.Amount, &orderID, &orderVersion,
			&reversedID, &depositLogID, &orderLogID, &proofID, &stored, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.Transaction{}, false, nil
	}
	if err != nil {
		return deposit.Transaction{}, false, fmt.Errorf("baca replay deposit: %w", err)
	}
	if !bytes.Equal(stored, fingerprint) {
		return deposit.Transaction{}, false, deposit.ErrIdempotencyReused
	}
	out.OrderVersion = orderVersion
	if orderID != nil {
		out.OrderID = *orderID
	}
	if reversedID != nil {
		out.ReversesTransactionID = *reversedID
	}
	if depositLogID != nil {
		out.DepositVerificationLogID = *depositLogID
	}
	if orderLogID != nil {
		out.OrderVerificationLogID = *orderLogID
	}
	if proofID != nil {
		out.SourceProofID = *proofID
	}
	return out, true, nil
}

func updateAccount(ctx context.Context, tx pgx.Tx, branchID string, delta int64) (deposit.Account, error) {
	var out deposit.Account
	err := tx.QueryRow(ctx, `UPDATE branch_deposit_accounts
SET balance=balance+$2,journal_version=journal_version+1,updated_at=now()
WHERE branch_id=$1 AND balance+$2>=0
RETURNING branch_id,balance,journal_version`, branchID, delta).
		Scan(&out.BranchID, &out.Balance, &out.JournalVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return deposit.Account{}, deposit.ErrNegativeBalance
	}
	if err != nil {
		return deposit.Account{}, fmt.Errorf("ubah akun deposit: %w", err)
	}
	return out, nil
}

func accountAtTransaction(ctx context.Context, tx pgx.Tx, transaction deposit.Transaction) (deposit.Account, error) {
	out := deposit.Account{BranchID: transaction.BranchID}
	err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount),0)::bigint,count(*)::bigint
FROM branch_deposit_transactions
WHERE branch_id=$1 AND (created_at,id) <= ($2,$3)`,
		transaction.BranchID, transaction.CreatedAt, transaction.ID).
		Scan(&out.Balance, &out.JournalVersion)
	if err != nil {
		return deposit.Account{}, fmt.Errorf("rekonstruksi response idempoten: %w", err)
	}
	return out, nil
}

func (r *Repo) Credit(ctx context.Context, in deposit.CreditInput) (deposit.Transaction, deposit.Account, bool, error) {
	if in.Amount <= 0 || (in.Kind != "topup_credit" && in.Kind != "overpay_credit") {
		return deposit.Transaction{}, deposit.Account{}, false, deposit.ErrInvalidKind
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return deposit.Transaction{}, deposit.Account{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = lockBranchAndAccount(ctx, tx, in.BranchID)
	if err != nil {
		return deposit.Transaction{}, deposit.Account{}, false, err
	}
	if replay, ok, err := loadReplay(ctx, tx, in.BranchID, in.IdempotencyKey, in.RequestFingerprint); err != nil {
		return deposit.Transaction{}, deposit.Account{}, false, err
	} else if ok {
		replayAccount, err := accountAtTransaction(ctx, tx, replay)
		if err != nil {
			return deposit.Transaction{}, deposit.Account{}, false, err
		}
		return replay, replayAccount, true, tx.Commit(ctx)
	}
	if in.Kind == "topup_credit" {
		var sourceBranch, sourceProof string
		if err := tx.QueryRow(ctx, `SELECT t.branch_id,t.payment_proof_id
FROM deposit_verification_logs v JOIN deposit_topups t ON t.id=v.topup_id
WHERE v.id=$1 FOR UPDATE OF t`, in.DepositVerificationLogID).Scan(&sourceBranch, &sourceProof); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return deposit.Transaction{}, deposit.Account{}, false, deposit.ErrNotFound
			}
			return deposit.Transaction{}, deposit.Account{}, false, err
		}
		if sourceBranch != in.BranchID || sourceProof != in.SourceProofID {
			return deposit.Transaction{}, deposit.Account{}, false, deposit.ErrBranchMismatch
		}
	}
	if in.Kind == "overpay_credit" {
		var sourceBranch, sourceOrder string
		if err := tx.QueryRow(ctx, `SELECT o.branch_id,v.order_id
FROM verification_logs v JOIN orders o ON o.id=v.order_id
WHERE v.id=$1 FOR UPDATE OF o`, in.OrderVerificationLogID).Scan(&sourceBranch, &sourceOrder); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return deposit.Transaction{}, deposit.Account{}, false, deposit.ErrNotFound
			}
			return deposit.Transaction{}, deposit.Account{}, false, err
		}
		if sourceBranch != in.BranchID || sourceOrder != in.OrderID {
			return deposit.Transaction{}, deposit.Account{}, false, deposit.ErrBranchMismatch
		}
	}
	var out deposit.Transaction
	var orderID, depositLogID, orderLogID, proofID *string
	err = tx.QueryRow(ctx, `INSERT INTO branch_deposit_transactions(
branch_id,kind,amount,source_proof_id,deposit_verification_log_id,
order_verification_log_id,order_id,order_version,idempotency_key_hash,
request_fingerprint,actor_kind,actor_id,reason)
VALUES($1,$2::branch_deposit_transaction_kind,$3,$4,$5,$6,$7,$8,$9,$10,$11::actor_kind,$12,$13)
RETURNING id,branch_id,kind::text,amount,order_id,order_version,
deposit_verification_log_id,order_verification_log_id,source_proof_id,created_at`,
		in.BranchID, in.Kind, in.Amount, nullableUUID(in.SourceProofID),
		nullableUUID(in.DepositVerificationLogID), nullableUUID(in.OrderVerificationLogID),
		nullableUUID(in.OrderID), in.OrderVersion, keyHash(in.IdempotencyKey),
		in.RequestFingerprint, in.ActorKind, nullableUUID(in.ActorID), in.Reason).
		Scan(&out.ID, &out.BranchID, &out.Kind, &out.Amount, &orderID, &out.OrderVersion,
			&depositLogID, &orderLogID, &proofID, &out.CreatedAt)
	if err != nil {
		return deposit.Transaction{}, deposit.Account{}, false, mapConstraint(err)
	}
	if orderID != nil {
		out.OrderID = *orderID
	}
	if depositLogID != nil {
		out.DepositVerificationLogID = *depositLogID
	}
	if orderLogID != nil {
		out.OrderVerificationLogID = *orderLogID
	}
	if proofID != nil {
		out.SourceProofID = *proofID
	}
	account, err := updateAccount(ctx, tx, in.BranchID, in.Amount)
	if err != nil {
		return deposit.Transaction{}, deposit.Account{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deposit.Transaction{}, deposit.Account{}, false, err
	}
	return out, account, false, nil
}

func (r *Repo) Allocate(ctx context.Context, in deposit.AllocationInput) (deposit.AllocationResult, error) {
	if in.Amount <= 0 {
		return deposit.AllocationResult{}, deposit.ErrExceedsOrder
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return deposit.AllocationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	account, err := lockBranchAndAccount(ctx, tx, in.BranchID)
	if err != nil {
		return deposit.AllocationResult{}, err
	}
	var orderBranch, status string
	var orderVersion int32
	var total int64
	if err := tx.QueryRow(ctx, `SELECT branch_id,status::text,versi,total FROM orders WHERE id=$1 FOR UPDATE`, in.OrderID).
		Scan(&orderBranch, &status, &orderVersion, &total); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deposit.AllocationResult{}, deposit.ErrNotFound
		}
		return deposit.AllocationResult{}, err
	}
	if orderBranch != in.BranchID {
		return deposit.AllocationResult{}, deposit.ErrBranchMismatch
	}
	if status != "submitted" {
		return deposit.AllocationResult{}, deposit.ErrOrderNotSubmitted
	}
	if replay, ok, err := loadReplay(ctx, tx, in.BranchID, in.IdempotencyKey, in.RequestFingerprint); err != nil {
		return deposit.AllocationResult{}, err
	} else if ok {
		var a deposit.Allocation
		err := tx.QueryRow(ctx, `SELECT id,branch_id,order_id,order_version,debit_transaction_id,
amount,reversed_amount,created_at FROM deposit_allocations WHERE debit_transaction_id=$1`, replay.ID).
			Scan(&a.ID, &a.BranchID, &a.OrderID, &a.OrderVersion, &a.DebitTransactionID, &a.Amount, &a.ReversedAmount, &a.CreatedAt)
		if err != nil {
			return deposit.AllocationResult{}, err
		}
		replayAccount, err := accountAtTransaction(ctx, tx, replay)
		if err != nil {
			return deposit.AllocationResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return deposit.AllocationResult{}, err
		}
		return deposit.AllocationResult{Allocation: a, Account: replayAccount, Replayed: true}, nil
	}
	if account.JournalVersion != in.ExpectedJournalVersion || orderVersion != in.ExpectedOrderVersion {
		return deposit.AllocationResult{}, deposit.ErrVersionConflict
	}
	var committed int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount-reversed_amount),0)::bigint
FROM deposit_allocations WHERE order_id=$1`, in.OrderID).Scan(&committed); err != nil {
		return deposit.AllocationResult{}, err
	}
	if in.Amount > account.Balance {
		return deposit.AllocationResult{}, deposit.ErrInsufficientBalance
	}
	if committed+in.Amount > total {
		return deposit.AllocationResult{}, deposit.ErrExceedsOrder
	}
	var transactionID string
	if err := tx.QueryRow(ctx, `INSERT INTO branch_deposit_transactions(
branch_id,kind,amount,order_id,order_version,idempotency_key_hash,request_fingerprint,
actor_kind,actor_id)
VALUES($1,'order_allocation_debit',$2,$3,$4,$5,$6,$7::actor_kind,$8) RETURNING id`,
		in.BranchID, -in.Amount, in.OrderID, orderVersion, keyHash(in.IdempotencyKey),
		in.RequestFingerprint, in.ActorKind, nullableUUID(in.ActorID)).Scan(&transactionID); err != nil {
		return deposit.AllocationResult{}, mapConstraint(err)
	}
	var a deposit.Allocation
	if err := tx.QueryRow(ctx, `INSERT INTO deposit_allocations(
branch_id,order_id,order_version,debit_transaction_id,amount,created_by_kind,created_by_id)
VALUES($1,$2,$3,$4,$5,$6::actor_kind,$7)
RETURNING id,branch_id,order_id,order_version,debit_transaction_id,amount,reversed_amount,created_at`,
		in.BranchID, in.OrderID, orderVersion, transactionID, in.Amount, in.ActorKind, nullableUUID(in.ActorID)).
		Scan(&a.ID, &a.BranchID, &a.OrderID, &a.OrderVersion, &a.DebitTransactionID, &a.Amount, &a.ReversedAmount, &a.CreatedAt); err != nil {
		return deposit.AllocationResult{}, mapConstraint(err)
	}
	account, err = updateAccount(ctx, tx, in.BranchID, -in.Amount)
	if err != nil {
		return deposit.AllocationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deposit.AllocationResult{}, err
	}
	return deposit.AllocationResult{Allocation: a, Account: account}, nil
}

func (r *Repo) ReverseAllocation(ctx context.Context, in deposit.ReversalInput) (deposit.ReversalResult, error) {
	if in.Amount <= 0 || in.Reason == "" {
		return deposit.ReversalResult{}, deposit.ErrReversalExceeds
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return deposit.ReversalResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	account, err := lockBranchAndAccount(ctx, tx, in.BranchID)
	if err != nil {
		return deposit.ReversalResult{}, err
	}
	var orderBranch string
	if err := tx.QueryRow(ctx, `SELECT branch_id FROM orders WHERE id=$1 FOR UPDATE`, in.OrderID).Scan(&orderBranch); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deposit.ReversalResult{}, deposit.ErrNotFound
		}
		return deposit.ReversalResult{}, err
	}
	if orderBranch != in.BranchID {
		return deposit.ReversalResult{}, deposit.ErrBranchMismatch
	}
	var a deposit.Allocation
	if err := tx.QueryRow(ctx, `SELECT id,branch_id,order_id,order_version,debit_transaction_id,
amount,reversed_amount,created_at FROM deposit_allocations WHERE id=$1 FOR UPDATE`, in.AllocationID).
		Scan(&a.ID, &a.BranchID, &a.OrderID, &a.OrderVersion, &a.DebitTransactionID, &a.Amount, &a.ReversedAmount, &a.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deposit.ReversalResult{}, deposit.ErrNotFound
		}
		return deposit.ReversalResult{}, err
	}
	if a.BranchID != in.BranchID || a.OrderID != in.OrderID {
		return deposit.ReversalResult{}, deposit.ErrBranchMismatch
	}
	if replay, ok, err := loadReplay(ctx, tx, in.BranchID, in.IdempotencyKey, in.RequestFingerprint); err != nil {
		return deposit.ReversalResult{}, err
	} else if ok {
		var replayReversed int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount),0)::bigint
FROM branch_deposit_transactions
WHERE reverses_transaction_id=$1 AND (created_at,id) <= ($2,$3)`,
			a.DebitTransactionID, replay.CreatedAt, replay.ID).Scan(&replayReversed); err != nil {
			return deposit.ReversalResult{}, err
		}
		replayAccount, err := accountAtTransaction(ctx, tx, replay)
		if err != nil {
			return deposit.ReversalResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return deposit.ReversalResult{}, err
		}
		return deposit.ReversalResult{Transaction: replay, ReversedAmount: replayReversed,
			ActiveAmount: a.Amount - replayReversed, Account: replayAccount, Replayed: true}, nil
	}
	if account.JournalVersion != in.ExpectedJournalVersion {
		return deposit.ReversalResult{}, deposit.ErrVersionConflict
	}
	var reversedJournal int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount),0)::bigint
FROM branch_deposit_transactions WHERE reverses_transaction_id=$1`, a.DebitTransactionID).
		Scan(&reversedJournal); err != nil {
		return deposit.ReversalResult{}, err
	}
	if reversedJournal != a.ReversedAmount {
		return deposit.ReversalResult{}, deposit.ErrReconciliation
	}
	if in.Amount > a.Amount-a.ReversedAmount {
		return deposit.ReversalResult{}, deposit.ErrReversalExceeds
	}
	var out deposit.Transaction
	if err := tx.QueryRow(ctx, `INSERT INTO branch_deposit_transactions(
branch_id,kind,amount,order_id,order_version,reverses_transaction_id,
idempotency_key_hash,request_fingerprint,actor_kind,actor_id,reason)
VALUES($1,'allocation_reversal_credit',$2,$3,$4,$5,$6,$7,$8::actor_kind,$9,$10)
RETURNING id,branch_id,kind::text,amount,order_id,order_version,reverses_transaction_id,created_at`,
		in.BranchID, in.Amount, in.OrderID, a.OrderVersion, a.DebitTransactionID, keyHash(in.IdempotencyKey),
		in.RequestFingerprint, in.ActorKind, nullableUUID(in.ActorID), in.Reason).
		Scan(&out.ID, &out.BranchID, &out.Kind, &out.Amount, &out.OrderID, &out.OrderVersion,
			&out.ReversesTransactionID, &out.CreatedAt); err != nil {
		return deposit.ReversalResult{}, mapConstraint(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE deposit_allocations
SET reversed_amount=reversed_amount+$2 WHERE id=$1`, a.ID, in.Amount); err != nil {
		return deposit.ReversalResult{}, err
	}
	a.ReversedAmount += in.Amount
	account, err = updateAccount(ctx, tx, in.BranchID, in.Amount)
	if err != nil {
		return deposit.ReversalResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deposit.ReversalResult{}, err
	}
	return deposit.ReversalResult{Transaction: out, ReversedAmount: a.ReversedAmount,
		ActiveAmount: a.Amount - a.ReversedAmount, Account: account}, nil
}

func (r *Repo) SnapshotActiveAllocations(ctx context.Context, in deposit.SnapshotInput) ([]deposit.SourceSnapshot, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockBranchAndAccount(ctx, tx, in.BranchID); err != nil {
		return nil, err
	}
	var orderBranch string
	var version int32
	var total int64
	if err := tx.QueryRow(ctx, `SELECT branch_id,versi,total FROM orders WHERE id=$1 FOR UPDATE`, in.OrderID).
		Scan(&orderBranch, &version, &total); err != nil {
		return nil, err
	}
	if orderBranch != in.BranchID {
		return nil, deposit.ErrBranchMismatch
	}
	if version != in.OrderVersion || total != in.SettlementTotal {
		return nil, deposit.ErrVersionConflict
	}
	var logDeposit int64
	if err := tx.QueryRow(ctx, `SELECT deposit_amount FROM verification_logs
WHERE id=$1 AND order_id=$2 AND order_version=$3 AND total_pesanan=$4`,
		in.VerificationLogID, in.OrderID, in.OrderVersion, in.SettlementTotal).Scan(&logDeposit); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, deposit.ErrNotFound
		}
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id,order_version,(amount-reversed_amount)::bigint
FROM deposit_allocations WHERE order_id=$1 AND amount>reversed_amount ORDER BY id FOR UPDATE`, in.OrderID)
	if err != nil {
		return nil, err
	}
	var out []deposit.SourceSnapshot
	var sum int64
	for rows.Next() {
		var s deposit.SourceSnapshot
		if err := rows.Scan(&s.AllocationID, &s.AllocationOrderVersion, &s.Amount); err != nil {
			rows.Close()
			return nil, err
		}
		if s.AllocationOrderVersion > in.OrderVersion {
			rows.Close()
			return nil, deposit.ErrVersionConflict
		}
		out = append(out, s)
		sum += s.Amount
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if sum != logDeposit {
		return nil, deposit.ErrReconciliation
	}
	for _, s := range out {
		if _, err := tx.Exec(ctx, `INSERT INTO verification_deposit_sources(
verification_log_id,order_id,settlement_order_version,settlement_total,
allocation_id,branch_id,allocation_order_version,amount_snapshot)
VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`,
			in.VerificationLogID, in.OrderID, in.OrderVersion, in.SettlementTotal,
			s.AllocationID, in.BranchID, s.AllocationOrderVersion, s.Amount); err != nil {
			return nil, err
		}
	}
	var storedCount int
	var storedSum int64
	if err := tx.QueryRow(ctx, `SELECT count(*)::int,COALESCE(sum(amount_snapshot),0)::bigint
FROM verification_deposit_sources WHERE verification_log_id=$1`, in.VerificationLogID).
		Scan(&storedCount, &storedSum); err != nil {
		return nil, err
	}
	if storedCount != len(out) || storedSum != sum {
		return nil, deposit.ErrReconciliation
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func mapConstraint(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return deposit.ErrIdempotencyReused
		case "23503", "23514":
			return fmt.Errorf("%w: %s", deposit.ErrInvalidKind, pgErr.ConstraintName)
		}
	}
	return err
}
