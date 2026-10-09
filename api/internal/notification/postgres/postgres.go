// Package postgres menyimpan email_outbox dan membaca data pesanan yang
// dibutuhkan email dokumen (F1-16).
//
// Klaim antrean memakai `FOR UPDATE SKIP LOCKED` sehingga dua worker tidak
// pernah mengirim baris yang sama; baris `sending` yang ditinggal proses mati
// kembali bisa diklaim setelah masa sewa habis.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/notification"
	"github.com/grahita/portal/api/internal/platform/database"
)

// Repo adalah implementasi notification.Store + notification.OrderReader.
type Repo struct {
	pool *database.Pool
}

// New membuat repository outbox di atas satu pool PostgreSQL.
func New(pool *database.Pool) *Repo { return &Repo{pool: pool} }

var _ notification.Store = (*Repo)(nil)
var _ notification.OrderReader = (*Repo)(nil)

const kolomEmail = `id, order_id, jenis, to_addr, cc_addr, subject, payload,
       status, attempts, next_try_at, last_error, sent_at, created_at`

const insertEmailSQL = `
INSERT INTO email_outbox (order_id, jenis, to_addr, cc_addr, subject, payload)
VALUES ($1::uuid, $2::email_kind, $3::text[], $4::text[], $5::text, $6::jsonb)
RETURNING ` + kolomEmail

// Enqueue menyimpan satu email baru berstatus queued.
func (r *Repo) Enqueue(ctx context.Context, e notification.Envelope) (notification.Email, error) {
	return r.insert(ctx, r.pool, e)
}

// EnqueueTx menyimpan baris outbox DI DALAM transaksi pemanggil.
//
// Ini jalur yang dipakai transaksi bisnis (mis. verifikasi pesanan F1-18):
// bila transaksinya rollback, baris email ikut hilang sehingga email tidak
// pernah terkirim — inti pola transactional outbox (SystemDesign.md §5.1).
func (r *Repo) EnqueueTx(ctx context.Context, tx pgx.Tx, e notification.Envelope) (notification.Email, error) {
	return r.insert(ctx, tx, e)
}

// EnqueueOnce menyimpan satu email HANYA bila (order_id, jenis) belum ada.
//
// Dipakai worker transisi Done (F1-18) yang boleh dijalankan ULANG: River
// bersifat at-least-once, jadi pekerjaan render dokumen yang gagal di tengah
// (setelah sebagian email diantre) akan dicoba lagi. Pemeriksaan dan penulisan
// berada di SATU transaksi yang diserialkan lewat advisory lock per
// (order_id, jenis) — pola yang sama dengan Resend — sehingga dua worker
// bersamaan tidak bisa sama-sama membuat baris untuk jenis yang sama.
//
// Baris yang SUDAH ada (termasuk yang `failed`) dianggap cukup: mengantre ulang
// email yang pernah dibuat adalah tindakan admin eksplisit (ResendOrder), bukan
// efek samping worker.
func (r *Repo) EnqueueOnce(ctx context.Context, e notification.Envelope) (notification.Email, bool, error) {
	if e.OrderID == "" {
		return notification.Email{}, false, fmt.Errorf("%w: email dokumen wajib punya pesanan", notification.ErrInvalidData)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return notification.Email{}, false, fmt.Errorf("mulai transaksi email sekali: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`, e.OrderID, e.Jenis); err != nil {
		return notification.Email{}, false, fmt.Errorf("kunci email dokumen: %w", err)
	}

	var ada bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM email_outbox
		    WHERE order_id = $1::uuid AND jenis = $2::email_kind)`,
		e.OrderID, e.Jenis).Scan(&ada); err != nil {
		return notification.Email{}, false, fmt.Errorf("periksa email dokumen: %w", err)
	}
	if ada {
		return notification.Email{}, false, nil
	}

	email, err := r.insert(ctx, tx, e)
	if err != nil {
		return notification.Email{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return notification.Email{}, false, fmt.Errorf("simpan email dokumen: %w", err)
	}
	return email, true, nil
}

// EnqueueOnceVersi menyimpan satu email HANYA bila (order_id, jenis, VERSI)
// belum pernah ada.
//
// Dipakai revisi invoice (F2-25): setiap versi pesanan yang baru boleh
// melahirkan satu email invoice baru (AP-REV-04), sedangkan pekerjaan worker
// yang dijalankan ulang untuk versi yang sama tidak boleh menggandakannya.
// Versi dibaca dari payload email (`payload->>'versi'`), bukan dari kolom
// tersendiri — tabel email_outbox memang tidak menyimpan versi (Database.md
// §4.7), dan email non-dokumen memakai bawaan 0.
//
// Pemeriksaan dan penulisan berada di SATU transaksi yang diserialkan lewat
// advisory lock per (order_id, jenis), sama seperti EnqueueOnce dan Resend.
func (r *Repo) EnqueueOnceVersi(ctx context.Context, e notification.Envelope, versi int) (notification.Email, bool, error) {
	if e.OrderID == "" {
		return notification.Email{}, false, fmt.Errorf("%w: email dokumen wajib punya pesanan", notification.ErrInvalidData)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return notification.Email{}, false, fmt.Errorf("mulai transaksi email sekali versi: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`, e.OrderID, e.Jenis); err != nil {
		return notification.Email{}, false, fmt.Errorf("kunci email dokumen versi: %w", err)
	}

	var ada bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM email_outbox
		    WHERE order_id = $1::uuid AND jenis = $2::email_kind
		      AND COALESCE((payload->>'versi')::int, 0) = $3::int)`,
		e.OrderID, e.Jenis, versi).Scan(&ada); err != nil {
		return notification.Email{}, false, fmt.Errorf("periksa email dokumen versi: %w", err)
	}
	if ada {
		return notification.Email{}, false, nil
	}

	email, err := r.insert(ctx, tx, e)
	if err != nil {
		return notification.Email{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return notification.Email{}, false, fmt.Errorf("simpan email dokumen versi: %w", err)
	}
	return email, true, nil
}

// insert menyimpan satu baris outbox memakai executor (pool atau transaksi).
func (r *Repo) insert(ctx context.Context, db execer, e notification.Envelope) (notification.Email, error) {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return notification.Email{}, fmt.Errorf("%w: payload bukan JSON: %v", notification.ErrInvalidData, err)
	}
	row := db.QueryRow(ctx, insertEmailSQL,
		nullUUID(e.OrderID), e.Jenis, e.To, nullTextSlice(e.Cc), e.Subject, payload)
	out, err := scanEmail(row)
	if err != nil {
		return notification.Email{}, fmt.Errorf("simpan email outbox: %w", err)
	}
	return out, nil
}

// Claim mengklaim sekumpulan email yang siap kirim.
//
// Satu baris hanya boleh dipegang satu worker sekaligus (SKIP LOCKED). Status
// langsung menjadi `sending`, attempts bertambah, dan next_try_at digeser ke
// akhir masa sewa sehingga baris yang prosesnya mati pulih sendiri.
//
// Sebelum mengklaim, baris `sending` yang masa sewanya habis DAN percobaannya
// sudah habis ditandai `failed` — tanpa langkah ini baris seperti itu akan
// tersangkut selamanya (tidak bisa diklaim lagi karena percobaannya habis,
// tetapi juga tidak pernah muncul di riwayat sebagai gagal).
func (r *Repo) Claim(ctx context.Context, c notification.Claim) ([]notification.Email, error) {
	limit := c.Limit
	if limit <= 0 {
		limit = 50
	}
	lease := c.Lease
	if lease <= 0 {
		lease = notification.DefaultLease
	}
	maxAttempts := c.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = notification.MaxAttempts
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE email_outbox
		SET status = 'failed',
		    last_error = COALESCE(last_error, 'percobaan habis saat email masih berstatus sending')
		WHERE status = 'sending'
		  AND attempts >= $2
		  AND next_try_at <= $1::timestamptz`, c.Now, maxAttempts); err != nil {
		return nil, fmt.Errorf("bersihkan email sending yang percobaannya habis: %w", err)
	}

	rows, err := r.pool.Query(ctx, `
		UPDATE email_outbox e
		SET status = 'sending',
		    attempts = e.attempts + 1,
		    next_try_at = $2::timestamptz
		WHERE e.id IN (
		    SELECT id FROM email_outbox
		    WHERE status IN ('queued','sending')
		      AND next_try_at <= $1::timestamptz
		      AND attempts < $4
		    ORDER BY next_try_at
		    LIMIT $3
		    FOR UPDATE SKIP LOCKED
		)
		RETURNING `+kolomEmail, c.Now, c.Now.Add(lease), limit, maxAttempts)
	if err != nil {
		return nil, fmt.Errorf("klaim email: %w", err)
	}
	defer rows.Close()

	out := []notification.Email{}
	for rows.Next() {
		e, err := scanEmail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkSent menandai satu email sudah terkirim.
func (r *Repo) MarkSent(ctx context.Context, id string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE email_outbox
		SET status = 'sent', sent_at = $2::timestamptz, last_error = NULL, next_try_at = $2::timestamptz
		WHERE id = $1::uuid`, id, at)
	if err != nil {
		return fmt.Errorf("tandai email terkirim: %w", err)
	}
	return nil
}

// Reschedule mengembalikan email ke antrean dengan waktu coba berikutnya.
func (r *Repo) Reschedule(ctx context.Context, id string, nextTryAt time.Time, cause string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE email_outbox
		SET status = 'queued', next_try_at = $2::timestamptz, last_error = $3::text
		WHERE id = $1::uuid`, id, nextTryAt, truncate(cause))
	if err != nil {
		return fmt.Errorf("jadwalkan ulang email: %w", err)
	}
	return nil
}

// GiveUp menandai email gagal permanen setelah percobaan habis.
func (r *Repo) GiveUp(ctx context.Context, id string, cause string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE email_outbox
		SET status = 'failed', last_error = $2::text
		WHERE id = $1::uuid`, id, truncate(cause))
	if err != nil {
		return fmt.Errorf("tandai email gagal: %w", err)
	}
	return nil
}

// ByOrder mengembalikan riwayat email satu pesanan (terbaru dulu).
func (r *Repo) ByOrder(ctx context.Context, orderID string) ([]notification.Email, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+kolomEmail+`
		FROM email_outbox
		WHERE order_id = $1::uuid
		ORDER BY created_at DESC, id DESC`, orderID)
	if err != nil {
		return nil, fmt.Errorf("riwayat email pesanan: %w", err)
	}
	defer rows.Close()

	out := []notification.Email{}
	for rows.Next() {
		e, err := scanEmail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Latest mengembalikan email terbaru untuk satu pesanan + jenis.
func (r *Repo) Latest(ctx context.Context, orderID, jenis string) (notification.Email, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+kolomEmail+`
		FROM email_outbox
		WHERE order_id = $1::uuid AND jenis = $2::email_kind
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, orderID, jenis)
	out, err := scanEmail(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return notification.Email{}, notification.ErrNotFound
	}
	if err != nil {
		return notification.Email{}, fmt.Errorf("email terakhir pesanan: %w", err)
	}
	return out, nil
}

// ActiveExists melaporkan apakah masih ada email queued/sending untuk satu
// pesanan + jenis.
func (r *Repo) ActiveExists(ctx context.Context, orderID, jenis string) (bool, error) {
	var ada bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM email_outbox
		    WHERE order_id = $1::uuid AND jenis = $2::email_kind
		      AND status IN ('queued','sending'))`, orderID, jenis).Scan(&ada)
	if err != nil {
		return false, fmt.Errorf("periksa antrean email: %w", err)
	}
	return ada, nil
}

// PesananEmailGagal mengembalikan id pesanan DISTINCT yang punya minimal satu
// email berstatus `failed`. Hanya id-nya (bukan isi email) yang keluar, dan
// urutannya deterministik supaya mudah diuji.
func (r *Repo) PesananEmailGagal(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT order_id::text
		FROM email_outbox
		WHERE status = 'failed' AND order_id IS NOT NULL
		ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("daftar pesanan ber-email gagal: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("baca id pesanan email gagal: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Resend menyimpan salinan email baru DAN entri audit dalam SATU transaksi.
//
// Penjaga "masih ada email jenis yang sama dalam antrean" diperiksa DI DALAM
// transaksi yang sama dan diserialkan per (order_id, jenis) lewat advisory lock
// transaksional. Dua permintaan kirim ulang yang bersamaan karena itu tidak
// bisa sama-sama lolos: yang kedua menunggu sampai yang pertama commit, lalu
// melihat baris baru dan ditolak ErrAlreadyQueued. Tanpa ini, klik ganda
// bersamaan menghasilkan dua email identik (temuan QA putaran 1, butir 1).
//
// EntityID entri audit diisi repository dengan id baris yang baru dibuat,
// sehingga audit selalu menunjuk ke email hasil kirim ulang.
func (r *Repo) Resend(ctx context.Context, e notification.Envelope, rec audit.Entry) (notification.Email, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return notification.Email{}, fmt.Errorf("mulai transaksi kirim ulang email: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Kunci berlaku sampai transaksi selesai; hashtext memetakan pasangan
	// (order_id, jenis) ke kunci int4 yang stabil.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`, e.OrderID, e.Jenis); err != nil {
		return notification.Email{}, fmt.Errorf("kunci kirim ulang email: %w", err)
	}

	var aktif bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM email_outbox
		    WHERE order_id = $1::uuid AND jenis = $2::email_kind
		      AND status IN ('queued','sending'))`,
		nullUUID(e.OrderID), e.Jenis).Scan(&aktif); err != nil {
		return notification.Email{}, fmt.Errorf("periksa antrean email: %w", err)
	}
	if aktif {
		return notification.Email{}, notification.ErrAlreadyQueued
	}

	email, err := r.insert(ctx, tx, e)
	if err != nil {
		return notification.Email{}, err
	}
	rec.EntityID = email.ID
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return notification.Email{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return notification.Email{}, fmt.Errorf("simpan kirim ulang email: %w", err)
	}
	return email, nil
}

// Release mengembalikan baris `sending` yang BELUM sempat dicoba ke `queued`.
//
// `attempts` dikembalikan ke nilai sebelum klaim (GREATEST(attempts - 1, 0))
// sehingga percobaan yang tidak pernah terjadi tidak menghabiskan jatah retry.
// Baris yang sudah dicoba (mis. gagal lalu dijadwalkan ulang) tidak lagi
// berstatus `sending` sehingga tidak tersentuh.
func (r *Repo) Release(ctx context.Context, ids []string, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE email_outbox
		SET status = 'queued',
		    attempts = GREATEST(attempts - 1, 0),
		    next_try_at = $2::timestamptz
		WHERE status = 'sending' AND id::text = ANY($1::text[])`, ids, now)
	if err != nil {
		return fmt.Errorf("lepas klaim email: %w", err)
	}
	return nil
}

// OrderByID memuat data pesanan + email cabangnya.
//
// Penerima invoice cabang adalah `branches.email` — kolom yang di Database.md
// §4.1 memang diberi keterangan "email cabang = email login" — bukan email
// akun di bagian lain, sehingga alamat kirim tidak bergantung pada keadaan
// akun cabang saat itu.
func (r *Repo) OrderByID(ctx context.Context, id string) (notification.OrderRef, error) {
	var (
		ref        notification.OrderRef
		nomor      *string
		tanggal    *time.Time
		jatuhTempo *time.Time
		status     string
	)
	err := r.pool.QueryRow(ctx, `
		SELECT o.id, o.nomor, b.nama, b.nama_kepala, b.email,
		       o.versi, o.total, o.tanggal, o.jatuh_tempo, o.is_cnc, o.status
		FROM orders o
		JOIN branches b ON b.id = o.branch_id
		WHERE o.id = $1::uuid`, id).
		Scan(&ref.ID, &nomor, &ref.Cabang, &ref.NamaKepala, &ref.CabangEmail,
			&ref.Versi, &ref.Total, &tanggal, &jatuhTempo, &ref.IsCNC, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return notification.OrderRef{}, notification.ErrNotFound
	}
	if err != nil {
		return notification.OrderRef{}, fmt.Errorf("muat pesanan untuk email: %w", err)
	}
	if nomor != nil {
		ref.Nomor = *nomor
	}
	if tanggal != nil {
		ref.Tanggal = *tanggal
	}
	ref.JatuhTempo = jatuhTempo
	return ref, nil
}

// Documents memuat seluruh dokumen milik satu pesanan.
func (r *Repo) Documents(ctx context.Context, orderID string) ([]notification.Dokumen, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT jenis::text, versi, storage_key
		FROM documents
		WHERE order_id = $1::uuid
		ORDER BY versi DESC, jenis`, orderID)
	if err != nil {
		return nil, fmt.Errorf("muat dokumen pesanan: %w", err)
	}
	defer rows.Close()

	out := []notification.Dokumen{}
	for rows.Next() {
		var d notification.Dokumen
		if err := rows.Scan(&d.Jenis, &d.Versi, &d.StorageKey); err != nil {
			return nil, fmt.Errorf("baca dokumen pesanan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---------- helper ----------

type execer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEmail(row rowScanner) (notification.Email, error) {
	var (
		e       notification.Email
		orderID *string
		to      []string
		cc      []string
		payload []byte
		last    *string
		sentAt  *time.Time
	)
	if err := row.Scan(&e.ID, &orderID, &e.Jenis, &to, &cc, &e.Subject, &payload,
		&e.Status, &e.Attempts, &e.NextTryAt, &last, &sentAt, &e.CreatedAt); err != nil {
		return notification.Email{}, err
	}
	if orderID != nil {
		e.OrderID = *orderID
	}
	e.To = to
	e.Cc = cc
	if last != nil {
		e.LastError = *last
	}
	e.SentAt = sentAt
	decoded, err := decodePayload(payload)
	if err != nil {
		return notification.Email{}, err
	}
	e.Payload = decoded
	return e, nil
}

func decodePayload(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("payload email bukan JSON objek: %w", err)
	}
	return out, nil
}

func nullUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func nullTextSlice(in []string) any {
	if len(in) == 0 {
		return nil
	}
	return in
}

// truncate membatasi panjang pesan error yang disimpan (kolom text).
func truncate(s string) string {
	const max = 1000
	if len(s) <= max {
		return s
	}
	return s[:max]
}
