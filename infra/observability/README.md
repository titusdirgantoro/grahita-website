# Observability Grahita (Architecture.md §8)

Stack: **Prometheus** (metrik) + **Alertmanager** (alarm) + **Loki/Promtail**
(log) + **Grafana** (dasbor), dijalankan sebagai overlay Compose di atas
`docker-compose.yml`.

Sumber metrik:

| Target | Endpoint | Isi |
|---|---|---|
| `api:8080/metrics` | proses API | permintaan HTTP (rute/metode/status), latensi, goroutine, pesanan per status, antrean email, status cadangan, rasio disk |
| `worker:9091/metrics` | proses worker | hasil & durasi pekerjaan River, state antrean, metrik proses |
| `node-exporter:9100` | host | CPU/memori/disk tingkat host |

Endpoint `/metrics` **tidak pernah dipublikasikan**: Caddy hanya mem-proxy
`/api/*` dan port API tidak dipetakan ke host pada overlay produksi
(`docker-compose.prod.yml: ports: !override []`). Grafana/Alertmanager juga
tanpa port host — akses lewat terowongan SSH:

```sh
ssh -L 3000:127.0.0.1:3000 -L 9093:127.0.0.1:9093 operator@host
```

## Menjalankan

```sh
export APP_ENV=production
export GRAFANA_PASSWORD='...'            # wajib, tanpa nilai bawaan
export ALERT_SMTP_SMARTHOST='smtp.gmail.com:587'   # wajib
export ALERT_SMTP_FROM='...'             # wajib (alamat pengirim alarm)
export ALERT_SMTP_USER='...' ALERT_SMTP_PASS='...' # wajib
export ALERT_EMAIL_TO='...'              # wajib: pengelola infrastruktur
export SENTRY_DSN='...'                  # opsional; kosong = no-op

docker compose \
  -f docker-compose.yml \
  -f docker-compose.prod.yml \
  -f docker-compose.observability.yml \
  up -d
```

Alertmanager **tidak** mendukung substitusi environment di berkas
konfigurasinya sendiri, jadi `infra/observability/alertmanager/mulai.sh`
merender `alertmanager.tmpl.yml` dari environment sebelum biner dijalankan, dan
**menolak start** bila ada variabel wajib yang kosong. Alertmanager yang
"berjalan" tanpa tujuan email akan lebih menyesatkan daripada container yang
menolak naik. Tidak ada alamat maupun kata sandi yang ditulis di repo (gate
`scripts/check-secrets.sh`).

## Alarm (Architecture.md §8)

| Alarm | Syarat | Artinya / tindakan |
|---|---|---|
| `GrahitaAPILajuErrorTINGGI` | rasio 5xx > 2% selama 5 menit | buka dasbor *API & HTTP*; telusuri Sentry + log Loki `{compose_service="api"}` |
| `GrahitaAPITidakTerjangkau` / `GrahitaWorkerTidakTerjangkau` | `up == 0` 2–5 menit | container mati atau loop restart; `docker compose ps` + log |
| `GrahitaJobSeringGagal` | > 3 kegagalan job / 10 menit | biasanya Gotenberg/S3 atau SMTP belum siap |
| `GrahitaJobGagalPermanen` | `jobs_current{state="discarded"} > 0` 10 menit | **risiko F1-18**: pesanan bisa Done tanpa dokumen/email → jalankan prosedur rekonsiliasi di bawah |
| `GrahitaAntreanJobMenumpuk` | `jobs_current{state="available"} > 50` 15 menit | worker lambat/berhenti; cek `WORKER_CONCURRENCY` |
| `GrahitaEmailTertahan` | email tertua > 30 menit | kredensial SMTP, jaringan, atau worker |
| `GrahitaEmailGagalPermanen` | `outbox_messages{status="failed"} > 0` 30 menit | kirim ulang dari riwayat email admin (chip "perlu tindakan") |
| `GrahitaCadanganGagal` | cadangan terakhir gagal 5 menit | lihat `backup_runs.pesan` di halaman Pengaturan; kunci age/rclone/ruang disk |
| `GrahitaCadanganBasi` | tanpa cadangan terlihat > 26 jam | jadwal cadangan tidak berjalan (02:00 WIB) |
| `GrahitaDiskHampirPenuh` / `GrahitaDiskKritis` | rasio > 80% / 92% | bersihkan cadangan lama & log, atau perbesar volume |

Alertmanager menahan alarm turunan ketika API/worker mati (inhibit rules) supaya
yang muncul adalah penyebabnya, bukan bising.

## Simulasi alarm (uji tanpa merusak produksi)

Verifikasi bahwa jalur alarm benar-benar bekerja (bukan sekadar berkas aturan):

**a. Simulasi otomatis (tanpa Prometheus/Docker, dipakai CI).**
`infra/observability/prometheus/tests/grahita-alerts_test.yml` menyuntikkan deret
waktu sintetis lalu membuktikan tiap alarm MENYALA pada keadaan buruk dan TIDAK
menyala pada keadaan sehat:

```sh
promtool check rules --lint=all infra/observability/prometheus/rules/grahita-alerts.yml
cd infra/observability/prometheus/tests && promtool test rules grahita-alerts_test.yml
```

Cakupan uji: laju 5xx > 2% (nyala) vs sehat (tidak), `up == 0`, job gagal > 3
(dan kontrol negatif satu kegagalan), `jobs_current{state="discarded"}`,
email tertahan > 30 menit, cadangan gagal, cadangan tak pernah terlihat
(`absent`), disk > 80% / > 92%. Job CI `observability` menjalankan ketiga
perintah itu pada setiap push/PR. Berkas uji sengaja berada di `tests/`, bukan
`rules/`, karena Prometheus memuat `rules/*.yml`.

**b. Simulasi manual pada host (butuh container berjalan):**

```sh
# 1. API tidak terjangkau: hentikan container api sebentar
docker compose stop api            # tunggu ~2 menit → GrahitaAPITidakTerjangkau
docker compose start api

# 2. job gagal permanen (efek nyata saat Gotenberg/S3 belum siap):
#    matikan Gotenberg lalu buat pesanan uji sampai job render `discarded`.
docker compose stop gotenberg

# 3. email tertahan: hentikan worker, biarkan email_outbox menumpuk
docker compose stop worker         # GrahitaEmailTertahan setelah 30 menit
docker compose start worker

# 4. disk > 80%: cukup andalkan metrik grahita_disk_used_ratio bila volume
#    produksi memang di atas ambang; jangan memenuhi disk untuk menguji.
```

Catatan: alarm diberi `for:` beberapa menit; jangan menunggu alarm pada
percobaan singkat kecuali ambang waktu diubah sementara di berkas aturan.

## Rekonsiliasi pekerjaan gagal permanen (risiko F1-18)

Konteks: email/dokumen diantre di luar transaksi verifikasi. Bila job River
berakhir `discarded`, pesanan tetap `done` tanpa dokumen/email. Dashboard
`GrahitaJobGagalPermanen` menandai keadaan ini.

Prosedur yang aman (tanpa menggandakan dokumen/email):

1. **Daftar job gagal**
   ```sh
   docker compose exec postgres psql -U grahita -d grahita -c \
     "SELECT id, kind, args, attempt, errors[1] AS galat_terakhir, finalized_at
        FROM river_job WHERE state='discarded' ORDER BY finalized_at DESC;"
   ```
2. **Kenali penyebabnya** dari `errors[1]`. Penyebab tersering: Gotenberg mati,
   object storage tidak terjangkau, atau SMTP salah kredensial.
3. **Perbaiki penyebabnya lebih dulu** (Gotenberg/S3/SMTP). Mengulang job sebelum
   penyebabnya hilang hanya akan `discarded` lagi.
4. **Pulihkan jalur idempoten**, bukan dengan membuat pesanan baru:
   * Dokumen: minta render ulang melalui portal admin — tombol "Buat/segarkan
     dokumen" pada pesanan memanggil `POST /api/v1/admin/orders/{id}/documents`
     yang **idempoten** (aman dipanggil berulang; tidak menggandakan baris
     `documents` karena kunci `(order_id, jenis, versi)`).
   * Email: buka riwayat email pesanan dan gunakan **kirim ulang** per jenis
     (`POST /api/v1/admin/orders/{id}/emails/{kind}/resend`). Pengiriman
     bersifat *at-least-once*: satu-dua duplikat langka mungkin terjadi dan itu
     diterima (disetujui QA F1-16) — dokumennya sendiri tidak pernah ganda.
5. **Beri job baru** hanya bila item yang hilang memang tidak akan pernah
   dibuat jalur di atas, dan pastikan idempotensi penyimpanan tetap dijaga.
6. **Buktikan pemulihan**: `grahita_jobs_current{state="discarded"}` kembali ke
   0, dokumen muncul di `GET /api/v1/orders/{id}/documents`, dan pesanan tetap
   satu (tidak ada pesanan/berkas ganda).

Sumber kebenaran keadaan pekerjaan tetap tabel `river_job`; metrik hanya
mengangkat keadaan itu agar terlihat dan bisa dialarmkan.

## Log (Loki)

`slog` JSON ke stdout container; Promtail mengumpulkannya melalui Docker
service discovery. Label yang tersedia: `compose_service`, `compose_project`,
`container`, `stream`, dan `level` (diangkat dari medan JSON).

Contoh kueri LogQL:

```
{compose_service="api"} |= "panic"
{compose_service="api"} | json | level="ERROR"
{compose_service="worker"} |= "cadangan gagal"
```

Retensi Loki 30 hari (`limits_config.retention_period`). Log tidak memuat kata
sandi/token: hanya penanda non-rahasia (`request_id`, peran, rute).

## Sentry

`SENTRY_DSN` dikosongkan berarti pelaporan dimatikan total (no-op). Bila diisi,
panic pada handler API dikirim ke Sentry melalui `observability.TangkapPanic`
(middleware `httpx.Recoverer`). `SENTRY_TRACES_SAMPLE_RATE` > 0 mengaktifkan
tracing sesuai laju sampel. DSN adalah rahasia: hanya dari environment.

## Verifikasi berkas konfigurasi

```sh
promtool check config infra/observability/prometheus/prometheus.yml
promtool check rules  infra/observability/prometheus/rules/grahita-alerts.yml

# Alertmanager: template dirender lebih dulu (persis jalur yang dipakai container)
ALERTMANAGER_RENDER_ONLY=1 \
ALERTMANAGER_TEMPLATE=infra/observability/alertmanager/alertmanager.tmpl.yml \
ALERTMANAGER_RENDERED=/tmp/alertmanager.yml \
ALERT_SMTP_SMARTHOST=smtp.gmail.com:587 ALERT_SMTP_FROM=a@b.test \
ALERT_SMTP_USER=u ALERT_SMTP_PASS=p ALERT_EMAIL_TO=c@d.test \
  sh infra/observability/alertmanager/mulai.sh
amtool check-config /tmp/alertmanager.yml
```
