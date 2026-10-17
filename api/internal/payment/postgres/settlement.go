package postgres

// Settlement campuran deposit + transfer di dalam transaksi verifikasi (C3;
// D3 §5.4, delta C0 §4.3.1/§4.4). Dipanggil MarkVerified sesudah cabang, akun
// deposit, dan order terkunci; seluruh tulisan di sini ikut commit/rollback
// transaksi verifikasi yang sama.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/db/sqlc"
	depositpg "github.com/grahita/portal/api/internal/deposit/postgres"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// settlementFingerprint mengikat Idempotency-Key ke isi keputusan.
func settlementFingerprint(orderID string, s *payment.SettlementInput) []byte {
	ids := append([]string(nil), s.DepositAllocationIDs...)
	sort.Strings(ids)
	splits := append([]payment.ProofSplit(nil), s.ProofSplits...)
	sort.Slice(splits, func(i, j int) bool { return splits[i].PaymentProofID < splits[j].PaymentProofID })
	raw, _ := json.Marshal([]any{"order_settlement", orderID, s.ExpectedOrderVersion, ids, splits})
	sum := sha256.Sum256(raw)
	return sum[:]
}

// creditKey menurunkan key journal per bukti: satu request boleh mengkredit
// beberapa bukti, sedangkan key journal unik per cabang.
func creditKey(key, proofID string) string { return key + "\x1f" + proofID }

type proofDecision struct {
	split payment.ProofSplit
	gross int64
	delta int64
}

// settle menulis attempt settlement Done: verifikasi log ber-breakdown,
// snapshot sumber deposit, keputusan ekonomi bukti, kredit overpay, split, dan
// pointer Done. Error apa pun membatalkan seluruh transaksi pemanggil.
func (r *Repo) settle(ctx context.Context, tx pgx.Tx, row sqlc.LockOrderForVerifyRow, req payment.MarkVerifiedRequest,
	allowlistVersion int64, fingerprint, destinationJSON []byte) (sqlc.InsertVerificationLogRow, *payment.SettlementResult, error) {
	s := req.Settlement
	if s.ExpectedOrderVersion != row.Versi {
		return sqlc.InsertVerificationLogRow{}, nil, payment.ErrOrderVersionConflict
	}

	// Alokasi aktif lintas versi dikunci menurut UUID; himpunan klien wajib
	// sama persis sehingga tidak ada debit committed yang terlewat.
	rows, err := tx.Query(ctx, `SELECT id,order_version,(amount-reversed_amount)::bigint
FROM deposit_allocations WHERE order_id=$1 ORDER BY id FOR UPDATE`, row.ID)
	if err != nil {
		return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("kunci alokasi settlement: %w", err)
	}
	var sources []payment.SettlementSource
	for rows.Next() {
		var src payment.SettlementSource
		if err := rows.Scan(&src.AllocationID, &src.AllocationOrderVersion, &src.Amount); err != nil {
			rows.Close()
			return sqlc.InsertVerificationLogRow{}, nil, err
		}
		if src.Amount > 0 {
			sources = append(sources, src)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return sqlc.InsertVerificationLogRow{}, nil, err
	}
	diminta := map[string]bool{}
	for _, id := range s.DepositAllocationIDs {
		diminta[id] = true
	}
	var depositAmount int64
	for _, src := range sources {
		if !diminta[src.AllocationID] || src.AllocationOrderVersion > row.Versi {
			return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("%w: daftar alokasi deposit tidak sama dengan alokasi aktif", payment.ErrBreakdownMismatch)
		}
		depositAmount += src.Amount
	}
	if len(sources) != len(diminta) {
		return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("%w: daftar alokasi deposit tidak sama dengan alokasi aktif", payment.ErrBreakdownMismatch)
	}

	// Keputusan ekonomi per bukti (C0 §4.3.1): approved_nominal tetap sepanjang
	// umur bukti, kredit kumulatif tidak boleh turun, journal hanya delta.
	splits := append([]payment.ProofSplit(nil), s.ProofSplits...)
	sort.Slice(splits, func(i, j int) bool { return splits[i].PaymentProofID < splits[j].PaymentProofID })
	decisions := make([]proofDecision, 0, len(splits))
	var transferAmount, newCredit int64
	for _, sp := range splits {
		d := proofDecision{split: sp}
		var approved int64
		err := tx.QueryRow(ctx, `SELECT approved_nominal FROM payment_proof_economic_decisions
WHERE payment_proof_id=$1 FOR UPDATE`, sp.PaymentProofID).Scan(&approved)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("kunci keputusan bukti: %w", err)
		case approved != sp.ApprovedNominal:
			return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("%w: nominal disetujui bukti sudah diputuskan %d", payment.ErrBreakdownMismatch, approved)
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount),0)::bigint FROM branch_deposit_transactions
WHERE kind='overpay_credit' AND source_proof_id=$1`, sp.PaymentProofID).Scan(&d.gross); err != nil {
			return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("baca kredit bukti: %w", err)
		}
		if sp.CreditedToDepositTotal < d.gross {
			return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("%w: kredit kumulatif bukti tidak boleh kurang dari %d", payment.ErrBreakdownMismatch, d.gross)
		}
		d.delta = sp.CreditedToDepositTotal - d.gross
		transferAmount += sp.AppliedToOrder
		newCredit += d.delta
		decisions = append(decisions, d)
	}
	switch {
	case depositAmount+transferAmount > row.Total:
		return sqlc.InsertVerificationLogRow{}, nil, payment.ErrOverpayDecisionRequired
	case depositAmount+transferAmount < row.Total:
		return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("%w: deposit %d + transfer %d kurang dari total %d",
			payment.ErrBreakdownMismatch, depositAmount, transferAmount, row.Total)
	}
	if newCredit > 0 {
		if !req.Actor.Can(auth.CapAdminDepositCredit) {
			return sqlc.InsertVerificationLogRow{}, nil, payment.ErrDepositCreditForbidden
		}
		if s.IdempotencyKey == "" {
			return sqlc.InsertVerificationLogRow{}, nil, payment.ErrSettlementKeyRequired
		}
	}

	var logRow sqlc.InsertVerificationLogRow
	if err := tx.QueryRow(ctx, `INSERT INTO verification_logs (order_id, metode, total_pesanan, total_terbaca,
  hasil, alasan, admin_id, catatan, order_version, deposit_amount, transfer_amount,
  branch_allowlist_version, proof_set_fingerprint, destination_results)
VALUES ($1,'manual',$2,$3,'cocok',NULL,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)
RETURNING id, order_id, metode::text, total_pesanan, COALESCE(total_terbaca,0)::bigint, selisih,
  hasil::text, ''::text, COALESCE(catatan,'')::text, admin_id, created_at,
  branch_allowlist_version, destination_results`,
		row.ID, row.Total, pgtypes.Int8Ptr(req.TotalTerbaca), adminIDVerifikasi(req), pgtypes.Text(req.Catatan),
		row.Versi, depositAmount, transferAmount, allowlistVersion, fingerprint, destinationJSON).
		Scan(&logRow.ID, &logRow.OrderID, &logRow.Metode, &logRow.TotalPesanan, &logRow.TotalTerbaca,
			&logRow.Selisih, &logRow.Hasil, &logRow.Alasan, &logRow.Catatan, &logRow.AdminID,
			&logRow.CreatedAt, &logRow.BranchAllowlistVersion, &logRow.DestinationResults); err != nil {
		return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("tulis log settlement: %w", err)
	}

	for _, src := range sources {
		if _, err := tx.Exec(ctx, `INSERT INTO verification_deposit_sources(
verification_log_id,order_id,settlement_order_version,settlement_total,
allocation_id,branch_id,allocation_order_version,amount_snapshot)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, logRow.ID, row.ID, row.Versi, row.Total,
			src.AllocationID, row.BranchID, src.AllocationOrderVersion, src.Amount); err != nil {
			return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("tulis sumber deposit settlement: %w", err)
		}
	}

	requestFingerprint := settlementFingerprint(row.ID, s)
	result := &payment.SettlementResult{DepositAmount: depositAmount, TransferAmount: transferAmount,
		NewCreditToDeposit: newCredit, Sources: sources, Splits: make([]payment.SettlementSplit, 0, len(decisions))}
	if result.Sources == nil {
		result.Sources = []payment.SettlementSource{}
	}
	for _, d := range decisions {
		sp := d.split
		if _, err := tx.Exec(ctx, `INSERT INTO payment_proof_economic_decisions
(payment_proof_id,order_id,branch_id,approved_nominal,decided_by)
VALUES($1,$2,$3,$4,$5) ON CONFLICT (payment_proof_id) DO NOTHING`,
			sp.PaymentProofID, row.ID, row.BranchID, sp.ApprovedNominal, req.Actor.UserID); err != nil {
			return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("tulis keputusan bukti: %w", err)
		}
		var creditID any
		if d.delta > 0 {
			credit, _, err := depositpg.CreditOverpayLocked(ctx, tx, depositpg.OverpayCredit{
				BranchID: row.BranchID, OrderID: row.ID, OrderVersion: row.Versi,
				VerificationLogID: logRow.ID, ProofID: sp.PaymentProofID, Amount: d.delta,
				IdempotencyKey: creditKey(s.IdempotencyKey, sp.PaymentProofID), RequestFingerprint: requestFingerprint,
				ActorID: req.Actor.UserID, Reason: req.Catatan,
			})
			if err != nil {
				return sqlc.InsertVerificationLogRow{}, nil, err
			}
			creditID = credit.ID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO verification_proof_splits(
verification_log_id,order_id,order_version,settlement_total,settlement_deposit_amount,
settlement_transfer_amount,payment_proof_id,branch_id,approved_nominal,applied_to_order,
credited_to_deposit_total,new_credit_to_deposit,new_credit_transaction_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			logRow.ID, row.ID, row.Versi, row.Total, depositAmount, transferAmount,
			sp.PaymentProofID, row.BranchID, sp.ApprovedNominal, sp.AppliedToOrder,
			sp.CreditedToDepositTotal, d.delta, creditID); err != nil {
			return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("tulis split bukti: %w", err)
		}
		result.Splits = append(result.Splits, payment.SettlementSplit{PaymentProofID: sp.PaymentProofID,
			ApprovedNominal: sp.ApprovedNominal, AppliedToOrder: sp.AppliedToOrder,
			CreditedToDepositTotal: sp.CreditedToDepositTotal, NewCreditToDeposit: d.delta})
	}

	tag, err := tx.Exec(ctx, `UPDATE orders
SET status='done', verif_status='cocok', alasan_review=NULL,
    revision_payment_state=NULL, revision_payment_started_at=NULL, done_at=now(),
    done_verification_log_id=$2, done_order_version=$3,
    done_deposit_amount=$4, done_transfer_amount=$5, updated_at=now()
WHERE id=$1 AND status='submitted'`, row.ID, logRow.ID, row.Versi, depositAmount, transferAmount)
	if err != nil {
		return sqlc.InsertVerificationLogRow{}, nil, fmt.Errorf("tandai Done settlement: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return sqlc.InsertVerificationLogRow{}, nil, payment.ErrAlreadyDone
	}
	return logRow, result, nil
}

// settlementReplay mengembalikan hasil settlement yang sudah commit bila retry
// membawa Idempotency-Key dan isi keputusan yang sama (D3 §3.7 butir 4).
func (r *Repo) settlementReplay(ctx context.Context, tx pgx.Tx, row sqlc.LockOrderForVerifyRow, req payment.MarkVerifiedRequest) (payment.VerifyOutcome, bool, error) {
	s := req.Settlement
	if s == nil || s.IdempotencyKey == "" {
		return payment.VerifyOutcome{}, false, nil
	}
	credits, err := tx.Query(ctx, `SELECT t.source_proof_id::text,t.idempotency_key_hash,t.request_fingerprint
FROM branch_deposit_transactions t JOIN orders o ON o.done_verification_log_id=t.order_verification_log_id
WHERE o.id=$1 AND t.kind='overpay_credit'`, row.ID)
	if err != nil {
		return payment.VerifyOutcome{}, false, err
	}
	matched, reused := false, false
	want := settlementFingerprint(row.ID, s)
	for credits.Next() {
		var proofID string
		var keyHash, stored []byte
		if err := credits.Scan(&proofID, &keyHash, &stored); err != nil {
			credits.Close()
			return payment.VerifyOutcome{}, false, err
		}
		sum := sha256.Sum256([]byte(creditKey(s.IdempotencyKey, proofID)))
		if bytes.Equal(keyHash, sum[:]) {
			matched = true
			reused = reused || !bytes.Equal(stored, want)
		}
	}
	credits.Close()
	if err := credits.Err(); err != nil {
		return payment.VerifyOutcome{}, false, err
	}
	if !matched {
		return payment.VerifyOutcome{}, false, nil
	}
	if reused {
		return payment.VerifyOutcome{}, false, payment.ErrIdempotencyReused
	}
	out, err := r.doneSettlementOutcome(ctx, tx, row.ID)
	if err != nil {
		return payment.VerifyOutcome{}, false, err
	}
	out.Settlement.Replayed = true
	return out, true, nil
}

// doneSettlementOutcome merekonstruksi outcome dari pointer Done immutable.
func (r *Repo) doneSettlementOutcome(ctx context.Context, tx pgx.Tx, orderID string) (payment.VerifyOutcome, error) {
	var out payment.VerifyOutcome
	var set payment.SettlementResult
	var destinationJSON []byte
	var totalTerbaca *int64
	var catatan, adminNama *string
	if err := tx.QueryRow(ctx, `SELECT o.id::text,COALESCE(o.nomor,''),o.versi,o.branch_id::text,o.status::text,
 o.verif_status::text,o.total,v.id::text,v.metode::text,v.total_pesanan,v.total_terbaca,v.hasil::text,
 v.catatan,v.created_at,COALESCE(v.branch_allowlist_version,0),v.destination_results,
 o.done_deposit_amount,o.done_transfer_amount,au.nama
FROM orders o JOIN verification_logs v ON v.id=o.done_verification_log_id
LEFT JOIN admin_users au ON au.id=v.admin_id
WHERE o.id=$1`, orderID).Scan(&out.Order.ID, &out.Order.Nomor, &out.Order.Versi, &out.Order.BranchID,
		&out.Order.Status, &out.Order.VerifStatus, &out.Order.Total, &out.Log.ID, &out.Log.Metode,
		&out.Log.TotalPesanan, &totalTerbaca, &out.Log.Hasil, &catatan, &out.Log.CreatedAt,
		&out.Log.BranchAllowlistVersion, &destinationJSON, &set.DepositAmount, &set.TransferAmount, &adminNama); err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("baca settlement Done: %w", err)
	}
	out.Log.OrderID = orderID
	if totalTerbaca != nil {
		out.Log.TotalTerbaca = *totalTerbaca
		out.Log.Selisih = *totalTerbaca - out.Log.TotalPesanan
	}
	if catatan != nil {
		out.Log.Catatan = *catatan
	}
	if adminNama != nil {
		out.Log.AdminNama = *adminNama
	}
	_ = json.Unmarshal(destinationJSON, &out.Log.DestinationResults)
	set.Sources = []payment.SettlementSource{}
	rows, err := tx.Query(ctx, `SELECT allocation_id::text,allocation_order_version,amount_snapshot
FROM verification_deposit_sources WHERE verification_log_id=$1 ORDER BY allocation_id`, out.Log.ID)
	if err != nil {
		return payment.VerifyOutcome{}, err
	}
	for rows.Next() {
		var src payment.SettlementSource
		if err := rows.Scan(&src.AllocationID, &src.AllocationOrderVersion, &src.Amount); err != nil {
			rows.Close()
			return payment.VerifyOutcome{}, err
		}
		set.Sources = append(set.Sources, src)
	}
	rows.Close()
	set.Splits = []payment.SettlementSplit{}
	rows, err = tx.Query(ctx, `SELECT payment_proof_id::text,approved_nominal,applied_to_order,
credited_to_deposit_total,new_credit_to_deposit
FROM verification_proof_splits WHERE verification_log_id=$1 ORDER BY payment_proof_id`, out.Log.ID)
	if err != nil {
		return payment.VerifyOutcome{}, err
	}
	for rows.Next() {
		var sp payment.SettlementSplit
		if err := rows.Scan(&sp.PaymentProofID, &sp.ApprovedNominal, &sp.AppliedToOrder,
			&sp.CreditedToDepositTotal, &sp.NewCreditToDeposit); err != nil {
			rows.Close()
			return payment.VerifyOutcome{}, err
		}
		set.NewCreditToDeposit += sp.NewCreditToDeposit
		set.Splits = append(set.Splits, sp)
	}
	rows.Close()
	out.Settlement = &set
	return out, nil
}
