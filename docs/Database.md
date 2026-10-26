# Database — Sistem Pemesanan Cabang Grahita Indonesia

Target: **PostgreSQL 18** (kompatibel ke PG 17; siap diupgrade ke 19 setelah stabil). Lihat `Architecture.md` §4.

---

## 1. Konvensi

| Hal | Aturan |
|---|---|
| Primary key | `uuid` dengan default `uuidv7()` (bawaan PG 18) — acak tapi urut waktu sehingga ramah indeks |
| Nama | `snake_case`, tabel jamak dalam bahasa Inggris, kolom domain boleh bahasa Indonesia bila itu istilah bisnis (`is_cnc`, `pajak_in`) |
| Uang | `BIGINT` Rupiah **bulat**, tidak pernah `float`/`money` |
| Waktu | `timestamptz` (UTC). Tanggal bisnis memakai `date` hasil konversi ke `Asia/Jakarta` |
| Soft delete | Tidak dipakai. Entitas master memakai `is_active`; data transaksi tidak pernah dihapus |
| Enum | `CREATE TYPE ... AS ENUM` untuk nilai yang stabil; tabel referensi untuk yang sering berubah |
| Audit kolom | `created_at`, `updated_at` di semua tabel; `created_by` pada tabel yang dibuat admin |
| Migrasi | goose, satu perubahan per file, selalu kompatibel mundur |

Ekstensi: `pgcrypto`, `pg_trgm`, `pg_stat_statements`.

---

## 2. Diagram relasi

```
branches ─┬─< branch_users                    admin_users ─< sessions
          ├─< registration_tokens             admin_users ─< audit_logs
          ├─< branch_prices >─ products
          └─< orders ─┬─< order_lines >─ products
                      ├─< payment_proofs
                      ├─< verification_logs
                      ├─< documents
                      ├─< invoice_revisions
                      ├─< order_events
                      ├─< cnc_reminders
                      └─< email_outbox

bank_accounts   settings   numbering_counters   backup_runs   river_job
```

---

## 3. Tipe enum

```sql
CREATE TYPE actor_kind    AS ENUM ('branch','admin','system');
CREATE TYPE admin_role    AS ENUM ('superadmin','manager');
CREATE TYPE product_kind  AS ENUM ('reguler','sertifikat');
CREATE TYPE branch_identity_status AS ENUM ('identity_incomplete','ready','registered');
CREATE TYPE order_status  AS ENUM ('draft','submitted','done'); -- 0015 menambah approved_unpaid
CREATE TYPE verif_status  AS ENUM ('belum','cocok','perlu_review');
CREATE TYPE review_reason AS ENUM ('kurang_bayar','lebih_bayar','tidak_terbaca','rekening_beda','lainnya');
CREATE TYPE line_kind     AS ENUM ('product','packaging');
CREATE TYPE verify_method AS ENUM ('manual','ai','virtual_account');
CREATE TYPE doc_kind      AS ENUM ('invoice_pdf','surat_jalan_xlsx','surat_jalan_pdf','report_xlsx');
CREATE TYPE email_kind    AS ENUM ('reset_password','undangan_admin','invoice_cabang','invoice_admin',
                                   'surat_jalan_admin','reminder_cnc');
CREATE TYPE email_status  AS ENUM ('queued','sending','sent','failed');
CREATE TYPE bank_code     AS ENUM ('BCA','BRI');
```

---

## 4. Tabel

### 4.1 Master cabang & pengguna

```sql
CREATE TABLE branches (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    nama            text        NOT NULL,
    nama_kepala     text,
    jabatan_kepala  text,
    email           citext      UNIQUE,                   -- email cabang = email login bila lengkap
    identity_status branch_identity_status NOT NULL DEFAULT 'identity_incomplete',
    telepon         text,
    is_cnc          boolean     NOT NULL DEFAULT false,   -- Cash and Carry
    is_special      boolean     NOT NULL DEFAULT false,   -- kolom warisan dorman; API tidak memakainya
    pajak_in        boolean     NOT NULL DEFAULT false,   -- "PI" (lihat PRD §8 no.2)
    is_active       boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX branches_nama_trgm ON branches USING gin (nama gin_trgm_ops);

CREATE TABLE branch_users (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id       uuid NOT NULL UNIQUE REFERENCES branches(id) ON DELETE RESTRICT, -- 1 cabang = 1 akun
    email           citext NOT NULL UNIQUE,
    password_hash   text   NOT NULL,
    telepon         text,
    is_active       boolean NOT NULL DEFAULT true,
    last_login_at   timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE admin_users (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    nama            text   NOT NULL,
    email           citext NOT NULL UNIQUE,
    password_hash   text,                                  -- NULL selama masih undangan
    role            admin_role NOT NULL DEFAULT 'manager',
    notif_invoice   boolean NOT NULL DEFAULT false,         -- flag "Kirim Invoice ke email"
    notif_surat_jalan boolean NOT NULL DEFAULT false,       -- flag "Kirim Surat Jalan ke email"
    is_active       boolean NOT NULL DEFAULT true,
    invited_at      timestamptz,
    activated_at    timestamptz,
    last_login_at   timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
-- minimal satu superadmin aktif dijaga di layer service + trigger pengaman
```

### 4.2 Token & sesi

```sql
CREATE TABLE registration_tokens (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id     uuid NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
    token_hash    bytea NOT NULL UNIQUE,            -- sha256(token); nilai asli tidak disimpan
    token_prefix  text  NOT NULL,                   -- 8 char pertama, untuk ditampilkan di admin
    expires_at    timestamptz NOT NULL,
    used_at       timestamptz,
    used_by       uuid REFERENCES branch_users(id),
    created_by    uuid REFERENCES admin_users(id),
    created_at    timestamptz NOT NULL DEFAULT now()
);
-- hanya satu token aktif per cabang
CREATE UNIQUE INDEX registration_tokens_one_active
    ON registration_tokens (branch_id) WHERE used_at IS NULL;

CREATE TABLE password_reset_tokens (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    actor_kind  actor_kind NOT NULL,
    user_id     uuid NOT NULL,
    token_hash  bytea NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    used_at     timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    actor_kind    actor_kind NOT NULL,
    user_id       uuid NOT NULL,
    branch_id     uuid REFERENCES branches(id),     -- diisi untuk sesi cabang
    token_hash    bytea NOT NULL UNIQUE,
    ip            inet,
    user_agent    text,
    expires_at    timestamptz NOT NULL,
    revoked_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user ON sessions (user_id) WHERE revoked_at IS NULL;
```

### 4.3 Katalog & harga

```sql
CREATE TABLE products (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    kode          text UNIQUE,
    nama          text NOT NULL,
    jenis         product_kind NOT NULL DEFAULT 'reguler',
    deskripsi     text,
    harga_default bigint NOT NULL DEFAULT 0 CHECK (harga_default >= 0),
    urutan        int    NOT NULL DEFAULT 0,
    is_active     boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sertifikat_gratis CHECK (jenis <> 'sertifikat' OR harga_default = 0)
);

CREATE TABLE branch_prices (
    branch_id   uuid   NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
    product_id  uuid   NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    harga       bigint NOT NULL CHECK (harga >= 0),
    updated_by  uuid REFERENCES admin_users(id),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (branch_id, product_id)
);
-- tidak ada baris = produk tidak dijual ke cabang tersebut
CREATE INDEX branch_prices_product ON branch_prices (product_id);

CREATE TABLE bank_accounts (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    bank        bank_code NOT NULL,
    nama_pemilik text NOT NULL,
    nomor       text NOT NULL,
    is_active   boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (bank, nomor)
);
```

### 4.4 Pesanan

```sql
CREATE TABLE orders (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id      uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
    nomor          text,                              -- YYYY-MM-XX, NULL saat draft
    versi          int  NOT NULL DEFAULT 0,           -- 0 = asli, >0 = revisi
    status         order_status NOT NULL DEFAULT 'draft',
    verif_status   verif_status NOT NULL DEFAULT 'belum',
    alasan_review  review_reason,
    is_cnc         boolean NOT NULL DEFAULT false,
    is_sertifikat  boolean NOT NULL DEFAULT false,    -- diturunkan dari isi baris
    tanggal        date,                              -- tanggal submit (WIB)
    jatuh_tempo    date,                              -- riwayat C&C lama (sebelum 2026-10-18)
    catatan        text,                              -- dari cabang
    note_admin     text,
    total          bigint NOT NULL DEFAULT 0 CHECK (total >= 0),
    submitted_at   timestamptz,
    done_at        timestamptz,
    created_by     uuid REFERENCES branch_users(id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT nomor_unik UNIQUE (nomor),
    CONSTRAINT draft_tanpa_nomor CHECK (
        (status = 'draft'  AND nomor IS NULL AND tanggal IS NULL) OR
        (status <> 'draft' AND nomor IS NOT NULL AND tanggal IS NOT NULL)),
    -- 0014: C&C dibayar deposit, jatuh tempo hanya riwayat pesanan C&C lama.
    CONSTRAINT jatuh_tempo_hanya_cnc CHECK (jatuh_tempo IS NULL OR is_cnc),
    CONSTRAINT done_punya_waktu CHECK (status <> 'done' OR done_at IS NOT NULL)
);
CREATE INDEX orders_branch_tanggal ON orders (branch_id, tanggal DESC);
CREATE INDEX orders_status         ON orders (status, verif_status);
CREATE INDEX orders_tanggal        ON orders (tanggal DESC);
CREATE INDEX orders_cnc_jatuh_tempo ON orders (jatuh_tempo)   -- sisa reminder lama
       WHERE is_cnc AND status <> 'done';
CREATE INDEX orders_nomor_trgm     ON orders USING gin (nomor gin_trgm_ops);

CREATE TABLE order_lines (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id        uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    line_no         int  NOT NULL,
    jenis           line_kind NOT NULL DEFAULT 'product',
    product_id      uuid REFERENCES products(id) ON DELETE RESTRICT,
    nama_snapshot   text   NOT NULL,                  -- nama produk saat dipesan
    qty             int    NOT NULL CHECK (qty > 0),
    harga_satuan    bigint NOT NULL CHECK (harga_satuan >= 0),
    subtotal        bigint GENERATED ALWAYS AS (qty * harga_satuan) STORED,
    serial_number   text,                             -- YYYYMMDDXXX, hanya jenis 'product'
    created_at      timestamptz NOT NULL DEFAULT now(),

    UNIQUE (order_id, line_no),
    CONSTRAINT serial_unik UNIQUE (serial_number),
    CONSTRAINT packaging_tanpa_produk CHECK (
        (jenis = 'product'   AND product_id IS NOT NULL) OR
        (jenis = 'packaging' AND product_id IS NULL AND qty = 1))
);
CREATE INDEX order_lines_order   ON order_lines (order_id);
CREATE INDEX order_lines_product ON order_lines (product_id);
CREATE INDEX order_lines_serial  ON order_lines (serial_number);
```

### 4.5 Pembayaran & verifikasi

```sql
CREATE TABLE payment_proofs (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id        uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    storage_key     text NOT NULL,
    nama_file       text NOT NULL,
    mime_type       text NOT NULL,
    ukuran_byte     bigint NOT NULL,
    sha256          bytea  NOT NULL,
    uploaded_by_kind actor_kind NOT NULL,             -- 'branch' atau 'admin'
    uploaded_by_id  uuid,
    uploaded_by_nama text,                            -- ditampilkan sebagai penanda unggahan admin
    -- hasil pembacaan AI/OCR
    ocr_nominal     bigint,
    ocr_tanggal     date,
    ocr_bank        text,
    ocr_rek_akhir   text,
    ocr_confidence  numeric(4,3),
    ocr_raw         jsonb,
    ocr_at          timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, sha256)                         -- cegah unggah file yang sama dua kali
);
CREATE INDEX payment_proofs_order ON payment_proofs (order_id);

CREATE TABLE verification_logs (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id      uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    metode        verify_method NOT NULL,
    total_pesanan bigint NOT NULL,
    total_terbaca bigint,
    selisih       bigint GENERATED ALWAYS AS (COALESCE(total_terbaca,0) - total_pesanan) STORED,
    hasil         verif_status NOT NULL,
    alasan        review_reason,
    admin_id      uuid REFERENCES admin_users(id),    -- NULL bila oleh AI
    catatan       text,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX verification_logs_order ON verification_logs (order_id, created_at DESC);
```

### 4.6 Dokumen, revisi, timeline

```sql
CREATE TABLE documents (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id    uuid REFERENCES orders(id) ON DELETE CASCADE,
    jenis       doc_kind NOT NULL,
    versi       int NOT NULL DEFAULT 0,
    storage_key text NOT NULL,
    ukuran_byte bigint,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, jenis, versi)                   -- idempoten untuk job render
);

CREATE TABLE invoice_revisions (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id    uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    versi_lama  int  NOT NULL,
    versi_baru  int  NOT NULL,
    alasan      text NOT NULL,
    snapshot    jsonb NOT NULL,                       -- isi pesanan versi lama
    delta_total bigint NOT NULL,
    admin_id    uuid NOT NULL REFERENCES admin_users(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, versi_baru)
);

CREATE TABLE order_events (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id    uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    at          timestamptz NOT NULL DEFAULT now(),
    icon        text,                                 -- nama ikon Phosphor di UI
    teks        text NOT NULL,
    actor_kind  actor_kind NOT NULL DEFAULT 'system',
    actor_id    uuid,
    meta        jsonb
);
CREATE INDEX order_events_order ON order_events (order_id, at);
```

### 4.7 Email, reminder, pengaturan, audit, backup

```sql
CREATE TABLE email_outbox (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id     uuid REFERENCES orders(id) ON DELETE CASCADE,
    jenis        email_kind NOT NULL,
    to_addr      text[] NOT NULL,
    cc_addr      text[],
    subject      text NOT NULL,
    payload      jsonb NOT NULL,                      -- data template + daftar lampiran
    status       email_status NOT NULL DEFAULT 'queued',
    attempts     int NOT NULL DEFAULT 0,
    next_try_at  timestamptz NOT NULL DEFAULT now(),
    last_error   text,
    sent_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_outbox_pending ON email_outbox (next_try_at)
       WHERE status IN ('queued','sending');
CREATE INDEX email_outbox_failed  ON email_outbox (created_at DESC) WHERE status = 'failed';

-- Riwayat pengingat jatuh tempo C&C lama; tidak ada penulis baru sejak C&C
-- dibayar dari deposit (2026-10-18).
CREATE TABLE cnc_reminders (
    order_id   uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    tahap      text NOT NULL,                         -- 'h-2','h-0','h+1','h+3'
    -- sent_at = waktu baris pengingat DIANTRE (DEFAULT now()), BUKAN waktu email
    -- terkirim: kebenaran "terkirim" ada di email_outbox.sent_at (migrasi
    -- 0007_cnc_reminders_sent_at.sql).
    sent_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (order_id, tahap)
);

CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_by uuid REFERENCES admin_users(id),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_logs (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    at          timestamptz NOT NULL DEFAULT now(),
    actor_kind  actor_kind NOT NULL,
    actor_id    uuid,
    actor_nama  text,
    ip          inet,
    entity      text NOT NULL,                        -- 'order','branch','product','price',...
    entity_id   uuid,
    action      text NOT NULL,                        -- 'create','update','verify','revise',...
    before      jsonb,
    after       jsonb
);
CREATE INDEX audit_logs_entity ON audit_logs (entity, entity_id, at DESC);
CREATE INDEX audit_logs_at     ON audit_logs (at DESC);

CREATE TABLE backup_runs (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    jenis       text NOT NULL,                        -- 'postgres','storage'
    mulai       timestamptz NOT NULL DEFAULT now(),
    selesai     timestamptz,
    sukses      boolean,
    ukuran_byte bigint,
    tujuan      text,                                 -- path Google Drive
    pesan       text
);
```

---

## 5. Pengaturan awal

```sql
INSERT INTO settings (key, value) VALUES
  ('biaya_packaging',      '3000'),
  ('token_hari',           '14'),
  ('reset_menit',          '60'),
  ('max_mb',               '5'),
  ('max_file',             '5'),
  ('digit_invoice',        '2'),
  ('manager_boleh_revisi', 'true'),
  ('ai', '{"aktif":false,"min_keyakinan":0.9,"tol_kurang":0,"tol_lebih":0,"digit_rek":4,"mundur":1}');
```

---

## 6. Penomoran

```sql
CREATE TABLE numbering_counters (
    scope      text NOT NULL,         -- 'invoice' | 'serial'
    periode    text NOT NULL,         -- 'YYYY-MM' untuk invoice, 'YYYYMMDD' untuk serial
    last_value int  NOT NULL DEFAULT 0,
    PRIMARY KEY (scope, periode)
);
```

```sql
-- Mengambil n nomor sekaligus, aman terhadap submit bersamaan.
-- Dipanggil di dalam transaksi submit; baris terkunci sampai COMMIT.
CREATE OR REPLACE FUNCTION next_numbers(p_scope text, p_periode text, p_count int)
RETURNS int AS $$
DECLARE v int;
BEGIN
    INSERT INTO numbering_counters (scope, periode, last_value)
    VALUES (p_scope, p_periode, p_count)
    ON CONFLICT (scope, periode)
    DO UPDATE SET last_value = numbering_counters.last_value + p_count
    RETURNING last_value - p_count + 1 INTO v;
    RETURN v;                        -- nomor pertama dari rentang [v, v+p_count-1]
END;
$$ LANGUAGE plpgsql;
```

**Pemakaian saat submit** (tanggal = `now() AT TIME ZONE 'Asia/Jakarta'`):

| Nomor | Format | Periode counter | Contoh |
|---|---|---|---|
| Invoice | `YYYY-MM-XX` (`XX` = `digit_invoice` digit) | `2026-10` | `2026-10-07` |
| Serial | `YYYYMMDDXXX` (3 digit) | `20261008` | `20261008003` |

Catatan desain:

- Memakai tabel counter, bukan `SEQUENCE`, agar nomor **tidak terbuang saat rollback** dan reset per periode berjalan alami.
- Semua baris `jenis='product'` dalam satu pesanan mengambil rentang sekaligus (`p_count = n`), bukan satu per satu — hanya satu kunci baris per submit.
- Baris `packaging` **tidak** mendapat serial number.
- Nomor invoice berlanjut global per bulan lintas cabang (lihat pertanyaan terbuka `PRD.md` §8 no.6; bila harus per cabang, `periode` menjadi `'<kode_cabang>:2026-10'`).
- Bila `digit_invoice` tidak cukup (urutan > 99 pada digit 2), service menambah digit otomatis dan mencatat peringatan, bukan gagal.

---

## 7. Query penting

### 7.1 Katalog untuk satu cabang

```sql
SELECT p.id, p.nama, p.jenis, p.deskripsi, bp.harga
FROM products p
JOIN branch_prices bp ON bp.product_id = p.id AND bp.branch_id = $1
WHERE p.is_active
ORDER BY p.urutan, p.nama;
```

### 7.2 Chip "perlu tindakan" di dashboard admin

```sql
SELECT
  count(*) FILTER (WHERE status='submitted' AND verif_status='belum')                      AS menunggu,
  count(*) FILTER (WHERE verif_status='perlu_review')                                      AS review,
  (SELECT count(*) FROM email_outbox WHERE status='failed')                                AS email_gagal
FROM orders
WHERE ($1::uuid IS NULL OR branch_id = $1);
```

### 7.3 Baris laporan (satu baris per order line)

```sql
SELECT o.tanggal, o.nomor, o.versi, b.nama AS cabang, b.nama_kepala,
       o.is_cnc, b.pajak_in,
       l.nama_snapshot AS produk, l.jenis, l.serial_number,
       l.qty, l.harga_satuan, l.subtotal,
       o.status, o.done_at,
       (SELECT metode FROM verification_logs v
         WHERE v.order_id = o.id ORDER BY v.created_at DESC LIMIT 1) AS metode_verifikasi
FROM order_lines l
JOIN orders   o ON o.id = l.order_id
JOIN branches b ON b.id = o.branch_id
WHERE o.status <> 'draft'
  AND o.tanggal BETWEEN $1 AND $2
  AND ($3::uuid[] IS NULL OR o.branch_id = ANY($3))
ORDER BY o.tanggal, o.nomor, l.line_no;
```

Untuk ekspor besar, query dijalankan dengan cursor/`FETCH` bertahap dan ditulis streaming oleh `excelize`.

### 7.4 Total bukti transfer sebuah pesanan

```sql
SELECT coalesce(sum(ocr_nominal), 0) AS total_terbaca,
       min(ocr_confidence)           AS keyakinan_terendah,
       count(*)                      AS jumlah_bukti
FROM payment_proofs WHERE order_id = $1;
```

---

## 8. Integritas & trigger

| Aturan | Penegakan |
|---|---|
| `orders.total` = Σ `order_lines.subtotal` | Trigger `AFTER INSERT/UPDATE/DELETE ON order_lines` menghitung ulang; juga diverifikasi di service |
| `orders.is_sertifikat` sesuai isi baris | Trigger yang sama menetapkannya dari `products.jenis` |
| Pesanan sertifikat tidak bercampur produk reguler | Trigger `AFTER` pada `order_lines` → raise exception bila campur |
| Tepat satu baris packaging pada pesanan sertifikat | Unique partial index `(order_id) WHERE jenis='packaging'` + trigger kewajiban ada |
| Pesanan `submitted`/`done` tidak boleh diubah cabang | Trigger `BEFORE UPDATE` menolak perubahan baris kecuali kolom admin (`note_admin`, `verif_status`, `versi`, …) |
| `updated_at` otomatis | Trigger `BEFORE UPDATE` generik pada semua tabel yang punya kolom tersebut |
| Produk yang pernah dipesan tidak bisa dihapus | `ON DELETE RESTRICT` pada `order_lines.product_id` |

Contoh unique partial index untuk packaging:

```sql
CREATE UNIQUE INDEX order_lines_satu_packaging
    ON order_lines (order_id) WHERE jenis = 'packaging';
```

---

## 9. Retensi & pertumbuhan

Estimasi: 34 cabang × ±30 pesanan/bulan × ±3 baris = ±3.060 order line/bulan (±36.720/tahun). Volume ini sangat kecil untuk PostgreSQL — tidak perlu partisi sekarang.

Bila dalam beberapa tahun `orders`/`order_lines` tumbuh besar, partisi `RANGE` per tahun pada `orders.tanggal` adalah langkah berikutnya (struktur tabel sudah siap karena semua query laporan memfilter tanggal).

| Data | Retensi |
|---|---|
| Pesanan, order line, invoice, dokumen | Permanen (kewajiban pembukuan) |
| Bukti transfer | Minimal 5 tahun (perlu konfirmasi, `PRD.md` §8 no.8) |
| `order_events`, `audit_logs` | 3 tahun online, lalu diarsipkan |
| `sessions`, token kedaluwarsa | Dibersihkan job harian |
| `email_outbox` terkirim | 90 hari |

---

## 10. Hak akses database

| Role DB | Hak |
|---|---|
| `grahita_app` | `SELECT/INSERT/UPDATE` pada tabel aplikasi; **tanpa** `DROP`/`ALTER` |
| `grahita_migrate` | DDL, hanya dipakai saat migrasi |
| `grahita_readonly` | `SELECT` untuk analitik/BI dan verifikasi backup |
| `grahita_backup` | `pg_read_all_data` untuk `pg_dump` |

Pemisahan data antar cabang ditegakkan di layer aplikasi (`branch_id` dari sesi). Row Level Security dapat ditambahkan sebagai lapisan kedua bila kelak ada akses langsung ke database di luar API.

## 11. Tambahan skema Tempo dan pilihan pembayaran (migrasi 0015–0017)

Migrasi 0015 menambah enum `approved_unpaid`, `tempo_repayment`, `tempo_statement`, dan `tempo_reminder`. Migrasi 0016 menambah `branches.is_tempo`, `tanggal_bayar`, `tahan_bila_telat` dan snapshot Tempo pada pesanan. Migrasi 0017 menambah `branches.allow_transfer` dan membolehkan satu atau lebih dari Transfer, C&C, dan Tempo sekaligus. Constraint mewajibkan setidaknya satu cara pembayaran serta tanggal bayar 1–28 bila Tempo aktif. `orders.is_cnc` dan `orders.is_tempo` adalah snapshot pilihan tunggal per pesanan; keduanya salah berarti Transfer di muka. Constraint settlement lama dipertahankan untuk pesanan non-Tempo dan diperluas secara eksplisit untuk status Tempo.

Ledger Tempo memakai `tempo_charges` per invoice/koreksi, `tempo_receipts` per bukti, `tempo_allocations` per alokasi pembayaran/kredit, `tempo_credit_entries` untuk saldo kredit Tempo yang terpisah dari jurnal deposit C&C, dan `tempo_adjustments` untuk delta revisi. `tempo_statements` dan `tempo_statement_lines` menyimpan versi PDF Surat Tagihan tanpa menimpa versi lama. `tempo_reminders` mencatat tahap pengingat yang sudah diantre agar job berulang tidak mengirim ganda. `tempo_hold_grace` menyimpan awal dan akhir satu masa tenggang penahanan; unggah ulang tidak memperpanjangnya.

Tanggal periode berasal dari `orders.tanggal` (tanggal submit WIB). Tanggal bayar cabang disalin saat submit ke `orders.tanggal_bayar_snapshot`; perubahan master tidak merombak pesanan lama. Piutang dan umur piutang dibaca dari saldo ledger aktif, sedangkan PDF lama adalah snapshot saat terbit. Kolom `branches.is_special` tetap ada hanya untuk kompatibilitas migrasi lama dan tidak dibaca/ditulis oleh aplikasi.
