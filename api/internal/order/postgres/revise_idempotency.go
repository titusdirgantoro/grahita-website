package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grahita/portal/api/internal/order"
)

type revisionFingerprintPayload struct {
	OrderID          string                       `json:"order_id"`
	Alasan           string                       `json:"alasan"`
	Catatan          string                       `json:"catatan"`
	Lines            []order.ReviseLine           `json:"lines"`
	DepositReversals []order.DepositReversalInput `json:"deposit_reversals"`
}

func revisionKeyHash(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

func revisionFingerprint(req order.ReviseRequest) ([]byte, error) {
	raw, err := json.Marshal(revisionFingerprintPayload{
		OrderID: req.OrderID, Alasan: req.Alasan, Catatan: req.Catatan,
		Lines: req.Lines, DepositReversals: req.DepositReversals,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal fingerprint revisi: %w", err)
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func decodeRevisionReplay(orderMatches bool, storedFingerprint, raw, fingerprint []byte) (order.ReviseOutcome, bool, error) {
	if !orderMatches || !bytes.Equal(storedFingerprint, fingerprint) {
		return order.ReviseOutcome{}, false, order.ErrRevisionIdempotencyReused
	}
	var out order.ReviseOutcome
	if err := json.Unmarshal(raw, &out); err != nil {
		return order.ReviseOutcome{}, false, fmt.Errorf("decode replay revisi: %w", err)
	}
	out.Replayed = true
	return out, true, nil
}

// Replay membaca hanya hasil committed dan sengaja tidak memeriksa queue,
// status invoice, atau setelan saat ini. Dengan demikian respons transaksi
// pertama tetap dapat dipulihkan setelah timeout meski lingkungan berubah.
func (r *Repo) Replay(ctx context.Context, req order.ReviseRequest) (order.ReviseOutcome, bool, error) {
	if req.IdempotencyKey == "" {
		return order.ReviseOutcome{}, false, nil
	}
	fingerprint, err := revisionFingerprint(req)
	if err != nil {
		return order.ReviseOutcome{}, false, err
	}
	var orderMatches bool
	var storedFingerprint, raw []byte
	err = r.pool.QueryRow(ctx, `SELECT order_id=$3::uuid,request_fingerprint,result
FROM invoice_revision_requests
WHERE admin_id=$1 AND idempotency_key_hash=$2`, req.AdminID, revisionKeyHash(req.IdempotencyKey), req.OrderID).
		Scan(&orderMatches, &storedFingerprint, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return order.ReviseOutcome{}, false, nil
	}
	if err != nil {
		return order.ReviseOutcome{}, false, fmt.Errorf("baca replay revisi: %w", err)
	}
	return decodeRevisionReplay(orderMatches, storedFingerprint, raw, fingerprint)
}

// loadRevisionReplay dipanggil setelah order dikunci. Dengan urutan ini,
// request konkuren pada order yang sama melihat row retry yang di-commit oleh
// request pertama sebelum memeriksa status invoice versi berjalan.
func loadRevisionReplay(ctx context.Context, tx pgx.Tx, req order.ReviseRequest) (order.ReviseOutcome, bool, error) {
	if req.IdempotencyKey == "" {
		return order.ReviseOutcome{}, false, nil
	}
	fingerprint, err := revisionFingerprint(req)
	if err != nil {
		return order.ReviseOutcome{}, false, err
	}
	var orderMatches bool
	var storedFingerprint, raw []byte
	err = tx.QueryRow(ctx, `SELECT order_id=$3::uuid,request_fingerprint,result
FROM invoice_revision_requests
WHERE admin_id=$1 AND idempotency_key_hash=$2`, req.AdminID, revisionKeyHash(req.IdempotencyKey), req.OrderID).
		Scan(&orderMatches, &storedFingerprint, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return order.ReviseOutcome{}, false, nil
	}
	if err != nil {
		return order.ReviseOutcome{}, false, fmt.Errorf("baca replay revisi: %w", err)
	}
	return decodeRevisionReplay(orderMatches, storedFingerprint, raw, fingerprint)
}

func storeRevisionReplay(ctx context.Context, tx pgx.Tx, req order.ReviseRequest, out order.ReviseOutcome) error {
	if req.IdempotencyKey == "" {
		return nil
	}
	fingerprint, err := revisionFingerprint(req)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("encode hasil revisi: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO invoice_revision_requests
(admin_id,idempotency_key_hash,order_id,request_fingerprint,result)
VALUES($1,$2,$3,$4,$5::jsonb)`, req.AdminID, revisionKeyHash(req.IdempotencyKey), req.OrderID, fingerprint, raw)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return order.ErrRevisionIdempotencyReused
		}
		return fmt.Errorf("simpan replay revisi: %w", err)
	}
	return nil
}
