# F1-11 — Draft, editor dan daftar pesanan cabang

Kartu: **t_4c247946 — F1-11 Draft, editor dan daftar pesanan cabang**

Dokumen acuan: PRD §3.5, §7; SystemDesign §2.1, §3, §6–§7; Database.md §4.4, §8.
Prototype acuan UI: `docs/prototype/customer_v2.html` (`pPesanan`, `pEditor`, `pDetail`).

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

Backend (baru — domain `internal/order`):

| Method | Path | Peran | Catatan |
|---|---|---|---|
| GET | `/api/v1/orders` | akun cabang | daftar pesanan cabang SESI, filter `?status=` |
| POST | `/api/v1/orders` | akun cabang | buat draft (harga dari `branch_prices` cabang sesi) |
| GET | `/api/v1/orders/{id}` | akun cabang | detail + baris pesanan |
| PUT | `/api/v1/orders/{id}` | akun cabang | ubah draft (hanya status `draft`) |
| DELETE | `/api/v1/orders/{id}` | akun cabang | hapus draft (hanya status `draft`) |

Frontend portal cabang:

- `/pesanan` — daftar + filter status (URL search param `?status=`), tombol
  **Buat pesanan**, tautan ke detail.
- `/pesanan/baru` — editor pesanan baru: baris produk (produk, qty stepper,
  harga satuan dari katalog cabang, subtotal), catatan, total live, **Simpan draft**.
- `/pesanan/{id}/ubah` — editor draft yang sudah ada (baris diganti utuh).
- `/pesanan/{id}` — detail: header + penanda status/C&C/Revisi/Sertifikat, catatan,
  tabel baris + total, aksi **Ubah**/**Hapus draft** (dengan konfirmasi modal) untuk
  draft; pesanan non-draft menampilkan notice terkunci tanpa aksi.

Pemetaan rute mengikuti SystemDesign §7.1 (`pPesanan → /pesanan`, `pEditor →
/pesanan/baru` + `/pesanan/{id}/ubah`, `pDetail → /pesanan/{id}`) dan router
prototype (`#/pesanan`, `#/pesanan/baru`, `#/pesanan/{id}/ubah`, `#/pesanan/{id}`).
Catatan kartu yang menulis `/pesanan/{id}/ubah/detail` dibaca sebagai singkatan
untuk "`/pesanan/{id}/ubah` dan detail"; detail dipasang di `/pesanan/{id}`
sesuai dokumen desain — tidak ada rute alias tambahan.

## Keputusan desain

1. **Harga order line diambil domain pesanan dari domain katalog, bukan dari
   tabel `branch_prices` langsung.** `internal/order` bergantung pada satu
   interface kecil (`Catalog.BranchProducts(ctx, branchID)`) yang dipenuhi
   `catalog.Service` (metode baru `BranchProducts`). Query-nya INNER JOIN
   `products × branch_prices` sehingga:
   - hanya produk dengan baris harga cabang yang bisa dipesan,
   - produk nonaktif tidak bisa dipesan,
   - `harga_default` **tidak pernah** menjadi cadangan (kewajiban QA F1-08 untuk
     F1-11). Produk yang hanya punya `harga_default` dijawab `400
     VALIDATION_FAILED: Produk tidak dijual ke cabang ini.`
   Aturan "antar-domain lewat interface service" (SystemDesign §2) tetap dijaga;
   `order/postgres` tidak menyentuh tabel milik katalog.
2. **`branch_id` hanya dari SESI.** Service mengambil `actor.BranchID` dan
   menolak aktor non-cabang (`ErrForbidden → 403`) maupun akun tanpa cabang
   (`ErrNoBranch → 409 BRANCH_MISSING`). Tidak ada satu pun endpoint yang
   menerima `branch_id` dari klien (`httpx.DecodeJSON` menolak field tak dikenal,
   jadi body berisi `branch_id`/`harga_satuan`/`total` dijawab `400`).
3. **IDOR lintas cabang dijawab `404`, bukan `403`.** Semua query `orders`
   menyertakan `branch_id` sebagai syarat, maka pesanan cabang lain tidak
   ditemukan sama sekali. `404` dipilih supaya keberadaan pesanan cabang lain
   tidak bisa ditebak dari selisih kode status (kartu menyebut "403/404").
4. **Id yang bukan UUID juga dijawab `404`.** `internal/order` memeriksa bentuk
   UUID sebelum menyentuh database, sehingga `/orders/tidak-ada` tidak berakhir
   sebagai `500` (`SQLSTATE 22P02`) — temuan sistemik yang dicatat QA pada F1-06/F1-09
   tidak terulang di domain ini.
5. **Total dihitung server dua lapis.** Service menjumlahkan `qty × harga cabang`
   untuk validasi, dan trigger `order_lines_recalc` (migrasi 0002) menghitung
   ulang `orders.total`/`is_sertifikat` dari barisnya di PostgreSQL. Setiap
   operasi tulis membaca ulang pesanannya di dalam transaksi yang sama, sehingga
   jawaban API selalu berisi nilai database — bukan angka dari klien (SystemDesign §3,
   BR-10). Kolom `subtotal` adalah `GENERATED ALWAYS` dan tidak pernah diisi aplikasi.
6. **Penguncian pesanan Submitted/Done ada di layer service (dan di klausa WHERE).**
   Sesuai handoff F1-02 (skema sengaja tidak memakai trigger pengunci), `UPDATE`/
   `DELETE` memakai syarat `branch_id = $sesi AND status = 'draft'`. Service lebih
   dulu memuat pesanan (404 bila bukan miliknya), lalu menolak status non-draft
   dengan `409 ORDER_LOCKED`; bila barisnya berubah di antara dua langkah itu
   (mis. submit bersamaan dari F1-14), `0 rows affected` tetap dijawab
   `409 ORDER_LOCKED`, bukan sukses palsu.
7. **Belum ada status Batal.** Tidak ada nilai `batal` di mana pun (enum DB
   `order_status` juga tidak punya), dan filter `?status=batal` dijawab `400`
   sebagai status tak dikenal. Pembatalan pesanan Submitted tetap pertanyaan
   terbuka PRD §8 no.4.
8. **Draft tidak punya nomor, tanggal, serial number, maupun bukti.** Ketiganya
   lahir saat submit (F1-14); constraint `draft_tanpa_nomor` menjaga hal ini di
   database. `jumlah_item` = Σ qty baris produk (sama dengan `itemCount` prototype).
9. **Sertifikat: campuran ditolak sejak editor.** Satu pesanan tidak boleh
   memuat Sertifikat + produk reguler (PRD CP-SER-03, invariant
   `order_lines_guard_kind` di Database.md §8). Penolakan ini mencegah pelanggaran
   trigger berubah menjadi `500`. Penambahan baris biaya packaging, penandaan
   Sertifikat, dan seluruh aturan Cash & Carry **milik F1-12 (t_d5636b31)** —
   draft Sertifikat sendirian tetap boleh disimpan di kartu ini dengan harga 0.
10. **Draft yang dihapus menghapus barisnya lewat `ON DELETE CASCADE`** dan
    pesanan Submitted/Done tidak pernah cocok dengan klausa WHERE, jadi tidak
    bisa terhapus dari jalur cabang.
11. **Kepemilikan draft = akun cabang pemiliknya.** Karena 1 cabang = 1 akun
    (BR-01, `branch_users.branch_id UNIQUE`), kepemilikan cukup ditegakkan lewat
    `branch_id` dari sesi; `orders.created_by` tetap diisi untuk jejak audit.
    Menambahkan syarat `created_by = user` akan membuat draft lama tidak bisa
    diubah bila akun cabang dibuat ulang, jadi tidak dipakai.
12. **Mutasi draft cabang tidak masuk `audit_logs`.** Audit log adalah catatan
    mutasi admin (SystemDesign §5.4: "setiap mutasi oleh admin"); tidak ada
    timeline/audit draft di PRD maupun prototype. Riwayat pesanan (order_events)
    dimulai saat submit (F1-14).
13. **Filter status di URL search param** (`/pesanan?status=draft`) sesuai
    SystemDesign §7.2 supaya bisa di-bookmark; daftar tanpa paginasi mengikuti
    volume cabang (±30 pesanan/bulan) dan tetap diurutkan tanggal submit lalu
    `created_at` terbaru lebih dahulu seperti prototype.

## Berkas yang berubah

Backend:

- `api/db/queries/order.sql` (baru) + `api/internal/db/sqlc/order.sql.go` (hasil sqlc)
- `api/internal/order/order.go`, `api/internal/order/memory.go` (baru)
- `api/internal/order/postgres/postgres.go` (baru)
- `api/internal/httpapi/handlers_orders.go` (baru), `router.go`, `deps.go`, `errors.go`
- `api/internal/catalog/catalog.go` (metode `BranchProducts`)
- `api/cmd/server/main.go` (wiring `order.Service`)
- `api/internal/arch/arch_test.go` (`internal/order` ikut aturan lapisan service)
- `openapi/openapi.yaml` (+`/orders`, `/orders/{id}`, skema Order/OrderLine/OrderDraftInput,
  kode `ORDER_LOCKED`)
- Uji: `api/internal/order/order_test.go`, `api/internal/httpapi/router_orders_test.go`,
  `api/internal/httpapi/integration_orders_pg_test.go` (baru),
  `api/internal/httpapi/integration_pg_test.go` (fixture menyertakan `Orders`)

Frontend:

- `web/apps/customer/src/pages/pesanan.tsx`, `pesanan.module.css` (baru)
- `web/apps/customer/src/routes.tsx` (4 rute pesanan), `useApi.ts` (+`muatUlang`)
- `web/apps/customer/src/pages/shell-pages.tsx` (placeholder `pPesanan` F1-03 dihapus)
- `web/apps/customer/src/pesanan.test.tsx` (baru, 9 uji)

## Bukti verifikasi (dijalankan sendiri)

### Gerbang statis

```
$ gofmt -l .                       -> (kosong)
$ go vet ./...                     -> exit 0
$ go build ./...                   -> exit 0
$ sqlc-v1.31.1 generate            -> tidak ada diff pada api/internal/db/sqlc & api/db
$ ./scripts/check-secrets.sh       -> "Tidak ada rahasia terdeteksi", exit 0
$ redocly lint openapi/openapi.yaml-> "Your API description is valid. 🎉 You have 10 warnings."
                                      (10 warning adalah baseline gaya lama, sama seperti F1-09)
```

### Uji backend (PostgreSQL nyata)

```
$ TEST_DATABASE_URL=postgres://.../grahita_test?sslmode=disable \
  go test -race -p 1 -count=1 ./...
ok  github.com/grahita/portal/api/internal/order        1.963s
ok  github.com/grahita/portal/api/internal/httpapi      238.422s
ok  ... (seluruh paket lain ok, 0 FAIL, 0 SKIP)
```

Uji baru untuk kartu ini (23):

| Berkas | Jumlah | Contoh cakupan |
|---|---|---|
| `internal/order/order_test.go` | 12 | harga dari cabang sesi, produk tanpa harga ditolak, IDOR 404, ORDER_LOCKED, campuran Sertifikat, validasi qty/catatan/baris, ubah = ganti baris, hapus, filter status, id bukan UUID |
| `internal/httpapi/router_orders_test.go` | 7 | siklus draft lewat HTTP (201/200/204/404), body berisi `harga`/`total`/`branch_id` ditolak 400, pesanan Submitted 409 ORDER_LOCKED, admin 403, CSRF |
| `internal/httpapi/integration_orders_pg_test.go` | 4 | di PostgreSQL: nomor/tanggal NULL + `harga_satuan` dari `branch_prices` + `subtotal` generated + cascade hapus, penolakan produk tanpa baris harga (0 orders tercipta), IDOR lintas cabang lewat SQL, pesanan Submitted utuh setelah percobaan ubah/hapus |

### Uji & build frontend

```
$ npm run typecheck   -> exit 0 (api-client, ui, admin, customer)
$ npm test            -> admin 29, api-client 7, customer 20 (9 di antaranya pesanan.test.tsx), ui 56 => 112 lulus
$ npm run build       -> dua SPA sukses (dist/index.html + font & ikon lokal)
```
`pesanan.test.tsx` memverifikasi: daftar (nomor/draft, tanggal, item, total,
status), filter status mengubah URL + memanggil `/orders?status=draft`, detail
draft menampilkan baris & catatan tanpa kolom serial, konfirmasi hapus mengirim
`DELETE /orders/{id}`, pesanan Submitted tampil terkunci tanpa tombol ubah/hapus,
editor menghitung subtotal/total live dari harga cabang, body tersimpan **hanya**
`{catatan, lines:[{product_id, qty}]}`, sertifikat memicu notice + mengunci
"Tambah produk", dan editor ubah memuat draft lalu mengirim `PUT`.

### Probe HTTP pada binary sungguhan + PostgreSQL

Binary `--mode=api` (kesempatan `grahita_f111live`, seed + migrasi 0001..0004,
PG16 + `scripts/pg-dev-shim.sql`), hasil nyata:

| Permintaan | Hasil |
|---|---|
| A `POST /orders` (produk P-07 tanpa baris harga) | `400` `Produk tidak dijual ke cabang ini.` |
| A `POST /orders` body `{"total":1,...}` | `400` `json: unknown field "total"` |
| A `POST /orders` body `{"branch_id":"…"}` | `400` `json: unknown field "branch_id"` |
| A `POST /orders` `{"lines":[]}` | `400` `lines: minimal 1` |
| B `GET /orders/{id-draft-A}` | `404 NOT_FOUND` |
| B `PUT /orders/{id-draft-A}` | `404 NOT_FOUND` |
| B `DELETE /orders/{id-draft-A}` | `404 NOT_FOUND` |
| A `GET /orders/{id-draft-A}` | `200` (draft utuh: total 1050000, 1 baris) |
| A `GET /orders/tidak-ada` | `404` (bukan 500) |

Baris database setelah seluruh percobaan: `orders.branch_id` = cabang A,
`nomor`/`tanggal` NULL, `total` 1050000 = Σ `subtotal`, `serial_number` NULL,
hanya 1 baris `order_lines` (percobaan cabang B tidak menyentuh apa pun).

### Verifikasi di browser nyata (Chrome via CDP) — portal cabang

Sesi login disuntikkan sebagai cookie sesi hasil login API (tanpa mengetik kata
sandi di browser). Alur yang dijalankan pada API + PostgreSQL sungguhan:

1. `/pesanan/baru`: memilih **Bio Magnetik** (katalog cabang hanya memuat 6
   produk reguler; **Sertifikat Psikotes tidak muncul** karena tidak punya baris
   harga cabang), qty dinaikkan ke 3 → total live `Rp 1.050.000`; **Simpan draft**
   → diarahkan ke `/pesanan/<uuid>`.
2. Detail draft: `Draft`, "Dibuat 08 Okt 2026", catatan tersimpan, baris
   `Bio Magnetik 3 × Rp 350.000 = Rp 1.050.000`, total `Rp 1.050.000`, **tanpa**
   kolom serial number. Database: `nomor`/`tanggal` NULL, `harga_satuan` 350000,
   `subtotal` 1050000, `serial_number` NULL.
3. `/pesanan` (mobile 390×844): tabel berubah menjadi kartu (`thead` `display:
   none`, `tr` `display: grid` 2 kolom) dan tautan detail benar.
   Filter **Menunggu** → URL `/pesanan?status=submitted` + empty state.
4. Ubah draft lewat tombol **Ubah**: editor terisi qty 3 dan catatan lama; qty
   diubah ke 4 → total live `Rp 1.400.000` → simpan → detail menampilkan
   `Qty 4`, `Rp 1.400.000`. Database: **1 baris** (diganti, bukan ditambah),
   `orders.total` = `order_lines.subtotal` = 1400000.
5. Hapus draft: tombol **Hapus draft** → modal konfirmasi "Hapus draft ini?" →
   konfirmasi → kembali ke `/pesanan` ("0 pesanan pada filter ini"). Database:
   `orders` 0 baris, `order_lines` 0 baris (cascade).

Ukuran `getComputedStyle` pada editor (tuntutan kartu "editor mobile/desktop teruji"):

| Properti | Desktop 1440×900 | Mobile 390×844 |
|---|---|---|
| `.lines-head` | `display: grid`, `616px 132px 130px 130px 36px` | `display: none` |
| baris `.line` | `display: grid`, `616px 132px 130px 130px 36px` | `display: grid`, `153px 153px` |
| `.m-lbl` | `display: none` | `display: block`, `font-size: 12px` |
| tombol hapus baris | `position: static` | `position: absolute`, `right: 0px` |
| kartu total | `position: static`, `border-radius: 16px` | `position: sticky`, `bottom: 76px` |
| bar total | `display: flex`, `justify-content: space-between` | idem |

Nilai-nilai ini sama dengan blok `@media (max-width:767px)` prototype
(`.lines-head{display:none}`, `.line{grid-template-columns:1fr 1fr}`,
`.m-lbl{display:block;font-size:12px}`, `.l-del{position:absolute;right:0}`,
`.sticky-total{bottom:calc(76px + env(safe-area-inset-bottom))}`).

## Yang belum dikerjakan (batas kartu, bukan kelalaian)

- **Baris biaya packaging & aturan Sertifikat penuh + Cash & Carry → F1-12
  (t_d5636b31)**: draft Sertifikat di kartu ini hanya memuat baris produk
  (harga 0) tanpa baris packaging; `is_cnc` selalu `false` pada draft.
- **Submit, penomoran, dan penguncian transisi draft→submitted → F1-14
  (t_5b21b48b)**; bukti transfer → F1-13; halaman admin → F1-15.
- **Tidak ada halaman/endpoint pembatalan pesanan Submitted** (keputusan kartu).
- **Daftar pesanan belum dipaginasi** (volume per cabang kecil); filter
  `status` disediakan, filter periode menunggu beranda/riwayat (F1-19).

## Sisa risiko

1. Daftar pesanan mengembalikan seluruh riwayat cabang tanpa batas — perlu
   paginasi bila satu cabang kelak punya ribuan pesanan.
2. Harga dibaca di luar transaksi tulis draft: perubahan harga yang terjadi
   serentak bisa membuat draft memakai harga saat pembacaan. Dampak kecil untuk
   draft (harga final dikunci saat submit F1-14), tetapi perlu diperhatikan saat
   submit memakai jalur serupa.
3. `created_by` tidak dipakai sebagai syarat otorisasi (lihat keputusan 11);
   bila kelak satu cabang boleh punya beberapa akun, aturan kepemilikan draft
   harus ditinjau ulang.
4. Uji dijalankan pada PostgreSQL 16.15 + `scripts/pg-dev-shim.sql` (bukan PG18
   asli/CI `postgres:18-alpine`) — sama seperti F1-02..F1-10.
