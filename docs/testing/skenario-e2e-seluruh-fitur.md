# Skenario pengujian menyeluruh portal Grahita

**Baseline kode:** `9cfae04a` (27 Oktober 2026). **Target:** `https://grahita-branch.tiusclaw.com/` dan `https://grahita-admin.tiusclaw.com/`. Dokumen ini adalah **rencana uji, bukan laporan hasil eksekusi**.

## 1. Ruang lingkup dan cara menjalankan

Skenario diturunkan dari alur UI kedua portal, rute API, service pesanan/pembayaran/piutang/dokumen, worker, serta [PRD](../PRD.md). Jika uraian PRD lama berbeda dengan perilaku implementasi terkini, kontrak kode yang diuji. Contohnya, satu cabang dapat mengizinkan beberapa cara bayar, tetapi **setiap pesanan memilih tepat satu** saat submit; invoice Tempo terbit saat `approved_unpaid`, sedangkan `done` berarti lunas. Periode Tempo mengikuti **tanggal submit WIB**, bukan tanggal approval. Nama instansi adalah atribut **setiap order line**.

Peta implementasi yang menjadi dasar: [rute cabang](../../web/apps/customer/src/routes.tsx), [rute admin](../../web/apps/admin/src/routes.tsx), [router API dan penjaga peran](../../api/internal/httpapi/router.go), [submit pesanan](../../api/internal/order/submit.go), [deposit](../../api/internal/deposit/service.go), [Tempo](../../api/internal/receivable/service.go), [Surat Tagihan dan reminder](../../api/internal/receivable/statement.go), [dokumen cetak](../../api/internal/document/render.go), [reporting](../../api/internal/report/report.go), dan [worker](../../api/internal/worker/worker.go).

Jalankan mutasi bisnis dan simulasi tanggal pada database/storage/email uji yang terisolasi. [Fixture browser lokal](browser-fixture-local.md) menyediakan sesi tiga peran dan storage sintetis; lengkapi dengan renderer PDF, worker dan SMTP uji agar alur dokumen/email benar-benar terbukti. Fixture tersebut bukan bukti lulus produksi. Pada produksi, mulai dari smoke baca saja; uji transaksi `UJI-` hanya dengan akun dan prosedur operasional yang memang dialokasikan untuk QA. Jangan mengubah jam server produksi untuk kasus tanggal.

**Prioritas:** P0 = gerbang rilis (uang, akses, penomoran, dokumen, data); P1 = fungsi utama/edge case; P2 = ketahanan/tampilan. **Lapisan:** UI = dua portal; API = kontrak dan otorisasi; DB = transaksi/invarian; Job = worker, email, PDF, backup.

Setiap hasil uji harus menyimpan: ID skenario, waktu WIB, environment dan commit, akun/peran, input tersanitasi, langkah, respons/status, screenshot UI, data DB/ledger yang relevan, file PDF/XLSX atau hash-nya, email outbox, serta status **PASS/FAIL/BLOCKED**. Jangan simpan cookie, token, password, DSN, atau bukti transfer asli dalam laporan.

### Data awal yang disiapkan

| Kode | Data uji |
|---|---|
| AK | 1 Superadmin aktif, 1 Manager aktif, 1 Manager undangan, 2 akun cabang aktif berbeda, 1 akun cabang nonaktif. Set email uji berbeda untuk flag penerima invoice dan surat jalan. |
| CB | Cabang `UJI-TRF` hanya Transfer; `UJI-DEP` hanya Deposit; `UJI-TMP` hanya Tempo (tanggal bayar 5, tahan bila telat aktif); `UJI-MIX` Transfer + Deposit + Tempo; `UJI-OTHER` untuk pengujian isolasi data. |
| BK | Rekening aktif dan ditugaskan ke cabang; rekening aktif tanpa penugasan; rekening nonaktif; cabang tanpa rekening efektif. |
| PR | Produk reguler A dan B; Sertifikat; produk nonaktif; produk tanpa harga cabang. Harga A pada dua cabang dibuat berbeda, misalnya Rp27.500 dan Rp30.000. Biaya packaging awal Rp3.000. |
| OR | Draft dua baris produk A dengan instansi berbeda (`TK Al-Fatih`, `SD Al-Fatih`) serta satu baris produk B. Siapkan bukti JPG/PNG/PDF sah, file > batas, file berisi MIME palsu, dan bukti identik. |
| TM | Set order Tempo Rp100.000 (O1, lebih tua) dan Rp200.000 (O2); bukti Rp150.000 seharusnya melunasi O1 dan membayar O2 Rp50.000. Siapkan pula periode lintas bulan dan saldo kredit. |
| WKT | Jam uji dapat disuntik/diatur: akhir hari WIB, 31 Desember/1 Januari, tanggal 1/3/5/28, hari Jumat/Sabtu/Senin, H-3/H/H+1/H+7. |

## 2. Perjalanan end to end wajib

Jalankan tiap perjalanan dari portal cabang sampai keluaran akhir; bandingkan angka yang sama pada UI, respons API, DB, dokumen, email, dan laporan.

| ID | P | Perjalanan dan hasil akhir |
|---|---|---|
| E2E-01 | P0 | Superadmin membuat cabang, mengisi satu cara bayar dan rekening, membuat token; cabang registrasi, login, melihat hanya katalog/harga sendiri; token tidak dapat dipakai lagi. |
| E2E-02 | P0 | Cabang Transfer membuat draft tiga baris dengan dua nama instansi berbeda, mengunggah bukti, submit; admin memverifikasi; order `done`, invoice PDF dan Surat Jalan PDF/XLSX terbit, email sesuai flag, ketiga baris dan serial muncul sama pada detail dan laporan. |
| E2E-03 | P0 | Cabang Deposit top-up; hanya Superadmin mengkredit setelah verifikasi; submit memakai saldo dan bukti hanya bila ada kekurangan; admin menyelesaikan settlement; saldo, jurnal, invoice, dan status konsisten. |
| E2E-04 | P0 | Cabang Tempo submit tanpa bukti; Manager menyetujui; order `approved_unpaid`, invoice dan Surat Jalan terbit; pada tanggal 1 berikutnya Surat Tagihan terbit; cabang bayar sebagian, admin verifikasi FIFO, sisa turun; setelah pelunasan order `done`. |
| E2E-05 | P0 | Cabang `UJI-MIX` melihat tiga opsi, mengirim tiga order masing-masing dengan satu opsi. Snapshot cara bayar per order tetap walau izin cabang diubah. Tunggakan Tempo menahan hanya submit Tempo; Transfer dan Deposit masih berfungsi. |
| E2E-06 | P0 | Admin merevisi order selesai dan order Tempo belum lunas, termasuk perubahan instansi per baris; nomor invoice tetap, versi baru dan PDF baru terbit, PDF lama tetap bisa diunduh, total/ledger/tagihan/laporan berubah sesuai delta dan email revisi benar. |
| E2E-07 | P0 | Bukti pelunasan Tempo menutup dua invoice lintas periode; alokasi O1 dahulu lalu O2. Bukti lebih bayar diputuskan Superadmin menjadi kredit Tempo, kredit otomatis membayar invoice Tempo berikutnya dan tidak menambah deposit. |
| E2E-08 | P0 | Bandingkan satu order multi-instansi dari detail cabang dan admin ke PDF invoice, PDF/XLSX Surat Jalan, laporan layar dan ekspor XLSX: jumlah baris, serial, nama instansi, qty, harga snapshot, subtotal dan total identik. |

## 3. Skenario rinci

### A. Akun, sesi, profil, dan hak akses

| ID | P | Lapisan | Aksi / variasi | Hasil yang diharapkan |
|---|---|---|---|---|
| AUT-01 | P0 | UI+API | Login cabang, Manager, Superadmin dengan kredensial sah pada portal masing-masing. | Sesi terbentuk; `/auth/me` sesuai peran; menu dan halaman sesuai hak. |
| AUT-02 | P0 | API | Login dengan email/password salah dan email tidak terdaftar. | Pesan generik; tidak membocorkan keberadaan akun; tidak ada sesi. |
| AUT-03 | P1 | API | Lebihi batas lima percobaan gagal dalam 15 menit per akun/IP. | Percobaan berikutnya dibatasi; login akun lain tidak ikut bocor/terkunci keliru. |
| AUT-04 | P0 | UI+API | Superadmin membuat token cabang valid; cabang validasi token, baca data cabang terkunci, isi telepon/password/konfirmasi, registrasi. | Satu akun terbentuk, data master tidak dapat diubah oleh pendaftar, token sekali pakai. |
| AUT-05 | P0 | API | Token salah, kedaluwarsa, sudah dipakai, tergantikan token baru, cabang nonaktif, dan cabang yang sudah punya akun. | Pendaftaran ditolak dengan kode/pesan sesuai; tidak ada akun ganda. |
| AUT-06 | P1 | UI+API | Password kurang 8 karakter, konfirmasi berbeda, telepon invalid, field wajib kosong. | Validasi klien dan server menolak tanpa membuat akun. |
| AUT-07 | P0 | UI+API+Job | Minta reset untuk email terdaftar dan tidak terdaftar, buka tautan dari email uji, reset password. | Respons permintaan sama; tautan sah sekali pakai dan habis sesuai setting; password baru berlaku. |
| AUT-08 | P0 | API | Pakai tautan reset lama/kedaluwarsa; ulangi setelah sukses. | Ditolak; sesi lama dibatalkan setelah reset. |
| AUT-09 | P1 | UI+API | Cabang/admin ubah password dari Profil; admin ubah nama tampilan. | Butuh password lama, sesi lain keluar; email/role/data master read-only. |
| AUT-10 | P0 | UI+API | Logout dan akses ulang rute privat via URL langsung atau tab lain. | Rute dialihkan ke masuk; API 401; cookie sesi tidak dapat dipakai. |
| AUT-11 | P0 | UI+API | Manager membuka Rekening/Reporting/Pengaturan/Audit/User Admin lewat menu dan URL/API langsung. | Menu tidak tampil dan API menolak 403; nilai rupiah dashboard tidak bocor. |
| AUT-12 | P0 | API | Cabang mengakses ID order, bukti, invoice, surat tagihan, deposit milik cabang lain. | Respons tidak memberi data/tautan cabang lain; tidak ada side effect. |

### B. Master cabang dan pilihan pembayaran

| ID | P | Lapisan | Aksi / variasi | Hasil yang diharapkan |
|---|---|---|---|---|
| CAB-01 | P0 | UI+API | Tambah/edit cabang dengan 1, 2, dan 3 cara pembayaran. | Set pilihan tersimpan, minimal satu opsi wajib; label Transfer/Deposit/Tempo benar. |
| CAB-02 | P0 | UI+API | Aktifkan Tempo; masukkan tanggal bayar 1, 28, lalu 0/29/kosong; ubah toggle tahan. | 1–28 diterima, nilai invalid ditolak; field Tempo muncul hanya ketika relevan. |
| CAB-03 | P0 | API+DB | Dua cabang memakai email sama atau email cabang bertabrakan saat edit. | Unik ditegakkan server/DB; tidak ada master parsial. |
| CAB-04 | P1 | UI+API | Filter/pencarian/status aktif dan detail cabang; ubah kepala cabang, telepon, PI. | Tampilan dan data tersimpan sesuai; flag Special tidak muncul di UI/API/ekspor. |
| CAB-05 | P0 | UI+API | Generate ulang token yang belum dipakai; registrasi dengan token lama dan baru. | Lama tidak valid; baru valid tepat sekali; masa berlaku mengikuti setting. |
| CAB-06 | P0 | UI+API | Nonaktifkan cabang dengan akun/order; coba login dan submit. | Login/aksi cabang ditolak; order historis/dokumen tetap utuh. |
| CAB-07 | P0 | UI+API | Hapus cabang tanpa order dan dengan order. | Yang kosong terhapus; yang punya order ditolak dan diarahkan nonaktifkan. |
| CAB-08 | P0 | UI+API | Ubah cara bayar cabang setelah order telah submit. | Order lama memakai snapshot pilihan; order baru memakai izin baru. |
| CAB-09 | P0 | UI+API | Cabut Tempo dari cabang yang masih punya tagihan; buka Tagihan dan bayar. | Piutang lama tetap dapat dilihat/diselesaikan melalui API; jangan hilangkan akses pembayaran historis. |
| CAB-10 | P1 | UI+API | Matikan/nyalakan `tahan_bila_telat` pada cabang bertunggakan. | Efek submit Tempo segera sesuai toggle; mutasi memiliki aktor/waktu di audit. |

### C. Produk, harga, rekening, katalog cabang

| ID | P | Lapisan | Aksi / variasi | Hasil yang diharapkan |
|---|---|---|---|---|
| MAS-01 | P0 | UI+API | CRUD produk reguler/Sertifikat, ubah urutan dan status aktif. | Katalog cabang hanya memuat produk aktif yang punya harga cabang. |
| MAS-02 | P0 | UI+API | Isi matriks HJM, P1%, SPV; cek angka hasil dengan kalkulasi manual. | Harga server = HJM + HJM×P1 + SPV dalam Rupiah bulat; input tidak sah ditolak. |
| MAS-03 | P0 | UI+API | Hapus sel harga cabang dan coba order produk itu; bandingkan harga A dua cabang. | Produk tanpa sel tidak dijual/dapat diorder; tiap cabang melihat harga sendiri. |
| MAS-04 | P1 | UI+API | Salin harga dari satu cabang ke satu/semua cabang dan edit satu sel sesudahnya. | Sel tujuan cocok; cabang lain dan order lama tidak berubah tanpa aksi eksplisit. |
| MAS-05 | P0 | UI+API+DB | Ubah harga/master setelah draft dibuat tetapi sebelum submit; ubah lagi setelah submit. | Submit memakai harga master terbaru saat itu; order submitted tetap pada snapshot. |
| MAS-06 | P0 | UI+API | Buat/edit/nonaktifkan rekening BCA/BRI; tetapkan per cabang. | Hanya rekening aktif dan diizinkan muncul di instruksi cabang, unggah pelunasan, dan invoice baru. |
| MAS-07 | P0 | API | Cabang tanpa rekening efektif mencoba Transfer, Deposit dengan kekurangan, dan approval Tempo. | Langkah yang butuh tujuan pembayaran ditolak; Deposit yang sepenuhnya tertutup saldo mengikuti aturan yang berlaku. |
| MAS-08 | P0 | API+DB | Cabut rekening tepat saat unggah bukti/submit/verifikasi; re-evaluasi rekening tujuan setelah policy berubah. | Validasi memakai kebijakan efektif terbaru, tidak menerima tujuan tercabut secara diam-diam; keputusan tercatat. |
| MAS-09 | P1 | UI | Katalog: nama, deskripsi, harga, penanda Sertifikat, pencarian/keadaan kosong, mobile. | Informasi konsisten dengan master; tidak ada harga cabang lain. |
| MAS-10 | P1 | DB | Hapus/nonaktifkan produk yang sudah muncul pada order lama. | Snapshot order, invoice, laporan historis tetap dapat dibaca. |

### D. Draft, baris instansi, submit, dan penomoran

| ID | P | Lapisan | Aksi / variasi | Hasil yang diharapkan |
|---|---|---|---|---|
| ORD-01 | P0 | UI+API | Buat draft 3 baris: produk sama dengan dua instansi berbeda, produk lain dengan instansi ketiga; simpan/edit/hapus satu baris. | Satu order berisi baris terpisah, masing-masing menyimpan `nama_instansi`; total live = jumlah subtotal. |
| ORD-02 | P0 | UI+API | Nama instansi kosong/spasi, 160/161 karakter, karakter Unicode, apostrof, tanda `&`, teks mirip HTML; qty 0/negatif/terlalu besar. | Batas validasi sesuai kontrak; teks disimpan/ditampilkan aman tanpa eksekusi HTML; nilai uang tidak overflow. |
| ORD-03 | P0 | UI+API | Draft tanpa produk, produk nonaktif/tanpa harga, campur Sertifikat-reguler. | Submit ditolak atau UI mencegah campuran dengan penjelasan; nomor belum dialokasikan. |
| ORD-04 | P0 | UI+API | Pilih beberapa Sertifikat untuk instansi berbeda. | Harga Sertifikat Rp0, tepat satu baris packaging per order, packaging tidak bisa dihapus. |
| ORD-05 | P0 | UI+API | Ubah biaya packaging setelah draft Sertifikat, lalu setelah submit. | Nilai saat submit yang dipakai; order lama tidak berubah. |
| ORD-06 | P0 | UI+API | Save, ubah, hapus draft; buka ulang browser. | Data draft persisten; order lain tidak berubah; draft boleh dihapus. |
| ORD-07 | P0 | UI+API+DB | Submit Transfer dengan ≥1 bukti sah, tanpa bukti, dan setelah bukti lebih dari satu. | Hanya yang berbukti berhasil; status `submitted`, nomor dan tanggal WIB baru saat submit. |
| ORD-08 | P0 | UI+API+DB | Submit Deposit saldo cukup, saldo kurang, saldo nol. | Saldo dipakai hingga min(total,saldo); bukti hanya wajib untuk kekurangan; jurnal tepat sekali. |
| ORD-09 | P0 | UI+API+DB | Submit Tempo tanpa bukti. | Berhasil bila diizinkan/tidak ditahan, status `submitted`; belum ada dokumen hingga disetujui. |
| ORD-10 | P0 | UI+API | Cabang tiga mode tidak memilih mode lalu submit; memilih mode yang tidak lagi diizinkan; mengirim dua mode sekaligus lewat API. | Server menolak pilihan kosong/invalid; tiap order hanya satu snapshot `transfer`/`deposit`/`tempo`. |
| ORD-11 | P0 | API+DB | Jalankan submit ulang/klik ganda dan 100 submit paralel lintas cabang. | Satu transisi per order, invoice `YYYY-MM-XX` unik global per bulan, serial `YYYYMMDDXXX` unik per baris per hari, tanpa nomor hilang karena transaksi gagal. |
| ORD-12 | P0 | API+DB | Submit pada 23:59:59 dan 00:00:00 WIB serta pergantian tahun. | Tanggal, awalan serial, dan nomor invoice mengikuti hari/bulan/tahun WIB; counter reset pada periodenya. |
| ORD-13 | P0 | UI+API | Setelah submit, coba edit/hapus via UI dan request langsung. | Ditolak; nama instansi, serial, qty, harga tetap kecuali lewat revisi admin. |
| ORD-14 | P1 | UI | Daftar/detail/filter status, timeline submit, nama instansi/serial dan label cara bayar. | Data sesuai order; navigasi kembali ke draft/riwayat berfungsi. |
| ORD-15 | P0 | API | Cabang B mengedit/submit/upload ke draft atau order cabang A memakai ID tebak. | Ditolak tanpa membocorkan data; penomoran/ledger tidak berubah. |
| ORD-16 | P1 | UI+API | Admin unggah bukti transfer tambahan atas nama cabang setelah submit. | Bukti tampil dengan nama pengunggah dan timeline, tidak menjadi milik admin. |
| ORD-17 | P0 | API | Unggah JPG/PNG/PDF sah dan berkas dengan ekstensi/MIME palsu, kosong, melebihi ukuran/jumlah. | Hanya tipe/ukuran/jumlah sesuai batas diterima; file gagal tidak meninggalkan referensi DB/objek aktif. |
| ORD-18 | P1 | UI | Beranda cabang setelah draft, submitted, approved_unpaid, done, dan tunggakan. | Hitungan/shortcut/pesanan terakhir/banner sesuai status nyata; tidak menampilkan uang milik cabang lain. |
| ORD-19 | P1 | UI+PDF+XLSX | Buka order historis yang dibuat sebelum `nama_instansi` diwajibkan. | Detail/dokumen/laporan tetap terbaca; instansi kosong tampil sebagai `—`, bukan error atau data buatan. |

### E. Verifikasi Transfer, rekening tujuan, dan OCR opsional

| ID | P | Lapisan | Aksi / variasi | Hasil yang diharapkan |
|---|---|---|---|---|
| VER-01 | P0 | UI+API+DB | Manager memeriksa dua bukti Transfer dan menandai nominal/cocok berikut rekening tujuan efektif. | Hanya nominal yang disetujui masuk settlement; order `done`, log verifikasi, timeline, audit, job dokumen tercipta satu kali. |
| VER-02 | P0 | UI+API | Nominal kurang, lebih, tidak terbaca, rekening salah: pilih `perlu_review` dengan alasan. | Order tetap `submitted`, substatus/alasan terlihat; dokumen/email belum terbit. |
| VER-03 | P0 | API | `perlu_review` tanpa alasan, alasan di luar daftar, `cocok` sambil mengirim alasan review, angka negatif/desimal. | Ditolak dengan validasi; tidak ada perubahan status/ledger. |
| VER-04 | P0 | API+DB | Verifikasi cocok dua kali/paralel dengan kunci idempotensi sama dan berbeda. | Tidak ada pembayaran/dokumen/email ganda; replay atau konflik sesuai kontrak. |
| VER-05 | P0 | API+DB | Bukti yang sama dicoba dipakai untuk dua keputusan ekonomi/order; `approved_nominal` tidak sama dengan jumlah `applied_to_order` + `credited_to_deposit_total`. | Ditolak; tidak ada kredit ganda atau uang tercipta. |
| VER-06 | P0 | UI+API | Bukti diarahkan ke rekening aktif tapi tidak diizinkan untuk cabang, rekening dinonaktifkan, atau assignment berubah saat pemeriksaan. | Verifikasi tidak lolos diam-diam; tampil status tujuan dan jalur review/evaluasi ulang. |
| VER-07 | P1 | UI+API | Admin meninjau bukti dengan preview/zoom, menambah catatan internal, melihat log verifikasi/timeline. | Berkas terbaca; catatan internal tidak tampak di portal cabang. |
| VER-08 | P0 | Job+DB | AI diaktifkan, bukti jelas dengan nominal, tanggal, bank, rekening dan keyakinan melewati ambang. | Job OCR hanya untuk order Transfer/Deposit, hasil dan bukti keputusan terekam; auto-approve hanya bila seluruh syarat terpenuhi. |
| VER-09 | P0 | Job+DB | AI: nominal kurang/lebih, bukti kabur, tanggal terlalu tua, rekening tidak cocok, keyakinan rendah, multi-bukti tidak cocok. | Masuk `perlu_review` dengan alasan; AI tidak menolak final dan admin bisa mengambil keputusan. |
| VER-10 | P0 | Job+DB | AI aktif tetapi scheduler/reader/storage gagal ketika submit atau proses. | Tidak ada keadaan setengah submit/settle; kegagalan job terlihat dan dapat dipulihkan. |
| VER-11 | P1 | API+DB | Setelah bukti/allowlist berubah, jalankan reevaluasi tujuan rekening dari admin. | Keputusan memakai versi policy dan fingerprint bukti terbaru, jejak perubahan tersimpan. |
| VER-12 | P1 | UI+API | Cari order berdasar nomor, cabang, serial; filter verifikasi, mode, email gagal, tanggal; grouping dan paginasi. | Daftar mengikuti filter, jumlah dan detail tetap konsisten. |
| VER-13 | P0 | API+DB | Bukti Transfer disetujui lebih besar dari kebutuhan order; Superadmin memilih bagian untuk order dan kredit ke Deposit, Manager mencoba pilihan sama. | Jumlah split tepat; hanya Superadmin dapat mencipta kredit Deposit; bukti tidak bisa dikreditkan ulang. |

### F. Deposit Cash & Carry

| ID | P | Lapisan | Aksi / variasi | Hasil yang diharapkan |
|---|---|---|---|---|
| DEP-01 | P0 | UI+API | Cabang Deposit melihat saldo, versi jurnal, mutasi, top-up; cabang tanpa Deposit membuka rute/API. | Data hanya milik cabang yang berhak; navigasi sesuai izin. |
| DEP-02 | P0 | UI+API+DB | Unggah top-up dengan nominal, rekening efektif, bukti sah. | Status pending; saldo belum bertambah; bukti dapat dilihat admin/cabang pemilik. |
| DEP-03 | P0 | API | Top-up tanpa bukti, bukti salah format/terlalu besar/duplikat, rekening bukan milik cabang. | Ditolak; tidak muncul kredit ataupun objek yatim. |
| DEP-04 | P0 | UI+API | Manager pilih review/reject beralasan, lalu coba verify. | Review/reject tercatat; Manager tidak bisa menambah kredit deposit. |
| DEP-05 | P0 | UI+API+DB | Superadmin verify nominal sama dan berbeda dari nominal deklarasi. | Saldo bertambah tepat nilai approved; beda nilai mensyaratkan alasan; jurnal dan audit terhubung ke bukti. |
| DEP-06 | P0 | API+DB | Ulangi approval dengan Idempotency-Key sama dan key sama untuk payload berbeda. | Replay tidak menggandakan saldo; payload berbeda ditolak. |
| DEP-07 | P0 | API+DB | Dua submit order bersamaan menggunakan saldo yang sama; saldo berubah setelah modal terbuka. | Tidak ada saldo negatif; versi jurnal lama menghasilkan konflik dan UI meminta muat ulang. |
| DEP-08 | P0 | UI+API+DB | Deposit Rp70.000, order Rp100.000, unggah bukti sisanya Rp30.000, verifikasi split. | Jurnal debit Rp70.000; order `done` hanya setelah Rp30.000 disetujui; jumlah sumber = total. |
| DEP-09 | P0 | API+DB | Tambah alokasi deposit ke order `submitted` sesudah submit, dari cabang sendiri atau admin beralasan. | Saldo dan sisa order turun tepat, versi/jurnal dan sumber transaksi tercatat. |
| DEP-10 | P0 | API+DB | Coba alokasi melebihi saldo/sisa, ke order lain/`done`, versi order/jurnal basi, tanpa Idempotency-Key. | Ditolak tanpa debit; replay aman. |
| DEP-11 | P0 | API+DB | Admin reverse sebagian/seluruh alokasi order belum `done` dengan alasan dan key. | Saldo kembali tepat, sumber reversal dan audit tercatat. |
| DEP-12 | P0 | API | Reverse tanpa alasan, melebihi alokasi, oleh cabang, atau setelah order `done`. | Ditolak, tidak ada mutasi jurnal. |
| DEP-13 | P0 | UI+API+DB | Revisi turun order Deposit hingga kurang dari jumlah deposit yang sudah committed. | Reversal plan wajib dan sesuai alokasi; revisi atomik atau seluruhnya gagal. |
| DEP-14 | P1 | UI+API | Tampilkan semua top-up pending/review, saldo cabang, riwayat mutasi, bukti dan sumber nominal. | Angka antrean sama dengan API/DB; status tidak menggandakan transaksi. |

### G. Tempo, Surat Tagihan, pelunasan, kredit, dan penahanan

| ID | P | Lapisan | Aksi / variasi | Hasil yang diharapkan |
|---|---|---|---|---|
| TMP-01 | P0 | UI+API+DB | Submit Tempo 31 Okt 23:59 WIB, admin approve 2 Nov. | Masuk periode Oktober; due berdasarkan tanggal bayar yang di-snapshot saat submit; status `approved_unpaid` kecuali total nol. |
| TMP-02 | P0 | API+DB | Approve sebelum, tepat pada, dan sesudah due asli. | Due normal bulan berikutnya; jika approval setelah due asli, pindah ke tanggal bayar berikut setelah approval. |
| TMP-03 | P0 | UI+API | Approve Tempo tanpa rekening efektif atau order bukan Tempo/masih draft/sudah approved; klik ganda. | Ditolak atau konflik, tidak ada charge/job ganda. |
| TMP-04 | P0 | UI+API+Job | Approve Tempo sah tanpa bukti transfer. | Invoice dan Surat Jalan terbit serta email terkirim sesuai penerima; PDF bertanda Tempo dan tanggal due; sisa piutang = total. |
| TMP-05 | P1 | API+DB | Ubah tanggal bayar cabang setelah submit, termasuk di tengah periode. | Due order lama tetap snapshot; order baru memakai tanggal baru. |
| TMP-06 | P0 | Job+DB | Lewati akhir bulan dan jalankan job `tempo-daily` pada tanggal 1. | Surat Tagihan PDF per cabang/periode terbit, daftar invoice/due/total sesuai; email cabang dan admin ber-flag invoice. |
| TMP-07 | P0 | Job+DB | Jalankan job harian berulang; approval order periode lama atau revisi setelah Surat Tagihan pertama. | Job idempoten bila isi sama; perubahan menghasilkan versi baru, PDF versi lama tetap utuh dan dapat diunduh. |
| TMP-08 | P0 | UI+API | Lihat status `berjalan`, `menunggu_bayar`, `lewat_jatuh_tempo`, `lunas`, versi lama dengan kasus tanggal terkontrol. | Status berubah tepat pada batas hari WIB; lewat jatuh tempo mulai H+1, tidak bergeser karena libur. |
| TMP-09 | P0 | UI+API+DB | Cabang unggah bukti pelunasan Rp150.000 untuk O1 Rp100.000 dan O2 Rp200.000, Manager verifikasi. | O1 lunas/`done`, O2 terbayar Rp50.000/sisa Rp150.000; alokasi FIFO lintas periode, saldo piutang turun Rp150.000. |
| TMP-10 | P0 | UI+API | Cabang unggah beberapa bukti sebagian pada rekening efektif berbeda; admin menolak satu beralasan. | Yang pending tidak mengurangi piutang; yang ditolak tetap bersejarah dan tidak dialokasikan. |
| TMP-11 | P0 | API | Nominal pelunasan 0/negatif/desimal/>Rp1 triliun, tanpa rekening/berkas, MIME palsu/>10 MB, berkas identik. | Ditolak sebelum/di transaksi; file gagal tidak bisa diunduh dan tak menjadi receipt aktif. |
| TMP-12 | P0 | API+DB | Manager verifikasi nominal > total seluruh piutang; Superadmin memverifikasi kasus sama. | Manager ditolak; Superadmin mencatat kelebihannya sebagai kredit Tempo tersendiri. |
| TMP-13 | P0 | API+DB | Buat order Tempo baru setelah kredit disetujui. | Kredit otomatis menutup invoice tertua yang terbuka; jurnal kredit/sisa akurat; deposit tidak berubah. |
| TMP-14 | P0 | API+DB | Dua admin memutuskan bukti pelunasan sama bersamaan, atau keputusan kedua setelah reject/verify. | Hanya satu keputusan efektif; tidak ada alokasi/kredit ganda. |
| TMP-15 | P0 | UI+API | Tagihan lewat due bersisa dengan toggle tahan aktif: buat/simpan draft, submit Tempo, Transfer, Deposit dari `UJI-MIX`. | Draft tetap boleh; hanya Tempo ditolak, dua mode lain boleh bila syarat masing-masing terpenuhi. |
| TMP-16 | P0 | API+DB | Bukti pending yang totalnya kurang dari tunggakan; kemudian tambah sampai menutup semua. | Hold tetap saat kurang; setelah cukup, hold ditunda mulai bukti yang pertama kali menutup seluruh tunggakan. |
| TMP-17 | P0 | API+DB | Upload yang menutup tunggakan pada Jumat, periksa Senin/Selasa/Rabu; upload lagi sebelum grace habis. | Grace dua hari kerja Mon–Fri, tidak bertambah karena unggahan ulang; setelah habis hold kembali jika masih menunggak. |
| TMP-18 | P0 | API+DB | Bukti pending ditolak/nominal verifikasi kurang dari tunggakan, lalu bukti baru atau pembayaran lengkap. | Hold dihitung ulang; lepas otomatis hanya bila sisa tunggakan 0 atau grace sah masih menutupnya. |
| TMP-19 | P0 | UI+API | Superadmin matikan toggle tahan untuk cabang bertunggakan. | Submit Tempo langsung dapat dilakukan; perubahan dicatat di audit. |
| TMP-20 | P0 | API+DB | Revisi Tempo positif ketika due lama belum lewat dan setelah lewat. | Total/charge/sisa naik tepat; koreksi lewat due memakai tanggal bayar berikutnya untuk tambahan. |
| TMP-21 | P0 | API+DB | Revisi Tempo negatif saat belum dibayar, sebagian dibayar, dan sudah lunas. | Sisa turun terlebih dahulu; kelebihan pembayaran jadi kredit hanya lewat otorisasi Superadmin; order/dokumen/tagihan sinkron. |
| TMP-22 | P0 | Job | Jalankan pengingat H-3, H, H+1, H+7 untuk due 5 dan due 1–3. | Tepat satu email per tahap/due/cabang; H-3 untuk due 1–3 tetap dikirim tanpa nominal final; tahap lain memakai saldo aktual. |
| TMP-23 | P1 | Job | Setelah lunas sebelum tahap berikut, ubah saldo melalui koreksi, atau jalankan reminder berulang. | Tidak mengirim duplikat atau tagihan yang sudah tidak bersisa; isi mencerminkan kondisi sah saat pengiriman. |
| TMP-24 | P0 | UI+API | Bandingkan total piutang, overdue, pending proof, aging 0–30/31–60/>60 pada cabang dan admin. | Angka = jumlah charge minus paid; aging dihitung dari due, tidak menghitung pending sebagai lunas. |
| TMP-25 | P1 | API+XLSX | Superadmin unduh ekspor piutang; Manager/cabang mencoba. | XLSX memuat cabang/periode/due/total/paid/sisa/status/umur sesuai overview; role lain ditolak. |
| TMP-26 | P1 | UI+API | Cabang melihat/unduh Surat Tagihan versi lama/baru dan bukti pembayaran sendiri; coba ID cabang lain. | Versi lama berstatus arsip; hanya pemilik dan admin berwenang dapat tautan. |
| TMP-27 | P1 | API+DB | Set biaya packaging Rp0, submit Sertifikat Tempo total Rp0, lalu approve. | Order langsung `done`, tidak ada sisa piutang negatif; dokumen tetap terbit. |
| TMP-28 | P0 | API+DB | Submit/approve beberapa order Tempo berjumlah besar ketika piutang lama masih terbuka tetapi belum ditahan karena due belum lewat/toggle off. | Tidak ada batas kredit tersembunyi; hanya aturan hold dan validasi angka yang dapat menolak. |

### H. Revisi, dokumen, PDF/XLSX, dan email

| ID | P | Lapisan | Aksi / variasi | Hasil yang diharapkan |
|---|---|---|---|---|
| DOC-01 | P0 | UI+API | Revisi order `done` dengan alasan, ubah qty/harga/nama instansi, tambah/hapus baris produk. | Nomor tetap, versi naik satu, serial baris lama tetap, baris baru mendapat serial baru; total dihitung server. |
| DOC-02 | P0 | API | Revisi tanpa alasan, dari cabang, Manager saat `manager_boleh_revisi=false`, pada draft/submitted biasa. | Ditolak; versi/ledger tidak berubah. |
| DOC-03 | P0 | API+Job | Revisi kedua sebelum PDF versi berjalan selesai dirender; revisi paralel setelah siap. | Revisi dini ditolak; revisi sah berurutan tanpa versi PDF yang hilang. |
| DOC-04 | P0 | UI+API+Job | Setelah Transfer/Deposit `done` dan Tempo `approved_unpaid`, tunggu render dokumen. | Invoice PDF dan Surat Jalan PDF/XLSX versi tepat tersedia; gagal render terlihat dan retry tidak menggandakan berkas. |
| DOC-05 | P0 | PDF | Bandingkan PDF Invoice dengan contoh bisnis: A4 mendatar, logo/kop, nomor/tanggal/versi, cabang, tabel per baris berisi serial, instansi, produk, qty, harga, subtotal, total, terbilang dan rekening. | Isi angka sesuai snapshot dan layout terbaca/cetak baik; tanda tangan tetap ruang kosong. |
| DOC-06 | P0 | PDF+XLSX | Bandingkan PDF dan XLSX Surat Jalan: A4 tegak untuk PDF, satu baris per order line, serial/instansi/cabang/produk/qty/catatan dan total qty. | Semua baris selaras; tidak memuat harga yang tidak diperlukan; tanda tangan kosong sesuai template. |
| DOC-07 | P0 | PDF | Pada invoice Tempo, Deposit dan Transfer, cek penanda pembayaran dan due. | Tempo menyebut due yang benar, Deposit `LUNAS — CASH & CARRY`, Transfer `LUNAS`; tidak memberi cap lunas pada piutang terbuka. |
| DOC-08 | P0 | PDF | Order dengan 1, 11, 25+ baris, nama instansi panjang, teks Unicode, catatan panjang, Sertifikat+packaging. | Tidak terpotong/bertumpuk; header berulang saat multipage; tidak ada halaman kosong yang menyesatkan. |
| DOC-09 | P0 | PDF | Cek kolom PI (5%), Expres, Bea SPV pada template Invoice. | Kolom menampilkan `—` karena komponen biaya tersebut belum ada dalam model order; total tidak mengarang pajak/biaya. |
| DOC-10 | P0 | UI+API+Storage | Unduh invoice dari detail dan Riwayat invoice, Surat Jalan dari admin, dokumen revisi lama/baru. | URL bertanda tangan berumur pendek, file valid, versi historis tidak tertimpa, otorisasi sesuai peran. |
| DOC-11 | P1 | UI+API | Filter Riwayat invoice per periode, termasuk order Tempo `approved_unpaid` dan semua versi revisi. | Order tampil sesuai tanggal; tombol unduh aktif setelah berkas siap, label status akurat. |
| DOC-12 | P0 | Job+Email | Setelah dokumen siap, periksa penerima: invoice ke cabang dan admin `notif_invoice`; Surat Jalan ke admin `notif_surat_jalan` saja. | Hanya penerima aktif yang memenuhi flag mendapat jenis dokumen yang benar; undangan/nonaktif tidak menerima. |
| DOC-13 | P0 | Job+Email | Revisi invoice. | Invoice versi baru dikirim ulang ke cabang dan admin penerima; riwayat email mencatat versi; Surat Jalan revisi tersedia sebagai dokumen, tanpa kirim ulang otomatis jika kebijakan saat ini begitu. |
| DOC-14 | P0 | Job+Email | Simulasikan SMTP gagal berturut-turut lalu pulih, dan worker mati saat status `sending`. | Retry 1m, 5m, 30m, 2j, 6j sesudah percobaan pertama (maks 6 percobaan); lease pulih; status akhirnya terkirim/gagal tanpa duplikasi tak terkendali. |
| DOC-15 | P0 | UI+API | Email gagal muncul pada detail dan chip dashboard; admin kirim ulang. | Satu aksi resend terotorisasi, status/riwayat/audit diperbarui; file dan penerima tidak berubah keliru. |
| DOC-16 | P1 | Job | Renderer PDF/storage tidak tersedia saat transisi `done`/approval Tempo. | Status bisnis tidak mengklaim file siap; job dapat retry dan email dokumen menunggu file benar. |
| DOC-17 | P1 | PDF+API | Coba teks instansi/catatan berisi HTML/script, tanda kutip, `&`; unduh PDF. | Ditampilkan sebagai teks, tidak mengeksekusi konten, tidak merusak struktur PDF. |
| DOC-18 | P1 | Storage+PDF | Unduh PDF lama yang dibuat sebelum template terbaru dan PDF revisi sesudahnya. | PDF historis tetap byte-for-byte sama; perubahan template hanya berlaku pada dokumen yang baru dirender. |

### I. Dashboard, reporting dan ekspor

| ID | P | Lapisan | Aksi / variasi | Hasil yang diharapkan |
|---|---|---|---|---|
| REP-01 | P0 | UI+API | Dashboard filter cabang dan hari/minggu/bulan/tahun, buka ulang URL. | Angka mengikuti periode/cabang; filter URL dapat dipakai ulang. |
| REP-02 | P0 | UI+API | Bandingkan dashboard Superadmin dan Manager. | Superadmin melihat nilai rupiah; respons API Manager tidak mengirim nilai yang dilarang, bukan sekadar UI menyembunyikannya. |
| REP-03 | P1 | UI+API | Bandingkan chip menunggu/review/email gagal/overdue dengan daftar detail setelah diklik. | Jumlah chip cocok dengan hasil filter; hide/show chip tidak mengubah data server. |
| REP-04 | P1 | UI+API | Grafik bulanan/harian, periode pembanding, komposisi, cabang dan produk teratas, CSV dashboard. | Total/label/urutan dan CSV sesuai dataset uji; tidak ada data cabang lain saat filter aktif. |
| REP-05 | P0 | UI+API | Reporting satu order berisi 3 order line dan satu revisi; filter tanggal/cabang/status. | Satu baris per item sesuai versi yang berlaku, nama instansi dan serial tepat; total subtotal = jumlah baris yang ditampilkan. |
| REP-06 | P1 | API | Filter lanjutan `cabang` multi, `jenis`, `cnc`, `pi`, batas tanggal, paginasi; input filter salah. | API mengembalikan himpunan tepat/menolak nilai invalid; UI saat ini hanya menyediakan tanggal, satu cabang, status. |
| REP-07 | P0 | UI+API+Job | Minta ekspor XLSX dengan filter yang sama, tunggu `queued→proses→selesai`, unduh. | Jumlah baris XLSX = total query terfilter; kolom instansi/serial/qty/harga/total serta sheet rekap benar. |
| REP-08 | P1 | Job | Dataset ekspor besar dan ekspor gagal lalu diulang. | UI menunjukkan status/galat; file tidak tersedia sebelum selesai, ekspor tidak mengunci portal. |
| REP-09 | P0 | API | Manager/cabang meminta laporan atau link unduh ekspor milik Superadmin. | Ditolak 403/404; signed URL tidak bocor lewat API. |
| REP-10 | P0 | UI+API | Ekspor piutang Tempo lalu cocokkan tiap baris dengan menu Tagihan cabang dan admin. | Total, terbayar, sisa, due dan aging sama; saldo pending tidak dihitung lunas. |

### J. User management, pengaturan, tutorial, audit, dan master import

| ID | P | Lapisan | Aksi / variasi | Hasil yang diharapkan |
|---|---|---|---|---|
| ADM-01 | P0 | UI+API | Manager/Superadmin melihat user cabang, aktif/nonaktif, mengirim reset, melihat status belum registrasi. | Hak sesuai peran; status/login terakhir dan email benar; akun nonaktif tidak bisa login. |
| ADM-02 | P0 | UI+API+Email | Superadmin undang admin baru, kirim ulang undangan, aktivasi, ubah role/flag email. | Akun undangan tidak bisa login sampai aktif; email hanya ke alamat tujuan; hak/menu berubah setelah role berubah. |
| ADM-03 | P0 | API | Turunkan/nonaktifkan Superadmin aktif terakhir; buat email admin duplikat. | Ditolak; sistem tetap memiliki Superadmin aktif. |
| ADM-04 | P0 | UI+API | Ubah setting packaging, token/reset TTL, batas berkas, digit invoice, izin revisi Manager, AI. | Hanya Superadmin; nilai invalid ditolak; perubahan berlaku pada transaksi baru, data lama tetap snapshot. |
| ADM-05 | P1 | UI+API | Edit tutorial per topik sebagai Superadmin, buka ulang portal cabang tanpa deploy; Manager/cabang coba menulis. | Konten terbaru tampil, input disanitasi, role lain 403. |
| ADM-06 | P0 | UI+API | Buka audit log setelah cabang/produk/harga/rekening/user/setting/verify/revisi/top-up/Tempo berubah. | Ada aktor, waktu, entitas, aksi, before/after dan alasan yang tepat; urut/filter/paginasi benar. |
| ADM-07 | P0 | API | Periksa audit dan respons API untuk password, token, cookie, presigned URL, isi bukti. | Rahasia disamarkan/tidak terekam; akun cabang/Manager tidak dapat membaca audit. |
| ADM-08 | P1 | API | Master Impact: preview → evidence → apply → rollback pada lingkungan uji, termasuk pengulangan dan data tidak valid. | Superadmin saja, jejak bukti/rollback konsisten; apply produksi tetap mengikuti guard konfigurasi. Tidak ada layar UI untuk fitur ini. |

### K. Keamanan, keandalan, operasional, dan pengalaman UI

| ID | P | Lapisan | Aksi / variasi | Hasil yang diharapkan |
|---|---|---|---|---|
| OPS-01 | P0 | API | Mutasi tanpa CSRF, dari Origin tak dikenal, sesi berakhir, atau role salah; GET publik kesehatan/meta. | Mutasi ditolak sesuai kode; akses publik terbatas; header keamanan/CORS sesuai kebijakan. |
| OPS-02 | P0 | API+DB | Ulangi request karena timeout pada submit, verifikasi, top-up, alokasi, reversal, revisi, pelunasan. | Tidak ada nomor, saldo, charge, dokumen, email, audit ekonomi ganda. |
| OPS-03 | P0 | API+DB | Simulasikan DB/queue/storage gagal sebelum commit dan setelah upload objek sementara. | Transaksi atomik; tidak ada state bisnis setengah jadi; objek sementara dibersihkan atau tercatat untuk rekonsiliasi. |
| OPS-04 | P0 | Job+DB | Worker restart saat dokumen/email/statement/export sedang diproses. | Pekerjaan diklaim ulang aman; hasil akhir satu versi/keputusan bisnis yang konsisten. |
| OPS-05 | P0 | Job+Storage | Jalankan backup harian DB dan objek, pastikan age-encrypted dan tercatat di Pengaturan; lakukan restore ke DB uji. | Dump dan file cocok dengan manifest; jumlah baris/objek dapat dipulihkan; kegagalan backup terlihat. |
| OPS-06 | P1 | API+Infra | `/healthz` dan `/readyz` saat DB, storage, renderer hidup/mati; lihat metrik/alert worker/email/backup. | Liveness/readiness membedakan kondisi, alert memberi sinyal saat layanan kritis terganggu. |
| OPS-07 | P1 | UI | Layar desktop, tablet dan ponsel pada dua portal: tabel panjang, modal submit/revisi, PDF preview/unduh, navigasi dan status loading/error/empty. | Tidak ada kontrol terpotong; navigasi keyboard dan label input jelas; pesan tidak menyembunyikan aksi penting. |
| OPS-08 | P1 | UI | Jalankan keyboard-only dan pembaca layar pada login, editor order line, pilihan mode bayar, unggah bukti, keputusan admin. | Fokus terlihat, urutan logis, label/error terasosiasi, tombol punya nama, kontras memadai. |
| OPS-09 | P1 | API | Input panjang/salah bentuk pada pencarian, filter tanggal/UUID, halaman/limit, nama file dan teks bebas. | Validasi mengembalikan 4xx terstruktur; tidak ada SQL/HTML injection atau informasi internal. |
| OPS-10 | P1 | API+DB | Hitung ulang invarian ekonomi setelah seluruh perjalanan: total order = jumlah line; Transfer/Deposit `done` settled; Tempo charge/paid/credit tidak negatif. | Semua jumlah cocok dengan jurnal dan dokumen; tidak ada saldo/counter negatif. |
| OPS-11 | P2 | UI+API | Refresh halaman di tengah request, kehilangan jaringan, dua tab mengubah data sama. | UI menampilkan galat/konflik dan dapat pulih; tidak mengklaim sukses sebelum respons final. |
| OPS-12 | P2 | API+Job | Uji waktu respons daftar 25/100 baris, PDF 25+ baris, ekspor besar, dan 100 submit paralel pada data uji representatif. | Tidak ada timeout/duplikasi; catat angka p95 dan ukuran file sebagai baseline, jangan menetapkan SLA fiktif. |

## 4. Matriks gerbang dan batas fitur

| Gerbang | Syarat lulus |
|---|---|
| Keuangan | Semua P0 Transfer, Deposit, Tempo, revisi, alokasi FIFO, dan hold PASS; tidak ada ketidakcocokan Rupiah/ledger. |
| Data/dokumen | Semua P0 penomoran dan nama instansi per baris PASS lintas UI, API, PDF/XLSX, email, laporan; versi lama tetap utuh. |
| Akses | Semua P0 role, isolasi cabang, CSRF, signed URL, rahasia PASS. |
| Asinkron | Job dokumen, statement, email retry, ekspor, backup/restore yang termasuk rilis PASS dengan bukti. |
| Produksi | Smoke dua host dan rute autentikasi sehat; perjalanan `UJI-` terkontrol (bila dijalankan) dicatat terpisah dari fixture lokal. |

**Batas cakupan saat ini:** Virtual Account hanya kode R&D domain, belum ada rute produk sehingga bukan skenario penerimaan portal. Master Impact memiliki API tetapi belum ada menu frontend; uji sebagai suite API terpisah. Efek PI terhadap harga/pajak dan angka Expres/Bea SPV belum dimodelkan: uji bahwa dokumen tidak mengarang nilainya. UI Reporting belum memaparkan semua filter yang didukung API, maka kedua lapisan diuji terpisah.

**Titik perhatian dari pembacaan kode untuk pengujian pertama:** navigasi cabang menyembunyikan `Tagihan` ketika izin Tempo dimatikan, meski tagihan historis bisa tetap terbuka (CAB-09). Tooltip invoice yang belum siap masih menyebut dokumen hanya tersedia setelah `Done`, padahal invoice Tempo terbit saat `approved_unpaid` (DOC-11). Keduanya perlu dibuktikan di UI lalu dicatat sebagai temuan bila benar menghambat pengguna.

**Urutan eksekusi:** validasi data awal dan role → E2E-01 s.d. E2E-08 → seluruh P0 per kelompok → P1/P2 → uji waktu dan kegagalan worker → rekonsiliasi invarian → smoke produksi. Simpan bukti gagal sebelum reset fixture agar diagnosis tidak hilang.

**Gerbang otomasi pendukung:** `make test`, `make test-integrasi` (dengan PostgreSQL uji), `make web-test`, `make web-typecheck`, `make web-build`, `make openapi`, `make migrasi-cek`, dan `make smoke`. Hasil unit/integrasi ini dicatat terpisah dari PASS E2E browser; bila email, renderer, atau storage uji tidak tersedia, tandai skenario terkait **BLOCKED**, bukan PASS.
