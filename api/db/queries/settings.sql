-- Pengaturan sistem (PRD §4.9). Nilai disimpan sebagai jsonb.

-- name: ListSettings :many
SELECT key, value, updated_at
  FROM settings
 ORDER BY key;

-- name: GetSetting :one
SELECT value
  FROM settings
 WHERE key = $1;

-- name: UpsertSetting :exec
INSERT INTO settings (key, value, updated_by)
VALUES (sqlc.arg(key), sqlc.arg(value), sqlc.arg(updated_by))
ON CONFLICT (key) DO UPDATE
   SET value      = EXCLUDED.value,
       updated_by = EXCLUDED.updated_by,
       updated_at = now();
