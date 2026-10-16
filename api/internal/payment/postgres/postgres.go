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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/grahita/portal/api/internal/bank"
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

// EnqueueDestinationReevaluation menitipkan penilaian ulang dari hasil OCR
// yang sudah tersimpan. Seluruh bukti wajib mempunyai ocr_at, sehingga worker
// tidak akan membuka object storage atau memanggil provider lagi.
func (r *Repo) EnqueueDestinationReevaluation(ctx context.Context, orderID string) (payment.ReevaluateResult, error) {
	if r.jobs == nil {
		return payment.ReevaluateResult{}, payment.ErrQueueUnavailable
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return payment.ReevaluateResult{}, fmt.Errorf("mulai transaksi reevaluate tujuan: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	var version int64
	if err := tx.QueryRow(ctx, `SELECT o.status::text,b.bank_allowlist_version
 FROM orders o JOIN branches b ON b.id=o.branch_id
 WHERE o.id=$1 FOR SHARE OF o,b`, orderID).Scan(&status, &version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return payment.ReevaluateResult{}, payment.ErrNotFound
		}
		return payment.ReevaluateResult{}, err
	}
	if status != payment.StatusSubmitted {
		return payment.ReevaluateResult{}, payment.ErrNotSubmitted
	}

	rows, err := tx.Query(ctx, `SELECT id::text,ocr_at FROM payment_proofs WHERE order_id=$1 ORDER BY id`, orderID)
	if err != nil {
		return payment.ReevaluateResult{}, err
	}
	h := sha256.New()
	count := 0
	for rows.Next() {
		var id string
		var at *time.Time
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return payment.ReevaluateResult{}, err
		}
		if at == nil {
			rows.Close()
			return payment.ReevaluateResult{}, payment.ErrOCRResultMissing
		}
		if count > 0 {
			h.Write([]byte{0x1e})
		}
		h.Write([]byte(strings.ToLower(id)))
		h.Write([]byte{0x1f})
		h.Write([]byte(at.UTC().Format(time.RFC3339Nano)))
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return payment.ReevaluateResult{}, err
	}
	rows.Close()
	if count == 0 {
		return payment.ReevaluateResult{}, payment.ErrPaymentProofRequired
	}
	fingerprint := h.Sum(nil)

	var attempted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM verification_logs
 WHERE order_id=$1 AND metode='ai' AND branch_allowlist_version=$2 AND proof_set_fingerprint=$3)`,
		orderID, version, fingerprint).Scan(&attempted); err != nil {
		return payment.ReevaluateResult{}, err
	}
	if attempted {
		if err := tx.Commit(ctx); err != nil {
			return payment.ReevaluateResult{}, err
		}
		return payment.ReevaluateResult{Duplicate: true}, nil
	}
	inserted, err := r.jobs.InsertTx(ctx, tx, jobs.OCRBuktiArgs{
		OrderID: orderID, AllowlistVersion: version, ProofSetFingerprint: hex.EncodeToString(fingerprint),
	}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}})
	if err != nil {
		return payment.ReevaluateResult{}, fmt.Errorf("titipkan reevaluate tujuan: %w", err)
	}
	duplicate := inserted != nil && inserted.UniqueSkippedAsDuplicate
	if err := tx.Commit(ctx); err != nil {
		return payment.ReevaluateResult{}, err
	}
	return payment.ReevaluateResult{Queued: !duplicate, Duplicate: duplicate}, nil
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
	// Ambil lock policy sebelum lock baris order. Urutan global ini mencegah
	// deadlock dengan submit/verifikasi dan murah untuk unggahan C&C.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(22102, 2)`); err != nil {
		return nil, fmt.Errorf("kunci kebijakan rekening: %w", err)
	}

	// Sesudah lock policy, kunci baris pesanan: hanya satu unggahan per pesanan
	// yang boleh menghitung + menulis dalam satu waktu.
	locked, err := q.LockOrderForProof(ctx, req.OrderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, payment.ErrNotFound
		}
		return nil, fmt.Errorf("query LockOrderForProof: %w", err)
	}
	if !locked.IsCnc {
		var tersedia bool
		if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM branches b
			JOIN branch_bank_accounts bba ON bba.branch_id = b.id AND bba.is_allowed
			JOIN bank_accounts ba ON ba.id = bba.bank_account_id AND ba.is_active
			WHERE b.id = $1 AND b.is_active
		)`, locked.BranchID).Scan(&tersedia); err != nil {
			return nil, fmt.Errorf("periksa rekening efektif cabang: %w", err)
		}
		if !tersedia {
			return nil, bank.ErrUnavailable
		}
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
