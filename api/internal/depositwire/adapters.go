// Package depositwire menghubungkan service deposit ke penyimpanan bukti
// (payment) dan allowlist rekening (bank). Paket deposit hanya mengenal
// interface miliknya supaya domain pembayaran (verifikasi campuran, C3) kelak
// boleh memakai deposit tanpa siklus impor.
package depositwire

import (
	"context"
	"io"

	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/deposit"
	"github.com/grahita/portal/api/internal/payment"
)

type proofStore struct{ p *payment.Service }

// Proofs membungkus payment.Service sebagai deposit.ProofStore.
func Proofs(p *payment.Service) deposit.ProofStore { return proofStore{p} }

func (d proofStore) Store(ctx context.Context, owner, nama string, content io.Reader, size int64) (deposit.StoredProof, error) {
	sp, err := d.p.StoreStandaloneProof(ctx, owner, payment.FileInput{NamaFile: nama, Content: content, UkuranHint: size})
	if err != nil {
		return deposit.StoredProof{}, err
	}
	return deposit.StoredProof{StorageKey: sp.StorageKey, NamaFile: sp.NamaFile, MimeType: sp.MimeType,
		UkuranByte: sp.UkuranByte, Sha256Hex: sp.Sha256Hex}, nil
}

func (d proofStore) Discard(ctx context.Context, key string) {
	d.p.DiscardStandaloneProof(ctx, key)
}

func (d proofStore) URL(ctx context.Context, key, nama, mime string) (deposit.ProofURL, error) {
	dl, err := d.p.StandaloneProofURL(ctx, key, nama, mime)
	if err != nil {
		return deposit.ProofURL{}, err
	}
	return deposit.ProofURL{URL: dl.URL, ExpiresIn: dl.ExpiresIn, NamaFile: dl.NamaFile, MimeType: dl.MimeType}, nil
}

type banks struct{ b *bank.Service }

// Banks membungkus bank.Service sebagai deposit.BankSource.
func Banks(b *bank.Service) deposit.BankSource { return banks{b} }

func (d banks) EffectiveForBranch(ctx context.Context, branchID string) (deposit.EffectiveAccounts, error) {
	set, err := d.b.EffectiveForBranch(ctx, branchID)
	if err != nil {
		return deposit.EffectiveAccounts{}, err
	}
	out := deposit.EffectiveAccounts{Version: set.Version}
	for _, a := range set.Accounts {
		n := bank.NormalizeNomor(a.Nomor)
		if len(n) > 4 {
			n = n[len(n)-4:]
		}
		out.Accounts = append(out.Accounts, deposit.BankAccount{ID: a.ID, Bank: a.Bank,
			NamaPemilik: a.NamaPemilik, LastDigits: n})
	}
	return out, nil
}
