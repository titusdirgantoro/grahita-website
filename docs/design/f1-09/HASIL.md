# F1-09 — Rekening bank dan pengaturan sistem

Kartu: **t_58e594c4 — F1-09 Rekening bank dan pengaturan sistem**

Dokumen acuan: PRD §2.1, §4.6, §4.9; SystemDesign §5.4, §6; Database.md §4.3,
§4.7, §5.

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Permukaan API yang dikerjakan

| Method | Path | Peran | Sesuai kartu |
|---|---|---|---|
| GET | `/api/v1/admin/bank-accounts` | Superadmin | ya (+ rekening nonaktif untuk dikelola) |
| POST | `/api/v1/admin/bank-accounts` | Superadmin | ya |
| PUT | `/api/v1/admin/bank-accounts/{id}` | Superadmin | ya (termasuk aktif/nonaktif) |
| GET | `/api/v1/bank-accounts` | semua yang login | tambahan: HANYA rekening AKTIF untuk instruksi pembayaran & referensi OCR |
| GET | `/api/v1/admin/settings` | Superadmin | ya (default + tersimpan, termasuk `ai`) |
| PUT | `/api/v1/admin/settings` | Superadmin | ya (satu kunci atau batch `values`) |

Seluruh rute terdaftar di `openapi/openapi.yaml`; dua arah router ⇄ kontrak
dijaga `TestKontrakOpenAPIIkutRouter` (rute baru tidak akan lolos tanpa
dokumentasi).

## Keputusan desain

1. **Rekening bank punya paket sendiri: `internal/bank`** (domain + service),
   `internal/bank/postgres` (repository), `internal/bank/memory.go` (repo uji).
   Mengikuti pola F1-06 (`branch`). Nama kolom database tetap `bank`
   (enum `bank_code` BCA/BRI) sehingga JSON field-nya `bank`, bukan `bank_code`.
   `bank::text` di query menghindari tipe enum hasil generate bocor ke service.
2. **Otorisasi ditegakkan di service, bukan hanya router.** `List`/`Create`/
   `Update` memeriksa `auth.CapAdminBank`; hanya Superadmin punya kapabilitas
   itu (PRD §2.1). Router juga memasang `requireCap` supaya Manager/akun cabang
   ditolak sebelum handler (403 FORBIDDEN).
3. **`GET /api/v1/bank-accounts` (semua yang login) hanya mengembalikan
   rekening AKTIF.** Inilah yang dipakai Customer Portal untuk instruksi
   pembayaran dan referensi pencocokan rekening tujuan verifikasi OCR
   (PRD §4.6/§5). Rekening nonaktif tidak pernah muncul di sini; daftar
   pengelolaan Superadmin tetap memuat keduanya agar bisa diaktifkan kembali.
4. **Validasi domain rekening**: bank ∈ {BCA, BRI}, nama_pemilik 2–120 karakter,
   nomor hanya digit 6–34 (spasi/titik/hubung dinormalisasi dibuang). Pasangan
   (bank, nomor) unik di database → konflik 409 `BANK_DUPLICATE`.
5. **Pengaturan punya spesifikasi per kunci (tipe + rentang).** Nilai divalidasi
   di domain (`settings.Check`), bukan dipercaya dari klien. Nilai uang Rupiah
   **wajib bilangan bulat** (tolak `3000.5`, `"3000"`, negatif) sesuai BR-10.
   Rentang: `biaya_packaging` 0..100.000.000; `cnc_hari`/`token_hari` 1..3650;
   `reset_menit` 1..43200; `max_mb` 1..1024; `max_file` 1..100;
   `digit_invoice` 1..10; `manager_boleh_revisi` boolean; `ai` objek
   {aktif bool, min_keyakinan 0..1, tol_kurang/tol_lebih 0..1e12, digit_rek
   1..20, mundur 0..3650}. Field `ai` yang tidak dikirim dilengkapi default
   sehingga yang tersimpan selalu objek lengkap; field tak dikenal ditolak.
6. **Nilai tersimpan selalu bentuk kanonik.** Bilangan bulat ditulis tanpa
   pecahan, pecahan `min_keyakinan` tanpa nol berekor (`0.9`), `ai` diurutkan
   kunci-nya — sehingga yang tampil di audit dan di UI konsisten.
7. **`GET /api/v1/settings` (kedua portal) tidak lagi memuat `ai`.** Endpoint
   itu tetap ada sebagai konstanta UI (mis. biaya packaging pada pesanan
   sertifikat, CP-SER-02), tetapi setelan internal verifikasi AI hanya dibaca
   Superadmin lewat `/admin/settings`. Ini memperketat kriteria "hanya
   Superadmin dapat membaca pengaturan lewat UI maupun API".
8. **Audit selalu satu transaksi dengan mutasinya** (SystemDesign §5.4).
   `audit.EntityBankAccount`/`EntitySetting` dicatat lewat `audit.NewEntry` dan
   disimpan repository **di dalam** `pgx.Tx` yang sama; kegagalan validasi atau
   constraint tidak meninggalkan event (diuji di PostgreSQL nyata). Satu batch
   pengaturan → satu entri audit per kunci yang berubah (before = nilai lama,
   after = nilai baru).
9. **Batch all-or-nothing.** `PUT /admin/settings` menerima `{"values":{...}}`
   (validasi SEMUA dulu, baru simpan) atau bentuk lama `{"key":..,"value":..}`
   yang responsnya tetap `{key,value}` demi kompatibilitas F1-04.
10. **Mengubah `biaya_packaging` tidak mengubah pesanan lama** (BR-05,
    CP-SER-05). Paket `settings` tidak pernah menyentuh `orders`/`order_lines`;
    harga di-snapshot saat submit. Dibuktikan di uji integrasi: pesanan dengan
    baris packaging 3000 tetap 3000 setelah setelan diubah ke 5000.
11. **Frontend**: `RekeningPage` dan `PengaturanPage` kini memuat data nyata
    (CRUD rekening; form pengaturan tervalidasi + pesan error per field dari
    `ApiClientError.mapFields`). Keduanya di balik `PenjagaSuperadmin`, dan
    pemuatannya sengaja diletakkan di komponen-dalam supaya Manager **tidak
    memanggil API** sama sekali (bukan sekadar tidak menampilkan hasilnya).
12. **`KnownKeys` berubah dari variabel menjadi fungsi** `KnownKeys()`
    (daftar terurut) dan tipe `Repository.Upsert` diganti `Repository.Save`
    (banyak perubahan + audit dalam satu transaksi). Pemakaian lama di uji
    httpapi disesuaikan.

## Bukti verifikasi (perintah + hasil nyata)

Semua perintah dijalankan di `/home/hermes/projects/grahita-website`.
Go: `go1.27.1`; sqlc pin CI: `v1.31.1`; PostgreSQL uji: 16.15 + `scripts/pg-dev-shim.sql`.

| Gerbang | Perintah | Hasil |
|---|---|---|
| gofmt | `gofmt -l .` (di `api/`) | kosong (bersih) |
| vet | `go vet ./...` (di `api/`) | exit 0 |
| build | `go build ./...` (di `api/`) | exit 0 |
| sqlc pin | `sqlc-v1.31.1 generate` lalu `git diff --stat -- internal/db/sqlc db` | tanpa diff |
| uji unit baru | `go test ./internal/bank/... ./internal/settings/... ./internal/httpapi/...` | ok |
| seluruh suite (race) | `go test -race -p 1 -count=1 ./...` | **EXIT=0**, semua paket `ok` (httpapi 154s) |
| integrasi PG | `go test -p 1 -count=1 ./...` dengan `TEST_DATABASE_URL` | `--- PASS: TestPGBankRekeningTransaksionalDanNonaktif`; `--- PASS: TestPGSettingsValidasiAuditDanSnapshotPackaging` |
| rahasia | `./scripts/check-secrets.sh` | "Tidak ada rahasia terdeteksi" (exit 0) |
| OpenAPI | `npx @redocly/cli@1 lint openapi/openapi.yaml` | "Your API description is valid" (10 warning gaya lama) |
| frontend typecheck | `npm run typecheck` (di `web/`) | exit 0 |
| frontend uji | `npm run test` (di `web/`) | 76 uji lulus (admin 19, ui 44, api-client 7, customer 6) |
| frontend build | `npm run build` (di `web/`) | 2 SPA ter-build |

Uji baru yang relevan (50 baris PASS termasuk subtest):

- `internal/bank/bank_test.go` — normalisasi bank/nomor, validasi domain,
  duplikat, otorisasi hanya Superadmin, `Active` menyembunyikan nonaktif,
  audit create/update.
- `internal/settings/settings_test.go` — default sesuai PRD §4.9, validasi
  tipe/rentang per kunci, validasi `ai` (termasuk parsial + penolakan field
  tak dikenal), update all-or-nothing, audit satu entri per kunci, `All` vs
  `Runtime` (tanpa `ai`).
- `internal/httpapi/router_bank_test.go` + `router_settings_test.go` — HTTP:
  anon 401 (mutasi 403 CSRF lebih dulu), cabang/Manager 403, Superadmin 200,
  duplikat 409 `BANK_DUPLICATE`, validasi per-field 400, rekening nonaktif
  hilang dari daftar publik, audit tercatat.
- `internal/httpapi/integration_bank_pg_test.go` — PostgreSQL nyata: constraint
  unik, audit transaksional (percobaan duplikat tidak meninggalkan event),
  batch gagal tidak mengubah nilai lama, dan pesanan lama tidak terpengaruh
  perubahan `biaya_packaging`.

## Risiko sisa

- Rentang nilai adalah keputusan kami (didokumentasikan di `settings.go` dan
  deskripsi OpenAPI). Bila Pusat butuh batas lain (mis. `max_mb` > 1024),
  rentangnya mudah disesuaikan di satu tempat.
- `GET /api/v1/settings` tetap dapat dibaca semua pengguna yang login untuk
  konstanta operasional (biaya packaging dsb.) — memang dipakai UI kedua
  portal; setelan `ai` sudah dikeluarkan dari sana. Bila kelak konstanta itu
  dianggap sensitif, tinggal dibatasi lewat allowlist `Runtime`.
- Halaman rekening/pengaturan memakai `useState`/`useApi` sederhana (belum
  TanStack Query) mengikuti pola kartu sebelumnya; cache akan menyusul bersama
  kartu fitur lain.
- Belum ada `DELETE` rekening (PRD §4.6 hanya menyebut Aktif; rekening
  dinonaktifkan, bukan dihapus) dan belum ada halaman audit log — keduanya di
  luar cakupan kartu ini.
