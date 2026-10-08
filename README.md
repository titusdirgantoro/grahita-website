# Grahita Portal — kerangka Docker

Kerangka awal yang bisa langsung dijalankan untuk sistem pemesanan cabang Grahita Indonesia.
Dokumen perencanaan: `PRD.md`, `SystemDesign.md`, `Architecture.md`, `Database.md`.

> **Status:** infrastruktur sudah berjalan (API, database + migrasi, worker, storage, penangkap email, render PDF, reverse proxy). Endpoint bisnis belum diimplementasikan — frontend masih prototype HTML statis.

---

## Prasyarat

- Docker Engine 24+ dan Docker Compose v2 (`docker compose version`)
- Port yang dipakai di host: `8080`, `8081`, `8088`, `8025`, `9000`, `9001`, `5433`

Tidak perlu memasang Go, Node, atau PostgreSQL di komputer Anda — semuanya di dalam container.

## Menjalankan

```bash
cp .env.example .env
make up          # atau: docker compose up -d --build
make seed        # mengisi data contoh (7 produk, 4 cabang, harga, rekening)
make smoke       # memastikan semua service hidup
```

Build pertama memerlukan koneksi internet (menarik image dan dependensi Go) dan memakan waktu beberapa menit. Setelah itu `make up` berjalan cepat.

| Layanan | Alamat |
|---|---|
| Customer Portal | http://localhost:8080 |
| Admin Portal | http://localhost:8081 |
| API (langsung) | http://localhost:8088/healthz |
| Mailpit — semua email dev tertangkap di sini | http://localhost:8025 |
| MinIO console (`grahita` / `grahita123`) | http://localhost:9001 |
| PostgreSQL | `localhost:5433` |

## Memverifikasi

```bash
curl http://localhost:8088/healthz        # proses hidup
curl http://localhost:8088/readyz         # database + migrasi siap
curl http://localhost:8088/api/v1/meta    # versi, env, versi PostgreSQL, waktu WIB
curl http://localhost:8088/api/v1/products
curl http://localhost:8080/api/v1/meta    # membuktikan proxy Caddy bekerja
make psql                                 # masuk ke psql, coba: \dt
```

## Perintah lain

```bash
make logs      # ikuti log api & worker
make ps        # status container
make migrate   # jalankan migrasi (otomatis saat up)
make down      # hentikan, data tetap ada
make reset     # hentikan DAN hapus semua volume
```

---

## Isi stack

| Service | Image / sumber | Fungsi |
|---|---|---|
| `postgres` | `postgres:18-alpine` | Database utama |
| `migrate` | build `./api` | Menjalankan migrasi lalu berhenti; service lain menunggunya selesai |
| `api` | build `./api` | HTTP API (Go) |
| `worker` | build `./api` | Job latar belakang — binary yang sama, mode berbeda |
| `minio` + `minio-init` | `quay.io/minio/minio` | Object storage S3 untuk bukti transfer & dokumen |
| `mailpit` | `axllent/mailpit` | Menangkap email saat development (tidak ada email keluar) |
| `gotenberg` | `gotenberg/gotenberg:8` | Render HTML → PDF untuk invoice & surat jalan |
| `caddy` | `caddy:2-alpine` | Menyajikan frontend + proxy `/api/*` ke `api` |

## Struktur

```
.
├─ docker-compose.yml           # development
├─ docker-compose.prod.yml      # overlay produksi (domain + TLS)
├─ Makefile
├─ infra/caddy/                 # Caddyfile.dev & Caddyfile.prod
├─ scripts/smoke.sh
├─ web/
│  ├─ customer/index.html       # prototype — nanti diganti build Vite
│  └─ admin/index.html
└─ api/
   ├─ Dockerfile                # multi-stage, image akhir ±20 MB
   ├─ go.mod                    # satu dependensi: pgx/v5
   ├─ cmd/server/main.go        # mode: api | worker | migrate | seed | healthcheck
   ├─ migrations/*.sql          # di-embed ke binary
   └─ internal/
      ├─ platform/config        # konfigurasi dari environment
      ├─ platform/db            # pool + migration runner
      ├─ httpapi                # router, middleware, handler
      ├─ worker                 # loop job latar belakang
      └─ seed                   # data contoh development
```

### Catatan desain

- **Satu binary, beberapa mode.** `api` dan `worker` memakai image yang sama; tidak ada duplikasi konfigurasi.
- **Migrasi di-embed** ke binary dan berjalan sebagai service `migrate` yang harus selesai sebelum `api` naik (`service_completed_successfully`), sehingga tidak ada kondisi balapan saat start.
- **Dependensi Go sengaja minimal** — hanya `pgx/v5`. Routing memakai `net/http` bawaan, migration runner ditulis sendiri (±80 baris). Dependensi lain ditambahkan hanya saat benar-benar dibutuhkan.
- **Healthcheck tanpa curl**: binary memanggil dirinya sendiri dengan `--mode=healthcheck`, jadi image runtime tetap kecil.
- **Tidak ada rahasia di dalam repo**; semua lewat environment. `.env` masuk `.gitignore`.

---

## Langkah berikutnya

Urutan yang saya sarankan:

1. **Auth** — registrasi via token, login, sesi, reset password (`SystemDesign.md` §4.1–4.2).
2. **Master data** — CRUD cabang, produk, matriks harga, rekening bank.
3. **Pesanan** — draft, submit + penomoran (fungsi `next_numbers` sudah ada di migrasi 0002), aturan sertifikat, Cash & Carry.
4. **Pembayaran & dokumen** — unggah bukti, verifikasi manual, invoice PDF lewat Gotenberg, surat jalan Excel.
5. **River** menggantikan loop worker manual, agar enqueue job bisa masuk transaksi bisnis yang sama.
6. **Frontend Vite** menggantikan prototype statis di `web/` — token CSS diekstrak dari prototype (`Architecture.md` §3.2).

## Produksi

```bash
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

Sebelum itu: arahkan DNS `grahitaindonesia.com` ke server, isi `.env` produksi (`POSTGRES_PASSWORD`, `SMTP_USER`, `SMTP_PASS`), dan siapkan job backup harian sesuai `Architecture.md` §7. Caddy mengurus sertifikat TLS secara otomatis.
