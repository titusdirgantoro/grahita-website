# F1-08 — Master produk, matriks harga dan katalog cabang

Kartu: **t_44754442 — F1-08 Master produk, matriks harga dan katalog cabang**

Dokumen acuan: PRD §3.4, §3.6, §4.5, §7; SystemDesign §2, §2.1, §5.4, §6, §7;
Database.md §4.3, §7.1.

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Permukaan API yang dikerjakan

| Method | Path | Peran | Sesuai kartu |
|---|---|---|---|
| GET | `/api/v1/catalog` | akun cabang (harga khusus cabang) / admin | ya (sudah ada sejak F1-03, kini kriteria CP-KAT-01/02 diuji) |
| GET | `/api/v1/admin/products` | Manager + Superadmin | ya (+ filter `q`, `jenis`, `aktif`) |
| POST | `/api/v1/admin/products` | Manager + Superadmin | ya (AP-KAT-01) |
| GET | `/api/v1/admin/products/{id}` | Manager + Superadmin | ya (bagian CRUD) |
| PUT | `/api/v1/admin/products/{id}` | Manager + Superadmin | ya (AP-KAT-01, termasuk `is_active`) |
| DELETE | `/api/v1/admin/products/{id}` | Manager + Superadmin | ya (AP-KAT-01; produk terpakai → 409) |
| GET | `/api/v1/admin/prices` | Manager + Superadmin | ya (AP-KAT-02 matriks + filter) |
| PUT | `/api/v1/admin/prices` | Manager + Superadmin | ya (batch edit; AP-KAT-02/03) |
| POST | `/api/v1/admin/prices/copy` | Manager + Superadmin | ya (AP-KAT-04 salin antar cabang) |

Semua rute terdaftar di `openapi/openapi.yaml` dan dijaga dua arah
(router ⇄ kontrak) oleh `TestKontrakOpenAPIIkutRouter`.

## Keputusan desain

1. **Harga default BUKAN cadangan katalog.** Katalog cabang memakai
   `INNER JOIN branch_prices` (`db/queries/catalog.sql: ListProductsForBranch`),
   jadi produk tanpa baris harga untuk cabang itu **tidak muncul** walau
   `harga_default` terisi, dan dengan begitu tidak bisa dipesan (PRD
   CP-KAT-01/02, AP-KAT-03). `harga_default` hanya nilai awal/rujukan saat
   mengisi sel matriks; `seed` memakainya untuk membentuk matriks awal
   (CROSS JOIN produk reguler × cabang).
2. **Sel kosong = tidak dijual, dan itu satu-satunya cara menyatakan "tidak
   dijual".** Batch edit dengan `harga: null`/kosong menghapus baris
   `branch_prices`; nilai 0 tetap berarti "dijual dengan harga 0" (mis.
   Sertifikat). Matriks pada `GET /admin/prices` hanya mengirim sel yang
   BERISI, sehingga UI bisa membedakan "kosong" dari "0".
3. **Sertifikat selalu 0, di dua tempat.** (a) `harga_default` sertifikat ≠ 0
   ditolak service **dan** sudah dijaga constraint `sertifikat_gratis` di
   database; (b) harga cabang sertifikat ≠ 0 ditolak service
   (`SetPrices`) — database tidak bisa menjaganya lewat CHECK karena
   constraint tidak boleh merujuk tabel lain, jadi tidak ada trigger baru yang
   dipasang (Database.md §4.3 tidak mendefinisikannya). Mengubah produk
   reguler menjadi sertifikat juga ditolak bila masih ada harga cabang ≠ 0.
4. **Katalog admin menampilkan produk nonaktif.** Sebelum kartu ini
   `GET /admin/products` hanya mengembalikan produk aktif. Master produk perlu
   memperlihatkan yang nonaktif supaya bisa diaktifkan kembali (kolom Status
   sudah ada di UI sejak F1-03), jadi permukaan admin kini mengembalikan
   **semua** produk dengan filter `aktif` opsional; `/catalog` untuk admin
   (sisi customer portal tidak dipakai) tidak berubah.
5. **Produk terpakai tidak dihapus, hanya dinonaktifkan.** `DELETE` menjawab
   409 `PRODUCT_IN_USE` bila ada `order_lines.product_id` (FK ON DELETE
   RESTRICT). Kode produk unik bila diisi (`kode` NULL boleh berulang) →
   409 `KODE_TAKEN`.
6. **Batch edit satu transaksi, audit per sel yang benar-benar berubah.**
   `ApplyPriceChanges` menulis seluruh `branch_prices` + entri audit dalam satu
   `pgx.Tx`. Service membaca nilai sekarang lebih dulu untuk menghitung diff,
   lalu: sel baru → audit `create`, sel berubah → audit `update` (dengan
   `before`), sel dikosongkan → audit `delete`, sel yang nilainya sama →
   `diabaikan` tanpa audit (PRD AP-KAT-06). `entity='branch_price'`,
   `entity_id` = `product_id`, snapshot memuat `branch_id` + `harga`; aktor
   (kind/id/nama) dan `ip` selalu terisi.
7. **"Salin harga" = replace, bukan merge (PRD AP-KAT-04).** Matriks cabang
   tujuan menjadi persis matriks cabang sumber: sel yang tidak ada di sumber
   ikut terhapus. Karena itu cabang sumber yang belum punya harga ditolak
   (`VALIDATION_FAILED`) alih-alih mengosongkan cabang tujuan tanpa peringatan,
   dan cabang tujuan = sumber diabaikan. Satu entri audit `copy` per cabang
   tujuan (entity_id = cabang tujuan, `before` = jumlah sel lama, `after` =
   sumber + jumlah + berapa yang berubah/terhapus).
8. **Master cabang tetap milik `internal/branch`.** Katalog tidak membaca
   tabel `branches`: kolom cabang pada matriks diambil handler dari
   `a.d.Branches.List` (aktor yang sama, kapabilitas `admin.branches`), dan
   setiap `branch_id` yang dikirim pada batch/salin diverifikasi lewat daftar
   itu sebelum menyentuh harga. Ini melanjutkan keputusan F1-06 ("F1-08 pakai
   layanan cabang yang baru").
9. **Batas batch.** Maksimal 1000 sel per permintaan (`maxPriceBatch`), minimal
   1 sel; `harga` negatif ditolak validasi transport (`gte=0`) dan service;
   `branch_id`/`product_id` wajib.
10. **Harga order lama tidak tersentuh.** Paket ini hanya menulis
    `branch_prices` + `audit_logs`; tidak ada jalur yang menyentuh `orders`/
    `order_lines`. Snapshot harga ada di `order_lines.harga_satuan`
    (BR-05/AP-KAT-05) — dibuktikan di uji integrasi dan probe hidup.
11. **Error domain baru dipetakan di satu tempat** (`httpapi/errors.go`):
    `KODE_TAKEN` (409), `PRODUCT_IN_USE` (409), `VALIDATION_FAILED` dengan
    field `katalog` untuk data produk/harga.
12. **UI admin `/katalog` dikerjakan di kartu ini** (bukan kerangka lagi):
    tab Produk (filter + modal tambah/ubah + aktif/nonaktif + hapus) dan tab
    Matriks harga (grid Produk × Cabang, edit inline lewat batch PUT, pencarian
    produk & cabang, panel "salin harga"). Halaman katalog cabang
    (`/katalog` customer) sudah benar sejak F1-03 dan tetap dipakai apa adanya
    (memakai `GET /catalog`).

## Berkas yang berubah

Backend:

- `api/db/queries/catalog.sql` — query baru: `ListProductsForAdmin`,
  `GetProductForAdmin`, `UpdateProduct`, `DeleteProduct`, `ProductKodeTaken`,
  `ProductHasOrderLines`, `ListBranchPrices`, `DeleteBranchPrice`,
  `DeleteBranchPricesOfBranch`; `CreateProduct` diberi `kode` nullable +
  `is_active`; `UpsertBranchPrice` tidak berubah.
- `api/internal/db/sqlc/catalog.sql.go` — hasil `sqlc-v1.31.1 generate`.
- `api/internal/catalog/catalog.go` — sentinel error + model + service:
  `ListProducts/GetProduct/CreateProduct/UpdateProduct/DeleteProduct`,
  `Prices`, `SetPrices`, `CopyPrices` + validasi domain.
- `api/internal/catalog/memory.go` (baru) — repository in-memory (uji + demo),
  sekaligus merekam audit.
- `api/internal/catalog/postgres/postgres.go` — repository PostgreSQL
  (transaksi + audit untuk produk & matriks harga).
- `api/internal/httpapi/handlers_catalog.go` — 8 handler baru + filter +
  validasi keberadaan cabang; `/catalog` & `/me/branch` tidak berubah.
- `api/internal/httpapi/router.go` — rute `/admin/products*` dan
  `/admin/prices*` di bawah kapabilitas `admin.catalog`.
- `api/internal/httpapi/errors.go` — pemetaan `KODE_TAKEN`, `PRODUCT_IN_USE`,
  `ErrInvalidData` katalog.
- `api/internal/audit/audit.go` — entity `product`, `branch_price`; action
  `copy`.
- `openapi/openapi.yaml` — 6 operasi baru (products/{id}, prices, prices/copy),
  9 skema baru, 2 kode error baru.

Uji:

- `api/internal/catalog/catalog_test.go` (baru, 11 uji unit domain).
- `api/internal/httpapi/router_catalog_test.go` (baru, 5 uji HTTP in-memory).
- `api/internal/httpapi/integration_catalog_pg_test.go` (baru, 4 uji integrasi
  PostgreSQL nyata).
- `api/internal/httpapi/router_test.go`, `router_auth_test.go`,
  `router_users_test.go` — fake `catalog.Repository` kini menanam
  `catalog.Memory` supaya metode baru tidak perlu disalin manual.

Frontend:

- `web/apps/admin/src/pages/katalog.tsx` (baru) — halaman `/katalog` fungsional.
- `web/apps/admin/src/katalog.test.tsx` (baru, 6 uji).
- `web/apps/admin/src/pages/admin-pages.module.css` — gaya grid matriks.
- `web/apps/admin/src/pages/shell-pages.tsx`, `routes.tsx` — `KatalogAdminPage`
  (kerangka F1-03) dihapus, diganti `pages/katalog.tsx`.

## Bukti verifikasi (dijalankan sendiri)

### Gerbang statis

| Perintah | Hasil |
|---|---|
| `gofmt -l .` (api) | kosong |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `sqlc-v1.31.1 generate` | idempoten (md5 `internal/db/sqlc/*.go` sama sebelum/sesudah) |
| `sh scripts/check-secrets.sh` | "Tidak ada rahasia terdeteksi pada berkas yang dilacak" (exit 0) |
| `@redocly/cli@1.34.20 lint openapi/openapi.yaml` | valid, **10 warning** (sama persis dengan baseline: 5 tag-description, 3 operation-4xx-response di healthz/readyz/meta, 1 license, 1 server example — tidak ada dari path baru) |
| `npm run typecheck` (4 workspace) | exit 0 |
| `npm run build` | dua SPA terbangun (`admin` 162 modul, `customer` 158 modul) |

### Uji backend

DB uji: PostgreSQL 16.15 di `127.0.0.1:5455` (`TEST_DATABASE_URL` skema
`grahita_test`, `scripts/pg-dev-shim.sql` dipasang otomatis oleh harness).

```
go test -race -p 1 -count=1 -v ./...   → 216 PASS, 0 FAIL, 0 SKIP, 8 paket ok
```

(Hitungan PASS termasuk subtest, metode yang sama dipakai kartu-kartu
sebelumnya; 0 SKIP berarti seluruh uji integrasi PostgreSQL benar-benar
dijalankan — `TEST_DATABASE_URL` diisi.)

20 uji baru kartu ini (semuanya PASS):

```
internal/catalog (unit domain)
  --- PASS: TestKatalogCabangHanyaProdukBerharga
  --- PASS: TestKatalogCabangMengabaikanProdukNonaktif
  --- PASS: TestCRUDProduk
  --- PASS: TestHapusProdukMencatatAudit
  --- PASS: TestSertifikatSelaluNol
  --- PASS: TestUbahProdukJadiSertifikatMenolakHargaCabang
  --- PASS: TestBatchEditMatriksHarga
  --- PASS: TestBatchEditMenolakMasukanTidakValid
  --- PASS: TestSalinHargaMenggantikanMatriksTujuan
  --- PASS: TestFilterDaftarProdukAdmin
  --- PASS: TestOtorisasiKatalog
internal/httpapi (HTTP penuh, in-memory)
  --- PASS: TestApiProdukCRUDDanFilter
  --- PASS: TestApiProdukTolakAkunCabang
  --- PASS: TestApiMatriksHargaBatchDanFilter
  --- PASS: TestApiSalinHargaAntarCabang
  --- PASS: TestApiKatalogCabangHanyaHargaSendiri
internal/httpapi (integrasi PostgreSQL nyata)
  --- PASS: TestPGKatalogHargaBerbedaDanProdukTanpaHarga
  --- PASS: TestPGPerubahanHargaTidakMengubahPesananLama
  --- PASS: TestPGPenyalinanHargaMenggantikanCabangTujuan
  --- PASS: TestPGMasterProdukValidasiDanAudit
```

Yang benar-benar dibuktikan (bukan hanya status kode):

- **Dua cabang melihat harga berbeda**: cabang A 100000 / cabang B 200000 dari
  baris `branch_prices` masing-masing (`GET /catalog`), dan mengubah harga
  cabang A tidak mengubah cabang B.
- **Produk tanpa harga cabang tidak tampil & tidak bisa dipesan**: produk
  dengan `harga_default=999000` tanpa baris `branch_prices` tidak ada di
  daftar `/catalog`; setelah selnya diisi lewat batch edit produk muncul,
  setelah selnya dikosongkan produk hilang lagi **dan baris
  `branch_prices` benar-benar terhapus** (SELECT count = 0).
- **Batch edit & filter matriks**: filter `q`, `jenis`, `aktif` (produk) dan
  `branch_q` (cabang) menyaring di SQL nyata; `?aktif=mungkin` → 400; batch
  edit mengembalikan `{diubah, dihapus, diabaikan}` yang cocok dengan keadaan
  DB; `branch_id` tak dikenal → 400 sebelum harga tersentuh.
- **Salin harga**: setelah `POST /admin/prices/copy {semua:true}` isi
  `branch_prices` cabang B identik dengan cabang A (dibandingkan dua arah
  lewat `EXCEPT` = 0 baris berbeda) dan harga yang hanya ada di B terhapus;
  tepat satu entri audit `copy` per cabang tujuan.
- **Harga order lama tidak berubah**: pesanan draft berisi order line dengan
  `harga_satuan` snapshot; setelah harga master dinaikkan, `harga_satuan`,
  `subtotal`, dan `orders.total` tetap; `DELETE` produk itu → 409
  `PRODUCT_IN_USE`.
- **Sertifikat**: `harga_default` ≠ 0 ditolak (service 400 + constraint DB),
  harga cabang ≠ 0 ditolak, harga cabang 0 diterima; mengubah produk menjadi
  sertifikat ditolak selama harga cabangnya masih ≠ 0.
- **Audit**: setiap mutasi yang BERHASIL menambah tepat satu entri
  (`entity`/`action`/aktor/ip/`before`/`after`); permintaan yang ditolak
  (409 kode duplikat, 400 sertifikat berbayar, 400 cabang tak dikenal) tidak
  menambah entri apa pun.
- **Otorisasi**: akun cabang 403 pada seluruh 8 rute admin katalog; manager
  boleh; mutasi tanpa CSRF 403 `CSRF_INVALID`.

### Uji & bukti frontend

```
npm test   → api-client 7 · ui 44 · admin 25 · customer 6 = 82 lulus, 0 gagal
```

6 uji baru (`web/apps/admin/src/katalog.test.tsx`) menggerakkan rute
`/katalog` sungguhan: daftar produk termasuk nonaktif + filter `jenis`,
POST tambah produk (body + header CSRF), penguncian harga 0 saat memilih
Sertifikat, simpan satu sel matriks lewat batch PUT, mengosongkan sel
mengirim `harga: null`, dan "salin harga ke semua cabang".

### Probe hidup pada binary sungguhan (`--mode=api`)

DB probe `grahita_f108_live` (baru) → `--mode=migrate` (4 migrasi + River) →
`--mode=seed` (`SEED_PASSWORD`) dengan 7 produk (1 Sertifikat) × 4 cabang,
server di `127.0.0.1:8098`, `Host: grahita-admin.tiusclaw.com` /
`grahita-branch.tiusclaw.com`.

```
POST /auth/login budi@… (manager)                    → 200, cookie CSRF terpasang
GET  /admin/products?q=bio                           → 200, 1 produk
GET  /admin/products?jenis=sertifikat                → 200, P-07 (harga_default 0)
GET  /admin/products?aktif=mungkin                   → 400 VALIDATION_FAILED
POST /admin/products P-PRB3 (harga 50000)            → 201; db: P-PRB3|Produk Probe 3|reguler|box|50000|9|t
POST /admin/products kode sama                       → 409 KODE_TAKEN
POST /admin/products sertifikat harga_default 5000   → 400 VALIDATION_FAILED
GET  /admin/prices                                   → produk=8 cabang=4 sel=24
PUT  /admin/prices {P-01 bandung: 111000}            → 200 {diubah:1, dihapus:0, diabaikan:0}
PUT  /admin/prices {sertifikat bandung: 5000}        → 400 VALIDATION_FAILED
GET  /catalog (cabang Bandung)                       → 6 produk, harga P-01 = 111000
GET  /catalog (cabang Bekasi)                        → 6 produk, harga P-01 = 350000
POST /admin/prices/copy {bandung, semua:true}        → 200 {jumlah:6, salinan:[3 cabang]}
GET  /catalog (cabang Bekasi)                        → harga P-01 = 111000 (tersalin)
PUT  /admin/prices {P-02 bandung: null}              → 200 {dihapus:1}
GET  /catalog (Bandung) → 5 produk (P-02 tidak ada) ; (Bekasi) → 6 produk (masih ada)
PUT  /admin/products/{P-PRB3} {nama…, is_active:false}→ 200 ; DELETE → 204 ; sisa baris = 0
psql order_lines (snapshot)  sebelum harga dinaikkan → 85000 / 255000
PUT  /admin/prices {P-03 bandung: 999000}            → 200
psql branch_prices           sesudah                  → 999000
psql order_lines             sesudah (harus tetap)    → 85000 / 255000   ✔
psql orders.total            sesudah (harus tetap)    → 255000           ✔
DELETE /admin/products/{P-03} (sudah dipakai pesanan)→ 409 PRODUCT_IN_USE
psql audit_logs → 20 entri, semua actor "Budi Santoso" + ip 127.0.0.1:
      product create/update/delete, branch_price create/update/delete/copy(×3)
      before/after terisi untuk update/delete/copy; permintaan yang ditolak tidak menambah entri
```

## Perilaku & keputusan yang menyimpang/eksplisit

- `PUT /admin/products/{id}` dengan body `{}` = tidak ada perubahan → 200
  (semua field opsional, sama seperti pola F1-05/F1-06).
- `GET /admin/prices` mengembalikan **semua** cabang (termasuk nonaktif) dengan
  penanda `is_active`, karena harga tetap boleh disiapkan sebelum cabangnya
  diaktifkan; UI menandai kolom nonaktif.
- Field `harga` yang tidak dikirim pada batch edit diperlakukan sama dengan
  `harga: null` (mengosongkan sel) — didokumentasikan di kontrak OpenAPI.
- Catatan turunan F1-06 yang diminta tetap berlaku: permukaan master cabang
  lama (`catalog.Service.Branches`, `ListBranches`) tidak dihidupkan lagi.
- Diff/audit dihitung dari pembacaan nilai sekarang di luar transaksi tulis:
  dua batch yang menyentuh sel yang sama secara bersamaan bisa menghasilkan
  entri `diabaikan`/`update` yang tidak sempurna di audit (kosmetik). Isi
  `branch_prices` sendiri selalu benar karena setiap perubahan dieksekusi
  dalam transaksinya sendiri.

## Yang belum dikerjakan (batas kartu)

- **Notifikasi/aksi dari admin ke cabang** saat produknya hilang dari katalog
  (tidak diminta PRD §4.5).
- **Virtualisasi grid matriks** (SystemDesign §7.2 menyebut virtualisasi untuk
  tabel panjang) — grid saat ini tabel biasa dengan `overflow-x`; jumlah
  produk/cabang fase 1 masih kecil.
- **Pemesanan produk tanpa harga** diuji di level katalog; penolakan saat
  submit pesanan adalah bagian F1-11 (`/orders`).
- Uji E2E lintas kartu T2 (tester) dengan rekening/pengaturan.

## Sisa risiko

- Halaman `/gaya` (galeri komponen) masih ikut build produksi (catatan F1-03).
- PostgreSQL 18 asli/CI `postgres:18-alpine` belum dieksekusi di sesi ini
  (PG16 + `scripts/pg-dev-shim.sql`), sama seperti kartu F1-02/F1-04/F1-06.
- `golangci-lint` tidak tersedia di sesi ini; gate lint tidak direproduksi
  (`gofmt`/`vet`/`build` + uji arsitektur lolos).
- Harga cabang Sertifikat hanya dijaga service (bukan trigger database) —
  jalur tulis di luar service (mis. psql manual) tidak dicegah; constraint
  `sertifikat_gratis` tetap menjaga `products.harga_default`.
- DB probe `grahita_f108_live` ditinggalkan (kebijakan sesi ini menolak
  `DROP DATABASE`); skrip probe ada di workspace, bukan di repo.
