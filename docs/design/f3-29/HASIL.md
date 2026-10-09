# F3-29 — Kajian & kontrak Virtual Account: keputusan desain dan bukti

Kartu: **t_5306bebf — F3-29 Kajian dan kontrak integrasi Virtual Account**
Repo: `/home/hermes/projects/grahita-website` (commit `main` setelah kartu ini)
Tanggal: 9 Oktober 2026

Dokumen acuan: `docs/PRD.md` §5, §8 no.10, §10; `docs/Architecture.md` §11;
`docs/SystemDesign.md` §4.3, §6; `docs/Database.md` §3, §4.5.

Dokumen hasil utama: **`docs/design/f3-29/KONTRAK-VA.md`** (studi opsi
BCA/BRI host-to-host vs penyedia, kontrak provider/callback, autentikasi
tanda tangan, idempotensi, model data usulan, kebutuhan kredensial/sandbox,
checklist provisioning, human gate K-01..K-10, rekomendasi biaya/risiko).
Halaman ini mencatat **keputusan desain** dan **bukti verifikasi** (perintah +
hasil nyata) — bukan ringkasan kode.

**Batas yang dipegang:** VA belum aktif, bank/penyedia belum dipilih, tidak ada
callback nyata yang diklaim. Verifikasi manual + Cash & Carry tidak berubah.
Tidak ada provider yang di-hardcode dan tidak ada satu pun nomor VA palsu yang
diterbitkan di sistem berjalan.

## Lingkup yang dikerjakan

1. **Kajian & keputusan tertunda** — `docs/design/f3-29/KONTRAK-VA.md`:
   perbandingan opsi VA (host-to-host BCA/BRI vs penyedia VA) dengan angka
   biaya yang dikutip beserta sumbernya, risiko, rekomendasi, kontrak
   provider/callback, model data usulan (belum dimigrasikan), kebutuhan
   kredensial & sandbox, checklist provisioning, dan gerbang manusia
   K-01..K-10.
2. **Prototipe adapter terkontrak-uji** — paket Go baru `internal/payment/va`
   (kontrak + tiruan deterministik) dan `internal/payment/va/vatest` (uji
   kontrak yang dapat dijalankan ulang terhadap adapter sungguhan).

Perubahan berkas (semuanya **berkas baru**, tidak ada berkas lama yang diubah):

| Berkas | Isi |
|---|---|
| `docs/design/f3-29/KONTRAK-VA.md` | kajian, kontrak provider/callback, pemetaan ke domain, model data usulan, kredensial/sandbox, provisioning, human gate, kewajiban kartu turunan |
| `docs/design/f3-29/HASIL.md` | halaman ini |
| `api/internal/payment/va/va.go` | kontrak `Provider`, tipe `PermintaanVA`/`HasilVA`/`PermintaanCallback`/`PeristiwaBayar`, sentinel galat, `CocokNominal`, `Kunci()`, `PenyimpanIdempotensi` + `MemoriIdempotensi`, `Adapter.TanganiCallback` |
| `api/internal/payment/va/signature.go` | jalur tanda tangan HMAC-SHA256: `Kanonik`, `TandaTangan`, `VerifikasiTandaTangan` (waktu-konstan), `VerifikasiKanonik`, `PeriksaUmur` (jendela replay) |
| `api/internal/payment/va/signature_rsa.go` | jalur asimetris SNAP BI: `KanonikSNAP`, `KanonikSNAPToken`, `TandaTanganRSASHA256`, `VerifikasiRSASHA256`, `KunciPublikDariPEM`, `KunciPrivatDariPEM` |
| `api/internal/payment/va/mock.go` | `MockProvider` deterministik + `BuatCallback`/`CallbackBadanMenandatangani` (perannya sebagai "bank" tiruan), `NominalDariString` |
| `api/internal/payment/va/va_test.go` | 10 uji unit kontrak (kanonik, tanda tangan, jendela waktu, kunci idempotensi, pencocokan nominal, penjaga memori, kebocoran rahasia) |
| `api/internal/payment/va/signature_rsa_test.go` | 3 uji unit jalur RSA/PEM (kunci dibangkitkan di uji, tanpa jaringan) |
| `api/internal/payment/va/vatest/contract.go` | `Harness` + `Uji(t, pabrik)` — 12 sub-uji kontrak yang berlaku untuk provider mana pun |
| `api/internal/payment/va/vatest/contract_test.go` | menjalankan seluruh kontrak terhadap `va.MockProvider` |

## Keputusan desain

1. **Kontrak dulu, provider kemudian.** Yang dikunci di kartu ini adalah
   *bentuk* (`Provider`, `PermintaanVA`, `PeristiwaBayar`, sentinel galat,
   skema tanda tangan, aturan idempotensi) — bukan nama vendor. Nama
   provider masuk lewat registry (`Provider.Nama()`) dan setelan
   `va.provider`, sehingga F3-30 tidak perlu menyentuh domain pembayaran.
2. **`TerimaCallback` murni (tanpa efek samping).** Provider hanya
   memverifikasi tanda tangan + bentuk payload dan mengembalikan peristiwa
   pembayaran. Efek samping (klaim idempotensi, pencocokan, audit, transisi)
   dikerjakan pemanggil di dalam transaksi basis data. Ini membuat kontrak
   dapat diuji tanpa PostgreSQL **dan** tanpa bank, sekaligus mencegah
   "provider" menjadi tempat kedua yang menulis keadaan pesanan.
3. **`MarkVerified` tetap satu-satunya pintu transisi Done** (kewajiban
   F1-18/F2-26 yang diteruskan). Kartu ini **tidak** menyentuh
   `internal/payment`; usulan VA hanya menghasilkan nilai
   `cocok`/`kurang_bayar`/`lebih_bayar` yang dipetakan ke `perlu_review` bila
   tidak cocok. Enum `verify_method` sudah memuat `virtual_account`
   (Database.md §3) sehingga tidak perlu migrasi enum untuk alur ini.
4. **Dua keluarga tanda tangan, satu tempat.** HMAC-SHA256 simetris
   (`signature.go`, kanonik `METHOD:PATH:sha256hex(BODY):TIMESTAMP`) dan
   SHA256withRSA SNAP BI (`signature_rsa.go`, kanonik memuat access token)
   berada di satu paket dengan perbandingan waktu-konstan dan pemetaan galat
   seragam, supaya F3-30 hanya memetakan nama header/kunci, bukan menulis
   ulang kriptografi. Bentuk final tetap bergantung dokumen resmi bank (K-02)
   — dan itu dicatat sebagai keputusan tertunda, bukan diasumsikan.
5. **Yang ditandatangani adalah sidik badan, bukan badan.** Perbedaan
   normalisasi JSON antar-pihak tidak boleh mematahkan verifikasi, tetapi
   setiap perubahan isi harus terdeteksi. Diuji: mengubah satu byte nominal
   pada bodi yang sudah ditandatangani → `ErrTandaTanganTidakSah`.
6. **Stempel waktu ikut ditandatangani** → mengubahnya mematahkan tanda
   tangan; stempel waktu yang memang rusak (tetapi ditandatangani sah)
   dijawab `ErrCallbackKedaluwarsa`. Jendela bawaan 15 menit + toleransi
   jam 5 menit (keduanya dapat dikonfigurasi) adalah lapis pertama anti-replay.
7. **Tiga lapis idempotensi.** (1) jendela waktu; (2) kunci peristiwa
   (`va:<id peristiwa provider>`, bila provider tidak memberi id peristiwa
   kuncinya diturunkan dari bank+nomor VA+waktu bayar+nominal — diuji stabil
   dan sensitif); (3) klaim atomik di basis data (F3-30 memakai unique index
   pada tabel callback; prototipe memakai `MemoriIdempotensi` yang sengaja
   hanya untuk uji).
8. **Urutan pemrosesan callback di `Adapter`.** Tanda tangan/jendela **lebih
   dahulu** (callback palsu tidak boleh "membakar" kunci peristiwa yang sah),
   pencocokan nominal di tengah, klaim idempotensi **paling akhir**. Hasilnya:
   `KodeDiterima`, `KodePerluReview`, `KodeDuplikat`, `KodeDitolak`, atau
   `KodeDiabaikan` (fitur mati) — kosakata yang siap dipetakan ke balasan bank.
9. **Fitur mati benar-benar mati.** `Adapter.Terpasang=false` (bawaan nol)
   mengembalikan `KodeDiabaikan` **tanpa memanggil provider sama sekali** —
   dibuktikan dengan provider yang panik bila dipanggil (uji
   `adapter_toggle_mati_diabaikan`). Paket ini juga tidak diimpor ke mana pun
   (lihat bukti "tidak tersambung").
10. **Toleransi uang satu sumber.** `CocokNominal` memakai
    `tol_kurang`/`tol_lebih` yang sama dengan verifikasi AI (PRD §4.9)
    sehingga dua mode pembayaran tidak dapat menyimpang; toleransi negatif
    diperlakukan `0` (fail closed).
11. **Model data dipisahkan dari bukti manual dan tidak dimigrasikan di kartu
    ini.** Usulan `va_accounts` (satu baris per penerbitan VA, `unique
    (reff_id)`) dan `va_callbacks` (satu baris per callback masuk, `unique
    (provider, kunci)`) ada di KONTRAK-VA.md §7; migrasi menjadi kewajiban
    F3-30 supaya tidak ada tabel mati di produksi.
12. **Biaya & risiko dinyatakan dengan sumber, bukan karangan.** Angka VA
    penyedia (±Rp4.000/transaksi di luar PPN 11%, settlement H+2, tanpa biaya
    setup/bulanan) dikutip dari halaman harga Midtrans/Xendit dan disandingkan
    dengan volume acuan (±480 pesanan/bulan → ≈Rp2,1 juta/bulan, batas atas)
    serta biaya infrastruktur saat ini (Rp0,5–1,1 juta/bulan). Untuk
    host-to-host bank langsung **tidak ada angka yang dikarang** — ada
    pernyataan tegas bahwa penawaran tertulis diperlukan (K-08).
13. **Gerbang manusia ditulis sebagai daftar keputusan, bukan catatan
    samping.** K-01..K-10 (provider & urutan, skema tanda tangan final, bentuk
    VA closed/open, kedaluwarsa VA, penanganan selisih, nasib unggah bukti &
    C&C, retensi PII, biaya/kontrak, override manual, pemilik rekonsiliasi)
    semuanya berstatus BELUM DIPUTUSKAN dan menjadi syarat sebelum
    implementasi hidup.
14. **Kewajiban kartu turunan ditulis eksplisit** (KONTRAK-VA.md §10):
    F3-30 mengimplementasikan provider yang dipilih + migrasi + rute callback
    + toggle dan **memblokir** bila kredensial/sandbox belum ada; T8
    menjalankan `vatest.Uji` pada sandbox nyata dan membandingkannya dengan
    `va.MockProvider`; F3-31 memakai `va_callbacks`/`va_accounts` sebagai
    sumber rekonsiliasi.

## Bukti verifikasi (dijalankan di repo target)

Perkakas: `go1.27.1` (`/home/hermes/toolchain/go/bin/go`). Tidak ada PostgreSQL
yang dipakai — paket ini murni (tanpa DB, tanpa jaringan).

```
$ cd api && gofmt -l .                 # kosong
$ go vet ./...                         # VET_OK (exit 0)
$ go build ./...                       # BUILD_OK (exit 0)
$ go test -race -count=1 -timeout 10m ./internal/payment/...
ok    github.com/grahita/portal/api/internal/payment         2.942s
?     github.com/grahita/portal/api/internal/payment/postgres [no test files]
ok    github.com/grahita/portal/api/internal/payment/va      1.837s
ok    github.com/grahita/portal/api/internal/payment/va/vatest 1.099s
```

Uji fokus (hitungan nyata): **14 uji PASS tingkat atas** (13 unit di paket `va`
+ 1 suite kontrak di `vatest`) dengan **12 sub-uji** di dalam suite kontrak ⇒
**26 baris PASS, 0 FAIL**.

Sub-uji kontrak (`vatest.Uji`, berlaku untuk provider mana pun):

| Sub-uji | Membuktikan |
|---|---|
| `daftar_VA_idempoten_dan_unik` | `ReffID` sama → nomor VA sama (idempotensi); `ReffID` berbeda → nomor berbeda; `Raw` JSON sah untuk audit |
| `daftar_VA_menolak_masukan_tidak_sah` | `ReffID` kosong / nominal ≤ 0 → `ErrPayloadTidakSah` (tidak ada VA "nominal 0") |
| `callback_sah_mengembalikan_peristiwa` | nominal, nomor VA, id peristiwa, waktu bayar terbaca; `Kunci()` stabil |
| `callback_tanpa_dan_bermasalah_tanda_tangan_ditolak` | tanpa header → ditolak; tanda tangan salah → ditolak; **isi diubah sesudah ditandatangani** → ditolak |
| `callback_kedaluwarsa_atau_dari_masa_depan_ditolak` | lebih tua dari jendela → `ErrCallbackKedaluwarsa`; dari masa depan di luar toleransi → ditolak; tepat di dalam jendela → diterima |
| `callback_stempel_waktu_diubah_atau_rusak_ditolak` | stempel waktu diubah tanpa menandatangani ulang → tanda tangan patah; stempel waktu rusak tetapi ditandatangani sah → `ErrCallbackKedaluwarsa` |
| `callback_nomor_VA_tidak_dikenal_ditolak` | nomor asing → `ErrNomorVATidakDikenal` |
| `callback_payload_cacat_ditolak` | JSON rusak, `nomor_va` kosong, nominal 0/negatif, waktu bukan RFC3339, bodi kosong → ditolak; **bidang tambahan justru diterima** (kontrak tidak terlalu ketat) |
| `callback_murni_tidak_menulis_ke_luar` | callback identik dua kali → hasil setara (dedupe bukan urusan provider) |
| `adapter_toggle_mati_diabaikan` | toggle mati → `KodeDiabaikan`, provider yang panik **tidak dipanggil**, tidak ada keadaan tersentuh |
| `adapter_provider_kosong_gagal_terang_terangan` | provider nil + toggle hidup → `ErrProviderBelumDipilih` + `KodeDitolak` |
| `adapter_duplikat_dan_pencocokan_nominal` | pertama `KodeDiterima`; ulang `KodeDuplikat`; kurang/lebih → `KodePerluReview` + alasan yang benar + selisih; toleransi `tol_kurang` dihormati |

Uji unit paket `va` (13): `TestKanonikBentukDanSidikBadan`,
`TestVerifikasiTandaTanganMenolakYangTidakCocok` (termasuk rahasia `nil` →
ditolak, jadi tidak ada jalur "kunci kosong = lolos"),
`TestVerifikasiKanonikMemerlukanHeaderLengkap`, `TestPeriksaUmur` (8 kasus batas
+ jendela bawaan), `TestKunciPeristiwaStabilDanSensitif` (sensitif terhadap
nominal/nomor VA/waktu, tidak sensitif terhadap offset zona waktu),
`TestCocokNominal` (9 kasus batas + toleransi negatif), `TestMemoriIdempotensiKlaimSekali`,
`TestMockProviderMenolakTanpaRahasia`, `TestMockProviderTidakMembocorkanRahasia`,
`TestNominalDariString`, `TestKanonikSNAPBentuk`,
`TestTandaTanganRSASHA256BolakBalik` (kunci RSA dibangkitkan di uji; kunci lain,
base64 rusak, tanda tangan terpotong, kunci `nil` → `ErrTandaTanganTidakSah`),
`TestKunciPEMBolakBalik`.

**Bukti "tidak tersambung" (fitur mati, tanpa pembayaran palsu):**

```
$ grep -rn "payment/va" --include="*.go" api | grep -v "^api/internal/payment/va/"
(kosong, exit 1)
```

Tidak ada satu berkas pun di luar paket itu yang mengimpor VA: `cmd/server`,
router, worker, dan domain `payment` tidak tersentuh. Tidak ada rute, tidak ada
migrasi, tidak ada setelan baru di kartu ini.

**Bukti "tidak ada klaim kontrak bank":** seluruh uji berjalan tanpa jaringan;
RSA diuji dengan kunci yang dibangkitkan di uji; angka biaya/tanda tangan di
KONTRAK-VA.md §11 dikutip dari sumber publik dengan peringatan verifikasi ulang
(K-01/K-02/K-08).

## Batas bukti & risiko tersisa (non-pemblokir)

- **Tidak ada verifikasi terhadap sandbox bank nyata** (tidak ada akses bank,
  dan memang bukan lingkup kartu ini — itu T8 dengan syarat eksternal; bila
  akses belum ada, T8 memblokir kartu). Karena itu klaim "callback bank
  berhasil" **tidak** dinyatakan di mana pun.
- **Bentuk tanda tangan final belum dikunci** (K-02). Yang teruji adalah
  bentuk yang dipakai standar publik (HMAC simetris + SNAP `SHA256withRSA`);
  perbedaan detail header/kanonik provider nyata akan muncul saat F3-30
  memetakannya, dan uji kontrak ini akan langsung menunjukkan bila salah.
- **`MemoriIdempotensi` bukan untuk produksi** (keadaan hilang saat restart).
  Klaim atomik produksi (unique index + transaksi) adalah kewajiban F3-30 dan
  belum diuji — tidak ada PostgreSQL yang dipakai di kartu ini.
- **Angka biaya bersifat indikatif** dari halaman harga publik (Mei 2026) dan
  belum termasuk PPN/penawaran resmi; rekomendasi §2.4 adalah saran, bukan
  keputusan — biaya harus disetujui user (K-08).
- **Bentuk VA (closed per pesanan vs open per cabang) belum diputuskan** (K-03)
  sehingga usulan `ReffID = order:<id>:v<versi>` dan aturan revisi (§6.4) masih
  menunggu persetujuan; model data §7 mengikuti bentuk closed.
- **Kebijakan retensi PII pembayar** (K-07) belum diputuskan; `raw` disensor
  sebelum disimpan, tetapi apa yang boleh ditampilkan ke cabang belum final.
- **`golangci-lint` tidak tersedia di sesi ini** (sama seperti kartu lain);
  gerbang yang dijalankan: `gofmt`, `go vet`, `go build`, `go test -race`.

## Kewajiban untuk kartu turunan

Ringkas (lengkap di KONTRAK-VA.md §10):

- **F3-30 (t_fc796fba)** — implementasi provider terpilih + migrasi
  `va_accounts`/`va_callbacks` + rute `POST /api/v1/va/callback` (tanpa CSRF,
  tanda tangan wajib, rate limit tetap) + toggle `va.aktif` bawaan mati +
  transisi lewat `payment.MarkVerified` (`virtual_account`) + audit
  `va_payment`; **jalankan `vatest.Uji`** terhadap adapter baru; **blokir**
  kartu bila kredensial/sandbox/dokumen callback belum ada.
- **T8 (t_06195c87)** — jalankan `vatest.Uji` pada sandbox nyata, bandingkan
  dengan `va.MockProvider`, uji replay/duplikasi/nominal mismatch/race
  manual+VA, dan pastikan laporan tidak menggandakan (hanya SA).
- **F3-31** — rekonsiliasi memakai `va_callbacks`/`va_accounts` sebagai
  sumber, memisahkan biaya transaksi penyedia dari total pesanan.
