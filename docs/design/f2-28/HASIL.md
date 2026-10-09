# F2-28 — Antarmuka audit log admin

Kartu: **t_f37774bb — F2-28 Antarmuka audit log admin**

Dokumen acuan: `docs/PRD.md` §4.3, §4.5, §4.10, §10; `docs/Architecture.md`
§6 dan §7; `docs/SystemDesign.md` §5.4 dan §6; `docs/Database.md` §4.7.

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil
nyata), bukan ringkasan kode.

## Lingkup yang dikerjakan

1. **Permukaan baca** `GET /api/v1/admin/audit-logs` — paginasi server + filter
   entitas/aksi/aktor/waktu, hanya Superadmin (`admin.audit`), beserta
   dokumentasi OpenAPI (skema `AuditLogEntry`, `AuditLogFilter`, `AuditLogPage`).
2. **Redaksi data sensitif** (`audit.Redact`) yang dijalankan **sebelum
   menyimpan** (di `audit.NewEntry`, jadi berlaku untuk SELURUH produsen entri)
   dan **saat menampilkan** (di `audit.Service.List`, untuk baris lama).
3. **Produsen entri untuk mutasi akun** (keputusan routing CEO pada kartu ini):
   undang admin, ubah admin (nama/role/flag/status), aktif-nonaktif akun cabang,
   kirim tautan atur ulang akun cabang, kirim ulang undangan admin, dan
   `PUT /api/v1/auth/me` (ganti nama admin sendiri). Sebelum kartu ini, seluruh
   mutasi tersebut **tidak** meninggalkan jejak audit.
4. **Halaman `/audit`** di Admin Portal (Superadmin): tabel audit log + filter
   di URL search param, diff before/after yang bisa dibuka, dan ringkasan
   alasan/delta untuk revisi invoice.

## Keputusan desain

1. **Pembaca dan penulis dipisah tegas.** `audit_logs` bersifat append-only.
   Paket `internal/audit` menambah `Service.List` (baca) di atas interface
   `Reader`; penulisan tetap milik produsen masing-masing di dalam
   transaksinya. Tidak ada jalur tulis lewat HTTP.
2. **Filter punya satu definisi.** `ListAuditLogs` dan `CountAuditLogs` memakai
   WHERE yang sama persis, sehingga `total` pada respons tidak pernah
   menyimpang dari jumlah baris yang bisa ditelusuri halaman demi halaman.
3. **Urutan stabil `at DESC, id DESC`.** Beberapa entri sering lahir pada
   milidetik yang sama (mis. beberapa topik tutorial dalam satu permintaan);
   tanpa tie-break `id`, baris bisa bertukar posisi antar halaman dan ada baris
   yang terlewat atau tampil dua kali. Diuji dengan 12 baris ber-`at` identik.
4. **Batas hari = tanggal bisnis WIB.** Tanggal filter dikirim sebagai teks
   `YYYY-MM-DD` dan dicor di SQL dengan `AT TIME ZONE 'Asia/Jakarta'`, bukan
   `::timestamptz`, supaya batasnya tidak bergeser mengikuti zona waktu sesi
   database (pola yang sama dengan `report.sql`).
5. **Filter tak dikenal ditolak 400.** `entitas` dan `aksi` divalidasi terhadap
   daftar nilai yang dikenal (`audit.EntitasSah()` / `AksiSah()`). Salah ketik
   tidak boleh diam-diam mengosongkan hasil dan membuat admin menyimpulkan
   "tidak ada aktivitas". Produsen entitas baru WAJIB menambahkan konstantanya
   ke daftar itu (ada uji yang menjaga).
6. **`aktor` menerima dua bentuk.** UUID dicocokkan persis ke `actor_id`;
   selain itu dianggap potongan nama (`actor_nama ILIKE`). UI bisa memakai
   keduanya tanpa endpoint daftar-aktor tersendiri.
7. **Redaksi berbasis nama kunci, idempoten, dan menyimpan presisi angka.**
   Snapshot disalin lewat JSON dengan `Decoder.UseNumber()` sehingga `int64`
   besar (total rupiah) tidak kehilangan presisi seperti kalau dibaca sebagai
   `float64`. Kunci yang diredaksi mencakup `password`/`sandi`, `token`,
   `secret`, `credential`, `authorization`/`cookie`/`csrf`, `otp`,
   `signature`/`presign`/`signed`, `storage_key`/`private_key`, `api_key`, dan
   `url`/`uri`/`tautan`/`link`/`href` (termasuk URL bukti transfer). Bila
   snapshot tidak bisa disalin, snapshot **dibuang** (fail-safe) dan kegagalannya
   dicatat di log — lebih baik kehilangan diff daripada menyimpan data yang
   belum tersaring.
   Snapshot yang **tidak** memuat kunci sensitif dikembalikan apa adanya
   (tipe struct domain tetap utuh), sehingga snapshot produsen lama tidak
   berubah bentuk hanya karena melewati fungsi ini. Redaksi dipasang di DUA
   batas tulis: `audit.NewEntry` (semua produsen) dan `audit/postgres.Insert`
   (jaring pengaman terakhir sebelum baris masuk tabel), lalu sekali lagi saat
   membaca (`audit.Service.List`) untuk baris lama.
8. **Pengecualian yang disengaja: `token_prefix`.** Nilainya hanya 8 karakter
   pertama token registrasi (bukan rahasia — sudah ditampilkan di halaman
   Cabang/User management) dan memang dipakai F1-06 untuk membuktikan token
   lama batal. Ia tetap diaudit; nilai token mentahnya tidak pernah masuk.
9. **Mutasi akun: entri ditulis di transaksi yang sama.** `users.Repository`
   kini menerima `[]audit.Entry` pada `CreateAdmin`/`UpdateAdmin`/
   `SetBranchUserActive`, dan implementasi PostgreSQL menulis perubahannya +
   entri audit dalam satu transaksi (baris dikunci `FOR UPDATE` supaya snapshot
   `before` tidak pernah basi). Kegagalan menulis audit membatalkan perubahan.
10. **Snapshot akun tanpa email.** `users.AdminSnapshot`/`BranchUserSnapshot`
    hanya memuat nama, role, flag, dan status — email login (PII) tidak ikut.
    Nama/kata sandi/tautan aktivasi juga tidak pernah masuk.
11. **No-op tidak menghasilkan event.** Mengirim status/role/nama yang sama
    dengan yang berlaku dilewati sebelum menulis apa pun (di service maupun
    repository), jadi `audit_logs` benar-benar berarti "ada yang berubah".
12. **`PUT /auth/me` menulis entri di repositori auth.** Paket `audit`
    mengimpor `auth` (untuk `auth.Actor`), sehingga `auth` tidak boleh mengimpor
    `audit` (siklus). Karena itu transaksi + entri audit untuk ganti nama
    profil hidup di `auth/postgres.UpdateName`: nama lama dibaca `FOR UPDATE`,
    `before`/`after` hanya `{"nama": …}`, dan aktor yang tercatat adalah nama
    yang berlaku saat aksi (identitas pada saat itu). Email/role/kata sandi
    tidak pernah masuk diff.
13. **Penerbitan tautan ditulis di transaksi tokennya.** Penerbitan tautan atur
    ulang (`POST /admin/users/branch/{id}/reset`) dan kirim ulang undangan admin
    perubahannya hidup di repositori auth (`password_reset_tokens`), bukan di
    repositori `users`. Karena itu fakta auditnya **diserahkan ke jalur token**
    (`auth.ResetAudit` → `ResetStore.CreateResetToken`) dan ditulis di dalam
    transaksi yang sama dengan token barunya: kegagalan menulis audit
    membatalkan tokennya, permintaan gagal tanpa efek samping (tidak ada token,
    tidak ada email diantre) dan bisa diulang dengan aman. Antrean email
    menyusul SETELAH commit, sama seperti undangan admin.
    `auth.ResetAudit` adalah tipe milik paket `auth` (bukan `audit.Entry`)
    karena paket `audit` sudah mengimpor `auth`; `auth/postgres` yang
    menerjemahkannya kembali menjadi `audit.Entry`. Tautannya sendiri tidak
    pernah masuk snapshot.
    *(Putaran 2: menggantikan desain lama `users.WithAuditRecorder` yang menulis
    entri SETELAH aksi berhasil di transaksi terpisah — lihat bagian Putaran 2.)*
14. **Kapabilitas `admin.audit` sudah ada sejak F1-04** dan hanya dimiliki
    Superadmin; router menegakkannya (`requireCap`) dan `audit.Service.List`
    memeriksanya ulang, jadi Manager/akun cabang dijawab 403 di dua tempat.
15. **Halaman `/audit` mengikuti pola penjaga yang sudah ada.** Komponen dalam
    baru dipasang setelah `PenjagaSuperadmin` lolos (seperti `/rekening`),
    sehingga Manager tidak pernah memanggil API audit sama sekali — bukan hanya
    menunya disembunyikan. Filter hidup di URL search param supaya hasil bisa
    di-bookmark/di-share, dan diff baru dirender saat diminta supaya satu
    respons tetap kecil.

## Berkas yang berubah

Backend:

- `api/internal/audit/audit.go` — entitas `admin_user` + aksi `reset`; snapshot
  `NewEntry` melewati `Redact`.
- `api/internal/audit/redact.go` (baru) — `Redact`, `Snapshot`, daftar kunci
  sensitif + pengecualian `token_prefix`.
- `api/internal/audit/service.go` (baru) — `Filter`/`Query`/`Log`/`Page`,
  interface `Reader` & `Recorder`, validasi/normalisasi filter, `Service.List`.
- `api/internal/audit/postgres/reader.go` (baru) — `Repo.List`/`Count`/`Record`
  di atas `ListAuditLogs`/`CountAuditLogs`.
- `api/db/queries/audit.sql` + `api/internal/db/sqlc/audit.sql.go` (baru) —
  query daftar & hitung.
- `api/internal/httpapi/handlers_audit.go` (baru) — handler + pembacaan filter.
- `api/internal/httpapi/router.go`, `deps.go`, `errors.go` — rute
  `GET /admin/audit-logs` (grup kapabilitas `admin.audit`), `Deps.Audit`,
  pemetaan error `audit.ErrForbidden`/`ErrFilterInvalid`.
- `api/internal/users/users.go`, `memory.go`, `postgres/postgres.go` — mutasi
  akun menulis entri audit (transaksional), `AdminSnapshot`/`BranchUserSnapshot`,
  no-op tanpa event; penerbitan tautan menitipkan fakta auditnya ke jalur token
  (`resetAudit`) alih-alih menulisnya sendiri (putaran 2).
- `api/internal/auth/reset.go` — `ResetToken.Audit` + tipe `ResetAudit`
  (proyeksi `audit.Entry` milik paket auth agar tidak ada siklus import).
- `api/internal/auth/service.go` — `IssuePasswordResetFor`/`SendPasswordResetFor`
  menerima fakta audit dan meneruskannya ke `CreateResetToken`.
- `api/internal/auth/postgres/postgres.go` — `CreateResetToken` transaksional
  (token + entri audit commit bersama) + `UpdateName` transaksional;
  `Repo` menyimpan pool.
- `api/internal/auth/memory.go` — token in-memory menyimpan fakta auditnya
  (`ResetAudits()` untuk pengujian).
- `api/cmd/server/main.go` — perakitan `audit.NewService(auditpg.New(pool))`,
  `Deps.Audit`.
- `openapi/openapi.yaml` — path `/api/v1/admin/audit-logs` + 3 skema
  (`AuditLogEntry`, `AuditLogFilter`, `AuditLogPage`).

Uji:

- `api/internal/audit/audit_test.go` (baru) — redaksi, normalisasi filter,
  paginasi, otorisasi, redaksi saat baca.
- `api/internal/auth/reset_audit_test.go` (baru) — fakta audit tersimpan bersama
  token; alur publik (lupa kata sandi) tidak menambah catatan.
- `api/internal/users/audit_test.go` (baru) — tepat satu event per mutasi,
  no-op tanpa event, penolakan tanpa event, snapshot tanpa email/token, dan
  fakta audit diteruskan ke jalur token untuk penerbitan tautan.
- `api/internal/httpapi/router_audit_test.go` (baru) — HTTP in-memory: cabang
  & Manager 403, tanpa sesi 401, filter tak valid 400, 503 tanpa layanan.
- `api/internal/httpapi/integration_audit_pg_test.go` (baru) — PostgreSQL
  nyata: mutasi akun tercatat tepat sekali, rollback tanpa event, **atomisitas
  penerbitan tautan (reset cabang & resend undangan) dengan trigger penggagal**,
  batas hari WIB, tie-break `at` identik, redaksi baris sensitif, alasan/delta
  revisi.
- `api/internal/httpapi/integration_pg_test.go` — fixture: service audit +
  service pengguna tanpa `WithAuditRecorder` (jalur audit kini milik transaksi
  token).

Frontend:

- `web/apps/admin/src/pages/audit.tsx` (baru) — halaman `/audit`.
- `web/apps/admin/src/routes.tsx`, `nav.ts` — rute `/audit` + menu Superadmin.
- `web/apps/admin/src/audit.test.tsx` (baru) — 4 uji perilaku.

## Bukti verifikasi (dijalankan sendiri)

### Gerbang statis

| Perintah | Hasil |
|---|---|
| `gofmt -l .` (api) | kosong |
| `go vet ./...` | exit 0 |
| `go build ./...` | exit 0 |
| `sqlc-v1.31.1 generate` | idempoten (md5 `internal/db/sqlc/audit.sql.go` sama sebelum/sesudah) |
| `sh scripts/check-secrets.sh` | "Tidak ada rahasia terdeteksi pada berkas yang dilacak" (exit 0) |
| `npx @redocly/cli@1 lint openapi/openapi.yaml` | valid, **10 warning** (sama seperti baseline HEAD; tidak ada warning/error baru) |
| `npm run typecheck` (web, 4 workspace) | exit 0 |
| `npm test` (web) | 205 lulus (api-client 8, ui 64, admin 74, customer 59) |

### Uji backend

DB uji: PostgreSQL 16.15 di `127.0.0.1:5455` (`grahita_f228` +
`scripts/pg-dev-shim.sql`); suite penuh `go test -race -p 1 -count=1 ./...`.

```
go test -race -p 1 -count=1 ./...  → seluruh paket ok, 0 FAIL, 0 DATA RACE
  (internal/httpapi 680.9s — termasuk seluruh uji integrasi PostgreSQL)
```

Uji baru kartu ini:

```
internal/audit (unit domain)
  --- PASS: TestRedactMenyembunyikanKunciSensitif
  --- PASS: TestRedactNilDanSnapshotKosong
  --- PASS: TestRedactMempertahankanTipeBilaTidakAdaKunciSensitif
  --- PASS: TestNewEntryMeredaksiSnapshotSebelumDisimpan
  --- PASS: TestListDitolakUntukSelainSuperadmin
  --- PASS: TestListMenormalkanFilterDanPaginasi
  --- PASS: TestListAktorNamaDanBatasBawaan
  --- PASS: TestListMenolakFilterTidakValid
  --- PASS: TestListMeneruskanGalatRepository
  --- PASS: TestDaftarEntitasDanAksiKonsisten
internal/auth (unit domain)
  --- PASS: TestCreateResetTokenMenyimpanAuditBersamaToken
internal/users (unit domain)
  --- PASS: TestAuditUndanganAdminSatuEventAman
  --- PASS: TestAuditUbahAdminTepatSatuEventDanNoOpTanpaEvent
  --- PASS: TestAuditUbahAdminYangDitolakTidakMeninggalkanEvent
  --- PASS: TestAuditStatusAkunCabangSatuEventDanNoOp
  --- PASS: TestAuditTautanResetDanKirimUlangUndanganLewatJalurToken
internal/httpapi (HTTP penuh, in-memory)
  --- PASS: TestApiAuditLogHanyaSuperadmin
  --- PASS: TestApiAuditLogFilterTidakValidDitolak
  --- PASS: TestApiAuditLogTanpaLayanan
internal/httpapi (integrasi PostgreSQL nyata)
  --- PASS: TestPGAuditMutasiAkunTercatatTepatSekali
  --- PASS: TestPGAuditRollbackTidakMeninggalkanEvent
  --- PASS: TestPGAuditInsertMeredaksiSebelumMenyimpan
  --- PASS: TestPGAuditFilterPaginasiDanRedaksi
  --- PASS: TestPGAuditPenerbitanTautanAtomikDenganMutasi   (putaran 2)
```

Yang benar-benar dibuktikan (bukan hanya status kode):

- **Tepat satu entri per mutasi.** Undang admin → 1 baris `admin_user/create`
  dengan `entity_id` akun baru, aktor + IP + nama aktor terisi, `before` NULL,
  `after` memuat nama/role/flag — dan **tidak** memuat email akun.
  Ubah role → 1 baris `admin_user/update` dengan delta role di before/after.
  Nonaktifkan akun cabang → 1 baris `branch_user/update` (`is_active`
  true→false). Tautan atur ulang → 1 baris `branch_user/reset` tanpa nilai
  tautan. `PUT /auth/me` → 1 baris `admin_user/update` untuk dirinya sendiri.
- **No-op tidak menambah apa pun.** Mengirim nilai yang sama (role, status akun,
  nama sendiri) tetap 200 tetapi jumlah entri tidak bertambah.
- **Rollback tanpa event.** Trigger penggagal INSERT `audit_logs` untuk
  `admin_user` membuat `PUT /auth/me` gagal 500, `admin_users.nama` **tidak
  berubah**, dan tidak ada baris audit; setelah trigger dilepas permintaan yang
  sama berhasil dan tercatat. Undangan dengan email terpakai → 409 tanpa entri.
- **Filter & paginasi stabil.** 16 baris uji (12 di antaranya ber-`at` identik)
  ditelusuri 4 halaman × 4 baris: seluruh baris tertutup **tanpa duplikat**;
  `total` = `count(*)`; filter `entitas` (dipisah koma), `aksi`, `aktor` (UUID
  dan potongan nama) menyaring benar; filter tanggal `2026-10-08` memasukkan
  baris `16:59:59Z` (23:59:59 WIB) dan **mengecualikan** `17:00:00Z` (00:00 WIB
  9 Okt); entitas tak dikenal → 400.
- **Data sensitif tidak bocor.** `audit/postgres.Insert` menulis
  `{"token": "[REDACTED]", "password": "[REDACTED]", "bukti_url": "[REDACTED]"}` —
  diperiksa langsung pada KOLOM jsonb mentah di database (bukan hanya lewat
  API) sementara `token_prefix` tetap utuh. Baris uji berisi `token`,
  `password`, dan `bukti_url` yang ditulis langsung dengan SQL (menirukan baris
  lama) dikembalikan API sebagai `[REDACTED]`; `alasan`/`delta_total` revisi dan
  snapshot `versi` lama tetap utuh (kriteria "revisi tampil aktor/alasan/delta").
- **Otorisasi.** Manager dan akun cabang 403 di API (in-memory dan PostgreSQL
  nyata), tanpa sesi 401; di UI Manager melihat halaman "Tidak berwenang" dan
  **tidak ada** permintaan ke `/admin/audit-logs` yang terkirim.

### Uji frontend

```
npx vitest run src/audit.test.tsx   (cwd web/apps/admin) → 4 lulus
  - audit log tampil dengan aktor, WIB, diff teredaksi, ringkasan alasan/delta
  - filter entitas tersimpan di URL search param dan dikirim ke API
  - paginasi memuat halaman berikutnya tanpa kehilangan total
  - Manager ditolak penjaga rute dan API tidak dipanggil
```

### Probe binary sungguhan (`--mode=api`)

DB `grahita_f228_live` (bersih) → `--mode=migrate` → `--mode=seed`
(`SEED_PASSWORD`) → server di `127.0.0.1:8098`; seluruh permintaan memakai
`Host: 127.0.0.1:8098` dan cookie jar + header `X-CSRF-Token` sungguhan
(skrip probe ada di workspace scratch pekerja, bukan di repo). **24 pemeriksaan,
0 gagal**, ringkasnya:

```
POST /auth/login (rina@grahitaindonesia.com)            → 200
GET  /auth/me                                           → 200 "Rina Kartika"
PUT  /auth/me {"nama":"Rina Kartika (probe)"}           → 200
PUT  /auth/me nama yang sama                            → 200 (no-op, tanpa entri)
POST /admin/users/admin (undang Dewi)                   → 201
GET  /admin/users/branch                                → 200
PUT  /admin/users/branch/{id} {"is_active":false}        → 200
PUT  /admin/users/branch/{id} {"is_active":false}        → 200 (no-op, tanpa entri)
POST /admin/users/branch/{id}/reset                      → 200
GET  /admin/audit-logs?per_halaman=5                     → 200 (items/total/halaman/
                                                            per_halaman/total_halaman/filter)
  entitas=admin_user&aksi=create   → 1 entri
  entitas=admin_user&aksi=update   → 1 entri  (ganti nama; no-op tidak dihitung)
  entitas=branch_user&aksi=update  → 1 entri  (nonaktifkan; no-op tidak dihitung)
  entitas=branch_user&aksi=reset   → 1 entri
isi respons tidak memuat GRH-/rahasia123/$argon2/lupa?token=/storage.example
GET  /admin/audit-logs?entitas=pesanan                   → 400 VALIDATION_FAILED
GET  /admin/audit-logs?halaman=99                        → 200 items kosong, total tetap
GET  /admin/audit-logs (Manager budi@)                   → 403 FORBIDDEN
POST /auth/login (bandung@, akun cabang)                 → 200, lalu
GET  /admin/audit-logs (akun cabang)                     → 403 FORBIDDEN
GET  /admin/audit-logs (tanpa sesi)                      → 401
```

## Putaran 2 — perubahan yang diminta QA (atomisitas audit penerbitan tautan)

QA putaran 1 (`grahita_qa_f228r1`, commit `825a72e`) memblokir kartu ini karena
dua jalur memakai `audit.Recorder`: `ResendBranchReset` dan `ResendInviteAdmin`
menjalankan aksi auth/outbox LEBIH DULU, lalu menulis audit di transaksi
terpisah. Probe QA dengan trigger penggagal `INSERT audit_logs` membuktikan
`POST /admin/users/branch/{id}/reset` → 500 tetapi baris `password_reset_tokens`
dan `email_outbox` tetap lahir, dan permintaan ulang menambah `email_outbox`
lagi tanpa satu pun entri audit.

Perbaikan yang dikerjakan:

1. **Fakta audit pindah ke jalur token.** `auth.ResetToken.Audit` (tipe
   `auth.ResetAudit`) diteruskan dari `users.Service` → `auth.Service` →
   `ResetStore.CreateResetToken`; `auth/postgres.CreateResetToken` membuka
   transaksi, menulis `password_reset_tokens` (CTE `ReplacePasswordResetToken`),
   lalu `audit_logs` (`audit/postgres.Insert`), baru `COMMIT`. Kegagalan audit
   membatalkan tokennya. `ForgetPassword`/`ForgotPassword` tetap tanpa audit
   (`Audit == nil`, satu pernyataan SQL seperti sebelumnya).
2. **Jalur `users.WithAuditRecorder` dihapus seluruhnya** (termasuk
   `discardRecorder`), sehingga entri audit tidak bisa lagi hilang diam-diam
   karena lupa memasang recorder: tidak ada lagi penulis audit "best effort".
3. **Uji regresi `TestPGAuditPenerbitanTautanAtomikDenganMutasi`** (PostgreSQL
   nyata): trigger menolak `branch_user/reset` dan `admin_user/resend` → kedua
   permintaan 500, token/email/audit **tidak bertambah**, diulang tetap tidak
   bertambah; setelah trigger dilepas satu permintaan = tepat satu entri audit +
   satu baris token + satu email. Aktor/IP tercatat, snapshot tanpa tautan/token.
4. Uji unit `TestCreateResetTokenMenyimpanAuditBersamaToken` (paket auth) dan
   penyesuaian `TestAuditTautanResetDanKirimUlangUndanganLewatJalurToken`
   (paket users) menjaga agar fakta audit benar-benar diteruskan, dan undangan
   admin baru tetap TIDAK menitipkan audit ke jalur token (entri `create` sudah
   ditulis `CreateAdmin` di transaksinya sendiri).

Bukti putaran 2 (dijalankan sendiri di DB `grahita_f228r2`, PG16.15 +
`scripts/pg-dev-shim.sql`):

```
gofmt -l .                              → kosong
go vet ./...                            → exit 0
go build ./...                          → exit 0
sh scripts/check-secrets.sh             → exit 0
go test -count=1 -run 'TestApiAudit|TestPGAudit' -v ./internal/httpapi/
  → 8/8 PASS (termasuk TestPGAuditPenerbitanTautanAtomikDenganMutasi)
go test -count=1 -run 'TestPGAkun|TestPGUser|TestPGAdmin|TestPGAturUlang|
     TestApiUserManagement|TestApiUndangAdmin|TestApiKirimUlangResetCabang|
     TestApiNonaktifkan|TestApiSuperadmin|TestApiDaftarAkunCabang|
     TestApiKontrakPathUser|TestPGOCR|TestPGKontenTutorial|TestPGTutorial|
     TestApiTutorial' ./internal/httpapi/   → 25/25 PASS (regresi akun + F2-26/F2-27)
go test -race -p 1 -count=1 -timeout 30m ./...  → 27 paket ok, 0 FAIL,
     0 DATA RACE, SUITE_EXIT=0 (internal/httpapi 691.0s)
```

Catatan: percobaan pertama tanpa `-timeout 30m` kena batas bawaan Go 10 menit di
tengah paket `httpapi` (paket itu memang ~11,5 menit dengan `-race`); bukan
kegagalan uji. Untuk isu atomisitas jalur `reset`, tangkapan log pada percobaan
itu sudah memperlihatkan galat audit sekarang muncul di jalur token
(`simpan token atur ulang: catat audit branch_user reset: ...`) — yaitu bukti
bahwa penulisan audit benar-benar terjadi di dalam transaksi token, bukan
sesudahnya.

Uji frontend tidak disentuh pada putaran 2 (tidak ada berkas `web/` yang
berubah); 4 uji `src/audit.test.tsx` sudah lulus di putaran 1 dan tetap berlaku.

## Perilaku & keputusan yang menyimpang/eksplisit

- Entri audit untuk **penerbitan tautan atur ulang** dan **kirim ulang
  undangan** ditulis di transaksi yang sama dengan baris
  `password_reset_tokens` (putaran 2); antrean emailnya menyusul setelah commit.
  Nilai tautannya tidak pernah masuk snapshot.
- `POST /admin/users/admin` yang gagal di tahap pengiriman email undangan
  tetap meninggalkan entri `create`: akunnya memang sudah ter-commit dan
  pemulihannya lewat `POST /users/admin/{id}/resend` (yang juga tercatat).
- Email akun sengaja **tidak** ikut ke snapshot audit mana pun (PII); untuk
  menelusuri akun tersangka, id akun pada `entity_id`/`actor_id` yang dipakai.
- `per_halaman` dipagari 100 di server (default 25) supaya satu permintaan tidak
  pernah membaca seluruh tabel yang terus tumbuh.
- Uji `PUT /auth/me` untuk akun cabang tetap 403 (perilaku F1-10 tidak diubah);
  hanya admin yang bisa mengganti namanya.

## Sisa risiko

- Redaksi bekerja pada **nama kunci** di dalam snapshot JSON. Nilai rahasia
  yang terselip pada kunci tak terduga (mis. sebuah string bebas yang berisi
  URL) tidak tertangkap; produsen entri tetap wajib tidak memasukkan nilai
  mentah ke snapshot — aturan ini sekarang terpusat di `audit.NewEntry`.
- Baris audit lama (sebelum F2-28) hanya melewati redaksi **saat dibaca**;
  isi kolomnya di database tidak diubah (append-only, tidak ada migrasi data).
- Filter tanggal memakai zona `Asia/Jakarta` literal di SQL; perubahan zona
  bisnis berarti mengubah query, bukan konfigurasi.
- Pencarian `aktor` memakai `ILIKE '%…%'` tanpa escape metakarakter LIKE
  (parameterized, bukan injeksi): `%`/`_` di kata kunci bisa memperlebar hasil,
  tetapi tidak pernah membuat permintaan gagal.
- PostgreSQL 18 asli/CI `postgres:18-alpine` belum dieksekusi di sesi ini (PG16
  + `scripts/pg-dev-shim.sql`), sama seperti kartu lain.
- `alasan`/`delta_total` revisi diuji dari baris audit berbentuk sama dengan
  keluaran F2-25, bukan melalui alur revisi penuh (alur itu sendiri sudah
  diuji QA di kartu F2-25).
