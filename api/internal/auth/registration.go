package auth

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// Token registrasi cabang (PRD §3.1, SystemDesign.md §4.1).
//
// Nilai asli token hanya ditampilkan SEKALI kepada admin saat di-generate;
// database hanya menyimpan SHA-256-nya (registration_tokens.token_hash)
// beserta 8 karakter pertamanya (token_prefix) untuk ditampilkan di Admin
// Portal (Database.md §4.2).
//
// Format tampilan: GRH-XXXX-XXXX-XXXX-XXXX dengan alfabet 32 karakter yang
// membuang I, O, 0, dan 1 supaya tidak salah baca saat admin menyalin token
// ke cabang. 16 karakter acak = 80 bit entropi.

// RegistrationTokenPrefix adalah awalan tampilan token registrasi.
const RegistrationTokenPrefix = "GRH"

// registrationAlphabet adalah alfabet token: alfanumerik tanpa I/O/0/1.
const registrationAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const (
	registrationGroupLen = 4
	registrationGroups   = 4
)

// NewRegistrationToken membuat token registrasi acak beserta awalan 8
// karakter pertamanya (untuk ditampilkan admin). Nilai yang dikembalikan
// adalah bentuk tampilan; yang disimpan ke database adalah
// HashRegistrationToken atas nilai tersebut.
func NewRegistrationToken() (token, prefix string, err error) {
	buf := make([]byte, registrationGroupLen*registrationGroups)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("membuat token registrasi: %w", err)
	}
	// 32 alfabet membagi habis 256, jadi modulo ini seragam (tanpa bias).
	groups := make([]string, 0, registrationGroups)
	for g := 0; g < registrationGroups; g++ {
		var b strings.Builder
		for i := 0; i < registrationGroupLen; i++ {
			b.WriteByte(registrationAlphabet[int(buf[g*registrationGroupLen+i])%len(registrationAlphabet)])
		}
		groups = append(groups, b.String())
	}
	token = RegistrationTokenPrefix + "-" + strings.Join(groups, "-")
	return token, token[:8], nil
}

// NormalizeRegistrationToken menyeragamkan token yang diketik atau ditempel
// pengguna: huruf besar, tanpa spasi, dan hanya menyisakan huruf/angka —
// sehingga token tetap sah walau tanda hubungnya terlewat hilang.
//
// Normalisasi ini TIDAK "memperbaiki" karakter yang salah: karakter di luar
// alfabet tetap ikut sehingga hash-nya tidak akan cocok dengan token mana pun.
func NormalizeRegistrationToken(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(raw)) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// HashRegistrationToken menghitung hash yang disimpan di
// registration_tokens.token_hash. Dipakai baik saat generate maupun saat
// validasi supaya keduanya tidak pernah berbeda.
func HashRegistrationToken(raw string) []byte {
	return HashToken(NormalizeRegistrationToken(raw))
}
