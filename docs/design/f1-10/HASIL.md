# F1-10 — Profil cabang/admin dan ganti password

Kartu: **t_cb5d8612 — F1-10 Profil cabang/admin dan ganti password**

Dokumen acuan: PRD §2.1, §3.2, §3.8, §4.10, §9(9); SystemDesign §6, §7; Database.md §4.1–4.2.

Halaman ini mencatat keputusan desain dan bukti verifikasi (perintah + hasil nyata),
bukan ringkasan kode.

## Lingkup yang dikerjakan

Backend:

| Method | Path | Peran | Catatan |
|---|---|---|---|
| GET | `/api/v1/me/branch` | akun cabang | sudah ada (F1-04); diperbaiki: `telepon` selalu dikembalikan (dulu hilang bila NULL) |
| GET | `/api/v1/auth/me` | semua yang login | sudah ada (F1-04) |
| **PUT** | **`/api/v1/auth/me`** | **admin** | **BARU**: ubah NAMA tampilan sendiri; email/role read-only |
| POST | `/api/v1/auth/password/change` | semua yang login | sudah ada (F1-04): verifikasi kata sandi lama, cabut sesi LAIN |
| POST | `/api/v1/auth/password/forgot` · `/reset` | publik | sudah ada (F1-04); kini punya UI di **kedua** portal |

Frontend:

- `/profil` portal cabang: data cabang read-only (nama, kepala cabang, email,
  telepon) + badge + tombol Keluar + form Ganti password.
- `/profil` portal admin: nama (editable) + email & role (read-only) + Ganti
  password + Keluar.
- `/lupa` **kedua** portal: minta tautan (tanpa query) dan membuat kata sandi
  baru (`?token=…`) — portal admin sebelumnya belum punya rute ini.
- Branding aset resmi GRAHITA INDONESIA pada shell, halaman auth, dan kartu
  profil.

## Permukaan API baru & alasan

`PUT /api/v1/auth/me` (body `{"nama": "..."}`, `additionalProperties: false`)
**tidak ada** di SystemDesign §6 — tabel itu memang "permukaan API (ringkas)".
Alasan menambahkannya:

1. PRD §4.10 menulis "**Nama**, email (read-only), role (read-only)" — hanya email
   dan role yang disebut read-only, jadi nama memang bisa diubah pemiliknya.
2. Prototype `admin_v2.html` `pProfil` menampilkan input Nama yang editable +
   tombol **Simpan** untuk **Manager maupun Superadmin**
   (`data-act="pr-save"` memperbarui `me().nama`).
3. Alternatif yang dipertimbangkan dan **ditolak**: memakai user management
   (`PUT /api/v1/admin/users/admin/{id}`). Endpoint itu butuh kapabilitas
   `admin.users.admin` yang menurut matriks PRD §2.1 **hanya Superadmin** —
   artinya Manager tidak akan bisa mengganti namanya sendiri, dan melonggarkan
   kapabilitas itu adalah penurunan keamanan.

Bentuknya sengaja minimal: hanya `nama`, dan hanya untuk aktor **admin**.

## Keputusan desain

1. **Batas kebijakan di service, bukan hanya di handler.** `auth.UpdateOwnProfile`
   menolak aktor non-admin (`ErrProfileNotEditable` → 403) dan menormalkan spasi
   (`strings.Fields`) sebelum memeriksa panjang 2–120 karakter; nama yang hanya
   berisi spasi dijawab 400 dengan field `nama`, bukan tersimpan kosong.
2. **Field read-only tidak bisa ditembus lewat endpoint ini.** `httpx.DecodeJSON`
   memakai `DisallowUnknownFields`, jadi mengirim `email`, `role`, atau
   `is_active` dijawab `400 VALIDATION_FAILED` (`json: unknown field "email"`),
   bukan diabaikan diam-diam. Email/role tetap hanya bisa diubah Superadmin lewat
   user management (dan proteksi superadmin terakhir tetap berlaku di sana).
3. **Profil cabang tetap tanpa rute tulis.** `GET /me/branch` mengambil cabang
   dari **sesi** (`actor.BranchID`), bukan dari parameter klien; `PUT`/`PATCH`/
   `POST /me/branch` dijawab `405 METHOD_NOT_ALLOWED`, dan aktor admin dijawab
   `403`. Nama/kepala/email/telepon cabang adalah data master Pusat (PRD §3.8).
4. **`telepon` selalu ada di respons profil cabang.** `GetBranch` memakai
   `COALESCE(telepon, '')` dan DTO `catalog.Branch` tidak lagi `omitempty`.
   Sebelumnya field ini menghilang saat NULL, sehingga bentuk respons profil
   berubah-ubah untuk UI. `Branch` di OpenAPI kini mencantumkan `telepon` sebagai
   field wajib + catatan read-only.
5. **Komponen UI dipakai bersama kedua portal** (`@grahita/ui`):
   `ChangePasswordForm` (validasi klien meniru aturan backend: ≥8 karakter,
   huruf+angka, tanpa spasi tepi, konfirmasi sama, baru ≠ lama; pesan `fields`
   dari server selalu menimpa) dan `PasswordResetPage` (dua mode: minta tautan /
   simpan kata sandi baru). Tujuannya agar portal cabang dan portal admin benar-
   benar memakai mekanisme yang sama (PRD CP-AUT-06, §4.10).
6. **`/lupa` admin memakai rute yang sama dengan portal cabang** dan membaca
   `?token=` dari **search param router** (bukan `window.location`), supaya rute
   dapat diuji dan tidak bergantung pada cara halaman dimuat. Tautan email tetap
   diarahkan ke host portal yang benar oleh `auth.NewResetURLBuilder`
   (cabang → `CUSTOMER_HOST`, admin → `ADMIN_HOST`) + `PASSWORD_RESET_PATH`
   (default `/lupa`) — tidak ada cookie yang dibagi antar subdomain.
7. **Aset branding resmi** (keputusan Tius dp, 8 Oktober 2026). Berkas sumber
   `img_3b99ba8e3dc6.png` (1254×1254, PNG beralfa) disalin apa adanya ke repo dan
   diturunkan tanpa menggambar ulang atau mengubah warna:

   | Berkas | Fungsi | Ukuran |
   |---|---|---|
   | `web/packages/ui/src/assets/logo-grahita-source.png` | aset asli (sumber, tidak diimpor UI) | 1254×1254, 515 KB |
   | `…/logo-grahita-lockup-320.png` | lambang + tulisan "GRAHITA INDONESIA" | 320×289, 71 KB |
   | `…/logo-grahita-mark-128.png` | lambang saja (kotak kuning + G) | 128×133, 15,6 KB |

   Turunan dibuat dengan pemotongan + pengecilan *box filter* beralfa
   *premultiplied* (skrip ad-hoc di scratch, bukan dependensi baru). Komponen
   `Logo` menjaga proporsi (`height` di-set, `width:auto`) sehingga tidak
   terdistorsi, dan memakai `alt="Grahita Indonesia"`; `Brand` mengirim `alt=""`
   karena nama sudah diucapkan wordmark di sebelahnya (menghindari pembaca layar
   menyebut dua kali). Karena tulisan pada aset berwarna ungu gelap sementara
   latar kedua portal berbeda (portal cabang brand-900 gelap, portal admin
   canvas terang), lockup pada halaman auth diletakkan di **chip putih** —
   tanpa mengubah warna aset. Mode mark (dominan kuning) dipakai di sidebar
   gelap/terang kedua shell.

## Bukti verifikasi

Gerbang backend (Go 1.27, `gofmt`/`vet`/`build`) dijalankan di repo target
`/home/hermes/projects/grahita-website`:

```
gofmt -l api        -> (kosong)
go vet ./...        -> exit 0
go build ./...      -> exit 0
sqlc-v1.31.1 generate -> idempoten (hanya perubahan query yang diharapkan)
```

Suite penuh di PostgreSQL 16 nyata (`TEST_DATABASE_URL` → DB baru
`grahita_f110` + `scripts/pg-dev-shim.sql`):

```
go test -race -p 1 -count=1 ./...   -> 182 PASS / 0 FAIL / 1 SKIP, 12 paket ok
```

(Satu SKIP adalah `TestWorkerMenjalankanJob` milik F1-01 yang butuh `--mode=migrate`
— sama seperti kartu-kartu sebelumnya, bukan regresi. Sebelum kartu ini: 176 PASS.)

Uji baru pada kartu ini:

- `internal/httpapi/router_profile_test.go` (4 uji, in-memory): CSRF wajib,
  normalisasi spasi nama, email/role tidak berubah, `email`/`role`/field asing
  ditolak 400, nama terlalu pendek/hanya spasi ditolak, manager boleh mengganti
  nama sendiri, akun cabang 403, tanpa sesi 401.
- `internal/httpapi/integration_profile_pg_test.go` (2 uji, PostgreSQL nyata):
  nama tersimpan di `admin_users` sementara email & role di database tidak
  berubah; sesi LAIN milik akun yang sama langsung melihat nama baru; akun cabang
  403 dan nama cabang di DB tidak berubah; `?branch_id=` cabang lain diabaikan;
  `PUT`/`PATCH`/`POST /me/branch` → 405; admin → 403.
- `openapi_contract_test.go` tetap lulus dua arah setelah `PUT /auth/me` dan
  catatan read-only ditambahkan ke kontrak.

Frontend (`web/`):

```
npm run typecheck   -> 4 workspace exit 0
npm run test        -> 103 lulus (api-client 7, ui 56, admin 29, customer 11)
npm run build       -> dua SPA sukses; aset lokal
                       dist/assets/logo-grahita-mark-128-*.png 15,58 kB
                       dist/assets/logo-grahita-lockup-320-*.png 71,02 kB
sh scripts/check-secrets.sh -> exit 0
```

Suite UI baru: `packages/ui/src/components/profile.test.tsx` (12 uji: Logo/Brand,
ChangePasswordForm, PasswordResetPage, aturan kata sandi), `apps/customer/src/profil.test.tsx`
(5 uji), `apps/admin/src/profil.test.tsx` (4 uji). Uji customer juga menegaskan
semua panggilan API **relatif** (`/api/v1/…`) sehingga cookie sesi tetap host-only
pada host portal yang sedang dibuka.

### Probe pada binary hidup (API nyata + PostgreSQL)

`bash probe-f110.sh` (DB `grahita_f110_live`, binary `--mode=api` pada `:8088`,
kedua SPA dev server dengan proxy `/api` same-origin):

```
login_cabang=200
PUT /auth/me sebagai cabang          -> 403 {"code":"FORBIDDEN","message":"Profil ini dikelola admin Pusat…"}
PUT /me/branch                       -> 405 {"code":"METHOD_NOT_ALLOWED"}
PUT /auth/me dengan field email      -> 400 {"code":"VALIDATION_FAILED","fields":{"body":"json: unknown field \"email\""}}
PUT /auth/me nama (admin Manager)    -> 200 {"nama":"Budi Santoso (Manager)","email":"budi@grahitaindonesia.com","role":"manager"}
```

Alur ganti kata sandi cabang lewat UI (dua sesi paralel; sesi kedua dibuat lewat
API sebelum perubahan):

```
GET /auth/me dengan sesi LAIN setelah ganti -> 401   (sesi lain dicabut)
login kata sandi lama                       -> 401
login kata sandi baru                       -> 200
```

Alur lupa/atur ulang admin (`/lupa` portal admin, tautan disimulasikan dengan
menyisipkan token SHA-256 ke `password_reset_tokens` karena SMTP lokal belum
dikonfigurasi — `mailer.Log` tidak mencetak tautan):

```
/lupa?token=GRH-ADMIN-UJI-1234 -> halaman "Buat kata sandi baru"; simpan -> pesan sukses
GET /auth/me sesi admin lama setelah reset -> 401 (seluruh sesi dicabut)
login kata sandi lama -> 401; login kata sandi baru -> 200
/lupa (tanpa token) + email terdaftar -> pesan seragam "Bila email terdaftar, …"
```

Rute `/lupa` portal admin memang harus ada di host admin: `auth.NewEmailResetMailer`
menyusun tautan `${ADMIN_HOST}/lupa?token=…` untuk `KindAdmin` dan
`${CUSTOMER_HOST}/lupa?token=…` untuk `KindBranch` (dijaga
`TestEmailResetMailerMenyusunTautanPortal`, dan `PASSWORD_RESET_PATH` tetap jadi
satu-satunya sumber rute).

### Pengukuran UI di browser nyata (Chrome, dist/dev server + API nyata)

Portal cabang `/profil` (desktop 1280×900 dan mobile 390×844):

```
field   : nama "Grahita Bandung", kepala "Dra. Sri Wahyuni",
          email "bandung@grahitaindonesia.com", telepon "0812-3456-7890" — semuanya readOnly=true
badge   : "Aktif", "Cash & Carry"
logo    : lockup natural 320×289 -> tampil 62×56 (proporsi sama), file logo-grahita-lockup-320.png
          mark shell natural 128×133 -> tampil 29×30
mobile  : overflow scrollWidth == clientWidth (390), sidebar display:none,
          bottom-nav grid 5 kolom (71,59px ×5) tinggi 62px, input 318×40
desktop : overflow 1265 == 1265, aside 240px
```

Portal admin `/profil` (desktop):

```
field   : nama "Budi Santoso (Manager)" (readOnly=false), email (readOnly=true),
          role "Manager" (readOnly=true)
simpan  : notice "Nama profil tersimpan.", sidebar langsung memakai nama baru,
          GET /auth/me di server -> nama baru, email & role tidak berubah
logo    : lockup tampil 62×56 (natural 320×289)
```

Tangkapan layar (artefak kartu):
`customer-profil-desktop.png`, `customer-profil-mobile.png`,
`admin-profil-desktop.png` (lihat lampiran kartu).

## Berkas yang diubah/ditambah

Backend: `api/db/queries/user.sql` (+`UpdateAdminNama`), `api/db/queries/branch.sql`
(`COALESCE(telepon)`), `api/internal/db/sqlc/{user,branch}.sql.go` (hasil generate),
`api/internal/auth/{profile.go,service.go,memory.go}`,
`api/internal/auth/postgres/postgres.go`, `api/internal/catalog/{catalog.go,postgres/postgres.go}`,
`api/internal/httpapi/{handlers_auth.go,router.go,errors.go}`,
`api/internal/httpapi/router_profile_test.go` (baru),
`api/internal/httpapi/integration_profile_pg_test.go` (baru),
`openapi/openapi.yaml`.

Frontend: `web/packages/ui/src/assets/logo-grahita-{source,lockup-320,mark-128}.png` (baru),
`web/packages/ui/src/components/{Logo.tsx,Logo.module.css,Brand.tsx,Brand.module.css,AuthLayout.tsx,AuthLayout.module.css,ChangePassword.tsx,PasswordReset.tsx,profile.test.tsx}`,
`web/packages/ui/src/password.ts` (baru), `web/packages/ui/src/index.ts`,
`web/apps/customer/src/{routes.tsx,pages/auth.tsx,pages/shell-pages.tsx,profil.test.tsx}`,
`web/apps/admin/src/{routes.tsx,session.tsx,pages/auth.tsx,pages/shell-pages.tsx,profil.test.tsx}`.

Dokumentasi: `README.md`, `docs/design/f1-10/HASIL.md` (berkas ini).

## Catatan non-pemblokir & risiko residu

- **`PUT /auth/me` bukan permukaan SystemDesign §6.** Rasionale ada di atas;
  bila CEO lebih suka jalur user management (Superadmin saja), cukup satu
  perubahan kecil di halaman profil admin + hapus handler.
- **Tidak ada entri audit untuk ganti nama sendiri.** `audit_logs` di kartu ini
  belum menyentuh `auth.Service` (audit mutasi admin ditulis `users.Service`);
  perubahan nama profil belum tercatat. Kandidat kartu lanjutan bila dianggap
  perlu jejak audit untuk aksi self-service.
- **Uji integrasi memakai PG16 + `pg-dev-shim`** (bukan `postgres:18-alpine`),
  sama seperti F1-02…F1-09. `golangci-lint`/`redocly` tidak tersedia di sesi ini
  (redocly tidak dijalankan; konsistensi kontrak dijaga uji dua arah).
- **Aset asli 515 KB ikut masuk repo** sebagai sumber; hanya turunannya yang
  masuk bundel produksi (15,6 KB + 71 KB). Bila ukuran repo mengganggu, berkas
  sumber bisa dipindah keluar `src/` tanpa mengubah kode.
- **Verifikasi browser memakai `localhost` untuk kedua portal**, sehingga cookie
  sesi secara teknis dibagi antar port (perilaku cookie: port diabaikan).
  Pemisahan host tetap diuji di Go: `Set-Cookie` tanpa atribut `Domain` dan
  `me` 401 di host lain (F1-04, `router_auth_test.go`).
- **Token reset pada probe browser disuntikkan langsung ke DB**; jalur
  `forgot → email → tautan` tidak bisa dijalankan ujung-ke-ujung di lingkungan
  lokal karena SMTP tidak dikonfigurasi (`mailer.Log`). Penyusunan tautan per
  portal tetap diuji unit dan route `/lupa` admin kini benar-benar ada.
- **Halaman `/gaya` dan kerangka halaman lain** (katalog, pesanan, invoice)
  tetap seperti F1-03/F1-06b/F1-07b; kartu ini tidak menyentuhnya.
