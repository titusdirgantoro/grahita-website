// Package postgres adalah implementasi repository rekening bank di PostgreSQL.
//
// Setiap metode yang mengubah data menuliskan entri audit di dalam transaksi
// yang sama, sehingga perubahan dan catatannya selalu sejalan
// (SystemDesign.md §5.4).
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/platform/database"
)

// Repo mengimplementasikan bank.Repository di atas PostgreSQL.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository rekening bank.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// List memuat seluruh rekening (aktif maupun nonaktif).
func (r *Repo) List(ctx context.Context) ([]bank.Account, error) {
	rows, err := r.q.ListBankAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListBankAccounts: %w", err)
	}
	out := make([]bank.Account, 0, len(rows))
	for _, row := range rows {
		out = append(out, bank.Account{
			ID: row.ID, Bank: row.Bank, NamaPemilik: row.NamaPemilik,
			Nomor: row.Nomor, IsActive: row.IsActive,
		})
	}
	return out, nil
}

// ListActive memuat hanya rekening aktif (PRD §4.6).
func (r *Repo) ListActive(ctx context.Context) ([]bank.Account, error) {
	rows, err := r.q.ListActiveBankAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ListActiveBankAccounts: %w", err)
	}
	out := make([]bank.Account, 0, len(rows))
	for _, row := range rows {
		out = append(out, bank.Account{
			ID: row.ID, Bank: row.Bank, NamaPemilik: row.NamaPemilik,
			Nomor: row.Nomor, IsActive: row.IsActive,
		})
	}
	return out, nil
}

// Get memuat satu rekening.
func (r *Repo) Get(ctx context.Context, id string) (bank.Account, error) {
	row, err := r.q.GetBankAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return bank.Account{}, bank.ErrNotFound
	}
	if err != nil {
		return bank.Account{}, fmt.Errorf("query GetBankAccount: %w", err)
	}
	return bank.Account{
		ID: row.ID, Bank: row.Bank, NamaPemilik: row.NamaPemilik,
		Nomor: row.Nomor, IsActive: row.IsActive,
	}, nil
}

// Create menyimpan rekening baru beserta catatan auditnya.
func (r *Repo) Create(ctx context.Context, in bank.Account, rec audit.Entry) (bank.Account, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return bank.Account{}, fmt.Errorf("mulai transaksi buat rekening: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := r.q.WithTx(tx).CreateBankAccount(ctx, sqlc.CreateBankAccountParams{
		Bank:        sqlc.BankCode(in.Bank),
		NamaPemilik: in.NamaPemilik,
		Nomor:       in.Nomor,
		IsActive:    in.IsActive,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return bank.Account{}, bank.ErrDuplicate
		}
		return bank.Account{}, fmt.Errorf("query CreateBankAccount: %w", err)
	}
	saved := bank.Account{
		ID: row.ID, Bank: row.Bank, NamaPemilik: row.NamaPemilik,
		Nomor: row.Nomor, IsActive: row.IsActive,
	}
	rec.EntityID = saved.ID
	// Snapshot "after" memakai baris yang benar-benar tersimpan (lengkap id).
	rec.After = saved
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return bank.Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return bank.Account{}, fmt.Errorf("commit buat rekening: %w", err)
	}
	return saved, nil
}

// Update menyimpan perubahan penuh satu rekening beserta catatan auditnya.
func (r *Repo) Update(ctx context.Context, next bank.Account, rec audit.Entry) (bank.Account, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return bank.Account{}, fmt.Errorf("mulai transaksi ubah rekening: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Kunci baris sekaligus memastikan rekeningnya benar-benar ada.
	var ada bool
	if err := tx.QueryRow(ctx, `SELECT true FROM bank_accounts WHERE id = $1 FOR UPDATE`, next.ID).Scan(&ada); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return bank.Account{}, bank.ErrNotFound
		}
		return bank.Account{}, fmt.Errorf("baca rekening saat ini: %w", err)
	}

	row, err := r.q.WithTx(tx).UpdateBankAccount(ctx, sqlc.UpdateBankAccountParams{
		Bank:        sqlc.BankCode(next.Bank),
		NamaPemilik: next.NamaPemilik,
		Nomor:       next.Nomor,
		IsActive:    next.IsActive,
		ID:          next.ID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return bank.Account{}, bank.ErrDuplicate
		}
		return bank.Account{}, fmt.Errorf("query UpdateBankAccount: %w", err)
	}
	saved := bank.Account{
		ID: row.ID, Bank: row.Bank, NamaPemilik: row.NamaPemilik,
		Nomor: row.Nomor, IsActive: row.IsActive,
	}
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return bank.Account{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return bank.Account{}, fmt.Errorf("commit ubah rekening: %w", err)
	}
	return saved, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
