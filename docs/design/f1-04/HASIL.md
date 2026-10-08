# F1-04 — Sesi, login, reset password dan otorisasi

Kartu: **t_1ceac46c — F1-04 Sesi, login, reset password dan otorisasi**

Dokumen acuan: PRD §2, §3.2, §4.10; Architecture.md §6; SystemDesign.md §2.1,
§4.1, §6; Database.md §4.1–4.2.

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Permukaan API yang dikerjakan

| Method | Path | Sesuai kartu |
|---|---|---|
| POST | `/api/v1/auth/login` | ya |
| POST | `/api/v1/auth/logout` | ya |
| GET | `/api/v1/auth/me` | ya |
| POST | `/api/v1/auth/password/forgot` | ya |
| POST | `/api/v1/auth/password/reset` | ya |
| POST | `/api/v1/auth/password/change` | ya |

Endpoint registrasi token (`/auth/registration/validate`, `/auth/register`)
adalah lingkup F1-07 dan **tidak** disentuh di sini.

## Keputusan desain

1. **Satu endpoint login untuk dua portal.** Identitas ditentukan dari akun
   (admin dicari lebih dulu, lalu cabang), bukan dari hostname. Kedua host
   melayani `/api/v1` yang sama (keputusan domain user: `grahita-branch.tiusclaw.com`
   dan `grahita-admin.tiusclaw.com`).
2. **Cookie sesi host-only.** Tanpa atribut `Domain`, `HttpOnly`, `SameSite=Lax`,
   `Path=/`, dan `Secure` di luar lokal. Ini yang mencegah sesi bocor antar
   subdomain; otorisasi tetap ditentukan role akun di server.
3. **Token sesi tidak pernah disimpan apa adanya.** Database hanya memuat
   SHA-256 (`sessions.token_hash`, `password_reset_tokens.token_hash`); nilai
   aslinya hanya ada di cookie/email. Login selalu membuat baris sesi baru
   (rotasi ID), idle 8 jam, absolute 30 hari (`Architecture.md` §6).
4. **Rate limit login menghitung percobaan GAGAL saja** (PRD CP-AUT-05):
   5 gagal / 15 menit, dihitung **terpisah per akun dan per IP**. Pemeriksaan
   sebelum percobaan tidak menambah hitungan (`httpx.RateLimiter.Exceeded`
   hanya mengintip); jatah akun dibersihkan saat login berhasil, jatah IP
   sengaja dibiarkan kedaluwarsa sendiri supaya kegagalan lintas akun dari satu
   IP tetap terhitung.
5. **Lupa password non-enumeratif** (PRD CP-AUT-03): respons selalu `200`
   dengan pesan identik untuk email terdaftar maupun tidak, akun nonaktif
   sekalipun. Kegagalan pengiriman email hanya masuk log — respons tetap sukses.
6. **Tautan reset sekali pakai, `reset_menit` menit** (default 60). Meminta
   tautan baru membatalkan tautan lama yang belum dipakai
   (`DELETE ... WHERE used_at IS NULL` lalu `INSERT`), dan pemakaian token
   memakai `UPDATE ... RETURNING` yang atomik sehingga dua permintaan
   bersamaan hanya menghasilkan satu pemakaian.
7. **Kata sandi divalidasi sebelum token dibakar.** Percobaan dengan kata sandi
   lemah ditolak `400` tanpa menandai token terpakai.
8. **Reset mencabut SELURUH sesi; change mencabut sesi LAIN saja.** Ganti kata
   sandi dari halaman Profil memverifikasi kata sandi lama dan menolak kata
   sandi baru yang identik dengan yang lama, lalu `RevokeOthers` menyisakan
   sesi yang sedang dipakai (PRD CP-AUT-06, CP-AUT-07).
9. **Aturan "boleh login" adalah SQL, bukan tebakan handler.** Query
   `GetAdminUserByEmail/ByID` dan `GetBranchUserByEmail/ByID` mengembalikan
   `boleh_login = COALESCE(<akun aktif> AND ..., false)`:
   - akun cabang: `branch_users.is_active AND branches.is_active`;
   - admin: `is_active AND password_hash IS NOT NULL AND activated_at IS NOT NULL`.
   Akun yang tidak boleh login saat kredensialnya benar dijawab `403 FORBIDDEN`
   (termasuk cabang yang dinonaktifkan setelah sesi terbit — sesinya langsung
   dicabut pada permintaan berikutnya).
10. **Admin undangan (belum punya kata sandi) dijawab `401` generik**, sama
    dengan akun tidak dikenal, supaya daftar email yang masih menganggur tidak
    bisa dienumerasi. Admin yang sudah punya kata sandi tetapi belum/tidak
    diaktivasi tetap `403` karena kredensialnya benar-benar terverifikasi.
11. **CSRF dikecualikan hanya untuk halaman publik**: `login`, `password/forgot`,
    `password/reset`. `password/change`, `logout`, dan semua mutasi lain wajib
    `X-CSRF-Token` (double-submit terhadap cookie `grahita_csrf`).
12. **Validasi ownership `branch_id` ada di layer service**, lewat
    `Actor.RequireBranch`/`Actor.OwnsBranch` yang hanya membaca `branch_id` dari
    sesi — bukan dari parameter klien. `catalog.Service.Branch` memakainya.
13. **Tautan email reset**: `${portal}/lupa?token=...` dengan rute dapat diubah
    lewat `PASSWORD_RESET_PATH` (default `/lupa`). Portal admin diarahkan ke
    host admin, portal cabang ke host cabang.

## Berkas yang diubah / ditambah

Backend (`api/`):

- `internal/auth/reset.go` (baru): model `ResetToken`, `ResetStore`,
  `ResetMailer`, `EmailResetMailer` (menyusun tautan portal + masa berlaku).
- `internal/auth/service.go`: `ForgotPassword`, `ResetPassword`,
  `ChangePassword`, `SetResetMailer`; `Credentials.Active` = boleh login.
- `internal/auth/actor.go`: `OwnsBranch`, `RequireBranch`, `ErrBranchForbidden`.
- `internal/auth/session.go`: `SessionStore.RevokeOthers`.
- `internal/auth/password.go`: semua kegagalan kekuatan kata sandi membungkus
  `ErrPasswordTooWeak` supaya bisa dipetakan ke `400` dengan field yang tepat.
- `internal/auth/memory.go`, `internal/auth/postgres/postgres.go`: implementasi
  `ResetStore`, `UpdatePassword`, `RevokeOthers`.
- `internal/httpapi/handlers_auth.go`: handler forgot/reset/change + rate limit
  login gagal per akun dan per IP.
- `internal/httpapi/router.go`: tiga rute baru + daftar pengecualian CSRF.
- `internal/httpapi/deps.go`, `errors.go`, `internal/catalog/catalog.go`.
- `internal/platform/httpx/ratelimit.go`: `RateLimiter.Exceeded` (intip tanpa
  menambah hitungan).
- `internal/platform/config/config.go`: `PASSWORD_RESET_PATH`.
- `internal/seed/seed.go`: akun admin contoh dibuat dengan `activated_at` terisi
  (kalau tidak, seed akan ditolak oleh aturan admin belum aktif).
- `db/queries/{user,session}.sql` + `db/queries/reset.sql` (baru) dan hasil
  `sqlc generate` di `internal/db/sqlc/`.
- `cmd/server/main.go`: wiring notifier email + dua pembatas laju (5/15 menit).

Kontrak & dokumen:

- `openapi/openapi.yaml`: 3 path auth baru, kode error `RESET_INVALID`, catatan
  rate limit pada login.
- `README.md`, `.env.example`, `Makefile`, `.github/workflows/ci.yml`.

Uji:

- `internal/auth/reset_test.go` (baru, 10 uji) — unit domain dengan jam tetap.
- `internal/httpapi/router_auth_test.go` (baru, 9 uji API) — lewat HTTP penuh
  dengan penyimpanan in-memory, termasuk isolasi cookie antar subdomain.
- `internal/httpapi/openapi_contract_test.go` (baru, 2 uji) — router ⇄ kontrak
  OpenAPI dua arah, dijalankan tanpa database.
- `internal/httpapi/integration_pg_test.go` (baru, 7 uji) — PostgreSQL nyata:
  migrasi sungguhan lalu alur auth dengan repository asli.

## Bukti verifikasi

Dijalankan di `/home/hermes/projects/grahita-website` (Go 1.27.1, PG16 +
`scripts/pg-dev-shim.sql`; CI memakai `postgres:18-alpine`).

| Perintah | Hasil |
|---|---|
| `gofmt -l .` (api) | kosong |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `golangci-lint v2.14.0 run ./...` | `0 issues.` |
| `sqlc-v1.31.1 generate` (dua kali) | md5 `internal/db/sqlc/*.go` identik → tidak ada drift |
| `go test -race -count=1 ./...` + `TEST_DATABASE_URL` | 6 paket `ok` (dua kali berturut-turut, tanpa flake) |
| `sh scripts/check-secrets.sh` | `Tidak ada rahasia terdeteksi` |

Uji yang dijalankan nyata (bukan skip):

- 68 uji lulus pada `./...` dengan `TEST_DATABASE_URL` (dan `DATABASE_URL`)
  diisi, tanpa satu pun `--- SKIP`. Tanpa kedua variabel itu, 7 uji integrasi
  PostgreSQL di `internal/httpapi` dilewati (dan uji worker River butuh
  `DATABASE_URL`).
- 7 uji integrasi PostgreSQL: `TestPGSesiLoginLogoutDanRotasi`,
  `TestPGAturUlangPasswordTersimpanDanSekaliPakai`,
  `TestPGGantiPasswordCabutSesiLainDiDatabase`,
  `TestPGOtorisasiLintasCabangDanRole`, `TestPGAkunNonaktifDanAdminUndanganDitolak`,
  `TestPGKredensialSalahTidakMembocorkanAkun`,
  `TestPGLupaPasswordAkunTidakAdaTetapSukses`.
- `TestPGAturUlangPasswordTersimpanDanSekaliPakai` memeriksa **isi database**:
  `token_hash = sha256(token)` (dan tidak memuat token mentah), `used_at` terisi
  setelah dipakai, `password_hash` berubah menjadi Argon2id kata sandi baru,
  serta `sessions` tersisa 0 untuk pengguna itu.
- `TestApiSesiTidakBocorAntarSubdomain` memakai `net/http/cookiejar` + dialer
  yang mengarahkan `grahita-branch.tiusclaw.com` dan
  `grahita-admin.tiusclaw.com` ke server uji: cookie yang dipasang host cabang
  tidak pernah terkirim ke host admin (`/auth/me` 401), dan tidak ada header
  `Set-Cookie` yang memuat atribut `Domain`.

### Uji langsung pada binary API (`--mode=api`)

`grahita_live` (PG16 + shim) → `--mode=migrate` → `--mode=seed` (SEED_PASSWORD),
lalu server dijalankan di `:8099` dan dipanggil dengan `curl`:

| Permintaan | Hasil |
|---|---|
| `GET /healthz`, `GET /readyz` | 200, `ready`, migrasi versi 4 |
| `POST /auth/login` cabang | 200; `Set-Cookie: grahita_session=...; Path=/; Expires=...; Max-Age=...; HttpOnly; SameSite=Lax` (tanpa `Domain`) |
| `GET /auth/me` | 200, `role=branch`, `branch_id` milik sesi |
| `GET /catalog` sebagai cabang | 200, hanya harga 6 produk cabang itu |
| `GET /admin/settings` sebagai cabang | 403 `FORBIDDEN` |
| `POST /password/forgot` email tak dikenal vs terdaftar | keduanya 200 dengan **body identik** |
| `POST /password/reset` token palsu | 400 `RESET_INVALID` |
| 5× `POST /auth/login` kata sandi salah, lalu login benar | 401,401,401,401,401 → **429** `RATE_LIMITED` (walau kata sandi benar) |
| `POST /auth/login` superadmin | 200; `GET /admin/settings` 200 |
| `POST /password/change` tanpa/dengan CSRF palsu | 403 `CSRF_INVALID` |
| `POST /password/change` kata sandi lama salah (CSRF sah) | 400 `VALIDATION_FAILED` + `fields.password_lama` |
| `POST /password/change` sah | 204; sesi pemanggil tetap 200, **sesi lain 401** |
| `POST /auth/login` kata sandi lama / baru | 401 / 200 |
| `POST /auth/logout` tanpa/dengan CSRF | 403 / 204; `GET /auth/me` setelahnya 401 |

## Isolasi database uji

Uji integrasi di `internal/httpapi` menempatkan seluruh skema pada **skema
database tersendiri** (`f104_<nano>`, `search_path` diset lewat DSN) dan
menghapusnya di akhir. Alasannya: uji migrasi F1-02 pada database yang sama
menurunkan/naikkan skema `public`, dan keduanya berjalan bersamaan antar paket.
Tabel versi goose dibuat di skema uji berisi baris awal `(0,true)` supaya goose
tidak salah membaca versi milik `public`. Dengan ini `go test ./...` hijau
berulang tanpa `-p 1` (CI tetap memakai `-p 1` sebagai pengaman tambahan).

## Kewajiban turunan (bukan lingkup kartu ini)

- **F1-05 (user admin)**: saat mengaktivasi admin, wajib mengisi `password_hash`
  **dan** `activated_at`, karena aturan `boleh_login` kartu ini menuntut
  keduanya; membiarkannya NULL membuat akun tidak bisa masuk (dan dijawab 401
  generik). Halaman "reset oleh admin" memakai `password_reset_tokens` yang
  sama.
- **F1-16 (email outbox)**: `ResetMailer` saat ini mengirim langsung lewat
  `mailer.Mailer`. Ketika outbox/worker tersedia, cukup ganti implementasi
  `auth.ResetMailer` menjadi penulis `email_outbox` dengan `email_kind =
  'reset_password'`; service dan handler tidak perlu berubah.
- **Frontend**: halaman atur ulang perlu membaca `?token=` pada rute `/lupa`.
  Portal cabang sudah punya rute `/lupa`; portal admin belum, sehingga rute
  setara perlu ditambahkan (atau `PASSWORD_RESET_PATH` diarahkan ke rute yang
  dipilih).
