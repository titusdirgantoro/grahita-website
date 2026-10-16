package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/deposit"
	depositpg "github.com/grahita/portal/api/internal/deposit/postgres"
	"github.com/grahita/portal/api/internal/platform/database"
)

type fixture struct {
	branchID, otherBranchID string
	adminID, bankID         string
	proofID, verificationID string
	orderA, orderB          string
}

func setup(t *testing.T) (context.Context, *database.Pool, *depositpg.Repo, fixture) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	if err := database.Up(ctx, url); err != nil {
		t.Fatalf("migrasi: %v", err)
	}
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := fmt.Sprintf("%d", time.Now().UnixNano())
	var f fixture
	if err := pool.QueryRow(ctx, `INSERT INTO branches(nama,nama_kepala,email)
VALUES($1,'Kepala UJI',$2) RETURNING id`, "UJI-Deposit-"+s, "uji-deposit-"+s+"@example.test").Scan(&f.branchID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO branches(nama,nama_kepala,email)
VALUES($1,'Kepala UJI',$2) RETURNING id`, "UJI-Deposit-Lain-"+s, "uji-deposit-lain-"+s+"@example.test").Scan(&f.otherBranchID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO admin_users(nama,email,role)
VALUES('UJI-Admin',$1,'superadmin') RETURNING id`, "uji-admin-deposit-"+s+"@example.test").Scan(&f.adminID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO bank_accounts(bank,nama_pemilik,nomor)
VALUES('BCA','UJI Pemilik',$1) RETURNING id`, "UJI"+s).Scan(&f.bankID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO branch_bank_accounts(branch_id,bank_account_id,is_allowed,assigned_by)
VALUES($1,$2,true,$3)`, f.branchID, f.bankID, f.adminID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO payment_proofs(
purpose,branch_id,order_id,storage_key,nama_file,mime_type,ukuran_byte,sha256,uploaded_by_kind)
VALUES('deposit_topup',$1,NULL,$2,'UJI-bukti','image/png',1,digest($3,'sha256'),'branch') RETURNING id`,
		f.branchID, "uji/deposit/"+s, s).Scan(&f.proofID); err != nil {
		t.Fatal(err)
	}
	var topupID string
	if err := pool.QueryRow(ctx, `INSERT INTO deposit_topups(
branch_id,bank_account_id,declared_amount,payment_proof_id,created_by_kind)
VALUES($1,$2,1000,$3,'branch') RETURNING id`, f.branchID, f.bankID, f.proofID).Scan(&topupID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO deposit_verification_logs(
topup_id,method,result,declared_amount,approved_amount,bank_account_id,
branch_allowlist_version,destination_result,admin_id)
VALUES($1,'manual','verified',1000,1000,$2,0,'{}',$3) RETURNING id`,
		topupID, f.bankID, f.adminID).Scan(&f.verificationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deposit_topups SET status='verified',verified_amount=1000,
verified_by=$2,verified_at=now() WHERE id=$1`, topupID, f.adminID); err != nil {
		t.Fatal(err)
	}
	for i, target := range []*string{&f.orderA, &f.orderB} {
		if err := pool.QueryRow(ctx, `INSERT INTO orders(
branch_id,nomor,versi,status,verif_status,tanggal,total,submitted_at)
VALUES($1,$2,0,'submitted','belum',current_date,1000,now()) RETURNING id`,
			f.branchID, fmt.Sprintf("UJI-DEP-%s-%d", s, i)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `TRUNCATE verification_proof_splits,
verification_deposit_sources,deposit_allocations,branch_deposit_transactions,
deposit_verification_logs,deposit_topups,payment_proof_economic_decisions,
branch_deposit_accounts`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM verification_logs WHERE order_id IN ($1,$2)`, f.orderA, f.orderB)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM payment_proofs WHERE id=$1`, f.proofID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM orders WHERE id IN ($1,$2)`, f.orderA, f.orderB)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM branch_bank_accounts WHERE branch_id=$1 AND bank_account_id=$2`, f.branchID, f.bankID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM bank_accounts WHERE id=$1`, f.bankID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM admin_users WHERE id=$1`, f.adminID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM branches WHERE id IN ($1,$2)`, f.branchID, f.otherBranchID)
	})
	return ctx, pool, depositpg.New(pool), f
}

func creditInput(f fixture, key string) deposit.CreditInput {
	return deposit.CreditInput{BranchID: f.branchID, Kind: "topup_credit", Amount: 1000,
		SourceProofID: f.proofID, DepositVerificationLogID: f.verificationID,
		IdempotencyKey: key, RequestFingerprint: []byte("credit-1000"), ActorKind: "admin", ActorID: f.adminID}
}

func TestPGCreditMenolakSumberDanPelakuTidakMengikat(t *testing.T) {
	t.Run("nominal berbeda", func(t *testing.T) {
		ctx, _, repo, f := setup(t)
		in := creditInput(f, "credit-amount-mismatch")
		in.Amount = 999
		if _, _, _, err := repo.Credit(ctx, in); !errors.Is(err, deposit.ErrCreditSourceInvalid) {
			t.Fatalf("amount mismatch err=%v", err)
		}
	})
	t.Run("aktor bukan pemutus", func(t *testing.T) {
		ctx, _, repo, f := setup(t)
		in := creditInput(f, "credit-actor-mismatch")
		in.ActorID = "00000000-0000-0000-0000-000000000001"
		if _, _, _, err := repo.Credit(ctx, in); !errors.Is(err, deposit.ErrCreditActorInvalid) {
			t.Fatalf("actor mismatch err=%v", err)
		}
	})
	t.Run("log bukan verified", func(t *testing.T) {
		ctx, pool, repo, f := setup(t)
		var topupID, logID string
		if err := pool.QueryRow(ctx, `SELECT id FROM deposit_topups WHERE payment_proof_id=$1`, f.proofID).Scan(&topupID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO deposit_verification_logs(
topup_id,method,result,declared_amount,approved_amount,bank_account_id,
branch_allowlist_version,destination_result,admin_id)
VALUES($1,'manual','needs_review',1000,1000,$2,0,'{}',$3) RETURNING id`,
			topupID, f.bankID, f.adminID).Scan(&logID); err != nil {
			t.Fatal(err)
		}
		in := creditInput(f, "credit-review-log")
		in.DepositVerificationLogID = logID
		if _, _, _, err := repo.Credit(ctx, in); !errors.Is(err, deposit.ErrCreditSourceInvalid) {
			t.Fatalf("non-verified log err=%v", err)
		}
	})
}

func TestPGCreditRetryAlokasiConcurrentDanReversalParsial(t *testing.T) {
	ctx, pool, repo, f := setup(t)
	type creditResult struct {
		transaction deposit.Transaction
		account     deposit.Account
		replay      bool
		err         error
	}
	credits := make([]creditResult, 2)
	var creditWG sync.WaitGroup
	for i := range credits {
		creditWG.Add(1)
		go func(i int) {
			defer creditWG.Done()
			credits[i].transaction, credits[i].account, credits[i].replay, credits[i].err =
				repo.Credit(ctx, creditInput(f, "credit-1"))
		}(i)
	}
	creditWG.Wait()
	var replayCount int
	for i, result := range credits {
		if result.err != nil || result.transaction.Amount != 1000 || result.account.Balance != 1000 {
			t.Fatalf("credit concurrent[%d]: %+v", i, result)
		}
		if result.replay {
			replayCount++
		}
	}
	if replayCount != 1 {
		t.Fatalf("credit concurrent replay=%d, ingin 1", replayCount)
	}
	beda := creditInput(f, "credit-1")
	beda.RequestFingerprint = []byte("credit-payload-berbeda")
	if _, _, _, err := repo.Credit(ctx, beda); !errors.Is(err, deposit.ErrIdempotencyReused) {
		t.Fatalf("key sama payload beda err=%v", err)
	}

	inputs := []deposit.AllocationInput{
		{BranchID: f.branchID, OrderID: f.orderA, Amount: 700, ExpectedJournalVersion: 1, ExpectedOrderVersion: 0, IdempotencyKey: "alloc-a", RequestFingerprint: []byte("a700"), ActorKind: "branch"},
		{BranchID: f.branchID, OrderID: f.orderB, Amount: 700, ExpectedJournalVersion: 1, ExpectedOrderVersion: 0, IdempotencyKey: "alloc-b", RequestFingerprint: []byte("b700"), ActorKind: "branch"},
	}
	results := make([]deposit.AllocationResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range inputs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = repo.Allocate(ctx, inputs[i]) }(i)
	}
	wg.Wait()
	success := -1
	for i, err := range errs {
		if err == nil {
			success = i
		} else if !errors.Is(err, deposit.ErrVersionConflict) && !errors.Is(err, deposit.ErrInsufficientBalance) {
			t.Fatalf("race error[%d]=%v", i, err)
		}
	}
	if success < 0 {
		t.Fatalf("tidak ada alokasi sukses: %v", errs)
	}
	allocationRetry, err := repo.Allocate(ctx, inputs[success])
	if err != nil || !allocationRetry.Replayed || allocationRetry.Account.Balance != 300 {
		t.Fatalf("retry allocation asli=%+v err=%v", allocationRetry, err)
	}
	account, err := repo.Account(ctx, f.branchID)
	if err != nil || account.Balance != 300 {
		t.Fatalf("saldo race=%+v err=%v", account, err)
	}

	a := results[success].Allocation
	rev1, err := repo.ReverseAllocation(ctx, deposit.ReversalInput{BranchID: f.branchID, OrderID: a.OrderID, AllocationID: a.ID,
		Amount: 200, ExpectedJournalVersion: 2, IdempotencyKey: "rev-1", RequestFingerprint: []byte("rev200"), ActorKind: "admin", ActorID: f.adminID, Reason: "UJI-koreksi"})
	if err != nil || rev1.ReversedAmount != 200 || rev1.Account.Balance != 500 {
		t.Fatalf("rev1=%+v err=%v", rev1, err)
	}
	rev1retry, err := repo.ReverseAllocation(ctx, deposit.ReversalInput{BranchID: f.branchID, OrderID: a.OrderID, AllocationID: a.ID,
		Amount: 200, ExpectedJournalVersion: 2, IdempotencyKey: "rev-1", RequestFingerprint: []byte("rev200"), ActorKind: "admin", ActorID: f.adminID, Reason: "UJI-koreksi"})
	if err != nil || !rev1retry.Replayed || rev1retry.Account.Balance != 500 {
		t.Fatalf("retry reversal=%+v err=%v", rev1retry, err)
	}
	rev2, err := repo.ReverseAllocation(ctx, deposit.ReversalInput{BranchID: f.branchID, OrderID: a.OrderID, AllocationID: a.ID,
		Amount: 200, ExpectedJournalVersion: 3, IdempotencyKey: "rev-2", RequestFingerprint: []byte("rev200b"), ActorKind: "admin", ActorID: f.adminID, Reason: "UJI-koreksi kedua"})
	if err != nil || rev2.ReversedAmount != 400 || rev2.ActiveAmount != 300 || rev2.Account.Balance != 700 {
		t.Fatalf("rev2=%+v err=%v", rev2, err)
	}
	_, err = repo.ReverseAllocation(ctx, deposit.ReversalInput{BranchID: f.branchID, OrderID: a.OrderID, AllocationID: a.ID,
		Amount: 400, ExpectedJournalVersion: 4, IdempotencyKey: "rev-over", RequestFingerprint: []byte("rev400"), ActorKind: "admin", ActorID: f.adminID, Reason: "UJI-melebihi"})
	if !errors.Is(err, deposit.ErrReversalExceeds) {
		t.Fatalf("over reversal err=%v", err)
	}
	raceReversals := []deposit.ReversalInput{
		{BranchID: f.branchID, OrderID: a.OrderID, AllocationID: a.ID, Amount: 300, ExpectedJournalVersion: 4, IdempotencyKey: "rev-race-a", RequestFingerprint: []byte("race-a"), ActorKind: "admin", ActorID: f.adminID, Reason: "UJI-sisa A"},
		{BranchID: f.branchID, OrderID: a.OrderID, AllocationID: a.ID, Amount: 300, ExpectedJournalVersion: 4, IdempotencyKey: "rev-race-b", RequestFingerprint: []byte("race-b"), ActorKind: "admin", ActorID: f.adminID, Reason: "UJI-sisa B"},
	}
	raceErrors := make([]error, len(raceReversals))
	var reverseWG sync.WaitGroup
	for i := range raceReversals {
		reverseWG.Add(1)
		go func(i int) {
			defer reverseWG.Done()
			_, raceErrors[i] = repo.ReverseAllocation(ctx, raceReversals[i])
		}(i)
	}
	reverseWG.Wait()
	var reverseSuccess int
	for _, raceErr := range raceErrors {
		if raceErr == nil {
			reverseSuccess++
		} else if !errors.Is(raceErr, deposit.ErrVersionConflict) && !errors.Is(raceErr, deposit.ErrReversalExceeds) {
			t.Fatalf("race reversal err=%v", raceErr)
		}
	}
	if reverseSuccess != 1 {
		t.Fatalf("race reversal sukses=%d, ingin 1 (%v)", reverseSuccess, raceErrors)
	}
	allocationLateRetry, err := repo.Allocate(ctx, inputs[success])
	if err != nil || !allocationLateRetry.Replayed || allocationLateRetry.Account.Balance != 300 || allocationLateRetry.Account.JournalVersion != 2 {
		t.Fatalf("late retry allocation tidak mengembalikan response awal: %+v err=%v", allocationLateRetry, err)
	}
	reversalLateRetry, err := repo.ReverseAllocation(ctx, deposit.ReversalInput{BranchID: f.branchID, OrderID: a.OrderID, AllocationID: a.ID,
		Amount: 200, ExpectedJournalVersion: 2, IdempotencyKey: "rev-1", RequestFingerprint: []byte("rev200"), ActorKind: "admin", ActorID: f.adminID, Reason: "UJI-koreksi"})
	if err != nil || !reversalLateRetry.Replayed || reversalLateRetry.ReversedAmount != 200 || reversalLateRetry.ActiveAmount != 500 || reversalLateRetry.Account.Balance != 500 {
		t.Fatalf("late retry reversal tidak mengembalikan response awal: %+v err=%v", reversalLateRetry, err)
	}
	var sum, balance int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(amount),0) FROM branch_deposit_transactions WHERE branch_id=$1`, f.branchID).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM branch_deposit_accounts WHERE branch_id=$1`, f.branchID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if sum != balance || balance != 1000 {
		t.Fatalf("invariant sum=%d balance=%d", sum, balance)
	}
}

func TestPGIsolasiCabangDanCarryForwardTanpaDebitUlang(t *testing.T) {
	ctx, pool, repo, f := setup(t)
	if _, _, _, err := repo.Credit(ctx, creditInput(f, "credit-carry")); err != nil {
		t.Fatal(err)
	}
	_, err := repo.Allocate(ctx, deposit.AllocationInput{BranchID: f.otherBranchID, OrderID: f.orderA, Amount: 100,
		ExpectedJournalVersion: 0, ExpectedOrderVersion: 0, IdempotencyKey: "cross", RequestFingerprint: []byte("cross"), ActorKind: "branch"})
	if !errors.Is(err, deposit.ErrBranchMismatch) {
		t.Fatalf("cross branch err=%v", err)
	}
	allocated, err := repo.Allocate(ctx, deposit.AllocationInput{BranchID: f.branchID, OrderID: f.orderA, Amount: 400,
		ExpectedJournalVersion: 1, ExpectedOrderVersion: 0, IdempotencyKey: "carry-alloc", RequestFingerprint: []byte("carry400"), ActorKind: "branch"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE orders SET versi=1 WHERE id=$1`, f.orderA); err != nil {
		t.Fatal(err)
	}
	var verificationID string
	if err := pool.QueryRow(ctx, `INSERT INTO verification_logs(
order_id,metode,total_pesanan,total_terbaca,hasil,order_version,deposit_amount,transfer_amount)
VALUES($1,'manual',1000,1000,'cocok',1,400,600) RETURNING id`, f.orderA).Scan(&verificationID); err != nil {
		t.Fatal(err)
	}
	sources, err := repo.SnapshotActiveAllocations(ctx, deposit.SnapshotInput{VerificationLogID: verificationID,
		BranchID: f.branchID, OrderID: f.orderA, OrderVersion: 1, SettlementTotal: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].AllocationID != allocated.Allocation.ID || sources[0].AllocationOrderVersion != 0 || sources[0].Amount != 400 {
		t.Fatalf("source carry-forward=%+v", sources)
	}
	before := allocated.Account.JournalVersion
	sources, err = repo.SnapshotActiveAllocations(ctx, deposit.SnapshotInput{VerificationLogID: verificationID,
		BranchID: f.branchID, OrderID: f.orderA, OrderVersion: 1, SettlementTotal: 1000})
	if err != nil || len(sources) != 1 {
		t.Fatalf("retry snapshot=%+v err=%v", sources, err)
	}
	account, err := repo.Account(ctx, f.branchID)
	if err != nil {
		t.Fatal(err)
	}
	if account.JournalVersion != before || account.Balance != 600 {
		t.Fatalf("snapshot mendebit ulang: %+v", account)
	}
}

func TestPGSnapshotRetryMenolakDistribusiBerubahDenganCountDanSumSama(t *testing.T) {
	ctx, pool, repo, f := setup(t)
	if _, _, _, err := repo.Credit(ctx, creditInput(f, "credit-snapshot-set")); err != nil {
		t.Fatal(err)
	}
	a1, err := repo.Allocate(ctx, deposit.AllocationInput{BranchID: f.branchID, OrderID: f.orderA, Amount: 200,
		ExpectedJournalVersion: 1, ExpectedOrderVersion: 0, IdempotencyKey: "snapshot-a1", RequestFingerprint: []byte("a1"), ActorKind: "branch"})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := repo.Allocate(ctx, deposit.AllocationInput{BranchID: f.branchID, OrderID: f.orderA, Amount: 200,
		ExpectedJournalVersion: 2, ExpectedOrderVersion: 0, IdempotencyKey: "snapshot-a2", RequestFingerprint: []byte("a2"), ActorKind: "branch"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE orders SET versi=1 WHERE id=$1`, f.orderA); err != nil {
		t.Fatal(err)
	}
	var verificationID string
	if err := pool.QueryRow(ctx, `INSERT INTO verification_logs(
order_id,metode,total_pesanan,total_terbaca,hasil,order_version,deposit_amount,transfer_amount)
VALUES($1,'manual',1000,1000,'cocok',1,400,600) RETURNING id`, f.orderA).Scan(&verificationID); err != nil {
		t.Fatal(err)
	}
	input := deposit.SnapshotInput{VerificationLogID: verificationID, BranchID: f.branchID,
		OrderID: f.orderA, OrderVersion: 1, SettlementTotal: 1000}
	if _, err := repo.SnapshotActiveAllocations(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deposit_allocations SET reversed_amount=CASE id WHEN $1 THEN 100 ELSE 200 END
WHERE id IN ($1,$2)`, a1.Allocation.ID, a2.Allocation.ID); err != nil {
		t.Fatal(err)
	}
	var debitID string
	if err := pool.QueryRow(ctx, `INSERT INTO branch_deposit_transactions(
branch_id,kind,amount,order_id,order_version,idempotency_key_hash,request_fingerprint,actor_kind)
VALUES($1,'order_allocation_debit',-300,$2,1,digest('snapshot-a3','sha256'),digest('a3','sha256'),'system') RETURNING id`,
		f.branchID, f.orderA).Scan(&debitID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO deposit_allocations(
branch_id,order_id,order_version,debit_transaction_id,amount,created_by_kind)
VALUES($1,$2,1,$3,300,'system')`, f.branchID, f.orderA, debitID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE branch_deposit_accounts
SET balance=balance-300,journal_version=journal_version+1 WHERE branch_id=$1`, f.branchID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SnapshotActiveAllocations(ctx, input); !errors.Is(err, deposit.ErrReconciliation) {
		t.Fatalf("retry distribusi berubah harus reconciliation, dapat %v", err)
	}
}
