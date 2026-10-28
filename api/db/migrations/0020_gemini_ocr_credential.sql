-- +goose Up
-- Kredensial OCR disimpan terpisah dari settings agar endpoint pengaturan
-- umum dan audit tidak pernah mengembalikan ciphertext atau API key.
CREATE TABLE ocr_credentials (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    verified_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid NOT NULL REFERENCES admin_users(id)
);

-- +goose Down
DROP TABLE ocr_credentials;
