# T5-RUNNER — Akses toolchain Go untuk gerbang test/lint

Kartu: **t_ac58a28e — T5-RUNNER Perbaiki akses Go toolchain untuk gerbang test/lint**
(kartu induk: T5-ulang `t_e36576b6`; kartu anak: T5-ulang2 `t_55ee4005`).

Dokumen acuan: PRD.md §9 butir 11, §10 F1; Architecture.md §1 (Go 1.27), §6
(keamanan), §7 (backup & alert), §9 CI/CD (`lint (golangci-lint, eslint,
tsc --noEmit)` → `test (go test ./...)`); SystemDesign.md §9 Pengujian;
Database.md §1 & §4.7; `Makefile` target `test` (baris 53–54) dan `ci-lokal`.

Halaman ini mencatat penyebab kegagalan gerbang, keputusan perbaikan, bukti
perintah+hasil nyata, dan batas yang **tidak** diklaim. Bukti mentah lengkap:
`docs/design/t5-runner/BUKTI-T5-RUNNER.txt`.

---

## 1. Masalah yang direproduksi (commit 34b0e94efcfa85eb4f8ac7546fb649f58768e653)

| Perintah (dari root repo) | Hasil sebelum perbaikan |
|---|---|
| `make test` | `/bin/sh: 1: go: not found` → `make: *** [Makefile:54: test] Error 127` (make keluar 2, `sh` keluar 127) |
| `cd api && golangci-lint run ./...` | exit 3, `context loading failed: ... go command required, not found: exec: "go": executable file not found in $PATH` |

Diagnosis: **kegagalan provisioning/PATH runner**, bukan cacat kode Go. Runner
tidak memiliki `go` pada PATH, sementara toolchain tersedia di
`/home/hermes/toolchain/go/bin/go` (Go 1.27.1) dan `golangci-lint` v2.14.0 di
`/home/hermes/bin/golangci-lint`. Tidak ada kode aplikasi yang diubah oleh
kartu ini.

## 2. Lingkup yang dikerjakan

| Berkas | Isi |
|---|---|
| `scripts/go-env.sh` (baru) | pemilih toolchain: Go >= 1.27 & `golangci-lint`; mode `--laporan`, `--export`, `--go`, `--golangci-lint`, `--cek`, `--uji-diri` |
| `scripts/runner-provision.sh` (baru) | provisioning runner tanpa sudo & idempotent: tautkan `go`/`gofmt` (dan `golangci-lint` bila perlu) ke direktori yang sudah ada di PATH (`~/bin`); `--cek`/`--hapus`/`--paksa` |
| `Makefile` | `GO`/`GOLANGCI_LINT` dari pemilih toolchain + `export PATH` untuk seluruh resep; target Go memakai `$(GO)`; target baru `lint`, `toolchain-cek`, `lint-diri`; `ci-lokal` kini menyertakan `lint` |
| `README.md` | bagian “Toolchain Go pada runner/pengembang” (invocation stabil untuk tester) + `make lint`/`make toolchain-cek` di daftar perintah |
| `docs/design/t5-runner/` | `HASIL.md` (halaman ini) + `BUKTI-T5-RUNNER.txt` (bukti mentah) |

## 3. Keputusan desain

1. **Pemilih toolchain ada di repo, bukan di PATH interaktif sesaat.** Urutan
   kandidat ditentukan: `GRAHITA_GO` (otoritatif) → `$GOROOT/bin/go` →
   `$HOME/toolchain/go/bin/go` → `$HOME/sdk/go*/bin/go` → `/usr/local/go/bin`,
   `/usr/lib/go-1.27/bin`, `/snap/bin/go`, `/opt/go/bin` → `go` di PATH.
   Kandidat pertama yang **memadai (>= 1.27)** dipakai; kandidat versi lebih
   rendah **ditolak**, bukan diam-diam dipakai. Tanpa kandidat memadai, skrip
   keluar != 0 dengan daftar kandidat yang dinilai dan saran tindakan.
2. **Makefile tidak lagi mengandalkan `go` di PATH.** Nilai `GO` diisi hasil
   pemilih, dan direktori toolchain ditambahkan lewat `export PATH := ...`
   sehingga perkakas yang memanggil `go` dari dalam resep (`golangci-lint`,
   `gofmt`) juga menemukannya. `make test GO=/jalur/ke/go` tetap bisa memaksa.
3. **`make lint` ditambahkan dan masuk `ci-lokal`.** Architecture.md §9
   menempatkan `golangci-lint` sebagai gerbang lint; `make ci-lokal` yang
   mengklaim “seluruh gerbang CI” kini benar-benar menyertakannya, memakai jalur
   biner eksplisit (`GOLANGCI_LINT`).
4. **Tidak ada pelonggaran linter.** `.golangci.yml` tidak disentuh; hasil pada
   commit ini `0 issues.` (sama seperti perbaikan 19 temuan pada `t_cf36bd3d`).
5. **Batas antara perbaikan repo dan provisioning host dipisah tegas.**
   Perbaikan repo cukup untuk seluruh target `make *` (dibuktikan dengan PATH
   minimal `/usr/bin:/bin`, tanpa toolchain sama sekali). Celah yang tersisa
   hanyalah perintah *polos* (`go ...`, `cd api && golangci-lint run ./...`)
   yang dijalankan langsung dari shell tester; untuk itu disediakan
   `scripts/runner-provision.sh` (tautan `~/bin/go`, `~/bin/gofmt`) — tanpa
   sudo, idempotent, dapat dihapus (`--hapus`). Skrip ini **tidak** dipanggil
   otomatis oleh Makefile/CI.
6. **Kendali diri, bukan klaim.** `./scripts/go-env.sh --uji-diri` menjalankan
   15 kendali positif+negatif: kandidat 1.27.1/1.28.0 diterima, 1.26.5 ditolak,
   direktori tanpa biner ditolak, `GRAHITA_GO` salah versi ditolak, `--export`
   benar-benar menambahkan `go`+`golangci-lint` ke PATH, `--cek` gagal saat
   `golangci-lint` hilang, dan integrasi Makefile (`make -n test`, `make -n lint`,
   `make toolchain-cek`) memakai jalur absolut hasil pemilih tanpa PATH. Mode ini
   mengikuti pola `./scripts/check-secrets.sh --uji-diri`.
7. **Tanpa rahasia.** Skrip hanya membaca jalur biner dan versinya; tidak ada
   kredensial, token, atau DSN pada kode, dokumentasi, maupun bukti.

## 4. Bukti verifikasi pada commit ini

Ringkasan (detail + keluaran mentah di `BUKTI-T5-RUNNER.txt`):

| # | Perintah | Hasil |
|---|---|---|
| 1 | `PATH=/usr/bin:/bin:/usr/sbin:/sbin make test` (PATH **tanpa** toolchain) | exit 0; resep tercetak `cd api && /home/hermes/toolchain/go/bin/go test ./...` |
| 2 | `make toolchain-cek` | exit 0; `go version go1.27.1 linux/amd64`, `golangci-lint has version 2.14.0 built with go1.27.0` |
| 3 | `make test` | exit 0 |
| 4 | `make lint` | exit 0, `0 issues.` |
| 5 | `make vet`, `make build` | exit 0, exit 0 |
| 6 | `cd api && eval "$(../scripts/go-env.sh --export)" && golangci-lint run ./...` | exit 0, `0 issues.` |
| 7 | `cd api && golangci-lint run ./...` setelah provisioning runner | exit 0, `0 issues.` (perintah polos ala tester) |
| 8 | `./scripts/go-env.sh --uji-diri` | exit 0, 15/15 lulus |
| 9 | `./scripts/runner-provision.sh` (dua kali, uji idempotensi) | exit 0/0, tidak menimpa berkas yang sudah ada |
| 10 | `make secrets`, `make migrasi-cek` | exit 0, exit 0 (skrip/gate repo tetap hijau) |
| 11 | Kendali negatif: `make -f <Makefile HEAD> test` pada PATH tanpa toolchain; `golangci-lint` tanpa `go` di PATH | exit 2 (di dalam `sh`: 127) dan exit 3 — **kegagalan lama dapat direproduksi persis**, jadi perbaikan bukan efek mesin lain |
| 12 | HTTPS dua host tanpa `-k`: 3× `curl --head` per host | 6/6 `200` HTTP/2; `verify return code: 0 (ok)`, TLSv1.3, SAN dua host, issuer Let's Encrypt YE1 (berlaku 11 Okt 2026 – 9 Jan 2027) |
| 13 | Jalur negatif publik (`/api/v1/tidak-ada`, `/metrics`, `/internal/*`, `/debug/pprof/`) pada kedua host | 404 semua |

Perintah dasar yang dicatat tester pada `t_e36576b6` (`make test`,
`cd api && golangci-lint run ./...`) kini keduanya exit 0 **pada commit yang
sama** dan tanpa perubahan aturan linter.

## 5. Invocation stabil untuk tester

```bash
cd /home/hermes/projects/grahita-website
git rev-parse HEAD                      # commit yang diuji
make toolchain-cek                      # jalur + versi go/golangci-lint yang dipakai resep
make test                               # exit 0 (tidak perlu set PATH)
make lint                               # exit 0, 0 issues
( cd api && eval "$(../scripts/go-env.sh --export)" && golangci-lint run ./... )  # perintah polos
./scripts/go-env.sh --uji-diri          # kendali positif+negatif pemilih
./scripts/runner-provision.sh --cek     # keadaan tautan ~/bin (provisioning host)
```

Bila `go` tidak ada di PATH sama sekali: `eval "$(./scripts/go-env.sh --export)"`
(dari root repo) atau `GRAHITA_GO=/jalur/ke/go make test`. Bila runner baru
belum diprovisikan: `./scripts/runner-provision.sh` (tanpa sudo).

## 6. Yang TIDAK diklaim (batas)

1. **Verdict GAGAL historis T5-ulang `t_e36576b6` tidak diubah.** Kartu ini hanya
   menutup penyebab kegagalan gerbang test/lint (akses toolchain).
2. **Tidak ada klaim rilis siap.** Backup harian + restore Google Drive/S3,
   physical base backup + WAL/PITR PostgreSQL 18, SMTP/alert nyata, dan smoke
   berakun (login → submit → verify → download, peran nyata, CSRF berbasis akun)
   tetap **TERHALANG** tanpa kredensial dan infrastruktur uji yang sah — sama
   seperti laporan `t_e36576b6`. Validasi TLS dua host juga bukan approval rilis.
3. **Provisioning host bersifat lokal runner.** Tautan `~/bin/go` hanya
   menguntungkan mesin ini; sifat *repeatable* di mesin lain bersandar pada
   `scripts/go-env.sh` (repo) + `GRAHITA_GO`/`$HOME/toolchain/go`. Karena dampak
   host-lah yang membuat perintah polos bekerja, keduanya dibuktikan terpisah
   pada bukti #1/#6 (repo, PATH minimal) dan #7 (host, setelah provisioning).
4. **Angka `go1.27.1`/`golangci-lint v2.14.0`** adalah toolchain runner ini;
   CI GitHub memakai `actions/setup-go` (`GO_VERSION: "1.27"`) dan memasang
   `golangci-lint v2.14.0` sendiri — pemilih toolchain tidak mengubah perilaku CI.
5. **Tidak ada perubahan pada aturan lint, kode aplikasi, atau port tenant lain.**
   `git status` bersih dari artefak (`bin/` hasil `make build` dihapus setelah bukti);
   diff terlacak hanya `Makefile` dan `README.md`.
