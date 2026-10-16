# C0 — Sub-state revisi pembayaran dan gerbang LUNAS D3

Status dokumen: keputusan desain final untuk implementasi C1 dan kartu D3 sesudahnya.
Dokumen ini adalah delta normatif terhadap `branch-deposit.md`. Bila ada konflik
khusus revisi order Done yang totalnya berubah, dokumen ini yang berlaku.

## 1. Ruang lingkup dan dasar keputusan

Dokumen ini hanya menetapkan kontrak revisi order Done yang totalnya tetap, naik,
atau turun. Dokumen ini tidak mengubah keputusan bisnis D3 §16 (paket 6A):

- deposit tetap saldo per cabang dan tidak dipindahkan antar-cabang;
- top-up dan overpay hanya menjadi kredit setelah keputusan Superadmin;
- allocation adalah debit committed dan reversal selalu eksplisit;
- pembayaran campuran tetap manual;
- Virtual Account tetap OFF;
- tidak ada opening balance produksi.

Pilihan final adalah **jalur pending wajib untuk setiap revisi yang mengubah
total**. Implementasi awal tidak menyediakan shortcut “revisi dan langsung Done”
dalam endpoint revisi. Settlement versi baru dilakukan oleh transaksi verifikasi
terpisah yang atomik. Pilihan ini lebih ketat daripada alternatif atomik satu
request di D3 §8.1, tetapi tidak mengubah kebijakan bisnis §16 dan tidak
memerlukan keputusan CEO baru.

Gerbang B1–B2 terakhir belum membuktikan E2E browser terautentikasi karena fixture
vault tidak tersedia. Hasil PostgreSQL dan frontend yang dilaporkan lulus tidak
dipakai sebagai klaim E2E. Risiko ini tetap menjadi gerbang implementasi/release,
bukan alasan mengubah kontrak desain berikut.

## 2. Pilihan bentuk state final

### 2.1 State utama dan sub-state

`order_status` tetap mempunyai tiga nilai lama:

```text
draft | submitted | done
```

Tambahkan sub-state nullable pada order:

```text
revision_payment_state =
  NULL
  | revision_underpayment
  | revision_overpayment_pending
```

Kombinasi yang sah:

| `orders.status` | `revision_payment_state` | Makna |
|---|---|---|
| `draft` | `NULL` | Draft biasa |
| `submitted` | `NULL` | Submitted biasa, belum pernah Done atau bukan revisi pembayaran aktif |
| `submitted` | `revision_underpayment` | Revisi Done menaikkan total; settlement versi baru belum cocok |
| `submitted` | `revision_overpayment_pending` | Revisi Done menurunkan total; rekonsiliasi delta/overpay versi baru belum final |
| `done` | `NULL` | Settlement aktif tepat untuk total kini |

Sub-state ditempatkan pada kolom terpisah, bukan menambah nilai `order_status`,
agar consumer lama secara fail-safe melihat revisi belum lunas sebagai
`submitted`, bukan `done`. API tetap mengembalikan sub-state secara eksplisit;
consumer baru dilarang menyimpulkan jenis pending hanya dari `status`.

### 2.2 Hubungan dengan status verifikasi

Kombinasi normatif:

```text
revision_underpayment:
  status = submitted
  verif_status = perlu_review
  alasan_review = kurang_bayar

revision_overpayment_pending:
  status = submitted
  verif_status = perlu_review
  alasan_review = lebih_bayar

done:
  revision_payment_state = NULL
  verif_status = cocok
  alasan_review = NULL
```

`revision_overpayment_pending` berarti versi dengan total turun belum mempunyai
settlement aktif yang final. Nama ini tidak berarti seluruh delta pasti transfer
tunai: sebagian/seluruh delta dapat diselesaikan oleh reversal allocation deposit
yang eksplisit. Bila masih ada transfer lebih setelah reversal, keputusan
`credit_to_deposit` tetap hanya milik Superadmin.

### 2.3 State machine

```text
Done vN -- revisi total tetap --> Done vN+1
  |                                  pointer settlement lama dipertahankan
  |
  +-- total naik --> Submitted + revision_underpayment
  |                    |
  |                    +-- verifikasi settlement vN+1 atomik --> Done vN+1
  |
  +-- total turun + reversal plan bila perlu
                       --> Submitted + revision_overpayment_pending
                              |
                              +-- rekonsiliasi/keputusan overpay dan
                                  verifikasi settlement vN+1 atomik
                                  --> Done vN+1
```

Revisi kedua ketika `revision_payment_state IS NOT NULL` ditolak pada implementasi
awal. Tidak ada cancel/revert revisi terselubung. Koreksi harus diselesaikan lewat
prosedur rekonsiliasi yang teraudit atau desain lanjutan; sistem tidak boleh
mengarang kembali pointer lama sebagai pointer aktif.

## 3. Invarian database

### 3.1 Kolom baru dan histori typed

Kontrak skema implementasi:

```sql
CREATE TYPE revision_payment_state AS ENUM (
  'revision_underpayment',
  'revision_overpayment_pending'
);

CREATE TYPE revision_payment_transition AS ENUM (
  'total_unchanged',
  'underpayment',
  'overpayment'
);

ALTER TABLE orders
  ADD COLUMN revision_payment_state revision_payment_state,
  ADD COLUMN revision_payment_started_at timestamptz;
```

`invoice_revisions` menyimpan identitas settlement yang dilepas dalam kolom typed,
bukan hanya JSON audit. Nama dan tipe kontraknya:

```text
previous_done_verification_log_id UUID
previous_done_order_version       INT
previous_done_total               BIGINT
previous_done_deposit_amount      BIGINT
previous_done_transfer_amount     BIGINT
previous_done_at                  TIMESTAMPTZ
payment_transition                revision_payment_transition
```

Migrasi menambah kolom sebagai nullable untuk kompatibilitas row lama. Semua write
revisi baru dari Done wajib mengisi ketujuh kolom. CHECK all-or-none melarang
sebagian snapshot terisi. FK berikut wajib nyata:

```text
(previous_done_verification_log_id, order_id,
 previous_done_order_version, previous_done_total,
 previous_done_deposit_amount, previous_done_transfer_amount)
  -> verification_logs
     (id, order_id, order_version, total_pesanan,
      deposit_amount, transfer_amount)
```

`previous_done_at` harus sama dengan `orders.done_at` yang terkunci sebelum pointer
aktif dilepas. Snapshot JSON versi lama tetap disimpan, tetapi tidak menggantikan
FK typed. Row
`invoice_revisions`, verification log, source snapshot, journal, allocation, dan
dokumen lama immutable untuk role aplikasi.

Tidak diperlukan `revision_origin_*` pada `orders`: workflow aktif diperoleh dari
`orders.revision_payment_state`, sedangkan settlement asal diperoleh dari row
`invoice_revisions` dengan `(order_id, versi_baru = orders.versi)`.

### 3.2 CHECK pointer dan waktu Done

Empat field `done_*` dan `done_at` adalah state settlement **aktif**, bukan arsip.
Constraint harus menyatakan:

```text
status = done
  <=> revision_payment_state IS NULL
      AND revision_payment_started_at IS NULL
      AND done_at IS NOT NULL
      AND semua done_* IS NOT NULL
      AND done_deposit_amount + done_transfer_amount = total
      AND verif_status = cocok
      AND alasan_review IS NULL
```

Untuk kedua sub-state revisi:

```text
status = submitted
AND revision_payment_started_at IS NOT NULL
AND seluruh done_* IS NULL
AND done_at IS NULL
AND verif_status/alasan_review cocok dengan §2.2
```

Untuk `draft` dan `submitted` biasa, `revision_payment_state`,
`revision_payment_started_at`, seluruh `done_*`, dan `done_at` harus NULL.
Constraint nomor/tanggal Submitted lama tetap berlaku; revisi pending bukan Draft.

`done_order_version` boleh lebih kecil dari `orders.versi` hanya pada Done hasil
revisi total tetap. Setelah revisi total berubah diselesaikan, pointer baru wajib
menunjuk attempt untuk `orders.versi` dan total kini.

### 3.3 Allocation dan source snapshot lintas versi

Semua aggregate memakai seluruh allocation aktif untuk `order_id`, tidak hanya
versi kini:

```text
committed_deposit(order_id)
  = SUM(amount - reversed_amount) untuk seluruh versi

committed_deposit(order_id) <= orders.total
```

Masuk ke sub-state tidak menulis debit kedua. Settlement versi baru membuat row
`verification_deposit_sources` baru untuk setiap allocation aktif lintas versi,
dengan `amount_snapshot` terkunci saat attempt. Snapshot attempt lama tidak
diubah. Unique `(verification_log_id, allocation_id)` mencegah sumber ganda dalam
satu attempt; retry attempt yang sama tidak menambah row.

### 3.4 Reversal pada total turun

Sebelum total diturunkan, service menghitung committed deposit di bawah lock.
Jika `committed_deposit > proposed_total`, request wajib membawa reversal plan
eksplisit yang total efektifnya sedikitnya:

```text
committed_deposit - proposed_total
```

Setiap item plan memuat `allocation_id`, `amount`, dan `reason`. Satu
`Idempotency-Key` pada request revisi mencakup seluruh plan; item tidak mempunyai
key mandiri. Service tidak memilih allocation atau nominal secara otomatis.

Setelah seluruh reversal terencana diterapkan, invariant
`committed_deposit <= proposed_total` harus benar sebelum update order. Reversal
menambah journal credit dan menaikkan saldo available; row debit/allocation lama
tidak diubah selain cache `reversed_amount` sesuai kontrak D3.

Plan kosong/kurang, allocation order/cabang lain, nominal melebihi active amount,
reason kosong, atau plan yang menyebabkan invariant journal/cache gagal menolak
seluruh transaksi.

## 4. Transaksi normatif

### 4.1 Revisi total tetap

Dalam satu transaksi:

1. lock branch `FOR SHARE`, deposit account, order, lalu allocation menurut urutan
   global D3;
2. claim idempotency key dan validasi `expected_order_version` serta pointer Done;
3. simpan `invoice_revisions` dengan transition `total_unchanged`;
4. ubah lines, naikkan versi, total tetap sama;
5. pertahankan `status=done`, `done_at`, dan seluruh `done_*`;
6. tulis audit/event dan enqueue render/email invoice versi baru;
7. commit.

Tidak ada journal, allocation, reversal, verification log, atau source snapshot
baru. Invoice baru boleh bertanda LUNAS karena pointer lama masih membuktikan total
yang sama. UI/dokumen wajib membedakan versi dokumen dari versi settlement.

### 4.2 Revisi total naik

Dalam satu transaksi:

1. lock dan claim seperti §4.1;
2. pastikan state awal Done dan tidak ada revisi pembayaran aktif;
3. simpan settlement lama pada `invoice_revisions`;
4. ubah lines/total, naikkan versi;
5. set `status=submitted`, state `revision_underpayment`, waktu mulai, status
   verifikasi `perlu_review/kurang_bayar`;
6. set `done_at` dan seluruh `done_*` ke NULL;
7. pertahankan allocation/journal lama tanpa debit ulang;
8. tulis audit/event dan enqueue notifikasi revisi belum lunas, bukan invoice LUNAS;
9. commit.

Allocation tambahan boleh dibuat sesudahnya melalui workflow admin D3, selalu
terikat versi kini dan tetap dibatasi aggregate seluruh versi.

### 4.3 Revisi total turun

Urutan sama dengan §4.2, dengan tambahan sebelum update total:

1. hitung committed deposit seluruh versi;
2. validasi dan jalankan reversal plan eksplisit bila committed melebihi total
   usulan;
3. cek ulang journal, balance, cache reversal, dan committed deposit;
4. set state `revision_overpayment_pending` dan alasan `lebih_bayar`;
5. kosongkan pointer aktif dan commit seluruh reversal + revisi secara atomik.

Tidak ada auto-credit untuk selisih transfer. Jika settlement asal memakai
transfer melebihi kebutuhan transfer versi baru, attempt final harus memuat
keputusan split baru dan kredit overpay yang diputuskan Superadmin. Reuse bukti
transfer yang sama untuk order yang sama pada versi baru adalah snapshot histori,
bukan penerimaan uang kedua; journal overpay hanya boleh dibuat satu kali untuk
keputusan ekonomi tersebut.

Untuk proof yang dipakai lintas versi, split attempt versi baru membedakan:

```text
approved_nominal
applied_to_order
credited_to_deposit_total   -- kumulatif sepanjang umur proof
new_credit_to_deposit       -- delta journal pada attempt ini
```

Pada settlement Done berlaku:

```text
approved_nominal
  = applied_to_order + credited_to_deposit_total

new_credit_to_deposit
  = credited_to_deposit_total - SUM(overpay_credit committed sebelumnya
                                    untuk payment_proof_id yang sama)
new_credit_to_deposit >= 0
```

Service mengunci proof dan seluruh journal kredit proof tersebut. Nilai kumulatif
tidak boleh turun, jumlah journal kredit tidak boleh melebihi approved nominal,
dan `new_credit_to_deposit > 0` memerlukan Superadmin. Dengan demikian revisi turun
bertahap dapat menambah kredit hanya sebesar delta, retry tidak menggandakan
kredit, dan revisi naik setelah overpay pernah dikreditkan harus memakai sumber
dana baru—uang yang sudah menjadi saldo deposit tidak boleh diam-diam dipakai lagi
sebagai transfer proof.

### 4.4 Settlement state revisi ke Done

Endpoint verifikasi memakai transaksi D3 §5.4 dengan tambahan:

1. menerima hanya `submitted` dengan sub-state revisi yang dikenal;
2. lock seluruh allocation aktif lintas versi dan proof/decision terkait;
3. hitung ulang current total, committed deposit, transfer applied, dan overpay;
4. untuk underpayment, tolak bila sumber baru + carry-forward belum tepat total;
5. untuk overpayment pending, tolak bila selisih transfer belum mempunyai keputusan
   Superadmin atau split tidak seimbang;
6. buat verification log immutable untuk `orders.versi` dan total kini;
7. buat source snapshot seluruh allocation aktif tepat sekali;
8. set `status=done`, clear sub-state/waktu mulai, set `verif_status=cocok`, clear
   alasan, isi `done_at` dan seluruh `done_*` secara bersamaan;
9. enqueue dokumen/email current version dalam transaksi yang sama;
10. commit.

Tidak ada commit antara langkah 6–9. Kegagalan render terjadi sesudah commit dan
ditangani retry job idempoten; kegagalan transaksi tidak boleh membuat dokumen
atau email LUNAS terlihat.

## 5. Kontrak API

### 5.1 Revisi

`POST /api/v1/admin/orders/{id}/revise` tetap menjadi endpoint tunggal. Header
`Idempotency-Key` dan CSRF wajib. Body menambah field:

```json
{
  "expected_order_version": 3,
  "expected_done_verification_log_id": "<uuid>",
  "reason": "Koreksi qty",
  "lines": [],
  "deposit_reversals": [
    {
      "allocation_id": "<uuid>",
      "amount": 100000,
      "reason": "Total revisi lebih kecil dari deposit committed"
    }
  ]
}
```

Aturan:

- `deposit_reversals` default `[]`;
- total tetap/naik menolak plan yang tidak kosong;
- total turun menerima plan hanya bila setiap item valid; plan wajib cukup jika
  committed deposit akan melebihi total baru;
- server menghitung total dari lines dan semua aggregate; nilai total dari klien
  diabaikan;
- response tidak pernah mengembalikan status Done untuk total berubah;
- retry fingerprint identik mengembalikan response committed pertama; key sama
  dengan payload berbeda ditolak.

Response order menambah:

```json
{
  "status": "submitted",
  "revision_payment_state": "revision_underpayment",
  "revision_payment_started_at": "<timestamp>",
  "payment": {
    "current_total": 1200000,
    "committed_deposit": 400000,
    "verified_transfer_for_current_version": 0,
    "remaining_due": 800000,
    "is_lunas": false
  },
  "previous_settlement": {
    "verification_log_id": "<uuid>",
    "order_version": 2,
    "total": 1000000,
    "historical": true
  },
  "current_invoice_lunas_available": false
}
```

Customer dan admin GET detail/list wajib mengembalikan sub-state. Customer tidak
boleh mengirim `branch_id`; isolasi tetap dari sesi.

### 5.2 Verifikasi

`POST /api/v1/admin/orders/{id}/verify` menerima Submitted biasa maupun kedua
sub-state. Untuk sub-state revisi:

- `expected_order_version` wajib sama dengan versi kini;
- `deposit_allocation_ids` wajib sama persis dengan seluruh allocation aktif lintas
  versi;
- proof split boleh mereferensikan proof order yang sama dari versi sebelumnya,
  tetapi wajib mengirim/menghasilkan snapshot kumulatif
  `credited_to_deposit_total` dan delta `new_credit_to_deposit`; proof tidak boleh
  dianggap transfer baru atau menggandakan overpay credit;
- keputusan yang menciptakan kredit tetap memerlukan `admin.deposit_credit`;
- Manager boleh menyelesaikan underpayment tepat bayar tanpa menciptakan kredit,
  tetapi tidak boleh memutuskan overpay credit;
- response Done hanya dikirim setelah pointer, source snapshot, audit, dan outbox
  committed.

### 5.3 Error tambahan

| HTTP | Code | Kondisi |
|---:|---|---|
| 409 | `ORDER_REVISION_PAYMENT_PENDING` | Revisi kedua/cancel terselubung saat sub-state aktif |
| 409 | `REVISION_ORIGIN_CONFLICT` | expected version/pointer Done tidak lagi sama |
| 409 | `REVISION_REVERSAL_PLAN_REQUIRED` | Total turun akan melanggar committed <= total tanpa plan cukup |
| 409 | `REVISION_REVERSAL_PLAN_INVALID` | Item plan salah order/cabang, nominal/reason tidak sah, duplicate allocation |
| 409 | `REVISION_SETTLEMENT_REQUIRED` | Aksi LUNAS/dokumen final diminta ketika sub-state aktif |
| 409 | `ORDER_VERSION_CONFLICT` | Versi berubah setelah layar dibuka |
| 422 | `PAYMENT_BREAKDOWN_MISMATCH` | Settlement versi kini tidak tepat total |
| 422 | `OVERPAY_DECISION_REQUIRED` | Selisih transfer versi turun belum diputuskan Superadmin |

Seluruh penolakan terjadi tanpa mutasi parsial, journal orphan, job, dokumen, atau
email.

## 6. Dokumen, email, UI, C&C, dan reporting

### 6.1 Gerbang LUNAS

Satu-satunya predikat LUNAS adalah:

```text
status = done
AND revision_payment_state IS NULL
AND pointer done_* lengkap dan valid
AND breakdown pointer = orders.total
```

Renderer, endpoint download current invoice, resend email invoice, list riwayat,
reporting, dan worker dilarang memakai `verif_status` saja atau verification log
historis terakhir sebagai tanda LUNAS.

Untuk revisi total berubah:

- tidak membuat `invoice_pdf` current version ketika masuk pending;
- tidak enqueue email invoice/LUNAS atau Surat Jalan current version;
- kirim notifikasi `revision_payment_pending` tanpa lampiran final, berisi versi,
  total baru, jenis selisih, dan kalimat tegas “belum lunas”;
- invoice/dokumen versi lama tetap immutable dan dapat diunduh sebagai histori,
  tetapi UI memberi label “versi historis — bukan status pembayaran saat ini”;
- sesudah settlement commit, barulah render dan kirim invoice current version
  bertanda LUNAS.

Aturan ini mengkhususkan AP-REV-04: revisi total tetap tetap langsung menghasilkan
dan mengirim invoice baru; revisi total berubah menunda invoice final sampai
settlement. Tidak ada overwrite berkas pending menjadi LUNAS pada key dokumen yang
sama.

### 6.2 UI dan cache

Pada kedua sub-state:

- chip order utama `Menunggu pelunasan revisi` atau `Menunggu keputusan selisih`;
- tombol download/resend invoice current version disabled dengan alasan;
- timeline tetap menampilkan settlement lama sebagai histori;
- kartu pembayaran memisahkan deposit carry-forward, allocation baru, transfer
  historis, transfer tambahan, reversal, overpay, dan remaining due;
- konflik 409 memicu refetch; aksi uang tidak diulang otomatis;
- invalidasi mencakup detail/list order, invoice history, deposit balance/journal,
  antrean admin, dashboard, reporting, dan reminder.

### 6.3 Cash & Carry dan reminder

Order C&C pada sub-state tetap belum lunas. `jatuh_tempo` tidak dihitung ulang;
BR-08 tetap memakai tanggal order asli + `cnc_hari`.

Deduplikasi reminder harus menjadi version-aware. `cnc_reminders` menambah kolom
`order_version INT`; primary key final adalah `(order_id, order_version, tahap)`.
Migrasi row lama mengisi versi yang dapat dibuktikan dari event/dokumen; row ambigu
tetap historis dan tidak boleh dianggap dedupe versi kini. Row reminder versi lama
tidak menghalangi reminder versi revisi. Saat masuk pending:

- notifikasi revisi belum lunas dikirim sekali melalui outbox idempoten per versi;
- scheduler memilih `submitted` biasa dan kedua sub-state;
- scheduler tidak mengirim empat tahap lama sekaligus. Pada satu run ia hanya boleh
  mengantrekan tahap terbaru yang sudah jatuh tempo dan belum tercatat untuk versi
  kini; run berikutnya tidak mengulang karena unique key;
- dashboard menandai overdue berdasarkan due date asli dan state kini;
- setelah Done, reminder versi itu berhenti; retry outbox yang sudah terlanjur
  diantrekan wajib mengecek state/version sebelum send dan membatalkan email basi.

### 6.4 Reporting

Row order current version menampilkan status belum lunas selama sub-state aktif.
Settlement historis tetap tersedia pada laporan/audit terpisah, tetapi tidak boleh
membuat current order dihitung sebagai pendapatan/pembayaran Done dua kali. Join
journal, proof, source snapshot, dan revisions harus diagregasi menjadi satu row
per order/version sebelum join order lines.

## 7. Backward compatibility dan rollout fail-closed

Urutan implementasi:

1. tambah enum/kolom nullable dan kolom histori typed secara additive;
2. backfill semua order lama dengan sub-state NULL; jangan mengarang histori yang
   tidak dapat dibuktikan;
3. deploy read path/API/UI/worker yang memahami sub-state dan gerbang LUNAS;
4. validasi CHECK/FK sebagai `NOT VALID` lalu `VALIDATE` setelah data lolos;
5. aktifkan endpoint revisi total berubah hanya setelah writer, renderer, outbox,
   reminder, reporting, dan reconciliation memakai kontrak baru;
6. uji nyata dilakukan di production dengan data berlabel `UJI-` hanya setelah QA
   dan gerbang implementasi mengizinkan; dokumen desain ini tidak mengubah data.

Consumer lama tetap menerima `status=submitted`, sehingga tidak menerbitkan LUNAS.
Namun writer lama dapat menghapus sub-state atau mengirim email salah; karena itu
mutasi revisi total berubah harus fail closed sampai seluruh writer/worker minimum
version aktif. Rollback aplikasi mematikan mutasi terkait dan mempertahankan read
path sub-state; rollback tidak boleh kembali ke renderer yang menganggap
verification historis terakhir sebagai pointer aktif.

## 8. Matriks pengujian wajib implementasi

### 8.1 State/FK/CHECK

1. total tetap: versi naik, Done/pointer lama tetap, tanpa journal/attempt baru;
2. total naik: status Submitted + underpayment, `done_at/done_*` NULL, histori lama
   utuh;
3. total turun: status Submitted + overpayment pending, pointer aktif NULL;
4. Done dengan sub-state atau pointer NULL ditolak;
5. pending dengan salah satu `done_*`/`done_at` non-NULL ditolak;
6. pemalsuan pointer attempt order/version/total lain ditolak FK;
7. `invoice_revisions.previous_done_*` menunjuk settlement asal yang tepat.

### 8.2 Race, retry, dan rollback

1. dua revisi paralel: tepat satu commit, satu version conflict;
2. revisi vs allocation/reversal: terserialisasi menurut lock global;
3. revisi turun dengan dua reversal plan paralel tidak over-reverse;
4. retry key sama tidak menggandakan revision, reversal, journal, audit, event,
   notifikasi, atau version increment;
5. key sama payload beda ditolak;
6. failure injection setelah reversal, setelah update total, setelah clear pointer,
   dan sebelum outbox: seluruh transaksi rollback ke Done lama;
7. settlement retry tidak menggandakan debit carry-forward, source snapshot,
   verification log, credit overpay, dokumen, atau email;
8. settlement vs reversal paralel tidak menghasilkan Done dari source yang stale.

### 8.3 Penolakan

1. total turun tanpa plan cukup ketika committed > total;
2. duplicate/cross-order/cross-branch allocation pada plan;
3. Manager mencoba credit overpay;
4. settlement kurang bayar, overpay tanpa keputusan, source pending, rekening D2
   tidak efektif, proof lintas cabang, atau expected version stale;
5. revisi kedua, cancel terselubung, endpoint customer terhadap order Done/pending,
   dan download/resend current invoice saat pending;
6. status/pointer invalid tidak boleh diperbaiki dengan fallback ke attempt lama.

### 8.4 Dokumen, email, reminder, dan kompatibilitas

1. pending tidak mempunyai invoice current version bertanda LUNAS;
2. invoice lama tetap byte-stable dan berlabel historis;
3. settlement final membuat tepat satu invoice current version dan outbox idempoten;
4. C&C revision pending mengirim satu notifikasi, reminder version-aware, tidak
   memburst semua tahap lama, dan berhenti setelah Done;
5. worker email membatalkan pesan basi bila version/state berubah;
6. consumer lama yang hanya mengenal status melihat Submitted, bukan Done;
7. reporting tidak menggandakan order line karena banyak attempt/source/journal;
8. browser E2E terautentikasi wajib diulang setelah fixture auth tersedia; hasil
   PG/frontend sebelumnya tidak dianggap pengganti E2E.

## 9. Keputusan final dan risiko tersisa

Keputusan final C0:

- gunakan `orders.status=submitted` plus sub-state nullable
  `revision_underpayment`/`revision_overpayment_pending`;
- semua revisi total berubah selalu masuk pending;
- `done_at` dan seluruh pointer settlement aktif dikosongkan atomik;
- histori settlement asal disimpan immutable dan ber-FK typed di revision row;
- allocation aktif lintas versi di-carry-forward tanpa debit kedua;
- total turun membutuhkan reversal plan eksplisit bila committed melebihi total;
- transisi kembali ke Done hanya melalui settlement atomik versi kini;
- dokumen/email current version tidak boleh LUNAS sebelum commit tersebut;
- reminder C&C tetap memakai due date asli tetapi dedupe per versi.

Risiko tersisa yang bukan keputusan desain baru:

- browser E2E terautentikasi belum terverifikasi karena fixture vault;
- reuse proof lintas versi dan aggregate overpay memerlukan constraint/repository
  test yang kuat agar tidak menciptakan kredit kedua;
- worker lama adalah risiko rollback, sehingga aktivasi mutasi harus fail closed;
- revisi kedua saat pending belum didukung dan harus ditangani rekonsiliasi/manual,
  bukan dengan mengubah histori.

Tidak ada kode, migrasi, seed, saldo, rekening, order, atau data production yang
diubah oleh keputusan desain ini.
