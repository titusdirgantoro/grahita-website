package deposit_test

import (
	"testing"

	"github.com/grahita/portal/api/internal/deposit"
)

func TestErrorKontrakBerbeda(t *testing.T) {
	errors := []error{
		deposit.ErrNotFound,
		deposit.ErrBranchMismatch,
		deposit.ErrInsufficientBalance,
		deposit.ErrExceedsOrder,
		deposit.ErrOrderNotSubmitted,
		deposit.ErrVersionConflict,
		deposit.ErrIdempotencyReused,
		deposit.ErrReversalExceeds,
		deposit.ErrNegativeBalance,
		deposit.ErrReconciliation,
	}
	seen := map[string]bool{}
	for _, err := range errors {
		if err == nil || err.Error() == "" {
			t.Fatal("error kontrak deposit tidak boleh kosong")
		}
		if seen[err.Error()] {
			t.Fatalf("pesan error kontrak duplikat: %q", err)
		}
		seen[err.Error()] = true
	}
}
