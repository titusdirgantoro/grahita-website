-- +goose Up
-- Enum additions are committed separately: PostgreSQL cannot use a new enum
-- value in another statement of the same transaction.
ALTER TYPE order_status ADD VALUE IF NOT EXISTS 'approved_unpaid';
ALTER TYPE payment_proof_purpose ADD VALUE IF NOT EXISTS 'tempo_repayment';
ALTER TYPE email_kind ADD VALUE IF NOT EXISTS 'tempo_statement';
ALTER TYPE email_kind ADD VALUE IF NOT EXISTS 'tempo_reminder';

-- +goose Down
-- PostgreSQL does not safely remove enum values while rows may use them.
SELECT 1;
