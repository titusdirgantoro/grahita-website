// Pengingat pembayaran Cash & Carry (F2-23) di atas PostgreSQL: pembacaan
// kandidat dan penulisan baris cnc_reminders + email_outbox + audit dalam satu
// transaksi (transactional outbox, SystemDesign.md §5.1).
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/audit"
	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/notification"
)

var _ notification.ReminderStore = (*Repo)(nil)

// KandidatCNC mengembalikan pesanan C&C yang belum Done dan punya jatuh tempo,
// beserta data cabang yang dibutuhkan email pengingat.
//
// Filter `status <> 'done'` adalah yang membuat pesanan yang lunas sebelum
// tahap berikutnya berhenti diingatkan (PRD CP-CNC-05/CP-CNC-07). Pesanan
// non-C&C dan C&C tanpa jatuh tempo tidak pernah masuk. Urutan
// jatuh_tempo,id menjaga hasil deterministik.
func (r *Repo) KandidatCNC(ctx context.Context) ([]notification.KandidatCNC, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT o.id, COALESCE(o.nomor, ''), b.nama, b.nama_kepala, b.email,
		       COALESCE(o.total, 0), o.jatuh_tempo
		FROM orders o
		JOIN branches b ON b.id = o.branch_id
		WHERE o.is_cnc AND o.status <> 'done' AND o.jatuh_tempo IS NOT NULL
		ORDER BY o.jatuh_tempo, o.id`)
	if err != nil {
		return nil, fmt.Errorf("baca kandidat pengingat C&C: %w", err)
	}
	defer rows.Close()

	out := []notification.KandidatCNC{}
	for rows.Next() {
		var k notification.KandidatCNC
		if err := rows.Scan(&k.OrderID, &k.Nomor, &k.Cabang, &k.NamaKepala,
			&k.CabangEmail, &k.Total, &k.JatuhTempo); err != nil {
			return nil, fmt.Errorf("baca kandidat pengingat C&C: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// AntreReminderCNC mengantre SATU tahap pengingat C&C.
//
// Ketiga penulisan berada di SATU transaksi:
//
//  1. baris `cnc_reminders (order_id, tahap)` — PRIMARY KEY-nya yang membuat
//     tiap tahap hanya lahir sekali walau pekerjaan berjalan ulang; bila baris
//     sudah ada (atau baru saja ditulis transaksi lain yang belum commit),
//     ON CONFLICT DO NOTHING mengembalikan tanpa baris dan hasil dibuat=false;
//  2. baris `email_outbox` — pengiriman sesungguhnya dilakukan worker
//     (notification.ProsesAntrean), bukan di sini;
//  3. entri audit_logs (entity 'cnc_reminder', aktor 'system').
//
// Bila langkah 2 atau 3 gagal, transaksi rollback sehingga baris cnc_reminders
// ikut hilang: tahap itu dicoba lagi pada pemindaian berikutnya — bukan
// hilang. Sebaliknya, setelah COMMIT baris cnc_reminders tetap ada walau
// emailnya gagal terkirim; pemulihan pengiriman diserahkan ke outbox (retry
// berjenjang), dan baris cnc_reminders TIDAK dihapus.
func (r *Repo) AntreReminderCNC(ctx context.Context, orderID, tahap string, e notification.Envelope) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("mulai transaksi pengingat C&C: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var dibuat bool
	err = tx.QueryRow(ctx, `
		INSERT INTO cnc_reminders (order_id, tahap)
		VALUES ($1::uuid, $2)
		ON CONFLICT (order_id, tahap) DO NOTHING
		RETURNING true`, orderID, tahap).Scan(&dibuat)
	if errors.Is(err, pgx.ErrNoRows) {
		// Tahap ini sudah pernah diantre: bukan kegagalan, cukup dilewati.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("catat pengingat C&C %s %s: %w", orderID, tahap, err)
	}

	if _, err := r.insert(ctx, tx, e); err != nil {
		return false, err
	}

	entri := audit.Entry{
		ActorKind: auth.KindSystem,
		ActorID:   notification.AktorSistemCNC,
		ActorNama: notification.NamaAktorSistemCNC,
		Entity:    audit.EntityCNCReminder,
		EntityID:  orderID,
		Action:    audit.ActionRemind,
		After: map[string]any{
			"tahap":       tahap,
			"nomor":       e.Payload["nomor"],
			"jatuh_tempo": e.Payload["jatuh_tempo"],
			"penerima":    e.To,
		},
	}
	if err := auditpg.Insert(ctx, tx, entri); err != nil {
		return false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("simpan pengingat C&C %s %s: %w", orderID, tahap, err)
	}
	return dibuat, nil
}
