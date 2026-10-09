# Deployment Grahita (F1-22, Architecture.md §5 & §9)

Dua lingkungan, dua host publik:

| Lingkungan | Host | Overlay Compose |
|---|---|---|
| staging | `staging.grahita-branch.tiusclaw.com`, `staging.grahita-admin.tiusclaw.com` | `docker-compose.staging.yml` + `docker-compose.observability.yml` |
| produksi | `grahita-branch.tiusclaw.com`, `grahita-admin.tiusclaw.com` | `docker-compose.prod.yml` + `docker-compose.observability.yml` |

API selalu **same-origin** `/api/v1` di kedua host; cookie sesi host-only.

## Alur rilis

`.github/workflows/release.yml`:

1. `gerbang` — tidak ada rahasia di repo, migrasi kompatibel mundur.
2. `terbitkan` — build & push image `api` (dipakai api **dan** worker) dan `web`
   ke GHCR, di-tag dengan commit SHA.
3. `staging` — **otomatis**: `infra/deploy/deploy.sh`, lalu `scripts/smoke-staging.sh`
   (login → submit → verify → download). Job gagal bila akun uji belum ada di
   Secrets, supaya staging tidak pernah "hijau" tanpa diuji.
4. `produksi` — **butuh persetujuan manual**: job memakai GitHub Environment
   `production` dengan protection rule *Required reviewers*. Tidak ada cara
   melewatinya dari dalam workflow.

## Rahasia yang dibutuhkan (GitHub Secrets)

| Nama | Isi |
|---|---|
| `STAGING_SSH_HOST` / `PROD_SSH_HOST` | alamat host |
| `STAGING_SSH_USER` / `PROD_SSH_USER` | pengguna SSH |
| `STAGING_SSH_KEY` / `PROD_SSH_KEY` | kunci privat SSH (deploy key khusus) |
| `SMOKE_EMAIL` / `SMOKE_PASSWORD` | akun cabang uji |
| `SMOKE_ADMIN_EMAIL` / `SMOKE_ADMIN_PASSWORD` | akun admin uji |
| `STAGING_BASIC_AUTH` | `user:kata-sandi` untuk basic-auth staging |

Variables: `STAGING_HOST`, `STAGING_ADMIN_HOST`, `CUSTOMER_HOST`, `ADMIN_HOST`,
`STAGING_REMOTE_DIR`, `PROD_REMOTE_DIR`.

Tidak satu pun nilai di atas boleh dituliskan di repo — gate
`scripts/check-secrets.sh` memeriksa berkas yang dilacak git.

## Persiapan host (sekali per host)

```sh
# 1. Docker Engine + Compose plugin
# 2. checkout repo di REMOTE_DIR (deploy key read-only)
sudo git clone git@github.com:ORG/REPO.git /srv/grahita

# 3. berkas environment host (di luar repo, izin 600)
sudo install -m 600 /dev/null /srv/grahita/.env
#    isi: POSTGRES_PASSWORD, SMTP_*, S3_*, BACKUP_*, SENTRY_DSN,
#        GRAFANA_PASSWORD, ALERT_*, STAGING_BASIC_AUTH_HASH, dll.

# 4. DNS A/AAAA kedua host -> IP VPS; Caddy menerbitkan TLS otomatis
# 5. firewall: hanya 80/443 terbuka; port API/DB/MinIO/Grafana/Alertmanager
#    tidak dipublikasikan (lihat overlay Compose masing-masing)
```

## Rollback

Jalankan `infra/deploy/deploy.sh` dengan `IMAGE_TAG` = commit sebelumnya.
Migrasi yang naik wajib kompatibel mundur (dijaga
`scripts/check-migration-kompatibel.sh` di CI), jadi image lama tetap dapat
melayani skema baru. Bila memang perlu turun skema, gunakan `--mode=migrate-down`
secara sadar dan terukur (bukan langkah otomatis).

## Backup

Operasional cadangan terenkripsi (age → Drive/rclone, WAL, rekonsiliasi objek
yatim) ada di `infra/backup/README.md`. Alarm cadangan ada di
`infra/observability/README.md`.
