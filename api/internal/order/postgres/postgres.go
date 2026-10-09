// Package postgres adalah implementasi repository pesanan di PostgreSQL.
//
// Seluruh query menyertakan branch_id sebagai syarat (dari SESI) dan status
// 'draft' diperiksa lagi di klausa WHERE update/delete, sehingga pesanan cabang
// lain maupun pesanan yang sudah dikirim tidak bisa disentuh dari jalur ini.
//
// Total dan penanda is_sertifikat TIDAK dihitung Go saja: trigger
// order_lines_recalc (migrasi 0002) menghitungnya dari order_lines pada setiap
// penulisan baris, dan setiap operasi tulis membaca ulang pesanannya di dalam
// transaksi yang sama sehingga nilai yang dikembalikan adalah nilai database.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// Repo adalah repository pesanan berbasis sqlc.
type Repo struct {
	pool *database.Pool
	q    *sqlc.Queries
	// jobs menitipkan pekerjaan lanjutan (render invoice versi baru + email)
	// pada transaksi revisi invoice (F2-25). Boleh nil untuk jalur yang tidak
	// butuh pekerjaan latar (mis. pengujian unit); revisi menolak jalan tanpa
	// penjadwal supaya versi tidak naik tanpa invoice baru.
	jobs JobInserter
}

// Option mengubah perilaku repository pesanan.
type Option func(*Repo)

// WithJobQueue memasang penjadwal pekerjaan River; revisi invoice menitipkan
// pekerjaan render invoice versi baru pada transaksi revisi (F2-25).
func WithJobQueue(q JobInserter) Option {
	return func(r *Repo) { r.jobs = q }
}

// New membuat repository pesanan.
func New(pool *database.Pool, opts ...Option) *Repo {
	r := &Repo{pool: pool, q: sqlc.New(pool)}
	for _, o := range opts {
		o(r)
	}
	return r
}

// List mengembalikan daftar pesanan satu cabang (PRD CP-ORD-01).
func (r *Repo) List(ctx context.Context, branchID, status string) ([]order.Order, error) {
	arg := sqlc.ListOrdersForBranchParams{BranchID: branchID}
	if status != "" {
		arg.Status = sqlc.NullOrderStatus{OrderStatus: sqlc.OrderStatus(status), Valid: true}
	}
	rows, err := r.q.ListOrdersForBranch(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("query ListOrdersForBranch: %w", err)
	}
	out := make([]order.Order, 0, len(rows))
	for _, row := range rows {
		out = append(out, order.Order{
			ID:           row.ID,
			BranchID:     branchID,
			Nomor:        pgtypes.TextOr(row.Nomor),
			Status:       row.Status,
			VerifStatus:  row.VerifStatus,
			Versi:        int(row.Versi),
			IsCNC:        row.IsCnc,
			IsSertifikat: row.IsSertifikat,
			Tanggal:      tanggalOr(row.Tanggal),
			JatuhTempo:   tanggalOr(row.JatuhTempo),
			Catatan:      row.Catatan,
			Total:        row.Total,
			JumlahItem:   row.JumlahItem,
			SubmittedAt:  waktuOr(row.SubmittedAt),
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}
	return out, nil
}

// Get mengembalikan satu pesanan milik cabang tersebut beserta barisnya.
func (r *Repo) Get(ctx context.Context, id, branchID string) (order.Order, error) {
	return loadOrder(ctx, r.q, id, branchID)
}

// CreateDraft menyimpan draft baru beserta seluruh barisnya dalam satu
// transaksi, lalu membaca ulang hasilnya (total dari trigger database).
func (r *Repo) CreateDraft(ctx context.Context, branchID, createdBy, catatan string, lines []order.Line) (order.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return order.Order{}, fmt.Errorf("mulai transaksi buat draft: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := r.q.WithTx(tx)
	id, err := q.CreateOrderDraft(ctx, sqlc.CreateOrderDraftParams{
		BranchID:  branchID,
		Catatan:   pgtypes.Text(catatan),
		CreatedBy: pgtypes.UUID(createdBy),
	})
	if err != nil {
		return order.Order{}, fmt.Errorf("query CreateOrderDraft: %w", err)
	}
	if err := insertLines(ctx, q, id, lines); err != nil {
		return order.Order{}, err
	}
	saved, err := loadOrder(ctx, q, id, branchID)
	if err != nil {
		return order.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return order.Order{}, fmt.Errorf("commit buat draft: %w", err)
	}
	return saved, nil
}

// UpdateDraft mengganti catatan dan seluruh baris draft. Baris diganti utuh
// (bukan diff) sehingga hasilnya sama dengan yang dilihat editor.
func (r *Repo) UpdateDraft(ctx context.Context, id, branchID, catatan string, lines []order.Line) (order.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return order.Order{}, fmt.Errorf("mulai transaksi ubah draft: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := r.q.WithTx(tx)
	if _, err := q.UpdateOrderDraft(ctx, sqlc.UpdateOrderDraftParams{
		Catatan:  pgtypes.Text(catatan),
		ID:       id,
		BranchID: branchID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Tidak ada, bukan milik cabang ini, atau sudah bukan draft lagi.
			return order.Order{}, order.ErrNotFound
		}
		return order.Order{}, fmt.Errorf("query UpdateOrderDraft: %w", err)
	}
	if err := q.ClearOrderLines(ctx, id); err != nil {
		return order.Order{}, fmt.Errorf("query ClearOrderLines: %w", err)
	}
	if err := insertLines(ctx, q, id, lines); err != nil {
		return order.Order{}, err
	}
	saved, err := loadOrder(ctx, q, id, branchID)
	if err != nil {
		return order.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return order.Order{}, fmt.Errorf("commit ubah draft: %w", err)
	}
	return saved, nil
}

// DeleteDraft menghapus draft milik cabang tersebut. Pesanan Submitted/Done
// tidak pernah cocok dengan klausa WHERE sehingga tidak terhapus.
func (r *Repo) DeleteDraft(ctx context.Context, id, branchID string) error {
	n, err := r.q.DeleteOrderDraft(ctx, sqlc.DeleteOrderDraftParams{ID: id, BranchID: branchID})
	if err != nil {
		return fmt.Errorf("query DeleteOrderDraft: %w", err)
	}
	if n == 0 {
		return order.ErrNotFound
	}
	return nil
}

// insertLines menulis ulang seluruh baris draft. subtotal tidak diisi karena
// kolomnya GENERATED ALWAYS.
//
// Baris `packaging` (biaya packaging pesanan Sertifikat) ditulis lewat query
// tersendiri: tanpa product_id, qty selalu 1, dan tanpa serial number. Baris
// itu disusun server di domain pesanan — repository tidak pernah menerimanya
// dari klien.
func insertLines(ctx context.Context, q *sqlc.Queries, orderID string, lines []order.Line) error {
	for _, l := range lines {
		if l.Jenis == order.JenisPackaging {
			if err := q.InsertOrderLinePackaging(ctx, sqlc.InsertOrderLinePackagingParams{
				OrderID:        orderID,
				LineNo:         int32(l.LineNo),
				NamaSnapshot:   l.Nama,
				SatuanSnapshot: l.Satuan,
				HargaSatuan:    l.HargaSatuan,
			}); err != nil {
				return fmt.Errorf("query InsertOrderLinePackaging: %w", err)
			}
			continue
		}
		if err := q.InsertOrderLineProduct(ctx, sqlc.InsertOrderLineProductParams{
			OrderID:        orderID,
			LineNo:         int32(l.LineNo),
			ProductID:      pgtypes.UUID(l.ProductID),
			NamaSnapshot:   l.Nama,
			SatuanSnapshot: l.Satuan,
			Qty:            int32(l.Qty),
			HargaSatuan:    l.HargaSatuan,
		}); err != nil {
			return fmt.Errorf("query InsertOrderLineProduct: %w", err)
		}
	}
	return nil
}

// Submit menjalankan transisi Draft -> Submitted dalam SATU transaksi
// (SystemDesign.md §4.2, kartu F1-14).
//
// Urutannya penting dan disengaja:
//
//  1. kunci baris pesanan (`FOR UPDATE`) memakai branch_id SESI — cegah double
//     submit dan submit yang menembus pemeriksaan status (SystemDesign §5.3);
//  2. baca baris draft beserta master produk dan harga cabang SAAT ITU, lalu
//     validasi ulang lewat order.SiapkanSubmit: produk aktif & dijual ke
//     cabang, Sertifikat berharga 0 + tepat satu baris packaging, campuran
//     ditolak (BR-05, CP-SER-01..03);
//  3. pesanan non-C&C wajib punya bukti transfer — dihitung DI DALAM transaksi
//     ini, setelah baris pesanan terkunci, supaya tidak ada celah antara
//     pembacaan bukti dan submit;
//  4. nomor invoice (global bulanan) dan serial (harian, satu rentang untuk
//     seluruh baris produk) diambil dari tabel counter DI DALAM transaksi yang
//     sama; rollback karena itu tidak memakai nomor (BR-09);
//  5. snapshot nama/satuan/harga disegarkan, serial ditulis, lalu status,
//     nomor, tanggal WIB, tenggat C&C, total, dan timeline ditulis; trigger
//     orders_guard_submit menjadi benteng terakhir atas invariant isi pesanan.
//
// Kegagalan di langkah mana pun membatalkan seluruhnya: tidak ada nomor yang
// terpakai, tidak ada baris yang berubah, dan tidak ada timeline yang tertulis.
func (r *Repo) Submit(ctx context.Context, req order.SubmitRequest) (order.Order, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return order.Order{}, fmt.Errorf("mulai transaksi submit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)

	// 1. Kunci baris pesanan milik cabang sesi.
	status, err := q.LockOrderForSubmit(ctx, sqlc.LockOrderForSubmitParams{ID: req.ID, BranchID: req.BranchID})
	if errors.Is(err, pgx.ErrNoRows) {
		return order.Order{}, order.ErrNotFound
	}
	if err != nil {
		return order.Order{}, fmt.Errorf("query LockOrderForSubmit: %w", err)
	}
	if status != order.StatusDraft {
		return order.Order{}, order.ErrLocked
	}

	// 2. Baca ulang isi draft + master + harga cabang, lalu validasi.
	rows, err := q.ListOrderLinesForSubmit(ctx, sqlc.ListOrderLinesForSubmitParams{
		BranchID: req.BranchID,
		OrderID:  req.ID,
	})
	if err != nil {
		return order.Order{}, fmt.Errorf("query ListOrderLinesForSubmit: %w", err)
	}
	lines := make([]order.SubmitLine, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, order.SubmitLine{
			LineNo:         int(row.LineNo),
			Jenis:          row.Jenis,
			Qty:            int(row.Qty),
			NamaLama:       row.NamaSnapshot,
			SatuanLama:     row.SatuanSnapshot,
			ProdukAda:      row.ProdukAda,
			ProdukAktif:    row.ProdukAktif,
			ProdukNama:     row.ProdukNama,
			ProdukSatuan:   row.ProdukSatuan,
			ProdukJenis:    row.ProdukJenis,
			HargaCabang:    row.HargaCabang,
			AdaHargaCabang: row.AdaHargaCabang,
		})
	}
	snapshots, err := order.SiapkanSubmit(lines, req.BiayaPackaging)
	if err != nil {
		return order.Order{}, err
	}

	// 3. Bukti transfer untuk pesanan non-C&C (PRD CP-ORD-04, BR-08).
	if !req.IsCNC {
		jumlah, err := q.CountPaymentProofs(ctx, req.ID)
		if err != nil {
			return order.Order{}, fmt.Errorf("query CountPaymentProofs: %w", err)
		}
		if jumlah < 1 {
			return order.Order{}, order.ErrProofRequired
		}
	}

	// 4. Nomor serial seluruh baris produk dalam SATU rentang.
	productLines := make([]int, 0, len(lines))
	for _, l := range lines {
		if l.Jenis == order.JenisProduct {
			productLines = append(productLines, l.LineNo)
		}
	}
	serial := make(map[int]string, len(productLines))
	if len(productLines) > 0 {
		periode, err := q.SerialPeriode(ctx, req.Tanggal)
		if err != nil {
			return order.Order{}, fmt.Errorf("query SerialPeriode: %w", err)
		}
		pertama, err := q.NextNumbers(ctx, sqlc.NextNumbersParams{
			Scope:   scopeSerial,
			Periode: periode,
			Jumlah:  int32(len(productLines)),
		})
		if err != nil {
			return order.Order{}, fmt.Errorf("query NextNumbers(serial): %w", err)
		}
		for i, lineNo := range productLines {
			nomor, err := q.FormatSerialNumber(ctx, sqlc.FormatSerialNumberParams{
				Tanggal: req.Tanggal,
				Urut:    pertama + int32(i),
			})
			if err != nil {
				return order.Order{}, fmt.Errorf("query FormatSerialNumber: %w", err)
			}
			serial[lineNo] = nomor
		}
	}

	// Nomor invoice: satu nomor global per bulan lintas cabang (BR-03).
	periodeInvoice, err := q.InvoicePeriode(ctx, req.Tanggal)
	if err != nil {
		return order.Order{}, fmt.Errorf("query InvoicePeriode: %w", err)
	}
	urut, err := q.NextNumbers(ctx, sqlc.NextNumbersParams{
		Scope:   scopeInvoice,
		Periode: periodeInvoice,
		Jumlah:  1,
	})
	if err != nil {
		return order.Order{}, fmt.Errorf("query NextNumbers(invoice): %w", err)
	}
	nomor, err := q.FormatInvoiceNumber(ctx, sqlc.FormatInvoiceNumberParams{
		Tanggal: req.Tanggal,
		Urut:    urut,
		Digit:   int32(req.DigitInvoice),
	})
	if err != nil {
		return order.Order{}, fmt.Errorf("query FormatInvoiceNumber: %w", err)
	}

	// 5. Tulis snapshot final, serial, lalu transisi status.
	for _, s := range snapshots {
		if err := q.UpdateOrderLineSnapshot(ctx, sqlc.UpdateOrderLineSnapshotParams{
			NamaSnapshot:   s.Nama,
			SatuanSnapshot: s.Satuan,
			HargaSatuan:    s.HargaSatuan,
			OrderID:        req.ID,
			LineNo:         int32(s.LineNo),
		}); err != nil {
			return order.Order{}, fmt.Errorf("query UpdateOrderLineSnapshot: %w", err)
		}
	}
	for lineNo, nomorSerial := range serial {
		if err := q.SetOrderLineSerial(ctx, sqlc.SetOrderLineSerialParams{
			SerialNumber: pgtypes.Text(nomorSerial),
			OrderID:      req.ID,
			LineNo:       int32(lineNo),
		}); err != nil {
			return order.Order{}, fmt.Errorf("query SetOrderLineSerial: %w", err)
		}
	}

	// Total dibaca ULANG dari barisnya (sumber tunggal: kolom generated),
	// supaya angka yang ditulis selalu sama dengan Σ subtotal yang dilihat
	// trigger orders_guard_submit.
	total, err := q.SumOrderLineSubtotals(ctx, req.ID)
	if err != nil {
		return order.Order{}, fmt.Errorf("query SumOrderLineSubtotals: %w", err)
	}

	params := sqlc.SetOrderSubmittedParams{
		Nomor:    pgtypes.Text(nomor),
		Tanggal:  req.Tanggal,
		IsCnc:    req.IsCNC,
		Total:    total,
		ID:       req.ID,
		BranchID: req.BranchID,
	}
	if !req.JatuhTempo.IsZero() {
		jt := req.JatuhTempo
		params.JatuhTempo = &jt
	}
	changed, err := q.SetOrderSubmitted(ctx, params)
	if err != nil {
		return order.Order{}, fmt.Errorf("query SetOrderSubmitted: %w", err)
	}
	if changed == 0 {
		// Berpindah status setelah penguncian (mis. submit paralel lain).
		return order.Order{}, order.ErrLocked
	}

	if err := q.InsertOrderEvent(ctx, sqlc.InsertOrderEventParams{
		OrderID:   req.ID,
		Icon:      pgtypes.Text("kirim"),
		Teks:      order.TeksEventSubmit,
		ActorKind: sqlc.ActorKindBranch,
		ActorID:   pgtypes.UUID(req.ActorID),
	}); err != nil {
		return order.Order{}, fmt.Errorf("query InsertOrderEvent: %w", err)
	}

	saved, err := loadOrder(ctx, q, req.ID, req.BranchID)
	if err != nil {
		return order.Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return order.Order{}, fmt.Errorf("commit submit: %w", err)
	}
	return saved, nil
}

// Scope counter penomoran (Database.md §6): invoice global per bulan, serial
// per hari.
const (
	scopeInvoice = "invoice"
	scopeSerial  = "serial"
)

// loadOrder membaca satu pesanan beserta barisnya memakai query yang sama untuk
// jalur transaksi maupun pembacaan biasa.
func loadOrder(ctx context.Context, q *sqlc.Queries, id, branchID string) (order.Order, error) {
	row, err := q.GetOrderForBranch(ctx, sqlc.GetOrderForBranchParams{ID: id, BranchID: branchID})
	if errors.Is(err, pgx.ErrNoRows) {
		return order.Order{}, order.ErrNotFound
	}
	if err != nil {
		return order.Order{}, fmt.Errorf("query GetOrderForBranch: %w", err)
	}
	lines, err := q.ListOrderLines(ctx, row.ID)
	if err != nil {
		return order.Order{}, fmt.Errorf("query ListOrderLines: %w", err)
	}
	out := order.Order{
		ID:           row.ID,
		Nomor:        pgtypes.TextOr(row.Nomor),
		Status:       row.Status,
		VerifStatus:  row.VerifStatus,
		Versi:        int(row.Versi),
		IsCNC:        row.IsCnc,
		IsSertifikat: row.IsSertifikat,
		Tanggal:      tanggalOr(row.Tanggal),
		JatuhTempo:   tanggalOr(row.JatuhTempo),
		Catatan:      row.Catatan,
		Total:        row.Total,
		JumlahItem:   row.JumlahItem,
		SubmittedAt:  waktuOr(row.SubmittedAt),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		Lines:        make([]order.Line, 0, len(lines)),
	}
	for _, l := range lines {
		out.Lines = append(out.Lines, order.Line{
			LineNo:       int(l.LineNo),
			Jenis:        l.Jenis,
			ProductID:    pgtypes.UUIDString(l.ProductID),
			Nama:         l.NamaSnapshot,
			Satuan:       l.SatuanSnapshot,
			Qty:          int(l.Qty),
			HargaSatuan:  l.HargaSatuan,
			Subtotal:     l.Subtotal.Int64,
			SerialNumber: pgtypes.TextOr(l.SerialNumber),
		})
	}
	return out, nil
}

// tanggalOr mengubah kolom date menjadi YYYY-MM-DD (kosong bila NULL).
func tanggalOr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(order.TanggalLayout)
}

// waktuOr mengubah kolom timestamptz menjadi RFC3339 UTC (kosong bila NULL).
func waktuOr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
