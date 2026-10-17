package postgres

// Reversal plan revisi turun (C0 §3.4, §4.3). Dijalankan di dalam transaksi
// revisi setelah cabang, akun deposit, order, dan seluruh alokasi terkunci.
// Service tidak pernah memilih alokasi atau nominal sendiri; plan yang salah
// menggagalkan seluruh revisi (tanpa journal yatim).

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/deposit"
	depositpg "github.com/grahita/portal/api/internal/deposit/postgres"
	"github.com/grahita/portal/api/internal/order"
)

func reversePlan(ctx context.Context, tx pgx.Tx, row sqlc.LockOrderForReviseRow, req order.ReviseRequest, totalBaru int64) error {
	if len(req.DepositReversals) == 0 {
		return nil
	}
	if totalBaru >= row.Total {
		return fmt.Errorf("%w: reversal plan hanya untuk revisi yang menurunkan total", order.ErrReversalPlanInvalid)
	}
	raw, _ := json.Marshal([]any{"revision_reversal_plan", req.OrderID, row.Versi, req.DepositReversals})
	sum := sha256.Sum256(raw)
	for _, p := range req.DepositReversals {
		_, _, _, err := depositpg.ReverseLocked(ctx, tx, deposit.ReversalInput{
			BranchID: row.BranchID, OrderID: req.OrderID, AllocationID: p.AllocationID, Amount: p.Amount,
			IdempotencyKey: req.IdempotencyKey + "\x1f" + p.AllocationID, RequestFingerprint: sum[:],
			ActorKind: "admin", ActorID: req.Actor.UserID, Reason: p.Reason,
		})
		switch {
		case errors.Is(err, deposit.ErrNotFound), errors.Is(err, deposit.ErrBranchMismatch):
			return fmt.Errorf("%w: alokasi %s bukan milik pesanan ini", order.ErrReversalPlanInvalid, p.AllocationID)
		case errors.Is(err, deposit.ErrReversalExceeds):
			return fmt.Errorf("%w: nominal reversal alokasi %s melebihi bagian aktif", order.ErrReversalPlanInvalid, p.AllocationID)
		case err != nil:
			return err
		}
	}
	return nil
}
