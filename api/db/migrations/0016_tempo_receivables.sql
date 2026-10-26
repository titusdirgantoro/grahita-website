-- +goose Up
-- Additive Tempo and accounts-receivable schema. Existing C&C and transfer rows
-- retain their original state and settlement constraints.
-- migration-check: allow-constraint-replacement
ALTER TABLE branches
  ADD COLUMN is_tempo boolean NOT NULL DEFAULT false,
  ADD COLUMN tanggal_bayar smallint,
  ADD COLUMN tahan_bila_telat boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT branches_tempo_payment_mode CHECK ((
    NOT (is_cnc AND is_tempo)
    AND (NOT is_tempo OR tanggal_bayar BETWEEN 1 AND 28)
    AND (is_tempo OR (tanggal_bayar IS NULL AND NOT tahan_bila_telat))
  ) IS TRUE);

ALTER TABLE orders
  ADD COLUMN is_tempo boolean NOT NULL DEFAULT false,
  ADD COLUMN tanggal_bayar_snapshot smallint,
  ADD COLUMN tempo_due_date date,
  ADD COLUMN tempo_approved_at timestamptz,
  ADD COLUMN tempo_paid_amount bigint NOT NULL DEFAULT 0,
  ADD CONSTRAINT orders_tempo_snapshot CHECK ((
    (NOT is_tempo AND tanggal_bayar_snapshot IS NULL AND tempo_due_date IS NULL
     AND tempo_approved_at IS NULL AND tempo_paid_amount = 0)
    OR
    (is_tempo AND NOT is_cnc AND tanggal_bayar_snapshot BETWEEN 1 AND 28
     AND tempo_paid_amount >= 0 AND tempo_paid_amount <= total
     AND (status = 'submitted' OR tempo_approved_at IS NOT NULL)
     AND (status = 'submitted' OR tempo_due_date IS NOT NULL))
  ) IS TRUE) NOT VALID;
CREATE INDEX orders_tempo_due_open ON orders (branch_id, tempo_due_date, tanggal, id)
  WHERE is_tempo AND status IN ('approved_unpaid','done');

-- A repayment proof belongs to the branch, not to one order. Compare the
-- existing CHECK parse tree before replacing it, as required by C1.
ALTER TABLE payment_proofs ADD CONSTRAINT __c1_expected_payment_proofs_owner_exactly_one CHECK (((
    (purpose = 'order_payment' AND order_id IS NOT NULL AND branch_id IS NOT NULL)
    OR (purpose = 'deposit_topup' AND order_id IS NULL AND branch_id IS NOT NULL)
  ) IS TRUE)) NOT VALID;
-- +goose StatementBegin
DO $$
DECLARE actual_expr text; expected_expr text;
BEGIN
  SELECT pg_get_expr(conbin, conrelid, false) INTO actual_expr FROM pg_constraint
    WHERE conrelid='payment_proofs'::regclass AND conname='payment_proofs_owner_exactly_one';
  IF NOT FOUND THEN RAISE EXCEPTION 'missing payment proof owner constraint'; END IF;
  SELECT pg_get_expr(conbin, conrelid, false) INTO STRICT expected_expr FROM pg_constraint
    WHERE conrelid='payment_proofs'::regclass AND conname='__c1_expected_payment_proofs_owner_exactly_one';
  IF actual_expr <> expected_expr THEN RAISE EXCEPTION 'foreign payment proof owner constraint'; END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE payment_proofs
  DROP CONSTRAINT __c1_expected_payment_proofs_owner_exactly_one,
  DROP CONSTRAINT payment_proofs_owner_exactly_one,
  ADD CONSTRAINT payment_proofs_owner_exactly_one CHECK ((
    (purpose = 'order_payment' AND order_id IS NOT NULL AND branch_id IS NOT NULL)
    OR (purpose IN ('deposit_topup','tempo_repayment') AND order_id IS NULL AND branch_id IS NOT NULL)
  ) IS TRUE);
CREATE UNIQUE INDEX payment_proofs_tempo_hash_once ON payment_proofs (branch_id, sha256)
  WHERE purpose = 'tempo_repayment';

CREATE TABLE tempo_receipts (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  branch_id uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
  payment_proof_id uuid NOT NULL UNIQUE REFERENCES payment_proofs(id) ON DELETE RESTRICT,
  bank_account_id uuid NOT NULL REFERENCES bank_accounts(id) ON DELETE RESTRICT,
  declared_amount bigint NOT NULL CHECK (declared_amount > 0),
  verified_amount bigint CHECK (verified_amount > 0),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','verified','rejected')),
  submitted_at timestamptz NOT NULL DEFAULT now(),
  grace_until date,
  verified_by uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
  verified_at timestamptz,
  rejection_reason text,
  CONSTRAINT tempo_receipt_verified CHECK ((
    (status='verified' AND verified_amount IS NOT NULL AND verified_by IS NOT NULL AND verified_at IS NOT NULL)
    OR (status<>'verified' AND verified_amount IS NULL AND verified_by IS NULL AND verified_at IS NULL)
  ) IS TRUE)
);
CREATE INDEX tempo_receipts_branch_pending ON tempo_receipts (branch_id, submitted_at)
  WHERE status='pending';

-- One grace window per overdue episode. Reuploads cannot extend it.
CREATE TABLE tempo_hold_grace (
  branch_id uuid PRIMARY KEY REFERENCES branches(id) ON DELETE RESTRICT,
  started_at timestamptz NOT NULL,
  ends_on date NOT NULL
);

CREATE TABLE tempo_credit_entries (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  branch_id uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
  receipt_id uuid REFERENCES tempo_receipts(id) ON DELETE RESTRICT,
  revision_id uuid REFERENCES invoice_revisions(id) ON DELETE RESTRICT,
  amount bigint NOT NULL CHECK (amount > 0),
  remaining bigint NOT NULL CHECK (remaining >= 0 AND remaining <= amount),
  approved_by uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT tempo_credit_source CHECK ((receipt_id IS NOT NULL) <> (revision_id IS NOT NULL))
);
CREATE UNIQUE INDEX tempo_credit_receipt_once ON tempo_credit_entries (receipt_id)
  WHERE receipt_id IS NOT NULL;
CREATE UNIQUE INDEX tempo_credit_revision_once ON tempo_credit_entries (revision_id)
  WHERE revision_id IS NOT NULL;

CREATE TABLE tempo_charges (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  branch_id uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
  order_id uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  revision_id uuid REFERENCES invoice_revisions(id) ON DELETE RESTRICT,
  amount bigint NOT NULL CHECK (amount >= 0),
  paid_amount bigint NOT NULL DEFAULT 0 CHECK (paid_amount >= 0 AND paid_amount <= amount),
  due_date date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tempo_charges_original_once ON tempo_charges(order_id)
  WHERE revision_id IS NULL;
CREATE UNIQUE INDEX tempo_charges_revision_once ON tempo_charges(revision_id)
  WHERE revision_id IS NOT NULL;
CREATE INDEX tempo_charges_due_open ON tempo_charges(branch_id,due_date)
  WHERE amount>paid_amount;

CREATE TABLE tempo_allocations (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  branch_id uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
  order_id uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  charge_id uuid NOT NULL REFERENCES tempo_charges(id) ON DELETE RESTRICT,
  receipt_id uuid REFERENCES tempo_receipts(id) ON DELETE RESTRICT,
  credit_entry_id uuid REFERENCES tempo_credit_entries(id) ON DELETE RESTRICT,
  amount bigint NOT NULL CHECK (amount > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT tempo_allocation_source CHECK ((receipt_id IS NOT NULL) <> (credit_entry_id IS NOT NULL))
);
CREATE INDEX tempo_allocations_order ON tempo_allocations (order_id, created_at);
CREATE INDEX tempo_allocations_receipt ON tempo_allocations (receipt_id) WHERE receipt_id IS NOT NULL;

CREATE TABLE tempo_adjustments (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  branch_id uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
  order_id uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  revision_id uuid NOT NULL UNIQUE REFERENCES invoice_revisions(id) ON DELETE RESTRICT,
  delta_amount bigint NOT NULL CHECK (delta_amount <> 0),
  due_date date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tempo_statements (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  branch_id uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
  period_month date NOT NULL,
  version int NOT NULL CHECK (version >= 1),
  issued_at timestamptz NOT NULL DEFAULT now(),
  storage_key text,
  fingerprint bytea NOT NULL,
  total_amount bigint NOT NULL CHECK (total_amount >= 0),
  paid_amount bigint NOT NULL CHECK (paid_amount >= 0),
  UNIQUE (branch_id, period_month, version),
  CONSTRAINT tempo_statement_month CHECK (EXTRACT(DAY FROM period_month)=1)
);
CREATE TABLE tempo_statement_lines (
  statement_id uuid NOT NULL REFERENCES tempo_statements(id) ON DELETE RESTRICT,
  charge_id uuid NOT NULL REFERENCES tempo_charges(id) ON DELETE RESTRICT,
  order_id uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  order_version int NOT NULL,
  amount bigint NOT NULL CHECK (amount >= 0),
  paid_amount bigint NOT NULL CHECK (paid_amount >= 0),
  due_date date NOT NULL,
  PRIMARY KEY (statement_id, charge_id)
);
CREATE TABLE tempo_reminders (
  branch_id uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
  due_date date NOT NULL,
  stage text NOT NULL CHECK (stage IN ('h_minus_3','due','h_plus_1','h_plus_7')),
  amount bigint NOT NULL DEFAULT 0 CHECK (amount >= 0),
  generic boolean NOT NULL DEFAULT false,
  enqueued_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (branch_id, due_date, stage)
);
CREATE UNIQUE INDEX email_outbox_tempo_statement_once
  ON email_outbox ((payload->>'statement_id'), (payload->>'recipient_kind'))
  WHERE jenis='tempo_statement';
CREATE UNIQUE INDEX email_outbox_tempo_reminder_once
  ON email_outbox ((payload->>'branch_id'), (payload->>'due_date'), (payload->>'stage'))
  WHERE jenis='tempo_reminder';

-- Verify the old settlement CHECK against its canonical parse tree.
ALTER TABLE orders
  ADD CONSTRAINT __c1_expected_orders_settlement_state_consistent CHECK ((
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
-- +goose StatementBegin
DO $$
DECLARE actual_expr text; expected_expr text;
BEGIN
  SELECT pg_get_expr(conbin, conrelid, false) INTO actual_expr FROM pg_constraint
    WHERE conrelid='orders'::regclass AND conname='orders_settlement_state_consistent';
  IF NOT FOUND THEN RAISE EXCEPTION 'missing order settlement constraint'; END IF;
  SELECT pg_get_expr(conbin, conrelid, false) INTO STRICT expected_expr FROM pg_constraint
    WHERE conrelid='orders'::regclass AND conname='__c1_expected_orders_settlement_state_consistent';
  IF actual_expr <> expected_expr THEN RAISE EXCEPTION 'foreign order settlement constraint'; END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE orders
  DROP CONSTRAINT __c1_expected_orders_settlement_state_consistent,
  DROP CONSTRAINT orders_settlement_state_consistent,
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
    OR
    (status = 'approved_unpaid' AND is_tempo
     AND revision_payment_state IS NULL AND revision_payment_started_at IS NULL
     AND done_at IS NULL AND done_verification_log_id IS NULL
     AND done_order_version IS NULL AND done_deposit_amount IS NULL
     AND done_transfer_amount IS NULL AND verif_status = 'belum'
     AND alasan_review IS NULL AND tempo_approved_at IS NOT NULL
     AND tempo_due_date IS NOT NULL AND tempo_paid_amount < total)
    OR
    (status = 'done' AND is_tempo
     AND revision_payment_state IS NULL AND revision_payment_started_at IS NULL
     AND done_at IS NOT NULL AND done_verification_log_id IS NULL
     AND done_order_version IS NULL AND done_deposit_amount IS NULL
     AND done_transfer_amount IS NULL AND verif_status = 'cocok'
     AND alasan_review IS NULL AND tempo_approved_at IS NOT NULL
     AND tempo_due_date IS NOT NULL AND tempo_paid_amount = total)
  ) IS TRUE) NOT VALID;


-- Tempo revisions carry their own paid snapshot rather than a legacy Done
-- verification pointer. Preserve both historical settlement contracts.
ALTER TABLE invoice_revisions ADD COLUMN tempo_previous_paid bigint;
ALTER TABLE invoice_revisions
  ADD CONSTRAINT __c1_expected_invoice_revisions_settlement_snapshot_consistent CHECK ((
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
  ) IS TRUE) NOT VALID;
-- +goose StatementBegin
DO $$
DECLARE actual_expr text; expected_expr text;
BEGIN
  SELECT pg_get_expr(conbin,conrelid,false) INTO actual_expr FROM pg_constraint
  WHERE conrelid='invoice_revisions'::regclass AND conname='invoice_revisions_settlement_snapshot_consistent';
  IF NOT FOUND THEN RAISE EXCEPTION 'missing revision settlement constraint'; END IF;
  SELECT pg_get_expr(conbin,conrelid,false) INTO STRICT expected_expr FROM pg_constraint
  WHERE conrelid='invoice_revisions'::regclass AND conname='__c1_expected_invoice_revisions_settlement_snapshot_consistent';
  IF actual_expr <> expected_expr THEN RAISE EXCEPTION 'foreign revision settlement constraint'; END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE invoice_revisions
  DROP CONSTRAINT __c1_expected_invoice_revisions_settlement_snapshot_consistent,
  DROP CONSTRAINT invoice_revisions_settlement_snapshot_consistent,
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
    OR
    (settlement_contract_version = 2
     AND payment_transition IS NULL
     AND previous_done_verification_log_id IS NULL
     AND previous_done_order_version IS NULL
     AND previous_done_total IS NULL
     AND previous_done_deposit_amount IS NULL
     AND previous_done_transfer_amount IS NULL
     AND previous_done_at IS NULL
     AND tempo_previous_paid IS NOT NULL AND tempo_previous_paid >= 0)
  ) IS TRUE) NOT VALID;

-- +goose Down
-- Rollback is intentionally refused once Tempo business data exists. At that
-- point repair must be forward-only; deleting receivables would lose money.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM branches WHERE is_tempo)
     OR EXISTS(SELECT 1 FROM orders WHERE is_tempo)
     OR EXISTS(SELECT 1 FROM payment_proofs WHERE purpose='tempo_repayment')
     OR EXISTS(SELECT 1 FROM tempo_statements)
     OR EXISTS(SELECT 1 FROM tempo_receipts) THEN
    RAISE EXCEPTION 'Tempo data exists; roll forward instead of dropping receivables';
  END IF;
END $$;
-- +goose StatementEnd
DROP INDEX IF EXISTS email_outbox_tempo_statement_once;
DROP INDEX IF EXISTS email_outbox_tempo_reminder_once;
DROP TABLE IF EXISTS tempo_reminders;
DROP TABLE IF EXISTS tempo_statement_lines;
DROP TABLE IF EXISTS tempo_statements;
DROP TABLE IF EXISTS tempo_adjustments;
DROP TABLE IF EXISTS tempo_allocations;
DROP TABLE IF EXISTS tempo_charges;
DROP TABLE IF EXISTS tempo_credit_entries;
DROP TABLE IF EXISTS tempo_hold_grace;
DROP TABLE IF EXISTS tempo_receipts;
DROP INDEX IF EXISTS payment_proofs_tempo_hash_once;
DROP INDEX IF EXISTS orders_tempo_due_open;
ALTER TABLE payment_proofs DROP CONSTRAINT payment_proofs_owner_exactly_one,
  ADD CONSTRAINT payment_proofs_owner_exactly_one CHECK (((
    (purpose = 'order_payment' AND order_id IS NOT NULL AND branch_id IS NOT NULL)
    OR (purpose = 'deposit_topup' AND order_id IS NULL AND branch_id IS NOT NULL)
  ) IS TRUE));
ALTER TABLE invoice_revisions
  DROP CONSTRAINT invoice_revisions_settlement_snapshot_consistent,
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
  ) IS TRUE) NOT VALID;
ALTER TABLE invoice_revisions DROP COLUMN tempo_previous_paid;
ALTER TABLE orders DROP CONSTRAINT orders_settlement_state_consistent,
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
ALTER TABLE orders
  DROP CONSTRAINT orders_tempo_snapshot,
  DROP COLUMN is_tempo,
  DROP COLUMN tanggal_bayar_snapshot,
  DROP COLUMN tempo_due_date,
  DROP COLUMN tempo_approved_at,
  DROP COLUMN tempo_paid_amount;
ALTER TABLE branches
  DROP CONSTRAINT branches_tempo_payment_mode,
  DROP COLUMN is_tempo,
  DROP COLUMN tanggal_bayar,
  DROP COLUMN tahan_bila_telat;
