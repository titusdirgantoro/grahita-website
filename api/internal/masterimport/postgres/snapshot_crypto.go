package postgres

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

var errSnapshotKeyInvalid = errors.New("MASTER_IMPORT_SNAPSHOT_KEY wajib berupa base64 32 byte")

type snapshotCipher struct {
	aead  cipher.AEAD
	keyID string
	err   error
}

func newSnapshotCipher(encodedKey string) *snapshotCipher {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return &snapshotCipher{err: errSnapshotKeyInvalid}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return &snapshotCipher{err: fmt.Errorf("buat cipher snapshot: %w", err)}
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return &snapshotCipher{err: fmt.Errorf("buat GCM snapshot: %w", err)}
	}
	digest := sha256.Sum256(key)
	return &snapshotCipher{aead: aead, keyID: hex.EncodeToString(digest[:8])}
}

func (c *snapshotCipher) encrypt(plaintext []byte, aad string) ([]byte, string, error) {
	if c == nil || c.err != nil {
		return nil, "", errSnapshotKeyInvalid
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, "", fmt.Errorf("buat nonce snapshot: %w", err)
	}
	sealed := c.aead.Seal(nil, nonce, plaintext, []byte(aad))
	return append(nonce, sealed...), c.keyID, nil
}

func (c *snapshotCipher) decrypt(ciphertext []byte, keyID, aad string) ([]byte, error) {
	if c == nil || c.err != nil || keyID != c.keyID {
		return nil, errSnapshotKeyInvalid
	}
	if len(ciphertext) < c.aead.NonceSize() {
		return nil, errors.New("ciphertext snapshot terlalu pendek")
	}
	nonce, sealed := ciphertext[:c.aead.NonceSize()], ciphertext[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, sealed, []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("dekripsi snapshot gagal: %w", err)
	}
	return plaintext, nil
}
