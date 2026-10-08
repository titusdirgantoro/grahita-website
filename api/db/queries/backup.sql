-- Query domain cadangan (kartu F1-21): jejak pekerjaan di tabel backup_runs
-- (Database.md §4.7) dan data pendukung sinkronisasi/rekonsiliasi.

-- name: InsertBackupRun :one
INSERT INTO backup_runs (jenis, mulai)
VALUES ($1, $2)
RETURNING id, jenis, mulai, selesai, sukses, ukuran_byte, tujuan, pesan;

-- name: SelesaiBackupRun :exec
UPDATE backup_runs
SET selesai     = $2,
    sukses      = $3,
    ukuran_byte = $4,
    tujuan      = $5,
    pesan       = $6
WHERE id = $1;

-- name: ListBackupRuns :many
SELECT id, jenis, mulai, selesai, sukses, ukuran_byte, tujuan, pesan
FROM backup_runs
ORDER BY mulai DESC
LIMIT $1;

-- name: BackupRunSejak :one
SELECT id, jenis, mulai, selesai, sukses, ukuran_byte, tujuan, pesan
FROM backup_runs
WHERE jenis = $1 AND mulai >= $2
ORDER BY mulai DESC
LIMIT 1;

-- name: ListSuperadminEmails :many
SELECT email
FROM admin_users
WHERE role = 'superadmin' AND is_active AND activated_at IS NOT NULL
ORDER BY email;

-- name: ListKunciBukti :many
SELECT storage_key
FROM payment_proofs
ORDER BY storage_key;

-- name: ListKunciDokumen :many
SELECT storage_key
FROM documents
ORDER BY storage_key;
