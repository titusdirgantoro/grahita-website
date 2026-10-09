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
	"errors"
	"fmt"

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
	row, err := r.q.OrderForOCR(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.OrderOCR{}, payment.ErrNotFound
	}
	if err != nil {
		return payment.OrderOCR{}, fmt.Errorf("query OrderForOCR: %w", err)
	}
	return payment.OrderOCR{
		ID:           row.ID,
		Status:       row.Status,
		Total:        row.Total,
		Tanggal:      row.Tanggal,
		VerifStatus:  row.VerifStatus,
		AlasanReview: row.AlasanReview,
	}, nil
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

// SudahDinilaiAI melaporkan apakah sudah ada keputusan AI yang dibuat sesudah
// bukti terakhir dibaca (penjaga idempotensi pekerjaan River).
func (r *Repo) SudahDinilaiAI(ctx context.Context, orderID string) (bool, error) {
	sudah, err := r.q.OrderSudahDinilaiAI(ctx, orderID)
	if err != nil {
		return false, fmt.Errorf("query OrderSudahDinilaiAI: %w", err)
	}
	return sudah, nil
}
