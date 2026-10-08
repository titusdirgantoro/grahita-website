-- +goose Up
-- 0003_order_integrity.sql — pengaman integritas pesanan (Database.md §4.4, §8).
--
-- Prinsip yang dipegang:
--   * draft boleh belum lengkap — tidak ada trigger yang menuntut isi baris
--     selama status masih 'draft', sehingga user bisa menyimpan draft kosong
--     lalu melengkapinya;
--   * revisi invoice admin yang sah (AP-REV-01/BR-07) tetap boleh mengubah
--     baris pesanan; penguncian "cabang tidak boleh mengubah pesanan
--     Submitted/Done" adalah tanggung jawab layer service (aktor) karena hanya
--     di sana pelaku (cabang vs admin) diketahui, bukan trigger;
--   * invariant ditegakkan saat transisi submit (draft -> submitted).

-- ---------- Bentuk nomor serial ----------
-- Serial resmi: YYYYMMDD + urut harian (Database.md §6). Minimal 3 digit agar
-- tetap muat bila satu hari melewati 999 pengambilan.
ALTER TABLE order_lines
    ADD CONSTRAINT serial_format CHECK (
        serial_number IS NULL OR serial_number ~ '^[0-9]{8}[0-9]{3,}$');

-- Hanya baris produk yang mendapat serial; baris packaging selalu tanpa serial.
ALTER TABLE order_lines
    ADD CONSTRAINT packaging_tanpa_serial CHECK (
        jenis = 'product' OR serial_number IS NULL);

-- ---------- Satu pesanan tidak boleh mencampur sertifikat & reguler ----------
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION order_lines_guard_kind() RETURNS trigger AS $$
DECLARE
    v_jenis product_kind;
    v_lain  int;
BEGIN
    IF NEW.jenis <> 'product' OR NEW.product_id IS NULL THEN
        RETURN NEW;                              -- baris packaging: tanpa produk
    END IF;

    SELECT jenis INTO v_jenis FROM products WHERE id = NEW.product_id;
    IF v_jenis IS NULL THEN
        RETURN NEW;                              -- produk hilang; FK RESTRICT mencegah
    END IF;

    SELECT count(*) INTO v_lain
      FROM order_lines l
      JOIN products p ON p.id = l.product_id
     WHERE l.order_id = NEW.order_id
       AND l.jenis = 'product'
       AND l.id <> NEW.id
       AND p.jenis <> v_jenis;

    IF v_lain > 0 THEN
        RAISE EXCEPTION
            'pesanan sertifikat tidak boleh dicampur produk reguler (CP-SER-03)'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER order_lines_guard_kind_trg
    BEFORE INSERT OR UPDATE ON order_lines
    FOR EACH ROW EXECUTE FUNCTION order_lines_guard_kind();

-- ---------- Invariant pada transisi submit ----------
-- Dipanggil hanya saat status berpindah draft -> submitted, sehingga draft
-- setengah jadi tidak pernah dihalangi dan revisi admin (tidak melewati
-- transisi ini) tidak terganggu.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION orders_guard_submit() RETURNS trigger AS $$
DECLARE
    v_baris_produk int;
    v_packaging    int;
    v_sertifikat   int;
    v_total        bigint;
    v_tanpa_serial int;
BEGIN
    SELECT count(*) FILTER (WHERE jenis = 'product'),
           count(*) FILTER (WHERE jenis = 'packaging'),
           COALESCE(sum(subtotal), 0)
      INTO v_baris_produk, v_packaging, v_total
      FROM order_lines
     WHERE order_id = NEW.id;

    IF v_baris_produk = 0 THEN
        RAISE EXCEPTION 'pesanan harus punya minimal satu baris produk saat submit'
            USING ERRCODE = '23514';
    END IF;

    -- Serial diberikan service di dalam transaksi submit (BR-09); tidak boleh
    -- ada baris produk yang lolos tanpa nomor.
    SELECT count(*) INTO v_tanpa_serial
      FROM order_lines
     WHERE order_id = NEW.id AND jenis = 'product' AND serial_number IS NULL;
    IF v_tanpa_serial > 0 THEN
        RAISE EXCEPTION 'semua baris produk harus punya serial number saat submit (BR-09)'
            USING ERRCODE = '23514';
    END IF;

    SELECT count(*) INTO v_sertifikat
      FROM order_lines l
      JOIN products p ON p.id = l.product_id
     WHERE l.order_id = NEW.id AND p.jenis = 'sertifikat';

    IF v_sertifikat > 0 THEN
        IF v_packaging <> 1 THEN
            RAISE EXCEPTION 'pesanan sertifikat wajib punya tepat satu baris biaya packaging (CP-SER-02)'
                USING ERRCODE = '23514';
        END IF;
    ELSIF v_packaging > 0 THEN
        RAISE EXCEPTION 'baris biaya packaging hanya untuk pesanan sertifikat (CP-SER-02)'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.total <> v_total THEN
        RAISE EXCEPTION 'total pesanan (%) tidak sama dengan jumlah baris (%)',
            NEW.total, v_total
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER orders_guard_submit_trg
    BEFORE UPDATE ON orders
    FOR EACH ROW
    WHEN (OLD.status = 'draft' AND NEW.status = 'submitted')
    EXECUTE FUNCTION orders_guard_submit();

-- ---------- Minimal satu superadmin aktif ----------
-- Pengaman di lapisan database (Database.md §4.1); aturan bisnisnya tetap
-- divalidasi di layer service saat menonaktifkan/menghapus admin.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION admin_users_guard_superadmin() RETURNS trigger AS $$
DECLARE
    v_sisa int;
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.role = 'superadmin' AND OLD.is_active THEN
            SELECT count(*) INTO v_sisa
              FROM admin_users
             WHERE role = 'superadmin' AND is_active AND id <> OLD.id;
            IF v_sisa = 0 THEN
                RAISE EXCEPTION 'minimal satu superadmin aktif harus tersisa'
                    USING ERRCODE = '23514';
            END IF;
        END IF;
        RETURN OLD;
    END IF;

    IF OLD.role = 'superadmin' AND OLD.is_active
       AND (NOT NEW.is_active OR NEW.role <> 'superadmin') THEN
        SELECT count(*) INTO v_sisa
          FROM admin_users
         WHERE role = 'superadmin' AND is_active AND id <> OLD.id;
        IF v_sisa = 0 THEN
            RAISE EXCEPTION 'minimal satu superadmin aktif harus tersisa'
                USING ERRCODE = '23514';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER admin_users_guard_superadmin_trg
    BEFORE UPDATE OR DELETE ON admin_users
    FOR EACH ROW EXECUTE FUNCTION admin_users_guard_superadmin();

-- Catatan retensi: bukti transfer (payment_proofs) disimpan tanpa batas waktu
-- sesuai keputusan user (override Database.md §9) — tidak ada job pembersih,
-- dan backup tidak menghapus baris/berkas apa pun.

-- +goose Down
DROP TRIGGER IF EXISTS admin_users_guard_superadmin_trg ON admin_users;
DROP FUNCTION IF EXISTS admin_users_guard_superadmin();
DROP TRIGGER IF EXISTS orders_guard_submit_trg ON orders;
DROP FUNCTION IF EXISTS orders_guard_submit();
DROP TRIGGER IF EXISTS order_lines_guard_kind_trg ON order_lines;
DROP FUNCTION IF EXISTS order_lines_guard_kind();

ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS packaging_tanpa_serial;
ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS serial_format;
