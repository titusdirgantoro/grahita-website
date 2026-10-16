-- +goose Up
-- Metadata preview dan model tarif berversi Impact. Migrasi ini hanya menambah
-- struktur; parser preview tidak menulis master cabang/produk/tarif.

CREATE TABLE master_import_artifacts (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    environment      text NOT NULL CHECK (environment <> '' AND environment <> 'production'),
    source_system    text NOT NULL,
    source_hash      text NOT NULL CHECK (source_hash ~ '^[0-9a-f]{64}$'),
    contract_version text NOT NULL,
    source_sheet     text NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (environment, source_hash, contract_version)
);

CREATE TABLE master_import_attempts (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    artifact_id   uuid NOT NULL REFERENCES master_import_artifacts(id) ON DELETE RESTRICT,
    actor_id      uuid NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    preview_hash  text NOT NULL CHECK (preview_hash ~ '^[0-9a-f]{64}$'),
    counts        jsonb NOT NULL,
    status        text NOT NULL DEFAULT 'previewed' CHECK (status IN ('previewed', 'resolved', 'applied', 'failed')),
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX master_import_attempts_artifact_created
    ON master_import_attempts (artifact_id, created_at DESC);

CREATE TABLE master_import_preview_items (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    attempt_id     uuid NOT NULL REFERENCES master_import_attempts(id) ON DELETE CASCADE,
    source_row     integer NOT NULL CHECK (source_row >= 0),
    entity_kind    text NOT NULL CHECK (entity_kind IN ('branch', 'product', 'variant', 'tariff', 'note')),
    entity_code    text NOT NULL,
    fingerprint    text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    existing_fingerprint text CHECK (existing_fingerprint IS NULL OR existing_fingerprint ~ '^[0-9a-f]{64}$'),
    preview_status text NOT NULL CHECK (preview_status IN ('insert', 'update', 'skip', 'conflict', 'needs_review')),
    reason_code    text,
    private_evidence text,
    decision       text,
    decided_by     uuid REFERENCES admin_users(id) ON DELETE RESTRICT,
    decided_at     timestamptz,
    UNIQUE (attempt_id, source_row, entity_kind, entity_code)
);
CREATE INDEX master_import_preview_items_attempt_status
    ON master_import_preview_items (attempt_id, preview_status);

CREATE TABLE master_import_identities (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    source_system text NOT NULL,
    entity_kind   text NOT NULL,
    source_key    text NOT NULL,
    entity_id     uuid NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_system, entity_kind, source_key),
    UNIQUE (source_system, entity_kind, entity_id)
);

CREATE TABLE product_variants (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    product_id               uuid NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    code                     text NOT NULL UNIQUE,
    nama                     text NOT NULL,
    tariff_parent_variant_id uuid REFERENCES product_variants(id) ON DELETE RESTRICT,
    is_active                boolean NOT NULL DEFAULT true,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    CHECK (tariff_parent_variant_id IS NULL OR tariff_parent_variant_id <> id)
);

CREATE TABLE branch_tariff_versions (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    branch_id             uuid NOT NULL REFERENCES branches(id) ON DELETE RESTRICT,
    variant_id            uuid NOT NULL REFERENCES product_variants(id) ON DELETE RESTRICT,
    hjm_rupiah            bigint CHECK (hjm_rupiah IS NULL OR hjm_rupiah >= 0),
    p1_ratio              numeric CHECK (p1_ratio IS NULL OR (p1_ratio >= 0 AND p1_ratio <= 1)),
    spv_rupiah            bigint CHECK (spv_rupiah IS NULL OR spv_rupiah >= 0),
    final_item_rupiah     bigint CHECK (final_item_rupiah IS NULL OR final_item_rupiah >= 0),
    is_available          boolean NOT NULL,
    source_import_attempt_id uuid NOT NULL REFERENCES master_import_attempts(id) ON DELETE RESTRICT,
    source_sheet          text NOT NULL,
    source_row            integer NOT NULL CHECK (source_row >= 3),
    source_columns        text NOT NULL,
    source_component_hash text NOT NULL CHECK (source_component_hash ~ '^[0-9a-f]{64}$'),
    valid_from            timestamptz NOT NULL DEFAULT now(),
    superseded_at         timestamptz,
    CHECK (
        (is_available AND hjm_rupiah IS NOT NULL AND p1_ratio IS NOT NULL
            AND spv_rupiah IS NOT NULL AND final_item_rupiah IS NOT NULL)
        OR
        (NOT is_available AND hjm_rupiah IS NULL AND p1_ratio IS NULL
            AND spv_rupiah IS NULL AND final_item_rupiah IS NULL)
    )
);
CREATE UNIQUE INDEX branch_tariff_versions_one_active
    ON branch_tariff_versions (branch_id, variant_id) WHERE superseded_at IS NULL;
CREATE INDEX branch_tariff_versions_history
    ON branch_tariff_versions (branch_id, variant_id, valid_from DESC);

-- +goose Down
DROP TABLE branch_tariff_versions;
DROP TABLE product_variants;
DROP TABLE master_import_identities;
DROP TABLE master_import_preview_items;
DROP TABLE master_import_attempts;
DROP TABLE master_import_artifacts;
