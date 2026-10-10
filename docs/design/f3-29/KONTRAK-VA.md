# F3-29 — Kontrak integrasi Virtual Account (kajian, keputusan tertunda, dan prototipe adapter)

Kartu: **t_5306bebf — F3-29 Kajian dan kontrak integrasi Virtual Account**
Tanggal: 9 Oktober 2026 · Status: **KAJIAN — TIDAK ADA PROVIDER AKTIF**

Dokumen acuan: `docs/PRD.md` §5 (tiga mode verifikasi, kolom *Virtual Account* =
Fase 3), §8 no.10 (bank mana & host-to-host vs gateway), §10 (Fase 3 =
"Virtual Account BCA (lalu BRI), rekonsiliasi otomatis, laporan pembayaran");
`docs/Architecture.md` §11 (risiko & pola interface provider);
`docs/SystemDesign.md` §4.3 (alur verifikasi + satu pintu `MarkVerified`), §6
(permukaan API); `docs/Database.md` §3 (`verify_method`, `bank_code`), §4.5
(`payment_proofs`, `verification_logs`).

**Keputusan user yang mengikat dokumen ini:** saat ini **tidak ada** payment
gateway / Virtual Account. VA **direncanakan** (bank/penyedia **belum
dipilih**). Karena itu kartu ini menghasilkan *kontrak* + *prototipe yang
terkontrak-uji*, **bukan** integrasi hidup. Verifikasi manual dan Cash & Carry
**tidak berubah**; tidak ada satu pun nomor VA palsu yang diterbitkan di sistem
berjalan, dan tidak ada klaim bahwa callback bank nyata sudah teruji (belum ada
akses bank).

Rekan dokumen: `docs/design/f3-29/HASIL.md` (keputusan desain + bukti
verifikasi perintah/hasil nyata).

**Pembaruan 10 Oktober 2026 (kartu t_c7a417e2 — F3-29b).** Jawaban pengguna
terbaru atas K-01..K-10 sudah diturunkan menjadi kebijakan, state machine,
tabel keputusan callback, batas antarmuka dua adapter bank, dan matriks uji di
`docs/design/f3-29/KEPUTUSAN-VA-BCA-BRI.md`. Dokumen itu **menggantikan**
usulan/rekomendasi di halaman ini bila keduanya bentrok — khususnya §2.4
butir 2 (piloting lewat penyedia/aggregator **dibatalkan** oleh K-01) dan
status "BELUM DIPUTUSKAN" di §9. Riwayat di halaman ini **tidak dihapus**:
setiap butir yang bergeser diberi penanda pembaruan di tempatnya, dan §9 kini
memuat status per keputusan (final vs menunggu).

**Putaran perbaikan 10 Oktober 2026 (hasil tinjauan QA atas F3-29b).** §5 (tabel
lapis, alur balasan, dan §5.1) diberi **presedensi lapis** dan penulisan **efek
per kasus**: jendela replay dinilai **lebih dahulu** daripada `waktu_bayar`,
kedatangan telat (V-06) = tolak tanpa efek + daftar rekonsiliasi, bayar setelah
kedaluwarsa / VA lama / pesanan sudah Done = `perlu_review`, duplikat = ack tanpa
efek — tidak ada lagi satu ember "audit + `perlu_review`". §6.4 sudah diberi
penanda pembaruan. Matriks uji acuan di `KEPUTUSAN-VA-BCA-BRI.md` §10 kini
**T-VA-01..T-VA-32**.

**Putaran perbaikan lanjutan 10 Oktober 2026 (temuan QA putaran 3).** Prinsip
**efek per kasus, jangan disatukan** di atas kini diterapkan juga di **§9.1
(baris K-04)** dan **§10 (baris F3-30)** — dua tempat yang sebelumnya masih
menaruh `duplikat` di dalam ember "audit + `perlu_review`". Aturan yang berlaku
di seluruh halaman ini: **duplikat** (V-04, retry normal provider) = ack
"sudah diproses", **tanpa efek**; **`perlu_review`** hanya untuk bayar setelah
kedaluwarsa (V-10/V-11), VA lama/versi lama (V-09/V-10), dan pesanan sudah Done
(V-12/V-13); **kedatangan telat** (V-06) = tolak tanpa efek + daftar
rekonsiliasi. Tidak ada lagi satu tempat pun di halaman ini yang menyatukan
ketiga efek berbeda itu.

---

## 1. Batas lingkup & prinsip

| Prinsip | Konsekuensi nyata di kartu ini |
|---|---|
| **Jangan hardcode provider** | Nama bank/penyedia tidak muncul di kode domain. Semua lewat `va.Provider` (registry: `Nama()`), konfigurasi `va.provider`, dan tabel referensi (F3-30). Prototipe hanya memuat provider tiruan deterministik. |
| **Jangan mengklaim callback nyata tanpa akses bank** | Semua uji di kartu ini berjalan tanpa jaringan. Bentuk payload/tanda tangan bank dipisahkan tegas menjadi *usulan kontrak* (§3–§5) dan *keputusan tertunda* (§9) yang menunggu dokumen resmi bank. Tidak ada berkas bukti yang menyatakan "callback BCA/BRI berhasil". |
| **Fitur mati secara bawaan** | Packaging `/api/v1/va/...` tidak pernah ditambahkan ke router di kartu ini; `va.Adapter.Terpasang` bawaan `false` dan diuji "provider tidak dipanggil sama sekali" saat mati. Setelan `va.aktif` (bawaan `false`) baru ditambahkan F3-30. **Pembaruan F3-29b:** toggle master `va.aktif` diputuskan **tetap `false`** dan ditambah usulan toggle **per bank** — lihat `KEPUTUSAN-VA-BCA-BRI.md` §1 dan §2.1. |
| **Satu pintu transisi Done tidak dilanggar** | VA hanya **mengusulkan** pembayaran; transisi Done tetap `payment.MarkVerified` (metode `virtual_account`, sudah ada di enum `verify_method`). Kartu ini tidak menyentuh `payment` domain. |
| **Uang bulat, tanpa sen** | Nominal VA `int64` Rupiah bulat (BR-10); cocok/lebih/kurang memakai toleransi `ai.tol_kurang`/`ai.tol_lebih` yang sudah ada supaya VA dan OCR tidak menyimpang. |
| **Idempotensi dan replay dijaga berlapis** | Kunci peristiwa provider + klaim atomik di basis data (F3-30) + jendela waktu (prototipe). Callback yang diulang provider **tidak pernah** menghasilkan dua pembayaran. |

---

## 2. Studi opsi (BCA/BRI host-to-host vs penyedia VA)

### 2.1 Opsi yang dibandingkan

| Opsi | Bagaimana ia bekerja | Yang perlu dimiliki Grahita |
|---|---|---|
| **A. Host-to-host langsung BCA** | Grahita memakai API mitra BCA (VA/inquiry/payment) langsung; nomor VA terbit dari sisi BCA, callback masuk ke endpoint kita | Perjanjian mitra/korporat dengan BCA, nomor perusahaan VA, kredensial mitra + pertukaran kunci, IP statis terdaftar, kemampuan menangani sertifikasi/kontrak API |
| **B. Host-to-host langsung BRI (BRIVA)** | Setara A, dengan BRIVA/API mitra BRI | Sama seperti A, ditambah proses onboarding BRI |
| **C. Penyedia VA / payment aggregator** | Satu kontrak + satu API ke penyedia (mis. Midtrans, Xendit, DOKU, Finpay, Flip — *contoh, bukan pilihan*); penyedia meneruskan ke banyak bank (BCA, BRI, BNI, Mandiri, …) | Perjanjian merchant dengan penyedia, akun settlement, callback HTTPS kita |

### 2.2 Perbandingan kriteria

| Kriteria | A/B. Host-to-host bank langsung | C. Penyedia VA |
|---|---|---|
| Biaya per transaksi | Umumnya **dinegosiasikan** dengan bank, bergantung profil rekening korporat dan volume. **Belum ada angka resmi untuk Grahita — wajib penawaran tertulis** | Publik & seragam: **±Rp4.000 flat per transaksi VA** (Midtrans & Xendit, tidak termasuk PPN 11%, Mei 2026) |
| Biaya setup/bulanan | Bergantung kontrak (dapat ada biaya implementasi/sertifikasi) | Umumnya Rp0 setup & Rp0 bulanan; hanya per transaksi berhasil |
| Settlement | Langsung ke rekening korporat Grahita di bank tersebut | Umumnya **H+2** ke rekening settlement penyedia |
| Cakupan bank | Satu bank per kontrak (BCA **atau** BRI) | Banyak bank dalam satu kontrak |
| Waktu integrasi | Paling berat (kredensial mitra, sertifikasi, kunci asimetris, kemungkinan uji terbatas) | Paling ringan; sandbox publik & dokumentasi matang |
| Rekonsiliasi | File/statement bank yang harus diolah sendiri | Laporan/API penyedia (dana masuk + biaya per transaksi) |
| Ketergantungan | Terikat proses & jadwal bank | Terikat penyedia (downtime penyedia = pembayaran terhenti) |
| Risiko kepatuhan | Kontrak korporat penuh | Data transaksi melewati pihak ketiga (PII pada payload callback, §7.6) |

### 2.3 Angka biaya indikatif untuk skala Grahita

Volume acuan (`Database.md` §9): ±16 cabang × ±30 pesanan/bulan ≈ **±480 pesanan/bulan**.
Bila seluruhnya dibayar lewat VA:

- Opsi **C** (aggregator, Rp4.000 + PPN 11%): 480 × Rp4.000 = **Rp1.920.000**, dengan PPN ≈ **Rp2.131.200/bulan** (≈ Rp25,6 juta/tahun). Ini adalah **batas atas** (sebagian pesanan C&C/berbukti manual tidak memakai VA).
- Opsi **A/B**: **tidak dapat diestimasi jujur** tanpa penawaran bank. Yang dapat dikatakan: harga bank langsung umumnya lebih murah per transaksi pada volume besar, tetapi punya biaya tetap/kontrak dan biaya implementasi yang harus dihitung terpisah.

Konteks penting: total biaya infrastruktur sistem saat ini diperkirakan
**Rp500.000–1.100.000/bulan** (`Architecture.md` §10). Artinya VA lewat
aggregator pada volume penuh akan menjadi **komponen biaya operasional
terbesar** sistem. Keputusan VA karena itu bukan keputusan teknis semata, ia
keputusan bisnis (menghemat kerja verifikasi manual median `< 4 jam kerja`,
PRD §1.1).

### 2.4 Rekomendasi (memerlukan persetujuan user — bukan keputusan teknis)

1. **Tetap manual sekarang** (selaras keputusan user). Tidak ada provider yang
   dipilih, tidak ada biaya, tidak ada permukaan serang baru.
2. Bila VA jadi diprioritaskan: **piloting lewat penyedia (opsi C) untuk satu
   bank (BCA)**, karena waktu integrasi & sandbox paling ringan, satu kontrak,
   settlement jelas, dan biaya per transaksi dapat dihitung pasti; gunakan
   `MockProvider` + kontrak ini agar peralihan ke host-to-host bank langsung
   tidak mengubah domain. **Pembaruan F3-29b: butir ini DIBATALKAN oleh K-01** —
   aggregator/penyedia bukan target; arsitektur yang berlaku adalah dua adapter
   host-to-host langsung (BCA dan BRI) sesuai
   `KEPUTUSAN-VA-BCA-BRI.md` §1/§2. Riwayat usulan lama sengaja dibiarkan
   terbaca.
3. Evaluasi ulang host-to-host bank langsung (opsi A/B) bila salah satu
   kondisi terpenuhi: volume VA melewati ±2.000/bulan, biaya per transaksi
   menjadi material terhadap margin, atau bank menawarkan biaya signifikan
   lebih rendah; atau bila ada larangan kontrak untuk memakai agregator.
4. Jangan mengaktifkan VA untuk pesanan C&C tanpa keputusan eksplisit
   (K-06b) — pengingat C&C dan jatuh tempo adalah alur yang sudah hidup.

### 2.5 Risiko yang harus diterima bila VA diaktifkan

| Risiko | Mitigasi yang sudah disiapkan kontrak |
|---|---|
| **Dua jalur pembayaran bersamaan** (bukti manual + VA) → risiko dobel catat | Klaim idempotensi per peristiwa + satu pintu `MarkVerified`; pesanan Done menolak pembayaran kedua (§5.4) |
| Callback diulang provider (at-least-once) | Kunci peristiwa + klaim atomik di basis data (§5) |
| Replay/callback palsu dari internet | Tanda tangan wajib + jendela waktu + penolakan tanpa efek samping (§4, §5) |
| Pembayaran tidak pas (kurang/lebih) atau VA tidak dikenal | Keputusan jatuh ke **perlu review** dengan alasan yang sudah ada; tidak pernah diam-diam Done (§6) |
| Nomor VA dibayar setelah kedaluwarsa / setelah pesanan dibatalkan | Kebijakan kedaluwarsa & pembayaran terlambat → keputusan tertunda K-04. **Pembaruan F3-29b: K-04 kini FINAL (kebijakan)** — bayar setelah kedaluwarsa = **perlu review**, tidak Done (`KEPUTUSAN-VA-BCA-BRI.md` §4.2 V-10/V-11); yang masih menunggu hanya kemampuan cancel/generate di sisi bank |
| Downtime penyedia/bank | Verifikasi manual tetap tersedia (VA tidak menggantikan jalur manual) |
| Biaya tersembunyi (PPN, settlement H+2, biaya per-transaksi gagal) | K-08: minta penawaran tertulis sebelum tanda tangan; hitung PPN |
| Data pribadi pada payload callback (nama pembayar) | Kebijakan penyimpanan & retensi K-07; `Raw` disensor sebelum disimpan |

---

## 3. Kontrak provider (yang harus dipenuhi adapter mana pun)

Kontrak ini diimplementasikan di kode dan **terkontrak-uji** (tanpa bank):
`api/internal/payment/va` (prototipe + tiruan) dan
`api/internal/payment/va/vatest` (uji kontrak yang dapat dijalankan ulang
terhadap adapter sungguhan).

```go
type Provider interface {
    Nama() string
    DaftarVA(ctx context.Context, in PermintaanVA) (HasilVA, error)
    TerimaCallback(ctx context.Context, in PermintaanCallback) (PeristiwaBayar, error)
}
```

Aturan yang mengikat implementasi:

1. **`TerimaCallback` murni.** Ia hanya memverifikasi tanda tangan + bentuk
   payload dan mengembalikan peristiwa; ia **tidak menulis** ke basis data,
   pesanan, dokumen, atau outbox. Efek samping (klaim idempotensi, pencocokan,
   audit, transisi) adalah pekerjaan pemanggil. Kemurnian dijamin oleh **bentuk
   kontrak** (provider tidak memegang handle basis data/pesanan/outbox) dan
   diperiksa saat review; sub-uji kontrak `callback_murni_tidak_menulis_ke_luar`
   hanya membuktikan **idempotensi hasil** (callback identik dua kali → hasil
   setara) — ia **tidak** dan tidak dapat membuktikan ketiadaan efek samping,
   jadi klaim "tidak menulis" tidak boleh disandarkan pada sub-uji itu.
2. **`DaftarVA` idempoten terhadap `ReffID`.** `ReffID` kita: `order:<order_id>:v<versi>`.
   Versi ikut karena revisi invoice mengubah total → VA lama tidak boleh
   dipakai untuk total baru (§6.4).
3. **Gagal terang-terangan, bukan mengarang.** Provider yang tidak tersedia
   (`ErrProviderTidakTersedia`), tanda tangan salah
   (`ErrTandaTanganTidakSah`), payload cacat (`ErrPayloadTidakSah`), di luar
   jendela waktu (`ErrCallbackKedaluwarsa`), nomor tidak dikenal
   (`ErrNomorVATidakDikenal`) — semuanya sentinel galat yang terpisah. Tidak ada
   jalur "kembalikan peristiwa kosong yang sukses".
4. **Kredensial tidak bocor.** `HasilVA.Raw`/`PeristiwaBayar.Raw` dipakai untuk
   audit dan diuji tidak memuat rahasia; domain pembayaran sudah memiliki
   `SanitizeRaw` (F2-26) untuk menyensor sebelum disimpan.
5. **Tidak ada keputusan final dari mesin.** VA mengusulkan; manusia selalu
   dapat menimpa (PRD §5).

### 3.1 Pemetaan kontrak → domain

| Kontrak VA | Domain yang sudah ada | Aturan |
|---|---|---|
| `PeristiwaBayar.Nominal` | `orders.total` | dibandingkan `CocokNominal` dengan toleransi `ai.tol_kurang`/`ai.tol_lebih` |
| hasil `cocok` | `payment.MarkVerified` (metode `virtual_account`, aktor sistem/admin sesuai K-09) | transisi Done + dokumen + email/timeline/audit seperti biasa |
| hasil `kurang_bayar`/`lebih_bayar` | `orders.verif_status='perlu_review'` + `review_reason` yang sudah ada | pesanan **tidak** menjadi Done |
| peristiwa pembayaran | `verification_logs` (`metode='virtual_account'`, `total_terbaca=nominal`, `admin_id` sesuai aktor) | satu baris per keputusan |
| `NomorVATidakDikenal` / pesanan bukan `submitted` | — | ditolak + audit; tidak ada perubahan keadaan |
| `HasilVA.Raw`, `PeristiwaBayar.Raw` | `audit_logs` (setelah `SanitizeRaw`) | `entity='va_payment'`, `action='callback'` |

---

## 4. Autentikasi & tanda tangan callback

Ada **dua keluarga skema** yang harus diantisipasi. Keduanya sudah ada di
prototipe agar F3-30 tidak mulai dari nol; **bentuk final wajib diambil dari
dokumen kontrak bank/penyedia** (K-02), bukan dari dokumen ini.

### 4.1 Jalur simetris (HMAC-SHA256)

Dipakai oleh banyak API VA generasi sebelumnya dan oleh tiruan kita:

```
stringToSign = METHOD + ":" + PATH + ":" + sha256hex(BODY) + ":" + TIMESTAMP
tanda        = hex(HMAC-SHA256(secret, stringToSign))
```

Poin desain: yang ditandatangani adalah **sidik** badan, bukan badan itu
sendiri — perbedaan normalisasi JSON (spasi, urutan kunci, escaping) antara kita
dan provider tidak mematahkan verifikasi, sementara setiap **perubahan isi**
tetap terdeteksi. Nama header (`X-VA-Signature` pada tiruan) adalah bagian yang
dipetakan adapter; stempel waktu `<header>-Timestamp` ikut ditandatangani
sehingga mengubahnya mematahkan tanda tangan.

### 4.2 Jalur asimetris SNAP BI (SHA256withRSA)

Standar yang dipakai mitra BCA/BRI saat ini (SNAP — Standar Nasional Open API
Pembayaran):

```
endpoint token : stringToSign = ClientKey + "|" + X-TIMESTAMP   →  SHA256withRSA(privateKey)
endpoint lain  : stringToSign = METHOD + ":" + PATH + ":" + AccessToken + ":" +
                               LowerCase(HexEncode(SHA-256(Minify(BODY)))) + ":" + X-TIMESTAMP
                 X-SIGNATURE  = Base64(SHA256withRSA(privateKey, stringToSign))
```

Header pendamping yang biasanya wajib pada SNAP: `X-PARTNER-ID`,
`X-EXTERNAL-ID` (idempotency key per permintaan), `CHANNEL-ID`,
`X-DEVICE-ID`, `Authorization: Bearer <access token>`. Perbedaan dari jalur
4.1: ada **access token per-transaksi** di dalam stringToSign dan tandanya
**asimetris** (privat kita untuk panggilan keluar, publik bank untuk
memverifikasi callback — atau sebaliknya, tergantung arah; ini K-02).

Referensi publik: `developers.bri.co.id/en/snap-bi/apidocs-oauth-snap-bi`
(struktur header & token `/snap/v1.0/access-token/b2b`) dan dokumentasi mitra
SNAP lain yang memuat bentuk stringToSign transaksi
(`docs-snap.paydia.id`, `docs.duitku.com/snap-api`, `docs.durianpay.id`).
Rujukan ini **bukan** kontrak Grahita-BCA/BRI.

### 4.3 Aturan yang wajib berlaku untuk skema apa pun

1. **Perbandingan waktu-konstan** (`crypto/subtle` untuk HMAC; `rsa.Verify*`
   untuk RSA). Diuji termasuk kunci/rahasia kosong → **ditolak** (tidak ada
   jalur "kosong = lolos"): HMAC kunci-kosong tetap deterministik, jadi
   `VerifikasiTandaTangan`/`VerifikasiKanonik` gagal-tertutup untuk
   `rahasia` `nil`/kosong (`TestVerifikasiTandaTanganMenolakRahasiaKosong`),
   sejalan dengan jalur RSA yang menolak kunci publik `nil`.
2. **Jendela waktu replay** (bawaan prototipe 15 menit, toleransi masa depan 5
   menit untuk selisih jam). Di luar jendela → `ErrCallbackKedaluwarsa`;
   pemanggil membalas ack **tanpa** efek samping.
3. **Tidak membocorkan sebab kegagalan** ke pengirim: pesan galat seragam.
4. **Callback URL** harus HTTPS dengan TLS valid, host khusus (Caddy), dan
   hanya menerima POST; tidak ada data pada query string.
5. **Rahasia/kunci tidak pernah masuk repo** — SOPS+age / environment
   (`Architecture.md` §6), sama seperti `ANTHROPIC_API_KEY` hari ini.

---

## 5. Idempotensi & replay (tiga lapis)

| Lapis | Isi | Status |
|---|---|---|
| 1. **Jendela waktu** | stempel waktu ikut ditandatangani, umur ≤ jendela — ini **kedatangan telat** (V-06), bukan "bayar setelah kedaluwarsa"; lapis ini dievaluasi **lebih dahulu** daripada penilaian `waktu_bayar` (§4.2 presedensi lapis, dok. F3-29b) | ada di prototipe (`PeriksaUmur`) |
| 2. **Kunci peristiwa** | `PeristiwaBayar.Kunci()` = `va:<id peristiwa provider>`; bila provider tidak memberi id peristiwa, kunci diturunkan dari (bank, nomor VA, waktu bayar, nominal) | ada di prototipe, diuji stabil & sensitif |
| 3. **Klaim atomik di basis data** | tabel callback dengan **kunci unik**; `INSERT ... ON CONFLICT DO NOTHING` di dalam transaksi → hanya pemanggil pertama yang boleh berefek | **wajib F3-30** (prototipe memakai peta memori `MemoriIdempotensi`) |

Alur balasan yang disepakati:

```
callback masuk
  ├─ fitur mati (va.aktif=false)          → abaikan, 0 efek (tidak pernah memanggil provider)
  ├─ tanda tangan/jendela/VA tidak sah    → tolak (audit), TIDAK mengklaim kunci
  ├─ peristiwa sah, kunci sudah diklaim   → balas ack "sudah diproses", 0 efek
  └─ peristiwa sah, kunci baru            → cocokkan nominal → usul Done / perlu review
```

Urutan ini disengaja: **verifikasi tanda tangan lebih dahulu** (callback palsu
tidak boleh "membakar" kunci peristiwa yang sah), **klaim paling akhir**
(percobaan yang gagal tidak menutup kesempatan percobaan sah berikutnya).
Bila fitur hidup tetapi penjaga idempotensi tidak dipasang, prototipe **menolak**
(`KodeDitolak` + `ErrPenjagaIdempotensiTidakAda`) — gagal tertutup, karena
callback duplikat yang dihitung dua kali adalah kerugian uang.

**Pembaruan F3-29b (K-04/K-05/K-09).** Cabang "peristiwa sah, kunci baru" di
atas tidak selalu berarti Done: hanya kasus **pasti** (VA aktif, versi terkini,
`waktu_bayar ≤ terbit + 24 jam`, nominal **persis**, bukan duplikat) yang
mengusulkan Done. Efek tiap kasus **berbeda dan tidak boleh disatukan**:

- **kedatangan telat** (callback tiba di luar `va.jendela_replay_menit`, apa pun
  `waktu_bayar`-nya — V-06) → **tolak tanpa efek ke pesanan**; kunci peristiwa
  **tidak** diklaim; audit + masuk **daftar kerja rekonsiliasi harian**;
- **bayar setelah kedaluwarsa** (`waktu_bayar > kedaluwarsa_pada`; disebut
  "terlambat" pada rumusan awal — V-10/V-11), **VA versi lama / kedaluwarsa /
  dibatalkan** (V-09/V-10), atau **pesanan sudah Done** (V-12/V-13)
  → **`perlu_review`** + audit (tidak ada auto-Done pada kasus tidak pasti);
- **duplikat** (kunci sudah diklaim — V-04) → balas **ack "sudah diproses",
  tanpa efek**.

**Presedensi lapis (§4.2 aturan lintas-baris dok. F3-29b).** Jendela replay
dievaluasi **lebih dahulu** daripada penilaian `waktu_bayar`, jadi callback yang
tiba telat **dan** sudah lewat kedaluwarsa berakhir sebagai **kedatangan telat
(V-06)** — bukan `perlu_review` — dan V-10/V-11 hanya berlaku untuk callback
yang tiba di dalam jendela. Pengecualian sempit (bila dokumen bank memakai
stempel waktu = waktu bayar sehingga kedua lapis berimpit): hasilnya
`perlu_review`, tetap bukan Done (§13 butir 6 dok. F3-29b). Verifikasi manual
admin tetap tersedia sebagai
pengecualian beralasan. Alur lengkap, definisi istilah waktu, diagram state VA,
dan tabel keputusan 15 baris: `KEPUTUSAN-VA-BCA-BRI.md` §4–§6.

### 5.1 Yang tetap menjadi kewajiban F3-30

1. Klaim kunci **dan** pencocokan pesanan dalam **satu transaksi** yang mengunci
   baris pesanan (`SELECT ... FOR UPDATE`, pola `SystemDesign.md` §4.2/§4.5).
2. **Pesanan sudah Done** → catat callback sebagai duplikat/perlu review **di
   `va_callbacks`** untuk rekonsiliasi, tetapi **efek ke pesanan = tanpa efek**:
   **jangan** pindahkan status; jangan buat dokumen/email kedua.
3. **Satu pesanan satu VA aktif**; VA untuk versi lama tidak boleh dipakai
   (K-03 memutuskan bentuk VA; §6.4 aturan versinya).
4. Balasan HTTP ke bank harus **idempoten dan cepat**: ack tanpa efek untuk
   duplikat, dan pekerjaan berat (dokumen/email) tetap lewat job River.
5. **Urutan evaluasi lapis (presedensi).** Toggle/provider → tanda tangan →
   **jendela replay** → kunci peristiwa → pesanan + nominal (§4.2 aturan
   lintas-baris dok. F3-29b). Jendela replay dinilai **sebelum** penilaian
   `waktu_bayar`: callback basi (kedatangan telat, V-06) selalu berakhir **tanpa
   efek ke pesanan** + daftar rekonsiliasi, **tidak** menjadi `perlu_review` VA,
   dan kuncinya tidak diklaim — walau `waktu_bayar > kedaluwarsa_pada`. Satu
   pengecualian sempit: bila dokumen bank menetapkan stempel waktu bertanda
   tangan **= waktu bayar** sehingga kedua lapis berimpit, hasilnya
   `perlu_review` (kasus batas §13 butir 6 dok. F3-29b) — tetap **bukan** Done.

---

## 6. Pencocokan nominal & transisi state

### 6.1 Fungsi pencocokan (prototipe, teruji)

`va.CocokNominal(totalPesanan, nominalCallback, tolKurang, tolLebih) → (hasil, selisih)`
dengan hasil bernilai sama dengan kosakata domain yang sudah ada:
`cocok` / `kurang_bayar` / `lebih_bayar`; `selisih = nominal − total`
(negatif = kurang). Toleransi negatif diperlakukan `0` (fail closed).

### 6.2 Matriks keputusan

| Keadaan | Keputusan | Alasan yang tercatat |
|---|---|---|
| Nominal dalam toleransi | usul **Done** (metode `virtual_account`) | — |
| Nominal < total − `tol_kurang` | **perlu review** | `kurang_bayar` |
| Nominal > total + `tol_lebih` | **perlu review** | `lebih_bayar` |
| Nomor VA tidak dikenal | tolak + audit, tanpa efek | (bukan keputusan pesanan) |
| Pesanan bukan `submitted` | tolak + audit, tanpa efek | — |
| Callback duplikat | ack tanpa efek | — |
| `va.aktif=false` | abaikan | — |

### 6.3 Callback tidak pernah mengubah harga

Nominal callback **tidak** menulis `orders.total`, tidak mengubah baris
pesanan, dan tidak memicu revisi. Bila nominal berbeda jauh dari total, itu
bahan pertanyaan admin ke cabang (K-05), bukan perubahan data otomatis.

### 6.4 Revisi invoice vs VA (aturan yang diusulkan)

Revisi menaikkan `orders.versi` (F2-25). VA lama sudah membawa nominal versi
lama:

**Pembaruan F3-29b (10 Oktober 2026).** Aturan di bawah tetap berlaku dengan
**satu koreksi wajib**: alasan review ketika VA versi lama dibayar setelah
revisi **bukan** `lebih_bayar`/`kurang_bayar`, melainkan **`lainnya`** pada
`orders.verif_status` + kode mesin **`va_versi_lama`** di `va_callbacks.alasan`
dan audit — enum `review_reason` tertutup lima nilai, jadi tidak ada alasan
khusus VA di tingkat pesanan. Rincian: `KEPUTUSAN-VA-BCA-BRI.md` §4.2 (V-09)
dan §4.3; ini juga menyelaraskan §9.1 di halaman ini yang sudah menyebut "kasus
VA memakai `lainnya`". Rumusan lama sengaja dibiarkan terbaca sebagai riwayat.

- `ReffID = order:<order_id>:v<versi>` → revisi memerlukan VA **baru**;
- VA versi lama **tidak dihapus** (jejak audit) tetapi tidak lagi ditampilkan
  kepada cabang sebagai instruksi pembayaran;
- bila VA versi lama dibayar **setelah** revisi → **perlu review**
  (`lebih_bayar`/`kurang_bayar` sesuai selisih terhadap total baru), bukan Done
  otomatis; **Pembaruan F3-29b: alasan review yang berlaku adalah `lainnya` +
  kode mesin `va_versi_lama`** (lihat paragraf pembaruan di atas serta
  `KEPUTUSAN-VA-BCA-BRI.md` §4.2 V-09/§4.3) — jadi acuan yang mengikat adalah
  yang itu, bukan nama alasan di butir riwayat ini;
- keputusan ini **sudah diambil (K-03, K-04 pada F3-29b)**: bentuk VA adalah
  closed VA per pesanan/versi dengan nominal tetap, VA kedaluwarsa 24 jam, dan
  VA lama yang dibayar setelah revisi **tidak pernah** Done otomatis — ia
  `perlu_review` (tampilan instruksi pembayaran ke cabang tetap hanya VA aktif
  versi terkini). Rincian: `KEPUTUSAN-VA-BCA-BRI.md` §1, §4 (V-09/V-10), §5.

---

## 7. Model data (usulan — belum dimigrasikan)

Belum ada migrasi di kartu ini dengan sengaja: tabel yang tidak dipakai tidak
boleh hidup di produksi. Usulan ini menjadi masukan F3-30 (yang benar-benar
menambahkan migrasi + sqlc + repository).

### 7.1 VA terpisah dari bukti manual

```
va_accounts        -- satu baris per penerbitan VA
  id uuid pk, order_id uuid not null, versi int not null default 0,
  reff_id text not null,                     -- order:<order_id>:v<versi>
  provider text not null,                    -- 'penyedia-x' | 'bca-h2h' (dari konfigurasi)
  bank bank_code not null,
  nomor_va text not null,
  nominal bigint not null check (nominal >= 0),
  kedaluwarsa_pada timestamptz,
  raw jsonb,                                 -- jawaban provider (disanitasi)
  created_by uuid, created_at, updated_at timestamptz
  unique (reff_id)                           -- idempotensi DaftarVA
  unique (provider, nomor_va)                -- pencarian saat callback
  check (nominal >= 0)
```

```
va_callbacks       -- satu baris per callback MASUK (jejak + penjaga replay)
  id uuid pk, va_account_id uuid references va_accounts(id),
  provider text not null,
  kunci text not null,                       -- PeristiwaBayar.Kunci()
  id_peristiwa text,                         -- id dari provider (bila ada)
  nomor_va text not null, nominal bigint not null,
  waktu_bayar timestamptz not null,
  diterima_pada timestamptz not null default now(),
  status text not null,                      -- 'diterima' | 'duplikat' | 'ditolak'
  alasan text,                               -- sentinel galat / alasan review
  hasil_nominal text,                        -- 'cocok' | 'kurang_bayar' | 'lebih_bayar'
  selisih bigint,
  raw jsonb,                                 -- payload disanitasi
  unique (provider, kunci)                   -- ⬅ penjaga idempotensi lapis 3
```

Pemisahan ini menjaga janji PRD §5: bukti manual tetap `payment_proofs`, dan
metode verifikasi tetap terekam di `verification_logs.metode`
(`virtual_account` sudah ada di enum `verify_method`). **Tidak ada kolom baru
yang diperlukan pada `orders`.**

### 7.2 Total pesanan yang dipakai untuk pencocokan

Sumber kebenaran tetap `orders.total` (dijaga trigger, `Database.md` §8).
Untuk pesanan C&C yang punya `jatuh_tempo`, VA mengikuti aturan yang sama; VA
**tidak** mengubah perhitungan jatuh tempo.

### 7.3 Pencarian saat callback

Callback hanya membawa `nomor_va` + nominal → indeks unik
`(provider, nomor_va)` adalah satu-satunya jalur pencarian. Tidak ada
pencocokan berdasarkan nama pembayar (nama bisa berbeda dari nama cabang).

### 7.4 Setelan baru (usulan)

| Setelan | Bawaan | Alasan | Status per F3-29b |
|---|---|---|---|
| `va.aktif` | `false` | saklar fitur; **wajib mati** sampai provisioning selesai | **final: tetap `false`** (K-06) |
| `va.provider` | `""` | nama provider di registry; kosong = tidak ada provider | usulan (F3-30) |
| `va.jendela_replay_menit` | `15` | jendela waktu callback | usulan (F3-30) |
| `va.kedaluwarsa_jam` | `24` | masa berlaku nomor VA | **naik status: keputusan K-04** (kedaluwarsa tepat 24 jam) |
| `va.boleh_timpa_manual` | `true` | admin tetap bisa verifikasi manual | **naik status: keputusan K-09** (manual sebagai pengecualian beralasan) |
| `va.<bank>.aktif` · `va.<bank>.provider` | `false` · `""` | toggle & provider **per bank** (BCA/BRI terpisah) | usulan baru F3-29b §2.1 (belum ada di kode) |

Toleransi nominal: F3-29 memakai `ai.tol_kurang`/`ai.tol_lebih` agar tidak ada
dua sumber kebijakan uang. **Pembaruan F3-29b:** karena VA berbentuk closed VA
dengan **nominal tetap** (K-03), pencocokan VA yang berlaku adalah **eksak**
(`selisih = 0`) dan setiap selisih ≠ 0 jatuh ke **perlu review** (K-05);
`ai.tol_*` (bawaan `0/0`, sehingga praktis sama) tetap menjadi kebijakan jalur
OCR/AI. Penyelarasan eksplisit menunggu saat F3-30 dikerjakan —
`KEPUTUSAN-VA-BCA-BRI.md` §13 asumsi 2.

### 7.5 Rute (usulan, F3-30)

```
POST /api/v1/va/callback        -- publik, tanpa sesi/CSRF, tanda tangan WAJIB
GET  /api/v1/admin/orders/{id}/va -- SA/Manager: VA aktif pesanan (bila ada)
```

**Pembaruan F3-29b:** karena BCA dan BRI adalah dua integrasi terpisah
(K-01), usulan rute callback dipisah per bank
(`POST /api/v1/va/<bank>/callback`) — bentuk final menunggu kontrak bank
(K-02); lihat `KEPUTUSAN-VA-BCA-BRI.md` §8.

Rute callback wajib **dikecualikan dari CSRF** (bukan permintaan browser) tetapi
**tidak** dikecualikan dari rate limit; ia juga harus berada di luar guard sesi.

### 7.6 Data pribadi

Payload callback dapat memuat nama pembayar. Kebijakan retensi (K-07) harus
menyebut: `raw` disensor (`SanitizeRaw`), nama pembayar tidak ditampilkan di
portal cabang kecuali diputuskan lain, dan retensi mengikuti `va_callbacks`
(usulan: 5 tahun, sama dengan bukti transfer, `Database.md` §9).

---

## 8. Kebutuhan kredensial & sandbox (checklist provisioning)

Semua baris berstatus **BELUM** — tidak ada yang diminta maupun diterima.

### 8.1 Dari bank/penyedia

| # | Kebutuhan | Untuk apa | Status |
|---|---|---|---|
| P-01 | Pilihan bank/penyedia + penawaran tertulis (biaya per transaksi, PPN, settlement, SLA) | keputusan biaya (K-08) | BELUM |
| P-02 | Draf dokumen spesifikasi API/callback resmi (payload, header, tanda tangan, error code) | kunci kebenaran K-02 | BELUM |
| P-03 | Kredensial sandbox: client id/client key, client secret (atau pasangan kunci), partner id, channel id | panggilan keluar & verifikasi masuk | BELUM |
| P-04 | Kunci publik bank (untuk verifikasi callback) & permintaan kunci publik kita | tanda tangan asimetris | BELUM |
| P-05 | Base URL sandbox & produksi + daftar IP allowlist dua arah | jaringan | BELUM |
| P-06 | Prefix/format nomor VA perusahaan & daftar nomor uji | pembentukan `NomorVA` | BELUM |
| P-07 | Contoh payload callback sukses/gagal/duplikat + cara memicu ulang dari sandbox | uji kontrak nyata (T8) | BELUM |
| P-08 | Akun settlement & jadwal settlement (H+0/H+2) | rekonsiliasi | BELUM |
| P-09 | Kontak teknis + jam dukungan | eskalasi | BELUM |

### 8.2 Dari sisi Grahita

| # | Kebutuhan | Status |
|---|---|---|
| G-01 | URL callback publik HTTPS stabil (mis. `https://grahita-branch.tiusclaw.com/api/v1/va/callback`) + TLS | BELUM (belum dibuat) |
| G-02 | IP statis VPS didaftarkan ke bank | BELUM |
| G-03 | Penyimpanan rahasia (SOPS+age / environment) untuk kredensial VA | pola sudah ada, isi BELUM |
| G-04 | Migrasi tabel VA + sqlc + repository + rute + toggle (F3-30) | BELUM |
| G-05 | Uji kontrak terhadap sandbox nyata + perbandingan dengan mock (T8) | BELUM |
| G-06 | Prosedur rekonsiliasi harian (dana masuk vs VA) + pemilik proses (F3-31) | BELUM |
| G-07 | Runbook: callback gagal, provider down, VA dibayar ganda, VA kedaluwarsa | BELUM |
| G-08 | Persetujuan user atas keputusan tertunda §9 | BELUM |

---

## 9. Gerbang manusia K-01..K-10 (rumusan awal + status terkini)

**Tidak ada implementasi hidup sebelum gerbang manusia terpenuhi (K-01..K-10
dan butir menunggu lain).** Ini gerbang manusia, bukan gerbang teknis.

**Pembaruan F3-29b (10 Oktober 2026).** Jawaban pengguna terbaru membuat
sebagian K-01..K-10 **final** (arsitektur dua adapter host-to-host tanpa
aggregator, closed VA satu pesanan/versi, kedaluwarsa 24 jam, VA lama
dibatalkan + VA baru diterbitkan, callback bayar-setelah-kedaluwarsa
("terlambat")/duplikat/VA lama tidak auto-Done (**efek per kasus, jangan
disatukan** — §9.1 baris K-04 dan §5: duplikat = ack tanpa efek; kedatangan
telat = tolak tanpa efek; bayar-setelah-kedaluwarsa/VA lama = `perlu_review`),
kurang/lebih bayar perlu
review, VA mati dan tetap mati, OCR/manual tetap berjalan, C&C tetap alur
existing, minimisasi PII, manual sebagai
pengecualian beralasan, Finance PIC rekonsiliasi harian) dan menyisakan
sebagian **menunggu** (urutan aktivasi bank, retensi, biaya/PPN/settlement +
plafon + penandatangan, Finance PIC pengganti + SLA, kebijakan VA untuk C&C,
serta dua keputusan teknis M-01/M-02). Tabel di bawah adalah **rumusan
pertanyaan awal** dan sengaja dibiarkan utuh sebagai riwayat; status terkininya
ada di §9.1, rincian + matriks kewajiban bukti di
`docs/design/f3-29/KEPUTUSAN-VA-BCA-BRI.md` §1 dan §11.

| # | Keputusan | Dampak bila salah | Pemilik |
|---|---|---|---|
| K-01 | Bank/penyedia mana dan urutannya (BCA dulu? penyedia dulu?) | seluruh bentuk integrasi & biaya | User |
| K-02 | Skema tanda tangan final dari dokumen resmi (HMAC simetris vs SHA256withRSA SNAP; arah kunci; header wajib) | callback sah ditolak / callback palsu lolos | User + bank |
| K-03 | Bentuk VA: **closed VA per pesanan** (nomor unik per pesanan, nominal tetap) vs **open VA per cabang** | pencocokan pesanan, pengalaman cabang | User |
| K-04 | Masa berlaku VA & perilaku bila dibayar setelah kedaluwarsa (terbitkan ulang? perlu review?) | uang masuk tanpa pesanan yang jelas | User |
| K-05 | Penanganan kurang/lebih bayar via VA (otomatis perlu review? hubungi cabang?) | salah catat pembayaran | User |
| K-06 | Apakah VA menggantikan unggah bukti atau berdampingan; perlakuan khusus C&C (K-06b) | alur C&C & reminder rusak | User |
| K-07 | Retensi & penggunaan data pribadi pembayar pada payload callback | kepatuhan data | User |
| K-08 | Persetujuan biaya/PPN/settlement & pihak penandatangan kontrak | biaya tak terduga/komitmen | User |
| K-09 | Apakah admin masih boleh melakukan verifikasi manual saat VA aktif | penanganan pengecualian | User |
| K-10 | Pemilik proses rekonsiliasi harian (F3-31) & SLA penanganannya | selisih tidak terdeteksi | User |

### 9.1 Status terkini per keputusan (F3-29b, 10 Oktober 2026)

| # | Status | Isi yang berlaku |
|---|---|---|
| K-01 | FINAL (arsitektur) · MENUNGGU (urutan aktivasi bank) | host-to-host langsung, dua adapter terpisah; aggregator bukan target; BCA dulu / BRI dulu **belum** ditentukan |
| K-02 | FINAL (aturan) · MENUNGGU (dokumen bank) | algoritme/header/tanda tangan hanya dari kontrak resmi tiap bank; tidak ditebak dari mock |
| K-03 | FINAL | closed VA: satu pesanan/versi, satu nomor, nominal tetap |
| K-04 | FINAL (kebijakan) · MENUNGGU (kemampuan cancel/generate bank) | kedaluwarsa tepat 24 jam; VA lama dibatalkan + VA baru diterbitkan; **efek callback disebut per kasus** (jangan disatukan — §5 halaman ini; `KEPUTUSAN-VA-BCA-BRI.md` §4.2): **bayar setelah kedaluwarsa** ("terlambat", V-10/V-11) dan **VA lama** (V-09) → `perlu_review` + audit; **duplikat** (V-04: retry normal provider, kunci sudah diklaim) → ack "sudah diproses", **tanpa efek**; **kedatangan telat** (V-06) → tolak tanpa efek + daftar rekonsiliasi; opsi lama "auto-terima dalam masa toleransi" **tidak berlaku lagi** |
| K-05 | FINAL | kurang/lebih bayar → perlu review manusia |
| K-06 | FINAL (VA mati; OCR/manual tetap) · MENUNGGU (VA untuk C&C) | `va.aktif=false` dan tetap false; unggah bukti + OCR/AI tetap jalur berjalan (auto-Done hanya bila cocok + ambang F2-26); C&C tidak auto-aktif |
| K-07 | FINAL (prinsip) · MENUNGGU (angka retensi) | minimisasi/sanitasi data pembayar + pembatasan akses; retensi menunggu persetujuan sebelum go-live |
| K-08 | FINAL (gerbang) · MENUNGGU (isi) | biaya/PPN/settlement + plafon + penandatangan menunggu penawaran resmi; bukan persetujuan kontrak |
| K-09 | FINAL | verifikasi manual admin tetap ada sebagai **pengecualian** dengan alasan, hak akses, audit, dan penjagaan pembayaran ganda |
| K-10 | FINAL (proses) · MENUNGGU (nama pengganti + SLA) | Finance PIC rekonsiliasi harian; pengganti & SLA ditetapkan sebelum VA aktif |

Butir menunggu tambahan yang muncul dari penurunan ini (rincian
`KEPUTUSAN-VA-BCA-BRI.md` §11.2): **M-01** bentuk `ReffID` untuk penerbitan VA
ulang pada versi sama; **M-02** jalur transisi Done V-01 (perluas otorisasi
aktor sistem `virtual_account` **atau** V-01 hanya mengusulkan lalu admin
menyetujui); **M-08** apakah alasan review khusus VA ditambahkan ke enum/UI
(catatan: enum `review_reason` yang ada tertutup lima nilai, jadi keputusan
pesanan untuk kasus VA memakai `lainnya` + kode mesin di `va_callbacks`); **M-09**
hak akses aksi admin VA (terbitkan/batalkan). Kewajiban bukti per bank
(B-1..B-9) ada di §11.3 dokumen yang sama.

---

## 10. Kewajiban kartu turunan

| Kartu | Kewajiban yang diturunkan dari kontrak ini |
|---|---|
| **F3-30 (t_fc796fba)** — adapter bank & callback | mengimplementasikan `va.Provider` untuk provider yang **akhirnya dipilih**; menambahkan migrasi `va_accounts`/`va_callbacks` (§7), repository, rute `POST /api/v1/va/callback` (tanpa CSRF, dengan tanda tangan wajib), saklar `va.aktif` bawaan mati, transisi Done lewat `payment.MarkVerified` metode `virtual_account`, audit `va_payment`; menjalankan `vatest.Uji` terhadap adapter baru; **memblokir** kartu bila kredensial/sandbox/dokumen callback belum ada (jangan mengarang). **Tambahan F3-29b:** satu adapter **per bank** + toggle/konfigurasi per bank (§2.1 dok. F3-29b), kedaluwarsa 24 jam + job penanda `kedaluwarsa`, pembatalan VA lama (kemampuan `BatalVA` opsional — bank tanpa API cancel tetap sah), penanganan **efek callback per kasus** (jangan disatukan): **bayar-setelah-kedaluwarsa (V-10/V-11)/VA lama/versi lama/pesanan sudah Done = `perlu_review` + audit**; **duplikat (V-04) = ack "sudah diproses", tanpa efek** (provider mengirim at-least-once, jadi retry normal **tidak boleh** menghasilkan `perlu_review`); **kedatangan telat (V-06) = tolak tanpa efek** (tidak auto-Done), partial unique "satu VA aktif per (order, versi)", **presedensi lapis** (jendela replay dievaluasi lebih dahulu daripada penilaian `waktu_bayar` — §4.2 aturan lintas-baris/§5.1 dok. F3-29b), dan matriks uji T-VA-01..T-VA-32 sebagai acuan. Butir M-01/M-02 (bentuk `ReffID` penerbitan ulang, jalur transisi Done) **wajib diputuskan lebih dulu**. |
| **T8 (t_06195c87)** — uji VA, rekonsiliasi, laporan | menjalankan `vatest.Uji` pada sandbox nyata dan membandingkan hasilnya dengan `va.MockProvider` (kriteria "bandingkan hasil sandbox nyata dengan mock"); menguji replay/duplikasi/nominal mismatch/race manual+VA; memverifikasi laporan tidak menggandakan dan hanya Superadmin. |
| **F3-31** — rekonsiliasi & laporan pembayaran | memakai `va_callbacks`/`va_accounts` sebagai sumber, tidak menghitung ulang dari `payment_proofs`; memisahkan biaya transaksi penyedia dari total pesanan. |
| **Rilis (T4/T5)** | variabel kredensial VA masuk daftar rahasia operator; IP statis & URL callback masuk checklist produksi. |

---

## 11. Rujukan (bukan kontrak Grahita)

- Midtrans & Xendit — harga VA ±Rp4.000/transaksi (di luar PPN 11%), settlement
  H+2, tanpa biaya setup/bulanan: `midtrans.com/pricing`,
  `xendit.co/en-id/pricing/` (ringkasan harga terverifikasi Mei 2026).
- BRI — SNAP BI, header/tanda tangan & endpoint token
  `/snap/v1.0/access-token/b2b`: `developers.bri.co.id/en/snap-bi/apidocs-oauth-snap-bi`.
- Bentuk stringToSign transaksi SNAP BI (publik, dari mitra SNAP):
  `docs-snap.paydia.id`, `docs.duitku.com/snap-api/en/`,
  `docs.durianpay.id/docs/snap-preparation-prerequisite`.

**Peringatan:** angka biaya dan bentuk tanda tangan di atas berasal dari sumber
publik dan **wajib diverifikasi ulang** terhadap penawaran/dokumen resmi
bank/penyedia sebelum integrasi hidup (K-01, K-02, K-08).
