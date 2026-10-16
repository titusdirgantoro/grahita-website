# Desain kontrak impor master Impact

Status: spesifikasi desain untuk persetujuan tertulis; belum boleh diimplementasikan atau dijalankan terhadap data master.

## 1. Tujuan dan batas

Dokumen ini menetapkan kontrak deterministik untuk membaca workbook Impact, membuat preview cabang/produk/varian/tarif, menerapkannya secara idempoten di development atau staging, dan mengaudit hasilnya. Dokumen ini tidak mengubah kode, skema, seed, credential, atau data database.

Acuan yang tetap berlaku, kecuali delta eksplisit pada dokumen desain ini yang harus diselaraskan ke dokumen dasar saat implementasi:

- `PRD.md` §3.4 dan §4.5: katalog hanya menampilkan produk aktif yang memiliki tarif cabang; tidak ada baris tarif berarti tidak dijual. Kewajiban satuan pada kedua bagian tersebut digantikan oleh keputusan tanpa satuan pada dokumen ini.
- `PRD.md` §4.4: master dan akses cabang tetap dikelola admin, tetapi nama Kepala Cabang dan email boleh `NULL` selama identitas belum lengkap; token hanya tersedia setelah identitas siap.
- `PRD.md` BR-05/BR-10: harga pesanan disnapshot dan semua uang berupa Rupiah bulat.
- `Architecture.md` §2.2/§6/§9: transaksi eksplisit, uang bukan `float`, audit, staging otomatis tetapi produksi membutuhkan approval manual.
- `SystemDesign.md` §3–§4, §6–§7, dan §9–§10: server menghitung ulang harga, otorisasi tidak bergantung pada UI, dan kontrak diuji lintas lapisan. Field snapshot satuan serta jumlah master lama yang masih dicontohkan di sana harus diselaraskan saat implementasi.
- `Database.md` §1, §4.1, §4.3–§4.4, dan §8: master memakai `is_active`, tarif berpasangan cabang-produk, order line historis tidak berubah. Constraint identitas `NOT NULL` dan kolom satuan pada skema lama adalah delta yang harus dimigrasikan secara kompatibel-mundur.

Di luar lingkup: impor order/invoice lama, pembuatan akun, token registrasi, pengiriman email, perubahan counter nomor, dan penafsiran otomatis catatan bebas.

## 2. Sumber dan provenance

Sumber privat tidak boleh masuk Git, log publik, fixture, screenshot, atau ringkasan kartu.

| Atribut | Nilai |
|---|---|
| Workbook | salinan privat `Impact_1-source-private.xlsx` |
| Lokasi privat eksekusi | `/home/hermes/hermes-dev-data/profiles/swe-tiusapp/cache/documents/t_63a929d5/Impact_1-source-private.xlsx` |
| SHA-256 | `eaf02f20e86b7383bf06342bd26d44e1fc4c714ab59b34657dfc9e0c3c0f91f7` |
| Sheet | `Master Tarif (Opsi A)` |
| Rentang terdeteksi | `A1:AV36` |
| Header grup | baris 1 |
| Header komponen | baris 2 |
| Data | baris 3–36, tepat 34 baris cabang |

Importer wajib menolak sebelum preview bila hash yang diotorisasi tidak cocok, sheet tidak ada, rentang menyusut, dua baris header berubah, atau jumlah baris bernama cabang bukan 34. Hash file dicatat pada setiap import run.

## 3. Keputusan normatif

Kata HARUS, TIDAK BOLEH, dan BOLEH bersifat normatif.

1. Semua 34 baris dengan kolom B terisi adalah kandidat master cabang. Draft lama yang menyebut 16 cabang dan 7 produk sudah tidak menjadi sumber jumlah.
2. Impor aktual pertama hanya BOLEH di development atau staging. Produksi dilarang sampai preview staging tertentu mendapat approval tertulis terpisah dari CEO dan kartu implementasi produksi dibuka.
3. Email sumber tidak ada. Untuk cabang baru, email HARUS `NULL`; importer TIDAK BOLEH membuat placeholder. Ketiadaan email pada workbook adalah *tidak ada otoritas perubahan*, bukan instruksi mengosongkan email valid yang sudah ada: cabang existing yang terpetakan mempertahankan email terverifikasi, status registrasi, dan akses sahnya selama identitas wajib dari sumber tidak menjadi kosong. Satu baris tanpa Kepala Cabang HARUS diimpor dengan `branches.nama_kepala=NULL` dan status identitas belum lengkap bila cabang baru atau existing memang sudah tanpa akses. Importer TIDAK BOLEH membuat nama placeholder. Cabang dengan email `NULL` atau identitas wajib belum lengkap tidak boleh mempunyai `branch_user` aktif, token registrasi/reset-password yang dapat digunakan, sesi aktif, atau jalur registrasi/login. Baris `branch_users` nonaktif boleh dipertahankan hanya sebagai rekam audit existing dan tidak dianggap akses. Semua jalur autentikasi HARUS fail-closed sampai email valid dan identitas wajib lengkap; importer tidak boleh mengaktifkan kembali baris akun nonaktif secara otomatis.
4. Sel HJM kosong selalu berarti varian tidak tersedia untuk cabang, walaupun P1 terisi. Importer TIDAK BOLEH menghitung atau membuat tarif dari P1/SPV saja.
5. Grup tanpa satu pun HJM boleh dibuat sebagai produk/varian katalog nonaktif dan `saleable=false`; tidak dibuat tarif cabang dan tidak tampil di Customer Portal.
6. Blank P1 dan blank SPV dinormalisasi menjadi nol hanya bila HJM terisi. Nilai nonangka, negatif, P1 di luar 0–1, atau nilai uang pecahan adalah conflict.
7. Catatan bebas tidak pernah mengubah cabang, produk, varian, atau tarif secara otomatis. Catatan menghasilkan item `needs_review` yang harus diputus manusia.
8. IDV Upset Emosi, Sales, dan AKA adalah varian dari IDV. Ketiganya memakai tarif IDV cabang kecuali sumber masa depan menyediakan harga khusus eksplisit untuk varian tersebut.
9. File impor bersifat upsert terbatas, bukan sinkronisasi destruktif. Tidak munculnya entitas pada file baru TIDAK BOLEH menghapus, menonaktifkan, atau mengosongkan master yang sudah ada.
10. Apply harus atomik per import run. Conflict yang belum diselesaikan menahan apply seluruh run; tidak ada partial apply diam-diam.
11. Pengecualian eksplisit terhadap aturan non-destruktif pada butir 9 adalah sel HJM kosong di dalam triplet cabang-varian yang memang tercakup file: keadaan itu adalah deklarasi `unavailable`, bukan ketidakhadiran entitas. Jika pasangan tersebut mempunyai tarif aktif lama, apply HARUS menutup versi aktif dan menghapus/menonaktifkan projection saleable tanpa menghapus versi historis. Cabang, produk, atau triplet yang sama sekali tidak tercakup file tetap tidak boleh disentuh.
12. Produk dan varian TIDAK mempunyai satuan. Importer, model produk/varian, snapshot order, API, UI, validasi, dan gerbang `saleable` TIDAK BOLEH meminta, menyimpan, menampilkan, atau mereka satuan seperti `pcs`. `qty` tetap wajib sebagai jumlah item yang dipesan; HJM, P1, dan SPV membentuk tarif untuk satu item kuantitas, lalu subtotal tetap `qty × tarif satu item`.
13. Rekonsiliasi identitas existing bersifat non-destruktif dan fail-closed. Workbook tanpa email TIDAK BOLEH mengosongkan email existing. Email valid yang dipertahankan hanya atribut identitas dorman bila status sudah `identity_incomplete`; keberadaannya sendiri tidak memberi akses dan tidak otomatis berarti identitas `ready`. Setelah pemeriksaan `inconsistent_existing_access` pada butir 14 didahulukan, baris dengan Kepala Cabang kosong yang terpetakan ke cabang berstatus `ready`/`registered` dan state akses konsisten HARUS menghasilkan `conflict/existing_access_identity_blank`; apply seluruh run ditahan. Untuk reason code ini saja, resolusi per item oleh aktor berwenang boleh berupa: `preserve_existing_verified_identity`, tetapi hanya bila identitas existing lengkap, terverifikasi, berstatus `ready`/`registered`, dan state aksesnya konsisten; atau `revoke_access_and_mark_incomplete`, yang dalam transaksi yang sama menetapkan seluruh `branch_users.is_active=false`, merevoke seluruh registration/password-reset token yang belum terpakai melalui status/waktu revokasi audit-able (bukan memalsukan `used_at`), merevoke seluruh sesi aktif, lalu mengosongkan nama dan menetapkan `identity_incomplete` sambil mempertahankan email existing sebagai atribut dorman untuk audit/pemulihan identitas kelak. Baris akun nonaktif dan audit tidak dihapus, tetapi tidak boleh dapat dipakai untuk registrasi, reset password, pembuatan sesi, refresh sesi, atau login dan tidak boleh otomatis aktif lagi. Pada reconcile berikutnya, kombinasi `identity_incomplete` + tidak ada jalur akses usable mempunyai prioritas sebagai state aman meskipun email dorman masih valid; state ini bukan `existing_access_identity_blank`, tidak mengulang revokasi, dan tidak mengaktifkan akun.
14. `conflict/inconsistent_existing_access` mempunyai matriks resolusi yang lebih ketat dan TIDAK BOLEH diselesaikan dengan `preserve_existing_verified_identity`, baik Kepala Cabang sumber kosong maupun terisi. State `identity_incomplete` dengan akses usable harus tetap menahan seluruh apply sampai salah satu tindakan berikut selesai: (a) resolusi eksplisit `revoke_inconsistent_access` di dalam transaksi apply menonaktifkan seluruh akun dan merevoke seluruh token/sesi usable dengan invariant audit yang sama seperti butir 13, tetap mempertahankan status `identity_incomplete`, dan tidak mengaktifkan identitas dari nilai sumber; atau (b) identitas dan akses diperbaiki melalui workflow identitas terpisah sampai menjadi state konsisten, lalu preview baru dibuat dan approval/keputusan attempt lama dinyatakan kedaluwarsa. Bila hasil perbaikan adalah identitas `ready`/`registered` dan sumber Kepala Cabang tetap kosong, preview baru HARUS menghasilkan `conflict/existing_access_identity_blank` dan meminta resolusi butir 13. Tidak boleh ada hasil apply yang menyisakan akses usable pada status `identity_incomplete`, dan importer tidak boleh memilih resolusi otomatis.

## 4. Rekonsiliasi dokumen lama

`SystemDesign.md` §10 dan estimasi lain masih menyebut 16 cabang. File yang diotorisasi memiliki 34 baris data bernama cabang. Implementasi kelak harus memperbarui dokumentasi jumlah setelah spesifikasi ini disetujui; importer menggunakan 34 sebagai invariant file ini.

Model lama `branches.email NOT NULL`, `branches.nama_kepala NOT NULL`, dan `branch_prices(harga)` belum cukup untuk sumber ini karena:

- tidak ada email pada seluruh 34 baris;
- satu baris sumber tidak mempunyai Kepala Cabang;
- tarif perlu menyimpan HJM, P1, SPV, hasil pembulatan, provenance, dan versi;
- IDV mempunyai beberapa varian yang berbagi tarif dasar.

Ketiadaan email di workbook tidak mempunyai semantik sinkronisasi destruktif. Untuk insert, importer menulis `email=NULL`; untuk cabang existing yang terpetakan, importer membaca email/status/relasi akses saat reconcile dan mempertahankan email existing. Kombinasi tidak konsisten—misalnya status incomplete tetapi masih memiliki `branch_users` aktif, token yang dapat digunakan, sesi aktif, atau reset-password aktif—adalah conflict yang menahan seluruh apply sampai akses direvoke secara atomik atau state diperbaiki melalui workflow identitas terpisah dan di-preview ulang. Preservasi bukan resolusi sah untuk state tidak konsisten. Baris `branch_users` nonaktif tanpa jalur autentikasi usable boleh tetap ada sebagai audit dan bukan conflict. Email valid yang tersisa pada status `identity_incomplete` juga bukan conflict selama seluruh akses sudah direvoke; email itu dorman, tidak cukup untuk `ready`, dan tidak boleh dipakai membuka jalur autentikasi. Khusus sel Kepala Cabang kosong yang akan menurunkan identitas existing berstatus `ready`/`registered` atau masih berakses, berlaku dua resolusi atomik pada §3.13; blank tidak boleh diam-diam menimpa identitas sah maupun meninggalkan akses lama hidup. Setelah resolusi revokasi, prioritas reconcile adalah state incomplete tanpa akses, bukan keberadaan email dorman, sehingga attempt baru deterministik dan tidak mengulang conflict/revokasi lama.

Dokumen dasar juga masih menganggap produk mempunyai satuan: `PRD.md` CP-KAT-03/AP-KAT-01, `SystemDesign.md` model `OrderLine` dan catatan migrasi §10, serta `Database.md` `products.satuan`, `order_lines.satuan_snapshot`, dan query katalog. Keputusan CEO adalah produk tidak menggunakan satuan sama sekali. Implementasi kelak harus menyelaraskan kontrak UI/API, model, query, dokumen, dan migrasi agar tidak ada field satuan produk maupun default `pcs`, sambil mempertahankan `qty`, tarif per satu item, dan formula subtotal. Perubahan dokumen dasar tersebut tidak dilakukan oleh amendemen desain ini.

Kartu implementasi wajib merancang migrasi kompatibel-mundur. Dokumen ini tidak mengotorisasi migrasi tersebut.

## 5. Model domain target

Nama tabel berikut adalah kontrak konseptual; implementasi boleh menyesuaikan nama tetapi tidak boleh menghilangkan invariant.

### 5.1 Cabang dan identitas sumber

`branches` tetap entitas utama. Tambahan konseptual:

- `jabatan_kepala text NULL` untuk kolom C;
- `email citext NULL` sampai email valid diisi;
- `registration_status = 'identity_incomplete' | 'ready' | 'registered'` atau representasi ekuivalen; `identity_incomplete` selalu menahan autentikasi walaupun email existing dipertahankan;
- `nama_kepala text NULL`; nilai `NULL` dari satu baris sumber menandai identitas belum lengkap dan bukan alasan membuat placeholder. Nilai ini bukan conflict untuk insert/existing tanpa akses, tetapi menjadi conflict rekonsiliasi bila akan menurunkan identitas existing sebagaimana §3.13.

Status `ready` hanya boleh dicapai setelah email valid serta seluruh identitas wajib, termasuk `nama_kepala`, telah lengkap. Pembuatan atau aktivasi `branch_user`, token registrasi, sesi, reset password, dan login harus menolak status `identity_incomplete` maupun email `NULL`; tidak cukup hanya menyembunyikan aksi di UI. Retensi baris `branch_users` nonaktif untuk audit tidak melanggar invariant ini selama seluruh jalur autentikasi menolaknya dan reaktivasi memerlukan proses identitas eksplisit di luar importer.

Aturan di atas berlaku langsung untuk insert dan cabang existing tanpa akses. Untuk cabang existing yang terpetakan, field email yang tidak tersedia di sumber tidak ikut dalam update set. Reconcile HARUS memuat existence check untuk email, status, baris `branch_users`, registration/password-reset token yang dapat digunakan, dan sesi aktif. Urutan klasifikasi normatif adalah: (1) status `identity_incomplete` dengan akses usable tetap `conflict/inconsistent_existing_access`; (2) nama sumber kosong terhadap status `ready`/`registered` atau akses usable menjadi `conflict/existing_access_identity_blank`; (3) status `identity_incomplete` tanpa akses usable aman walaupun email valid dipertahankan sebagai atribut dorman, sehingga tidak menjadi conflict akses lama. Matriks resolusi mengikuti §3.13–§3.14: preservasi hanya sah untuk `existing_access_identity_blank` dengan identitas ready/registered yang lengkap dan state akses konsisten; `inconsistent_existing_access` wajib direvoke atomik atau diperbaiki di luar importer dan di-preview ulang. Optimistic lock mencakup seluruh state identitas dan akses tersebut agar akses yang lahir setelah preview menggagalkan apply.

`master_import_identities` memetakan identitas sumber ke UUID internal:

- unique `(source_system, entity_kind, source_key)`;
- `source_system='impact'`;
- branch `source_key='branch:' + normalize(B)` pada penciptaan pertama;
- mapping yang sudah tersimpan tidak dihitung ulang ketika nama display diubah;
- perubahan nama yang tidak cocok dengan mapping lama menjadi conflict dan membutuhkan remap eksplisit, bukan insert otomatis.

Normalisasi key: trim, Unicode normalize NFKC, lowercase, rangkaian selain huruf/angka menjadi satu `-`, lalu trim `-`. Preview menampilkan fingerprint/hash, bukan PII mentah, untuk mendeteksi duplikat. Kolom A hanya nomor diagnostik dan bukan identity key karena posisi baris dapat berubah.

### 5.2 Produk dan varian

Pisahkan konsep keluarga produk dari pilihan yang dipesan:

- `products`: `code` immutable, nama display, urutan, `is_active`, `saleable`;
- `product_variants`: `code` immutable, `product_id`, nama display, `tariff_parent_variant_id NULL`, `is_active`; tidak memiliki kolom satuan;
- order line merujuk varian terpilih, bukan menebak dari teks nama.

Produk/varian dengan tarif valid dapat `saleable=true` tanpa satuan. `qty` tetap bilangan kuantitas positif dan bukan representasi satuan produk.

Kode stabil dari file:

| Grup | Kode keluarga | Kode varian tarif utama | Default awal |
|---|---|---|---|
| SETORAN KLASIKAL LENGKAP | `setoran-klasikal-lengkap` | `setoran-klasikal-lengkap` | aktif bila ada tarif |
| IQ12 | `iq12` | `iq12` | aktif bila ada tarif |
| KHUSUS - PPM | `ppm` | `ppm` | aktif bila ada tarif |
| KHUSUS - IDV | `idv` | `idv` | aktif bila ada tarif |
| KHUSUS - MUKPB | `mukpb` | `mukpb` | aktif bila ada tarif |
| KHUSUS - AU | `au` | `au` | aktif bila ada tarif |
| KHUSUS - MGRPLUS | `mgrplus` | `mgrplus` | aktif bila ada tarif |
| KHUSUS - CSR | `csr` | `csr` | nonaktif bila seluruh HJM kosong |
| EXPRES - KLASIKAL | `expres-klasikal` | `expres-klasikal` | nonaktif bila seluruh HJM kosong |
| EXPRES - IDV | `expres-idv` | `expres-idv` | nonaktif bila seluruh HJM kosong |
| EXPRES - S.EXPRES | `s-expres` | `s-expres` | nonaktif bila seluruh HJM kosong |
| PTSD | `ptsd` | `ptsd` | aktif hanya bila ada tarif valid |
| KLINIS | `klinis` | `klinis` | aktif hanya bila ada tarif valid |

Varian tambahan `idv-upset-emosi`, `idv-sales`, dan `idv-aka` memiliki `tariff_parent_variant_id` ke `idv`. Override eksplisit, bila kelak ada, disimpan sebagai tarif varian anak dan menang atas parent. Nama alias dari catatan tidak boleh mengubah kode immutable.

### 5.3 Tarif berversi

Tarif efektif adalah append-only `branch_tariff_versions` atau model ekuivalen, dengan minimal:

- `branch_id`, `variant_id`;
- `hjm_rupiah bigint`, `p1_ratio numeric`/basis point eksak, `spv_rupiah bigint`;
- `final_item_rupiah bigint` (tarif untuk satu item kuantitas, bukan satuan produk);
- `source_import_run_id`, `source_sheet`, `source_row`, `source_columns`;
- `source_component_hash`;
- `valid_from`, `superseded_at`, `is_available`;
- unique satu versi aktif per `(branch_id, variant_id)`.

`branch_prices` yang ada boleh menjadi projection kompatibilitas dari versi aktif, tetapi bukan satu-satunya rekam audit. Semua perubahan tarif tercatat di audit log.

Versi `is_available=false` adalah tombstone append-only untuk transisi tarif aktif menjadi unavailable. Tombstone menyimpan provenance triplet sumber dan `source_component_hash` yang membedakan blank HJM dari tarif bernilai nol. Saat tombstone menjadi versi aktif, `branch_prices` untuk pasangan itu HARUS tidak ada atau berstatus nonaktif sehingga lookup katalog maupun validasi submit order sama-sama menolak penjualan. Versi tersedia sebelumnya tetap tersimpan dengan `superseded_at`; order historis tetap menunjuk snapshot/version lamanya. Triplet blank yang sudah mempunyai tombstone aktif dengan component hash sama adalah `skip`, bukan tombstone baru.

## 6. Matriks kolom

Frasa sumber “kelompok produk E:AO” berarti titik awal grup berada dari E sampai AO. Karena setiap grup adalah tiga kolom, grup terakhir yang mulai di AO berakhir di AQ. Parser HARUS membaca triplet lengkap E:AQ; berhenti di AO akan membuang P1/SPV KLINIS.

| Kolom | Makna | Aturan |
|---|---|---|
| A | nomor sumber | diagnostik saja; wajib terisi tetapi bukan key |
| B | Cabang | wajib, trim; duplicate normalized key = conflict |
| C | Jabatan | nullable, trim; blank sah |
| D | Kepala Cabang | nullable, trim; blank sah dan menghasilkan `nama_kepala=NULL` + status `identity_incomplete` untuk insert/existing tanpa akses; existing beridentitas atau berakses mengikuti conflict §3.13 |
| E:G | SETORAN KLASIKAL LENGKAP | HJM, P1, SPV |
| H:J | IQ12 | HJM, P1, SPV |
| K:M | KHUSUS - PPM | HJM (header komponen tertulis PPM), P1, SPV |
| N:P | KHUSUS - IDV | HJM (header komponen tertulis IDV), P1, SPV |
| Q:S | KHUSUS - MUKPB | HJM (header komponen tertulis MUKPB), P1, SPV |
| T:V | KHUSUS - AU | HJM (header komponen tertulis AU), P1, SPV |
| W:Y | KHUSUS - MGRPLUS | HJM (header komponen tertulis MGRPLUS), P1, SPV |
| Z:AB | KHUSUS - CSR | HJM, P1, SPV |
| AC:AE | EXPRES - KLASIKAL | HJM (header komponen tertulis KLASIKAL), P1, SPV |
| AF:AH | EXPRES - IDV | HJM (header komponen tertulis IDV), P1, SPV |
| AI:AK | EXPRES - S.EXPRES | HJM (header komponen tertulis S.EXPRES), P1, SPV |
| AL:AN | PTSD | HJM (header komponen tertulis PTSD), P1, SPV |
| AO:AQ | KLINIS | HJM (header komponen tertulis KLINIS), P1, SPV |
| AR:AT | CATATAN KHUSUS | simpan privat sebagai evidence item; selalu `needs_review`, tidak diterapkan otomatis |
| AU:AV | di luar kontrak | blank diharapkan; nilai apa pun menjadi `needs_review`/conflict struktural |

Label kolom pertama triplet boleh berupa HJM atau kode produk seperti PPM/IDV. Secara semantik semuanya adalah HJM/tarif dasar untuk satu item kuantitas; istilah ini tidak membentuk satuan produk.

## 7. Perhitungan harga

Gunakan aritmetika desimal/rational eksak, bukan binary float.

```text
raw_item = HJM + (HJM × P1) + SPV
final_item = round_half_up(raw_item, 0 Rupiah)
subtotal = qty × final_item
```

`round_half_up` berarti pecahan tepat 0,5 dibulatkan menjauh dari nol; semua input valid nonnegatif. Pembulatan hanya sekali setelah ketiga komponen dijumlahkan, bukan per komponen. Qty dikalikan setelah tarif satu item bulat. `item` di sini adalah satu hitungan kuantitas dan bukan satuan produk.

Contoh deterministik non-PII:

- HJM 100.000, P1 0,10, SPV 2.500 → raw 112.500 → tarif satu item 112.500; qty 3 → 337.500.
- HJM 10.001, P1 0,05, SPV 0 → raw 10.501,05 → tarif satu item 10.501.
- HJM kosong, P1 0,10, SPV 2.500 → tidak tersedia; tidak ada tarif dan tidak ada fallback.
- HJM 10.005, P1 0,10, SPV 0 → raw 11.005,5 → tarif satu item 11.006.

Order line saat submit wajib menyimpan snapshot minimal: varian terpilih, varian sumber tarif, nama, HJM, P1, SPV, aturan pembulatan, tarif satu item, qty, subtotal, dan tariff version ID. Snapshot TIDAK BOLEH mempunyai field satuan. Perubahan master tidak boleh mengubah order `submitted`/`done`. Draft dihitung ulang dari tarif aktif saat submit; bila berubah sejak draft disimpan, API mengembalikan harga terbaru dan meminta konfirmasi sesuai kontrak implementasi.

## 8. Pipeline preview dan apply

### 8.1 Tahap

1. `ingest`: simpan file privat, hash, aktor, lingkungan, waktu, dan sheet.
2. `parse`: baca header dan 34 baris tanpa menulis master.
3. `normalize`: bentuk source key, kode varian, angka desimal, dan fingerprint komponen.
4. `validate`: jalankan invariant struktur, identitas, tarif, dan catatan.
5. `reconcile`: bandingkan dengan identity mapping dan versi tarif aktif.
6. `preview`: hasil immutable dengan status per item dan agregat.
7. `approve`: hanya staging; approval mengikat `run_id + source_hash + preview_hash`.
8. `apply`: satu transaksi atomik; verifikasi ulang hash dan optimistic lock.
9. `verify`: baca ulang jumlah/master hash tanpa memaparkan PII.

### 8.2 Status preview

| Status | Makna |
|---|---|
| `insert` | source key belum dipetakan dan data valid; cabang boleh beridentitas belum lengkap sesuai keputusan §3.3 |
| `update` | identity sama, nilai material berbeda; ketiadaan email di workbook tidak pernah dihitung sebagai update email |
| `skip` | nilai ter-normalisasi dan component hash identik |
| `conflict` | ambiguous identity, nilai invalid, header berubah, field wajib selain pengecualian identitas §3.3 belum diputus, concurrent change, status `identity_incomplete` masih mempunyai akses usable, atau blank Kepala Cabang akan menurunkan identitas existing lengkap yang `ready`/`registered` dengan state akses konsisten; email dorman pada `identity_incomplete` tanpa akses bukan conflict |
| `needs_review` | catatan bebas/kolom di luar kontrak yang tidak boleh diterka; menahan apply sampai resolved/ignored dengan alasan |

Untuk tarif, `update` mencakup transisi available → unavailable ketika HJM pada triplet yang tercakup eksplisit kosong. Preview memberi reason code `tariff_mark_unavailable`, menyebut version ID lama secara privat, dan menghitungnya sebagai satu update tarif; preview TIDAK BOLEH menampilkan harga lama sebagai fallback. Jika triplet blank belum pernah mempunyai tarif, hasilnya `skip` dengan reason `already_unavailable` dan tidak membuat tombstone yang tidak diperlukan. Ketidakhadiran seluruh cabang, produk, atau triplet dari scope file bukan `tariff_mark_unavailable` dan tidak menghasilkan mutasi.

Untuk cabang existing, preview wajib menampilkan hasil rekonsiliasi identitas secara privat dan deterministik:

- email valid existing tanpa blank pada identitas wajib: email/status/access dipertahankan; ketiadaan email sumber tidak menghasilkan mutasi;
- status `identity_incomplete` dan tidak ada access existing (termasuk bila email valid dipertahankan sebagai atribut dorman atau hanya ada baris `branch_users` nonaktif yang tidak mempunyai token/sesi usable): cabang tetap fail-closed, email tidak dihapus, dan import cabang/tarif lain yang valid boleh dilanjutkan;
- status incomplete tetapi masih ada `branch_users` aktif, token yang dapat digunakan, sesi aktif, atau reset-password aktif: `conflict/inconsistent_existing_access`, terlepas dari apakah email `NULL` atau valid;
- setelah `inconsistent_existing_access` disisihkan lebih dahulu, Kepala Cabang sumber blank terhadap identitas existing lengkap berstatus `ready`/`registered` dengan state akses konsisten: `conflict/existing_access_identity_blank`, lalu hanya resolusi §3.13 yang boleh membuka apply. Keberadaan email valid saja tidak memicu reason code ini bila status sudah `identity_incomplete` dan seluruh akses telah direvoke.

Matriks resolusi reason code bersifat normatif:

| Reason code | Kondisi | Resolusi yang sah | Resolusi yang ditolak |
|---|---|---|---|
| `existing_access_identity_blank` | Identitas existing lengkap, `ready`/`registered`, akses konsisten, tetapi nama sumber kosong | `preserve_existing_verified_identity` atau `revoke_access_and_mark_incomplete` | preservasi bila identitas tidak lengkap/state akses tidak konsisten |
| `inconsistent_existing_access` | Status `identity_incomplete` tetapi masih ada akun/token/sesi/reset-password usable, baik nama sumber kosong maupun terisi | `revoke_inconsistent_access` secara atomik; atau perbaikan identitas+akses melalui workflow terpisah lalu preview baru | `preserve_existing_verified_identity`, reuse approval lama, atau apply yang menyisakan akses usable |

Resolver HARUS memvalidasi reason code dan precondition lagi di dalam transaksi. Perubahan yang membuat resolusi tidak lagi cocok menggagalkan seluruh apply sebagai optimistic conflict.

Preview menyimpan versi/`updated_at` serta fingerprint relasi akses yang diperiksa. Apply membaca ulang semuanya di dalam transaksi; perubahan email, status, user, token, sesi, atau reset-password sejak preview adalah concurrent conflict dan menggagalkan seluruh run. Scope dokumen ini tidak membuat akun/token baru; revokasi hanya boleh terjadi sebagai resolusi eksplisit `revoke_access_and_mark_incomplete` atau `revoke_inconsistent_access`, bukan efek samping default impor.

Preview wajib mempunyai hitungan terpisah untuk branch, product, variant, tariff, note, serta daftar issue berdasarkan source row dan kode field tanpa nama personal pada log umum. Setiap catatan khusus atau kolom di luar kontrak menjadi item preview tersendiri yang harus diputus `ignored_with_reason`, `mapped_by_explicit_rule`, atau `source_corrected`; tidak boleh diabaikan otomatis secara agregat.

### 8.3 Idempotensi

- Identitas artefak sumber dipisahkan dari identitas attempt. `master_import_artifacts` (atau ekuivalen) unique pada `(environment, source_hash, contract_version)` dan dapat direferensikan banyak attempt; artefak yang sama tidak disalin atau dianggap file baru.
- Setiap permintaan preview membuat `master_import_attempt` dengan `attempt_id` unik, `artifact_id`, aktor, waktu, dan hasil preview. Karena attempt tidak unique pada source hash, preview/import kedua atas file identik tetap dapat dijalankan dan diaudit.
- Setiap attempt apply mempunyai `apply_idempotency_key` unique. Retry dengan key yang sama mengembalikan hasil apply attempt tersebut; permintaan baru dengan key berbeda melakukan reconcile baru terhadap state saat ini.
- Upsert cabang memakai persisted source identity, bukan nomor baris.
- Produk/varian memakai kode immutable pada §5.2.
- Tarif memakai `(branch_id, variant_id, source_component_hash)` dan hanya menambah versi bila komponen material berubah.
- Keputusan item `needs_review` dan resolusi conflict identitas berlaku hanya untuk `attempt_id` yang memuat preview tersebut; keputusan tidak diwariskan otomatis ke attempt baru, walaupun `source_hash` sama. Attempt baru HARUS menerbitkan kembali note/kolom luar kontrak sebagai `needs_review` dan conflict identitas berdasarkan state terkini, lalu meminta keputusan eksplisit baru. Kebijakan ini sengaja menghindari reuse keputusan yang sudah kedaluwarsa ketika identitas atau akses berubah.
- Menjalankan file sama dua kali sebagai dua attempt: setelah issue per-item pada attempt kedua diputus kembali, semua kandidat **mutasi master/tarif** yang state-nya identik dengan hasil apply pertama harus `skip`, termasuk triplet blank yang sudah unavailable. Status review/conflict sebelum resolusi tidak dihitung sebagai `skip`. Attempt kedua menambah audit operasional preview/reconcile dan audit keputusan per-item baru, tetapi tidak membuat audit mutasi master, versi tarif, tombstone, akun, atau token baru bila tidak ada perubahan state.
- Retry apply attempt yang sama memakai idempotency key yang sama, tidak membuat attempt/audit tambahan, dan mengembalikan hasil apply pertama.

## 9. Validasi file ini

Pemeriksaan privat terhadap hash di §2 menghasilkan fakta non-PII berikut:

- 34/34 baris data mempunyai Cabang; normalized branch key unik 34/34.
- Kolom C kosong pada beberapa baris dan sah.
- Tidak ada satu pun alamat email pada data.
- Satu baris mempunyai Kepala Cabang kosong; baris itu sah diimpor dengan `nama_kepala=NULL` dan status `identity_incomplete`, tanpa user aktif/token usable/akses login, bila insert atau terpetakan ke existing tanpa identitas/access yang harus dipreservasi. Baris user nonaktif existing boleh tetap ada hanya untuk audit. Existing `ready`/`registered` yang konsisten mengikuti §3.13, sedangkan existing `identity_incomplete` yang masih berakses mengikuti §3.14.
- Semua nilai HJM/P1/SPV yang terisi bertipe angka, tidak negatif, dan P1 berada pada rentang 0–1.
- Ada HJM kosong dengan P1 terisi; semua kasus tersebut tetap unavailable.
- Ada grup tanpa harga dan ada grup yang hanya mempunyai sangat sedikit harga; aturan nonaktif/tidak dijual berlaku.
- Ada catatan khusus dan satu nilai di luar matriks kontrak; semuanya `needs_review`, tanpa penafsiran otomatis.

Apply tidak boleh lolos hanya karena total baris 34; seluruh issue di atas harus terlihat pada preview.

## 10. Proteksi lingkungan, approval, dan rollback

### 10.1 Larangan produksi

Importer harus fail-closed bila `APP_ENV=production`. Tidak boleh ada flag UI tunggal untuk melewatinya. Kartu produksi terpisah kelak minimal mensyaratkan:

- preview staging sukses atas source hash yang sama;
- QA menyetujui bukti idempotensi dan rekonsiliasi;
- approval tertulis CEO mengikat preview hash;
- backup dan restore drill tersedia;
- kredensial produksi terpisah;
- dua kontrol eksplisit pada CLI/job, termasuk environment allowlist dan approval artifact yang belum kedaluwarsa.

Spesifikasi ini sendiri bukan approval produksi.

### 10.2 Rollback staging

Sebelum apply, simpan encrypted before-snapshot dari entitas yang akan berubah dan versi/`updated_at` masing-masing. Rollback staging:

- hanya untuk `run_id` tertentu;
- satu transaksi;
- menghapus insert yang belum direferensikan transaksi lain;
- mengaktifkan kembali versi tarif sebelumnya;
- untuk `tariff_mark_unavailable`, menutup/membatalkan tombstone milik run dan memulihkan projection `branch_prices` dari versi available sebelumnya;
- mengembalikan update hanya jika optimistic version masih sama;
- menolak dan melaporkan conflict bila ada perubahan sesudah import;
- tidak menghapus audit trail atau source provenance.

Revokasi credential/sesi oleh `revoke_access_and_mark_incomplete` atau `revoke_inconsistent_access` adalah batas keamanan yang tidak boleh dibalik otomatis dari before-snapshot. Untuk run yang memuat resolusi tersebut, rollback staging tetap satu transaksi tetapi memakai kompensasi fail-closed berikut:

- `branch_users` yang dinonaktifkan tetap nonaktif;
- token registrasi/reset-password dan sesi yang direvoke tetap revoked dan tidak boleh dihidupkan kembali, termasuk bila nilai before-snapshot masih menunjukkan usable;
- status cabang tetap `identity_incomplete` dan email tetap dorman; nama yang dikosongkan oleh resolusi blank tidak otomatis dipulihkan karena hal itu dapat membentuk identitas ready semu dengan akun nonaktif;
- pemulihan identitas dilakukan hanya lewat workflow terpisah oleh aktor berwenang setelah email dan identitas wajib diverifikasi, lalu membuat credential/token/sesi baru; credential lama tidak pernah direaktivasi;
- audit rollback mencatat `identity_revocation_not_reversed`, entity IDs, aktor, run asal, dan alasan, selain hasil rollback master/tarif lain.

Dengan demikian, frasa “mengembalikan before-snapshot” hanya berlaku pada perubahan yang reversibel. Rollback run revokasi boleh mengembalikan master/tarif lain, tetapi tidak boleh menghasilkan status `ready` dengan akses nonaktif yang tidak dijelaskan maupun status `identity_incomplete` dengan akses usable.

Rollback tidak pernah mengubah order line historis. Produksi, bila kelak diizinkan, memakai compensating import/version baru alih-alih penghapusan audit.

## 11. Kontrak keamanan dan audit

- Hanya Superadmin atau worker khusus berotorisasi yang boleh membuat preview/apply; service tetap memeriksa role.
- Upload tetap melalui kontrol MIME/magic byte, ukuran, nama file generated, dan storage privat.
- Preview UI menyamarkan nama personal kecuali aktor berizin membuka detail.
- Log aplikasi hanya memuat run ID, hash, row number, entity kind, source-key fingerprint, status, dan error code.
- Audit apply mencatat aktor, environment, source/preview hash, counts, before/after entity IDs, dan alasan resolution; tidak menyimpan workbook mentah dalam `audit_logs`.
- Catatan bebas disimpan sebagai evidence privat dengan akses terbatas dan tidak dipindahkan ke deskripsi produk.

## 12. Keputusan eksplisit dan ambiguitas

### 12.1 Keputusan CEO untuk amendemen ini

1. Satu Kepala Cabang kosong diizinkan menjadi `branches.nama_kepala=NULL` dengan status `identity_incomplete`. Cabang baru atau existing tanpa akses dapat masuk master development/staging, tetapi tidak boleh mempunyai user aktif, token yang dapat digunakan, sesi aktif, reset password, registrasi, atau login sampai email valid dan identitas wajib lengkap. Baris user nonaktif boleh tetap ada untuk audit setelah revokasi, tetapi seluruh autentikasi harus menolaknya dan importer tidak boleh mengaktifkannya kembali. Placeholder nama/email dilarang. Untuk cabang existing, ketiadaan email sumber tidak menghapus email terverifikasi; email tersebut boleh tetap tersimpan sebagai atribut dorman pada status `identity_incomplete` dan tidak memberi akses. Blank Kepala Cabang hanya menjadi conflict penurunan identitas bila state existing masih `ready`/`registered` atau mempunyai akses usable; preservasi hanya boleh untuk identitas terverifikasi yang lengkap dan state akses konsisten, sedangkan state `identity_incomplete` dengan akses usable wajib direvoke atomik atau diperbaiki melalui workflow identitas terpisah dan di-preview ulang sebagaimana §3.13–§3.14. Attempt berikutnya harus mengklasifikasikan state incomplete tanpa akses sebagai aman meskipun email dorman tetap valid. Rollback staging tidak boleh menghidupkan kembali akun/token/sesi yang telah direvoke; pemulihan identitas wajib eksplisit dan memakai credential baru.
2. Produk tidak menggunakan satuan. Produk bertarif valid dapat `saleable=true` tanpa satuan; tidak ada mapping/default `pcs`, field satuan pada varian/order snapshot/API, tampilan unit di UI, atau validasi satuan. `qty` dan formula harga tetap berlaku.

Keputusan yang masih terbuka dan tidak boleh diterka oleh importer:

3. Apakah jabatan hanya metadata profil atau perlu ditampilkan pada dokumen. Impor menyimpan nilai apa adanya tetapi tidak memberi efek bisnis sebelum diputus.
4. Makna setiap catatan khusus dan nilai di luar AU:AV. Setiap item harus di-resolve `ignored_with_reason`, `mapped_by_explicit_rule`, atau `source_corrected`; tidak boleh inference atau pengabaian otomatis.
5. Efek flag Special/PI tetap mengikuti pertanyaan terbuka PRD dan tidak diturunkan dari tarif/catatan file.

### 12.2 Sudah diputus oleh kontrak ini

- 34 baris, bukan 16.
- email nullable tanpa placeholder; tidak ada akun aktif/token usable/login sebelum email valid, sementara akun nonaktif existing boleh dipertahankan untuk audit tanpa jalur autentikasi.
- nama Kepala Cabang nullable untuk baris sumber yang kosong; status identity incomplete menahan seluruh akses akun sampai identitas wajib lengkap.
- produk/varian dan snapshot order tidak mempunyai satuan; `qty` serta tarif per satu item kuantitas tetap ada.
- HJM kosong = unavailable meski P1 ada.
- blank P1/SPV = nol hanya saat HJM ada.
- tarif satu item memakai satu pembulatan half-up ke Rupiah terdekat sebelum qty.
- komponen dan snapshot historis disimpan.
- varian IDV yang disebutkan mewarisi tarif IDV kecuali override eksplisit.
- grup produk boleh ada nonaktif/tidak dijual.
- catatan bebas selalu review manual.
- produksi dilarang tanpa approval terpisah.

## 13. Rencana pengujian implementasi

### 13.1 Unit

1. Parser menerima tepat sheet/header/rentang yang dikontrak dan menolak perubahan struktur.
2. Normalisasi 34 nama menghasilkan key unik tanpa menulis PII ke log.
3. P1/SPV blank menjadi nol hanya dengan HJM terisi.
4. HJM blank + P1 terisi menghasilkan unavailable, bukan harga nol atau inherited price.
5. Half-up: kasus 10.005 × 1,10 menghasilkan 11.006; tidak memakai bankers rounding.
6. Tarif satu item dihitung sebelum qty; overflow `int64` ditolak dan subtotal tetap `qty × final_item`.
7. Catatan AR:AT dan nilai AU:AV menghasilkan `needs_review` tanpa mutasi.
8. IDV Upset Emosi/Sales/AKA resolve ke tariff parent IDV; override eksplisit menang.
9. Produk/varian bertarif valid menjadi saleable tanpa field satuan; parser/validator tidak meminta atau membuat `pcs`.
10. Kepala Cabang blank untuk insert atau existing berstatus `identity_incomplete` tanpa akses usable dinormalisasi menjadi `NULL` + `identity_incomplete`, bukan placeholder; email valid dorman boleh dipertahankan. Existing `ready`/`registered` dengan state akses konsisten menghasilkan `existing_access_identity_blank`, sedangkan existing `identity_incomplete` dengan akses usable menghasilkan `inconsistent_existing_access` terlepas dari nilai Kepala Cabang sumber.
11. Rekonsiliasi cabang existing tidak memasukkan email ke update set ketika workbook tidak menyediakan email; email/status/access terverifikasi dipertahankan.
12. Kepala Cabang blank yang terpetakan ke cabang existing lengkap berstatus `ready`/`registered` dan state akses konsisten menghasilkan `conflict/existing_access_identity_blank`; state `identity_incomplete` tanpa akses tidak menghasilkan conflict hanya karena email dorman valid, dan resolver tidak boleh memilih preservasi atau revokasi otomatis.
13. Keputusan `needs_review` dan resolusi identitas terikat `attempt_id`; resolver menolak penggunaan keputusan attempt lama pada attempt baru meskipun source hash dan item fingerprint sama.
14. Resolver menerima `preserve_existing_verified_identity` hanya untuk `existing_access_identity_blank` dengan identitas lengkap, status `ready`/`registered`, dan state akses konsisten; resolver menolaknya untuk `inconsistent_existing_access`, baik nama sumber kosong maupun terisi.

### 13.2 Integrasi database

1. Preview tidak mengubah tabel master.
2. Apply staging atomik: satu conflict menggagalkan seluruh transaksi.
3. Import pertama menghasilkan counts sesuai preview. Attempt kedua untuk file identik menerbitkan kembali setiap `needs_review`/conflict per-item dan memerlukan keputusan baru; sesudah keputusan itu, seluruh kandidat mutasi master/tarif yang tidak berubah menjadi `skip`, tidak menambah baris master/versi/audit mutasi, dan hanya mencatat audit operasional serta audit keputusan attempt sebagaimana §8.3.
4. Perubahan satu komponen membuat tepat satu versi tarif baru dan menutup versi lama.
5. Tarif aktif lama yang triplet sumbernya kini mempunyai HJM blank dipreview sebagai `update/tariff_mark_unavailable`; apply membuat tepat satu tombstone/penutupan versi, menghilangkan projection saleable, dan lookup katalog serta submit order menolak pasangan tersebut.
6. Apply ulang file blank yang sama menghasilkan `skip/already_unavailable`, tanpa tombstone, versi, atau audit mutasi master tambahan.
7. File yang menghilangkan produk/cabang/triplet dari scope tidak menonaktifkan atau menghapus data.
8. Rollback staging atas `tariff_mark_unavailable` mengaktifkan versi available sebelumnya dan memulihkan projection; order historis tidak berubah.
9. Dua attempt untuk file/hash identik boleh tercipta. Catatan AR:AT/AU:AV kembali menjadi `needs_review` dan tidak mewarisi keputusan attempt pertama; setelah diputus ulang, kandidat mutasi master/tarif yang identik menjadi `skip` dan hanya menambah audit operasional/keputusan attempt, bukan audit mutasi master.
10. Retry apply dengan idempotency key yang sama mengembalikan hasil pertama tanpa attempt atau audit tambahan.
11. Cabang email NULL tidak mempunyai user aktif/token usable/sesi aktif; bila ada baris user nonaktif untuk audit, endpoint generate token, reset password, registrasi, pembuatan/refresh sesi, dan login tetap menolak dengan error terarah.
12. Order submitted/done mempertahankan snapshot sebelum impor; draft memakai tarif aktif saat submit.
13. Rollback staging mengembalikan before-snapshot untuk perubahan reversibel dan menolak bila data berubah sesudah apply; revokasi identitas mengikuti kompensasi fail-closed §10.2 dan tidak menghidupkan credential/sesi lama.
14. Cabang `nama_kepala=NULL` dapat di-apply ke master development/staging sebagai `identity_incomplete`; pembuatan/aktivasi user, token, registrasi, reset password, sesi, dan login semuanya fail-closed sampai email valid dan identitas wajib lengkap. Baris user nonaktif yang dipertahankan untuk audit tidak dapat digunakan dan tidak otomatis aktif lagi.
15. Order produk menyimpan qty, tarif satu item, dan subtotal yang benar tanpa kolom satuan pada master varian maupun snapshot historis; perubahan tarif tidak mengubah snapshot lama.
16. Cabang existing dengan email valid, identitas sumber lengkap, serta akun/token/sesi aktif mempertahankan email, status, baris `branch_users`, token, dan sesi; workbook tanpa email tidak membuat update email atau revokasi akses.
17. Cabang existing dengan email valid dan identitas sumber lengkap tetapi tanpa `branch_users`, token, atau sesi juga mempertahankan email/status dan tidak dibuatkan akses baru.
18. Cabang existing berstatus `identity_incomplete` dan tanpa user aktif/token usable/sesi/reset-password dapat menerima update master/tarif yang valid tanpa pembuatan akses, baik emailnya `NULL` maupun email valid dipertahankan sebagai atribut dorman; baris user nonaktif untuk audit tidak mengubah hasil ini.
19. Cabang existing berstatus incomplete tetapi mempunyai `branch_users` aktif, token yang dapat digunakan, sesi aktif, atau reset-password aktif menghasilkan `conflict/inconsistent_existing_access` dan menahan seluruh apply, baik emailnya `NULL` maupun valid serta baik Kepala Cabang sumber kosong maupun terisi. `preserve_existing_verified_identity` ditolak; apply hanya dapat lanjut melalui `revoke_inconsistent_access` yang atomik atau setelah workflow identitas terpisah memperbaiki state dan preview baru disetujui. Tidak satu pun hasil boleh mempertahankan akses usable pada status incomplete.
20. Cabang existing dengan identitas lengkap berstatus `ready`/`registered`, state akses konsisten, dan Kepala Cabang sumber blank menghasilkan `conflict/existing_access_identity_blank`. Resolusi preservasi tidak mengubah nama/email/status/access; resolusi revokasi menetapkan seluruh `branch_users.is_active=false`, merevoke seluruh registration/password-reset token yang belum terpakai melalui status/waktu revokasi audit-able tanpa memalsukan `used_at`, dan merevoke seluruh sesi aktif sebelum nama menjadi `NULL` dan status menjadi incomplete, semuanya atomik dan ter-audit tanpa menghapus akun, audit, atau email existing. Setelah revokasi, email valid tetap tersimpan hanya sebagai atribut dorman; baris akun nonaktif tetap ada, tetapi generate token/reset password/registrasi/login/pembuatan atau refresh sesi semuanya ditolak dan akun tidak otomatis aktif lagi.
21. Attempt baru setelah resolusi preservasi menerbitkan kembali `conflict/existing_access_identity_blank` dan meminta preservasi/revokasi diputus ulang; bila preservasi dipilih lagi dan state tidak berubah, tidak ada mutasi identitas/akses baru. Attempt baru setelah resolusi revokasi melihat status `identity_incomplete`, nama `NULL`, email valid dorman yang tetap tersimpan, dan tidak ada akses usable; prioritas reason code mengklasifikasikannya sebagai state aman, mempertahankan baris akun nonaktif untuk audit, tidak menerbitkan `existing_access_identity_blank`/`inconsistent_existing_access`, tidak mengulang mutasi revokasi, dan tidak mengaktifkan akun kembali. Note tetap `needs_review` dan kandidat master/tarif identik menjadi `skip` setelah review selesai.
22. Perubahan salah satu email/status/user/token/sesi/reset-password setelah preview menggagalkan apply lewat optimistic conflict; keputusan dari preview/attempt lama tidak boleh dipakai untuk melewati reconcile baru, dan tidak pernah ada apply yang mengosongkan identitas sambil menyisakan akses lama.
23. Rollback staging terhadap run yang menjalankan `revoke_access_and_mark_incomplete` atau `revoke_inconsistent_access` tidak mengaktifkan kembali `branch_users`, token, reset-password token, atau sesi lama; cabang tetap `identity_incomplete`, email tetap dorman, audit `identity_revocation_not_reversed` tercatat, dan login/refresh dengan credential lama tetap ditolak.
24. Pemulihan setelah rollback revokasi hanya melalui workflow identitas eksplisit: identitas diverifikasi, akun diaktifkan atau dibuat sesuai kebijakan implementasi, dan token/sesi baru diterbitkan. Token/sesi lama tetap invalid dan rollback tidak boleh menghasilkan state `ready` dengan akun nonaktif tanpa status pemulihan yang eksplisit.

### 13.3 Kontrak/E2E

1. Preview menampilkan insert/update/skip/conflict/needs-review dan hitungan per entity.
2. Produk dengan tarif tampil hanya pada cabang terkait; blank HJM tidak tampil/tidak bisa dipesan.
3. Grup nonaktif tidak tampil di Customer Portal.
4. Manager/non-Superadmin ditolak oleh API walaupun memanggil endpoint langsung; CSRF tetap wajib.
5. Produksi selalu fail-closed tanpa approval artifact; spesifikasi/kartu ini tidak dapat dipakai sebagai bypass.
6. Preview dan log umum tidak membocorkan Kepala Cabang, catatan bebas, atau isi workbook.
7. Kontrak API katalog, draft, detail order, dan laporan tidak mensyaratkan atau mengembalikan satuan produk; payload dengan satuan tidak diperlukan untuk membuat produk saleable.
8. Customer/Admin UI dan dokumen order tidak menampilkan label unit/satuan untuk produk, tetapi tetap menerima dan menampilkan `qty`, tarif satu item, dan subtotal yang dihitung server.
9. Admin tidak dapat menghasilkan token bagi cabang `identity_incomplete` atau email `NULL`, termasuk lewat pemanggilan API langsung.
10. Preview menampilkan conflict dan pilihan resolusi identitas existing hanya kepada aktor berizin; tanpa resolusi eksplisit apply ditolak, dan log umum tetap tidak memuat email/nama/token/sesi mentah.

## 14. Gerbang sebelum kartu implementasi

Kartu implementasi baru boleh dibuka setelah:

1. QA menyetujui konsistensi spesifikasi dan rencana tes;
2. CEO memberi persetujuan tertulis atas dokumen amendemen lengkap ini setelah review QA; keputusan bisnis §12.1 sudah menjadi input normatif, bukan izin implementasi;
3. source hash dan lokasi privat tetap tersedia;
4. scope implementasi memisahkan migrasi/model, parser-preview, apply/rollback staging, UI approval, dan gerbang produksi;
5. tidak ada pekerjaan produksi tersirat dalam approval staging.

Kartu implementasi juga wajib menyelaraskan delta terhadap dokumen dasar yang dirinci di §1 dan §4: nullable `branches.nama_kepala`/email dengan akses akun fail-closed, serta penghapusan satuan produk dari model, snapshot, UI/API, validasi, query, dan dokumen tanpa menghapus `qty`. Persetujuan implementasi tidak sekaligus mengizinkan impor produksi; produksi tetap memerlukan preview staging dan persetujuan terpisah sebagaimana §10.1.
