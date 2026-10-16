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
- akun cabang uji: alias `UJI-Cabang Smoke 20261016` (alamat hanya di secret env)
- produk: `UJI-Produk Smoke 20261016` (`UJI-SMOKE-20261016`)
- rekening: `UJI-Rekening Smoke 20261016`
- harga khusus cabang: Rp161.000
- assignment rekening: rekening UJI di atas ke cabang UJI
- pesanan smoke: nomor `2026-10-01`, `2026-10-02`, dan `2026-10-03`,
  masing-masing total Rp322.000 dengan status akhir `done`

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

`scripts/smoke-staging.sh` versi awal dijalankan terhadap kedua hostname
production dua kali dan masing-masing menghasilkan exit `2`:

- LULUS: UI dan API same-origin kedua host, HTTPS tervalidasi tanpa bypass,
  header keamanan, login cabang, atribut cookie, katalog/harga server, draft,
  submit C&C, isolasi cookie lintas host, login Superadmin, dan verifikasi sampai
  status `done`.
- TERHALANG pada pemeriksaan langsung runner: daftar dokumen masih kosong segera
  setelah verifikasi. Pesan runner lama yang menyatakan Gotenberg/S3 tidak ada
  bukan diagnosis dan tidak dipakai sebagai bukti penyebab.

Pemeriksaan lanjutan pada pesanan kedua memberi waktu 60 detik untuk pekerjaan
asinkron. Hasilnya:

- proses production untuk API dan worker terlihat aktif; proses renderer dan
  object storage juga terlihat aktif, tanpa membaca konfigurasi atau rahasia;
- setelah waktu tunggu tambahan, endpoint daftar dokumen mengembalikan satu
  dokumen invoice;
- endpoint unduh dokumen berhasil menerbitkan tautan bertanda tangan, tetapi
  host pada tautan itu tidak dapat di-resolve dari klien production. Nilai URL,
  kunci objek, query signature, dan hostname internal tidak dicatat.

Dengan demikian, job render/worker bukan lagi penyebab yang dapat dinyatakan
terhalang: dokumen terbentuk. Gerbang tersisa berada pada keterjangkauan tautan
unduh object storage dari klien. Operator perlu menyediakan endpoint S3 yang
dapat di-resolve dan dijangkau pengguna untuk presign, sambil tetap dapat
dijangkau proses API/worker, lalu mengulang unduh tanpa membocorkan URL mentah.

Runner juga dikoreksi agar menunggu dokumen asinkron sampai batas waktu,
mengambil URL dari respons JSON endpoint unduh, lalu mengunduh object URL itu.
Sebelumnya runner keliru mengukur ukuran respons JSON endpoint sebagai berkas.
Pengulangan ketiga dengan runner terkoreksi membentuk dokumen dalam 5 detik,
kemudian menghasilkan exit `1` saat object URL tidak dapat di-resolve. Tidak ada
URL mentah, hostname internal, signature, UUID, atau rahasia yang dicatat.

Probe live terpisah untuk batas role dan CSRF menghasilkan exit `0`:

- akun cabang ke `GET /api/v1/admin/branches` → HTTP 403 `FORBIDDEN`;
- POST cabang tanpa token CSRF → HTTP 403 `CSRF_INVALID`;
- akun Superadmin ke endpoint rekening efektif cabang → HTTP 403 `FORBIDDEN`;
- POST Superadmin tanpa token CSRF → HTTP 403 `CSRF_INVALID`.

Kesimpulan: blocker akun uji sah dan master data telah ditutup. Alur
login→submit→verifikasi dan pembentukan dokumen terbukti live, tetapi gerbang
login→submit→verifikasi→unduh belum LULUS penuh karena tautan object storage
belum dapat dijangkau klien. Hasil historis T5-ulang2 tidak diubah dan rilis
tidak dinyatakan siap hanya berdasarkan hasil ini.

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
