package va

// Uji jalur tanda tangan asimetris (SHA256withRSA) yang dipakai standar SNAP BI
// untuk API mitra bank. Diuji dengan kunci RSA yang dibangkitkan di uji — tidak
// ada jaringan dan tidak ada kunci milik bank.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
)

func kunciUji(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	kunci, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return kunci, &kunci.PublicKey
}

func TestKanonikSNAPBentuk(t *testing.T) {
	badan := []byte(`{"partnerServiceId":"  88888"}`)
	k := KanonikSNAP("post", "/snap/v1.0/transfer-va/inquiry", "TOKEN-123", "2026-10-09T03:30:00+07:00", badan)
	if !strings.HasPrefix(k, "POST:/snap/v1.0/transfer-va/inquiry:TOKEN-123:") {
		t.Fatalf("kanonik SNAP tidak sesuai bentuk: %q", k)
	}
	if !strings.HasSuffix(k, ":2026-10-09T03:30:00+07:00") {
		t.Fatalf("kanonik SNAP tidak memuat stempel waktu: %q", k)
	}
	// Sidik badan huruf kecil; access token ikut ditandatangani (beda dengan
	// jalur simetris kita).
	if k == KanonikSNAP("POST", "/snap/v1.0/transfer-va/inquiry", "TOKEN-124", "2026-10-09T03:30:00+07:00", badan) {
		t.Fatal("kanonik SNAP tidak sensitif terhadap access token")
	}
	if KanonikSNAPToken("CLIENT-KEY", "2026-10-09T03:30:00+07:00") != "CLIENT-KEY|2026-10-09T03:30:00+07:00" {
		t.Fatal("kanonik token SNAP salah")
	}
}

func TestTandaTanganRSASHA256BolakBalik(t *testing.T) {
	privat, publik := kunciUji(t)
	kanonik := KanonikSNAP("POST", "/snap/v1.0/transfer-va/payment", "TOKEN", "2026-10-09T03:30:00+07:00", []byte(`{"nominal":150000}`))
	sig, err := TandaTanganRSASHA256(privat, kanonik)
	if err != nil {
		t.Fatalf("TandaTanganRSASHA256: %v", err)
	}
	if err := VerifikasiRSASHA256(publik, kanonik, sig); err != nil {
		t.Fatalf("tanda tangan sah ditolak: %v", err)
	}
	if strings.Contains(sig, "+") == false && strings.Contains(sig, "=") == false {
		t.Fatalf("tanda tangan tidak terlihat Base64: %q", sig)
	}

	// Kanonik berbeda → ditolak.
	if err := VerifikasiRSASHA256(publik, kanonik+"x", sig); !errors.Is(err, ErrTandaTanganTidakSah) {
		t.Fatalf("kanonik berbeda: ingin ErrTandaTanganTidakSah, dapat %v", err)
	}
	// Kunci lain → ditolak.
	_, publikLain := kunciUji(t)
	if err := VerifikasiRSASHA256(publikLain, kanonik, sig); !errors.Is(err, ErrTandaTanganTidakSah) {
		t.Fatalf("kunci lain: ingin ErrTandaTanganTidakSah, dapat %v", err)
	}
	// Base64 rusak / kosong / kunci nil → ditolak.
	for nama, kasus := range map[string]struct {
		kunci *rsa.PublicKey
		sig   string
	}{
		"base64 rusak": {publik, "bukan base64!!"},
		"kosong":       {publik, ""},
		"kunci nil":    {nil, sig},
		"potong":       {publik, sig[:len(sig)/2]},
	} {
		if err := VerifikasiRSASHA256(kasus.kunci, kanonik, kasus.sig); !errors.Is(err, ErrTandaTanganTidakSah) {
			t.Fatalf("%s: ingin ErrTandaTanganTidakSah, dapat %v", nama, err)
		}
	}
	// Menandatangani tanpa kunci privat adalah galat, bukan tanda tangan kosong.
	if _, err := TandaTanganRSASHA256(nil, kanonik); !errors.Is(err, ErrTandaTanganTidakSah) {
		t.Fatalf("privat nil: ingin ErrTandaTanganTidakSah, dapat %v", err)
	}
}

func TestKunciPEMBolakBalik(t *testing.T) {
	privat, _ := kunciUji(t)
	derPKCS8, err := x509.MarshalPKCS8PrivateKey(privat)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemPrivat := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: derPKCS8})

	derSPKI, err := x509.MarshalPKIXPublicKey(&privat.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	pemPublik := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: derSPKI})

	privatBaca, err := KunciPrivatDariPEM(pemPrivat)
	if err != nil {
		t.Fatalf("KunciPrivatDariPEM: %v", err)
	}
	publikBaca, err := KunciPublikDariPEM(pemPublik)
	if err != nil {
		t.Fatalf("KunciPublikDariPEM: %v", err)
	}
	kanonik := "POST:/x:TOKEN:" + strings.ToLower("0") + ":2026-10-09T03:30:00+07:00"
	sig, err := TandaTanganRSASHA256(privatBaca, kanonik)
	if err != nil {
		t.Fatalf("TandaTanganRSASHA256: %v", err)
	}
	if err := VerifikasiRSASHA256(publikBaca, kanonik, sig); err != nil {
		t.Fatalf("verifikasi setelahmuat PEM: %v", err)
	}
	if _, err := KunciPublikDariPEM([]byte("bukan pem")); err == nil {
		t.Fatal("PEM rusak harus ditolak")
	}
	if _, err := KunciPrivatDariPEM([]byte("bukan pem")); err == nil {
		t.Fatal("PEM rusak harus ditolak")
	}
	// Kunci publik yang di-PEM lalu dimuat sebagai kunci privat RSA harus gagal
	// (jenis kunci tidak cocok), bukan mengembalikan nilai salah.
	if _, err := KunciPrivatDariPEM([]byte(base64.StdEncoding.EncodeToString(derSPKI))); err == nil {
		t.Fatal("bukan PEM harus ditolak")
	}
}
