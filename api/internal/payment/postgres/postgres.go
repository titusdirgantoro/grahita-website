// Package postgres adalah implementasi repository bukti transfer di
// PostgreSQL.
//
// Paket ini memenuhi tiga kontrak domain sekaligus (pola yang sama dipakai
// domain email): Repository (metadata bukti), OrderSource (konteks pesanan yang
// dibutuhkan untuk otorisasi), dan Timeline (menandai unggahan di timeline
// pesanan). Membaca `orders`/menulis `order_events` dari sini sah karena
// domain bukti memang milik kartu bukti transfer, dan querynya terpisah dari
// repository pesanan sehingga tidak ada ketergantungan paket.
package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo adalah repository bukti transfer berbasis sqlc.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
}

// New membuat repository bukti transfer.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool, q: sqlc.New(pool)} }

// CountByOrder menghitung bukti yang sudah tersimpan pada satu pesanan.
func (r *Repo) CountByOrder(ctx context.Context, orderID string) (int64, error) {
	n, err := r.q.CountPaymentProofs(ctx, orderID)
	if err != nil {
		return 0, fmt.Errorf("query CountPaymentProofs: %w", err)
	}
	return n, nil
}

// Add menyimpan metadata satu bukti.
//
// Indeks unik (order_id, sha256) di database adalah benteng terakhir anti
// duplikat: pelanggarannya (SQLSTATE 23505) diterjemahkan menjadi
// payment.ErrDuplicate supaya lapisan atas tidak pernah menyentuh kode pgx.
func (r *Repo) Add(ctx context.Context, p payment.Proof) (payment.Proof, error) {
	sum, err := hex.DecodeString(p.Sha256)
	if err != nil {
		return payment.Proof{}, fmt.Errorf("sha256 %q bukan hex: %w", p.Sha256, err)
	}
	row, err := r.q.InsertPaymentProof(ctx, sqlc.InsertPaymentProofParams{
		OrderID:        p.OrderID,
		StorageKey:     p.StorageKey,
		NamaFile:       p.NamaFile,
		MimeType:       p.MimeType,
		UkuranByte:     p.UkuranByte,
		Sha256:         sum,
		UploadedByKind: sqlc.ActorKind(p.UploadedByKind),
		UploadedByID:   pgtypes.UUID(p.UploadedByID),
		UploadedByNama: pgtypes.Text(p.UploadedByNama),
	})
	if err != nil {
		if isDuplicate(err) {
			return payment.Proof{}, payment.ErrDuplicate
		}
		return payment.Proof{}, fmt.Errorf("query InsertPaymentProof: %w", err)
	}
	return toProof(row.ID, row.OrderID, row.StorageKey, row.NamaFile, row.MimeType,
		row.UkuranByte, row.Sha256, row.UploadedByKind, row.UploadedByID, row.UploadedByNama, row.CreatedAt), nil
}

// ListByOrder mengembalikan seluruh bukti satu pesanan, terlama dulu.
func (r *Repo) ListByOrder(ctx context.Context, orderID string) ([]payment.Proof, error) {
	rows, err := r.q.ListPaymentProofs(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("query ListPaymentProofs: %w", err)
	}
	out := make([]payment.Proof, 0, len(rows))
	for _, row := range rows {
		out = append(out, toProof(row.ID, row.OrderID, row.StorageKey, row.NamaFile, row.MimeType,
			row.UkuranByte, row.Sha256, row.UploadedByKind, row.UploadedByID, row.UploadedByNama, row.CreatedAt))
	}
	return out, nil
}

// Get mengembalikan satu bukti MILIK pesanan tersebut.
func (r *Repo) Get(ctx context.Context, id, orderID string) (payment.Proof, error) {
	row, err := r.q.GetPaymentProof(ctx, sqlc.GetPaymentProofParams{ID: id, OrderID: orderID})
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.Proof{}, payment.ErrNotFound
	}
	if err != nil {
		return payment.Proof{}, fmt.Errorf("query GetPaymentProof: %w", err)
	}
	return toProof(row.ID, row.OrderID, row.StorageKey, row.NamaFile, row.MimeType,
		row.UkuranByte, row.Sha256, row.UploadedByKind, row.UploadedByID, row.UploadedByNama, row.CreatedAt), nil
}

// OrderForProof membaca konteks pesanan (pemilik + status).
func (r *Repo) OrderForProof(ctx context.Context, orderID string) (payment.OrderRef, error) {
	row, err := r.q.GetOrderForProof(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.OrderRef{}, payment.ErrNotFound
	}
	if err != nil {
		return payment.OrderRef{}, fmt.Errorf("query GetOrderForProof: %w", err)
	}
	return payment.OrderRef{
		ID:       row.ID,
		BranchID: row.BranchID,
		Status:   row.Status,
		Nomor:    pgtypes.TextOr(row.Nomor),
	}, nil
}

// RecordProofUpload menandai unggahan bukti di timeline pesanan. Nama
// pengunggah admin ditulis apa adanya (PRD AP-ORD-06); unggahan cabang
// ditandai sebagai datang dari cabang.
func (r *Repo) RecordProofUpload(ctx context.Context, orderID, nama, kind, actorID string) error {
	teks := "Bukti transfer diunggah oleh cabang"
	if kind == "admin" {
		if nama == "" {
			nama = "admin Pusat"
		}
		teks = "Bukti transfer diunggah oleh " + nama + " (admin Pusat)"
	}
	err := r.q.InsertProofOrderEvent(ctx, sqlc.InsertProofOrderEventParams{
		OrderID:   orderID,
		Icon:      pgtypes.Text("upload"),
		Teks:      teks,
		ActorKind: sqlc.ActorKind(kind),
		ActorID:   pgtypes.UUID(actorID),
	})
	if err != nil {
		return fmt.Errorf("query InsertProofOrderEvent: %w", err)
	}
	return nil
}

// isDuplicate mengenali pelanggaran indeks unik PostgreSQL (SQLSTATE 23505).
func isDuplicate(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// toProof menyusun nilai domain dari baris hasil query.
func toProof(id, orderID, storageKey, namaFile, mimeType string, ukuran int64, sum []byte,
	kind string, uploadedByID pgtype.UUID, uploadedByNama pgtype.Text, createdAt time.Time) payment.Proof {
	return payment.Proof{
		ID:             id,
		OrderID:        orderID,
		StorageKey:     storageKey,
		NamaFile:       namaFile,
		MimeType:       mimeType,
		UkuranByte:     ukuran,
		Sha256:         hex.EncodeToString(sum),
		UploadedByKind: kind,
		UploadedByID:   pgtypes.UUIDString(uploadedByID),
		UploadedByNama: pgtypes.TextOr(uploadedByNama),
		CreatedAt:      createdAt.UTC(),
	}
}
