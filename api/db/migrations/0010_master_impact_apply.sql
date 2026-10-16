-- +goose Up
-- Apply/rollback staging master Impact. Payload sumber ternormalisasi disimpan
-- privat per attempt agar apply tidak perlu membaca ulang workbook atau PII log.

ALTER TABLE master_import_attempts
    ADD COLUMN normalized_source jsonb NOT NULL DEFAULT '{"source_hash":"","branches":[]}'::jsonb;

ALTER TABLE master_import_preview_items
    ADD COLUMN decision_reason text;

CREATE TABLE master_import_apply_runs (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    attempt_id            uuid NOT NULL UNIQUE REFERENCES master_import_attempts(id) ON DELETE RESTRICT,
    actor_id              uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    preview_hash          text NOT NULL CHECK (preview_hash ~ '^[0-9a-f]{64}$'),
    idempotency_key       text NOT NULL UNIQUE CHECK (btrim(idempotency_key) <> ''),
    status                text NOT NULL DEFAULT 'applied' CHECK (status IN ('applied', 'rolled_back')),
    result                jsonb NOT NULL,
    contains_revocation   boolean NOT NULL DEFAULT false,
    applied_at            timestamptz NOT NULL DEFAULT now(),
    rolled_back_at        timestamptz,
    rolled_back_by        uuid REFERENCES admin_users(id) ON DELETE RESTRICT
);

CREATE TABLE master_import_apply_changes (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    apply_run_id      uuid NOT NULL REFERENCES master_import_apply_runs(id) ON DELETE RESTRICT,
    sequence_no       integer NOT NULL CHECK (sequence_no > 0),
    entity_kind       text NOT NULL CHECK (entity_kind IN ('branch', 'product', 'variant', 'tariff', 'identity_revocation')),
    entity_id         uuid NOT NULL,
    operation         text NOT NULL CHECK (operation IN ('insert', 'update', 'revoke')),
    before_state      bytea NOT NULL,
    encryption_key_id text NOT NULL CHECK (btrim(encryption_key_id) <> ''),
    after_fingerprint text NOT NULL CHECK (after_fingerprint ~ '^[0-9a-f]{64}$'),
    reversible        boolean NOT NULL DEFAULT true,
    UNIQUE (apply_run_id, sequence_no)
);
CREATE INDEX master_import_apply_changes_run
    ON master_import_apply_changes (apply_run_id, sequence_no DESC);

-- +goose Down
DROP TABLE master_import_apply_changes;
DROP TABLE master_import_apply_runs;
ALTER TABLE master_import_preview_items DROP COLUMN decision_reason;
ALTER TABLE master_import_attempts DROP COLUMN normalized_source;
