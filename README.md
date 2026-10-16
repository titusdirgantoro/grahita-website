# Grahita Portal — Sistem Pemesanan Cabang

Modular monolith Go + dua SPA React untuk sistem pemesanan cabang Grahita Indonesia.
Dokumen perencanaan: `docs/PRD.md`, `docs/SystemDesign.md`, `docs/Architecture.md`, `docs/Database.md`.

> **Status (F1-03):** sistem desain shared (`web/packages/ui`) dan shell responsif
> dua portal selesai diport dari prototype — token, komponen (tombol, form,
> tabel, chip/badge, modal, notice, segmented, switch, avatar, state kosong,
> skeleton, toast, accordion), serta `CustomerShell` (sidebar + bottom-nav) dan
> `AdminShell` (sidebar ciut + drawer) dengan menu yang siap diguard per peran.
> Font Plus Jakarta Sans dan ikon Phosphor di-self-host (tanpa CDN produksi).
> Bukti kesesuaian visual dengan prototype ada di `docs/design/f1-03/`.
>
> **Status (F1-01):** fondasi aplikasi berdiri — modular monolith dengan aturan
> lapisan `http → service → repo`, dua SPA yang benar-benar build, API `/api/v1`
> dengan sesi cookie host-only + otorisasi berbasis role, worker River, skema
> awal + migrasi goose naik/turun, kontrak OpenAPI, dan CI. Endpoint bisnis
> (pesanan, verifikasi, dokumen) menyusul pada kartu berikutnya.
>
> Prototype desain asli disimpan di `docs/prototype/customer_v2.html` dan
> `docs/prototype/admin_v2.html` sebagai acuan visual (kartu F1-03).

---

## Domain (keputusan user — tidak boleh diubah tanpa persetujuan)

| Portal | Host | Isi |
|---|---|---|
| Customer Portal | `grahita-branch.tiusclaw.com` | SPA cabang + API `/api/v1` same-origin |
| Admin Portal | `grahita-admin.tiusclaw.com` | SPA admin + API `/api/v1` same-origin |

Rute admin berada di **root host admin** (bukan `/admin` seperti dokumen lama).
Endpoint API tetap `/api/v1/admin`. Karena API selalu same-origin di host
masing-masing, browser tidak pernah melakukan request lintas-origin.

---

## Prasyarat

- Docker Engine 24+ dan Docker Compose v2
- (untuk pengembangan tanpa container) Go 1.27+, Node 22+
- Port host yang dipakai: `8080`, `8081`, `8088`, `8025`, `9000`, `9001`, `5433`

### Toolchain Go pada runner/pengembang

Target Makefile backend **tidak mengandalkan `go` sudah ada di PATH**.
`scripts/go-env.sh` memilih biner Go >= 1.27 secara deterministik
(`GRAHITA_GO` → `$GOROOT/bin` → `$HOME/toolchain/go/bin` → PATH), dan Makefile
memakai jalur itu untuk `test`/`vet`/`test-integrasi`/`build`/`lint` sekaligus
menambahkannya ke PATH resep. Pada runner yang `go`-nya belum ada di PATH:

```bash
make toolchain-cek                 # laporkan jalur + versi toolchain yang dipakai resep
make test                          # gerbang unit backend (tanpa set PATH manual)
eval "$(./scripts/go-env.sh --export)"   # hanya bila memanggil perkakas Go langsung:
cd api && golangci-lint run ./...  # setara: make lint
```

`make lint-diri` menjalankan kendali positif+negatif pemilih toolchain
(kandidat versi salah harus ditolak, dan target Makefile harus **gagal** bila
toolchain tidak teresolusi). Bila runner benar-benar belum punya Go
1.27.x, setel `GRAHITA_GO=/jalur/ke/go` atau pasang di `$HOME/toolchain/go`.

Target Go bersifat **fail-closed**: bila `GO`/`GOLANGCI_LINT` tidak teresolusi
(mis. `GRAHITA_GO` salah, atau tidak ada kandidat), `make test`/`vet`/`build`/
`lint`/`toolchain-cek` berhenti dengan pesan `GALAT toolchain: …` dan exit != 0
— jadi pada jalur yang melewati pemilih toolchain target tidak pernah "hijau
tanpa menjalankan satu tes pun". Batasnya: override eksplisit dari command line
(`make test GO=/jalur/ke/go`, `make lint GOLANGCI_LINT=/jalur/ke/golangci-lint`)
sengaja **tidak** melewati pemilih sehingga versinya tidak divalidasi; bila
menunjuk biner lain yang selalu exit 0 (mis. `/usr/bin/true`), hasilnya memang
exit 0 tanpa menjalankan tes/lint.

## Menjalankan (lokal)

```bash
cp .env.example .env
make up                                  # build + jalankan stack
make seed SEED_PASSWORD=rahasia123       # data contoh + akun uji
make smoke                               # pastikan semua service hidup
```

| Layanan | Alamat |
|---|---|
| Customer Portal | http://localhost:8080 |
| Admin Portal | http://localhost:8081 |
| API (langsung) | http://localhost:8088/healthz |
| Mailpit | http://localhost:8025 |
| MinIO console (`grahita` / `grahita123`) | http://localhost:9001 |
| PostgreSQL | `localhost:5433` |

Data contoh: 7 produk, 4 cabang, matriks harga, 2 rekening, dan (bila
`SEED_PASSWORD` diisi) akun uji `rina@grahitaindonesia.com` (Superadmin),
`budi@grahitaindonesia.com` (Manager), dan satu akun per cabang.

Untuk uji browser lokal tanpa mengetik kredensial ke tool browser, gunakan
fixture sesi yang tetap menempuh login normal dengan password internal proses:

```bash
make build
APP_ENV=local DATABASE_URL=<dsn-loopback-baru> ./scripts/browser-fixture-local.sh --prepare-database
APP_ENV=local DATABASE_URL=<dsn-loopback-yang-sama> ./scripts/browser-fixture-local.sh
```

Panduan URL Superadmin/Manager/cabang, guard production, sanitasi, storage, dan
cleanup ada di `docs/testing/browser-fixture-local.md`.

## Memverifikasi

```bash
curl http://localhost:8088/healthz      # liveness
curl http://localhost:8088/readyz       # readiness (db, migrasi, storage, gotenberg, smtp)
curl http://localhost:8088/api/v1/meta
curl http://localhost:8080/api/v1/meta  # membuktikan proxy Caddy + API same-origin
```

---

## Aturan lapisan backend

```
httpapi → service → repo → database
```

- `internal/httpapi` adalah satu-satunya lapisan transport; ia **tidak boleh**
  mengimpor repository (`*/postgres`) maupun query sqlc.
- `internal/<domain>` (auth, catalog, settings) berisi logika bisnis dan
  **tidak boleh** mengimpor `net/http` atau `internal/httpapi`.
- `internal/platform/*` adalah fondasi dan tidak bergantung pada domain.
- Hanya `internal/*/postgres` dan `cmd/*` yang boleh mengimpor `internal/db/sqlc`.

Aturan ini **ditegakkan oleh pengujian** `internal/arch/arch_test.go` yang
membaca import graph kode sumber. Melanggarnya = CI merah.

## Struktur

```
.
├─ .github/workflows/ci.yml     # lint, test, migrasi bersih, build, kontrak, rahasia
├─ docker-compose.yml           # pengembangan
├─ docker-compose.prod.yml      # overlay produksi (domain + TLS)
├─ Makefile
├─ docs/                        # PRD, SystemDesign, Architecture, Database
├─ docs/prototype/              # customer_v2.html, admin_v2.html (acuan desain)
├─ docs/design/f1-03/           # snapshot visual + perbandingan vs prototype
├─ infra/caddy/                 # Caddyfile.dev & Caddyfile.prod
├─ openapi/openapi.yaml         # kontrak API
├─ scripts/                     # smoke.sh, check-secrets.sh, go-env.sh, pg-dev-shim.sql
├─ web/                         # monorepo SPA (npm workspaces)
│  ├─ packages/api-client/      # klien /api/v1 (amplop data/error, CSRF)
│  ├─ packages/ui/              # sistem desain: token, komponen, shell (F1-03)
│  ├─ apps/customer/            # grahita-branch.tiusclaw.com
│  └─ apps/admin/               # grahita-admin.tiusclaw.com
└─ api/
   ├─ Dockerfile
   ├─ sqlc.yaml
   ├─ cmd/server/               # --mode=api|worker|migrate|migrate-down|seed|healthcheck
   ├─ db/migrations/*.sql       # goose (embed ke binary)
   ├─ db/queries/*.sql          # sqlc
   └─ internal/
      ├─ httpapi                # router, middleware, handler
      ├─ auth catalog settings  # service + kontrak repository
      ├─ document fulfillment   # dokumen pesanan + pekerjaan setelah Done
      ├─ notification           # outbox email: template, retry, worker
      ├─ */postgres             # implementasi repository (sqlc)
      ├─ db/sqlc                # kode hasil generate
      ├─ platform/*             # config, database, httpx, apierr, clock, storage, mailer, pdf, pgtypes, jobs
      ├─ worker                 # River
      ├─ seed                   # data contoh
      └─ arch                   # pengujian batas lapisan
```

## Kontrak API

- Semua endpoint bisnis di bawah `/api/v1`, same-origin di kedua host.
- Sukses: `{ "data": ... }`. Gagal: `{ "error": { "code", "message", "fields"? } }`.
- Sesi: cookie **host-only** `HttpOnly; SameSite=Lax` (`Secure` di luar lokal),
  tanpa atribut `Domain`, nilainya disimpan sebagai hash di PostgreSQL.
- CSRF: double-submit — header `X-CSRF-Token` harus sama dengan cookie `grahita_csrf`
  untuk semua request non-GET (login dikecualikan).
- Otorisasi diperiksa di layer service berdasarkan role akun, bukan hostname.
- Spesifikasi lengkap: `openapi/openapi.yaml` (`make openapi`).

## Perintah berguna

```bash
make vet test                 # backend
make lint                     # golangci-lint (API)
make toolchain-cek            # jalur+versi toolchain Go/lint yang dipakai resep
make test-integrasi           # butuh TEST_DATABASE_URL (PostgreSQL 18 bersih)
make web-typecheck web-test   # frontend
make web-build                # build dua SPA
make sqlc                     # bangkitkan query
make openapi                  # lint kontrak
make secrets                  # pindai pola rahasia
make ci-lokal                 # jalankan seluruh gerbang CI di mesin lokal
```

## Catatan pengujian di PostgreSQL < 18

Target resmi proyek adalah PostgreSQL 18 (`uuidv7()` bawaan). Bila Anda
menjalankan uji integrasi pada PostgreSQL yang lebih lama:

```bash
psql "$TEST_DATABASE_URL" -f scripts/pg-dev-shim.sql   # menambahkan uuidv7()
make test-integrasi
```

CI selalu memakai image `postgres:18-alpine`, jadi shim ini hanya untuk mesin
pengembang dan tidak pernah dipakai di produksi.

## Basis data

Migrasi goose di `api/db/migrations` di-embed ke binary dan dijalankan dengan
`make migrate` (atau `go run ./cmd/server --mode=migrate`):

| Versi | Isi |
|---|---|
| `0001_init.sql` | skema inti: 22 tabel + 12 enum (Database.md §3–§4) |
| `0002_functions.sql` | `next_numbers()`, trigger `updated_at`, trigger total pesanan, seed pengaturan PRD §4.9 |
| `0003_order_integrity.sql` | pengaman integritas: campur sertifikat/reguler, kewajiban packaging & serial saat submit, minimal satu superadmin aktif |
| `0004_numbering.sql` | tanggal bisnis WIB (`wib_today()`), periode `invoice_periode()`/`serial_periode()`, format nomor |

Aturan penting:

- **Penomoran tanpa bolong & tanpa bentrok.** `next_numbers(scope, periode, jumlah)`
  menaikkan baris `numbering_counters` dengan `UPDATE … RETURNING` di dalam
  transaksi submit. Karena memakai tabel (bukan `SEQUENCE`), rollback tidak
  membocorkan nomor dan reset periode (bulan untuk invoice, hari untuk serial)
  berjalan alami.
- **Draft tidak pernah dihalangi.** Invariant (minimal satu baris produk,
  kewajiban baris packaging pada pesanan sertifikat, serial wajib ada, total
  konsisten) hanya diperiksa saat status berpindah `draft → submitted`.
- **Revisi admin tetap sah.** Penguncian pesanan `Submitted`/`Done` untuk cabang
  ditegakkan di layer service (aktor), bukan trigger, sehingga revisi invoice
  admin (AP-REV-01) tidak terhalang.
- **Retensi bukti transfer: tanpa batas** untuk saat ini (keputusan user,
  override Database.md §9) — tidak ada job pembersih dan backup tidak menghapus.
- Query penomoran tersedia lewat sqlc di `api/db/queries/numbering.sql`.

Uji integrasi skema (`go test ./internal/platform/database/...`) mencakup
migrasi naik/turun pada database bersih, keunikan email (citext) & satu akun per
cabang, keunikan/format serial, seed pengaturan, penomoran bersamaan (24 submit
paralel tanpa nomor kembar/bolong) dan penomoran setelah rollback.

## Autentikasi, sesi & otorisasi (F1-04)

Endpoint (semua di bawah `/api/v1`):

| Method | Path | Catatan |
|---|---|---|
| POST | `/auth/login` | satu endpoint untuk kedua portal; identitas dari akun, bukan host |
| POST | `/auth/logout` | idempoten, wajib CSRF |
| GET | `/auth/me` | profil + role + `branch_id` dari sesi |
| PUT | `/auth/me` | ubah NAMA tampilan sendiri (admin); email/role read-only, akun cabang 403 |
| POST | `/auth/password/forgot` | respons selalu sama (non-enumeratif), dibatasi per IP |
| POST | `/auth/password/reset` | token sekali pakai; mencabut **semua** sesi |
| POST | `/auth/password/change` | verifikasi kata sandi lama; mencabut sesi **lain** saja |

- **Cookie sesi host-only**: tanpa atribut `Domain`, `HttpOnly`, `SameSite=Lax`,
  `Secure` di luar lokal. Token hanya disimpan sebagai SHA-256 di tabel
  `sessions`; token atur ulang pun hanya SHA-256 di `password_reset_tokens`.
- **Rate limit login** menghitung percobaan gagal: 5 per 15 menit, terpisah per
  akun dan per IP (PRD CP-AUT-05). Lupa/atur ulang kata sandi dibatasi per IP.
- **Boleh login** ditentukan SQL (`boleh_login`): akun aktif DAN cabangnya aktif,
  atau admin yang `is_active` + sudah punya `password_hash` + `activated_at`.
  Cabang nonaktif kehilangan sesinya pada permintaan berikutnya.
- **CSRF** dikecualikan hanya untuk `login`, `password/forgot`, dan
  `password/reset`; sisanya wajib `X-CSRF-Token` (double-submit cookie
  `grahita_csrf`).
- **Otorisasi di service**: `Actor.RequireBranch`/`OwnsBranch` membaca
  `branch_id` dari sesi, dan matriks kapabilitas (`Auth.Can`) menegakkan
  Manager vs Superadmin (bank/reporting/pengaturan/user admin hanya Superadmin)
  — bukan sekadar menyembunyikan menu.
- Tautan email reset: `${portal}/lupa?token=...`; rutenya dapat diganti lewat
  `PASSWORD_RESET_PATH`. **Kedua portal punya rute `/lupa`**: portal cabang
  (`grahita-branch…`) dan portal admin (`grahita-admin…`) melayani form minta
  tautan maupun form "buat kata sandi baru" saat `?token=` ada — tidak ada
  cookie yang dibagi antar subdomain (F1-10).

Uji: `internal/auth/reset_test.go` (unit, jam tetap),
`internal/httpapi/router_auth_test.go` (API lewat HTTP penuh, termasuk isolasi
cookie antar-subdomain dengan cookie jar),
`internal/httpapi/openapi_contract_test.go` (router ⇄ kontrak OpenAPI), dan
`internal/httpapi/integration_pg_test.go` (PostgreSQL nyata; butuh
`TEST_DATABASE_URL`, memakai skema database tersendiri lalu menghapusnya).
Detail keputusan dan bukti: `docs/design/f1-04/HASIL.md`.

## Sistem desain & shell (F1-03)

`web/packages/ui` adalah satu paket sistem desain yang dipakai kedua SPA:

| Isi | Berkas |
|---|---|
| Token desain (brand, status, ukuran kontrol) | `src/tokens.css` |
| Reset + utilitas global (`.muted`, `.num`, `.sr`, `.hatch`, fokus) | `src/base.css` |
| Komponen | `src/components/*` (Button, Field/Form, Card, Badge/Chip, Table, Modal, Notice, Segmented, Switch, Avatar, EmptyState, Skeleton, Toast, Accordion, AuthLayout, Brand, **Logo**, **ChangePasswordForm**, **PasswordResetPage**, PageHead) |
| Shell + model menu | `src/shell/*` (`CustomerShell`, `AdminShell`, `filterNav`/`filterGroups`/`bolehLihat`) |

- **Perbedaan antar portal** (mis. `--h-control` 40px vs 36px, `--primary`
  brand-900 vs ink, radius kartu 16px vs 20px) dipilih lewat atribut
  `data-portal` pada `<html>` (di-set `main.tsx`), bukan dengan menyalin
  komponen. Jadi satu set komponen tetap meniru kedua prototype.
- **Self-host aset**: font Plus Jakarta Sans (`@fontsource-variable/…`) dan font
  ikon Phosphor (`@phosphor-icons/web`) diimpor dari `node_modules` dan ikut
  ter-bundel; `index.html` tidak memuat URL eksternal (diuji di CI). **Logo
  GRAHITA INDONESIA juga self-host** (`src/assets/logo-grahita-*.png`, turunan
  langsung dari aset resmi) dan dipakai lewat komponen `Logo`.
- **Guard menu per peran**: item `superadminOnly` (Rekening bank, Reporting,
  Pengaturan) disaring `AdminShell`, halamannya menolak lewat `PenjagaSuperadmin`,
  dan API tetap otoritas akhir. Manager melihat metrik jumlah, Superadmin melihat
  rupiah.
- **Router**: TanStack Router dengan `beforeLoad`-style guard di layout shell
  (redirect ke `/masuk` bila belum ada sesi). Shell sendiri lepas dari router —
  aplikasi memasok komponen `Link`, sehingga shell mudah diuji.
- **Galeri komponen** `/gaya` di kedua app menampilkan shell + seluruh komponen
  memakai akun contoh tanpa memanggil API — dipakai perbandingan visual
  (`docs/design/f1-03/HASIL.md`).

Perintah terkait: `npm run typecheck`, `npm run test`, `npm run build` di `web/`
(103 tes: api-client 7, ui 56, admin 29, customer 11; termasuk suite profil &
kata sandi F1-10).

## Profil & ganti kata sandi (F1-10)

- **Profil cabang** `/profil` (portal cabang): data dari `GET /api/v1/me/branch`
  — nama, kepala cabang, email, telepon tampil **read-only** (dikelola admin
  Pusat), ditambah tombol Keluar dan form ganti kata sandi. Tidak ada rute tulis
  untuk profil cabang (`PUT`/`PATCH`/`POST` → 405) dan `branch_id` tidak pernah
  dibaca dari klien, jadi profil cabang lain tidak bisa dibaca.
- **Profil admin** `/profil` (portal admin): dari `GET /auth/me`; **nama** boleh
  diubah sendiri lewat `PUT /auth/me`, sedangkan **email** dan **role**
  read-only (hanya Superadmin lewat user management yang mengubahnya).
- **Ganti kata sandi** (kedua portal, komponen `ChangePasswordForm`):
  `POST /auth/password/change` memverifikasi kata sandi lama; sesi yang dipakai
  tetap hidup dan **sesi lain dicabut** (PRD CP-AUT-06/07).
- **Lupa/atur ulang** (kedua portal, rute `/lupa`): tanpa query = minta tautan
  (respons seragam); dengan `?token=` = buat kata sandi baru lewat
  `POST /auth/password/reset` (token sekali pakai, seluruh sesi lama dicabut).
- Bukti & keputusan: `docs/design/f1-10/HASIL.md`.

## Email transaksional: outbox, retry & kirim ulang (F1-16)

Email tidak pernah dikirim dari handler HTTP. Service menulis baris ke
`email_outbox` (di dalam transaksi bisnisnya lewat `EnqueueTx`), worker
(`--mode=worker`, job periodik `email_outbox_pindai`) memindai, mengirim, lalu
memperbarui status (`queued` → `sending` → `sent`/`failed`).

- **Jenis**: `reset_password`, `undangan_admin`, `invoice_cabang`,
  `invoice_admin`, `surat_jalan_admin`, `reminder_cnc` (Database.md §3).
- **Retry**: 1m, 5m, 30m, 2j, 6j — maksimum 5 percobaan **ulang** (jadi 6 kali
  kirim), setelah itu `failed` dengan `last_error` (PRD EM-01). Baris `sent`
  tidak pernah dikirim ulang worker.
- **Klaim aman paralel**: `FOR UPDATE SKIP LOCKED` + masa sewa 15 menit,
  sehingga baris tidak terkirim dua kali dan baris `sending` yang ditinggal
  proses mati pulih sendiri.
- **Semantik pengiriman at-least-once**: bila proses mati (atau penandaan
  `sent` gagal) SETELAH SMTP menerima pesan, baris diklaim ulang setelah sewa
  habis sehingga email yang sama bisa terkirim **dua kali**. Ini inheren pada
  pola outbox dan disengaja; kebutuhan tepat-sekali harus lewat idempotency key
  di sisi penerima. Yang dijamin: baris `sent` tidak pernah dikirim ulang.
- **Pemindaian berhenti di tengah batch**: bila context job dibatalkan (mis.
  `JobTimeout` River 2 menit), baris yang **belum** sempat dicoba dilepas
  kembali ke `queued` dengan `attempts` seperti sebelum klaim, sehingga jatah
  retry tidak habis oleh email yang tidak pernah dikirim.
- **Penerima**: email Pusat hanya ke admin **aktif & sudah diaktivasi** yang
  ber-flag `notif_invoice`/`notif_surat_jalan`; admin undangan/nonaktif tidak
  pernah menerima. Email cabang memakai `branches.email`.
- **Lampiran**: email dokumen menunggu baris `documents` pada versi pesanan
  yang diminta (`409 DOCUMENT_NOT_READY` bila belum) dan melampirkan berkas
  dari object storage (invoice PDF; surat jalan PDF + XLSX).
- **Endpoint admin**:

| Method | Path | Catatan |
|---|---|---|
| GET | `/admin/orders/{id}/emails` | riwayat + status + `last_error` (EM-02); id bukan UUID → `404` |
| POST | `/admin/orders/{id}/emails/{kind}/resend` | kirim ulang dokumen, ber-audit; penjaga antrean **atomik** (klik ganda bersamaan pun maksimum satu email + satu audit, sisanya `409`) |
| POST | `/admin/users/admin/{id}/resend` | pemulihan undangan admin (Superadmin) |

- **Konfigurasi** (tanpa rahasia di repo): `SMTP_HOST/PORT/USER/PASS`,
  `MAIL_FROM` (alamat pengirim; untuk integrasi diisi alamat akun pengelola
  lewat environment, tidak di-hardcode di repo), `SMTP_TIMEOUT` (batas waktu
  total satu kirim, default 30s), `OUTBOX_INTERVAL`, `OUTBOX_MAX_ATTEMPTS`,
  `OUTBOX_BATCH`. Lokal memakai Mailpit (`http://localhost:8025`).
- **Uji**: `internal/notification` (unit + integrasi PostgreSQL: rollback tidak
  mengirim, klaim paralel, retry tersimpan, kirim ulang bersamaan tidak
  menggandakan email, at-least-once setelah SMTP menerima, sisa klaim dilepas
  saat pemindaian dibatalkan, id bukan UUID → 404), `internal/platform/mailer`
  (MIME + lampiran, deadline peer diam), dan `internal/httpapi` (integrasi PG:
  tautan reset/undangan dari outbox, penerima email dokumen, kirim ulang +
  audit, email gagal di riwayat, id bukan UUID tidak 500).

## Dokumen pesanan: Invoice PDF & Surat Jalan XLSX/PDF (F1-17)

Saat pesanan selesai (Done), sistem membuat **tiga dokumen** dan menyimpannya di
object storage dengan kunci `documents/{order_id}/{jenis}-{versi}.{ext}` serta
satu baris `documents` per `(order_id, jenis, versi)`:

| Jenis | Cara | Isi |
|---|---|---|
| `invoice_pdf` | template HTML A4 → Gotenberg | identitas PT, nomor + versi revisi, tanggal, data cabang, order line + serial, biaya packaging, total, rekening AKTIF, penanda `LUNAS` / `CASH & CARRY — jatuh tempo …` |
| `surat_jalan_xlsx` | `excelize` | header tetap `No \| Nama Barang \| Satuan \| Qty \| Serial Number`, **satu baris per order line**, A4 tegak, header diulang saat cetak |
| `surat_jalan_pdf` | template HTML (data sama) → Gotenberg | kolom & baris sama dengan XLSX-nya |

- **Hanya untuk pesanan Done** (PRD §6.1); status lain dijawab
  `409 ORDER_NOT_DONE`.
- **Idempoten**: kunci memuat versi dan `documents` unik per
  `(order_id, jenis, versi)`, sehingga render ulang tidak menggandakan dokumen
  dan **tidak menimpa berkas versi yang sudah ada**. Revisi invoice (versi +1,
  F2-25) menghasilkan berkas baru; berkas lama tetap utuh (PRD §9 no.8).
- **Satu permintaan = satu unit kerja**: seluruh isi dirender lebih dulu, lalu
  ditulis ke storage, lalu seluruh barisnya disimpan dalam satu transaksi —
  gagal di tengah jalan tidak meninggalkan berkas yatim/dokumen separuh jadi.
- **Akses selalu lewat presigned URL berumur pendek** (300 detik); kunci storage
  tidak pernah dikirim ke klien. Akun cabang hanya boleh melihat/mengunduh
  **invoice** pesanan cabang sesinya; surat jalan adalah dokumen Pusat (dikirim
  ke admin ber-flag `surat_jalan`), jadi dijawab `404` untuk cabang.
- **Endpoint**:

| Method | Path | Peran | Catatan |
|---|---|---|---|
| GET | `/orders/{id}/documents` | cabang & staf Pusat | cabang hanya melihat invoice-nya |
| GET | `/documents/{id}/download` | cabang & staf Pusat | tautan unduh berumur pendek |
| GET | `/admin/orders/{id}/documents` | Manager & Superadmin | seluruh dokumen semua versi |
| POST | `/admin/orders/{id}/documents` | Manager & Superadmin | buat/segarkan dokumen versi berjalan (idempoten) |

- **Konfigurasi**: `GOTENBERG_URL` (tanpa itu render PDF dijawab
  `503 DOCUMENT_RENDER_UNAVAILABLE`); storage memakai `S3_*` yang sama dengan
  bukti transfer.
- **Paket**: `internal/document` (service + generator + repository in-memory),
  `internal/document/postgres`, query `db/queries/document.sql`.
- **Uji**: `internal/document` (unit: idempotensi, versi, otorisasi, isi HTML &
  XLSX, kegagalan tanpa sisa), `internal/httpapi` (integrasi PG + uji PDF nyata
  lewat Chromium lokal sebagai tiruan protokol Gotenberg — dilewati bila
  `google-chrome` tidak ada), dan `web/apps/admin` (tombol Unduh/Buat dokumen).
- **Catatan bisnis terbuka**: PRD §8 no.7 (format Surat Jalan resmi) dan no.9
  (tanda tangan/stempel digital) belum diputuskan → dokumen memakai template
  awal tanpa tanda tangan/stempel.

## Verifikasi pembayaran manual & transisi Done (F1-18)

Satu jalur transisi untuk semua cara verifikasi: `payment.VerificationService.MarkVerified`
(dipakai endpoint admin sekarang sebagai `metode = manual`; kelak OCR/AI dan
Virtual Account memakai method yang sama). Efek sampingnya selalu identik.

- **Endpoint**: `POST /api/v1/admin/orders/{id}/verify` — Manager & Superadmin
  (kapabilitas `admin.verify`). Body:
  `{"hasil":"cocok"|"perlu_review","total_terbaca":<rupiah>,"alasan":<...>,"catatan":<...>}`.
  `metode` **tidak** diterima dari klien: endpoint ini adalah verifikasi manual.
- **`cocok` (setujui)** → `status = done`, `done_at` terisi,
  `verif_status = cocok`.
- **`perlu_review`** → pesanan **tetap `submitted`** (PRD §7.1: perlu review
  adalah sub-status verifikasi) dengan `alasan_review` wajib dari
  `kurang_bayar`, `lebih_bayar`, `tidak_terbaca`, `rekening_beda`, `lainnya`.
  Admin boleh menimpanya nanti dengan persetujuan.
- **Satu transaksi**: `orders` (status/`verif_status`/`done_at`),
  `verification_logs` (`total_pesanan` dari baris terkunci, `total_terbaca`
  dari admin, `selisih` kolom generated), `order_events`, `audit_logs`, dan
  **penjadwalan job River** `render_dokumen_pesanan` (SystemDesign.md §4.2/§5.1)
  — tidak ada pesanan Done tanpa pekerjaan dokumennya.
- **Tepat sekali**: baris pesanan dikunci `FOR UPDATE`; verifikasi kedua pada
  pesanan yang sudah `done` dijawab `409 ORDER_ALREADY_DONE` tanpa efek samping
  kedua. Pesanan `draft` dijawab `409 ORDER_NOT_SUBMITTED`.
- **Worker**: job `render_dokumen_pesanan` merender Invoice/Surat Jalan
  (`document.Ensure`) **lalu** mengantre email dokumen
  (`notification.EnqueueOrderEmailSekali`, idempoten per (pesanan, jenis)).
  Bila mesin PDF/storage belum siap, job gagal dan diulang — email tidak pernah
  keluar tanpa lampiran. Penerima yang belum berflag dilewati (bukan gagal).
- **C&C**: submit tanpa bukti tetap sah (BR-08) dan pesanan yang lewat jatuh
  tempo **tidak** auto-Done — ia baru selesai setelah diverifikasi.
- **Paket**: `internal/payment` (`verify.go` + `payment/postgres/verify.go`),
  `internal/fulfillment`, `internal/platform/jobs`, `internal/worker`.
- **Konfigurasi**: worker kini memakai object storage yang sama
  (`S3_*`) dan `GOTENBERG_URL`; tanpa itu pekerjaan dokumen gagal `503`/diulang.

## Produksi

```bash
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

Sebelum itu: arahkan DNS `grahita-branch.tiusclaw.com` dan
`grahita-admin.tiusclaw.com` ke server, isi rahasia lewat environment
(`POSTGRES_PASSWORD`, `SMTP_USER`, `SMTP_PASS`), dan siapkan backup harian
sesuai `Architecture.md` §7. Caddy mengurus sertifikat TLS otomatis.

## Operasional: observability, rilis & staging (F1-22)

Rincian tiap bagian ada di dokumennya masing-masing; ringkasannya:

| Kebutuhan | Berkas |
|---|---|
| Metrik, alarm, dasbor, log, runbook | `infra/observability/README.md` |
| Overlay Prometheus/Alertmanager/Loki/Promtail/Grafana | `docker-compose.observability.yml` |
| Aturan alarm (12 aturan) | `infra/observability/prometheus/rules/grahita-alerts.yml` |
| Deploy SSH + rahasia yang dibutuhkan | `infra/deploy/README.md`, `infra/deploy/deploy.sh` |
| CI (lint/test/migrasi/build/scan rahasia+image) | `.github/workflows/ci.yml` |
| Rilis: staging otomatis → produksi disetujui manual | `.github/workflows/release.yml` |
| Uji asap alur kritis dua host | `scripts/smoke-staging.sh` |
| Gerbang migrasi kompatibel mundur | `scripts/check-migration-kompatibel.sh` |

Endpoint metrik (`api:8080/metrics` dan `worker:9091/metrics`) **tidak
dipublikasikan**: Caddy hanya mem-proxy `/api/*` dan overlay produksi tidak
memetakan port API ke host. Caddy juga memblokir `/metrics` di tepi (404)
sebagai pertahanan berlapis.

```bash
make observability-up        # Prometheus + Alertmanager + Loki + Grafana
make observability-prom-cek  # promtool check config/rules
make migrasi-cek             # gerbang migrasi kompatibel mundur
make smoke-staging           # uji asap dua host (butuh env kredensial)
```

Verifikasi replika staging tanpa DNS/VPS nyata: jalankan `caddy validate` pada
`infra/caddy/Caddyfile.staging`, adapt konfigurasi itu dengan
`caddy adapt`, lalu ganti hanya nilai bergantung-lingkungan (alamat listen,
upstream, akar SPA, TLS internal) dan jalankan `scripts/smoke-staging.sh`
dengan `SMOKE_CURL_OPTS="--resolve ... --cacert ..."`. Cara ini yang dipakai
untuk membuktikan kedua host menyajikan UI/API pada mesin tanpa DNS; lihat
`docs/design/f1-22/HASIL.md`.
