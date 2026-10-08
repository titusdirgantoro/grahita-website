-- pg-dev-shim.sql — HANYA untuk pengembangan lokal.
--
-- Target resmi proyek adalah PostgreSQL 18, yang sudah menyediakan uuidv7()
-- bawaan (Architecture.md §4). Bila Anda menjalankan suite pada PostgreSQL
-- versi lebih lama (mis. paket distribusi Linux yang masih 16), jalankan
-- skrip ini pada database uji SEBELUM migrasi:
--
--   psql "$TEST_DATABASE_URL" -f scripts/pg-dev-shim.sql
--
-- Skrip ini tidak dipakai di CI (CI memakai image postgres:18) dan tidak
-- boleh diterapkan di produksi.

CREATE OR REPLACE FUNCTION uuidv7() RETURNS uuid
LANGUAGE sql VOLATILE AS $$
    SELECT (
        -- 48 bit pertama: milidetik Unix
        lpad(to_hex((extract(epoch FROM clock_timestamp()) * 1000)::bigint), 12, '0')
        -- rand_a (12 bit)
        || substr(md5(random()::text || clock_timestamp()::text), 1, 3)
        -- versi 7
        || '7'
        -- rand_b (62 bit), varian RFC 4122 (10xx -> '8')
        || substr(md5(random()::text || clock_timestamp()::text || 'a'), 1, 3)
        || '8'
        || substr(md5(random()::text || clock_timestamp()::text || 'b'), 1, 12)
    )::uuid;
$$;
