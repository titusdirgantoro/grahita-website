// Package postgres adalah implementasi repository pesanan di PostgreSQL.
//
// Seluruh query menyertakan branch_id sebagai syarat (dari SESI) dan status
// 'draft' diperiksa lagi di klausa WHERE update/delete, sehingga pesanan cabang
// lain maupun pesanan yang sudah dikirim tidak bisa disentuh dari jalur ini.
//
// Total dan penanda is_sertifikat TIDAK dihitung Go saja: trigger
// order_lines_recalc (migrasi 0002) menghitungnya dari order_lines pada setiap
// penulisan baris, dan setiap operasi tulis membaca ulang pesanannya di dalam
// transaksi yang sama sehingga nilai yang dikembalikan adalah nilai database.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo adalah repository pesanan berbasis sqlc.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository pesanan.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// List mengembalikan daftar pesanan satu cabang (PRD CP-ORD-01).
func (r *Repo) List(ctx context.Context, branchID, status string) ([]order.Order, error) {
	arg := sqlc.ListOrdersForBranchParams{BranchID: branchID}
	if status != "" {
		arg.Status = sqlc.NullOrderStatus{OrderStatus: sqlc.OrderStatus(status), Valid: true}
	}
	rows, err := r.q.ListOrdersForBranch(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("query ListOrdersForBranch: %w", err)
	}
	out := make([]order.Order, 0, len(rows))
	for _, row := range rows {
		out = append(out, order.Order{
			ID:           row.ID,
			BranchID:     branchID,
			Nomor:        pgtypes.TextOr(row.Nomor),
			Status:       row.Status,
			VerifStatus:  row.VerifStatus,
			Versi:        int(row.Versi),
			IsCNC:        row.IsCnc,
			IsSertifikat: row.IsSertifikat,
			Tanggal:      tanggalOr(row.Tanggal),
			Catatan:      row.Catatan,
			Total:        row.Total,
			JumlahItem:   row.JumlahItem,
			SubmittedAt:  waktuOr(row.SubmittedAt),
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}
	return out, nil
}

// Get mengembalikan satu pesanan milik cabang tersebut beserta barisnya.
func (r *Repo) Get(ctx context.Context, id, branchID string) (order.Order, error) {
	return loadOrder(ctx, r.q, id, branchID)
}

// CreateDraft menyimpan draft baru beserta seluruh barisnya dalam satu
// transaksi, lalu membaca ulang hasilnya (total dari trigger database).
func (r *Repo) CreateDraft(ctx context.Context, branchID, createdBy, catatan string, lines []order.Line) (order.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return order.Order{}, fmt.Errorf("mulai transaksi buat draft: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := r.q.WithTx(tx)
	id, err := q.CreateOrderDraft(ctx, sqlc.CreateOrderDraftParams{
		BranchID:  branchID,
		Catatan:   pgtypes.Text(catatan),
		CreatedBy: pgtypes.UUID(createdBy),
	})
	if err != nil {
		return order.Order{}, fmt.Errorf("query CreateOrderDraft: %w", err)
	}
	if err := insertLines(ctx, q, id, lines); err != nil {
		return order.Order{}, err
	}
	saved, err := loadOrder(ctx, q, id, branchID)
	if err != nil {
		return order.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return order.Order{}, fmt.Errorf("commit buat draft: %w", err)
	}
	return saved, nil
}

// UpdateDraft mengganti catatan dan seluruh baris draft. Baris diganti utuh
// (bukan diff) sehingga hasilnya sama dengan yang dilihat editor.
func (r *Repo) UpdateDraft(ctx context.Context, id, branchID, catatan string, lines []order.Line) (order.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return order.Order{}, fmt.Errorf("mulai transaksi ubah draft: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := r.q.WithTx(tx)
	if _, err := q.UpdateOrderDraft(ctx, sqlc.UpdateOrderDraftParams{
		Catatan:  pgtypes.Text(catatan),
		ID:       id,
		BranchID: branchID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Tidak ada, bukan milik cabang ini, atau sudah bukan draft lagi.
			return order.Order{}, order.ErrNotFound
		}
		return order.Order{}, fmt.Errorf("query UpdateOrderDraft: %w", err)
	}
	if err := q.ClearOrderLines(ctx, id); err != nil {
		return order.Order{}, fmt.Errorf("query ClearOrderLines: %w", err)
	}
	if err := insertLines(ctx, q, id, lines); err != nil {
		return order.Order{}, err
	}
	saved, err := loadOrder(ctx, q, id, branchID)
	if err != nil {
		return order.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return order.Order{}, fmt.Errorf("commit ubah draft: %w", err)
	}
	return saved, nil
}

// DeleteDraft menghapus draft milik cabang tersebut. Pesanan Submitted/Done
// tidak pernah cocok dengan klausa WHERE sehingga tidak terhapus.
func (r *Repo) DeleteDraft(ctx context.Context, id, branchID string) error {
	n, err := r.q.DeleteOrderDraft(ctx, sqlc.DeleteOrderDraftParams{ID: id, BranchID: branchID})
	if err != nil {
		return fmt.Errorf("query DeleteOrderDraft: %w", err)
	}
	if n == 0 {
		return order.ErrNotFound
	}
	return nil
}

// insertLines menulis ulang seluruh baris draft. subtotal tidak diisi karena
// kolomnya GENERATED ALWAYS.
func insertLines(ctx context.Context, q *sqlc.Queries, orderID string, lines []order.Line) error {
	for _, l := range lines {
		if err := q.InsertOrderLineProduct(ctx, sqlc.InsertOrderLineProductParams{
			OrderID:        orderID,
			LineNo:         int32(l.LineNo),
			ProductID:      pgtypes.UUID(l.ProductID),
			NamaSnapshot:   l.Nama,
			SatuanSnapshot: l.Satuan,
			Qty:            int32(l.Qty),
			HargaSatuan:    l.HargaSatuan,
		}); err != nil {
			return fmt.Errorf("query InsertOrderLineProduct: %w", err)
		}
	}
	return nil
}

// loadOrder membaca satu pesanan beserta barisnya memakai query yang sama untuk
// jalur transaksi maupun pembacaan biasa.
func loadOrder(ctx context.Context, q *sqlc.Queries, id, branchID string) (order.Order, error) {
	row, err := q.GetOrderForBranch(ctx, sqlc.GetOrderForBranchParams{ID: id, BranchID: branchID})
	if errors.Is(err, pgx.ErrNoRows) {
		return order.Order{}, order.ErrNotFound
	}
	if err != nil {
		return order.Order{}, fmt.Errorf("query GetOrderForBranch: %w", err)
	}
	lines, err := q.ListOrderLines(ctx, row.ID)
	if err != nil {
		return order.Order{}, fmt.Errorf("query ListOrderLines: %w", err)
	}
	out := order.Order{
		ID:           row.ID,
		Nomor:        pgtypes.TextOr(row.Nomor),
		Status:       row.Status,
		VerifStatus:  row.VerifStatus,
		Versi:        int(row.Versi),
		IsCNC:        row.IsCnc,
		IsSertifikat: row.IsSertifikat,
		Tanggal:      tanggalOr(row.Tanggal),
		JatuhTempo:   tanggalOr(row.JatuhTempo),
		Catatan:      row.Catatan,
		Total:        row.Total,
		JumlahItem:   row.JumlahItem,
		SubmittedAt:  waktuOr(row.SubmittedAt),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		Lines:        make([]order.Line, 0, len(lines)),
	}
	for _, l := range lines {
		out.Lines = append(out.Lines, order.Line{
			LineNo:       int(l.LineNo),
			Jenis:        l.Jenis,
			ProductID:    pgtypes.UUIDString(l.ProductID),
			Nama:         l.NamaSnapshot,
			Satuan:       l.SatuanSnapshot,
			Qty:          int(l.Qty),
			HargaSatuan:  l.HargaSatuan,
			Subtotal:     l.Subtotal.Int64,
			SerialNumber: pgtypes.TextOr(l.SerialNumber),
		})
	}
	return out, nil
}

// tanggalOr mengubah kolom date menjadi YYYY-MM-DD (kosong bila NULL).
func tanggalOr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(order.TanggalLayout)
}

// waktuOr mengubah kolom timestamptz menjadi RFC3339 UTC (kosong bila NULL).
func waktuOr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
