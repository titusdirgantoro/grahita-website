// Package postgres adalah implementasi repository bukti transfer di
// PostgreSQL.
//
// Paket ini memenuhi dua kontrak domain sekaligus (pola yang sama dipakai
// domain email): Repository (metadata bukti + komit atomik beserta timeline)
// dan OrderSource (konteks pesanan yang dibutuhkan untuk otorisasi). Membaca
// `orders`/menulis `order_events` dari sini sah karena domain bukti memang
// milik kartu bukti transfer, dan querynya terpisah dari repository pesanan
// sehingga tidak ada ketergantungan paket.
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
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo adalah repository bukti transfer berbasis sqlc.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
	// jobs menitipkan pekerjaan River pada transaksi verifikasi (F1-18).
	// Boleh nil: tanpa penjadwal, verifikasi ditolak alih-alih menyelesaikan
	// pesanan tanpa dokumen/email.
	jobs JobInserter
}

// New membuat repository bukti transfer.
func New(pool *database.Pool, opts ...Option) *Repo {
	r := &Repo{pool: pool, q: sqlc.New(pool)}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Sha256ByOrder mengembalikan sidik jari isi seluruh bukti satu pesanan,
// terlama dulu. Dipakai domain memeriksa duplikat SEBELUM berkas ditulis ke
// object storage.
func (r *Repo) Sha256ByOrder(ctx context.Context, orderID string) ([]string, error) {
	rows, err := r.q.Sha256PaymentProofs(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("query Sha256PaymentProofs: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, sum := range rows {
		out = append(out, hex.EncodeToString(sum))
	}
	return out, nil
}

// CommitProofs menyimpan seluruh metadata bukti satu permintaan beserta
// peristiwa timeline-nya dalam SATU transaksi.
//
// Baris pesanan dikunci (`FOR UPDATE`) sebelum hitungan dibaca, sehingga
// hitung-dan-tulis menjadi aman-balapan: dua unggahan paralel pada pesanan yang
// sama berbaris rapi dan batas `max_file` tidak bisa dilampaui. Bila ada satu
// baris yang ditolak (duplikat, batas, atau error lain), transaksi dibatalkan
// dan tidak ada metadata/timeline yang tersisa.
func (r *Repo) CommitProofs(ctx context.Context, req payment.CommitRequest) ([]payment.Proof, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("mulai transaksi bukti: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)

	// Kunci baris pesanan lebih dulu: hanya satu unggahan per pesanan yang
	// boleh menghitung + menulis dalam satu waktu.
	if _, err := q.LockOrderForProof(ctx, req.OrderID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, payment.ErrNotFound
		}
		return nil, fmt.Errorf("query LockOrderForProof: %w", err)
	}
	ada, err := q.CountPaymentProofs(ctx, req.OrderID)
	if err != nil {
		return nil, fmt.Errorf("query CountPaymentProofs: %w", err)
	}
	if ada+int64(len(req.Proofs)) > req.MaxFile {
		return nil, fmt.Errorf("%w: maksimal %d berkas per pesanan (tersimpan %d)",
			payment.ErrTooMany, req.MaxFile, ada)
	}

	out := make([]payment.Proof, 0, len(req.Proofs))
	for i, p := range req.Proofs {
		sum, err := hex.DecodeString(p.Sha256)
		if err != nil {
			return nil, fmt.Errorf("sha256 %q bukan hex: %w", p.Sha256, err)
		}
		row, err := q.InsertPaymentProof(ctx, sqlc.InsertPaymentProofParams{
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
				return nil, fmt.Errorf("berkas ke-%d: %w", i+1, payment.ErrDuplicate)
			}
			return nil, fmt.Errorf("query InsertPaymentProof: %w", err)
		}
		out = append(out, toProof(row.ID, row.OrderID, row.StorageKey, row.NamaFile, row.MimeType,
			row.UkuranByte, row.Sha256, row.UploadedByKind, row.UploadedByID, row.UploadedByNama, row.CreatedAt))
	}

	for _, e := range req.Events {
		if err := q.InsertProofOrderEvent(ctx, sqlc.InsertProofOrderEventParams{
			OrderID:   req.OrderID,
			Icon:      pgtypes.Text("upload"),
			Teks:      teksEvent(e.Kind, e.Nama),
			ActorKind: sqlc.ActorKind(e.Kind),
			ActorID:   pgtypes.UUID(e.ActorID),
		}); err != nil {
			return nil, fmt.Errorf("query InsertProofOrderEvent: %w", err)
		}
	}

	// Pekerjaan pembacaan AI/OCR (kartu F2-26) DITITIPKAN pada transaksi yang
	// sama: bukti yang tersimpan tidak pernah kehilangan pembacaannya, dan job
	// yang batal ikut rollback bersama barisnya (SystemDesign.md §4.2/§5.1).
	// Tanpa penjadwal, unggahan DITOLAK alih-alih menyimpan bukti yang tidak
	// akan pernah dibaca AI — gagal terang-terangan, bukan setengah jalan.
	if req.EnqueueOCR {
		if r.jobs == nil {
			return nil, fmt.Errorf("%w: penjadwal pekerjaan pembacaan bukti belum terpasang", payment.ErrQueueUnavailable)
		}
		if _, err := r.jobs.InsertTx(ctx, tx, jobs.OCRBuktiArgs{OrderID: req.OrderID}, nil); err != nil {
			return nil, fmt.Errorf("titipkan pekerjaan pembacaan bukti: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit bukti: %w", err)
	}
	return out, nil
}

// teksEvent menyusun kalimat timeline unggahan bukti: nama pengunggah admin
// ditulis apa adanya (PRD AP-ORD-06), unggahan cabang ditandai sebagai cabang.
func teksEvent(kind, nama string) string {
	if kind != "admin" {
		return "Bukti transfer diunggah oleh cabang"
	}
	if nama == "" {
		nama = "admin Pusat"
	}
	return "Bukti transfer diunggah oleh " + nama + " (admin Pusat)"
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
