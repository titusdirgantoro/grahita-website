package postgres

// Berkas ini memuat sisi penyimpanan PEMBACAAN BUKTI AI/OCR (kartu F2-26;
// PRD §5; Database.md §4.5 dan §7.4).
//
// Satu hasil baca = SATU transaksi kecil: hasil bacaan (kolom `ocr_*`) dan
// entri audit raw-nya (SystemDesign.md §5.4) ditulis BERSAMA, sehingga tidak
// mungkin ada bacaan yang tercatat tanpa jejak auditnya. `ocr_at` diisi di
// transaksi ini dan menjadi penanda idempotensi: bukti yang sudah dibaca tidak
// pernah dipanggil ulang ke provider.
//
// Bila transaksi batal, `ocr_at` juga batal — percobaan berikutnya membaca
// ulang bukti tersebut dari awal (tidak ada bacaan setengah tersimpan).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo memenuhi kontrak penyimpanan pembacaan bukti domain pembayaran.
var _ payment.OCRRepository = (*Repo)(nil)

// OrderForOCR membaca konteks pesanan untuk penilaian otomatis.
func (r *Repo) OrderForOCR(ctx context.Context, orderID string) (payment.OrderOCR, error) {
	var row payment.OrderOCR
	err := r.pool.QueryRow(ctx, `SELECT id::text,branch_id::text,versi,status::text,total,tanggal,
verif_status::text,COALESCE(alasan_review::text,'') FROM orders WHERE id=$1`, orderID).Scan(
		&row.ID, &row.BranchID, &row.Versi, &row.Status, &row.Total, &row.Tanggal,
		&row.VerifStatus, &row.AlasanReview)
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.OrderOCR{}, payment.ErrNotFound
	}
	if err != nil {
		return payment.OrderOCR{}, fmt.Errorf("query OrderForOCR: %w", err)
	}
	return row, nil
}

// ListProofsForOCR mengembalikan seluruh bukti satu pesanan beserta hasil baca
// yang sudah ada, terlama dulu (urutan unggah).
func (r *Repo) ListProofsForOCR(ctx context.Context, orderID string) ([]payment.ProofOCR, error) {
	rows, err := r.q.ListProofsForOCR(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("query ListProofsForOCR: %w", err)
	}
	out := make([]payment.ProofOCR, 0, len(rows))
	for _, row := range rows {
		p := payment.ProofOCR{
			ID:         row.ID,
			OrderID:    row.OrderID,
			StorageKey: row.StorageKey,
			MimeType:   row.MimeType,
			NamaFile:   row.NamaFile,
			Tanggal:    row.OcrTanggal,
			Bank:       pgtypes.TextOr(row.OcrBank),
			RekAkhir:   pgtypes.TextOr(row.OcrRekAkhir),
			Confidence: row.OcrConfidence,
			At:         row.OcrAt,
		}
		if row.OcrNominal.Valid {
			n := row.OcrNominal.Int64
			p.Nominal = &n
		}
		out = append(out, p)
	}
	return out, nil
}

// SimpanBacaan menyimpan hasil baca satu bukti beserta entri auditnya dalam
// SATU transaksi (SystemDesign.md §5.4).
func (r *Repo) SimpanBacaan(ctx context.Context, req payment.SimpanBacaanRequest) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi baca bukti: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)

	if err := q.UpdateProofOCR(ctx, sqlc.UpdateProofOCRParams{
		OcrNominal:    pgtypes.Int8Ptr(req.Hasil.Nominal),
		OcrTanggal:    req.Hasil.Tanggal,
		OcrBank:       pgtypes.Text(req.Hasil.Bank),
		OcrRekAkhir:   pgtypes.Text(req.Hasil.RekAkhir),
		OcrConfidence: pgtypes.Numeric(req.Hasil.Confidence),
		OcrRaw:        req.Raw,
		ID:            req.ProofID,
		OrderID:       req.OrderID,
	}); err != nil {
		return fmt.Errorf("query UpdateProofOCR: %w", err)
	}
	if err := auditpg.Insert(ctx, tx, req.Audit); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit baca bukti: %w", err)
	}
	return nil
}

// SudahDinilaiAI hanya menerima attempt dengan versi policy dan fingerprint
// himpunan bukti yang persis sama. Perubahan assignment/status membuat attempt
// lama stale tanpa menghapus histori atau menulis attempt baru.
func (r *Repo) SudahDinilaiAI(ctx context.Context, orderID string) (bool, error) {
	var version int64
	var orderVersion int
	if err := r.pool.QueryRow(ctx, `SELECT b.bank_allowlist_version,o.versi
		FROM orders o JOIN branches b ON b.id=o.branch_id WHERE o.id=$1`, orderID).Scan(&version, &orderVersion); err != nil {
		return false, fmt.Errorf("baca versi allowlist OCR: %w", err)
	}
	rows, err := r.pool.Query(ctx, `SELECT id::text, ocr_at FROM payment_proofs WHERE order_id=$1 ORDER BY id`, orderID)
	if err != nil {
		return false, fmt.Errorf("baca fingerprint bukti OCR: %w", err)
	}
	defer rows.Close()
	h := sha256.New()
	i := 0
	for rows.Next() {
		var id string
		var at *time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return false, err
		}
		if i > 0 {
			h.Write([]byte{0x1e})
		}
		h.Write([]byte(strings.ToLower(id)))
		h.Write([]byte{0x1f})
		if at != nil {
			h.Write([]byte(at.UTC().Format(time.RFC3339Nano)))
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	var sudah bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM payment_ai_assessments
		WHERE order_id=$1 AND branch_allowlist_version=$2
		  AND order_version=$3 AND proof_set_fingerprint=$4
	)`, orderID, version, orderVersion, h.Sum(nil)).Scan(&sudah); err != nil {
		return false, fmt.Errorf("periksa attempt AI terkini: %w", err)
	}
	return sudah, nil
}

// SimpanPenilaianAI menyimpan rekomendasi AI secara atomik tanpa menyentuh
// orders.status/verif_status, settlement, dokumen, maupun antrean email.
func (r *Repo) SimpanPenilaianAI(ctx context.Context, req payment.PenilaianAIRequest) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(22102, 2)`); err != nil {
		return false, err
	}
	var status string
	var orderVersion int
	var total, allowlistVersion int64
	err = tx.QueryRow(ctx, `SELECT o.status::text,o.versi,o.total,b.bank_allowlist_version
FROM orders o JOIN branches b ON b.id=o.branch_id WHERE o.id=$1 FOR SHARE OF b FOR UPDATE OF o`, req.OrderID).
		Scan(&status, &orderVersion, &total, &allowlistVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, payment.ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if status != payment.StatusSubmitted {
		return false, payment.ErrOCRSnapshotStale
	}
	if orderVersion != req.OrderVersion || total != req.TotalPesanan || allowlistVersion != req.AllowlistVersion {
		return false, payment.ErrOCRSnapshotStale
	}
	rows, err := tx.Query(ctx, `SELECT id::text,ocr_at FROM payment_proofs WHERE order_id=$1 ORDER BY id FOR SHARE`, req.OrderID)
	if err != nil {
		return false, err
	}
	h := sha256.New()
	n := 0
	for rows.Next() {
		var id string
		var at *time.Time
		if err = rows.Scan(&id, &at); err != nil {
			rows.Close()
			return false, err
		}
		if at == nil {
			rows.Close()
			return false, payment.ErrOCRSnapshotStale
		}
		if n > 0 {
			h.Write([]byte{0x1e})
		}
		h.Write([]byte(strings.ToLower(id)))
		h.Write([]byte{0x1f})
		h.Write([]byte(at.UTC().Format(time.RFC3339Nano)))
		n++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	if n != req.Keputusan.JumlahBukti || hex.EncodeToString(h.Sum(nil)) != req.ProofSetFingerprint {
		return false, payment.ErrOCRSnapshotStale
	}
	var totalRead *int64
	if req.Keputusan.Alasan != payment.AlasanTidakTerbaca {
		totalRead = &req.Keputusan.TotalTerbaca
	}
	var inserted string
	err = tx.QueryRow(ctx, `INSERT INTO payment_ai_assessments
(order_id,order_version,branch_allowlist_version,proof_set_fingerprint,result,reason,total_read,confidence,proof_count,note)
VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10)
ON CONFLICT (order_id,order_version,branch_allowlist_version,proof_set_fingerprint) DO NOTHING
RETURNING id::text`, req.OrderID, req.OrderVersion, req.AllowlistVersion, h.Sum(nil),
		req.Keputusan.Hasil, req.Keputusan.Alasan, totalRead, req.Keputusan.KeyakinanTerendah,
		req.Keputusan.JumlahBukti, req.Keputusan.Catatan).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = auditpg.Insert(ctx, tx, req.Audit); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
