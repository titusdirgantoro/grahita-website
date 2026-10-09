package audit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
)

// NilaiRedaksi adalah pengganti nilai yang dianggap sensitif.
const NilaiRedaksi = "[REDACTED]"

// kunciSensitif memuat potongan nama kunci yang isinya TIDAK PERNAH boleh
// tersimpan maupun tampil di audit log (kartu F2-28): password/kata sandi,
// token, secret, kredensial, cookie/CSRF, dan URL (termasuk URL bukti transfer
// yang ditandatangani). Pencocokan memakai potongan huruf kecil supaya
// `password_hash`, `storage_token`, atau `BuktiURL` ikut tertangkap.
var kunciSensitif = []string{
	"password", "passwd", "sandi", "rahasia", "secret",
	"token", "credential", "authorization", "cookie", "csrf",
	"otp", "signature", "presign", "signed", "storage_key",
	"private_key", "kunci_privat", "passphrase",
	"api_key", "apikey", "secret_key",
	"url", "uri", "tautan", "link", "href",
}

// kunciAman adalah pengecualian yang disengaja: nilainya bukan rahasia dan
// memang dimaksudkan untuk diaudit.
//
//   - `token_prefix` hanyalah 8 karakter pertama token registrasi; nilai
//     mentahnya tidak pernah masuk audit (F1-06) dan prefix itu sendiri sudah
//     ditampilkan di halaman Cabang/User management.
var kunciAman = map[string]bool{
	"token_prefix": true,
}

// Redact mengembalikan salinan snapshot dengan seluruh nilai sensitif
// diganti NilaiRedaksi.
//
// Snapshot disalin lewat JSON (dengan decoder UseNumber) supaya bentuk
// apa pun — struct domain, map, slice — diperlakukan sama dan angka besar
// (mis. total rupiah int64) tidak kehilangan presisi seperti kalau dibaca
// sebagai float64. Redaction bersifat idempoten: menjalankannya dua kali
// (sekali saat menyimpan, sekali saat menampilkan) tidak mengubah hasil.
//
// Bila TIDAK ada kunci sensitif di dalamnya, nilai asli dikembalikan apa
// adanya (tipe struct tetap utuh) sehingga snapshot yang sudah bersih tidak
// berubah bentuk hanya karena melewati fungsi ini.
//
// Bila snapshot tidak bisa disalin, fungsi mengembalikan nil: lebih baik
// kehilangan isi snapshot daripada menyimpan data yang belum tersaring.
func Redact(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		slog.Warn("snapshot audit tidak bisa disalin untuk redaksi; snapshot dibuang", "err", err)
		return nil
	}
	salinan, err := parseJSON(raw)
	if err != nil {
		slog.Warn("snapshot audit tidak bisa diurai ulang untuk redaksi; snapshot dibuang", "err", err)
		return nil
	}
	if !adaKunciSensitif(salinan) {
		return v
	}
	return redactValue(salinan)
}

// Snapshot mengurai isi kolom jsonb audit menjadi bentuk generik yang siap
// dikirim/diredaksi. Angka dipertahankan sebagai json.Number. Isi kosong
// (NULL jsonb) menghasilkan nil.
func Snapshot(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	return parseJSON(raw)
}

// parseJSON mengurai JSON dengan UseNumber supaya presisi angka utuh.
func parseJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// adaKunciSensitif melaporkan apakah struktur memuat minimal satu kunci yang
// harus diredaksi.
func adaKunciSensitif(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if kunciSensitifTerlarang(k) || adaKunciSensitif(val) {
				return true
			}
		}
	case []any:
		for _, val := range t {
			if adaKunciSensitif(val) {
				return true
			}
		}
	}
	return false
}

// redactValue menelusuri struktur generik hasil JSON dan mengganti nilai pada
// kunci sensitif.
func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if kunciSensitifTerlarang(k) {
				out[k] = NilaiRedaksi
				continue
			}
			out[k] = redactValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = redactValue(val)
		}
		return out
	default:
		return v
	}
}

// kunciSensitifTerlarang melaporkan apakah sebuah nama kunci harus diredaksi.
func kunciSensitifTerlarang(k string) bool {
	nama := strings.ToLower(strings.TrimSpace(k))
	if kunciAman[nama] {
		return false
	}
	for _, s := range kunciSensitif {
		if strings.Contains(nama, s) {
			return true
		}
	}
	return false
}
