package postgres

// Helper transaksi deposit untuk orkestrator lain (C3): settlement campuran di
// verifikasi order dan reversal plan pada revisi. Pemanggil memegang transaksi
// dan wajib sudah mengikuti urutan kunci global D3 §5.1
// (branch -> account -> order -> allocation); helper di sini tidak membuka atau
// meng-commit transaksi sendiri.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/deposit"
)

// LockAccount mengunci cabang (FOR SHARE) lalu akun deposit (FOR UPDATE) dan
// menolak bila cache saldo berbeda dari journal.
func LockAccount(ctx context.Context, tx pgx.Tx, branchID string) (deposit.Account, error) {
	return lockBranchAndAccount(ctx, tx, branchID)
}

// DebitLocked adalah alokasi deposit ke satu order yang ditulis di dalam
// transaksi pemanggil: endpoint alokasi dan submit pesanan Cash & Carry.
type DebitLocked struct {
	BranchID           string
	OrderID            string
	OrderVersion       int32
	Amount             int64
	IdempotencyKey     string
	RequestFingerprint []byte
	ActorKind          string
	ActorID            string
}

// AllocateLocked menulis journal `order_allocation_debit` + baris alokasi dan
// menurunkan saldo. Pemanggil sudah mengunci cabang -> akun -> order dan sudah
// memastikan nominal tidak melebihi saldo maupun sisa total order; saldo yang
// akan negatif tetap ditolak updateAccount.
func AllocateLocked(ctx context.Context, tx pgx.Tx, in DebitLocked) (deposit.Allocation, deposit.Account, error) {
	if in.Amount <= 0 {
		return deposit.Allocation{}, deposit.Account{}, deposit.ErrExceedsOrder
	}
	var transactionID string
	if err := tx.QueryRow(ctx, `INSERT INTO branch_deposit_transactions(
branch_id,kind,amount,order_id,order_version,idempotency_key_hash,request_fingerprint,
actor_kind,actor_id)
VALUES($1,'order_allocation_debit',$2,$3,$4,$5,$6,$7::actor_kind,$8) RETURNING id`,
		in.BranchID, -in.Amount, in.OrderID, in.OrderVersion, keyHash(in.IdempotencyKey),
		in.RequestFingerprint, in.ActorKind, nullableUUID(in.ActorID)).Scan(&transactionID); err != nil {
		return deposit.Allocation{}, deposit.Account{}, mapConstraint(err)
	}
	var a deposit.Allocation
	if err := tx.QueryRow(ctx, `INSERT INTO deposit_allocations(
branch_id,order_id,order_version,debit_transaction_id,amount,created_by_kind,created_by_id)
VALUES($1,$2,$3,$4,$5,$6::actor_kind,$7)
RETURNING id,branch_id,order_id,order_version,debit_transaction_id,amount,reversed_amount,created_at`,
		in.BranchID, in.OrderID, in.OrderVersion, transactionID, in.Amount, in.ActorKind, nullableUUID(in.ActorID)).
		Scan(&a.ID, &a.BranchID, &a.OrderID, &a.OrderVersion, &a.DebitTransactionID, &a.Amount, &a.ReversedAmount, &a.CreatedAt); err != nil {
		return deposit.Allocation{}, deposit.Account{}, mapConstraint(err)
	}
	account, err := updateAccount(ctx, tx, in.BranchID, -in.Amount)
	if err != nil {
		return deposit.Allocation{}, deposit.Account{}, err
	}
	return a, account, nil
}

// OverpayCredit adalah kredit lebih bayar yang bersumber dari satu bukti order
// pada attempt verifikasi tertentu.
type OverpayCredit struct {
	BranchID           string
	OrderID            string
	OrderVersion       int32
	VerificationLogID  string
	ProofID            string
	Amount             int64
	IdempotencyKey     string
	RequestFingerprint []byte
	ActorID            string
	Reason             string
}

// CreditOverpayLocked menulis journal `overpay_credit` dan menaikkan saldo.
// Hanya sah di dalam transaksi settlement yang sama dengan attempt Done-nya.
func CreditOverpayLocked(ctx context.Context, tx pgx.Tx, in OverpayCredit) (deposit.Transaction, deposit.Account, error) {
	if in.Amount <= 0 || in.ActorID == "" || in.VerificationLogID == "" || in.ProofID == "" {
		return deposit.Transaction{}, deposit.Account{}, deposit.ErrCreditSourceInvalid
	}
	out := deposit.Transaction{BranchID: in.BranchID, Kind: "overpay_credit", Amount: in.Amount,
		OrderID: in.OrderID, OrderVersion: &in.OrderVersion,
		OrderVerificationLogID: in.VerificationLogID, SourceProofID: in.ProofID}
	if err := tx.QueryRow(ctx, `INSERT INTO branch_deposit_transactions(
branch_id,kind,amount,source_proof_id,order_verification_log_id,order_id,order_version,
idempotency_key_hash,request_fingerprint,actor_kind,actor_id,reason)
VALUES($1,'overpay_credit',$2,$3,$4,$5,$6,$7,$8,'admin',$9,$10)
RETURNING id,created_at`,
		in.BranchID, in.Amount, in.ProofID, in.VerificationLogID, in.OrderID, in.OrderVersion,
		keyHash(in.IdempotencyKey), in.RequestFingerprint, in.ActorID, nullableText(in.Reason)).
		Scan(&out.ID, &out.CreatedAt); err != nil {
		return deposit.Transaction{}, deposit.Account{}, mapConstraint(err)
	}
	account, err := updateAccount(ctx, tx, in.BranchID, in.Amount)
	if err != nil {
		return deposit.Transaction{}, deposit.Account{}, err
	}
	return out, account, nil
}

// ReverseLocked membalik sebagian/seluruh alokasi aktif milik order dan cabang
// yang sudah dikunci pemanggil (reversal plan revisi, C0 §3.4).
func ReverseLocked(ctx context.Context, tx pgx.Tx, in deposit.ReversalInput) (deposit.Transaction, deposit.Allocation, deposit.Account, error) {
	if in.Amount <= 0 || in.Reason == "" {
		return deposit.Transaction{}, deposit.Allocation{}, deposit.Account{}, deposit.ErrReversalExceeds
	}
	var a deposit.Allocation
	if err := tx.QueryRow(ctx, `SELECT id,branch_id,order_id,order_version,debit_transaction_id,
amount,reversed_amount,created_at FROM deposit_allocations WHERE id=$1 FOR UPDATE`, in.AllocationID).
		Scan(&a.ID, &a.BranchID, &a.OrderID, &a.OrderVersion, &a.DebitTransactionID, &a.Amount, &a.ReversedAmount, &a.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deposit.Transaction{}, deposit.Allocation{}, deposit.Account{}, deposit.ErrNotFound
		}
		return deposit.Transaction{}, deposit.Allocation{}, deposit.Account{}, err
	}
	if a.BranchID != in.BranchID || a.OrderID != in.OrderID {
		return deposit.Transaction{}, deposit.Allocation{}, deposit.Account{}, deposit.ErrBranchMismatch
	}
	return applyReversal(ctx, tx, a, in)
}

// applyReversal menulis kredit reversal, menaikkan cache reversed_amount, dan
// mengembalikan saldo. Alokasi `a` wajib sudah dikunci FOR UPDATE.
func applyReversal(ctx context.Context, tx pgx.Tx, a deposit.Allocation, in deposit.ReversalInput) (deposit.Transaction, deposit.Allocation, deposit.Account, error) {
	var reversedJournal int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount),0)::bigint
FROM branch_deposit_transactions WHERE reverses_transaction_id=$1`, a.DebitTransactionID).
		Scan(&reversedJournal); err != nil {
		return deposit.Transaction{}, deposit.Allocation{}, deposit.Account{}, err
	}
	if reversedJournal != a.ReversedAmount {
		return deposit.Transaction{}, deposit.Allocation{}, deposit.Account{}, deposit.ErrReconciliation
	}
	if in.Amount > a.Amount-a.ReversedAmount {
		return deposit.Transaction{}, deposit.Allocation{}, deposit.Account{}, deposit.ErrReversalExceeds
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
		return deposit.Transaction{}, deposit.Allocation{}, deposit.Account{}, mapConstraint(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE deposit_allocations
SET reversed_amount=reversed_amount+$2 WHERE id=$1`, a.ID, in.Amount); err != nil {
		return deposit.Transaction{}, deposit.Allocation{}, deposit.Account{}, fmt.Errorf("ubah cache reversal: %w", err)
	}
	a.ReversedAmount += in.Amount
	account, err := updateAccount(ctx, tx, in.BranchID, in.Amount)
	if err != nil {
		return deposit.Transaction{}, deposit.Allocation{}, deposit.Account{}, err
	}
	return out, a, account, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
