-- +goose Up
-- 0005_report_jobs.sql — pekerjaan ekspor laporan XLSX (kartu F2-24,
-- PRD §4.7 AP-REP-04, SystemDesign.md §6/§8).
--
-- Satu baris = satu permintaan ekspor Superadmin: filter yang diminta, status
-- pekerjaan, dan metadata berkas hasilnya. Job River dititipkan DI DALAM
-- transaksi yang sama dengan baris ini, sehingga tidak mungkin ada pekerjaan
-- tanpa baris pendampingnya (SystemDesign.md §4.2).
--
-- Tabel ini SENGAJA terpisah dari `documents`. Tabel `documents` punya
-- endpoint unduh sendiri (/api/v1/documents/{id}/download) yang boleh dipakai
-- Manager (CapAdminOrders); menaruh laporan Superadmin di sana akan
-- membocorkan laporan ke Manager. Unduhan laporan hanya lewat jalur reporting
-- (CapAdminReports, khusus Superadmin).
--
-- Kolom `jenis` disediakan agar kartu lanjutan (F3-32: laporan pembayaran)
-- memakai tabel pekerjaan yang sama tanpa migrasi baru.

CREATE TYPE report_job_status AS ENUM ('queued','proses','selesai','gagal');

CREATE TABLE report_jobs (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    jenis        text NOT NULL DEFAULT 'order_lines',
    diminta_oleh uuid REFERENCES admin_users(id) ON DELETE SET NULL,
    diminta_nama text NOT NULL,
    filter       jsonb NOT NULL DEFAULT '{}'::jsonb,
    status       report_job_status NOT NULL DEFAULT 'queued',
    storage_key  text,
    nama_file    text,
    jumlah_baris bigint,
    pesan        text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    finished_at  timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX report_jobs_jenis_created ON report_jobs (jenis, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS report_jobs;
DROP TYPE IF EXISTS report_job_status;
