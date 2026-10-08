# F1-06 — Master cabang dan token registrasi

Kartu: **t_056b363a — F1-06 Master cabang dan token registrasi**

Dokumen acuan: PRD §3.1, §4.4, §8; SystemDesign §4.1, §5.4, §6; Database.md
§4.1–4.2.

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Permukaan API yang dikerjakan

| Method | Path | Peran | Sesuai kartu |
|---|---|---|---|
| GET | `/api/v1/admin/branches` | Manager + Superadmin | ya (+ filter `q`, `aktif`, `cnc`, `special`) |
| POST | `/api/v1/admin/branches` | Manager + Superadmin | ya |
| GET | `/api/v1/admin/branches/{id}` | Manager + Superadmin | ya (sudah ada sejak F1-04, kini dari paket `branch`) |
| PUT | `/api/v1/admin/branches/{id}` | Manager + Superadmin | ya (mutasi eksplisit) |
| DELETE | `/api/v1/admin/branches/{id}` | Manager + Superadmin | ya (CRUD; AP-CAB-04) |
| POST | `/api/v1/admin/branches/{id}/token` | Manager + Superadmin | ya |

Semua rute terdaftar di `openapi/openapi.yaml` dan dijaga dua arah
(router ⇄ kontrak) oleh `TestKontrakOpenAPIIkutRouter` +
`TestApiKontrakPathMasterCabang`.

## Keputusan desain

1. **Master cabang punya paket sendiri: `internal/branch`.** SystemDesign §2
   menetapkan `branch/` untuk "cabang + user cabang". Sebelumnya GET
   `/admin/branches` membaca lewat paket `catalog`; sekarang seluruh CRUD
   cabang + token registrasi dimiliki `internal/branch`, dan sisa pembacaan
   katalog (produk + `/me/branch`) tetap di `internal/catalog`. Supaya tidak
   ada dua pemilik data, `catalog.Service.Branches` + `catalog.Repository.
   ListBranches` + query `ListBranches` dihapus (tidak ada pemakainya lagi).
2. **Filter daftar memakai `sqlc.narg`** sehingga satu query melayani semua
   kombinasi `q` (ILIKE nama/email), `aktif`, `cnc`, `special`. Parameter
   boolean yang bukan boolean ditolak `400 VALIDATION_FAILED`.
3. **Email cabang unik dan = email login.** `branches.email` citext unik;
   mengubah email cabang **ikut menyelaraskan `branch_users.email`** dalam
   transaksi yang sama (Database.md §4.1). Jika akun cabang sudah ada, email
   login benar-benar berpindah: login dengan email baru berhasil, email lama
   401 (dibuktikan di uji integrasi).
   *Tambahan pengaman*: email yang sudah dipakai **akun admin** juga ditolak
   (`EMAIL_TAKEN`), karena `auth.Service.findByEmail` mencari admin lebih dulu
   — tanpa aturan ini, cabang dengan email itu tidak akan pernah bisa masuk.
4. **Cabang berp pesanan hanya dinonaktifkan (AP-CAB-04).** `DELETE` menjawab
   `409 BRANCH_HAS_ORDERS`; yang belum punya pesanan dihapus bersama akun,
   sesinya (`sessions` tidak memakai ON DELETE CASCADE) dan tokennya — riwayat
   audit tetap tinggal karena `audit_logs` tidak berelasi ke cabang.
5. **Cabang nonaktif tidak bisa login.** Aturan `boleh_login` (F1-04) tetap
   satu-satunya sumber kebenaran; paket ini **tidak** menduplikasinya di
   handler. handler hanya menuliskan flag `is_active`; sesi yang berjalan
   gugur dan login berikutnya 403 (dibuktikan di uji integrasi + probe curl).
6. **Token registrasi sekali pakai, disimpan sebagai SHA-256.** Format tampilan
   `GRH-XXXX-XXXX-XXXX-XXXX` (alfabet 32 karakter tanpa I/O/0/1, 80 bit
   entropi); `registration_tokens.token_prefix` = 8 karakter pertama untuk
   ditampilkan ulang di Admin Portal. Normalisasi token (huruf besar, tanpa
   pemisah) dan hashing dipusatkan di `auth.NormalizeRegistrationToken` /
   `auth.HashRegistrationToken` supaya **generate (F1-06) dan validasi
   (F1-07) tidak pernah berbeda**. Masa berlaku dari pengaturan `token_hari`
   (PRD §4.9, default 14 hari).
7. **Generate menggantikan token lama yang belum terpakai (AP-CAB-02)** dan
   hanya untuk cabang yang **belum punya akun** (1 cabang = 1 akun). Ditolak
   `409 BRANCH_HAS_ACCOUNT` bila akunnya sudah ada (pesan mengarahkan ke lupa
   kata sandi) dan `409 BRANCH_INACTIVE` bila cabangnya nonaktif — token untuk
   cabang mati tidak ada gunanya. Pemeriksaan ini diulang di dalam transaksi
   yang memegang `FOR UPDATE` pada baris cabang, jadi dua permintaan bersamaan
   tetap tidak bisa melahirkan dua akun.
8. **Audit log transaksional (`audit_logs`).** Paket baru `internal/audit`
   (SystemDesign §5.4) menyediakan `audit.Entry`, `audit.NewEntry(ctx, actor,
   …)` dan penulis `audit/postgres.Insert` yang menerima `Execer` (pool atau
   `pgx.Tx`). Setiap mutasi cabang menulis entri **di dalam transaksi yang
   sama** dengan perubahannya: `entity='branch'`, action `create` / `update` /
   `delete` / `token_generate`, memuat aktor (kind, id, nama), `ip` dari
   `X-Forwarded-For`/`RemoteAddr` (middleware `requestMeta`), dan snapshot JSON
   `before`/`after`. Snapshot create memuat id baris barunya; snapshot token
   hanya memuat `token_prefix` + masa berlaku — **nilai token mentah tidak
   pernah masuk audit**. Permintaan yang ditolak tidak meninggalkan entri.
9. **Flag Special & PI hanya atribut + filter.** Sesuai keputusan user,
   keduanya disimpan sebagai `is_special` / `pajak_in` dan tidak mengubah
   harga, pajak, atau invoice (PRD AP-CAB-05, §8 no.1–2). Tidak ada logika
   harga di paket ini.
10. **Frontend halaman Cabang belum dikerjakan di kartu ini.** Kartu
    menyebutkan permukaan API-nya, dan halaman `/cabang` masih kerangka F1-03
    (read-only, memakai GET `/admin/branches`). UI CRUD + dialog token perlu
    kartu lanjutan (lihat "Yang belum dikerjakan").

## Berkas yang berubah

Backend:

- `api/internal/branch/branch.go` (baru) — service: CRUD, filter, token.
- `api/internal/branch/memory.go` (baru) — repository in-memory (uji + demo),
  sekaligus merekam entri audit.
- `api/internal/branch/postgres/postgres.go` (baru) — repository PostgreSQL.
- `api/internal/audit/audit.go` (baru) — Entry + metadata request.
- `api/internal/audit/postgres/postgres.go` (baru) — penulis `audit_logs`.
- `api/internal/auth/registration.go` (baru) — generate/normalisasi/hash token.
- `api/internal/httpapi/handlers_branches.go` (baru) — 6 handler.
- `api/internal/httpapi/router.go`, `deps.go`, `middleware.go`, `errors.go`
  — rute baru, `Deps.Branches`, middleware `requestMeta` (IP audit), pemetaan
  error `EMAIL_TAKEN` / `BRANCH_HAS_ORDERS` / `BRANCH_HAS_ACCOUNT` /
  `BRANCH_INACTIVE`.
- `api/internal/httpapi/handlers_catalog.go`, `api/internal/catalog/catalog.go`,
  `api/internal/catalog/postgres/postgres.go` — master cabang dipindahkan ke
  paket `branch`.
- `api/internal/platform/pgtypes/pgtypes.go` — helper `Bool`/`BoolPtr` untuk
  argumen filter `sqlc.narg`.
- `api/db/queries/branch.sql` + hasil `sqlc generate`
  (`api/internal/db/sqlc/branch.sql.go`).
- `api/cmd/server/main.go` — perakitan service.
- `openapi/openapi.yaml` — 5 operasi baru, skema `BranchInput`/`BranchUpdate`/
  `RegistrationToken`, `token_prefix`/`token_expires_at`, 3 kode error baru.

Uji:

- `api/internal/branch/branch_test.go` (baru, 11 uji unit domain).
- `api/internal/httpapi/router_branches_test.go` (baru, 5 uji HTTP in-memory).
- `api/internal/httpapi/integration_branches_pg_test.go` (baru, 4 uji integrasi
  PostgreSQL nyata).
- `api/internal/httpapi/integration_pg_test.go` (fixture: service `branch`,
  `audit_logs`/`branch_users`/`registration_tokens` ikut dikosongkan).
- `api/internal/httpapi/router_auth_test.go` (fixture in-memory memberi
  `Deps.Branches`).
- `api/internal/arch/arch_test.go` — aturan "service tidak mengimpor
  transport/net/http" diperluas ke paket `branch` dan `audit`.

## Bukti verifikasi (dijalankan sendiri)

### Gerbang statis

| Perintah | Hasil |
|---|---|
| `gofmt -l .` (api) | kosong |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `sqlc-v1.31.1 generate` | idempoten (md5 `internal/db/sqlc/*.go` sama sebelum/sesudah generate ulang) |
| `golangci-lint v2.14.0 run ./...` (cwd `api/`, konfigurasi repo) | `0 issues` |
| `sh scripts/check-secrets.sh` | "Tidak ada rahasia terdeteksi pada berkas yang dilacak" (exit 0) |
| `npx @redocly/cli@1 lint openapi/openapi.yaml` | valid, **10 warning** (sama seperti sebelum kartu ini; tidak ada warning baru) |

### Uji backend

DB uji: PostgreSQL 16.15 di `127.0.0.1:5455` (`grahita_f106`,
`scripts/pg-dev-shim.sql` dipasang lebih dulu, lalu `--mode=migrate`).

```
go test -race -p 1 -count=1 -v ./...   → 114 PASS, 0 FAIL, 0 SKIP, 8 paket ok
  internal/arch ok · internal/auth ok · internal/branch ok · internal/httpapi ok
  internal/platform/apierr ok · internal/platform/database ok
  internal/users ok · internal/worker ok
```

Uji baru kartu ini (verbose):

```
internal/branch (unit domain)
  --- PASS: TestLayananMasterCabangDibatasiKapabilitas
  --- PASS: TestBuatCabangMenormalkanEmailDanMencatatAudit
  --- PASS: TestBuatCabangMenolakDataTidakValid
  --- PASS: TestBuatDanUbahCabangMenolakEmailTerpakai
  --- PASS: TestUbahCabangMenonaktifkanDanMencatatPerubahan
  --- PASS: TestHapusCabangDitolakBilaSudahPunyaPesanan
  --- PASS: TestGenerateTokenSekaliPakaiMenggantikanTokenLama
  --- PASS: TestGenerateTokenDitolakSaatPunyaAkunAtauCabangNonaktif
  --- PASS: TestDaftarCabangDapatDisaring
  --- PASS: TestCabangTidakDikenalMenghasilkanNotFound
  --- PASS: TestTokenRegistrasiNormalisasiDanHash
internal/httpapi (HTTP penuh, in-memory)
  --- PASS: TestApiMasterCabangButuhSesiDanKapabilitas
  --- PASS: TestApiDaftardanFilterCabang
  --- PASS: TestApiCrudCabang
  --- PASS: TestApiGenerateTokenRegistrasiSekaliPakai
  --- PASS: TestApiKontrakPathMasterCabang
internal/httpapi (integrasi PostgreSQL nyata)
  --- PASS: TestPGCrudCabangKonflikEmailDanAudit
  --- PASS: TestPGCabangNonaktifTidakBisaLoginDanSesiGugur
  --- PASS: TestPGTokenRegistrasiSHA256SekaliPakai
  --- PASS: TestPGHapusCabangTerjagaOlehPesanan
```

Yang benar-benar dibuktikan (bukan hanya status kode):

- **CRUD + filter**: baris benar-benar tersimpan (`nama`, `email` huruf kecil
  karena citext, `is_cnc`/`is_special`/`pajak_in`/`is_active`); filter
  `aktif`/`cnc`/`special`/`q` menyaring di SQL nyata; `?aktif=mungkin` → 400.
- **Konflik email**: `CABANG-F@EXAMPLE.TEST` vs `cabang-f@example.test` → 409
  `EMAIL_TAKEN`; email milik admin → 409 `EMAIL_TAKEN`; permintaan ditolak
  tidak menambah baris audit.
- **Email login mengikuti email cabang**: setelah PUT email baru,
  `branch_users.email` ikut berubah, login dengan email baru 200, email lama
  401.
- **Cabang nonaktif**: sesi berjalan → 401, login → 403, token baru ditolak
  409 `BRANCH_INACTIVE`; setelah diaktifkan lagi login 200.
- **Token registrasi**: format & prefix benar; di database `token_hash` =
  `sha256(canonical(token))`, **bukan** nilai mentah; `used_at` NULL;
  `expires_at` ≈ +14 hari (setelan `token_hari`); tepat **1** token aktif.
  Generate kedua: hash token lama hilang (token lama invalid), hash token baru
  ada, tetap 1 aktif, dan entri audit `token_generate` kedua memuat token lama
  di `before`.
- **Token untuk cabang berakun / nonaktif**: 409 `BRANCH_HAS_ACCOUNT` /
  `BRANCH_INACTIVE`.
- **Hapus cabang**: berp pesanan → 409 `BRANCH_HAS_ORDERS` dan baris tetap ada;
  tanpa pesanan → 204 dan baris `branches`, `branch_users`, `sessions`,
  `registration_tokens` hilang, akunnya tidak bisa login lagi (401), dan entri
  audit `delete` bertahan dengan snapshot `before`.
- **Otorisasi**: akun cabang 403 di keenam rute; tanpa sesi 401; manager boleh
  (kapabilitas `admin.branches`); mutasi tanpa CSRF 403 `CSRF_INVALID` dan
  tidak mencatat audit.
- **Audit**: setiap mutasi yang BERHASIL menambah tepat satu entri
  `entity='branch'` dengan aktor (`admin`, id manager, nama "Budi"), `ip`
  terisi, dan snapshot `before`/`after`; urutan aksi create → update → delete
  diuji eksplisit.

### Bukti end-to-end pada binary sungguhan (`--mode=api`)

DB `grahita_f106_live2` (bersih) → `--mode=migrate` → `--mode=seed`
(`SEED_PASSWORD`), server di `127.0.0.1:8097`, seluruh permintaan memakai
`Host: grahita-admin.tiusclaw.com` (dan `grahita-branch.tiusclaw.com` untuk
akun cabang). Hasil nyata (keluaran lengkap: `f106_probe_final.txt` di
workspace scratch pekerja):

```
POST  /auth/login (budi@grahitaindonesia.com)           → 200, role manager, cookie CSRF terpasang
GET   /admin/branches                                   → 200, 4 cabang seed
GET   /admin/branches?aktif=true&q=bandung              → 200, 1 baris
GET   /admin/branches?aktif=mungkin                     → 400 VALIDATION_FAILED
POST  /admin/branches {"email":"Live@Example.Test",…}   → 201, email menjadi live@example.test
                                                        (is_cnc/is_special/pajak_in = true)
POST  /admin/branches email "LIVE@EXAMPLE.TEST"         → 409 EMAIL_TAKEN
POST  /admin/branches email "rina@grahitaindonesia.com" → 409 EMAIL_TAKEN
PUT   /admin/branches/{id} {"email":"live-baru@…"}      → 200, email+telepon berubah
POST  /admin/branches/{id}/token  (pertama)             → 200 GRH-SGQX-… berlaku 2026-10-22
POST  /admin/branches/{id}/token  (kedua)               → 200 GRH-234K-… (token lama batal)
psql  registration_tokens → prefix GRH-234K | cocok token baru = t |
      cocok token lama = f | belum_terpakai = t | sisa_hari = 14 | token_aktif = 1
POST  /admin/branches/{cabang berakun}/token            → 409 BRANCH_HAS_ACCOUNT
PUT   /admin/branches/{id} {"is_active":false}          → 200
POST  /admin/branches/{id}/token                        → 409 BRANCH_INACTIVE
POST  /auth/login (bandung@…, host cabang)              → 200, lalu
GET   /admin/branches (host cabang)                     → 403
DELETE /admin/branches/{cabang berp pesanan}            → 409 BRANCH_HAS_ORDERS
DELETE /admin/branches/{id} (tanpa pesanan)             → 204 (branches & tokens = 0)
psql  audit_logs → 6 entri, semuanya actor admin "Budi Santoso" + ip 127.0.0.1/32:
      create / update / token_generate / token_generate / update / delete
      (create: before NULL, after memuat id barunya; delete: before = snapshot cabang)
```

## Perilaku & keputusan yang menyimpang/eksplisit

- Valider transport menolak email dengan spasi tepi (`validate:"email"`),
  sementara service tetap menormalkan huruf besar/kecil sebelum menyimpan
  (perilaku sama dengan endpoint F1-05).
- `PUT` dengan body `{}` = tidak ada perubahan (semua field opsional) → 200.
- `DELETE` juga menghapus akun + sesi cabang bila cabang itu belum punya
  pesanan; riwayat audit tetap tersimpan (`audit_logs` sengaja tanpa FK ke
  cabang).
- Paket `internal/branch/postgres` membaca tabel `orders`, `branch_users`, dan
  `admin_users` lewat query `EXISTS` untuk keputusan di atas. Ini pola yang
  sudah dipakai repository lain di repo ini (mis. `users/postgres` membaca
  `branches` + `registration_tokens`); bila kelak domain pesanan punya service
  sendiri, `BranchHasOrders` dapat dipindahkan ke belakang interface itu.

## Yang belum dikerjakan (batas kartu)

- **UI halaman Cabang** (PRD §4.4): dialog tambah/ubah, tab filter, tombol
  Generate token + salin. Halaman `/cabang` masih kerangka F1-03 read-only.
- **Registrasi akun cabang** (POST `/auth/registration/validate`,
  `/auth/register`) adalah kartu F1-07; `internal/branch` sudah menyediakan
  format + `auth.HashRegistrationToken` untuk dipakai validasinya, dan tabel
  `registration_tokens` sudah diisi F1-06.
- Uji end-to-end T1 (tester) yang menyilangkan registrasi → login → token
  ulang → isolasi cabang.

## Sisa risiko

- Token registrasi tidak dibatasi laju (rate limit) di endpoint validate F1-07
  — 80 bit entropi membuat tebak-tebakan tidak praktis, tetapi pembatasan
  per-IP layak ditambahkan saat F1-07 dikerjakan.
- PostgreSQL 18 asli/CI `postgres:18-alpine` belum dieksekusi di sesi ini
  (PG16 + `scripts/pg-dev-shim.sql`), sama seperti F1-02/F1-04/F1-05.
- Email yang bertabrakan antar tabel (`admin_users` vs `branches`) hanya
  dijaga dari sisi pembuatan/ubah cabang; `POST /admin/users/admin` (F1-05)
  belum memeriksa `branches`. Dampaknya kecil (login memilih akun admin) tetapi
  kartu lanjutan F1-05 dapat menambahkan pemeriksaan sebaliknya.
