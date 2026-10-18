-- +goose Up
-- Cash & Carry = cabang berdeposit (keputusan user 2026-10-18): pesanan C&C
-- dibayar dari saldo deposit saat dikirim (sisanya transfer berbukti), sehingga
-- tidak ada lagi utang C&C maupun jatuh tempo. Kolom jatuh_tempo dipertahankan
-- sebagai riwayat pesanan C&C lama; constraint yang MEWAJIBKANNYA diganti
-- constraint yang hanya membolehkannya pada pesanan C&C.
-- migration-check: allow-constraint-replacement
-- Ekspresi constraint lama dibuktikan terhadap parse tree kanonik sebelum
-- diganti atomik, seperti 0012.

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conrelid = 'orders'::regclass
       AND conname = '__c1_expected_cnc_punya_jatuh_tempo'
  ) THEN
    RAISE EXCEPTION 'nama constraint pembanding sudah terpakai';
  END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE orders
  ADD CONSTRAINT __c1_expected_cnc_punya_jatuh_tempo CHECK (
    NOT is_cnc OR status = 'draft' OR jatuh_tempo IS NOT NULL
  ) NOT VALID;
-- +goose StatementBegin
DO $$
DECLARE actual_expr text; expected_expr text;
BEGIN
  SELECT pg_get_expr(conbin, conrelid, false) INTO actual_expr
    FROM pg_constraint
   WHERE conrelid = 'orders'::regclass AND conname = 'cnc_punya_jatuh_tempo';
  IF FOUND THEN
    SELECT pg_get_expr(conbin, conrelid, false) INTO STRICT expected_expr
      FROM pg_constraint
     WHERE conrelid = 'orders'::regclass
       AND conname = '__c1_expected_cnc_punya_jatuh_tempo';
    IF actual_expr <> expected_expr THEN
      RAISE EXCEPTION 'constraint orders.cnc_punya_jatuh_tempo mempunyai ekspresi asing';
    END IF;
  END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE orders
  DROP CONSTRAINT __c1_expected_cnc_punya_jatuh_tempo,
  DROP CONSTRAINT IF EXISTS cnc_punya_jatuh_tempo,
  ADD CONSTRAINT jatuh_tempo_hanya_cnc CHECK (jatuh_tempo IS NULL OR is_cnc);

-- Tutorial "Pesanan Cash & Carry" bawaan 0006 masih menjelaskan jatuh tempo.
-- Hanya baris yang isinya persis seed lama yang diperbarui; tutorial yang sudah
-- diedit Superadmin tidak disentuh.
UPDATE tutorial_topics
   SET ringkas = 'Pesanan dibayar dari saldo deposit cabang yang sudah diverifikasi Pusat.',
       langkah = '["Cash & Carry berlaku untuk cabang yang ditetapkan Pusat; pesanannya dibayar dari saldo deposit.",
       "Top-up deposit lewat menu Deposit: transfer ke rekening cabang lalu unggah buktinya.",
       "Saldo bertambah setelah top-up diverifikasi Pusat; top-up yang masih menunggu belum bisa dipakai.",
       "Saat pesanan dikirim, saldo deposit langsung dipotong sebesar total pesanan.",
       "Bila saldo kurang, transfer sisanya dan unggah buktinya saat mengirim pesanan.",
       "Pesanan selesai setelah pembayarannya diverifikasi Pusat."]'::jsonb,
       updated_at = now()
 WHERE ikon = 'cnc'
   AND ringkas = 'Bayar sebelum jatuh tempo yang dihitung dari tanggal pesanan.'
   AND langkah = '["Opsi Cash & Carry hanya muncul untuk cabang yang diizinkan Pusat.",
       "Saat mengirim pesanan, aktifkan centang Cash & Carry; bukti transfer boleh menyusul.",
       "Jatuh tempo = tanggal pesanan + jumlah hari C&C yang berlaku (bawaan 7 hari).",
       "Bayar sebelum tanggal jatuh tempo; pesanan baru selesai setelah pembayaran diverifikasi Pusat.",
       "Pengingat dikirim ke email cabang mendekati dan pada hari jatuh tempo."]'::jsonb;

-- +goose Down
-- Rollback ke kode lama yang selalu mengisi jatuh tempo C&C. Pesanan C&C
-- berdeposit yang lahir sesudah migrasi naik tidak punya jatuh tempo, jadi
-- constraint lama dipasang NOT VALID: baris baru tetap diperiksa, baris itu
-- tidak membuat rollback gagal.
ALTER TABLE orders DROP CONSTRAINT IF EXISTS jatuh_tempo_hanya_cnc;
ALTER TABLE orders
  ADD CONSTRAINT cnc_punya_jatuh_tempo CHECK (
    NOT is_cnc OR status = 'draft' OR jatuh_tempo IS NOT NULL
  ) NOT VALID;

UPDATE tutorial_topics
   SET ringkas = 'Bayar sebelum jatuh tempo yang dihitung dari tanggal pesanan.',
       langkah = '["Opsi Cash & Carry hanya muncul untuk cabang yang diizinkan Pusat.",
       "Saat mengirim pesanan, aktifkan centang Cash & Carry; bukti transfer boleh menyusul.",
       "Jatuh tempo = tanggal pesanan + jumlah hari C&C yang berlaku (bawaan 7 hari).",
       "Bayar sebelum tanggal jatuh tempo; pesanan baru selesai setelah pembayaran diverifikasi Pusat.",
       "Pengingat dikirim ke email cabang mendekati dan pada hari jatuh tempo."]'::jsonb,
       updated_at = now()
 WHERE ikon = 'cnc'
   AND ringkas = 'Pesanan dibayar dari saldo deposit cabang yang sudah diverifikasi Pusat.'
   AND langkah = '["Cash & Carry berlaku untuk cabang yang ditetapkan Pusat; pesanannya dibayar dari saldo deposit.",
       "Top-up deposit lewat menu Deposit: transfer ke rekening cabang lalu unggah buktinya.",
       "Saldo bertambah setelah top-up diverifikasi Pusat; top-up yang masih menunggu belum bisa dipakai.",
       "Saat pesanan dikirim, saldo deposit langsung dipotong sebesar total pesanan.",
       "Bila saldo kurang, transfer sisanya dan unggah buktinya saat mengirim pesanan.",
       "Pesanan selesai setelah pembayarannya diverifikasi Pusat."]'::jsonb;
