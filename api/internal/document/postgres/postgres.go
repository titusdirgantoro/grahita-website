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
	"errors"
	"fmt"
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
	rekening, err := r.q.ListActiveBankAccounts(ctx)
	if err != nil {
		return document.Order{}, fmt.Errorf("query ListActiveBankAccounts: %w", err)
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
		JatuhTempo:   row.JatuhTempo,
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
			Satuan:       l.SatuanSnapshot,
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
			Nomor:    b.Nomor,
			AtasNama: b.NamaPemilik,
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
