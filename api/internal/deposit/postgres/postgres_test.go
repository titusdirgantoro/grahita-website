package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/grahita/portal/api/internal/deposit"
	depositpg "github.com/grahita/portal/api/internal/deposit/postgres"
)

func TestCreditOverpayStandaloneDitolakSebelumMembukaTransaksi(t *testing.T) {
	repo := depositpg.New(nil)
	version := int32(0)

	_, _, _, err := repo.Credit(context.Background(), deposit.CreditInput{
		Kind:         "overpay_credit",
		Amount:       200,
		OrderVersion: &version,
	})
	if !errors.Is(err, deposit.ErrOverpaySettlementRequired) {
		t.Fatalf("Credit() error = %v, ingin ErrOverpaySettlementRequired", err)
	}
}
