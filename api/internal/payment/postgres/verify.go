package postgres

// Berkas ini memuat transaksi VERIFIKASI PEMBAYARAN (kartu F1-18;
// SystemDesign.md §4.3–4.4; Database.md §4.4–4.6).
//
// SATU permintaan verifikasi = SATU transaksi. Urutannya:
//
//  1. kunci baris pesanan (`FOR UPDATE`) dan baca keadaan sebelumnya;
//  2. tolak dengan ORDER_ALREADY_DONE bila sudah berstatus done (verifikasi
//     kedua tidak boleh menghasilkan log/dokumen/email kedua), dan
//     ORDER_NOT_SUBMITTED bila pesannya masih draft;
//  3. tulis transisi (status done + verif cocok + done_at, ATAU verif
//     perlu_review + alasan);
//  4. tulis log verifikasi (total_pesanan dibaca dari baris yang dikunci,
//     selisih dihitung kolom generated);
//  5. tulis peristiwa timeline + entri audit;
//  6. TITIPKAN pekerjaan River "render dokumen pesanan lalu antre email
//     dokumen" pada transaksi YANG SAMA, sehingga tidak mungkin ada pesanan
//     selesai tanpa pekerjaan dokumennya (pola SystemDesign.md §4.2/§5.1).
//
// Langkah 6 memakai `InsertTx` dari klien River sehingga baris river_job ikut
// rollback bila transaksi batal. Bila penjadwal pekerjaan belum terpasang
// (mis. konfigurasi salah), verifikasi DITOLAK alih-alih menyelesaikan pesanan
// tanpa dokumen dan email — gagal terang-terangan, bukan setengah jalan.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/db/sqlc"
	depositpg "github.com/grahita/portal/api/internal/deposit/postgres"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// JobInserter menitipkan pekerjaan River DI DALAM transaksi yang sedang
// berjalan. Dipenuhi oleh *river.Client[pgx.Tx]; dipisah menjadi interface
// supaya repository bisa diuji tanpa River.
type JobInserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// Repo memenuhi kontrak verifikasi domain pembayaran.
var _ payment.Verifier = (*Repo)(nil)

// Option mengubah perilaku repository bukti/verifikasi.
type Option func(*Repo)

// WithJobQueue memasang penjadwal pekerjaan River (transisi Done menitipkan
// job render dokumen pada transaksi verifikasi).
func WithJobQueue(q JobInserter) Option {
	return func(r *Repo) { r.jobs = q }
}

// MarkVerified menjalankan verifikasi pembayaran satu pesanan dalam SATU
// transaksi (lihat catatan berkas ini).
func (r *Repo) MarkVerified(ctx context.Context, req payment.MarkVerifiedRequest) (payment.VerifyOutcome, error) {
	if r.jobs == nil {
		return payment.VerifyOutcome{}, fmt.Errorf("%w: penjadwal pekerjaan dokumen belum terpasang", payment.ErrQueueUnavailable)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("mulai transaksi verifikasi: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(22102, 2)`); err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("kunci kebijakan rekening: %w", err)
	}
	var branchID string
	if err := tx.QueryRow(ctx, `SELECT branch_id FROM orders WHERE id=$1`, req.OrderID).Scan(&branchID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return payment.VerifyOutcome{}, payment.ErrNotFound
		}
		return payment.VerifyOutcome{}, err
	}
	var allowlistVersion int64
	if err := tx.QueryRow(ctx, `SELECT bank_allowlist_version FROM branches WHERE id=$1 FOR SHARE`, branchID).Scan(&allowlistVersion); err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("kunci allowlist cabang: %w", err)
	}
	if req.Hasil == payment.VerifCocok {
		// Urutan kunci global D3 §5.1: akun deposit sebelum order, supaya
		// settlement tidak berebut kunci terbalik dengan alokasi/reversal.
		if _, err := depositpg.LockAccount(ctx, tx, branchID); err != nil {
			return payment.VerifyOutcome{}, err
		}
	}

	// 1. Kunci baris pesanan: hanya satu verifikasi per pesanan dalam satu
	//    waktu, dan seluruh keputusan di bawah dibaca dari baris terkunci.
	row, err := q.LockOrderForVerify(ctx, req.OrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.VerifyOutcome{}, payment.ErrNotFound
	}
	if err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("query LockOrderForVerify: %w", err)
	}

	// 2. Keadaan yang menentukan boleh-tidaknya transisi.
	switch row.Status {
	case payment.StatusDone:
		if out, ok, err := r.settlementReplay(ctx, tx, row, req); err != nil || ok {
			if err != nil {
				return payment.VerifyOutcome{}, err
			}
			return out, tx.Commit(ctx)
		}
		return payment.VerifyOutcome{}, payment.ErrAlreadyDone
	case payment.StatusSubmitted:
		// lanjut
	default:
		return payment.VerifyOutcome{}, payment.ErrNotSubmitted
	}
	if req.Hasil == payment.VerifCocok {
		var revisionPending, hasActiveAllocation bool
		if err := tx.QueryRow(ctx, `SELECT revision_payment_state IS NOT NULL,
EXISTS(SELECT 1 FROM deposit_allocations a
       WHERE a.order_id=orders.id AND a.amount>a.reversed_amount)
FROM orders WHERE id=$1`, req.OrderID).Scan(&revisionPending, &hasActiveAllocation); err != nil {
			return payment.VerifyOutcome{}, fmt.Errorf("periksa settlement deposit: %w", err)
		}
		if (revisionPending || hasActiveAllocation) && req.Settlement == nil {
			if req.Metode == payment.MetodeAI {
				return payment.VerifyOutcome{}, fmt.Errorf("%w: settlement deposit harus diorkestrasi atomik", payment.ErrInvalidData)
			}
			return payment.VerifyOutcome{}, fmt.Errorf("%w: pesanan memakai deposit atau revisi; kirim rincian settlement", payment.ErrBreakdownMismatch)
		}
	}
	destinationResults, fingerprint, finalHasil, finalAlasan, err := r.validateDestinations(ctx, tx, req, row.BranchID)
	if err != nil {
		return payment.VerifyOutcome{}, err
	}
	if req.ExpectedAllowlistVersion > 0 && req.ExpectedAllowlistVersion != allowlistVersion {
		return payment.VerifyOutcome{}, payment.ErrOCRSnapshotStale
	}
	if req.ExpectedProofFingerprint != "" && req.ExpectedProofFingerprint != hex.EncodeToString(fingerprint) {
		return payment.VerifyOutcome{}, payment.ErrOCRSnapshotStale
	}
	req.Hasil, req.Alasan = finalHasil, finalAlasan

	// Log dibuat sebelum transisi Done agar pointer historis dapat ditulis dalam
	// update status yang sama. Seluruhnya tetap satu transaksi.
	destinationJSON, err := json.Marshal(destinationResults)
	if err != nil {
		return payment.VerifyOutcome{}, err
	}
	//    dari angka yang dikirim klien) supaya selisih selalu benar.
	var logRow sqlc.InsertVerificationLogRow
	var settlement *payment.SettlementResult
	if req.Settlement != nil {
		logRow, settlement, err = r.settle(ctx, tx, row, req, allowlistVersion, fingerprint, destinationJSON)
		if err != nil {
			return payment.VerifyOutcome{}, err
		}
	} else {
		logRow, err = q.InsertVerificationLog(ctx, sqlc.InsertVerificationLogParams{
			OrderID:                req.OrderID,
			Metode:                 sqlc.VerifyMethod(req.Metode),
			TotalPesanan:           row.Total,
			TotalTerbaca:           pgtypes.Int8Ptr(req.TotalTerbaca),
			Hasil:                  sqlc.VerifStatus(req.Hasil),
			Alasan:                 sqlc.NullReviewReason{ReviewReason: sqlc.ReviewReason(req.Alasan), Valid: req.Alasan != ""},
			AdminID:                adminIDVerifikasi(req),
			Catatan:                pgtypes.Text(req.Catatan),
			OrderVersion:           row.Versi,
			TransferAmount:         row.Total,
			BranchAllowlistVersion: pgtype.Int8{Int64: allowlistVersion, Valid: true},
			ProofSetFingerprint:    fingerprint,
			DestinationResults:     destinationJSON,
		})
		if err != nil {
			if req.Metode == payment.MetodeAI && isDuplicate(err) {
				// Dua worker review dapat sama-sama melewati precheck. Unique index
				// menjadi arbiter akhir; transaksi pecundang dibatalkan lalu membaca
				// outcome immutable yang sudah dikomit worker pemenang.
				_ = tx.Rollback(ctx)
				return r.existingAIOutcome(ctx, req.OrderID, allowlistVersion, fingerprint)
			}
			return payment.VerifyOutcome{}, fmt.Errorf("query InsertVerificationLog: %w", err)
		}

		switch req.Hasil {
		case payment.VerifCocok:
			changed, err := q.SetOrderDone(ctx, sqlc.SetOrderDoneParams{
				OrderID: req.OrderID, VerificationLogID: logRow.ID,
				OrderVersion: row.Versi, TransferAmount: row.Total,
			})
			if err != nil {
				return payment.VerifyOutcome{}, fmt.Errorf("query SetOrderDone: %w", err)
			}
			if changed == 0 {
				return payment.VerifyOutcome{}, payment.ErrAlreadyDone
			}
		case payment.VerifPerluReview:
			changed, err := q.SetOrderPerluReview(ctx, sqlc.SetOrderPerluReviewParams{Alasan: sqlc.ReviewReason(req.Alasan), ID: req.OrderID})
			if err != nil {
				return payment.VerifyOutcome{}, fmt.Errorf("query SetOrderPerluReview: %w", err)
			}
			if changed == 0 {
				return payment.VerifyOutcome{}, payment.ErrNotSubmitted
			}
		default:
			return payment.VerifyOutcome{}, payment.ErrHasilInvalid
		}

	}

	// 5a. Timeline: nama pelaku + keputusannya.
	if err := q.InsertVerifyOrderEvent(ctx, sqlc.InsertVerifyOrderEventParams{
		OrderID:   req.OrderID,
		Icon:      pgtypes.Text(ikonVerifikasi(req.Hasil)),
		Teks:      teksVerifikasi(req),
		ActorKind: actorKindVerifikasi(req),
		ActorID:   pgtypes.UUID(req.Actor.UserID),
	}); err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("query InsertVerifyOrderEvent: %w", err)
	}

	// 5b. Audit: Before/After diambil dari baris yang dikunci + hasil akhir,
	//     sehingga entri audit tidak pernah memuat snapshot basi.
	rec := req.Audit
	rec.EntityID = req.OrderID
	rec.Before = snapshotVerifikasi(row)
	after := map[string]any{
		"status":        statusSesudah(req.Hasil, row.Status),
		"verif_status":  req.Hasil,
		"total":         row.Total,
		"total_terbaca": int64(0),
		"selisih":       int64(0),
		"hasil":         req.Hasil,
		"metode":        req.Metode,
	}
	if req.TotalTerbaca != nil {
		after["total_terbaca"] = *req.TotalTerbaca
	}
	if logRow.Selisih.Valid {
		after["selisih"] = logRow.Selisih.Int64
	}
	if req.Alasan != "" {
		after["alasan_review"] = req.Alasan
	}
	if settlement != nil {
		after["deposit_amount"] = settlement.DepositAmount
		after["transfer_amount"] = settlement.TransferAmount
		after["new_credit_to_deposit"] = settlement.NewCreditToDeposit
		after["proof_splits"] = settlement.Splits
		after["deposit_sources"] = settlement.Sources
	}
	rec.After = after
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return payment.VerifyOutcome{}, err
	}

	// 6. Pekerjaan lanjutan: render dokumen + antre email, dititipkan pada
	//    transaksi yang sama. Hanya persetujuan yang melahirkan dokumen dan
	//    email (perlu review jelas belum boleh menyelesaikan pesanan).
	if req.Hasil == payment.VerifCocok {
		if _, err := r.jobs.InsertTx(ctx, tx, jobs.RenderDokumenArgs{OrderID: req.OrderID}, nil); err != nil {
			return payment.VerifyOutcome{}, fmt.Errorf("titipkan pekerjaan render dokumen: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("commit verifikasi: %w", err)
	}

	return payment.VerifyOutcome{
		Order: payment.VerifiedOrder{
			ID:           row.ID,
			Nomor:        row.Nomor,
			Versi:        int(row.Versi),
			BranchID:     row.BranchID,
			Status:       statusSesudah(req.Hasil, row.Status),
			VerifStatus:  req.Hasil,
			AlasanReview: req.Alasan,
			Total:        row.Total,
		},
		Log:        toVerificationLog(logRow, req.Actor.Nama),
		Settlement: settlement,
	}, nil
}

func (r *Repo) existingAIOutcome(ctx context.Context, orderID string, version int64, fingerprint []byte) (payment.VerifyOutcome, error) {
	var out payment.VerifyOutcome
	var totalTerbaca *int64
	var alasan, catatan *string
	var destinationJSON []byte
	err := r.pool.QueryRow(ctx, `SELECT o.id::text,COALESCE(o.nomor,''),o.versi,o.branch_id::text,
 o.status::text,o.verif_status::text,COALESCE(o.alasan_review::text,''),o.total,
 v.id::text,v.metode::text,v.total_pesanan,v.total_terbaca,v.hasil::text,
 v.alasan::text,v.catatan,v.created_at,v.destination_results
 FROM orders o JOIN verification_logs v ON v.order_id=o.id
 WHERE o.id=$1 AND v.metode='ai' AND v.branch_allowlist_version=$2
   AND v.proof_set_fingerprint=$3`, orderID, version, fingerprint).Scan(
		&out.Order.ID, &out.Order.Nomor, &out.Order.Versi, &out.Order.BranchID,
		&out.Order.Status, &out.Order.VerifStatus, &out.Order.AlasanReview, &out.Order.Total,
		&out.Log.ID, &out.Log.Metode, &out.Log.TotalPesanan, &totalTerbaca, &out.Log.Hasil,
		&alasan, &catatan, &out.Log.CreatedAt, &destinationJSON)
	if err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("baca outcome AI final: %w", err)
	}
	out.Log.OrderID = orderID
	out.Log.BranchAllowlistVersion = version
	if totalTerbaca != nil {
		out.Log.TotalTerbaca = *totalTerbaca
		out.Log.Selisih = *totalTerbaca - out.Log.TotalPesanan
	}
	if alasan != nil {
		out.Log.Alasan = *alasan
	}
	if catatan != nil {
		out.Log.Catatan = *catatan
	}
	if err := json.Unmarshal(destinationJSON, &out.Log.DestinationResults); err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("urai outcome AI final: %w", err)
	}
	return out, nil
}

// ikonVerifikasi memilih ikon timeline untuk keputusan verifikasi.
func (r *Repo) validateDestinations(ctx context.Context, tx pgx.Tx, req payment.MarkVerifiedRequest, branchID string) ([]payment.DestinationResult, []byte, string, string, error) {
	type proofRow struct {
		id       string
		at       *time.Time
		bank     string
		rekAkhir string
	}
	rows, err := tx.Query(ctx, `SELECT id,ocr_at,COALESCE(ocr_bank::text,''),COALESCE(ocr_rek_akhir,'')
 FROM payment_proofs WHERE order_id=$1 ORDER BY id`, req.OrderID)
	if err != nil {
		return nil, nil, "", "", err
	}
	proofs := []proofRow{}
	for rows.Next() {
		var p proofRow
		if err := rows.Scan(&p.id, &p.at, &p.bank, &p.rekAkhir); err != nil {
			rows.Close()
			return nil, nil, "", "", err
		}
		proofs = append(proofs, p)
	}
	rows.Close()

	h := sha256.New()
	for i, p := range proofs {
		if i > 0 {
			h.Write([]byte{0x1e})
		}
		h.Write([]byte(strings.ToLower(p.id)))
		h.Write([]byte{0x1f})
		if p.at != nil {
			h.Write([]byte(p.at.UTC().Format(time.RFC3339Nano)))
		}
	}
	fingerprint := h.Sum(nil)
	if req.Hasil == payment.VerifCocok && req.Metode != payment.MetodeAI && len(proofs) != len(req.ProofDestinations) {
		return nil, nil, "", "", payment.ErrDestinationRequired
	}
	mapped := map[string]string{}
	for _, d := range req.ProofDestinations {
		if _, exists := mapped[d.PaymentProofID]; exists {
			return nil, nil, "", "", payment.ErrDestinationRequired
		}
		mapped[d.PaymentProofID] = d.BankAccountID
	}
	results := make([]payment.DestinationResult, 0, len(proofs))
	var digitRek int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT (value->>'digit_rek')::bigint FROM settings WHERE key='ai'),4)`).Scan(&digitRek); err != nil {
		return nil, nil, "", "", err
	}
	if digitRek < 1 {
		digitRek = 4
	}
	for _, p := range proofs {
		if req.Metode == payment.MetodeAI {
			result, err := r.destinationFromOCR(ctx, tx, branchID, p.id, p.bank, p.rekAkhir, req.Metode, digitRek)
			if err != nil {
				return nil, nil, "", "", err
			}
			results = append(results, result)
			continue
		}
		accountID, ok := mapped[p.id]
		if req.Hasil == payment.VerifCocok && !ok {
			return nil, nil, "", "", payment.ErrDestinationRequired
		}
		if !ok {
			result, err := r.destinationFromOCR(ctx, tx, branchID, p.id, p.bank, p.rekAkhir, req.Metode, digitRek)
			if err != nil {
				return nil, nil, "", "", err
			}
			results = append(results, result)
			continue
		}
		var bankCode, owner, nomor string
		err := tx.QueryRow(ctx, `SELECT ba.bank::text,ba.nama_pemilik,ba.nomor
 FROM branches b JOIN branch_bank_accounts x ON x.branch_id=b.id AND x.is_allowed
 JOIN bank_accounts ba ON ba.id=x.bank_account_id AND ba.is_active
 WHERE b.id=$1 AND b.is_active AND ba.id=$2`, branchID, accountID).Scan(&bankCode, &owner, &nomor)
		if errors.Is(err, pgx.ErrNoRows) {
			if req.Hasil == payment.VerifCocok {
				return nil, nil, "", "", payment.ErrDestinationNotAllowed
			}
			results = append(results, payment.DestinationResult{PaymentProofID: p.id, Source: req.Metode,
				Status: "not_allowed", BankAccountID: accountID})
			continue
		}
		if err != nil {
			return nil, nil, "", "", err
		}
		digits := nomor
		if len(digits) > 4 {
			digits = digits[len(digits)-4:]
		}
		results = append(results, payment.DestinationResult{PaymentProofID: p.id, Source: req.Metode,
			Status: "allowed", BankAccountID: accountID, BankSnapshot: bankCode,
			AccountOwnerSnapshot: owner, AccountLastDigitsSnapshot: digits})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].PaymentProofID < results[j].PaymentProofID })
	finalHasil, finalAlasan := req.Hasil, req.Alasan
	if req.Metode == payment.MetodeAI && req.Hasil == payment.VerifCocok {
		for _, result := range results {
			if result.Status == "allowed" {
				continue
			}
			finalHasil, finalAlasan = payment.VerifPerluReview, payment.AlasanRekeningBeda
			if result.Status == "unreadable" {
				finalAlasan = payment.AlasanTidakTerbaca
			}
			break
		}
	}
	return results, fingerprint, finalHasil, finalAlasan, nil
}

func (r *Repo) destinationFromOCR(ctx context.Context, tx pgx.Tx, branchID, proofID, bankCode, rekAkhir, source string, minDigits int64) (payment.DestinationResult, error) {
	result := payment.DestinationResult{PaymentProofID: proofID, Source: source}
	digits := onlyDigits(rekAkhir)
	if digits == "" || int64(len(digits)) < minDigits {
		result.Status = "unreadable"
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT ba.id,ba.bank::text,ba.nama_pemilik,ba.nomor
 FROM branches b JOIN branch_bank_accounts x ON x.branch_id=b.id AND x.is_allowed
 JOIN bank_accounts ba ON ba.id=x.bank_account_id AND ba.is_active
 WHERE b.id=$1 AND b.is_active AND right(ba.nomor,length($2))=$2
   AND ($3='' OR ba.bank::text=$3) ORDER BY ba.id`, branchID, digits, strings.ToUpper(strings.TrimSpace(bankCode)))
	if err != nil {
		return result, err
	}
	type matchedAccount struct{ id, bank, owner, nomor string }
	matches := []matchedAccount{}
	for rows.Next() {
		var a matchedAccount
		if err := rows.Scan(&a.id, &a.bank, &a.owner, &a.nomor); err != nil {
			rows.Close()
			return result, err
		}
		matches = append(matches, a)
	}
	rows.Close()
	if len(matches) > 1 {
		result.Status = "ambiguous"
		return result, nil
	}
	if len(matches) == 1 {
		a := matches[0]
		last := a.nomor
		if len(last) > 4 {
			last = last[len(last)-4:]
		}
		result.Status, result.BankAccountID = "allowed", a.id
		result.BankSnapshot, result.AccountOwnerSnapshot, result.AccountLastDigitsSnapshot = a.bank, a.owner, last
		return result, nil
	}
	var masterCount int
	var assigned, inactive bool
	if err := tx.QueryRow(ctx, `SELECT count(*)::int,
 COALESCE(bool_or(x.is_allowed),false),COALESCE(bool_or(NOT ba.is_active),false)
 FROM bank_accounts ba LEFT JOIN branch_bank_accounts x ON x.bank_account_id=ba.id AND x.branch_id=$1
 WHERE right(ba.nomor,length($2))=$2 AND ($3='' OR ba.bank::text=$3)`,
		branchID, digits, strings.ToUpper(strings.TrimSpace(bankCode))).Scan(&masterCount, &assigned, &inactive); err != nil {
		return result, err
	}
	switch {
	case masterCount == 0:
		result.Status = "unknown"
	case assigned && inactive:
		result.Status = "inactive"
	default:
		result.Status = "not_allowed"
	}
	return result, nil
}

func onlyDigits(v string) string {
	var out strings.Builder
	for _, r := range v {
		if r >= '0' && r <= '9' {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func ikonVerifikasi(hasil string) string {
	if hasil == payment.VerifCocok {
		return "selesai"
	}
	return "review"
}

// teksVerifikasi menyusun kalimat timeline verifikasi.
func teksVerifikasi(req payment.MarkVerifiedRequest) string {
	if req.Actor.Kind == auth.KindSystem {
		if req.Hasil == payment.VerifCocok {
			return payment.TeksEventSetujuSistem()
		}
		return payment.TeksEventPerluReviewSistem(req.Alasan)
	}
	if req.Hasil == payment.VerifCocok {
		return payment.TeksEventSetuju(req.Actor.Nama)
	}
	return payment.TeksEventPerluReview(req.Actor.Nama, req.Alasan)
}

// actorKindVerifikasi menentukan jenis aktor pada timeline: `system` untuk
// keputusan otomatis (AI/OCR, kartu F2-26), `admin` untuk keputusan manusia
// (PRD §5 — timeline tidak boleh mengaku dikerjakan admin padahal bukan).
func actorKindVerifikasi(req payment.MarkVerifiedRequest) sqlc.ActorKind {
	if req.Actor.Kind == auth.KindSystem {
		return sqlc.ActorKindSystem
	}
	return sqlc.ActorKindAdmin
}

// adminIDVerifikasi mengisi pemeriksa pada `verification_logs`. Verifikasi
// otomatis (metode `ai`) menyimpan NULL — database & dokumen menyatakan
// admin_id NULL berarti AI (Database.md §4.5); id mesin tidak pernah ditulis
// sebagai pemeriksa manusia.
func adminIDVerifikasi(req payment.MarkVerifiedRequest) pgtype.UUID {
	if req.Metode == payment.MetodeAI || req.Actor.Kind == auth.KindSystem {
		return pgtype.UUID{}
	}
	return pgtypes.UUID(req.Actor.UserID)
}

// statusSesudah mengembalikan status pesanan sesudah keputusan: Done hanya
// untuk persetujuan, selain itu tetap seperti semula (Submitted).
func statusSesudah(hasil, sebelum string) string {
	if hasil == payment.VerifCocok {
		return payment.StatusDone
	}
	return sebelum
}

// snapshotVerifikasi menyusun potongan keadaan pesanan SEBELUM verifikasi untuk
// entri audit.
func snapshotVerifikasi(row sqlc.LockOrderForVerifyRow) map[string]any {
	out := map[string]any{
		"status":       row.Status,
		"verif_status": row.VerifStatus,
		"total":        row.Total,
		"versi":        int(row.Versi),
	}
	if row.AlasanReview != "" {
		out["alasan_review"] = row.AlasanReview
	}
	return out
}

// toVerificationLog menyusun nilai domain dari baris hasil insert.
func toVerificationLog(row sqlc.InsertVerificationLogRow, adminNama string) payment.VerificationLog {
	out := payment.VerificationLog{
		ID:           row.ID,
		OrderID:      row.OrderID,
		Metode:       row.Metode,
		TotalPesanan: row.TotalPesanan,
		TotalTerbaca: row.TotalTerbaca,
		Hasil:        row.Hasil,
		Alasan:       row.Alasan,
		Catatan:      row.Catatan,
		CreatedAt:    row.CreatedAt.UTC(),
	}
	if row.BranchAllowlistVersion.Valid {
		out.BranchAllowlistVersion = row.BranchAllowlistVersion.Int64
	}
	_ = json.Unmarshal(row.DestinationResults, &out.DestinationResults)
	if row.Selisih.Valid {
		out.Selisih = row.Selisih.Int64
	}
	// Nama pemeriksa hanya berarti untuk verifikasi manusia; verifikasi AI
	// (metode ai) menyimpan admin_id NULL dan tidak punya nama admin.
	if row.Metode != payment.MetodeAI {
		out.AdminNama = adminNama
	}
	return out
}
