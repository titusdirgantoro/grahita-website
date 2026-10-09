// Package va memuat KONTRAK dan PROTOTIPE adapter Virtual Account (kartu
// F3-29 "Kajian dan kontrak integrasi Virtual Account").
//
// Status paket ini dengan sengaja adalah PROTOTIPE:
//
//   - paket ini TIDAK diimpor oleh cmd/server, router, worker, maupun domain
//     pembayaran. Tidak ada satu pun nomor Virtual Account palsu yang dibuat
//     atau callback yang diproses di sistem berjalan. Fitur VA tetap MATI
//     (keputusan user: belum ada gateway/VA, bank/penyedia belum dipilih) dan
//     verifikasi manual + Cash & Carry tidak berubah;
//   - paket ini TIDAK memuat provider sungguhan. Ia memuat (a) bentuk kontrak
//     yang harus dipenuhi adapter bank/penyedia mana pun, (b) `MockProvider`
//     deterministik untuk menguji kontrak itu tanpa akses bank, dan
//     (c) pencocokan nominal pesanan-versus-callback yang murni.
//   - pemetaan ke domain (transisi Done lewat `payment.MarkVerified` dengan
//     metode `virtual_account`, tabel `va_*`, rute callback bertanda tangan,
//     dan toggle `va.aktif` bawaan mati) adalah lingkup kartu F3-30 yang
//     bertumpu pada kontrak di sini.
//
// Dokumen kontrak lengkap (studi opsi BCA/BRI host-to-host versus penyedia,
// autentikasi/tanda tangan, idempotensi, model data, kebutuhan kredensial &
// sandbox, checklist provisioning, human gate, dan keputusan tertunda):
// `docs/design/f3-29/KONTRAK-VA.md`.
//
// Aturan yang disengaja berlaku untuk SEMUA implementasi `Provider`:
//
//  1. `TerimaCallback` TIDAK PERNAH menulis apa pun ke basis data, pesanan,
//     atau outbox. Ia hanya memverifikasi tanda tangan + bentuk payload dan
//     mengembalikan peristiwa pembayaran. Efek samping (klaim idempotensi,
//     pencocokan nominal, transisi Done, audit) adalah pekerjaan pemanggil.
//  2. Provider TIDAK PERNAH memutuskan "lunas secara final": keputusan
//     verifikasi tetap milik manusia/`MarkVerified` (PRD §5: AI pun tidak
//     menolak secara final; VA mengikuti sikap yang sama — selisih nominal atau
//     rekening tidak cocok jatuh ke "perlu review", bukan diam-diam Done).
//  3. Tanda tangan wajib diverifikasi dengan perbandingan waktu-konstan
//     (`crypto/subtle`) sebelum payload disentuh.
//  4. Kredensial (client id, secret, private key) tidak pernah masuk ke
//     `Raw`/log/entri audit.
package va

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Sentinel error kontrak. Semuanya dipetakan pemanggil (F3-30) menjadi balasan
// HTTP/audit; paket ini tidak memuat pemetaan HTTP.
var (
	// ErrProviderBelumDipilih dikembalikan bila adapter dibuat tanpa bank/
	// penyedia (keadaan sekarang: keputusan user belum ada).
	ErrProviderBelumDipilih = errors.New("bank/penyedia virtual account belum dipilih")
	// ErrProviderTidakTersedia dikembalikan bila provider belum dapat dipakai
	// (kredensial/sandbox belum ada, endpoint tidak terjangkau). Pemanggil
	// memperlakukannya sebagai kegagalan yang dapat diulang — bukan sebagai
	// pembayaran yang sah.
	ErrProviderTidakTersedia = errors.New("provider virtual account tidak tersedia")
	// ErrTandaTanganTidakSah dikembalikan bila tanda tangan callback tidak
	// cocok dengan isi permintaan.
	ErrTandaTanganTidakSah = errors.New("tanda tangan callback tidak sah")
	// ErrPayloadTidakSah dikembalikan bila isi callback tidak dapat dipahami
	// atau tidak memuat bidang wajib.
	ErrPayloadTidakSah = errors.New("payload callback tidak sah")
	// ErrCallbackKedaluwarsa dikembalikan bila stempel waktu callback berada
	// di luar jendela yang diterima (indikasi replay). Pemanggil membalas ack
	// tanpa efek samping.
	ErrCallbackKedaluwarsa = errors.New("callback di luar jendela waktu yang diterima")
	// ErrNomorVATidakDikenal dikembalikan bila callback menunjuk nomor VA yang
	// tidak pernah diterbitkan sistem ini.
	ErrNomorVATidakDikenal = errors.New("nomor virtual account tidak dikenal")
	// ErrPenjagaIdempotensiTidakAda dikembalikan bila `Adapter` dipasang tanpa
	// penjaga idempotensi. Tanpa penjaga, callback yang diulang provider dapat
	// dihitung dua kali — jadi keadaannya GAGAL TERTUTUP, bukan "lanjut tanpa
	// penjaga".
	ErrPenjagaIdempotensiTidakAda = errors.New("penjaga idempotensi callback belum dipasang")
)

// Bank adalah kode bank seperti pada enum `bank_code` (Database.md §3). Nilai
// sengaja TIDAK menentukan provider: BCA dapat diakses host-to-host langsung
// atau lewat penyedia VA.
type Bank string

const (
	BankBCA Bank = "BCA"
	BankBRI Bank = "BRI"
)

// PermintaanVA adalah satu permintaan penerbitan nomor Virtual Account untuk
// sebuah pesanan.
type PermintaanVA struct {
	// ReffID adalah kunci idempotensi dari sisi kita (mis. "order:<uuid>").
	// Dua permintaan dengan ReffID sama WAJIB menghasilkan nomor VA yang sama.
	ReffID string
	// OrderID/NomorPesanan dipakai sebagai keterangan dan untuk pencocokan.
	OrderID      string
	NomorPesanan string
	// Nominal adalah total pesanan (Rupiah bulat, BR-10).
	Nominal int64
	// NamaCabang/EmailCabang hanya dipakai bila provider memerlukan data
	// pelanggan; tidak ada data pribadi lain yang dikirim.
	NamaCabang  string
	EmailCabang string
	Keterangan  string
	// KedaluwarsaPada nol berarti memakai bawaan provider.
	KedaluwarsaPada time.Time
}

// HasilVA adalah jawaban provider atas `PermintaanVA`.
type HasilVA struct {
	Bank            Bank
	NomorVA         string
	ReffID          string
	KedaluwarsaPada time.Time
	// Raw adalah jawaban mentah provider untuk audit. Nilai yang tampak
	// rahasia dibuang sebelum disimpan (SanitizeRaw milik domain pembayaran
	// dapat dipakai ulang).
	Raw json.RawMessage
}

// PermintaanCallback adalah satu panggilan callback dari provider, sudah
// dibaca apa adanya dari jaringan.
type PermintaanCallback struct {
	// Method/Path adalah bagian stringToSign (bentuk kanonik tanda tangan).
	Method string
	Path   string
	Header http.Header
	// Body adalah isi permintaan MENTAH (belum di-parse) — tanda tangan
	// dihitung atas byte ini.
	Body []byte
	// DiterimaPada adalah jam server kita saat callback tiba (dipakai untuk
	// jendela replay; diuji deterministik lewat Clock).
	DiterimaPada time.Time
}

// PeristiwaBayar adalah satu pembayaran yang diklaim provider sudah terjadi.
// Nominal adalah uang yang benar-benar masuk menurut provider; ia BELUM
// dibandingkan dengan pesanan (pencocokan dilakukan `CocokNominal`).
type PeristiwaBayar struct {
	// IDProvider adalah id peristiwa unik dari provider (kunci idempotensi
	// utama bila ada; sebagian penyedia hanya memberi id transaksi bank).
	IDProvider   string
	Bank         Bank
	NomorVA      string
	Nominal      int64
	WaktuBayar   time.Time
	NamaPembayar string
	Raw          json.RawMessage
}

// Kunci mengembalikan kunci idempotensi stabil untuk peristiwa ini. Bila
// provider memberi id peristiwa, id itulah kuncinya; kalau tidak, kunci
// diturunkan dari isi pembayaran sehingga callback yang diulang persis sama
// tetap terdeteksi sebagai duplikat.
func (p PeristiwaBayar) Kunci() string {
	if p.IDProvider != "" {
		return "va:" + p.IDProvider
	}
	h := sha256.New()
	h.Write([]byte(p.Bank))
	h.Write([]byte{0})
	h.Write([]byte(p.NomorVA))
	h.Write([]byte{0})
	h.Write([]byte(p.WaktuBayar.UTC().Format(time.RFC3339)))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatInt(p.Nominal, 10)))
	return "va:" + hex.EncodeToString(h.Sum(nil))
}

// Provider adalah kontrak satu adapter bank/penyedia Virtual Account.
// Implementasi nyata (F3-30) memetakan kontrak ini ke API bank/penyedia yang
// akhirnya dipilih; `MockProvider` memenuhinya tanpa jaringan.
type Provider interface {
	// Nama mengembalikan nama provider untuk registry/konfigurasi (mis.
	// "mock", "bca-h2h", "bri-h2h", "penyedia-va"). Sistem TIDAK boleh
	// menghardcode nama provider di tempat lain.
	Nama() string
	// DaftarVA menerbitkan (atau mengambil kembali) nomor VA satu pesanan.
	// WAJIB idempoten terhadap `PermintaanVA.ReffID`.
	DaftarVA(ctx context.Context, in PermintaanVA) (HasilVA, error)
	// TerimaCallback memverifikasi tanda tangan + bentuk payload dan
	// mengembalikan peristiwa pembayaran. WAJIB:
	//   - memakai `VerifikasiTandaTangan`/`VerifikasiKanonik` (waktu-konstan);
	//   - menolak callback kedaluwarsa (ErrCallbackKedaluwarsa);
	//   - menolak payload cacat/bidang wajib kosong (ErrPayloadTidakSah);
	//   - TIDAK menulis apa pun ke luar dirinya (murni).
	TerimaCallback(ctx context.Context, in PermintaanCallback) (PeristiwaBayar, error)
}

// HasilNominal adalah hasil pencocokan nominal callback dengan total pesanan.
// Nilainya sengaja sama dengan `verif_status`/`review_reason` (Database.md §3)
// supaya pemetaan ke domain tidak menciptakan kosakata baru.
type HasilNominal string

const (
	// NominalCocok berarti selisih berada di dalam toleransi.
	NominalCocok HasilNominal = "cocok"
	// NominalKurangBayar menandakan pembayaran kurang (perlu review).
	NominalKurangBayar HasilNominal = "kurang_bayar"
	// NominalLebihBayar menandakan pembayaran berlebih (perlu review).
	NominalLebihBayar HasilNominal = "lebih_bayar"
)

// CocokNominal membandingkan nominal callback dengan total pesanan memakai
// toleransi `ai.tol_kurang`/`ai.tol_lebih` yang sudah ada (PRD §4.9/§5) supaya
// perilaku VA dan OCR tidak saling menyimpang. Mengembalikan hasil dan selisih
// (nominal − total, negatif = kurang).
func CocokNominal(totalPesanan, nominalCallback, tolKurang, tolLebih int64) (HasilNominal, int64) {
	if tolKurang < 0 {
		tolKurang = 0
	}
	if tolLebih < 0 {
		tolLebih = 0
	}
	selisih := nominalCallback - totalPesanan
	switch {
	case selisih < -tolKurang:
		return NominalKurangBayar, selisih
	case selisih > tolLebih:
		return NominalLebihBayar, selisih
	default:
		return NominalCocok, selisih
	}
}

// PenyimpanIdempotensi adalah penjaga replay/duplikasi callback. Implementasi
// produksi (F3-30) memakai tabel `va_callbacks` dengan kunci unik
// (`provider_id` atau kunci turunan) di dalam transaksi; implementasi
// prototipe (`MemoriIdempotensi`) memakai peta.
type PenyimpanIdempotensi interface {
	// Klaim mencoba mengklaim kunci. `true` berarti pemanggil PERTAMA dan
	// peristiwa boleh diproses; `false` berarti callback duplikat/replay dan
	// pemanggil hanya boleh membalas ack tanpa efek samping. Klaim yang gagal
	// karena galat penyimpanan bukan duplikat — galat itu wajib diteruskan
	// supaya pemanggil dapat meminta provider mengirim ulang callback.
	Klaim(ctx context.Context, kunci string) (bool, error)
}

// MemoriIdempotensi adalah penjaga idempotensi dalam memori untuk prototipe
// dan pengujian (bukan untuk produksi: keadaan hilang saat proses restart).
type MemoriIdempotensi struct {
	terklaim map[string]bool
}

// NewMemoriIdempotensi membuat penjaga kosong.
func NewMemoriIdempotensi() *MemoriIdempotensi {
	return &MemoriIdempotensi{terklaim: map[string]bool{}}
}

// Klaim mengimplementasikan PenyimpanIdempotensi.
func (m *MemoriIdempotensi) Klaim(_ context.Context, kunci string) (bool, error) {
	if kunci == "" {
		return false, ErrPayloadTidakSah
	}
	if m.terklaim[kunci] {
		return false, nil
	}
	m.terklaim[kunci] = true
	return true, nil
}

// KodeCallback adalah hasil pemrosesan satu callback oleh `Adapter`.
type KodeCallback string

const (
	// KodeDiterima: callback sah, pertama kali, dan nominal cocok. Pemanggil
	// boleh melanjutkan ke transisi Done lewat `payment.MarkVerified`.
	KodeDiterima KodeCallback = "diterima"
	// KodePerluReview: callback sah dan pertama kali, tetapi nominal di luar
	// toleransi → pesanan tetap menunggu keputusan manusia.
	KodePerluReview KodeCallback = "perlu_review"
	// KodeDuplikat: callback sudah pernah diproses — balas ack, tanpa efek.
	KodeDuplikat KodeCallback = "duplikat"
	// KodeDiabaikan: fitur VA tidak terpasang (toggle mati). Tidak ada
	// provider yang dipanggil dan tidak ada keadaan yang disentuh.
	KodeDiabaikan KodeCallback = "diabaikan"
	// KodeDitolak: tanda tangan/payload tidak sah, atau provider tidak dapat
	// dipakai. Balasan mengikuti kontrak provider; tidak ada efek samping.
	KodeDitolak KodeCallback = "ditolak"
)

// HasilCallback adalah ringkasan pemrosesan satu callback oleh `Adapter`.
type HasilCallback struct {
	Kode      KodeCallback
	Peristiwa PeristiwaBayar
	Nominal   HasilNominal
	Selisih   int64
	Total     int64
	// Kunci adalah kunci idempotensi yang dipakai (untuk audit).
	Kunci string
}

// Adapter adalah PROTOTIPE perakit callback: provider + penjaga idempotensi +
// pencocokan nominal. Ia sengaja tidak menyentuh basis data: F3-30 mengganti
// `PenyimpanIdempotensi` dengan implementasi PostgreSQL dan memasang rute
// HTTP-nya. `Provider` dan `Penjaga` dua-duanya WAJIB terisi saat `Terpasang`
// bernilai benar — keadaannya gagal tertutup (`KodeDitolak` + galat), bukan
// lanjut diam-diam tanpa idempotensi.
type Adapter struct {
	Provider Provider
	Penjaga  PenyimpanIdempotensi
	// Terpasang adalah saklar fitur (bawaan wajib MATI: `va.aktif=false`).
	Terpasang bool
}

// TanganiCallback memproses satu callback: verifikasi tanda tangan di provider,
// cocokkan nominal, lalu klaim idempotensi. Urutan ini disengaja —
// tanda tangan diperiksa lebih dahulu (callback tidak sah tidak boleh menyentuh
// penjaga idempotensi), dan klaim dilakukan paling akhir supaya callback yang
// tidak sah tidak "membakar" kunci peristiwa yang sah.
func (a *Adapter) TanganiCallback(ctx context.Context, in PermintaanCallback, totalPesanan, tolKurang, tolLebih int64) (HasilCallback, error) {
	if !a.Terpasang {
		return HasilCallback{Kode: KodeDiabaikan, Total: totalPesanan}, nil
	}
	if a.Provider == nil {
		return HasilCallback{Kode: KodeDitolak, Total: totalPesanan}, ErrProviderBelumDipilih
	}
	if a.Penjaga == nil {
		return HasilCallback{Kode: KodeDitolak, Total: totalPesanan}, ErrPenjagaIdempotensiTidakAda
	}
	peristiwa, err := a.Provider.TerimaCallback(ctx, in)
	if err != nil {
		return HasilCallback{Kode: KodeDitolak, Total: totalPesanan}, err
	}
	nominal, selisih := CocokNominal(totalPesanan, peristiwa.Nominal, tolKurang, tolLebih)
	hasil := HasilCallback{
		Peristiwa: peristiwa,
		Nominal:   nominal,
		Selisih:   selisih,
		Total:     totalPesanan,
		Kunci:     peristiwa.Kunci(),
	}
	baru, kerr := a.Penjaga.Klaim(ctx, hasil.Kunci)
	if kerr != nil {
		hasil.Kode = KodeDitolak
		return hasil, kerr
	}
	if !baru {
		hasil.Kode = KodeDuplikat
		return hasil, nil
	}
	if nominal == NominalCocok {
		hasil.Kode = KodeDiterima
	} else {
		hasil.Kode = KodePerluReview
	}
	return hasil, nil
}
