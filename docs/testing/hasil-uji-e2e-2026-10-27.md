# Hasil uji end to end Grahita — 27 Oktober 2026

Laporan ini merekam baseline `9cfae04a`. Hasil perbaikan dan uji ulang ada di [tindak lanjut QA](hasil-uji-tindak-lanjut-2026-10-27.md).

**Baseline:** commit `9cfae04a`; 162 skenario pada [rencana uji](skenario-e2e-seluruh-fitur.md).

**Lingkungan transaksi:** PostgreSQL fixture lokal terisolasi, API dan kedua portal lokal, storage sintetis, Gotenberg, Mailpit, worker. Data `UJI-` saja. **Produksi:** pemeriksaan baca saja di dua host, tanpa akun QA terautentikasi.

## Arti status

- **PASS:** seluruh hasil utama skenario terbukti pada lapisan yang disebut dalam catatan.
- **FAIL:** perilaku yang teramati bertentangan dengan hasil yang diharapkan.
- **PARTIAL:** sebagian alur atau variasi lulus; skenario keseluruhan belum lulus.
- **NOT RUN:** belum dieksekusi sebagai skenario lengkap.
- **BLOCKED:** prasyarat eksternal untuk uji nyata tidak tersedia.

| Status | Semua 162 | P0 dari 124 |
|---|---:|---:|
| PASS | 28 | 27 |
| FAIL | 1 | 1 |
| PARTIAL | 106 | 79 |
| NOT RUN | 24 | 14 |
| BLOCKED | 3 | 3 |

**Keputusan gerbang:** BELUM LULUS. Ada satu kegagalan P0 (CAB-09), dan sejumlah P0 masih PARTIAL/NOT RUN/BLOCKED. Status produksi tidak dapat diwarisi dari fixture lokal.

## Bukti utama

- Enam pesanan sintetis selesai, total Rp2.865.000. Tidak ada selisih `orders.total` terhadap jumlah line. Empat charge Tempo total Rp1.560.000 dan terbayar Rp1.560.000; tidak ada mismatch charge/order. Deposit/jurnal juga direkonsiliasi pada transaksi uji. [Hasil kueri rekonsiliasi](/opt/temp/grahita-qa-evidence-2026-10-27/rekonsiliasi-db.txt) disimpan.
- Delapan email `invoice_cabang` dan lima `reset_password` berstatus `sent` di outbox lokal. Dua puluh empat dokumen order tercatat. [PDF/XLSX sintetis](/opt/temp/grahita-qa-evidence-2026-10-27/) disimpan terpisah; invoice revisi v0 SHA-256 `81e4d7780613b4038824699fef8942a88203ba590b378b0f10f4def1bb3ffbd6` tetap sama setelah v1 dibuat.
- Frontend: 245/245 tes lulus (API client 9, UI 65, cabang 71, admin 100); typecheck, build, migrasi kompatibel, dan validasi OpenAPI lulus. OpenAPI mengeluarkan 10 peringatan tentang respons 4xx.
- Integrasi PostgreSQL: 146 tes lulus, 4 SKIP karena fixture khusus tidak tersedia, dan 1 tes dokumen gagal karena ekspektasi kolom XLSX lama. Tes ekspor 50.000 baris lulus: 25,3 detik pada tahap ekspor, 101 batch dengan maksimum 500 baris, dan kenaikan heap 64,6 MB. Migrasi database dan worker package lulus ketika dijalankan serial. Backup/restore lulus dengan overlay sementara yang memperbaiki fixture uji.
- Dua host produksi memberi HTTP 200 untuk halaman dan `/api/v1/meta`; `/api/v1/auth/me` memberi 401 tanpa sesi, endpoint fixture memberi 404. Container API produksi `healthy` dan image API/web bertag `9cfae04`. Ini hanya smoke baca.

## Temuan dan batas yang memengaruhi keputusan

1. **CAB-09 FAIL (P0):** saat izin Tempo cabang dicabut, navigasi cabang menyembunyikan menu Tagihan. Tagihan historis masih bisa dibuka lewat URL langsung dan dibayar, sehingga pengguna kehilangan jalur yang dapat ditemukan untuk melunasi piutang. Kondisi ini teramati di UI fixture dan sesuai filter pada `web/apps/customer/src/nav.ts:27` yang dipanggil oleh `routes.tsx:50`.
2. **Suite integrasi repo belum hijau:** tanpa overlay, test backup mengisi `payment_proofs` tanpa `branch_id` sehingga ditolak constraint terbaru. Test dokumen lama mengharapkan serial XLSX di D9/D10, sedangkan template terkini menaruhnya di kolom E setelah kolom instansi; kegagalan ini terulang pada uji tunggal. XLSX yang dihasilkan secara nyata telah diperiksa dan berisi data yang benar. Percobaan menjalankan semua paket paralel di satu database juga menimbulkan interferensi migrasi dan timeout 10 menit; hasil tersebut tidak diartikan sebagai cacat migrasi produk.
3. **DOC-11 perlu perbaikan teks:** tooltip invoice yang belum siap mengatakan dokumen baru tersedia setelah `Done`, padahal invoice Tempo dapat terbit saat `approved_unpaid`. Ini ditemukan pada source, belum ditetapkan sebagai FAIL UI.
4. **Produksi belum diuji terautentikasi:** tidak ada kredensial QA yang tersedia pada sesi ini. Tidak ada pesanan/pembayaran produksi yang dibuat, dan tidak ada klaim PASS produksi untuk 162 skenario.
5. **Bukti visual terbatas:** PDF/XLSX dan log tersimpan, sedangkan tangkapan layar untuk setiap langkah browser belum diarsipkan. Status PASS lokal berarti perilaku diamati dan data akhir diperiksa; laporan ini belum menjadi paket bukti audit yang lengkap menurut rencana uji.

## Hasil per ID

### Perjalanan menyeluruh

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| E2E-01 | P0 | **PASS** | UI registrasi token sekali pakai, login, katalog/harga cabang; API dan DB lokal. |
| E2E-02 | P0 | **PASS** | UI Transfer tiga baris multi-instansi, bukti, verifikasi Manager, dokumen, email, laporan/XLSX. |
| E2E-03 | P0 | **PASS** | UI top-up pending→Superadmin verify; order saldo penuh dan saldo+transfer; jurnal, PDF dan email cocok. |
| E2E-04 | P0 | **PARTIAL** | Approval, PDF/email, bayar sebagian dan lunas lulus; job tanggal 1 belum dijalankan di browser fixture. |
| E2E-05 | P0 | **PARTIAL** | Tiga cara bayar dan snapshot terbukti; hold overdue baru diuji pada integrasi API/DB, belum perjalanan UI lengkap. |
| E2E-06 | P0 | **PARTIAL** | Revisi Transfer/Tempo, versi PDF dan email terbukti; semua variasi delta dan rekonsiliasi laporan belum lengkap di UI. |
| E2E-07 | P0 | **PARTIAL** | FIFO dan kredit Tempo lulus integrasi; browser hanya FIFO satu periode, belum lintas periode dan kredit UI. |
| E2E-08 | P0 | **PASS** | Tiga baris dan serial dicocokkan pada UI, DB, PDF Invoice/Surat Jalan, XLSX Surat Jalan dan laporan. |

### Akun dan hak akses

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| AUT-01 | P0 | **PASS** | UI/API lokal: tiga peran, login generik/rate limit, registrasi, reset invalid/replay/sesi, logout, RBAC Manager. |
| AUT-02 | P0 | **PASS** | UI/API lokal: tiga peran, login generik/rate limit, registrasi, reset invalid/replay/sesi, logout, RBAC Manager. |
| AUT-03 | P1 | **PASS** | UI/API lokal: tiga peran, login generik/rate limit, registrasi, reset invalid/replay/sesi, logout, RBAC Manager. |
| AUT-04 | P0 | **PASS** | UI/API lokal: tiga peran, login generik/rate limit, registrasi, reset invalid/replay/sesi, logout, RBAC Manager. |
| AUT-05 | P0 | **PARTIAL** | Token kedaluwarsa/nonaktif/berakun dan sekali pakai lulus integrasi; token pengganti belum diuji. |
| AUT-06 | P1 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| AUT-07 | P0 | **PARTIAL** | Permintaan reset dan email Mailpit, password baru dan token sekali pakai lulus; TTL alami belum ditunggu. |
| AUT-08 | P0 | **PASS** | UI/API lokal: tiga peran, login generik/rate limit, registrasi, reset invalid/replay/sesi, logout, RBAC Manager. |
| AUT-09 | P1 | **PARTIAL** | Ubah password dan cabut sesi lain lulus integrasi; seluruh variasi UI Profil belum dijalankan. |
| AUT-10 | P0 | **PASS** | UI/API lokal: tiga peran, login generik/rate limit, registrasi, reset invalid/replay/sesi, logout, RBAC Manager. |
| AUT-11 | P0 | **PASS** | UI/API lokal: tiga peran, login generik/rate limit, registrasi, reset invalid/replay/sesi, logout, RBAC Manager. |
| AUT-12 | P0 | **PARTIAL** | Akses order, bukti dan dokumen lintas cabang ditolak; Surat Tagihan/deposit lintas cabang belum dicoba. |

### Cabang

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| CAB-01 | P0 | **PASS** | UI/API lokal: pilihan 1–3, batas tanggal 1–28, email unik, hapus kosong/tolak cabang berorder. |
| CAB-02 | P0 | **PASS** | UI/API lokal: pilihan 1–3, batas tanggal 1–28, email unik, hapus kosong/tolak cabang berorder. |
| CAB-03 | P0 | **PASS** | UI/API lokal: pilihan 1–3, batas tanggal 1–28, email unik, hapus kosong/tolak cabang berorder. |
| CAB-04 | P1 | **PARTIAL** | Profil dan pilihan bayar terlihat; semua filter/detail/PI serta audit Special belum diperiksa. |
| CAB-05 | P0 | **PARTIAL** | Token sekali pakai lulus integrasi; alur generate ulang dan TTL setting belum diuji. |
| CAB-06 | P0 | **PARTIAL** | Integrasi API membuktikan nonaktif mencabut sesi/login; UI dan dokumen historis belum dicek bersama. |
| CAB-07 | P0 | **PASS** | UI/API lokal: pilihan 1–3, batas tanggal 1–28, email unik, hapus kosong/tolak cabang berorder. |
| CAB-08 | P0 | **PARTIAL** | Order lama menyimpan snapshot; seluruh kombinasi perubahan izin dan order baru belum dijalankan. |
| CAB-09 | P0 | **FAIL** | Setelah Tempo dicabut dari cabang berpiutang, tautan menu Tagihan hilang; URL langsung/API masih bisa melunasi. |
| CAB-10 | P1 | **PARTIAL** | Integrasi API membuktikan toggle melepas hold dan audit; pengujian UI bertunggakan belum lengkap. |

### Master

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| MAS-01 | P0 | **PARTIAL** | Produk dengan harga tampil di katalog; CRUD/aktif/nonaktif lengkap belum diuji di UI. |
| MAS-02 | P0 | **PARTIAL** | Perhitungan harga ditunjukkan pada data lokal; seluruh kombinasi HJM/P1/SPV dan input salah belum diuji. |
| MAS-03 | P0 | **PARTIAL** | Cabang tanpa harga tidak melihat produk; penolakan order langsung dan harga dua cabang didukung integrasi. |
| MAS-04 | P1 | **PARTIAL** | Salin harga lulus integrasi; alur UI dan efek order historis belum dicek penuh. |
| MAS-05 | P0 | **PARTIAL** | Snapshot harga saat submit lulus integrasi; perjalanan edit master lewat UI belum lengkap. |
| MAS-06 | P0 | **PARTIAL** | Assignment rekening BCA/BRI diuji; seluruh status aktif/nonaktif dan tujuan invoice belum dibandingkan. |
| MAS-07 | P0 | **PARTIAL** | Tujuan rekening efektif wajib pada verifikasi lulus integrasi; tiga mode tanpa rekening belum diuji penuh. |
| MAS-08 | P0 | **PARTIAL** | Race nonaktif/versi rekening dan re-evaluasi policy lulus integrasi; semua titik upload/submit/verify belum lengkap. |
| MAS-09 | P1 | **PARTIAL** | Keadaan katalog berharga/tanpa harga diuji; mobile, pencarian dan Sertifikat belum lengkap. |
| MAS-10 | P1 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |

### Pesanan

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| ORD-01 | P0 | **PARTIAL** | Draft tiga baris multi-instansi dan total cocok; hapus satu baris belum diuji. |
| ORD-02 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| ORD-03 | P0 | **PARTIAL** | Integrasi menolak produk nonaktif; draft kosong dan campuran Sertifikat belum lengkap. |
| ORD-04 | P0 | **PARTIAL** | Sertifikat Rp0 dan packaging otomatis lulus integrasi; alur UI multi-instansi belum dijalankan. |
| ORD-05 | P0 | **PARTIAL** | Snapshot packaging lulus integrasi; UI sebelum/sesudah submit belum lengkap. |
| ORD-06 | P0 | **PARTIAL** | Draft disimpan dan dibuka kembali; variasi edit/hapus antarorder belum lengkap. |
| ORD-07 | P0 | **PARTIAL** | Transfer tanpa bukti ditolak dan dengan bukti berhasil; beberapa bukti dalam satu submit belum diuji. |
| ORD-08 | P0 | **PARTIAL** | Deposit saldo penuh dan kurang berhasil; kondisi saldo nol/stale version belum diuji UI. |
| ORD-09 | P0 | **PASS** | Tempo submit tanpa bukti lulus UI; 100 submit paralel/klik ganda/rollback nomor dan serial lulus integrasi. |
| ORD-10 | P0 | **PARTIAL** | Pilihan kosong ditolak dan snapshot tiga mode terbukti; payload dua mode/izin dicabut belum semua dicoba. |
| ORD-11 | P0 | **PASS** | Tempo submit tanpa bukti lulus UI; 100 submit paralel/klik ganda/rollback nomor dan serial lulus integrasi. |
| ORD-12 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| ORD-13 | P0 | **PARTIAL** | Integrasi melarang edit submitted; semua aksi UI dan field historis belum diuji. |
| ORD-14 | P1 | **PARTIAL** | Daftar/detail dan label diamati; seluruh filter/timeline belum dicocokkan. |
| ORD-15 | P0 | **PARTIAL** | GET lintas cabang ditolak; mutasi edit/submit/upload dengan ID asing belum dijalankan. |
| ORD-16 | P1 | **PARTIAL** | Admin unggah atas nama cabang dan timeline lulus integrasi API; UI belum diamati. |
| ORD-17 | P0 | **PARTIAL** | PNG sah dan bukti identik diuji; MIME palsu, file kosong dan limit belum lengkap. |
| ORD-18 | P1 | **PARTIAL** | Beranda diamati pada beberapa status; seluruh hitungan dan banner overdue belum lengkap. |
| ORD-19 | P1 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |

### Verifikasi

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| VER-01 | P0 | **PARTIAL** | Verifikasi satu bukti Transfer, settlement, dokumen dan email lulus; dua bukti/split belum lengkap. |
| VER-02 | P0 | **PARTIAL** | Keputusan perlu_review dan status tidak selesai lulus integrasi; seluruh variasi rekening/nominal UI belum lengkap. |
| VER-03 | P0 | **PARTIAL** | Payload verifikasi salah ditolak integrasi; semua kombinasi alasan/angka belum diuji. |
| VER-04 | P0 | **PARTIAL** | Delapan verifikasi paralel menghasilkan satu sukses dan tujuh konflik tanpa duplikasi; variasi Idempotency-Key belum diuji. |
| VER-05 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| VER-06 | P0 | **PARTIAL** | Rekening nonaktif/cabang lulus integrasi master; race saat verifikasi belum diuji. |
| VER-07 | P1 | **PARTIAL** | Preview bukti admin diamati; catatan internal dan isolasinya belum lengkap. |
| VER-08 | P0 | **BLOCKED** | Integrasi OCR dengan provider tiruan lulus; uji provider nyata memerlukan ANTHROPIC_API_KEY uji yang tidak tersedia. |
| VER-09 | P0 | **BLOCKED** | Integrasi OCR dengan provider tiruan lulus; uji provider nyata memerlukan ANTHROPIC_API_KEY uji yang tidak tersedia. |
| VER-10 | P0 | **BLOCKED** | Integrasi OCR dengan provider tiruan lulus; uji provider nyata memerlukan ANTHROPIC_API_KEY uji yang tidak tersedia. |
| VER-11 | P1 | **PARTIAL** | API re-evaluasi idempoten dan job stale menolak policy/fingerprint lama; perjalanan UI belum diuji. |
| VER-12 | P1 | **PARTIAL** | Daftar admin diamati; semua kombinasi filter/paginasi belum diuji. |
| VER-13 | P0 | **PARTIAL** | Split lebih bayar dan pembatasan Superadmin lulus integrasi; alur UI belum lengkap. |

### Deposit

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| DEP-01 | P0 | **PARTIAL** | Saldo, mutasi dan top-up cabang Deposit diamati; rute cabang tanpa izin belum dicoba. |
| DEP-02 | P0 | **PASS** | Bukti top-up pending terlihat di cabang/admin dan saldo tidak bertambah sebelum Superadmin verify. |
| DEP-03 | P0 | **PARTIAL** | Bukti top-up sah dan duplikat diuji; berkas salah/terlalu besar/tanpa bukti belum lengkap. |
| DEP-04 | P0 | **PARTIAL** | Manager tidak dapat verify top-up; review/reject beralasan belum diuji. |
| DEP-05 | P0 | **PARTIAL** | Superadmin verify nominal sama dan jurnal cocok; nominal berbeda/alasan belum dicoba. |
| DEP-06 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| DEP-07 | P0 | **PARTIAL** | Alokasi Deposit paralel lulus integrasi; konflik journal_version lewat UI belum diuji. |
| DEP-08 | P0 | **PARTIAL** | Saldo+transfer Rp15.000+Rp70.000 untuk order Rp85.000 lulus; kasus nominal 70/30 belum dieksekusi. |
| DEP-09 | P0 | **PARTIAL** | Integrasi alokasi/reversal tersedia; penambahan alokasi pada submitted via cabang/admin belum lengkap. |
| DEP-10 | P0 | **PARTIAL** | Saldo kurang, versi basi, cabang asing dan alokasi paralel ditolak; order done/key kosong belum lengkap. |
| DEP-11 | P0 | **PARTIAL** | Reversal parsial admin beralasan dan saldo/jurnal lulus integrasi; reversal seluruhnya belum dicoba. |
| DEP-12 | P0 | **PASS** | Integrasi API menolak reversal tanpa alasan, melebihi alokasi, oleh cabang dan setelah order Done. |
| DEP-13 | P0 | **PARTIAL** | Integrasi revisi turun/reversal plan lulus; variasi UI dan semua batas belum lengkap. |
| DEP-14 | P1 | **PARTIAL** | Saldo/top-up pending terlihat; review dan seluruh antrean belum direkonsiliasi. |

### Tempo

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| TMP-01 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| TMP-02 | P0 | **PARTIAL** | Approval terlambat diuji integrasi; sebelum/pada due belum lengkap. |
| TMP-03 | P0 | **PARTIAL** | Approval sah/idempotensi dasar diamati; tanpa rekening dan status invalid belum lengkap. |
| TMP-04 | P0 | **PASS** | Approval Tempo tanpa bukti, PDF/email lokal; integrasi overpay hanya SA dan kredit otomatis ke order berikutnya. |
| TMP-05 | P1 | **PARTIAL** | Snapshot tanggal bayar dibuktikan integrasi; perubahan master di tengah periode belum lengkap. |
| TMP-06 | P0 | **PARTIAL** | IssueMonth, PDF statement dan dua penerima email lulus integrasi; job harian tanggal 1 belum dijalankan. |
| TMP-07 | P0 | **PARTIAL** | Versi statement 1–3 lulus integrasi; idempotensi job harian berulang belum lengkap. |
| TMP-08 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| TMP-09 | P0 | **PARTIAL** | FIFO O1→O2 lulus browser/integrasi; dua periode berbeda belum diuji. |
| TMP-10 | P0 | **PARTIAL** | Bukti pending tidak mengurangi piutang; reject dan rekening berbeda belum diuji. |
| TMP-11 | P0 | **PARTIAL** | Bukti identik ditolak; seluruh batas nominal/berkas belum diuji. |
| TMP-12 | P0 | **PASS** | Approval Tempo tanpa bukti, PDF/email lokal; integrasi overpay hanya SA dan kredit otomatis ke order berikutnya. |
| TMP-13 | P0 | **PASS** | Approval Tempo tanpa bukti, PDF/email lokal; integrasi overpay hanya SA dan kredit otomatis ke order berikutnya. |
| TMP-14 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| TMP-15 | P0 | **PARTIAL** | Integrasi menahan hanya Tempo dan mengizinkan Transfer/Deposit; perjalanan UI overdue belum lengkap. |
| TMP-16 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| TMP-17 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| TMP-18 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| TMP-19 | P0 | **PARTIAL** | API toggle off dan audit lulus integrasi; tindakan UI belum diamati. |
| TMP-20 | P0 | **PARTIAL** | Revisi naik piutang lulus lokal/integrasi; koreksi setelah due belum dicoba. |
| TMP-21 | P0 | **PARTIAL** | Revisi turun setelah bayar sebagian menghasilkan kredit/versi baru di integrasi; semua variasi belum lengkap. |
| TMP-22 | P0 | **PARTIAL** | H-3 untuk due tanggal 1 lulus integrasi idempoten; H, H+1, H+7 dan due 2/3/5 belum diuji. |
| TMP-23 | P1 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| TMP-24 | P0 | **PARTIAL** | Saldo piutang cocok DB pada data lokal; aging tiga kelompok/pending/overdue belum lengkap. |
| TMP-25 | P1 | **PARTIAL** | Ekspor XLSX piutang lulus integrasi; RBAC Manager/cabang dan overview belum dibandingkan. |
| TMP-26 | P1 | **PARTIAL** | Versi lama/baru statement lulus integrasi; unduh lintas cabang belum dicoba. |
| TMP-27 | P1 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| TMP-28 | P0 | **PARTIAL** | Tidak terlihat batas piutang pada pesanan lokal; volume tinggi dan kondisi due/toggle belum diuji. |

### Dokumen dan email

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| DOC-01 | P0 | **PARTIAL** | Revisi qty/instansi dan nomor/versi terbukti; tambah/hapus baris belum diuji UI. |
| DOC-02 | P0 | **PARTIAL** | Pembatasan Manager lulus integrasi; semua payload/role/status negatif belum lengkap. |
| DOC-03 | P0 | **PARTIAL** | Revisi paralel/versi job lulus integrasi; simulasi renderer tertunda belum lengkap. |
| DOC-04 | P0 | **PARTIAL** | 24 dokumen empat jenis/versi terbit lewat worker lokal; gagal render dan retry belum diuji. |
| DOC-05 | P0 | **PASS** | PDF/XLSX sintetis dibandingkan dengan contoh dan data DB: layout, instansi per baris, due/cap lunas, kolom PI/Expres/SPV. |
| DOC-06 | P0 | **PASS** | PDF/XLSX sintetis dibandingkan dengan contoh dan data DB: layout, instansi per baris, due/cap lunas, kolom PI/Expres/SPV. |
| DOC-07 | P0 | **PASS** | PDF/XLSX sintetis dibandingkan dengan contoh dan data DB: layout, instansi per baris, due/cap lunas, kolom PI/Expres/SPV. |
| DOC-08 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| DOC-09 | P0 | **PASS** | PDF/XLSX sintetis dibandingkan dengan contoh dan data DB: layout, instansi per baris, due/cap lunas, kolom PI/Expres/SPV. |
| DOC-10 | P0 | **PARTIAL** | Unduh versi 0/1 dan isolasi cabang diuji; TTL signed URL dan semua asal UI belum lengkap. |
| DOC-11 | P1 | **PARTIAL** | Riwayat versi dan Tempo terlihat; filter periode belum lengkap. Tooltip belum-siap masih menyebut harus Done. |
| DOC-12 | P0 | **PASS** | Email dokumen lokal sent dan integrasi memeriksa flag penerima invoice/Surat Jalan serta akun aktif. |
| DOC-13 | P0 | **PARTIAL** | Invoice revisi terkirim dan versi tercatat; semua penerima/Surat Jalan revisi belum dibandingkan. |
| DOC-14 | P0 | **PARTIAL** | Integrasi email gagal, retry dan resend lulus; seluruh jeda retry dan pemulihan lease belum disimulasikan. |
| DOC-15 | P0 | **PARTIAL** | Integrasi resend RBAC/audit lulus; UI email gagal/chip dan pemulihan SMTP belum lengkap. |
| DOC-16 | P1 | **PARTIAL** | Renderer 503 dan kegagalan put/commit lulus integrasi; pemulihan job/email nyata belum diuji penuh. |
| DOC-17 | P1 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| DOC-18 | P1 | **PARTIAL** | PDF versi 0 tetap byte-for-byte sama setelah revisi; pergantian template historis belum diuji. |

### Laporan

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| REP-01 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| REP-02 | P0 | **PASS** | Respons dashboard Manager tidak memiliki nilai rupiah, Superadmin memilikinya; UI mengikuti peran. |
| REP-03 | P1 | **PARTIAL** | Chip/status dashboard diamati; semua hitungan chip vs daftar belum dibandingkan. |
| REP-04 | P1 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| REP-05 | P0 | **PARTIAL** | Enam baris laporan dan instansi cocok; semua filter serta revisi dalam satu laporan belum lengkap. |
| REP-06 | P1 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| REP-07 | P0 | **PARTIAL** | XLSX enam baris + rekap Rp2.440.000 cocok; transisi queued→proses dan semua filter belum direkam. |
| REP-08 | P1 | **PARTIAL** | Ekspor 50.000 baris diuji integrasi; status galat UI dan retry ekspor belum diuji. |
| REP-09 | P0 | **PARTIAL** | Manager ditolak 403 dari Reporting; akses cabang dan tautan ekspor asing belum diuji. |
| REP-10 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |

### Administrasi

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| ADM-01 | P0 | **PARTIAL** | Daftar akun cabang dan reset terbukti lokal/integrasi; semua status dan UI belum lengkap. |
| ADM-02 | P0 | **PARTIAL** | Integrasi undangan/aktivasi/penerima dokumen ada; UI dan kirim ulang belum dieksekusi di fixture. |
| ADM-03 | P0 | **PARTIAL** | Proteksi Superadmin terakhir lulus integrasi; email duplikat belum diuji di suite ini. |
| ADM-04 | P0 | **PARTIAL** | Integrasi validasi/audit/snapshot packaging lulus; semua setting UI belum dijalankan. |
| ADM-05 | P1 | **PARTIAL** | Tutorial dibaca tanpa deploy, sanitasi dan RBAC lulus integrasi; perjalanan UI belum dicoba. |
| ADM-06 | P0 | **PARTIAL** | Audit beberapa mutasi lokal/integrasi tersedia; seluruh entitas/filter/before-after belum lengkap. |
| ADM-07 | P0 | **PARTIAL** | RBAC audit dan redaksi didukung integrasi; semua jalur respons/secret belum diperiksa. |
| ADM-08 | P1 | **PARTIAL** | Preview/apply/rollback/idempotensi lulus pada API uji; workbook dan orkestrasi browser khusus SKIP. |

### Operasional

| ID | Prioritas | Status lokal | Bukti atau batas |
|---|---|---|---|
| OPS-01 | P0 | **PARTIAL** | CSRF tanpa token 403, role salah 403, produksi publik smoke; variasi Origin/sesi/header belum lengkap. |
| OPS-02 | P0 | **PARTIAL** | Submit 100 paralel dan revisi/settlement integrasi idempoten; seluruh jalur retry belum diuji. |
| OPS-03 | P0 | **PARTIAL** | Rollback nomor/revisi lulus integrasi; gagal DB/queue/storage semua titik belum disimulasikan. |
| OPS-04 | P0 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| OPS-05 | P0 | **PARTIAL** | Restore DB lulus dengan overlay fixture uji; suite repo asli gagal fixture proof, backup objek/age harian belum lengkap. |
| OPS-06 | P1 | **PARTIAL** | Health container API produksi sehat; simulasi dependency mati dan alert belum diuji. |
| OPS-07 | P1 | **PARTIAL** | UI desktop beberapa alur diuji; tablet/ponsel dan semua state belum ditinjau. |
| OPS-08 | P1 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| OPS-09 | P1 | **PARTIAL** | Beberapa validasi 4xx diuji; fuzz input panjang/SQL/HTML belum lengkap. |
| OPS-10 | P1 | **PARTIAL** | Enam order: total vs line, empat Tempo charge vs paid dan jurnal Deposit cocok; semua perjalanan belum dijalankan. |
| OPS-11 | P2 | **NOT RUN** | Belum dieksekusi sebagai skenario lengkap; tidak dinyatakan lulus. |
| OPS-12 | P2 | **PARTIAL** | 100 submit paralel dan ekspor 50.000 baris lulus integrasi; p95 respons dan PDF 25+ baris belum diukur. |

## Bukti otomasi

- `/tmp/grahita-qa-web-all-20261027.log`: 245 tes frontend lulus.
- `/tmp/grahita-qa-go-remaining-4-20261027.log`: batch integrasi tambahan 4.
- `/tmp/grahita-qa-go-remaining-3-20261027.log`: batch integrasi tambahan 3.
- `/tmp/grahita-qa-go-remaining-2-20261027.log`: batch integrasi tambahan 2.
- `/tmp/grahita-qa-go-remaining-1-20261027.log`: batch integrasi tambahan 1.
- `/tmp/grahita-qa-go-tempo-targeted-20261027.log`: empat tes Tempo lulus.
- `/tmp/grahita-qa-go-money-targeted-20261027.log`: 15 tes keuangan/nomor/konkurensi lulus.
- `/tmp/grahita-qa-go-business-targeted-20261027.log`: 15 tes master/laporan/email lulus.
- `/tmp/grahita-qa-go-doc-regression-20261027.log`: uji tunggal dokumen gagal karena ekspektasi kolom XLSX lama.
- `/tmp/grahita-qa-go-report-50k-20261027.log`: ekspor 50.000 baris lulus.
- `/tmp/grahita-qa-go-backup-overlay-20261027.log`: backup/restore dengan fixture overlay lulus.
- `/tmp/grahita-qa-admin-vitest-serial-20261027.log`, `/tmp/grahita-qa-openapi-20261027.log`.
- `/opt/temp/grahita-tempo/.tmp-go-integration-20261027.log`: percobaan suite integrasi tanpa overlay; tetap gagal dan tidak dianggap hijau.
