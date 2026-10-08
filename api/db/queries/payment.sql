-- Bukti transfer pesanan (F1-13, Database.md §4.5).
--
-- Berkasnya sendiri TIDAK pernah disimpan di database: yang tersimpan hanya
-- `storage_key` pada object storage S3-compatible. Satu pesanan boleh punya
-- beberapa bukti (pembayaran cicilan/terpisah, PRD CP-ORD-07); yang sama
-- (order_id + sha256) ditolak oleh indeks unik `UNIQUE (order_id, sha256)`.
--
-- Kolom enum dikembalikan sebagai teks (`uploaded_by_kind::text`) agar lapisan
-- repository tidak perlu mengimpor tipe enum hasil generate — sama seperti
-- query pesanan.
--
-- Nama query memakai awalan `PaymentProof`/`Proof` supaya tidak bertabrakan
-- dengan query domain pesanan (mis. `InsertOrderEvent` milik F1-14).

-- name: CountPaymentProofs :one
-- Jumlah bukti satu pesanan; dipakai menegakkan batas `max_file` SERVER
-- (bukan hanya validasi klien di UI). Selalu dipanggil DI DALAM transaksi
-- CommitProofs, setelah baris pesanan dikunci (LockOrderForProof), supaya
-- hitung-dan-tulis tidak bisa dilampaui unggahan paralel.
SELECT count(*)::bigint FROM payment_proofs WHERE order_id = sqlc.arg(order_id)::uuid;

-- name: Sha256PaymentProofs :many
-- Sidik jari isi seluruh bukti satu pesanan, terlama dulu. Dibaca SEBELUM
-- berkas ditulis ke object storage supaya duplikat ditolak tanpa menyentuh
-- storage; indeks unik (order_id, sha256) tetap menjadi benteng terakhir.
SELECT sha256 FROM payment_proofs
 WHERE order_id = sqlc.arg(order_id)::uuid
 ORDER BY created_at, id;

-- name: LockOrderForProof :one
-- Mengunci baris pesanan selama transaksi unggah bukti: hitung + insert jadi
-- aman-balapan per pesanan (dua permintaan bersamaan tidak bisa sama-sama
-- membaca hitungan di bawah batas).
SELECT o.id FROM orders o WHERE o.id = sqlc.arg(id)::uuid FOR UPDATE;

-- name: InsertPaymentProof :one
-- Menyimpan metadata satu bukti. sha256 NOT NULL sehingga duplikat isi pada
-- pesanan yang sama ditolak PostgreSQL (23505) — bukan hanya oleh service.
INSERT INTO payment_proofs (
    order_id, storage_key, nama_file, mime_type, ukuran_byte, sha256,
    uploaded_by_kind, uploaded_by_id, uploaded_by_nama
) VALUES (
    sqlc.arg(order_id)::uuid, sqlc.arg(storage_key), sqlc.arg(nama_file),
    sqlc.arg(mime_type), sqlc.arg(ukuran_byte), sqlc.arg(sha256),
    sqlc.arg(uploaded_by_kind)::actor_kind, sqlc.narg(uploaded_by_id)::uuid,
    sqlc.narg(uploaded_by_nama)
)
RETURNING id, order_id, storage_key, nama_file, mime_type, ukuran_byte, sha256,
          uploaded_by_kind::text AS uploaded_by_kind, uploaded_by_id, uploaded_by_nama,
          created_at;

-- name: ListPaymentProofs :many
-- Seluruh bukti satu pesanan, terlama dulu (urutan unggah).
SELECT id, order_id, storage_key, nama_file, mime_type, ukuran_byte, sha256,
       uploaded_by_kind::text AS uploaded_by_kind, uploaded_by_id, uploaded_by_nama,
       created_at
  FROM payment_proofs
 WHERE order_id = sqlc.arg(order_id)::uuid
 ORDER BY created_at, id;

-- name: GetPaymentProof :one
-- Satu bukti MILIK pesanan tersebut; bukti pesanan lain tidak ditemukan.
SELECT id, order_id, storage_key, nama_file, mime_type, ukuran_byte, sha256,
       uploaded_by_kind::text AS uploaded_by_kind, uploaded_by_id, uploaded_by_nama,
       created_at
  FROM payment_proofs
 WHERE id = sqlc.arg(id)::uuid
   AND order_id = sqlc.arg(order_id)::uuid;

-- name: GetOrderForProof :one
-- Konteks pesanan yang dibutuhkan domain bukti: cabang pemilik, status, nomor.
-- Dibaca lewat query sendiri (bukan repository pesanan) supaya paket bukti
-- tidak bergantung pada paket pesanan — pola yang sama dipakai domain email.
SELECT o.id, o.branch_id, o.status::text AS status, o.nomor
  FROM orders o
 WHERE o.id = sqlc.arg(id)::uuid;

-- name: InsertProofOrderEvent :exec
-- Menandai unggahan bukti di timeline pesanan (PRD CP-ORD-08, AP-ORD-06).
-- Aktor unggahan tercatat apa adanya: nama CABANG atau nama ADMIN.
INSERT INTO order_events (order_id, icon, teks, actor_kind, actor_id)
VALUES (sqlc.arg(order_id)::uuid, sqlc.arg(icon), sqlc.arg(teks),
        sqlc.arg(actor_kind)::actor_kind, sqlc.narg(actor_id)::uuid);
