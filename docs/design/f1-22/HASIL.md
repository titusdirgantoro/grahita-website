# F1-22 — Observability, keamanan deployment dan rilis

Kartu: **t_be28b545 — F1-22 Observability, keamanan deployment dan rilis**

Dokumen acuan: Architecture.md §5 (topologi deployment, dua subdomain, api/worker
binary sama, PG18/MinIO/Gotenberg), §6 (keamanan: cookie, CSP/HSTS/header, CSRF,
rate limit, rahasia di luar repo), §7 (backup & alert backup gagal), §8
(observability: `log/slog` JSON, metrik, Sentry, `/healthz` `/readyz`, daftar
alert: error rate > 2%, job gagal, email tertahan > 30 menit, backup gagal, disk
> 80%), §9 (CI: lint/test/migrasi/build/scan; staging otomatis → produksi
persetujuan manual; migrasi kompatibel mundur); PRD.md §9–10; SystemDesign.md §9.

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), serta batas yang **tidak** diklaim.

## Lingkup yang dikerjakan

| Permukaan | Isi |
|---|---|
| `api/internal/platform/observability` | paket baru: metrik Prometheus (namespace `grahita`), middleware HTTP berbasis pola rute, pencatat hasil pekerjaan, rasio disk, dan pelaporan Sentry opsional (`metrics.go`, `sentry.go`, `statfs_linux.go`, `statfs_other.go`) |
| `api/cmd/server` | wiring metrik + Sentry, `stats.go` (sumber angka dari PostgreSQL), listener `/metrics` proses worker (`WORKER_METRICS_ADDR`) |
| `api/internal/worker` | opsi `DenganPencatatJob` + hook River `HookMetrikJob` (durasi & hasil tiap job) |
| `api/internal/httpapi` | `GET /metrics` (tanpa sesi, tidak dijangkau publik) + middleware metrik pada router produksi |
| `api/internal/platform/httpx` | panic pada `Recoverer` juga dikirim ke Sentry |
| `infra/observability` | Prometheus (scrape api/worker/node + 12 aturan alarm), Alertmanager (route + inhibit + email dari environment), Loki/Promtail, Grafana (provisioning + 2 dasbor), **uji satuan alarm `prometheus/tests/grahita-alerts_test.yml`**, README runbook |
| `infra/caddy` | `Caddyfile.prod` diperketat (CSP + Permissions-Policy + blokir `/metrics` di tepi), `Caddyfile.staging` baru (basic-auth, `X-Robots-Tag`, HSTS pendek) |
| Compose | `docker-compose.observability.yml` baru; `docker-compose.staging.yml` baru; overlay prod/staging kini memakai image GHCR bertag commit |
| CI/CD | `ci.yml` + gerbang migrasi kompatibel mundur, govulncheck jadi blocking, job Trivy (image + Dockerfile), **job `observability`** (promtool: config + `--lint=all` + uji satuan alarm), job `rahasia` menjalankan kendali diri `check-secrets.sh --uji-diri` sebelum pemindaian (putaran 3); `release.yml` baru: staging otomatis, produksi menunggu persetujuan reviewer GitHub Environment |
| Skrip | `scripts/check-migration-kompatibel.sh`, `scripts/smoke-staging.sh`; `infra/deploy/deploy.sh` + README |

## Keputusan desain

1. **Nama metrik stabil & dipakai apa adanya oleh aturan alarm.** Namespace
   `grahita`; `grahita_http_requests_total{route,method,status}`,
   `grahita_http_request_duration_seconds`, `grahita_jobs_completed_total{kind,outcome}`,
   `grahita_jobs_current{state}`, `grahita_outbox_messages{status}`,
   `grahita_outbox_oldest_pending_seconds`, `grahita_orders_current{status}`,
   `grahita_backup_last_run_success{jenis}`,
   `grahita_backup_last_run_timestamp_seconds{jenis}`,
   `grahita_disk_used_ratio{mount}`, `grahita_build_info{version,env}`.
   Label `route` memakai **pola rute chi**, bukan path mentah, supaya id pesanan
   tidak meledakkan jumlah deret waktu (diuji: `TestMiddlewareMemakaiPolaRute`).
2. **`/metrics` tidak pernah publik, dua lapis.** Caddy hanya mem-proxy `/api/*`
   dan overlay produksi tidak memetakan port API ke host; sebagai pertahanan
   berlapis Caddy juga memblokir `/metrics` di tepi dengan 404.
3. **Worker punya listener metrik sendiri** (`WORKER_METRICS_ADDR`, bawaan
   `:9091`) karena worker tidak melayani HTTP lain — tanpa ini metrik pekerjaan
   River tidak akan pernah dipanen.
4. **Database tetap di balik interface.** `observability.StatsSource` diisi
   composition root (`cmd/server/stats.go`), sehingga paket platform tidak
   menyentuh pgx/sqlc (aturan lapisan `internal/arch` tetap hijau).
5. **Sentry opsional dan no-op tanpa DSN.** Tanpa `SENTRY_DSN` tidak ada
   pengiriman apa pun; tidak ada DSN/kredensial di repo.
6. **Gagal mengumpulkan metrik bersifat lunak.** Satu sumber (mis. database)
   gagal → metrik lain (khususnya disk) tetap diperbarui dan kegagalan dicatat
   ke log (diuji: `TestPerbaruiStatistikTetapJalanSaatSumberGagal`).
7. **Ambang alarm persis Architecture.md §8**: error rate 5xx > 2% (5 menit);
   job gagal > 3 dalam 10 menit; email tertua menunggu > 30 menit; cadangan
   gagal; disk > 80%. Ditambah alarm yang diminta catatan QA F1-18: job
   `discarded` (gagal permanen) dan antrean job menumpuk.
8. **Caddy: `handle` bukan `respond` untuk blokir internal.** Ditemukan lewat
   verifikasi replika: urutan direktif Caddy menaruh `handle` SEBELUM `respond`,
   sehingga `respond @internal 404` dievaluasi setelah penangan SPA catch-all
   dan `/metrics` justru dilayani `index.html` (200). Setelah diperbaiki menjadi
   `handle @internal { respond 404 }`, `/metrics` di tepi menjawab **404**.
9. **Rilis: staging otomatis, produksi disetujui manusia.** Job `produksi` pada
   `release.yml` memakai GitHub Environment `production` dengan protection rule
   *Required reviewers*; tidak ada jalan melewatinya dari dalam workflow. Staging
   diuji oleh `scripts/smoke-staging.sh` dan job-nya **gagal** bila akun uji belum
   ada di Secrets — staging tidak boleh "hijau" tanpa diuji.
10. **Migrasi kompatibel mundur ditegakkan mesin**, bukan disiplin:
    `scripts/check-migration-kompatibel.sh` menolak `DROP`/`RENAME`/`ALTER TYPE`/
    `SET NOT NULL` tanpa `DEFAULT` di bagian Up migrasi 0002+ (bagian Down
    dikecualikan), dijalankan di CI dan `release.yml`.
11. **Alertmanager dirender dari template, bukan `--config.expand-env`.**
    Alertmanager v0.34.1 **tidak** mendukung substitusi environment di berkas
    konfigurasinya (dibuktikan: `--help` tidak memuat opsi itu, dan
    `amtool check-config` atas berkas mentah gagal). Karena itu
    `infra/observability/alertmanager/mulai.sh` merender
    `alertmanager.tmpl.yml` memakai `sed`, **menolak start** bila ada variabel
    wajib yang kosong, dan baru menjalankan biner. Verifikasi memakai jalur
    render yang sama seperti container.
12. **Compose produksi/staging memakai image bertag commit** (`IMAGE_PREFIX`/
    `IMAGE_TAG`) supaya yang berjalan di host adalah artefak yang sudah lolos
    CI, bukan hasil build di host.
13. **Alarm disimulasikan dengan uji satuan `promtool test rules`, bukan hanya
    di-lint.** `infra/observability/prometheus/tests/grahita-alerts_test.yml`
    menyuntikkan deret waktu sintetis dan membuktikan tiap alarm MENYALA pada
    keadaan buruk serta TIDAK menyala pada keadaan sehat (termasuk kontrol
    negatif: satu kegagalan job tidak memicu alarm, disk 85% tidak memicu alarm
    kritis, `for:` dihormati). Berkas uji sengaja di `tests/` karena Prometheus
    memuat `rules/*.yml`; job CI `observability` menjalankannya setiap push/PR.

## Bukti verifikasi (perintah + hasil nyata)

Semua perintah dijalankan di mesin ini (tanpa VPS/DNS nyata). Log mentah ada di
berkas `BUKTI-*.txt` pada direktori yang sama.

### 1. Unit test paket baru

```
cd api && go test -count=1 ./internal/platform/observability/ ./internal/worker/ ./internal/arch/
ok  github.com/grahita/portal/api/internal/platform/observability
ok  github.com/grahita/portal/api/internal/worker
ok  github.com/grahita/portal/api/internal/arch
```

Uji baru: pemetaan pola rute, penghitung permintaan/job, gauges dari sumber,
ketahanan saat sumber gagal, `JalankanPemantauan` berhenti saat context
dibatalkan, Sentry no-op tanpa DSN, hook River metrik, `/metrics` pada router
produksi (tersedia tanpa sesi; 404 bila metrik dimatikan), dan hook River
mencatat durasi/hasil serta meneruskan error apa adanya.

### 2. Validasi konfigurasi dengan biner resmi → `BUKTI-VERIFIKASI-KONFIG.txt`

| Alat | Hasil |
|---|---|
| `caddy v2.11.7 validate Caddyfile.prod` | **Valid configuration** |
| `caddy v2.11.7 validate Caddyfile.staging` | **Valid configuration** |
| `promtool v3.15.0 check config prometheus.yml` | **SUCCESS: valid prometheus config file syntax** |
| `promtool v3.15.0 check rules --lint=all grahita-alerts.yml` | **SUCCESS: 12 rules found** |
| `amtool v0.34.1 check-config` pada keluaran `mulai.sh` (jalur render yang sama dengan container) | **SUCCESS** (route + 2 inhibit rules + 1 receiver) |
| `mulai.sh` tanpa `ALERT_SMTP_PASS` | **exit 1** — menolak render (fail closed) |
| JSON kedua dasbor Grafana | parse OK |
| `scripts/cek-uid-grafana.py` (uid sumber data dasbor ⊂ uid provisioning) | **LULUS** + kontrol negatif (uid palsu ditolak) |
| `loki v3.5.0 -verify-config loki-config.yml` | **exit 0** (konfigurasi valid) |
| `promtail v3.5.0 -dry-run promtail-config.yml` | konfigurasi terurai (klien `loki:3100` terbaca). `docker_sd` gagal karena socket Docker tidak dapat diakses di sesi ini — batas lingkungan, bukan cacat konfigurasi |
| `docker compose config -q` (prod, observability, staging) | ketiganya exit 0 |

### 3. Uji asap dua host pada replika staging → `BUKTI-SMOKE-STAGING-DUA-HOST.txt`

Replika dibuat **dari `infra/caddy/Caddyfile.staging` yang asli**: `caddy adapt`
lalu hanya nilai bergantung-lingkungan yang diganti (alamat listen :8443,
upstream `api:8080` → `127.0.0.1:8088`, akar SPA → `web/apps/*/dist`, TLS
otomatis ACME → CA internal Caddy). Struktur rute, header, basic-auth, dan
blokir `/metrics` tidak disentuh.

```
$ bash scripts/smoke-staging.sh
1. Kedua host menyajikan UI dan API same-origin
   LULUS cabang: halaman SPA tersaji (200); CSP/HSTS/nosniff/X-Frame-Options ada
   LULUS admin : halaman SPA tersaji (200); CSP/HSTS/nosniff/X-Frame-Options ada
   LULUS kedua host: /api/v1/meta 200 (env staging)
2. Login cabang + atribut cookie sesi
   LULUS login cabang 200
   LULUS cookie sesi memuat HttpOnly / SameSite=Lax / Secure
   LULUS cookie sesi host-only (tanpa Domain)
3. Katalog cabang 200 (produk uji terpilih dari harga cabang sesi)
4. Draft dibuat (201, total 700000) → submit 200 (status submitted, nomor 2026-10-01)
5. Isolasi sesi antar host: sesi cabang → host admin 401 (tidak bocor)
6. Login admin 200 (peran superadmin) → verifikasi 200 (status pesanan done, hasil cocok)
7. Daftar dokumen: KOSONG → TERHALANG (butuh Gotenberg + object storage)
HASIL: TERHALANG — alur login→submit→verify lulus, unduh dokumen belum bisa dibuktikan
SMOKE_EXIT=2
```

Kode keluar 2 (bukan 0) sengaja: unduh dokumen belum bisa dibuktikan di mesin
ini, jadi alurnya **tidak** disebut lulus. Tambahan bukti pada replika yang
sama: `/metrics` di tepi → **404**, halaman tanpa basic-auth → **401**.

### 4. Metrik hidup (API dan worker) → `BUKTI-METRIK.txt`

Setelah alur di atas, `GET /metrics` API menunjukkan (kutipan nyata):

```
grahita_http_requests_total{method="POST",route="/api/v1/orders",status="201"} 3
grahita_http_requests_total{method="POST",route="/api/v1/orders/{id}/submit",status="200"} 2
grahita_http_requests_total{method="POST",route="/api/v1/admin/orders/{id}/verify",status="200"} 2
grahita_http_requests_total{method="GET",route="/api/v1/auth/me",status="401"} 2
grahita_orders_current{status="done"} 2
grahita_orders_current{status="draft"} 1
grahita_jobs_current{state="available"} 2
grahita_disk_used_ratio{mount="/"} 0.7324
grahita_build_info{env="staging",version="dev"} 1
```

Mode worker (`WORKER_METRICS_ADDR=:9091`) melayani `/healthz` (`ok`) dan
`/metrics`, dan hook River benar-benar mencatat kegagalan nyata:

```
grahita_jobs_completed_total{kind="email_outbox_pindai",outcome="sukses"} 2
grahita_jobs_completed_total{kind="render_dokumen_pesanan",outcome="gagal"} 4
grahita_job_duration_seconds_count{kind="render_dokumen_pesanan"} 2
```

Baris terakhir adalah bukti langsung bahwa risiko residu F1-18 (job render
gagal karena Gotenberg/S3 belum terpasang) **terukur dan dapat dialarmkan** —
alarm `GrahitaJobSeringGagal`/`GrahitaJobGagalPermanen` menyala pada keadaan
itu. Tidak ada kredensial pada keluaran metrik/log.

### 5. Gerbang migrasi & rahasia → `BUKTI-GERBANG-MIGRASI.txt`, `BUKTI-KONTROL-NEGATIF-RAHASIA.txt`

```
$ bash scripts/check-migration-kompatibel.sh
Migrasi kompatibel mundur: tidak ada pernyataan destruktif di bagian Up.
$ ./scripts/check-secrets.sh
Tidak ada rahasia terdeteksi pada berkas yang dilacak.
$ ./scripts/check-secrets.sh --uji-diri        # kendali diri (putaran 3)
UJI DIRI check-secrets: LULUS (15/15 kasus)   # 11 pola wajib TERDETEKSI,
                                              # 2 contoh interpolasi wajib bersih,
                                              # berkas tak terbaca wajib fail closed
$ actionlint v1.7.7 .github/workflows/ci.yml .github/workflows/release.yml
(exit 0, tanpa temuan)
```

Gerbang rahasia tidak lagi "hijau hampa": pemblokir QA putaran 1 (pola
`-----BEGIN …` diperlakukan grep sebagai *option* sehingga pemeriksaan 1 selalu
melaporkan bersih) sudah diperbaiki di `f2b40fc`, dan sekarang ada **kendali
negatif tetap** — berkas terlacak berisi token/kunci privat membuktikan gerbang
benar-benar MERAH (exit 1), berkas tak terbaca membuktikan gerbang MENOLAK
(exit 2, fail closed). Rinciannya: `BUKTI-KONTROL-NEGATIF-RAHASIA.txt`.

Kontrol negatif gerbang migrasi (berkas migrasi uji berisi `DROP COLUMN` di
bagian Up, dibuat lalu dihapus kembali):

```
PELANGGARAN 9999_kontrol_negatif.sql:2: pernyataan DROP di bagian Up: ALTER TABLE orders DROP COLUMN catatan;
HASIL: LULUS — gerbang menolak migrasi destruktif (exit != 0)
--- gerbang atas repo bersih ---
Migrasi kompatibel mundur: tidak ada pernyataan destruktif di bagian Up.
```

### 6. Suite backend penuh (PostgreSQL nyata)

```
$ TEST_DATABASE_URL=postgres://...@127.0.0.1:5455/grahita_f122suite?sslmode=disable \
  go test -race -p 1 -count=1 -timeout 45m ./...
23 paket ok, 0 FAIL, 0 SKIP
SUITE_EXIT=0   (mulai 08:55:47, selesai 09:12:02)
```

Catatan: paket `internal/httpapi` dijalankan ulang sendiri pada HEAD (setelah
urutan middleware metrik diubah) dengan hasil yang sama; angka di atas berasal
dari suite penuh yang menyelesaikan seluruh paket berurutan.

### 7. Verifikasi ulang pada HEAD (91ca217) → `BUKTI-VERIFIKASI-ULANG-HEAD.txt`

Karena HEAD bergerak setelah commit F1-22 (kartu lain: F1-19/F1-20/F1-21/F2-23),
seluruh gerbang dan bukti perilaku **dijalankan ulang pada HEAD 91ca217**, bukan
sekadar mengutip bukti commit `5bbffb4`:

- gerbang statis: `gofmt -l` kosong, `go vet`, `go build`, gerbang migrasi,
  `check-secrets.sh`, `actionlint` (ci.yml + release.yml) — semuanya exit 0;
- validasi konfigurasi: caddy (prod + staging), promtool (config + 12 aturan),
  loki `-verify-config`, promtail `-dry-run`, `amtool` lewat `mulai.sh`
  (menolak render tanpa kredensial SMTP = fail closed), `docker compose config -q`
  (prod + staging + observability), uid Grafana;
- replika staging dua host dibangun ulang dari `Caddyfile.staging` asli:
  kedua host 200 dengan CSP/HSTS/nosniff/X-Frame-Options, tanpa basic-auth 401,
  `/metrics` di tepi 404 (**hanya terukur bila basic-auth diberikan**; tanpa
  basic-auth nilainya 401), dan `scripts/smoke-staging.sh` → **login → katalog →
  draft → submit → verifikasi (done) lulus; unduh dokumen TERHALANG** (`SMOKE_EXIT=2`);
- metrik API + worker hidup kembali (termasuk `grahita_jobs_completed_total
  {kind="render_dokumen_pesanan",outcome="gagal"}` — risiko residu F1-18 terukur
  dan dapat dialarmkan), label `route` tetap pola chi bukan id mentah;
- **simulasi alarm** `promtool test rules infra/observability/prometheus/tests/
  grahita-alerts_test.yml` → `SUCCESS` (11 kasus: alarm menyala pada keadaan
  buruk, tidak menyala pada keadaan sehat), dijalankan pula oleh job CI
  `observability` yang divalidasi `actionlint`;
- suite backend penuh `-race -p 1` pada database `grahita_f122r3` (PostgreSQL
  nyata) di HEAD: **`SUITE_EXIT=0`, 23 paket ok, 0 FAIL, 0 SKIP**
  (`internal/httpapi` 706,7 s; log: `BUKTI-SUITE-BACKEND-HEAD.txt`).

### 8. Verifikasi ulang pada HEAD terintegrasi (2707967) → `BUKTI-VERIFIKASI-ULANG-HEAD2.txt`

HEAD bergerak 17 commit sejak 91ca217 (berkas F1-22 sendiri **tidak** berubah:
`git log 65d7cc1..HEAD -- infra/observability .github/workflows infra/caddy
scripts api/internal/platform/observability ...` kosong, tetapi
`api/cmd/server/main.go`, `api/internal/worker/worker.go`, dan `ci.yml` tersentuh
kartu lain). Karena itu seluruh bukti dijalankan ulang pada HEAD terintegrasi,
memakai biner resmi (promtool 3.15.0, actionlint 1.7.7, caddy v2.11.7):

- gerbang statis (`gofmt -l`, `go vet`, `go build`, gerbang migrasi,
  `check-secrets.sh`) dan `actionlint` atas `ci.yml` + `release.yml`: exit 0;
- `promtool check config` + `check rules --lint=all` (12 aturan) + **simulasi
  alarm** `promtool test rules` → `SUCCESS`; `caddy validate` prod → valid,
  staging tanpa hash → **menolak** (fail closed), staging dengan hash → valid;
- replika staging dua host dibangun ulang dari `Caddyfile.staging` asli
  (hanya nilai lingkungan yang dipatch): tanpa basic-auth 401, kedua UI 200
  dengan CSP/HSTS/nosniff/X-Frame-Options/X-Robots-Tag, `/api/v1/meta` 200,
  `/metrics` di tepi **404 bila basic-auth diberikan** (tanpa basic-auth: 401)
  dan `/internal/*` di tepi **404**;
- `scripts/smoke-staging.sh` → login → katalog → draft → submit → verifikasi
  (done) **lulus**; daftar dokumen KOSONG → **TERHALANG** (`SMOKE_EXIT=2`);
- metrik API + worker hidup kembali (termasuk
  `grahita_jobs_completed_total{kind="render_dokumen_pesanan",outcome="gagal"} 5`
  dan `grahita_jobs_current{state="retryable"}`) — risiko residu F1-18 terukur;
- `/healthz` ok, `/readyz` jujur (`gotenberg`/`storage` = "tidak dikonfigurasi",
  migrasi versi 7);
- suite backend penuh `-race -p 1` pada `grahita_f122r4` (PostgreSQL nyata):
  **`SUITE_EXIT=0`, 27 paket ok, 0 FAIL, 0 SKIP** (`internal/httpapi` 721,1 s);
- gate lint CI **masih merah** pada HEAD ini: `golangci-lint v2.14.0` → 19 temuan
  (13 lama + 6 baru dari kartu lain), lihat `BUKTI-GOLANGCI-LINT-HEAD2.txt`.

### 9. Putaran 3 — perbaikan temuan QA putaran 1 (commit f2b40fc)

QA putaran 1 (lensa artifact + eksekusi) meminta dua perubahan; keduanya
dikerjakan di atas HEAD `0eb0120` dan diverifikasi ulang pada pohon BERSIH.

**1. (PEMBLOKIR) `scripts/check-secrets.sh` tidak dapat mendeteksi pola
berkeyakinan tinggi — gerbang "tanpa secret repo" selalu hijau.**

- Direproduksi SEBELUM perbaikan (`0eb0120`): berkas **terlacak** berisi token
  `ghp_…` + kunci privat RSA → skrip tetap `exit 0` "tidak ada rahasia
  terdeteksi". Sebabnya persis seperti temuan QA: pola dimulai `-----BEGIN`
  dan diteruskan ke grep tanpa `-e`, sehingga GNU grep 3.11 memparsenya sebagai
  *option* (`unrecognized option`, exit 2); `2>/dev/null` membuang pesannya dan
  `|| true` menelan statusnya → keluaran kosong dibaca sebagai "bersih".
- Perbaikan: `grep -nI -E -e "$pola" --`; status grep **tidak pernah ditelan** —
  galat alat (grep exit > 1, mis. berkas tak terbaca) membuat gerbang MENOLAK
  (`exit 2`, fail closed) alih-alih melaporkan hijau. Status perintah yang perlu
  diperiksa ditulis sebagai daftar `&& … || _st=$?` (dikecualikan dari errexit),
  bukan dengan `set +e`/`set -e`, karena `set -e` yang dinyalakan di dalam
  fungsi membuat `return 1` yang sah justru mematikan skrip (ditemukan dan
  diperbaiki saat menguji perbaikan ini).
- Kontrol negatif/positif TETAP: subperintah baru `--uji-diri` — 15 kasus
  (11 pola wajib TERDETEKSI: kunci privat RSA/EC/OPENSSH/PGP, `AKIA…`,
  `aws_secret_access_key`, `sk-ant-`, `ghp_`, `xox`, `SERVICE_ACCOUNT_JSON`,
  DSN literal; 2 contoh interpolasi wajib bersih; berkas tak terbaca wajib
  `exit 2`). Ia dijalankan job CI `rahasia` **sebelum** pemindaian, oleh gerbang
  pra-rilis `release.yml`, dan tersedia sebagai `make uji-rahasia`.
- Bukti: `BUKTI-KONTROL-NEGATIF-RAHASIA.txt` (sebelum/sesudah + 6 kontrol);
  log mentah `f123_before.log` / `f123_after.log` dilampirkan ke kartu.
- Bukti tambahan §2 [7] pada berkas itu: gerbang juga MENYALA pada berkas
  TERLACAK BARU — dokumen bukti putaran 3 sempat memuat baris contoh DSN dan
  gerbang langsung `exit 1`, lalu dilinearkan (masker) dan hijau kembali.
- Catatan implementasi: berkas skrip ikut dipindai gerbangnya sendiri, sehingga
  contoh uji ditulis dengan penanda `*` yang dihapus saat uji berjalan — tidak
  ada pengecualian berkas baru pada gerbang (tidak ada lubang baru).

**2. (NON-PEMBLOKIR) label bukti menyesatkan.** Header `BUKTI-METRIK-HEAD2.txt`
kini menyatakan 401 diukur TANPA basic-auth dan 404 hanya terukur DENGAN
basic-auth; dua butir "`/metrics` … 404" di HASIL.md §7 dan §8 diselaraskan
dengan syarat pengukuran itu.

**Verifikasi ulang pada pohon BERSIH `f2b40fc`** (`BUKTI-GERBANG-HEAD3.txt`):
`gofmt -l` kosong; `go vet`, `go build ./...`, `go build ./cmd/server` exit 0;
gerbang migrasi kompatibel mundur exit 0; `check-secrets.sh` exit 0 dan
`--uji-diri` LULUS 15/15; `actionlint` (ci.yml + release.yml) exit 0;
`promtool check config` + `check rules --lint=all` (12 aturan) + **simulasi
alarm** `promtool test rules` `SUCCESS`; `caddy validate` prod valid, staging
TANPA hash **MENOLAK** (fail closed) dan dengan hash valid; `docker compose
config -q` overlay staging/observability **MENOLAK tanpa env** (fail closed);
uji unit observability + arch `ok`.

Suite backend penuh pada PostgreSQL 16.15 nyata (`grahita_f123suite`,
`-race -p 1 -count=1 -timeout 45m ./...`): **`SUITE_EXIT=0`, 27 paket ok,
0 FAIL, 0 SKIP** (`internal/httpapi` 710,9 s; 19:46:03 → 20:00:02 UTC) —
`BUKTI-SUITE-BACKEND-HEAD3.txt`. Perubahan putaran 3 hanya menyentuh
skrip/CI/Makefile/docs, sehingga kode Go yang diuji identik dengan kandidat
`f2b40fc`.

Gate lint CI pada HEAD ini **masih MERAH** karena 19 temuan `golangci-lint`
pada kode kartu LAIN (kartu **t_cf36bd3d**) — tidak satu pun dari berkas F1-22,
gate tidak dilonggarkan; keadaannya tidak berubah dari putaran 2
(`BUKTI-GOLANGCI-LINT-HEAD2.txt`).

## Batas yang TIDAK diklaim

1. **Tidak ada deployment nyata.** Tidak ada VPS/DNS/kredensial di sesi ini, dan
   daemon Docker tidak dapat diakses (`permission denied` pada
   `/var/run/docker.sock`), sehingga `docker compose up`, Caddy produksi,
   Prometheus/Grafana/Alertmanager **tidak pernah dijalankan**. Yang dibuktikan
   adalah validitas konfigurasi (biner resmi) dan perilaku tepi+API+DB pada
   replika lokal.
2. **Alur unduh dokumen belum terbukti** (butuh Gotenberg + object storage
   nyata). Uji asap berhenti dengan exit 2, bukan "sukses".
3. **Integrasi nyata adalah gerbang T5** (t_fab8ae74): DNS kedua host, kredensial
   Drive/MinIO, SMTP Gmail nyata, PITR/RPO/RTO, presigned URL yang dijangkau
   browser, drill korupsi arsip `.age`.
4. **PG18 asli tidak tersedia** (PG 16.15 + `scripts/pg-dev-shim.sql`), dan
   `golangci-lint` tidak terpasang di sesi ini — dua gerbang itu hanya
   dijalankan di CI.
5. **Alarm belum dikirim ke email.** Aturannya lolos
   `promtool check rules --lint=all` **dan** dibuktikan MENYALA oleh uji satuan
   `promtool test rules` (`prometheus/tests/grahita-alerts_test.yml`, dijalankan
   juga oleh job CI `observability`), dan metrik sumbernya terbukti hidup —
   tetapi pengiriman alarm ke email end-to-end baru terbukti saat Prometheus +
   Alertmanager + SMTP nyata berjalan (T5). Prosedur simulasi manual pada host
   ada di `infra/observability/README.md`.
6. **Grafana/Loki/Alertmanager belum berjalan sebagai container.** Berkas
   konfigurasinya divalidasi dengan biner resmi (loki/promtail/promtool/amtool)
   dan `docker compose config`, tetapi render dasbor, penyimpanan Loki, dan
   pengiriman alarm end-to-end baru terbukti saat stack dijalankan di host
   (T5).
7. **Sudah dicatat di `infra/observability/README.md`**:
   `grahita_backup_last_run_*` tidak muncul selama `backup_runs` belum punya
   baris; alarm `GrahitaCadanganBasi` menutup celah itu dengan `absent(...)`.
8. **`scripts/check-secrets.sh` bukan pemindai rahasia penuh.** Ia bekerja pada
   daftar pola berkeyakinan tinggi, melewati isi berkas biner (`grep -I`), dan
   (seperti sebelum perbaikan putaran 3) mengecualikan baris yang memuat
   interpolasi `${...}` sebagai contoh konfigurasi — blind spot yang disadari
   dan didokumentasikan di `BUKTI-KONTROL-NEGATIF-RAHASIA.txt` §4. Yang
   dibuktikan: mesin pemindainya benar-benar dapat MERAH (kontrol negatif
   berkas terlacak berisi token/kunci privat → exit 1) dan MENOLAK saat alatnya
   gagal (exit 2, fail closed) — bukan bahwa repo bebas dari segala bentuk
   rahasia.

## Kewajiban gerbang rilis (dibawa dari catatan CEO/QA — bukan diklaim selesai)

Catatan-catatan berikut menyentuh kode atau layanan di luar lingkup F1-22 dan
harus ditutup sebelum go-live. F1-22 hanya menyediakan alat pantau/gerbangnya:

| # | Kewajiban | Pemilik | Status di F1-22 |
|---|---|---|---|
| 1 | Rute `/gaya` (galeri komponen) ikut build produksi & dapat diakses tanpa sesi | swe (kartu UI) | belum diubah; CSP ketat + `X-Robots-Tag` staging tidak menutupnya |
| 2 | `httpx.ClientIP` memakai entri kiri `X-Forwarded-For` (per-IP limiter bisa dielakkan) | swe (platform) | belum diubah; perlu `trusted_proxies`/overwrite XFF + uji spoof |
| 3 | Retensi 90 hari `email_outbox` status `sent` (Database.md §7) belum punya job pembersih | swe (F1-16 lanjutan) | alarm email tertahan/gagal sudah ada; pembersih belum |
| 4 | Id non-UUID → 500 pada beberapa endpoint (pola lintas modul) | swe | alarm 5xx akan menangkapnya; validasi batas HTTP belum |
| 5 | Migrasi maju 0005+ untuk predikat `loginable` superadmin & TOCTOU email lintas tabel | swe (F1-05 lanjutan) | gerbang migrasi kompatibel mundur sudah aktif |
| 6 | Aset logo final kedua portal (bukan placeholder/CDN/cache) | swe/T5 | belum diverifikasi |
| 7 | Cadangan Drive/MinIO nyata, WAL `.age` tidak disalin polos, drill korupsi, PITR/RPO/RTO | T5 | alarm cadangan & runbook sudah ada |
| 8 | Golangci-lint & PostgreSQL 18 dijalankan nyata | CI/T5 | gerbang CI sudah memuat keduanya |
| 9 | **Gate lint CI sedang MERAH di main**: `golangci-lint v2.14.0 run ./...` menemukan 13 temuan pada kode/uji kartu lain (pada HEAD terintegrasi **2707967** jumlahnya menjadi **19** — 13 lama + 6 baru dari `internal/payment/va/*` dan `internal/tutorial/tutorial.go`; bukti `BUKTI-GOLANGCI-LINT-HEAD2.txt`) (5 errcheck `defer x.Close()` di berkas `_pg_test.go`/`s3_test.go`, 3 ineffassign di `router_proofs_test.go`, 4 staticcheck di `render.go`/`order.go`/`integration_documents_pdf_test.go`, 1 unused di `notification_test.go`). Tidak satu pun berasal dari kode F1-22 | swe | dilaporkan + dibuatkan kartu tindak lanjut **t_cf36bd3d** (diberi komentar pembaruan daftar 19 temuan); gate lint tidak dilonggarkan |

## Cara menjalankan ulang bukti

```sh
# 1. validasi konfigurasi (butuh caddy, promtool, amtool, docker compose)
caddy validate --config infra/caddy/Caddyfile.prod --adapter caddyfile
promtool check config infra/observability/prometheus/prometheus.yml
promtool check rules  infra/observability/prometheus/rules/grahita-alerts.yml
docker compose -f docker-compose.yml -f docker-compose.observability.yml config -q

# 2. gerbang
./scripts/check-migration-kompatibel.sh
./scripts/check-secrets.sh
./scripts/check-secrets.sh --uji-diri   # kendali diri gerbang rahasia (15 kasus)

# 3. uji asap dua host (staging nyata: tanpa SMOKE_CURL_OPTS)
CUSTOMER_BASE=https://staging.grahita-branch.tiusclaw.com \
ADMIN_BASE=https://staging.grahita-admin.tiusclaw.com \
SMOKE_EMAIL=... SMOKE_PASSWORD=... \
SMOKE_ADMIN_EMAIL=... SMOKE_ADMIN_PASSWORD=... \
./scripts/smoke-staging.sh
```
