# Fixture sesi browser lokal

Fixture ini hanya untuk pengujian browser terhadap API dan PostgreSQL sintetis lokal. Browser memperoleh sesi Superadmin, Manager, atau cabang tanpa mengetik maupun menampilkan password, token sesi, cookie, connection string, atau kunci proxy.

## Model ancaman dan pengaman

Endpoint fixture dapat menerbitkan sesi berprivilege, sehingga `APP_ENV=local` saja bukan batas keamanan. Fixture memakai seluruh pengaman berikut secara bersamaan dan gagal start bila salah satunya tidak terpenuhi:

- API wajib bind ke IP loopback eksplisit (`127.0.0.1`/`::1`), bukan `:8080`, `0.0.0.0`, hostname, IP LAN, atau interface publik.
- DSN wajib PostgreSQL pada IP loopback eksplisit dan nama database wajib diawali `grahita_browser_fixture_`.
- Database wajib memiliki tabel marker yang hanya dapat dibuat oleh mode `--prepare-database` ketika database benar-benar kosong. API memverifikasi marker itu lagi sebelum mendaftarkan layanan fixture. Nama database saja tidak dianggap bukti.
- Launcher menjalankan kedua Vite dev server pada `127.0.0.1`. Proxy menyuntikkan kunci acak sementara ke header request; kunci tidak tersedia bagi JavaScript/browser dan tidak dicetak.
- Handler hanya menerima Host origin loopback yang dikonfigurasi. Navigasi address bar harus memiliki Fetch Metadata `none`; navigasi dalam portal harus `same-origin` dengan Referer/Origin yang cocok. Navigasi `cross-site`, Host palsu, akses API langsung, atau proxy tanpa kunci disamarkan sebagai 404 dan tidak menerbitkan cookie.
- Browser tidak mengirim password. Handler memakai password acak internal untuk menempuh `auth.Service.Login` normal, lalu memverifikasi role akun. Pemeriksaan akun aktif, sesi PostgreSQL, cookie host-only, dan CSRF sama dengan login biasa.
- Route hanya didaftarkan ketika `APP_ENV=local` dan flag aktif. Konfigurasi production menolak flag, router production tetap 404, dan role/CSRF endpoint produk tidak diubah.

Reverse proxy lokal tidak dipercaya hanya karena `RemoteAddr` terlihat loopback; kunci proxy dan bukti origin tetap wajib. Jangan menaruh proxy lain di depan Vite fixture atau mengekspos port 5173/5174/8088 ke jaringan.

## Prasyarat dan persiapan database

1. Buat database PostgreSQL kosong pada listener loopback. Namanya wajib diawali `grahita_browser_fixture_`. Jangan memakai database production, salinan production, tunnel ke server remote, atau database yang sudah berisi tabel aplikasi.
2. Bangun binary:

       make build

3. Tandai database kosong. Langkah ini memeriksa alamat server aktual, nama database aktual, dan jumlah relasi sebelum menulis marker:

       APP_ENV=local DATABASE_URL=<dsn-127.0.0.1-ke-database-kosong> ./scripts/browser-fixture-local.sh --prepare-database

   Perintah gagal bila server bukan loopback, nama tidak sesuai, atau database tidak kosong. Jangan mencetak atau menyalin DSN ke log/kartu.

4. Jalankan fixture menggunakan DSN yang sama:

       APP_ENV=local DATABASE_URL=<dsn-yang-sama> ./scripts/browser-fixture-local.sh

Launcher memverifikasi marker sebelum migrasi dan seed, memasang shim UUID PostgreSQL lokal yang idempoten, membuat password serta kunci proxy acak, menjalankan migrasi+seed, menjalankan Vite customer/admin pada loopback, lalu menjalankan API pada `127.0.0.1:8088`. Password dan kunci tidak dicetak atau ditulis ke repo. Email profil dapat dioverride melalui `BROWSER_FIXTURE_SUPERADMIN_EMAIL`, `BROWSER_FIXTURE_MANAGER_EMAIL`, dan `BROWSER_FIXTURE_BRANCH_EMAIL`; bawaan cocok dengan akun seed.

## Membuka sesi

Buka salah satu URL yang dicetak launcher langsung dari address bar browser:

- Superadmin: `http://127.0.0.1:5174/api/v1/test/browser-session/superadmin`
- Manager: `http://127.0.0.1:5174/api/v1/test/browser-session/manager`
- Cabang sintetis: `http://127.0.0.1:5173/api/v1/test/browser-session/branch`

Respons menerbitkan cookie sesi dan CSRF lalu redirect ke root portal. Cookie browser tidak terikat port, sehingga membuka profil baru pada host `127.0.0.1` mengganti sesi sebelumnya. Uji profil secara berurutan atau gunakan browser context terpisah.

Verifikasi UI memanggil `/api/v1/auth/me` dan endpoint bisnis nyata melalui Vite → API → PostgreSQL sintetis. Mutasi non-GET tetap wajib membawa header CSRF dari cookie `grahita_csrf`; Manager dan cabang tetap dibatasi role normal.

## Bukti negatif yang wajib

- `HTTP_ADDR=:8080`, `0.0.0.0:8080`, hostname, atau IP LAN ditolak saat konfigurasi fixture aktif.
- DSN remote, nama database non-fixture, database tanpa marker, dan marker salah ditolak sebelum API fixture melayani request.
- Host/Origin/Referer tidak tepercaya, `Sec-Fetch-Site: cross-site`, akses API langsung, atau kunci proxy hilang/salah menghasilkan 404 tanpa cookie sesi.
- `APP_ENV=production` dengan flag fixture aktif gagal start; router production tidak mendaftarkan route fixture.
- Login hasil fixture tetap menghasilkan cookie normal, `/auth/me` sesuai role, mutasi tanpa CSRF tetap 403, dan endpoint role-terlarang tetap 403.

## Storage dan upload

Fixture sesi tidak menyalakan atau memalsukan object storage. Jika object storage sintetis lokal tidak tersedia, skenario upload nyata tetap TERHALANG dan tidak boleh diklaim PASS.

## Sanitasi dan cleanup

1. Hentikan launcher; trap menghentikan API serta kedua Vite dev server dan menghapus log runtime sementara.
2. Hapus database `grahita_browser_fixture_*` yang dipakai. Jangan memakai marker database itu untuk pekerjaan lain.
3. Pastikan tidak ada `BROWSER_FIXTURE_PASSWORD`, `BROWSER_FIXTURE_PROXY_KEY`, `SEED_PASSWORD`, cookie, token, atau DSN di log, screenshot, komentar kartu, atau commit.
4. Jalankan tes guard:

       cd api
       go test ./internal/platform/config ./internal/httpapi -run BrowserFixture -count=1

5. Jalankan gerbang repository yang relevan (`make test`, `make lint`, `make build`, frontend test/typecheck/build, secret scan, dan `git diff --check`).
