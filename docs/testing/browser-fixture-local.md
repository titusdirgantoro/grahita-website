# Fixture sesi browser lokal

Fixture ini dipakai hanya untuk pengujian browser terhadap API dan PostgreSQL sintetis lokal. Tujuannya memberi browser sesi Superadmin, Manager, atau cabang tanpa mengetik maupun menampilkan password, token sesi, cookie, atau connection string di tool browser.

## Pengaman

- Route hanya didaftarkan bila `APP_ENV=local` dan `BROWSER_FIXTURE_ENABLED=true`.
- Validasi konfigurasi menolak fixture pada environment selain `local`, menolak cookie `Secure` untuk HTTP lokal, dan mewajibkan password internal.
- Browser tidak mengirim password. Handler mengambil password dari environment proses lalu memakai `auth.Service.Login` normal, sehingga pemeriksaan akun aktif, penerbitan sesi PostgreSQL, cookie host-only, dan CSRF tetap sama dengan login biasa.
- Role dan CSRF endpoint produk tidak diubah. Profil fixture juga diverifikasi terhadap role akun setelah login.
- Route production tetap 404 walaupun `Config` dibentuk manual dengan flag fixture aktif.

## Prasyarat

1. Gunakan PostgreSQL sintetis, bukan production.
2. Migrasikan database dan bangun binary:

       make build
       DATABASE_URL=<dsn-sintetis> ./bin/server --mode=migrate

3. Jalankan dua Vite dev server pada terminal terpisah:

       npm --prefix web run dev:customer
       npm --prefix web run dev:admin

4. Jalankan API fixture pada terminal lain. Berikan `DATABASE_URL` melalui environment tanpa mencetak nilainya:

       APP_ENV=local DATABASE_URL=<dsn-sintetis> ./scripts/browser-fixture-local.sh

Skrip membuat password acak sementara, menjalankan seed dengan password tersebut, lalu menjalankan API dengan fixture aktif. Nilai password tidak dicetak atau ditulis ke repo.

Bila binary berada di lokasi lain, set `GRAHITA_SERVER`. Email profil dapat dioverride dengan `BROWSER_FIXTURE_SUPERADMIN_EMAIL`, `BROWSER_FIXTURE_MANAGER_EMAIL`, dan `BROWSER_FIXTURE_BRANCH_EMAIL`; bawaan cocok dengan akun seed.

## Membuka sesi

Buka URL berikut langsung melalui browser. Respons menerbitkan cookie sesi dan CSRF lalu redirect ke root portal:

- Superadmin: `http://localhost:5174/api/v1/test/browser-session/superadmin`
- Manager: `http://localhost:5174/api/v1/test/browser-session/manager`
- Cabang sintetis: `http://localhost:5173/api/v1/test/browser-session/branch`

Cookie browser tidak terikat port. Bila admin dan customer sama-sama memakai host `localhost`, membuka profil baru mengganti sesi profil sebelumnya. Ini sengaja aman untuk pengujian berurutan. Untuk sesi paralel, gunakan browser context terpisah atau host loopback berbeda yang diarahkan ke masing-masing Vite server.

Setelah membuka fixture, verifikasi UI memanggil `/api/v1/auth/me` dan endpoint bisnis nyata melalui proxy Vite ke API dan PostgreSQL sintetis. Mutasi non-GET tetap wajib membawa header CSRF yang dibaca api-client dari cookie `grahita_csrf`.

## Storage dan upload

Fixture sesi tidak menyalakan atau memalsukan object storage. Jika `S3_ENDPOINT`/`S3_BUCKET` lokal tidak tersedia, skenario upload nyata tetap berstatus TERHALANG dan tidak boleh diklaim PASS. Jika MinIO sintetis tersedia, isi konfigurasi S3 lokal pada proses API sebelum menjalankan skrip.

## Sanitasi dan cleanup

1. Hentikan API fixture dan dua Vite dev server.
2. Hapus database sintetis atau cabut seluruh sesi/data `UJI-` sesuai mekanisme runner.
3. Pastikan tidak ada nilai `BROWSER_FIXTURE_PASSWORD`, `SEED_PASSWORD`, cookie, token, atau DSN yang masuk ke log, komentar kartu, screenshot, atau commit.
4. Jalankan bukti guard production:

       cd api
       go test ./internal/platform/config ./internal/httpapi -run BrowserFixture

Expected: konfigurasi production menolak flag fixture dan router production menjawab 404 untuk route fixture.
