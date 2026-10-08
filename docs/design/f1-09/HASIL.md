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
7. **Tidak ada permukaan pengaturan lintas-role.** `GET /api/v1/settings` dan
   `settings.Service.Runtime` **dihapus** (keputusan CEO atas review QA putaran
   1): endpoint itu tidak punya konsumen di `web/`, tidak tercantum di
   SystemDesign §6, dan berbenturan dengan kriteria kartu "hanya Superadmin
   dapat membaca/mengubah lewat UI maupun API". Baca lengkap & ubah pengaturan
   hanya lewat `/api/v1/admin/settings` (Superadmin); filter "semua kunci kecuali
   `ai`" tidak disisakan dalam bentuk apa pun. Rute yang dihapus dijawab **404**
   oleh router (diuji untuk anon, cabang, Manager, dan Superadmin).
8. **Audit selalu satu transaksi dengan mutasinya** (SystemDesign §5.4).
   `audit.EntityBankAccount`/`EntitySetting` dicatat lewat `audit.NewEntry` dan
   disimpan repository **di dalam** `pgx.Tx` yang sama; kegagalan validasi atau
   constraint tidak meninggalkan event (diuji di PostgreSQL nyata). Satu batch
   pengaturan → satu entri audit **per kunci yang nilainya benar-benar berubah**
   (perbandingan bentuk kanonik: urutan kunci `ai` dan penulisan angka tidak
   dianggap perubahan); mengirim ulang nilai yang sama adalah **no-op** — tetap
   `200` tetapi tidak menulis baris maupun event audit baru.
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
  tak dikenal), update all-or-nothing, audit hanya untuk kunci yang berubah
  (no-op nilai sama, nilai bawaan dengan urutan kunci berbeda, batch campuran,
  rollback saat simpan gagal), dan `All` satu-satunya permukaan baca.
- `internal/httpapi/router_bank_test.go` + `router_settings_test.go` — HTTP:
  anon 401 (mutasi 403 CSRF lebih dulu), cabang/Manager 403, Superadmin 200,
  duplikat 409 `BANK_DUPLICATE`, validasi per-field 400, rekening nonaktif
  hilang dari daftar publik, audit tercatat, dan `GET /api/v1/settings` 404
  untuk semua peran.
- `internal/httpapi/integration_bank_pg_test.go` — PostgreSQL nyata: constraint
  unik, audit transaksional (percobaan duplikat tidak meninggalkan event),
  no-op nilai sama (0 baris, 0 audit), batch gagal tidak mengubah nilai lama,
  batch campuran hanya mencatat kunci yang berubah, dan pesanan lama tidak
  terpengaruh perubahan `biaya_packaging`.

## Risiko sisa

- Rentang nilai adalah keputusan kami (didokumentasikan di `settings.go` dan
  deskripsi OpenAPI). Bila Pusat butuh batas lain (mis. `max_mb` > 1024),
  rentangnya mudah disesuaikan di satu tempat.
- Permukaan pengaturan lintas-role sudah **dihapus**, bukan dibatasi allowlist:
  bila kelak portal butuh konstanta operasional (mis. biaya packaging), nilai
  itu disajikan endpoint yang memang memiliki datanya (draft pesanan/katalog/
  unggah bukti), bukan endpoint pengaturan baru.
- Halaman rekening/pengaturan memakai `useState`/`useApi` sederhana (belum
  TanStack Query) mengikuti pola kartu sebelumnya; cache akan menyusul bersama
  kartu fitur lain.
- Belum ada `DELETE` rekening (PRD §4.6 hanya menyebut Aktif; rekening
  dinonaktifkan, bukan dihapus) dan belum ada halaman audit log — keduanya di
  luar cakupan kartu ini.

## Revisi putaran 1 (permintaan QA)

Review QA putaran 1 (lensa artifact, commit `5079e69`) meminta dua koreksi;
keduanya dikerjakan di kartu yang sama dengan keputusan CEO pada komentar
2026-10-08 12:05.

1. **`GET /api/v1/settings` + `settings.Service.Runtime` DIHAPUS** (opsi 1a).
   Tidak ada lagi jalur "semua kunci kecuali `ai`": pengaturan hanya dibaca dan
   diubah lewat `/api/v1/admin/settings` (Superadmin). Perubahan: `router.go`
   (rute dihapus), `handlers_settings.go` (handler dihapus), `openapi/openapi.yaml`
   (path dihapus, deskripsi `put` diperjelas), `settings.go` (`merged` tanpa
   parameter allow). Uji: `TestApiSettingsRuntimeTanpaAi` → diganti
   `TestApiSettingsLintasRoleTidakAda` (404 untuk anon, cabang, Manager,
   Superadmin) dan blok 404 di uji integrasi PG.
2. **Audit hanya untuk kunci yang berubah.** `Service.Update` membandingkan
   nilai berlaku dengan nilai baru dalam bentuk kanonik (`samaNilai`) dan
   melewati kunci yang sama — tidak ada baris ditulis maupun entri audit.
   Uji unit baru `TestUpdateHanyaMencatatKunciYangBerubah` (no-op nilai sama,
   nilai bawaan `ai` yang urutan kuncinya berbeda, batch campuran, rollback
   saat simpan gagal) dan blok audit di `TestPGSettingsValidasiAuditDanSnapshotPackaging`
   (no-op = 0 baris + 0 audit, batch valid = 2, PUT ulang = tetap 2, batch
   campuran = 3, `ai` tidak valid tetap 3, `ai` valid = 4).

### Gerbang setelah revisi (dijalankan ulang)

| Gerbang | Perintah | Hasil |
|---|---|---|
| gofmt/vet/build | `gofmt -l .`; `go vet ./...`; `go build ./...` (di `api/`) | kosong; exit 0; exit 0 |
| sqlc pin | `sqlc-v1.31.1 generate` + `git status --porcelain -- internal/db/sqlc db` | tanpa diff |
| seluruh suite (race) | `go test -race -p 1 -count=1 ./...` | **EXIT=0**, 11 paket `ok`, 0 FAIL |
| integrasi PG | `-run 'TestPGBank\|TestPGSettings' -v` dengan `TEST_DATABASE_URL` (PG16 `grahita_test` + shim) | `--- PASS: TestPGBankRekeningTransaksionalDanNonaktif`; `--- PASS: TestPGSettingsValidasiAuditDanSnapshotPackaging` |
| rahasia | `./scripts/check-secrets.sh` | exit 0 |
| OpenAPI | `redocly lint openapi/openapi.yaml` (`@redocly/cli` v1.34.20, pin mayor 1) | valid, 10 warning gaya lama |
| frontend | `npm run typecheck`; `npm run test` (di `web/`) | exit 0; 76 uji lulus (api-client 7, ui 44, admin 19, customer 6) |

