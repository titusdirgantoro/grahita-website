-- +goose Up
-- C1/D3: skema deposit cabang, journal immutable, settlement source, dan state
-- revisi pembayaran. Migrasi ini tidak membuat saldo awal atau data deposit.
-- migration-check: allow-constraint-replacement
-- C0 mewajibkan signature CHECK lama diverifikasi terhadap parse tree kanonik
-- sebelum diganti atomik oleh constraint settlement yang lebih ketat.

CREATE TYPE payment_proof_purpose AS ENUM ('order_payment', 'deposit_topup');
CREATE TYPE deposit_topup_status AS ENUM
  ('pending_review', 'needs_review', 'verified', 'rejected', 'reversed');
CREATE TYPE branch_deposit_transaction_kind AS ENUM (
  'topup_credit',
  'order_allocation_debit',
  'overpay_credit',
  'allocation_reversal_credit',
  'topup_reversal_debit',
  'overpay_reversal_debit'
);
CREATE TYPE revision_payment_state AS ENUM
  ('revision_underpayment', 'revision_overpayment_pending');
CREATE TYPE revision_payment_transition AS ENUM
  ('total_unchanged', 'underpayment', 'overpayment');

ALTER TABLE payment_proofs
  ADD COLUMN purpose payment_proof_purpose NOT NULL DEFAULT 'order_payment',
  ADD COLUMN branch_id uuid REFERENCES branches(id) ON DELETE RESTRICT;

UPDATE payment_proofs p
   SET branch_id = o.branch_id
  FROM orders o
 WHERE o.id = p.order_id;

ALTER TABLE payment_proofs ALTER COLUMN order_id DROP NOT NULL;
ALTER TABLE payment_proofs
  ADD CONSTRAINT payment_proofs_owner_exactly_one CHECK ((
    (purpose = 'order_payment' AND order_id IS NOT NULL AND branch_id IS NOT NULL)
    OR
    (purpose = 'deposit_topup' AND order_id IS NULL AND branch_id IS NOT NULL)
  ) IS TRUE),
  ADD CONSTRAINT payment_proofs_order_identity UNIQUE (id, order_id);
CREATE UNIQUE INDEX payment_proofs_topup_hash_once
  ON payment_proofs (branch_id, sha256) WHERE purpose = 'deposit_topup';
CREATE UNIQUE INDEX payment_proofs_order_hash_once
  ON payment_proofs (order_id, sha256) WHERE purpose = 'order_payment';

ALTER TABLE orders
  ADD COLUMN revision_payment_state revision_payment_state,
  ADD COLUMN revision_payment_started_at timestamptz,
  ADD COLUMN done_order_version int,
  ADD COLUMN done_deposit_amount bigint,
  ADD COLUMN done_transfer_amount bigint,
  ADD CONSTRAINT orders_branch_identity UNIQUE (id, branch_id);

ALTER TABLE verification_logs
  ADD COLUMN order_version int,
  ADD COLUMN deposit_amount bigint,
  ADD COLUMN transfer_amount bigint,
  ADD CONSTRAINT verification_logs_payment_breakdown CHECK ((
    (deposit_amount IS NULL AND transfer_amount IS NULL)
    OR
    (deposit_amount >= 0 AND transfer_amount >= 0
     AND deposit_amount + transfer_amount = total_pesanan)
  ) IS TRUE);

-- Backfill hanya settlement aktif transfer-only yang dapat dibuktikan oleh pointer
-- D2 dan total yang masih sama. Attempt lain tetap histori nullable/fail-closed.
UPDATE verification_logs v
   SET order_version = COALESCE((
         SELECT min(ir.versi_lama) FROM invoice_revisions ir
          WHERE ir.order_id = o.id
       ), o.versi),
       deposit_amount = 0,
       transfer_amount = v.total_pesanan
  FROM orders o
 WHERE o.done_verification_log_id = v.id
   AND o.status = 'done'
   AND v.total_pesanan = o.total;

UPDATE orders o
   SET done_order_version = v.order_version,
       done_deposit_amount = v.deposit_amount,
       done_transfer_amount = v.transfer_amount
  FROM verification_logs v
 WHERE o.done_verification_log_id = v.id
   AND v.order_version IS NOT NULL
   AND v.deposit_amount IS NOT NULL
   AND v.transfer_amount IS NOT NULL;

ALTER TABLE verification_logs
  ADD CONSTRAINT verification_logs_settlement_identity
    UNIQUE (id, order_id, order_version, total_pesanan),
  ADD CONSTRAINT verification_logs_settlement_breakdown
    UNIQUE (id, order_id, order_version, total_pesanan,
            deposit_amount, transfer_amount);

ALTER TABLE orders
  ADD CONSTRAINT orders_done_verification_attempt_fk
    FOREIGN KEY (done_verification_log_id, id, done_order_version, total,
                 done_deposit_amount, done_transfer_amount)
    REFERENCES verification_logs
      (id, order_id, order_version, total_pesanan,
       deposit_amount, transfer_amount)
    ON DELETE RESTRICT NOT VALID,
  ADD CONSTRAINT orders_done_payment_pointer_all_or_none CHECK ((
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
  ) IS TRUE) NOT VALID;

CREATE TABLE branch_deposit_accounts (
    branch_id       uuid PRIMARY KEY REFERENCES branches(id) ON DELETE RESTRICT,
    balance         bigint NOT NULL DEFAULT 0 CHECK (balance >= 0),
    journal_version bigint NOT NULL DEFAULT 0 CHECK (journal_version >= 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE deposit_topups (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id             uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
    bank_account_id       uuid NOT NULL REFERENCES bank_accounts(id) ON DELETE RESTRICT,
    status                deposit_topup_status NOT NULL DEFAULT 'pending_review',
    declared_amount       bigint NOT NULL CHECK (declared_amount > 0),
    verified_amount       bigint CHECK (verified_amount > 0),
    payment_proof_id      uuid NOT NULL UNIQUE REFERENCES payment_proofs(id) ON DELETE RESTRICT,
    verified_by           uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    verified_at           timestamptz,
    rejection_reason      text,
    created_by_kind       actor_kind NOT NULL,
    created_by_id         uuid,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT deposit_topups_verified_fields CHECK ((
      (status IN ('verified','reversed') AND verified_amount IS NOT NULL
       AND verified_by IS NOT NULL AND verified_at IS NOT NULL)
      OR
      (status NOT IN ('verified','reversed') AND verified_amount IS NULL
       AND verified_by IS NULL AND verified_at IS NULL)
    ) IS TRUE)
);

CREATE TABLE deposit_verification_logs (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    topup_id                 uuid NOT NULL REFERENCES deposit_topups(id) ON DELETE RESTRICT,
    method                   verify_method NOT NULL,
    result                   text NOT NULL,
    declared_amount          bigint NOT NULL CHECK (declared_amount > 0),
    observed_amount          bigint CHECK (observed_amount > 0),
    approved_amount          bigint CHECK (approved_amount > 0),
    bank_account_id          uuid REFERENCES bank_accounts(id) ON DELETE RESTRICT,
    branch_allowlist_version bigint NOT NULL CHECK (branch_allowlist_version >= 0),
    destination_result       jsonb NOT NULL,
    admin_id                 uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    reason                   text,
    created_at               timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT deposit_verification_destination_object
      CHECK (jsonb_typeof(destination_result) = 'object')
);

CREATE TABLE branch_deposit_transactions (
    id                          uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id                   uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
    kind                        branch_deposit_transaction_kind NOT NULL,
    amount                      bigint NOT NULL CHECK (amount <> 0),
    source_proof_id             uuid REFERENCES payment_proofs(id) ON DELETE RESTRICT,
    deposit_verification_log_id uuid REFERENCES deposit_verification_logs(id) ON DELETE RESTRICT,
    order_verification_log_id   uuid REFERENCES verification_logs(id) ON DELETE RESTRICT,
    order_id                    uuid REFERENCES orders(id) ON DELETE RESTRICT,
    order_version               int,
    reverses_transaction_id     uuid REFERENCES branch_deposit_transactions(id) ON DELETE RESTRICT,
    idempotency_key_hash        bytea NOT NULL,
    request_fingerprint         bytea NOT NULL,
    actor_kind                  actor_kind NOT NULL,
    actor_id                    uuid,
    reason                      text,
    created_at                  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (branch_id, idempotency_key_hash),
    CONSTRAINT branch_deposit_transaction_sign CHECK ((
      (kind IN ('topup_credit','overpay_credit','allocation_reversal_credit') AND amount > 0)
      OR
      (kind IN ('order_allocation_debit','topup_reversal_debit','overpay_reversal_debit') AND amount < 0)
    ) IS TRUE),
    CONSTRAINT branch_deposit_transaction_order_fields CHECK ((
      (kind IN ('order_allocation_debit','allocation_reversal_credit',
                'overpay_credit','overpay_reversal_debit')
       AND order_id IS NOT NULL AND order_version IS NOT NULL)
      OR
      (kind IN ('topup_credit','topup_reversal_debit')
       AND order_id IS NULL AND order_version IS NULL)
    ) IS TRUE),
    CONSTRAINT branch_deposit_transaction_reversal_fields CHECK ((
      (kind IN ('allocation_reversal_credit','topup_reversal_debit','overpay_reversal_debit')
       AND reverses_transaction_id IS NOT NULL AND btrim(COALESCE(reason,'')) <> '')
      OR
      (kind NOT IN ('allocation_reversal_credit','topup_reversal_debit','overpay_reversal_debit')
       AND reverses_transaction_id IS NULL)
    ) IS TRUE),
    CONSTRAINT branch_deposit_transaction_typed_source CHECK ((
      (kind = 'topup_credit' AND deposit_verification_log_id IS NOT NULL
       AND order_verification_log_id IS NULL AND source_proof_id IS NOT NULL)
      OR
      (kind = 'overpay_credit' AND order_verification_log_id IS NOT NULL
       AND deposit_verification_log_id IS NULL AND source_proof_id IS NOT NULL)
      OR
      (kind NOT IN ('topup_credit','overpay_credit')
       AND deposit_verification_log_id IS NULL
       AND order_verification_log_id IS NULL)
    ) IS TRUE),
    CONSTRAINT branch_deposit_transactions_topup_once UNIQUE (deposit_verification_log_id),
    CONSTRAINT branch_deposit_transactions_overpay_typed_identity UNIQUE (
      id, order_verification_log_id, source_proof_id,
      order_id, branch_id, order_version, amount
    )
);
CREATE INDEX branch_deposit_transactions_branch_created
  ON branch_deposit_transactions (branch_id, created_at, id);
CREATE INDEX branch_deposit_transactions_order
  ON branch_deposit_transactions (order_id, created_at, id)
  WHERE order_id IS NOT NULL;
CREATE INDEX branch_deposit_transactions_reversal_source
  ON branch_deposit_transactions (reverses_transaction_id, created_at, id)
  WHERE reverses_transaction_id IS NOT NULL;
CREATE UNIQUE INDEX branch_deposit_transactions_overpay_split_once
  ON branch_deposit_transactions (order_verification_log_id, source_proof_id)
  WHERE kind = 'overpay_credit';

CREATE TABLE deposit_allocations (
    id                   uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id            uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
    order_id             uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
    order_version        int NOT NULL CHECK (order_version >= 0),
    debit_transaction_id uuid NOT NULL UNIQUE
      REFERENCES branch_deposit_transactions(id) ON DELETE RESTRICT,
    amount               bigint NOT NULL CHECK (amount > 0),
    reversed_amount      bigint NOT NULL DEFAULT 0
      CHECK (reversed_amount >= 0 AND reversed_amount <= amount),
    created_by_kind      actor_kind NOT NULL,
    created_by_id        uuid,
    created_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, branch_id, order_id, order_version)
);
CREATE INDEX deposit_allocations_order
  ON deposit_allocations (order_id, created_at, id);

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
    FOREIGN KEY (verification_log_id, order_id, settlement_order_version, settlement_total)
      REFERENCES verification_logs
        (id, order_id, order_version, total_pesanan) ON DELETE RESTRICT,
    FOREIGN KEY (allocation_id, branch_id, order_id, allocation_order_version)
      REFERENCES deposit_allocations
        (id, branch_id, order_id, order_version) ON DELETE RESTRICT,
    CHECK (allocation_order_version <= settlement_order_version)
);

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
    credited_to_deposit_total  bigint NOT NULL CHECK (credited_to_deposit_total >= 0),
    new_credit_to_deposit      bigint NOT NULL CHECK (new_credit_to_deposit >= 0),
    new_credit_transaction_id  uuid UNIQUE,
    created_at                 timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (verification_log_id, payment_proof_id),
    FOREIGN KEY (verification_log_id, order_id, order_version, settlement_total,
                 settlement_deposit_amount, settlement_transfer_amount)
      REFERENCES verification_logs
        (id, order_id, order_version, total_pesanan,
         deposit_amount, transfer_amount) ON DELETE RESTRICT,
    FOREIGN KEY (payment_proof_id, order_id)
      REFERENCES payment_proofs (id, order_id) ON DELETE RESTRICT,
    FOREIGN KEY (order_id, branch_id)
      REFERENCES orders (id, branch_id) ON DELETE RESTRICT,
    FOREIGN KEY (payment_proof_id, order_id, branch_id, approved_nominal)
      REFERENCES payment_proof_economic_decisions
        (payment_proof_id, order_id, branch_id, approved_nominal) ON DELETE RESTRICT,
    FOREIGN KEY (new_credit_transaction_id, verification_log_id, payment_proof_id,
                 order_id, branch_id, order_version, new_credit_to_deposit)
      REFERENCES branch_deposit_transactions
        (id, order_verification_log_id, source_proof_id,
         order_id, branch_id, order_version, amount) ON DELETE RESTRICT,
    CHECK (approved_nominal = applied_to_order + credited_to_deposit_total),
    CHECK ((new_credit_to_deposit = 0 AND new_credit_transaction_id IS NULL)
        OR (new_credit_to_deposit > 0 AND new_credit_transaction_id IS NOT NULL))
);

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
  ) IS TRUE) NOT VALID,
  ADD CONSTRAINT invoice_revisions_previous_done_fk
    FOREIGN KEY (previous_done_verification_log_id, order_id,
                 previous_done_order_version, previous_done_total,
                 previous_done_deposit_amount, previous_done_transfer_amount)
    REFERENCES verification_logs
      (id, order_id, order_version, total_pesanan,
       deposit_amount, transfer_amount)
    ON DELETE RESTRICT NOT VALID;

-- Ganti CHECK lama secara fail-closed. Nama pembanding dicadangkan selama transaksi.
LOCK TABLE orders IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT conname, contype, convalidated, connoinherit
      FROM pg_constraint
     WHERE conrelid = 'orders'::regclass
       AND conname IN ('done_punya_waktu', 'orders_done_payment_breakdown')
  LOOP
    IF r.contype <> 'c' OR NOT r.convalidated OR r.connoinherit THEN
      RAISE EXCEPTION 'constraint orders.% mempunyai tipe/flag asing', r.conname;
    END IF;
  END LOOP;
  IF EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conrelid = 'orders'::regclass
       AND conname IN ('__c1_expected_done_punya_waktu',
                       '__c1_expected_done_payment_breakdown')
  ) THEN
    RAISE EXCEPTION 'nama constraint pembanding C1 sudah terpakai';
  END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE orders
  ADD CONSTRAINT __c1_expected_done_punya_waktu CHECK (
    status <> 'done' OR done_at IS NOT NULL
  ) NOT VALID,
  ADD CONSTRAINT __c1_expected_done_payment_breakdown CHECK (
    (status = 'done' AND done_verification_log_id IS NOT NULL
     AND done_deposit_amount + done_transfer_amount = total)
    OR
    (status <> 'done' AND done_verification_log_id IS NULL
     AND done_order_version IS NULL AND done_deposit_amount IS NULL
     AND done_transfer_amount IS NULL)
  ) NOT VALID;
-- +goose StatementBegin
DO $$
DECLARE actual_expr text; expected_expr text; pair text[];
BEGIN
  FOREACH pair SLICE 1 IN ARRAY ARRAY[
    ARRAY['done_punya_waktu', '__c1_expected_done_punya_waktu'],
    ARRAY['orders_done_payment_breakdown', '__c1_expected_done_payment_breakdown']
  ] LOOP
    SELECT pg_get_expr(conbin, conrelid, false) INTO actual_expr
      FROM pg_constraint
     WHERE conrelid = 'orders'::regclass AND conname = pair[1];
    IF FOUND THEN
      SELECT pg_get_expr(conbin, conrelid, false) INTO STRICT expected_expr
        FROM pg_constraint
       WHERE conrelid = 'orders'::regclass AND conname = pair[2];
      IF actual_expr <> expected_expr THEN
        RAISE EXCEPTION 'constraint orders.% mempunyai ekspresi asing', pair[1];
      END IF;
    END IF;
  END LOOP;
END $$;
-- +goose StatementEnd
ALTER TABLE orders
  DROP CONSTRAINT __c1_expected_done_punya_waktu,
  DROP CONSTRAINT __c1_expected_done_payment_breakdown,
  DROP CONSTRAINT IF EXISTS done_punya_waktu,
  DROP CONSTRAINT IF EXISTS orders_done_payment_breakdown,
  ADD CONSTRAINT orders_settlement_state_consistent CHECK ((
    (status = 'draft' AND revision_payment_state IS NULL
     AND revision_payment_started_at IS NULL AND done_at IS NULL
     AND done_verification_log_id IS NULL AND done_order_version IS NULL
     AND done_deposit_amount IS NULL AND done_transfer_amount IS NULL
     AND verif_status = 'belum' AND alasan_review IS NULL)
    OR
    (status = 'submitted' AND revision_payment_state IS NULL
     AND revision_payment_started_at IS NULL AND done_at IS NULL
     AND done_verification_log_id IS NULL AND done_order_version IS NULL
     AND done_deposit_amount IS NULL AND done_transfer_amount IS NULL
     AND ((verif_status = 'belum' AND alasan_review IS NULL)
       OR (verif_status = 'perlu_review' AND alasan_review IS NOT NULL)))
    OR
    (status = 'submitted' AND revision_payment_state = 'revision_underpayment'
     AND revision_payment_started_at IS NOT NULL AND done_at IS NULL
     AND done_verification_log_id IS NULL AND done_order_version IS NULL
     AND done_deposit_amount IS NULL AND done_transfer_amount IS NULL
     AND verif_status = 'perlu_review' AND alasan_review = 'kurang_bayar')
    OR
    (status = 'submitted' AND revision_payment_state = 'revision_overpayment_pending'
     AND revision_payment_started_at IS NOT NULL AND done_at IS NULL
     AND done_verification_log_id IS NULL AND done_order_version IS NULL
     AND done_deposit_amount IS NULL AND done_transfer_amount IS NULL
     AND verif_status = 'perlu_review' AND alasan_review = 'lebih_bayar')
    OR
    (status = 'done' AND revision_payment_state IS NULL
     AND revision_payment_started_at IS NULL AND done_at IS NOT NULL
     AND done_verification_log_id IS NOT NULL AND done_order_version IS NOT NULL
     AND done_order_version BETWEEN 0 AND versi
     AND done_deposit_amount IS NOT NULL AND done_deposit_amount >= 0
     AND done_transfer_amount IS NOT NULL AND done_transfer_amount >= 0
     AND done_deposit_amount + done_transfer_amount = total
     AND verif_status = 'cocok' AND alasan_review IS NULL)
  ) IS TRUE) NOT VALID;

-- Semua tabel ledger/snapshot append-only. Cache account/allocation tetap dapat
-- berubah hanya melalui repository transaksi.
-- +goose StatementBegin
CREATE FUNCTION reject_immutable_deposit_row() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% is immutable', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER branch_deposit_transactions_immutable
  BEFORE UPDATE OR DELETE ON branch_deposit_transactions
  FOR EACH ROW EXECUTE FUNCTION reject_immutable_deposit_row();
CREATE TRIGGER deposit_verification_logs_immutable
  BEFORE UPDATE OR DELETE ON deposit_verification_logs
  FOR EACH ROW EXECUTE FUNCTION reject_immutable_deposit_row();
CREATE TRIGGER verification_deposit_sources_immutable
  BEFORE UPDATE OR DELETE ON verification_deposit_sources
  FOR EACH ROW EXECUTE FUNCTION reject_immutable_deposit_row();
CREATE TRIGGER payment_proof_economic_decisions_immutable
  BEFORE UPDATE OR DELETE ON payment_proof_economic_decisions
  FOR EACH ROW EXECUTE FUNCTION reject_immutable_deposit_row();
CREATE TRIGGER verification_proof_splits_immutable
  BEFORE UPDATE OR DELETE ON verification_proof_splits
  FOR EACH ROW EXECUTE FUNCTION reject_immutable_deposit_row();

-- +goose Down
DROP TRIGGER IF EXISTS verification_proof_splits_immutable ON verification_proof_splits;
DROP TRIGGER IF EXISTS payment_proof_economic_decisions_immutable ON payment_proof_economic_decisions;
DROP TRIGGER IF EXISTS verification_deposit_sources_immutable ON verification_deposit_sources;
DROP TRIGGER IF EXISTS deposit_verification_logs_immutable ON deposit_verification_logs;
DROP TRIGGER IF EXISTS branch_deposit_transactions_immutable ON branch_deposit_transactions;
DROP FUNCTION IF EXISTS reject_immutable_deposit_row();

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_settlement_state_consistent;
ALTER TABLE orders ADD CONSTRAINT done_punya_waktu CHECK (status <> 'done' OR done_at IS NOT NULL);
ALTER TABLE invoice_revisions
  DROP CONSTRAINT IF EXISTS invoice_revisions_previous_done_fk,
  DROP CONSTRAINT IF EXISTS invoice_revisions_settlement_snapshot_consistent,
  DROP COLUMN IF EXISTS settlement_contract_version,
  DROP COLUMN IF EXISTS payment_transition,
  DROP COLUMN IF EXISTS previous_done_at,
  DROP COLUMN IF EXISTS previous_done_transfer_amount,
  DROP COLUMN IF EXISTS previous_done_deposit_amount,
  DROP COLUMN IF EXISTS previous_done_total,
  DROP COLUMN IF EXISTS previous_done_order_version,
  DROP COLUMN IF EXISTS previous_done_verification_log_id;
DROP TABLE IF EXISTS verification_proof_splits;
DROP TABLE IF EXISTS payment_proof_economic_decisions;
DROP TABLE IF EXISTS verification_deposit_sources;
DROP TABLE IF EXISTS deposit_allocations;
DROP TABLE IF EXISTS branch_deposit_transactions;
DROP TABLE IF EXISTS deposit_verification_logs;
DROP TABLE IF EXISTS deposit_topups;
DROP TABLE IF EXISTS branch_deposit_accounts;
ALTER TABLE orders
  DROP CONSTRAINT IF EXISTS orders_done_payment_pointer_all_or_none,
  DROP CONSTRAINT IF EXISTS orders_done_verification_attempt_fk,
  DROP CONSTRAINT IF EXISTS orders_branch_identity,
  DROP COLUMN IF EXISTS done_transfer_amount,
  DROP COLUMN IF EXISTS done_deposit_amount,
  DROP COLUMN IF EXISTS done_order_version,
  DROP COLUMN IF EXISTS revision_payment_started_at,
  DROP COLUMN IF EXISTS revision_payment_state;
ALTER TABLE verification_logs
  DROP CONSTRAINT IF EXISTS verification_logs_settlement_breakdown,
  DROP CONSTRAINT IF EXISTS verification_logs_settlement_identity,
  DROP CONSTRAINT IF EXISTS verification_logs_payment_breakdown,
  DROP COLUMN IF EXISTS transfer_amount,
  DROP COLUMN IF EXISTS deposit_amount,
  DROP COLUMN IF EXISTS order_version;
DROP INDEX IF EXISTS payment_proofs_order_hash_once;
DROP INDEX IF EXISTS payment_proofs_topup_hash_once;
ALTER TABLE payment_proofs
  DROP CONSTRAINT IF EXISTS payment_proofs_order_identity,
  DROP CONSTRAINT IF EXISTS payment_proofs_owner_exactly_one,
  ALTER COLUMN order_id SET NOT NULL,
  DROP COLUMN IF EXISTS branch_id,
  DROP COLUMN IF EXISTS purpose;
DROP TYPE IF EXISTS revision_payment_transition;
DROP TYPE IF EXISTS revision_payment_state;
DROP TYPE IF EXISTS branch_deposit_transaction_kind;
DROP TYPE IF EXISTS deposit_topup_status;
DROP TYPE IF EXISTS payment_proof_purpose;
