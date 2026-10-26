package receivable

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxProofBytes = 10 << 20

type Repayment struct {
	ID              string    `json:"id"`
	BranchID        string    `json:"branch_id"`
	BranchName      string    `json:"branch_name,omitempty"`
	Bank            string    `json:"bank,omitempty"`
	AccountNumber   string    `json:"account_number,omitempty"`
	Filename        string    `json:"filename,omitempty"`
	RejectionReason string    `json:"rejection_reason,omitempty"`
	DeclaredAmount  int64     `json:"declared_amount"`
	VerifiedAmount  *int64    `json:"verified_amount,omitempty"`
	Status          string    `json:"status"`
	SubmittedAt     time.Time `json:"submitted_at"`
}

type UploadInput struct {
	Amount        int64
	BankAccountID string
	Filename      string
	Body          []byte
}

func (s *Service) ProofURL(ctx context.Context, actor auth.Actor, store storage.Store, id string) (string, error) {
	if !actor.IsBranch() && !actor.Can(auth.CapAdminVerify) {
		return "", ErrForbidden
	}
	if !uuidPattern.MatchString(id) {
		return "", ErrNotFound
	}
	var branchID, key string
	err := s.pool.QueryRow(ctx, `SELECT r.branch_id::text,p.storage_key
FROM tempo_receipts r JOIN payment_proofs p ON p.id=r.payment_proof_id
WHERE r.id=$1`, id).Scan(&branchID, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if actor.IsBranch() && actor.BranchID != branchID {
		return "", ErrNotFound
	}
	return store.PresignGet(ctx, key, 10*time.Minute)
}

// UploadProof stores a branch-owned receipt. The object is deleted if the
// database transaction fails, so a failed request cannot leave a usable proof.
func (s *Service) UploadProof(ctx context.Context, actor auth.Actor, store storage.Store, in UploadInput) (Repayment, error) {
	if !actor.IsBranch() || actor.BranchID == "" {
		return Repayment{}, ErrForbidden
	}
	if in.Amount <= 0 || in.Amount > 1_000_000_000_000 || len(in.Body) == 0 || len(in.Body) > maxProofBytes ||
		!uuidPattern.MatchString(in.BankAccountID) {
		return Repayment{}, ErrInvalid
	}
	mime := http.DetectContentType(in.Body)
	if mime != "application/pdf" && mime != "image/jpeg" && mime != "image/png" {
		return Repayment{}, ErrInvalid
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return Repayment{}, err
	}
	key := "tempo-proof/" + actor.BranchID + "/" + hex.EncodeToString(nonce)
	if err := store.Put(ctx, key, bytes.NewReader(in.Body), int64(len(in.Body)), mime); err != nil {
		return Repayment{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = store.Delete(ctx, key)
		}
	}()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Repayment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM branches WHERE id=$1 FOR UPDATE`, actor.BranchID); err != nil {
		return Repayment{}, err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM branch_bank_accounts x
 JOIN bank_accounts ba ON ba.id=x.bank_account_id
 JOIN branches b ON b.id=x.branch_id
 WHERE x.branch_id=$1 AND x.bank_account_id=$2 AND x.is_allowed AND ba.is_active AND b.is_active)`,
		actor.BranchID, in.BankAccountID).Scan(&active); err != nil {
		return Repayment{}, err
	}
	if !active {
		return Repayment{}, ErrInvalid
	}
	var proofID string
	hash := sha256.Sum256(in.Body)
	err = tx.QueryRow(ctx, `INSERT INTO payment_proofs
 (order_id,branch_id,purpose,storage_key,nama_file,mime_type,ukuran_byte,sha256,
 uploaded_by_kind,uploaded_by_id,uploaded_by_nama)
 VALUES(NULL,$1,'tempo_repayment',$2,$3,$4,$5,$6,'branch',$7,$8) RETURNING id::text`,
		actor.BranchID, key, in.Filename, mime, len(in.Body), hash[:], actor.UserID, actor.Nama).Scan(&proofID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "payment_proofs_tempo_hash_once" {
			return Repayment{}, ErrDuplicateProof
		}
		return Repayment{}, fmt.Errorf("simpan bukti Tempo: %w", err)
	}
	var out Repayment
	err = tx.QueryRow(ctx, `INSERT INTO tempo_receipts
(branch_id,payment_proof_id,bank_account_id,declared_amount)
VALUES($1,$2,$3,$4)
RETURNING id::text,branch_id::text,declared_amount,status,submitted_at`,
		actor.BranchID, proofID, in.BankAccountID, in.Amount).
		Scan(&out.ID, &out.BranchID, &out.DeclaredAmount, &out.Status, &out.SubmittedAt)
	if err != nil {
		return Repayment{}, err
	}
	var overdue int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount-paid_amount),0)::bigint
 FROM tempo_charges WHERE branch_id=$1
 AND due_date<(now() AT TIME ZONE 'Asia/Jakarta')::date`, actor.BranchID).Scan(&overdue); err != nil {
		return Repayment{}, err
	}
	var pending int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(declared_amount),0)::bigint
FROM tempo_receipts WHERE branch_id=$1 AND status='pending'`, actor.BranchID).Scan(&pending); err != nil {
		return Repayment{}, err
	}
	if overdue > 0 && pending >= overdue {
		until := GraceEnd(out.SubmittedAt)
		if _, err := tx.Exec(ctx, `INSERT INTO tempo_hold_grace(branch_id,started_at,ends_on)
 VALUES($1,$2,$3) ON CONFLICT(branch_id) DO NOTHING`, actor.BranchID, out.SubmittedAt, until); err != nil {
			return Repayment{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Repayment{}, err
	}
	committed = true
	return out, nil
}

// List shows a branch its own repayments, or the verification queue to admin.
func (s *Service) List(ctx context.Context, actor auth.Actor, onlyPending bool) ([]Repayment, error) {
	if !actor.IsBranch() && !actor.Can(auth.CapAdminVerify) {
		return nil, ErrForbidden
	}
	query := `SELECT r.id::text,r.branch_id::text,r.declared_amount,r.verified_amount,r.status,r.submitted_at,
 b.nama,ba.bank::text,ba.nomor,p.nama_file,COALESCE(r.rejection_reason,'')
 FROM tempo_receipts r JOIN branches b ON b.id=r.branch_id
 JOIN bank_accounts ba ON ba.id=r.bank_account_id JOIN payment_proofs p ON p.id=r.payment_proof_id
 WHERE ($1::uuid IS NULL OR r.branch_id=$1::uuid)
 AND (NOT $2::boolean OR r.status='pending') ORDER BY r.submitted_at DESC,r.id DESC`
	var branch any
	if actor.IsBranch() {
		branch = actor.BranchID
	}
	rows, err := s.pool.Query(ctx, query, branch, onlyPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repayment{}
	for rows.Next() {
		var r Repayment
		if err := rows.Scan(&r.ID, &r.BranchID, &r.DeclaredAmount, &r.VerifiedAmount, &r.Status, &r.SubmittedAt,
			&r.BranchName, &r.Bank, &r.AccountNumber, &r.Filename, &r.RejectionReason); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Decide verifies a receipt and allocates the amount to the oldest approved
// open invoices by submit date. Overpayment becomes separate Tempo credit, and
// therefore requires a Superadmin decision.
func (s *Service) Decide(ctx context.Context, actor auth.Actor, receiptID, decision string, amount int64, reason string) (Repayment, error) {
	if !actor.Can(auth.CapAdminVerify) {
		return Repayment{}, ErrForbidden
	}
	if !uuidPattern.MatchString(receiptID) {
		return Repayment{}, ErrNotFound
	}
	if decision != "verify" && decision != "reject" {
		return Repayment{}, ErrInvalid
	}
	if decision == "verify" && (amount <= 0 || amount > 1_000_000_000_000) {
		return Repayment{}, ErrInvalid
	}
	if decision == "reject" && strings.TrimSpace(reason) == "" {
		return Repayment{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Repayment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var branchID string
	if err := tx.QueryRow(ctx, `SELECT branch_id::text FROM tempo_receipts WHERE id=$1`, receiptID).Scan(&branchID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Repayment{}, ErrNotFound
		}
		return Repayment{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM branches WHERE id=$1 FOR UPDATE`, branchID); err != nil {
		return Repayment{}, err
	}
	var out Repayment
	err = tx.QueryRow(ctx, `SELECT id::text,branch_id::text,declared_amount,verified_amount,status,submitted_at
 FROM tempo_receipts WHERE id=$1 FOR UPDATE`, receiptID).
		Scan(&out.ID, &out.BranchID, &out.DeclaredAmount, &out.VerifiedAmount, &out.Status, &out.SubmittedAt)
	if err != nil {
		return Repayment{}, err
	}
	if out.Status != "pending" {
		return Repayment{}, ErrConflict
	}
	if decision == "reject" {
		if _, err := tx.Exec(ctx, `UPDATE tempo_receipts SET status='rejected',rejection_reason=$2
 WHERE id=$1`, receiptID, reason); err != nil {
			return Repayment{}, err
		}
		out.Status = "rejected"
	} else {
		type debt struct {
			id        string
			orderID   string
			remaining int64
		}
		debts := []debt{}
		rows, err := tx.Query(ctx, `SELECT c.id::text,c.order_id::text,c.amount-c.paid_amount
 FROM tempo_charges c JOIN orders o ON o.id=c.order_id
 WHERE c.branch_id=$1 AND c.amount>c.paid_amount
 ORDER BY o.tanggal,o.id,c.created_at,c.id FOR UPDATE OF c`, branchID)
		if err != nil {
			return Repayment{}, err
		}
		var sum int64
		for rows.Next() {
			var d debt
			if err := rows.Scan(&d.id, &d.orderID, &d.remaining); err != nil {
				rows.Close()
				return Repayment{}, err
			}
			debts = append(debts, d)
			sum += d.remaining
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return Repayment{}, err
		}
		rows.Close()
		if amount > sum && actor.Role != auth.RoleSuperadmin {
			return Repayment{}, ErrSuperadmin
		}
		remaining := amount
		for _, d := range debts {
			if remaining == 0 {
				break
			}
			a := d.remaining
			if a > remaining {
				a = remaining
			}
			if _, err := tx.Exec(ctx, `INSERT INTO tempo_allocations(branch_id,order_id,charge_id,receipt_id,amount)
 VALUES($1,$2,$3,$4,$5)`, branchID, d.orderID, d.id, receiptID, a); err != nil {
				return Repayment{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE tempo_charges SET paid_amount=paid_amount+$2 WHERE id=$1`, d.id, a); err != nil {
				return Repayment{}, err
			}
			if err := increasePaid(ctx, tx, d.orderID, a); err != nil {
				return Repayment{}, err
			}
			remaining -= a
		}
		if remaining > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO tempo_credit_entries
 (branch_id,receipt_id,amount,remaining,approved_by) VALUES($1,$2,$3,$3,$4)`,
				branchID, receiptID, remaining, actor.UserID); err != nil {
				return Repayment{}, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE tempo_receipts SET status='verified',verified_amount=$2,
 verified_by=$3,verified_at=now() WHERE id=$1`, receiptID, amount, actor.UserID); err != nil {
			return Repayment{}, err
		}
		out.Status = "verified"
		out.VerifiedAmount = &amount
	}
	if err := auditpg.Insert(ctx, tx, audit.NewEntry(ctx, actor, "tempo_receipt", receiptID,
		audit.ActionVerify, map[string]any{"status": "pending"}, map[string]any{"status": out.Status, "amount": out.VerifiedAmount, "reason": reason})); err != nil {
		return Repayment{}, err
	}
	var overdue int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount-paid_amount),0)::bigint
 FROM tempo_charges WHERE branch_id=$1
 AND due_date<(now() AT TIME ZONE 'Asia/Jakarta')::date`, branchID).Scan(&overdue); err != nil {
		return Repayment{}, err
	}
	if overdue == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM tempo_hold_grace WHERE branch_id=$1`, branchID); err != nil {
			return Repayment{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Repayment{}, err
	}
	return out, nil
}
