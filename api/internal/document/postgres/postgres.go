// Package postgres adalah implementasi repository dokumen di PostgreSQL.
//
// Paket ini memegang SATU-SATUNYA jalur tulis ke tabel `documents` (Database.md
// §4.6) dan membaca snapshot pesanan/cabang/rekening yang dibutuhkan generator
// dokumen (SystemDesign.md §8). Membaca `orders`, `order_lines`, dan
// `branches` dari sini sah karena querynya terpisah dari repository pesanan —
// tidak ada ketergantungan paket antar-domain.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/document"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo adalah repository dokumen berbasis sqlc.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository dokumen.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// OrderForDocument membaca snapshot pesanan lengkap: header, identitas cabang,
// baris pesanan, dan rekening pembayaran AKTIF yang berlaku sekarang.
func (r *Repo) OrderForDocument(ctx context.Context, orderID string) (document.Order, error) {
	row, err := r.q.GetOrderForDocument(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return document.Order{}, document.ErrNotFound
	}
	if err != nil {
		return document.Order{}, fmt.Errorf("query GetOrderForDocument: %w", err)
	}

	lines, err := r.q.ListOrderLines(ctx, orderID)
	if err != nil {
		return document.Order{}, fmt.Errorf("query ListOrderLines(dokumen): %w", err)
	}
	type historicalAccount struct{ ID, Bank, Owner, Number string }
	rekening := []historicalAccount{}
	if row.Status == "done" {
		var pointer *string
		var destinationJSON []byte
		var logOrderID, logResult *string
		if err := r.pool.QueryRow(ctx, `SELECT o.done_verification_log_id::text,v.destination_results,
 v.order_id::text,v.hasil::text
 FROM orders o LEFT JOIN verification_logs v ON v.id=o.done_verification_log_id
 WHERE o.id=$1`, orderID).Scan(&pointer, &destinationJSON, &logOrderID, &logResult); err != nil {
			return document.Order{}, fmt.Errorf("baca pointer verifikasi dokumen: %w", err)
		}
		if pointer == nil {
			return document.Order{}, fmt.Errorf("invariant dokumen: pesanan done tanpa pointer verifikasi")
		}
		if logOrderID == nil || *logOrderID != orderID || logResult == nil || *logResult != "cocok" {
			return document.Order{}, fmt.Errorf("invariant dokumen: pointer verifikasi bukan attempt sukses milik pesanan")
		}
		type destinationSnapshot struct {
			PaymentProofID            string `json:"payment_proof_id"`
			Status                    string `json:"status"`
			BankAccountID             string `json:"bank_account_id"`
			BankSnapshot              string `json:"bank_snapshot"`
			AccountOwnerSnapshot      string `json:"account_owner_snapshot"`
			AccountLastDigitsSnapshot string `json:"account_last_digits_snapshot"`
		}
		var snapshots []destinationSnapshot
		if err := json.Unmarshal(destinationJSON, &snapshots); err != nil {
			return document.Order{}, fmt.Errorf("invariant dokumen: destination_results rusak: %w", err)
		}
		proofRows, err := r.pool.Query(ctx, `SELECT id::text FROM payment_proofs WHERE order_id=$1 ORDER BY id`, orderID)
		if err != nil {
			return document.Order{}, fmt.Errorf("baca bukti dokumen: %w", err)
		}
		proofIDs := map[string]bool{}
		for proofRows.Next() {
			var proofID string
			if err := proofRows.Scan(&proofID); err != nil {
				proofRows.Close()
				return document.Order{}, fmt.Errorf("baca id bukti dokumen: %w", err)
			}
			proofIDs[proofID] = true
		}
		if err := proofRows.Err(); err != nil {
			proofRows.Close()
			return document.Order{}, fmt.Errorf("baca bukti dokumen: %w", err)
		}
		proofRows.Close()
		if len(snapshots) != len(proofIDs) {
			return document.Order{}, fmt.Errorf("invariant dokumen: snapshot tujuan tidak lengkap")
		}
		seenProof, seenAccount := map[string]bool{}, map[string]bool{}
		for _, snap := range snapshots {
			if snap.Status != "allowed" || snap.PaymentProofID == "" || !proofIDs[snap.PaymentProofID] || seenProof[snap.PaymentProofID] ||
				snap.BankAccountID == "" || strings.TrimSpace(snap.BankSnapshot) == "" ||
				strings.TrimSpace(snap.AccountOwnerSnapshot) == "" || strings.TrimSpace(snap.AccountLastDigitsSnapshot) == "" {
				return document.Order{}, fmt.Errorf("invariant dokumen: snapshot tujuan tidak valid")
			}
			seenProof[snap.PaymentProofID] = true
			var bankCode, number string
			if err := r.pool.QueryRow(ctx, `SELECT bank::text,nomor FROM bank_accounts WHERE id=$1`, snap.BankAccountID).Scan(&bankCode, &number); err != nil {
				return document.Order{}, fmt.Errorf("invariant dokumen: master rekening snapshot hilang: %w", err)
			}
			if bankCode != snap.BankSnapshot || !strings.HasSuffix(number, snap.AccountLastDigitsSnapshot) {
				return document.Order{}, fmt.Errorf("invariant dokumen: identitas rekening tidak cocok dengan snapshot")
			}
			if seenAccount[snap.BankAccountID] {
				continue
			}
			seenAccount[snap.BankAccountID] = true
			rekening = append(rekening, historicalAccount{ID: snap.BankAccountID, Bank: snap.BankSnapshot,
				Owner: snap.AccountOwnerSnapshot, Number: number})
		}
		sort.Slice(rekening, func(i, j int) bool {
			if rekening[i].Bank == rekening[j].Bank {
				return rekening[i].ID < rekening[j].ID
			}
			return rekening[i].Bank < rekening[j].Bank
		})
	}

	out := document.Order{
		ID:           row.ID,
		Nomor:        row.Nomor,
		Status:       row.Status,
		Versi:        int(row.Versi),
		BranchID:     row.BranchID,
		IsCNC:        row.IsCnc,
		IsSertifikat: row.IsSertifikat,
		Total:        row.Total,
		Catatan:      row.Catatan,
		Cabang: document.Cabang{
			Nama:    row.CabangNama,
			Kepala:  row.CabangKepala,
			Email:   row.CabangEmail,
			Telepon: row.CabangTelepon,
		},
	}
	if row.Tanggal != nil {
		out.Tanggal = *row.Tanggal
	}

	out.Lines = make([]document.Line, 0, len(lines))
	for _, l := range lines {
		out.Lines = append(out.Lines, document.Line{
			LineNo:       int(l.LineNo),
			Jenis:        l.Jenis,
			Nama:         l.NamaSnapshot,
			Qty:          int(l.Qty),
			HargaSatuan:  l.HargaSatuan,
			Subtotal:     l.Subtotal.Int64,
			SerialNumber: pgtypes.TextOr(l.SerialNumber),
		})
	}

	out.Rekening = make([]document.Rekening, 0, len(rekening))
	for _, b := range rekening {
		out.Rekening = append(out.Rekening, document.Rekening{
			Bank:     b.Bank,
			Nomor:    b.Number,
			AtasNama: b.Owner,
		})
	}
	return out, nil
}

// ListByOrder mengembalikan seluruh dokumen satu pesanan (semua versi).
func (r *Repo) ListByOrder(ctx context.Context, orderID string) ([]document.Document, error) {
	rows, err := r.q.ListDocumentsByOrder(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("query ListDocumentsByOrder: %w", err)
	}
	out := make([]document.Document, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDocument(d.ID, d.OrderID, d.Jenis, int(d.Versi), d.StorageKey, d.UkuranByte, d.CreatedAt))
	}
	return out, nil
}

// GetByID mengembalikan satu dokumen beserta pemilik pesanannya.
func (r *Repo) GetByID(ctx context.Context, id string) (document.Document, error) {
	row, err := r.q.GetDocument(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return document.Document{}, document.ErrNotFound
	}
	if err != nil {
		return document.Document{}, fmt.Errorf("query GetDocument: %w", err)
	}
	doc := toDocument(row.ID, row.OrderID, row.Jenis, int(row.Versi), row.StorageKey, row.UkuranByte, row.CreatedAt)
	doc.BranchID = row.BranchID
	doc.OrderNomor = row.OrderNomor
	return doc, nil
}

// SaveAll menyimpan seluruh dokumen satu permintaan dalam SATU transaksi.
//
// Baris yang sudah ada TIDAK ditimpa (`ON CONFLICT DO NOTHING`), jadi render
// ulang idempoten: yang dikembalikan adalah baris final — baris baru yang baru
// saja dibuat, atau baris lama yang sudah ada. Bila satu baris gagal, seluruh
// transaksi batal sehingga tidak ada dokumen separuh jadi.
func (r *Repo) SaveAll(ctx context.Context, req document.SaveRequest) ([]document.Document, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("mulai transaksi dokumen: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)

	out := make([]document.Document, 0, len(req.Docs))
	for _, d := range req.Docs {
		row, err := q.InsertDocument(ctx, sqlc.InsertDocumentParams{
			OrderID:    d.OrderID,
			Jenis:      sqlc.DocKind(d.Jenis),
			Versi:      int32(d.Versi),
			StorageKey: d.StorageKey,
			UkuranByte: pgtype.Int8{Int64: d.UkuranByte, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Bentrok dengan baris yang sudah ada: pakai baris lama itu.
			lama, err := q.GetDocumentByOrderJenisVersi(ctx, sqlc.GetDocumentByOrderJenisVersiParams{
				OrderID: d.OrderID,
				Jenis:   sqlc.DocKind(d.Jenis),
				Versi:   int32(d.Versi),
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("dokumen %s versi %d hilang setelah konflik", d.Jenis, d.Versi)
			}
			if err != nil {
				return nil, fmt.Errorf("query GetDocumentByOrderJenisVersi: %w", err)
			}
			final := toDocument(lama.ID, lama.OrderID, lama.Jenis, int(lama.Versi), lama.StorageKey, lama.UkuranByte, lama.CreatedAt)
			final.BranchID = d.BranchID
			final.OrderNomor = d.OrderNomor
			out = append(out, final)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("query InsertDocument: %w", err)
		}
		final := toDocument(row.ID, row.OrderID, row.Jenis, int(row.Versi), row.StorageKey, row.UkuranByte, row.CreatedAt)
		final.BranchID = d.BranchID
		final.OrderNomor = d.OrderNomor
		out = append(out, final)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit dokumen: %w", err)
	}
	return out, nil
}

// ListInvoices membaca riwayat invoice satu cabang (kartu F1-19): satu baris
// per dokumen invoice versi berapa pun + pesanan ber-nomor yang dokumennya
// belum ada. branch_id selalu dari SESI; dari/sampai menyaring tanggal bisnis
// pesanan secara inklusif.
//
// Membaca `orders` dari paket ini mengikuti pola yang sama dengan
// OrderForDocument: querynya milik paket ini dan tidak menimbulkan
// ketergantungan paket antar-domain (SystemDesign.md §2).
func (r *Repo) ListInvoices(ctx context.Context, branchID string, dari, sampai *time.Time) ([]document.InvoiceRow, error) {
	rows, err := r.q.ListInvoicesForBranch(ctx, sqlc.ListInvoicesForBranchParams{
		BranchID: branchID,
		Dari:     dari,
		Sampai:   sampai,
	})
	if err != nil {
		return nil, fmt.Errorf("query ListInvoicesForBranch: %w", err)
	}
	out := make([]document.InvoiceRow, 0, len(rows))
	for _, row := range rows {
		invoice := document.InvoiceRow{
			OrderID:    row.OrderID,
			Nomor:      row.Nomor,
			Status:     row.Status,
			Versi:      int(row.Versi),
			IsCNC:      row.IsCnc,
			Total:      row.Total,
			JumlahItem: row.JumlahItem,
			UkuranByte: row.UkuranByte,
		}
		invoice.Tanggal = tanggalInvoice(row.Tanggal)
		if row.DokumenID != "" && row.VersiDokumen >= 0 {
			invoice.DokumenID = row.DokumenID
			versi := int(row.VersiDokumen)
			invoice.VersiDokumen = &versi
		}
		out = append(out, invoice)
	}
	return out, nil
}

// tanggalInvoice mengubah kolom date menjadi bentuk YYYY-MM-DD; kosong bila
// kolomnya NULL (draft tidak punya tanggal, BR-04).
func tanggalInvoice(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(document.TanggalLayout)
}

// toDocument menyusun nilai domain dari kolom baris `documents`.
func toDocument(id, orderID, jenis string, versi int, storageKey string, ukuran int64, createdAt time.Time) document.Document {
	return document.Document{
		ID:         id,
		OrderID:    orderID,
		Jenis:      jenis,
		Versi:      versi,
		StorageKey: storageKey,
		UkuranByte: ukuran,
		CreatedAt:  createdAt.UTC(),
	}
}
