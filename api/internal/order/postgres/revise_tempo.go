package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
	"github.com/grahita/portal/api/internal/receivable"
	"github.com/jackc/pgx/v5"
)

// reviseTempo shares the invoice line rules with legacy revisions, but keeps
// its correction in the separate Tempo ledger. The temporary submitted state
// is private to this transaction; no payment pointer is invented for Tempo.
func (r *Repo) reviseTempo(ctx context.Context, tx pgx.Tx, q *sqlc.Queries, row sqlc.LockOrderForReviseRow,
	old []barisLama, final []barisFinal, req order.ReviseRequest) (order.ReviseOutcome, error) {
	if row.TempoDueDate == nil || !row.TanggalBayarSnapshot.Valid {
		return order.ReviseOutcome{}, order.ErrInvalidData
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET status='submitted',versi=versi+1,
 verif_status='belum',alasan_review=NULL,done_at=NULL,tempo_paid_amount=0,updated_at=now()
 WHERE id=$1 AND is_tempo AND status IN ('approved_unpaid','done')`, req.OrderID); err != nil {
		return order.ReviseOutcome{}, err
	}
	if err := q.DeleteOrderLines(ctx, req.OrderID); err != nil {
		return order.ReviseOutcome{}, err
	}
	for _, l := range final {
		if l.Jenis == order.JenisPackaging {
			if err := q.InsertOrderLinePackaging(ctx, sqlc.InsertOrderLinePackagingParams{
				OrderID: req.OrderID, LineNo: int32(l.LineNo), NamaSnapshot: l.Nama, HargaSatuan: l.HargaSatuan}); err != nil {
				return order.ReviseOutcome{}, err
			}
		} else {
			if err := q.InsertOrderLineProductWithSerial(ctx, sqlc.InsertOrderLineProductWithSerialParams{
				OrderID: req.OrderID, LineNo: int32(l.LineNo), ProductID: pgtypes.UUID(l.ProductID),
				NamaSnapshot: l.Nama, NamaInstansi: l.NamaInstansi, Qty: int32(l.Qty), HargaSatuan: l.HargaSatuan, SerialNumber: pgtypes.Text(l.Serial)}); err != nil {
				return order.ReviseOutcome{}, err
			}
		}
	}
	total, err := q.SumOrderLineSubtotals(ctx, req.OrderID)
	if err != nil {
		return order.ReviseOutcome{}, err
	}
	delta := total - row.Total
	due := *row.TempoDueDate
	if delta > 0 && order.TanggalWIB(time.Now()).After(due) {
		due = receivable.NextPayDay(time.Now(), int(row.TanggalBayarSnapshot.Int16))
	}
	snapshot, err := json.Marshal(snapshotVersi(row, old))
	if err != nil {
		return order.ReviseOutcome{}, err
	}
	var revisionID string
	var created time.Time
	err = tx.QueryRow(ctx, `INSERT INTO invoice_revisions
(order_id,versi_lama,versi_baru,alasan,snapshot,delta_total,admin_id,
 settlement_contract_version,tempo_previous_paid)
 VALUES($1,$2,$3,$4,$5::jsonb,$6,$7,2,$8) RETURNING id::text,created_at`,
		req.OrderID, row.Versi, row.Versi+1, req.Alasan, string(snapshot), delta, req.AdminID, row.TempoPaidAmount).
		Scan(&revisionID, &created)
	if err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("simpan revisi Tempo: %w", err)
	}
	priorPaid := row.TempoPaidAmount
	var credit int64
	if delta > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO tempo_charges(branch_id,order_id,revision_id,amount,due_date)
 VALUES($1,$2,$3,$4,$5)`, row.BranchID, req.OrderID, revisionID, delta, due); err != nil {
			return order.ReviseOutcome{}, err
		}
	} else if delta < 0 {
		credit, err = reduceTempoCharges(ctx, tx, req.OrderID, -delta)
		if err != nil {
			return order.ReviseOutcome{}, err
		}
		if credit > 0 && req.Actor.Role != auth.RoleSuperadmin {
			return order.ReviseOutcome{}, order.ErrTempoCreditSuperadmin
		}
		priorPaid -= credit
		if credit > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO tempo_credit_entries
 (branch_id,revision_id,amount,remaining,approved_by)
 VALUES($1,$2,$3,$3,$4)`, row.BranchID, revisionID, credit, req.Actor.UserID); err != nil {
				return order.ReviseOutcome{}, err
			}
		}
	}
	if delta != 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO tempo_adjustments
 (branch_id,order_id,revision_id,delta_amount,due_date)
 VALUES($1,$2,$3,$4,$5)`, row.BranchID, req.OrderID, revisionID, delta, due); err != nil {
			return order.ReviseOutcome{}, err
		}
	}
	if priorPaid < 0 || priorPaid > total {
		return order.ReviseOutcome{}, order.ErrInvalidData
	}
	status := "approved_unpaid"
	verif := "belum"
	var doneAt any
	if priorPaid == total {
		status = "done"
		verif = "cocok"
		doneAt = time.Now().UTC()
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET status=$2::order_status,verif_status=$3::verif_status,
 tempo_paid_amount=$4,done_at=$5,updated_at=now() WHERE id=$1`,
		req.OrderID, status, verif, priorPaid, doneAt); err != nil {
		return order.ReviseOutcome{}, err
	}
	if err := receivable.ApplyAvailableCreditsTx(ctx, tx, row.BranchID); err != nil {
		return order.ReviseOutcome{}, err
	}
	if err := q.InsertOrderEvent(ctx, sqlc.InsertOrderEventParams{
		OrderID: req.OrderID, Icon: pgtypes.Text(order.IconEventRevisi), Teks: teksRevisi(req, int(row.Versi)+1),
		ActorKind: sqlc.ActorKindAdmin, ActorID: pgtypes.UUID(req.Actor.UserID)}); err != nil {
		return order.ReviseOutcome{}, err
	}
	rec := req.Audit
	rec.EntityID = req.OrderID
	rec.Before = snapshotVersi(row, old)
	rec.After = map[string]any{"versi": int(row.Versi) + 1, "total": total, "delta_total": delta,
		"tempo_credit": credit, "alasan": req.Alasan, "lines": snapshotBaris(final)}
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return order.ReviseOutcome{}, err
	}
	if _, err := r.jobs.InsertTx(ctx, tx, jobs.RevisiInvoiceArgs{OrderID: req.OrderID, Versi: int(row.Versi) + 1}, nil); err != nil {
		return order.ReviseOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return order.ReviseOutcome{}, err
	}
	return order.ReviseOutcome{OrderID: req.OrderID, Nomor: row.Nomor, Versi: int(row.Versi) + 1,
		Total: total, JumlahItem: jumlahItem(final), DeltaTotal: delta, Lines: garisKeluar(final),
		Revisi: order.Revision{ID: revisionID, VersiLama: int(row.Versi), VersiBaru: int(row.Versi) + 1,
			Alasan: req.Alasan, DeltaTotal: delta, AdminNama: req.Actor.Nama, CreatedAt: created.UTC().Format(rfc3339)}}, nil
}

// reduceTempoCharges removes a downward correction from the newest unpaid
// charges first. Once all unpaid value is gone, paid value is converted to a
// credit entry by the caller; historic allocations remain immutable.
func reduceTempoCharges(ctx context.Context, tx pgx.Tx, orderID string, reduction int64) (int64, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,amount,paid_amount FROM tempo_charges
 WHERE order_id=$1 ORDER BY created_at DESC,id DESC FOR UPDATE`, orderID)
	if err != nil {
		return 0, err
	}
	type charge struct {
		id           string
		amount, paid int64
	}
	charges := []charge{}
	for rows.Next() {
		var c charge
		if err := rows.Scan(&c.id, &c.amount, &c.paid); err != nil {
			rows.Close()
			return 0, err
		}
		charges = append(charges, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	left := reduction
	for i := range charges {
		c := &charges[i]
		n := c.amount - c.paid
		if n > left {
			n = left
		}
		if n > 0 {
			if _, err := tx.Exec(ctx, `UPDATE tempo_charges SET amount=amount-$2 WHERE id=$1`, c.id, n); err != nil {
				return 0, err
			}
			c.amount -= n
			left -= n
		}
		if left == 0 {
			break
		}
	}
	var credit int64
	if left > 0 {
		for i := range charges {
			c := &charges[i]
			n := c.paid
			if n > left {
				n = left
			}
			if n > 0 {
				if _, err := tx.Exec(ctx, `UPDATE tempo_charges SET amount=amount-$2,
 paid_amount=paid_amount-$2 WHERE id=$1`, c.id, n); err != nil {
					return 0, err
				}
				c.amount -= n
				c.paid -= n
				left -= n
				credit += n
			}
			if left == 0 {
				break
			}
		}
	}
	if left != 0 {
		return 0, fmt.Errorf("revisi melebihi nilai invoice")
	}
	return credit, nil
}
