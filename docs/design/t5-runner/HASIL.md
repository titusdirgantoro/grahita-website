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

Temuan QA putaran 2 (lensa Eksekusi) atas perbaikan pertama — **cacat fail-open**:
gerbang yang tadinya gagal keras 127 bisa **hijau tanpa menjalankan satu tes pun**.

| Perintah (dari root repo) | Hasil sebelum putaran 3 |
|---|---|
| `env -i HOME=<kosong> PATH=/usr/bin:/bin make test` | **exit 0**, resep tercetak `cd api &&  test ./...`, **0 paket diuji** |
| `GRAHITA_GO=/salah/jalur/go make test` (jalur override yang didokumentasikan) | **exit 0**, **0 paket diuji** |

Akar: `GO ?= $(shell scripts/go-env.sh --go)` membuang exit != 0 pemilih, sehingga
`$(GO)` kosong dan `cd api && $(GO) test ./...` merosot menjadi perintah shell
builtin `test ./...` yang selalu true; target lain (vet/build/lint/toolchain-cek)
kebetulan gagal keras 127. Ditutup pada putaran 3 — lihat §3 butir 8 dan bukti
`BUKTI-T5-RUNNER.txt` bagian [8] butir 9a–9d.

## 2. Lingkup yang dikerjakan

| Berkas | Isi |
|---|---|
| `scripts/go-env.sh` (baru) | pemilih toolchain: Go >= 1.27 & `golangci-lint`; mode `--laporan`, `--export`, `--go`, `--golangci-lint`, `--cek`, `--uji-diri` (kendali positif+negatif, **kini 20 kasus**) |
| `scripts/runner-provision.sh` (baru) | provisioning runner tanpa sudo & idempotent: tautkan `go`/`gofmt` (dan `golangci-lint` bila perlu) ke direktori yang sudah ada di PATH (`~/bin`); `--cek`/`--hapus`/`--paksa` |
| `Makefile` | `GO`/`GOLANGCI_LINT` dari pemilih toolchain (diambil **sekali**, `ifndef` + `:=`) + `export PATH` untuk seluruh resep; **penjaga fail-closed `_wajib_go`/`_wajib_lint`** pada resep `vet`/`test`/`test-integrasi`/`build`/`lint`/`toolchain-cek` (putaran 3); target baru `lint`, `toolchain-cek`, `lint-diri`; `ci-lokal` kini menyertakan `lint` |
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
   **20 kendali positif+negatif** (15 kendali putaran 1–2 + 5 kendali putaran 3):
   kandidat 1.27.1/1.28.0 diterima, 1.26.5 ditolak,
   direktori tanpa biner ditolak, `GRAHITA_GO` salah versi ditolak, `--export`
   benar-benar menambahkan `go`+`golangci-lint` ke PATH, `--cek` gagal saat
   `golangci-lint` hilang, dan integrasi Makefile (`make -n test`, `make -n lint`,
   `make toolchain-cek`) memakai jalur absolut hasil pemilih tanpa PATH.
   Lima kendali putaran 3 adalah yang fail-closed beserta satu kendali positifnya:
   tanpa toolchain → `make test` exit != 0; `GRAHITA_GO` salah → `make test`
   exit != 0; `GO=` dari **command line** → `make test` exit != 0; `GOLANGCI_LINT=`
   dari command line → `make lint` exit != 0; dan kendali **positif** (stub Go
   1.27.1 → exit 0, penjaga tidak memblokir konfigurasi sah). Hasil akhirnya
   “20 lulus, 0 gagal” — direkam di `BUKTI-T5-RUNNER.txt` bagian [8] butir 8 dan
   diulang di [9], lalu diukur ulang pada commit dokumentasi ini di bagian [10].
   Mode ini mengikuti pola `./scripts/check-secrets.sh --uji-diri`.
7. **Tanpa rahasia.** Skrip hanya membaca jalur biner dan versinya; tidak ada
   kredensial, token, atau DSN pada kode, dokumentasi, maupun bukti.
8. **Gerbang `make test` fail-closed (perbaikan putaran 3 atas temuan QA).**
   Sebelumnya `GO ?= $(shell scripts/go-env.sh --go)` membuang exit != 0 pemilih
   toolchain: bila tidak ada kandidat (atau `GRAHITA_GO` salah ketik), `$(GO)`
   menjadi **kosong** dan `cd api && $(GO) test ./...` merosot menjadi perintah
   shell builtin `test ./...` yang **selalu true** — `make test` HIJAU dengan
   **0 tes**, persis kebalikan dari janji kartu (“jangan mengklaim gate hijau”).
   Kini: (a) nilai pemilih diambil **sekali** (`ifndef` + `:=`) sehingga pesan
   resolver tidak lagi tercetak berulang; (b) resep `vet`/`test`/`test-integrasi`/
   `build`/`lint`/`toolchain-cek` melewati penjaga `_wajib_go`/`_wajib_lint`
   sebagai baris pertama — bila `$(GO)`/`$(GOLANGCI_LINT)` kosong, make berhenti
   dengan pesan jelas + exit 2, termasuk bila kosong dari **command line**
   (`make test GO=`).
   **Batas eksplisit (jangan dibaca sebagai jaminan mutlak):** penjaga ini
   menjamin kasus nilai toolchain **tidak teresolusi/kosong**, *bukan* override
   eksplisit dari command line. `make test GO=/jalur/ke/go` dan
   `make lint GOLANGCI_LINT=/jalur/ke/golangci-lint` melewati pemilih toolchain
   (`ifndef` membuat Makefile tidak memanggil `scripts/go-env.sh`), sehingga
   versinya **tidak divalidasi**: bila `$(GO)` menunjuk biner lain yang selalu
   exit 0 (mis. `/usr/bin/true`), `make test` memang HIJAU dengan 0 tes
   (diukur ulang pada `BUKTI-T5-RUNNER.txt` bagian [10]). Jadi yang dijamin
   adalah “gagal bila tidak teresolusi”, bukan “selalu ada tes yang dijalankan”.

## 4. Bukti verifikasi pada commit ini

Commit bukti: **`f336231af0a51f5a5d423bad1a838b82d9d10e47`** (putaran 3 — penjaga
fail-closed) dan **`7a45c7ea1deecbd255072ddfd88c770e334b1b26`** (putaran 1–2 — akses
toolchain). Tabel di bawah merangkum keluaran; seluruh keluaran mentah ada di
`BUKTI-T5-RUNNER.txt` — bagian [1]–[5] direkam pada pohon kerja sebelum commit, dan
bagian **[6] (gerbang) + [7] (HTTPS)** serta **[8] (putaran 3, seluruh blok diulang
pada commit dokumentasi di bagian [9])** adalah blok bukti yang tertaut ke commit;
**[10]** merekam pengukuran ulang pada kartu dokumentasi `t_14e91b3c` — hitungan
kendali `--uji-diri` **20/20** dan batas override `GO=`/`GOLANGCI_LINT=` dari
command line (pohon bersih sebelum & sesudah).

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
| 8 | `./scripts/go-env.sh --uji-diri` (putaran 1–2) | exit 0, 15/15 lulus; **kini 20/20** karena 5 kendali fail-closed putaran 3 — lihat baris 20 dan bagian [8]/[9]/[10] |
| 9 | `./scripts/runner-provision.sh` (dua kali, uji idempotensi) | exit 0/0, tidak menimpa berkas yang sudah ada |
| 10 | `make secrets`, `make migrasi-cek` | exit 0, exit 0 (skrip/gate repo tetap hijau) |
| 11 | Kendali negatif: `make -f <Makefile HEAD> test` pada PATH tanpa toolchain; `golangci-lint` tanpa `go` di PATH | exit 2 (di dalam `sh`: 127) dan exit 3 — **kegagalan lama dapat direproduksi persis**, jadi perbaikan bukan efek mesin lain |
| 12 | HTTPS dua host tanpa `-k`: 3× `curl --head` per host | 6/6 `200` HTTP/2; `verify return code: 0 (ok)`, TLSv1.3, SAN dua host, issuer Let's Encrypt YE1 (berlaku 11 Okt 2026 – 9 Jan 2027) |
| 13 | Jalur negatif publik (`/api/v1/tidak-ada`, `/metrics`, `/internal/*`, `/debug/pprof/`) pada kedua host | 404 semua |
| 14 | **[6] PASCA-COMMIT** pada `7a45c7e` dengan pohon bersih: `git rev-parse HEAD`, `git status --porcelain` | `7a45c7e…`, **KOSONG** (0 baris) sebelum dan sesudah eksekusi |
| 15 | **[6] PASCA-COMMIT**: `PATH=/usr/bin:/bin make test`, `make test`, `make lint`, `( cd api && golangci-lint run ./... )`, `make toolchain-cek` | exit 0 semua; 27 paket `ok` per `make test`; `0 issues.` pada kedua jalur lint; `go1.27.1` + `golangci-lint v2.14.0` |
| 16 | **[6] PASCA-COMMIT** kendali negatif: `make -f <Makefile 34b0e94> test` pada PATH minimal | exit 2 (`/bin/sh: 1: go: not found`, di dalam `sh` 127) — kegagalan lama tetap dapat direproduksi dari commit yang sama |
| 17 | **[7] PASCA-COMMIT** HTTPS dua host tanpa `-k` (3× `curl --head` + rantai sertifikat + 4 jalur negatif per host) | 6/6 `200` HTTP/2 exit 0; `Verify return code: 0 (ok)`, TLSv1.3; 8/8 jalur negatif 404 |
| 18 | **[8] PUTARAN 3** pada `f336231` dengan pohon bersih: `git rev-parse HEAD`, `git status --porcelain` | `f336231…`; **KOSONG** (0 baris) sebelum dan sesudah eksekusi |
| 19 | **[8]** gerbang: `PATH=/usr/bin:/bin make test`, `make test`, `make lint`, `( cd api && golangci-lint run ./... )`, `make toolchain-cek`, `make vet`, `make build` | exit 0 semua; 27 paket `ok` tanpa FAIL; `0 issues.` pada kedua jalur lint; `go1.27.1` + `golangci-lint v2.14.0`; artefak `bin/server` hasil build dihapus di dalam skrip |
| 20 | **[8]** `./scripts/go-env.sh --uji-diri` (5 kendali baru) | exit 0; **20/20 lulus** (15 kendali sebelumnya + 5 fail-closed) |
| 21 | **[8]** kendali negatif **fail-closed** (cacat QA putaran 2): `env -i … make test` tanpa toolchain; `GRAHITA_GO=/salah/jalur/go make test`; `make test GO=`; `make lint GOLANGCI_LINT=` | **exit 2 masing-masing** dengan pesan `GALAT toolchain: …`, dan **0 baris `ok`** (0 tes dijalankan) — tidak lagi hijau tanpa tes; ditambah kendali positif bahwa penjaga tidak memblokir konfigurasi sah |
| 22 | **[8]** kendali negatif historis: `make -f <Makefile 34b0e94> test` pada PATH minimal; jumlah pesan resolver per satu panggilan `make` | exit 2 (`/bin/sh: 1: go: not found`, di dalam `sh` 127); resolver **1×** (sebelumnya berulang) |
| 23 | **[9]** seluruh blok [8] diulang pada commit dokumentasi yang memuat bagian [8] | exit 0, hasil identik (pohon bersih sebelum & sesudah)
| 24 | **[10]** pengukuran ulang pada kartu dokumentasi ini (`c1184f8`, pohon bersih): `git rev-parse HEAD` + `git status --porcelain`; `./scripts/go-env.sh --uji-diri`; `make test GO=/usr/bin/true`; `GRAHITA_GO=/usr/bin/true make test`; `make test GO=`; `make lint GOLANGCI_LINT=/usr/bin/true`; `make test`; `make lint`; `( cd api && golangci-lint run ./... )`; `make toolchain-cek` | pohon **KOSONG** (0 baris) sebelum & sesudah; `--uji-diri` exit 0 **20 lulus, 0 gagal**; `make test GO=/usr/bin/true` → resep `cd api && /usr/bin/true test ./...`, **exit 0 dengan 0 baris `ok`** (override command line tidak divalidasi versinya); `GRAHITA_GO=/usr/bin/true make test` → **exit 2** `bukan Go >= 1.27`; `make test GO=` → **exit 2**; `make lint GOLANGCI_LINT=/usr/bin/true` → resep `cd api && /usr/bin/true run ./...`, **exit 0 tanpa lint dijalankan**; gerbang normal `make test` exit 0 (27 paket `ok`), `make lint` dan lint polos `0 issues.`, `make toolchain-cek` exit 0 |

Perintah dasar yang dicatat tester pada `t_e36576b6` (`make test`,
`cd api && golangci-lint run ./...`) kini keduanya exit 0 **pada commit yang
sama** (`7a45c7e`, pohon bersih, bukti [6] baris 3–6) dan tanpa perubahan aturan
linter. Kendali negatif pada commit yang sama ([6] butir 7) tetap keluar 2/127
dengan Makefile pra-perbaikan, sehingga hasil hijau bukan akibat mesin lain.

Putaran 3 mengulang kedua perintah itu pada commit penjaga fail-closed
`f336231` ([8] butir 3–6) dengan hasil sama, **dan** menutup cacat fail-open yang
ditemukan QA putaran 2: bila toolchain tidak teresolusi, `make test` sekarang
**exit 2 + pesan jelas dengan 0 tes dijalankan** ([8] butir 9a–9d), bukan lagi
exit 0 tanpa tes. Janji kartu “jangan mengklaim gate hijau” karena itu kini
berlaku pada target Makefile, bukan hanya pada skrip pemilih.

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

**Reproduksi blok bukti [6]/[7] (pasca-commit) — urutan yang dipakai kartu ini.**
Jalankan dari root repo pada pohon **bersih** (`git status --porcelain` kosong);
jangan menaruh skrip bukti di dalam repo supaya worktree tetap bersih:

```bash
git rev-parse HEAD ; git status --porcelain        # harus kosong (0 baris)
PATH=/usr/bin:/bin make test                       # gerbang dengan PATH TANPA toolchain
make toolchain-cek ; make test ; make lint          # gerbang normal
( cd api && golangci-lint run ./... )               # perintah polos ala tester
# kendali negatif: Makefile pra-perbaikan (34b0e94) pada PATH tanpa toolchain
git show 34b0e94efcfa85eb4f8ac7546fb649f58768e653:Makefile > /tmp/Makefile-HEAD
PATH=/usr/bin:/bin make -f /tmp/Makefile-HEAD test  # harus exit 2 ("go: not found", sh 127)
git status --porcelain                              # harus tetap kosong sesudah eksekusi
```

Bila `go` tidak ada di PATH sama sekali: `eval "$(./scripts/go-env.sh --export)"`
(dari root repo) atau `GRAHITA_GO=/jalur/ke/go make test`. Bila runner baru
belum diprovisikan: `./scripts/runner-provision.sh` (tanpa sudo).

**Reproduksi kendali fail-closed putaran 3 (bagian [8]/[9]) — semua harus GAGAL
(exit != 0) dan tidak boleh menjalankan satu tes pun.** Jalankan dari root repo
pada pohon bersih; semuanya hanya membaca, tidak menulis ke repo:

```bash
git rev-parse HEAD ; git status --porcelain          # harus kosong (0 baris)
env -i HOME=/tmp/rumah-kosong PATH=/usr/bin:/bin GRAHITA_GO_KANDIDAT=/tmp/tidak-ada \
  make test                                          # harus exit 2 + "GALAT toolchain: …"
GRAHITA_GO=/salah/jalur/go make test                 # harus exit 2 + "GALAT toolchain: …"
make test GO=                                        # harus exit 2 (penjaga tidak bisa dilewati)
make lint GOLANGCI_LINT=                             # harus exit 2 + "… golangci-lint …"
make vet GO= ; make build GO= ; make toolchain-cek GO=   # semua harus exit 2
# kendali POSITIF (penjaga tidak memblokir konfigurasi sah): gunakan pemilih normal
make test ; make lint ; ./scripts/go-env.sh --uji-diri    # exit 0 / 0 issues. / 20 dari 20 lulus
git status --porcelain                                # harus tetap kosong sesudah eksekusi
```

Seluruh blok di atas direkam mentah di `BUKTI-T5-RUNNER.txt` bagian **[8]**
(commit `f336231`) dan diulang pada commit dokumentasi yang memuatnya (**[9]**).

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
   Diff commit `7a45c7e` terhadap `34b0e94` = 6 berkas: `Makefile`, `README.md`,
   `scripts/go-env.sh`, `scripts/runner-provision.sh`, `docs/design/t5-runner/*`;
   diff commit implementasi putaran 3 (`f336231` terhadap induknya `25e7b8a`) =
   2 berkas saja: `Makefile` dan `scripts/go-env.sh`;
   `git diff --name-only 34b0e94..HEAD -- api web` = **0 berkas**, dan
   `.golangci.yml`, `api/`, serta `web/` tidak tersentuh
   (sha256 `api/.golangci.yml` identik: `759dd775…`). Pada saat blok bukti
   [6]/[7] maupun [8]/[9] direkam, `git status --porcelain` **KOSONG sebelum dan
   sesudah** eksekusi — artefak `bin/` hasil `make build` dibersihkan di dalam
   skrip, jadi tidak ada sisa di pohon. Suntingan dokumentasi yang memuat blok
   bukti dilakukan SETELAH perekaman, hanya menyentuh `docs/design/t5-runner/` +
   `README.md`, dan di-commit terpisah dari commit yang diuji (hash commit
   dokumentasi dicatat pada komentar kartu `t_ac58a28e`).
6. **Fail-closed ≠ selalu gagal.** Penjaga `_wajib_go`/`_wajib_lint` hanya
   berhenti bila nilai toolchain kosong; kendali positif (stub Go 1.27.1,
   [8] butir 8 baris “tidak menghalangi konfigurasi sah”) membuktikan jalan sah
   tetap exit 0, dan seluruh gerbang di [8]/[9] dijalankan dengan pemilih normal.
7. **Override command line bukan jalur tervalidasi (batas putaran dokumentasi
   `t_14e91b3c`).** `make test GO=/jalur/ke/go` dan
   `make lint GOLANGCI_LINT=/jalur/ke/golangci-lint` adalah override eksplisit
   yang didokumentasikan pada komentar `Makefile` baris 11; karena Makefile
   memakai `ifndef`, pemilih `scripts/go-env.sh` **tidak dipanggil** pada jalur
   ini sehingga **versi toolchain tidak divalidasi**. Konsekuensinya, klaim
   “tidak pernah hijau tanpa menjalankan satu tes pun” hanya berlaku pada jalur
   yang melewati pemilih toolchain: bila `$(GO)` menunjuk biner lain yang selalu
   exit 0 (mis. `/usr/bin/true`), `make test` memang exit 0 dengan 0 tes
   dijalankan — diukur ulang dan direkam di `BUKTI-T5-RUNNER.txt` [10]
   (`make test GO=/usr/bin/true` → exit 0, 0 baris `ok`; pembanding
   `GRAHITA_GO=/usr/bin/true make test` → exit 2, dan `make test GO=` → exit 2).
   Penjaga fail-closed hanya menjamin kasus “tidak teresolusi/kosong”. Perilaku
   guard **tidak diubah** oleh kartu dokumentasi ini (tanpa perubahan `Makefile`
   maupun `scripts/go-env.sh`).
