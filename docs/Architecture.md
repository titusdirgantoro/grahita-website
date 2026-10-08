# Architecture — Sistem Pemesanan Cabang Grahita Indonesia

Dokumen ini menjelaskan **pilihan teknologi, topologi deployment, dan operasional**. Desain domain/aplikasi ada di `SystemDesign.md`, skema data di `Database.md`.

---

## 1. Ringkasan rekomendasi stack

| Lapisan | Pilihan | Alasan singkat |
|---|---|---|
| Backend | **Go 1.27** (rilis Agustus 2026) — modular monolith | Sesuai permintaan; binary tunggal, deploy mudah, hemat resource di VPS lokal |
| HTTP | `net/http` + **chi v5** | Routing standar library sudah kuat sejak Go 1.22; chi hanya untuk middleware & grouping, bukan framework berat |
| Akses DB | **pgx v5** + **sqlc** | Query SQL asli, type-safe, tanpa overhead ORM; mudah di-review |
| Migrasi | **goose** (atau golang-migrate) | SQL polos, bisa dijalankan dari binary yang sama |
| Background job | **River** (job queue di atas PostgreSQL) | Email, PDF, OCR, backup, reminder tanpa perlu Redis/RabbitMQ |
| Auth | Session cookie `HttpOnly; Secure; SameSite=Lax` di PostgreSQL, password **Argon2id** | Lebih aman & sederhana dari JWT untuk aplikasi web internal; logout instan |
| PDF | **Gotenberg** (HTML/CSS → PDF via Chromium headless) | Template invoice memakai HTML/CSS yang sama dengan desain portal — hasilnya konsisten dengan prototype |
| Excel | **excelize** | Standar de-facto di Go untuk .xlsx |
| OCR / AI | **Claude API (vision)** di balik interface `PaymentReader` | Akurat untuk struk m-banking Indonesia; bisa ditukar ke Google Document AI tanpa ubah domain |
| Object storage | **S3-compatible** (MinIO self-host atau Cloudflare R2 / IDCloudHost Object Storage) | File bukti transfer & dokumen tidak di dalam DB; presigned URL |
| Frontend | **React 19 + TypeScript + Vite**, TanStack Router + TanStack Query + TanStack Table, React Hook Form + Zod | Lihat §3 |
| Styling | **CSS asli dengan design token** hasil port langsung dari prototype (+ CSS Modules) | Prototype sudah punya token & komponen matang; menulis ulang ke Tailwind justru menambah risiko dan hilangnya nuansa desain |
| Database | **PostgreSQL 18** sekarang, upgrade ke 19 setelah GA stabil | Lihat §4 |
| Reverse proxy | **Caddy 2** | TLS otomatis untuk grahitaindonesia.com |
| Container | Docker Compose (fase 1) | Satu VPS cukup; migrasi ke k8s hanya bila benar-benar perlu |
| Observability | Prometheus + Grafana + Loki, `log/slog` JSON, Sentry | Cukup ringan untuk satu server |
| CI/CD | GitHub Actions → build image → deploy via SSH/Watchtower | Sederhana dan auditable |

### Mengapa modular monolith, bukan microservice

Beban kerja sistem ini kecil (± 16 cabang, puluhan pesanan/hari). Microservice akan menambah biaya operasional tanpa manfaat nyata. Struktur kode tetap dipisah per domain (lihat `SystemDesign.md` §2) sehingga bila kelak perlu dipecah, batasnya sudah jelas.

---

## 2. Backend Go — detail

### 2.1 Dependensi inti

```
go 1.27

github.com/go-chi/chi/v5          // routing & middleware
github.com/jackc/pgx/v5           // driver + pool PostgreSQL
github.com/riverqueue/river       // job queue di PostgreSQL
github.com/pressly/goose/v3       // migrasi
github.com/xuri/excelize/v2       // Excel
github.com/go-playground/validator/v10
github.com/alexedwards/argon2id   // hashing password
github.com/aws/aws-sdk-go-v2      // S3-compatible storage
github.com/wneessen/go-mail       // SMTP (Gmail)
github.com/getsentry/sentry-go
github.com/prometheus/client_golang
```

Semua query ditulis di `.sql` dan dibangkitkan dengan **sqlc** — tidak ada ORM.

### 2.2 Prinsip

- **Transaksi eksplisit** untuk operasi yang melibatkan penomoran, status, dan enqueue job (lihat pola transactional outbox di `SystemDesign.md` §5).
- **Context propagation** di seluruh handler → service → repository, dengan timeout per request.
- **Tidak ada state di memori**: semua state di PostgreSQL agar proses bisa di-restart kapan saja.
- **Zona waktu**: simpan `timestamptz` (UTC), konversi ke `Asia/Jakarta` hanya di batas sistem (penomoran, tampilan, laporan).
- **Uang**: `BIGINT` Rupiah bulat, tidak pernah `float`.

### 2.3 Alternatif yang dipertimbangkan

| Alternatif | Mengapa tidak dipilih |
|---|---|
| Echo / Fiber / Gin | Framework penuh tidak memberi nilai tambah di sini; `net/http` + chi lebih dekat ke standar dan lebih awet |
| GORM | Query dinamis sulit diaudit, masalah performa pada laporan order line; sqlc lebih transparan |
| Asynq (Redis) | Menambah satu komponen infrastruktur; River cukup dan transaksional bersama data |
| JWT stateless | Sulit mencabut sesi saat ganti password / nonaktifkan user |
| wkhtmltopdf | Tidak dikembangkan lagi; dukungan CSS modern buruk |

---

## 3. Frontend — rekomendasi

### 3.1 Pilihan: React 19 + TypeScript + Vite (SPA), bukan Next.js

Aplikasi ini **100% di balik login**, tidak butuh SEO maupun SSR. SPA yang berbicara ke API Go lebih sederhana untuk di-deploy (file statis di belakang Caddy) dan menghindari dua runtime (Node + Go) di produksi.

| Paket | Kegunaan |
|---|---|
| `react` 19 + `react-dom` | UI |
| `typescript` 5.x, `vite` | Tooling & build |
| `@tanstack/react-router` | Routing type-safe + route guard per role |
| `@tanstack/react-query` | Server state, caching, retry, invalidasi setelah mutasi |
| `@tanstack/react-table` | Tabel admin (sorting, grouping, paginasi server-side) |
| `react-hook-form` + `zod` | Form & validasi, skema dibagi dengan respons API |
| `@tanstack/react-virtual` | Virtualisasi matriks harga (Produk × Cabang) |
| `dnd-kit` (opsional) | Urutan produk di katalog |
| `vitest` + `@testing-library/react` + `playwright` | Unit & E2E |

Dua aplikasi dibangun dari satu monorepo dengan paket `ui` bersama:

```
web/
├─ packages/ui/          # token CSS + komponen dasar hasil port prototype
├─ apps/customer/        # grahitaindonesia.com
└─ apps/admin/           # grahitaindonesia.com/admin
```

### 3.2 Styling: pertahankan desain prototype

`customer_v2.html` dan `admin_v2.html` sudah berisi sistem desain yang matang dan konsisten. Rencananya:

1. Ekstrak blok `:root{...}` menjadi `packages/ui/tokens.css` — warna brand, skala turunan `color-mix`, warna status (success/warning/danger/info), `--h-control`, bayangan, radius.
2. Ubah tiap pola komponen prototype (`.btn`, `.input`, `.card`, `.table`, `.chip`, `.modal`, `.seg`, sidebar `a-shell`, bottom-nav mobile) menjadi komponen React dengan CSS Modules bernama sama.
3. **Self-host** font Plus Jakarta Sans dan ikon Phosphor (prototype memakai CDN; produksi tidak boleh bergantung CDN pihak ketiga).
4. Pertahankan aksesibilitas yang sudah ada di prototype: `aria-current`, `aria-pressed`, label `sr`, `:focus-visible`.
5. Grafik dashboard tetap **SVG buatan sendiri** seperti di prototype (ringan, cocok dengan token), bukan library chart besar.

**Mengapa bukan Tailwind/shadcn:** desain sudah jadi dan khas; memaksakannya ke utility class berisiko mengubah tampilan dan memperlambat pengerjaan. Bila tim lebih nyaman dengan Tailwind, token di atas dapat dipetakan ke `@theme`, tapi itu pilihan opsional, bukan rekomendasi utama.

### 3.3 Alternatif

| Alternatif | Catatan |
|---|---|
| **SvelteKit** | Bundle terkecil, DX sangat baik. Pilih ini bila tim sudah terbiasa Svelte; ekosistem tabel/grid untuk admin lebih tipis daripada React. |
| **Next.js** | Berlebihan: tidak ada kebutuhan SEO/SSR, menambah runtime Node di produksi. |
| **HTMX + templ (Go)** | Paling sedikit kode dan satu bahasa saja. Cocok bila tim hanya Go — tapi interaksi kaya di prototype (matriks harga inline edit, filter bertumpuk, grafik interaktif, zoom bukti) lebih nyaman di SPA. |

---

## 4. PostgreSQL — versi

Per Oktober 2026, PostgreSQL 19 masih berada di tahap beta/release candidate dengan GA direncanakan sekitar Oktober 2026, sementara seri stabil yang berjalan adalah PostgreSQL 18.

**Rekomendasi: bangun di PostgreSQL 18, rencanakan upgrade ke 19 setelah rilis minor 19.1–19.2 keluar.** Memulai langsung di versi yang baru GA berisiko untuk sistem produksi pertama, sementara PG 18 sudah membawa fitur yang kita pakai.

Fitur PG 18 yang relevan:

- `uuidv7()` bawaan — identifier acak yang tetap urut waktu, bagus untuk indeks primary key.
- I/O asinkron, perbaikan performa `VACUUM` dan planner.
- `CREATE INDEX`/`REINDEX` paralel yang lebih baik — berguna untuk tabel order line yang terus tumbuh.

Ekstensi yang dipakai: `pg_stat_statements`, `pgcrypto`, `pg_trgm` (pencarian nama cabang/produk), `unaccent` (opsional), dan `btree_gin` bila diperlukan filter kombinasi di laporan.

Semua fitur yang dipakai tetap kompatibel dengan PG 17 ke atas sehingga upgrade/downgrade tidak mengunci proyek.

---

## 5. Topologi deployment

### 5.1 Fase 1 — satu VPS (rekomendasi awal)

```
                    Internet
                       │  HTTPS (Caddy, TLS otomatis)
         ┌─────────────▼──────────────────────────────────┐
         │  VPS Indonesia (4 vCPU / 8 GB / 160 GB SSD)    │
         │                                                 │
         │  caddy ──┬─► web-customer (static, Vite build)  │
         │          ├─► web-admin    (static)              │
         │          └─► api (Go, :8080)                    │
         │                 │                               │
         │                 ├─► postgres 18  (volume)       │
         │                 ├─► minio        (volume)       │
         │                 ├─► gotenberg    (PDF)          │
         │                 └─► worker (Go, River)          │
         │                                                 │
         │  prometheus · grafana · loki                    │
         └──────────────────┬──────────────────────────────┘
                            │
      ┌─────────────────────┼──────────────────────┐
      ▼                     ▼                      ▼
  Gmail SMTP        Google Drive (backup)     Claude API (OCR)
```

`api` dan `worker` adalah **binary Go yang sama** dengan flag berbeda (`--mode=api` / `--mode=worker`), sehingga tidak ada duplikasi kode dan konfigurasi.

Penyedia yang cocok (latensi & kepatuhan data di Indonesia): Biznet Gio, IDCloudHost, Alibaba Cloud Jakarta, atau AWS ap-southeast-3.

### 5.2 Fase 2 — bila trafik tumbuh

- Pisahkan PostgreSQL ke layanan terkelola dengan replika baca dan PITR.
- Jalankan 2+ instance `api` di belakang load balancer (sudah stateless karena sesi di DB).
- Pindahkan object storage ke layanan terkelola (R2/S3) dan sajikan file lewat presigned URL.
- Tambah worker terpisah untuk job berat (ekspor laporan besar, OCR).

### 5.3 Lingkungan

| Lingkungan | Keterangan |
|---|---|
| `local` | Docker Compose, MinIO, Mailpit sebagai penangkap email, OCR mode mock |
| `staging` | Salinan produksi dengan data anonim, subdomain `staging.grahitaindonesia.com` dengan proteksi basic-auth |
| `production` | grahitaindonesia.com (customer) dan /admin (admin) |

---

## 6. Keamanan

| Area | Kontrol |
|---|---|
| Transport | HTTPS wajib, HSTS, TLS 1.2+ |
| Sesi | Cookie `HttpOnly; Secure; SameSite=Lax`, rotasi ID saat login, idle timeout 8 jam, absolute 30 hari, dicabut saat ganti password |
| Password | Argon2id (m=64MB, t=3, p=2), minimal 8 karakter, cek daftar password umum |
| CSRF | Token double-submit untuk request non-GET |
| Rate limit | Login, lupa password, validasi token registrasi, unggah file |
| Otorisasi | Dicek di layer service berdasarkan role + kepemilikan cabang, bukan hanya disembunyikan di UI |
| Isolasi data cabang | Setiap query pesanan/invoice cabang wajib difilter `branch_id` dari sesi |
| Unggahan | Validasi MIME asli (magic bytes), batas ukuran & jumlah, nama file di-generate ulang, disajikan via presigned URL `Content-Disposition: attachment` |
| Header | CSP ketat, `X-Content-Type-Options`, `Referrer-Policy`, `frame-ancestors 'none'` |
| Rahasia | Variabel lingkungan / file terenkripsi (SOPS + age), tidak pernah masuk repo |
| Audit | Semua aksi admin yang mengubah data tercatat (aktor, IP, sebelum/sesudah) |
| Backup | Terenkripsi sebelum diunggah ke Google Drive |

---

## 7. Backup & pemulihan

| Komponen | Mekanisme | Frekuensi | Retensi |
|---|---|---|---|
| PostgreSQL | `pg_dump -Fc` terenkripsi (age) → Google Drive via rclone | Harian 02:00 WIB | 30 harian, 12 bulanan |
| PostgreSQL WAL | Arsip WAL ke object storage (PITR) | Kontinu | 7 hari |
| Object storage | Sinkronisasi inkremental bukti transfer & dokumen ke Google Drive | Harian | 90 hari |
| Konfigurasi | Repo infrastruktur (git) | Tiap perubahan | — |

Hasil tiap backup dicatat di tabel `backup_runs` dan ditampilkan di halaman Pengaturan; kegagalan memunculkan notifikasi di dashboard dan email ke Superadmin. **Uji restore dijadwalkan setiap kuartal** ke lingkungan staging — backup yang tidak pernah diuji tidak dihitung sebagai backup.

Target: **RPO ≤ 24 jam** (≤ 5 menit bila PITR aktif), **RTO ≤ 4 jam**.

---

## 8. Observability

- **Log**: `log/slog` format JSON dengan `request_id`, `user_id`, `branch_id`, `order_id`; dikumpulkan Loki.
- **Metrik**: latensi & status HTTP per rute, durasi & kegagalan job River, panjang antrean email, durasi generate PDF, jumlah pesanan per status.
- **Trace** (opsional fase 2): OpenTelemetry pada alur submit → verifikasi → dokumen.
- **Alert**: API error rate > 2% selama 5 menit, job gagal > 3 berturut-turut, email outbox tertahan > 30 menit, backup gagal, disk > 80%.
- **Health**: `/healthz` (liveness) dan `/readyz` (cek DB, storage, Gotenberg).

---

## 9. CI/CD

```
push / PR
   ├─ lint (golangci-lint, eslint, tsc --noEmit)
   ├─ test (go test ./..., vitest)
   ├─ migrasi diuji di container Postgres bersih
   ├─ build image (api/worker) + build statis frontend
   ├─ scan (govulncheck, trivy, npm audit)
   └─ deploy: staging otomatis → produksi manual approval
```

Migrasi dijalankan sebagai langkah terpisah sebelum rilis dan harus **kompatibel mundur** (tambah kolom nullable dulu, isi data, baru perketat) agar rollback aman.

---

## 10. Perkiraan biaya bulanan (indikatif)

| Item | Perkiraan |
|---|---|
| VPS 4 vCPU / 8 GB | Rp 400.000 – 900.000 |
| Backup storage (Google Drive / Workspace) | sudah ada / Rp 50.000 |
| Domain + TLS | Rp 25.000 (TLS gratis via Caddy) |
| Claude API untuk OCR bukti transfer | < Rp 100.000 pada volume ratusan pesanan/bulan |
| Email (Gmail akun Grahita) | sudah ada |
| **Total** | **± Rp 500.000 – 1.100.000 / bulan** |

---

## 11. Risiko teknis

| Risiko | Mitigasi |
|---|---|
| Limit pengiriman Gmail (±2.000 email/hari untuk Workspace) | Volume kita jauh di bawah limit; sediakan opsi beralih ke SMTP relay (Resend/SES) lewat konfigurasi |
| Bukti transfer buram/screenshot terpotong → OCR salah | Ambang keyakinan + selalu jatuh ke review manual; AI tidak pernah menolak sendiri |
| Nomor invoice bentrok saat submit bersamaan | Counter di PostgreSQL dengan `UPDATE ... RETURNING` di dalam transaksi (lihat `Database.md` §6) |
| Satu VPS = satu titik kegagalan | Backup + PITR, image yang bisa dideploy ulang < 30 menit, upgrade ke HA di fase 2 |
| Ketergantungan Gotenberg (Chromium) boros memori | Batasi konkurensi job PDF, pakai antrean, limit memori container |
| Vendor lock-in penyedia AI | Interface `PaymentReader` dengan implementasi ganda + mode mock |
