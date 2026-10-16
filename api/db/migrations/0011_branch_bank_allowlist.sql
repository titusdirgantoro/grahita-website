-- +goose Up
-- D2: rekening tujuan yang diizinkan per cabang. Migrasi additive dan fail-closed:
-- tidak ada assignment otomatis untuk cabang lama.

ALTER TABLE branches
  ADD COLUMN bank_allowlist_version bigint NOT NULL DEFAULT 0
  CHECK (bank_allowlist_version >= 0);

CREATE TABLE branch_bank_accounts (
    branch_id       uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
    bank_account_id uuid NOT NULL REFERENCES bank_accounts(id) ON DELETE RESTRICT,
    is_allowed      boolean NOT NULL DEFAULT true,
    assigned_by     uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    assigned_at     timestamptz NOT NULL DEFAULT now(),
    revoked_by      uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    revoked_at      timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (branch_id, bank_account_id),
    CONSTRAINT branch_bank_accounts_lifecycle CHECK (
      (is_allowed AND revoked_by IS NULL AND revoked_at IS NULL) OR
      (NOT is_allowed AND revoked_by IS NOT NULL AND revoked_at IS NOT NULL)
    )
);
CREATE INDEX branch_bank_accounts_effective
  ON branch_bank_accounts (branch_id, bank_account_id) WHERE is_allowed;
CREATE INDEX branch_bank_accounts_by_account
  ON branch_bank_accounts (bank_account_id, branch_id) WHERE is_allowed;

ALTER TABLE verification_logs
  ADD COLUMN branch_allowlist_version bigint,
  ADD COLUMN proof_set_fingerprint bytea,
  ADD COLUMN destination_results jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD CONSTRAINT verification_destination_results_array
    CHECK (jsonb_typeof(destination_results) = 'array');

ALTER TABLE orders
  ADD COLUMN done_verification_log_id uuid
    REFERENCES verification_logs(id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX verification_logs_ai_policy_once
  ON verification_logs (order_id, branch_allowlist_version, proof_set_fingerprint)
  WHERE metode = 'ai'
    AND branch_allowlist_version IS NOT NULL
    AND proof_set_fingerprint IS NOT NULL;

-- Identitas rekening historis immutable. Nama pemilik boleh diperbarui; snapshot
-- keputusan verifikasi tetap menjadi sumber nama historis dokumen.
-- +goose StatementBegin
CREATE FUNCTION bank_account_identity_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.bank IS DISTINCT FROM OLD.bank OR NEW.nomor IS DISTINCT FROM OLD.nomor THEN
    RAISE EXCEPTION 'bank account identity is immutable' USING ERRCODE = '23000';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER bank_accounts_identity_immutable
BEFORE UPDATE ON bank_accounts
FOR EACH ROW EXECUTE FUNCTION bank_account_identity_immutable();

-- Backfill pointer legacy hanya untuk pasangan sukses yang lahir pada timestamp
-- transaksi Done yang sama. Kandidat ambigu atau tidak ada dibiarkan NULL.
WITH kandidat AS (
  SELECT o.id AS order_id, min(v.id::text)::uuid AS verification_id
    FROM orders o
    JOIN verification_logs v
      ON v.order_id = o.id
     AND v.hasil = 'cocok'
     AND v.created_at = o.done_at
   WHERE o.status = 'done'
   GROUP BY o.id
  HAVING count(*) = 1
)
UPDATE orders o
   SET done_verification_log_id = k.verification_id
  FROM kandidat k
 WHERE o.id = k.order_id;

-- +goose Down
DROP TRIGGER IF EXISTS bank_accounts_identity_immutable ON bank_accounts;
DROP FUNCTION IF EXISTS bank_account_identity_immutable();
DROP INDEX IF EXISTS verification_logs_ai_policy_once;
ALTER TABLE orders DROP COLUMN IF EXISTS done_verification_log_id;
ALTER TABLE verification_logs
  DROP CONSTRAINT IF EXISTS verification_destination_results_array,
  DROP COLUMN IF EXISTS destination_results,
  DROP COLUMN IF EXISTS proof_set_fingerprint,
  DROP COLUMN IF EXISTS branch_allowlist_version;
DROP TABLE IF EXISTS branch_bank_accounts;
ALTER TABLE branches DROP COLUMN IF EXISTS bank_allowlist_version;
