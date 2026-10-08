# F1-17 — Invoice PDF dan Surat Jalan XLSX/PDF

Kartu: **t_a8c17fc0 — F1-17 Invoice PDF dan Surat Jalan XLSX/PDF**

Dokumen acuan: PRD §6.1 (tabel dokumen + isi invoice), §8 no.7/no.9 (pertanyaan
terbuka format surat jalan & tanda tangan digital), §9 no.6/no.8; SystemDesign.md
§4.4 (alur setelah Done), §8 (generator dokumen), §5.3 (idempotensi);
Database.md §4.6 (`documents`, unique `(order_id, jenis, versi)`).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Paket baru `api/internal/document` (service + generator + repository in-memory)
dan `api/internal/document/postgres` (repository), query baru
`api/db/queries/document.sql` (hasil sqlc `internal/db/sqlc/document.sql.go`),
tiga endpoint, dan satu tombol UI:

| Method | Path | Peran | Catatan |
|---|---|---|---|
| GET | `/api/v1/orders/{id}/documents` | Cabang / staf Pusat | daftar dokumen satu pesanan; cabang hanya melihat **invoice**-nya sendiri |
| GET | `/api/v1/documents/{id}/download` | Cabang / staf Pusat | presigned URL berumur pendek (300 detik), diperiksa per peran + branch_id |
| GET | `/api/v1/admin/orders/{id}/documents` | Manager & Superadmin | seluruh dokumen pesanan cabang mana pun |
| POST | `/api/v1/admin/orders/{id}/documents` | Manager & Superadmin | membuat/menyegarkan ketiga dokumen versi berjalan (idempoten) |

Yang dihasilkan satu pesanan Done (SystemDesign.md §4.4): **Invoice PDF**,
**Surat Jalan XLSX**, **Surat Jalan PDF** — kunci object storage
`documents/{order_id}/{jenis}-{versi}.{ext}` dan satu baris `documents` per
(jenis, versi).

## Keputusan desain

1. **Dokumen hanya lahir untuk pesanan `done`.** `Ensure` menolak status lain
   dengan `ErrNotDone` (`409 ORDER_NOT_DONE`). Ini menjaga janji PRD §6.1 dan
   sekaligus mencegah invoice tanpa nomor/tanggal — keduanya baru ada setelah
   submit (BR-04). Repository juga menolak snapshot yang tidak lengkap
   (nomor/tanggal/baris kosong → `ErrInvalidData`).
2. **Idempotensi lewat kunci + unique constraint, bukan lewat flag.** Kunci
   object storage memuat `versi`, dan `documents` punya unique
   `(order_id, jenis, versi)`; baris baru ditulis dengan `ON CONFLICT DO NOTHING`
   lalu baris finalnya dibaca kembali. Menjalankan ulang render untuk versi yang
   sudah ada **tidak menyentuh berkasnya sama sekali** (`Put` tidak dipanggil) dan
   tidak menggandakan baris. Bukti: `TestEnsureIdempotenTidakMenimpaBerkas`,
   `TestPGDokumenGenerateIdempotenDanIsolasi`.
3. **Berkas versi lama tidak pernah tertimpa (PRD §9 no.8).** Versi dokumen
   selalu = versi pesanan SAAT render; `EnsureInvoice` menolak merender versi
   lain. Revisi invoice (F2-25) menaikkan `orders.versi`, sehingga invoice baru
   memakai kunci `...-1.pdf` sementara `...-0.pdf` tetap utuh. Bukti:
   `TestVersiBaruMemakaiKunciBaru`,
   `TestPGDokumenVersiBaruTidakMenimpaBerkasLama` (4 baris = 3 versi 0 + 1
   versi 1, berkas v0 byte-identik sebelum/sesudah, `Ensure` penuh setelahnya
   menjadi 6 baris tanpa menyentuh v0).
4. **SATU permintaan render = SATU unit kerja.** Seluruh isi dirender lebih
   dahulu di memori, baru ditulis ke object storage, lalu **seluruh** baris
   `documents` disimpan dalam satu transaksi. Kegagalan di manapun membuang
   objek yang sudah ditulis, jadi tidak ada berkas yatim maupun dokumen separuh
   jadi. Bukti: `TestKegagalanRenderTidakMenyisakanApaPun` (3 skenario: mesin PDF
   mati, gagal tulis storage pada berkas ke-2, gagal simpan metadata) dan
   `TestPGDokumenRendererTidakSiapMenjawab503` lewat HTTP.
5. **Renderer tetap Gotenberg (SystemDesign.md §8), dan template diuji sampai
   PDF nyata.** Invoice/surat jalan PDF lewat `platform/pdf.Renderer`
   (`GOTENBERG_URL`); XLSX lewat `excelize`. Karena Gotenberg di Docker tidak
   tersedia di lingkungan ini, uji
   `TestPGDokumenPDFNyataMelaluiChromium` menjalankan **tiruan protokol Gotenberg
   di atas Chromium lokal** (mesin render yang sama dengan Gotenberg) sehingga
   berkas PDF yang dihasilkan diperiksa sungguhan (`%PDF-`, `%%EOF`,
   `/Type /Page`, ukuran > 3 KB). Uji dilewati bila `google-chrome` tidak ada
   (mis. CI), jadi bukan gate yang rapuh.
6. **Isi dokumen = snapshot pesanan, bukan perhitungan ulang.** Generator hanya
   membaca `orders`/`order_lines` (harga & serial yang sudah di-snapshot saat
   submit, BR-05) + `branches` + rekening AKTIF. Nomor, versi, tanggal, baris,
   serial, total, dan penanda pembayaran tidak pernah dihitung ulang di dokumen,
   sehingga dokumen selalu cocok dengan pesanan yang tersimpan. Bukti: uji HTML
   (`TestInvoiceHTMLMemuatIdentitasDanAngka`) dan uji HTTP yang memeriksa isi
   berkas terhadap nilai yang sama di database.
7. **Surat jalan XLSX satu baris per order line, siap cetak.** Header kolom
   tetap `No | Nama Barang | Satuan | Qty | Serial Number`; baris data urut
   `line_no`; serial apa adanya (`-` untuk baris packaging yang memang tidak
   berserial); A4 tegak, `FitToWidth=1`, header tabel diulang saat cetak
   (`_xlnm.Print_Titles`), header dibekukan saat dilihat, area cetak sebatas
   isi. PDF surat jalan memakai data dan kolom yang sama supaya kedua berkas
   konsisten. Bukti: `TestSuratJalanXLSXSatuBarisPerLine` (berkas dibuka ulang
   dengan excelize: A1/A2 kop, B3 nomor, E3 tanggal, header baris 8, tiga baris
   data termasuk packaging, layout & panes).
8. **Akses dokumen selalu lewat presigned URL berumur pendek.** Kunci storage
   tidak pernah keluar dari service (`json:"-"`) dan tidak pernah dikirim ke
   klien; unduhan memakai `PresignDownload` (nama berkas benar, paksa
   `attachment`) bila storage mendukungnya, kalau tidak `PresignGet`.
9. **Otorisasi per peran + branch_id di layer service, bukan UI.**
   - Akun cabang (`CapInvoiceOwn`) hanya boleh **melihat dan mengunduh
     `invoice_pdf`** pesanan cabang SESI-nya. Surat jalan adalah dokumen Pusat
     (PRD §6.1: dikirim ke admin ber-flag `surat_jalan`), jadi meminta surat
     jalan dijawab `404` — sama seperti dokumen yang tidak ada, supaya jenis
     dokumen Pusat tidak terbaca dari percobaan unduh.
   - Staf Pusat (`CapAdminOrders`) boleh semua dokumen semua cabang. Manager
     termasuk (PRD §2.1).
   - Pesanan/dokumen cabang lain dijawab `404` (bukan `403`), dan id yang bukan
     UUID juga `404`. Bukti: `TestOtorisasiDaftarDanUnduh` dan uji HTTP
     `TestPGDokumenGenerateIdempotenDanIsolasi` (400/403/404 per peran).
10. **Rekening pembayaran hanya yang AKTIF.** Invoice memuat seluruh rekening
    aktif (bank, nomor, atas nama) dan tidak pernah mencetak rekening nonaktif
    (PRD §4.6/§6.1). Dokumen adalah snapshot: menonaktifkan rekening setelah
    render tidak mengubah berkas yang sudah ada (versi barulah yang berubah).
    Bukti: `TestPGDokumenRekeningNonAktifTidakTercetak`.
11. **Penanda pembayaran**: pesanan C&C dicetak `CASH & CARRY — jatuh tempo
    <tanggal>`; pesanan lain dicetak `LUNAS` (invoice baru lahir saat pesanan
    selesai). Bukti: `TestInvoiceHTMLVersiDanPenanda`,
    `TestInvoiceTanpaJatuhTempoTetapCNC`.
12. **Escape nilai dari database.** Dokumen dibentuk dengan `html/template`,
    jadi nama cabang/catatan yang berisi markup tidak bisa menyuntikkan HTML ke
    invoice/surat jalan. Bukti: `TestTemplateMengEscapeNilaiDariDatabase`.
13. **Angka & tanggal diformat di Go, bukan di template**: Rupiah bulat dengan
    pemisah ribuan (BR-10) dan tanggal Indonesia ("08 Oktober 2026") memakai
    komponen tanggal bisnis apa adanya (tanpa konversi zona, BR-04/BR-11).
    Bukti: `TestFormatAngkaDanTanggal`, `TestSuratJalanHTMLKonsistenDenganXLSX`
    (menolak pergeseran tanggal).
14. **Nama berkas unduhan/lampiran email satu aturan.** `document.NamaBerkas`
    menghasilkan `Invoice-<nomor>.pdf`, `Surat-Jalan-<nomor>.pdf`,
    `Surat-Jalan-<nomor>.xlsx` — sama persis dengan aturan lampiran email
    domain notification (`order_email.go`). Keduanya sengaja tidak saling
    mengimpor (domain notification lebih dulu ada dan sudah teruji), sehingga
    aturan ini dijaga oleh pengujian di kedua sisi.
15. **Pintu masuk untuk kartu turunan.** `Ensure(ctx, orderID)` (semua dokumen
    versi berjalan) dan `EnsureInvoice(ctx, orderID)` (invoice saja) adalah
    jalur yang dipakai transisi Done (F1-18) dan revisi invoice (F2-25); tidak
    ada aktor di dalamnya sehingga bisa dipanggil di dalam transaksi bisnis
    kartu-kartu itu. Endpoint admin memakai `Generate` yang sama setelah
    memeriksa `CapAdminOrders`.

## Penyimpangan / keputusan yang perlu dicatat

- **Template awal, tanpa tanda tangan/stempel digital.** PRD §8 no.9 masih
  terbuka, jadi dokumen memuat catatan "belum memuat tanda tangan atau stempel
  digital" dan tidak menebak keputusan bisnis. Identitas PT memakai teks +
  monogram SVG inline (tanpa berkas logo) sampai aset resmi tersedia; PRD §8
  no.7 (format Surat Jalan resmi) juga masih terbuka, sehingga template ini
  disebut "template awal".
- **Endpoint `POST /admin/orders/{id}/documents` tidak ada di SystemDesign §6**
  (ringkasan permukaan API). Ia ditambahkan sebagai pemulihan/penyegaran
  eksplisit (mis. render gagal karena mesin PDF sempat mati) dan sebagai jalur
  yang bisa diuji lewat HTTP sebelum transisi Done (F1-18) ada. Sifatnya
  idempoten, jadi tidak bisa menggandakan dokumen; jawabannya `200` (bukan
  `201`) karena operasinya "pastikan tersedia", bukan "buat selalu".
- **Tidak ada baris timeline / audit baru.** Dokumen bukan mutasi data bisnis
  oleh admin; timeline "pesanan selesai" dan email dokumen adalah milik F1-18.
  Dokumen sendiri terlihat di detail pesanan (`dokumen[]`) dan di endpoint
  daftar dokumen.
- **Mode `api` tanpa `GOTENBERG_URL`** menolak render dengan
  `503 DOCUMENT_RENDER_UNAVAILABLE` (bukan 500) — senada dengan storage
  `Disabled`; surat jalan XLSX pun tidak dibuat supaya tidak ada dokumen
  separuh jadi. Worker/renderer terpisah (River) belum ada; pemanggilnya
  F1-18/F2-25.
- **Baris `documents` tidak menyimpan content type / nama berkas.** Keduanya
  diturunkan dari `jenis` (`MimeType`, `NamaBerkas`), jadi tidak ada keadaan
  ganda yang bisa menyimpang. Tabel `documents` juga tidak diubah (Database.md
  §4.6 sudah cukup).
- **`biaya_packaging` tampil sebagai baris biasa** di invoice (baris
  `packaging` dari `order_lines`), sesuai PRD §6.1 "biaya packaging bila ada";
  baris itu ikut tercetak di surat jalan karena kartu meminta "satu baris per
  line".
- **Dependensi baru**: `github.com/xuri/excelize/v2 v2.11.0` (SystemDesign.md §8
  menyebut `excelize` untuk XLSX; belum ada di `go.mod` sebelumnya). Ditambahkan
  lewat `go get` + `go mod tidy`, jadi dependensi tidak langsungnya
  (mscfb, msoleps, efp, nfp, tiendc/go-deepcopy, x/net) tercatat sebagai
  `// indirect` dan tidak ada perubahan lain di `go.mod`.
- **Halaman cabang untuk daftar/unduh invoice (riwayat invoice) belum dibuat** —
  itu kartu F1-19. Yang disiapkan di sini adalah API-nya
  (`GET /orders/{id}/documents`, `GET /documents/{id}/download`); UI admin
  (detail pesanan) sudah memakai tombol **Unduh** dan **Buat dokumen**.

## Bukti verifikasi

Gerbang (cwd `api/`, toolchain Go `go1.27.0`, sqlc `v1.31.1`, PostgreSQL 16.15
di 127.0.0.1:5455 + `scripts/pg-dev-shim.sql`):

```
gofmt -l .                                   # kosong
go vet ./...                                 # exit 0
go build ./...                               # exit 0
sqlc-v1.31.1 generate                        # tanpa diff (hanya berkas baru F1-17)
./scripts/check-secrets.sh                   # exit 0 — tidak ada rahasia
redocly lint openapi/openapi.yaml            # valid, 10 warning gaya lama (sama seperti sebelumnya)
TEST_DATABASE_URL=…/grahita_f117  go test -race -p 1 -count=1 ./...
npm run typecheck (web)                      # exit 0
npm test (web)                               # seluruh workspace lulus
```

Hasil nyata suite penuh (`go test -race -p 1 -count=1 ./...`, DB
`grahita_f117`, log `cache/scratch/f117-full.log`, dijalankan setelah `go mod
tidy` menambahkan `excelize` sebagai dependensi langsung): **18 paket `ok`,
0 FAIL, 0 SKIP, exit=0** (`httpapi` 109,5 s; `auth` 32,5 s; `document` 2,8 s).

Uji yang ditambahkan kartu ini:

- `internal/document` (unit, tanpa DB): 18 uji — format Rupiah/tanggal, kunci
  storage & nama berkas, render 3 dokumen + kunci, idempotensi & berkas tidak
  tertimpa, versi baru memakai kunci baru, pesanan belum Done ditolak,
  tiga skenario kegagalan tanpa sisa berkas/baris (3 sub-uji), otorisasi daftar
  & unduh (termasuk surat jalan 404 untuk cabang), `Generate` butuh
  CapAdminOrders, hasil render kosong ditolak, snapshot kurang ditolak; plus uji
  isi HTML & XLSX (escape, penanda C&C/LUNAS, header kolom, satu baris per line,
  layout cetak, panes).
- `internal/httpapi` (integrasi PostgreSQL nyata, skema bersih per uji): 7 uji —
  `TestPGDokumenGenerateIdempotenDanIsolasi` (3 berkas + kunci + isi cocok
  snapshot + idempoten + isolasi cabang + unduhan per peran),
  `TestPGDokumenPesananBelumDone`, `TestPGDokumenVersiBaruTidakMenimpaBerkasLama`,
  `TestPGDokumenRekeningNonAktifTidakTercetak`,
  `TestPGDokumenRendererTidakSiapMenjawab503`,
  `TestPGDokumenPDFNyataMelaluiChromium` (PDF sungguhan dari Chromium lokal
  lewat klien Gotenberg + XLSX dibuka excelize), dan
  `TestChromeTersediaUntukUjiPDF` (penanda ketersediaan Chromium).
- `web/apps/admin/src/pesanan.test.tsx`: 2 uji baru — tombol Unduh memanggil
  `/documents/{id}/download` dan tidak pernah menyentuh kunci storage; tombol
  "Buat dokumen" hanya muncul untuk pesanan Done dan memanggil endpoint admin
  lalu memuat ulang daftar.

## Risiko residu

- **Gotenberg nyata (service Docker) belum dijalankan**; bukti PDF nyata berasal
  dari tiruan protokol Gotenberg di atas Chromium lokal (mesin render yang
  sama) — persamaan versi Chromium antara lingkungan ini dan image Gotenberg
  tidak diklaim.
- **Object storage S3/MinIO nyata belum dijalankan** (uji memakai
  `storage.Memory`); unduhan memakai jalur `PresignDownload` yang sama seperti
  bukti transfer (F1-13), yang SigV4-nya juga belum diuji ke MinIO/R2.
- PG18 asli (`postgres:18-alpine`) belum dieksekusi; uji integrasi berjalan di
  PG16 + shim `uuidv7()`, sama seperti kartu F1-02..F1-16.
- `golangci-lint` (v2.14.0) tidak tersedia di sesi ini, jadi gate lint CI tidak
  direproduksi.
- Render masih sinkron di dalam permintaan HTTP (tanpa antrean job); jalur
  River/worker untuk dokumen belum dimiliki kartu mana pun dan tidak dibuat di
  sini supaya tidak menebak bentuk antrean F2-26.
- Bila berkas di object storage hilang (tanpa baris DB yang hilang), render
  ulang tidak akan memperbaikinya karena barisnya sudah ada — rekonsiliasi
  DB↔objek adalah lingkup F1-21 (backup/restore).

## Kewajiban untuk kartu turunan

- **F1-18 (verifikasi → Done)**: panggil `document.Service.Ensure(ctx, orderID)`
  di dalam/tepat setelah transisi Done, lalu `notification.EnqueueOrderEmail`
  (yang akan menemukan `invoice_pdf`, `surat_jalan_pdf`, `surat_jalan_xlsx`
  versi berjalan). Jangan menulis tabel `documents` dari domain lain.
- **F2-25 (revisi invoice)**: naikkan `orders.versi` **lalu** panggil
  `EnsureInvoice(ctx, orderID)`; dokumen versi lama otomatis tetap utuh dan
  email revisi memakai dokumen versi baru.
- **F1-19 (beranda & riwayat invoice cabang)**: pakai
  `GET /api/v1/orders/{id}/documents` (hanya invoice yang terlihat) +
  `GET /api/v1/documents/{id}/download` untuk unduhan/cetak.
- **F1-21 (backup/restore)**: daftar objek dokumen ada di `documents.storage_key`;
  restore harus memverifikasi DB↔objek untuk kunci ini karena render ulang tidak
  memperbaiki objek yang hilang.

## Hotspot

- `api/internal/httpapi/router.go`, `openapi/openapi.yaml`,
  `api/internal/httpapi/integration_pg_test.go`, dan
  `api/cmd/server/main.go` disentuh bersama kartu lain; perubahannya aditif
  (menambah grup rute, path, field `Deps.Documents`, dan wiring service).
- `web/apps/admin/src/pages/pesanan.tsx` + `pesanan.test.tsx`: F1-15 baru saja
  mendarat di berkas ini; perubahan kartu ini kecil (tombol unduh/buat dokumen).
