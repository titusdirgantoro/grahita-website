-- Verifikasi pembayaran pesanan (F1-18; PRD §4.2 AP-ORD-07, §5, §7;
-- SystemDesign.md §4.3–4.4; Database.md §4.4–4.6).
--
-- SATU permintaan verifikasi = SATU transaksi. Urutannya:
--
--   LockOrderForVerify  -> baris pesanan dikunci dan keadaan sebelumnya dibaca
--   SetOrderDone        -> status done + verif cocok + done_at (persetujuan)
--   SetOrderPerluReview -> verif perlu_review + alasan (keputusan review)
--   InsertVerificationLog -> catatan hasil (nominal terbaca + selisih generated)
--   InsertOrderEvent    -> timeline
--
-- Karena baris pesanan dikunci lebih dahulu, dua verifikasi yang bersamaan
-- berbaris rapi: yang kedua melihat hasil yang pertama dan dijawab
-- ORDER_ALREADY_DONE tanpa menulis log/dokumen/email kedua kali.
--
-- Kolom enum dikembalikan sebagai teks (`status::text`) agar lapisan repository
-- tidak perlu mengimpor tipe enum hasil generate — sama seperti query lain.

-- name: LockOrderForVerify :one
-- Mengunci baris pesanan selama transaksi verifikasi sekaligus membaca keadaan
-- sebelumnya (status, verif, alasan, total, nomor) untuk audit Before/After.
SELECT o.id,
       o.branch_id,
       COALESCE(o.nomor, '')::text                   AS nomor,
       o.versi,
       o.status::text                                AS status,
       o.verif_status::text                          AS verif_status,
       COALESCE(o.alasan_review::text, '')::text     AS alasan_review,
       o.total,
       o.done_at
  FROM orders o
 WHERE o.id = sqlc.arg(id)::uuid
 FOR UPDATE;

-- name: SetOrderDone :execrows
-- Persetujuan verifikasi: status berpindah ke done (PRD §7.1) dan verif_status
-- menjadi cocok. `done_at` diisi DI SINI — constraint
-- `done_punya_waktu` menuntut status done selalu punya done_at. Klausa WHERE
-- memeriksa ulang status sehingga transisi tidak bisa menembus dua kali.
UPDATE orders
   SET status        = 'done',
       verif_status  = 'cocok',
       alasan_review = NULL,
       done_at       = now(),
       updated_at    = now()
 WHERE id = sqlc.arg(id)::uuid
   AND status = 'submitted';

-- name: SetOrderPerluReview :execrows
-- Keputusan review: pesanan TETAP submitted (sub-status verifikasi, PRD §7.1)
-- dan alasannya dicatat. Status pesanan sengaja tidak disentuh.
UPDATE orders
   SET verif_status  = 'perlu_review',
       alasan_review = sqlc.arg(alasan)::review_reason,
       updated_at    = now()
 WHERE id = sqlc.arg(id)::uuid
   AND status = 'submitted';

-- name: InsertVerificationLog :one
-- Satu baris log verifikasi (Database.md §4.5). `selisih` adalah kolom
-- GENERATED ALWAYS (total_terbaca - total_pesanan) sehingga tidak pernah
-- datang dari klien; `total_terbaca` NULL berarti nominal belum terbaca.
-- admin_id NULL berarti verifikasi oleh AI (Fase 2) — manual selalu mengisi.
INSERT INTO verification_logs (order_id, metode, total_pesanan, total_terbaca,
                               hasil, alasan, admin_id, catatan)
VALUES (sqlc.arg(order_id)::uuid,
        sqlc.arg(metode)::verify_method,
        sqlc.arg(total_pesanan),
        sqlc.narg(total_terbaca),
        sqlc.arg(hasil)::verif_status,
        sqlc.narg(alasan)::review_reason,
        sqlc.narg(admin_id)::uuid,
        sqlc.narg(catatan))
RETURNING id, order_id, metode::text AS metode, total_pesanan,
          COALESCE(total_terbaca, 0)::bigint AS total_terbaca, selisih,
          hasil::text AS hasil, COALESCE(alasan::text, '')::text AS alasan,
          COALESCE(catatan, '')::text AS catatan, admin_id, created_at;

-- name: InsertVerifyOrderEvent :exec
-- Timeline pesanan (PRD AP-ORD-07/CP-ORD-08): keputusan verifikasi dan
-- pelakunya. Icon membedakan persetujuan dari penandaan perlu review.
INSERT INTO order_events (order_id, icon, teks, actor_kind, actor_id)
VALUES (sqlc.arg(order_id)::uuid, sqlc.arg(icon), sqlc.arg(teks),
        sqlc.arg(actor_kind)::actor_kind, sqlc.narg(actor_id)::uuid);
