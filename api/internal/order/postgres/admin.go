// Riwayat & detail pesanan untuk admin Pusat (F1-15) di PostgreSQL.
//
// Berbeda dari order.sql, query di sini tidak dibatasi branch_id sesi —
// pembatasan cabang hanyalah salah satu FILTER, karena admin Pusat memang
// melihat semua cabang (PRD §2.1). Otorisasi dilakukan service (CapAdminOrders).
//
// Dua daftar bersifat lintas-domain dan sengaja READ-ONLY:
//   - `documents` — dokumen invoice/surat jalan; pemiliknya domain dokumen
//     (F1-18/F2-25) yang belum ada. Hanya dibaca untuk ditampilkan.
//   - `verification_logs` — ditulis domain verifikasi (F1-18). Hanya dibaca.
//
// Tidak ada satu pun query di berkas ini yang menulis ke tabel domain lain.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

var _ order.AdminRepository = (*Repo)(nil)

// rfc3339 adalah bentuk waktu yang dipakai seluruh respons API.
const rfc3339 = time.RFC3339

// filterAdmin adalah irisan parameter yang dipakai ketiga query daftar
// (list, count, group) supaya semuanya benar-benar melihat himpunan yang sama.
type filterAdmin struct {
	BranchID      pgtype.UUID
	Status        sqlc.NullOrderStatus
	VerifStatus   sqlc.NullVerifStatus
	IsCnc         pgtype.Bool
	Terlambat     pgtype.Bool
	EmailGagal    pgtype.Bool
	EmailGagalIDs []string
	Dari          *time.Time
	Sampai        *time.Time
	Cari          pgtype.Text
}

// newFilterAdmin menyusun parameter SQL dari filter yang sudah dinormalkan
// service. Nilai kosong selalu berarti "tanpa filter".
func newFilterAdmin(q order.AdminQuery) filterAdmin {
	f := filterAdmin{
		BranchID:      pgtypes.UUID(q.BranchID),
		IsCnc:         pgtypes.BoolPtr(q.IsCNC),
		Terlambat:     pgtypes.BoolPtr(q.Terlambat),
		EmailGagal:    pgtypes.BoolPtr(q.EmailGagal),
		EmailGagalIDs: q.EmailGagalIDs,
		Dari:          q.Dari,
		Sampai:        q.Sampai,
		Cari:          pgtypes.Text(q.Cari),
	}
	if q.Status != "" {
		f.Status = sqlc.NullOrderStatus{OrderStatus: sqlc.OrderStatus(q.Status), Valid: true}
	}
	if q.VerifStatus != "" {
		f.VerifStatus = sqlc.NullVerifStatus{VerifStatus: sqlc.VerifStatus(q.VerifStatus), Valid: true}
	}
	return f
}

// ListAdmin mengembalikan satu halaman riwayat beserta total dan ringkasan
// grup. Ketiga query memakai filter yang sama sehingga angka ringkasannya
// konsisten dengan halaman yang dilihat.
func (r *Repo) ListAdmin(ctx context.Context, q order.AdminQuery) (order.AdminPage, error) {
	f := newFilterAdmin(q)

	rows, err := r.q.ListAdminOrders(ctx, sqlc.ListAdminOrdersParams{
		EmailGagalIds: f.EmailGagalIDs,
		BranchID:      f.BranchID,
		Status:        f.Status,
		VerifStatus:   f.VerifStatus,
		IsCnc:         f.IsCnc,
		Terlambat:     f.Terlambat,
		EmailGagal:    f.EmailGagal,
		Dari:          f.Dari,
		Sampai:        f.Sampai,
		Cari:          f.Cari,
		Grup:          q.Grup,
		LimitN:        int32(q.Limit),
		OffsetN:       int32(q.Offset),
	})
	if err != nil {
		return order.AdminPage{}, fmt.Errorf("query ListAdminOrders: %w", err)
	}
	items := make([]order.AdminRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, order.AdminRow{
			ID:           row.ID,
			Nomor:        row.Nomor,
			Versi:        int(row.Versi),
			BranchID:     row.BranchID,
			BranchNama:   row.BranchNama,
			Tanggal:      tanggalOr(row.Tanggal),
			JatuhTempo:   tanggalOr(row.JatuhTempo),
			Status:       row.Status,
			VerifStatus:  row.VerifStatus,
			IsCNC:        row.IsCnc,
			IsSertifikat: row.IsSertifikat,
			Total:        row.Total,
			JumlahItem:   row.JumlahItem,
			Terlambat:    row.Terlambat,
			EmailGagal:   row.EmailGagal,
			SubmittedAt:  waktuOr(row.SubmittedAt),
			CreatedAt:    row.CreatedAt.UTC().Format(rfc3339),
			UpdatedAt:    row.UpdatedAt.UTC().Format(rfc3339),
		})
	}

	total, err := r.q.CountAdminOrders(ctx, sqlc.CountAdminOrdersParams{
		BranchID:      f.BranchID,
		Status:        f.Status,
		VerifStatus:   f.VerifStatus,
		IsCnc:         f.IsCnc,
		Terlambat:     f.Terlambat,
		EmailGagal:    f.EmailGagal,
		EmailGagalIds: f.EmailGagalIDs,
		Dari:          f.Dari,
		Sampai:        f.Sampai,
		Cari:          f.Cari,
	})
	if err != nil {
		return order.AdminPage{}, fmt.Errorf("query CountAdminOrders: %w", err)
	}

	page := order.AdminPage{Items: items, Total: total}
	if q.Grup != "" {
		grup, err := r.q.GroupAdminOrders(ctx, sqlc.GroupAdminOrdersParams{
			Grup:          q.Grup,
			BranchID:      f.BranchID,
			Status:        f.Status,
			VerifStatus:   f.VerifStatus,
			IsCnc:         f.IsCnc,
			Terlambat:     f.Terlambat,
			EmailGagal:    f.EmailGagal,
			EmailGagalIds: f.EmailGagalIDs,
			Dari:          f.Dari,
			Sampai:        f.Sampai,
			Cari:          f.Cari,
		})
		if err != nil {
			return order.AdminPage{}, fmt.Errorf("query GroupAdminOrders: %w", err)
		}
		page.Ringkasan = make([]order.AdminGrup, 0, len(grup))
		for _, g := range grup {
			page.Ringkasan = append(page.Ringkasan, order.AdminGrup{
				Kunci:  g.Kunci,
				Label:  g.Label,
				Jumlah: g.Jumlah,
				Total:  g.Total,
			})
		}
	}
	return page, nil
}

// GetAdmin membaca detail lengkap satu pesanan: header + cabang, baris, timeline,
// dokumen, dan log verifikasi.
func (r *Repo) GetAdmin(ctx context.Context, id string, emailGagalIDs []string) (order.AdminDetail, error) {
	row, err := r.q.GetAdminOrder(ctx, sqlc.GetAdminOrderParams{EmailGagalIds: emailGagalIDs, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return order.AdminDetail{}, order.ErrNotFound
	}
	if err != nil {
		return order.AdminDetail{}, fmt.Errorf("query GetAdminOrder: %w", err)
	}

	out := order.AdminDetail{
		ID:           row.ID,
		Nomor:        row.Nomor,
		Versi:        int(row.Versi),
		BranchID:     row.BranchID,
		BranchNama:   row.BranchNama,
		BranchKepala: row.BranchKepala,
		BranchEmail:  row.BranchEmail,
		BranchCNC:    row.BranchCnc,
		Status:       row.Status,
		VerifStatus:  row.VerifStatus,
		AlasanReview: row.AlasanReview,
		IsCNC:        row.IsCnc,
		IsSertifikat: row.IsSertifikat,
		Tanggal:      tanggalOr(row.Tanggal),
		JatuhTempo:   tanggalOr(row.JatuhTempo),
		SubmittedAt:  waktuOr(row.SubmittedAt),
		DoneAt:       waktuOr(row.DoneAt),
		CreatedAt:    row.CreatedAt.UTC().Format(rfc3339),
		UpdatedAt:    row.UpdatedAt.UTC().Format(rfc3339),
		Catatan:      row.Catatan,
		NoteAdmin:    row.NoteAdmin,
		Total:        row.Total,
		JumlahItem:   row.JumlahItem,
		Terlambat:    row.Terlambat,
		EmailGagal:   row.EmailGagal,
	}

	lines, err := r.q.ListOrderLines(ctx, row.ID)
	if err != nil {
		return order.AdminDetail{}, fmt.Errorf("query ListOrderLines(admin): %w", err)
	}
	out.Lines = make([]order.Line, 0, len(lines))
	for _, l := range lines {
		out.Lines = append(out.Lines, order.Line{
			LineNo:       int(l.LineNo),
			Jenis:        l.Jenis,
			ProductID:    pgtypes.UUIDString(l.ProductID),
			Nama:         l.NamaSnapshot,
			Qty:          int(l.Qty),
			HargaSatuan:  l.HargaSatuan,
			Subtotal:     l.Subtotal.Int64,
			SerialNumber: pgtypes.TextOr(l.SerialNumber),
		})
	}

	events, err := r.q.ListOrderEvents(ctx, row.ID)
	if err != nil {
		return order.AdminDetail{}, fmt.Errorf("query ListOrderEvents: %w", err)
	}
	out.Timeline = make([]order.Event, 0, len(events))
	for _, e := range events {
		out.Timeline = append(out.Timeline, order.Event{
			At:        e.At.UTC().Format(rfc3339),
			Icon:      e.Icon,
			Teks:      e.Teks,
			ActorKind: e.ActorKind,
			ActorID:   e.ActorID,
		})
	}

	docs, err := r.q.ListOrderDocuments(ctx, row.ID)
	if err != nil {
		return order.AdminDetail{}, fmt.Errorf("query ListOrderDocuments: %w", err)
	}
	out.Dokumen = make([]order.Dokumen, 0, len(docs))
	for _, d := range docs {
		out.Dokumen = append(out.Dokumen, order.Dokumen{
			ID:         d.ID,
			Jenis:      d.Jenis,
			Versi:      int(d.Versi),
			UkuranByte: d.UkuranByte,
			CreatedAt:  d.CreatedAt.UTC().Format(rfc3339),
		})
	}

	verifs, err := r.q.ListVerificationLogs(ctx, row.ID)
	if err != nil {
		return order.AdminDetail{}, fmt.Errorf("query ListVerificationLogs: %w", err)
	}
	out.Verifikasi = make([]order.VerifikasiLog, 0, len(verifs))
	for _, v := range verifs {
		out.Verifikasi = append(out.Verifikasi, order.VerifikasiLog{
			ID:                     v.ID,
			Metode:                 v.Metode,
			TotalPesanan:           v.TotalPesanan,
			TotalTerbaca:           v.TotalTerbaca,
			Selisih:                v.Selisih.Int64,
			Hasil:                  v.Hasil,
			Alasan:                 v.Alasan,
			Catatan:                v.Catatan,
			AdminNama:              v.AdminNama,
			BranchAllowlistVersion: v.BranchAllowlistVersion.Int64,
			DestinationResults:     v.DestinationResults,
			CreatedAt:              v.CreatedAt.UTC().Format(rfc3339),
		})
	}
	return out, nil
}

// SetNoteAdmin menyimpan catatan internal dan menulis peristiwa timeline-nya
// dalam SATU transaksi, sehingga catatan tanpa jejak siapa-kapan tidak
// mungkin tersimpan. Pesanan yang tidak ada dijawab ErrNotFound.
func (r *Repo) SetNoteAdmin(ctx context.Context, id, note, actorID, actorKind, teksEvent string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mulai transaksi catatan internal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)

	n, err := q.UpdateOrderNoteAdmin(ctx, sqlc.UpdateOrderNoteAdminParams{
		NoteAdmin: pgtypes.Text(note),
		ID:        id,
	})
	if err != nil {
		return fmt.Errorf("query UpdateOrderNoteAdmin: %w", err)
	}
	if n == 0 {
		return order.ErrNotFound
	}
	if err := q.InsertOrderEvent(ctx, sqlc.InsertOrderEventParams{
		OrderID:   id,
		Icon:      pgtypes.Text(order.IconEventNoteAdmin),
		Teks:      teksEvent,
		ActorKind: sqlc.ActorKind(actorKind),
		ActorID:   pgtypes.UUID(actorID),
	}); err != nil {
		return fmt.Errorf("query InsertOrderEvent(catatan): %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit catatan internal: %w", err)
	}
	return nil
}
