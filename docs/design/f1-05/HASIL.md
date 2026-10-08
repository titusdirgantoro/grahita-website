# F1-05 — Pengelolaan user admin dan user cabang

Kartu: **t_11670088 — F1-05 Pengelolaan user admin dan user cabang**

Dokumen acuan: PRD §2, §4.8, §6.1–6.2; SystemDesign §6–7; Database.md §4.1–4.2,
§4.7.

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Permukaan API yang dikerjakan

| Method | Path | Peran | Sesuai kartu |
|---|---|---|---|
| GET | `/api/v1/admin/users/branch` | Manager + Superadmin | ya |
| PUT | `/api/v1/admin/users/branch/{id}` | Manager + Superadmin | ya (mutasi eksplisit) |
| POST | `/api/v1/admin/users/branch/{id}/reset` | Manager + Superadmin | ya (mutasi eksplisit) |
| GET | `/api/v1/admin/users/admin` | Superadmin | ya |
| POST | `/api/v1/admin/users/admin` | Superadmin | ya (mutasi eksplisit) |
| PUT | `/api/v1/admin/users/admin/{id}` | Superadmin | ya (mutasi eksplisit) |

`{id}` pada rute cabang adalah id AKUN (`branch_users.id`), bukan id cabang.
Semua rute terdaftar di `openapi/openapi.yaml` dan diuji dua arah (router ⇄
kontrak) oleh `TestKontrakOpenAPIIkutRouter` + `TestApiKontrakPathUserManagement`.

## Keputusan desain

1. **Satu baris per CABANG di tab User cabang.** 1 cabang = 1 akun (PRD BR-01),
   dan akun dibuat lewat token registrasi, jadi cabang tanpa akun tetap harus
   tampil. Daftar memakai `LEFT JOIN branch_users` + token registrasi aktif:
   kolom `status` = `belum_registrasi` (tidak ada akun), `aktif`, atau
   `nonaktif`.
   *Penyimpangan sadar dari prototype*: prototype (`akunStatus`) hanya melihat
   flag akun, sedangkan di sini **cabang nonaktif juga membuat status
   `nonaktif`** — karena keadaan itu sama-sama membuat akun tidak bisa login
   (aturan `boleh_login` F1-04). Respons tetap memuat `is_active` (flag akun)
   dan `branch_active` (flag cabang) supaya UI bisa membedakan keduanya.
2. **Manager hanya akun cabang; akun admin khusus Superadmin** (PRD §2.1).
   Ditegakkan di router (`requireCap`) *dan* di service (`ErrForbidden`), bukan
   hanya disembunyikan di UI. Manager juga menyembunyikan tab "User admin".
3. **Undangan admin = akun tanpa kata sandi.** `POST /admin/users/admin` membuat
   baris `admin_users` dengan `password_hash` NULL, `activated_at` NULL,
   `invited_at = now()`, `is_active = true`. Akibatnya admin undangan:
   tidak bisa login (aturan `boleh_login` menolak), tidak menerima email
   dokumen (daftar penerima mensyaratkan `activated_at` terisi), dan `forgot
   password` tidak mengirim apa pun untuknya.
4. **Aktivasi memakai tautan sekali pakai + `password_reset_tokens`.**
   Undangan menghasilkan token lewat `auth.Service.IssuePasswordResetFor`
   (tanpa mengirim email) lalu tautannya dimasukkan ke email undangan.
   `POST /auth/password/reset` pada admin undangan = AKTIVASI: mengisi
   `password_hash` **dan** `activated_at` bersamaan (kewajiban turunan F1-04),
   `UPDATE ... SET activated_at = COALESCE(activated_at, now())` sehingga
   aktivasi ulang tidak menimpa jejak yang sudah ada. Admin yang sudah
   `is_active = false` tetap ditolak (`ErrInactiveAccount`).
5. **Masa berlaku tautan undangan = `token_hari` (default 14 hari).**
   PRD §4.9 tidak menyediakan setelan khusus undangan; token aktivasi bersifat
   "registrasi" sehingga memakai kunci `token_hari`, bukan `reset_menit`
   (60 menit terlalu pendek untuk undangan). Reset ulang oleh admin tetap
   memakai `reset_menit` (jalur yang sama dengan lupa kata sandi).
6. **Reset cabang oleh admin memakai jalur email yang sudah ada**
   (`auth.Service.SendPasswordResetFor` → `auth.ResetMailer`): F1-16 cukup
   mengalihkan `auth.ResetMailer` ke penulis `email_outbox` di satu tempat,
   tanpa mengubah handler ini.
7. **Email undangan TIDAK dikirim langsung.** Paket `internal/users`
   mengantre lewat interface `users.EmailQueue`, dan email
   `jenis = undangan_admin` berisi payload template + tautan. Implementasi
   default `users.LogQueue` hanya mencatat antrean ("outbox F1-16 belum
   terpasang"); penulis `email_outbox` sesungguhnya adalah kartu F1-16.
8. **Admin nonaktif atau undangan tidak pernah menerima dokumen**
   (`users.Service.DocumentRecipients`): syaratnya `is_active` DAN
   `password_hash IS NOT NULL` DAN `activated_at IS NOT NULL` DAN flag
   `notif_invoice` / `notif_surat_jalan` (PRD AP-USR-01/02). Tidak ada alamat
   Pusat yang di-hardcode. Dipakai F1-16 saat mengirim invoice/surat jalan.
9. **Superadmin aktif terakhir dilindungi, termasuk saat request bersamaan.**
   Repository menjalankan `UPDATE` di dalam satu transaksi yang lebih dulu
   mengambil kunci penasihat tetap (`pg_advisory_xact_lock`, lihat
   `users/postgres.AdminMutationLockKey`), lalu membaca baris target
   `FOR UPDATE` dan menghitung superadmin aktif lain. Dua permintaan yang
   datang bersamaan karena itu berurutan dan yang kedua melihat hasil commit
   yang pertama. Trigger pengaman di database (0003) tetap menjadi jaring
   terakhir dan dipetakan ke error yang sama (`ErrLastSuperadmin` → 409
   `LAST_SUPERADMIN`).
   **Alasan kunci eksplisit:** trigger saja tidak cukup. Probe mandiri di bawah
   membuktikan dua UPDATE bersamaan pada dua superadmin aktif berbeda sama-sama
   lolos trigger dan meninggalkan NOL superadmin aktif.
10. **Nonaktifkan akun mencabut sesi.** Menonaktifkan akun cabang maupun admin
    memanggil `auth.Service.RevokeAllForUser`, sehingga sesi yang sedang
    berjalan langsung gugur (bukan hanya ditolak saat login berikutnya).
11. **Galat domain terpisah dari validasi bentuk.** `EMAIL_TAKEN` (409),
    `LAST_SUPERADMIN` (409), `NO_ACCOUNT` (409), `ACCOUNT_INACTIVE` (409),
    `FORBIDDEN` (403), `NOT_FOUND` (404) tercantum di enum `ErrorEnvelope`
    kontrak OpenAPI. `POST .../reset` untuk akun/cabang nonaktif dijawab
    `409 ACCOUNT_INACTIVE` dengan pesan yang menyuruh admin mengaktifkan
    akunnya lebih dulu (bukan pesan generik "Akun Anda sedang tidak aktif").

## Perubahan berkas

Backend:

- `api/internal/users/users.go` (baru) — service, tipe, sentinel error, aturan
  status, penerima dokumen, antrean email.
- `api/internal/users/memory.go` (baru) — repository in-memory untuk pengujian.
- `api/internal/users/postgres/postgres.go` (baru) — repository PostgreSQL,
  termasuk transaksi berkunci untuk mutasi admin.
- `api/internal/httpapi/handlers_users.go`, `router.go`, `deps.go`, `errors.go`.
- `api/internal/auth/service.go`, `reset.go`, `memory.go`,
  `auth/postgres/postgres.go` — `Credentials.IsActive`, `ActivateAdmin`,
  `IssuePasswordResetFor`, `SendPasswordResetFor`, `ResetURLBuilder`, dan
  aktivasi admin pada alur reset.
- `api/db/queries/user.sql` + hasil `sqlc generate`
  (`api/internal/db/sqlc/user.sql.go`).
- `api/cmd/server/main.go` — perakitan service.
- `openapi/openapi.yaml` — 6 operasi baru, skema `BranchUser`/`AdminUser`,
  respons `Conflict`, kode error baru.

Frontend (Admin Portal):

- `web/apps/admin/src/pages/pengguna.tsx` (baru) — halaman `/pengguna` dua tab.
- `web/apps/admin/src/routes.tsx`, `web/apps/admin/src/pages/shell-pages.tsx`
  (placeholder dipindahkan ke berkas sendiri).

Uji:

- `api/internal/users/users_test.go` (baru, 9 uji unit domain).
- `api/internal/auth/activation_test.go` (baru, 4 uji aktivasi admin).
- `api/internal/httpapi/router_users_test.go` (baru, 8 uji HTTP in-memory).
- `api/internal/httpapi/integration_users_pg_test.go` (baru, 5 uji integrasi
  PostgreSQL nyata).
- `web/apps/admin/src/pengguna.test.tsx` (baru, 4 uji UI).

## Bukti verifikasi (dijalankan sendiri)

### Gerbang statis

| Perintah | Hasil |
|---|---|
| `gofmt -l .` (api) | kosong |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `sqlc-v1.31.1 generate` | tanpa diff (`git status` hanya memuat berkas yang memang diubah kartu ini) |
| `sh scripts/check-secrets.sh` | "Tidak ada rahasia terdeteksi pada berkas yang dilacak" |
| `npx @redocly/cli@1 lint openapi/openapi.yaml` | valid; 10 warning lama (license, server example, tag, 4xx pada healthz/readyz/meta) — tidak ada warning baru dari path F1-05 |
| `npm run typecheck` (4 workspace) | exit 0 |
| `npm run test` | 69 lulus (api-client 7, ui 44, admin 12, customer 6) |
| `npm run build` | dua SPA terbangun |

### Uji backend

DB uji: PostgreSQL 16.15 di `127.0.0.1:5455` (`TEST_DATABASE_URL`),
`scripts/pg-dev-shim.sql` dipasang lebih dulu.

```
go test -race -p 1 -count=1 ./...   → semua paket ok
  internal/arch ok · internal/auth ok · internal/httpapi ok
  internal/platform/apierr ok · internal/platform/database ok
  internal/users ok · internal/worker ok
```

Uji integrasi F1-05 (verbose):

```
--- PASS: TestPGAkunCabangStatusDanLoginTerakhir (2.20s)
--- PASS: TestPGResetCabangBerhasilSampaiKataSandiBaru (1.69s)
--- PASS: TestPGUndangAdminAktivasiDanPenerimaDokumen (1.44s)
--- PASS: TestPGProteksiSuperadminAktifTerakhir (1.40s)
--- PASS: TestPGAdminNonaktifTidakBisaLoginDanSesiDicabut (1.43s)
```

Yang benar-benar dibuktikan (bukan hanya status kode):

- **Daftar status & login terakhir**: `last_login_at` terisi setelah login
  nyata, `null` untuk cabang yang belum pernah login, `belum_registrasi` untuk
  cabang tanpa akun (dengan `token_prefix`), `nonaktif` untuk cabang yang
  induknya dinonaktifkan.
- **Nonaktifkan akun cabang**: sesi yang sudah ada → 401, login → 403.
- **Reset cabang oleh admin**: mailer menerima 1 email, baris
  `password_reset_tokens` aktif tepat 1, token → 204, login kata sandi baru
  berhasil, kata sandi lama 401.
- **Undangan → aktivasi**: baris `admin_users` sebelum aktivasi
  `password_hash NULL`, `activated_at NULL`, `invited_at` terisi; login
  undangan 401; setelah tautan dipakai, `password_hash` dan `activated_at`
  keduanya terisi, login 200, status daftar berubah `undangan` → `aktif`;
  tautan kedua kali → 400 `RESET_INVALID`; email ganda → 409 `EMAIL_TAKEN`.
- **Penerima dokumen**: sebelum aktivasi tidak ada penerima; sesudah aktivasi
  hanya admin yang diaktivasi DAN ber-flag yang masuk daftar; admin nonaktif
  ber-flag tetap tidak masuk.
- **Proteksi superadmin terakhir berurutan**: 409 `LAST_SUPERADMIN` untuk
  penurunan role dan penonaktifan.
- **Proteksi pada request bersamaan (deterministik)**: kunci penasihat dipegang
  dari koneksi lain, dua PUT bersamaan (turunkan SA-A dan SA-B) dibuktikan
  MASIH MENUNGGU (belum selesai setelah 500 ms); setelah kunci dilepas tepat
  satu 200 dan satu 409, dan database menyisakan tepat 1 superadmin aktif.
- **Kontrol negatif**: dengan kunci penasihat dinonaktifkan sementara, uji yang
  sama GAGAL (`mutasi admin selesai padahal kunci masih dipegang (status
  [409 200])`) — jadi uji ini benar-benar menjaga mekanismenya, bukan lulus
  karena kebetulan.

### Bukti end-to-end pada binary sungguhan (`--mode=api` + curl)

DB `grahita_f105_live` (bersih) → `--mode=migrate` → `--mode=seed` dengan
`SEED_PASSWORD`; server di `127.0.0.1:8098`. Hasil nyata:

```
GET  /api/v1/admin/users/branch           (superadmin) → 200, 4 cabang dengan
     user_id/status/last_login_at (bandung last_login_at null sebelum login)
GET  /api/v1/admin/users/admin            (superadmin) → 200, rina+budi "aktif"
PUT  /api/v1/admin/users/admin/{rina}     {"role":"manager"} → 409 LAST_SUPERADMIN
POST /api/v1/admin/users/admin            undangan dewi → 201 status "undangan";
     di DB: password_hash NULL = t, activated_at NULL = t, invited_at terisi = t;
     log server: 1 email diantre (outbox F1-16 belum terpasang)
POST /api/v1/auth/login                   (dewi, masih undangan) → 401
GET  /api/v1/admin/users/admin            (manager budi) → 403
GET  /api/v1/admin/users/branch           (manager budi) → 200
PUT  /api/v1/admin/users/branch/{usr}     {"is_active":false} (manager) → 200
GET  /api/v1/auth/me                      (sesi cabang itu) → 401  ← sesi dicabut
POST /api/v1/admin/users/branch/{usr}/reset (akun nonaktif) → 409 ACCOUNT_INACTIVE
```

Ini membuktikan perakitan di `cmd/server/main.go` (service, queue, tautan)
benar-benar jalan, bukan hanya `Deps` rakitan pengujian.

### Probe mandiri di luar tes repo (SQL)

Skema `probe105` berisi tiruan `admin_users` + trigger pengaman 0003, dua
superadmin aktif:

```
# tanpa kunci penasihat (mengandalkan trigger saja):
psql A: BEGIN; UPDATE ... id=A; pg_sleep(2); COMMIT;   → sukses
psql B: BEGIN; UPDATE ... id=B; pg_sleep(2); COMMIT;   → sukses
SELECT count(*) ... WHERE role='superadmin' AND is_active;  → 0   ← trigger LOLOS, superadmin habis

# dengan kunci penasihat (cara F1-05):
A: SELECT pg_advisory_xact_lock(4815162342); ... → sukses (superadmin_lain = 1)
B: menunggu kunci; sesudah A commit → ERROR "minimal satu superadmin aktif harus tersisa"
SELECT count(*) ... AND is_active;  → 1
```

Kesimpulan: trigger database saja tidak cukup untuk request bersamaan; kunci
transaksi di repository adalah yang menjaganya, trigger tetap menjadi jaring
terakhir.

### Uji UI (`web/apps/admin/src/pengguna.test.tsx`, 4 lulus)

- Manager: tabel akun cabang tampil dengan status (`Belum registrasi`,
  `Nonaktif`, `Aktif`), kolom telepon, dan **login terakhir terformat WIB**
  (`2026-10-07T03:04Z` → `7 Okt 2026, 10.04`); tab "User admin" dan tombol
  "Undang admin" TIDAK ada; `/admin/users/admin` tidak pernah dipanggil.
- Superadmin: tab "User admin" menampilkan daftar admin + badge
  "Undangan terkirim"; "Undang admin" membuka modal; submit mengirim
  `POST /api/v1/admin/users/admin` dengan header `X-CSRF-Token` dan body
  `{nama,email,role,notif_invoice,notif_surat_jalan}`, lalu daftar dimuat ulang.
- Menonaktifkan akun cabang mengirim `PUT .../branch/usr-a` dengan
  `{"is_active":false}`.
- Galat 409 `LAST_SUPERADMIN` dari server ditampilkan apa adanya di modal
  "Ubah admin".

## Kewajiban turunan untuk kartu lain

- **F1-16**: sediakan implementasi `users.EmailQueue` yang menulis
  `email_outbox` (`jenis = undangan_admin`, payload memuat `tautan`, `nama`,
  `email`, `role`, `berlaku_sampai`) lalu ganti `users.LogQueue` di
  `cmd/server/main.go`; ganti juga implementasi `auth.ResetMailer` supaya
  tautan reset (`reset_password`) dan undangan berbagi outbox yang sama.
  Penerima dokumen: panggil `users.Service.DocumentRecipients` (tidak ada
  alamat Pusat tambahan; admin nonaktif/undangan tidak pernah masuk).
- **F1-06** (master cabang): "Buat token registrasi" untuk cabang berstatus
  `belum_registrasi` tetap milik halaman Cabang
  (`POST /admin/branches/{id}/token`); halaman `/pengguna` hanya menampilkan
  `token_prefix` yang sedang aktif.
- **Frontend rute reset**: portal admin belum punya rute `/lupa` yang membaca
  `?token=`; tautan undangan menunjuk `ADMIN_HOST` + `PASSWORD_RESET_PATH`.
- **Audit log**: mutasi akun di kartu ini belum menulis `audit_logs` (penulis
  audit belum ada di F1-01..F1-04); perlu disatukan saat kartu audit
  dikerjakan.

## Risiko residu

- PostgreSQL 18 asli/CI `postgres:18-alpine` belum dieksekusi di sesi ini
  (PG16 + `scripts/pg-dev-shim.sql`), sama seperti F1-02/F1-03/F1-04.
- Email undangan hanya tercatat di log sampai F1-16 terpasang (default
  `LogQueue`), jadi alur undangan tidak benar-benar sampai ke kotak masuk.
- `UpdateAdmin` menyerialkan SELURUH mutasi admin lewat satu kunci penasihat
  (bukan hanya yang menyentuh role/`is_active`). Volume admin kecil, jadi
  dampaknya dapat diabaikan; bila kelak perlu, kunci bisa dipersempit.
- Daftar akun cabang memakai dua `LEFT JOIN` per baris cabang; jumlah cabang
  kecil (belasan) sehingga belum perlu paginasi.
- `golangci-lint` tidak tersedia di sesi ini (gate lint tidak direproduksi;
  vet/build/gofmt/uji arsitektur lolos).
- Database uji `grahita_f105` dan skema `probe105` ditinggalkan di server uji
  (DROP DATABASE diblokir kebijakan shell); skema uji sementara di dalamnya
  dihapus otomatis oleh uji, dan `probe105` sudah di-`DROP SCHEMA`.
