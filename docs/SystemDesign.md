# System Design — Sistem Pemesanan Cabang Grahita Indonesia

Dokumen ini menjelaskan **struktur aplikasi, API, dan alur kerja internal**. Pilihan teknologi & deployment ada di `Architecture.md`, skema data di `Database.md`, kebutuhan produk di `PRD.md`.

---

## 1. Peta komponen

```
┌───────────────────┐   ┌───────────────────┐
│ Customer SPA      │   │ Admin SPA         │
│ React + Vite      │   │ React + Vite      │
└─────────┬─────────┘   └─────────┬─────────┘
          │  JSON over HTTPS (cookie session)
          └───────────┬─────────────┘
                      ▼
        ┌─────────────────────────────┐
        │  API (Go, modular monolith) │
        │  http → service → repo      │
        └──────┬───────────┬──────────┘
               │           │ enqueue (dalam transaksi yang sama)
               ▼           ▼
        ┌────────────┐  ┌──────────────────────────┐
        │ PostgreSQL │◄─┤ Worker (River)           │
        └────────────┘  │ email · pdf · xlsx · ocr │
               ▲        │ reminder · backup        │
               │        └───┬───────┬────────┬─────┘
        ┌──────┴─────┐      ▼       ▼        ▼
        │ S3 storage │  Gotenberg  SMTP   Claude API
        └────────────┘             Gmail  (OCR bukti)
```

---

## 2. Struktur kode backend

```
cmd/
  server/main.go            # --mode=api | --mode=worker | --mode=migrate
internal/
  platform/                 # config, db, logger, storage, mailer, pdf, ai, clock
  http/                     # router, middleware (auth, csrf, ratelimit, recover), dto
  auth/                     # sesi, password, token registrasi, reset password
  branch/                   # cabang + user cabang
  catalog/                  # produk + matriks harga per cabang
  bank/                     # rekening bank
  order/                    # draft, submit, penomoran, state machine, sertifikat, C&C
  payment/                  # bukti transfer, verifikasi manual & AI
  document/                 # invoice PDF, surat jalan xlsx+pdf, revisi
  notification/             # outbox email, template, reminder
  report/                   # query laporan + ekspor xlsx
  settings/                 # pengaturan sistem
  audit/                    # audit log
  backup/                   # dump + unggah ke Google Drive
db/
  migrations/*.sql          # goose
  queries/*.sql             # sqlc
```

Aturan ketergantungan: `http → service → repository → db`. Antar-domain hanya boleh lewat interface service, tidak boleh saling membaca tabel milik domain lain.

### 2.1 Pola service

```go
type OrderService interface {
    CreateDraft(ctx context.Context, actor Actor, in DraftInput) (Order, error)
    UpdateDraft(ctx context.Context, actor Actor, id uuid.UUID, in DraftInput) (Order, error)
    Submit(ctx context.Context, actor Actor, id uuid.UUID, in SubmitInput) (Order, error)
    Get(ctx context.Context, actor Actor, id uuid.UUID) (OrderDetail, error)
    List(ctx context.Context, actor Actor, f OrderFilter) (Page[OrderRow], error)
}
```

`Actor` membawa `UserID`, `Kind` (branch/admin), `Role`, dan `BranchID`. Setiap service memeriksa otorisasi sendiri — UI tidak pernah menjadi satu-satunya penjaga.

---

## 3. Model domain inti

```go
type Order struct {
    ID          uuid.UUID
    Nomor       string          // YYYY-MM-XX
    Versi       int             // 0 = asli, 1.. = revisi
    BranchID    uuid.UUID
    Status      OrderStatus     // draft | submitted | done
    VerifStatus VerifStatus     // belum | cocok | perlu_review
    AlasanReview *ReviewReason  // kurang_bayar | lebih_bayar | tidak_terbaca | rekening_beda
    IsCNC       bool
    JatuhTempo  *time.Time
    Tanggal     time.Time       // tanggal submit (WIB)
    Total       int64           // Rupiah bulat
    Lines       []OrderLine
}

type OrderLine struct {
    ID           uuid.UUID
    LineNo       int
    Jenis        LineKind       // product | packaging
    ProductID    *uuid.UUID
    NamaSnapshot string
    Qty          int
    HargaSatuan  int64          // snapshot harga cabang saat submit
    Subtotal     int64
    SerialNumber *string        // YYYYMMDDXXX, hanya untuk jenis product
}
```

Invarian yang dijaga di service `order`:

1. Order `draft` tidak punya nomor dan serial number.
2. Order `submitted`/`done` tidak dapat diubah lewat jalur cabang.
3. Baris `packaging` hanya ada pada pesanan yang memuat produk sertifikat, tepat satu baris, qty 1.
4. Pesanan sertifikat tidak boleh memuat produk reguler.
5. `Total = Σ Subtotal`, dihitung ulang di server — nilai dari klien selalu diabaikan.
6. `IsCNC` hanya boleh `true` jika cabang punya flag Cash & Carry.

---

## 4. Alur utama

### 4.1 Registrasi

```
Cabang            API                         DB
  │ POST /auth/registration/validate {token}
  │────────────────►│ hash token, cari yang belum dipakai & belum kedaluwarsa
  │◄── 200 {nama_cabang, kepala, email} ──│
  │ POST /auth/register {token, telp, password}
  │────────────────►│ BEGIN
  │                 │  kunci baris token (FOR UPDATE), cek ulang
  │                 │  buat branch_user (argon2id)
  │                 │  tandai token used_at
  │                 │  audit log
  │                 │ COMMIT
  │◄── 201 ─────────│
```

Token disimpan sebagai **hash** di DB; nilai aslinya hanya ditampilkan sekali ke admin saat generate.

### 4.2 Submit pesanan (bagian paling kritis)

```
BEGIN;
  SELECT pg_advisory_xact_lock(...);         -- policy rekening (D2)
  -- cabang C&C: kunci cabang FOR SHARE → akun deposit FOR UPDATE, versi saldo
  --             harus sama dengan expected_journal_version (D3 §5.1)
  SELECT ... FROM orders WHERE id=$1 AND branch_id=$2 FOR UPDATE;   -- cegah double submit
  -- validasi: status=draft, ada minimal 1 baris, produk aktif & punya harga cabang,
  --           aturan sertifikat, bukti wajib untuk bagian yang dibayar transfer
  -- snapshot harga terbaru dari branch_prices ke order_lines
  -- penomoran:
  nomor  := next_invoice_no(tanggal_wib);    -- UPDATE counters ... RETURNING
  serial := next_serial_no(tanggal_wib, n);  -- n serial sekaligus untuk n baris produk
  UPDATE orders SET status='submitted', nomor=..., tanggal=..., total=...,
                    submitted_at = now();
  -- cabang C&C: debit min(saldo, total) → branch_deposit_transactions +
  --             deposit_allocations; sisanya wajib berbukti
  INSERT INTO order_events (...);            -- "Pesanan dikirim oleh <cabang>"
  INSERT INTO river_job (...);               -- job: verifikasi AI (bila aktif)
COMMIT;
```

Karena enqueue job berada **di dalam transaksi yang sama** (River menyimpan job di PostgreSQL), tidak mungkin terjadi "pesanan tersimpan tapi job hilang" atau sebaliknya.

### 4.3 Verifikasi pembayaran

```
                    ┌─ ai.aktif = false ─► antre verifikasi manual
submit / unggah ────┤
                    └─ ai.aktif = true ──► job OCR
                                             │
                        baca nominal, tanggal, bank, 4 digit rekening
                                             │
                    ┌────────────────────────┴───────────────────────┐
         keyakinan ≥ ambang                            selain itu
         Σ nominal dalam toleransi
         rekening cocok (rekening aktif)
         tanggal ≥ tanggal pesanan − ai.mundur
                    │                                        │
                    ▼                                        ▼
            status → done                        verif → perlu_review (+ alasan)
            enqueue dokumen & email                 muncul di chip dashboard admin
```

Transisi ke `done` dilakukan satu service (`payment.MarkVerified`) baik dari AI maupun manual, sehingga efek sampingnya (dokumen + email + timeline + audit) selalu identik.

### 4.4 Setelah pesanan Done

```
done ──► job: render Invoice PDF    ─┐
     ──► job: render Surat Jalan XLSX │ simpan ke object storage
     ──► job: render Surat Jalan PDF ─┘ catat di tabel documents
         └──► outbox: email invoice ke cabang
         └──► outbox: email invoice ke admin ber-flag invoice
         └──► outbox: email surat jalan ke admin ber-flag surat_jalan
```

Setiap job idempoten: kunci `(order_id, jenis, versi)` — menjalankan ulang tidak menghasilkan dokumen ganda.

### 4.5 Revisi invoice

```
BEGIN;
  SELECT ... FOR UPDATE;                       -- status harus done
  simpan snapshot versi lama ke invoice_revisions (jsonb + file lama tetap ada)
  terapkan perubahan baris, hitung ulang total
  versi := versi + 1                           -- nomor invoice tetap
  catat alasan, aktor, delta nilai di audit log + timeline
  enqueue: render invoice versi baru + kirim ulang email
COMMIT;
```

Serial number baris lama **tidak** dibuat ulang; baris yang benar-benar baru mendapat serial number baru dengan tanggal pesanan asli.

### 4.6 Cash & Carry (deposit)

Sejak 2026-10-18 Cash & Carry adalah sistem deposit: pesanan cabang C&C dibayar dari saldo deposit di dalam transaksi submit (§4.2; `design/branch-deposit.md` §3.8). Tidak ada lagi jatuh tempo maupun job reminder; tabel `cnc_reminders` dan kolom `orders.jatuh_tempo` hanya menyimpan riwayat pesanan C&C lama.

---

## 5. Pola lintas-domain

### 5.1 Transactional outbox untuk email

Email tidak pernah dikirim langsung dari handler HTTP. Service menulis baris ke `email_outbox` dalam transaksi bisnis; worker memindainya, mengirim, lalu memperbarui status. Retry memakai backoff eksponensial (1m, 5m, 30m, 2j, 6j) hingga 5 kali, setelah itu ditandai `failed` dan muncul sebagai chip "email gagal" di dashboard dengan tombol kirim ulang.

### 5.2 Penomoran tanpa bolong dan tanpa bentrok

Dijelaskan lengkap di `Database.md` §6. Intinya: tabel `numbering_counters (scope, periode, last_value)` dengan `UPDATE ... RETURNING` di dalam transaksi submit. Karena memakai tabel (bukan `SEQUENCE`), nomor tidak terbuang saat transaksi rollback dan reset per hari/bulan menjadi natural.

### 5.3 Idempotensi

- Submit: dikunci dengan `FOR UPDATE` + cek status; submit kedua mengembalikan 409.
- Job: unique key per `(order_id, jenis, versi)`.
- Unggah file: hash SHA-256 isi file mencegah duplikat bukti yang sama di satu pesanan.

### 5.4 Audit log

Setiap mutasi oleh admin memanggil `audit.Record(ctx, actor, entity, action, before, after)` di dalam transaksi yang sama dengan perubahan datanya.

### 5.5 Waktu

Satu interface `Clock` di-inject ke seluruh service agar penomoran dan tanggal bisnis dapat diuji deterministik. Perhitungan tanggal bisnis memakai `time.LoadLocation("Asia/Jakarta")`.

---

## 6. Permukaan API (ringkas)

Semua endpoint di bawah `/api/v1`, autentikasi cookie sesi, respons error berbentuk seragam:

```json
{ "error": { "code": "VALIDATION_FAILED", "message": "...", "fields": { "qty": "harus > 0" } } }
```

### Publik / auth

| Method | Path | Keterangan |
|---|---|---|
| POST | `/auth/login` | Login cabang & admin (dibedakan dari konteks akun) |
| POST | `/auth/logout` | Hapus sesi |
| GET | `/auth/me` | Profil + role + flag UI |
| POST | `/auth/registration/validate` | Validasi token, kembalikan data cabang |
| POST | `/auth/register` | Buat akun cabang |
| POST | `/auth/password/forgot` | Kirim link reset |
| POST | `/auth/password/reset` | Set password baru dengan token |
| POST | `/auth/password/change` | Ganti password saat login |

### Customer

| Method | Path | Keterangan |
|---|---|---|
| GET | `/me/branch` | Data cabang (profil) |
| GET | `/catalog` | Produk + harga cabang |
| GET | `/orders` | List pesanan cabang (filter status, periode) |
| POST | `/orders` | Buat draft |
| GET | `/orders/{id}` | Detail + lines + bukti + timeline |
| PUT | `/orders/{id}` | Ubah draft |
| DELETE | `/orders/{id}` | Hapus draft |
| POST | `/orders/{id}/submit` | Submit (multipart: bukti + flag C&C) |
| POST | `/orders/{id}/proofs` | Tambah bukti transfer |
| GET | `/invoices` | Riwayat invoice |
| GET | `/documents/{id}/download` | Presigned URL dokumen milik cabang |

### Admin

| Method | Path | Keterangan |
|---|---|---|
| GET | `/admin/dashboard` | Metrik + chip perlu tindakan (periode, cabang) |
| GET | `/admin/orders` | List + filter + grouping + paginasi |
| GET | `/admin/orders/{id}` | Detail lengkap |
| POST | `/admin/orders/{id}/notes` | Tambah catatan internal |
| POST | `/admin/orders/{id}/proofs` | Unggah bukti atas nama cabang |
| POST | `/admin/orders/{id}/verify` | Setujui (→ done) atau tandai perlu review |
| POST | `/admin/orders/{id}/revise` | Revisi invoice (versi +1) |
| POST | `/admin/orders/{id}/emails/{kind}/resend` | Kirim ulang email |
| GET/POST/PUT | `/admin/branches` | Master cabang |
| POST | `/admin/branches/{id}/token` | Generate token registrasi |
| GET/POST/PUT | `/admin/products` | Master produk |
| GET/PUT | `/admin/prices` | Matriks harga (batch update) |
| GET/POST/PUT | `/admin/bank-accounts` | Rekening bank (SA) |
| GET | `/admin/reports/order-lines` | Data laporan (paginasi) |
| POST | `/admin/reports/export` | Minta ekspor xlsx (asinkron) → job id |
| GET | `/admin/users/branch` · `/admin/users/admin` | User management |
| GET/PUT | `/admin/settings` | Pengaturan sistem (SA) |
| GET | `/admin/audit-logs` | Audit log (SA) |

---

## 7. Frontend

### 7.1 Peta layar → komponen (sesuai prototype)

| Prototype | Rute | Catatan |
|---|---|---|
| `pMasuk`, `pDaftar`, `pLupa` | `/masuk`, `/daftar`, `/lupa` | Layout auth terpisah dari shell |
| `pBeranda` | `/` | Kartu ringkas + pesanan terakhir |
| `pKatalog` | `/katalog` | Grid produk, harga cabang |
| `pPesanan` | `/pesanan` | List + filter status |
| `pEditor` | `/pesanan/baru`, `/pesanan/{id}/ubah` | Form order line + total live |
| `pDetail` | `/pesanan/{id}` | Header, lines, bukti, timeline |
| `pInvoice` | `/invoice` | Riwayat + unduh |
| `pProfil`, `pTutorial` | `/profil`, `/tutorial` | |
| `pDashboard` (admin) | `/admin` | Filter cabang + periode, chip, grafik SVG |
| `pPesanan`/`pDetail` (admin) | `/admin/pesanan`, `/admin/pesanan/{id}` | Filter lanjutan, verifikasi, revisi |
| `pCabang`, `pKatalog`, `pRekening` | `/admin/cabang`, `/admin/katalog`, `/admin/rekening` | Katalog punya tab Produk & Matriks harga |
| `pReporting`, `pPengguna`, `pPengaturan`, `pProfil` | `/admin/...` | Tiga di antaranya khusus Superadmin |

### 7.2 Aturan implementasi UI

- **Server state** seluruhnya lewat TanStack Query; tidak ada duplikasi data ke store global. State UI lokal (filter, tab, sidebar collapsed) disimpan di URL search params agar bisa di-bookmark dan di-share.
- **Optimistic update** hanya untuk aksi ringan (sembunyikan chip, edit sel matriks harga); aksi uang (submit, verifikasi, revisi) selalu menunggu respons server.
- **Guard rute** berdasarkan `/auth/me`; menu Superadmin disembunyikan **dan** API-nya menolak.
- **Uang** diformat `Intl.NumberFormat('id-ID')`, angka memakai `font-variant-numeric: tabular-nums` seperti di prototype.
- **Unggah** menampilkan progres, pratinjau gambar, zoom, dan validasi ukuran/jumlah di klien sebelum dikirim.
- **Skeleton & empty state** mengikuti pola prototype; tabel panjang memakai paginasi server, matriks harga memakai virtualisasi.
- **Responsif**: sidebar menjadi drawer, tabel menjadi kartu di mobile — persis seperti perilaku prototype.

---

## 8. Generator dokumen

| Dokumen | Cara |
|---|---|
| Invoice PDF | Template HTML (Go `html/template`) memakai token CSS yang sama dengan portal → Gotenberg → PDF A4. Memuat nomor + versi, data cabang, order line + serial number, biaya packaging, total, rekening, penanda C&C / LUNAS. |
| Surat Jalan XLSX | `excelize`, header kolom tetap, satu baris per order line, siap cetak. |
| Surat Jalan PDF | XLSX dirender lewat template HTML yang sama datanya → Gotenberg (hasil lebih konsisten daripada konversi spreadsheet). |
| Laporan XLSX | `excelize` streaming writer untuk data besar, freeze header, format Rupiah, sheet ringkasan per cabang. |

Dokumen disimpan di object storage dengan kunci `documents/{order_id}/{jenis}-{versi}.{ext}` dan dicatat di tabel `documents`; akses selalu lewat presigned URL berumur pendek.

---

## 9. Pengujian

| Lapisan | Cakupan |
|---|---|
| Unit | Aturan sertifikat & packaging, perhitungan total, potongan deposit C&C, format penomoran, toleransi verifikasi |
| Integrasi (testcontainers) | Penomoran paralel 100 submit, state machine, outbox, revisi versi, isolasi data antar cabang |
| Kontrak | Skema OpenAPI divalidasi di CI; tipe frontend di-generate dari skema |
| E2E (Playwright) | Registrasi→login→pesan→submit→verifikasi→invoice; alur sertifikat; alur C&C; batas akses Manager |
| Beban (k6) | 50 submit bersamaan, laporan 50.000 order line |

---

## 10. Catatan migrasi data awal

Sebelum go-live: impor 34 cabang dengan identitas nullable (`nama_kepala`, `jabatan_kepala`, dan email) serta status `identity_incomplete | ready | registered`, produk tanpa field satuan, dan matriks harga per cabang (sel kosong berarti produk tidak dijual ke cabang itu) dari spreadsheet yang ada melalui pipeline import tervalidasi. `qty` tetap menyatakan jumlah item. Nomor invoice historis **tidak** diimpor ke counter; counter dimulai dari 1 pada bulan go-live kecuali diputuskan lain.
