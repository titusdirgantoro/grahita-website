-- +goose Up
-- 0002_functions.sql — fungsi penomoran, trigger, dan pengaturan default.

-- ---------- updated_at otomatis ----------
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'branches','branch_users','admin_users','products','bank_accounts','orders'
    ] LOOP
        EXECUTE format(
            'CREATE TRIGGER %I_set_updated_at BEFORE UPDATE ON %I
             FOR EACH ROW EXECUTE FUNCTION set_updated_at()', t, t);
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- ---------- Penomoran invoice & serial number ----------
-- Mengambil p_count nomor berurutan sekaligus. Dipanggil di dalam transaksi
-- submit; baris counter terkunci sampai COMMIT sehingga aman terhadap
-- submit bersamaan dan nomor tidak bolong saat rollback.
-- Mengembalikan nomor PERTAMA dari rentang [v, v + p_count - 1].
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION next_numbers(p_scope text, p_periode text, p_count int)
RETURNS int AS $$
DECLARE v int;
BEGIN
    IF p_count < 1 THEN
        RAISE EXCEPTION 'p_count harus >= 1';
    END IF;

    INSERT INTO numbering_counters (scope, periode, last_value)
    VALUES (p_scope, p_periode, p_count)
    ON CONFLICT (scope, periode)
    DO UPDATE SET last_value = numbering_counters.last_value + p_count
    RETURNING last_value - p_count + 1 INTO v;

    RETURN v;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- ---------- Total pesanan selalu sinkron dengan order_lines ----------
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION recalc_order_totals() RETURNS trigger AS $$
DECLARE oid uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN
        oid := OLD.order_id;
    ELSE
        oid := NEW.order_id;
    END IF;

    UPDATE orders o
       SET total = COALESCE((SELECT sum(l.subtotal) FROM order_lines l WHERE l.order_id = oid), 0),
           is_sertifikat = EXISTS (
               SELECT 1 FROM order_lines l
                 JOIN products p ON p.id = l.product_id
                WHERE l.order_id = oid AND p.jenis = 'sertifikat'),
           updated_at = now()
     WHERE o.id = oid;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER order_lines_recalc
    AFTER INSERT OR UPDATE OR DELETE ON order_lines
    FOR EACH ROW EXECUTE FUNCTION recalc_order_totals();

-- ---------- Pengaturan default ----------
INSERT INTO settings (key, value) VALUES
    ('biaya_packaging',      '3000'::jsonb),
    ('cnc_hari',             '7'::jsonb),
    ('token_hari',           '14'::jsonb),
    ('reset_menit',          '60'::jsonb),
    ('max_mb',               '5'::jsonb),
    ('max_file',             '5'::jsonb),
    ('digit_invoice',        '2'::jsonb),
    ('manager_boleh_revisi', 'true'::jsonb),
    ('ai', '{"aktif":false,"min_keyakinan":0.9,"tol_kurang":0,"tol_lebih":0,"digit_rek":4,"mundur":1}'::jsonb)
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DROP TRIGGER IF EXISTS order_lines_recalc ON order_lines;
DROP FUNCTION IF EXISTS recalc_order_totals();
DROP FUNCTION IF EXISTS next_numbers(text, text, int);

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'branches','branch_users','admin_users','products','bank_accounts','orders'
    ] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS %I_set_updated_at ON %I', t, t);
    END LOOP;
END;
$$;
-- +goose StatementEnd

DROP FUNCTION IF EXISTS set_updated_at();
DELETE FROM settings WHERE key IN (
    'biaya_packaging','cnc_hari','token_hari','reset_menit','max_mb','max_file',
    'digit_invoice','manager_boleh_revisi','ai');
