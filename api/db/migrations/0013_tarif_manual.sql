-- +goose Up
-- Versi tarif boleh lahir dari edit manual matriks harga (Superadmin/Manager),
-- bukan hanya dari impor master Impact. Komponen HJM/P1/SPV tetap wajib dan
-- harga jual tetap dihitung server; yang berbeda hanya provenance: versi impor
-- menunjuk attempt + sel workbook, versi manual menunjuk admin pembuatnya.
-- Migrasi additive: versi impor lama tetap sah tanpa perubahan data.

ALTER TABLE branch_tariff_versions
    ADD COLUMN source_kind text NOT NULL DEFAULT 'import',
    ADD COLUMN created_by uuid REFERENCES admin_users(id) ON DELETE RESTRICT;

ALTER TABLE branch_tariff_versions ALTER COLUMN source_import_attempt_id DROP NOT NULL;
ALTER TABLE branch_tariff_versions ALTER COLUMN source_sheet DROP NOT NULL;
ALTER TABLE branch_tariff_versions ALTER COLUMN source_row DROP NOT NULL;
ALTER TABLE branch_tariff_versions ALTER COLUMN source_columns DROP NOT NULL;

ALTER TABLE branch_tariff_versions
    ADD CONSTRAINT branch_tariff_versions_provenance CHECK (
        (source_kind = 'import'
            AND source_import_attempt_id IS NOT NULL AND source_sheet IS NOT NULL
            AND source_row IS NOT NULL AND source_columns IS NOT NULL)
        OR
        (source_kind = 'manual'
            AND source_import_attempt_id IS NULL AND source_sheet IS NULL
            AND source_row IS NULL AND source_columns IS NULL
            AND created_by IS NOT NULL)
    );

-- +goose Down
-- Versi manual tidak bisa direpresentasikan oleh skema lama; rollback menolak
-- alih-alih menghapus riwayat tarif diam-diam.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM branch_tariff_versions WHERE source_kind = 'manual') THEN
        RAISE EXCEPTION 'masih ada versi tarif manual; rollback 0013 ditolak';
    END IF;
END
$$;
-- +goose StatementEnd
ALTER TABLE branch_tariff_versions DROP CONSTRAINT branch_tariff_versions_provenance;
ALTER TABLE branch_tariff_versions ALTER COLUMN source_columns SET NOT NULL;
ALTER TABLE branch_tariff_versions ALTER COLUMN source_row SET NOT NULL;
ALTER TABLE branch_tariff_versions ALTER COLUMN source_sheet SET NOT NULL;
ALTER TABLE branch_tariff_versions ALTER COLUMN source_import_attempt_id SET NOT NULL;
ALTER TABLE branch_tariff_versions DROP COLUMN created_by;
ALTER TABLE branch_tariff_versions DROP COLUMN source_kind;
