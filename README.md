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
├─ scripts/                     # smoke.sh, check-secrets.sh, pg-dev-shim.sql
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
      ├─ */postgres             # implementasi repository (sqlc)
      ├─ db/sqlc                # kode hasil generate
      ├─ platform/*             # config, database, httpx, apierr, clock, storage, mailer, pdf, pgtypes
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
  `PASSWORD_RESET_PATH`.

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
| Komponen | `src/components/*` (Button, Field/Form, Card, Badge/Chip, Table, Modal, Notice, Segmented, Switch, Avatar, EmptyState, Skeleton, Toast, Accordion, AuthLayout, Brand, PageHead) |
| Shell + model menu | `src/shell/*` (`CustomerShell`, `AdminShell`, `filterNav`/`filterGroups`/`bolehLihat`) |

- **Perbedaan antar portal** (mis. `--h-control` 40px vs 36px, `--primary`
  brand-900 vs ink, radius kartu 16px vs 20px) dipilih lewat atribut
  `data-portal` pada `<html>` (di-set `main.tsx`), bukan dengan menyalin
  komponen. Jadi satu set komponen tetap meniru kedua prototype.
- **Self-host aset**: font Plus Jakarta Sans (`@fontsource-variable/…`) dan font
  ikon Phosphor (`@phosphor-icons/web`) diimpor dari `node_modules` dan ikut
  ter-bundel; `index.html` tidak memuat URL eksternal (diuji di CI).
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
(65 tes: api-client 7, ui 44, admin 8, customer 6).

## Produksi

```bash
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

Sebelum itu: arahkan DNS `grahita-branch.tiusclaw.com` dan
`grahita-admin.tiusclaw.com` ke server, isi rahasia lewat environment
(`POSTGRES_PASSWORD`, `SMTP_USER`, `SMTP_PASS`), dan siapkan backup harian
sesuai `Architecture.md` §7. Caddy mengurus sertifikat TLS otomatis.
