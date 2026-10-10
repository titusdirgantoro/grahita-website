// Package vatest memuat UJI KONTRAK adapter Virtual Account (kartu F3-29).
//
// Tujuannya: kontrak di paket `va` dapat dijalankan terhadap implementasi mana
// pun — `va.MockProvider` sekarang (tanpa akses bank), dan adapter bank/penyedia
// sungguhan pada F3-30/T8 (dibandingkan dengan mock, persis kriteria T8
// "bandingkan hasil sandbox nyata dengan mock").
//
// Paket ini HANYA memuat bantuan pengujian: tidak diimpor kode produksi.
package vatest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/payment/va"
)

// Harness adalah provider yang dapat membangkitkan callback tiruan. Adapter
// sungguhan memenuhinya dengan memakai sandbox bank; `va.MockProvider`
// memenuhinya secara lokal.
type Harness interface {
	va.Provider
	// CallbackSah mengembalikan callback yang tanda tangannya benar.
	CallbackSah(in va.MockCallbackInput) va.PermintaanCallback
	// CallbackTandaTanganRusak mengembalikan callback berisi data sah dengan
	// tanda tangan salah.
	CallbackTandaTanganRusak(in va.MockCallbackInput) va.PermintaanCallback
	// CallbackBadanMenandatangani menandatangani badan yang ditentukan
	// pemanggil (untuk kasus payload: bidang tambahan, JSON rusak, dan
	// sejenisnya). Adapter sungguhan memenuhinya dengan kunci yang diberikan
	// sandbox bank.
	CallbackBadanMenandatangani(in va.MockCallbackInput, badan []byte) va.PermintaanCallback
}

// Uji menjalankan seluruh kontrak terhadap harness yang dibangun pabrik.
// Pabrik dipanggil sekali per sub-uji supaya tidak ada keadaan yang bocor
// antar sub-uji.
func Uji(t *testing.T, pabrik func(t *testing.T) Harness) {
	t.Helper()

	const (
		reffID = "order:11111111-1111-1111-1111-111111111111"
		total  = int64(250_000)
	)

	t.Run("daftar_VA_idempoten_dan_unik", func(t *testing.T) {
		h := pabrik(t)
		ctx := context.Background()

		pertama, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID, OrderID: reffID, NomorPesanan: "2026-10-07", Nominal: total, NamaCabang: "Cabang Bandung"})
		if err != nil {
			t.Fatalf("DaftarVA pertama: %v", err)
		}
		if pertama.NomorVA == "" {
			t.Fatal("nomor VA kosong")
		}
		if pertama.ReffID != reffID {
			t.Fatalf("ReffID tidak dikembalikan apa adanya: %q", pertama.ReffID)
		}
		if len(pertama.Raw) == 0 || !json.Valid(pertama.Raw) {
			t.Fatalf("Raw harus JSON sah untuk audit, dapat %q", pertama.Raw)
		}

		ulang, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID, OrderID: reffID, NomorPesanan: "2026-10-07", Nominal: total, NamaCabang: "Cabang Bandung"})
		if err != nil {
			t.Fatalf("DaftarVA ulang: %v", err)
		}
		if ulang.NomorVA != pertama.NomorVA {
			t.Fatalf("DaftarVA tidak idempoten: %q != %q", ulang.NomorVA, pertama.NomorVA)
		}

		lain, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID + "-lain", Nominal: total})
		if err != nil {
			t.Fatalf("DaftarVA reff lain: %v", err)
		}
		if lain.NomorVA == pertama.NomorVA {
			t.Fatal("dua ReffID berbeda mendapat nomor VA yang sama")
		}
	})

	t.Run("daftar_VA_menolak_masukan_tidak_sah", func(t *testing.T) {
		h := pabrik(t)
		ctx := context.Background()

		if _, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: "", Nominal: total}); !errors.Is(err, va.ErrPayloadTidakSah) {
			t.Fatalf("reff_id kosong: ingin ErrPayloadTidakSah, dapat %v", err)
		}
		if _, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID, Nominal: 0}); !errors.Is(err, va.ErrPayloadTidakSah) {
			t.Fatalf("nominal 0: ingin ErrPayloadTidakSah, dapat %v", err)
		}
		if _, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID, Nominal: -1}); !errors.Is(err, va.ErrPayloadTidakSah) {
			t.Fatalf("nominal negatif: ingin ErrPayloadTidakSah, dapat %v", err)
		}
	})

	t.Run("callback_sah_mengembalikan_peristiwa", func(t *testing.T) {
		h := pabrik(t)
		ctx := context.Background()
		waktu := time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC)

		hasilVA, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID, Nominal: total})
		if err != nil {
			t.Fatalf("DaftarVA: %v", err)
		}
		cb := h.CallbackSah(va.MockCallbackInput{
			NomorVA: hasilVA.NomorVA, EventID: "EV-1", Nominal: total,
			WaktuBayar: waktu, NamaPembayar: "Budi", JamServer: waktu,
		})
		peristiwa, err := h.TerimaCallback(ctx, cb)
		if err != nil {
			t.Fatalf("TerimaCallback: %v", err)
		}
		if peristiwa.Nominal != total {
			t.Fatalf("nominal %d, ingin %d", peristiwa.Nominal, total)
		}
		if peristiwa.NomorVA != hasilVA.NomorVA {
			t.Fatalf("nomor VA %q, ingin %q", peristiwa.NomorVA, hasilVA.NomorVA)
		}
		if peristiwa.IDProvider != "EV-1" {
			t.Fatalf("id peristiwa %q, ingin EV-1", peristiwa.IDProvider)
		}
		if !peristiwa.WaktuBayar.Equal(waktu) {
			t.Fatalf("waktu bayar %v, ingin %v", peristiwa.WaktuBayar, waktu)
		}
		kunciPeristiwa := peristiwa.Kunci()
		if kunciPeristiwa == "" {
			t.Fatal("kunci idempotensi kosong")
		}
		// Kunci harus stabil untuk peristiwa yang sama (dipakai penjaga replay).
		if peristiwa.Kunci() != kunciPeristiwa {
			t.Fatal("kunci idempotensi tidak stabil")
		}
		if len(peristiwa.Raw) == 0 || !json.Valid(peristiwa.Raw) {
			t.Fatalf("Raw peristiwa harus JSON sah, dapat %q", peristiwa.Raw)
		}
	})

	t.Run("callback_tanpa_dan_bermasalah_tanda_tangan_ditolak", func(t *testing.T) {
		h := pabrik(t)
		ctx := context.Background()
		waktu := time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC)
		hasilVA, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID, Nominal: total})
		if err != nil {
			t.Fatalf("DaftarVA: %v", err)
		}
		bahan := va.MockCallbackInput{NomorVA: hasilVA.NomorVA, EventID: "EV-2", Nominal: total, WaktuBayar: waktu, JamServer: waktu}

		tanpa := h.CallbackSah(bahan)
		tanpa.Header = map[string][]string{}
		if _, err := h.TerimaCallback(ctx, tanpa); !errors.Is(err, va.ErrTandaTanganTidakSah) {
			t.Fatalf("tanpa tanda tangan: ingin ErrTandaTanganTidakSah, dapat %v", err)
		}

		rusak := h.CallbackTandaTanganRusak(bahan)
		if _, err := h.TerimaCallback(ctx, rusak); !errors.Is(err, va.ErrTandaTanganTidakSah) {
			t.Fatalf("tanda tangan salah: ingin ErrTandaTanganTidakSah, dapat %v", err)
		}

		// Isi diubah SESUDAH ditandatangani (mis. nominal dibesarkan) harus
		// mematahkan tanda tangan: sidik badan ikut ditandatangani.
		diubah := h.CallbackSah(bahan)
		diubah.Body = []byte(strings.Replace(string(diubah.Body), `"nominal":250000`, `"nominal":999999`, 1))
		if string(diubah.Body) == string(h.CallbackSah(bahan).Body) {
			t.Fatal("bahan uji tidak mengandung nominal yang diharapkan")
		}
		if _, err := h.TerimaCallback(ctx, diubah); !errors.Is(err, va.ErrTandaTanganTidakSah) {
			t.Fatalf("badan diubah: ingin ErrTandaTanganTidakSah, dapat %v", err)
		}
	})

	t.Run("callback_kedaluwarsa_atau_dari_masa_depan_ditolak", func(t *testing.T) {
		h := pabrik(t)
		ctx := context.Background()
		waktu := time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC)
		hasilVA, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID, Nominal: total})
		if err != nil {
			t.Fatalf("DaftarVA: %v", err)
		}
		bahan := va.MockCallbackInput{NomorVA: hasilVA.NomorVA, EventID: "EV-3", Nominal: total, WaktuBayar: waktu}

		lampau := bahan
		lampau.JamServer = waktu.Add(va.JendelaReplayBawaan + time.Minute)
		if _, err := h.TerimaCallback(ctx, h.CallbackSah(lampau)); !errors.Is(err, va.ErrCallbackKedaluwarsa) {
			t.Fatalf("callback terlalu tua: ingin ErrCallbackKedaluwarsa, dapat %v", err)
		}

		depan := bahan
		depan.JamServer = waktu.Add(-10 * time.Minute)
		if _, err := h.TerimaCallback(ctx, h.CallbackSah(depan)); !errors.Is(err, va.ErrCallbackKedaluwarsa) {
			t.Fatalf("callback dari masa depan: ingin ErrCallbackKedaluwarsa, dapat %v", err)
		}

		tepat := bahan
		tepat.JamServer = waktu.Add(va.JendelaReplayBawaan - time.Minute)
		if _, err := h.TerimaCallback(ctx, h.CallbackSah(tepat)); err != nil {
			t.Fatalf("callback di dalam jendela harus diterima, dapat %v", err)
		}
	})

	t.Run("callback_stempel_waktu_diubah_atau_rusak_ditolak", func(t *testing.T) {
		h := pabrik(t)
		ctx := context.Background()
		waktu := time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC)
		hasilVA, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID, Nominal: total})
		if err != nil {
			t.Fatalf("DaftarVA: %v", err)
		}
		bahan := va.MockCallbackInput{NomorVA: hasilVA.NomorVA, EventID: "EV-4", Nominal: total, WaktuBayar: waktu, JamServer: waktu}

		// (a) Stempel waktu diubah SESUDAH ditandatangani → tanda tangan
		// mematahkan diri (stempel waktu ikut ditandatangani).
		diubah := h.CallbackSah(bahan)
		for nama := range diubah.Header {
			if strings.HasSuffix(nama, "-Timestamp") {
				diubah.Header.Set(nama, waktu.Add(-2*time.Hour).Format(time.RFC3339))
			}
		}
		if _, err := h.TerimaCallback(ctx, diubah); !errors.Is(err, va.ErrTandaTanganTidakSah) {
			t.Fatalf("stempel waktu diubah: ingin ErrTandaTanganTidakSah, dapat %v", err)
		}

		// (b) Stempel waktu memang rusak tetapi ditandatangani SAH → provider
		// wajib menolaknya sebagai callback kedaluwarsa (bukan panik/lolos).
		badan := h.CallbackSah(bahan).Body
		rusak := h.CallbackBadanMenandatangani(va.MockCallbackInput{
			NomorVA: hasilVA.NomorVA, EventID: "EV-4b", Nominal: total, WaktuBayar: waktu, JamServer: waktu,
			TimestampLangsung: "kemarin",
		}, badan)
		if _, err := h.TerimaCallback(ctx, rusak); !errors.Is(err, va.ErrCallbackKedaluwarsa) {
			t.Fatalf("stempel waktu rusak: ingin ErrCallbackKedaluwarsa, dapat %v", err)
		}
	})

	t.Run("callback_nomor_VA_tidak_dikenal_ditolak", func(t *testing.T) {
		h := pabrik(t)
		ctx := context.Background()
		waktu := time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC)
		cb := h.CallbackSah(va.MockCallbackInput{NomorVA: "8808000000009999", EventID: "EV-5", Nominal: total, WaktuBayar: waktu, JamServer: waktu})
		if _, err := h.TerimaCallback(ctx, cb); !errors.Is(err, va.ErrNomorVATidakDikenal) {
			t.Fatalf("nomor VA asing: ingin ErrNomorVATidakDikenal, dapat %v", err)
		}
	})

	t.Run("callback_payload_cacat_ditolak", func(t *testing.T) {
		h := pabrik(t)
		ctx := context.Background()
		waktu := time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC)
		hasilVA, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID, Nominal: total})
		if err != nil {
			t.Fatalf("DaftarVA: %v", err)
		}

		kasus := map[string]string{
			"json rusak":      `{`,
			"nomor_va kosong": `{"nominal":250000,"waktu_bayar":"2026-10-09T03:30:00Z"}`,
			"nominal nol":     fmt.Sprintf(`{"nomor_va":%q,"nominal":0,"waktu_bayar":"2026-10-09T03:30:00Z"}`, hasilVA.NomorVA),
			"waktu tidak sah": fmt.Sprintf(`{"nomor_va":%q,"nominal":250000,"waktu_bayar":"09-10-2026"}`, hasilVA.NomorVA),
			"badan kosong":    "",
			"nominal negatif": fmt.Sprintf(`{"nomor_va":%q,"nominal":-5,"waktu_bayar":"2026-10-09T03:30:00Z"}`, hasilVA.NomorVA),
		}
		for nama, badan := range kasus {
			cb := h.CallbackBadanMenandatangani(va.MockCallbackInput{NomorVA: hasilVA.NomorVA, EventID: "EV-7", Nominal: total, WaktuBayar: waktu, JamServer: waktu}, []byte(badan))
			if _, err := h.TerimaCallback(ctx, cb); !errors.Is(err, va.ErrPayloadTidakSah) && !errors.Is(err, va.ErrTandaTanganTidakSah) {
				t.Fatalf("%s: ingin ErrPayloadTidakSah, dapat %v", nama, err)
			}
		}

		// Bidang tambahan justru HARUS diterima (provider bebas menambah
		// bidang); dijaga di sini supaya kontrak tidak menjadi terlalu ketat.
		tambahan := fmt.Sprintf(`{"nomor_va":%q,"nominal":250000,"waktu_bayar":"2026-10-09T03:30:00Z","catatan":"x","biaya_admin":2500}`, hasilVA.NomorVA)
		cb := h.CallbackBadanMenandatangani(va.MockCallbackInput{NomorVA: hasilVA.NomorVA, EventID: "EV-6", Nominal: total, WaktuBayar: waktu, JamServer: waktu}, []byte(tambahan))
		if _, err := h.TerimaCallback(ctx, cb); err != nil {
			t.Fatalf("bidang tambahan harus diterima, dapat %v", err)
		}
	})

	t.Run("callback_murni_tidak_menulis_ke_luar", func(t *testing.T) {
		h := pabrik(t)
		ctx := context.Background()
		waktu := time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC)
		hasilVA, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID, Nominal: total})
		if err != nil {
			t.Fatalf("DaftarVA: %v", err)
		}
		cb := h.CallbackSah(va.MockCallbackInput{NomorVA: hasilVA.NomorVA, EventID: "EV-8", Nominal: total, WaktuBayar: waktu, JamServer: waktu})
		pertama, err := h.TerimaCallback(ctx, cb)
		if err != nil {
			t.Fatalf("TerimaCallback pertama: %v", err)
		}
		kedua, err := h.TerimaCallback(ctx, cb)
		if err != nil {
			t.Fatalf("TerimaCallback ulang: %v", err)
		}
		if pertama.Kunci() != kedua.Kunci() {
			t.Fatal("kunci peristiwa berubah pada callback identik")
		}
		if kedua.Nominal != pertama.Nominal || kedua.NomorVA != pertama.NomorVA {
			t.Fatal("hasil callback identik tidak sama")
		}
	})

	t.Run("adapter_toggle_mati_diabaikan", func(t *testing.T) {
		// Provider yang PANIK membuktikan fitur mati tidak menyentuh provider.
		a := &va.Adapter{Provider: providerPanik{}, Penjaga: va.NewMemoriIdempotensi(), Terpasang: false}
		hasil, err := a.TanganiCallback(context.Background(), va.PermintaanCallback{}, total, 0, 0)
		if err != nil {
			t.Fatalf("toggle mati tidak boleh menghasilkan galat: %v", err)
		}
		if hasil.Kode != va.KodeDiabaikan {
			t.Fatalf("toggle mati: ingin KodeDiabaikan, dapat %q", hasil.Kode)
		}
		if hasil.Kunci != "" || hasil.Peristiwa.Nominal != 0 {
			t.Fatal("toggle mati menyentuh keadaan")
		}
	})

	t.Run("adapter_provider_kosong_gagal_terang_terangan", func(t *testing.T) {
		a := &va.Adapter{Penjaga: va.NewMemoriIdempotensi(), Terpasang: true}
		hasil, err := a.TanganiCallback(context.Background(), va.PermintaanCallback{}, total, 0, 0)
		if !errors.Is(err, va.ErrProviderBelumDipilih) {
			t.Fatalf("provider kosong: ingin ErrProviderBelumDipilih, dapat %v", err)
		}
		if hasil.Kode != va.KodeDitolak {
			t.Fatalf("provider kosong: ingin KodeDitolak, dapat %q", hasil.Kode)
		}
	})

	t.Run("adapter_duplikat_dan_pencocokan_nominal", func(t *testing.T) {
		h := pabrik(t)
		ctx := context.Background()
		waktu := time.Date(2026, 10, 9, 3, 30, 0, 0, time.UTC)
		hasilVA, err := h.DaftarVA(ctx, va.PermintaanVA{ReffID: reffID, Nominal: total})
		if err != nil {
			t.Fatalf("DaftarVA: %v", err)
		}
		a := &va.Adapter{Provider: h, Penjaga: va.NewMemoriIdempotensi(), Terpasang: true}
		bahan := va.MockCallbackInput{NomorVA: hasilVA.NomorVA, EventID: "EV-9", Nominal: total, WaktuBayar: waktu, JamServer: waktu}
		cb := h.CallbackSah(bahan)

		pertama, err := a.TanganiCallback(ctx, cb, total, 0, 0)
		if err != nil {
			t.Fatalf("callback pertama: %v", err)
		}
		if pertama.Kode != va.KodeDiterima {
			t.Fatalf("callback pertama: ingin KodeDiterima, dapat %q (%s)", pertama.Kode, pertama.Nominal)
		}
		if pertama.Nominal != va.NominalCocok || pertama.Selisih != 0 {
			t.Fatalf("pencocokan nominal salah: %q selisih %d", pertama.Nominal, pertama.Selisih)
		}

		ulang, err := a.TanganiCallback(ctx, cb, total, 0, 0)
		if err != nil {
			t.Fatalf("callback ulang: %v", err)
		}
		if ulang.Kode != va.KodeDuplikat {
			t.Fatalf("callback ulang: ingin KodeDuplikat, dapat %q", ulang.Kode)
		}

		kurang := h.CallbackSah(va.MockCallbackInput{NomorVA: hasilVA.NomorVA, EventID: "EV-10", Nominal: total - 1, WaktuBayar: waktu, JamServer: waktu})
		hasilKurang, err := a.TanganiCallback(ctx, kurang, total, 0, 0)
		if err != nil {
			t.Fatalf("kurang bayar: %v", err)
		}
		if hasilKurang.Kode != va.KodePerluReview || hasilKurang.Nominal != va.NominalKurangBayar || hasilKurang.Selisih != -1 {
			t.Fatalf("kurang bayar: dapat kode %q nom %q selisih %d", hasilKurang.Kode, hasilKurang.Nominal, hasilKurang.Selisih)
		}

		lebih := h.CallbackSah(va.MockCallbackInput{NomorVA: hasilVA.NomorVA, EventID: "EV-11", Nominal: total + 5_000, WaktuBayar: waktu, JamServer: waktu})
		hasilLebih, err := a.TanganiCallback(ctx, lebih, total, 0, 0)
		if err != nil {
			t.Fatalf("lebih bayar: %v", err)
		}
		if hasilLebih.Kode != va.KodePerluReview || hasilLebih.Nominal != va.NominalLebihBayar || hasilLebih.Selisih != 5_000 {
			t.Fatalf("lebih bayar: dapat kode %q nom %q selisih %d", hasilLebih.Kode, hasilLebih.Nominal, hasilLebih.Selisih)
		}

		// Toleransi dihormati: kurang 5.000 dengan tol_kurang 5.000 = cocok.
		hasilToleransi, err := a.TanganiCallback(ctx, h.CallbackSah(va.MockCallbackInput{NomorVA: hasilVA.NomorVA, EventID: "EV-12", Nominal: total - 5_000, WaktuBayar: waktu, JamServer: waktu}), total, 5_000, 0)
		if err != nil {
			t.Fatalf("toleransi: %v", err)
		}
		if hasilToleransi.Kode != va.KodeDiterima || hasilToleransi.Nominal != va.NominalCocok {
			t.Fatalf("toleransi kurang: dapat kode %q nom %q", hasilToleransi.Kode, hasilToleransi.Nominal)
		}
	})
}

// rahasiaUji harus sama dengan yang dipakai pabrik;
// `PabrikMock` mengekspornya supaya kedua sisi konsisten.
const rahasiaUji = "rahasia-uji-kontrak-va-0123456789"

// PabrikMock membuat `va.MockProvider` yang memenuhi Harness, memakai rahasia
// uji yang sama dengan `tandaTanganUji`.
func PabrikMock(t *testing.T) Harness {
	t.Helper()
	p, err := va.NewMockProvider(va.MockConfig{Rahasia: []byte(rahasiaUji), Bank: va.BankBCA})
	if err != nil {
		t.Fatalf("NewMockProvider: %v", err)
	}
	return p
}

// providerPanik sengaja meledak bila dipanggil; dipakai membuktikan saklar
// fitur benar-benar mematikan jalur provider.
type providerPanik struct{}

func (providerPanik) Nama() string { return "panik" }

func (providerPanik) DaftarVA(context.Context, va.PermintaanVA) (va.HasilVA, error) {
	panic("provider dipanggil saat fitur mati")
}

func (providerPanik) TerimaCallback(context.Context, va.PermintaanCallback) (va.PeristiwaBayar, error) {
	panic("provider dipanggil saat fitur mati")
}
