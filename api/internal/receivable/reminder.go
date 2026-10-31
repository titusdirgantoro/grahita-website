package receivable

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// SendReminders sends one email per branch, due date and stage. For pay days
// 1–3, H-3 is skipped because the monthly statement has not yet been issued;
// the first reminder for those branches is sent on the due date.
func (s *Service) SendReminders(ctx context.Context, now time.Time) error {
	today := now.In(jakarta)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, jakarta)
	stages := []struct {
		name   string
		offset int
	}{{"h_minus_3", 3}, {"due", 0}, {"h_plus_1", -1}, {"h_plus_7", -7}}
	for _, stage := range stages {
		due := today.AddDate(0, 0, stage.offset)
		// Cabang bertanggal bayar 1–3 belum memiliki Surat Tagihan final pada
		// H-3 (tanggalnya masih berada pada bulan sebelumnya). Sesuai keputusan
		// bisnis, tahap ini dilewati seluruhnya; pengingat pertama adalah hari H.
		if stage.name == "h_minus_3" && due.Day() <= 3 {
			continue
		}
		rows, err := s.pool.Query(ctx, `SELECT b.id::text,b.email::text,
 COALESCE(sum(o.amount-o.paid_amount),0)::bigint
 FROM branches b JOIN tempo_charges o ON o.branch_id=b.id
 AND o.amount>o.paid_amount AND o.due_date=$1::date
 WHERE b.is_active AND b.email IS NOT NULL
 GROUP BY b.id,b.email`, due)
		if err != nil {
			return err
		}
		type candidate struct {
			id, email string
			amount    int64
		}
		candidates := []candidate{}
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.email, &c.amount); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, c)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, c := range candidates {
			generic := false
			if c.amount == 0 && !generic {
				continue
			}
			if err := s.enqueueReminder(ctx, c.id, c.email, due, stage.name, c.amount, generic); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) enqueueReminder(ctx context.Context, branchID, email string, due time.Time, stage string, amount int64, generic bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var inserted string
	err = tx.QueryRow(ctx, `INSERT INTO tempo_reminders(branch_id,due_date,stage,amount,generic)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING RETURNING stage`, branchID, due, stage, amount, generic).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	payload := map[string]any{"template": "tempo_reminder", "branch_id": branchID,
		"due_date": due.Format("2006-01-02"), "stage": stage, "amount": strconv.FormatInt(amount, 10), "generic": generic}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO email_outbox(jenis,to_addr,subject,payload)
 VALUES('tempo_reminder',$1,'Pengingat jatuh tempo tagihan — Grahita Indonesia',$2::jsonb)
 ON CONFLICT DO NOTHING`, []string{email}, string(raw)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
