# C0 — Sub-state revisi pembayaran dan gerbang LUNAS D3

Status dokumen: keputusan desain final untuk implementasi C1 dan kartu D3 sesudahnya.
Dokumen ini adalah delta normatif terhadap `branch-deposit.md`. Bila ada konflik
khusus revisi order Done yang totalnya berubah, dokumen ini yang berlaku.

## 1. Ruang lingkup dan dasar keputusan

Dokumen ini hanya menetapkan kontrak revisi order Done yang totalnya tetap, naik,
atau turun. Dokumen ini tidak mengubah keputusan bisnis D3 §16 (paket 6A):

- deposit tetap saldo per cabang dan tidak dipindahkan antar-cabang;
- top-up dan overpay hanya menjadi kredit setelah keputusan Superadmin;
- allocation adalah debit committed dan reversal selalu eksplisit;
- pembayaran campuran tetap manual;
- Virtual Account tetap OFF;
- tidak ada opening balance produksi.

Pilihan final adalah **jalur pending wajib untuk setiap revisi yang mengubah
total**. Implementasi awal tidak menyediakan shortcut “revisi dan langsung Done”
dalam endpoint revisi. Settlement versi baru dilakukan oleh transaksi verifikasi
terpisah yang atomik. Pilihan ini lebih ketat daripada alternatif atomik satu
request di D3 §8.1, tetapi tidak mengubah kebijakan bisnis §16 dan tidak
memerlukan keputusan CEO baru.

Gerbang B1–B2 terakhir belum membuktikan E2E browser terautentikasi karena fixture
vault tidak tersedia. Hasil PostgreSQL dan frontend yang dilaporkan lulus tidak
dipakai sebagai klaim E2E. Risiko ini tetap menjadi gerbang implementasi/release,
bukan alasan mengubah kontrak desain berikut.

## 2. Pilihan bentuk state final

### 2.1 State utama dan sub-state

`order_status` tetap mempunyai tiga nilai lama:

```text
draft | submitted | done
```

Tambahkan sub-state nullable pada order:

```text
revision_payment_state =
  NULL
  | revision_underpayment
  | revision_overpayment_pending
```

Kombinasi yang sah:

| `orders.status` | `revision_payment_state` | Makna |
|---|---|---|
| `draft` | `NULL` | Draft biasa |
| `submitted` | `NULL` | Submitted biasa, belum pernah Done atau bukan revisi pembayaran aktif |
| `submitted` | `revision_underpayment` | Revisi Done menaikkan total; settlement versi baru belum cocok |
| `submitted` | `revision_overpayment_pending` | Revisi Done menurunkan total; rekonsiliasi delta/overpay versi baru belum final |
| `done` | `NULL` | Settlement aktif tepat untuk total kini |

Sub-state ditempatkan pada kolom terpisah, bukan menambah nilai `order_status`,
agar consumer lama secara fail-safe melihat revisi belum lunas sebagai
`submitted`, bukan `done`. API tetap mengembalikan sub-state secara eksplisit;
consumer baru dilarang menyimpulkan jenis pending hanya dari `status`.

### 2.2 Hubungan dengan status verifikasi

Kombinasi normatif:

```text
revision_underpayment:
  status = submitted
  verif_status = perlu_review
  alasan_review = kurang_bayar

revision_overpayment_pending:
  status = submitted
  verif_status = perlu_review
  alasan_review = lebih_bayar

done:
  revision_payment_state = NULL
  verif_status = cocok
  alasan_review = NULL
```

`revision_overpayment_pending` berarti versi dengan total turun belum mempunyai
settlement aktif yang final. Nama ini tidak berarti seluruh delta pasti transfer
tunai: sebagian/seluruh delta dapat diselesaikan oleh reversal allocation deposit
yang eksplisit. Bila masih ada transfer lebih setelah reversal, keputusan
`credit_to_deposit` tetap hanya milik Superadmin.

### 2.3 State machine

```text
Done vN -- revisi total tetap --> Done vN+1
  |                                  pointer settlement lama dipertahankan
  |
  +-- total naik --> Submitted + revision_underpayment
  |                    |
  |                    +-- verifikasi settlement vN+1 atomik --> Done vN+1
  |
  +-- total turun + reversal plan bila perlu
                       --> Submitted + revision_overpayment_pending
                              |
                              +-- rekonsiliasi/keputusan overpay dan
                                  verifikasi settlement vN+1 atomik
                                  --> Done vN+1
```

Revisi kedua ketika `revision_payment_state IS NOT NULL` ditolak pada implementasi
awal. Tidak ada cancel/revert revisi terselubung. Koreksi harus diselesaikan lewat
prosedur rekonsiliasi yang teraudit atau desain lanjutan; sistem tidak boleh
mengarang kembali pointer lama sebagai pointer aktif.

## 3. Invarian database

### 3.1 Kolom baru dan histori typed

Kontrak skema implementasi:

```sql
CREATE TYPE revision_payment_state AS ENUM (
  'revision_underpayment',
  'revision_overpayment_pending'
);

CREATE TYPE revision_payment_transition AS ENUM (
  'total_unchanged',
  'underpayment',
  'overpayment'
);

ALTER TABLE orders
  ADD COLUMN revision_payment_state revision_payment_state,
  ADD COLUMN revision_payment_started_at timestamptz;
```

`invoice_revisions` menyimpan identitas settlement yang dilepas dalam kolom typed,
bukan hanya JSON audit. Nama dan tipe kontraknya:

```text
previous_done_verification_log_id UUID
previous_done_order_version       INT
previous_done_total               BIGINT
previous_done_deposit_amount      BIGINT
previous_done_transfer_amount     BIGINT
previous_done_at                  TIMESTAMPTZ
payment_transition                revision_payment_transition
settlement_contract_version       SMALLINT
```

Migrasi menambah enam kolom `previous_done_*` dan `payment_transition` sebagai
nullable. `settlement_contract_version` menjadi diskriminator yang membedakan row
legacy secara deterministik dari write baru, bukan menebak dari `created_at` atau
isi JSON:

1. tambah `settlement_contract_version` nullable tanpa default;
2. backfill seluruh row yang sudah ada menjadi `0` dalam transaksi migrasi;
3. ubah default menjadi `1`, lalu `SET NOT NULL`;
4. pasang CHECK di bawah sebagai `NOT VALID`; semua INSERT/UPDATE baru, termasuk
   writer yang mengandalkan default `1`, langsung wajib membawa snapshot lengkap;
5. setelah inventaris legacy selesai, `VALIDATE CONSTRAINT`. Row versi `0` tetap
   immutable dan hanya boleh mempunyai seluruh field settlement baru NULL.

Semua write revisi baru dari Done wajib memakai versi kontrak `1`, mengisi
`payment_transition`, dan mengisi keenam kolom snapshot. CHECK all-or-none melarang
snapshot parsial sekaligus mencegah write baru menyamar sebagai row legacy:

```sql
ALTER TABLE invoice_revisions
  ADD COLUMN previous_done_verification_log_id uuid,
  ADD COLUMN previous_done_order_version int,
  ADD COLUMN previous_done_total bigint,
  ADD COLUMN previous_done_deposit_amount bigint,
  ADD COLUMN previous_done_transfer_amount bigint,
  ADD COLUMN previous_done_at timestamptz,
  ADD COLUMN payment_transition revision_payment_transition,
  ADD COLUMN settlement_contract_version smallint;

UPDATE invoice_revisions SET settlement_contract_version = 0;

ALTER TABLE invoice_revisions
  ALTER COLUMN settlement_contract_version SET DEFAULT 1,
  ALTER COLUMN settlement_contract_version SET NOT NULL,
  ADD CONSTRAINT invoice_revisions_settlement_snapshot_consistent CHECK ((
    (settlement_contract_version = 0
     AND payment_transition IS NULL
     AND previous_done_verification_log_id IS NULL
     AND previous_done_order_version IS NULL
     AND previous_done_total IS NULL
     AND previous_done_deposit_amount IS NULL
     AND previous_done_transfer_amount IS NULL
     AND previous_done_at IS NULL)
    OR
    (settlement_contract_version = 1
     AND payment_transition IS NOT NULL
     AND previous_done_verification_log_id IS NOT NULL
     AND previous_done_order_version IS NOT NULL
     AND previous_done_total IS NOT NULL
     AND previous_done_deposit_amount IS NOT NULL
     AND previous_done_transfer_amount IS NOT NULL
     AND previous_done_at IS NOT NULL)
  ) IS TRUE) NOT VALID;
```

Tidak ada writer aplikasi yang boleh mengirim
`settlement_contract_version=0`. Setelah cutover, grant INSERT role aplikasi harus
berupa daftar kolom yang **mengecualikan** `settlement_contract_version`, sehingga
INSERT selalu memakai default `1`; role aplikasi juga tidak diberi hak UPDATE atas
kolom tersebut atau atas row legacy. Hanya role migrasi memiliki akses menulis
versi `0`. Constraint hanya mengenal versi `0` dan `1`, sehingga nilai lain maupun
NULL ditolak oleh pembungkus `IS TRUE`.

FK berikut wajib nyata:

```text
(previous_done_verification_log_id, order_id,
 previous_done_order_version, previous_done_total,
 previous_done_deposit_amount, previous_done_transfer_amount)
  -> verification_logs
     (id, order_id, order_version, total_pesanan,
      deposit_amount, transfer_amount)
```

`previous_done_at` harus sama dengan `orders.done_at` yang terkunci sebelum pointer
aktif dilepas. Snapshot JSON versi lama tetap disimpan, tetapi tidak menggantikan
FK typed. Row
`invoice_revisions`, verification log, source snapshot, journal, allocation, dan
dokumen lama immutable untuk role aplikasi.

Tidak diperlukan `revision_origin_*` pada `orders`: workflow aktif diperoleh dari
`orders.revision_payment_state`, sedangkan settlement asal diperoleh dari row
`invoice_revisions` dengan `(order_id, versi_baru = orders.versi)`.

### 3.2 CHECK pointer dan waktu Done

Empat field `done_*` dan `done_at` adalah state settlement **aktif**, bukan arsip.
Constraint lama `done_punya_waktu` dan, bila D3 pernah dipasang sebagian,
`orders_done_payment_breakdown` diganti, bukan ditumpuk dengan aturan samar.
Baseline repository sampai migrasi `0011` hanya mempunyai
`done_punya_waktu`; karena itu penggantian wajib menerima kedua lintasan secara
deterministik:

1. preflight katalog memastikan setiap constraint bernama
   `done_punya_waktu`/`orders_done_payment_breakdown` yang ada benar-benar CHECK
   pada `orders`; nama sama dengan jenis atau tabel yang berbeda adalah drift dan
   migrasi berhenti;
2. `DROP CONSTRAINT IF EXISTS` dipakai untuk keduanya. Ini bukan mengabaikan drift:
   preflight sebelumnya sudah menolak objek bernama sama yang tidak sesuai;
3. tambah satu-satunya pengganti `orders_settlement_state_consistent` sebagai
   `NOT VALID`, lakukan rekonsiliasi, lalu `VALIDATE` sebelum aktivasi writer.

Bentuk SQL normatif berikut sengaja menulis setiap cabang state secara eksplisit
sehingga evaluasi `NULL` tidak dapat berubah menjadi `UNKNOWN` dan lolos sebagai
CHECK:

```sql
ALTER TABLE orders
  DROP CONSTRAINT IF EXISTS done_punya_waktu,
  DROP CONSTRAINT IF EXISTS orders_done_payment_breakdown,
  ADD CONSTRAINT orders_settlement_state_consistent CHECK ((
    -- Draft biasa.
    (status = 'draft'
     AND revision_payment_state IS NULL
     AND revision_payment_started_at IS NULL
     AND done_at IS NULL
     AND done_verification_log_id IS NULL
     AND done_order_version IS NULL
     AND done_deposit_amount IS NULL
     AND done_transfer_amount IS NULL
     AND verif_status = 'belum'
     AND alasan_review IS NULL)
    OR
    -- Submitted biasa: belum diperiksa, atau sedang review dengan alasan nyata.
    (status = 'submitted'
     AND revision_payment_state IS NULL
     AND revision_payment_started_at IS NULL
     AND done_at IS NULL
     AND done_verification_log_id IS NULL
     AND done_order_version IS NULL
     AND done_deposit_amount IS NULL
     AND done_transfer_amount IS NULL
     AND (
       (verif_status = 'belum' AND alasan_review IS NULL)
       OR
       (verif_status = 'perlu_review' AND alasan_review IS NOT NULL)
     ))
    OR
    -- Revisi total naik.
    (status = 'submitted'
     AND revision_payment_state = 'revision_underpayment'
     AND revision_payment_started_at IS NOT NULL
     AND done_at IS NULL
     AND done_verification_log_id IS NULL
     AND done_order_version IS NULL
     AND done_deposit_amount IS NULL
     AND done_transfer_amount IS NULL
     AND verif_status = 'perlu_review'
     AND alasan_review = 'kurang_bayar')
    OR
    -- Revisi total turun.
    (status = 'submitted'
     AND revision_payment_state = 'revision_overpayment_pending'
     AND revision_payment_started_at IS NOT NULL
     AND done_at IS NULL
     AND done_verification_log_id IS NULL
     AND done_order_version IS NULL
     AND done_deposit_amount IS NULL
     AND done_transfer_amount IS NULL
     AND verif_status = 'perlu_review'
     AND alasan_review = 'lebih_bayar')
    OR
    -- Settlement aktif yang boleh dipublikasikan sebagai LUNAS.
    (status = 'done'
     AND revision_payment_state IS NULL
     AND revision_payment_started_at IS NULL
     AND done_at IS NOT NULL
     AND done_verification_log_id IS NOT NULL
     AND done_order_version IS NOT NULL
     AND done_order_version BETWEEN 0 AND versi
     AND done_deposit_amount IS NOT NULL
     AND done_deposit_amount >= 0
     AND done_transfer_amount IS NOT NULL
     AND done_transfer_amount >= 0
     AND done_deposit_amount + done_transfer_amount = total
     AND verif_status = 'cocok'
     AND alasan_review IS NULL)
  ) IS TRUE) NOT VALID;
```

Constraint nomor/tanggal Submitted lama tetap berlaku; revisi pending bukan Draft.
`orders_done_payment_pointer_all_or_none` boleh dipertahankan sebagai defense in
depth, tetapi bukan pengganti CHECK lengkap di atas.

FK histori revisi ditargetkan ke unique enam kolom yang sama dengan pointer aktif.
`verification_logs_settlement_breakdown` adalah bagian normatif D3 §4.8, tetapi
belum ada pada baseline migrasi repository `0001`–`0011`. C1 adalah pemasang D3,
bukan konsumen yang boleh menganggap D3 sudah terpasang. Urutan implementable-nya:

1. tambah/backfill `verification_logs.order_version`, `deposit_amount`, dan
   `transfer_amount`, lalu jalankan pemeriksaan breakdown serta duplikat enam kolom;
2. periksa katalog untuk nama `verification_logs_settlement_breakdown`;
3. bila belum ada (lintasan baseline `0011`), buat UNIQUE enam kolom **tepat satu
   kali**; bila sudah ada (lintasan D3 terpasang sebagian/retry), lanjut hanya jika
   ia constraint UNIQUE pada `verification_logs` dengan urutan kolom persis
   `(id, order_id, order_version, total_pesanan, deposit_amount,
   transfer_amount)`; nama sama dengan jenis/kolom/urutan berbeda adalah drift dan
   migrasi berhenti fail-closed;
4. baru pasang kedua FK pemakai target itu: pointer aktif `orders` menurut D3 §4.8
   dan histori `invoice_revisions` di bawah. Jangan membuat unique kedua dengan
   nama lain dan jangan melakukan drop/recreate target pada retry.

Preflight harus membaca `pg_constraint` (`contype = 'u'`, `conrelid` dan `conkey`
berdasarkan `pg_attribute.attnum`), bukan sekadar mencari nama. Bentuk pemasangan
untuk lintasan baseline adalah:

```sql
ALTER TABLE verification_logs
  ADD CONSTRAINT verification_logs_settlement_breakdown
  UNIQUE (
    id, order_id, order_version, total_pesanan,
    deposit_amount, transfer_amount
  );
```

Pernyataan itu dijalankan hanya setelah preflight menyatakan constraint absen;
UNIQUE tidak memakai `NOT VALID`, sehingga pemeriksaan duplicate/backfill harus
selesai lebih dahulu. Setelah target tersedia dan telah diverifikasi, pasang FK:

```sql
ALTER TABLE invoice_revisions
  ADD CONSTRAINT invoice_revisions_previous_done_fk
  FOREIGN KEY (
    previous_done_verification_log_id, order_id,
    previous_done_order_version, previous_done_total,
    previous_done_deposit_amount, previous_done_transfer_amount
  ) REFERENCES verification_logs (
    id, order_id, order_version, total_pesanan,
    deposit_amount, transfer_amount
  ) ON DELETE RESTRICT NOT VALID;
```

FK sengaja tetap `MATCH SIMPLE` karena `order_id` existing selalu non-NULL dan row
legacy versi kontrak `0` mempunyai seluruh kolom snapshot NULL. Integritas
all-or-none dijamin oleh
`invoice_revisions_settlement_snapshot_consistent`; untuk versi kontrak `1`, semua
komponen key FK pasti non-NULL sehingga FK tidak dapat dilewati oleh semantik NULL.

Urutan cutover wajib: tambah kolom nullable; backfill dan buktikan tidak ada
breakdown/duplicate ilegal; buat-atau-verifikasi unique target D3 sesuai lintasan
di atas; pasang FK/CHECK `NOT VALID` (PostgreSQL tetap memeriksa semua
INSERT/UPDATE baru); matikan feature
revisi total berubah dan semua publisher LUNAS lama; inventaris row legacy yang
melanggar ke antrean rekonsiliasi tanpa mengubahnya menjadi Done; backfill hanya
dari bukti typed yang deterministik; jalankan query anti-join/negative-state sampai
nol; `VALIDATE CONSTRAINT` FK lalu CHECK; baru aktifkan writer dan publisher baru.
Selama masih ada row legacy invalid, predikat §6.1 tetap fail-closed sehingga row
tersebut tidak publishable walaupun constraint masih `NOT VALID`.

Matriks uji migrasi C1 wajib mencakup:

- database bersih dari seluruh migrasi sampai `0011`: unique enam kolom dibuat
  sekali, kedua nama CHECK lama ditangani walau
  `orders_done_payment_breakdown` absen, dan semua FK dapat dipasang/validasi;
- skema D3 terpasang sebagian dengan unique enam kolom dan
  `orders_done_payment_breakdown` yang benar: unique tidak dibuat ulang, CHECK lama
  diganti, dan hasil akhir identik dengan lintasan baseline;
- retry migrasi setelah create target tetapi sebelum FK: tidak ada duplicate
  unique dan proses melanjutkan dari hasil katalog;
- constraint target bernama sama tetapi `contype`, tabel, kolom, atau urutannya
  salah: migrasi ditolak sebelum drop/add lain;
- data duplikat enam kolom atau breakdown/backfill ilegal: pemasangan UNIQUE
  ditolak dan transaksi rollback utuh;
- baseline dengan hanya `done_punya_waktu`, skema tanpa kedua CHECK lama, serta
  skema dengan constraint bernama sama tetapi bukan CHECK pada `orders`: dua kasus
  pertama berhasil menuju constraint final, kasus drift terakhir ditolak;
- negative-state SQL §8 tetap menolak seluruh kombinasi pointer/state ilegal
  setelah `VALIDATE CONSTRAINT`.

`done_order_version` boleh lebih kecil dari `orders.versi` hanya pada Done hasil
revisi total tetap. Setelah revisi total berubah diselesaikan, pointer baru wajib
menunjuk attempt untuk `orders.versi` dan total kini.

### 3.3 Allocation dan source snapshot lintas versi

Semua aggregate memakai seluruh allocation aktif untuk `order_id`, tidak hanya
versi kini:

```text
committed_deposit(order_id)
  = SUM(amount - reversed_amount) untuk seluruh versi

committed_deposit(order_id) <= orders.total
```

Masuk ke sub-state tidak menulis debit kedua. Settlement versi baru membuat row
`verification_deposit_sources` baru untuk setiap allocation aktif lintas versi,
dengan `amount_snapshot` terkunci saat attempt. Snapshot attempt lama tidak
diubah. Unique `(verification_log_id, allocation_id)` mencegah sumber ganda dalam
satu attempt; retry attempt yang sama tidak menambah row.

### 3.4 Reversal pada total turun

Sebelum total diturunkan, service menghitung committed deposit di bawah lock.
Jika `committed_deposit > proposed_total`, request wajib membawa reversal plan
eksplisit yang total efektifnya sedikitnya:

```text
committed_deposit - proposed_total
```

Setiap item plan memuat `allocation_id`, `amount`, dan `reason`. Satu
`Idempotency-Key` pada request revisi mencakup seluruh plan; item tidak mempunyai
key mandiri. Service tidak memilih allocation atau nominal secara otomatis.

Setelah seluruh reversal terencana diterapkan, invariant
`committed_deposit <= proposed_total` harus benar sebelum update order. Reversal
menambah journal credit dan menaikkan saldo available; row debit/allocation lama
tidak diubah selain cache `reversed_amount` sesuai kontrak D3.

Plan kosong/kurang, allocation order/cabang lain, nominal melebihi active amount,
reason kosong, atau plan yang menyebabkan invariant journal/cache gagal menolak
seluruh transaksi.

## 4. Transaksi normatif

### 4.1 Revisi total tetap

Dalam satu transaksi:

1. lock branch `FOR SHARE`, deposit account, order, lalu allocation menurut urutan
   global D3;
2. claim idempotency key dan validasi `expected_order_version` serta pointer Done;
3. simpan `invoice_revisions` dengan transition `total_unchanged`;
4. ubah lines, naikkan versi, total tetap sama;
5. pertahankan `status=done`, `done_at`, dan seluruh `done_*`;
6. tulis audit/event dan enqueue render/email invoice versi baru;
7. commit.

Tidak ada journal, allocation, reversal, verification log, atau source snapshot
baru. Invoice baru boleh bertanda LUNAS karena pointer lama masih membuktikan total
yang sama. UI/dokumen wajib membedakan versi dokumen dari versi settlement.

### 4.2 Revisi total naik

Dalam satu transaksi:

1. lock dan claim seperti §4.1;
2. pastikan state awal Done dan tidak ada revisi pembayaran aktif;
3. simpan settlement lama pada `invoice_revisions`;
4. ubah lines/total, naikkan versi;
5. set `status=submitted`, state `revision_underpayment`, waktu mulai, status
   verifikasi `perlu_review/kurang_bayar`;
6. set `done_at` dan seluruh `done_*` ke NULL;
7. pertahankan allocation/journal lama tanpa debit ulang;
8. tulis audit/event dan enqueue notifikasi revisi belum lunas, bukan invoice LUNAS;
9. commit.

Allocation tambahan boleh dibuat sesudahnya melalui workflow admin D3, selalu
terikat versi kini dan tetap dibatasi aggregate seluruh versi.

### 4.3 Revisi total turun

Urutan sama dengan §4.2, dengan tambahan sebelum update total:

1. hitung committed deposit seluruh versi;
2. validasi dan jalankan reversal plan eksplisit bila committed melebihi total
   usulan;
3. cek ulang journal, balance, cache reversal, dan committed deposit;
4. set state `revision_overpayment_pending` dan alasan `lebih_bayar`;
5. kosongkan pointer aktif dan commit seluruh reversal + revisi secara atomik.

Tidak ada auto-credit untuk selisih transfer. Jika settlement asal memakai
transfer melebihi kebutuhan transfer versi baru, attempt final harus memuat
keputusan split baru dan kredit overpay yang diputuskan Superadmin. Reuse bukti
transfer yang sama untuk order yang sama pada versi baru adalah snapshot histori,
bukan penerimaan uang kedua; journal overpay hanya boleh dibuat satu kali untuk
keputusan ekonomi tersebut.

Untuk proof yang dipakai lintas versi, split attempt versi baru membedakan:

```text
approved_nominal
applied_to_order
credited_to_deposit_total   -- kumulatif sepanjang umur proof
new_credit_to_deposit       -- delta journal pada attempt ini
```

Pada settlement Done berlaku:

```text
approved_nominal
  = applied_to_order + credited_to_deposit_total

new_credit_to_deposit
  = credited_to_deposit_total - SUM(overpay_credit committed sebelumnya
                                    untuk payment_proof_id yang sama)
new_credit_to_deposit >= 0
```

Service mengunci proof dan seluruh journal kredit proof tersebut. Nilai kumulatif
tidak boleh turun, jumlah journal kredit tidak boleh melebihi approved nominal,
dan `new_credit_to_deposit > 0` memerlukan Superadmin. Dengan demikian revisi turun
bertahap dapat menambah kredit hanya sebesar delta, retry tidak menggandakan
kredit, dan revisi naik setelah overpay pernah dikreditkan harus memakai sumber
dana baru—uang yang sudah menjadi saldo deposit tidak boleh diam-diam dipakai lagi
sebagai transfer proof.

#### 4.3.1 Sumber kebenaran split proof dan kredit lintas versi

Objek JSON `credited_to_deposit` pada D3 §4.7/§6.2 adalah bentuk request untuk
attempt pertama. Untuk proof yang pernah dipakai lintas versi, bentuk penyimpanan
final adalah tabel immutable berikut; API baru memakai nama kumulatif C0:

```sql
ALTER TABLE payment_proofs
  ADD CONSTRAINT payment_proofs_order_identity UNIQUE (id, order_id);

ALTER TABLE orders
  ADD CONSTRAINT orders_branch_identity UNIQUE (id, branch_id);

CREATE TABLE payment_proof_economic_decisions (
    payment_proof_id uuid PRIMARY KEY,
    order_id         uuid NOT NULL,
    branch_id        uuid NOT NULL,
    approved_nominal bigint NOT NULL CHECK (approved_nominal >= 0),
    decided_by       uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    created_at       timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (payment_proof_id, order_id)
      REFERENCES payment_proofs (id, order_id) ON DELETE RESTRICT,
    FOREIGN KEY (order_id, branch_id)
      REFERENCES orders (id, branch_id) ON DELETE RESTRICT,
    UNIQUE (payment_proof_id, order_id, branch_id, approved_nominal)
);

ALTER TABLE deposit_transactions
  ADD COLUMN order_verification_log_id uuid
      REFERENCES verification_logs(id) ON DELETE RESTRICT,
  ADD CONSTRAINT deposit_transactions_overpay_typed CHECK (
    kind <> 'overpay_credit'
    OR
    (order_verification_log_id IS NOT NULL
     AND source_proof_id IS NOT NULL
     AND order_id IS NOT NULL
     AND order_version IS NOT NULL
     AND amount > 0)
  ),
  ADD CONSTRAINT deposit_transactions_overpay_typed_identity UNIQUE (
    id, order_verification_log_id, source_proof_id,
    order_id, branch_id, order_version, amount
  );

CREATE TABLE verification_proof_splits (
    verification_log_id       uuid NOT NULL,
    order_id                   uuid NOT NULL,
    order_version              int NOT NULL,
    settlement_total           bigint NOT NULL,
    settlement_deposit_amount  bigint NOT NULL,
    settlement_transfer_amount bigint NOT NULL,
    payment_proof_id           uuid NOT NULL,
    branch_id                  uuid NOT NULL,
    approved_nominal           bigint NOT NULL CHECK (approved_nominal >= 0),
    applied_to_order           bigint NOT NULL CHECK (applied_to_order >= 0),
    credited_to_deposit_total  bigint NOT NULL
        CHECK (credited_to_deposit_total >= 0),
    new_credit_to_deposit      bigint NOT NULL
        CHECK (new_credit_to_deposit >= 0),
    new_credit_transaction_id  uuid UNIQUE,
    created_at                 timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (verification_log_id, payment_proof_id),
    FOREIGN KEY (
      verification_log_id, order_id, order_version, settlement_total,
      settlement_deposit_amount, settlement_transfer_amount
    ) REFERENCES verification_logs (
      id, order_id, order_version, total_pesanan,
      deposit_amount, transfer_amount
    ) ON DELETE RESTRICT,
    FOREIGN KEY (payment_proof_id, order_id)
      REFERENCES payment_proofs (id, order_id) ON DELETE RESTRICT,
    FOREIGN KEY (order_id, branch_id)
      REFERENCES orders (id, branch_id) ON DELETE RESTRICT,
    FOREIGN KEY (payment_proof_id, order_id, branch_id, approved_nominal)
      REFERENCES payment_proof_economic_decisions
        (payment_proof_id, order_id, branch_id, approved_nominal)
      ON DELETE RESTRICT,
    FOREIGN KEY (
      new_credit_transaction_id, verification_log_id, payment_proof_id,
      order_id, branch_id, order_version, new_credit_to_deposit
    ) REFERENCES deposit_transactions (
      id, order_verification_log_id, source_proof_id,
      order_id, branch_id, order_version, amount
    ) ON DELETE RESTRICT,
    CHECK (
      approved_nominal = applied_to_order + credited_to_deposit_total
    ),
    CHECK (
      (new_credit_to_deposit = 0 AND new_credit_transaction_id IS NULL)
      OR
      (new_credit_to_deposit > 0 AND new_credit_transaction_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX deposit_transactions_overpay_split_once
  ON deposit_transactions (order_verification_log_id, source_proof_id)
  WHERE kind = 'overpay_credit';
```

Implementasi mengganti bentuk umum `source_verification_id` dari D3 §4.5: untuk
kredit order dipakai kolom typed `order_verification_log_id` yang benar-benar
mereferensikan `verification_logs(id)`, bukan UUID polymorphic tanpa FK.
Trigger/service transaksi
memastikan journal pada `new_credit_transaction_id` mempunyai
`kind='overpay_credit'`, `source_proof_id`, verification, order, branch, version,
dan amount yang sama dengan delta keputusan. Role aplikasi tidak boleh
UPDATE/DELETE split.

`payment_proof_economic_decisions` adalah sumber kebenaran nominal approved yang
tepat satu per proof; row pertama dibuat di bawah lock proof oleh actor
`admin.verify`. Attempt versi berikutnya wajib mereferensikan nominal yang sama dan
tidak dapat “menaikkan” approved amount. Hak `admin.deposit_credit` baru diperlukan
bila delta kredit positif, bukan untuk mencatat keputusan nominal tanpa kredit.

`new_credit_to_deposit` disimpan sebagai snapshot immutable keputusan attempt dan
harus sama dengan amount journal melalui FK komposit di atas; journal tetap sumber
kebenaran ledger. Server menghitung delta ini, tidak mempercayai nilai turunan dari
klien:

```text
proof_credit_gross = SUM(amount overpay_credit committed untuk proof)
proof_credit_reversed = SUM(ABS(amount) overpay_reversal_debit committed
                            yang menunjuk kredit proof tersebut)
proof_credit_net = proof_credit_gross - proof_credit_reversed

new_credit_to_deposit
  = credited_to_deposit_total - proof_credit_gross_sebelum_attempt
```

`credited_to_deposit_total` harus sama dengan gross setelah attempt. Gross bersifat
monotonik dan menghitung keputusan ekonomi sepanjang umur proof; reversal hanya
mengurangi net saldo, tidak membuat nominal proof dapat dikreditkan atau diterapkan
lagi. Karena itu `new_credit_to_deposit = 0` mewajibkan transaction FK NULL,
sedangkan nilai positif mewajibkan tepat satu transaction dengan amount yang sama.
Constraint lintas-row ini ditegakkan trigger/service di transaksi yang sama dan
direkonsiliasi terhadap journal.

Contoh normatif: proof Rp1.000.000 pada v1 menerapkan Rp800.000 dan mengkreditkan
Rp200.000 (gross/net 200.000). Revisi turun v2 menerapkan Rp700.000, sehingga target
kumulatif Rp300.000 dan hanya journal delta Rp100.000 dibuat. Revisi turun v3
menerapkan Rp650.000, sehingga hanya delta Rp50.000 dibuat. Dua retry/paralel v3
bertemu PK split, unique journal, dan lock proof: satu membuat Rp50.000, lainnya
mengembalikan hasil idempoten/konflik tanpa kredit kedua. Bila Rp50.000 kemudian
direversal, gross tetap Rp350.000 dan net menjadi Rp300.000. Revisi naik v4 tidak
boleh menerapkan Rp750.000 dari proof yang sama karena Rp750.000 + gross Rp350.000
melebihi approved Rp1.000.000; kekurangan Rp100.000 harus memakai proof/sumber baru,
bukan memakai ulang kredit yang sudah pernah diputuskan.

### 4.4 Settlement state revisi ke Done

Endpoint verifikasi memakai transaksi D3 §5.4 dengan tambahan:

1. menerima hanya `submitted` dengan sub-state revisi yang dikenal;
2. lock seluruh allocation aktif lintas versi dan proof/decision terkait;
3. hitung ulang current total, committed deposit, transfer applied, dan overpay;
4. untuk underpayment, tolak bila sumber baru + carry-forward belum tepat total;
5. untuk overpayment pending, tolak bila selisih transfer belum mempunyai keputusan
   Superadmin atau split tidak seimbang;
6. buat verification log immutable untuk `orders.versi` dan total kini;
7. buat source snapshot seluruh allocation aktif tepat sekali;
8. set `status=done`, clear sub-state/waktu mulai, set `verif_status=cocok`, clear
   alasan, isi `done_at` dan seluruh `done_*` secara bersamaan;
9. enqueue dokumen/email current version dalam transaksi yang sama;
10. commit.

Tidak ada commit antara langkah 6–9. Kegagalan render terjadi sesudah commit dan
ditangani retry job idempoten; kegagalan transaksi tidak boleh membuat dokumen
atau email LUNAS terlihat.

## 5. Kontrak API

### 5.0 Otorisasi revisi dan mutasi uang

Hak `admin.revise` tetap mengendalikan semua revisi Done, termasuk total tetap,
naik, dan turun. Manager hanya boleh memanggil revisi bila setting server
`manager_boleh_revisi=true`; Superadmin tidak bergantung toggle tersebut. Reversal
plan yang menjadi bagian request revisi memakai hak debit/reversal
`admin.verify`, karena mengembalikan allocation committed dan bukan menciptakan
kredit baru. Konsekuensinya:

- Manager dengan `admin.revise`, toggle aktif, dan `admin.verify` boleh merevisi
  total turun serta menyertakan reversal plan yang cukup;
- Manager tanpa `admin.verify` ditolak atomik bila request memerlukan reversal;
- Manager dengan toggle mati ditolak sebelum plan/journal apa pun diproses;
- aksi yang membuat `overpay_credit` tetap memerlukan
  `admin.deposit_credit` milik Superadmin, baik saat settlement revisi maupun
  endpoint lain; `admin.revise`/`admin.verify` tidak pernah mengimplikasikan hak
  kredit.

Seluruh capability dan toggle diperiksa ulang di service dalam transaksi, bukan
hanya melalui visibility tombol UI.

### 5.1 Revisi

`POST /api/v1/admin/orders/{id}/revise` tetap menjadi endpoint tunggal. Header
`Idempotency-Key` dan CSRF wajib. Body menambah field:

```json
{
  "expected_order_version": 3,
  "expected_done_verification_log_id": "<uuid>",
  "reason": "Koreksi qty",
  "lines": [],
  "deposit_reversals": [
    {
      "allocation_id": "<uuid>",
      "amount": 100000,
      "reason": "Total revisi lebih kecil dari deposit committed"
    }
  ]
}
```

Aturan:

- `deposit_reversals` default `[]`;
- total tetap/naik menolak plan yang tidak kosong;
- total turun menerima plan hanya bila setiap item valid; plan wajib cukup jika
  committed deposit akan melebihi total baru;
- server menghitung total dari lines dan semua aggregate; nilai total dari klien
  diabaikan;
- response tidak pernah mengembalikan status Done untuk total berubah;
- retry fingerprint identik mengembalikan response committed pertama; key sama
  dengan payload berbeda ditolak.

Response order menambah:

```json
{
  "status": "submitted",
  "revision_payment_state": "revision_underpayment",
  "revision_payment_started_at": "<timestamp>",
  "payment": {
    "current_total": 1200000,
    "committed_deposit": 400000,
    "verified_transfer_for_current_version": 0,
    "remaining_due": 800000,
    "is_lunas": false
  },
  "previous_settlement": {
    "verification_log_id": "<uuid>",
    "order_version": 2,
    "total": 1000000,
    "historical": true
  },
  "current_invoice_lunas_available": false
}
```

Customer dan admin GET detail/list wajib mengembalikan sub-state. Customer tidak
boleh mengirim `branch_id`; isolasi tetap dari sesi.

### 5.2 Verifikasi

`POST /api/v1/admin/orders/{id}/verify` menerima Submitted biasa maupun kedua
sub-state. Untuk sub-state revisi:

- `expected_order_version` wajib sama dengan versi kini;
- `deposit_allocation_ids` wajib sama persis dengan seluruh allocation aktif lintas
  versi;
- proof split boleh mereferensikan proof order yang sama dari versi sebelumnya,
  tetapi wajib mengirim/menghasilkan snapshot kumulatif
  `credited_to_deposit_total` dan delta `new_credit_to_deposit`; proof tidak boleh
  dianggap transfer baru atau menggandakan overpay credit;
- keputusan yang menciptakan kredit tetap memerlukan `admin.deposit_credit`;
- Manager boleh menyelesaikan underpayment tepat bayar tanpa menciptakan kredit,
  tetapi tidak boleh memutuskan overpay credit;
- response Done hanya dikirim setelah pointer, source snapshot, audit, dan outbox
  committed.

### 5.3 Error tambahan

| HTTP | Code | Kondisi |
|---:|---|---|
| 409 | `ORDER_REVISION_PAYMENT_PENDING` | Revisi kedua/cancel terselubung saat sub-state aktif |
| 409 | `REVISION_ORIGIN_CONFLICT` | expected version/pointer Done tidak lagi sama |
| 409 | `REVISION_REVERSAL_PLAN_REQUIRED` | Total turun akan melanggar committed <= total tanpa plan cukup |
| 409 | `REVISION_REVERSAL_PLAN_INVALID` | Item plan salah order/cabang, nominal/reason tidak sah, duplicate allocation |
| 409 | `REVISION_SETTLEMENT_REQUIRED` | Aksi LUNAS/dokumen final diminta ketika sub-state aktif |
| 409 | `ORDER_VERSION_CONFLICT` | Versi berubah setelah layar dibuka |
| 422 | `PAYMENT_BREAKDOWN_MISMATCH` | Settlement versi kini tidak tepat total |
| 422 | `OVERPAY_DECISION_REQUIRED` | Selisih transfer versi turun belum diputuskan Superadmin |

Seluruh penolakan terjadi tanpa mutasi parsial, journal orphan, job, dokumen, atau
email.

## 6. Dokumen, email, UI, C&C, dan reporting

### 6.1 Gerbang LUNAS

Satu-satunya predikat LUNAS adalah:

```text
status = done
AND revision_payment_state IS NULL
AND pointer done_* lengkap dan valid
AND breakdown pointer = orders.total
```

Renderer, endpoint download current invoice, resend email invoice, list riwayat,
reporting, dan worker dilarang memakai `verif_status` saja atau verification log
historis terakhir sebagai tanda LUNAS.

Untuk revisi total berubah:

- tidak membuat `invoice_pdf` current version ketika masuk pending;
- tidak enqueue email invoice/LUNAS atau Surat Jalan current version;
- kirim notifikasi `revision_payment_pending` tanpa lampiran final, berisi versi,
  total baru, jenis selisih, dan kalimat tegas “belum lunas”;
- invoice/dokumen versi lama tetap immutable dan dapat diunduh sebagai histori,
  tetapi UI memberi label “versi historis — bukan status pembayaran saat ini”;
- sesudah settlement commit, barulah render dan kirim invoice current version
  bertanda LUNAS.

Aturan ini mengkhususkan AP-REV-04: revisi total tetap tetap langsung menghasilkan
dan mengirim invoice baru; revisi total berubah menunda invoice final sampai
settlement. Tidak ada overwrite berkas pending menjadi LUNAS pada key dokumen yang
sama.

### 6.2 UI dan cache

Pada kedua sub-state:

- chip order utama `Menunggu pelunasan revisi` atau `Menunggu keputusan selisih`;
- tombol download/resend invoice current version disabled dengan alasan;
- timeline tetap menampilkan settlement lama sebagai histori;
- kartu pembayaran memisahkan deposit carry-forward, allocation baru, transfer
  historis, transfer tambahan, reversal, overpay, dan remaining due;
- konflik 409 memicu refetch; aksi uang tidak diulang otomatis;
- invalidasi mencakup detail/list order, invoice history, deposit balance/journal,
  antrean admin, dashboard, reporting, dan reminder.

### 6.3 Cash & Carry dan reminder

Order C&C pada sub-state tetap belum lunas. `jatuh_tempo` tidak dihitung ulang;
BR-08 tetap memakai tanggal order asli + `cnc_hari`.

Deduplikasi reminder harus menjadi version-aware. Skema aktif final mempunyai
`order_version NOT NULL` dan PK `(order_id, order_version, tahap)`. Row legacy yang
versinya ambigu tidak boleh dipaksa ke sentinel atau versi order kini karena kedua
pilihan dapat salah menahan reminder sah. Ia dipindah atomik ke arsip typed yang
tidak dibaca scheduler:

```sql
CREATE TABLE cnc_reminders_legacy_archive (
    order_id       uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    tahap          text NOT NULL,
    sent_at        timestamptz NOT NULL,
    archived_at    timestamptz NOT NULL DEFAULT now(),
    archive_reason text NOT NULL CHECK (archive_reason = 'ambiguous_order_version'),
    PRIMARY KEY (order_id, tahap)
);

ALTER TABLE cnc_reminders ADD COLUMN order_version int;
-- Backfill hanya mapping yang dibuktikan event/dokumen immutable.
-- INSERT arsip <- row yang tetap NULL; verifikasi count/checksum; DELETE row itu.
ALTER TABLE cnc_reminders DROP CONSTRAINT cnc_reminders_pkey;
ALTER TABLE cnc_reminders ALTER COLUMN order_version SET NOT NULL;
ALTER TABLE cnc_reminders
  ADD PRIMARY KEY (order_id, order_version, tahap);
CREATE INDEX cnc_reminders_current_lookup
  ON cnc_reminders (order_id, order_version, sent_at DESC);
```

Cutover dilakukan dengan scheduler reminder dipause tetapi outbox sender tetap
berjalan: deploy reader yang memahami payload `order_version`; pause enqueue;
backfill row terbukti; copy row ambigu ke arsip dan cocokkan jumlah serta checksum;
hapus hanya row yang sudah ada di arsip; ubah PK/NOT NULL; deploy scheduler baru;
baru resume enqueue. Arsip hanya untuk audit dan tidak ikut anti-join/dedupe.

Scheduler baru dalam satu transaksi melakukan `INSERT ... ON CONFLICT DO NOTHING`
ke `cnc_reminders(order_id,order_version,tahap)` dan hanya bila insert berhasil
menulis outbox dengan payload `order_id`, `order_version`, `tahap`, serta dedupe key
yang sama. Unique `(order_id,order_version,tahap)` adalah otoritas enqueue; retry
outbox tidak membuat reminder row kedua. Worker sebelum send mengunci/membaca ulang
order dan membatalkan outbox bila version/state sudah berubah. Row reminder versi
lama tidak menghalangi reminder versi revisi. Saat masuk pending:

- notifikasi revisi belum lunas dikirim sekali melalui outbox idempoten per versi;
- scheduler memilih `submitted` biasa dan kedua sub-state;
- scheduler tidak mengirim empat tahap lama sekaligus. Pada satu run ia hanya boleh
  mengantrekan tahap terbaru yang sudah jatuh tempo dan belum tercatat untuk versi
  kini; run berikutnya tidak mengulang karena unique key;
- dashboard menandai overdue berdasarkan due date asli dan state kini;
- setelah Done, reminder versi itu berhenti; retry outbox yang sudah terlanjur
  diantrekan wajib mengecek state/version sebelum send dan membatalkan email basi.

### 6.4 Reporting

Row order current version menampilkan status belum lunas selama sub-state aktif.
Settlement historis tetap tersedia pada laporan/audit terpisah, tetapi tidak boleh
membuat current order dihitung sebagai pendapatan/pembayaran Done dua kali. Join
journal, proof, source snapshot, dan revisions harus diagregasi menjadi satu row
per order/version sebelum join order lines.

## 7. Backward compatibility dan rollout fail-closed

Urutan implementasi:

1. tambah enum/kolom nullable dan kolom histori typed secara additive;
2. backfill semua order lama dengan sub-state NULL; jangan mengarang histori yang
   tidak dapat dibuktikan;
3. deploy read path/API/UI/worker yang memahami sub-state dan gerbang LUNAS;
4. validasi CHECK/FK sebagai `NOT VALID` lalu `VALIDATE` setelah data lolos;
5. aktifkan endpoint revisi total berubah hanya setelah writer, renderer, outbox,
   reminder, reporting, dan reconciliation memakai kontrak baru;
6. uji nyata dilakukan di production dengan data berlabel `UJI-` hanya setelah QA
   dan gerbang implementasi mengizinkan; dokumen desain ini tidak mengubah data.

Consumer lama tetap menerima `status=submitted`, sehingga tidak menerbitkan LUNAS.
Namun writer lama dapat menghapus sub-state atau mengirim email salah; karena itu
mutasi revisi total berubah harus fail closed sampai seluruh writer/worker minimum
version aktif. Rollback aplikasi mematikan mutasi terkait dan mempertahankan read
path sub-state; rollback tidak boleh kembali ke renderer yang menganggap
verification historis terakhir sebagai pointer aktif.

## 8. Matriks pengujian wajib implementasi

### 8.1 State/FK/CHECK

1. total tetap: versi naik, Done/pointer lama tetap, tanpa journal/attempt baru;
2. total naik: status Submitted + underpayment, `done_at/done_*` NULL, histori lama
   utuh;
3. total turun: status Submitted + overpayment pending, pointer aktif NULL;
4. Done dengan sub-state atau pointer NULL ditolak;
5. pending dengan salah satu `done_*`/`done_at` non-NULL ditolak;
6. pemalsuan pointer attempt order/version/total lain ditolak FK;
7. `invoice_revisions.previous_done_*` menunjuk settlement asal yang tepat.
8. masing-masing cabang CHECK diuji negatif: Draft dengan status verifikasi/alasan
   selain `belum/NULL`; Submitted biasa dengan `cocok`, alasan tanpa
   `perlu_review`, atau waktu revisi; underpayment dengan alasan selain
   `kurang_bayar`; overpayment pending dengan alasan selain `lebih_bayar`; Done
   dengan sub-state/waktu revisi, `done_at` NULL, pointer parsial, breakdown negatif,
   total tidak cocok, atau `perlu_review`—semuanya ditolak;
9. FK histori revisi enam kolom menolak attempt dari order, versi, total, deposit,
   atau transfer yang berbeda; CHECK/FK `NOT VALID` menolak write baru sebelum
   validasi dan legacy invalid tetap tidak publishable.
10. `invoice_revisions` versi kontrak `0` hanya menerima ketujuh field settlement
    NULL; versi `1` mewajibkan transition dan keenam `previous_done_*` lengkap;
    versi lain, snapshot parsial, atau transition tanpa snapshot ditolak.

Reproduksi SQL minimum untuk celah three-valued logic wajib menjadi test migrasi.
Fixture `:submitted_order_id` harus menunjuk tepat satu order Submitted biasa;
statement berikut wajib gagal dengan SQLSTATE `23514`, bukan sukses karena hasil
CHECK `UNKNOWN`:

```sql
BEGIN;
UPDATE orders
SET revision_payment_state = NULL,
    revision_payment_started_at = now(),
    done_at = NULL,
    done_verification_log_id = NULL,
    done_order_version = NULL,
    done_deposit_amount = NULL,
    done_transfer_amount = NULL,
    verif_status = 'perlu_review',
    alasan_review = 'kurang_bayar'
WHERE id = :'submitted_order_id';
-- expected: ERROR 23514, constraint orders_settlement_state_consistent
ROLLBACK;
```

Harness repository mengulang pola yang sama untuk setiap cabang pada butir 8 dan
menegaskan row fixture ditemukan sebelum UPDATE. Kasus pointer NULL wajib mencakup
Done dengan keempat `done_*` NULL, Done dengan tepat satu `done_*` NULL, dan pending
dengan tepat satu `done_*` non-NULL; semuanya harus menghasilkan `23514`.

Reproduksi SQL minimum snapshot histori parsial juga wajib gagal sebelum FK dapat
melewati NULL dengan `MATCH SIMPLE`:

```sql
BEGIN;
INSERT INTO invoice_revisions (
  order_id, versi_lama, versi_baru, alasan, snapshot, delta_total, admin_id,
  settlement_contract_version, payment_transition,
  previous_done_verification_log_id, previous_done_order_version,
  previous_done_total, previous_done_deposit_amount,
  previous_done_transfer_amount, previous_done_at
) VALUES (
  :'done_order_id', 0, 1, 'UJI-snapshot-parsial', '{}'::jsonb, 1, :'admin_id',
  1, 'underpayment',
  :'verification_log_id', NULL,
  :'previous_total', :'previous_deposit',
  :'previous_transfer', :'previous_done_at'
);
-- expected: ERROR 23514,
-- constraint invoice_revisions_settlement_snapshot_consistent
ROLLBACK;
```

Test positif pasangan constraint wajib membuktikan: row legacy hasil backfill
`settlement_contract_version=0` dengan seluruh field baru NULL tetap valid; insert
versi `1` dengan snapshot lengkap dan FK tepat berhasil; snapshot lengkap tetapi
salah satu identitas order/version/total/breakdown salah gagal `23503`.

### 8.2 Race, retry, dan rollback

1. dua revisi paralel: tepat satu commit, satu version conflict;
2. revisi vs allocation/reversal: terserialisasi menurut lock global;
3. revisi turun dengan dua reversal plan paralel tidak over-reverse;
4. retry key sama tidak menggandakan revision, reversal, journal, audit, event,
   notifikasi, atau version increment;
5. key sama payload beda ditolak;
6. failure injection setelah reversal, setelah update total, setelah clear pointer,
   dan sebelum outbox: seluruh transaksi rollback ke Done lama;
7. settlement retry tidak menggandakan debit carry-forward, source snapshot,
   verification log, credit overpay, dokumen, atau email;
8. settlement vs reversal paralel tidak menghasilkan Done dari source yang stale.
9. dua settlement paralel proof yang sama hanya membuat satu split/journal kredit;
   retry setelah commit mengembalikan delta nol/hasil idempoten;
10. reversal overpay mengubah net tetapi tidak gross consumed proof; revisi naik
    setelah kredit tidak dapat double-apply proof lama.

### 8.3 Penolakan

1. total turun tanpa plan cukup ketika committed > total;
2. duplicate/cross-order/cross-branch allocation pada plan;
3. Manager mencoba credit overpay;
4. settlement kurang bayar, overpay tanpa keputusan, source pending, rekening D2
   tidak efektif, proof lintas cabang, atau expected version stale;
5. revisi kedua, cancel terselubung, endpoint customer terhadap order Done/pending,
   dan download/resend current invoice saat pending;
6. status/pointer invalid tidak boleh diperbaiki dengan fallback ke attempt lama.
7. Manager: toggle revisi mati ditolak; toggle aktif tetapi reversal dibutuhkan
   tanpa `admin.verify` ditolak; dengan kedua hak dapat merevisi turun memakai plan,
   tetapi tetap ditolak saat mencoba membuat overpay credit tanpa
   `admin.deposit_credit`.

### 8.4 Dokumen, email, reminder, dan kompatibilitas

1. pending tidak mempunyai invoice current version bertanda LUNAS;
2. invoice lama tetap byte-stable dan berlabel historis;
3. settlement final membuat tepat satu invoice current version dan outbox idempoten;
4. C&C revision pending mengirim satu notifikasi, reminder version-aware, tidak
   memburst semua tahap lama, dan berhenti setelah Done;
5. worker email membatalkan pesan basi bila version/state berubah;
6. consumer lama yang hanya mengenal status melihat Submitted, bukan Done;
7. reporting tidak menggandakan order line karena banyak attempt/source/journal;
8. browser E2E terautentikasi wajib diulang setelah fixture auth tersedia; hasil
   PG/frontend sebelumnya tidak dianggap pengganti E2E.
9. migrasi reminder menguji row versi terbukti, row ambigu yang pindah arsip dengan
   count/checksum sama, PK/NOT NULL final, reminder versi baru yang tidak terhalang
   arsip, retry scheduler, dan outbox basi yang dibatalkan worker.

## 9. Keputusan final dan risiko tersisa

Keputusan final C0:

- gunakan `orders.status=submitted` plus sub-state nullable
  `revision_underpayment`/`revision_overpayment_pending`;
- semua revisi total berubah selalu masuk pending;
- `done_at` dan seluruh pointer settlement aktif dikosongkan atomik;
- histori settlement asal disimpan immutable dan ber-FK typed di revision row;
- allocation aktif lintas versi di-carry-forward tanpa debit kedua;
- total turun membutuhkan reversal plan eksplisit bila committed melebihi total;
- transisi kembali ke Done hanya melalui settlement atomik versi kini;
- dokumen/email current version tidak boleh LUNAS sebelum commit tersebut;
- reminder C&C tetap memakai due date asli tetapi dedupe per versi.

Risiko tersisa yang bukan keputusan desain baru:

- browser E2E terautentikasi belum terverifikasi karena fixture vault;
- reuse proof lintas versi dan aggregate overpay memerlukan constraint/repository
  test yang kuat agar tidak menciptakan kredit kedua;
- worker lama adalah risiko rollback, sehingga aktivasi mutasi harus fail closed;
- revisi kedua saat pending belum didukung dan harus ditangani rekonsiliasi/manual,
  bukan dengan mengubah histori.

Tidak ada kode, migrasi, seed, saldo, rekening, order, atau data production yang
diubah oleh keputusan desain ini.
