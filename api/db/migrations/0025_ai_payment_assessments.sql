-- +goose Up
-- Pemeriksaan AI adalah saran untuk admin, bukan persetujuan pembayaran.
-- Riwayat verification_logs metode ai yang lama tetap utuh untuk pesanan
-- yang sudah diputuskan sebelum perubahan alur ini.
CREATE TABLE payment_ai_assessments (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    order_version int NOT NULL,
    branch_allowlist_version bigint NOT NULL,
    proof_set_fingerprint bytea NOT NULL,
    result text NOT NULL CHECK (result IN ('cocok', 'perlu_review')),
    reason text,
    total_read bigint,
    confidence numeric(4,3) NOT NULL,
    proof_count int NOT NULL CHECK (proof_count > 0),
    note text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, order_version, branch_allowlist_version, proof_set_fingerprint),
    CONSTRAINT payment_ai_assessment_reason CHECK (
        (result = 'cocok' AND reason IS NULL) OR
        (result = 'perlu_review' AND reason IS NOT NULL)
    )
);

CREATE INDEX payment_ai_assessments_order_latest
    ON payment_ai_assessments (order_id, created_at DESC);

-- +goose Down
DROP TABLE payment_ai_assessments;
