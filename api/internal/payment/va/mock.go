package va

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MockProvider adalah tiruan bank/penyedia yang memenuhi kontrak `Provider`
// TANPA jaringan. Perannya dua:
//
//  1. menjadi "provider" dalam kontrak-test sehingga kontrak dapat diuji di CI
//     tanpa sandbox bank (kartu F3-29 wajib membuktikan kontrak teruji);
//  2. menjadi alat bantu untuk kartu F3-30/T8: adapter sungguhan dapat
//     dibandingkan perilakunya dengan mock ini (kriteria T8: "bandingkan hasil
//     sandbox nyata dengan mock").
//
// MockProvider TIDAK pernah dipakai di jalur produksi: ia tidak diimpor
// `cmd/server`, dan nomor VA yang dihasilkannya deterministik dari ReffID
// (bukan nomor sungguhan).
type MockProvider struct {
	cfg MockConfig

	mu      sync.Mutex
	perReff map[string]HasilVA
	dikenal map[string]PermintaanVA
}

// MockConfig mengatur perilaku tiruan.
type MockConfig struct {
	// Bank yang ditiru (bawaan BCA).
	Bank Bank
	// Rahasia adalah kunci HMAC bersama (setara client secret host-to-host).
	// Wajib diisi; kosong berarti provider tidak dapat dipakai.
	Rahasia []byte
	// HeaderTandaTangan adalah nama header tanda tangan; stempel waktu dibaca
	// dari `<HeaderTandaTangan>-Timestamp` (bawaan "X-VA-Signature" →
	// "X-VA-Signature-Timestamp").
	HeaderTandaTangan string
	// PrefixVA adalah awalan nomor VA (BCA memakai 8808 untuk VA perusahaan).
	PrefixVA string
	// KedaluwarsaVA adalah masa berlaku bawaan nomor VA.
	KedaluwarsaVA time.Duration
	// JendelaReplay adalah lebar jendela waktu yang diterima (bawaan
	// JendelaReplayBawaan).
	JendelaReplay time.Duration
	// ToleransiJamDepan menyerap selisih jam kita↔provider.
	ToleransiJamDepan time.Duration
	// Nama provider untuk registry (bawaan "mock").
	Nama string
	// PathCallback adalah path yang ditandatangani provider (bawaan
	// "/api/v1/va/callback"). Harus sama dengan path rute nyata nanti.
	PathCallback string
}

func (c MockConfig) denganBawaan() MockConfig {
	if c.Bank == "" {
		c.Bank = BankBCA
	}
	if c.HeaderTandaTangan == "" {
		c.HeaderTandaTangan = "X-VA-Signature"
	}
	if c.PrefixVA == "" {
		c.PrefixVA = "8808"
	}
	if c.KedaluwarsaVA <= 0 {
		c.KedaluwarsaVA = 24 * time.Hour
	}
	if c.JendelaReplay <= 0 {
		c.JendelaReplay = JendelaReplayBawaan
	}
	if c.ToleransiJamDepan <= 0 {
		c.ToleransiJamDepan = 5 * time.Minute
	}
	if c.Nama == "" {
		c.Nama = "mock"
	}
	if c.PathCallback == "" {
		c.PathCallback = "/api/v1/va/callback"
	}
	return c
}

// NewMockProvider membuat tiruan provider. Kunci HMAC wajib diisi.
func NewMockProvider(cfg MockConfig) (*MockProvider, error) {
	cfg = cfg.denganBawaan()
	if len(cfg.Rahasia) == 0 {
		return nil, ErrProviderTidakTersedia
	}
	return &MockProvider{
		cfg:     cfg,
		perReff: map[string]HasilVA{},
		dikenal: map[string]PermintaanVA{},
	}, nil
}

// Nama mengimplementasikan Provider.
func (m *MockProvider) Nama() string { return m.cfg.Nama }

// Bank mengembalikan bank yang ditiru.
func (m *MockProvider) Bank() Bank { return m.cfg.Bank }

// HeaderTandaTangan mengembalikan nama header tanda tangan (dipakai uji).
func (m *MockProvider) HeaderTandaTangan() string { return m.cfg.HeaderTandaTangan }

// DaftarVA menerbitkan nomor VA deterministik untuk satu ReffID. Permintaan
// dengan ReffID sama mengembalikan nomor VA yang sama (idempotensi kontrak).
func (m *MockProvider) DaftarVA(_ context.Context, in PermintaanVA) (HasilVA, error) {
	if in.ReffID == "" {
		return HasilVA{}, fmt.Errorf("%w: reff_id kosong", ErrPayloadTidakSah)
	}
	if in.Nominal <= 0 {
		return HasilVA{}, fmt.Errorf("%w: nominal harus > 0", ErrPayloadTidakSah)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if hasil, ada := m.perReff[in.ReffID]; ada {
		return hasil, nil
	}

	nomor := m.nomorVAUntuk(in.ReffID)
	kedaluwarsa := in.KedaluwarsaPada
	if kedaluwarsa.IsZero() {
		kedaluwarsa = time.Now().UTC().Add(m.cfg.KedaluwarsaVA)
	}
	raw, _ := json.Marshal(map[string]any{
		"provider":      m.cfg.Nama,
		"bank":          string(m.cfg.Bank),
		"nomor_va":      nomor,
		"reff_id":       in.ReffID,
		"trx_id":        "MOCK-" + nomor,
		"expired_at":    kedaluwarsa.Format(time.RFC3339),
		"nominal":       in.Nominal,
		"nomor_pesanan": in.NomorPesanan,
	})
	hasil := HasilVA{
		Bank:            m.cfg.Bank,
		NomorVA:         nomor,
		ReffID:          in.ReffID,
		KedaluwarsaPada: kedaluwarsa,
		Raw:             raw,
	}
	m.perReff[in.ReffID] = hasil
	m.dikenal[nomor] = in
	return hasil, nil
}

// nomorVAUntuk menurunkan nomor VA deterministik dari ReffID (16 digit:
// prefix + 12 digit angka). Pemanggil WAJIB memegang kunci `mu`.
func (m *MockProvider) nomorVAUntuk(reffID string) string {
	ringkas := sha256.Sum256([]byte(m.cfg.Nama + "|" + reffID))
	var angka strings.Builder
	for i := 0; i < 6 && angka.Len() < 12; i++ {
		v := binary.BigEndian.Uint32(ringkas[i*4 : i*4+4])
		angka.WriteString(fmt.Sprintf("%010d", v%1_000_000_000))
	}
	s := angka.String()
	if len(s) > 12 {
		s = s[:12]
	}
	return m.cfg.PrefixVA + s
}

// TerimaCallback memverifikasi tanda tangan + jendela waktu + nomor VA, lalu
// mengembalikan peristiwa pembayaran. Ia tidak menulis apa pun ke luar dirinya.
func (m *MockProvider) TerimaCallback(_ context.Context, in PermintaanCallback) (PeristiwaBayar, error) {
	if len(in.Body) == 0 {
		return PeristiwaBayar{}, fmt.Errorf("%w: badan kosong", ErrPayloadTidakSah)
	}
	method := in.Method
	if method == "" {
		method = http.MethodPost
	}
	permin := in
	permin.Method = method

	timestamp := permin.Header.Get(m.cfg.HeaderTandaTangan + "-Timestamp")
	if err := VerifikasiKanonik(m.cfg.Rahasia, permin, m.cfg.HeaderTandaTangan); err != nil {
		return PeristiwaBayar{}, err
	}
	if err := PeriksaUmur(timestamp, permin.DiterimaPada, m.cfg.JendelaReplay, m.cfg.ToleransiJamDepan); err != nil {
		return PeristiwaBayar{}, err
	}

	var isi struct {
		NomorVA      string `json:"nomor_va"`
		EventID      string `json:"event_id"`
		Nominal      int64  `json:"nominal"`
		WaktuBayar   string `json:"waktu_bayar"`
		NamaPembayar string `json:"nama_pembayar"`
		Bank         string `json:"bank"`
	}
	if err := json.Unmarshal(in.Body, &isi); err != nil {
		return PeristiwaBayar{}, fmt.Errorf("%w: %v", ErrPayloadTidakSah, err)
	}
	if isi.NomorVA == "" {
		return PeristiwaBayar{}, fmt.Errorf("%w: nomor_va kosong", ErrPayloadTidakSah)
	}
	if isi.Nominal <= 0 {
		return PeristiwaBayar{}, fmt.Errorf("%w: nominal harus > 0", ErrPayloadTidakSah)
	}
	waktu, err := time.Parse(time.RFC3339, isi.WaktuBayar)
	if err != nil {
		return PeristiwaBayar{}, fmt.Errorf("%w: waktu_bayar bukan RFC3339", ErrPayloadTidakSah)
	}

	m.mu.Lock()
	_, dikenal := m.dikenal[isi.NomorVA]
	m.mu.Unlock()
	if !dikenal {
		return PeristiwaBayar{}, ErrNomorVATidakDikenal
	}

	bank := m.cfg.Bank
	if isi.Bank != "" {
		bank = Bank(isi.Bank)
	}
	return PeristiwaBayar{
		IDProvider:   isi.EventID,
		Bank:         bank,
		NomorVA:      isi.NomorVA,
		Nominal:      isi.Nominal,
		WaktuBayar:   waktu.UTC(),
		NamaPembayar: isi.NamaPembayar,
		Raw:          json.RawMessage(append([]byte(nil), in.Body...)),
	}, nil
}

// MockCallbackInput adalah bahan pembuatan callback sah oleh tiruan (perannya
// sebagai bank).
type MockCallbackInput struct {
	NomorVA      string
	EventID      string
	Nominal      int64
	WaktuBayar   time.Time
	NamaPembayar string
	// Method/Path boleh dikosongkan (POST + PathCallback bawaan).
	Method string
	Path   string
	// JamServer adalah waktu terima di sisi kita (bawaan WaktuBayar bila nol).
	JamServer time.Time
	// TimestampLangsung mengisi stempel waktu yang ditandatangani dengan string
	// apa adanya (bukan hasil format WaktuBayar). HANYA untuk uji kontrak
	// (mis. stempel waktu rusak); adapter sungguhan tidak memakainya.
	TimestampLangsung string
}

func (m *MockProvider) methodPath(in MockCallbackInput) (string, string) {
	method := in.Method
	if method == "" {
		method = http.MethodPost
	}
	path := in.Path
	if path == "" {
		path = m.cfg.PathCallback
	}
	return method, path
}

func (m *MockProvider) badan(in MockCallbackInput, waktu time.Time) []byte {
	badan, _ := json.Marshal(map[string]any{
		"nomor_va":      in.NomorVA,
		"event_id":      in.EventID,
		"nominal":       in.Nominal,
		"waktu_bayar":   waktu.UTC().Format(time.RFC3339),
		"nama_pembayar": in.NamaPembayar,
		"bank":          string(m.cfg.Bank),
	})
	return badan
}

// CallbackBadanMenandatangani mengembalikan callback dengan badan yang
// ditentukan pemanggil tetapi tanda tangan yang SAH. Diperlukan uji kontrak
// untuk kasus payload (mis. bidang tambahan yang justru harus diterima).
func (m *MockProvider) CallbackBadanMenandatangani(in MockCallbackInput, badan []byte) PermintaanCallback {
	method, path := m.methodPath(in)
	waktu := in.WaktuBayar
	if waktu.IsZero() {
		waktu = time.Now().UTC()
	}
	timestamp := in.TimestampLangsung
	if timestamp == "" {
		timestamp = waktu.UTC().Format(time.RFC3339)
	}
	kanonik := Kanonik(method, path, timestamp, badan)
	header := http.Header{}
	header.Set(m.cfg.HeaderTandaTangan, TandaTangan(m.cfg.Rahasia, kanonik))
	header.Set(m.cfg.HeaderTandaTangan+"-Timestamp", timestamp)
	header.Set("Content-Type", "application/json")

	jamServer := in.JamServer
	if jamServer.IsZero() {
		jamServer = waktu
	}
	return PermintaanCallback{Method: method, Path: path, Header: header, Body: badan, DiterimaPada: jamServer}
}

// BuatCallback menghasilkan callback yang benar-benar bertanda tangan sah,
// seolah dikirim bank. Dipakai kontrak-test dan kartu T8.
func (m *MockProvider) BuatCallback(in MockCallbackInput) PermintaanCallback {
	waktu := in.WaktuBayar
	if waktu.IsZero() {
		waktu = time.Now().UTC()
	}
	return m.CallbackBadanMenandatangani(in, m.badan(in, waktu))
}

// CallbackSah mengembalikan callback yang sah (tanda tangan benar). Ia adalah
// bagian harness uji kontrak (`va/vatest`) — perannya sebagai "bank" tiruan.
func (m *MockProvider) CallbackSah(in MockCallbackInput) PermintaanCallback {
	return m.BuatCallback(in)
}

// CallbackTandaTanganRusak mengembalikan callback yang isinya sah tetapi tanda
// tangannya salah (nilai pengganti deterministik), untuk membuktikan provider
// menolak tanda tangan yang tidak cocok.
func (m *MockProvider) CallbackTandaTanganRusak(in MockCallbackInput) PermintaanCallback {
	c := m.BuatCallback(in)
	c.Header.Set(m.cfg.HeaderTandaTangan, strings.Repeat("0", 64))
	return c
}

// ItuNomorVA adalah pembantu uji: apakah nomor VA pernah diterbitkan.
func (m *MockProvider) ItuNomorVA(nomor string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ada := m.dikenal[nomor]
	return ada
}

// NomorVAPermintaan adalah pembantu uji: nomor VA untuk ReffID ("" bila belum
// pernah diterbitkan).
func (m *MockProvider) NomorVAPermintaan(reffID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ada := m.perReff[reffID]; ada {
		return h.NomorVA
	}
	return ""
}

// JumlahVA adalah pembantu uji: jumlah VA yang diterbitkan.
func (m *MockProvider) JumlahVA() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.perReff)
}

// NominalDariString dipakai uji/kartu T8 untuk membaca nominal uang dari teks
// rupiah sederhana (mis. "150000" atau "150.000"); nilai tak sah → galat.
func NominalDariString(s string) (int64, error) {
	bersih := strings.NewReplacer(".", "", ",", "", " ", "", "Rp", "").Replace(s)
	v, err := strconv.ParseInt(bersih, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: nominal %q tidak dapat dibaca", ErrPayloadTidakSah, s)
	}
	return v, nil
}

var _ Provider = (*MockProvider)(nil)
