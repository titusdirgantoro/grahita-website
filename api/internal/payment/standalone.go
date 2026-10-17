package payment

import (
	"bytes"
	"context"
	"fmt"
)

// StoredProof adalah bukti mandiri yang tidak dimiliki pesanan (mis. bukti
// top-up deposit, D3 §4.3) dan sudah tervalidasi serta tersimpan di object
// storage. Metadata-nya dicatat domain pemiliknya dalam transaksinya sendiri.
type StoredProof struct {
	StorageKey string
	NamaFile   string
	MimeType   string
	UkuranByte int64
	Sha256Hex  string
}

// StoreStandaloneProof memvalidasi satu berkas dengan aturan yang sama dengan
// bukti pesanan (jenis dari magic bytes, batas ukuran setelan) lalu
// menyimpannya dengan kunci acak di bawah `proofs/{owner}/`. Bila transaksi
// metadata pemanggil gagal, objeknya wajib dibuang lewat DiscardStandaloneProof.
func (s *Service) StoreStandaloneProof(ctx context.Context, owner string, f FileInput) (StoredProof, error) {
	_, maxBytes, err := s.limits(ctx)
	if err != nil {
		return StoredProof{}, err
	}
	p, err := bacaBerkas(f, maxBytes)
	if err != nil {
		return StoredProof{}, err
	}
	key, err := kunciAcak(owner, p.kind)
	if err != nil {
		return StoredProof{}, err
	}
	if err := s.store.Put(ctx, key, bytes.NewReader(p.isi), int64(len(p.isi)), p.mime); err != nil {
		return StoredProof{}, fmt.Errorf("%w: %v", ErrStorageUnavailable, err)
	}
	return StoredProof{StorageKey: key, NamaFile: p.namaFile, MimeType: p.mime,
		UkuranByte: int64(len(p.isi)), Sha256Hex: p.sha256Hex}, nil
}

// DiscardStandaloneProof membuang objek bukti mandiri yang metadata-nya batal
// dicatat (best-effort, sama seperti hapusObjek).
func (s *Service) DiscardStandaloneProof(ctx context.Context, key string) {
	s.hapusObjek(ctx, []string{key})
}

// StandaloneProofURL menerbitkan tautan unduh berumur pendek untuk bukti
// mandiri. Otorisasi aksesnya tanggung jawab domain pemilik bukti.
func (s *Service) StandaloneProofURL(ctx context.Context, key, namaFile, mime string) (Download, error) {
	url, err := s.presign(ctx, Proof{StorageKey: key, NamaFile: namaFile})
	if err != nil {
		return Download{}, err
	}
	return Download{URL: url, ExpiresIn: int(s.ttl.Seconds()), NamaFile: namaFile, MimeType: mime}, nil
}
