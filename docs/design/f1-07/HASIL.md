# F1-07 — Registrasi akun cabang melalui token

Kartu: **t_2fdebcf9 — F1-07 Registrasi akun cabang melalui token**

Dokumen acuan: PRD §3.1, §9(1); SystemDesign §4.1, §6–7; Database.md §4.1–4.2.

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Permukaan API yang dikerjakan

| Method | Path | Peran | Sesuai kartu |
|---|---|---|---|
| POST | `/api/v1/auth/registration/validate` | publik (langkah 1) | ya |
| POST | `/api/v1/auth/register` | publik (langkah 2) | ya |

Keduanya terdaftar di `openapi/openapi.yaml`, dikecualikan dari CSRF, dibatasi
per IP, dan dijaga dua arah (router ⇄ kontrak) oleh
`TestKontrakOpenAPIIkutRouter` + `TestKontrakOpenAPIPathAuthLengkap`.

## Keputusan desain

1. **Paket baru `internal/registration`.** SystemDesign §2 menempatkan
   "token registrasi" di domain `auth`, tetapi `internal/auth` tidak bisa
   mengimpor `internal/audit` (audit mengimpor auth → siklus). Paket service
   tersendiri memakai ulang helper token/kata sandi dari `auth`
   (`NormalizeRegistrationToken`, `HashRegistrationToken`,
   `ValidRegistrationTokenFormat`, `CheckPasswordStrength`, `HashPassword`)
   tanpa memecah lapisan; `internal/arch` diperluas agar paket ini tetap
   dilarang mengimpor `net/http`/repo/sqlc.
2. **Kode error spesifik per keadaan** (PRD CP-REG-07), dengan pesan yang
   mengarahkan tindakan:
   `400 TOKEN_INVALID` (tidak dikenal/salah bentuk/token lama yang sudah
   digantikan), `400 TOKEN_EXPIRED`, `409 TOKEN_USED`,
   `409 BRANCH_INACTIVE`, `409 BRANCH_HAS_ACCOUNT`, `409 EMAIL_TAKEN`.
   Urutan pemeriksaan tetap: terpakai → kedaluwarsa → cabang aktif → cabang
   belum berakun.
3. **Format token diperiksa sebelum menyentuh DB.** Token yang bentuknya bukan
   `GRH` + 16 karakter alfabet token langsung `TOKEN_INVALID` tanpa query —
   inilah "validasi format" PRD CP-REG-02. Token tetap sah walau tanda
   hubungnya hilang atau hurufnya kecil.
4. **Token dipakai tepat sekali, dua pendaftaran bersamaan hanya satu lolos.**
   Langkah 2 berjalan dalam satu transaksi: `SELECT … FOR UPDATE OF t` pada
   baris token, **periksa ulang** kelayakan dari baris yang sudah terkunci,
   `INSERT branch_users`, `UPDATE registration_tokens SET used_at=now(),
   used_by=…` (dengan `AND used_at IS NULL`, jumlah baris diperiksa), lalu
   entri audit — baru COMMIT. Pemeriksaan awal di luar transaksi hanya untuk
   memberi pesan cepat; keputusan akhir selalu dari pemeriksaan terkunci.
5. **Data cabang read-only benar-benar tidak bisa dipalsukan.**
   `registerRequest` hanya punya `token`, `telepon`, `password`,
   `password_konfirmasi`; nama cabang, nama kepala, dan email **tidak ada** di
   DTO dan diambil dari `branches` lewat token. `httpx.DecodeJSON` memakai
   `DisallowUnknownFields`, jadi payload yang menyelipkan `nama_cabang` /
   `email` / `branch_id` ditolak `400 VALIDATION_FAILED` (bukan diabaikan).
   Diuji di dua lapisan (in-memory + PostgreSQL).
6. **1 cabang = 1 akun** dijaga dua lapis: pemeriksaan `branch_has_account`
   pada token (`BRANCH_HAS_ACCOUNT`) dan `UNIQUE (branch_id)` pada
   `branch_users`; pelanggaran indeks unik dipetakan ke sentinel domain yang
   sesuai (bukan 500).
7. **Kata sandi divalidasi sebelum token disentuh** (sama polanya dengan
   tautan atur ulang F1-04): konfirmasi tidak sama → `ErrPasswordMismatch`
   (`400`, field `password_konfirmasi`), kata sandi lemah →
   `auth.ErrPasswordTooWeak` (`400`, field `password`). Percobaan gagal **tidak** membakar token — diuji eksplisit.
   Minimal 8 karakter + huruf & angka tetap dari `auth.PasswordPolicy`
   (PRD CP-REG-05 menuntut 8 karakter; indikator kekuatan adalah UI).
8. **Email akun selalu = email cabang** (`branch_users.email` ←
   `branches.email`), bukan dari payload. Akun baru aktif sehingga langsung
   bisa login (kriteria kartu) — dibuktikan di uji integrasi PostgreSQL,
   termasuk login setelah mendaftar.
9. **Audit transaksional.** Satu entri `audit_logs` per pendaftaran berhasil:
   `entity='branch_user'`, `action='register'`, `entity_id=actor_id=id akun
   baru`, `actor_kind='branch'`, `actor_nama=nama cabang`, `ip` dari
   middleware `requestMeta`, `after` memuat user/branch/nama/email. Permintaan
   yang ditolak tidak meninggalkan entri.
10. **Pembatas laju per IP** (`Architecture.md §6`: "validasi token
    registrasi" dibatasi). `RegistrationLimiter` memakai jendela tetap
    `httpx.RateLimiter`; kunci terpisah untuk validate dan register
    (`registration.validate:<ip>` / `registration.register:<ip>`), 10
    permintaan / 15 menit di `main.go`. Ini juga menutup risiko yang dicatat
    F1-06 ("token registrasi tidak dibatasi laju").
11. **Frontend belum dikerjakan di kartu ini.** Kartu menyebut permukaan API
    dan seluruh kriterianya API-level ("pesan spesifik", "dua register
    bersamaan", "tidak dapat dipalsukan dari payload", "akun baru bisa
    login"): halaman `/daftar` (langkah 1–2 + indikator kekuatan) masih perlu
    kartu UI lanjutan — keputusan yang sama dengan F1-06 → F1-06b.

## Berkas yang berubah

Backend:

- `api/internal/registration/registration.go` (baru) — service, snapshot
  kelayakan token (`Snapshot.Cek`), sentinel error domain, validasi input.
- `api/internal/registration/memory.go` (baru) — repository in-memory
  (uji + demo) yang hanya menyimpan hash token dan merekam entri audit.
- `api/internal/registration/postgres/postgres.go` (baru) — repository
  PostgreSQL: `LookupRegistrationToken`, `LockRegistrationToken`
  (`FOR UPDATE OF t`), `CreateBranchAccount`, `MarkRegistrationTokenUsed`,
  plus audit dalam transaksi yang sama.
- `api/db/queries/registration.sql` (baru) + hasil `sqlc generate`
  (`api/internal/db/sqlc/registration.sql.go`).
- `api/internal/httpapi/handlers_registration.go` (baru) — 2 handler.
- `api/internal/httpapi/router.go` — 2 rute publik + CSRF exempt.
- `api/internal/httpapi/deps.go` — `Deps.Registration`,
  `Deps.RegistrationLimiter`.
- `api/internal/httpapi/errors.go` — pemetaan 7 sentinel pendaftaran.
- `api/internal/auth/registration.go` — `RegistrationTokenPanjang` +
  `ValidRegistrationTokenFormat` (validasi bentuk, dipakai langkah 1 & 2).
- `api/internal/audit/audit.go` — `EntityBranchUser`, `ActionRegister`.
- `api/cmd/server/main.go` — perakitan service + limiter.
- `openapi/openapi.yaml` — 2 operasi baru, skema langkah 1/2, kode
  `TOKEN_INVALID`/`TOKEN_EXPIRED`/`TOKEN_USED` pada enum error.

Uji:

- `api/internal/registration/registration_test.go` (baru, 5 uji unit domain,
  11 sub-uji).
- `api/internal/httpapi/router_registration_test.go` (baru, 5 uji HTTP
  in-memory, 11 sub-uji).
- `api/internal/httpapi/integration_registration_pg_test.go` (baru, 4 uji
  integrasi PostgreSQL nyata).
- `api/internal/httpapi/integration_pg_test.go` (fixture: service
  `registration` + limiter).
- `api/internal/httpapi/openapi_contract_test.go` (2 path F1-07 dipin).
- `api/internal/arch/arch_test.go` — aturan "service tidak mengimpor
  transport/net/http" diperluas ke paket `registration`.

## Bukti verifikasi (dijalankan sendiri)

### Gerbang statis

| Perintah | Hasil |
|---|---|
| `gofmt -l .` (api) | kosong |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `sqlc-v1.31.1 generate` | idempoten (tidak mengubah berkas hasil generate yang sudah ada) |
| `sh scripts/check-secrets.sh` | "Tidak ada rahasia terdeteksi pada berkas yang dilacak" (exit 0) |
| `golangci-lint` | tidak tersedia di sesi ini (sama seperti sesi F1-04/F1-05/F1-06); gofmt/vet/build + uji arsitektur lolos |
| `npx @redocly/cli@1 lint openapi.yaml` | tidak dijalankan (butuh npm/jaringan); perubahan OpenAPI murni deskriptif & ter-parse uji kontrak |

### Uji backend

DB uji: PostgreSQL 16.15 di `127.0.0.1:5455` (`grahita_dev_f107`,
`scripts/pg-dev-shim.sql` dipasang lebih dulu untuk `uuidv7()`).

```
go test -race -p 1 -count=1 -v ./...   → 155 PASS, 0 FAIL, 1 SKIP, 11 paket ok
  internal/arch ok · internal/auth ok · internal/bank ok · internal/branch ok
  internal/httpapi ok · internal/platform/apierr ok · internal/platform/database ok
  internal/registration ok · internal/settings ok · internal/users ok · internal/worker ok
```

Catatan SKIP (bukan akibat kartu ini): `TestWorkerMenjalankanJob` (uji F1-01)
dilewati karena tabel `river_job` belum ada di DB uji (butuh `--mode=migrate`);
sama seperti yang tercatat di serah terima F1-05.

Uji baru kartu ini:

```
internal/registration (unit domain)
  --- PASS: TestValidasiTokenMengembalikanDataCabangReadOnly
  --- PASS: TestValidasiTokenMenolakSetiapKeadaanDenganPesanSpesifik
      tidak_dikenal · bentuknya_tidak_sah · kedaluwarsa · sudah_dipakai
      · cabang_nonaktif · cabang_sudah_punya_akun           (6 sub-uji)
  --- PASS: TestDaftarMembuatAkunDanMemakaiTokenTepatSekali
  --- PASS: TestDaftarMenolakKataSandiKonfirmasiDanTeleponTidakSah
  --- PASS: TestDaftarMenolakTokenTidakSahTanpaMenyentuhData
internal/httpapi (HTTP penuh, in-memory)
  --- PASS: TestApiRegistrasiDuaLangkahMembuatAkun
  --- PASS: TestApiRegistrasiPesanSpesifikPerKeadaanToken    (6 sub-uji)
  --- PASS: TestApiRegistrasiMenolakDataReadOnlyDariPayload
  --- PASS: TestApiRegistrasiMenolakKonfirmasiDanKataSandiLemah
  --- PASS: TestApiRegistrasiDibatasiPerIP
internal/httpapi (integrasi PostgreSQL nyata)
  --- PASS: TestPGPendaftaranAkunCabangLengkapDanBisaLogin
  --- PASS: TestPGPendaftaranTidakBisaMemalsukanDataReadOnly
  --- PASS: TestPGPendaftaranMenolakTokenKedaluwarsaNonaktifDanBerakun
  --- PASS: TestPGDuaPendaftaranBersamaanHanyaSatuBerhasil
```

Yang benar-benar dibuktikan (bukan hanya status kode):

- **Dua langkah**: token dibuat lewat API admin F1-06 → `validate` 200 dengan
  `nama_cabang`/`nama_kepala`/`email` dari DB → `register` 201 → baris
  `branch_users` benar-benar ada (`email` = email cabang, `telepon`
  tersimpan, `password_hash` **Argon2id** dan lolos `auth.VerifyPassword`
  atas kata sandi pendaftaran, `is_active`).
- **Akun baru bisa login**: `POST /auth/login` dengan email cabang + kata
  sandi pendaftaran → 200 (kriteria kartu, diuji di router + DB nyata).
- **Token sekali pakai**: `register` kedua → `409 TOKEN_USED`; `validate`
  setelahnya → `409 TOKEN_USED`; `used_at` terisi dan `used_by` = id akun
  baru; tetap tepat **1** baris `branch_users` untuk cabang itu.
- **Dua pendaftaran BERSAMAAN**: dua goroutine `POST /auth/register` dengan
  token yang sama → tepat satu `201` dan satu `409 TOKEN_USED`; satu akun;
  token terpakai sekali. Permintaan yang kalah terlihat tertahan pada kunci
  baris (durasi ~223 ms) — kunci `FOR UPDATE` bekerja, bukan sekadar uji
  sekuensial.
- **Data read-only tidak dapat dipalsukan**: payload dengan `nama_cabang` /
  `email` / `branch_id` → `400 VALIDATION_FAILED`, tidak ada akun yang lahir,
  token tidak terpakai.
- **Normalisasi token**: mendaftar memakai token tanpa tanda hubung + huruf
  kecil tetap berhasil (hash bentuk ternormalisasi, kewajiban dari F1-06).
- **Pesan spesifik**: tidak dikenal / bentuk salah → `TOKEN_INVALID`;
  kedaluwarsa → `TOKEN_EXPIRED`; sudah dipakai → `TOKEN_USED`; cabang
  nonaktif → `BRANCH_INACTIVE`; cabang sudah berakun → `BRANCH_HAS_ACCOUNT`;
  token yang sudah digantikan token baru → `TOKEN_INVALID`.
- **Kata sandi**: konfirmasi beda → `400` field `password_konfirmasi`; kata
  sandi < 8 karakter → `400` field `password`; keduanya tidak membakar token
  (validate setelahnya masih 200).
- **Pembatas laju per IP**: 3 permintaan dilayani, ke-4 → `429`; IP lain tetap
  dilayani.
- **Audit**: satu entri `branch_user/register` per pendaftaran berhasil,
  `actor_id` = akun baru, `actor_nama` = nama cabang, `ip` terisi, `after`
  memuat email cabang; permintaan yang ditolak (6 keadaan + 3 payload palsu)
  tidak menambah entri.

## Perilaku & keputusan yang menyimpang/eksplisit

- **Nomor telepon wajib** (trim, maks 40 karakter) → `400` field `telepon`.
  PRD CP-REG-04 menyebutnya sebagai field yang diisi pengguna; kosong berarti
  permintaan belum lengkap. Tidak ada validasi format angka (nomor telepon
  Indonesia beragam).
- **`409` untuk token terpakai/cabang nonaktif/berakun, `400` untuk token
  tidak dikenal/kedaluwarsa.** Pemisahan ini memberi kode yang bisa
  ditindaklanjuti UI tanpa membocorkan apa pun yang sensitif (token hanya
  diketahui pemegangnya).
- **Tidak ada login otomatis** setelah mendaftar: kartu tidak memintanya
  (SystemDesign §4.1 berakhir di `201`); klien diarahkan ke halaman masuk.
- **`Register` memakai versi `for update of t`** sehingga hanya baris token
  yang terkunci — baris `branches` tidak ikut terkunci dan tidak bisa
  deadlock dengan operasi master cabang F1-06.
- `telepon` dikirim sebagai `pgtype.Text`; karena wajib diisi dan non-kosong,
  nilainya tidak pernah NULL pada jalur ini.

## Yang belum dikerjakan (batas kartu)

- **UI `/daftar`** (PRD §3.1 langkah 1–2, SystemDesign §7.1 peta `pDaftar`):
  form token, tampilan data cabang read-only, field telepon/kata sandi/
  konfirmasi, dan **indikator kekuatan kata sandi**. Halaman ini belum ada di
  portal cabang; perlu kartu UI lanjutan (pola F1-06 → F1-06b).
- Uji end-to-end T1 (`t_c80fd59b`) yang menyilangkan registrasi → login →
  token ulang → isolasi cabang.

## Sisa risiko

- **PG18 asli/CI `postgres:18-alpine` belum dieksekusi** di sesi ini (PG16.15
  + `scripts/pg-dev-shim.sql`), sama seperti F1-02…F1-06. `FOR UPDATE OF t`
  dan `EXISTS(...)::boolean` adalah SQL standar sehingga risiko rendah.
- **Limiter hanya di memori proses** (konsekuensi `httpx.RateLimiter` F1-01/
  F1-04) dan kunci per-IP diturunkan dari `X-Forwarded-For` — sama dengan
  catatan F1-04; cukup untuk satu VPS, gugur saat multi-instance.
- Pendaftaran tidak mengirim email konfirmasi/notifikasi ke cabang maupun
  admin; PRD tidak memintanya (email transaksional dicakup F1-16).
- `ErrEmailTaken` (tabrakan email pada `branch_users.email`) hanya mungkin
  lewat data yang bertentangan (mis. dua cabang berbagi email setelah diubah
  admin); pesannya mengarahkan menghubungi admin Pusat.
