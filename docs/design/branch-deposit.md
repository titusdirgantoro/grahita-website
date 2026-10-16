# D3 — Kontrak deposit cabang dan pembayaran campuran

Status: rancangan untuk review QA dan persetujuan tertulis CEO; belum boleh
diimplementasikan.

Kartu: `t_622a6b11`.

## 1. Tujuan dan batas lingkup

Dokumen ini menetapkan kontrak saldo deposit per cabang yang hanya berasal dari
dana terverifikasi, dapat dialokasikan bertahap ke beberapa pesanan, dan dapat
digabung dengan transfer baru pada satu pesanan.

Rujukan utama:

- `docs/PRD.md` §3.5, §3.7, §4.2, §5, serta BR-07, BR-08, dan BR-10;
- `docs/Architecture.md` §2.2, §6, dan §9;
- `docs/SystemDesign.md` §2–§6 dan §9;
- `docs/Database.md` §1, §4.4, §4.5, §4.7, dan §8;
- `docs/design/branch-bank-allowlist.md` sebagai kontrak D2 rekening efektif;
- `docs/design/branch-deposit-revision-state.md` sebagai delta normatif C0 untuk
  revisi order Done yang totalnya berubah dan gerbang LUNAS;
- kontrak F1/F2 yang mempertahankan satu pintu transisi Done melalui
  `payment.MarkVerified`;
- kontrak F3-30 tetap tidak berubah dan Virtual Account tetap OFF.

Lingkup rancangan:

1. bukti top-up deposit yang terpisah dari bukti pembayaran order;
2. journal deposit immutable per cabang;
3. alokasi parsial deposit ke beberapa order dan pembayaran campuran;
4. verifikasi manual top-up, transfer order, dan keputusan kelebihan bayar;
5. konkurensi, idempotensi, reversal, audit, API, UI, migrasi, dan pengujian.

Di luar lingkup kartu D3:

- menjalankan migrasi, mengubah kode, data, konfigurasi, atau saldo nyata;
- impor atau pemberian saldo awal produksi;
- transfer antar-cabang;
- penarikan/refund dana ke rekening cabang;
- bunga, biaya administrasi, kedaluwarsa saldo, atau multi-currency;
- integrasi Virtual Account atau perubahan kontrak F3-30;
- keputusan bisnis pembatalan order Submitted/Done yang masih terbuka di PRD;
- rekonsiliasi bank otomatis. Semua kredit tetap membutuhkan keputusan admin.

## 2. Keputusan normatif dan istilah

Kata HARUS, TIDAK BOLEH, dan BOLEH bersifat normatif.

### 2.1 Istilah

- **Top-up**: transfer yang tujuan utamanya menambah deposit cabang dan tidak
  melekat pada satu order.
- **Transfer order**: bukti transfer baru yang melekat pada satu order.
- **Kredit deposit**: transaksi journal positif yang telah disetujui admin.
- **Debit deposit**: transaksi journal negatif karena deposit dialokasikan ke
  order.
- **Reversal**: transaksi journal baru yang membalik transaksi sebelumnya tanpa
  mengubah atau menghapus baris lama.
- **Alokasi aktif**: bagian deposit yang sedang diperhitungkan bagi versi
  pembayaran order dan belum dibalik.
- **Pembayaran campuran**: total order dipenuhi oleh alokasi deposit ditambah
  transfer order terverifikasi.
- **Overpay**: transfer order terverifikasi melebihi sisa yang harus dibayar
  setelah alokasi deposit.
- **Sumber dana terverifikasi**: top-up atau transfer order yang bukti, nominal,
  rekening tujuan, cabang, dan keputusan adminnya telah lolos kontrak ini.
- **Saldo tersedia**: jumlah kredit dikurangi debit journal committed untuk satu
  cabang. Dana pending tidak termasuk.

### 2.2 Keputusan mengikat

1. Saldo dimiliki satu cabang. Saldo, bukti, kredit, debit, dan alokasi tidak
   boleh dipindah atau dipakai lintas cabang.
2. Top-up pending/review/rejected bukan saldo dan tidak dapat dialokasikan.
3. Hanya admin ber-capability `admin.verify` yang boleh memverifikasi bukti.
   Hanya Superadmin ber-capability baru `admin.deposit_credit` yang boleh:
   - mengkredit top-up ke deposit; dan
   - mengkredit selisih overpay transfer order ke deposit.
   Manager boleh menilai bukti/order tetapi tidak dapat menciptakan kredit.
4. Kredit overpay tidak otomatis. Setelah bukti transfer order diverifikasi,
   Superadmin harus memilih keputusan eksplisit `credit_to_deposit` atau keputusan
   non-deposit yang kelak disepakati. Sampai keputusan selesai, order tetap
   `Submitted/perlu_review` dan tidak menjadi Done.
5. Satu bukti hanya mempunyai satu tujuan ekonomi final: top-up deposit ATAU
   transfer order. File yang sama tidak boleh menghasilkan kredit/debit ganda.
6. Satu order boleh memakai nol atau satu total alokasi deposit aktif, yang dapat
   tersusun dari beberapa baris alokasi journal, ditambah nol atau beberapa bukti
   transfer order.
7. Pada titik Done berlaku:

   ```text
   verified_deposit_amount + verified_order_transfer_amount = order.total
   ```

   Overpay yang secara eksplisit dikreditkan ke deposit tidak dihitung sebagai
   pembayaran order:

   ```text
   sum(verified proof nominal)
     = verified_order_transfer_amount + credited_overpay_amount
   ```

8. Semua uang adalah `BIGINT` Rupiah bulat, tanpa nilai negatif atau pecahan.
9. Rekening tujuan setiap top-up/transfer harus efektif untuk cabang pada titik
   keputusan sesuai D2: assignment allowed, rekening aktif, dan cabang aktif.
   Admin tidak memiliki override lintas cabang atau rekening nonaktif.
10. Mutasi ekonomi memakai journal append-only. Koreksi dilakukan dengan reversal
    dan transaksi pengganti, bukan UPDATE nominal atau DELETE histori.
11. Saldo tersedia tidak boleh negatif pada commit, termasuk saat dua request
    paralel memakai saldo yang sama.
12. Transisi Done tetap melalui `payment.MarkVerified`; jalur deposit tidak boleh
    membuat pintu Done kedua.
13. Harga dan total yang dipakai settlement adalah snapshot order/invoice versi
    terkait, bukan harga master terkini.
14. OCR top-up hanya advisory. OCR tidak pernah mengkredit deposit atau membuat
    order Done otomatis.
15. VA tetap OFF. Tidak ada callback, tabel, route, atau metode VA baru dari D3.
16. Implementasi hanya boleh dimulai setelah QA menyetujui spesifikasi ini dan
    CEO memberi persetujuan tertulis atas §16.

## 3. State dan alur bisnis

### 3.1 State top-up

```text
uploaded -> pending_review -> verified
                          \-> rejected
                          \-> needs_review
needs_review -> verified | rejected
verified -> reversed (melalui journal reversal; bukti/log lama tetap immutable)
```

`verified` berarti bukti mempunyai keputusan admin final dan kredit journal yang
lahir atomik. Tidak boleh ada state `verified` tanpa transaksi kredit atau kredit
tanpa keputusan verifikasi.

Alasan `needs_review` minimal:

- nominal tidak terbaca atau berbeda dari nominal yang dinyatakan;
- rekening tidak terbaca, ambigu, nonaktif, atau tidak diizinkan;
- file duplikat;
- tanggal tidak masuk akal;
- cabang/rekening sumber data tidak konsisten;
- indikasi bukti yang sama pernah dipakai untuk order atau top-up lain.

`rejected` adalah keputusan manusia final atas attempt tersebut. Bila cabang
mengunggah bukti baru, sistem membuat top-up baru; baris lama tidak dipakai ulang.

### 3.2 State settlement order

State order utama tetap `draft -> submitted -> done`. Deposit menambah rincian
settlement pada order Submitted:

```text
unfunded
  -> partially_funded
  -> funded_pending_verification
  -> verified (order menjadi Done melalui MarkVerified)

setiap state non-Done dapat -> needs_review
```

State settlement adalah turunan, bukan enum order baru:

- `deposit_allocated`: jumlah alokasi deposit aktif;
- `transfer_pending`: total bukti yang belum diputuskan;
- `transfer_verified_for_order`: nominal transfer yang dipakai ke order;
- `overpay_pending`: nominal lebih yang menunggu keputusan;
- `remaining_due = max(order_total - deposit_allocated -
  transfer_verified_for_order, 0)`.

UI tidak boleh menganggap bukti pending sebagai pembayaran terverifikasi.

### 3.3 Top-up deposit

1. User Cabang memilih rekening efektif D2 dan mengunggah bukti top-up tersendiri.
2. Request menyertakan nominal yang dinyatakan cabang dan idempotency key.
3. Backend mengambil `branch_id` dari sesi; klien tidak dapat memilih cabang.
4. File disimpan dengan hash; top-up dibuat `pending_review`. Tidak ada journal.
5. OCR, bila AI aktif, boleh mengisi saran nominal/tanggal/bank/digit akhir dan
   status advisory. OCR tidak memanggil `MarkVerified` dan tidak mengubah saldo.
6. Admin meninjau bukti dan memetakan rekening tujuan efektif. Manager dapat
   menandai review/reject, tetapi aksi kredit membutuhkan Superadmin.
7. Superadmin menyetujui nominal kredit final. Dalam satu transaksi sistem
   memvalidasi ulang rekening D2, mengunci top-up, menulis verification attempt,
   menulis satu kredit journal, audit, dan event. Setelah commit saldo bertambah.
8. Nominal kredit final boleh berbeda dari nominal yang dinyatakan/OCR, tetapi
   perbedaan membutuhkan alasan admin dan ditampilkan eksplisit.

Nominal top-up tidak harus sama dengan order mana pun.

### 3.4 Alokasi deposit ke order

Cabang boleh memilih jumlah deposit ketika order masih Draft atau Submitted,
namun konsekuensi uang baru terjadi saat endpoint alokasi menulis debit journal:

- Draft hanya boleh menyimpan **intent** non-finansial. Intent tidak menahan saldo
  dan dihitung ulang ketika submit.
- Saat submit, atau sesudah submit lewat endpoint khusus, service mengalokasikan
  nilai final di bawah lock saldo. Bila saldo berubah sejak layar dibuka, request
  gagal `DEPOSIT_BALANCE_INSUFFICIENT`; sistem tidak mengurangi nilai diam-diam.
- Jumlah alokasi harus `> 0`, `<= remaining_order_total`, dan `<= available_balance`.
- Alokasi boleh parsial. Saldo tersisa dapat dipakai order lain.
- Penambahan alokasi kedua pada order yang sama boleh dilakukan sebagai baris
  baru; aggregate aktif tetap tidak boleh melebihi total order.
- Pengurangan/pembatalan alokasi dilakukan dengan reversal penuh atau parsial
  yang menunjuk alokasi asal. Tidak mengubah nominal alokasi lama.

Deposit yang telah dialokasikan langsung menjadi debit committed, bukan sekadar
reservation terpisah. Ini membuat saldo tersedia tunggal dan mencegah double-use.
Order belum Done tetap memiliki hak atas debit tersebut sampai dibalik.

### 3.5 Pembayaran campuran

Untuk total order `T`, alokasi deposit terverifikasi `D`, dan transfer order
terverifikasi yang dipakai `P`:

```text
0 <= D <= T
0 <= P <= T - D
D + P = T pada keputusan Done
```

Admin harus melihat rincian sumber, tidak hanya aggregate:

- setiap alokasi deposit aktif dan journal debitnya;
- setiap bukti transfer, nominal terbaca, nominal disetujui, dan rekening tujuan;
- bagian setiap transfer yang dipakai untuk order;
- selisih kurang/lebih;
- keputusan overpay bila ada.

Contoh:

- total order Rp1.000.000;
- saldo tersedia awal Rp700.000;
- cabang mengalokasikan Rp400.000 (`D=400.000`), saldo tersisa Rp300.000;
- bukti transfer Rp600.000 diverifikasi untuk order (`P=600.000`);
- `D+P=1.000.000`, order dapat Done.

Contoh parsial lintas order:

- saldo awal Rp1.000.000;
- Order A debit Rp250.000, Order B debit Rp300.000;
- saldo tersedia Rp450.000;
- kedua order menyimpan allocation/journal berbeda yang menunjuk cabang sama;
- reversal Rp100.000 dari Order B membuat kredit reversal Rp100.000 dan saldo
  tersedia Rp550.000, tanpa menghapus debit awal.

### 3.6 Kurang bayar

Jika `D + P < T`, order tetap Submitted dan `perlu_review/kurang_bayar`. Cabang
boleh:

- menambah alokasi deposit bila saldo tersedia;
- mengunggah transfer order tambahan;
- memakai gabungan keduanya.

Attempt lama tetap immutable. Attempt baru menghitung ulang seluruh sumber aktif.

### 3.7 Lebih bayar

Misal `T=1.000.000`, `D=200.000`, dan bukti terverifikasi berjumlah Rp900.000.
Kebutuhan transfer order hanya Rp800.000; overpay Rp100.000.

Aturan:

1. Sistem menandai `perlu_review/lebih_bayar`; tidak auto-Done dan tidak auto-credit.
2. Superadmin melihat split yang diusulkan:
   - Rp800.000 untuk order;
   - Rp100.000 kandidat kredit deposit.
3. Aksi `credit_to_deposit` menulis dalam satu transaksi:
   - keputusan split final;
   - nilai Rp800.000 sebagai bagian transfer order;
   - kredit deposit Rp100.000 bersumber dari bukti order;
   - audit dan verification log;
   - lalu `MarkVerified` bila semua invariant terpenuhi.
4. Retry aksi dengan idempotency key sama mengembalikan hasil yang sama, tanpa
   kredit atau Done kedua.
5. Aksi dengan key berbeda terhadap overpay yang sudah diputuskan ditolak
   `OVERPAY_ALREADY_RESOLVED`.

Pilihan selain kredit deposit—misalnya refund manual atau menahan dana di luar
sistem—belum disepakati. Sampai keputusan §16 dibuat, implementasi pertama hanya
boleh menyediakan `credit_to_deposit`; jika Superadmin tidak memilihnya, order
harus tetap review. Sistem tidak boleh mengarang write-off atau refund.

### 3.8 Cash & Carry

- C&C tetap boleh submit tanpa bukti dan tanpa alokasi deposit.
- Bila deposit cukup, cabang boleh mengalokasikan sebagian/seluruhnya setelah
  submit; hal itu tidak otomatis Done.
- C&C hanya Done setelah admin memverifikasi seluruh settlement melalui pintu
  yang sama.
- Reminder memakai `remaining_due` settlement. Bila `remaining_due=0` tetapi
  admin belum menyelesaikan verifikasi, reminder uang dapat dihentikan, namun
  dashboard tetap menampilkan `menunggu verifikasi`.
- Reversal yang membuka kekurangan pada C&C membuat `remaining_due` naik kembali
  dan reminder berikutnya kembali eligible sesuai jadwal; email lama tidak
  ditarik.

## 4. Model data target

Bagian ini adalah desain migrasi masa depan, bukan SQL yang dijalankan kartu D3.

### 4.1 `branch_deposit_accounts`

Satu row agregat per cabang dipakai sebagai lock anchor dan cache saldo:

```sql
CREATE TABLE branch_deposit_accounts (
    branch_id          uuid PRIMARY KEY
        REFERENCES branches(id) ON DELETE RESTRICT,
    balance            bigint NOT NULL DEFAULT 0 CHECK (balance >= 0),
    journal_version    bigint NOT NULL DEFAULT 0 CHECK (journal_version >= 0),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
```

`balance` adalah cache yang diperbarui atomik bersama journal, bukan sumber
kebenaran independen. Sumber kebenaran audit adalah jumlah journal posted:

```text
balance = SUM(deposit_transactions.amount)
```

`amount` bertanda: kredit positif dan debit negatif. Reconciliation job wajib
membandingkan cache dan SUM; mismatch adalah invariant violation yang memblokir
mutasi account sampai direkonsiliasi, bukan dibetulkan diam-diam.

### 4.2 `deposit_topups`

```sql
CREATE TYPE deposit_topup_status AS ENUM
  ('pending_review','needs_review','verified','rejected','reversed');

CREATE TABLE deposit_topups (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id             uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
    bank_account_id       uuid NOT NULL REFERENCES bank_accounts(id) ON DELETE RESTRICT,
    status                deposit_topup_status NOT NULL DEFAULT 'pending_review',
    declared_amount       bigint NOT NULL CHECK (declared_amount > 0),
    verified_amount       bigint CHECK (verified_amount > 0),
    payment_proof_id      uuid NOT NULL UNIQUE
        REFERENCES payment_proofs(id) ON DELETE RESTRICT,
    verified_by           uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    verified_at           timestamptz,
    rejection_reason      text,
    created_by_kind       actor_kind NOT NULL,
    created_by_id         uuid,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT deposit_topups_verified_fields CHECK (
      (status IN ('verified','reversed') AND verified_amount IS NOT NULL
       AND verified_by IS NOT NULL AND verified_at IS NOT NULL)
      OR
      (status NOT IN ('verified','reversed') AND verified_amount IS NULL
       AND verified_by IS NULL AND verified_at IS NULL)
    )
);
```

Status lifecycle boleh berubah, tetapi keputusan ekonomi tidak disimpan hanya di
row mutable ini. Setiap attempt ada di `deposit_verification_logs`, dan kredit ada
di journal immutable.

### 4.3 Generalisasi `payment_proofs`

Bukti harus mempunyai tepat satu owner ekonomi:

```sql
CREATE TYPE payment_proof_purpose AS ENUM ('order_payment','deposit_topup');

ALTER TABLE payment_proofs
  ADD COLUMN purpose payment_proof_purpose NOT NULL DEFAULT 'order_payment',
  ALTER COLUMN order_id DROP NOT NULL,
  ADD COLUMN branch_id uuid REFERENCES branches(id) ON DELETE RESTRICT,
  ADD CONSTRAINT payment_proofs_owner_exactly_one CHECK (
    (purpose='order_payment' AND order_id IS NOT NULL) OR
    (purpose='deposit_topup' AND order_id IS NULL AND branch_id IS NOT NULL)
  );
```

Migrasi harus backfill seluruh bukti lama sebagai `order_payment` dan menurunkan
`branch_id` dari order sebelum constraint diperketat. Service tetap memverifikasi
bahwa `branch_id` bukti order sama dengan cabang order.

Duplikasi file dicegah dua lapis:

- unique `(branch_id, sha256)` untuk bukti top-up;
- unique `(order_id, sha256)` untuk bukti order;
- sebelum kredit/Done, lookup global terhadap hash yang sama di cabang tersebut.
  Jika file sudah menjadi sumber transaksi ekonomi lain, fail closed
  `PAYMENT_PROOF_ALREADY_USED`. Hash sama di cabang lain tidak mengungkap detail,
  tetapi tetap masuk review keamanan dan tidak boleh lolos otomatis.

Karena PostgreSQL unique constraint dengan NULL memerlukan perhatian, gunakan
partial unique index per purpose, bukan mengandalkan unique lama saja.

### 4.4 `deposit_verification_logs`

```sql
CREATE TABLE deposit_verification_logs (
    id                     uuid PRIMARY KEY DEFAULT uuidv7(),
    topup_id               uuid NOT NULL REFERENCES deposit_topups(id) ON DELETE RESTRICT,
    method                 verify_method NOT NULL,
    result                 text NOT NULL,
    declared_amount        bigint NOT NULL,
    observed_amount        bigint,
    approved_amount        bigint,
    bank_account_id        uuid REFERENCES bank_accounts(id) ON DELETE RESTRICT,
    branch_allowlist_version bigint NOT NULL,
    destination_result     jsonb NOT NULL,
    admin_id               uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    reason                 text,
    created_at             timestamptz NOT NULL DEFAULT now()
);
```

Log append-only. OCR menulis result advisory/review; hanya log manual Superadmin
`verified` yang dapat menjadi `source_verification_id` kredit top-up.
`destination_result` mengikuti snapshot D2 (bank, owner, digit akhir; tanpa nomor
lengkap).

### 4.5 `deposit_transactions` — journal immutable

```sql
CREATE TYPE deposit_transaction_kind AS ENUM (
  'topup_credit',
  'order_allocation_debit',
  'overpay_credit',
  'allocation_reversal_credit',
  'topup_reversal_debit',
  'overpay_reversal_debit'
);

CREATE TABLE deposit_transactions (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id                uuid NOT NULL
        REFERENCES branches(id) ON DELETE RESTRICT,
    kind                     deposit_transaction_kind NOT NULL,
    amount                   bigint NOT NULL CHECK (amount <> 0),
    source_proof_id          uuid REFERENCES payment_proofs(id) ON DELETE RESTRICT,
    source_verification_id   uuid,
    order_id                 uuid REFERENCES orders(id) ON DELETE RESTRICT,
    order_version            int,
    reverses_transaction_id  uuid
        REFERENCES deposit_transactions(id) ON DELETE RESTRICT,
    idempotency_key          text NOT NULL,
    actor_kind               actor_kind NOT NULL,
    actor_id                 uuid,
    reason                   text,
    created_at               timestamptz NOT NULL DEFAULT now(),
    UNIQUE (branch_id, idempotency_key),
    CONSTRAINT deposit_transaction_sign CHECK (
      (kind IN ('topup_credit','overpay_credit','allocation_reversal_credit')
       AND amount > 0)
      OR
      (kind IN ('order_allocation_debit','topup_reversal_debit',
                'overpay_reversal_debit') AND amount < 0)
    ),
    CONSTRAINT deposit_transaction_order_fields CHECK (
      (kind IN ('order_allocation_debit','allocation_reversal_credit',
                'overpay_credit','overpay_reversal_debit')
       AND order_id IS NOT NULL AND order_version IS NOT NULL)
      OR
      (kind IN ('topup_credit','topup_reversal_debit'))
    )
);

CREATE INDEX deposit_transactions_reversal_source_idx
  ON deposit_transactions (reverses_transaction_id, created_at, id)
  WHERE reverses_transaction_id IS NOT NULL;
```

Aturan tambahan service/trigger:

- row tidak boleh di-UPDATE atau DELETE oleh role aplikasi;
- `reverses_transaction_id` adalah relasi banyak-ke-satu: satu transaksi ekonomi
  asal boleh mempunyai beberapa row reversal parsial. Kolom ini sengaja **tidak
  UNIQUE**; index non-unique di atas dipakai untuk lock/aggregate dan penelusuran;
- transaksi non-reversal wajib mempunyai `reverses_transaction_id IS NULL`;
  transaksi reversal wajib menunjuk langsung transaksi asal non-reversal dengan
  pasangan kind yang sah (`order_allocation_debit` ->
  `allocation_reversal_credit`, `topup_credit` -> `topup_reversal_debit`, dan
  `overpay_credit` -> `overpay_reversal_debit`). Reversal atas reversal dan rantai
  reversal tidak diizinkan;
- setiap reversal harus sama `branch_id`, berlawanan tanda, dan memenuhi invariant
  aggregate setelah source dikunci:

  ```text
  SUM(ABS(amount) WHERE reverses_transaction_id = source.id)
    <= ABS(source.amount)
  ```

  Dengan demikian beberapa reversal parsial sah, tetapi total seluruh reversal
  committed tidak dapat melebihi transaksi asal;
- kredit top-up tepat satu per `source_verification_id` sukses;
- kredit overpay tepat satu per keputusan split;
- transaksi bersumber bukti harus mempunyai cabang yang sama dengan bukti;
- transaksi order harus mempunyai cabang yang sama dengan order;
- `order_version` mematok snapshot total yang diselesaikan;
- `reason` wajib untuk reversal dan koreksi admin.

FK `source_verification_id` secara implementasi dapat berupa dua kolom typed
(`deposit_verification_log_id` dan `order_verification_log_id`) dengan CHECK
exactly-one, bukan polymorphic FK. Bentuk final harus menjaga FK nyata; UUID tanpa
FK tidak diterima.

### 4.6 `deposit_allocations`

```sql
CREATE TABLE deposit_allocations (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id                uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
    order_id                 uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    order_version            int NOT NULL,
    debit_transaction_id     uuid NOT NULL UNIQUE
        REFERENCES deposit_transactions(id) ON DELETE RESTRICT,
    amount                   bigint NOT NULL CHECK (amount > 0),
    reversed_amount          bigint NOT NULL DEFAULT 0
        CHECK (reversed_amount >= 0 AND reversed_amount <= amount),
    created_by_kind          actor_kind NOT NULL,
    created_by_id            uuid,
    created_at               timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, branch_id, order_id, order_version),
    CONSTRAINT allocation_amount_matches_debit
      CHECK (amount > 0)
);
```

`reversed_amount` adalah cache lifecycle yang bertambah atomik setiap reversal;
histori dan sumber kebenaran nilai reversal tetap berasal dari SUM journal yang
menunjuk `debit_transaction_id`. Update wajib berbentuk compare-and-increment di
bawah lock allocation/source dan gagal bila `reversed_amount + requested > amount`.
Reconciliation membandingkan cache dengan aggregate journal; mismatch memblokir
mutasi. Kontrak D3 memilih model ini agar tidak ada ambiguitas implementasi.

Alokasi aktif:

```text
active_amount = amount - reversed_amount
```

`order_version` pada allocation adalah versi ketika debit dibuat, bukan pembatas
masa pakai debit. Allocation aktif dari versi lama tetap merupakan dana committed
untuk order yang sama sampai direversal. Karena itu aggregate operasional selalu
melintasi seluruh versi order:

```text
committed_deposit(order_id)
  = SUM(amount - reversed_amount untuk seluruh allocation order_id)

committed_deposit(order_id) <= orders.total
remaining_deposit_capacity = orders.total - committed_deposit(order_id)
```

Invariant ini diperiksa di bawah lock order/account dan seluruh allocation order;
query yang hanya memfilter `allocation.order_version = orders.versi` dilarang.
Versi tetap disimpan untuk audit dan untuk membuktikan kapan debit dibuat.

### 4.7 Split transfer order

`payment_proofs.ocr_nominal` bukan keputusan keuangan. Tambahkan detail keputusan
per bukti pada verification attempt, misalnya:

```json
{
  "payment_proof_id": "<uuid>",
  "approved_nominal": 900000,
  "applied_to_order": 800000,
  "credited_to_deposit": 100000,
  "destination": {
    "status": "allowed",
    "bank_account_id": "<uuid>",
    "bank_snapshot": "BCA",
    "account_owner_snapshot": "PT Contoh",
    "account_last_digits_snapshot": "1234"
  }
}
```

Per bukti wajib:

```text
approved_nominal = applied_to_order + credited_to_deposit
```

Semua nilai nonnegatif. Aggregate `credited_to_deposit` harus sama dengan kredit
journal overpay yang menunjuk attempt/bukti tersebut.

### 4.8 Snapshot settlement pada `orders`

Tambahkan pointer dan aggregate untuk query cepat, tetapi histori rinci tetap
berasal dari attempt/journal:

```sql
ALTER TABLE verification_logs
  ADD COLUMN order_version int,
  ADD COLUMN deposit_amount bigint,
  ADD COLUMN transfer_amount bigint,
  ADD CONSTRAINT verification_logs_payment_breakdown CHECK (
    (deposit_amount IS NULL AND transfer_amount IS NULL)
    OR
    (deposit_amount >= 0 AND transfer_amount >= 0
     AND deposit_amount + transfer_amount = total_pesanan)
  );

-- Setelah backfill tervalidasi, order_version dibuat NOT NULL dan key komposit
-- ini menjadi target FK agar pointer Done tidak dapat menunjuk attempt milik
-- order atau versi lain.
ALTER TABLE verification_logs
  ADD CONSTRAINT verification_logs_settlement_identity
      UNIQUE (id, order_id, order_version, total_pesanan),
  ADD CONSTRAINT verification_logs_settlement_breakdown
      UNIQUE (
        id, order_id, order_version, total_pesanan,
        deposit_amount, transfer_amount
      );

CREATE TABLE verification_deposit_sources (
    verification_log_id      uuid NOT NULL,
    order_id                 uuid NOT NULL,
    settlement_order_version int NOT NULL,
    settlement_total         bigint NOT NULL,
    allocation_id            uuid NOT NULL,
    branch_id                 uuid NOT NULL,
    allocation_order_version int NOT NULL,
    amount_snapshot           bigint NOT NULL CHECK (amount_snapshot > 0),
    created_at                timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (verification_log_id, allocation_id),
    FOREIGN KEY (
      verification_log_id, order_id, settlement_order_version, settlement_total
    ) REFERENCES verification_logs
        (id, order_id, order_version, total_pesanan) ON DELETE RESTRICT,
    FOREIGN KEY (
      allocation_id, branch_id, order_id, allocation_order_version
    ) REFERENCES deposit_allocations
        (id, branch_id, order_id, order_version) ON DELETE RESTRICT,
    CHECK (allocation_order_version <= settlement_order_version)
);

ALTER TABLE orders
  ADD COLUMN done_verification_log_id uuid,
  ADD COLUMN done_order_version int,
  ADD COLUMN done_deposit_amount bigint,
  ADD COLUMN done_transfer_amount bigint,
  ADD CONSTRAINT orders_done_verification_attempt_fk
      FOREIGN KEY (
        done_verification_log_id, id, done_order_version, total,
        done_deposit_amount, done_transfer_amount
      )
      REFERENCES verification_logs
          (id, order_id, order_version, total_pesanan,
           deposit_amount, transfer_amount)
      ON DELETE RESTRICT,
  ADD CONSTRAINT orders_done_payment_pointer_all_or_none CHECK (
    (done_verification_log_id IS NULL
     AND done_order_version IS NULL
     AND done_deposit_amount IS NULL
     AND done_transfer_amount IS NULL)
    OR
    (done_verification_log_id IS NOT NULL
     AND done_order_version IS NOT NULL
     AND done_deposit_amount IS NOT NULL
     AND done_transfer_amount IS NOT NULL
     AND done_order_version BETWEEN 0 AND versi
     AND done_deposit_amount >= 0
     AND done_transfer_amount >= 0)
  ),
  ADD CONSTRAINT orders_done_payment_breakdown CHECK (
    (status = 'done'
     AND done_verification_log_id IS NOT NULL
     AND done_deposit_amount + done_transfer_amount = total)
    OR
    (status <> 'done'
     AND done_verification_log_id IS NULL
     AND done_order_version IS NULL
     AND done_deposit_amount IS NULL
     AND done_transfer_amount IS NULL)
  );
```

Transaksi Done mengisi seluruh field sekaligus. Dokumen, laporan, dan render ulang
memakai snapshot attempt Done, bukan menghitung ulang saldo kini.
Attempt yang bukan settlement sukses boleh menyimpan kedua nilai breakdown NULL;
attempt yang menjadi pointer Done wajib mempunyai keduanya non-NULL melalui FK
komposit di atas.

`verification_deposit_sources` adalah snapshot immutable pemetaan allocation ke
attempt settlement, bukan journal baru. Satu allocation boleh muncul pada banyak
attempt historis order yang sama (carry-forward), tetapi hanya sekali per attempt.
Saat membuat attempt, service mengunci seluruh allocation order menurut UUID dan
wajib menulis tepat satu row untuk **setiap** allocation yang `active_amount > 0`,
termasuk allocation versi lama. `amount_snapshot` wajib sama dengan
`amount - reversed_amount` pada saat lock; allocation yang sudah habis direversal
tidak boleh masuk. Invariant attempt:

```text
SUM(verification_deposit_sources.amount_snapshot untuk attempt)
  = verification_logs.deposit_amount
  = orders.done_deposit_amount
```

Daftar yang menghilangkan allocation aktif, menggandakannya, memakai allocation
order/cabang lain, atau mengambil allocation dari versi setelah settlement harus
ditolak. Constraint lintas-row/SUM ini ditegakkan service dalam transaksi dan
direkonsiliasi; role aplikasi tidak mempunyai UPDATE/DELETE pada tabel snapshot.

`done_order_version` adalah versi settlement yang ditunjuk, bukan selalu versi
dokumen terkini. Nilainya boleh lebih kecil dari `orders.versi` hanya ketika
revisi berikutnya tidak mengubah `total`; FK komposit tetap membuktikan bahwa
attempt itu milik order yang sama, benar-benar dibuat untuk versi tersebut, dan
mempunyai `total_pesanan` yang sama dengan total order kini.

Keempat field `done_*` adalah **pointer settlement aktif**, bukan arsip pointer.
Ketika revisi mengubah total dan belum mempunyai settlement pengganti, satu
transaksi wajib mengikuti lock global branch/account/order/allocations, menjalankan
reversal plan eksplisit bila total baru lebih kecil daripada committed deposit,
memindahkan `status` dari `done` ke `submitted` dengan sub-state C0
`revision_underpayment` atau `revision_overpayment_pending`, menaikkan
versi/mengubah total, dan menyetel `done_at` serta seluruh field `done_*` ke `NULL`.
Dengan demikian FK tidak dipaksa mencocokkan attempt lama dengan total baru,
sementara CHECK melarang pointer aktif tersisa pada order yang tidak `done`.

Mengosongkan pointer aktif **tidak menghapus histori**: attempt lama tetap
immutable di `verification_logs` dengan `order_id`, `order_version`, total, dan
breakdown asal; journal/alokasi lama tetap immutable; `audit_logs.before/after`
mencatat UUID pointer yang dilepas serta alasan revisi. Query timeline histori
berdasarkan `order_id`, bukan hanya pointer aktif pada `orders`. Setelah delta
selesai, transaksi settlement baru membuat attempt untuk versi/total terkini dan
mengisi `done_at` serta keempat field `done_*` bersamaan dengan status `done`.
Implementasi awal memilih periode pending wajib; endpoint revisi tidak mempunyai
shortcut langsung Done. Kontrak lengkap berada di delta C0.

Backfill `verification_logs.order_version` wajib memakai snapshot versi yang
dapat dibuktikan dari histori order/invoice. Attempt legacy yang terbukti
transfer-only boleh diisi `deposit_amount=0` dan `transfer_amount=total_pesanan`;
karena belum ada allocation D3, source row-nya kosong. Row legacy yang versi atau
breakdown-nya ambigu tidak boleh dijadikan `done_verification_log_id`; order
tersebut masuk rekonsiliasi dan fitur D3 tetap fail closed. Urutan constraint
mengikuti §11: tambah nullable, backfill, validasi, baru `NOT NULL` yang relevan,
unique, FK, dan CHECK.

### 4.9 Audit

`audit_logs` mencatat aksi, bukan menjadi ledger:

- `deposit_topup/upload`;
- `deposit_topup/review|verify|reject|reverse`;
- `deposit/allocation_create|allocation_reverse`;
- `order_payment/overpay_credit`;
- `order/verify_mixed_payment`.

`before/after` boleh memuat UUID, nominal, version, status, dan alasan; tidak boleh
memuat file, OCR raw, nomor rekening penuh, cookie, CSRF, atau idempotency key
utuh. Audit ditulis dalam transaksi yang sama dengan mutasi ekonomi.

## 5. Transaksi, lock, dan idempotensi

### 5.1 Urutan lock global

Semua mutasi deposit memakai urutan konsisten:

1. lock `branches` target `FOR SHARE` agar linearizable terhadap D2/status cabang;
2. lock `branch_deposit_accounts` target `FOR UPDATE`;
3. lock `orders` target `FOR UPDATE` bila ada, urut UUID bila lebih dari satu;
4. lock `deposit_topups`/`deposit_allocations`/verification target `FOR UPDATE`
   menurut UUID;
5. baca ulang rekening efektif D2 dan seluruh invariant;
6. insert journal + update cache/account + audit/event;
7. commit.

Mutasi kebijakan D2 memegang branch `FOR UPDATE`, sehingga keputusan kredit yang
memegang branch `FOR SHARE` tidak dapat melihat keadaan setengah berubah. D3 tidak
mengambil row lock bank setelah memegang branch; query efektif mengikuti pola D2
untuk menghindari deadlock.

### 5.2 Verifikasi top-up

```text
BEGIN
  lock branch FOR SHARE
  lock deposit account FOR UPDATE
  lock topup FOR UPDATE
  claim idempotency key
  pastikan pending/review dan belum punya kredit
  validasi ulang proof owner/hash dan rekening efektif D2
  insert deposit_verification_log manual verified
  insert deposit_transaction topup_credit (+amount)
  update account SET balance=balance+amount, version=version+1
  update topup verified
  insert audit + event
COMMIT
```

Jika retry menemukan idempotency key dan request fingerprint sama, kembalikan
hasil tersimpan. Key sama dengan payload berbeda menghasilkan
`IDEMPOTENCY_KEY_REUSED`.

### 5.3 Alokasi paralel

```text
BEGIN
  lock branch FOR SHARE
  lock deposit account FOR UPDATE
  lock order FOR UPDATE
  claim idempotency key
  pastikan order submitted (termasuk sub-state revisi pembayaran C0 yang sah)
  hitung saldo dan committed_deposit order dari allocation aktif seluruh versi
  validasi amount <= balance dan <= (order total - committed_deposit)
  insert debit transaction (-amount)
  insert allocation
  update account SET balance=balance-amount, version=version+1
  insert audit + order event
COMMIT
```

Contoh race: saldo Rp500.000, request A Rp400.000 dan B Rp300.000. Keduanya
menunggu row account yang sama. Tepat satu boleh sukses; request kedua membaca
saldo setelah commit pertama dan gagal. Tidak ada saldo -Rp200.000 dan tidak ada
partial journal.

### 5.4 Verifikasi campuran dan Done

```text
BEGIN
  lock branch FOR SHARE
  lock deposit account FOR UPDATE
  lock order FOR UPDATE
  claim verification idempotency key
  load + lock active allocations untuk order dari seluruh versi <= current version
  bentuk verification_deposit_sources tepat sekali per allocation aktif
  load all proofs + decisions; validate ownership, hash, destination D2
  validate no pending source and no unresolved overpay
  validate deposit + transfer_for_order = order total snapshot
  insert immutable verification log with breakdown
  call payment.MarkVerified in this transaction
  fill orders.done_* snapshot
  insert audit/timeline/jobs
COMMIT
```

Walaupun alokasi sudah mendebit saldo sebelumnya, account tetap dikunci saat Done
agar reversal paralel tidak dapat mengubah aggregate di tengah keputusan.
Carry-forward tidak menulis `order_allocation_debit` kedua: attempt versi baru
hanya menulis snapshot source baru yang menunjuk allocation/debit lama. Aggregate
deposit untuk Done berasal dari source snapshot tersebut dan harus sama dengan
`committed_deposit(order_id)` yang dihitung ulang di bawah lock.

### 5.5 Reversal

Reversal tidak boleh berdiri sendiri di luar service domain.

```text
BEGIN
  lock branch, account, order, source transaction, allocation in global order
  claim idempotency key
  SUM seluruh reversal committed yang menunjuk source
  cocokkan aggregate itu dengan allocation.reversed_amount bila relevan
  hitung unreversed amount = ABS(source.amount) - aggregate reversal
  validate requested reversal <= unreversed amount
  insert opposite deposit_transaction referencing source
  compare-and-increment allocation.reversed_amount bila relevan
  update account balance atomik; untuk debit reversal pastikan balance cukup
  insert audit/event with mandatory reason
COMMIT
```

Lock source transaction dan allocation membuat dua reversal paralel terhadap
source yang sama terserialisasi. Request kedua wajib menghitung ulang SUM setelah
request pertama commit; ia boleh mengambil sisa saja atau gagal
`REVERSAL_EXCEEDS_AVAILABLE`. Unique `(branch_id, idempotency_key)` memastikan
retry tidak membuat row kedua, tetapi tidak membatasi reversal parsial sah yang
memakai key berbeda.

`topup_reversal_debit` atau `overpay_reversal_debit` dapat gagal karena saldo
sudah dipakai. Sistem TIDAK BOLEH membuat saldo negatif. Kasus itu membutuhkan
rekonsiliasi bisnis manual dan tetap `needs_review`; keputusan apakah boleh
membuat piutang cabang berada di §16 dan bukan bagian implementasi awal.

### 5.6 Idempotency key

Semua POST mutasi uang menerima header `Idempotency-Key`:

- 16–128 karakter opaque; tidak diturunkan dari waktu saja;
- scope mencakup actor + endpoint + branch;
- server menyimpan SHA-256 key, fingerprint request kanonik, status, dan response;
- claim berada dalam transaksi bisnis;
- retry identik mengembalikan status/body pertama;
- key sama + payload berbeda ditolak;
- kegagalan validasi sebelum claim boleh diulang; hasil committed tidak boleh
  dieksekusi ulang;
- unique constraint journal menjadi pertahanan kedua.

Upload file tetap memakai SHA-256 isi untuk deduplikasi. Idempotency key tidak
menggantikan hash file dan hash file tidak menggantikan idempotency aksi.

## 6. Kontrak API dan otorisasi

Semua endpoint di `/api/v1`, cookie sesi yang ada, error seragam, dan CSRF
double-submit wajib untuk seluruh request non-GET. Nilai `branch_id` customer
selalu berasal dari sesi.

### 6.1 Customer

#### `GET /deposit`

Mengembalikan:

```json
{
  "data": {
    "available_balance": 450000,
    "journal_version": 8,
    "pending_topup_count": 1,
    "pending_topup_amount_declared": 200000
  }
}
```

Pending ditampilkan terpisah dan tidak dijumlahkan ke available balance.

#### `GET /deposit/transactions`

Daftar paginated journal cabang sendiri. Respons user-facing menampilkan tipe,
nominal, waktu, referensi order/top-up, dan status reversal; tidak mengekspos
catatan internal, actor cabang lain, atau nomor rekening penuh.

#### `POST /deposit/topups`

Multipart: `proof`, `declared_amount`, `bank_account_id`. Header idempotency +
CSRF wajib. Rekening harus efektif bagi cabang sesi. Sukses `201` selalu pending;
tidak pernah mengembalikan saldo bertambah.

#### `POST /orders/{id}/deposit-allocations`

```json
{
  "amount": 400000,
  "expected_journal_version": 8,
  "expected_order_version": 0
}
```

Hanya cabang pemilik order Submitted. Boleh juga dipakai secara internal oleh
submit untuk intent Draft. Respons memuat balance baru dan remaining due.

#### `POST /orders/{id}/deposit-allocations/{allocation_id}/reverse`

Tidak tersedia bagi User Cabang pada implementasi awal karena Submitted terkunci
(BR-07). Bila bisnis kelak mengizinkan pembatalan oleh cabang, harus ada desain
terpisah. Reversal dilakukan admin dengan alasan.

### 6.2 Admin

#### `GET /admin/deposits`

Capability `admin.verify`. Filter cabang/status/periode; Manager melihat antrean
operasional dan saldo, tetapi tidak melihat aksi kredit jika tidak mempunyai
`admin.deposit_credit`.

#### `GET /admin/deposits/{branch_id}`

Menampilkan saldo, pending top-up, journal, alokasi aktif, reconciliation status,
dan rekening efektif masked. Nomor penuh hanya pada permukaan D2 yang memang
membutuhkannya.

#### `POST /admin/deposit-topups/{id}/decision`

Body:

```json
{
  "action": "verify",
  "approved_amount": 500000,
  "bank_account_id": "<uuid>",
  "reason": "Nominal dan rekening telah dicocokkan"
}
```

- `review`/`reject`: `admin.verify`;
- `verify`: `admin.deposit_credit` (Superadmin);
- server mengabaikan hasil OCR sebagai otoritas dan memvalidasi ulang D2;
- verify membutuhkan Idempotency-Key + CSRF.

#### `POST /admin/orders/{id}/deposit-allocations`

Capability `admin.verify`; admin dapat menambah alokasi atas permintaan cabang
hanya jika saldo cabang cukup. Actor dan alasan wajib tercatat. Manager boleh
mengalokasikan saldo yang sudah ada karena aksi ini debit, bukan penciptaan
kredit; keputusan ini termasuk paket approval §16.
Untuk revisi total naik, endpoint hanya boleh dipanggil oleh workflow admin pada
sub-state revisi pembayaran yang telah disetujui; endpoint customer tetap tidak
dapat mengubah order Done/terkunci.

#### `POST /admin/orders/{id}/deposit-allocations/{allocation_id}/reverse`

Capability `admin.verify`; alasan wajib. Ditolak bila order Done kecuali dipanggil
sebagai bagian workflow revisi/rekonsiliasi yang atomik menurut §8.

Body meminta nominal reversal pada panggilan tersebut, bukan total kumulatif:

```json
{
  "amount": 100000,
  "expected_journal_version": 9,
  "reason": "Koreksi sebagian alokasi"
}
```

`amount` harus positif dan tidak melebihi `amount - reversed_amount` terkini.
Beberapa request dengan idempotency key berbeda boleh mereversal allocation yang
sama secara bertahap; retry key yang sama mengembalikan reversal yang sama.
Respons memuat `reversed_amount` kumulatif dan `active_amount` tersisa.

#### `POST /admin/orders/{id}/verify`

Endpoint existing diperluas dengan breakdown eksplisit:

```json
{
  "action": "approve",
  "expected_order_version": 0,
  "deposit_allocation_ids": ["<uuid>"],
  "proof_splits": [
    {
      "payment_proof_id": "<uuid>",
      "bank_account_id": "<uuid>",
      "approved_nominal": 600000,
      "applied_to_order": 600000,
      "credited_to_deposit": 0
    }
  ],
  "catatan": ""
}
```

Server menghitung ulang aggregate; total dari klien bukan sumber kebenaran.
`credited_to_deposit > 0` memerlukan Superadmin `admin.deposit_credit`. Bila actor
Manager mengirim overpay credit, seluruh request ditolak tanpa Done.
`deposit_allocation_ids` harus sama persis dengan himpunan allocation aktif order
dari seluruh versi sampai `expected_order_version`; omission, duplikasi, allocation
future-version, stale active amount, atau ID lintas order/cabang ditolak. Server
menulis `verification_deposit_sources`; daftar klien tidak dapat memilih untuk
mengabaikan sebagian debit committed.

### 6.3 Error contract

| HTTP | Code | Kondisi |
|---:|---|---|
| 400 | `VALIDATION_FAILED` | body rusak, nominal <= 0, UUID duplikat, unknown field |
| 401 | `UNAUTHENTICATED` | sesi tidak ada/kedaluwarsa |
| 403 | `FORBIDDEN` | capability tidak cukup atau resource cabang lain |
| 403 | `CSRF_INVALID` | token non-GET tidak sah |
| 404 | `TOPUP_NOT_FOUND` | top-up tidak ada/dilarang dilihat |
| 404 | `ALLOCATION_NOT_FOUND` | alokasi tidak ada/dilarang dilihat |
| 409 | `DEPOSIT_BALANCE_INSUFFICIENT` | saldo committed kurang |
| 409 | `DEPOSIT_VERSION_CONFLICT` | expected journal version basi |
| 409 | `ORDER_VERSION_CONFLICT` | versi snapshot order berubah |
| 409 | `DEPOSIT_ALLOCATION_EXCEEDS_ORDER` | aggregate alokasi melebihi total |
| 409 | `DEPOSIT_ALLOCATION_EXCEEDS_REVISED_TOTAL` | revisi turun belum menyertakan reversal eksplisit yang cukup |
| 409 | `TOPUP_ALREADY_DECIDED` | keputusan final sudah ada |
| 409 | `OVERPAY_ALREADY_RESOLVED` | selisih sudah mempunyai keputusan |
| 409 | `OVERPAY_DECISION_REQUIRED` | approve order masih punya selisih lebih |
| 409 | `PAYMENT_PROOF_ALREADY_USED` | bukti menjadi sumber ekonomi lain |
| 409 | `IDEMPOTENCY_KEY_REUSED` | key sama, payload berbeda |
| 409 | `REVERSAL_EXCEEDS_AVAILABLE` | reversal melebihi bagian belum dibalik |
| 409 | `REVERSAL_WOULD_NEGATIVE_BALANCE` | debit koreksi melebihi saldo kini |
| 409 | `BRANCH_BANK_ACCOUNT_UNAVAILABLE` | rekening efektif tidak tersedia |
| 409 | `BANK_ACCOUNT_NOT_ALLOWED` | rekening bukan assignment efektif cabang |
| 409 | `ORDER_ALREADY_DONE` | mutasi settlement biasa terhadap order Done |
| 409 | `ORDER_NOT_SUBMITTED` | alokasi/verifikasi pada state tidak sah |
| 422 | `PAYMENT_BREAKDOWN_MISMATCH` | rincian tidak cocok total order/proof |
| 422 | `PAYMENT_SOURCE_PENDING` | ada top-up/bukti yang belum final |
| 422 | `PAYMENT_PROOF_REQUIRED` | sisa bayar ada tetapi bukti/sumber tidak cukup |
| 503 | `DEPOSIT_RECONCILIATION_REQUIRED` | cache saldo dan journal berbeda |

Respons tidak boleh membocorkan apakah UUID/hash tertentu milik cabang lain.

## 7. Kontrak UI

### 7.1 Customer Portal

Tambahkan area `Deposit cabang`:

- saldo tersedia dengan label tegas “Dana terverifikasi yang dapat digunakan”;
- pending top-up dipisahkan dengan label “Menunggu verifikasi—belum dapat dipakai”;
- tombol `Top up deposit`, daftar transaksi, dan detail sumber/order;
- formulir top-up hanya menampilkan rekening efektif D2;
- pratinjau file, nominal yang dinyatakan, dan peringatan bahwa admin menentukan
  nominal final;
- file/nominal pending tidak menaikkan angka saldo secara optimistis.

Editor/detail order:

- tampilkan saldo tersedia hasil server;
- input “Gunakan deposit” dengan batas order dan saldo;
- ringkasan `Deposit + Transfer baru = Total`;
- bila saldo berubah saat submit/alokasi, tampilkan konflik dan refetch; jangan
  menurunkan alokasi diam-diam;
- detail Submitted menampilkan deposit committed, transfer pending/verified,
  remaining due, dan timeline;
- Done menampilkan snapshot breakdown saat verifikasi, bukan saldo kini.

Cache TanStack Query harus branch/actor-scoped dan dibersihkan saat logout.
Mutasi menginvalidasi saldo, journal, detail order, list order, dan antrean admin.

### 7.2 Admin Portal

Daftar deposit:

- filter `Pending`, `Perlu review`, `Terverifikasi`, `Ditolak`, `Direversal`;
- kolom cabang, nominal declared/OCR/final, rekening masked, umur antrean;
- Manager tidak melihat tombol kredit;
- Superadmin melihat tombol `Verifikasi & kreditkan` dengan modal konfirmasi.

Detail order:

- kartu rekonsiliasi dengan total order snapshot;
- daftar alokasi deposit dan reversal;
- daftar bukti transfer beserta split;
- indikator kurang/tepat/lebih bayar;
- tombol approve disabled bila sumber pending, mismatch, rekening tidak efektif,
  atau overpay belum diputuskan;
- keputusan overpay menampilkan akibat permanen “Kredit RpX ke deposit cabang”;
- konflik versi me-refetch data dan tidak mengulang aksi otomatis.

Journal admin:

- setiap baris menampilkan tanda kredit/debit, saldo sesudah transaksi (boleh
  dihitung/query window), actor, referensi, reversal link, dan alasan;
- tidak ada tombol edit/delete;
- aksi reversal memerlukan alasan dan konfirmasi dampak order/saldo.

## 8. Revisi, pembatalan, dan koreksi

### 8.1 Revisi order Done

Revisi invoice mematok harga/total versi baru. Pembayaran versi lama tidak boleh
diam-diam dianggap cocok bila total berubah.

- Total tidak berubah: `orders.versi` bertambah sesuai AP-REV-02, sedangkan
  `done_order_version` dan `done_verification_log_id` boleh tetap menunjuk attempt
  Done lama. Breakdown lama masih harus sama dengan `orders.total`; revisi dokumen
  tidak membuat journal atau attempt settlement baru. Dokumen versi baru membaca
  pointer immutable tersebut dan mencetak versi invoice baru, bukan mengklaim
  bahwa pembayaran terjadi ulang pada versi baru.
- Total turun: selisih menjadi `revision_overpayment_pending`. Tidak otomatis
  kredit deposit. Seluruh allocation aktif lintas versi tetap dihitung sebagai
  committed deposit. Jika committed deposit tidak melebihi total baru, semuanya
  di-carry-forward dan Superadmin memutuskan selisih transfer melalui transaksi
  koreksi. Jika committed deposit melebihi total baru, request revisi wajib
  membawa rencana reversal allocation eksplisit (allocation, nominal, alasan,
  idempotency key) yang cukup; revisi ditolak bila rencana tidak ada/tidak cukup.
  Tidak ada reversal otomatis atau pemilihan allocation diam-diam.
- Total naik: kekurangan menjadi `revision_underpayment`; order versi baru tidak
  boleh menerbitkan invoice bertanda lunas sampai deposit tambahan/transfer baru
  diverifikasi. Semua allocation aktif versi lama di-carry-forward tanpa debit
  kedua; allocation tambahan dibuat untuk versi baru dan ikut aggregate order.
- Bentuk final mengikuti delta C0: `orders.status='submitted'` ditambah sub-state
  nullable `revision_underpayment` atau `revision_overpayment_pending`. Setiap
  perubahan total wajib melewati pending; tidak ada shortcut revisi langsung
  Done pada implementasi awal. Dokumen current version tidak boleh diterbitkan
  atau dikirim sebagai LUNAS ketika settlement belum cocok.
- Secara database, update `total` sambil mempertahankan `status=done` dan pointer
  lama ditolak oleh FK/CHECK. Implementasi tidak boleh menyiasatinya dengan
  menulis `done_order_version = versi` pada pointer attempt lama.
- Jalur pending yang sah adalah satu transaksi: lock branch, account, order, lalu
  seluruh allocation order menurut urutan global; pindahkan status ke sub-state
  revisi belum lunas yang disetujui; jalankan reversal eksplisit yang diperlukan
  untuk menjaga `committed_deposit <= total baru`; lalu ubah versi/total dan
  kosongkan seluruh pointer/breakdown `done_*`. Attempt lama serta source
  snapshot-nya tetap menjadi histori immutable; UUID pointer lama dan reversal
  plan dicatat pada audit revisi.
  Bila salah satu langkah gagal, seluruh reversal/journal/saldo/update rollback
  sehingga order tetap Done dengan total dan pointer lama yang konsisten.
- Jalur tanpa periode pending hanya sah bila perubahan versi/total, penyelesaian
  delta, pembuatan attempt versi baru, dan penggantian seluruh pointer/breakdown
  commit atomik. Tidak ada keadaan antara yang dapat dirender sebagai LUNAS.
- Journal debit/allocation versi lama tidak ditulis ulang. Reversal eksplisit,
  bila diperlukan oleh revisi turun, menambah row journal dan mengurangi active
  amount tanpa mengubah row lama. Attempt versi baru membuat
  `verification_deposit_sources` baru yang memetakan **semua** allocation aktif
  lintas versi tepat sekali dengan active amount terkunci saat itu; row snapshot
  attempt lama tidak diubah. Dengan demikian carry-forward tercatat tanpa debit
  ganda, sedangkan allocation baru versi terkini juga masuk aggregate yang sama.
- Query remaining, verifikasi, dan rekonsiliasi selalu menghitung allocation aktif
  berdasarkan `order_id` lintas versi. Filter versi hanya memastikan
  `allocation_order_version <= settlement_order_version`, bukan mengeluarkan
  sumber lama.

### 8.2 Pembatalan

PRD §8 masih membuka apakah cabang boleh membatalkan Submitted. Karena belum ada
status `cancelled`, implementasi D3 awal TIDAK BOLEH menambah pembatalan sendiri.

Jika desain pembatalan kelak disetujui:

- semua alokasi aktif order harus direversal atomik;
- transfer order yang sudah masuk harus mempunyai keputusan refund atau kredit
  deposit oleh Superadmin;
- bukti, nomor invoice, journal, verification logs, dan event tidak dihapus;
- nomor tidak digunakan ulang;
- gagal pada salah satu langkah menggagalkan seluruh transaksi pembatalan.

### 8.3 Koreksi top-up salah

Kredit top-up salah dibalik dengan `topup_reversal_debit`. Bila saldo cukup,
reversal menurunkan saldo dan top-up menjadi `reversed`. Bila saldo sudah dipakai,
reversal ditolak agar saldo tidak negatif dan kasus masuk rekonsiliasi manual.
Tidak boleh mengubah `verified_amount` lama.

### 8.4 Bukti/rekening setelah Done

Penonaktifan rekening atau assignment setelah Done tidak mengubah settlement
historis. Render dokumen memakai snapshot verification Done sesuai D2. Reversal
adalah aksi bisnis terpisah yang membutuhkan alasan; perubahan kebijakan rekening
sendiri tidak memicu reversal otomatis.

## 9. OCR, verifikasi manual, dokumen, dan reporting

### 9.1 OCR top-up

- OCR boleh membaca nominal, tanggal, bank, digit akhir, confidence, dan raw yang
  telah disanitasi;
- hasil dibandingkan hanya dengan rekening efektif cabang;
- hasil cocok tetap `pending_review`; tidak ada auto-credit;
- unreadable/mismatch/ambiguous menjadi advisory `needs_review`;
- retry OCR idempoten per proof hash + `ocr_at`/policy version;
- perubahan allowlist menandai hasil tujuan stale dan admin harus memetakan ulang;
- OCR raw tidak masuk journal/audit umum.

AI untuk transfer order tetap mengikuti F2/D2, tetapi auto-Done harus dinonaktifkan
untuk settlement yang memakai deposit atau mempunyai overpay. Implementasi paling
aman adalah seluruh pembayaran campuran selalu manual sampai kontrak auto-approve
campuran disetujui terpisah.

### 9.2 Verifikasi manual

Satu attempt menyimpan:

- order/version/total snapshot;
- daftar allocation ID + amount aktif;
- daftar proof split;
- rekening destination snapshot;
- aggregate deposit/transfer/overpay;
- actor, method, reason, dan policy/allowlist version.

Attempt immutable. Attempt review tidak dapat diubah menjadi cocok; admin membuat
attempt baru.

### 9.3 Invoice dan dokumen

Invoice Done menampilkan minimal:

- total order;
- “Dibayar dari deposit”;
- “Dibayar melalui transfer”;
- status LUNAS bila jumlah tepat;
- rekening transfer yang benar-benar dipakai dari snapshot D2;
- tidak perlu menampilkan saldo deposit tersisa kecuali bisnis menyetujuinya.

Render pertama, re-render, dan revisi membaca `done_verification_log_id` serta
breakdown snapshot. Tidak menghitung saldo/journal terkini dan tidak membaca semua
rekening aktif global.

### 9.4 Reporting

Laporan order line tetap menggunakan total/harga snapshot. Tambahkan field
settlement pada laporan pembayaran terpisah atau kolom ringkas:

- deposit amount;
- transfer amount;
- overpay credited amount;
- verification method (`manual` untuk campuran awal);
- verification time/admin.

Jangan menggandakan nilai order saat satu order mempunyai banyak journal/bukti;
query aggregate harus dilakukan sebelum join order lines atau memakai subquery
satu row per order.

## 10. Keamanan dan observability

Kontrol wajib:

- capability diperiksa di router dan service;
- kepemilikan cabang diambil dari sesi, bukan body/path customer;
- CSRF semua mutasi;
- MIME magic byte, ukuran/jumlah file, nama storage generated, presigned URL;
- rate limit upload dan aksi keputusan uang;
- hash bukti dan idempotency claim;
- branch/account/order row lock;
- journal role tidak memiliki UPDATE/DELETE;
- nomor rekening penuh, file, OCR raw, cookie, CSRF, dan key idempotency tidak ada
  di log;
- audit transaksi yang sama;
- response lintas cabang dibuat indistinguishable 403/404;
- anomaly alert untuk saldo mismatch, reversal gagal, hash reuse, dan banyak
  version conflict.

Metrik tanpa label UUID ber-cardinality tinggi:

- total saldo deposit aggregate (akses Superadmin; bukan telemetry publik);
- jumlah/umur top-up pending;
- kredit/debit per jenis dan periode;
- kegagalan insufficient balance;
- conflict/idempotency replay;
- overpay pending;
- reconciliation mismatch;
- reversal yang ditolak karena saldo tidak cukup.

## 11. Migrasi dan rollout fail-closed

Urutan implementasi masa depan:

1. migrasi additive: enum/tabel/kolom nullable, tanpa saldo awal;
2. backfill `payment_proofs.branch_id` dari order dan purpose order lama;
3. validasi duplikasi/hash dan cabang sebelum constraint diperketat;
4. backfill attempt legacy yang terbukti transfer-only dengan deposit 0 dan tanpa
   source row; masukkan versi/breakdown ambigu ke rekonsiliasi fail-closed;
5. deploy read path/admin observability dengan fitur mutasi OFF;
6. reconciliation memastikan setiap account baru balance 0 dan SUM journal 0;
7. aktifkan upload top-up ke pending saja;
8. aktifkan verifikasi/kredit untuk Superadmin pada staging;
9. aktifkan alokasi dan pembayaran campuran setelah race/E2E lulus;
10. produksi dengan manual approval dan kill switch fail-closed.

Sebelum workflow revisi total berubah diaktifkan, migrasi harus memasang FK dan
kedua CHECK §4.8, lalu aplikasi lama yang dapat meninggalkan pointer pada status
non-Done harus sudah tidak berjalan. Backfill pointer hanya untuk settlement Done
yang dapat dibuktikan. Uji rollout wajib membuktikan transaksi masuk sub-state
pending mengosongkan pointer aktif tetapi tidak menghapus `verification_logs`,
journal, atau audit; rollback transaksi mempertahankan row Done lama secara utuh.

Tidak ada seed saldo produksi. Saldo awal, bila bisnis mempunyai catatan lama,
harus melalui proyek rekonsiliasi terpisah dengan bukti, approval, dan journal
`opening_balance_credit` yang belum diizinkan dokumen ini. Sampai disetujui, semua
cabang mulai saldo 0.

Rollback aplikasi tidak boleh kembali ke versi yang mengabaikan debit journal dan
mengizinkan Done berdasarkan transfer saja. Rollback aman berarti mutasi deposit
dimatikan dan order yang mempunyai allocation/campuran tetap fail closed.

Migrasi wajib kompatibel mundur sesuai `Architecture.md` §9: tambah nullable,
backfill tervalidasi, baru constraint/index. Uji database bersih dan salinan
anonim. VA migration/configuration tidak disentuh.

## 12. Skenario numerik dan error

### 12.1 Top-up pending tidak dapat dipakai

- saldo Rp100.000;
- upload top-up declared Rp500.000 masih pending;
- available tetap Rp100.000;
- alokasi Rp200.000 ditolak `DEPOSIT_BALANCE_INSUFFICIENT`;
- setelah Superadmin verify Rp500.000, balance menjadi Rp600.000 dan alokasi dapat
  dicoba ulang dengan idempotency key baru.

### 12.2 Alokasi paralel

- balance Rp500.000;
- Order A minta Rp400.000 dan Order B Rp300.000 bersamaan;
- row lock membuat satu sukses;
- lainnya melihat balance committed tersisa dan gagal;
- SUM journal = balance >= 0; tidak ada debit orphan.

### 12.3 Campuran tepat

- order Rp1.250.000;
- deposit Rp450.000;
- dua bukti transfer Rp500.000 + Rp300.000;
- seluruh rekening allowed dan nominal disetujui;
- breakdown Rp450.000 + Rp800.000 = Rp1.250.000;
- satu attempt manual membuat Done; retry tidak menggandakan journal/log/job.

### 12.4 Overpay

- order Rp1.000.000;
- deposit Rp250.000;
- transfer approved Rp800.000;
- kebutuhan transfer Rp750.000; overpay Rp50.000;
- order review sampai Superadmin memilih credit;
- transaksi final memakai Rp750.000 ke order, kredit deposit Rp50.000, lalu Done;
- tidak ada auto-credit saat OCR/upload.

### 12.5 Cross-branch

- bukti milik cabang A menunjuk rekening yang hanya efektif bagi B;
- OCR/manual menghasilkan not allowed;
- top-up/order A tidak memperoleh kredit/Done;
- request dengan allocation/topup UUID B oleh actor A mengembalikan not found atau
  forbidden tanpa mengungkap data B.

### 12.6 Reversal

- top-up kredit Rp500.000; Rp450.000 sudah dialokasikan; balance Rp50.000;
- admin mencoba membalik seluruh top-up Rp500.000;
- ditolak `REVERSAL_WOULD_NEGATIVE_BALANCE`, journal lama tetap utuh;
- tidak ada balance negatif atau debit koreksi parsial tanpa keputusan baru.

### 12.7 Dua reversal parsial pada alokasi yang sama

- allocation asal Rp300.000 menunjuk satu `order_allocation_debit -300.000`;
- reversal pertama Rp100.000 menulis journal `+100.000`, cache
  `reversed_amount=100.000`, dan active Rp200.000;
- reversal kedua dengan key berbeda Rp100.000 menunjuk debit asal yang sama,
  menulis journal kedua `+100.000`, cache `reversed_amount=200.000`, dan active
  Rp100.000;
- retry salah satu key tidak menambah row atau saldo;
- request berikut Rp150.000 ditolak `REVERSAL_EXCEEDS_AVAILABLE` karena sisa hanya
  Rp100.000; dua request paralel masing-masing Rp100.000 pada sisa Rp100.000
  menghasilkan tepat satu commit.

### 12.8 Revisi Done dan pointer settlement

- order Done versi 0 total Rp1.000.000 mempunyai attempt versi 0, breakdown
  Rp400.000 deposit + Rp600.000 transfer, dan `done_order_version=0`;
- revisi dokumen tanpa perubahan total menaikkan `orders.versi` menjadi 1;
  pointer dan `done_order_version=0` tetap sah, tidak ada journal/attempt baru,
  serta invoice `-R1` merender breakdown attempt versi 0;
- revisi berikutnya menaikkan total menjadi Rp1.200.000; update yang membiarkan
  row `done` dengan pointer versi 0 ditolak karena breakdown Rp1.000.000 tidak
  sama dengan total baru;
- mengganti angka pointer menjadi versi 2 tanpa attempt versi 2 juga ditolak FK;
- jalur pending yang sah mengubah status dari Done, mengubah total/versi, dan
  mengosongkan seluruh field `done_*` dalam satu transaksi; attempt versi 0 tetap
  dapat dibaca sebagai histori melalui `verification_logs` dan audit menyimpan
  pointer yang dilepas;
- ketika pointer aktif NULL/status belum Done, invoice revisi tidak boleh
  berlabel/dikirim LUNAS;
- setelah attempt versi 2 memverifikasi tambahan Rp200.000, status Done dan seluruh
  pointer/breakdown versi 2 diisi atomik. Jika transaksi pending atau settlement
  gagal, rollback tidak boleh meninggalkan total baru dengan pointer lama maupun
  pointer NULL pada row yang masih Done.

Carry-forward deposit pada contoh yang sama wajib konkret:

- kenaikan ke Rp1.200.000 tidak mendebit ulang allocation versi 0 Rp400.000;
  bila ditambah allocation versi 2 Rp100.000, attempt versi 2 mempunyai dua source
  snapshot Rp400.000 + Rp100.000, sehingga deposit Rp500.000 dan transfer yang
  diterapkan harus Rp700.000;
- dua source itu membuat `committed_deposit=Rp500.000`, bukan Rp900.000; retry
  settlement tidak menambah debit maupun row source kedua untuk attempt yang sama;
- revisi turun dari settlement awal ke Rp800.000 membawa allocation Rp400.000
  utuh, menerapkan transfer Rp400.000, dan menahan selisih transfer Rp200.000 pada
  review sampai keputusan Superadmin; tidak ada reversal deposit otomatis;
- revisi turun ke Rp300.000 ditolak selama allocation aktif masih Rp400.000.
  Request revisi harus secara eksplisit mereversal sedikitnya Rp100.000 dalam
  transaksi revisi; attempt baru kemudian memetakan active amount Rp300.000.

## 13. Rencana pengujian implementasi

### 13.1 Unit backend

1. pending/review/rejected top-up tidak menambah available balance.
2. verifikasi top-up menghasilkan tepat satu kredit dengan nominal final admin.
3. OCR cocok tetap advisory dan tidak mengkredit.
4. hash/file sama tidak dapat menjadi dua sumber ekonomi.
5. aggregate balance sama dengan SUM journal bertanda.
6. semua kind journal menegakkan tanda yang benar.
7. alokasi > saldo atau > remaining order ditolak.
8. alokasi parsial beberapa order menghitung saldo tepat.
9. reversal parsial/penuh menghitung unreversed amount dan menolak over-reversal.
10. reversal debit yang membuat negatif ditolak.
11. breakdown campuran tepat memungkinkan MarkVerified.
12. kurang bayar tetap review; overpay membutuhkan keputusan eksplisit.
13. proof split memenuhi approved = applied + credited.
14. overpay credit hanya Superadmin dan tepat satu kali.
15. idempotency retry identik mengembalikan hasil; payload beda ditolak.
16. allocation/topup/order branch mismatch fail closed.
17. rekening nonaktif/dicabut/branch lain ditolak pada final transaction.
18. harga/total settlement memakai order version snapshot.
19. C&C tanpa pembayaran tetap submitted; campuran C&C memakai aturan sama.
20. render breakdown memakai attempt Done, bukan balance kini.
21. revisi Done total tetap mempertahankan pointer/version settlement lama tanpa
    journal baru; total berubah tidak dapat memakai pointer lama sebagai lunas dan
    harus mengosongkan pointer aktif saat masuk sub-state belum lunas.
22. committed deposit dan remaining menghitung allocation aktif seluruh versi,
    sedangkan source snapshot carry-forward tidak membuat debit kedua.
23. revisi turun yang committed deposit-nya melebihi total baru membutuhkan
    reversal plan eksplisit; omission/nominal kurang ditolak tanpa mutasi.

### 13.2 Integrasi PostgreSQL/API

1. FK/constraint mencegah owner proof ambigu dan transaksi orphan.
2. role aplikasi tidak dapat UPDATE/DELETE journal.
3. kredit top-up dan update balance commit/rollback atomik.
4. dua verify top-up paralel: tepat satu kredit.
5. dua alokasi paralel yang totalnya melebihi balance: paling banyak kombinasi
   yang muat saldo yang commit; balance tidak negatif.
6. dua alokasi paralel pada order sama tidak melebihi total order.
7. dua reversal parsial berurutan pada debit yang sama sama-sama commit, menunjuk
   source yang sama, dan aggregate/cache/active amount tepat.
8. retry reversal parsial dengan key sama tidak menambah journal/saldo; key sama
   dengan amount berbeda ditolak.
9. dua reversal paralel yang masing-masing mencoba memakai sisa terakhir:
   tepat satu commit dan aggregate tidak melebihi amount asal.
10. verify order paralel dengan reversal diserialisasi; tidak ada Done berdasarkan
   allocation yang lebih dahulu dibalik.
11. perubahan allowlist paralel dengan verify top-up/order diserialisasi menurut
   D2; rekening yang dicabut lebih dahulu tidak lolos.
12. retry POST setelah timeout tidak menggandakan journal, log, event, atau job.
13. key sama payload beda ditolak tanpa mutasi.
14. top-up pending tidak bisa dipakai lewat API langsung.
15. Manager dapat review tetapi tidak credit top-up/overpay.
16. User Cabang tidak dapat mengakses cabang lain dengan UUID tebakan.
17. seluruh mutasi tanpa CSRF/token salah ditolak tanpa side effect.
18. bukti order lintas cabang/rekening masuk review.
19. campuran dengan beberapa proof dan allocation cocok persis total.
20. satu sumber pending membuat approve ditolak.
21. overpay tanpa keputusan tetap review; keputusan Superadmin atomik credit+Done.
22. audit actor/amount/reference benar dan tidak memuat rahasia.
23. reconciliation mismatch memblokir mutasi baru.
24. migrasi backfill proof lama menghasilkan branch/purpose tepat tanpa kredit.
25. database bersih mulai seluruh balance 0.
26. VA route/config/table tidak berubah dan callback tidak terlibat.
27. revisi Done total tetap menaikkan `orders.versi` tetapi mempertahankan
    `done_order_version`, pointer, dan breakdown lama; CHECK/FK tetap lolos.
28. revisi Done total naik/turun yang mempertahankan status/pointer lama gagal
    FK/CHECK tanpa mutasi parsial; pemalsuan versi pointer gagal FK komposit.
29. transaksi revisi total berubah ke sub-state belum lunas mengosongkan keempat
    field `done_*`, lolos FK karena seluruh pointer NULL, mempertahankan attempt
    lama di `verification_logs`, dan mencatat pointer lama pada audit.
30. status non-Done dengan salah satu pointer `done_*` non-NULL ditolak CHECK;
    status Done dengan pointer NULL juga ditolak.
31. kegagalan di tengah perubahan status/total/version/pointer rollback ke row Done
    lama secara utuh; endpoint dokumen tidak menerbitkan invoice LUNAS dari state
    pending tanpa pointer aktif.
32. setelah delta revisi selesai, attempt versi baru dan update status/pointer/
    breakdown commit atomik; retry idempoten dan rollback tidak menerbitkan dokumen
    LUNAS baru.
33. revisi naik membawa allocation versi lama ke
    `verification_deposit_sources`, menambah allocation versi baru hanya untuk
    delta, dan aggregate source sama dengan `done_deposit_amount` tanpa debit ganda.
34. revisi turun dengan committed deposit <= total baru membawa semua allocation
    aktif dan memproses selisih transfer sebagai overpay manual; source attempt lama
    tetap immutable.
35. revisi turun dengan committed deposit > total baru gagal tanpa reversal plan;
    plan yang cukup menulis reversal + saldo + total/version/pointer secara atomik,
    sedangkan kegagalan di tengah rollback seluruhnya.
36. race revisi naik/turun melawan allocation atau reversal biasa terserialisasi
    oleh account/order/allocation lock; hasil akhir menjaga
    `committed_deposit <= total`, source set lengkap, dan saldo tidak negatif.
37. retry revisi/settlement memakai key sama tidak menggandakan reversal, debit,
    source snapshot, verification log, atau event; payload berbeda ditolak.

### 13.3 Kontrak/OpenAPI

- skema endpoint, capability, Idempotency-Key, CSRF, dan semua error §6 diuji;
- nominal `int64`, bukan float; unknown field dan angka negatif ditolak;
- breakdown response membedakan pending, verified, applied, overpay;
- tipe frontend generated tidak menjadikan pending sebagai available;
- UUID cabang tidak diterima pada endpoint customer.

### 13.4 UI unit/component

1. saldo available dan pending tampil terpisah.
2. upload sukses tidak menaikkan saldo optimistis.
3. input allocation dibatasi tampilan tetapi error server tetap ditangani.
4. version conflict memicu refetch, bukan retry uang otomatis.
5. rincian campuran menjumlah tepat ke total.
6. overpay menampilkan review dan tombol kredit hanya bagi Superadmin.
7. Manager tidak melihat/tidak dapat menjalankan aksi kredit.
8. journal tidak mempunyai edit/delete.
9. logout membersihkan cache saldo cabang.
10. detail Done stabil setelah saldo/rekening berubah.

### 13.5 Browser/E2E

1. cabang upload top-up; saldo tidak berubah sampai Superadmin verify.
2. Manager review top-up tetapi tidak dapat kredit via UI maupun API.
3. Superadmin verify; saldo bertambah satu kali walau submit ganda/reload.
4. saldo dipakai parsial ke Order A dan B; remaining balance benar.
5. dua browser mengalokasikan saldo terbatas bersamaan; satu mendapat konflik/
   insufficient balance dan tidak ada saldo negatif.
6. order campuran deposit+transfer tepat menjadi Done setelah admin verify.
7. overpay tetap review sampai Superadmin memilih kredit; selisih muncul di saldo.
8. bukti/rekening cross-branch ditolak dan data cabang lain tidak bocor.
9. C&C submit tanpa bayar lalu dilunasi campuran; reminder/status konsisten.
10. reversal alokasi mengembalikan saldo dan timeline tanpa menghapus histori.
11. revisi total naik/turun tidak mengirim invoice LUNAS baru sebelum settlement
    delta diselesaikan.
12. assignment rekening dicabut saat layar terbuka; verify gagal dan UI refetch.
13. revisi total tetap menghasilkan invoice versi baru dengan breakdown settlement
    lama, tanpa membuat pembayaran/journal baru atau menampilkan versi settlement
    seolah sama dengan versi dokumen.
14. revisi total berubah masuk state belum lunas dengan aksi/download LUNAS
    disabled, pointer settlement aktif tidak ditampilkan sebagai pembayaran versi
    baru, tetapi timeline tetap menampilkan attempt versi lama sebagai histori;
    setelah delta diverifikasi, invoice LUNAS baru memakai attempt versi baru.
15. revisi naik menampilkan deposit carry-forward dan delta sebagai sumber berbeda
    tanpa pengurangan saldo kedua; revisi turun yang butuh reversal menampilkan
    rencana eksplisit dan tidak dapat disimpan sebelum nominalnya cukup.

### 13.6 Property/load/security

- property-based sequence acak credit/debit/reversal selalu menjaga
  `balance = SUM(journal)` dan `balance >= 0`;
- load test alokasi concurrent pada satu hot account dan banyak account;
- deadlock test D2 policy mutation vs D3 verify/allocation;
- property/race revisi vs allocation/reversal lintas versi menjaga source snapshot
  lengkap, `committed_deposit <= total`, dan tidak mendebit carry-forward ulang;
- fuzz canonical request fingerprint/idempotency;
- upload MIME spoof, oversized, malware hook, filename traversal;
- log scan memastikan nomor rekening penuh, OCR raw, file, token, dan key tidak
  muncul;
- authorization matrix seluruh endpoint untuk branch/Manager/Superadmin.

## 14. Kriteria penerimaan desain

Reviewer harus dapat menjawab “ya” untuk seluruh butir:

- top-up pending tidak menjadi saldo dan tidak dapat dipakai;
- journal append-only mempunyai invariant kredit-debit dan reconciliation;
- alokasi parsial paralel tidak dapat membuat saldo negatif/double-use;
- saldo dan bukti tidak dapat dipakai lintas cabang;
- campuran deposit + transfer cocok persis total order pada Done;
- overpay selalu review dan hanya dikreditkan setelah keputusan Superadmin;
- bukti/file/aksi duplikat tidak menggandakan kredit, debit, atau Done;
- rekening D2 divalidasi ulang pada titik transaksi final;
- reversal menambah histori, bukan menghapus/mengubah transaksi lama;
- revisi/pembatalan tidak mengarang settlement dan harga memakai snapshot;
- role, CSRF, idempotency, error, UI, OCR advisory, C&C, dan test tercakup;
- VA tetap OFF dan F3-30 tidak diubah;
- tidak ada kode, migrasi, data, atau saldo nyata yang diubah oleh kartu D3.

## 15. Risiko tersisa dan mitigasi

| Risiko | Mitigasi desain |
|---|---|
| Hot row satu cabang saat banyak order paralel | lock account singkat, query/index tepat, retry konflik di level user—bukan auto-repeat uang |
| Cache balance menyimpang dari journal | update atomik, reconciliation, mutasi fail closed |
| Bukti sama dipotong/diubah sehingga hash berbeda | review manusia, metadata/OCR anomaly; tidak mengklaim deduplikasi perceptual sempurna |
| Kredit salah sudah terpakai | larang saldo negatif; rekonsiliasi manual, bukan mutasi histori |
| Join journal menggandakan laporan order | aggregate satu row per order sebelum join line |
| Revisi total mengubah makna “LUNAS” | tahan publikasi sampai settlement versi baru disetujui |
| Manager menciptakan nilai lewat overpay | capability terpisah `admin.deposit_credit`, service guard, audit |
| Allowlist berubah setelah OCR | revalidasi D2 dalam transaksi final |

## 16. Keputusan yang memerlukan persetujuan tertulis CEO

Dokumen sengaja tidak mengarang edge bisnis berikut. Sebelum implementasi, CEO
harus menyetujui paket keputusan ini atau memberi pengganti tertulis:

1. Deposit adalah saldo per cabang, tidak dapat ditransfer antar-cabang dan tidak
   mempunyai penarikan/refund otomatis.
2. Top-up dan overpay hanya menciptakan saldo setelah keputusan Superadmin;
   Manager dapat review/verifikasi operasional dan mengalokasikan saldo yang sudah
   ada, tetapi tidak menciptakan kredit.
3. Overpay order tidak auto-credit. Implementasi awal hanya menawarkan keputusan
   Superadmin `credit_to_deposit`; tanpa keputusan itu order tetap review.
4. Alokasi deposit adalah debit committed sejak dialokasikan ke order Submitted,
   bukan reservation lunak; pembatalannya memakai reversal.
5. User Cabang tidak dapat membalik alokasi pada order Submitted karena BR-07;
   admin melakukannya dengan alasan sampai desain pembatalan disepakati.
6. Kredit yang salah tidak boleh direversal jika akan membuat saldo negatif.
   Kasus tersebut menjadi rekonsiliasi manual; D3 tidak membuat piutang negatif.
7. Pembayaran campuran pada implementasi awal selalu diverifikasi manual; OCR
   top-up hanya advisory dan AI tidak auto-Done untuk settlement campuran.
8. Revisi Done dengan total berubah harus menyelesaikan delta pembayaran sebelum
   invoice versi baru boleh ditandai/dikirim sebagai LUNAS. Allocation aktif dari
   seluruh versi di-carry-forward melalui source snapshot tanpa debit ulang. Revisi
   turun yang membuat committed deposit melebihi total baru wajib menyertakan
   reversal plan eksplisit dan atomik; tidak ada reversal otomatis. Bentuk final
   dipilih oleh delta C0: `status=submitted` dengan
   `revision_underpayment`/`revision_overpayment_pending`, pointer aktif kosong,
   lalu settlement versi kini kembali ke Done secara atomik.
9. Tidak ada opening balance produksi dari D3. Saldo lama memerlukan proyek
   rekonsiliasi dan approval terpisah.
10. Invoice menampilkan breakdown deposit/transfer, tetapi tidak menampilkan saldo
    deposit tersisa.
11. VA tetap OFF dan tidak ada perubahan terhadap F3-30.

Approval QA atas dokumen tidak menggantikan approval CEO. Kartu implementasi harus
menunggu keduanya.
