package payment

// MockReader adalah implementasi deterministik `PaymentReader` untuk
// lingkungan lokal dan pengujian (Architecture.md §1: "OCR mode mock";
// Architecture.md §11: implementasi ganda untuk menghindari vendor lock-in).
//
// Deterministik berarti bacaan hanya ditentukan oleh ISI berkas: sidik jari
// SHA-256 isi dipakai sebagai kunci. Berkas yang sama selalu menghasilkan
// bacaan yang sama, berkas berbeda bisa dipetakan berbeda, dan berkas yang
// tidak dikenal memakai nilai bawaan — bukan hasil acak.
//
// Bawaannya (tanpa konfigurasi apa pun) adalah "tidak terbaca": nominal nil
// dan keyakinan 0. Lingkungan lokal karena itu TIDAK pernah menyetujui
// pembayaran atas dasar angka karangan; ia hanya menghasilkan antrean perlu
// review. Uji yang ingin auto-approve memetakan berkasnya secara eksplisit.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// MockKey mengembalikan sidik jari isi berkas (hex SHA-256) yang dipakai
// sebagai kunci pemetaan hasil bacaan MockReader.
func MockKey(isi []byte) string {
	sum := sha256.Sum256(isi)
	return hex.EncodeToString(sum[:])
}

// NewMockReader membuat pembaca tiruan dengan pemetaan sidik jari → bacaan.
func NewMockReader(hasil map[string]ReadResult) *MockReader {
	if hasil == nil {
		hasil = map[string]ReadResult{}
	}
	return &MockReader{Hasil: hasil}
}

// MockReader memenuhi PaymentReader.
var _ PaymentReader = (*MockReader)(nil)

// MockReader memetakan sidik jari isi berkas ke bacaan yang sudah ditentukan.
type MockReader struct {
	// Hasil memetakan sidik jari isi (MockKey) ke bacaan.
	Hasil map[string]ReadResult
	// Bawaan dipakai untuk berkas yang tidak ada di peta. Nilai nol berarti
	// "tidak terbaca" (nominal nil, keyakinan 0).
	Bawaan ReadResult
	// Galat, bila diisi, selalu dikembalikan menggantikan bacaan apa pun —
	// dipakai menguji perilaku ketika provider gagal (pekerjaan harus
	// diulang, bukan menyimpan bacaan kosong).
	Galat error

	mu        sync.Mutex
	dipanggil int
}

// Read mengembalikan bacaan yang dipetakan untuk isi berkas tersebut.
func (m *MockReader) Read(_ context.Context, in ReadInput) (ReadResult, error) {
	m.mu.Lock()
	m.dipanggil++
	m.mu.Unlock()
	if m.Galat != nil {
		return ReadResult{}, m.Galat
	}
	if r, ok := m.Hasil[MockKey(in.Content)]; ok {
		return salinBacaan(r), nil
	}
	return salinBacaan(m.Bawaan), nil
}

// JumlahPanggilan melaporkan berapa kali provider tiruan dipanggil. Dipakai
// pengujian "AI mati tidak memanggil provider".
func (m *MockReader) JumlahPanggilan() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dipanggil
}

// salinBacaan menyalin bacaan supaya pemanggil tidak bisa mengubah peta
// konfigurasi maupun nilai bersama lewat pointer.
func salinBacaan(r ReadResult) ReadResult {
	out := r
	if r.Nominal != nil {
		n := *r.Nominal
		out.Nominal = &n
	}
	if r.Tanggal != nil {
		t := *r.Tanggal
		out.Tanggal = &t
	}
	if r.Raw != nil {
		out.Raw = append([]byte(nil), r.Raw...)
	}
	return out
}
