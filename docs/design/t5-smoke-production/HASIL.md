# Smoke production berakun UJI — 2026-10-16

Dokumen ini mencatat provisioning dan hasil smoke untuk kartu `t_500b8f98`.
Istilah “staging” pada nama `scripts/smoke-staging.sh` bersifat historis. Sesuai
keputusan pemilik 2026-10-16, target nyata hanya production:

- `https://grahita-branch.tiusclaw.com`
- `https://grahita-admin.tiusclaw.com`

## Kendali rahasia

Kredensial tidak ditulis di repo, log, kartu, atau argumen proses. Runner membaca
berkas lokal mode privat:

`/home/hermes/hermes-dev-data/secrets/grahita-uji.env`

Nama variabel yang dipakai:

- `GRAHITA_ADMIN_URL`, `GRAHITA_BRANCH_URL`
- `GRAHITA_UJI_ADMIN_EMAIL`, `GRAHITA_UJI_ADMIN_PASSWORD`
- `GRAHITA_UJI_BRANCH_EMAIL`, `GRAHITA_UJI_BRANCH_PASSWORD`

Nilai akun cabang dibuat melalui alur aplikasi resmi: Superadmin membuat cabang,
menerbitkan token registrasi sekali pakai, lalu endpoint publik
`/api/v1/auth/registration/validate` dan `/api/v1/auth/register` membuat akun.
Token mentah hanya berada di memori proses provisioning dan tidak disimpan.

## Data UJI yang dibuat

Semua master data diberi penanda `UJI-` dan hanya digunakan untuk pengujian:

- cabang: `UJI-Smoke Production 20261016`
- email cabang uji: `uji-smoke-20261016@grahita.test`
- produk: `UJI-Produk Smoke 20261016` (`UJI-SMOKE-20261016`)
- rekening: `UJI-Rekening Smoke 20261016`
- harga khusus cabang: Rp161.000
- assignment rekening: rekening UJI di atas ke cabang UJI
- pesanan smoke pertama: nomor `2026-10-01`, total Rp322.000, status akhir `done`

Nomor rekening, kata sandi, token registrasi, cookie, dan UUID tidak dicatat di
dokumen ini.

## Prosedur provisioning aman

1. Muat kredensial UJI-Superadmin hanya dari secret env lokal.
2. Login ke host admin dan pastikan role respons adalah `superadmin`.
3. Inventarisasi berdasarkan label/kode UJI sebelum membuat data agar retry tidak
   menggandakan master data.
4. Buat cabang aktif beserta identitas lengkap, produk reguler aktif, harga
   khusus cabang, rekening aktif, dan assignment rekening melalui API same-origin.
5. Terbitkan token registrasi sekali pakai. Jangan menulis token ke stdout atau
   berkas.
6. Daftarkan akun cabang melalui host cabang dan simpan kredensial baru hanya ke
   secret env lokal dengan permission `0600`.
7. Verifikasi login cabang sebelum menjalankan smoke.
8. Jalankan runner dengan pemetaan environment berikut di dalam wrapper lokal
   yang tidak dikomit:

       CUSTOMER_BASE=$GRAHITA_BRANCH_URL
       ADMIN_BASE=$GRAHITA_ADMIN_URL
       SMOKE_EMAIL=$GRAHITA_UJI_BRANCH_EMAIL
       SMOKE_PASSWORD=$GRAHITA_UJI_BRANCH_PASSWORD
       SMOKE_ADMIN_EMAIL=$GRAHITA_UJI_ADMIN_EMAIL
       SMOKE_ADMIN_PASSWORD=$GRAHITA_UJI_ADMIN_PASSWORD
       sh scripts/smoke-staging.sh

Jangan memakai `-k`, `SMOKE_CURL_OPTS`, basic auth, data pelanggan, atau akun
pemilik.

## Hasil nyata production

Provisioning selesai dengan seluruh mutasi wajib HTTP 200/201: cabang, produk,
harga, rekening, assignment, token registrasi, registrasi akun, serta verifikasi
login cabang.

`scripts/smoke-staging.sh` dijalankan terhadap kedua hostname production dan
menghasilkan exit `2`:

- LULUS: UI dan API same-origin kedua host, HTTPS tervalidasi tanpa bypass,
  header keamanan, login cabang, atribut cookie, katalog/harga server, draft,
  submit C&C, isolasi cookie lintas host, login Superadmin, dan verifikasi sampai
  status `done`.
- TERHALANG: daftar dokumen tetap kosong, sehingga unduh dokumen belum dapat
  dibuktikan. Runner mengidentifikasi ketergantungan render/object storage
  (Gotenberg/S3) belum tersedia pada layanan nyata.

Probe live terpisah untuk batas role dan CSRF menghasilkan exit `0`:

- akun cabang ke `GET /api/v1/admin/branches` → HTTP 403 `FORBIDDEN`;
- POST cabang tanpa token CSRF → HTTP 403 `CSRF_INVALID`;
- akun Superadmin ke endpoint rekening efektif cabang → HTTP 403 `FORBIDDEN`;
- POST Superadmin tanpa token CSRF → HTTP 403 `CSRF_INVALID`.

Kesimpulan: blocker akun uji sah dan master data telah ditutup. Alur
login→submit→verifikasi terbukti live, tetapi gerbang login→submit→verifikasi→unduh
belum LULUS penuh karena dokumen tidak terbentuk. Hasil historis T5-ulang2 tidak
diubah dan rilis tidak dinyatakan siap hanya berdasarkan hasil ini.

## Cleanup setelah pengujian independen

Jangan cleanup sebelum kartu pengujian turunan selesai karena tester memerlukan
akun yang sama. Setelah tester selesai:

1. Login sebagai UJI-Superadmin dari secret env.
2. Nonaktifkan cabang `UJI-Smoke Production 20261016`; tindakan ini mencabut sesi
   dan akses akun cabang.
3. Nonaktifkan produk `UJI-Produk Smoke 20261016`. Produk tidak dihapus karena
   sudah direferensikan pesanan smoke.
4. Kosongkan assignment rekening cabang dengan optimistic version terbaru, lalu
   nonaktifkan rekening `UJI-Rekening Smoke 20261016` bila tidak dipakai data UJI
   lain.
5. Pertahankan pesanan dan audit trail sebagai bukti historis; jangan menghapus
   langsung dari database.
6. Hapus dua variabel kredensial cabang dari secret env bila tidak ada smoke
   lanjutan, atau rotasi kata sandinya lewat alur aplikasi.
7. Catat status cleanup pada kartu tanpa menyalin rahasia, nomor rekening, cookie,
   token, atau payload autentikasi.
