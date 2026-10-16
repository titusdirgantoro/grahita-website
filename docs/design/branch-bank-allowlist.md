# D2 — Kontrak rekening tujuan yang diizinkan per cabang

Status: rancangan untuk persetujuan tertulis; belum boleh diimplementasikan.

Kartu: `t_56d591b3`.

## 1. Tujuan dan batas lingkup

Dokumen ini menetapkan kontrak fail-closed agar setiap cabang hanya melihat dan
menggunakan subset rekening bank pusat yang secara eksplisit ditetapkan oleh
Superadmin.

Rujukan utama:

- `docs/PRD.md` §2, §4.4, §4.6, dan §5;
- `docs/Architecture.md` §2.2, §6, dan §9;
- `docs/SystemDesign.md` §2, §4.3, §6, §7, dan §9;
- `docs/Database.md` §1, §4.1, §4.3, §4.5, dan §8;
- `docs/design/f2-26/HASIL.md` untuk perilaku OCR yang sudah ada.

Lingkup rancangan:

1. relasi many-to-many cabang–rekening;
2. aturan rekening efektif untuk instruksi pembayaran dan verifikasi;
3. API dan otorisasi pengelolaan allowlist;
4. UI Customer Portal dan Admin Portal;
5. histori, audit, konkurensi, error, migrasi, dan rencana pengujian.

Di luar lingkup:

- pembuatan atau perubahan rekening nyata;
- perubahan data, kode, migrasi, seed, atau konfigurasi produksi;
- Virtual Account; VA tetap OFF dan kontrak F3-30 tidak berubah;
- saldo deposit/pembayaran campuran, yang dirancang terpisah di D3;
- perubahan aturan nominal, tanggal, keyakinan OCR, atau state machine pesanan
  selain validasi rekening tujuan yang dijelaskan di sini.

## 2. Keputusan normatif

Kata HARUS, TIDAK BOLEH, dan BOLEH di dokumen ini bersifat normatif.

1. Superadmin menetapkan nol, satu, atau beberapa `bank_accounts` untuk setiap
   cabang. Tidak ada rekening bawaan global.
2. Rekening efektif bagi cabang adalah irisan:
   `assignment diizinkan` AND `bank_accounts.is_active` AND
   `branches.is_active`.
3. Hanya rekening efektif yang:
   - tampil sebagai instruksi pembayaran Customer Portal;
   - dapat dipilih sebagai rekening tujuan oleh admin;
   - dapat meloloskan pemeriksaan rekening OCR;
   - dapat dipakai oleh alur top-up/deposit D3 kelak.
4. Ketiadaan assignment adalah keadaan bisnis sah tetapi memblokir transfer.
   Sistem TIDAK BOLEH mengarang rekening, mengambil semua rekening aktif,
   mengambil rekening pertama, atau memakai fallback konfigurasi lama.
5. Otorisasi dan validasi dilakukan di service/backend. Menyembunyikan rekening
   atau tombol di UI saja tidak cukup.
6. Penonaktifan rekening global atau pencabutan assignment berlaku untuk
   keputusan verifikasi baru. Pesanan yang sudah `done` tidak dibuka kembali.
7. Bukti yang rekening tujuannya tidak terbaca, ambigu, nonaktif, atau tidak
   diizinkan masuk `perlu_review`; AI tidak boleh menandainya `done`.
8. Admin manual tidak boleh mengubah rekening yang tidak efektif menjadi sah
   hanya dengan override. Untuk menyetujui pembayaran, setiap bukti yang
   diperhitungkan HARUS dipetakan ke rekening efektif cabang pada titik
   linearizasi verifikasi. Bukti mismatch boleh ditinjau, dicatat, dan ditolak,
   tetapi tidak boleh meloloskan order.
9. Mutasi assignment dan keputusan verifikasi harus dapat diaudit tanpa
   menyimpan nomor rekening lengkap di log aplikasi, pesan error, atau payload
   audit umum.
10. Identitas rekening (`bank` + `nomor`) immutable setelah rekening dibuat.
    Koreksi identitas dilakukan dengan membuat rekening baru dan menonaktifkan
    rekening lama; endpoint update tidak boleh menulis ulang identitas yang
    dapat mengubah arti bukti atau invoice historis.
11. Implementasi hanya boleh dimulai setelah QA menyetujui dokumen ini dan CEO
    memberi persetujuan spesifikasi secara tertulis.

## 3. Model domain

### 3.1 Istilah

- Rekening global: baris `bank_accounts` yang dikelola Superadmin.
- Assignment: hubungan satu cabang dengan satu rekening global.
- Rekening efektif: assignment berstatus diizinkan, rekening global aktif, dan
  cabang aktif.
- Allowlist version: nomor revisi monotonik per cabang untuk mencegah lost
  update dan mendeteksi keputusan OCR yang memakai snapshot basi.
- Keputusan verifikasi: satu baris immutable `verification_logs` beserta hasil
  tujuan per bukti.

### 3.2 Invarian

Untuk cabang `B`, himpunan rekening efektif adalah:

```text
Effective(B) = {
  A | branch_bank_accounts(B, A).is_allowed = true
      AND bank_accounts(A).is_active = true
      AND branches(B).is_active = true
}
```

Invarian wajib:

- pasangan `(branch_id, bank_account_id)` unik;
- satu pasangan tidak dihapus untuk operasi normal; cabut/aktifkan ulang dengan
  `is_allowed` agar histori relasi tetap tersedia;
- assignment ke rekening nonaktif tidak boleh dibuat/diaktifkan;
- perubahan `bank_accounts.is_active` langsung memengaruhi semua cabang tanpa
  menghapus assignment;
- `bank_allowlist_version` adalah revisi input kebijakan, bukan hash
  `Effective(B)`. Ia bertambah tepat satu per cabang dalam satu transaksi bila
  salah satu input kebijakan cabang berubah: set assignment `is_allowed`,
  `branches.is_active`, atau `bank_accounts.is_active` dari rekening yang sedang
  `is_allowed`. Karena itu versi juga berubah saat cabang sedang nonaktif atau
  assignment yang diubah sedang dormant; mutasi idempoten tidak menaikkan versi;
- endpoint customer tidak menerima `branch_id` dari klien; cabang selalu
  berasal dari sesi;
- satu cabang tidak dapat memakai assignment cabang lain walaupun mengetahui
  UUID rekening atau UUID assignment;
- `done` hanya dapat terjadi bila validasi rekening dijalankan lagi di dalam
  transaksi final, bukan hanya berdasarkan hasil OCR yang dibaca sebelumnya.

## 4. Kontrak database

Bagian ini adalah rancangan target, bukan migrasi yang dijalankan oleh kartu D2.

### 4.1 Perubahan `branches`

Tambahkan versi allowlist per cabang:

```sql
ALTER TABLE branches
  ADD COLUMN bank_allowlist_version bigint NOT NULL DEFAULT 0
  CHECK (bank_allowlist_version >= 0);
```

Versi dipakai untuk optimistic concurrency pada Admin API dan revalidasi OCR.
Nilai `0` berarti belum pernah mempunyai mutasi input kebijakan D2 sejak fitur
berlaku; nilai ini bukan izin memakai rekening global.

Walaupun namanya `bank_allowlist_version`, versi ini merepresentasikan revisi
seluruh input kebijakan rekening cabang, bukan hanya hasil `Effective(B)` saat
itu. Versi HARUS bertambah bila:

- set assignment `is_allowed` berubah, termasuk ketika cabang nonaktif atau
  rekening globalnya nonaktif;
- `branches.is_active` berubah, termasuk ketika set assignment kosong; atau
- rekening global yang sedang `is_allowed` bagi cabang berubah status aktif,
  termasuk ketika cabang tersebut nonaktif.

Dengan demikian setiap perubahan input yang dapat menentukan efektivitas
mempunyai token concurrency baru. Kenaikan yang konservatif ketika
`Effective(B)` kebetulan tetap kosong memang dapat membuat attempt OCR lama
stale, tetapi tidak pernah memberi izin secara permissive dan mencegah dua edit
assignment dormant memakai versi yang sama. Mutasi idempoten tidak menaikkan
versi atau menulis audit sukses baru.

### 4.2 Tabel `branch_bank_accounts`

```sql
CREATE TABLE branch_bank_accounts (
    branch_id       uuid NOT NULL
        REFERENCES branches(id) ON DELETE RESTRICT,
    bank_account_id uuid NOT NULL
        REFERENCES bank_accounts(id) ON DELETE RESTRICT,
    is_allowed      boolean NOT NULL DEFAULT true,
    assigned_by     uuid NOT NULL
        REFERENCES admin_users(id) ON DELETE RESTRICT,
    assigned_at     timestamptz NOT NULL DEFAULT now(),
    revoked_by      uuid
        REFERENCES admin_users(id) ON DELETE RESTRICT,
    revoked_at      timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (branch_id, bank_account_id),
    CONSTRAINT branch_bank_accounts_lifecycle CHECK (
      (is_allowed AND revoked_by IS NULL AND revoked_at IS NULL) OR
      (NOT is_allowed AND revoked_by IS NOT NULL AND revoked_at IS NOT NULL)
    )
);

CREATE INDEX branch_bank_accounts_effective
  ON branch_bank_accounts (branch_id, bank_account_id)
  WHERE is_allowed;

CREATE INDEX branch_bank_accounts_by_account
  ON branch_bank_accounts (bank_account_id, branch_id)
  WHERE is_allowed;
```

Semantik:

- assignment baru mengisi `assigned_*`, `is_allowed=true`, dan `revoked_*=NULL`;
- pencabutan mengubah baris yang sama menjadi `is_allowed=false` dan mengisi
  `revoked_*`;
- pengaktifan ulang mengubah baris yang sama menjadi `is_allowed=true`,
  mengosongkan `revoked_*`, dan memperbarui `assigned_*` ke aktor/waktu terbaru;
- riwayat setiap perubahan tetap dicatat di `audit_logs`; kolom lifecycle pada
  tabel ini menyimpan keadaan terakhir, bukan seluruh rangkaian kejadian;
- tidak ada `ON DELETE CASCADE` dari rekening karena rekening yang pernah
  dipakai harus dinonaktifkan, bukan dihapus.

### 4.3 Snapshot keputusan tujuan pembayaran

`payment_proofs.ocr_rek_akhir` yang sudah ada tetap merupakan hasil pembacaan,
bukan bukti bahwa rekening diizinkan. Keputusan allowlist harus disimpan per
attempt verifikasi agar perubahan assignment di masa depan tidak menulis ulang
sejarah.

Tambahkan ke `verification_logs`:

```sql
ALTER TABLE verification_logs
  ADD COLUMN branch_allowlist_version bigint,
  ADD COLUMN proof_set_fingerprint bytea,
  ADD COLUMN destination_results jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE orders
  ADD COLUMN done_verification_log_id uuid
      REFERENCES verification_logs(id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX verification_logs_ai_policy_once
  ON verification_logs (
    order_id, branch_allowlist_version, proof_set_fingerprint
  )
  WHERE metode = 'ai'
    AND branch_allowlist_version IS NOT NULL
    AND proof_set_fingerprint IS NOT NULL;
```

`destination_results` adalah array immutable, satu elemen per bukti yang ikut
attempt tersebut:

```json
[
  {
    "payment_proof_id": "<uuid>",
    "source": "ocr",
    "status": "allowed",
    "bank_account_id": "<uuid>",
    "bank_snapshot": "BCA",
    "account_owner_snapshot": "PT Contoh",
    "account_last_digits_snapshot": "1234"
  }
]
```

Nilai `source`: `ocr` atau `manual`.

Nilai `status`:

- `allowed` — tepat satu rekening efektif cocok;
- `unreadable` — tujuan tidak terbaca/tidak dipilih;
- `ambiguous` — digit yang dibaca cocok dengan lebih dari satu rekening
  efektif;
- `not_allowed` — rekening dikenali tetapi bukan assignment cabang;
- `inactive` — rekening/assignment dikenali tetapi tidak efektif;
- `unknown` — tidak dapat dipetakan ke master rekening.

Aturan penyimpanan:

- snapshot hanya memuat kode bank dan digit akhir sebanyak `ai.digit_rek`,
  nama pemilik saat keputusan, serta UUID rekening; tidak memuat nomor lengkap;
- `branch_allowlist_version` wajib terisi pada setiap attempt setelah fitur ini
  diberlakukan, termasuk attempt review;
- `proof_set_fingerprint` adalah SHA-256 dari representasi kanonik seluruh
  `(payment_proof_id, ocr_at)` yang diurutkan berdasarkan byte UUID. Setiap tuple
  dikodekan sebagai UUID lowercase, byte pemisah `0x1f`, lalu timestamp UTC
  RFC3339Nano; tuple dipisahkan `0x1e`. Fingerprint berubah bila bukti baru masuk
  atau hasil OCR suatu bukti memang dibuat ulang;
- `destination_results` tidak diperbarui setelah INSERT;
- account UUID boleh tetap menunjuk master yang kemudian nonaktif; keputusan
  historis dibaca dari snapshot dan versi, bukan dievaluasi ulang;
- baris `verification_logs` dan `payment_proofs` tidak dihapus;
- `bank_accounts.bank` dan `bank_accounts.nomor` tidak dapat diubah setelah
  create, sehingga UUID tetap dapat menunjuk nomor lengkap yang sama untuk
  kebutuhan dokumen historis tanpa menyalinnya ke log verifikasi;
- keputusan `done` wajib mempunyai satu hasil untuk setiap bukti yang
  diperhitungkan dan semuanya berstatus `allowed`;
- attempt AI mempunyai unique partial index pada
  `(order_id, branch_allowlist_version, proof_set_fingerprint)` untuk
  `metode='ai'`; duplikasi job pada versi dan himpunan bukti yang sama membaca
  kembali attempt final, bukan membuat log/timeline/audit kedua;
- transaksi yang pertama kali mengubah order ke `done` HARUS mengisi
  `orders.done_verification_log_id` dengan attempt sukses yang sama; pointer ini
  immutable selama pembayaran order tidak dibuka kembali oleh desain lain;
- jika lebih dari satu bukti sah menuju rekening efektif yang berbeda, semuanya
  boleh diperhitungkan; nominal tetap dijumlahkan sesuai kontrak yang ada.

Validasi bentuk JSON dilakukan di service dan dapat diperkuat CHECK ringan
(`jsonb_typeof(destination_results) = 'array'`). Struktur ini sengaja menempel
pada attempt verifikasi karena hasil tujuan dapat berbeda antar-attempt ketika
assignment berubah atau admin menambahkan bukti baru.

### 4.4 Query efektif

Satu query domain `bank` menjadi sumber kebenaran rekening efektif terkini bagi
Customer Portal, OCR, verifikasi manual, dan D3 sebelum keputusan pembayaran:

```sql
SELECT ba.id, ba.bank, ba.nama_pemilik, ba.nomor,
       b.bank_allowlist_version
FROM branches b
JOIN branch_bank_accounts bba
  ON bba.branch_id = b.id AND bba.is_allowed
JOIN bank_accounts ba
  ON ba.id = bba.bank_account_id AND ba.is_active
WHERE b.id = $1
  AND b.is_active
ORDER BY ba.bank, ba.id;
```

Tidak boleh ada query alternatif berbentuk `WHERE bank_accounts.is_active`
tanpa filter assignment untuk alur cabang.

Query ini TIDAK dipakai untuk merender dokumen order `done`. Sumber rekening
dokumen historis ditetapkan terpisah di §9 melalui
`orders.done_verification_log_id`.

### 4.5 Audit

Setiap replacement allowlist menulis satu `audit_logs` dalam transaksi yang
sama:

- `entity = 'branch_bank_allowlist'`;
- `entity_id = branch_id`;
- `action = 'replace'`;
- `before` dan `after` berisi `version` dan array UUID rekening terurut;
- audit tidak memuat nomor rekening lengkap;
- percobaan gagal/rollback tidak meninggalkan audit sukses.

Perubahan status rekening global tetap memakai audit `bank_account/update` yang
sudah ada. Audit tersebut harus mencatat dampak jumlah assignment/cabang, tanpa
menyalin nomor rekening lengkap.

Transaksi perubahan `bank_accounts.is_active` mengikuti protokol serialisasi
§5.1 dan menaikkan `bank_allowlist_version` setiap cabang yang mempunyai
assignment `is_allowed` ke rekening tersebut, termasuk cabang nonaktif. Perubahan
identitas `bank`/`nomor` ditolak; koreksi dilakukan lewat create rekening
pengganti + deactivate rekening lama.

## 5. Transaksi dan konkurensi

### 5.1 Protokol serialisasi seluruh mutasi kebijakan

Semua penulis yang dapat mengubah `Effective(B)` HARUS memakai urutan lock yang
sama. Penulis tersebut adalah replacement assignment, perubahan
`bank_accounts.is_active`, dan perubahan `branches.is_active`.

Urutan globalnya:

1. `BEGIN` lalu ambil transaction-scoped advisory lock eksklusif yang sama:
   `SELECT pg_advisory_xact_lock(22102, 2)`. Angka pertama adalah namespace
   aplikasi dan angka kedua adalah fitur D2; tidak boleh ada key alternatif;
2. tentukan ID rekening dan cabang terdampak di bawah advisory lock. Penulis
   status rekening membaca pasangan `is_allowed` SETELAH lock ini didapat,
   sehingga assignment baru tidak dapat lolos dari himpunan terdampak;
3. lock seluruh `bank_accounts` terdampak `FOR UPDATE` menurut UUID menaik;
4. lock seluruh `branches` terdampak `FOR UPDATE` menurut UUID menaik;
5. baca ulang input kebijakan, validasi, lakukan mutasi, naikkan versi cabang
   yang salah satu input kebijakannya benar-benar berubah menurut §4.1, tulis
   audit, lalu `COMMIT`.

Advisory lock sengaja membuat mutasi kebijakan jarang ini serial secara global.
Ini mengutamakan correctness dan menghilangkan siklus account→branch versus
branch→account. Pembacaan customer/OCR tidak mengambil advisory lock. Transaksi
final verifikasi tetap mengambil branch `FOR SHARE` (§5.2), sehingga ia
linearizable terhadap langkah 4 tanpa deadlock: verifikasi tidak mengambil row
lock rekening setelah memegang branch.

Replacement `PUT` dijalankan sebagai satu transaksi dengan protokol di atas:

```text
BEGIN
  advisory lock (22102, 2)
  baca current assignment; gabungkan current + desired account IDs
  lock bank_accounts gabungan tersebut FOR UPDATE urut UUID
  lock branches(branch_id) FOR UPDATE
  cek cabang ada
  cek expected_version = bank_allowlist_version
  validasi semua UUID unik dan ada
  untuk setiap desired ID:
    izinkan jika rekening aktif; ATAU
    izinkan hanya untuk mempertahankan pasangan current is_allowed=true
      yang rekeningnya kini nonaktif
    selain itu tolak BANK_ACCOUNT_INACTIVE
  hitung desired set dan current allowed set, termasuk assignment dormant
  jika sama: kembalikan sukses tanpa perubahan versi/audit
  upsert pasangan desired sebagai is_allowed=true
  tandai pasangan lain is_allowed=false + revoked_by/revoked_at
  increment branches.bank_allowlist_version karena input assignment berubah
  insert audit_logs before/after
COMMIT
```

Tidak boleh ada keadaan parsial ketika hanya sebagian rekening telah berubah.
Dua editor dengan versi sama: satu berhasil, satu mendapat konflik versi.

Perubahan status rekening memakai transaksi berikut:

```text
BEGIN
  advisory lock (22102, 2)
  lock bank_accounts(account_id) FOR UPDATE
  baca branch IDs dengan assignment is_allowed untuk account tersebut
  lock seluruh branches itu FOR UPDATE urut UUID
  ubah is_active bila nilainya memang berbeda
  increment version tepat sekali pada setiap branch yang mempunyai assignment
    is_allowed ke rekening ini, walaupun branch nonaktif
  insert audit; versi baru menjadi invalidation state yang diwajibkan §11
COMMIT
```

Reaktivasi memakai algoritme yang sama. Update idempoten (`true→true` atau
`false→false`) tidak menaikkan versi. Perubahan status cabang juga memakai
advisory lock, mengunci rekening assignment-nya urut UUID sebelum mengunci
cabang, lalu menaikkan versi tepat sekali bila status cabang memang berubah,
termasuk ketika assignment kosong.

### 5.2 Verifikasi manual/AI

Titik linearizasi adalah transaksi final `MarkVerified`, bukan saat OCR membaca
berkas.

Urutan final:

1. lock cabang `FOR SHARE`;
2. lock order yang masih `submitted`;
3. baca ulang rekening efektif dan versi cabang;
4. petakan setiap bukti yang diperhitungkan;
5. jika hasil apa pun bukan `allowed`, tulis attempt `perlu_review` dengan hasil
   tujuan dan jangan transisi ke `done`;
6. jika semua tujuan allowed dan aturan nominal/tanggal/keyakinan lain lolos,
   jalankan jalur `MarkVerified` yang sudah ada menuju `done`;
7. tulis verification log, timeline, audit, dan job dokumen dalam transaksi yang
   sama seperti kontrak F1-18/F2-26.

Mutasi allowlist memakai lock cabang `FOR UPDATE`, sehingga tidak dapat menyela
di tengah keputusan final. Jika verifikasi memegang lock lebih dahulu,
keputusan sah pada titik tersebut lalu pencabutan berlaku untuk keputusan
berikutnya. Jika pencabutan memegang lock lebih dahulu, verifikasi harus melihat
assignment baru dan fail closed.

OCR boleh membaca bukti di luar transaksi panjang. OCR membawa
`branch_allowlist_version` yang dibacanya, tetapi `MarkVerified` wajib
memetakan ulang terhadap versi terkini. Snapshot OCR basi tidak pernah cukup
untuk menyetujui.

### 5.3 Pencocokan digit akhir

Per bukti:

1. normalisasi hanya digit;
2. panjang harus sekurang-kurangnya `ai.digit_rek`;
3. cocokkan kode bank bila OCR dapat membacanya;
4. cocokkan digit akhir hanya terhadap `Effective(branch_id)`;
5. nol kecocokan menghasilkan `not_allowed`, `inactive`, `unknown`, atau
   `unreadable` sesuai informasi yang tersedia;
6. lebih dari satu kecocokan menghasilkan `ambiguous`, bukan memilih rekening
   pertama;
7. hanya tepat satu kecocokan menghasilkan `allowed`.

Untuk membedakan `not_allowed`/`inactive` dari `unknown`, service boleh melakukan
lookup diagnostik ke master setelah pencocokan efektif gagal. Lookup diagnostik
hanya menentukan alasan review; tidak boleh membuat rekening itu valid.

## 6. Kontrak API

Semua endpoint berada di `/api/v1`, memakai cookie sesi yang ada, bentuk error
seragam, dan token CSRF double-submit untuk request non-GET.

### 6.1 Customer Portal

#### `GET /bank-accounts`

Tetap memakai path yang sudah ada, tetapi semantiknya berubah dari semua
rekening aktif menjadi rekening efektif milik cabang sesi.

- Role: User Cabang.
- `branch_id` selalu dari sesi; parameter cabang dari query/body diabaikan atau
  ditolak sebagai field tak dikenal.
- Respons `200` walaupun kosong.
- Nomor lengkap boleh dikirim ke cabang pemilik sesi untuk instruksi transfer,
  tetapi tidak boleh masuk telemetry/log frontend.

Contoh tanpa nomor rekening nyata:

```json
{
  "data": [],
  "meta": {
    "payment_available": false,
    "allowlist_version": 0,
    "reason": "NO_ASSIGNED_BANK_ACCOUNT"
  }
}
```

Jika tersedia, setiap item memuat `id`, `bank`, `nama_pemilik`, `nomor`, dan
`display_nomor`; `meta.payment_available=true`. Urutan deterministik berdasarkan
bank lalu UUID, bukan rekening global pertama.

Kontrak blocking:

- draft tetap dapat dibuat/disimpan;
- submit transfer non-C&C ditolak bila rekening efektif kosong;
- penambahan bukti transfer customer ditolak bila rekening efektif kosong;
- submit C&C tanpa bukti tetap mengikuti aturan C&C yang ada, tetapi instruksi
  transfer dan unggah pembayaran setelahnya tetap menunggu assignment;
- order `done` dan dokumen historis tetap dapat dibaca.

#### Submit/unggah yang sudah ada

`POST /orders/{id}/submit` dan `POST /orders/{id}/proofs` menambah precondition
server-side untuk alur transfer: minimal satu rekening efektif pada transaksi.
Cek ini bukan pengganti validasi rekening saat verifikasi.

### 6.2 Admin — pengelolaan assignment

#### `GET /admin/branches/{branch_id}/bank-accounts`

- Capability: `admin.bank` (Superadmin saja).
- Mengembalikan seluruh rekening global dengan status assignment agar layar
  dapat menampilkan checkbox tanpa query N+1.
- Rekening nonaktif tetap muncul. Jika sudah `is_allowed`, item ditandai
  `dormant=true`, tetap menjadi anggota replacement sampai Superadmin memilih
  aksi cabut eksplisit, tetapi tidak efektif. Rekening nonaktif yang belum
  assigned atau sudah revoked tidak dapat ditambahkan/diaktifkan ulang.
- Respons memuat `version` cabang dan status turunan `effective`.

Bentuk item:

```json
{
  "id": "<bank-account-uuid>",
  "bank": "BCA",
  "nama_pemilik": "PT Contoh",
  "display_nomor": "••••1234",
  "is_active": true,
  "is_allowed": true,
  "dormant": false,
  "effective": true
}
```

Nomor lengkap tidak diperlukan pada layar assignment; gunakan bentuk masked.
Endpoint master rekening yang sudah ada tetap menjadi tempat Superadmin melihat
atau mengubah detail lengkap.

#### `PUT /admin/branches/{branch_id}/bank-accounts`

- Capability: `admin.bank` (Superadmin saja).
- CSRF wajib.
- Semantik replacement atomik, bukan patch parsial.
- Array kosong sah dan berarti memblokir transfer cabang secara eksplisit.
- UUID duplikat ditolak, bukan dideduplikasi diam-diam.
- `bank_account_ids` adalah seluruh desired `is_allowed` set, termasuk
  assignment dormant yang hendak dipertahankan. Untuk ID rekening nonaktif,
  server hanya menerima ID yang pada awal transaksi sudah `is_allowed=true`
  bagi cabang itu. Dengan demikian rekening dormant dapat dipertahankan saat
  rekening aktif lain diedit, tetapi rekening nonaktif tidak dapat ditambahkan
  atau diaktifkan ulang. Menghilangkan ID dormant dari replacement adalah
  pencabutan eksplisit dan menaikkan versi.

Request:

```json
{
  "expected_version": 4,
  "bank_account_ids": ["<uuid-a>", "<uuid-b>"]
}
```

Respons `200`:

```json
{
  "data": {
    "branch_id": "<branch-uuid>",
    "version": 5,
    "bank_account_ids": ["<uuid-a>", "<uuid-b>"],
    "payment_available": true
  }
}
```

PUT dengan set identik dan versi terkini mengembalikan `200`, versi tetap, dan
`meta.changed=false`.

Contoh: set saat ini `{aktif-A, dormant-B}`. Payload `{aktif-C, dormant-B}` sah:
A dicabut, C ditambahkan, dan B dipertahankan dormant. Payload yang menyertakan
rekening nonaktif C yang tidak sedang allowed ditolak seluruhnya dengan
`BANK_ACCOUNT_INACTIVE`. Setelah B dicabut ketika masih nonaktif, B tidak dapat
dimasukkan lagi sampai master B diaktifkan kembali.

### 6.3 Admin — tampilan cabang dan verifikasi

- `GET /admin/branches`/detail untuk Manager boleh menampilkan status turunan
  `payment_account_status = configured|blocked` dan jumlah rekening efektif,
  tetapi tidak mengekspos editor atau nomor rekening penuh.
- Superadmin melihat tombol `Kelola rekening tujuan` pada detail cabang.
- Detail order admin menampilkan rekening tujuan yang diharapkan untuk cabang
  dan hasil per bukti (masked).
- Request verifikasi manual yang menyetujui harus menyertakan pemetaan setiap
  bukti ke rekening efektif:

```json
{
  "action": "approve",
  "proof_destinations": [
    {
      "payment_proof_id": "<proof-uuid>",
      "bank_account_id": "<bank-account-uuid>"
    }
  ],
  "catatan": ""
}
```

Server tidak percaya status `allowed` dari klien. Server memvalidasi bahwa
proof milik order, account efektif bagi cabang order, dan semua bukti yang
masuk total verifikasi dipetakan tepat sekali.

Action `review` boleh menyimpan tujuan `unknown`/mismatch dan alasan; action itu
tidak mengubah order menjadi `done`.

#### `POST /admin/orders/{id}/verification/reevaluate-destination`

- Capability: `admin.verify` (Superadmin atau Manager sesuai aturan verifikasi
  yang sudah ada), serta CSRF wajib.
- Hanya untuk order `submitted` yang mempunyai minimal satu bukti.
- Tidak menerima `branch_id`, versi, rekening, atau hasil OCR dari klien;
  semuanya dibaca server dari order dan database.
- Dengan `ai.aktif=true`, menghitung versi + fingerprint terkini, menitipkan job
  idempoten sesuai §11, lalu menjawab `202` dengan `queued=true`. Request ulang
  untuk key yang sama menjawab `202` dengan `queued=false`/`duplicate=true`.
- Jika salah satu bukti belum mempunyai `ocr_at`, endpoint menolak dengan
  `OCR_RESULT_MISSING`; aksi ini khusus menilai ulang tujuan dari hasil provider
  yang sudah tersimpan, bukan pintu pemanggilan provider baru.
- Endpoint ini tidak memanggil provider secara sinkron dan tidak menjanjikan
  order menjadi Done; hasil mismatch tetap membuat attempt review.

### 6.4 Error contract

| HTTP | Code | Kondisi |
|---:|---|---|
| 400 | `VALIDATION_FAILED` | body rusak, UUID duplikat, field tak dikenal |
| 401 | `UNAUTHENTICATED` | sesi tidak ada/kedaluwarsa |
| 403 | `FORBIDDEN` | Manager/cabang mencoba mutasi assignment atau akses cabang lain |
| 403 | `CSRF_INVALID` | token CSRF tidak ada/tidak cocok pada PUT/POST |
| 404 | `BRANCH_NOT_FOUND` | cabang target tidak ada |
| 404 | `BANK_ACCOUNT_NOT_FOUND` | salah satu rekening tidak ada; respons tidak mengungkap nomor |
| 409 | `ALLOWLIST_VERSION_CONFLICT` | `expected_version` basi |
| 409 | `BANK_ACCOUNT_INACTIVE` | replacement mencoba menambah/mengaktifkan ulang rekening nonaktif; mempertahankan assignment dormant yang sudah allowed tetap sah |
| 409 | `BANK_ACCOUNT_IDENTITY_IMMUTABLE` | update mencoba mengubah `bank`/`nomor`; buat rekening pengganti |
| 409 | `BRANCH_BANK_ACCOUNT_UNAVAILABLE` | alur transfer cabang tidak punya rekening efektif |
| 409 | `BANK_ACCOUNT_NOT_ALLOWED` | rekening tujuan bukan assignment efektif cabang |
| 409 | `DESTINATION_ACCOUNT_AMBIGUOUS` | hasil bukti cocok ke lebih dari satu rekening efektif |
| 409 | `ORDER_ALREADY_DONE` | attempt verifikasi terlambat pada order selesai |
| 409 | `ORDER_NOT_SUBMITTED` | reevaluate dipanggil untuk status selain submitted |
| 409 | `AI_DISABLED` | reevaluate AI dipanggil saat `ai.aktif=false` |
| 409 | `OCR_RESULT_MISSING` | reevaluate eksplisit menemukan bukti yang belum pernah dibaca provider |
| 422 | `DESTINATION_ACCOUNT_REQUIRED` | approve manual tidak memetakan seluruh bukti |
| 422 | `PAYMENT_PROOF_REQUIRED` | reevaluate dipanggil tanpa bukti |

Pesan user-facing boleh menjelaskan tindakan, tetapi tidak boleh memuat nomor
rekening lengkap. Error assignment tidak boleh membocorkan apakah UUID tertentu
milik cabang lain.

## 7. Kontrak UI

### 7.1 Customer Portal

Area instruksi pembayaran:

- bila tersedia: tampilkan hanya rekening efektif dari respons sesi;
- bila lebih dari satu: tampilkan semua tanpa label “utama” kecuali konsep
  rekening utama disetujui pada desain terpisah;
- bila kosong: tampilkan blocking state “Rekening tujuan untuk cabang ini belum
  ditetapkan. Jangan melakukan transfer; hubungi admin.”;
- jangan menampilkan contoh nomor, rekening terakhir yang tersimpan di cache,
  atau rekening global;
- tombol submit non-C&C/unggah bukti dinonaktifkan dengan alasan yang sama,
  tetapi backend tetap menjadi penjaga akhir;
- cache TanStack Query harus berkunci actor/branch dan dibersihkan saat logout;
- perubahan assignment menginvalidasi query rekening pada login/refetch; respons
  kosong tidak boleh diganti data stale.

Jika rekening dicabut saat halaman terbuka, backend dapat menolak submit dengan
`BRANCH_BANK_ACCOUNT_UNAVAILABLE`/`BANK_ACCOUNT_NOT_ALLOWED`; UI harus refetch
rekening dan menampilkan blocking state, bukan mengulang otomatis.

### 7.2 Admin Portal — detail cabang

- semua role admin melihat chip `Rekening pembayaran: N aktif` atau
  `Diblokir—belum ada rekening`;
- hanya Superadmin melihat aksi editor;
- editor menampilkan checkbox rekening global, bank, nama pemilik, nomor masked,
  status aktif, dan peringatan dampak;
- rekening global nonaktif yang bukan assignment tampil disabled dan tidak dapat
  dipilih. Assignment dormant tampil checked+locked dengan badge `Dormant`,
  tidak efektif, dan mempunyai aksi terpisah `Cabut assignment`; aksi ini
  meminta konfirmasi karena tidak dapat dibatalkan sampai rekening aktif lagi;
- Save dibangun dari state model, bukan serialisasi native checkbox HTML:
  replacement lengkap mencakup assignment dormant yang tetap checked/locked +
  `expected_version`. Karena itu edit rekening aktif lain tidak boleh diam-diam
  mencabut assignment dormant;
- memilih nol rekening memerlukan konfirmasi eksplisit karena akan memblokir
  transfer, tetapi API tetap menerima nol sebagai keputusan sah;
- konflik versi menampilkan pesan “Data telah berubah”, refetch, dan tidak
  menimpa perubahan admin lain;
- sukses menginvalidasi detail cabang, daftar cabang, rekening customer terkait,
  dan detail order yang menampilkan rekening harapan.

### 7.3 Admin Portal — verifikasi

Per bukti tampilkan:

- preview yang sudah ada;
- hasil OCR bank/digit akhir/keyakinan bila tersedia;
- hasil allowlist `Sesuai`, `Tidak diizinkan`, `Nonaktif`, `Ambigu`, atau
  `Tidak terbaca`;
- selector rekening efektif untuk review manual, memakai nomor masked;
- alasan review wajib ketika tidak approve.

Tombol approve dinonaktifkan di UI jika ada bukti tanpa tujuan allowed, namun
API wajib mengulang validasi. Manager tetap boleh memverifikasi sesuai
`admin.verify`, tetapi tidak boleh mengubah assignment; ia harus meminta
Superadmin memperbaiki assignment yang memang salah.

Jika attempt AI terakhir memakai versi lama, layar menampilkan status “Rekening
perlu dinilai ulang”. Untuk bukti yang seluruhnya sudah mempunyai `ocr_at` dan
AI aktif, Superadmin/Manager dapat menjalankan aksi `Nilai ulang rekening` yang
memanggil endpoint §6.3. Aksi disabled ketika AI mati atau hasil OCR belum ada;
verifikasi manual tetap tersedia sesuai capability. UI tidak boleh menjalankan
aksi ini otomatis setelah perubahan assignment.

## 8. Perubahan status rekening

### 8.1 Rekening global dinonaktifkan

Efek langsung:

- hilang dari semua instruksi Customer Portal;
- tidak efektif bagi seluruh assignment;
- tidak dapat dipilih untuk verifikasi baru;
- OCR yang sedang berjalan harus gagal revalidasi di transaksi final dan masuk
  review;
- order `submitted` tetap `submitted`; tidak ada auto-cancel atau auto-Done;
- order `done`, verification log, bukti, invoice, dan audit historis tidak
  diubah;
- assignment disimpan agar dapat didiagnosis/diaktifkan ulang secara eksplisit.

Dalam transaksi yang sama, versi allowlist seluruh cabang terdampak dinaikkan.
Hal ini membuat attempt OCR lama tidak dianggap masih berlaku walaupun
assignment penghubungnya sendiri tidak berubah.

API update rekening global sebaiknya mengembalikan `affected_branch_count` dan
UI meminta konfirmasi jika menonaktifkan rekening yang masih diizinkan cabang.
Tidak ada cascade yang otomatis memilih rekening pengganti.

### 8.2 Assignment dicabut

Efek sama tetapi hanya bagi cabang itu. Pencabutan tidak menghapus bukti atau
log. Bukti/order yang belum selesai harus dinilai menggunakan allowlist baru
pada attempt berikutnya. Tidak ada grandfathering tersembunyi untuk order lama.

Jika bisnis ingin menerima transfer lama ke rekening yang telah dicabut,
Superadmin harus mengaktifkan kembali assignment secara eksplisit dan tercatat,
lalu admin mengulang verifikasi. Dokumen ini tidak menyediakan override
per-order yang melewati allowlist.

### 8.3 Assignment ditambahkan/diaktifkan ulang

- hanya rekening global aktif yang dapat ditambahkan;
- tidak menyetujui order secara otomatis;
- OCR/manual harus membuat attempt baru terhadap himpunan bukti yang sama;
- attempt review lama tetap immutable;
- cache dan tampilan customer diperbarui melalui refetch/invalidation.

## 9. Dokumen dan histori

Ada dua sumber data yang tidak boleh dicampur:

1. Instruksi sebelum pembayaran memakai `Effective(branch_id)` terkini dari
   §4.4. Respons kosong berarti blocked dan tidak boleh memakai snapshot lama.
2. Dokumen order `done` memakai attempt yang ditunjuk
   `orders.done_verification_log_id`, bukan allowlist terkini.

Kontrak query renderer pertama, render ulang, dan revisi:

- load order + `done_verification_log_id`; order `done` tanpa pointer adalah
  invariant violation dan job gagal/retry, bukan fallback global;
- load verification log yang ditunjuk beserta `destination_results`;
- untuk setiap hasil `allowed`, gunakan `bank_snapshot` dan
  `account_owner_snapshot` dari verification log sebagai teks bank/pemilik yang
  dicetak. Load UUID `bank_account_id` langsung dari master, termasuk baris
  nonaktif, hanya untuk memperoleh nomor lengkap yang identitasnya immutable;
  cocokkan bank dan digit akhir master dengan snapshot sebagai invariant/audit
  silang. `nama_pemilik` master boleh berubah dan DILARANG mengganti owner
  snapshot pada render pertama yang tertunda, render ulang, atau revisi;
- attempt sukses baru wajib mempunyai snapshot bank, pemilik, dan digit akhir
  nonkosong untuk setiap destination `allowed`. Jika snapshot historis legacy
  tidak lengkap, render/revisi baru fail closed untuk rekonsiliasi; jangan
  mengambil metadata pengganti dari master. Berkas dokumen lama yang sudah ada
  tetap dapat diunduh;
- deduplikasi UUID rekening dan urutkan deterministik menurut bank lalu UUID;
- renderer DILARANG memanggil `ListActiveBankAccounts` atau query §4.4 untuk
  dokumen `done`;
- bila beberapa bukti menuju beberapa rekening, tampilkan semua rekening yang
  benar-benar dipakai. Beberapa bukti ke rekening sama hanya menghasilkan satu
  entri rekening;
- order C&C yang mencapai `done` tanpa bukti mempunyai
  `destination_results=[]`; dokumen menampilkan metode C&C dan tidak mencetak
  rekening transfer. Bila C&C mempunyai bukti yang diverifikasi sebelum Done,
  sumbernya sama dengan order transfer biasa;
- assignment kosong pada waktu render tidak menggagalkan dokumen Done karena
  snapshot suksesnya tetap menjadi sumber. Sebaliknya, order yang belum Done
  dan belum punya assignment tidak boleh memperoleh dokumen yang mengarang
  rekening;
- pencabutan/deaktivasi setelah transaksi Done tetapi sebelum job render tidak
  mengubah sumber rekening: job tetap memakai pointer attempt sukses;
- render ulang versi dokumen yang sama bersifat idempoten dan membaca pointer
  yang sama; baris/berkas yang sudah ada tidak ditimpa;
- revisi invoice (versi order +1) tetap memakai
  `done_verification_log_id` yang sama. Revisi harga/baris tidak menilai ulang
  pembayaran dan tidak mengambil allowlist saat itu. Perubahan pembayaran pada
  revisi berada di luar D2 dan harus menetapkan attempt sukses/pointer baru lewat
  desain tersendiri sebelum boleh mengubah perilaku ini.

Dokumen tidak boleh tiba-tiba menampilkan semua rekening aktif atau mengganti
rekening hanya karena assignment berubah.

- Bukti, OCR raw yang sudah disanitasi, destination results, verification log,
  timeline, dan audit dipertahankan menurut kebijakan retensi yang berlaku.
- Nomor rekening lengkap hanya berada di master terenkripsi/database dan output
  bisnis yang memang memerlukannya; log observability memakai UUID + digit
  akhir.

## 10. Transisi cabang lama

Transisi harus fail closed dan tidak boleh memberi seluruh rekening kepada
cabang lama secara otomatis.

Urutan rilis:

1. migrasi additive membuat tabel/kolom dengan assignment kosong;
2. deploy backend yang membaca allowlist dan endpoint Admin;
3. semua cabang tanpa assignment langsung berstatus `blocked` untuk transfer;
4. Superadmin mengisi assignment satu per satu berdasarkan keputusan bisnis
   tertulis; tidak ada seed “semua rekening aktif”;
5. laporan preflight menampilkan setiap cabang aktif beserta jumlah rekening
   efektif dan status blocked/configured;
6. QA memverifikasi lintas cabang di staging;
7. produksi memerlukan approval manual sesuai CI/CD dan persetujuan CEO.

Untuk memperkecil gangguan, daftar mapping dapat disiapkan dan disetujui sebelum
rilis, lalu diterapkan melalui API setelah deploy. Namun sampai mapping benar-
benar tersimpan, transfer tetap diblokir. Tidak ada feature flag permissive,
dual-read fallback, atau pembacaan rekening global lama.

Perlakuan data lama:

- order `done`: tidak berubah dan tidak diverifikasi ulang. Migrasi mencoba
  backfill `done_verification_log_id` hanya bila ada tepat satu verification log
  `hasil='cocok'` dengan `created_at = orders.done_at`. Karena `now()` PostgreSQL
  stabil dalam satu transaksi MarkVerified, log dan `done_at` yang lahir bersama
  dapat dikorelasikan tanpa rentang waktu heuristik. Jika kandidat tidak ada atau
  jumlahnya lebih dari satu, migrasi TIDAK
  mengarang pointer; dokumen yang sudah tersimpan tetap dapat diunduh, sedangkan
  render baru/revisi order tersebut diblokir dengan invariant error sampai
  Superadmin menyelesaikan rekonsiliasi tertulis dan teraudit;
- order `submitted`: belum boleh Done sampai mempunyai rekening tujuan yang
  efektif pada attempt verifikasi baru;
- bukti lama: tetap tersedia; OCR/manual menilai ulang terhadap assignment
  eksplisit;
- cabang nonaktif: tidak memerlukan assignment untuk login/transfer, tetapi
  assignment historis tetap disimpan;
- akun/rekening seed development tidak boleh dijadikan mapping produksi.

Rollback aplikasi tidak boleh dilakukan ke versi yang kembali menampilkan
rekening global. Rollback yang aman berarti aplikasi tetap fail closed atau
akses transfer dimatikan sampai versi yang mendukung allowlist pulih.

## 11. Dampak ke OCR/manual yang sudah ada

Kontrak F2-26 tetap berlaku untuk keterbacaan, tanggal, nominal, toleransi,
sanitasi raw, idempotensi job, dan keputusan manusia. Perubahan wajib hanya pada
sumber rekening dan revalidasi final:

- `RekeningSource.Active(ctx)` global diganti kontrak branch-scoped, misalnya
  `EffectiveForBranch(ctx, branchID)`;
- `OCRService` mengambil `branch_id` order dan versi allowlist;
- fungsi penilaian murni menerima hanya rekening efektif cabang;
- ketiadaan rekening efektif menghasilkan `rekening_beda`/review, tidak pernah
  cocok;
- digit pendek tetap fail closed;
- kecocokan ambigu menjadi review;
- `MarkVerified` mengulang validasi dalam transaksi sebelum Done;
- admin manual memakai jalur final yang sama dan tidak boleh melewati aturan
  assignment;
- `verification_logs.total_terbaca=NULL` untuk bukti tidak terbaca tetap
  dipertahankan;
- bukti baru tetap memicu attempt baru; perubahan assignment juga harus membuat
  keputusan lama tidak dianggap cukup untuk auto-approve.

Perubahan status aktif rekening global atau cabang ikut menaikkan versi
kebijakan cabang, sehingga mekanisme invalidasi yang sama berlaku tanpa
fallback khusus.

Idempotensi OCR yang sekarang hanya membandingkan waktu log dengan `ocr_at`
harus diperluas: attempt AI dianggap masih berlaku hanya jika versi allowlist
dan `proof_set_fingerprint` sama dengan keadaan kini.

Mutasi assignment/status TIDAK otomatis men-enqueue OCR dan TIDAK boleh membuat
order menjadi Done. Ia hanya menaikkan versi, menginvalidasi attempt lama untuk
auto-approval, dan membuat order submitted terkait tampil sebagai “rekening
perlu dinilai ulang”. Keputusan ini menghindari ledakan job dan auto-Done yang
tidak dipicu manusia hanya karena konfigurasi berubah.

Attempt baru hanya dibuat oleh salah satu pemicu nyata berikut:

1. upload bukti baru pada order `submitted` ketika `ai.aktif=true`, melalui job
   OCR yang sudah ada;
2. aksi eksplisit admin ber-capability `admin.verify` dan CSRF sah,
   `POST /admin/orders/{id}/verification/reevaluate-destination`;
3. aksi verifikasi manual yang sudah ada, yang selalu memetakan ulang terhadap
   allowlist terkini dan menulis attempt metode `manual`.

Endpoint reevaluate hanya sah untuk order `submitted` yang mempunyai bukti.
Ketika `ai.aktif=true`, endpoint men-enqueue job secara idempoten dengan kunci
`(order_id, current_allowlist_version, proof_set_fingerprint)` dan menjawab
`202`; job memakai hasil OCR tersimpan (`ocr_at` terisi), tidak memanggil
provider lagi, lalu membuat paling banyak satu attempt AI untuk kunci tersebut.
Duplikasi request/job ditahan oleh unique attempt di §4.3 dan tidak menggandakan
timeline/audit/Done.

Fingerprint hanya dihitung bila semua bukti mempunyai `ocr_at`. Bukti yang belum
dibaca menghasilkan `OCR_RESULT_MISSING`; admin dapat memakai verifikasi manual,
sedangkan pembacaan provider baru hanya boleh datang dari alur OCR existing
(misalnya job yang dititipkan saat upload), bukan dari endpoint reevaluate.

Ketika `ai.aktif=false`, upload maupun endpoint reevaluate tidak membuat job AI;
endpoint menjawab `409 AI_DISABLED`. Admin tetap dapat memakai verifikasi manual,
yang tidak memanggil provider dan wajib menilai rekening terhadap versi terkini.
Mengaktifkan AI kembali tidak otomatis menilai order lama; admin harus memakai
aksi eksplisit atau ada upload bukti baru. Attempt versi lama tetap immutable dan
tidak pernah cukup untuk auto-Done.

## 12. Rencana pengujian implementasi

### 12.1 Unit backend

1. `EffectiveForBranch` hanya mengembalikan irisan assignment allowed + rekening
   aktif + cabang aktif.
2. Nol assignment menghasilkan slice kosong, bukan seluruh rekening.
3. Rekening yang sama boleh diizinkan ke beberapa cabang; assignment cabang A
   tidak terlihat cabang B.
4. Replacement set menghitung add/revoke/reactivate secara deterministik.
5. PUT set identik idempoten: versi dan audit tidak bertambah.
6. Versi basi menghasilkan `ALLOWLIST_VERSION_CONFLICT`.
7. Rekening nonaktif yang bukan current allowed/UUID hilang/duplikat ditolak
   atomik; current assignment dormant boleh dipertahankan tetapi tidak efektif.
8. Pencocokan digit akhir hanya terhadap cabang order.
9. Nol match, match rekening cabang lain, inactive, unreadable, dan multi-match
   menghasilkan status review yang tepat.
10. Snapshot destination tidak memuat nomor lengkap atau raw secret.
11. AI snapshot versi lama tidak dapat Done setelah allowlist berubah.
12. Manual approve tanpa pemetaan lengkap atau dengan rekening tidak efektif
    ditolak.
13. C&C tanpa bukti tetap dapat submit; pembayaran berikutnya tetap memerlukan
    assignment.
14. Error/log/audit menyensor nomor rekening penuh.
15. Identitas bank/nomor rekening yang sudah dibuat tidak dapat diedit; koreksi
    memakai rekening pengganti.
16. Fingerprint kanonik stabil untuk urutan input berbeda dan berubah ketika
    proof ID/`ocr_at` berubah.
17. Mutasi versi saja menandai attempt lama stale tetapi tidak membuat attempt
    baru dan tidak mengubah order menjadi Done.
18. Reevaluate dengan AI aktif memakai OCR tersimpan tanpa provider; AI mati
    menghasilkan `AI_DISABLED`; manual tetap menilai versi terkini.
19. Pemilihan rekening dokumen memakai pointer attempt Done, termasuk
    deduplikasi multi-bukti dan C&C tanpa bukti.
20. Versi naik tepat sekali ketika set assignment berubah walau cabang nonaktif
    atau rekeningnya dormant, ketika status cabang berubah walau assignment
    kosong, dan ketika status rekening assigned berubah walau cabang nonaktif;
    operasi idempoten tidak menaikkan versi.
21. Renderer memakai bank/owner snapshot attempt dan nomor immutable dari master;
    perubahan `nama_pemilik` master tidak mengubah output historis.

### 12.2 Integrasi PostgreSQL/API

1. constraint primary key menolak pasangan duplikat.
2. FK menolak branch/account/admin yang tidak ada dan `ON DELETE RESTRICT`
   menjaga histori.
3. Superadmin GET/PUT berhasil dengan CSRF sah.
4. PUT tanpa CSRF, token salah, dan cookie tanpa token ditolak tanpa mutasi.
5. Manager dan User Cabang mendapat 403 pada GET/PUT admin assignment.
6. Cabang A `GET /bank-accounts` hanya memperoleh rekening A; UUID/query untuk
   cabang B tidak mengubah scope.
7. Cabang tanpa assignment memperoleh `data=[]`, `payment_available=false`, dan
   submit transfer ditolak.
8. Rekening yang dinonaktifkan hilang dari customer dan ditolak verifikasi.
9. Pencabutan assignment hanya memengaruhi cabang target.
10. Replacement dua rekening dilakukan satu transaksi; satu ID invalid membuat
    seluruh request rollback dan tanpa audit.
11. Dua PUT paralel dengan expected version sama: tepat satu sukses.
12. Race OCR approve vs revoke diserialisasi oleh lock cabang; tidak ada Done
    berdasarkan assignment yang sudah lebih dahulu dicabut.
13. Bukti cabang A yang menunjuk rekening hanya milik cabang B masuk review.
14. Multi-bukti ke dua rekening efektif dapat Done bila seluruh aturan lain
    cocok.
15. Multi-bukti dengan satu tujuan mismatch tidak dapat Done.
16. Assignment PUT paralel dengan deactivate rekening yang sama diserialisasi:
    hasil akhir tidak pernah mempunyai assignment baru efektif ke rekening
    nonaktif, tidak deadlock, dan versi cabang berubah tepat sekali untuk setiap
    perubahan input kebijakan.
17. Assignment PUT paralel dengan reactivate rekening diserialisasi; himpunan
    cabang terdampak dibaca setelah advisory lock dan tidak ada cabang terlewat
    dari kenaikan versi.
18. Perubahan versi saja tidak men-enqueue OCR, tidak menambah attempt, dan tidak
    mengubah order submitted menjadi Done.
19. Reevaluate eksplisit setelah versi berubah menggunakan `ocr_at` lama tanpa
    pemanggilan provider dan membuat tepat satu attempt untuk
    `(order, version, fingerprint)` walau request/job diduplikasi.
20. Dengan `ai.aktif=false`, upload/perubahan versi/reevaluate tidak membuat
    attempt AI; reevaluate mengembalikan `AI_DISABLED`, sedangkan manual dapat
    membuat attempt terhadap versi terkini.
21. Attempt review lama dan attempt sukses baru sama-sama tersimpan immutable.
22. Rekening dinonaktifkan tidak mengubah order Done/log/dokumen lama.
23. Audit before/after dan actor benar; nomor lengkap tidak ada dalam JSON.
24. Tidak ada query customer/OCR yang memanggil daftar rekening aktif global.
25. Menonaktifkan satu rekening menaikkan versi seluruh cabang dengan assignment
    allowed ke rekening itu dan membuat attempt AI lama tidak reusable.
26. Render pertama cabang A/B hanya memakai pointer attempt masing-masing;
    rekening cabang lain dan rekening aktif global tidak bocor.
27. Order Done tetap berhasil dirender ketika assignment kini kosong atau
    rekening dicabut setelah verifikasi tetapi sebelum job render.
28. Multi-bukti ke dua rekening menghasilkan dua rekening terurut; beberapa
    bukti ke rekening sama dideduplikasi.
29. C&C Done tanpa bukti merender metode C&C tanpa rekening transfer.
30. Re-render versi sama dan render revisi versi berikutnya memakai pointer
    attempt Done yang sama, tidak mengambil allowlist terkini.
31. Backfill order Done lama hanya memilih kandidat sukses deterministik;
    histori tanpa kandidat/ambigu tidak diberi pointer rekaan, dokumen lama tetap
    dapat diunduh, dan render/revisi baru fail closed.
32. Reevaluate dengan salah satu bukti `ocr_at=NULL` ditolak
    `OCR_RESULT_MISSING` tanpa pemanggilan provider atau attempt baru.
33. Reevaluate tanpa CSRF/token salah ditolak tanpa job atau attempt; Manager
    dengan `admin.verify` boleh menjalankan, User Cabang mendapat 403.
34. PUT pada cabang nonaktif yang mengubah active/dormant assignment set menaikkan
    versi; PUT identik tidak menaikkan versi. Mencabut assignment rekening
    nonaktif menaikkan versi walau `Effective(B)` tetap kosong.
35. Alur deactivate B → edit active A/C dengan payload tetap memuat dormant B →
    reactivate B mempertahankan assignment B dan menjadikannya efektif lagi.
    Menghilangkan B mencabutnya eksplisit; upaya menambah kembali B saat masih
    nonaktif ditolak atomik.
36. Menonaktifkan/mengaktifkan rekening assigned menaikkan versi cabang aktif dan
    nonaktif yang terdampak; update status idempoten tidak menaikkan versi.
37. Setelah order Done, ubah `nama_pemilik` master sebelum render pertama dan
    sebelum revisi: kedua dokumen tetap mencetak owner snapshot attempt. Snapshot
    legacy yang tidak lengkap membuat render/revisi baru fail closed tanpa
    fallback metadata master.

### 12.3 Kontrak/OpenAPI

- skema request/response dan semua error code di §6 divalidasi;
- field UUID, versi nonnegatif, array unik, dan unknown fields diuji;
- endpoint reevaluate mendokumentasikan 202 idempoten, `AI_DISABLED`,
  `ORDER_NOT_SUBMITTED`, `OCR_RESULT_MISSING`, dan `PAYMENT_PROOF_REQUIRED`;
- tipe frontend di-generate ulang dan tidak memakai fallback optional yang
  mengubah `payment_available=false` menjadi true;
- endpoint lama `/bank-accounts` didokumentasikan ulang sebagai branch-scoped.

### 12.4 UI unit/component

1. daftar customer menampilkan hanya respons branch-scoped;
2. empty state memblokir tombol tanpa nomor contoh/stale;
3. logout menghapus cache rekening;
4. 409 akibat revoke memicu refetch dan blocking state;
5. Manager melihat status/count tanpa editor;
6. Superadmin dapat memilih beberapa rekening dan menyimpan replacement;
7. rekening nonaktif yang belum assigned disabled; assignment dormant tampil
   checked+locked, ikut payload Save, dan hanya dicabut lewat aksi eksplisit;
8. zero-selection confirmation tampil;
9. version conflict tidak menimpa state server;
10. layar verifikasi menonaktifkan approve pada unreadable/ambiguous/mismatch.
11. badge stale muncul saat versi attempt berbeda; aksi reevaluate tidak berjalan
    otomatis, disabled saat AI mati/OCR belum ada, dan duplicate 202 tidak
    menambah notifikasi sukses ganda.

### 12.5 Browser/E2E

1. login cabang A: hanya rekening A tampil; logout/login cabang B: data A tidak
   bocor dari cache.
2. cabang tanpa assignment: tidak ada rekening, submit transfer/unggah bukti
   gagal dengan pesan aman.
3. Superadmin menetapkan dua rekening: keduanya muncul setelah refetch.
4. Manager tidak melihat editor dan navigasi langsung ke endpoint ditolak.
5. tanpa CSRF, perubahan UI/API gagal dan reload menunjukkan tidak ada mutasi.
6. nonaktifkan salah satu rekening: langsung hilang dari instruksi; rekening
   lain tetap ada.
7. cabut assignment saat customer membuka halaman: submit ditolak, UI refetch,
   tidak menggunakan data stale.
8. upload bukti lintas cabang/rekening tak diizinkan: order tetap Submitted,
   chip review tampil, admin melihat alasan.
9. order yang sudah Done tetap menampilkan histori/dokumen semula setelah
    assignment dicabut.
10. deactivate rekening B, edit rekening aktif lain, lalu reactivate B: B tidak
    tercabut diam-diam dan kembali tampil efektif; pencabutan eksplisit B meminta
    konfirmasi dan tidak dapat diaktifkan ulang selama B nonaktif.

### 12.6 Gerbang migrasi/rilis

- migrasi diuji dari database bersih dan salinan anonim;
- setelah migrasi, jumlah assignment adalah nol kecuali ada input eksplisit;
- tidak ada SQL `INSERT ... SELECT` yang memberi semua rekening ke semua cabang;
- preflight mencetak jumlah cabang configured/blocked tanpa nomor rekening;
- staging membuktikan minimal satu cabang configured dan satu blocked;
- produksi memerlukan approval manual dan rencana rollback fail-closed.

## 13. Observability dan keamanan

Metrik tanpa label UUID ber-cardinality tinggi:

- jumlah cabang aktif configured vs blocked;
- jumlah kegagalan `BRANCH_BANK_ACCOUNT_UNAVAILABLE`;
- jumlah attempt review per status tujuan;
- jumlah konflik versi Admin API;
- jumlah rekening global nonaktif yang masih mempunyai assignment allowed.

Log terstruktur boleh memuat `request_id`, actor ID, branch ID, order ID,
bank-account ID, allowlist version, dan status hasil. Log tidak boleh memuat
nomor rekening lengkap, payload OCR raw, cookie, CSRF token, atau bukti.

Kontrol keamanan:

- `admin.bank` diperiksa di router dan service;
- kepemilikan cabang diperiksa di service untuk endpoint customer/order;
- CSRF wajib pada seluruh mutasi;
- rate limit yang ada untuk unggahan tetap berlaku;
- audit berada di transaksi yang sama dengan mutasi;
- nomor masked tidak dianggap otorisasi; UUID juga bukan kapabilitas;
- respons 403/404 tidak membocorkan assignment cabang lain.

## 14. Kriteria penerimaan desain

Desain dinyatakan siap implementasi hanya bila reviewer dapat menjawab “ya”
untuk seluruh butir berikut:

- default cabang lama adalah blocked, bukan semua rekening;
- customer hanya melihat rekening aktif dan diizinkan untuk cabang sesinya;
- manual dan OCR memakai aturan allowlist yang sama di backend;
- perubahan assignment tidak dapat race menghasilkan Done yang tidak sah;
- rekening nonaktif/dicabut tidak sah untuk keputusan baru;
- histori Done dan attempt lama tidak ditulis ulang;
- mismatch/ambigu/tidak terbaca selalu review, tidak auto-reject final dan tidak
  auto-Done;
- lintas cabang, tanpa assignment, rekening nonaktif, dan CSRF tercakup tes;
- tidak ada nomor rekening nyata/rahasia di dokumen ini;
- VA tetap OFF dan tidak ada fallback permissive;
- QA sudah menyetujui dan CEO sudah memberi persetujuan tertulis sebelum kartu
  implementasi dibuka.

## 15. Keputusan yang memerlukan approval tertulis CEO

Dokumen ini tidak meninggalkan fallback teknis terbuka. CEO diminta menyetujui
secara tertulis paket keputusan berikut sebelum implementasi:

1. assignment nol memblokir transfer, termasuk selama transisi cabang lama;
2. hanya Superadmin yang mengubah assignment; Manager hanya melihat status dan
   tetap dapat melakukan verifikasi sesuai capability yang ada;
3. rekening yang dicabut/nonaktif tidak mempunyai grandfathering per-order;
4. admin manual tidak mempunyai override untuk meloloskan rekening yang tidak
   efektif;
5. beberapa rekening efektif setara—belum ada konsep rekening utama;
6. VA tetap OFF dan tidak disentuh oleh implementasi allowlist.
