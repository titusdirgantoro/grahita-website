-- +goose Up
-- Retry revisi invoice harus mengembalikan hasil transaksi pertama tanpa
-- membuat versi, dokumen, email, timeline, atau audit kedua.
CREATE TABLE invoice_revision_requests (
    admin_id uuid NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    idempotency_key_hash bytea NOT NULL,
    order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    request_fingerprint bytea NOT NULL,
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (admin_id, idempotency_key_hash)
);

CREATE INDEX invoice_revision_requests_order_idx
    ON invoice_revision_requests (order_id, created_at DESC);

-- +goose Down
DROP TABLE invoice_revision_requests;
