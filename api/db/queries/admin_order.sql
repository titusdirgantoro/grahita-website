-- Riwayat & detail pesanan untuk admin (F1-15, PRD §4.2 AP-ORD-01..05).
--
-- Berbeda dari order.sql (jalur cabang), query di sini TIDAK dibatasi branch_id
-- sesi: admin Pusat memang melihat seluruh cabang (PRD §2.1). Otorisasi ada di
-- lapisan service (auth.CapAdminOrders), bukan di klausa WHERE.
--
-- Tiga hal yang dijaga di sini:
--   * seluruh tanggal BISNIS memakai zona Asia/Jakarta (PRD §7.2) — bukan UTC —
--     termasuk grouping per hari/bulan/tahun dan penanda "lewat jatuh tempo";
--   * daftar SELALU dipaginasi di server (LIMIT/OFFSET) dan ringkasan grup
--     dihitung agregat, sehingga pesanan besar tidak pernah diunduh seluruhnya
--     ke browser (AP-ORD-03);
--   * penanda email gagal tidak membaca tabel milik domain lain: id pesanan
--     yang emailnya gagal dikirim diserahkan domain notification lewat
--     parameter `email_gagal_ids`.

-- name: ListAdminOrders :many
-- Satu halaman daftar pesanan seluruh cabang (AP-ORD-01/02/03).
--
-- Urutannya stabil: grup lebih dulu (nama cabang ASC untuk grouping per cabang,
-- kunci periode DESC untuk grouping hari/bulan/tahun), lalu tanggal pesanan
-- terbaru, lalu created_at dan id sebagai pemecah seri terakhir supaya halaman
-- berikutnya tidak pernah mengulang/melewatkan baris.
SELECT o.id,
       COALESCE(o.nomor, '')                                       AS nomor,
       o.versi,
       o.branch_id,
       b.nama                                                      AS branch_nama,
       o.tanggal,
       o.jatuh_tempo,
       o.status::text                                              AS status,
       o.verif_status::text                                        AS verif_status,
       o.is_cnc,
       o.is_sertifikat,
       o.total,
       COALESCE((SELECT sum(l.qty) FROM order_lines l
                  WHERE l.order_id = o.id AND l.jenis = 'product'), 0)::bigint AS jumlah_item,
       COALESCE(o.is_cnc AND o.status <> 'done'
                AND o.jatuh_tempo < (now() AT TIME ZONE 'Asia/Jakarta')::date, false)::bool AS terlambat,
       (o.id = ANY(sqlc.arg(email_gagal_ids)::uuid[]))::bool       AS email_gagal,
       o.submitted_at,
       o.created_at,
       o.updated_at
  FROM orders o
  JOIN branches b ON b.id = o.branch_id
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR o.branch_id = sqlc.narg(branch_id)::uuid)
   AND (sqlc.narg(status)::order_status IS NULL OR o.status = sqlc.narg(status)::order_status)
   AND (sqlc.narg(verif_status)::verif_status IS NULL OR o.verif_status = sqlc.narg(verif_status)::verif_status)
   AND (sqlc.narg(is_cnc)::bool IS NULL OR o.is_cnc = sqlc.narg(is_cnc)::bool)
   AND (sqlc.narg(terlambat)::bool IS NULL
        OR (sqlc.narg(terlambat)::bool AND COALESCE(o.is_cnc AND o.status <> 'done'
             AND o.jatuh_tempo < (now() AT TIME ZONE 'Asia/Jakarta')::date, false))
        OR (NOT sqlc.narg(terlambat)::bool AND NOT COALESCE(o.is_cnc AND o.status <> 'done'
             AND o.jatuh_tempo < (now() AT TIME ZONE 'Asia/Jakarta')::date, false)))
   AND (sqlc.narg(email_gagal)::bool IS NULL
        OR (sqlc.narg(email_gagal)::bool AND o.id = ANY(sqlc.arg(email_gagal_ids)::uuid[]))
        OR (NOT sqlc.narg(email_gagal)::bool AND NOT (o.id = ANY(sqlc.arg(email_gagal_ids)::uuid[]))))
   AND (sqlc.narg(dari)::date IS NULL
        OR COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) >= sqlc.narg(dari)::date)
   AND (sqlc.narg(sampai)::date IS NULL
        OR COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) <= sqlc.narg(sampai)::date)
   AND (sqlc.narg(cari)::text IS NULL
        OR o.nomor ILIKE '%' || sqlc.narg(cari)::text || '%'
        OR b.nama ILIKE '%' || sqlc.narg(cari)::text || '%'
        OR EXISTS (SELECT 1 FROM order_lines l
                    WHERE l.order_id = o.id
                      AND l.serial_number ILIKE '%' || sqlc.narg(cari)::text || '%'))
 ORDER BY CASE WHEN sqlc.arg(grup)::text = 'cabang' THEN b.nama END ASC NULLS LAST,
       CASE WHEN sqlc.arg(grup)::text IN ('hari', 'bulan', 'tahun') THEN
         CASE sqlc.arg(grup)::text
           WHEN 'hari'  THEN to_char(COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date), 'YYYY-MM-DD')
           WHEN 'bulan' THEN to_char(COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date), 'YYYY-MM')
           WHEN 'tahun' THEN to_char(COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date), 'YYYY')
         END
       END DESC NULLS LAST,
       COALESCE(o.tanggal::timestamptz, o.created_at) DESC,
       o.created_at DESC,
       o.id DESC
 LIMIT sqlc.arg(limit_n)::int OFFSET sqlc.arg(offset_n)::int;

-- name: CountAdminOrders :one
-- Jumlah SELURUH baris yang cocok filter — paginasi server (AP-ORD-03).
SELECT count(*)::bigint AS total
  FROM orders o
  JOIN branches b ON b.id = o.branch_id
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR o.branch_id = sqlc.narg(branch_id)::uuid)
   AND (sqlc.narg(status)::order_status IS NULL OR o.status = sqlc.narg(status)::order_status)
   AND (sqlc.narg(verif_status)::verif_status IS NULL OR o.verif_status = sqlc.narg(verif_status)::verif_status)
   AND (sqlc.narg(is_cnc)::bool IS NULL OR o.is_cnc = sqlc.narg(is_cnc)::bool)
   AND (sqlc.narg(terlambat)::bool IS NULL
        OR (sqlc.narg(terlambat)::bool AND COALESCE(o.is_cnc AND o.status <> 'done'
             AND o.jatuh_tempo < (now() AT TIME ZONE 'Asia/Jakarta')::date, false))
        OR (NOT sqlc.narg(terlambat)::bool AND NOT COALESCE(o.is_cnc AND o.status <> 'done'
             AND o.jatuh_tempo < (now() AT TIME ZONE 'Asia/Jakarta')::date, false)))
   AND (sqlc.narg(email_gagal)::bool IS NULL
        OR (sqlc.narg(email_gagal)::bool AND o.id = ANY(sqlc.arg(email_gagal_ids)::uuid[]))
        OR (NOT sqlc.narg(email_gagal)::bool AND NOT (o.id = ANY(sqlc.arg(email_gagal_ids)::uuid[]))))
   AND (sqlc.narg(dari)::date IS NULL
        OR COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) >= sqlc.narg(dari)::date)
   AND (sqlc.narg(sampai)::date IS NULL
        OR COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) <= sqlc.narg(sampai)::date)
   AND (sqlc.narg(cari)::text IS NULL
        OR o.nomor ILIKE '%' || sqlc.narg(cari)::text || '%'
        OR b.nama ILIKE '%' || sqlc.narg(cari)::text || '%'
        OR EXISTS (SELECT 1 FROM order_lines l
                    WHERE l.order_id = o.id
                      AND l.serial_number ILIKE '%' || sqlc.narg(cari)::text || '%'));

-- name: GroupAdminOrders :many
-- Ringkasan per grup untuk SELURUH baris yang cocok filter (AP-ORD-02).
-- Kunci periode memakai tanggal bisnis Asia/Jakarta; kunci cabang memakai id
-- cabang dan labelnya nama cabang. Ringkasannya agregat (count + sum), jadi
-- halaman tetap kecil walau pesanannya ratusan ribu.
SELECT CASE sqlc.arg(grup)::text
         WHEN 'cabang' THEN b.id::text
         WHEN 'hari'   THEN to_char(COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date), 'YYYY-MM-DD')
         WHEN 'bulan'  THEN to_char(COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date), 'YYYY-MM')
         WHEN 'tahun'  THEN to_char(COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date), 'YYYY')
         ELSE ''
       END::text                                                  AS kunci,
       CASE WHEN sqlc.arg(grup)::text = 'cabang' THEN b.nama ELSE '' END::text AS label,
       count(*)::bigint                                           AS jumlah,
       COALESCE(sum(o.total), 0)::bigint                          AS total
  FROM orders o
  JOIN branches b ON b.id = o.branch_id
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR o.branch_id = sqlc.narg(branch_id)::uuid)
   AND (sqlc.narg(status)::order_status IS NULL OR o.status = sqlc.narg(status)::order_status)
   AND (sqlc.narg(verif_status)::verif_status IS NULL OR o.verif_status = sqlc.narg(verif_status)::verif_status)
   AND (sqlc.narg(is_cnc)::bool IS NULL OR o.is_cnc = sqlc.narg(is_cnc)::bool)
   AND (sqlc.narg(terlambat)::bool IS NULL
        OR (sqlc.narg(terlambat)::bool AND COALESCE(o.is_cnc AND o.status <> 'done'
             AND o.jatuh_tempo < (now() AT TIME ZONE 'Asia/Jakarta')::date, false))
        OR (NOT sqlc.narg(terlambat)::bool AND NOT COALESCE(o.is_cnc AND o.status <> 'done'
             AND o.jatuh_tempo < (now() AT TIME ZONE 'Asia/Jakarta')::date, false)))
   AND (sqlc.narg(email_gagal)::bool IS NULL
        OR (sqlc.narg(email_gagal)::bool AND o.id = ANY(sqlc.arg(email_gagal_ids)::uuid[]))
        OR (NOT sqlc.narg(email_gagal)::bool AND NOT (o.id = ANY(sqlc.arg(email_gagal_ids)::uuid[]))))
   AND (sqlc.narg(dari)::date IS NULL
        OR COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) >= sqlc.narg(dari)::date)
   AND (sqlc.narg(sampai)::date IS NULL
        OR COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) <= sqlc.narg(sampai)::date)
   AND (sqlc.narg(cari)::text IS NULL
        OR o.nomor ILIKE '%' || sqlc.narg(cari)::text || '%'
        OR b.nama ILIKE '%' || sqlc.narg(cari)::text || '%'
        OR EXISTS (SELECT 1 FROM order_lines l
                    WHERE l.order_id = o.id
                      AND l.serial_number ILIKE '%' || sqlc.narg(cari)::text || '%'))
 GROUP BY 1, 2;

-- name: GetAdminOrder :one
-- Header detail satu pesanan mana pun (AP-ORD-04); tidak dibatasi cabang.
SELECT o.id,
       COALESCE(o.nomor, '')                                       AS nomor,
       o.versi,
       o.branch_id,
       b.nama                                                      AS branch_nama,
       b.nama_kepala                                               AS branch_kepala,
       b.email                                                     AS branch_email,
       b.is_cnc                                                    AS branch_cnc,
       o.status::text                                              AS status,
       o.verif_status::text                                        AS verif_status,
       COALESCE(o.alasan_review::text, '')::text                   AS alasan_review,
       o.is_cnc,
       o.is_sertifikat,
       o.tanggal,
       o.jatuh_tempo,
       COALESCE(o.catatan, '')                                     AS catatan,
       COALESCE(o.note_admin, '')                                  AS note_admin,
       o.total,
       COALESCE((SELECT sum(l.qty) FROM order_lines l
                  WHERE l.order_id = o.id AND l.jenis = 'product'), 0)::bigint AS jumlah_item,
       COALESCE(o.is_cnc AND o.status <> 'done'
                AND o.jatuh_tempo < (now() AT TIME ZONE 'Asia/Jakarta')::date, false)::bool AS terlambat,
       (o.id = ANY(sqlc.arg(email_gagal_ids)::uuid[]))::bool       AS email_gagal,
       o.submitted_at,
       o.done_at,
       o.created_at,
       o.updated_at
  FROM orders o
  JOIN branches b ON b.id = o.branch_id
 WHERE o.id = sqlc.arg(id)::uuid;

-- name: UpdateOrderNoteAdmin :execrows
-- Catatan internal pesanan (AP-ORD-05). Kolom tunggal `note_admin` mengikuti
-- Database.md §4.4 dan perilaku prototype ("Note admin" satu textarea), jadi
-- penyimpanan mengganti isi catatan; riwayat siapa-kapan ada di order_events
-- yang ditulis repository pada transaksi yang sama.
UPDATE orders
   SET note_admin = sqlc.arg(note_admin),
       updated_at = now()
 WHERE id = sqlc.arg(id)::uuid;

-- name: ListOrderEvents :many
SELECT e.at,
       COALESCE(e.icon, '')            AS icon,
       e.teks,
       e.actor_kind::text              AS actor_kind,
       COALESCE(e.actor_id::text, '')::text AS actor_id
  FROM order_events e
 WHERE e.order_id = sqlc.arg(order_id)::uuid
 ORDER BY e.at, e.id;

-- name: ListOrderDocuments :many
-- Daftar dokumen pesanan (invoice/surat jalan). `storage_key` tidak pernah
-- keluar dari repository — unduhan dokumen memakai presigned URL domain
-- dokumen (F1-18/F2-25), bukan kunci mentah.
SELECT d.id,
       d.jenis::text                   AS jenis,
       d.versi,
       COALESCE(d.ukuran_byte, 0)::bigint AS ukuran_byte,
       d.created_at
  FROM documents d
 WHERE d.order_id = sqlc.arg(order_id)::uuid
 ORDER BY d.versi, d.jenis;

-- name: ListVerificationLogs :many
-- Log verifikasi pesanan (AP-ORD-04) — ditulis domain verifikasi (F1-18).
SELECT v.id,
       v.metode::text                          AS metode,
       v.total_pesanan,
       COALESCE(v.total_terbaca, 0)::bigint    AS total_terbaca,
       v.selisih,
       v.hasil::text                           AS hasil,
       COALESCE(v.alasan::text, '')::text      AS alasan,
       COALESCE(v.catatan, '')                 AS catatan,
       COALESCE(a.nama, '')                    AS admin_nama,
       v.created_at
  FROM verification_logs v
  LEFT JOIN admin_users a ON a.id = v.admin_id
 WHERE v.order_id = sqlc.arg(order_id)::uuid
 ORDER BY v.created_at DESC, v.id DESC;
