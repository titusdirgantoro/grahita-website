// Package tarif menghitung tarif satu item dari komponen HJM, P1, dan SPV
// (docs/design/master-impact-import.md §7) dan sidik komponennya.
//
// Satu-satunya sumber rumus untuk impor master Impact maupun edit manual
// matriks harga: keduanya HARUS menghasilkan tarif dan sidik yang identik untuk
// komponen yang sama, sehingga impor berikutnya mengenali nilai hasil edit
// manual yang tidak berubah sebagai `skip`, bukan versi tarif baru.
package tarif

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"strconv"
	"strings"
)

// ErrRasioTidakValid dikembalikan bila P1 bukan angka rasio 0–1.
var ErrRasioTidakValid = errors.New("P1 harus berupa rasio 0 sampai 1")

// Sidik adalah SHA-256 dari bagian-bagian berawalan panjang, sehingga batas
// antarbagian tidak ambigu ("ab","c" ≠ "a","bc").
func Sidik(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = io.WriteString(h, strconv.Itoa(len(p)))
		_, _ = io.WriteString(h, ":")
		_, _ = io.WriteString(h, p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SidikTersedia adalah sidik komponen tarif yang dijual. p1 memakai bentuk
// kanonik big.Rat.RatString ("1/10"), bukan teks masukan.
func SidikTersedia(hjm int64, p1 *big.Rat, spv, final int64) string {
	return Sidik("available", strconv.FormatInt(hjm, 10), p1.RatString(), strconv.FormatInt(spv, 10), strconv.FormatInt(final, 10))
}

// ParseRasio membaca P1 sebagai rasio eksak 0–1 (mis. "0.1", "0.05", "1/10").
// Teks kosong berarti 0.
func ParseRasio(teks string) (*big.Rat, error) {
	teks = strings.TrimSpace(teks)
	r := new(big.Rat)
	if teks == "" {
		return r, nil
	}
	if _, ok := r.SetString(teks); !ok {
		return nil, ErrRasioTidakValid
	}
	if r.Sign() < 0 || r.Cmp(big.NewRat(1, 1)) > 0 {
		return nil, ErrRasioTidakValid
	}
	return r, nil
}

// Desimal menulis rasio sebagai teks desimal untuk kolom numeric (maksimal 18
// digit pecahan, tanpa nol di belakang) — bentuk yang sama untuk versi impor
// dan versi manual.
func Desimal(r *big.Rat) string {
	return strings.TrimRight(strings.TrimRight(r.FloatString(18), "0"), ".")
}

// Final menghitung tarif satu item: round_half_up(HJM + HJM×P1 + SPV) ke Rupiah.
// Pembulatan hanya sekali setelah ketiga komponen dijumlahkan. ok=false bila
// komponen negatif atau hasilnya melampaui int64.
func Final(hjm int64, p1 *big.Rat, spv int64) (int64, bool) {
	if hjm < 0 || spv < 0 || p1 == nil || p1.Sign() < 0 {
		return 0, false
	}
	raw := new(big.Rat).SetInt64(hjm)
	raw.Add(raw, new(big.Rat).Mul(new(big.Rat).SetInt64(hjm), p1))
	raw.Add(raw, new(big.Rat).SetInt64(spv))
	return roundHalfUp(raw)
}

// roundHalfUp membulatkan bilangan nonnegatif ke bilangan bulat terdekat,
// pecahan tepat 0,5 menjauhi nol (bukan bankers rounding).
func roundHalfUp(v *big.Rat) (int64, bool) {
	if v.Sign() < 0 {
		return 0, false
	}
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(v.Num(), v.Denom(), rem)
	if new(big.Int).Lsh(rem, 1).Cmp(v.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64(), q.IsInt64()
}
