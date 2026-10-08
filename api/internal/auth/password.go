package auth

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alexedwards/argon2id"
)

// PasswordPolicy adalah aturan kata sandi minimum (PRD CP-REG-05 dan
// Architecture.md §6: Argon2id m=64MB, t=3, p=2).
var PasswordPolicy = struct {
	MinLength   int
	ArgonParams argon2id.Params
}{
	MinLength: 8,
	ArgonParams: argon2id.Params{
		Memory:      64 * 1024, // 64 MiB
		Iterations:  3,
		Parallelism: 2,
		SaltLength:  16,
		KeyLength:   32,
	},
}

// ErrPasswordTooWeak dikembalikan bila kata sandi tidak memenuhi kebijakan.
var ErrPasswordTooWeak = errors.New("kata sandi minimal 8 karakter")

// HashPassword menghasilkan hash Argon2id yang aman disimpan di kolom
// password_hash.
func HashPassword(plain string) (string, error) {
	if err := CheckPasswordStrength(plain); err != nil {
		return "", err
	}
	hash, err := argon2id.CreateHash(plain, &PasswordPolicy.ArgonParams)
	if err != nil {
		return "", fmt.Errorf("hash kata sandi: %w", err)
	}
	return hash, nil
}

// VerifyPassword membandingkan kata sandi dengan hash tersimpan. Waktu
// komparasi konstan; hash rusak dilaporkan sebagai tidak cocok.
func VerifyPassword(hash, plain string) bool {
	if hash == "" || plain == "" {
		return false
	}
	match, err := argon2id.ComparePasswordAndHash(plain, hash)
	if err != nil {
		return false
	}
	return match
}

// CheckPasswordStrength memvalidasi panjang dan variasi karakter dasar.
func CheckPasswordStrength(plain string) error {
	if utf8.RuneCountInString(plain) < PasswordPolicy.MinLength {
		return ErrPasswordTooWeak
	}
	var hasLetter, hasDigit bool
	for _, r := range plain {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errors.New("kata sandi harus memuat huruf dan angka")
	}
	if strings.TrimSpace(plain) != plain {
		return errors.New("kata sandi tidak boleh diawali atau diakhiri spasi")
	}
	return nil
}

// NormalizeEmail menyeragamkan email sebelum dicari: citext di database
// menangani case, ini hanya merapikan spasi di sisi aplikasi.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
