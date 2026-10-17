package deposit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// Error domain C2 (D3 §6.3). Error ledger C1 di deposit.go tetap dipakai apa
// adanya; yang di sini mengatur otorisasi, validasi permintaan, dan top-up.
var (
	ErrForbidden           = errors.New("tidak berwenang atas deposit")
	ErrNoBranch            = errors.New("sesi cabang tanpa cabang")
	ErrTopupNotFound       = errors.New("top-up deposit tidak ditemukan")
	ErrAllocationNotFound  = errors.New("alokasi deposit tidak ditemukan")
	ErrTopupDecided        = errors.New("top-up sudah diputuskan")
	ErrBankNotAllowed      = errors.New("rekening bukan rekening efektif cabang")
	ErrBankUnavailable     = errors.New("cabang belum punya rekening efektif")
	ErrProofUsed           = errors.New("bukti sudah dipakai untuk top-up lain")
	ErrIdempotencyRequired = errors.New("header Idempotency-Key wajib")
	ErrOrderDone           = errors.New("order sudah Done")
	ErrReasonRequired      = errors.New("alasan wajib diisi")
	ErrProofRequired       = errors.New("bukti top-up wajib diunggah")
	ErrInvalidAmount       = errors.New("nominal harus lebih dari nol")
	ErrInvalidAction       = errors.New("aksi keputusan top-up tidak dikenal")
	ErrStorageUnavailable  = errors.New("penyimpanan bukti tidak tersedia")
)

// Status top-up (enum deposit_topup_status, D3 §3.1).
const (
	TopupPendingReview = "pending_review"
	TopupNeedsReview   = "needs_review"
	TopupVerified      = "verified"
	TopupRejected      = "rejected"
	TopupReversed      = "reversed"
)

// Aksi keputusan admin atas top-up (D3 §6.2).
const (
	ActionReview = "review"
	ActionReject = "reject"
	ActionVerify = "verify"
)

// Summary adalah saldo cabang untuk portal cabang (GET /deposit). Top-up yang
// belum diputuskan ditampilkan terpisah dan tidak pernah dijumlahkan ke saldo.
type Summary struct {
	BranchID                   string `json:"branch_id"`
	AvailableBalance           int64  `json:"available_balance"`
	JournalVersion             int64  `json:"journal_version"`
	PendingTopupCount          int    `json:"pending_topup_count"`
	PendingTopupAmountDeclared int64  `json:"pending_topup_amount_declared"`
}

// TransactionView adalah satu baris journal untuk tampilan: tanpa catatan
// internal, aktor, atau nomor rekening.
type TransactionView struct {
	ID                    string    `json:"id"`
	Kind                  string    `json:"kind"`
	Amount                int64     `json:"amount"`
	OrderID               string    `json:"order_id,omitempty"`
	OrderNomor            string    `json:"order_nomor,omitempty"`
	TopupID               string    `json:"topup_id,omitempty"`
	ReversesTransactionID string    `json:"reverses_transaction_id,omitempty"`
	ReversedAmount        int64     `json:"reversed_amount"`
	CreatedAt             time.Time `json:"created_at"`
}

// Topup adalah satu pengajuan top-up beserta keputusannya.
type Topup struct {
	ID              string     `json:"id"`
	BranchID        string     `json:"branch_id"`
	BranchNama      string     `json:"branch_nama"`
	BankAccountID   string     `json:"bank_account_id"`
	Bank            string     `json:"bank"`
	DisplayNomor    string     `json:"display_nomor"`
	Status          string     `json:"status"`
	DeclaredAmount  int64      `json:"declared_amount"`
	VerifiedAmount  *int64     `json:"verified_amount,omitempty"`
	RejectionReason string     `json:"rejection_reason,omitempty"`
	ProofID         string     `json:"payment_proof_id"`
	ProofNamaFile   string     `json:"proof_nama_file"`
	ProofMimeType   string     `json:"proof_mime_type"`
	OCRNominal      *int64     `json:"ocr_nominal,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	VerifiedAt      *time.Time `json:"verified_at,omitempty"`
	// ProofKey tidak pernah dikirim ke klien; dipakai menerbitkan tautan unduh.
	ProofKey string `json:"-"`
}

// BranchOverview adalah satu baris antrean admin (GET /admin/deposits).
type BranchOverview struct {
	BranchID           string `json:"branch_id"`
	BranchNama         string `json:"branch_nama"`
	AvailableBalance   int64  `json:"available_balance"`
	JournalVersion     int64  `json:"journal_version"`
	PendingTopupCount  int    `json:"pending_topup_count"`
	PendingTopupAmount int64  `json:"pending_topup_amount_declared"`
}

// BranchDetail adalah halaman detail deposit satu cabang untuk admin.
type BranchDetail struct {
	Summary
	BranchNama   string            `json:"branch_nama"`
	Topups       []Topup           `json:"topups"`
	Transactions []TransactionView `json:"transactions"`
	Reconciled   bool              `json:"reconciled"`
}

// AllocationView adalah alokasi deposit satu order beserta nilai aktifnya.
type AllocationView struct {
	ID             string    `json:"id"`
	OrderID        string    `json:"order_id"`
	OrderVersion   int32     `json:"order_version"`
	Amount         int64     `json:"amount"`
	ReversedAmount int64     `json:"reversed_amount"`
	ActiveAmount   int64     `json:"active_amount"`
	CreatedAt      time.Time `json:"created_at"`
}

// OrderRef adalah konteks order yang dibutuhkan alokasi.
type OrderRef struct {
	ID       string
	BranchID string
	Status   string
	Versi    int32
	Total    int64
}

// TopupFilter menyaring antrean top-up admin.
type TopupFilter struct {
	BranchID string
	Status   string
	Limit    int
	Offset   int
}

// StoredProof adalah bukti yang sudah tersimpan di object storage.
type StoredProof struct {
	StorageKey string
	NamaFile   string
	MimeType   string
	UkuranByte int64
	Sha256Hex  string
}

// ProofURL adalah tautan unduh berumur pendek.
type ProofURL struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
	NamaFile  string `json:"nama_file"`
	MimeType  string `json:"mime_type"`
}

// BankAccount adalah rekening efektif cabang beserta snapshot tujuan.
type BankAccount struct {
	ID          string
	Bank        string
	NamaPemilik string
	LastDigits  string
}

// EffectiveAccounts adalah himpunan rekening efektif cabang pada versi
// allowlist tertentu (D2).
type EffectiveAccounts struct {
	Accounts []BankAccount
	Version  int64
}

// CreateTopupInput adalah metadata top-up yang dicatat satu transaksi bersama
// bukti mandirinya.
type CreateTopupInput struct {
	BranchID       string
	BankAccountID  string
	DeclaredAmount int64
	Proof          StoredProof
	ActorKind      string
	ActorID        string
	// Audit dicatat repository dalam transaksi yang sama; EntityID diisi id
	// top-up baru.
	Audit audit.Entry
}

// MarkTopupInput adalah keputusan review/reject atas top-up.
type MarkTopupInput struct {
	TopupID          string
	Action           string
	ActorID          string
	Reason           string
	AllowlistVersion int64
	Audit            audit.Entry
}

// QueryRepository adalah sisi baca dan pencatatan top-up yang dipakai Service
// di atas ledger C1 (Repository).
type QueryRepository interface {
	Repository
	Summary(ctx context.Context, branchID string) (Summary, error)
	Transactions(ctx context.Context, branchID string, limit, offset int) ([]TransactionView, int, error)
	CreateTopup(ctx context.Context, in CreateTopupInput) (Topup, bool, error)
	Topups(ctx context.Context, f TopupFilter) ([]Topup, int, error)
	Topup(ctx context.Context, id string) (Topup, error)
	MarkTopup(ctx context.Context, in MarkTopupInput) (Topup, error)
	Overview(ctx context.Context) ([]BranchOverview, error)
	BranchNama(ctx context.Context, branchID string) (string, error)
	Reconciled(ctx context.Context, branchID string) (bool, error)
	OrderRef(ctx context.Context, orderID string) (OrderRef, error)
	OrderAllocations(ctx context.Context, orderID string) ([]AllocationView, error)
}

// ProofStore menyimpan bukti mandiri dan menerbitkan tautan unduhnya.
type ProofStore interface {
	Store(ctx context.Context, owner, namaFile string, content io.Reader, size int64) (StoredProof, error)
	Discard(ctx context.Context, key string)
	URL(ctx context.Context, key, namaFile, mime string) (ProofURL, error)
}

// BankSource memberi rekening efektif cabang (allowlist D2).
type BankSource interface {
	EffectiveForBranch(ctx context.Context, branchID string) (EffectiveAccounts, error)
}

// Service adalah aturan bisnis deposit C2 di atas ledger C1.
type Service struct {
	repo   QueryRepository
	proofs ProofStore
	banks  BankSource
}

// NewService merangkai Service deposit.
func NewService(repo QueryRepository, proofs ProofStore, banks BankSource) *Service {
	return &Service{repo: repo, proofs: proofs, banks: banks}
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validUUID(id string) bool { return uuidRe.MatchString(strings.TrimSpace(id)) }

// branchOf mengembalikan cabang sesi untuk aktor cabang ber-capability deposit.
func branchOf(actor auth.Actor) (string, error) {
	if !actor.IsBranch() || !actor.Can(auth.CapDepositSelf) {
		return "", ErrForbidden
	}
	if actor.BranchID == "" {
		return "", ErrNoBranch
	}
	return actor.BranchID, nil
}

func requireAdmin(actor auth.Actor, c auth.Capability) error {
	if !actor.IsAdmin() || !actor.Can(c) {
		return ErrForbidden
	}
	return nil
}

// fingerprint mengikat idempotency key ke isi permintaan: key yang sama dengan
// payload lain ditolak IDEMPOTENCY_KEY_REUSED oleh ledger.
func fingerprint(parts ...any) []byte {
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return sum[:]
}

// Summary mengembalikan saldo cabang sesi.
func (s *Service) Summary(ctx context.Context, actor auth.Actor) (Summary, error) {
	branchID, err := branchOf(actor)
	if err != nil {
		return Summary{}, err
	}
	return s.repo.Summary(ctx, branchID)
}

// Transactions mengembalikan journal cabang sesi, terbaru dulu.
func (s *Service) Transactions(ctx context.Context, actor auth.Actor, limit, offset int) ([]TransactionView, int, error) {
	branchID, err := branchOf(actor)
	if err != nil {
		return nil, 0, err
	}
	return s.repo.Transactions(ctx, branchID, clampLimit(limit), max(offset, 0))
}

// MyTopups mengembalikan top-up cabang sesi.
func (s *Service) MyTopups(ctx context.Context, actor auth.Actor, limit, offset int) ([]Topup, int, error) {
	branchID, err := branchOf(actor)
	if err != nil {
		return nil, 0, err
	}
	return s.repo.Topups(ctx, TopupFilter{BranchID: branchID, Limit: clampLimit(limit), Offset: max(offset, 0)})
}

func clampLimit(limit int) int {
	if limit <= 0 || limit > 100 {
		return 50
	}
	return limit
}

// TopupRequest adalah pengajuan top-up dari cabang (POST /deposit/topups).
type TopupRequest struct {
	BankAccountID  string
	DeclaredAmount int64
	IdempotencyKey string
	NamaFile       string
	Content        io.Reader
	Size           int64
}

// CreateTopup menyimpan bukti dan mencatat top-up pending_review. Tidak ada
// journal: saldo hanya bertambah lewat keputusan verify Superadmin (D3 §3.3).
func (s *Service) CreateTopup(ctx context.Context, actor auth.Actor, in TopupRequest) (Topup, bool, error) {
	branchID, err := branchOf(actor)
	if err != nil {
		return Topup{}, false, err
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return Topup{}, false, ErrIdempotencyRequired
	}
	if in.DeclaredAmount <= 0 {
		return Topup{}, false, ErrInvalidAmount
	}
	if in.Content == nil {
		return Topup{}, false, ErrProofRequired
	}
	if _, err := s.effectiveAccount(ctx, branchID, in.BankAccountID); err != nil {
		return Topup{}, false, err
	}
	stored, err := s.proofs.Store(ctx, "topups/"+branchID, in.NamaFile, in.Content, in.Size)
	if err != nil {
		return Topup{}, false, err
	}
	out, replayed, err := s.repo.CreateTopup(ctx, CreateTopupInput{
		BranchID: branchID, BankAccountID: strings.TrimSpace(in.BankAccountID),
		DeclaredAmount: in.DeclaredAmount, Proof: stored,
		ActorKind: string(actor.Kind), ActorID: actor.UserID,
		Audit: audit.NewEntry(ctx, actor, audit.EntityDepositTopup, "", audit.ActionCreate, nil,
			map[string]any{"declared_amount": in.DeclaredAmount, "bank_account_id": in.BankAccountID}),
	})
	if err != nil || replayed {
		// Bukti identik yang sudah tercatat tidak menyimpan objek kedua.
		s.proofs.Discard(ctx, stored.StorageKey)
	}
	return out, replayed, err
}

// effectiveAccount memastikan rekening tujuan adalah rekening efektif cabang
// (D2): tanpa assignment efektif permintaan ditolak fail-closed.
func (s *Service) effectiveAccount(ctx context.Context, branchID, bankAccountID string) (EffectiveAccounts, error) {
	if !validUUID(bankAccountID) {
		return EffectiveAccounts{}, ErrBankNotAllowed
	}
	set, err := s.banks.EffectiveForBranch(ctx, branchID)
	if err != nil {
		return EffectiveAccounts{}, err
	}
	if len(set.Accounts) == 0 {
		return EffectiveAccounts{}, ErrBankUnavailable
	}
	for _, a := range set.Accounts {
		if a.ID == strings.TrimSpace(bankAccountID) {
			return set, nil
		}
	}
	return EffectiveAccounts{}, ErrBankNotAllowed
}

// AdminOverview mengembalikan saldo dan antrean pending seluruh cabang.
func (s *Service) AdminOverview(ctx context.Context, actor auth.Actor) ([]BranchOverview, error) {
	if err := requireAdmin(actor, auth.CapAdminVerify); err != nil {
		return nil, err
	}
	return s.repo.Overview(ctx)
}

// AdminTopups mengembalikan antrean top-up untuk admin.
func (s *Service) AdminTopups(ctx context.Context, actor auth.Actor, f TopupFilter) ([]Topup, int, error) {
	if err := requireAdmin(actor, auth.CapAdminVerify); err != nil {
		return nil, 0, err
	}
	if f.BranchID != "" && !validUUID(f.BranchID) {
		return nil, 0, ErrNotFound
	}
	f.Limit, f.Offset = clampLimit(f.Limit), max(f.Offset, 0)
	return s.repo.Topups(ctx, f)
}

// AdminBranch mengembalikan detail deposit satu cabang.
func (s *Service) AdminBranch(ctx context.Context, actor auth.Actor, branchID string) (BranchDetail, error) {
	if err := requireAdmin(actor, auth.CapAdminVerify); err != nil {
		return BranchDetail{}, err
	}
	if !validUUID(branchID) {
		return BranchDetail{}, ErrNotFound
	}
	nama, err := s.repo.BranchNama(ctx, branchID)
	if err != nil {
		return BranchDetail{}, err
	}
	sum, err := s.repo.Summary(ctx, branchID)
	if err != nil {
		return BranchDetail{}, err
	}
	topups, _, err := s.repo.Topups(ctx, TopupFilter{BranchID: branchID, Limit: 100})
	if err != nil {
		return BranchDetail{}, err
	}
	tx, _, err := s.repo.Transactions(ctx, branchID, 100, 0)
	if err != nil {
		return BranchDetail{}, err
	}
	ok, err := s.repo.Reconciled(ctx, branchID)
	if err != nil {
		return BranchDetail{}, err
	}
	return BranchDetail{Summary: sum, BranchNama: nama, Topups: topups, Transactions: tx, Reconciled: ok}, nil
}

// TopupProofURL menerbitkan tautan unduh bukti top-up. Cabang hanya boleh
// melihat bukti miliknya; admin butuh capability verifikasi.
func (s *Service) TopupProofURL(ctx context.Context, actor auth.Actor, topupID string) (ProofURL, error) {
	if !validUUID(topupID) {
		return ProofURL{}, ErrTopupNotFound
	}
	if actor.IsBranch() {
		if _, err := branchOf(actor); err != nil {
			return ProofURL{}, err
		}
	} else if err := requireAdmin(actor, auth.CapAdminVerify); err != nil {
		return ProofURL{}, err
	}
	t, err := s.repo.Topup(ctx, topupID)
	if err != nil {
		return ProofURL{}, err
	}
	if actor.IsBranch() && t.BranchID != actor.BranchID {
		return ProofURL{}, ErrTopupNotFound
	}
	return s.proofs.URL(ctx, t.ProofKey, t.ProofNamaFile, t.ProofMimeType)
}

// DecisionRequest adalah keputusan admin atas top-up (D3 §6.2).
type DecisionRequest struct {
	Action         string
	ApprovedAmount int64
	BankAccountID  string
	Reason         string
	IdempotencyKey string
}

// DecisionResult adalah hasil keputusan beserta saldo setelahnya.
type DecisionResult struct {
	Topup    Topup    `json:"topup"`
	Balance  *Summary `json:"balance,omitempty"`
	Replayed bool     `json:"replayed"`
}

// DecideTopup menjalankan review/reject (admin.verify) atau verify yang
// menciptakan kredit (admin.deposit_credit, Superadmin). Hasil OCR tidak pernah
// menjadi otoritas; rekening tujuan divalidasi ulang terhadap D2.
func (s *Service) DecideTopup(ctx context.Context, actor auth.Actor, topupID string, in DecisionRequest) (DecisionResult, error) {
	if err := requireAdmin(actor, auth.CapAdminVerify); err != nil {
		return DecisionResult{}, err
	}
	if !validUUID(topupID) {
		return DecisionResult{}, ErrTopupNotFound
	}
	t, err := s.repo.Topup(ctx, topupID)
	if err != nil {
		return DecisionResult{}, err
	}
	reason := strings.TrimSpace(in.Reason)
	switch in.Action {
	case ActionReview, ActionReject:
		if reason == "" {
			return DecisionResult{}, ErrReasonRequired
		}
		if t.Status != TopupPendingReview && t.Status != TopupNeedsReview {
			return DecisionResult{}, ErrTopupDecided
		}
		if in.Action == ActionReview && t.Status == TopupNeedsReview {
			return DecisionResult{Topup: t}, nil
		}
		set, err := s.banks.EffectiveForBranch(ctx, t.BranchID)
		if err != nil {
			return DecisionResult{}, err
		}
		aksi := audit.ActionReview
		if in.Action == ActionReject {
			aksi = audit.ActionReject
		}
		out, err := s.repo.MarkTopup(ctx, MarkTopupInput{TopupID: t.ID, Action: in.Action,
			ActorID: actor.UserID, Reason: reason, AllowlistVersion: set.Version,
			Audit: audit.NewEntry(ctx, actor, audit.EntityDepositTopup, t.ID, aksi,
				map[string]any{"status": t.Status}, map[string]any{"action": in.Action, "reason": reason})})
		if err != nil {
			return DecisionResult{}, err
		}
		return DecisionResult{Topup: out}, nil
	case ActionVerify:
		return s.verifyTopup(ctx, actor, t, in, reason)
	default:
		return DecisionResult{}, ErrInvalidAction
	}
}

func (s *Service) verifyTopup(ctx context.Context, actor auth.Actor, t Topup, in DecisionRequest, reason string) (DecisionResult, error) {
	if !actor.Can(auth.CapAdminDepositCredit) {
		return DecisionResult{}, ErrForbidden
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return DecisionResult{}, ErrIdempotencyRequired
	}
	if in.ApprovedAmount <= 0 {
		return DecisionResult{}, ErrInvalidAmount
	}
	// Nominal final boleh berbeda dari yang dinyatakan, tetapi wajib beralasan
	// (D3 §3.3 butir 8).
	if in.ApprovedAmount != t.DeclaredAmount && reason == "" {
		return DecisionResult{}, ErrReasonRequired
	}
	bankID := strings.TrimSpace(in.BankAccountID)
	if bankID == "" {
		bankID = t.BankAccountID
	}
	set, err := s.effectiveAccount(ctx, t.BranchID, bankID)
	if err != nil {
		return DecisionResult{}, err
	}
	var acct BankAccount
	for _, a := range set.Accounts {
		if a.ID == bankID {
			acct = a
		}
	}
	dest, _ := json.Marshal(map[string]string{
		"payment_proof_id":             t.ProofID,
		"source":                       "manual",
		"status":                       "allowed",
		"bank_account_id":              acct.ID,
		"bank_snapshot":                acct.Bank,
		"account_owner_snapshot":       acct.NamaPemilik,
		"account_last_digits_snapshot": acct.LastDigits,
	})
	res, err := s.repo.ApproveTopup(ctx, TopupApprovalInput{
		TopupID: t.ID, BranchID: t.BranchID, BankAccountID: bankID, Amount: in.ApprovedAmount,
		BranchAllowlistVersion: set.Version, DestinationResult: dest,
		IdempotencyKey:     in.IdempotencyKey,
		RequestFingerprint: fingerprint("topup_verify", t.ID, bankID, in.ApprovedAmount),
		ActorID:            actor.UserID, Reason: reason,
	})
	if err != nil {
		return DecisionResult{}, err
	}
	out, err := s.repo.Topup(ctx, t.ID)
	if err != nil {
		return DecisionResult{}, err
	}
	sum := Summary{BranchID: res.Account.BranchID, AvailableBalance: res.Account.Balance,
		JournalVersion: res.Account.JournalVersion}
	return DecisionResult{Topup: out, Balance: &sum, Replayed: res.Replayed}, nil
}

// AllocationRequest adalah permintaan alokasi deposit ke order.
type AllocationRequest struct {
	Amount                 int64
	ExpectedJournalVersion int64
	ExpectedOrderVersion   int32
	IdempotencyKey         string
	Reason                 string
}

// AllocationResponse adalah hasil alokasi beserta saldo dan sisa tagihan.
type AllocationResponse struct {
	Allocation   AllocationView `json:"allocation"`
	Balance      Summary        `json:"balance"`
	RemainingDue int64          `json:"remaining_due"`
	Replayed     bool           `json:"replayed"`
}

// Allocate mendebit saldo ke order Submitted (D3 §3.4). Cabang hanya order
// miliknya; admin (admin.verify) atas permintaan cabang dengan alasan wajib.
func (s *Service) Allocate(ctx context.Context, actor auth.Actor, orderID string, in AllocationRequest) (AllocationResponse, error) {
	if !validUUID(orderID) {
		return AllocationResponse{}, ErrNotFound
	}
	var branchID string
	switch {
	case actor.IsBranch():
		b, err := branchOf(actor)
		if err != nil {
			return AllocationResponse{}, err
		}
		branchID = b
	case actor.IsAdmin():
		if err := requireAdmin(actor, auth.CapAdminVerify); err != nil {
			return AllocationResponse{}, err
		}
		if strings.TrimSpace(in.Reason) == "" {
			return AllocationResponse{}, ErrReasonRequired
		}
	default:
		return AllocationResponse{}, ErrForbidden
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return AllocationResponse{}, ErrIdempotencyRequired
	}
	if in.Amount <= 0 {
		return AllocationResponse{}, ErrInvalidAmount
	}
	ref, err := s.repo.OrderRef(ctx, orderID)
	if err != nil {
		return AllocationResponse{}, err
	}
	if actor.IsBranch() && ref.BranchID != branchID {
		return AllocationResponse{}, ErrNotFound
	}
	if ref.Status == "done" {
		return AllocationResponse{}, ErrOrderDone
	}
	res, err := s.repo.Allocate(ctx, AllocationInput{
		BranchID: ref.BranchID, OrderID: ref.ID, Amount: in.Amount,
		ExpectedJournalVersion: in.ExpectedJournalVersion, ExpectedOrderVersion: in.ExpectedOrderVersion,
		IdempotencyKey: in.IdempotencyKey,
		RequestFingerprint: fingerprint("allocate", ref.ID, in.Amount,
			in.ExpectedJournalVersion, in.ExpectedOrderVersion),
		ActorKind: string(actor.Kind), ActorID: actor.UserID,
	})
	if err != nil {
		return AllocationResponse{}, err
	}
	return s.allocationResponse(ctx, ref, res.Allocation, res.Account, res.Replayed)
}

func (s *Service) allocationResponse(ctx context.Context, ref OrderRef, a Allocation, acct Account, replayed bool) (AllocationResponse, error) {
	views, err := s.repo.OrderAllocations(ctx, ref.ID)
	if err != nil {
		return AllocationResponse{}, err
	}
	var active int64
	for _, v := range views {
		active += v.ActiveAmount
	}
	sum, err := s.repo.Summary(ctx, ref.BranchID)
	if err != nil {
		return AllocationResponse{}, err
	}
	sum.AvailableBalance, sum.JournalVersion = acct.Balance, acct.JournalVersion
	return AllocationResponse{
		Allocation: AllocationView{ID: a.ID, OrderID: a.OrderID, OrderVersion: a.OrderVersion,
			Amount: a.Amount, ReversedAmount: a.ReversedAmount, ActiveAmount: a.Amount - a.ReversedAmount,
			CreatedAt: a.CreatedAt},
		Balance: sum, RemainingDue: max(ref.Total-active, 0), Replayed: replayed,
	}, nil
}

// OrderAllocations mengembalikan alokasi satu order untuk cabang pemilik atau
// admin.
func (s *Service) OrderAllocations(ctx context.Context, actor auth.Actor, orderID string) ([]AllocationView, error) {
	if !validUUID(orderID) {
		return nil, ErrNotFound
	}
	ref, err := s.repo.OrderRef(ctx, orderID)
	if err != nil {
		return nil, err
	}
	switch {
	case actor.IsBranch():
		b, err := branchOf(actor)
		if err != nil {
			return nil, err
		}
		if ref.BranchID != b {
			return nil, ErrNotFound
		}
	case actor.IsAdmin():
		if !actor.CanAny(auth.CapAdminVerify, auth.CapAdminOrders) {
			return nil, ErrForbidden
		}
	default:
		return nil, ErrForbidden
	}
	return s.repo.OrderAllocations(ctx, ref.ID)
}

// ReversalRequest adalah reversal parsial/penuh alokasi oleh admin.
type ReversalRequest struct {
	Amount                 int64
	ExpectedJournalVersion int64
	Reason                 string
	IdempotencyKey         string
}

// ReversalResponse adalah hasil reversal.
type ReversalResponse struct {
	AllocationID   string  `json:"allocation_id"`
	ReversedAmount int64   `json:"reversed_amount"`
	ActiveAmount   int64   `json:"active_amount"`
	Balance        Summary `json:"balance"`
	Replayed       bool    `json:"replayed"`
}

// Reverse membalik sebagian/seluruh alokasi order yang belum Done (D3 §5.5).
func (s *Service) Reverse(ctx context.Context, actor auth.Actor, orderID, allocationID string, in ReversalRequest) (ReversalResponse, error) {
	if err := requireAdmin(actor, auth.CapAdminVerify); err != nil {
		return ReversalResponse{}, err
	}
	if !validUUID(orderID) {
		return ReversalResponse{}, ErrNotFound
	}
	if !validUUID(allocationID) {
		return ReversalResponse{}, ErrAllocationNotFound
	}
	if strings.TrimSpace(in.Reason) == "" {
		return ReversalResponse{}, ErrReasonRequired
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return ReversalResponse{}, ErrIdempotencyRequired
	}
	if in.Amount <= 0 {
		return ReversalResponse{}, ErrInvalidAmount
	}
	ref, err := s.repo.OrderRef(ctx, orderID)
	if err != nil {
		return ReversalResponse{}, err
	}
	if ref.Status == "done" {
		return ReversalResponse{}, ErrOrderDone
	}
	res, err := s.repo.ReverseAllocation(ctx, ReversalInput{
		BranchID: ref.BranchID, OrderID: ref.ID, AllocationID: allocationID, Amount: in.Amount,
		ExpectedJournalVersion: in.ExpectedJournalVersion, IdempotencyKey: in.IdempotencyKey,
		RequestFingerprint: fingerprint("reverse", allocationID, in.Amount, in.ExpectedJournalVersion,
			strings.TrimSpace(in.Reason)),
		ActorKind: string(actor.Kind), ActorID: actor.UserID, Reason: strings.TrimSpace(in.Reason),
	})
	if errors.Is(err, ErrNotFound) {
		return ReversalResponse{}, ErrAllocationNotFound
	}
	if err != nil {
		return ReversalResponse{}, err
	}
	sum, err := s.repo.Summary(ctx, ref.BranchID)
	if err != nil {
		return ReversalResponse{}, err
	}
	sum.AvailableBalance, sum.JournalVersion = res.Account.Balance, res.Account.JournalVersion
	return ReversalResponse{AllocationID: allocationID, ReversedAmount: res.ReversedAmount,
		ActiveAmount: res.ActiveAmount, Balance: sum, Replayed: res.Replayed}, nil
}

// String membantu log/audit tanpa membocorkan isi.
func (t Topup) String() string { return fmt.Sprintf("topup %s (%s)", t.ID, t.Status) }
