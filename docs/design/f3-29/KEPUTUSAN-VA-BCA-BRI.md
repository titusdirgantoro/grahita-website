# F3-29b — Kebijakan VA langsung BCA/BRI: keputusan, state, dan rencana implementasi

Kartu: **t_c7a417e2 — F3-29b Rancangan kebijakan VA langsung BCA/BRI (nonaktif)**
Kartu induk: **t_5306bebf — F3-29 Kajian dan kontrak integrasi Virtual Account**
Tanggal: 10 Oktober 2026
Repo: `/home/hermes/projects/grahita-website`

Status: **RANCANGAN KEBIJAKAN — VA TETAP MATI.** Kartu ini **tidak** menulis kode,
migrasi, rute callback hidup, setelan hidup, atau pengaktifan VA. Yang berubah
hanya dokumen desain.

Dokumen acuan: `docs/PRD.md` §5 (tiga mode verifikasi), §8 no.10 (bank mana &
host-to-host vs gateway), §10 (Fase 3); `docs/Architecture.md` §2.2 (prinsip),
§6 (keamanan); `docs/SystemDesign.md` §4.3 (alur verifikasi), §5.3–§5.5
(idempotensi, audit, waktu), §6 (permukaan API + bentuk error);
`docs/Database.md` §1 (konvensi), §3 (enum), §4.5 (pembayaran & verifikasi).
Dokumen sebelumnya: `docs/design/f3-29/KONTRAK-VA.md` (§1, §3–§9) dan
`docs/design/f3-29/HASIL.md`.

**Hubungan dengan F3-29 (agar tidak diulang):** F3-29 mengunci *bentuk* —
kontrak `va.Provider`, dua keluarga tanda tangan, tiga lapis idempotensi,
model data usulan, checklist provisioning, dan gerbang manusia K-01..K-10.
Kartu ini **tidak mengulang kajian itu**; ia menurunkan **jawaban pengguna
terbaru** menjadi kebijakan, state machine, tabel keputusan, batas antarmuka
dua adapter terpisah, dan matriks uji yang dapat langsung dipakai F3-30/QA.

---

## 0. Batas lingkup & pernyataan yang TIDAK boleh disimpulkan

| Kartu ini | Bukan kartu ini |
|---|---|
| Kebijakan + state + tabel keputusan + inventaris + matriks uji **tertulis** | Tidak ada kode produksi, migrasi, sqlc, rute, setelan, atau UI baru |
| Menyelaraskan catatan keputusan tertunda di `KONTRAK-VA.md` §9 (histori dijaga) | Tidak mengulang kajian opsi/biaya F3-29 §2 |
| Dua adapter terpisah (BCA, BRI) **direncanakan**; urutan aktivasi belum ditentukan | Tidak memilih bank pertama, tidak menandatangani apa pun |
| Ketergantungan kontrak/sandbox dicatat per bank | Tidak ada klaim sandbox, penawaran harga, atau akses bank |

Yang **tidak boleh** dibaca sebagai fakta dari dokumen ini:

1. Tidak ada perjanjian, penawaran, atau harga resmi BCA/BRI (K-08 menunggu).
2. Tidak ada akses sandbox, kredensial, atau dokumentasi API resmi bank
   (K-02 menunggu; bentuk payload/header/tanda tangan **tidak dikarang** di sini).
3. Tidak ada nomor VA nyata/palsu yang diterbitkan; `va.aktif` tetap `false`.
4. Jalur pembayaran yang **hidup hari ini** tetap: verifikasi manual admin,
   opsional AI/OCR bila `ai.aktif=true` dan pembaca terkonfigurasi, serta C&C
   dengan reminder + jatuh tempo yang sudah berjalan.

---

## 1. Keputusan berlaku (K-01..K-10) — status per kartu ini

Turunan jawaban pengguna terbaru. Kolom **Status** memisahkan keputusan yang
sudah final dari yang masih menunggu; kolom **Delta** menyebut apa yang
berubah terhadap `KONTRAK-VA.md`.

| # | Keputusan berlaku | Status | Delta terhadap F3-29 |
|---|---|---|---|
| K-01 | Arsitektur untuk BCA dan BRI: **host-to-host langsung dengan dua adapter terpisah**. **Aggregator/penyedia bukan target.** Urutan aktivasi bank **belum** ditentukan. | FINAL (arsitektur) · MENUNGGU (urutan aktivasi) | Menggantikan rekomendasi §2.4 F3-29 (piloting lewat penyedia opsi C) |
| K-02 | Algoritme/header/tanda tangan final **hanya** dari kontrak resmi masing-masing bank; **tidak ditebak** dari mock. | FINAL | Menegaskan K-02; dua adapter berarti dua pemetaan tanda tangan yang dapat berbeda |
| K-03 | **Closed VA**: satu pesanan/versi, satu nomor VA, **nominal tetap**. | FINAL | K-03 tidak lagi "pending"; `ReffID = order:<order_id>:v<versi>` (§7) |
| K-04 | VA **kedaluwarsa tepat 24 jam** setelah terbit. VA lama **dibatalkan** dan VA **baru** diterbitkan. Callback tidak pernah auto-Done; **efeknya per kasus** (§4.2): bayar setelah kedaluwarsa ("terlambat")/**VA lama = perlu review + audit**, **duplikat = ack "sudah diproses" tanpa efek**, **kedatangan telat = tolak tanpa efek + daftar rekonsiliasi**. Kenyataan cancel/generate **diselaraskan dengan kontrak bank kelak**. | FINAL (kebijakan) · MENUNGGU (kemampuan cancel/generate di sisi bank) | Menghapus pilihan lama "auto-terima dalam masa toleransi"; membedakan "terlambat" (bayar setelah kedaluwarsa) dari kedatangan telat |
| K-05 | Kurang/lebih bayar **perlu review manusia**; tidak pernah auto-Done. | FINAL | K-05 alasan review: daftar `review_reason` tertutup **lima nilai**, lihat §4.3 |
| K-06 | VA untuk masa depan **dan wajib mati sekarang**. Unggah bukti + OCR/AI tetap **jalur berjalan**; boleh auto-Done hanya bila cocok **dan** ambang keyakinan terpenuhi sesuai kebijakan F2-26; admin menangani pengecualian. Jangan mengklaim OCR eksternal aktif bila tidak ada konfigurasi/akses. **C&C tetap alur existing** sampai kebijakan VA C&C diputuskan — tidak auto-aktif. | FINAL (VA mati + OCR/manual tetap) · MENUNGGU (kebijakan VA untuk C&C) | K-06b (C&C) tetap menunggu; jalur AI tidak disentuh |
| K-07 | **Minimisasi/sanitasi** data pembayar + **pembatasan akses**; masa **retensi spesifik menunggu persetujuan** sebelum go-live. | FINAL (prinsip) · MENUNGGU (angka retensi) | §7.6 F3-29 (usulan 5 tahun) berstatus **usulan**, bukan keputusan |
| K-08 | Biaya/PPN/settlement, **plafon**, dan **penandatangan** menunggu **penawaran resmi** dan persetujuan pemilik. Ini **bukan** persetujuan kontrak. | FINAL (gerbang) · MENUNGGU (isi) | Plafon menjadi hook desain (§9, T-VA-21) |
| K-09 | Verifikasi **manual admin sebagai pengecualian**, dengan **alasan**, **hak akses**, **audit**, dan **penjagaan pembayaran ganda**. | FINAL | Menjawab K-09 F3-29 "apakah admin masih boleh manual" = **ya, sebagai pengecualian** (§6) |
| K-10 | **Finance PIC** rekonsiliasi harian; **pengganti dan SLA** ditetapkan sebelum VA aktif. | FINAL (proses) · MENUNGGU (nama + SLA) | Melanjutkan §7.5/§10 F3-29 (F3-31) |
| — | Operasi umum: toggle `va.aktif` **bawaan `false` dan tetap `false`**; tanpa kontrak + sandbox BCA **dan** BRI tidak ada provider nyata, tidak ada produksi. | FINAL | Menegaskan §1 F3-29 butir "fitur mati secara bawaan" |

Ringkas: **tidak ada satu pun butir di atas yang membuka pintu implementasi.**
K-01..K-10 yang berstatus MENUNGGU tetap merupakan gerbang manusia sebelum
aktivasi (§11).

---

## 2. Arsitektur: dua adapter terpisah, satu domain

```
                 ┌──────────────────────── domain pembayaran (SUDAH ADA) ───────────────┐
   bank BCA ──►  │  va.Adapter(callback) ──► pencocokan nominal ──► payment.MarkVerified │
   bank BRI ──►  │  (tanda tangan/liveness)      (§4)               (satu pintu Done)     │
                 └──────────────────────────────────────────────────────────────────────┘
                        ▲                     ▲
        adapter terpisah┘                     └ adapter terpisah
        va/bca (Nama="bca-h2h")                 va/bri (Nama="bri-h2h")
        kredensial, seri nomor VA,              kredensial, seri nomor VA,
        pemetaan tanda tangan, toggle           pemetaan tanda tangan, toggle
        ── TIDAK ADA aggregator di antaranya ──
```

Aturan arsitektur yang mengikat (dan tidak bergantung pada isi kontrak bank):

1. **Satu `va.Provider` per bank.** Kedua adapter mengimplementasikan interface
   yang sudah dikontrak-uji di `api/internal/payment/va` (§3.1). Domain
   pembayaran **tidak** tahu nama bank: bank masuk lewat `Provider.Nama()`,
   kolom `va_accounts.provider`/`bank`, dan konfigurasi.
2. **Tidak ada jalur agregator.** Tidak ada broker pihak ketiga antara Grahita
   dan bank; bila suatu saat aggregator dipertimbangkan lagi, itu keputusan user
   baru (K-01 kini menutupnya).
3. **Bank terpisah juga pada kegagalan.** Satu bank mati/salah tidak boleh
   membuat VA bank lain berhenti: adapter, kredensial, seri nomor, jendela
   replay, dan toggle **per bank**.
4. **Satu pesanan/versi hanya punya satu VA aktif dari satu bank** (turunan
   K-03 "satu pesanan satu versi, nominal tetap"). Menghindari dua nomor VA
   untuk satu nominal — sumber pembayaran ganda yang paling sulit dilacak.
5. **Tanpa dokumentasi resmi tidak ada adapter nyata.** Adapter BCA/BRI **hanya
   dibuat setelah** dokumen kontrak (K-02) ada; sampai itu, satu-satunya
   provider yang ada adalah `va.MockProvider` (deterministik, tanpa jaringan),
   dan itu **bukan** integrasi.

### 2.1 Strategi toggle per bank

Prinsip: **mati secara bawaan, mati bila ragu, dan mati per bank.**

| Lapis toggle | Perilaku yang diharapkan | Bawaan |
|---|---|---|
| `va.aktif` (master, usulan nama dari F3-29 §7.4) | VA tidak pernah diterbitkan; rute callback tidak melayani provider apa pun | **`false`** (final) |
| `va.<bank>.aktif` (mis. `va.bca.aktif`, `va.bri.aktif`) | hanya memengaruhi bank itu; bank lain tidak terpengaruh | **`false`** |
| `va.<bank>.provider` | nama provider di registry; kosong = tidak ada provider → **gagal terang-terangan**, bukan "sukses tanpa provider" | `""` |
| Provider terpasang (`Adapter.Terpasang`, sudah ada di prototipe) | toggle hidup + provider kosong → `KodeDitolak` + `ErrProviderBelumDipilih` | `false` |

Catatan kejujuran: **nama setelan di atas adalah usulan.** Belum ada satu pun
yang tertulis di kode atau di `settings` (kunci yang ada sekarang hanya
`biaya_packaging`, `cnc_hari`, `token_hari`, `reset_menit`, `max_mb`, `max_file`,
`digit_invoice`, `manager_boleh_revisi`, `ai`). Penambahan kunci adalah
pekerjaan F3-30 dan tidak dilakukan di kartu ini.

Kriteria mati (harus benar seluruhnya sebelum toggle bank mana pun boleh `true`):
kontrak resmi bank, kredensial sandbox, uji sandbox lulus, runbook, persetujuan
retensi (K-07), persetujuan biaya + plafon (K-08), Finance PIC + SLA (K-10),
dan persetujuan user atas §11. Lihat §11.2 (daftar bukti).

---

## 3. Batas antarmuka provider (tanpa mengarang payload bank)

### 3.1 Kontrak yang sudah ada (dipakai apa adanya)

```go
// api/internal/payment/va/va.go — SUDAH ADA, terkontrak-uji (vatest.Uji)
type Provider interface {
    Nama() string
    DaftarVA(ctx context.Context, in PermintaanVA) (HasilVA, error)
    TerimaCallback(ctx context.Context, in PermintaanCallback) (PeristiwaBayar, error)
}
```

Sifat yang mengikat kedua adapter bank (semuanya sudah diuji di F3-29):
`TerimaCallback` **murni** (tidak menulis apa pun), `DaftarVA` idempoten
terhadap `ReffID`, galat sentinel terpisah (tidak ada "peristiwa kosong yang
sukses"), `Raw` disanitasi sebelum disimpan.

### 3.2 Tambahan yang dibutuhkan kebijakan K-04 — kemampuan **opsional**

Kebijakan 24 jam + "VA lama dibatalkan" **bergantung pada kemampuan bank**.
Karena itu pembatalan ditulis sebagai **kemampuan opsional**, bukan sebagai
payload yang diarang:

```go
// USULAN (belum ditulis). Interface terpisah supaya adapter bank yang tidak
// punya API cancel tetap sah dan uji kontrak lama tidak berubah.
type PembatalVA interface {
    // BatalVA membatalkan/menutup nomor VA di sisi bank.
    BatalVA(ctx context.Context, in PermintaanBatal) (HasilBatal, error)
}
```

| Keadaan bank | Perilaku Grahita |
|---|---|
| Bank **mendukung** cancel/close VA | VA lama benar-benar ditutup di bank; status baris `dibatalkan` + jejak `dibatalkan_di_bank=true` |
| Bank **tidak mendukung** cancel | VA lama **tetap dapat dibayar di sisi bank**. VA lama hanya ditandai internal `dibatalkan`, **tidak** ditampilkan lagi kepada cabang, dan **setiap** callback yang masuk untuk VA lama wajib **perlu review** (bukan Done) — lihat §4 baris V-09/V-10 |
| Belum diketahui | Diperlakukan seperti "tidak mendukung" (fail closed) sampai kontrak bank menyatakan lain (K-04, K-02) |

### 3.3 Yang **tidak** ditulis di sini (menunggu K-02)

Path/versi endpoint bank, nama & daftar header wajib, bentuk `stringToSign`,
algoritme/arah kunci, format nomor VA perusahaan, kode galat bank, cara memicu
ulang callback di sandbox, batas waktu / plafon per transaksi. Semuanya adalah
item P-01..P-09 (F3-29 §8.1) yang **berstatus BELUM** dan tidak boleh diisi
dengan tebakan.

---

## 4. State VA & tabel keputusan callback

### 4.1 Diagram state satu baris `va_accounts` (usulan, §7)

```
  (belum ada)
      │  terbit: DaftarVA(reff_id = order:<id>:v<n>)   [syarat: va.aktif & va.<bank>.aktif
      ▼                                                  & provider terpasang & pesanan submitted]
   [AKTIF]  kedaluwarsa_pada = terbit_pada + 24 jam
      │
      ├─ callback sah & pasti (§4.2 baris V-01) ──────────────► [DIPAKAI]
      │                                                            └─► pesanan Done
      │                                                                (metode virtual_account,
      │                                                                 lewat payment.MarkVerified)
      ├─ clock ≥ kedaluwarsa_pada (job penanda, Clock) ────────► [KEDALUWARSA]
      │                                                             └─► terbitkan VA baru (versi sama? lihat §5)
      └─ revisi invoice (versi+1) atau pembatalan admin ───────► [DIBATALKAN]
                                                                    └─► BatalVA dikirim HANYA bila bank
                                                                        mendukung (§3.2)
```

Status yang **tidak ada**: tidak ada state "Done otomatis dalam masa toleransi"
(masa toleransi lama dihapus oleh K-04). Tidak ada state yang bisa berpindah
`KEDALUWARSA → DIPAKAI`: VA kedaluwarsa yang dibayar masuk sebagai **review**.

### 4.2 Tabel keputusan callback (inti K-03/K-04/K-05/K-09)

Kolom **Efek pesanan** adalah satu-satunya hal yang menyentuh pembayaran.
"Review" = `orders.verif_status='perlu_review'` + `verification_logs` +
audit; pesanan **tetap** Submitted (PRD §7.1).

**Definisi istilah waktu (wajib; dua istilah ini tidak boleh tertukar).**
Permintaan QA putaran 1: satu definisi "terlambat", bukan dua.

- **Kedatangan telat** *(arrival lag)* — callback **tiba** di luar
  `va.jendela_replay_menit` (usia tanda tangan/`waktu_bayar` terhadap jam server
  saat diterima). Ini soal **jalur pengiriman**, dan **tidak** melihat apakah
  pembayarannya on-time di sisi bank. → baris **V-06**.
- **Pembayaran setelah kedaluwarsa** *(paid after expiry)* —
  `waktu_bayar > va_accounts.kedaluwarsa_pada`. Ini soal **kapan uang dibayar**,
  tidak melihat kapan callback dikirim. → baris **V-10** (baris sudah dibalik
  job penanda) dan **V-11** (baris masih `aktif`).

Kata telanjang "terlambat" **tidak dipakai** di tabel ini. Bila muncul di
dokumen lain (K-04, §5 butir 2, §11.1), ia berarti **pembayaran setelah
kedaluwarsa** (V-10/V-11) — bukan kedatangan telat (V-06).

| # | Keadaan callback | Tanda tangan | Efek pesanan | Jejak |
|---|---|---|---|---|
| V-01 | VA **aktif**, versi **terkini**, `waktu_bayar ≤ kedaluwarsa_pada`, nominal **persis** (`selisih = 0`), kunci belum diklaim, pesanan `submitted` | sah & dalam jendela | **usul Done** (`virtual_account`) lewat `payment.MarkVerified` | `va_callbacks` baris `diterima` + `verification_logs` |
| V-02 | Sama seperti V-01 tetapi nominal **kurang** (selisih < 0) | sah | **perlu review** (K-05) | kode mesin di `va_callbacks.alasan`; alasan tertutup pesanan: `kurang_bayar` |
| V-03 | Sama seperti V-01 tetapi nominal **lebih** (selisih > 0) | sah | **perlu review** (K-05) | idem, alasan `lebih_bayar` |
| V-04 | Callback **duplikat** (kunci idempotensi sudah diklaim) | sah | **tanpa efek** (ack "sudah diproses") | `va_callbacks` baris `duplikat` |
| V-05 | **Replay/callback palsu** (tanda tangan/kanonik salah, `Raw` diubah, header kurang) | tidak sah | **tanpa efek** | audit; **kunci peristiwa TIDAK diklaim** (agar callback sah berikutnya tidak terhalang) |
| V-06 | **Kedatangan telat** (§4.2 definisi): callback tiba di luar jendela replay (`va.jendela_replay_menit`), **apa pun** nilai `waktu_bayar`-nya | sah tapi basi | **tanpa efek ke pesanan** | audit + masuk **daftar kerja rekonsiliasi harian** (bisa jadi pembayaran nyata yang tersampaikan telat → jalur manual admin, K-09/K-10); **kunci peristiwa TIDAK diklaim** (pengiriman ulang yang sah tidak boleh terhalang) |
| V-07 | Nomor VA **tidak dikenal** | sah | **tanpa efek** | audit `va_payment` |
| V-08 | Pesanan **bukan** `submitted` (draft/batal) | sah | **tanpa efek** | audit |
| V-09 | **VA lama** (versi < versi terkini) dibayar — termasuk setelah revisi invoice | sah | **perlu review** (uang masuk tidak boleh diabaikan; nominalnya milik versi lama) | `va_callbacks` `perlu_review` + kode mesin `va_versi_lama` |
| V-10 | **Bayar setelah kedaluwarsa** (`waktu_bayar > kedaluwarsa_pada`) **atau** VA sudah ditutup, dan baris `va_accounts` sudah berstatus final `kedaluwarsa` (job penanda) atau `dibatalkan` (revisi/admin), mis. karena bank tidak mendukung cancel | sah | **perlu review** (uang masuk tidak boleh diabaikan) | kode mesin `va_kedaluwarsa` / `va_dibatalkan` |
| V-11 | **Bayar setelah kedaluwarsa** (`waktu_bayar > kedaluwarsa_pada`) tetapi baris `va_accounts` **masih `aktif`** — celah antara lewatnya 24 jam dan jalannya job penanda (`Clock`, §5 butir 2) | sah | **perlu review** — **tidak pernah Done** | kode mesin `va_bayar_setelah_kedaluwarsa` (boleh disatukan dengan kode V-10 bila implementasi memilih satu kode; hasilnya tetap review) |
| V-12 | **Pesanan sudah Done** (oleh manual, AI, atau VA sebelumnya) | sah | **tanpa efek**; tidak ada dokumen/email kedua | `va_callbacks` `duplikat`/`perlu_review` + audit |
| V-13 | VA **aktif** dibayar sementara admin **sedang** menyelesaikan verifikasi manual → siapa pun yang menang lebih dulu; yang kedua tidak mengubah status (penguncian baris pesanan) | sah | pemenang: Done; yang kedua: `ErrAlreadyDone` → **perlu review** pembayaran ganda | audit ganda + masuk rekonsiliasi K-10 |
| V-14 | `va.aktif=false` (kondisi hari ini) atau `va.<bank>.aktif=false` | — | **tanpa efek**, provider **tidak dipanggil sama sekali** | tidak ada baris keadaan; (bila rute belum terdaftar: 404) |
| V-15 | Toggle hidup tetapi **provider tidak terpasang/kosong** | — | **gagal terang-terangan** (`KodeDitolak`, galat provider) | audit |

Aturan lintas-baris:

- **Tidak ada auto-Done pada kasus tidak pasti.** V-01 adalah satu-satunya
  jalur yang mengusulkan Done; ia mensyaratkan lima kondisi sekaligus (aktif,
  versi terkini, on-time, nominal persis, bukan duplikat/replay).
- **Kebijakan waktu, bukan status.** Hasil V-10 dan V-11 identik
  (**perlu review**); yang berbeda hanya kode mesin dan apakah job penanda
  sudah sempat berjalan. Implementasi **tidak boleh** memakai status internal
  `aktif` sebagai alasan menganggap pembayaran on-time atau mengusulkan Done —
  syarat on-time adalah `waktu_bayar ≤ kedaluwarsa_pada` (V-01), bukan status
  baris.
- **Presedensi lapis: jendela waktu dievaluasi lebih dahulu.** Baris tabel di
  atas dibaca **berlapis**, mengikuti urutan F3-29 §5: (1) toggle + provider
  (V-14/V-15), (2) tanda tangan (V-05), (3) **jendela replay** (V-06),
  (4) kunci peristiwa (V-04), (5) pesanan + nominal (V-01..V-03, V-07..V-13).
  Konsekuensi tegas: **V-06 menang atas V-10/V-11.** Callback yang tiba di luar
  `va.jendela_replay_menit` dinilai **kedatangan telat** — tolak **tanpa efek ke
  pesanan**, kunci **tidak** diklaim, audit + daftar kerja rekonsiliasi harian —
  **meskipun** `waktu_bayar > kedaluwarsa_pada`. **V-10/V-11 hanya berlaku untuk
  callback yang tiba di dalam jendela replay.** Alasan: callback basi tidak
  boleh diproses sebagai peristiwa baru hanya karena isinya sudah lewat
  kedaluwarsa, dan uang yang mungkin nyata tetap terlihat manusia lewat daftar
  rekonsiliasi (K-09/K-10) — tidak ada yang hilang diam-diam. Uji kombinasi:
  §10 **T-VA-32**.
  *Kasus batas yang tetap terbuka sampai K-02:* bila dokumen bank menetapkan
  stempel waktu bertanda tangan **= waktu bayar** (sehingga lapis (3) dan
  penilaian `waktu_bayar` berimpit dan tidak terpisah), hasilnya tetap
  **`perlu review`, tidak pernah Done** — perlakuan paling konservatif, uang
  masuk tidak diabaikan (§13 butir 6).
- **Verifikasi tanda tangan lebih dahulu, klaim kunci paling akhir** (urutan
  F3-29 §5) — supaya callback palsu tidak dapat "membakar" kunci yang sah.
- **Klaim kunci + pencocokan pesanan dalam satu transaksi yang mengunci baris
  pesanan** (`SELECT ... FOR UPDATE`, `SystemDesign.md` §5.3).
- **Nominal callback tidak pernah mengubah harga**: `orders.total` tidak
  ditulis; selisih hanya tercatat.
- **Idempotensi**: kunci peristiwa = `PeristiwaBayar.Kunci()`; unique
  `(provider, kunci)` di `va_callbacks` adalah penjaga lapis 3.

### 4.3 Alasan review: daftar tertutup tetap, kode mesin terpisah

Enum `review_reason` yang ada sekarang **tertutup lima nilai**
(`kurang_bayar`, `lebih_bayar`, `tidak_terbaca`, `rekening_beda`, `lainnya`)
dan UI/prototype memakai daftar itu. Karena itu:

- keputusan pesanan untuk V-09/V-10/V-11/V-12/V-13 memakai `lainnya`
  (tidak ada migrasi enum, tidak ada perubahan UI di kartu ini);
- **alasan spesifik mesin** (mis. `va_versi_lama`, `va_kedaluwarsa`,
  `va_dibatalkan`, `va_duplikat`, `pembayaran_ganda`) ditulis di
  `va_callbacks.alasan` dan audit — bukan di enum pesanan;
- bila user menghendaki alasan khusus muncul di UI admin, itu **penambahan
  enum + label UI** (keputusan menunggu, §11) dan dikerjakan F3-30 sebagai
  perubahan terpisah — bukan diam-diam di kartu ini.

---

## 5. Alur 24 jam: terbit → expire → cancel lama → terbitkan baru

1. **Terbit.** Hanya bila `va.aktif` + `va.<bank>.aktif` + provider terpasang;
   `ReffID = order:<order_id>:v<versi>` (idempoten); `nominal` = `orders.total`
   saat itu (**tetap**, tidak ikut revisi berikutnya); `kedaluwarsa_pada =
   terbit_pada + 24 jam` (durasi tetap memakai `Clock`, bukan "akhir hari WIB" —
   jadi tidak ada ambiguitas zona waktu; `Database.md` §1: simpan `timestamptz`
   UTC, tanggal bisnis WIB hanya untuk tampilan).
2. **Kedaluwarsa tepat 24 jam.** Batas bersifat inklusif pada
   `kedaluwarsa_pada`: `waktu_bayar ≤ kedaluwarsa_pada` = on-time;
   `waktu_bayar > kedaluwarsa_pada` = **bayar setelah kedaluwarsa** → **perlu
   review, tidak pernah Done** (§4.2 V-10/V-11). Penanda status `kedaluwarsa`
   dijalankan job deterministik (River + `Clock`, `SystemDesign.md` §5.5) —
   bukan dihitung di UI. **Hasil review tidak bergantung pada kapan job itu
   berjalan**: job sudah jalan → V-10, belum jalan (status masih `aktif`) →
   V-11; keduanya `perlu review`, jadi tidak ada celah antara "lewatnya jam"
   dan "jalannya job". **Presedensi:** butir ini hanya berlaku untuk callback
   yang **tiba di dalam jendela replay** — callback basi (kedatangan telat,
   V-06) dievaluasi lebih dahulu dan menang, walau `waktu_bayar` sudah lewat
   kedaluwarsa (§4.2 presedensi; uji §10 T-VA-32). **Angka 24 jam adalah
   keputusan user (K-04); bila
   kontrak bank mensyaratkan batas lain, angka dari kontrak yang berlaku
   (K-02).**
3. **Cancel VA lama** terjadi pada: (a) revisi invoice (versi+1, F2-25),
   (b) pembatalan administratif dengan alasan, (c) pesanan Done lewat jalur lain
   sementara VA masih aktif (supaya cabang tidak membayar pesanan yang sudah
   lunas). Bila bank mendukung cancel → kirim `BatalVA` + catat hasilnya; bila
   tidak → tandai internal dan **wajibkan review** untuk setiap pembayarannya
   (§4 V-10).
4. **Terbitkan VA baru.** Setelah cancel/expire, VA baru dibuat dengan
   `ReffID` **baru**. Dua bentuk yang harus dibedakan (dan tidak diketahui mana
   yang benar sebelum kontrak bank / keputusan user):
   - **versi sama** (`order:<id>:v<n>` yang sama) — tidak mungkin dengan
     `unique (reff_id)`; memerlukan nomor penerbitan (mis. `:v<n>#2`) dan
     memperumit "satu versi satu nomor";
   - **versi baru** (`v<n+1>`) — konsisten dengan K-03 dan idempotensi
     `ReffID` sekarang.
   → **Usulan desain (menunggu persetujuan, §11 M-01):** nomor penerbitan
   ulang ditandai pada `ReffID` (`order:<id>:v<versi>#<urutan>`) sehingga
   "versi pesanan" tetap milik revisi invoice, sementara penerbitan VA ke-2, ke-3,
   … tetap idempoten terhadap `reff_id`-nya sendiri. Alternatifnya adalah
   mewajibkan kenaikan versi pesanan untuk setiap penerbitan ulang — lebih
   sederhana untuk `ReffID`, tetapi mencampur revisi invoice dengan siklus
   kedaluwarsa VA. Sampai diputuskan, satu versi = satu penerbitan.
5. **Satu VA aktif per (order_id, versi)** ditegakkan basis data (partial unique
   index), bukan hanya kode — dua penerbitan bersamaan tidak boleh menghasilkan
   dua nomor yang sama-sama ditampilkan.
6. **Yang ditampilkan ke cabang**: hanya VA **aktif** dari **versi terkini**.
   VA kedaluwarsa/dibatalkan tetap tersimpan (jejak + audit) tetapi bukan
   instruksi pembayaran (K-04, §6.4 F3-29 tetap berlaku).

---

## 6. Bentrok manual vs VA, replay, dan pemisahan dua bank

### 6.1 Manual tetap ada, sebagai pengecualian (K-09)

`payment.MarkVerified` tetap satu-satunya pintu Done; metode `manual` tetap
milik staf Pusat ber-`admin.verify` (PRD §2.1) dan **tidak** berubah. Kebijakan
VA menambahkan tiga kewajiban:

1. **Pengecualian beralasan** — verifikasi manual atas pesanan yang sudah punya
   VA aktif dicatat dengan catatan/alasan (bukan klik tanpa jejak).
2. **Penjagaan pembayaran ganda** — setelah Done, VA aktif pesanan itu
   ditutup/ditandai (§5 butir 3c); callback yang tetap masuk dicatat sebagai
   **V-12/V-13** dan masuk rekonsiliasi harian Finance (K-10).
3. **Urutan otorisasi belum final untuk jalur otomatis (fakta kode).**
   `verifikatorSah` saat ini menerima aktor **sistem** hanya untuk metode `ai`;
   metode `virtual_account` dari aktor sistem **ditolak**. Jadi auto-Done V-01
   memerlukan **salah satu** keputusan berikut (keduanya **menunggu**, §11
   item M-02): (a) memperluas aturan otorisasi agar aktor sistem diizinkan untuk
   `virtual_account` (perubahan aturan keamanan yang harus disetujui eksplisit),
   atau (b) V-01 hanya **menandai** usulan dan admin menekan verifikasi
   (metode `virtual_account` oleh admin) — lebih lambat, tanpa perubahan aturan.

### 6.2 Replay & race

- Jendela waktu (F3-29 §4.3) menangani replay; klaim kunci mencegah efek ganda.
  **Jendela dievaluasi lebih dahulu daripada penilaian `waktu_bayar`** (§4.2
  presedensi): callback yang tiba di luar jendela = kedatangan telat (V-06,
  tolak tanpa efek) walaupun `waktu_bayar` sudah melewati kedaluwarsa; V-10/V-11
  hanya berlaku untuk callback yang tiba di dalam jendela.
- Dua callback bersamaan untuk kunci berbeda (mis. VA lama + VA baru) →
  diserialkan oleh penguncian baris pesanan; pemenang ditentukan transaksi,
  yang kalah menjadi review (V-09/V-13).
- Tidak ada efek samping sebelum tanda tangan sah (V-05/V-06 tidak mengklaim
  kunci).

### 6.3 Dua bank terpisah berarti dua jalur callback

- Nomor VA dari dua bank tidak boleh saling tertukar: pencarian **selalu**
  `(provider, nomor_va)`; tidak ada pencocokan berdasarkan nama pembayar atau
  tebakan bank (F3-29 §7.3 tetap).
- Rute callback dipisah per bank (usulan `POST /api/v1/va/<bank>/callback`,
  bentuk final menunggu K-02 karena beberapa bank mensyaratkan path tetap).
- Satu bank nonaktif **tidak** menghalangi bank lain, dan tidak ada penerbitan
  silang otomatis.

---

## 7. Model data (usulan delta, **tanpa migrasi di kartu ini**)

Turunan dari F3-29 §7. Kolom yang **ditambah** karena kebijakan baru dicetak
miring; semuanya tetap **usulan** sampai F3-30.

```
va_accounts                                  -- satu baris per penerbitan VA
  id uuid pk, order_id uuid not null, versi int not null default 0,
  urutan int not null default 1,             -- penerbitan ke-N untuk versi itu (§5 butir 4)
  reff_id text not null,                     -- order:<order_id>:v<versi>[#<urutan>]
  provider text not null,                    -- 'bca-h2h' | 'bri-h2h'
  bank bank_code not null,                   -- enum yang sudah ada: BCA | BRI
  nomor_va text not null,
  nominal bigint not null check (nominal >= 0),
  status text not null,                      -- 'aktif' | 'kedaluwarsa' | 'dibatalkan' | 'dipakai'
  terbit_pada timestamptz not null,
  kedaluwarsa_pada timestamptz not null,     -- terbit_pada + 24 jam (K-04)
  dibatalkan_pada timestamptz,
  dibatalkan_alasan text,
  dibatalkan_di_bank boolean,                -- true hanya bila BatalVA berhasil (§3.2)
  raw jsonb, created_by uuid, created_at, updated_at
  unique (reff_id)
  unique (provider, nomor_va)
  unique (order_id, versi, urutan)
  -- hanya SATU VA aktif per (order_id, versi):
  unique (order_id, versi) where status = 'aktif'          -- partial unique index

va_callbacks                                 -- satu baris per callback MASUK
  id uuid pk, va_account_id uuid references va_accounts(id),
  provider text not null, kunci text not null, id_peristiwa text,
  nomor_va text not null, nominal bigint not null, waktu_bayar timestamptz not null,
  diterima_pada timestamptz not null default now(),
  status text not null,                      -- 'diterima' | 'duplikat' | 'ditolak' | 'perlu_review' | 'diabaikan'
  alasan text,                               -- kode mesin (§4.3) / sentinel galat
  hasil_nominal text,                        -- 'cocok' | 'kurang_bayar' | 'lebih_bayar'
  selisih bigint, raw jsonb,
  unique (provider, kunci)                   -- penjaga idempotensi lapis 3
```

Catatan:

- **Tidak ada kolom baru pada `orders`**; metode verifikasi tetap di
  `verification_logs.metode='virtual_account'` (enum sudah ada,
  `Database.md` §3).
- `bank_code` (BCA/BRI) **sudah ada** di enum sehingga dua bank terpisah tidak
  memerlukan enum baru.
- **Retensi** `va_accounts`/`va_callbacks`: **belum ditetapkan** (K-07). Tidak
  ada kebijakan retensi yang ditulis ke migrasi sebelum disetujui; usulan F3-29
  (5 tahun, setara bukti transfer) tetap berstatus usulan.
- **PII pembayar**: `raw` wajib lewat `SanitizeRaw` (F2-26) sebelum disimpan;
  nama pembayar tidak ditampilkan di portal cabang; akses dibatasi pada
  peran/permission yang sudah ada (+ audit SA), sesuai K-07.

---

## 8. Permukaan API & konfigurasi (usulan)

| Method | Path (usulan) | Akses | Catatan |
|---|---|---|---|
| POST | `/api/v1/va/bca/callback` | publik, **tanpa** sesi & CSRF, **rate limit tetap** | tanda tangan wajib; bentuk & path final menunggu K-02 |
| POST | `/api/v1/va/bri/callback` | idem | dipisah per bank (§6.3) |
| GET | `/api/v1/admin/orders/{id}/va` | Manager/SA | VA aktif + riwayat penerbitan (jejak, bukan instruksi bila nonaktif) |
| POST | `/api/v1/admin/orders/{id}/va/terbitkan` | SA (usulan) | terbitkan VA manual — **menunggu** keputusan siapa yang berhak |
| POST | `/api/v1/admin/va/{id}/batalkan` | SA (usulan) | pembatalan beralasan + audit (§5 butir 3b) |
| GET | `/api/v1/admin/va/rekonsiliasi` | SA (usulan) | daftar kerja harian Finance (K-10) — sebenarnya lingkup F3-31 |

Aturan transport yang sudah berlaku umum (`Architecture.md` §6,
`SystemDesign.md` §6): HTTPS + TLS valid, hanya POST untuk callback, tanpa data
di query string, bentuk error seragam
`{"error":{"code","message","fields"}}`, dan rahasia hanya dari
environment/SOPS+age.

---

## 9. Inventaris berkas & perubahan yang **diusulkan** (pekerjaan F3-30, bukan kartu ini)

| Berkas/area | Isi yang diusulkan | Catatan |
|---|---|---|
| `api/db/migrations/0008_va.sql` | `va_accounts`, `va_callbacks` (§7) + partial unique aktif | nomor `0008` berikutnya bebas (terakhir `0007`) |
| `api/db/queries/va.sql` | klaim kunci idempotensi, pencarian `(provider, nomor_va)`, tandai kedaluwarsa, tandai dibatalkan | lalu `sqlc generate` → `api/internal/db/sqlc/` |
| `api/internal/payment/va/postgres/` | repository VA: klaim + pencocokan dalam satu transaksi + `SELECT ... FOR UPDATE` | pola `payment/postgres` |
| `api/internal/payment/va/bca/`, `.../bri/` | adapter per bank + pemetaan tanda tangan dari dokumen resmi | **diblokir** sampai P-01..P-09 ada |
| `api/internal/payment/va/service.go` | orkestrasi callback (§4.2) + pemicu `payment.MarkVerified` | satu pintu Done |
| `api/internal/httpapi/handlers_va.go` + pendaftaran rute | rute §8, pengecualian CSRF, di luar guard sesi | `router.go` |
| `openapi/openapi.yaml` | entri rute VA | belum ada entri VA hari ini |
| `api/internal/settings/settings.go` | kunci `va.*` (§2.1) | belum ada; usulan |
| `cmd/server/main.go` | perakitan adapter + toggle | hanya bila diaktifkan |
| `web/` (admin + cabang) | tampilan VA aktif/riwayat + aksi batal | menunggu keputusan UI |
| `docs/design/f3-29/KONTRAK-VA.md` | catatan keputusan tertunda diselaraskan (histori dijaga) | **dikerjakan di kartu ini** |
| Matriks uji §10 | executable nanti | **dikerjakan di kartu ini** (sebagai spesifikasi) |

Tidak ada satu pun baris di atas yang dikerjakan di kartu ini.

---

## 10. Matriks test case (dapat langsung dipakai implementasi/QA)

Tingkat: **K** = uji kontrak `vatest.Uji` (tanpa DB/jaringan), **S** = uji unit
service/repository (DB palsu), **I** = uji integrasi PostgreSQL, **Q** = QA
manual (API hidup di lokal), **X** = uji sandbox bank (**butuh akses eksternal
— tidak dapat dijalankan sekarang**).

| # | Skenario | Harapan | Tingkat | Sumber |
|---|---|---|---|---|
| T-VA-01 | `DaftarVA` dengan `ReffID` sama dipanggil dua kali | nomor VA sama (idempoten), `Raw` JSON sah | K | K-03 |
| T-VA-02 | VA terbit untuk pesanan A (BCA) dan pesanan B (BRI) | nomor dari seri masing-masing; tidak tertukar | K/S | K-01 |
| T-VA-03 | `va.aktif=false` | provider **tidak dipanggil**, tanpa efek | K | K-06 |
| T-VA-04 | toggle bank A hidup, bank B mati | hanya BCA yang menerbitkan | S/I | K-01 |
| T-VA-05 | Terbitkan VA kedua untuk (order, versi) yang sudah punya VA aktif | ditolak (partial unique) | I | K-03 |
| T-VA-06 | `waktu_bayar` **tepat** `kedaluwarsa_pada` | on-time (batas inklusif) | S | K-04 |
| T-VA-07 | `waktu_bayar` = batas + 1 detik (job penanda sudah jalan), **callback tiba di dalam jendela replay** (asumsi §4.2 presedensi) | perlu review (V-10), **tanpa** Done | S/I | K-04 |
| T-VA-08 | Job penanda expiry (`Clock` maju 24 jam) | status `kedaluwarsa`, tidak mengubah pesanan | I | K-04 |
| T-VA-09 | Revisi invoice versi+1 saat VA aktif | VA lama `dibatalkan` + VA baru terbit | I | K-04 |
| T-VA-10 | Bank mendukung `BatalVA` (mock) | `dibatalkan_di_bank=true`, jejak audit ada | K/S | §3.2 |
| T-VA-11 | Bank **tidak** mendukung `BatalVA` | VA lama tetap bisa dibayar → setiap callback = perlu review | S/I | §3.2, V-10 |
| T-VA-12 | Callback sah, VA aktif, versi terkini, on-time, nominal persis (V-01) | pesanan Done, `verification_logs.metode='virtual_account'`, dokumen/email terjadwal sekali | I | K-03/K-04 |
| T-VA-13 | Nominal kurang (V-02) | `perlu_review` + alasan tertutup `kurang_bayar`, **tidak** Done | I | K-05 |
| T-VA-14 | Nominal lebih (V-03) | `perlu_review` + `lebih_bayar` | I | K-05 |
| T-VA-15 | Callback untuk VA **versi lama** setelah revisi (V-09) | `perlu_review` + kode mesin `va_versi_lama` | I | K-04 |
| T-VA-16 | Callback identik dua kali (V-04) | ack tanpa efek; tidak ada log/dokumen/email kedua | K/I | K-04 |
| T-VA-17 | Tanda tangan salah / `Raw` diubah setelah ditandatangani (V-05) | ditolak, **kunci tidak diklaim** (callback sah berikutnya tetap diterima) | K | F3-29 §5 |
| T-VA-18 | **Kedatangan telat** (V-06): callback tiba di luar jendela replay, apa pun `waktu_bayar`-nya | ditolak tanpa efek; **kunci tidak diklaim**; masuk daftar kerja rekonsiliasi | K | F3-29 §4.3 |
| T-VA-19 | `nomor_va` tidak dikenal (V-07) | ditolak + audit, tanpa efek | K/I | K-02 |
| T-VA-20 | Callback ke pesanan yang sudah Done (V-12) | tanpa efek, tanpa dokumen/email kedua | I | K-09 |
| T-VA-21 | Nominal pesanan melebihi **plafon** VA (bila plafon sudah ditetapkan, K-08) | VA tidak diterbitkan; jalur manual/bukti tetap | S | K-08 |
| T-VA-22 | Manual verifikasi selesai lebih dulu, callback VA menyusul (V-13) | status tidak berubah; tercatat untuk rekonsiliasi (potensi bayar ganda) | I | K-09/K-10 |
| T-VA-23 | Dua callback bersamaan kunci berbeda | satu menang; yang lain review; tidak ada dua transisi Done | I (race) | §6.2 |
| T-VA-24 | Toggle hidup tanpa provider | gagal terang-terangan (`KodeDitolak` + galat) | K/S | §2.1 |
| T-VA-25 | Rute callback diuji dari luar sesi | dapat diakses tanpa sesi/CSRF, hanya POST, rate limit tetap, tanpa data di query string | Q | `SystemDesign.md` §6 |
| T-VA-26 | Regresi jalur berjalan | verifikasi manual + OCR/AI + C&C **tidak** berubah; suite lama tetap lulus | I | K-06 |
| T-VA-27 | `raw` callback memuat nama/identitas pembayar | tersimpan tersanitasi; tidak tampil di portal cabang | S/I | K-07 |
| T-VA-28 | Audit tiap callback | satu baris `va_callbacks` + entri audit `va_payment`, dengan aktor/kode | I | K-07/K-09 |
| T-VA-29 | (X) Callback sandbox nyata bank | bentuk/header/tanda tangan cocok dengan dokumen resmi | X | K-02 — **butuh akses, belum ada** |
| T-VA-30 | (X) Cancel/generate di sandbox bank | perilaku cancel/generate bank sesuai asumsi §5 | X | K-04 — **butuh akses, belum ada** |
| T-VA-31 | **Bayar setelah kedaluwarsa** sementara baris `va_accounts` **masih `aktif`** (job penanda `Clock` belum jalan), **callback tiba di dalam jendela replay** (asumsi §4.2 presedensi): `waktu_bayar = kedaluwarsa_pada + 1 detik`, status `aktif` | `perlu_review` + kode mesin `va_bayar_setelah_kedaluwarsa` (V-11); **bukan** Done, status VA tidak boleh dianggap on-time | S/I | K-04, §5 butir 2 |
| T-VA-32 | **Kombinasi**: callback **kedatangan telat** **dan** **bayar setelah kedaluwarsa** — tiba di luar `va.jendela_replay_menit` sementara `waktu_bayar > kedaluwarsa_pada`. **Asumsi yang diuji berpasangan (§13 butir 6): stempel waktu bertanda tangan ≠ `waktu_bayar`** sehingga lapis (3) dan penilaian `waktu_bayar` benar-benar dua hal berbeda; bila dokumen bank menetapkan keduanya **sama** (berimpit), harapan baris ini **batal** dan hasilnya mengikuti §13 butir 6 (review manusia, bukan Done) | **V-06 menang** (§4.2 presedensi lapis): tolak **tanpa efek ke pesanan** — `perlu_review` **tidak** diset, tidak pernah Done; kunci peristiwa **tidak** diklaim; audit + daftar kerja rekonsiliasi harian | K | §4.2 presedensi, §13 butir 6 |

(Urutan baris tidak signifikan; `X` = uji sandbox bank yang **tidak dapat
dijalankan sekarang** karena belum ada akses.)

---

## 11. Keputusan **final** vs **menunggu** & kewajiban bukti

### 11.1 Final (sudah berlaku, tidak perlu ditanyakan lagi)

Tabel §1 baris ber-status FINAL: arsitektur dua adapter host-to-host tanpa
aggregator; tanda tangan hanya dari kontrak resmi; closed VA satu
pesanan/versi nominal tetap; **kedaluwarsa tepat 24 jam**; VA lama dibatalkan +
VA baru diterbitkan; callback **bayar setelah kedaluwarsa** (V-10/V-11; istilah
lama "terlambat" = definisi ini, bukan kedatangan telat
V-06) dan **VA lama** (V-09) → **review manusia** + audit; **duplikat** (V-04) =
ack "sudah diproses" **tanpa efek**; **kedatangan telat** (V-06) = **tolak tanpa
efek** + daftar rekonsiliasi — tidak ada auto-Done untuk kasus mana pun di sini;
kurang/lebih bayar **perlu review manusia**; VA mati (`va.aktif=false`) dan
wajib tetap mati; OCR/manual tetap jalur berjalan dengan ambang F2-26; C&C
tetap alur existing; minimisasi/sanitasi PII + pembatasan akses; verifikasi
manual sebagai pengecualian beralasan dengan penjagaan pembayaran ganda;
Finance PIC rekonsiliasi harian **ada** (nama orang + SLA menyusul); dua kantor
bank dibangun terpisah tanpa agregator; satu pintu Done `payment.MarkVerified`
dengan metode `manual`/`ai`/`virtual_account`.

### 11.2 Menunggu (gerbang manusia sebelum VA hidup)

| # | Yang menunggu | Harus diputuskan oleh | Buahnya |
|---|---|---|---|
| M-01 | Bentuk `ReffID` untuk penerbitan VA **ulang** pada versi sama (`#urutan` vs wajib naik versi) | User (mengubah kontrak yang sudah terkontrak-uji) | §5 butir 4 |
| M-02 | Jalur transisi Done untuk V-01: perluas otorisasi aktor sistem `virtual_account` **atau** V-01 hanya mengusulkan lalu admin menyetujui | User (menyentuh aturan keamanan `verifikatorSah`) | §6.1 butir 3 |
| M-03 | **Urutan aktivasi bank** (BCA dulu / BRI dulu / paralel) | User (K-01/F3-29 §9 K-01) | jadwal & provisioning |
| M-04 | **Retensi** `va_accounts`/`va_callbacks` dan batas paparan PII pembayar | User (K-07) | migrasi retensi + tampilan |
| M-05 | **Biaya/PPN/settlement + plafon + penandatangan** kontrak | User (K-08, dari penawaran resmi) | kelayakan bisnis, `T-VA-21` |
| M-06 | **Finance PIC + penggantinya + SLA** penanganan selisih | User (K-10) | runbook rekonsiliasi |
| M-07 | **Kebijakan VA untuk Cash & Carry** (`cnc_hari`, jatuh tempo, reminder) | User (K-06b) | apakah VA boleh untuk C&C |
| M-08 | Alasan review **khusus VA** muncul di UI (penambahan enum + label) atau cukup `lainnya` + kode mesin | User/desain UI | §4.3 |
| M-09 | Hak akses aksi admin VA (terbitkan/batalkan: SA saja atau Manager juga) | User (PRD §2.1) | §8 |
| M-10 | Kemampuan **cancel/generate** di sisi bank (butuh dokumen) | Bank (K-02/K-04) | §3.2, §5 |

### 11.3 Kewajiban bukti (evidence obligations) — per bank

Sebelum VA bank X boleh `aktif`, harus ada **bukti tertulis** berikut (bukan
pernyataan lisan, bukan mock):

| # | Bukti | Untuk | BCA | BRI |
|---|---|---|---|---|
| B-1 | Dokumen kontrak/spesifikasi API resmi (payload, header, tanda tangan, error) | K-02 | BELUM | BELUM |
| B-2 | Penawaran tertulis: biaya per transaksi, PPN, settlement, plafon, SLA | K-08 | BELUM | BELUM |
| B-3 | Kredensial sandbox + kunci + base URL + allowlist IP | panggilan keluar/masuk | BELUM | BELUM |
| B-4 | Hasil uji sandbox (**T-VA-29/T-VA-30**) dengan bukti keluaran nyata | kelayakan teknis | BELUM | BELUM |
| B-5 | URL callback HTTPS publik + sertifikat + IP statis terdaftar | prasyarat bank | BELUM | BELUM |
| B-6 | Runbook: provider mati, callback gagal, VA dibayar ganda, VA kedaluwarsa | operasi | BELUM | BELUM |
| B-7 | Persetujuan retensi + kebijakan PII pembayar | K-07 | BELUM | BELUM |
| B-8 | Finance PIC + SLA + prosedur rekonsiliasi harian | K-10 | BELUM | BELUM |
| B-9 | Persetujuan user atas §11.2 | gerbang manusia | BELUM | BELUM |

Tanpa B-1..B-9 **per bank**, F3-30 wajib **memblokir** dirinya sendiri untuk
bank itu (bukan mengarang adapter).

---

## 12. Self-review: konsistensi dengan PRD/F3-29 dan keberlangsungan OCR/manual

| Pemeriksaan | Hasil |
|---|---|
| PRD §5 tiga mode berdampingan (manual / AI-OCR / VA) | Selaras: VA mati; manual & AI tidak berubah; satu pintu Done sama |
| PRD §5 "VA: callback bank menandai pembayaran lunas tanpa unggah bukti" | Selaras untuk V-01 saja; semua kasus lain → review (K-04/K-05) |
| PRD §7.1 state machine (Done hanya dari Submitted; revisi → versi+1) | Selaras: VA tidak menambah status pesanan; VA lama setelah revisi → review |
| PRD §8 no.10 (bank mana & host-to-host vs gateway) | Terjawab sebagian: host-to-host langsung, dua adapter; **urutan bank masih menunggu** |
| PRD §10 Fase 3 "VA BCA (lalu BRI)" | Tidak dibantah; arsitektur disiapkan untuk keduanya, urutan tetap menunggu |
| `Architecture.md` §2.2 (transaksi eksplisit, uang BIGINT, UTC + WIB bisnis) | Selaras: §5/§7 (integer Rupiah, `timestamptz`, WIB hanya tampilan) |
| `Architecture.md` §6 (rahasia tidak masuk repo, rate limit, CSRF) | Selaras: §8 (tanpa sesi/CSRF untuk callback, rate limit tetap, rahasia via env/SOPS) |
| `SystemDesign.md` §4.3 (satu pintu `MarkVerified`) | Selaras: §6.1; tambahan fakta kode: aktor sistem saat ini hanya untuk `ai` (M-02) |
| `SystemDesign.md` §5.3 (idempotensi) & §5.4 (audit) | Selaras: §4.2 (klaim kunci + audit dalam transaksi) |
| `SystemDesign.md` §6 (bentuk error seragam) | Selaras: §8 |
| `Database.md` §1/§3/§4.5 (konvensi, enum, tabel pembayaran) | Selaras: §7 (tanpa kolom baru di `orders`, enum `verify_method`/`bank_code`/`review_reason` dipakai apa adanya) |
| F3-29 `KONTRAK-VA.md` §3 (kontrak provider murni, idempotensi `ReffID`) | Selaras: §3 (dipakai apa adanya + kemampuan `BatalVA` opsional, bukan payload karangan) |
| F3-29 `KONTRAK-VA.md` §6.4 (VA versi lama dibayar setelah revisi) | **Diselaraskan:** penanda pembaruan F3-29b ditambahkan **di tempatnya** (pola §2.4); alasan review yang berlaku = **`lainnya`** + kode mesin `va_versi_lama` (§4.2 V-09, §4.3) — rumusan lama `lebih_bayar`/`kurang_bayar` ditandai **tidak berlaku lagi**, dan pemeriksaan ini masuk tabel §12 (baris ini) |
| Definisi tunggal istilah waktu (§4.2) | **Selaras/tertutup:** "kedatangan telat" (V-06) dan "bayar setelah kedaluwarsa" (V-10/V-11) didefinisikan terpisah; tidak ada lagi dua baris dengan trigger sama dan efek berbeda; nasib kunci idempotensi dinyatakan eksplisit (V-06 **tidak** mengklaim kunci, V-05 juga tidak) |
| Celah "lewatnya 24 jam" vs "jalannya job penanda" | **Tertutup:** V-11 menangani `waktu_bayar > kedaluwarsa_pada` selagi status masih `aktif` → `perlu review` (bukan Done); §5 butir 2 kini berbasis **waktu**, bukan status |
| Presedensi baris tabel keputusan (§4.2) | **Tertutup:** aturan lintas-baris "jendela waktu dievaluasi lebih dahulu" ditambahkan — **V-06 menang atas V-10/V-11**; V-10/V-11 hanya berlaku untuk callback yang tiba di dalam jendela; T-VA-07/T-VA-31 menyatakan asumsi jendela, dan **T-VA-32** menguji kombinasi kedatangan telat + bayar setelah kedaluwarsa — **T-VA-32 kini juga menyatakan asumsinya** (stempel waktu bertanda tangan ≠ `waktu_bayar`; bila berimpit → mengikuti §13 butir 6, review bukan Done) |
| Atribusi efek `duplikat` (V-04) di `KONTRAK-VA.md` §1 K-04 / §9.1 (baris K-04) / §10 (baris F3-30) | **Diselaraskan:** ketiga tempat menyebut efek **per kasus** dan **tidak** lagi menaruh `duplikat` di ember `perlu_review` — duplikat = ack "sudah diproses", **tanpa efek**; `perlu_review` hanya bayar setelah kedaluwarsa (V-10/V-11), VA lama/versi lama (V-09/V-10), pesanan sudah Done (V-12/V-13); kedatangan telat (V-06) = tolak tanpa efek + daftar rekonsiliasi. Konsisten dengan V-04, `KONTRAK-VA` §5/§5.1 butir 4/§6.2 |
| `KONTRAK-VA.md` §5 (alur balasan + paragraf Pembaruan F3-29b) | **Diselaraskan:** efek disebut **terpisah per kasus** — kedatangan telat (V-06) = tolak tanpa efek + daftar rekonsiliasi; bayar-setelah-kedaluwarsa/VA lama/pesanan sudah Done = `perlu_review`; duplikat (V-04) = ack tanpa efek; tidak ada lagi satu ember "audit + `perlu_review`" |
| F3-29 §2.4 rekomendasi piloting aggregator | **Digantikan K-01**; ditandai di `KONTRAK-VA.md` (histori dijaga) |
| F3-29 §7.4 `va.kedaluwarsa_jam=24` | Naik status menjadi keputusan (K-04); tabel §2.1 menambah toggle per bank (usulan) |
| OCR/manual tetap berjalan (K-06) | Tidak ada perubahan yang menyentuh `payment`, `ocr`, `verify`, `worker`, atau route yang ada; `va` tetap tidak diimpor di luar pohonnya |
| C&C (K-06b) | Tidak disentuh: VA **tidak** otomatis berlaku untuk C&C sampai diputuskan (M-07); reminder/jatuh tempo tetap |
| Klaim sandbox/OCR | Tidak ada klaim: §0/§3.3/§11.3 menyatakan BELUM; `T-VA-29/30` ditandai butuh akses eksternal |

### 12.1 Fakta kode yang diverifikasi untuk dokumen ini (tanpa menjalankan apa pun)

- `api/internal/payment/verify.go`: `MetodeManual`/`MetodeAI`/
  `MetodeVirtualAccount` ada; `verifikatorSah` menerima aktor sistem **hanya**
  untuk `ai` (dasar §6.1 butir 3, M-02).
- `api/internal/payment/verify.go`: daftar tertutup alasan review = 5 nilai
  (dasar §4.3).
- `api/internal/settings/settings.go`: belum ada kunci `va.*` (dasar §2.1).
- `api/db/migrations/`: berkas terakhir `0007_cnc_reminders_sent_at.sql`
  (dasar nomor `0008` di §9).
- `api/internal/payment/va/` (dari F3-29): `va.go`, `signature.go`,
  `signature_rsa.go`, `mock.go` + uji `va_test.go`, `signature_rsa_test.go`
  + paket `vatest` (`contract.go`, `contract_test.go`) — dipakai apa adanya,
  tidak diubah.

---

## 13. Catatan asumsi (dinyatakan, bukan disembunyikan)

1. **Auto-Done pada kasus pasti (V-01)** disimpulkan dari PRD §5 + K-04 + K-05
   (yang eksplisit menyebut kasus **tidak pasti** sebagai review). Bila user
   menghendaki VA **tidak pernah** auto-Done, itu perubahan kebijakan dan harus
   dinyatakan — dan §6.1 M-02 opsi (b) sudah menyiapkan jalur itu.
2. **Pencocokan nominal VA = eksak (`selisih = 0`)** karena K-03 menetapkan
   nominal tetap pada closed VA. F3-29 §6.1/§7.4 menyebut pemakaian
   `ai.tol_kurang`/`ai.tol_lebih` (bawaan `0/0`, jadi praktis sama). Selisih
   non-nol = review pada kedua bacaan; **penyelarasan eksplisit menunggu**
   (masuk M-05/M-08 saat F3-30 dikerjakan).
3. **Nama setelan/kunci/properti** (`va.aktif`, `va.<bank>.aktif`, kolom
   `status`/`urutan`/`dibatalkan_*`) adalah **usulan** desain ini; tidak ada
   kode/setelan yang berubah di kartu ini.
4. **Path rute** `/api/v1/va/<bank>/callback` adalah usulan; bentuk final bisa
   berbeda bila bank mensyaratkan path tetap (K-02).
5. **Satu pesanan satu VA aktif satu bank** adalah turunan K-03; bila user ingin
   memperbolehkan VA di dua bank sekaligus untuk satu pesanan, itu keputusan
   baru dan berisiko pembayaran ganda.
6. **Semantik stempel waktu bertanda tangan bank** (K-02) menentukan apakah lapis
   jendela replay dan `waktu_bayar` benar-benar dua hal terpisah. Presedensi §4.2
   mengasumsikan keduanya **dapat** berbeda (stempel waktu = waktu kirim/tanda
   tangan; `waktu_bayar` = waktu uang dibayar). Bila dokumen bank menetapkan
   keduanya sama sehingga berimpit, hasilnya tetap **`perlu review`, tidak pernah
   Done** — bukan auto-Done dan bukan "tanpa efek" yang menyembunyikan uang
   masuk; kode mesin yang dipakai ditentukan saat pemetaan dokumen bank (F3-30).
   Asumsi ini dinyatakan **berpasangan** di matriks uji: **T-VA-32** (kombinasi
   kedatangan telat + bayar setelah kedaluwarsa) hanya berlaku bila stempel waktu
   bertanda tangan **≠** `waktu_bayar`; bila sama, harapan T-VA-32 batal dan
   berlakulah butir ini (`perlu review`).

---

## 14. Rujukan

- `docs/PRD.md` §5, §7.1, §8 no.10, §10; `docs/Architecture.md` §2.2, §6;
  `docs/SystemDesign.md` §4.3, §5.3–§5.5, §6; `docs/Database.md` §1, §3, §4.5.
- `docs/design/f3-29/KONTRAK-VA.md` (kontrak provider, tanda tangan,
  idempotensi, model data usulan, checklist provisioning, gerbang K-01..K-10 —
  §9 diselaraskan oleh kartu ini).
- `docs/design/f3-29/HASIL.md` (bukti uji kontrak F3-29).
- `docs/design/f2-26/HASIL.md` (kebijakan ambang keyakinan OCR/AI yang tetap
  berlaku untuk jalur berjalan, K-06).
