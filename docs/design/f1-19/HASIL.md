# F1-19 — Beranda, riwayat invoice dan tutorial statis cabang

Kartu: **t_ba2b541f — F1-19 Beranda, riwayat invoice dan tutorial statis cabang**

Dokumen acuan: PRD §3.3 (beranda), §3.8 (riwayat invoice, tutorial), §3.5–3.7
(aturan pesanan/bukti/sertifikat/C&C), §3.10 CP-ORD-10; SystemDesign.md §6
(permukaan API), §7.1–7.2 (peta layar → komponen, aturan UI), §4.5 (revisi
invoice), §8 (akses dokumen lewat presigned URL); Database.md §4.4 (`orders`),
§4.6 (`documents`, `invoice_revisions`). Prototype acuan UI:
`docs/prototype/customer_v2.html` (`pBeranda`, `pInvoice`, `pTutorial`).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Backend (perluasan domain dokumen F1-17 — riwayat invoice adalah daftar
DOKUMEN invoice + pesanan ber-nomor yang dokumennya belum ada):

| Method | Path | Peran | Catatan |
|---|---|---|---|
| GET | `/api/v1/invoices` | akun cabang | riwayat invoice cabang SESI: nomor, tanggal, versi, total, status, ketersediaan PDF; filter `?dari=&sampai=` (YYYY-MM-DD, inklusif) |

Berkas: `api/db/queries/document.sql` (`ListInvoicesForBranch`) + hasil sqlc
`internal/db/sqlc/document.sql.go`, `internal/document/document.go`
(`InvoiceRow`, `InvoiceFilter`, `Service.Invoices`, `ErrNoBranch`,
`ErrFilterInvalid`), `internal/document/postgres/postgres.go`
(`Repo.ListInvoices`), `internal/document/memory.go` (cermin untuk uji),
`internal/httpapi/handlers_documents.go` (`invoices`), `router.go`,
`errors.go`, `openapi/openapi.yaml` (path + schema `InvoiceRow`).

Frontend portal cabang:

- `/` (beranda) — `pages/beranda.tsx`: ringkasan periode berjalan (jumlah
  pesanan, total belanja, pesanan selesai, selisih vs bulan lalu), jumlah
  Submitted yang menunggu verifikasi, jumlah draft, pengingat C&C yang
  mendekati/melewati jatuh tempo, pintasan "Buat pesanan", dan tabel 5 pesanan
  terakhir.
- `/invoice` — `pages/invoice.tsx`: daftar invoice dengan filter periode di URL
  search param, kolom versi (Asli / Revisi N / versi lama), status, dan tombol
  **Unduh** yang memakai presigned URL. Invoice yang belum selesai tampil
  dengan tombol nonaktif "Belum tersedia".
- `/tutorial` — `pages/tutorial.tsx`: empat topik bergambar (cara memesan,
  cara unggah bukti transfer, aturan Sertifikat, aturan Cash & Carry) dengan
  ilustrasi SVG inline, isi langkah mengikuti prototype dan PRD.

`pages/shell-pages.tsx` kini hanya berisi katalog (F1-08) dan profil (F1-10);
`routes.tsx` memetakan rute/qr komponen baru dan menambah search param periode
pada rute `/invoice`.

## Keputusan desain

1. **Riwayat invoice dibaca dari domain dokumen, bukan endpoint agregat baru.**
   Satu baris = satu DOKUMEN invoice (`jenis = 'invoice_pdf'`) milik pesanan
   cabang SESI. Karena revisi invoice menghasilkan dokumen versi baru tanpa
   menimpa berkas lama (F1-17/F2-25), **versi lama tetap tampil** dan tetap
   dapat diunduh — janji kartu "versi lama tetap dapat dilihat setelah revisi"
   terpenuhi tanpa rilis baru di UI. Field `terbaru` menandai baris yang
   versinya sama dengan `orders.versi`.
   Query-nya ada di `document/postgres` dengan pola yang sama seperti
   `OrderForDocument` (paket ini sudah membaca `orders`/`order_lines`): tidak
   ada ketergantungan paket antar-domain (SystemDesign.md §2).
2. **Invoice yang belum Done tetap tampil, tetapi TIDAK bisa diunduh.**
   Pesanan ber-nomor yang dokumennya belum ada (masih `submitted`, atau render
   dokumen belum selesai) muncul satu baris dengan `versi_dokumen = null`,
   `dokumen_id` kosong, dan `dapat_diunduh = false`. Di server, dokumen memang
   hanya lahir untuk pesanan Done (`document.Ensure` → `409 ORDER_NOT_DONE`,
   F1-17/F1-18), jadi tidak ada berkas yang bisa diunduh walau id-nya ditebak.
   `dapat_diunduh` dihitung service (`dokumen_id != "" && status == done`),
   bukan dipercayakan ke UI.
3. **Isolasi cabang ditegakkan di layer service, bukan UI.** `Service.Invoices`
   menolak aktor non-cabang (`ErrForbidden` → `403`) dan akun cabang tanpa
   cabang (`ErrNoBranch` → `409 BRANCH_MISSING`); `branch_id` selalu dari SESI
   (`Actor.BranchID`), tidak ada satu pun parameter klien yang bisa menunjuk
   cabang lain. Unduhan dokumen juga tetap diperiksa ulang di F1-17
   (`CapInvoiceOwn` + kepemilikan pesanan), sehingga percobaan lintas cabang
   dijawab `404`.
4. **Filter periode inklusif dan divalidasi, bukan diam-diam kosong.** Filter
   memakai `orders.tanggal` (tanggal bisnis WIB, BR-04) dan inklusif di kedua
   ujungnya. Format yang tidak dikenal atau rentang terbalik dijawab
   `400 VALIDATION_FAILED` (`document.ErrFilterInvalid`) supaya UI tidak
   menampilkan daftar kosong karena salah tulis tanggal. Tidak ada batas jumlah
   baris (daftar invoice satu cabang kecil dan sudah terurut tanggal menurun).
5. **Beranda memakai SATU sumber data yang sudah ada (GET `/orders`).**
   Seluruh angka dihitung klien dari daftar pesanan cabang sesi — himpunan yang
   sama dengan halaman Pesanan — jadi "jumlah/status sesuai data DB" tanpa
   endpoint agregat baru yang bisa menyimpang dari daftar tersebut (prototype
   juga menghitung di klien). Ringkasan periode memakai bulan berjalan lokal
   pengguna dan `orders.tanggal` (tanggal bisnis), sehingga pesanan bulan lalu
   tidak pernah ikut terhitung.
6. **Pengingat C&C hanya tampilan; tidak ada auto-Done.** Beranda memilih
   pesanan `is_cnc && status == submitted` dengan `jatuh_tempo` terdekat dan
   menampilkan "N hari lagi / hari ini / lewat jatuh tempo N hari". Statusnya
   tidak pernah diubah dari sini: C&C baru menjadi Done setelah Pusat
   memverifikasi pembayarannya (PRD CP-CNC-07, F1-18). Uji UI memastikan
   seluruh panggilan beranda adalah GET dan pesanan C&C yang lewat jatuh tempo
   tetap berlabel "Menunggu verifikasi".
7. **Unduhan invoice lewat presigned URL berumur pendek.** Tombol Unduh
   memanggil `GET /api/v1/documents/{id}/download` (300 detik) lalu membuka URL
   yang dikembalikan; UI tidak pernah menyusun URL object storage sendiri dan
   kunci storage tetap tidak pernah dikirim ke klien (SystemDesign.md §8).
   `dokumen_id` disertakan pada daftar invoice supaya satu klik = satu
   permintaan tautan (tanpa permintaan daftar dokumen per baris).
8. **Filter periode hidup di URL.** `/invoice?dari=&sampai=` divalidasi router
   (`validateSearch`) dan menjadi satu-satunya sumber filter, sehingga halaman
   ini bisa di-bookmark/di-share dan tombol Terapkan/Reset hanya mengubah URL
   (SystemDesign.md §7.2). Tabel memakai `Table mobileCards`, jadi di mobile
   setiap baris menjadi kartu seperti prototype.
9. **Tutorial: empat topik, kontennya bukan keputusan baru.**
   `pages/tutorial.tsx` memuat empat topik yang diminta kartu — memesan, bukti
   transfer, Sertifikat, C&C — dengan langkah yang disalin dari prototype
   (`TUT`) dan aturan PRD §3.5–3.7 (termasuk "Nominal seluruh bukti dijumlahkan
   saat verifikasi", "harga Sertifikat Rp0 + biaya packaging otomatis",
   "jatuh tempo = tanggal pesanan + cnc_hari"). Ilustrasi SVG **inline**
   (bukan berkas/CDN) agar tetap tampil tanpa aset eksternal dan ikut
   menyesuaikan lebar; di <560px gambar pindah ke atas teks. Setiap kartu
   topik memuat judul + ringkasan singkat dan satu accordion berlabel
   "Langkah-langkah" (judulnya tidak diulang di accordion). Isi panduan tetap
   teks biasa sehingga terbaca pembaca layar dan tidak bergantung pada gambar.
10. **Tidak ada tabel/migrasi baru.** Riwayat invoice memakai `orders` +
   `documents` yang sudah ada (Database.md §4.4/§4.6); tidak ada kolom baru,
   tidak ada perubahan skema, dan tidak ada data yang disimpan klien.

## Penyimpangan / keputusan yang perlu dicatat

- **`GET /api/v1/invoices` belum ada di SystemDesign §6** (ringkasan permukaan
  API menyebutnya sebagai "Riwayat invoice" tanpa path eksplisit, sedangkan
  kartu F1-19 menyebut `GET /api/v1/invoices`). Endpoint ini didokumentasikan
  lengkap di `openapi/openapi.yaml` dan dijaga uji dua arah
  (`TestKontrakOpenAPIIkutRouter`).
- **Tidak ada endpoint agregat untuk beranda.** SystemDesign §6 tidak memuat
  dashboard cabang; beranda memakai `GET /orders` + sesi login. Konsekuensinya
  beranda menerima seluruh daftar pesanan cabang (halaman Pesanan juga begitu,
  tanpa paginasi).
- **Kolom "versi" pada daftar invoice adalah versi DOKUMEN, bukan versi
  pesanan.** Satu pesanan yang sudah direvisi tampil beberapa kali (v1 terbaru,
  v0 sebagai "versi lama"). Ini yang membuat versi lama tetap terlihat dan
  tetap dapat diunduh tanpa perlu layar terpisah.
- **`ukuran_byte` dikirim apa adanya** (0 bila baris belum punya dokumen);
  UI belum memakainya untuk ditampilkan, hanya tersedia bila nanti mau
  ditampilkan.
- **Koreksi skema OpenAPI saat QA F2-24 menemukan galat `struct`.** Versi kerja
  pertama menulis `versi_dokumen: { type: integer, nullable: true }` (gaya
  OpenAPI 3.0) padahal spec ini 3.1, sehingga `redocly lint` gagal pada working
  tree saat QA F2-24 memeriksa commit `a6a8718`. Sudah diperbaiki menjadi
  `type: [integer, "null"]` (pola yang sudah dipakai spec ini), dan validasi
  ulang pada commit beku `3d316fa` maupun pada spec `a6a8718` sama-sama
  **valid, 10 warning (baseline)**.
- **Tutorial masih statis di bundel SPA.** PRD §3.8 menyebut konten tutorial
  "dikelola sebagai halaman statis yang bisa diperbarui tanpa deploy ulang
  (fase 2)"; F1-19 adalah fase 1 sehingga konten hidup di kode. Tidak ada
  endpoint CMS.
- **Beranda tidak menampilkan grafik mini/top product prototype.** Kartu
  F1-19 meminta ringkasan periode, Submitted, draft, pengingat C&C, dan 5
  pesanan terakhir; top product 3 bulan butuh baris pesanan (`order_lines`)
  yang tidak ada di daftar pesanan, jadi sengaja tidak dikerjakan di sini.

## Bukti verifikasi

Semua perintah dijalankan dari `/home/hermes/projects/grahita-website`.

Gerbang backend (`api/`):

| Perintah | Hasil |
|---|---|
| `gofmt -l .` | kosong |
| `go vet ./...` | `exit 0` |
| `go build ./...` | `exit 0` |
| `sqlc-v1.31.1 generate` | 0 diff (md5 `internal/db/sqlc/document.sql.go` sebelum == sesudah: `3662f01a…`) |
| `bash scripts/check-secrets.sh` | `exit 0` — "Tidak ada rahasia terdeteksi" |
| `redocly lint openapi/openapi.yaml` | valid, 10 warning (baseline) — diverifikasi pada commit beku `3d316fa` DAN pada spec induk `a6a8718` |
| `go test -race -p 1 -count=1 ./...` (DB `grahita_f119` + shim) | `SUITE_EXIT=0`, **21 paket ok, 0 FAIL, 0 SKIP** (`httpapi` 584 s; `document` 4,4 s) — dijalankan dua kali, log `cache/scratch/f119-suite-full.log` |

Uji fokus F1-19:

- `go test ./internal/document/ -run TestInvoices -count=1`: 4/4 PASS —
  `TestInvoicesTerbatasCabangSesi` (hanya cabang sesi; admin `403`; akun tanpa
  cabang `ErrNoBranch`), `TestInvoicesVersiLamaTetapTampil` (v0 tetap tampil
  setelah revisi + `terbaru` hanya v1 + invoice Submitted tanpa dokumen),
  `TestInvoicesFilterPeriode` (inklusif dua ujung + format salah/rentang
  terbalik → `ErrFilterInvalid`), `TestInvoicesTidakMembocorkanKunciStorage`.
- `go test ./internal/httpapi/ -run TestPGInvoicesRiwayatCabang -count=1`
  (PostgreSQL 16.15 nyata, skema bersih per uji, renderer uji tanpa mesin PDF):
  PASS. Yang dibuktikan lewat HTTP: jumlah baris sesuai data DB (3 pesanan
  ber-nomor cabang A; nomor cabang B tidak pernah muncul; `jumlah_item` = Σ qty
  baris produk = 3); invoice Done `dapat_diunduh` true dengan `dokumen_id` yang
  benar-benar bisa diunduh (`200`); cabang lain `404`; invoice Submitted tanpa
  dokumen + `dapat_diunduh` false + render paksa `409 ORDER_NOT_DONE`; filter
  periode `2026-10-01..2026-10-31` menyisakan 2 baris dan batas `2026-09-30`
  inklusif; tiga bentuk filter salah → `400 VALIDATION_FAILED`; setelah revisi
  (`orders.versi = 1` + `EnsureInvoice`) daftar memuat v1 DAN v0 dengan
  `terbaru` hanya di v1 dan kedua berkas masih bisa diunduh; akun Pusat pada
  `/invoices` dijawab `403`.
- Ukuran suites/`benchmark` tidak ada; `-count=1` dipakai agar hasil bukan
  cache.

Gerbang frontend (`web/`):

| Perintah | Hasil |
|---|---|
| `npm run typecheck` (4 workspace) | `exit 0` |
| `npm test` | `exit 0` — 185 uji lulus (api-client 8, ui 64, admin 60, customer 53); dijalankan tiga kali dengan hasil sama |
| `npm run build` | `exit 0` (kedua SPA) |

Uji vitest baru (semuanya lulus):

- `apps/customer/src/beranda.test.tsx` — 3 uji: ringkasan periode (2 pesanan
  bulan ini, Rp500.000, 1 menunggu verifikasi, 1 draft) dengan pengingat
  "C&C lewat jatuh tempo" yang pesanannya TETAP "Menunggu verifikasi"; keadaan
  kosong; galat API ditampilkan tanpa angka palsu. Juga memastikan seluruh
  panggilan beranda adalah GET ke `/api/v1/orders` tanpa parameter cabang.
- `apps/customer/src/invoice.test.tsx` — 4 uji: baris invoice + "Revisi 1" +
  "versi lama" + tombol "Belum tersedia" nonaktif untuk pesanan yang belum
  selesai; unduhan memanggil `/documents/d-1/download` dan membuka URL presigned
  (UI tidak pernah menyusun URL `/files/...`); filter periode dari URL dipakai
  ke API dan tombol Terapkan mengirim rentang baru; galat validasi periode dari
  server ditampilkan.
- `apps/customer/src/tutorial.test.tsx` — 2 uji: empat topik + empat ilustrasi
  (`data-testid="ilustrasi-topik"`) + accordion pertama terbuka; langkah-langkah
  wajib ada sebagai teks (memesan, bukti transfer, Sertifikat, C&C).
- `apps/customer/src/App.test.tsx` disesuaikan: rute `/tutorial` kini diuji
  dengan empat topik baru (dulu mengecek kerangka F1-03).

Pemeriksaan browser nyata (SPA dilayani `vite dev` :5173 dengan API TIRUAN di
:8088 — data contoh, bukan PostgreSQL; tujuannya memeriksa tata letak/HTML
yang dihasilkan, bukan alur server). Enam tangkapan layar disimpan di luar repo,
di workspace scratch profil: `cache/scratch/f119-shots/f119-{beranda,invoice,tutorial}-{desktop,mobile}-{1440x900,390x844}.png`.

Nilai yang saya ukur sendiri lewat CDP (`getComputedStyle`) dan isi halaman:

- desktop 1440×900: `Beranda` menampilkan KPI "Pesanan bulan ini 3", "Total
  belanja bulan ini Rp 1.900.000", "Menunggu verifikasi 2", "Draft 1", banner
  C&C ("jatuh tempo hari ini" karena tanggal contoh = hari berjalan), tabel 5
  pesanan terakhir (baris Draft + 4 ber-nomor); `/invoice` menampilkan 4 baris
  dengan "Revisi 1", "Asli versi lama", "Belum tersedia" (tombol `disabled`
  hanya pada baris Submitted) dan 3 tombol Unduh aktif; `/tutorial`
  menampilkan 4 kartu topik + 4 ilustrasi + 4 accordion (pertama terbuka).
  Tidak ada overflow horizontal pada ketiga halaman.
- mobile 390×844: sidebar `display: none`, bottom-nav tampil, KPI menjadi
  deretan geser (`.kpis` `display: flex`, `scrollWidth > clientWidth`), baris
  tabel menjadi kartu (`tr` `display: grid`), ilustrasi tutorial 240 px dengan
  kepala kartu `flex-direction: column`, dan `scrollWidth - innerWidth = 0`
  pada ketiga halaman.

Catatan kejujuran: pada satu run `npm test` yang berjalan BARENGAN dengan suite
Go `-race` di mesin yang sama, satu uji customer timeout (1 dari 53) dan tidak
muncul lagi pada tiga run berikutnya (termasuk dua run penuh berurutan).
Saya tidak bisa mereproduksinya, jadi saya catat sebagai flake beban mesin —
bukan klaim "bersih tanpa catatan" untuk run itu.

## Risiko residu

- Uji integrasi memakai PostgreSQL 16.15 + shim `uuidv7()` dan renderer tiruan
  (bukan Gotenberg/S3 nyata); batas bukti ini sama dengan F1-17/F1-18 dan
  tercakup gate T4/T5.
- Pesanan Done yang render dokumennya gagal (job River `discarded`, catatan
  F1-18) akan tampil di riwayat sebagai "Belum tersedia" walau sudah selesai —
  tidak ada jalur pemulihan dari portal cabang (admin punya
  `POST /admin/orders/{id}/documents`). Ini keterbatasan yang sudah diketahui,
  bukan regresi kartu ini.
- Beranda mengambil seluruh daftar pesanan cabang; untuk cabang dengan riwayat
  sangat panjang kelak perlu paginasi/agregat server (belum jadi masalah pada
  volume sekarang).
- Ilustrasi tutorial adalah SVG hias (bukan tangkapan layar aplikasi); PRD
  fase 2 (konten statis yang bisa diperbarui tanpa deploy) belum dikerjakan.

## Kewajiban untuk kartu turunan

- **E2E/T4–T5**: uji browser nyata untuk beranda + invoice disarankan memakai
  akun cabang dengan riwayat lintas bulan dan satu pesanan C&C lewat jatuh
  tempo; pastikan tombol Unduh benar-benar mengunduh berkas (ambil URL
  presigned lalu periksa byte, bukan hanya HTTP 200).
- **F2-25 (revisi invoice)**: setelah versi pesanan naik, `GET /invoices`
  otomatis menampilkan baris baru tanpa perubahan kode — pastikan revisi
  memanggil `document.EnsureInvoice` supaya berkas versi baru benar-benar ada
  (kalau tidak, baris v1 muncul sebagai "Belum tersedia").
- **F2-26/F2-XX pengingat C&C**: beranda hanya menampilkan pengingat; email
  H-2/H-0/H+1/H+3 tetap milik job notifikasi.

## Hotspot

`api/internal/document/document.go`, `api/internal/document/postgres/postgres.go`,
`api/db/queries/document.sql`, `api/internal/db/sqlc/document.sql.go`,
`openapi/openapi.yaml`, `web/apps/customer/src/routes.tsx`,
`web/apps/customer/src/pages/shell-pages.tsx` dipakai bersama F1-17/F1-18/F2-25
dan kartu portal cabang lain.
