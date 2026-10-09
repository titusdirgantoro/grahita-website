package va

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
)

// Berkas ini memuat JALUR TANDA TANGAN ASIMETRIS, karena standar VA/open API
// perbankan Indonesia (SNAP BI — dipakai BCA & BRI untuk API mitra) menandatangani
// permintaan dengan `SHA256withRSA`, bukan HMAC simetris:
//
//	stringToSign = HTTPMethod + ":" + EndpointPath + ":" + AccessToken + ":" +
//	               LowerCase(HexEncode(SHA-256(Minify(RequestBody)))) + ":" + X-TIMESTAMP
//	X-SIGNATURE  = Base64(SHA256withRSA(privateKey, stringToSign))
//
// Perhatikan perbedaan penting dengan jalur simetris di `signature.go`:
// stringToSign SNAP memuat ACCESS TOKEN per-transaksi, dan endpoint token
// (`/snap/v1.0/access-token/b2b`) memakai stringToSign lain:
//
//	stringToSign_token = ClientKey + "|" + X-TIMESTAMP
//
// Kedua bentuk itu publik (dokumentasi mitra bank/penyedia SNAP) dan belum
// diverifikasi terhadap kontrak resmi Grahita — lihat keputusan tertunda
// `K-01`/`K-02` di `docs/design/f3-29/KONTRAK-VA.md`. Sampai dokumen kontrak
// bank diterima, jalur ini diuji lewat kunci RSA yang dibangkitkan di uji
// (tanpa jaringan) supaya F3-30 tidak memulai dari nol.

// KanonikSNAP menyusun stringToSign SNAP untuk endpoint TRANSAKSI.
func KanonikSNAP(method, path, accessToken, timestamp string, body []byte) string {
	ringkas := sha256.Sum256(body)
	return strings.ToUpper(method) + ":" + path + ":" + accessToken + ":" +
		strings.ToLower(hex.EncodeToString(ringkas[:])) + ":" + timestamp
}

// KanonikSNAPToken menyusun stringToSign SNAP untuk endpoint token B2B.
func KanonikSNAPToken(clientKey, timestamp string) string {
	return clientKey + "|" + timestamp
}

// TandaTanganRSASHA256 menghasilkan tanda tangan Base64 (SHA256withRSA) atas
// stringToSign. Dipakai harness uji; di produksi kunci privat hanya ada di sisi
// kita saat memanggil API bank.
func TandaTanganRSASHA256(privateKey *rsa.PrivateKey, stringToSign string) (string, error) {
	if privateKey == nil {
		return "", ErrTandaTanganTidakSah
	}
	ringkas := sha256.Sum256([]byte(stringToSign))
	sig, err := rsa.SignPKCS1v15(nil, privateKey, crypto.SHA256, ringkas[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// VerifikasiRSASHA256 memverifikasi tanda tangan Base64 milik provider terhadap
// stringToSign, memakai kunci publik yang ditukar saat provisioning. Galat
// apa pun (kunci tidak sah, base64 rusak, tanda tangan tidak cocok) dipetakan
// ke `ErrTandaTanganTidakSah` supaya tidak ada informasi yang bocor lewat
// pesan galat.
func VerifikasiRSASHA256(publicKey *rsa.PublicKey, stringToSign, tandaTanganB64 string) error {
	if publicKey == nil || tandaTanganB64 == "" {
		return ErrTandaTanganTidakSah
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(tandaTanganB64))
	if err != nil {
		return ErrTandaTanganTidakSah
	}
	ringkas := sha256.Sum256([]byte(stringToSign))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, ringkas[:], sig); err != nil {
		return ErrTandaTanganTidakSah
	}
	return nil
}

// KunciPublikDariPEM membaca kunci publik RSA (SPKI/PKCS#1) dari PEM. Dipakai
// saat provisioning kunci mitra dari bank/penyedia.
func KunciPublikDariPEM(pemBytes []byte) (*rsa.PublicKey, error) {
	blok, _ := pem.Decode(pemBytes)
	if blok == nil {
		return nil, errors.New("PEM kunci publik tidak dapat dibaca")
	}
	if kunci, err := x509.ParsePKIXPublicKey(blok.Bytes); err == nil {
		if rsaKunci, ok := kunci.(*rsa.PublicKey); ok {
			return rsaKunci, nil
		}
		return nil, errors.New("kunci publik bukan RSA")
	}
	return x509.ParsePKCS1PublicKey(blok.Bytes)
}

// KunciPrivatDariPEM membaca kunci privat RSA (PKCS#8/PKCS#1) dari PEM. HANYA
// untuk harness uji dan pemanggilan keluar; kunci TIDAK pernah masuk repo.
func KunciPrivatDariPEM(pemBytes []byte) (*rsa.PrivateKey, error) {
	blok, _ := pem.Decode(pemBytes)
	if blok == nil {
		return nil, errors.New("PEM kunci privat tidak dapat dibaca")
	}
	if kunci, err := x509.ParsePKCS8PrivateKey(blok.Bytes); err == nil {
		if rsaKunci, ok := kunci.(*rsa.PrivateKey); ok {
			return rsaKunci, nil
		}
		return nil, errors.New("kunci privat bukan RSA")
	}
	return x509.ParsePKCS1PrivateKey(blok.Bytes)
}
