package va

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Tanda tangan callback VA memakai HMAC-SHA256 atas sebuah STRING KANONIK yang
// memuat metode, path, sidik isi badan, dan stempel waktu. Bentuk kanonik ini
// adalah bagian kontrak yang dipilih karena cocok dengan praktik umum
// host-to-host Indonesia dan mudah diverifikasi ulang oleh pihak bank:
//
//	kanonik = METHOD + ":" + PATH + ":" + sha256hex(BODY) + ":" + TIMESTAMP
//	tanda   = hex(HMAC-SHA256(secret, kanonik))
//
// Perbedaan tiap provider hanya pada NAMA header, sumber `secret` (client
// secret host-to-host, atau access token OAuth per-transaksi untuk BCA), dan
// cara menyusun `PATH`; perbedaan itu dipetakan di adapter F3-30, sementara
// perhitungan tanda tangannya tetap satu fungsi di sini supaya tidak ada dua
// implementasi kriptografi di repo.
//
// Catatan penting: yang ikut ditandatangani adalah SIDIK (SHA-256) isi badan,
// bukan badan itu sendiri. Ini menghindari perbedaan normalisasi JSON antara
// kita dan provider (spasi, urutan kunci, escaping) mematahkan verifikasi.

// Kanonik menyusun string kanonik untuk penandatanganan/verifikasi.
func Kanonik(method, path string, timestamp string, body []byte) string {
	ringkas := sha256.Sum256(body)
	return strings.ToUpper(method) + ":" + path + ":" + hex.EncodeToString(ringkas[:]) + ":" + timestamp
}

// TandaTangan menghitung tanda tangan hex (huruf kecil) atas string kanonik.
func TandaTangan(rahasia []byte, kanonik string) string {
	mac := hmac.New(sha256.New, rahasia)
	mac.Write([]byte(kanonik))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifikasiTandaTangan membandingkan tanda tangan yang dikirim provider dengan
// tanda tangan yang dihitung ulang, MEMAKAI PERBANDINGAN WAKTU-KONSTAN
// (`crypto/subtle`). Panjang berbeda atau nilai berbeda sama-sama menghasilkan
// `ErrTandaTanganTidakSah`; tidak ada informasi yang bocor lewat pesan galat.
func VerifikasiTandaTangan(rahasia []byte, kanonik, terkirim string) error {
	diharapkan := TandaTangan(rahasia, kanonik)
	kirim := strings.ToLower(strings.TrimSpace(terkirim))
	if subtle.ConstantTimeCompare([]byte(diharapkan), []byte(kirim)) != 1 {
		return ErrTandaTanganTidakSah
	}
	return nil
}

// VerifikasiKanonik adalah jalan pintas: menyusun kanonik dari
// `PermintaanCallback` lalu memverifikasi tanda tangan pada `header`.
func VerifikasiKanonik(rahasia []byte, in PermintaanCallback, header string) error {
	if header == "" {
		return errors.New("nama header tanda tangan tidak ditentukan")
	}
	timestamp := in.Header.Get(header + "-Timestamp")
	if timestamp == "" {
		// Sebagian provider menaruh stempel waktu di dalam badan atau memakai
		// nama header lain (mis. `X-BCA-Timestamp`); adapter F3-30 memetakan
		// nama itu ke satu bentuk. Prototipe memakai bentuk `<header>-Timestamp`.
		return ErrTandaTanganTidakSah
	}
	kanonik := Kanonik(in.Method, in.Path, timestamp, in.Body)
	return VerifikasiTandaTangan(rahasia, kanonik, in.Header.Get(header))
}

// JendelaReplayBawaan adalah lebar jendela waktu yang diterima untuk callback.
// Callback yang lebih tua ditolak (indikasi replay) karena provider wajib
// mencoba ulang dengan tanda tangan baru.
const JendelaReplayBawaan = 15 * time.Minute

// PeriksaUmur memastikan stempel waktu callback berada di dalam jendela
// `[DiterimaPada-jendela, DiterimaPada+toleransiMasaDepan]`. `toleransiMasaDepan`
// menyerap selisih jam antara server kita dan provider (bawaan 5 menit).
// Stempel waktu yang rusak dianggap tidak sah.
func PeriksaUmur(timestamp string, diterimaPada time.Time, jendela, toleransiMasaDepan time.Duration) error {
	if jendela <= 0 {
		jendela = JendelaReplayBawaan
	}
	if toleransiMasaDepan < 0 {
		toleransiMasaDepan = 0
	}
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return ErrCallbackKedaluwarsa
	}
	if diterimaPada.IsZero() {
		return ErrCallbackKedaluwarsa
	}
	umur := diterimaPada.Sub(t)
	if umur > jendela || umur < -toleransiMasaDepan {
		return ErrCallbackKedaluwarsa
	}
	return nil
}
