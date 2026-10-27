# PRD — Sistem Pemesanan Cabang Grahita Indonesia

| | |
|---|---|
| **Produk** | Portal Pemesanan Cabang (Customer Portal + Admin Portal) |
| **Domain** | grahitaindonesia.com |
| **Versi** | 2.1 (tambahan pembayaran Tempo dan piutang cabang) |
| **Tanggal** | 8 Oktober 2026 |
| **Status** | Draft untuk review |
| **Dokumen terkait** | `SystemDesign.md`, `Architecture.md`, `Database.md` |

---

## 1. Ringkasan

Cabang Grahita memesan produk ke Pusat. Saat ini prosesnya manual (chat, email, rekap manual), sehingga penomoran tidak konsisten, verifikasi pembayaran lambat, dan rekap bulanan memakan waktu.

Sistem ini menyediakan satu kanal resmi: cabang membuat pesanan di Customer Portal, Pusat memverifikasi pembayaran dan mengelola master data di Admin Portal, dan sistem menghasilkan Invoice (PDF) serta Surat Jalan (Excel + PDF) secara otomatis beserta pengiriman emailnya.

**Desain UI mengacu pada prototype** `customer_v2.html` dan `admin_v2.html` — token warna brand (`--brand-primary #504068`, `--brand-secondary #906088`, `--brand-accent #FFF038`), tipografi Plus Jakarta Sans, ikon Phosphor, dan seluruh pola komponen di dalamnya adalah acuan desain yang mengikat untuk implementasi.

### 1.1 Tujuan terukur

| Tujuan | Indikator |
|---|---|
| Mempercepat verifikasi pembayaran | Median waktu `Submitted → Done` < 4 jam kerja |
| Menghilangkan penomoran manual | 100% invoice & serial number ter-generate sistem, tanpa duplikat |
| Mempercepat rekap | Laporan order line per cabang/periode tersedia < 1 menit (ekspor Excel) |
| Mengurangi kesalahan harga | 0 pesanan memakai harga di luar daftar harga cabang |
| Keamanan dokumen | Backup harian ke Google Drive berhasil ≥ 99% |

### 1.2 Di luar lingkup (fase ini)

Aplikasi mobile native, 2-step verification, integrasi akuntansi/ERP, manajemen stok dan gudang, multi-currency, pembayaran kartu kredit.

---

## 2. Pengguna & peran

| Peran | Portal | Deskripsi |
|---|---|---|
| **User Cabang** | Customer Portal | Satu akun per cabang, login dengan email cabang. Membuat pesanan, unggah bukti transfer, mengunduh invoice. |
| **Manager** | Admin Portal | Operasional harian: verifikasi pembayaran, kelola cabang, katalog, user cabang. |
| **Superadmin** | Admin Portal | Semua akses Manager + rekening bank, reporting, pengaturan sistem, user admin. |

### 2.1 Matriks hak akses

| Kapabilitas | User Cabang | Manager | Superadmin |
|---|:--:|:--:|:--:|
| Lihat katalog + harga cabang sendiri | ✓ | — | — |
| Buat / submit pesanan | ✓ | — | — |
| Unduh invoice cabang sendiri | ✓ | — | — |
| Dashboard admin (jumlah pesanan) | — | ✓ | ✓ |
| Dashboard admin (nilai rupiah) | — | — | ✓ |
| Riwayat pemesanan semua cabang | — | ✓ | ✓ |
| Verifikasi pembayaran | — | ✓ | ✓ |
| Unggah bukti transfer atas nama cabang | — | ✓ | ✓ |
| Revisi invoice | — | opsional¹ | ✓ |
| Master cabang & generate token | — | ✓ | ✓ |
| Katalog produk & matriks harga | — | ✓ | ✓ |
| Rekening bank | — | — | ✓ |
| Reporting & ekspor Excel | — | — | ✓ |
| User management — user cabang | — | ✓ | ✓ |
| User management — user admin | — | — | ✓ |
| Pengaturan sistem | — | — | ✓ |

¹ Dikendalikan oleh setting `manager_boleh_revisi` (default: aktif), sesuai toggle di halaman Pengaturan prototype.

---

## 3. Customer Portal

Navigasi (sidebar desktop / bottom-bar mobile): **Beranda · Katalog · Pesanan · Riwayat invoice · Tutorial · Profil**.

### 3.1 Registrasi (token)

| ID | Kebutuhan | Prioritas |
|---|---|---|
| CP-REG-01 | Admin men-generate token registrasi per cabang; token punya masa berlaku (`token_hari`, default 14 hari). | Must |
| CP-REG-02 | Halaman daftar langkah 1: input token → validasi (format, ada, belum dipakai, belum kedaluwarsa, cabang aktif). | Must |
| CP-REG-03 | Langkah 2: Nama Cabang, Nama Kepala Cabang, Email Cabang tampil terisi dan **read-only**. | Must |
| CP-REG-04 | Field yang diisi user: Nomor telepon, Password, Konfirmasi Password. | Must |
| CP-REG-05 | Kekuatan password minimal 8 karakter dengan indikator kekuatan; konfirmasi harus sama. | Must |
| CP-REG-06 | Setelah sukses, token ditandai terpakai dan tidak bisa dipakai lagi. 1 cabang = 1 akun. | Must |
| CP-REG-07 | Token salah/kedaluwarsa menampilkan pesan spesifik dan arahan menghubungi admin. | Should |

### 3.2 Login, lupa password, ganti password

| ID | Kebutuhan | Prioritas |
|---|---|---|
| CP-AUT-01 | Login dengan email cabang + password. Tanpa 2FA. | Must |
| CP-AUT-02 | Kredensial salah → pesan generik + tautan "Lupa password". | Must |
| CP-AUT-03 | Lupa password: input email cabang → email berisi link reset. Respons selalu sukses (tidak membocorkan keberadaan email). | Must |
| CP-AUT-04 | Link reset berlaku `reset_menit` (default 60) dan sekali pakai. | Must |
| CP-AUT-05 | Rate limit: maks 5 percobaan login gagal / 15 menit per akun + per IP. | Should |
| CP-AUT-06 | Ganti password dari Profil memakai mekanisme yang sama (verifikasi password lama atau link email). | Must |
| CP-AUT-07 | Setelah ganti/reset password, seluruh sesi lain di-logout. | Should |

### 3.3 Beranda

Ringkasan untuk cabang: jumlah pesanan periode berjalan, pesanan menunggu verifikasi, dan draft yang belum dikirim. Shortcut "Buat pesanan" dan daftar 5 pesanan terakhir.

### 3.4 Katalog (read-only)

| ID | Kebutuhan | Prioritas |
|---|---|---|
| CP-KAT-01 | Menampilkan hanya produk **aktif** yang punya harga untuk cabang tersebut. | Must |
| CP-KAT-02 | Harga yang tampil = harga khusus cabang. Produk tanpa harga cabang **tidak ditampilkan** dan tidak bisa dipesan. | Must |
| CP-KAT-03 | Tiap produk menampilkan nama, harga, deskripsi singkat. Produk tidak memiliki field satuan; `qty` tetap menyatakan jumlah item. | Must |
| CP-KAT-04 | Produk Sertifikat ditandai khusus beserta catatan biaya packaging. | Must |

### 3.5 Pesanan

**Status:** `Draft` (editable) → `Submitted` (terkunci, menunggu verifikasi) → `Done` (selesai).

| ID | Kebutuhan | Prioritas |
|---|---|---|
| CP-ORD-01 | List pesanan menampilkan nomor, tanggal, jumlah item, total, status, penanda C&C; dapat difilter per status. | Must |
| CP-ORD-02 | Editor pesanan: header (catatan) + order line (nama instansi bebas, produk dari master, qty, harga otomatis, subtotal) dengan total live. Nama instansi wajib pada setiap baris produk baru; produk yang sama boleh muncul beberapa kali untuk instansi berbeda dan setiap baris mendapat serial sendiri. | Must |
| CP-ORD-03 | **Save** → status Draft; draft bisa diubah dan dihapus. | Must |
| CP-ORD-04 | **Submit** → modal unggah bukti transfer (≥1 file, maks `max_file` file × `max_mb` MB, JPG/PNG/PDF). | Must |
| CP-ORD-05 | Saat submit: nomor invoice dan serial number per order line ter-generate, status → Submitted, pesanan terkunci. | Must |
| CP-ORD-06 | Tanggal pesanan = tanggal submit (zona waktu Asia/Jakarta). | Must |
| CP-ORD-07 | Bukti transfer boleh lebih dari satu (pembayaran dicicil/terpisah); nominal dijumlahkan saat verifikasi. | Must |
| CP-ORD-08 | Detail pesanan menampilkan timeline (dikirim, bukti diunggah, diverifikasi, dokumen dikirim). | Should |
| CP-ORD-09 | Pesanan Submitted/Done tidak dapat diedit atau dihapus cabang. | Must |
| CP-ORD-10 | Setelah Done, Invoice PDF dapat diunduh dari detail pesanan dan menu Riwayat invoice. | Must |

### 3.6 Aturan produk Sertifikat

| ID | Kebutuhan | Prioritas |
|---|---|---|
| CP-SER-01 | Harga satuan Sertifikat = 0 berapa pun qty-nya. | Must |
| CP-SER-02 | Sistem otomatis menambahkan satu baris **"Biaya packaging"** sebesar `biaya_packaging` (default Rp3.000) per pesanan sertifikat. Baris ini tidak bisa dihapus/diubah user. | Must |
| CP-SER-03 | Pesanan sertifikat tidak boleh dicampur produk reguler: memilih Sertifikat mengosongkan/menolak baris lain dengan penjelasan. | Must |
| CP-SER-04 | Satu pesanan sertifikat = satu invoice tersendiri. | Must |
| CP-SER-05 | Besaran biaya packaging dapat diubah Superadmin di Pengaturan; perubahan tidak mengubah pesanan lama. | Must |

### 3.7 Cash & Carry

Cash & Carry adalah **sistem deposit** (keputusan user 2026-10-18): cabang berflag C&C menyetor deposit lebih dulu, lalu pesanannya dibayar dari saldo deposit itu. Tidak ada lagi utang C&C, jatuh tempo, maupun pengingat jatuh tempo. Kontrak saldo, top-up, dan settlement ada di `design/branch-deposit.md`.

| ID | Kebutuhan | Prioritas |
|---|---|---|
| CP-CNC-01 | Cabang C&C ditentukan flag `cash_and_carry` di master cabang (dibaca server, tidak dipilih cabang saat submit). Hanya cabang C&C yang memakai deposit: menu Deposit dan top-up hanya untuk cabang ini. Cabang non-C&C tetap membayar dengan transfer + bukti per pesanan. | Must |
| CP-CNC-02 | Saat submit, saldo deposit cabang C&C langsung dipotong sebesar `min(saldo, total)` di dalam transaksi submit. Bila saldo cukup, bukti transfer tidak diperlukan; bila kurang, sisanya wajib ditransfer dengan minimal satu bukti. Saldo yang berubah sejak ditampilkan membatalkan submit (tidak ada potongan diam-diam). | Must |
| CP-CNC-03 | Pesanan C&C diberi penanda jelas di portal cabang dan admin. | Must |
| CP-CNC-04 | Top-up deposit diunggah cabang C&C dan baru menambah saldo setelah diverifikasi Superadmin. | Must |
| CP-CNC-05 | Pesanan C&C tetap Submitted sampai admin memverifikasi rincian pembayarannya (deposit + transfer = total); tidak ada Done otomatis. | Must |

### 3.8 Riwayat invoice, Profil, Tutorial

- **Riwayat invoice** — daftar invoice cabang, filter periode, unduh PDF, menampilkan versi revisi bila ada.
- **Profil** — data cabang read-only (nama, kepala cabang, email, telepon) + tombol ganti password.
- **Tutorial** — panduan bergambar: cara memesan, cara unggah bukti transfer, aturan sertifikat, aturan C&C. Konten dikelola sebagai halaman statis yang bisa diperbarui tanpa deploy ulang (fase 2).

---

## 4. Admin Portal (`/admin`)

Navigasi sesuai prototype: **Menu** (Dashboard, Riwayat pemesanan) · **Data master** (Cabang, Katalog product, Rekening bank*) · **Lainnya** (Reporting*, User management, Pengaturan*) · Profil. (*) hanya Superadmin.

### 4.1 Dashboard

| ID | Kebutuhan | Prioritas |
|---|---|---|
| AP-DSH-01 | Filter global: pilihan cabang (semua / satu cabang) dan periode (hari, minggu, bulan, tahun ini). | Must |
| AP-DSH-02 | Superadmin melihat metrik dalam rupiah; Manager melihat metrik dalam jumlah pesanan. | Must |
| AP-DSH-03 | Chip "perlu tindakan": menunggu verifikasi, perlu review, email gagal — dapat diklik ke daftar terfilter dan dapat disembunyikan. | Must |
| AP-DSH-04 | Grafik: tren 12 bulan berjalan dan tren harian bulan berjalan. | Should |
| AP-DSH-05 | Ringkasan status pembayaran periode terpilih (terverifikasi / menunggu / perlu review). | Should |
| AP-DSH-06 | Peringkat cabang berdasarkan nilai atau jumlah pesanan. | Could |
| AP-DSH-07 | Tata letak mengikuti `prototype/admin_v2.html`: sapaan, pembanding periode sebelumnya (kemarin / minggu lalu / bulan lalu / tahun lalu sampai tanggal yang sama), rata-rata nilai & item per pesanan, komposisi Reguler vs Cash and Carry, dan product terlaris dalam qty; filter cabang & periode di topbar (keputusan user 2026-10-17). | Should |

### 4.2 Riwayat pemesanan

| ID | Kebutuhan | Prioritas |
|---|---|---|
| AP-ORD-01 | Daftar pesanan seluruh cabang dengan pencarian (nomor, cabang, serial number) dan filter: status, status verifikasi, C&C, email gagal, rentang tanggal. | Must |
| AP-ORD-02 | Grouping: tanpa grup, per cabang, per hari, per bulan, per tahun. | Must |
| AP-ORD-03 | Paginasi server-side. | Must |
| AP-ORD-04 | Detail pesanan: header, order line + serial number, bukti transfer (dengan zoom), timeline, catatan, dokumen, log verifikasi, riwayat email. | Must |
| AP-ORD-05 | Admin dapat menambah catatan internal pada pesanan. | Must |
| AP-ORD-06 | Admin dapat mengunggah bukti transfer baru; sistem menandai pengunggah (admin + nama) dan mencatatnya di timeline. | Must |
| AP-ORD-07 | Tombol **Verifikasi**: menampilkan total pesanan vs total nominal terbaca + selisih, lalu admin menyetujui (→ Done) atau menandai perlu review dengan alasan (kurang bayar, lebih bayar, bukti tidak terbaca, rekening tidak sesuai). | Must |
| AP-ORD-08 | Admin dapat mengirim ulang email (invoice / surat jalan) yang gagal. | Must |
| AP-ORD-09 | Admin dapat mengunduh / mencetak Invoice dan Surat Jalan dari detail. | Must |

### 4.3 Revisi invoice

| ID | Kebutuhan | Prioritas |
|---|---|---|
| AP-REV-01 | Admin yang berwenang dapat merevisi pesanan ber-status Done: mengubah qty/harga/baris, dengan alasan wajib. | Must |
| AP-REV-02 | Nomor invoice tetap; versi bertambah (`-R1`, `-R2`) dan tercetak pada PDF. | Must |
| AP-REV-03 | Versi invoice sebelumnya disimpan (tidak ditimpa) dan dapat diunduh untuk audit. | Must |
| AP-REV-04 | Revisi menghasilkan Invoice PDF dan Surat Jalan PDF/XLSX versi baru dengan nama instansi per baris; email invoice dikirim ulang ke cabang dan admin ber-flag. | Must |
| AP-REV-05 | Seluruh revisi tercatat di timeline dan audit log (siapa, kapan, alasan, delta nilai). | Must |

### 4.4 Cabang

Field: Nama Cabang, Nama Kepala Cabang, Email Cabang, Telepon, **Cara pembayaran yang diizinkan** (pilih satu atau lebih dari Transfer di muka / Deposit Cash & Carry / Tempo), **tanggal bayar** 1–28 dan **tahan bila telat** untuk Tempo, **Pajak In / PI** (bool), Aktif (bool), Token registrasi + tombol **Generate token**. Cabang memilih tepat satu cara pembayaran untuk setiap pesanan saat dikirim; pilihan itu disimpan sebagai snapshot pesanan. Deposit tetap dapat ditambah dengan transfer untuk kekurangan saldo.

| ID | Kebutuhan | Prioritas |
|---|---|---|
| AP-CAB-01 | CRUD cabang; email cabang unik. | Must |
| AP-CAB-02 | Generate token menggantikan token lama yang belum terpakai. | Must |
| AP-CAB-03 | Cabang nonaktif: user tidak bisa login dan tidak muncul di pilihan aktif. | Must |
| AP-CAB-04 | Cabang tidak dapat dihapus bila sudah punya pesanan — hanya dinonaktifkan. | Must |
| AP-CAB-05 | Flag Special dihapus dari aplikasi. Kolom lama `is_special` tetap dorman untuk kompatibilitas migrasi. Efek PI terhadap invoice/harga masih perlu diputuskan (lihat §8). | Must |

### 4.5 Katalog product & matriks harga

| ID | Kebutuhan | Prioritas |
|---|---|---|
| AP-KAT-01 | CRUD produk: nama, jenis (reguler / sertifikat), harga default, urutan tampil, aktif. Produk tidak memiliki field satuan. | Must |
| AP-KAT-02 | Tab **Matriks harga**: grid Produk × Cabang, pencarian cabang. Setiap sel diisi komponen **HJM, P1 (%), dan SPV**; harga jual sel dihitung server = HJM + (HJM × P1) + SPV dan tidak diketik langsung (keputusan user 2026-10-17, rumus `design/master-impact-import.md` §7). | Must |
| AP-KAT-03 | Sel kosong = produk tidak dijual ke cabang itu (tidak tampil di portal cabang). | Must |
| AP-KAT-04 | Aksi "salin harga" dari satu cabang ke cabang lain / ke semua cabang. | Should |
| AP-KAT-05 | Perubahan harga tidak mengubah pesanan yang sudah ada (harga di-snapshot pada order line). | Must |
| AP-KAT-06 | Riwayat perubahan harga tercatat di audit log. | Should |

### 4.6 Rekening bank (Superadmin)

Field: Bank (BCA / BRI), Nama di rekening, Nomor rekening, Aktif. Rekening aktif ditampilkan di instruksi pembayaran Customer Portal dan dipakai sebagai referensi pencocokan rekening tujuan saat verifikasi AI.

### 4.7 Reporting (Superadmin)

| ID | Kebutuhan | Prioritas |
|---|---|---|
| AP-REP-01 | Baris laporan = order line dari seluruh pesanan (bukan per pesanan), termasuk nama instansi per baris pada API dan ekspor Excel. | Must |
| AP-REP-02 | Filter: rentang tanggal, cabang (multi), status, jenis produk, C&C, PI. | Must |
| AP-REP-03 | Kolom: tanggal, nomor invoice, versi, cabang, kepala cabang, C&C, PI, produk, nama instansi, jenis, serial number, qty, harga satuan, subtotal, status, tanggal verifikasi, metode verifikasi. | Must |
| AP-REP-04 | Ekspor Excel (.xlsx) dengan header ter-freeze dan format angka Rupiah; ekspor besar diproses asinkron lalu diunduh. | Must |
| AP-REP-05 | Ringkasan bulanan per cabang (rekap) sebagai sheet kedua. | Should |

### 4.8 User management

- **Tab User Cabang** — daftar akun cabang: nama cabang, kepala cabang, email, telepon, status (aktif / belum registrasi / nonaktif), login terakhir. Aksi: aktif-nonaktifkan, kirim ulang link reset password, lihat/generate token bila belum registrasi.
- **Tab User Admin (Superadmin)** — nama, email, role (Superadmin / Manager), flag **terima email Invoice**, flag **terima email Surat Jalan**, status aktif, status undangan. Aksi: undang admin baru via email, ubah role, nonaktifkan.

| ID | Kebutuhan | Prioritas |
|---|---|---|
| AP-USR-01 | Penerima email Invoice dan Surat Jalan ditentukan oleh flag per admin, bukan alamat yang di-hardcode. | Must |
| AP-USR-02 | Admin nonaktif atau masih berstatus undangan tidak menerima email dan tidak bisa login. | Must |
| AP-USR-03 | Superadmin terakhir yang aktif tidak boleh dinonaktifkan atau diturunkan role-nya. | Must |

### 4.9 Pengaturan (Superadmin)

| Setting | Default | Keterangan |
|---|---|---|
| `biaya_packaging` | 3000 | Biaya packaging pesanan sertifikat |
| `token_hari` | 14 | Masa berlaku token registrasi |
| `reset_menit` | 60 | Masa berlaku link reset password |
| `max_mb` / `max_file` | 5 / 5 | Batas unggah bukti transfer |
| `digit_invoice` | 2 | Jumlah digit urut pada nomor invoice |
| `manager_boleh_revisi` | true | Izin Manager merevisi invoice |
| `ai.aktif` | false | Mengaktifkan verifikasi bukti transfer otomatis |
| `ai.min_keyakinan` | 0.90 | Ambang keyakinan OCR untuk auto-approve |
| `ai.tol_kurang` / `ai.tol_lebih` | 0 / 0 | Toleransi selisih nominal (Rupiah) |
| `ai.digit_rek` | 4 | Jumlah digit akhir rekening yang dicocokkan |
| `ai.mundur` | 1 | Toleransi mundur tanggal transfer (hari) |

### 4.10 Profil admin

Nama, email (read-only), role (read-only), ganti password dengan mekanisme sama seperti customer.

---

## 5. Verifikasi bukti transfer

Tiga mode, dapat berjalan berdampingan:

| Mode | Cara kerja | Status |
|---|---|---|
| **Manual** | Admin membandingkan total pesanan dan nominal pada bukti, lalu klik Verifikasi. | Fase 1 — wajib ada |
| **AI / OCR** | Sistem membaca nominal, tanggal, bank, dan 4 digit akhir rekening tujuan dari gambar. Cocok + keyakinan ≥ ambang → otomatis Done. Selain itu → masuk antrean "perlu review" dengan alasan. | Fase 2 |
| **Virtual Account** | Callback bank menandai pembayaran lunas tanpa unggah bukti. | Fase 3 (R&D BCA, BRI TBD) |

Aturan AI:
- Multi-bukti → nominal dijumlahkan sebelum dibandingkan.
- Selisih di luar `tol_kurang`/`tol_lebih` → perlu review (alasan: kurang bayar / lebih bayar).
- Rekening tujuan tidak cocok dengan rekening aktif → perlu review.
- Tanggal transfer lebih tua dari `tanggal pesanan − ai.mundur` hari → perlu review.
- Semua hasil (nominal terbaca, keyakinan, alasan) disimpan untuk audit, dan admin selalu bisa menimpa keputusan AI.
- AI tidak pernah menolak pesanan secara final; keputusan akhir selalu bisa dilakukan manusia.

---

## 6. Dokumen & email

### 6.1 Dokumen

| Dokumen | Format | Dibuat saat | Penerima |
|---|---|---|---|
| Invoice | PDF | Status → Done, dan setiap revisi | Unduhan cabang, email cabang, email admin ber-flag Invoice |
| Surat Jalan | XLSX + PDF | Status → Done | Email admin ber-flag Surat Jalan |
| Laporan | XLSX | Diminta Superadmin | Unduhan |

Invoice memuat: logo & identitas PT Grahita Indonesia, nomor invoice (+ versi revisi), tanggal, data cabang, rincian order line dengan serial number, biaya packaging bila ada, total, rekening pembayaran, dan penanda LUNAS (pesanan C&C: "LUNAS — CASH & CARRY").

### 6.2 Email

| Template | Penerima | Pemicu |
|---|---|---|
| Link reset / ganti password | User cabang / admin | Permintaan reset |
| Undangan admin | Admin baru | Dibuat Superadmin |
| Invoice | Email cabang | Status Done & setiap revisi |
| Invoice | Admin ber-flag Invoice | Status Done & setiap revisi |
| Surat Jalan (XLSX + PDF) | Admin ber-flag Surat Jalan | Status Done |

| ID | Kebutuhan | Prioritas |
|---|---|---|
| EM-01 | Pengiriman email asinkron melalui outbox dengan retry berjenjang (maks 5 kali). | Must |
| EM-02 | Status email (antre / terkirim / gagal) tampil di detail pesanan, dengan tombol kirim ulang. | Must |
| EM-03 | Email gagal muncul sebagai chip "perlu tindakan" di dashboard admin. | Must |
| EM-04 | Pengiriman memakai akun Gmail Grahita (SMTP/OAuth2) — akun diminta ke Forta. | Must |

---

## 7. Aturan bisnis

| Kode | Aturan |
|---|---|
| BR-01 | 1 cabang = 1 akun. Registrasi hanya lewat token dari admin. |
| BR-02 | Serial number per order line: `YYYYMMDDXXX`, `XXX` urut 3 digit yang di-reset tiap hari (basis tanggal submit, Asia/Jakarta). |
| BR-03 | Nomor invoice: `YYYY-MM-XX`, `XX` urut ber-digit `digit_invoice` yang di-reset tiap bulan. Nomor invoice = nomor pesanan. |
| BR-04 | Tanggal pesanan = tanggal submit (WIB), bukan tanggal pembuatan draft. |
| BR-05 | Harga order line di-snapshot saat submit dan tidak ikut berubah bila master harga berubah. |
| BR-06 | Produk Sertifikat: harga 0, otomatis + baris biaya packaging, tidak boleh digabung produk lain, satu invoice tersendiri. |
| BR-07 | Pesanan Submitted/Done terkunci untuk cabang; perubahan hanya lewat revisi invoice oleh admin. |
| BR-08 | Pesanan C&C dibayar dari saldo deposit saat submit; bukti transfer hanya untuk sisa yang tidak tertutup saldo. Tidak ada jatuh tempo. |
| BR-09 | Penomoran hanya diberikan saat submit berhasil; nomor tidak boleh bolong atau dipakai ulang. |
| BR-10 | Semua perhitungan uang dalam Rupiah bulat (tanpa sen). |
| BR-11 | Semua tanggal bisnis memakai zona waktu Asia/Jakarta; penyimpanan memakai UTC. |

### 7.1 State machine pesanan

```
          save              submit                verifikasi cocok
(baru) ──────────► Draft ──────────────► Submitted ─────────────────► Done
                     │                      │   ▲                       │
                     │ hapus                │   │ admin unggah bukti /  │ revisi
                     ▼                      ▼   │ tandai perlu review   ▼
                  (hilang)            Perlu review ───────────────► Done (versi +1)
```

`Perlu review` adalah sub-status verifikasi dari `Submitted`, bukan status pesanan tersendiri.

---

## 8. Pertanyaan terbuka

| No | Pertanyaan | Dampak |
|---|---|---|
| 1 | Selesai: flag Special dihapus; termin pembayaran menggunakan cara pembayaran **Tempo** yang memiliki aturan tersendiri pada §11. | Logika harga & invoice |
| 2 | Apa efek **Pajak In (PI)** — apakah invoice perlu baris PPN/materai? | Perhitungan total & layout invoice |
| 3 | ~~Apakah C&C otomatis Done saat jatuh tempo lewat tanpa bayar?~~ Diputus 2026-10-18: C&C adalah sistem deposit tanpa jatuh tempo; pesanan tetap diverifikasi admin (CP-CNC-05). | State machine |
| 4 | Apakah cabang boleh membatalkan pesanan Submitted (status Batal)? | State machine & penomoran |
| 5 | Email Pusat untuk Surat Jalan: cukup via flag per admin, atau ada alamat tetap tambahan? | Konfigurasi email |
| 6 | Apakah nomor invoice berlanjut lintas cabang (global per bulan) atau per cabang? | Penomoran — asumsi saat ini: global per bulan |
| 7 | Format Surat Jalan (template Excel resmi Grahita?) | Generator dokumen |
| 8 | Retensi file bukti transfer dan backup (berapa tahun?) | Storage & biaya |
| 9 | Perlukah tanda tangan/stempel digital pada Invoice & Surat Jalan? | Generator PDF |
| 10 | Bank mana yang diprioritaskan untuk Virtual Account (BCA dulu?) dan lewat payment gateway atau host-to-host? | Fase 3 |

---

## 9. Kriteria penerimaan (ringkas)

1. Token kedaluwarsa ditolak; token valid memunculkan data cabang terkunci; akun terbentuk dan bisa login.
2. Dua cabang dengan harga berbeda melihat harga masing-masing untuk produk yang sama.
3. Submit 100 pesanan paralel menghasilkan 100 nomor invoice dan serial number unik berurutan tanpa bolong.
4. Memilih Sertifikat memunculkan baris biaya packaging Rp3.000 otomatis dan menolak pencampuran produk lain.
5. Cabang C&C bersaldo cukup dapat submit tanpa bukti dan saldonya terpotong tepat sebesar total; saldo kurang mewajibkan bukti untuk sisanya; dua submit yang berebut saldo tidak pernah membuat saldo negatif.
6. Verifikasi cocok mengubah status ke Done, menghasilkan Invoice PDF + Surat Jalan XLSX/PDF, dan mengirim email sesuai flag penerima.
7. Email gagal muncul di chip dashboard dan berhasil dikirim ulang.
8. Revisi invoice menaikkan versi, menyimpan PDF lama, dan mengirim ulang invoice baru.
9. Manager tidak dapat membuka Rekening bank, Reporting, dan Pengaturan (UI tersembunyi dan API menolak).
10. Ekspor Excel reporting menghasilkan jumlah baris sama dengan jumlah order line pada filter yang sama.
11. Backup harian muncul di folder Google Drive dan dapat di-restore pada lingkungan uji.

---

## 10. Tahapan rilis

| Fase | Isi |
|---|---|
| **F1 — MVP (inti)** | Auth & token, master cabang/produk/harga/rekening, pesanan + sertifikat + C&C, unggah bukti, verifikasi manual, Invoice & Surat Jalan, email outbox, riwayat pemesanan, dashboard, user management, pengaturan, backup harian. |
| **F2 — Otomasi** | Verifikasi AI/OCR, reporting lanjutan + ekspor asinkron, revisi invoice, halaman tutorial dinamis, audit log UI. |
| **F3 — Pembayaran** | Virtual Account BCA (lalu BRI), rekonsiliasi otomatis, laporan pembayaran. |

---

## 11. Pembayaran Tempo dan piutang cabang (keputusan 26 Oktober 2026)

1. **Periode** memakai bulan kalender dari tanggal pesanan **dikirim** menurut WIB. Pesanan yang dikirim 31 Oktober dan disetujui 2 November tetap masuk tagihan Oktober. Surat Tagihan terbit tanggal 1 bulan berikutnya; persetujuan terlambat dan revisi menghasilkan versi baru tanpa menimpa PDF lama.
2. **Jatuh tempo** setiap invoice adalah tanggal bayar cabang (1–28) di bulan berikutnya, yang disalin ke pesanan saat dikirim. Perubahan tanggal bayar cabang berlaku hanya untuk pesanan yang dikirim sesudah perubahan, termasuk jika masih dalam bulan yang sama. Bila approval terjadi setelah tanggal jatuh tempo asli, invoice memakai tanggal bayar berikutnya setelah approval. Hari libur tidak menggeser jatuh tempo. Status lewat jatuh tempo dimulai keesokan harinya.
3. **Persetujuan pesanan** Tempo tidak memerlukan bukti transfer. Manager/Superadmin menyetujui pesanan dan status menjadi `approved_unpaid` (Disetujui — belum lunas), lalu Invoice dan Surat Jalan terbit. `done` tetap berarti lunas penuh. Invoice dikirim ke cabang dan admin dengan `notif_invoice`; Surat Jalan hanya ke admin dengan `notif_surat_jalan`. Bila cabang mengizinkan beberapa cara pembayaran, cabang wajib memilih satu saat mengirim pesanan. Pesanan Tempo memakai tagihan, pesanan deposit memakai saldo deposit dengan transfer untuk kekurangannya, dan pesanan transfer memakai bukti transfer.
4. **Pelunasan** boleh sebagian. Bukti diunggah cabang dengan nominal dan rekening tujuan; Manager/Superadmin memverifikasi atau menolak. Nominal terverifikasi dialokasikan ke invoice tertua menurut tanggal kirim pesanan, lintas periode. Kelebihan bayar harus diputuskan Superadmin dan dicatat sebagai saldo kredit piutang Tempo terpisah dari deposit C&C; saldo itu otomatis memotong invoice berikutnya. Tidak ada batas piutang.
5. **Revisi** invoice Tempo boleh setelah sebagian/lunas. Koreksi positif menambah tagihan; jika jatuh tempo asli telah lewat, koreksi memakai tanggal bayar berikutnya. Koreksi negatif mengurangi sisa tagihan terlebih dahulu; nilai yang sudah dibayar menjadi saldo kredit Tempo dengan persetujuan Superadmin. Versi koreksi tercatat dan Surat Tagihan diterbitkan ulang sebagai versi baru.
6. **Pengingat** H-3, H, H+1, H+7 dikirim menurut jatuh tempo. Untuk tanggal bayar 1–3, H-3 terjadi sebelum Surat Tagihan terbit sehingga email H-3 bersifat umum tanpa nominal final; tidak dilewati. Email berikutnya memakai saldo aktual.
7. **Tahan Tempo** hanya jika toggle cabang aktif dan masih ada tagihan lewat jatuh tempo. Draft tetap boleh dibuat; submit dengan cara bayar Tempo ditolak, sedangkan Transfer di muka dan Deposit tetap dapat dipilih bila cabang mengizinkannya. Bukti pelunasan pending yang nominalnya menutup seluruh tunggakan menunda penahanan paling lama dua hari kerja. Unggahan ulang tidak memperpanjang jendela. Setelah tunggakan lunas, penahanan lepas otomatis. Superadmin dapat mematikan toggle dengan jejak audit.
8. **Pemantauan** menampilkan total piutang per cabang, tagihan lewat jatuh tempo, umur piutang 0–30/31–60/>60 hari dari tanggal jatuh tempo, dan ekspor XLSX. Surat Tagihan dikirim ke cabang dan admin yang menerima email invoice.

Perubahan ini menggunakan migrasi tambahan dan mengaktifkan mode Tempo bersama alur pelunasan; cabang lama tidak otomatis berubah cara pembayarannya.
