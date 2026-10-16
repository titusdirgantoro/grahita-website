-- +goose Up
-- Identitas cabang dapat belum lengkap dan katalog tidak memakai satuan.
-- Data existing tetap lengkap: cabang dengan akun menjadi registered, sisanya ready.

CREATE TYPE branch_identity_status AS ENUM ('identity_incomplete', 'ready', 'registered');

ALTER TABLE branches
    ALTER COLUMN nama_kepala DROP NOT NULL,
    ALTER COLUMN email DROP NOT NULL,
    ADD COLUMN jabatan_kepala text,
    ADD COLUMN identity_status branch_identity_status;

UPDATE branches b
   SET identity_status = CASE
       WHEN EXISTS (SELECT 1 FROM branch_users bu WHERE bu.branch_id = b.id)
           THEN 'registered'::branch_identity_status
       ELSE 'ready'::branch_identity_status
   END;

ALTER TABLE branches
    ALTER COLUMN identity_status SET NOT NULL,
    ALTER COLUMN identity_status SET DEFAULT 'identity_incomplete',
    ADD CONSTRAINT branches_identity_ready_complete CHECK (
        identity_status = 'identity_incomplete'
        OR (nama_kepala IS NOT NULL AND btrim(nama_kepala) <> ''
            AND email IS NOT NULL AND btrim(email::text) <> '')
    );

ALTER TABLE registration_tokens ADD COLUMN revoked_at timestamptz;
ALTER TABLE password_reset_tokens ADD COLUMN revoked_at timestamptz;

-- Index lama tetap dipertahankan selama fase expand agar binary pra-0008 tetap
-- kompatibel. Token yang dicabut juga ditandai used_at, sehingga constraint
-- lama tetap mengizinkan penerbitan token pengganti tanpa DROP/RECREATE index.

-- Cabang yang sudah tidak lengkap harus langsung fail-closed, termasuk state
-- lama yang sebelumnya tidak mempunyai konsep status identitas.
UPDATE branch_users bu
   SET is_active = false,
       password_hash = '!identity-revoked!',
       updated_at = now()
  FROM branches b
 WHERE b.id = bu.branch_id
   AND b.identity_status = 'identity_incomplete'
   AND (bu.is_active OR bu.password_hash <> '!identity-revoked!');
UPDATE registration_tokens t
   SET revoked_at = now(), used_at = COALESCE(used_at, now())
  FROM branches b
 WHERE b.id = t.branch_id
   AND b.identity_status = 'identity_incomplete'
   AND t.used_at IS NULL
   AND t.revoked_at IS NULL;
UPDATE password_reset_tokens t
   SET revoked_at = now()
  FROM branch_users bu
  JOIN branches b ON b.id = bu.branch_id
 WHERE t.actor_kind = 'branch'
   AND t.user_id = bu.id
   AND b.identity_status = 'identity_incomplete'
   AND t.used_at IS NULL
   AND t.revoked_at IS NULL;
UPDATE sessions s
   SET revoked_at = now()
  FROM branches b
 WHERE b.id = s.branch_id
   AND b.identity_status = 'identity_incomplete'
   AND s.revoked_at IS NULL;

-- Kolom satuan lama sengaja dipertahankan sebagai alias transisi untuk binary
-- lama. Binary baru tidak membaca/menulisnya; DEFAULT historis menjaga insert
-- baru tetap kompatibel. Penghapusan fisik dilakukan pada migrasi contract di
-- rilis berikutnya setelah seluruh klien lama dihentikan.

-- +goose Down
-- Rollback hanya aman selama tidak ada cabang incomplete/nullable. Menolak
-- rollback lebih aman daripada menghidupkan kembali credential lama lewat
-- binary lama yang belum mengenal identity_status.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM branches
         WHERE identity_status = 'identity_incomplete'
            OR nama_kepala IS NULL
            OR email IS NULL
    ) THEN
        RAISE EXCEPTION 'rollback 0008 ditolak: identitas cabang belum lengkap';
    END IF;
END $$;
-- +goose StatementEnd

-- Binary pra-0008 tidak mengenal revoked_at. Hapus token yang sudah dicabut
-- sebelum kolomnya dilepas agar rollback tidak menghidupkannya kembali.
DELETE FROM password_reset_tokens WHERE revoked_at IS NOT NULL;
DELETE FROM registration_tokens WHERE revoked_at IS NOT NULL;

ALTER TABLE password_reset_tokens DROP COLUMN revoked_at;
ALTER TABLE registration_tokens DROP COLUMN revoked_at;

ALTER TABLE branches DROP CONSTRAINT branches_identity_ready_complete;
ALTER TABLE branches DROP COLUMN identity_status;
ALTER TABLE branches DROP COLUMN jabatan_kepala;
ALTER TABLE branches ALTER COLUMN email SET NOT NULL;
ALTER TABLE branches ALTER COLUMN nama_kepala SET NOT NULL;

DROP TYPE branch_identity_status;
