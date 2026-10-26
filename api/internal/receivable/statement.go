package receivable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/pdf"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/jackc/pgx/v5"
)

type Statement struct {
	ID          string    `json:"id"`
	BranchID    string    `json:"branch_id"`
	PeriodMonth string    `json:"period_month"`
	Version     int       `json:"version"`
	Total       int64     `json:"total"`
	Paid        int64     `json:"paid"`
	Remaining   int64     `json:"remaining"`
	Status      string    `json:"status"`
	Latest      bool      `json:"latest"`
	Ready       bool      `json:"ready"`
	IssuedAt    time.Time `json:"issued_at"`
}

type statementLine struct {
	chargeID string
	orderID  string
	nomor    string
	version  int
	amount   int64
	paid     int64
	due      time.Time
}

// Statements includes every immutable PDF version. The latest version shows
// current payment status; older versions retain their issue-time amounts.
func (s *Service) Statements(ctx context.Context, actor auth.Actor) ([]Statement, error) {
	if !actor.IsBranch() && !actor.Can(auth.CapAdminVerify) {
		return nil, ErrForbidden
	}
	var branch any
	if actor.IsBranch() {
		branch = actor.BranchID
	}
	rows, err := s.pool.Query(ctx, `SELECT st.id::text,st.branch_id::text,st.period_month,st.version,
 st.total_amount,st.paid_amount,COALESCE(sum(LEAST(c.paid_amount,sl.amount)),0)::bigint,
 st.storage_key IS NOT NULL,st.issued_at,
 st.version=(SELECT max(x.version) FROM tempo_statements x
 WHERE x.branch_id=st.branch_id AND x.period_month=st.period_month),
 COALESCE(bool_or(c.due_date<(now() AT TIME ZONE 'Asia/Jakarta')::date
 AND c.paid_amount<c.amount),false)
 FROM tempo_statements st
 JOIN tempo_statement_lines sl ON sl.statement_id=st.id
 JOIN orders o ON o.id=sl.order_id
 JOIN tempo_charges c ON c.id=sl.charge_id
 WHERE ($1::uuid IS NULL OR st.branch_id=$1::uuid)
 GROUP BY st.id ORDER BY st.period_month DESC,st.branch_id,st.version DESC`, branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Statement{}
	for rows.Next() {
		var st Statement
		var month time.Time
		var paidAtIssue, paidNow int64
		var overdue bool
		if err := rows.Scan(&st.ID, &st.BranchID, &month, &st.Version, &st.Total, &paidAtIssue, &paidNow,
			&st.Ready, &st.IssuedAt, &st.Latest, &overdue); err != nil {
			return nil, err
		}
		st.PeriodMonth = month.Format("2006-01-02")
		if st.Latest {
			st.Paid = paidNow
		} else {
			st.Paid = paidAtIssue
		}
		st.Remaining = st.Total - st.Paid
		switch {
		case !st.Latest:
			st.Status = "versi_lama"
		case st.Remaining <= 0:
			st.Status = "lunas"
		case overdue:
			st.Status = "lewat_jatuh_tempo"
		case month.AddDate(0, 1, 0).After(time.Now().In(jakarta)):
			st.Status = "berjalan"
		default:
			st.Status = "menunggu_bayar"
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// StatementURL issues only a signed read URL after checking session ownership.
func (s *Service) StatementURL(ctx context.Context, actor auth.Actor, store storage.Store, id string) (string, error) {
	if !actor.IsBranch() && !actor.Can(auth.CapAdminVerify) {
		return "", ErrForbidden
	}
	if !uuidPattern.MatchString(id) {
		return "", ErrNotFound
	}
	var branchID string
	var key *string
	err := s.pool.QueryRow(ctx, `SELECT branch_id::text,storage_key FROM tempo_statements WHERE id=$1`, id).Scan(&branchID, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if actor.IsBranch() && actor.BranchID != branchID {
		return "", ErrNotFound
	}
	if key == nil {
		return "", ErrConflict
	}
	return store.PresignGet(ctx, *key, 10*time.Minute)
}

// IssueMonth freezes invoice membership and due dates. A later approval or
// revision creates a new version; old statement PDFs remain immutable.
func (s *Service) IssueMonth(ctx context.Context, branchID string, month time.Time) (string, error) {
	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, jakarta)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM branches WHERE id=$1 FOR UPDATE`, branchID); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, `SELECT c.id::text,o.id::text,o.nomor,o.versi,c.amount,c.paid_amount,c.due_date
 FROM tempo_charges c JOIN orders o ON o.id=c.order_id
 WHERE c.branch_id=$1 AND o.is_tempo AND o.tempo_approved_at IS NOT NULL
 AND o.tanggal >= $2::date AND o.tanggal < ($2::date + INTERVAL '1 month')
 ORDER BY o.tanggal,o.id,c.created_at,c.id`, branchID, first)
	if err != nil {
		return "", err
	}
	lines := []statementLine{}
	var total, paid int64
	h := sha256.New()
	for rows.Next() {
		var l statementLine
		if err := rows.Scan(&l.chargeID, &l.orderID, &l.nomor, &l.version, &l.amount, &l.paid, &l.due); err != nil {
			rows.Close()
			return "", err
		}
		lines = append(lines, l)
		total += l.amount
		paid += l.paid
		_, _ = fmt.Fprintf(h, "%s/%s/%d/%d/%s;", l.chargeID, l.orderID, l.version, l.amount, l.due.Format("2006-01-02"))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	if len(lines) == 0 {
		return "", nil
	}
	fingerprint := h.Sum(nil)
	var lastID string
	var version int
	var oldHash []byte
	err = tx.QueryRow(ctx, `SELECT id::text,version,fingerprint FROM tempo_statements
 WHERE branch_id=$1 AND period_month=$2 ORDER BY version DESC LIMIT 1`, branchID, first).
		Scan(&lastID, &version, &oldHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if err == nil && bytes.Equal(fingerprint, oldHash) {
		return lastID, nil
	}
	version++
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO tempo_statements
(branch_id,period_month,version,total_amount,paid_amount,fingerprint)
 VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text`, branchID, first, version, total, paid, fingerprint).Scan(&id); err != nil {
		return "", err
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `INSERT INTO tempo_statement_lines
(statement_id,charge_id,order_id,order_version,amount,paid_amount,due_date)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, id, l.chargeID, l.orderID, l.version, l.amount, l.paid, l.due); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// RenderStatement renders and emails a single frozen version exactly once.
// A deterministic object key makes retries safe after an interrupted upload.
func (s *Service) RenderStatement(ctx context.Context, store storage.Store, renderer pdf.Renderer, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var branchID, cabang, email string
	var month time.Time
	var version int
	var key *string
	err = tx.QueryRow(ctx, `SELECT st.branch_id::text,b.nama,COALESCE(b.email::text,''),st.period_month,
 st.version,st.storage_key FROM tempo_statements st JOIN branches b ON b.id=st.branch_id
 WHERE st.id=$1 FOR UPDATE`, id).Scan(&branchID, &cabang, &email, &month, &version, &key)
	if err != nil {
		return err
	}
	if key != nil {
		return tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `SELECT o.nomor,sl.amount,sl.paid_amount,sl.due_date
 FROM tempo_statement_lines sl JOIN orders o ON o.id=sl.order_id
 WHERE sl.statement_id=$1 ORDER BY o.tanggal,o.id`, id)
	if err != nil {
		return err
	}
	var body strings.Builder
	fmt.Fprintf(&body, `<!doctype html><html><head><meta charset="utf-8"><style>
body{font-family:Arial,sans-serif;font-size:12px;color:#172b3d;padding:24px}h1{font-size:22px}table{border-collapse:collapse;width:100%%}th,td{border:1px solid #ccd5dd;padding:7px;text-align:left}th{background:#edf3f7}.right{text-align:right}</style></head><body>
<h1>Surat Tagihan Tempo</h1><p>Cabang: %s<br>Periode: %s<br>Versi: %d</p><table>
<tr><th>Invoice</th><th>Jatuh tempo</th><th class="right">Total</th><th class="right">Terbayar saat terbit</th><th class="right">Sisa saat terbit</th></tr>`,
		html.EscapeString(cabang), html.EscapeString(month.Format("January 2006")), version)
	var total, paid int64
	for rows.Next() {
		var nomor string
		var amount, amountPaid int64
		var due time.Time
		if err := rows.Scan(&nomor, &amount, &amountPaid, &due); err != nil {
			rows.Close()
			return err
		}
		total += amount
		paid += amountPaid
		fmt.Fprintf(&body, "<tr><td>%s</td><td>%s</td><td class=right>Rp %d</td><td class=right>Rp %d</td><td class=right>Rp %d</td></tr>",
			html.EscapeString(nomor), due.Format("02-01-2006"), amount, amountPaid, amount-amountPaid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	fmt.Fprintf(&body, `</table><p><strong>Total Rp %d · Terbayar Rp %d · Sisa Rp %d</strong></p>
<p>Jatuh tempo berlaku per invoice; pembayaran sebagian dialokasikan ke invoice tertua lebih dulu.</p></body></html>`, total, paid, total-paid)
	pdfBytes, err := renderer.HTMLToPDF(ctx, []byte(body.String()))
	if err != nil {
		return err
	}
	objectKey := "tempo-statement/" + id + ".pdf"
	if err := store.Put(ctx, objectKey, bytes.NewReader(pdfBytes), int64(len(pdfBytes)), "application/pdf"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE tempo_statements SET storage_key=$2 WHERE id=$1`, id, objectKey); err != nil {
		return err
	}
	payload := map[string]any{"template": "tempo_statement", "statement_id": id, "periode": month.Format("01/2006"),
		"versi": version, "cabang": cabang, "lampiran": []map[string]string{{"storage_key": objectKey, "nama": fmt.Sprintf("Surat-Tagihan-%s-v%d.pdf", month.Format("2006-01"), version), "content_type": "application/pdf"}}}
	if email != "" {
		if err := insertStatementEmail(ctx, tx, payload, "branch", []string{email}); err != nil {
			return err
		}
	}
	adminRows, err := tx.Query(ctx, `SELECT email::text FROM admin_users WHERE is_active AND notif_invoice ORDER BY email`)
	if err != nil {
		return err
	}
	admins := []string{}
	for adminRows.Next() {
		var addr string
		if err := adminRows.Scan(&addr); err != nil {
			adminRows.Close()
			return err
		}
		admins = append(admins, addr)
	}
	if err := adminRows.Err(); err != nil {
		adminRows.Close()
		return err
	}
	adminRows.Close()
	if len(admins) > 0 {
		if err := insertStatementEmail(ctx, tx, payload, "admin", admins); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func insertStatementEmail(ctx context.Context, tx pgx.Tx, payload map[string]any, recipient string, to []string) error {
	copyPayload := map[string]any{}
	for k, v := range payload {
		copyPayload[k] = v
	}
	copyPayload["recipient_kind"] = recipient
	raw, err := json.Marshal(copyPayload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO email_outbox(jenis,to_addr,subject,payload)
 VALUES('tempo_statement',$1,'Surat Tagihan Tempo — Grahita Indonesia',$2::jsonb)
 ON CONFLICT DO NOTHING`, to, string(raw))
	return err
}

// Daily reconciles all approved periods, renders missing PDFs and sends due
// reminders. It is safe to run repeatedly or after a missed schedule.
func (s *Service) Daily(ctx context.Context, store storage.Store, renderer pdf.Renderer, now time.Time) error {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT branch_id::text,date_trunc('month',tanggal)::date
 FROM orders WHERE is_tempo AND tempo_approved_at IS NOT NULL ORDER BY 1,2`)
	if err != nil {
		return err
	}
	type period struct {
		branch string
		month  time.Time
	}
	periods := []period{}
	for rows.Next() {
		var p period
		if err := rows.Scan(&p.branch, &p.month); err != nil {
			rows.Close()
			return err
		}
		periods = append(periods, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, p := range periods {
		nextMonthWIB := time.Date(p.month.Year(), p.month.Month()+1, 1, 0, 0, 0, 0, jakarta)
		if nextMonthWIB.After(now.In(jakarta)) {
			continue
		}
		if _, err := s.IssueMonth(ctx, p.branch, p.month); err != nil {
			return err
		}
	}
	pending, err := s.pool.Query(ctx, `SELECT id::text FROM tempo_statements WHERE storage_key IS NULL ORDER BY issued_at,id`)
	if err != nil {
		return err
	}
	ids := []string{}
	for pending.Next() {
		var id string
		if err := pending.Scan(&id); err != nil {
			pending.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := pending.Err(); err != nil {
		pending.Close()
		return err
	}
	pending.Close()
	for _, id := range ids {
		if err := s.RenderStatement(ctx, store, renderer, id); err != nil {
			return err
		}
	}
	return s.SendReminders(ctx, now)
}

// HexFingerprint is useful in reconciliation logs and tests.
func HexFingerprint(f []byte) string { return hex.EncodeToString(f) }
