// Package receivable owns Tempo approval, repayment and credit allocation.
// All monetary changes are serialized by the branch row and written with their
// source allocation in one PostgreSQL transaction.
package receivable

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/jobs"
)

var (
	ErrForbidden      = errors.New("tidak berhak mengelola tagihan Tempo")
	ErrNotFound       = errors.New("tagihan atau pesanan Tempo tidak ditemukan")
	ErrInvalid        = errors.New("data pelunasan Tempo tidak valid")
	ErrConflict       = errors.New("status tagihan Tempo sudah berubah")
	ErrSuperadmin     = errors.New("kelebihan bayar Tempo memerlukan keputusan Superadmin")
	ErrNoBankAccount  = errors.New("cabang Tempo tidak memiliki rekening tujuan aktif")
	ErrDuplicateProof = errors.New("bukti pelunasan Tempo ini sudah pernah diunggah")
)

type JobInserter interface {
	InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

type Service struct {
	pool *database.Pool
	jobs JobInserter
}

func New(pool *database.Pool, jobs JobInserter) *Service { return &Service{pool: pool, jobs: jobs} }

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Approve releases a submitted Tempo order for fulfilment without recording a
// transfer. Credit already approved for this branch is applied FIFO first.
func (s *Service) Approve(ctx context.Context, actor auth.Actor, orderID string) (string, error) {
	if !actor.Can(auth.CapAdminVerify) {
		return "", ErrForbidden
	}
	if !uuidPattern.MatchString(orderID) {
		return "", ErrNotFound
	}
	if s.jobs == nil {
		return "", fmt.Errorf("penjadwal dokumen Tempo belum tersedia")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var branchID string
	if err := tx.QueryRow(ctx, `SELECT branch_id::text FROM orders WHERE id=$1`, orderID).Scan(&branchID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM branches WHERE id=$1 FOR UPDATE`, branchID); err != nil {
		return "", err
	}
	var submitted time.Time
	var status string
	var isTempo bool
	var payDay int16
	var total int64
	if err := tx.QueryRow(ctx, `SELECT tanggal,status::text,is_tempo,tanggal_bayar_snapshot,total
FROM orders WHERE id=$1 AND branch_id=$2 FOR UPDATE`, orderID, branchID).
		Scan(&submitted, &status, &isTempo, &payDay, &total); err != nil {
		return "", err
	}
	if !isTempo {
		return "", ErrNotFound
	}
	if status != "submitted" {
		return "", ErrConflict
	}
	var hasAccount bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM branch_bank_accounts bba
 JOIN bank_accounts ba ON ba.id=bba.bank_account_id
 WHERE bba.branch_id=$1 AND bba.is_allowed AND ba.is_active)`, branchID).Scan(&hasAccount); err != nil {
		return "", err
	}
	if !hasAccount {
		return "", ErrNoBankAccount
	}
	now := time.Now().UTC()
	due := DueDate(submitted, now, int(payDay))
	if total == 0 {
		_, err = tx.Exec(ctx, `UPDATE orders SET status='done',verif_status='cocok',done_at=$2,
 tempo_approved_at=$2,tempo_due_date=$3,updated_at=$2 WHERE id=$1`, orderID, now, due)
	} else {
		_, err = tx.Exec(ctx, `UPDATE orders SET status='approved_unpaid',tempo_approved_at=$2,
 tempo_due_date=$3,updated_at=$2 WHERE id=$1`, orderID, now, due)
	}
	if err != nil {
		return "", fmt.Errorf("setujui pesanan Tempo: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tempo_charges(branch_id,order_id,amount,due_date)
VALUES($1,$2,$3,$4)`, branchID, orderID, total, due); err != nil {
		return "", err
	}
	if total > 0 {
		if err := ApplyAvailableCreditsTx(ctx, tx, branchID); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO order_events
(order_id,at,icon,teks,actor_kind,actor_id) VALUES ($1,$2,'selesai',
'Pesanan Tempo disetujui; invoice dan surat jalan diterbitkan','admin',$3)`, orderID, now, actor.UserID); err != nil {
		return "", err
	}
	if err := auditpg.Insert(ctx, tx, audit.NewEntry(ctx, actor, audit.EntityOrder, orderID,
		audit.ActionVerify, map[string]any{"status": "submitted"},
		map[string]any{"status": "approved_unpaid", "tempo_due_date": due.Format("2006-01-02")})); err != nil {
		return "", err
	}
	if _, err := s.jobs.InsertTx(ctx, tx, jobs.RenderDokumenArgs{OrderID: orderID}, nil); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return due.Format("2006-01-02"), nil
}

// ApplyAvailableCreditsTx applies approved Tempo credit to the oldest open
// invoice, then to its oldest charge. Caller holds the branch row lock.
func ApplyAvailableCreditsTx(ctx context.Context, tx pgx.Tx, branchID string) error {
	creditRows, err := tx.Query(ctx, `SELECT id::text,remaining FROM tempo_credit_entries
WHERE branch_id=$1 AND remaining>0 ORDER BY created_at,id FOR UPDATE`, branchID)
	if err != nil {
		return err
	}
	type credit struct {
		id     string
		amount int64
	}
	credits := []credit{}
	for creditRows.Next() {
		var c credit
		if err := creditRows.Scan(&c.id, &c.amount); err != nil {
			creditRows.Close()
			return err
		}
		credits = append(credits, c)
	}
	if err := creditRows.Err(); err != nil {
		creditRows.Close()
		return err
	}
	creditRows.Close()
	chargeRows, err := tx.Query(ctx, `SELECT c.id::text,c.order_id::text,c.amount-c.paid_amount
FROM tempo_charges c JOIN orders o ON o.id=c.order_id
WHERE c.branch_id=$1 AND c.amount>c.paid_amount AND o.status='approved_unpaid'
ORDER BY o.tanggal,o.id,c.created_at,c.id FOR UPDATE OF c`, branchID)
	if err != nil {
		return err
	}
	type charge struct {
		id, orderID string
		remaining   int64
	}
	charges := []charge{}
	for chargeRows.Next() {
		var c charge
		if err := chargeRows.Scan(&c.id, &c.orderID, &c.remaining); err != nil {
			chargeRows.Close()
			return err
		}
		charges = append(charges, c)
	}
	if err := chargeRows.Err(); err != nil {
		chargeRows.Close()
		return err
	}
	chargeRows.Close()
	chargeIndex := 0
	for _, credit := range credits {
		left := credit.amount
		for left > 0 && chargeIndex < len(charges) {
			charge := &charges[chargeIndex]
			if charge.remaining == 0 {
				chargeIndex++
				continue
			}
			amount := left
			if amount > charge.remaining {
				amount = charge.remaining
			}
			if _, err := tx.Exec(ctx, `INSERT INTO tempo_allocations(branch_id,order_id,charge_id,credit_entry_id,amount)
VALUES($1,$2,$3,$4,$5)`, branchID, charge.orderID, charge.id, credit.id, amount); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE tempo_charges SET paid_amount=paid_amount+$2 WHERE id=$1`, charge.id, amount); err != nil {
				return err
			}
			if err := increasePaid(ctx, tx, charge.orderID, amount); err != nil {
				return err
			}
			charge.remaining -= amount
			left -= amount
		}
		if left != credit.amount {
			if _, err := tx.Exec(ctx, `UPDATE tempo_credit_entries SET remaining=$2 WHERE id=$1`, credit.id, left); err != nil {
				return err
			}
		}
	}
	return nil
}

func increasePaid(ctx context.Context, tx pgx.Tx, orderID string, amount int64) error {
	tag, err := tx.Exec(ctx, `UPDATE orders SET tempo_paid_amount=tempo_paid_amount+$2,
 status=CASE WHEN tempo_paid_amount+$2=total THEN 'done'::order_status ELSE status END,
 verif_status=CASE WHEN tempo_paid_amount+$2=total THEN 'cocok'::verif_status ELSE verif_status END,
 done_at=CASE WHEN tempo_paid_amount+$2=total THEN now() ELSE done_at END,
 updated_at=now() WHERE id=$1 AND is_tempo AND status='approved_unpaid'
 AND tempo_paid_amount+$2<=total`, orderID, amount)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
