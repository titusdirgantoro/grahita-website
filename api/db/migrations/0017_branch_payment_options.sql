-- +goose Up
-- Existing branches retain their single configured method. The default keeps
-- older API binaries able to create transfer branches during image rollback.
ALTER TABLE branches ADD COLUMN allow_transfer boolean NOT NULL DEFAULT true;
UPDATE branches SET allow_transfer = NOT is_cnc AND NOT is_tempo;

-- Compare the old CHECK parse tree before replacing it (C1).
-- migration-check: allow-constraint-replacement
ALTER TABLE branches ADD CONSTRAINT __c1_expected_branches_tempo_payment_mode CHECK ((
  NOT (is_cnc AND is_tempo)
  AND (NOT is_tempo OR tanggal_bayar BETWEEN 1 AND 28)
  AND (is_tempo OR (tanggal_bayar IS NULL AND NOT tahan_bila_telat))
) IS TRUE) NOT VALID;
-- +goose StatementBegin
DO $$
DECLARE actual_expr text; expected_expr text;
BEGIN
  SELECT pg_get_expr(conbin, conrelid, false) INTO actual_expr FROM pg_constraint
    WHERE conrelid='branches'::regclass AND conname='branches_tempo_payment_mode';
  IF NOT FOUND THEN RAISE EXCEPTION 'missing branch payment mode constraint'; END IF;
  SELECT pg_get_expr(conbin, conrelid, false) INTO STRICT expected_expr FROM pg_constraint
    WHERE conrelid='branches'::regclass AND conname='__c1_expected_branches_tempo_payment_mode';
  IF actual_expr <> expected_expr THEN RAISE EXCEPTION 'foreign branch payment mode constraint'; END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE branches
  DROP CONSTRAINT __c1_expected_branches_tempo_payment_mode,
  DROP CONSTRAINT branches_tempo_payment_mode,
  ADD CONSTRAINT branches_payment_options CHECK ((
    (allow_transfer OR is_cnc OR is_tempo)
    AND (NOT is_tempo OR tanggal_bayar BETWEEN 1 AND 28)
    AND (is_tempo OR (tanggal_bayar IS NULL AND NOT tahan_bila_telat))
  ) IS TRUE);

-- +goose Down
-- Do not discard a branch's configured options during rollback.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM branches WHERE
    (is_cnc::int + is_tempo::int + allow_transfer::int) > 1) THEN
    RAISE EXCEPTION 'branches with multiple payment options exist; roll forward instead';
  END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE branches
  DROP CONSTRAINT branches_payment_options,
  ADD CONSTRAINT branches_tempo_payment_mode CHECK ((
    NOT (is_cnc AND is_tempo)
    AND (NOT is_tempo OR tanggal_bayar BETWEEN 1 AND 28)
    AND (is_tempo OR (tanggal_bayar IS NULL AND NOT tahan_bila_telat))
  ) IS TRUE);
ALTER TABLE branches DROP COLUMN allow_transfer;
