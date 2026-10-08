# Grahita Portal — Sistem Pemesanan Cabang

Modular monolith Go + dua SPA React untuk sistem pemesanan cabang Grahita Indonesia.
Dokumen perencanaan: `docs/PRD.md`, `docs/SystemDesign.md`, `docs/Architecture.md`, `docs/Database.md`.

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
├─ infra/caddy/                 # Caddyfile.dev & Caddyfile.prod
├─ openapi/openapi.yaml         # kontrak API
├─ scripts/                     # smoke.sh, check-secrets.sh, pg-dev-shim.sql
├─ web/                         # monorepo SPA (npm workspaces)
│  ├─ packages/api-client/      # klien /api/v1 (amplop data/error, CSRF)
│  ├─ packages/ui/              # token desain + gaya dasar (diisi penuh di F1-03)
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

## Produksi

```bash
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

Sebelum itu: arahkan DNS `grahita-branch.tiusclaw.com` dan
`grahita-admin.tiusclaw.com` ke server, isi rahasia lewat environment
(`POSTGRES_PASSWORD`, `SMTP_USER`, `SMTP_PASS`), dan siapkan backup harian
sesuai `Architecture.md` §7. Caddy mengurus sertifikat TLS otomatis.
