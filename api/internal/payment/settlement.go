package payment

// Settlement campuran deposit + transfer (C3; D3 §3.5–3.7, §4.7–4.8, §5.4,
// §6.2; delta C0 §4.3.1, §4.4, §5.2).
//
// Approve order yang memiliki alokasi deposit aktif, berada di sub-state revisi,
// atau mengirim rincian split WAJIB melalui jalur ini. Klien menyebut alokasi
// aktif dan keputusan per bukti; server menghitung ulang seluruh aggregate di
// bawah kunci dan menolak bila `deposit + transfer_untuk_order != total`.

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrBreakdownMismatch: rincian deposit/transfer tidak tepat total pesanan
	// atau tidak sesuai sumber yang terkunci (422 PAYMENT_BREAKDOWN_MISMATCH).
	ErrBreakdownMismatch = errors.New("rincian pembayaran tidak cocok dengan total pesanan")
	// ErrOverpayDecisionRequired: transfer yang diterapkan melebihi kebutuhan
	// sehingga selisih wajib diputuskan sebagai kredit deposit oleh Superadmin.
	ErrOverpayDecisionRequired = errors.New("selisih lebih bayar belum diputuskan")
	// ErrOrderVersionConflict: expected_order_version sudah basi.
	ErrOrderVersionConflict = errors.New("versi pesanan berubah")
	// ErrDepositCreditForbidden: keputusan yang menciptakan kredit deposit
	// hanya untuk pemegang admin.deposit_credit (Superadmin).
	ErrDepositCreditForbidden = errors.New("kredit deposit hanya boleh diputuskan Superadmin")
	// ErrSettlementKeyRequired: kredit deposit membutuhkan Idempotency-Key.
	ErrSettlementKeyRequired = errors.New("header Idempotency-Key wajib untuk kredit deposit")
	// ErrIdempotencyReused: key sama dipakai untuk keputusan berbeda.
	ErrIdempotencyReused = errors.New("idempotency key dipakai untuk keputusan lain")
	// ErrProofSplitInvalid: bentuk split bukti tidak sah.
	ErrProofSplitInvalid = fmt.Errorf("%w: rincian split bukti tidak valid", ErrInvalidData)
)

// ProofSplit adalah keputusan ekonomi satu bukti transfer order.
//
// CreditedToDepositTotal adalah nilai KUMULATIF sepanjang umur bukti (C0
// §4.3.1); server menghitung delta journal baru dari kredit yang sudah ada.
type ProofSplit struct {
	PaymentProofID         string `json:"payment_proof_id"`
	BankAccountID          string `json:"bank_account_id"`
	ApprovedNominal        int64  `json:"approved_nominal"`
	AppliedToOrder         int64  `json:"applied_to_order"`
	CreditedToDepositTotal int64  `json:"credited_to_deposit_total"`
}

// SettlementInput adalah rincian approve campuran dari admin.
type SettlementInput struct {
	ExpectedOrderVersion int32
	DepositAllocationIDs []string
	ProofSplits          []ProofSplit
	IdempotencyKey       string
}

// SettlementSource adalah snapshot satu alokasi deposit pada attempt Done.
type SettlementSource struct {
	AllocationID           string `json:"allocation_id"`
	AllocationOrderVersion int32  `json:"allocation_order_version"`
	Amount                 int64  `json:"amount"`
}

// SettlementSplit adalah split bukti yang tersimpan pada attempt Done.
type SettlementSplit struct {
	PaymentProofID         string `json:"payment_proof_id"`
	ApprovedNominal        int64  `json:"approved_nominal"`
	AppliedToOrder         int64  `json:"applied_to_order"`
	CreditedToDepositTotal int64  `json:"credited_to_deposit_total"`
	NewCreditToDeposit     int64  `json:"new_credit_to_deposit"`
}

// SettlementResult adalah breakdown settlement yang menjadi pointer Done.
type SettlementResult struct {
	DepositAmount      int64              `json:"deposit_amount"`
	TransferAmount     int64              `json:"transfer_amount"`
	NewCreditToDeposit int64              `json:"new_credit_to_deposit"`
	Sources            []SettlementSource `json:"deposit_sources"`
	Splits             []SettlementSplit  `json:"proof_splits"`
	Replayed           bool               `json:"replayed"`
}

// siapkanSettlement memvalidasi bentuk rincian dan menurunkan pemetaan
// rekening tujuan dari split (satu sumber kebenaran per bukti).
func siapkanSettlement(in *SettlementInput, hasil, metode string, destinations []ProofDestination) (*SettlementInput, []ProofDestination, error) {
	if in == nil {
		return nil, destinations, nil
	}
	if hasil != VerifCocok || metode != MetodeManual {
		return nil, nil, fmt.Errorf("%w: rincian settlement hanya untuk persetujuan manual", ErrInvalidData)
	}
	if len(destinations) > 0 {
		return nil, nil, fmt.Errorf("%w: rekening tujuan dikirim lewat proof_splits", ErrProofSplitInvalid)
	}
	if in.ExpectedOrderVersion < 0 {
		return nil, nil, ErrProofSplitInvalid
	}
	out := &SettlementInput{ExpectedOrderVersion: in.ExpectedOrderVersion,
		IdempotencyKey: strings.TrimSpace(in.IdempotencyKey)}
	seen := map[string]bool{}
	for _, id := range in.DepositAllocationIDs {
		id = strings.ToLower(strings.TrimSpace(id))
		if !uuidRe.MatchString(id) || seen[id] {
			return nil, nil, fmt.Errorf("%w: deposit_allocation_ids berisi UUID tidak sah/duplikat", ErrProofSplitInvalid)
		}
		seen[id] = true
		out.DepositAllocationIDs = append(out.DepositAllocationIDs, id)
	}
	dest := make([]ProofDestination, 0, len(in.ProofSplits))
	for _, s := range in.ProofSplits {
		s.PaymentProofID = strings.ToLower(strings.TrimSpace(s.PaymentProofID))
		s.BankAccountID = strings.ToLower(strings.TrimSpace(s.BankAccountID))
		if !uuidRe.MatchString(s.PaymentProofID) || !uuidRe.MatchString(s.BankAccountID) || seen[s.PaymentProofID] {
			return nil, nil, ErrDestinationRequired
		}
		if s.ApprovedNominal < 0 || s.AppliedToOrder < 0 || s.CreditedToDepositTotal < 0 ||
			s.ApprovedNominal != s.AppliedToOrder+s.CreditedToDepositTotal {
			return nil, nil, fmt.Errorf("%w: approved_nominal harus = applied_to_order + credited_to_deposit_total", ErrProofSplitInvalid)
		}
		seen[s.PaymentProofID] = true
		out.ProofSplits = append(out.ProofSplits, s)
		dest = append(dest, ProofDestination{PaymentProofID: s.PaymentProofID, BankAccountID: s.BankAccountID})
	}
	return out, dest, nil
}
