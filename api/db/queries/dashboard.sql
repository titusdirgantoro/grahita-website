-- Metrik dashboard admin Pusat (F1-20, PRD §4.1 AP-DSH-01..06).
--
-- Seluruh TANGGAL BISNIS memakai zona Asia/Jakarta (PRD §7.2), bukan UTC:
-- tanggal pesanan dipakai bila ada, dan draft (tanpa tanggal) digolongkan ke
-- hari bisnis `created_at`-nya. Ekspresi
--
--   COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date)
--
-- sengaja ditulis PERSIS sama dengan query riwayat pemesanan admin
-- (db/queries/admin_order.sql), supaya angka dashboard dan daftar terfilter
-- yang dibuka dari chip tidak pernah berbeda.
--
-- Chip "perlu tindakan" TIDAK dibatasi periode (Database.md §7.2): ia keadaan
-- yang menunggu penanganan sekarang, bukan ringkasan periode. Yang membatasinya
-- hanyalah filter cabang — sama seperti daftar yang dibuka saat chip diklik.

-- name: DashboardRingkasan :one
-- Ringkasan periode terpilih: jumlah + nilai pesanan, dan ringkasan status
-- pembayaran (AP-DSH-05) yang tiga keranjangnya saling lepas.
--
-- Draft ikut terhitung selama tanggal bisnisnya di dalam rentang — persis
-- seperti daftar pesanan yang memakai ekspresi tanggal yang sama.
SELECT count(*)::bigint                                                    AS jumlah,
       COALESCE(sum(o.total), 0)::bigint                                   AS nilai,
       count(*) FILTER (WHERE o.status = 'done')::bigint                   AS terverifikasi_jumlah,
       COALESCE(sum(o.total) FILTER (WHERE o.status = 'done'), 0)::bigint  AS terverifikasi_nilai,
       count(*) FILTER (WHERE o.status = 'submitted'
                          AND o.verif_status <> 'perlu_review')::bigint    AS menunggu_jumlah,
       COALESCE(sum(o.total) FILTER (WHERE o.status = 'submitted'
                          AND o.verif_status <> 'perlu_review'), 0)::bigint AS menunggu_nilai,
       count(*) FILTER (WHERE o.status = 'submitted'
                          AND o.verif_status = 'perlu_review')::bigint     AS perlu_review_jumlah,
       COALESCE(sum(o.total) FILTER (WHERE o.status = 'submitted'
                          AND o.verif_status = 'perlu_review'), 0)::bigint  AS perlu_review_nilai
  FROM orders o
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR o.branch_id = sqlc.narg(branch_id)::uuid)
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) >= sqlc.arg(dari)::date
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) <= sqlc.arg(sampai)::date;

-- name: DashboardChip :one
-- Empat chip "perlu tindakan" (AP-DSH-03). Tiap angkanya adalah jumlah pesanan
-- yang akan muncul bila chip diklik, karena klausanya sama dengan filter daftar
-- riwayat (admin_order.sql):
--   menunggu            -> status=submitted & verif=belum
--   perlu_review        -> verif=perlu_review
--   lewat_jatuh_tempo   -> is_cnc & status<>'done' & jatuh_tempo < hari ini (WIB)
--   email_gagal         -> id pesanan ada di daftar email `failed` (domain notification)
--
-- C&C yang lewat jatuh tempo TIDAK diubah statusnya oleh sistem: ia tetap
-- `submitted` dan hanya muncul sebagai chip (PRD CP-CNC-06, §7.1 state machine).
SELECT count(*) FILTER (WHERE o.status = 'submitted'
                          AND o.verif_status = 'belum')::bigint            AS menunggu,
       count(*) FILTER (WHERE o.verif_status = 'perlu_review')::bigint     AS perlu_review,
       count(*) FILTER (WHERE o.is_cnc AND o.status <> 'done'
                          AND o.jatuh_tempo < sqlc.arg(hari_ini)::date)::bigint AS lewat_jatuh_tempo,
       count(*) FILTER (WHERE o.id = ANY(sqlc.arg(email_gagal_ids)::uuid[]))::bigint AS email_gagal
  FROM orders o
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR o.branch_id = sqlc.narg(branch_id)::uuid);

-- name: DashboardTrenBulanan :many
-- Tren per bulan pada tahun bisnis berjalan (AP-DSH-04). Bulan tanpa pesanan
-- tidak muncul di sini; service mengisinya dengan nol supaya grafiknya tetap
-- 12 batang.
SELECT to_char(COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date), 'YYYY-MM') AS kunci,
       count(*)::bigint                                                 AS jumlah,
       COALESCE(sum(o.total), 0)::bigint                                AS nilai,
       count(*) FILTER (WHERE o.status = 'done')::bigint                AS terverifikasi
  FROM orders o
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR o.branch_id = sqlc.narg(branch_id)::uuid)
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) >= sqlc.arg(dari)::date
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) <= sqlc.arg(sampai)::date
 GROUP BY 1
 ORDER BY 1;

-- name: DashboardHarian :many
-- Tren harian bulan bisnis berjalan (AP-DSH-04) — selalu bulan berjalan,
-- tidak terpengaruh periode yang dipilih. Hari tanpa pesanan diisikan nol oleh
-- service.
SELECT to_char(COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date), 'YYYY-MM-DD') AS kunci,
       count(*)::bigint                                                 AS jumlah,
       COALESCE(sum(o.total), 0)::bigint                                AS nilai,
       count(*) FILTER (WHERE o.status = 'done')::bigint                AS terverifikasi
  FROM orders o
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR o.branch_id = sqlc.narg(branch_id)::uuid)
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) >= sqlc.arg(dari)::date
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) <= sqlc.arg(sampai)::date
 GROUP BY 1
 ORDER BY 1;

-- name: DashboardPeringkatCabang :many
-- Peringkat cabang pada periode terpilih (AP-DSH-06), terbesar lebih dahulu.
-- Urutannya dibuat deterministik (jumlah, lalu nilai, lalu nama cabang A→Z)
-- supaya peringkat tidak berubah-ubah saat nilainya sama.
SELECT b.id::text        AS branch_id,
       b.nama            AS nama,
       count(*)::bigint  AS jumlah,
       COALESCE(sum(o.total), 0)::bigint AS nilai
  FROM orders o
  JOIN branches b ON b.id = o.branch_id
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR o.branch_id = sqlc.narg(branch_id)::uuid)
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) >= sqlc.arg(dari)::date
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) <= sqlc.arg(sampai)::date
 GROUP BY b.id, b.nama
 ORDER BY jumlah DESC, nilai DESC, b.nama ASC
 LIMIT sqlc.arg(batas)::int;

-- name: DashboardItem :one
-- Σ qty baris produk (tanpa baris packaging) pesanan pada rentang terpilih —
-- dasar "rata-rata item per pesanan". Filter cabang & tanggal sama dengan
-- DashboardRingkasan sehingga pembaginya konsisten.
SELECT COALESCE(sum(l.qty), 0)::bigint AS item
  FROM orders o
  JOIN order_lines l ON l.order_id = o.id AND l.jenis = 'product'
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR o.branch_id = sqlc.narg(branch_id)::uuid)
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) >= sqlc.arg(dari)::date
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) <= sqlc.arg(sampai)::date;

-- name: DashboardKomposisi :one
-- Komposisi pesanan periode terpilih: reguler vs Cash and Carry, Sertifikat,
-- dan C&C lewat jatuh tempo (klausa lewat tempo sama dengan DashboardChip).
SELECT count(*) FILTER (WHERE NOT o.is_cnc)::bigint                       AS reguler,
       count(*) FILTER (WHERE o.is_cnc)::bigint                           AS cnc,
       count(*) FILTER (WHERE o.is_sertifikat)::bigint                    AS sertifikat,
       count(*) FILTER (WHERE o.is_cnc AND o.status <> 'done'
                          AND o.jatuh_tempo < sqlc.arg(hari_ini)::date)::bigint AS cnc_lewat_tempo
  FROM orders o
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR o.branch_id = sqlc.narg(branch_id)::uuid)
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) >= sqlc.arg(dari)::date
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) <= sqlc.arg(sampai)::date;

-- name: DashboardProdukTerlaris :many
-- Product terlaris periode terpilih dalam qty baris produk, terbanyak lebih
-- dahulu; urutan deterministik (qty, lalu nama A→Z).
SELECT p.id::text            AS product_id,
       p.nama                AS nama,
       sum(l.qty)::bigint    AS qty
  FROM orders o
  JOIN order_lines l ON l.order_id = o.id AND l.jenis = 'product'
  JOIN products p ON p.id = l.product_id
 WHERE (sqlc.narg(branch_id)::uuid IS NULL OR o.branch_id = sqlc.narg(branch_id)::uuid)
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) >= sqlc.arg(dari)::date
   AND COALESCE(o.tanggal, (o.created_at AT TIME ZONE 'Asia/Jakarta')::date) <= sqlc.arg(sampai)::date
 GROUP BY p.id, p.nama
 ORDER BY qty DESC, p.nama ASC
 LIMIT sqlc.arg(batas)::int;
