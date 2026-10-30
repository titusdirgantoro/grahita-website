-- +goose Up
-- Komponen tarif Expres serta snapshot rincian tagihan per order line.
-- Kolom lama harga_satuan/subtotal tetap dipertahankan sebagai tarif final
-- per pcs dan total baris agar migrasi kompatibel mundur.

ALTER TABLE branch_tariff_versions
    ADD COLUMN express_rupiah bigint CHECK (express_rupiah IS NULL OR express_rupiah >= 0);

UPDATE branch_tariff_versions
   SET express_rupiah = 0
 WHERE is_available AND express_rupiah IS NULL;

ALTER TABLE order_lines
    ADD COLUMN keterangan text NOT NULL DEFAULT '',
    ADD COLUMN harga_dasar bigint NOT NULL DEFAULT 0 CHECK (harga_dasar >= 0),
    ADD COLUMN pi_ratio numeric NOT NULL DEFAULT 0 CHECK (pi_ratio >= 0 AND pi_ratio <= 1),
    ADD COLUMN pi_per_pcs bigint NOT NULL DEFAULT 0 CHECK (pi_per_pcs >= 0),
    ADD COLUMN express_per_pcs bigint NOT NULL DEFAULT 0 CHECK (express_per_pcs >= 0),
    ADD COLUMN spv_per_pcs bigint NOT NULL DEFAULT 0 CHECK (spv_per_pcs >= 0),
    ADD CONSTRAINT order_lines_keterangan_length CHECK (char_length(keterangan) <= 250);

-- Data historis tidak mempunyai rincian komponen. Seluruh tarif lama dianggap
-- Produksi sehingga total dan dokumen lama tidak berubah satu rupiah pun.
UPDATE order_lines SET harga_dasar = harga_satuan;

-- +goose Down
ALTER TABLE order_lines
    DROP CONSTRAINT order_lines_keterangan_length,
    DROP COLUMN spv_per_pcs,
    DROP COLUMN express_per_pcs,
    DROP COLUMN pi_per_pcs,
    DROP COLUMN pi_ratio,
    DROP COLUMN harga_dasar,
    DROP COLUMN keterangan;

ALTER TABLE branch_tariff_versions DROP COLUMN express_rupiah;
