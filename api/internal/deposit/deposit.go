// Package deposit memuat kontrak repository ledger deposit cabang.
package deposit

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound            = errors.New("deposit tidak ditemukan")
	ErrBranchMismatch      = errors.New("cabang deposit tidak cocok")
	ErrInsufficientBalance = errors.New("saldo deposit tidak cukup")
	ErrExceedsOrder        = errors.New("alokasi deposit melebihi total order")
	ErrOrderNotSubmitted   = errors.New("order belum submitted")
	ErrVersionConflict     = errors.New("versi deposit atau order berubah")
	ErrIdempotencyReused   = errors.New("idempotency key dipakai untuk payload lain")
	ErrReversalExceeds     = errors.New("reversal melebihi nilai aktif")
	ErrNegativeBalance     = errors.New("reversal akan membuat saldo negatif")
	ErrReconciliation      = errors.New("saldo cache tidak sama dengan journal")
	ErrInvalidKind         = errors.New("jenis transaksi deposit tidak sah")
	ErrCreditSourceInvalid = errors.New("sumber kredit deposit tidak sah")
	ErrCreditActorInvalid  = errors.New("pelaku kredit deposit tidak berwenang")
)

type Account struct {
	BranchID       string
	Balance        int64
	JournalVersion int64
}

type Transaction struct {
	ID                       string
	BranchID                 string
	Kind                     string
	Amount                   int64
	OrderID                  string
	OrderVersion             *int32
	ReversesTransactionID    string
	DepositVerificationLogID string
	OrderVerificationLogID   string
	SourceProofID            string
	CreatedAt                time.Time
}

type CreditInput struct {
	BranchID                 string
	Kind                     string
	Amount                   int64
	SourceProofID            string
	DepositVerificationLogID string
	OrderVerificationLogID   string
	OrderID                  string
	OrderVersion             *int32
	ApprovedNominal          int64
	AppliedToOrder           int64
	CreditedToDepositTotal   int64
	IdempotencyKey           string
	RequestFingerprint       []byte
	ActorKind                string
	ActorID                  string
	Reason                   string
}

// TopupApprovalInput adalah keputusan manual Superadmin yang harus mengubah
// top-up, menulis log verifikasi immutable, dan mengkredit journal dalam satu
// transaksi. DepositVerificationLogID sengaja tidak diterima dari pemanggil;
// repository membuatnya di dalam transaksi approval.
type TopupApprovalInput struct {
	TopupID                string
	BranchID               string
	BankAccountID          string
	Amount                 int64
	BranchAllowlistVersion int64
	DestinationResult      []byte
	IdempotencyKey         string
	RequestFingerprint     []byte
	ActorID                string
	Reason                 string
}

type TopupApprovalResult struct {
	VerificationLogID string
	Transaction       Transaction
	Account           Account
	Replayed          bool
}

type AllocationInput struct {
	BranchID               string
	OrderID                string
	Amount                 int64
	ExpectedJournalVersion int64
	ExpectedOrderVersion   int32
	IdempotencyKey         string
	RequestFingerprint     []byte
	ActorKind              string
	ActorID                string
}

type Allocation struct {
	ID                 string
	BranchID           string
	OrderID            string
	OrderVersion       int32
	DebitTransactionID string
	Amount             int64
	ReversedAmount     int64
	CreatedAt          time.Time
}

type AllocationResult struct {
	Allocation Allocation
	Account    Account
	Replayed   bool
}

type ReversalInput struct {
	BranchID               string
	OrderID                string
	AllocationID           string
	Amount                 int64
	ExpectedJournalVersion int64
	IdempotencyKey         string
	RequestFingerprint     []byte
	ActorKind              string
	ActorID                string
	Reason                 string
}

type ReversalResult struct {
	Transaction    Transaction
	ReversedAmount int64
	ActiveAmount   int64
	Account        Account
	Replayed       bool
}

type SourceSnapshot struct {
	AllocationID           string
	AllocationOrderVersion int32
	Amount                 int64
}

type SnapshotInput struct {
	VerificationLogID string
	BranchID          string
	OrderID           string
	OrderVersion      int32
	SettlementTotal   int64
}

type Repository interface {
	Account(context.Context, string) (Account, error)
	Credit(context.Context, CreditInput) (Transaction, Account, bool, error)
	ApproveTopup(context.Context, TopupApprovalInput) (TopupApprovalResult, error)
	Allocate(context.Context, AllocationInput) (AllocationResult, error)
	ReverseAllocation(context.Context, ReversalInput) (ReversalResult, error)
	SnapshotActiveAllocations(context.Context, SnapshotInput) ([]SourceSnapshot, error)
}
