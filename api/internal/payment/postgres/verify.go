package postgres

// Berkas ini memuat transaksi VERIFIKASI PEMBAYARAN (kartu F1-18;
// SystemDesign.md §4.3–4.4; Database.md §4.4–4.6).
//
// SATU permintaan verifikasi = SATU transaksi. Urutannya:
//
//  1. kunci baris pesanan (`FOR UPDATE`) dan baca keadaan sebelumnya;
//  2. tolak dengan ORDER_ALREADY_DONE bila sudah berstatus done (verifikasi
//     kedua tidak boleh menghasilkan log/dokumen/email kedua), dan
//     ORDER_NOT_SUBMITTED bila pesannya masih draft;
//  3. tulis transisi (status done + verif cocok + done_at, ATAU verif
//     perlu_review + alasan);
//  4. tulis log verifikasi (total_pesanan dibaca dari baris yang dikunci,
//     selisih dihitung kolom generated);
//  5. tulis peristiwa timeline + entri audit;
//  6. TITIPKAN pekerjaan River "render dokumen pesanan lalu antre email
//     dokumen" pada transaksi YANG SAMA, sehingga tidak mungkin ada pesanan
//     selesai tanpa pekerjaan dokumennya (pola SystemDesign.md §4.2/§5.1).
//
// Langkah 6 memakai `InsertTx` dari klien River sehingga baris river_job ikut
// rollback bila transaksi batal. Bila penjadwal pekerjaan belum terpasang
// (mis. konfigurasi salah), verifikasi DITOLAK alih-alih menyelesaikan pesanan
// tanpa dokumen dan email — gagal terang-terangan, bukan setengah jalan.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// JobInserter menitipkan pekerjaan River DI DALAM transaksi yang sedang
// berjalan. Dipenuhi oleh *river.Client[pgx.Tx]; dipisah menjadi interface
// supaya repository bisa diuji tanpa River.
type JobInserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// Repo memenuhi kontrak verifikasi domain pembayaran.
var _ payment.Verifier = (*Repo)(nil)

// Option mengubah perilaku repository bukti/verifikasi.
type Option func(*Repo)

// WithJobQueue memasang penjadwal pekerjaan River (transisi Done menitipkan
// job render dokumen pada transaksi verifikasi).
func WithJobQueue(q JobInserter) Option {
	return func(r *Repo) { r.jobs = q }
}

// MarkVerified menjalankan verifikasi pembayaran satu pesanan dalam SATU
// transaksi (lihat catatan berkas ini).
func (r *Repo) MarkVerified(ctx context.Context, req payment.MarkVerifiedRequest) (payment.VerifyOutcome, error) {
	if r.jobs == nil {
		return payment.VerifyOutcome{}, fmt.Errorf("%w: penjadwal pekerjaan dokumen belum terpasang", payment.ErrQueueUnavailable)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("mulai transaksi verifikasi: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)

	// 1. Kunci baris pesanan: hanya satu verifikasi per pesanan dalam satu
	//    waktu, dan seluruh keputusan di bawah dibaca dari baris terkunci.
	row, err := q.LockOrderForVerify(ctx, req.OrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.VerifyOutcome{}, payment.ErrNotFound
	}
	if err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("query LockOrderForVerify: %w", err)
	}

	// 2. Keadaan yang menentukan boleh-tidaknya transisi.
	switch row.Status {
	case payment.StatusDone:
		return payment.VerifyOutcome{}, payment.ErrAlreadyDone
	case payment.StatusSubmitted:
		// lanjut
	default:
		return payment.VerifyOutcome{}, payment.ErrNotSubmitted
	}

	// 3. Transisi status.
	switch req.Hasil {
	case payment.VerifCocok:
		changed, err := q.SetOrderDone(ctx, req.OrderID)
		if err != nil {
			return payment.VerifyOutcome{}, fmt.Errorf("query SetOrderDone: %w", err)
		}
		if changed == 0 {
			// Baris berpindah status sesudah penguncian (mis. verifikasi
			// paralel lain) — tidak ada efek samping yang ditulis.
			return payment.VerifyOutcome{}, payment.ErrAlreadyDone
		}
	case payment.VerifPerluReview:
		changed, err := q.SetOrderPerluReview(ctx, sqlc.SetOrderPerluReviewParams{
			Alasan: sqlc.ReviewReason(req.Alasan),
			ID:     req.OrderID,
		})
		if err != nil {
			return payment.VerifyOutcome{}, fmt.Errorf("query SetOrderPerluReview: %w", err)
		}
		if changed == 0 {
			// Klausa WHERE memeriksa ulang status; 0 baris berarti status
			// berubah di luar dugaan (tidak terjadi selama baris terkunci).
			return payment.VerifyOutcome{}, payment.ErrNotSubmitted
		}
	default:
		return payment.VerifyOutcome{}, payment.ErrHasilInvalid
	}

	// 4. Log verifikasi. total_pesanan diambil dari baris yang DIKUNCI (bukan
	//    dari angka yang dikirim klien) supaya selisih selalu benar.
	logRow, err := q.InsertVerificationLog(ctx, sqlc.InsertVerificationLogParams{
		OrderID:      req.OrderID,
		Metode:       sqlc.VerifyMethod(req.Metode),
		TotalPesanan: row.Total,
		TotalTerbaca: pgtypes.Int8Ptr(req.TotalTerbaca),
		Hasil:        sqlc.VerifStatus(req.Hasil),
		Alasan:       sqlc.NullReviewReason{ReviewReason: sqlc.ReviewReason(req.Alasan), Valid: req.Alasan != ""},
		AdminID:      adminIDVerifikasi(req),
		Catatan:      pgtypes.Text(req.Catatan),
	})
	if err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("query InsertVerificationLog: %w", err)
	}

	// 5a. Timeline: nama pelaku + keputusannya.
	if err := q.InsertVerifyOrderEvent(ctx, sqlc.InsertVerifyOrderEventParams{
		OrderID:   req.OrderID,
		Icon:      pgtypes.Text(ikonVerifikasi(req.Hasil)),
		Teks:      teksVerifikasi(req),
		ActorKind: actorKindVerifikasi(req),
		ActorID:   pgtypes.UUID(req.Actor.UserID),
	}); err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("query InsertVerifyOrderEvent: %w", err)
	}

	// 5b. Audit: Before/After diambil dari baris yang dikunci + hasil akhir,
	//     sehingga entri audit tidak pernah memuat snapshot basi.
	rec := req.Audit
	rec.EntityID = req.OrderID
	rec.Before = snapshotVerifikasi(row)
	after := map[string]any{
		"status":        statusSesudah(req.Hasil, row.Status),
		"verif_status":  req.Hasil,
		"total":         row.Total,
		"total_terbaca": int64(0),
		"selisih":       int64(0),
		"hasil":         req.Hasil,
		"metode":        req.Metode,
	}
	if req.TotalTerbaca != nil {
		after["total_terbaca"] = *req.TotalTerbaca
	}
	if logRow.Selisih.Valid {
		after["selisih"] = logRow.Selisih.Int64
	}
	if req.Alasan != "" {
		after["alasan_review"] = req.Alasan
	}
	rec.After = after
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return payment.VerifyOutcome{}, err
	}

	// 6. Pekerjaan lanjutan: render dokumen + antre email, dititipkan pada
	//    transaksi yang sama. Hanya persetujuan yang melahirkan dokumen dan
	//    email (perlu review jelas belum boleh menyelesaikan pesanan).
	if req.Hasil == payment.VerifCocok {
		if _, err := r.jobs.InsertTx(ctx, tx, jobs.RenderDokumenArgs{OrderID: req.OrderID}, nil); err != nil {
			return payment.VerifyOutcome{}, fmt.Errorf("titipkan pekerjaan render dokumen: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return payment.VerifyOutcome{}, fmt.Errorf("commit verifikasi: %w", err)
	}

	return payment.VerifyOutcome{
		Order: payment.VerifiedOrder{
			ID:           row.ID,
			Nomor:        row.Nomor,
			Versi:        int(row.Versi),
			BranchID:     row.BranchID,
			Status:       statusSesudah(req.Hasil, row.Status),
			VerifStatus:  req.Hasil,
			AlasanReview: req.Alasan,
			Total:        row.Total,
		},
		Log: toVerificationLog(logRow, req.Actor.Nama),
	}, nil
}

// ikonVerifikasi memilih ikon timeline untuk keputusan verifikasi.
func ikonVerifikasi(hasil string) string {
	if hasil == payment.VerifCocok {
		return "selesai"
	}
	return "review"
}

// teksVerifikasi menyusun kalimat timeline verifikasi.
func teksVerifikasi(req payment.MarkVerifiedRequest) string {
	if req.Actor.Kind == auth.KindSystem {
		if req.Hasil == payment.VerifCocok {
			return payment.TeksEventSetujuSistem()
		}
		return payment.TeksEventPerluReviewSistem(req.Alasan)
	}
	if req.Hasil == payment.VerifCocok {
		return payment.TeksEventSetuju(req.Actor.Nama)
	}
	return payment.TeksEventPerluReview(req.Actor.Nama, req.Alasan)
}

// actorKindVerifikasi menentukan jenis aktor pada timeline: `system` untuk
// keputusan otomatis (AI/OCR, kartu F2-26), `admin` untuk keputusan manusia
// (PRD §5 — timeline tidak boleh mengaku dikerjakan admin padahal bukan).
func actorKindVerifikasi(req payment.MarkVerifiedRequest) sqlc.ActorKind {
	if req.Actor.Kind == auth.KindSystem {
		return sqlc.ActorKindSystem
	}
	return sqlc.ActorKindAdmin
}

// adminIDVerifikasi mengisi pemeriksa pada `verification_logs`. Verifikasi
// otomatis (metode `ai`) menyimpan NULL — database & dokumen menyatakan
// admin_id NULL berarti AI (Database.md §4.5); id mesin tidak pernah ditulis
// sebagai pemeriksa manusia.
func adminIDVerifikasi(req payment.MarkVerifiedRequest) pgtype.UUID {
	if req.Metode == payment.MetodeAI || req.Actor.Kind == auth.KindSystem {
		return pgtype.UUID{}
	}
	return pgtypes.UUID(req.Actor.UserID)
}

// statusSesudah mengembalikan status pesanan sesudah keputusan: Done hanya
// untuk persetujuan, selain itu tetap seperti semula (Submitted).
func statusSesudah(hasil, sebelum string) string {
	if hasil == payment.VerifCocok {
		return payment.StatusDone
	}
	return sebelum
}

// snapshotVerifikasi menyusun potongan keadaan pesanan SEBELUM verifikasi untuk
// entri audit.
func snapshotVerifikasi(row sqlc.LockOrderForVerifyRow) map[string]any {
	out := map[string]any{
		"status":       row.Status,
		"verif_status": row.VerifStatus,
		"total":        row.Total,
		"versi":        int(row.Versi),
	}
	if row.AlasanReview != "" {
		out["alasan_review"] = row.AlasanReview
	}
	return out
}

// toVerificationLog menyusun nilai domain dari baris hasil insert.
func toVerificationLog(row sqlc.InsertVerificationLogRow, adminNama string) payment.VerificationLog {
	out := payment.VerificationLog{
		ID:           row.ID,
		OrderID:      row.OrderID,
		Metode:       row.Metode,
		TotalPesanan: row.TotalPesanan,
		TotalTerbaca: row.TotalTerbaca,
		Hasil:        row.Hasil,
		Alasan:       row.Alasan,
		Catatan:      row.Catatan,
		CreatedAt:    row.CreatedAt.UTC(),
	}
	if row.Selisih.Valid {
		out.Selisih = row.Selisih.Int64
	}
	// Nama pemeriksa hanya berarti untuk verifikasi manusia; verifikasi AI
	// (metode ai) menyimpan admin_id NULL dan tidak punya nama admin.
	if row.Metode != payment.MetodeAI {
		out.AdminNama = adminNama
	}
	return out
}
