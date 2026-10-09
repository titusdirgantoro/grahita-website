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
   juga menghitung di klien). Ringkasan periode memakai bulan **tanggal bisnis
   WIB** yang dihitung sendiri di klien (`hariIni()` = UTC+7, bukan zona waktu
   browser) dan `orders.tanggal` (tanggal bisnis), sehingga pesanan bulan lalu
   tidak pernah ikut terhitung dan angka tidak bergeser bagi pengguna yang
   membuka portal dari zona waktu lain dekat pergantian hari/bulan.
6. **Pengingat C&C hanya tampilan; tidak ada auto-Done.** Beranda memilih
   pesanan `is_cnc && status == submitted` dengan `jatuh_tempo` terdekat
   (kolom itu dibawa `GET /orders`; perbaikan putaran 2 di bawah) dan
   menampilkan "N hari lagi / hari ini / lewat jatuh tempo N hari". Statusnya
   tidak pernah diubah dari sini: C&C baru menjadi Done setelah Pusat
   memverifikasi pembayarannya (PRD CP-CNC-07, F1-18). Uji UI memastikan
   seluruh panggilan beranda adalah GET dan pesanan C&C yang lewat jatuh tempo
   tetap berlabel "Menunggu verifikasi"; urutan "5 pesanan terakhir"
   mengikuti urutan server apa adanya tanpa disortir ulang di klien.
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

### Putaran 2 — perbaikan dari review QA (putaran 1, lensa artifact)

QA memblokir karena **pengingat C&C di beranda tidak akan pernah tampil pada
data nyata**: `beranda.tsx` mensyaratkan `o.jatuh_tempo`, tetapi sumber datanya
`GET /api/v1/orders` tidak pernah mengirim field itu (`ListOrdersForBranch`
tidak memilih `o.jatuh_tempo` dan `Repo.List` tidak mengisi `Order.JatuhTempo`),
padahal `Order.jatuh_tempo` sudah didokumentasikan di OpenAPI — drift kontrak.
Pemeriksaan browser putaran 1 memakai API tiruan yang menyuntikkan `jatuh_tempo`
sehingga celah ini tersembunyi.

Perbaikan (pilihan a dari QA — membawa jatuh tempo sampai ke beranda):

- `api/db/queries/order.sql` → `ListOrdersForBranch` kini memilih `o.jatuh_tempo`
  (menyamai `GetOrderForBranch`); `sqlc generate` dijalankan ulang
  (`internal/db/sqlc/order.sql.go` bertambah `JatuhTempo *time.Time`).
- `api/internal/order/postgres/postgres.go` → `Repo.List` mengisi
  `JatuhTempo: tanggalOr(row.JatuhTempo)` (menyamai `loadOrder`).
- **Uji integrasi PostgreSQL nyata baru**
  `api/internal/httpapi/integration_beranda_pg_test.go`
  (`TestPGBerandaPengingatCNCOverdueDariResponsNyata`): pesanan C&C dibuat lewat
  alur submit nyata, tanggalnya digeser ke masa lalu (jatuh tempo terlewat),
  lalu `GET /api/v1/orders` harus (1) membawa `jatuh_tempo` yang sama dengan
  kolom DB, (2) tetap `status=submitted` tanpa auto-Done, (3) memenuhi prasyarat
  pengingat (`is_cnc && submitted && jatuh_tempo && sisa < 0`) dengan judul
  "C&C lewat jatuh tempo 3 hari", (4) tidak membocorkan `jatuh_tempo` pesanan
  non-C&C, (5) tidak mengubah apa pun saat dibaca (status/verif/done_at/events
  tetap) dan idempoten. Pesanan non-C&C dan isolasi lintas cabang juga diperiksa.
  Uji ini **MERAH tanpa perbaikan** (lihat bukti), jadi benar-benar menutup celah.
- Catatan non-pemblokir QA juga dibereskan: "periode berjalan" kini memakai
  tanggal bisnis **WIB** (`hariIni()` = UTC+7, bukan bulan lokal browser) dan
  urutan "5 pesanan terakhir" memakai urutan server apa adanya (sortir klien
  dengan kunci campuran `tanggal`/`created_at` dihapus). Uji vitest menambahkan
  kasus `hariIni` (pergantian hari/bulan lintas zona) dan kasus `pengingatCNC`
  yang membuktikan pengingat TIDAK muncul bila `jatuh_tempo` absen.

## Bukti verifikasi

Semua perintah dijalankan dari `/home/hermes/projects/grahita-website`.

Gerbang backend (`api/`):

| Perintah | Hasil |
|---|---|
| `gofmt -l .` | kosong |
| `go vet ./...` | `exit 0` |
| `go build ./...` | `exit 0` |
| `sqlc-v1.31.1 generate` | 0 diff (md5 19 berkas `internal/db/sqlc/*.go` sebelum == sesudah; putaran 2 menambah `order.sql.go`) |
| `bash scripts/check-secrets.sh` | `exit 0` — "Tidak ada rahasia terdeteksi" |
| `redocly lint openapi/openapi.yaml` | valid, 10 warning (baseline) |
| `go test -race -p 1 -count=1 ./...` (DB `grahita_f119r2` + shim) | `SUITE_EXIT=0`, **22 paket ok, 0 FAIL, 0 SKIP** (`httpapi` 138,8 s) — log `cache/scratch/f119r2_suite_final.log` |

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
- **`go test ./internal/httpapi/ -run TestPGBerandaPengingatCNCOverdueDariResponsNyata -count=1 -v`
  (BARU, putaran 2)**: PASS. PostgreSQL 16.15 nyata + router nyata; pesanan C&C
  dibuat lewat alur submit sungguhan lalu tanggalnya digeser ke masa lalu. Log
  uji (bukti dari server nyata):
  `GET /orders baris C&C: is_cnc=true status=submitted tanggal=2026-09-29 jatuh_tempo=2026-10-06`
  dan `pengingat beranda dari data server: judul="C&C lewat jatuh tempo 3 hari" sisa_hari=-3 hari_ini_WIB=2026-10-09`.
  Uji juga menegaskan `done_at` NULL, `verif_status` tetap, jumlah event tetap
  setelah dibaca (tanpa efek samping), pesanan non-C&C tidak membawa
  `jatuh_tempo`, dan daftar cabang lain tidak memuatnya (isolasi).
  **Tanpa perbaikan uji ini MERAH** (kolom `JatuhTempo` dihapus sementara dari
  `Repo.List`): `GET /orders harus membawa jatuh_tempo "2026-10-06", dapat <nil>`
  dan `pengingat C&C tidak akan tampil: server mengirim jatuh_tempo kosong` —
  persis galat yang dilaporkan QA putaran 1. Jadi uji ini benar-benar menutup
  celah, bukan sekadar ikut hijau.
- `go test ./internal/httpapi/ -run TestKontrakOpenAPIIkutRouter -count=1`: PASS.

Gerbang frontend (`web/`):

| Perintah | Hasil |
|---|---|
| `npm run typecheck` (4 workspace) | `exit 0` |
| `npx vitest run --no-file-parallelism` per workspace | `exit 0` — **197 uji lulus** (api-client 8, ui 64, admin 70, customer 55) |
| `npm run build` | `exit 0` (kedua SPA) |

Catatan kejujuran (flake mesin, BUKAN regresi F1-19): `apps/customer/src/pesanan.test.tsx`
(uji F1-11, bukan berkas F1-19) timeout 1 s pada `npm test` paralel karena
delapan lingkungan jsdom dibuat bersamaan. Saya buktikan ini bukan akibat
perubahan F1-19: dengan seluruh perubahan F1-19 di-`git stash`, run paralel pada
HEAD bersih `0a03f02` juga gagal di uji yang sama (2× berturut), dan berkas itu
lulus 21/21 saat dijalankan sendiri. Dengan `--no-file-parallelism` seluruh 55
uji customer (termasuk 21 uji pesanan) lulus 100%. Uji F1-19 sendiri
(`beranda/invoice/tutorial/App`) hijau di kedua mode.

Uji vitest F1-19 (semuanya lulus):

- `apps/customer/src/beranda.test.tsx` — 5 uji: tiga uji putaran 1 (ringkasan
  periode, keadaan kosong, galat API) + dua uji putaran 2: `hariIni` memakai
  tanggal bisnis **WIB** (2026-10-09T18:30Z → `2026-10-10`,
  2026-10-31T17:00Z → `2026-11-01`), dan `pengingatCNC` menampilkan pengingat
  hanya bila `jatuh_tempo` ada (`"C&C lewat jatuh tempo 4 hari"`) serta TIDAK
  menampilkannya saat `jatuh_tempo` absen — bentuk respons `GET /orders` sebelum
  perbaikan. Uji pertama juga menegaskan urutan "5 pesanan terakhir" sama persis
  dengan urutan server (klien tidak menyortir ulang) dan seluruh panggilan GET.
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

Pemeriksaan browser putaran 2 — **DENGAN API dan PostgreSQL NYATA** (menutup
catatan QA bahwa putaran 1 memakai API tiruan sehingga celah `jatuh_tempo`
tersembunyi):

- Lingkungan: DB `grahita_f119live` (PostgreSQL 16.15, dimigrasi + `--mode=seed`),
  binary API nyata `--mode=api` di `127.0.0.1:8088`, SPA `vite dev` di
  `:5173` (proxy `/api` → `:8088`). Tidak ada mock.
- Data: cabang Bandung punya pesanan C&C `2026-09-0001` (`status=submitted`,
  `tanggal = wib_today()-10`, `jatuh_tempo = wib_today()-3`, total Rp 750.000)
  dan satu pesanan non-C&C `2026-10-0002`.
- `GET /api/v1/orders` lewat HTTP nyata (sesi cabang) mengembalikan
  `"is_cnc":true, "tanggal":"2026-09-29", "jatuh_tempo":"2026-10-06"` untuk
  pesanan C&C — dan **tidak** memuat `jatuh_tempo` untuk pesanan non-C&C.
- Browser nyata (cookie sesi disuntik via CDP `Network.setCookie`, kata sandi
  tidak pernah diketik di browser): elemen `[data-testid="banner-cnc"]` tampil
  dengan teks **"C&C lewat jatuh tempo 3 hari"** / "Pesanan 2026-09-0001 ·
  Rp 750.000. Bayar lalu unggah bukti transfer."; KPI 1 / Rp 125.000 / 2 / 0;
  tabel "Pesanan terakhir" menampilkan baris C&C itu tetap berstatus
  **"Menunggu verifikasi"** (bukan "Selesai").
- mobile 390×844: banner tetap tampil, `aside` `display:none`, bottom-nav ada,
  `scrollWidth - innerWidth = 0`.
- **Tanpa auto-Done dibuktikan dua sisi**: log API hanya memuat GET dari SPA
  (`/auth/me`, `/orders`) — satu-satunya POST adalah login skrip pengujian saya,
  bukan browser; dan di DB setelah browsing, `2026-09-0001` masih `submitted`
  dengan `done_at` NULL dan `verif_status = belum`.
- Tangkapan layar baru (data nyata):
  `cache/scratch/f119-shots/f119r2-beranda-nyata-desktop-1440x900.png` dan
  `...-mobile-390x844.png`.

Pemeriksaan tata letak putaran 1 (SPA `vite dev` :5173 dengan API TIRUAN di
:8088 — data contoh untuk memeriksa tata letak/HTML `/invoice` dan `/tutorial`
yang tidak berubah di putaran 2). Enam tangkapan layar di
`cache/scratch/f119-shots/f119-{beranda,invoice,tutorial}-{desktop,mobile}-{1440x900,390x844}.png`:

- desktop 1440×900: `/invoice` menampilkan 4 baris dengan "Revisi 1", "Asli
  versi lama", "Belum tersedia" (tombol `disabled` hanya pada baris Submitted)
  dan 3 tombol Unduh aktif; `/tutorial` menampilkan 4 kartu topik + 4 ilustrasi
  + 4 accordion (pertama terbuka). Tidak ada overflow horizontal.
- mobile 390×844: KPI beranda menjadi deretan geser (`.kpis` `display: flex`),
  baris tabel menjadi kartu (`tr` `display: grid`), ilustrasi tutorial 240 px
  dengan kepala kartu `flex-direction: column`, dan `scrollWidth - innerWidth = 0`
  pada ketiga halaman.

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
- Pemeriksaan browser putaran 2 memakai DB live dengan baris pesanan C&C
  disemai lewat SQL (tanggal digeser ke masa lalu); jalur submit C&C yang
  menghasilkan baris seperti itu dibuktikan uji PG
  `TestPGBerandaPengingatCNCOverdueDariResponsNyata` (membuat pesanan lewat
  `POST /orders/{id}/submit` sungguhan).
- `apps/customer/src/pesanan.test.tsx` (F1-11) timeout 1 s pada `npm test`
  paralel di mesin ini; terbukti juga gagal pada HEAD bersih tanpa perubahan
  F1-19, dan lulus penuh dengan `--no-file-parallelism`. Bukan milik kartu ini,
  tapi perlu dirapikan pemilik kartu F1-11 (naikkan timeout / kurangi
  paralelisme jsdom) supaya gerbang `npm test` tidak merah acak.
- Hotspot baru yang ikut disentuh: `api/db/queries/order.sql`,
  `api/internal/db/sqlc/order.sql.go`, `api/internal/order/postgres/postgres.go`
  (dipakai bersama F1-11/F1-12/F1-13/F1-14/F1-15/F1-18/F1-20).

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
