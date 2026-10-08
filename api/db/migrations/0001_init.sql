-- +goose Up
-- 0001_init.sql — skema awal (lihat Database.md).
-- Target: PostgreSQL 18 (uuidv7() bawaan).

CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ---------- Enum ----------
CREATE TYPE actor_kind    AS ENUM ('branch','admin','system');
CREATE TYPE admin_role    AS ENUM ('superadmin','manager');
CREATE TYPE product_kind  AS ENUM ('reguler','sertifikat');
CREATE TYPE order_status  AS ENUM ('draft','submitted','done');
CREATE TYPE verif_status  AS ENUM ('belum','cocok','perlu_review');
CREATE TYPE review_reason AS ENUM ('kurang_bayar','lebih_bayar','tidak_terbaca','rekening_beda','lainnya');
CREATE TYPE line_kind     AS ENUM ('product','packaging');
CREATE TYPE verify_method AS ENUM ('manual','ai','virtual_account');
CREATE TYPE doc_kind      AS ENUM ('invoice_pdf','surat_jalan_xlsx','surat_jalan_pdf','report_xlsx');
CREATE TYPE email_kind    AS ENUM ('reset_password','undangan_admin','invoice_cabang','invoice_admin',
                                   'surat_jalan_admin','reminder_cnc');
CREATE TYPE email_status  AS ENUM ('queued','sending','sent','failed');
CREATE TYPE bank_code     AS ENUM ('BCA','BRI');

-- ---------- Master cabang & pengguna ----------
CREATE TABLE branches (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    nama        text   NOT NULL,
    nama_kepala text   NOT NULL,
    email       citext NOT NULL UNIQUE,
    telepon     text,
    is_cnc      boolean NOT NULL DEFAULT false,
    is_special  boolean NOT NULL DEFAULT false,
    pajak_in    boolean NOT NULL DEFAULT false,
    is_active   boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX branches_nama_trgm ON branches USING gin (nama gin_trgm_ops);

CREATE TABLE branch_users (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id     uuid NOT NULL UNIQUE REFERENCES branches(id) ON DELETE RESTRICT,
    email         citext NOT NULL UNIQUE,
    password_hash text   NOT NULL,
    telepon       text,
    is_active     boolean NOT NULL DEFAULT true,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE admin_users (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    nama              text   NOT NULL,
    email             citext NOT NULL UNIQUE,
    password_hash     text,
    role              admin_role NOT NULL DEFAULT 'manager',
    notif_invoice     boolean NOT NULL DEFAULT false,
    notif_surat_jalan boolean NOT NULL DEFAULT false,
    is_active         boolean NOT NULL DEFAULT true,
    invited_at        timestamptz,
    activated_at      timestamptz,
    last_login_at     timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- ---------- Token & sesi ----------
CREATE TABLE registration_tokens (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id    uuid  NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE,
    token_prefix text  NOT NULL,
    expires_at   timestamptz NOT NULL,
    used_at      timestamptz,
    used_by      uuid REFERENCES branch_users(id),
    created_by   uuid REFERENCES admin_users(id),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX registration_tokens_one_active
    ON registration_tokens (branch_id) WHERE used_at IS NULL;

CREATE TABLE password_reset_tokens (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    actor_kind actor_kind NOT NULL,
    user_id    uuid  NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    actor_kind   actor_kind NOT NULL,
    user_id      uuid  NOT NULL,
    branch_id    uuid REFERENCES branches(id),
    token_hash   bytea NOT NULL UNIQUE,
    ip           inet,
    user_agent   text,
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user ON sessions (user_id) WHERE revoked_at IS NULL;

-- ---------- Katalog & harga ----------
CREATE TABLE products (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    kode          text UNIQUE,
    nama          text NOT NULL,
    jenis         product_kind NOT NULL DEFAULT 'reguler',
    satuan        text NOT NULL DEFAULT 'pcs',
    deskripsi     text,
    harga_default bigint  NOT NULL DEFAULT 0 CHECK (harga_default >= 0),
    urutan        int     NOT NULL DEFAULT 0,
    is_active     boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sertifikat_gratis CHECK (jenis <> 'sertifikat' OR harga_default = 0)
);

CREATE TABLE branch_prices (
    branch_id  uuid   NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
    product_id uuid   NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    harga      bigint NOT NULL CHECK (harga >= 0),
    updated_by uuid REFERENCES admin_users(id),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (branch_id, product_id)
);
CREATE INDEX branch_prices_product ON branch_prices (product_id);

CREATE TABLE bank_accounts (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    bank         bank_code NOT NULL,
    nama_pemilik text NOT NULL,
    nomor        text NOT NULL,
    is_active    boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (bank, nomor)
);

-- ---------- Pesanan ----------
CREATE TABLE orders (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id     uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
    nomor         text,
    versi         int  NOT NULL DEFAULT 0,
    status        order_status NOT NULL DEFAULT 'draft',
    verif_status  verif_status NOT NULL DEFAULT 'belum',
    alasan_review review_reason,
    is_cnc        boolean NOT NULL DEFAULT false,
    is_sertifikat boolean NOT NULL DEFAULT false,
    tanggal       date,
    jatuh_tempo   date,
    catatan       text,
    note_admin    text,
    total         bigint NOT NULL DEFAULT 0 CHECK (total >= 0),
    submitted_at  timestamptz,
    done_at       timestamptz,
    created_by    uuid REFERENCES branch_users(id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT nomor_unik UNIQUE (nomor),
    CONSTRAINT draft_tanpa_nomor CHECK (
        (status = 'draft'  AND nomor IS NULL AND tanggal IS NULL) OR
        (status <> 'draft' AND nomor IS NOT NULL AND tanggal IS NOT NULL)),
    CONSTRAINT cnc_punya_jatuh_tempo CHECK (
        NOT is_cnc OR status = 'draft' OR jatuh_tempo IS NOT NULL),
    CONSTRAINT done_punya_waktu CHECK (status <> 'done' OR done_at IS NOT NULL)
);
CREATE INDEX orders_branch_tanggal  ON orders (branch_id, tanggal DESC);
CREATE INDEX orders_status          ON orders (status, verif_status);
CREATE INDEX orders_tanggal         ON orders (tanggal DESC);
CREATE INDEX orders_cnc_jatuh_tempo ON orders (jatuh_tempo) WHERE is_cnc AND status <> 'done';
CREATE INDEX orders_nomor_trgm      ON orders USING gin (nomor gin_trgm_ops);

CREATE TABLE order_lines (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id        uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    line_no         int  NOT NULL,
    jenis           line_kind NOT NULL DEFAULT 'product',
    product_id      uuid REFERENCES products(id) ON DELETE RESTRICT,
    nama_snapshot   text   NOT NULL,
    satuan_snapshot text   NOT NULL DEFAULT 'pcs',
    qty             int    NOT NULL CHECK (qty > 0),
    harga_satuan    bigint NOT NULL CHECK (harga_satuan >= 0),
    subtotal        bigint GENERATED ALWAYS AS (qty * harga_satuan) STORED,
    serial_number   text,
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
-- Maksimal satu baris biaya packaging per pesanan.
CREATE UNIQUE INDEX order_lines_satu_packaging
    ON order_lines (order_id) WHERE jenis = 'packaging';

-- ---------- Pembayaran & verifikasi ----------
CREATE TABLE payment_proofs (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id         uuid  NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    storage_key      text  NOT NULL,
    nama_file        text  NOT NULL,
    mime_type        text  NOT NULL,
    ukuran_byte      bigint NOT NULL,
    sha256           bytea NOT NULL,
    uploaded_by_kind actor_kind NOT NULL,
    uploaded_by_id   uuid,
    uploaded_by_nama text,
    ocr_nominal      bigint,
    ocr_tanggal      date,
    ocr_bank         text,
    ocr_rek_akhir    text,
    ocr_confidence   numeric(4,3),
    ocr_raw          jsonb,
    ocr_at           timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, sha256)
);
CREATE INDEX payment_proofs_order ON payment_proofs (order_id);

CREATE TABLE verification_logs (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id      uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    metode        verify_method NOT NULL,
    total_pesanan bigint NOT NULL,
    total_terbaca bigint,
    selisih       bigint GENERATED ALWAYS AS (COALESCE(total_terbaca, 0) - total_pesanan) STORED,
    hasil         verif_status NOT NULL,
    alasan        review_reason,
    admin_id      uuid REFERENCES admin_users(id),
    catatan       text,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX verification_logs_order ON verification_logs (order_id, created_at DESC);

-- ---------- Dokumen, revisi, timeline ----------
CREATE TABLE documents (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id    uuid REFERENCES orders(id) ON DELETE CASCADE,
    jenis       doc_kind NOT NULL,
    versi       int NOT NULL DEFAULT 0,
    storage_key text NOT NULL,
    ukuran_byte bigint,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, jenis, versi)
);

CREATE TABLE invoice_revisions (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id    uuid  NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    versi_lama  int   NOT NULL,
    versi_baru  int   NOT NULL,
    alasan      text  NOT NULL,
    snapshot    jsonb NOT NULL,
    delta_total bigint NOT NULL,
    admin_id    uuid  NOT NULL REFERENCES admin_users(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, versi_baru)
);

CREATE TABLE order_events (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id   uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    at         timestamptz NOT NULL DEFAULT now(),
    icon       text,
    teks       text NOT NULL,
    actor_kind actor_kind NOT NULL DEFAULT 'system',
    actor_id   uuid,
    meta       jsonb
);
CREATE INDEX order_events_order ON order_events (order_id, at);

-- ---------- Email, reminder, pengaturan, audit, backup ----------
CREATE TABLE email_outbox (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id    uuid REFERENCES orders(id) ON DELETE CASCADE,
    jenis       email_kind NOT NULL,
    to_addr     text[] NOT NULL,
    cc_addr     text[],
    subject     text  NOT NULL,
    payload     jsonb NOT NULL DEFAULT '{}'::jsonb,
    status      email_status NOT NULL DEFAULT 'queued',
    attempts    int   NOT NULL DEFAULT 0,
    next_try_at timestamptz NOT NULL DEFAULT now(),
    last_error  text,
    sent_at     timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_outbox_pending ON email_outbox (next_try_at) WHERE status IN ('queued','sending');
CREATE INDEX email_outbox_failed  ON email_outbox (created_at DESC) WHERE status = 'failed';

CREATE TABLE cnc_reminders (
    order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    tahap    text NOT NULL,
    sent_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (order_id, tahap)
);

CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_by uuid REFERENCES admin_users(id),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_logs (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    at         timestamptz NOT NULL DEFAULT now(),
    actor_kind actor_kind NOT NULL,
    actor_id   uuid,
    actor_nama text,
    ip         inet,
    entity     text NOT NULL,
    entity_id  uuid,
    action     text NOT NULL,
    before     jsonb,
    after      jsonb
);
CREATE INDEX audit_logs_entity ON audit_logs (entity, entity_id, at DESC);
CREATE INDEX audit_logs_at     ON audit_logs (at DESC);

CREATE TABLE backup_runs (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    jenis       text NOT NULL,
    mulai       timestamptz NOT NULL DEFAULT now(),
    selesai     timestamptz,
    sukses      boolean,
    ukuran_byte bigint,
    tujuan      text,
    pesan       text
);

-- ---------- Penomoran ----------
CREATE TABLE numbering_counters (
    scope      text NOT NULL,
    periode    text NOT NULL,
    last_value int  NOT NULL DEFAULT 0,
    PRIMARY KEY (scope, periode)
);

-- +goose Down
DROP TABLE IF EXISTS numbering_counters;
DROP TABLE IF EXISTS backup_runs;
DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS cnc_reminders;
DROP TABLE IF EXISTS email_outbox;
DROP TABLE IF EXISTS order_events;
DROP TABLE IF EXISTS invoice_revisions;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS verification_logs;
DROP TABLE IF EXISTS payment_proofs;
DROP TABLE IF EXISTS order_lines;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS bank_accounts;
DROP TABLE IF EXISTS branch_prices;
DROP TABLE IF EXISTS products;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS password_reset_tokens;
DROP TABLE IF EXISTS registration_tokens;
DROP TABLE IF EXISTS admin_users;
DROP TABLE IF EXISTS branch_users;
DROP TABLE IF EXISTS branches;

DROP TYPE IF EXISTS bank_code;
DROP TYPE IF EXISTS email_status;
DROP TYPE IF EXISTS email_kind;
DROP TYPE IF EXISTS doc_kind;
DROP TYPE IF EXISTS verify_method;
DROP TYPE IF EXISTS line_kind;
DROP TYPE IF EXISTS review_reason;
DROP TYPE IF EXISTS verif_status;
DROP TYPE IF EXISTS order_status;
DROP TYPE IF EXISTS product_kind;
DROP TYPE IF EXISTS admin_role;
DROP TYPE IF EXISTS actor_kind;
