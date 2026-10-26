package receivable

import (
	"context"
	"time"

	"github.com/grahita/portal/api/internal/auth"
)

// Balance is the live receivable for one branch and billing period. Statements
// remain immutable PDFs; payments and corrections are read from the ledger.
type Balance struct {
	BranchID    string `json:"branch_id"`
	BranchName  string `json:"branch_name"`
	PeriodMonth string `json:"period_month"`
	DueDate     string `json:"due_date"`
	Total       int64  `json:"total"`
	Paid        int64  `json:"paid"`
	Remaining   int64  `json:"remaining"`
	Status      string `json:"status"`
	AgeDays     int    `json:"age_days"`
}

type Overview struct {
	TotalReceivable int64     `json:"total_receivable"`
	Overdue         int64     `json:"overdue"`
	Age0To30        int64     `json:"age_0_30"`
	Age31To60       int64     `json:"age_31_60"`
	AgeOver60       int64     `json:"age_over_60"`
	PendingProof    int64     `json:"pending_proof"`
	HoldActive      bool      `json:"hold_active"`
	Items           []Balance `json:"items"`
}

func (s *Service) Overview(ctx context.Context, actor auth.Actor) (Overview, error) {
	if !actor.IsBranch() && !actor.Can(auth.CapAdminVerify) {
		return Overview{}, ErrForbidden
	}
	var branch any
	if actor.IsBranch() {
		branch = actor.BranchID
	}
	rows, err := s.pool.Query(ctx, `SELECT c.branch_id::text,b.nama,date_trunc('month',o.tanggal)::date,c.due_date,
 sum(c.amount)::bigint,sum(c.paid_amount)::bigint,
 (now() AT TIME ZONE 'Asia/Jakarta')::date
 FROM tempo_charges c JOIN orders o ON o.id=c.order_id
 JOIN branches b ON b.id=c.branch_id
 WHERE ($1::uuid IS NULL OR c.branch_id=$1::uuid)
 GROUP BY c.branch_id,b.nama,date_trunc('month',o.tanggal)::date,c.due_date
 HAVING sum(c.amount)>0
 ORDER BY c.due_date,c.branch_id,date_trunc('month',o.tanggal)::date`, branch)
	if err != nil {
		return Overview{}, err
	}
	defer rows.Close()
	out := Overview{Items: []Balance{}}
	for rows.Next() {
		var item Balance
		var period, due, today time.Time
		if err := rows.Scan(&item.BranchID, &item.BranchName, &period, &due, &item.Total, &item.Paid, &today); err != nil {
			return Overview{}, err
		}
		item.PeriodMonth = period.Format("2006-01")
		item.DueDate = due.Format("2006-01-02")
		item.Remaining = item.Total - item.Paid
		if item.Remaining < 0 {
			item.Remaining = 0
		}
		item.AgeDays = int(today.Sub(due).Hours() / 24)
		switch {
		case item.Remaining == 0:
			item.Status = "lunas"
		case item.AgeDays > 0:
			item.Status = "lewat_jatuh_tempo"
		case period.Year() == today.Year() && period.Month() == today.Month():
			item.Status = "berjalan"
		default:
			item.Status = "menunggu_bayar"
		}
		if item.Remaining > 0 {
			out.TotalReceivable += item.Remaining
			if item.AgeDays > 0 {
				out.Overdue += item.Remaining
			}
			switch {
			case item.AgeDays >= 0 && item.AgeDays <= 30:
				out.Age0To30 += item.Remaining
			case item.AgeDays > 30 && item.AgeDays <= 60:
				out.Age31To60 += item.Remaining
			case item.AgeDays > 60:
				out.AgeOver60 += item.Remaining
			}
		}
		out.Items = append(out.Items, item)
	}
	if err := rows.Err(); err != nil {
		return Overview{}, err
	}
	if actor.IsBranch() {
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(declared_amount),0)::bigint FROM tempo_receipts
 WHERE branch_id=$1 AND status='pending'`, actor.BranchID).Scan(&out.PendingProof); err != nil {
			return Overview{}, err
		}
		var holdEnabled, grace bool
		if err := s.pool.QueryRow(ctx, `SELECT tahan_bila_telat FROM branches WHERE id=$1`, actor.BranchID).Scan(&holdEnabled); err != nil {
			return Overview{}, err
		}
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tempo_hold_grace WHERE branch_id=$1
 AND ends_on >= (now() AT TIME ZONE 'Asia/Jakarta')::date)`, actor.BranchID).Scan(&grace); err != nil {
			return Overview{}, err
		}
		out.HoldActive = holdEnabled && out.Overdue > 0 && !(grace && out.PendingProof >= out.Overdue)
	}
	return out, nil
}
