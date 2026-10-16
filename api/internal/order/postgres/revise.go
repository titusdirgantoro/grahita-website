package postgres

// Berkas ini memuat transaksi REVISI INVOICE BERVERSI (kartu F2-25;
// SystemDesign.md §4.5; PRD §4.3 AP-REV-01..05; Database.md §4.6, §8).
//
// SATU permintaan revisi = SATU transaksi. Urutannya:
//
//  1. kunci baris pesanan (`FOR UPDATE`) dan baca keadaan versi lama;
//  2. tolak pesanan yang belum Done (ORDER_NOT_DONE) — invoice baru terbit
//     saat Done (PRD §6.1);
//  3. FAIL CLOSED: tolak revisi selama invoice VERSI BERJALAN belum dirender
//     (INVOICE_NOT_READY). Pekerjaan lanjutan merender invoice versi pesanan
//     SAAT ITU, jadi tanpa penjaga ini revisi berikutnya yang menaikkan versi
//     lebih dahulu membuat invoice versi ini terlewat permanen walau API
//     sudah menjawab 200 (temuan QA putaran 1);
//  4. baca baris versi lama + jenis produk master, lalu SUSUN baris final di
//     server: baris lama mempertahankan serial/jenis/produknya, baris baru
//     mendapat serial dari tabel counter dengan periode TANGGAL PESANAN ASLI;
//  5. ganti seluruh baris (daftar final, bukan diff);
//  6. naikkan versi SATU langkah (nomor invoice tidak berubah);
//  7. baca total dari barisnya (BR-10) dan hitung delta nilai;
//  8. tulis snapshot versi lama ke `invoice_revisions` (jsonb) + alasan + delta;
//  9. tulis peristiwa timeline + entri audit (aktor/alasan/delta);
//  10. TITIPKAN pekerjaan River "render invoice versi baru + email invoice"
//     pada transaksi YANG SAMA, DIPATOK ke versi barunya.
//
// Langkah 10 memakai `InsertTx` dari klien River sehingga baris river_job ikut
// rollback bila transaksi batal. Bila penjadwal belum terpasang, revisi
// DITOLAK alih-alih menaikkan versi tanpa invoice baru dan email — gagal
// terang-terangan, bukan setengah jalan.
//
// Serial lama TIDAK pernah dibuat ulang: baris yang dipertahankan membawa
// serialnya apa adanya. Serial baris BARU diambil dari tabel counter, bukan
// SEQUENCE, sehingga rollback tidak membuang nomor (BR-09) dan tidak ada versi
// atau serial "palsu" yang tertinggal saat transaksi batal.
//
// Penjaga langkah 3 diperiksa DI DALAM transaksi yang mengunci baris pesanan:
// baris `documents` tidak pernah dihapus (F1-17) dan versi tidak bisa naik
// tanpa kunci ini, sehingga tidak ada celah balapan antara pemeriksaan dan
// kenaikan versi.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	auditpg "github.com/grahita/portal/api/internal/audit/postgres"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/db/sqlc"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/platform/pgtypes"
)

// JobInserter menitipkan pekerjaan River DI DALAM transaksi yang sedang
// berjalan. Dipenuhi oleh *river.Client[pgx.Tx]; dipisah menjadi interface
// supaya repository bisa diuji tanpa River.
type JobInserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// Repo memenuhi kontrak revisi domain pesanan.
var _ order.RevisionRepository = (*Repo)(nil)

// barisLama adalah satu baris pesanan versi lama beserta jenis produk master.
type barisLama struct {
	LineNo      int
	Jenis       string
	ProductID   string
	Nama        string
	Qty         int
	HargaSatuan int64
	Serial      string
	ProdukJenis string
	ProdukAda   bool
	ProdukNama  string
}

// barisFinal adalah satu baris yang akan ditulis sebagai versi baru.
type barisFinal struct {
	LineNo      int
	Jenis       string
	ProductID   string
	Nama        string
	Qty         int
	HargaSatuan int64
	Serial      string
}

// Revise menjalankan revisi invoice satu pesanan dalam SATU transaksi (lihat
// catatan berkas ini).
func (r *Repo) Revise(ctx context.Context, req order.ReviseRequest) (order.ReviseOutcome, error) {
	if r.jobs == nil {
		return order.ReviseOutcome{}, fmt.Errorf("%w: penjadwal pekerjaan revisi belum terpasang", order.ErrQueueUnavailable)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("mulai transaksi revisi: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)

	// 1. Kunci baris pesanan: hanya satu revisi per pesanan dalam satu waktu.
	row, err := q.LockOrderForRevise(ctx, req.OrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return order.ReviseOutcome{}, order.ErrNotFound
	}
	if err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("query LockOrderForRevise: %w", err)
	}

	// 2. Revisi hanya untuk pesanan yang sudah selesai.
	if row.Status != order.StatusDone {
		return order.ReviseOutcome{}, order.ErrNotDone
	}
	if !row.DoneVerificationLogID.Valid || !row.DoneOrderVersion.Valid ||
		!row.DoneDepositAmount.Valid || !row.DoneTransferAmount.Valid || row.DoneAt == nil {
		return order.ReviseOutcome{}, fmt.Errorf("%w: snapshot settlement Done tidak lengkap", order.ErrInvalidData)
	}
	if row.Tanggal == nil {
		// Pesanan Done selalu punya tanggal (constraint draft_tanpa_nomor);
		// ketiadaannya menandakan data rusak, bukan permintaan pengguna.
		return order.ReviseOutcome{}, fmt.Errorf("%w: tanggal pesanan kosong", order.ErrInvalidData)
	}

	// 3. FAIL CLOSED (temuan QA putaran 1): invoice VERSI BERJALAN wajib sudah
	//    dirender. Pekerjaan lanjutan revisi merender invoice versi pesanan
	//    SAAT ITU (document.EnsureInvoice), jadi bila revisi berikutnya
	//    menaikkan versi lebih dahulu, invoice versi ini TIDAK akan pernah
	//    dirender dan emailnya tidak pernah dikirim — padahal API sudah
	//    menjawab 200. Menolak di sini berarti setiap versi yang dijawab 200
	//    PASTI punya invoice, dan berkas versi sebelumnya selalu tersedia.
	adaInvoice, err := q.InvoiceVersiAda(ctx, sqlc.InvoiceVersiAdaParams{
		OrderID: req.OrderID,
		Versi:   row.Versi,
	})
	if err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("query InvoiceVersiAda: %w", err)
	}
	if !adaInvoice {
		return order.ReviseOutcome{}, fmt.Errorf("%w: invoice versi %d belum dirender",
			order.ErrInvoiceVersiBelumAda, row.Versi)
	}

	// 4. Baca baris versi lama.
	rowsLama, err := q.ListOrderLinesForRevise(ctx, req.OrderID)
	if err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("query ListOrderLinesForRevise: %w", err)
	}
	lama := make([]barisLama, 0, len(rowsLama))
	for _, l := range rowsLama {
		lama = append(lama, barisLama{
			LineNo:      int(l.LineNo),
			Jenis:       l.Jenis,
			ProductID:   l.ProductID,
			Nama:        l.NamaSnapshot,
			Qty:         int(l.Qty),
			HargaSatuan: l.HargaSatuan,
			Serial:      l.SerialNumber,
			ProdukJenis: l.ProdukJenis,
			ProdukAda:   l.ProdukAda,
			ProdukNama:  l.ProdukNama,
		})
	}
	if len(lama) == 0 {
		return order.ReviseOutcome{}, fmt.Errorf("%w: pesanan tanpa baris", order.ErrInvalidData)
	}

	// 5. Susun baris final di server (serial baris baru diambil di sini).
	final, err := r.susunBarisFinal(ctx, q, row, lama, req)
	if err != nil {
		return order.ReviseOutcome{}, err
	}

	// 6. Lepas pointer settlement aktif dan naikkan versi sebelum trigger total
	//    mengubah nilai order. State sementara valid ini tidak pernah terlihat di
	//    luar transaksi; state final ditentukan setelah total baru dihitung.
	changed, err := q.SetOrderVersi(ctx, req.OrderID)
	if err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("query SetOrderVersi: %w", err)
	}
	if changed == 0 {
		return order.ReviseOutcome{}, order.ErrNotDone
	}
	versiBaru := int(row.Versi) + 1

	// 7. Ganti seluruh baris.
	if err := q.DeleteOrderLines(ctx, req.OrderID); err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("query DeleteOrderLines: %w", err)
	}
	for _, l := range final {
		if l.Jenis == order.JenisPackaging {
			if err := q.InsertOrderLinePackaging(ctx, sqlc.InsertOrderLinePackagingParams{
				OrderID:      req.OrderID,
				LineNo:       int32(l.LineNo),
				NamaSnapshot: l.Nama,
				HargaSatuan:  l.HargaSatuan,
			}); err != nil {
				return order.ReviseOutcome{}, fmt.Errorf("query InsertOrderLinePackaging(revisi): %w", err)
			}
			continue
		}
		if err := q.InsertOrderLineProductWithSerial(ctx, sqlc.InsertOrderLineProductWithSerialParams{
			OrderID:      req.OrderID,
			LineNo:       int32(l.LineNo),
			ProductID:    pgtypes.UUID(l.ProductID),
			NamaSnapshot: l.Nama,
			Qty:          int32(l.Qty),
			HargaSatuan:  l.HargaSatuan,
			SerialNumber: pgtypes.Text(l.Serial),
		}); err != nil {
			return order.ReviseOutcome{}, fmt.Errorf("query InsertOrderLineProductWithSerial: %w", err)
		}
	}

	// 8. Total dihitung ULANG dari barisnya (BR-10) — angka klien tidak dipakai.
	totalBaru, err := q.SumOrderLineSubtotals(ctx, req.OrderID)
	if err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("query SumOrderLineSubtotals(revisi): %w", err)
	}
	delta := totalBaru - row.Total
	transition := sqlc.RevisionPaymentTransitionTotalUnchanged
	if delta > 0 {
		transition = sqlc.RevisionPaymentTransitionUnderpayment
	} else if delta < 0 {
		transition = sqlc.RevisionPaymentTransitionOverpayment
	}
	finalized, err := q.FinalizeOrderRevisionSettlement(ctx, sqlc.FinalizeOrderRevisionSettlementParams{
		PaymentTransition: transition, PreviousDoneAt: *row.DoneAt,
		PreviousDoneVerificationLogID: pgtypes.UUIDString(row.DoneVerificationLogID),
		PreviousDoneOrderVersion:      row.DoneOrderVersion.Int32,
		PreviousDoneDepositAmount:     row.DoneDepositAmount.Int64,
		PreviousDoneTransferAmount:    row.DoneTransferAmount.Int64,
		OrderID:                       req.OrderID,
	})
	if err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("query FinalizeOrderRevisionSettlement: %w", err)
	}
	if finalized == 0 {
		return order.ReviseOutcome{}, fmt.Errorf("%w: state revisi berubah", order.ErrInvalidData)
	}

	snapshotLama, err := json.Marshal(snapshotVersi(row, lama))
	if err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("marshal snapshot versi lama: %w", err)
	}
	revisiRow, err := q.InsertInvoiceRevision(ctx, sqlc.InsertInvoiceRevisionParams{
		OrderID:                       req.OrderID,
		VersiLama:                     row.Versi,
		VersiBaru:                     int32(versiBaru),
		Alasan:                        req.Alasan,
		Snapshot:                      snapshotLama,
		DeltaTotal:                    delta,
		AdminID:                       req.AdminID,
		PaymentTransition:             transition,
		PreviousDoneVerificationLogID: pgtypes.UUIDString(row.DoneVerificationLogID),
		PreviousDoneOrderVersion:      row.DoneOrderVersion.Int32,
		PreviousDoneTotal:             row.Total,
		PreviousDoneDepositAmount:     row.DoneDepositAmount.Int64,
		PreviousDoneTransferAmount:    row.DoneTransferAmount.Int64,
		PreviousDoneAt:                *row.DoneAt,
	})
	if err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("query InsertInvoiceRevision: %w", err)
	}

	// 9a. Timeline.
	if err := q.InsertOrderEvent(ctx, sqlc.InsertOrderEventParams{
		OrderID:   req.OrderID,
		Icon:      pgtypes.Text(order.IconEventRevisi),
		Teks:      teksRevisi(req, versiBaru),
		ActorKind: sqlc.ActorKindAdmin,
		ActorID:   pgtypes.UUID(req.Actor.UserID),
	}); err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("query InsertOrderEvent(revisi): %w", err)
	}

	// 9b. Audit: Before/After dari baris yang dikunci + hasil akhir, sehingga
	//     entri audit tidak pernah memuat snapshot basi.
	rec := req.Audit
	rec.EntityID = req.OrderID
	rec.Before = snapshotVersi(row, lama)
	rec.After = map[string]any{
		"versi":       versiBaru,
		"nomor":       row.Nomor,
		"total":       totalBaru,
		"delta_total": delta,
		"alasan":      req.Alasan,
		"catatan":     req.Catatan,
		"lines":       snapshotBaris(final),
	}
	if err := auditpg.Insert(ctx, tx, rec); err != nil {
		return order.ReviseOutcome{}, err
	}

	// 10. Pekerjaan lanjutan: render invoice VERSI BARU + antre email invoice,
	//     dititipkan pada transaksi yang sama. Versinya DIPATOK pada muatan job
	//     supaya worker memastikan invoice versi INI ada — bukan invoice versi
	//     berjalan saat worker kebetulan berjalan (temuan QA putaran 1).
	if transition == sqlc.RevisionPaymentTransitionTotalUnchanged {
		if _, err := r.jobs.InsertTx(ctx, tx, jobs.RevisiInvoiceArgs{
			OrderID: req.OrderID,
			Versi:   versiBaru,
		}, nil); err != nil {
			return order.ReviseOutcome{}, fmt.Errorf("titipkan pekerjaan revisi invoice: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return order.ReviseOutcome{}, fmt.Errorf("commit revisi: %w", err)
	}

	return order.ReviseOutcome{
		OrderID:    req.OrderID,
		Nomor:      row.Nomor,
		Versi:      versiBaru,
		Total:      totalBaru,
		JumlahItem: jumlahItem(final),
		DeltaTotal: delta,
		Lines:      garisKeluar(final),
		Revisi: order.Revision{
			ID:         revisiRow.ID,
			VersiLama:  int(row.Versi),
			VersiBaru:  versiBaru,
			Alasan:     req.Alasan,
			DeltaTotal: delta,
			AdminNama:  strings.TrimSpace(req.Actor.Nama),
			CreatedAt:  revisiRow.CreatedAt.UTC().Format(rfc3339),
		},
	}, nil
}

// susunBarisFinal menyusun daftar baris versi baru dari baris lama + daftar
// baris final yang dikirim admin.
//
// Aturan yang ditegakkan DI DALAM transaksi (butuh master produk):
//
//   - baris lama yang disebut `line_no`-nya harus ada dan berjenis product;
//     qty/harga boleh berubah, tetapi serial/jenis/produknya dipertahankan;
//   - baris baru wajib menunjuk produk yang ada dan AKTIF; nama
//     diambil master, harga dari admin (kecuali Sertifikat yang selalu 0);
//   - pesanan Sertifikat tetap tidak boleh dicampur produk reguler dan tetap
//     punya TEPAT SATU baris packaging (biaya dari setelan server);
//   - nomor baris (line_no) ditulis ulang berurutan 1..N supaya unique
//     (order_id, line_no) tetap terjaga setelah baris diganti.
func (r *Repo) susunBarisFinal(ctx context.Context, q *sqlc.Queries, row sqlc.LockOrderForReviseRow, lama []barisLama, req order.ReviseRequest) ([]barisFinal, error) {
	indeks := make(map[int]barisLama, len(lama))
	for _, l := range lama {
		if l.Jenis == order.JenisProduct {
			indeks[l.LineNo] = l
		}
	}

	type baru struct {
		productID   string
		nama        string
		produkJenis string
		qty         int
		harga       int64
		serial      string
	}
	// barisBaru menampung hasil sementara; serial baris baru diisi setelah
	// seluruh baris divalidasi supaya nomor tidak terpakai bila revisi batal.
	hasil := make([]baru, 0, len(req.Lines))
	var butuhSerial int
	for i, in := range req.Lines {
		var b baru
		if in.LineNo > 0 {
			l, ok := indeks[in.LineNo]
			if !ok {
				return nil, fmt.Errorf("%w: baris %d tidak ada pada versi sebelumnya", order.ErrInvalidData, in.LineNo)
			}
			if !l.ProdukAda {
				return nil, fmt.Errorf("%w: produk baris %d tidak ditemukan di master", order.ErrInvalidData, in.LineNo)
			}
			b = baru{
				productID:   l.ProductID,
				nama:        l.Nama,
				produkJenis: l.ProdukJenis,
				qty:         in.Qty,
				harga:       in.HargaSatuan,
				serial:      l.Serial,
			}
		} else {
			p, err := q.GetProductForRevise(ctx, in.ProductID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("%w: produk baris baru ke-%d tidak ditemukan", order.ErrInvalidData, i+1)
			}
			if err != nil {
				return nil, fmt.Errorf("query GetProductForRevise: %w", err)
			}
			if !p.IsActive {
				return nil, fmt.Errorf("%w: produk baris baru ke-%d tidak aktif", order.ErrInvalidData, i+1)
			}
			nama := strings.TrimSpace(p.Nama)
			if nama == "" {
				return nil, fmt.Errorf("%w: nama produk baris baru ke-%d kosong", order.ErrInvalidData, i+1)
			}
			b = baru{
				productID:   in.ProductID,
				nama:        nama,
				produkJenis: p.Jenis,
				qty:         in.Qty,
				harga:       in.HargaSatuan,
			}
			butuhSerial++
		}

		sertifikat := b.produkJenis == catalog.JenisSertifikat
		if row.IsSertifikat != sertifikat {
			return nil, fmt.Errorf("%w: pesanan Sertifikat tidak boleh dicampur produk reguler", order.ErrInvalidData)
		}
		if sertifikat {
			// CP-SER-01/BR-06: harga Sertifikat SELALU 0, apa pun yang dikirim.
			b.harga = 0
		}
		hasil = append(hasil, b)
	}

	// Serial untuk baris BARU: satu rentang dari tabel counter dengan periode
	// TANGGAL PESANAN ASLI (SystemDesign.md §4.5), sehingga nomor tidak bolong
	// dan tidak bentrok dengan serial pesanan lain di tanggal yang sama.
	if butuhSerial > 0 {
		periode, err := q.SerialPeriode(ctx, *row.Tanggal)
		if err != nil {
			return nil, fmt.Errorf("query SerialPeriode: %w", err)
		}
		pertama, err := q.NextNumbers(ctx, sqlc.NextNumbersParams{
			Scope:   scopeSerial,
			Periode: periode,
			Jumlah:  int32(butuhSerial),
		})
		if err != nil {
			return nil, fmt.Errorf("query NextNumbers(serial revisi): %w", err)
		}
		n := 0
		for i := range hasil {
			if hasil[i].serial != "" {
				continue
			}
			nomor, err := q.FormatSerialNumber(ctx, sqlc.FormatSerialNumberParams{
				Tanggal: *row.Tanggal,
				Urut:    pertama + int32(n),
			})
			if err != nil {
				return nil, fmt.Errorf("query FormatSerialNumber(revisi): %w", err)
			}
			hasil[i].serial = nomor
			n++
		}
	}

	final := make([]barisFinal, 0, len(hasil)+1)
	for i, b := range hasil {
		final = append(final, barisFinal{
			LineNo:      i + 1,
			Jenis:       order.JenisProduct,
			ProductID:   b.productID,
			Nama:        b.nama,
			Qty:         b.qty,
			HargaSatuan: b.harga,
			Serial:      b.serial,
		})
	}

	// Pesanan Sertifikat tetap punya TEPAT SATU baris packaging senilai setelan
	// server saat revisi (CP-SER-02/05).
	if row.IsSertifikat {
		final = append(final, barisFinal{
			LineNo:      len(final) + 1,
			Jenis:       order.JenisPackaging,
			Nama:        order.NamaBiayaPackaging,
			Qty:         1,
			HargaSatuan: req.BiayaPackaging,
		})
	}
	if len(final) == 0 {
		return nil, order.ErrEmptyLines
	}
	// Serial wajib lengkap untuk setiap baris produk (BR-09).
	for _, l := range final {
		if l.Jenis == order.JenisProduct && strings.TrimSpace(l.Serial) == "" {
			return nil, fmt.Errorf("%w: serial baris %d kosong", order.ErrInvalidData, l.LineNo)
		}
	}
	return final, nil
}

// snapshotVersi menyusun snapshot satu versi pesanan untuk kolom
// invoice_revisions.snapshot dan audit Before (Database.md §4.6).
func snapshotVersi(row sqlc.LockOrderForReviseRow, lines []barisLama) map[string]any {
	out := map[string]any{
		"id":            row.ID,
		"nomor":         row.Nomor,
		"branch_id":     row.BranchID,
		"versi":         int(row.Versi),
		"status":        row.Status,
		"total":         row.Total,
		"is_sertifikat": row.IsSertifikat,
		"is_cnc":        row.IsCnc,
		"catatan":       row.Catatan,
	}
	if row.Tanggal != nil {
		out["tanggal"] = row.Tanggal.Format(order.TanggalLayout)
	}
	out["lines"] = snapshotBarisLama(lines)
	return out
}

// snapshotBarisLama menyusun daftar baris versi lama untuk snapshot.
func snapshotBarisLama(lines []barisLama) []map[string]any {
	out := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		baris := map[string]any{
			"line_no":      l.LineNo,
			"jenis":        l.Jenis,
			"nama":         l.Nama,
			"qty":          l.Qty,
			"harga_satuan": l.HargaSatuan,
			"subtotal":     int64(l.Qty) * l.HargaSatuan,
		}
		if l.ProductID != "" {
			baris["product_id"] = l.ProductID
		}
		if l.Serial != "" {
			baris["serial_number"] = l.Serial
		}
		out = append(out, baris)
	}
	return out
}

// snapshotBaris menyusun daftar baris versi baru untuk audit After.
func snapshotBaris(lines []barisFinal) []map[string]any {
	out := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		baris := map[string]any{
			"line_no":      l.LineNo,
			"jenis":        l.Jenis,
			"nama":         l.Nama,
			"qty":          l.Qty,
			"harga_satuan": l.HargaSatuan,
			"subtotal":     int64(l.Qty) * l.HargaSatuan,
		}
		if l.ProductID != "" {
			baris["product_id"] = l.ProductID
		}
		if l.Serial != "" {
			baris["serial_number"] = l.Serial
		}
		out = append(out, baris)
	}
	return out
}

// garisKeluar mengubah baris final menjadi nilai domain untuk respons.
func garisKeluar(lines []barisFinal) []order.Line {
	out := make([]order.Line, 0, len(lines))
	for _, l := range lines {
		out = append(out, order.Line{
			LineNo:       l.LineNo,
			Jenis:        l.Jenis,
			ProductID:    l.ProductID,
			Nama:         l.Nama,
			Qty:          l.Qty,
			HargaSatuan:  l.HargaSatuan,
			Subtotal:     int64(l.Qty) * l.HargaSatuan,
			SerialNumber: l.Serial,
		})
	}
	return out
}

// jumlahItem menghitung Σ qty baris produk (baris packaging tidak dihitung).
func jumlahItem(lines []barisFinal) int64 {
	var n int64
	for _, l := range lines {
		if l.Jenis == order.JenisProduct {
			n += int64(l.Qty)
		}
	}
	return n
}

// teksRevisi menyusun kalimat timeline revisi (aktor + alasan + versi baru).
func teksRevisi(req order.ReviseRequest, versiBaru int) string {
	teks := req.TeksEvent
	if strings.TrimSpace(teks) == "" {
		teks = order.TeksEventRevisi(req.Actor.Nama, req.Alasan)
	}
	teks = fmt.Sprintf("%s (versi -R%d)", teks, versiBaru)
	if c := strings.TrimSpace(req.Catatan); c != "" {
		teks += " — catatan: " + c
	}
	return teks
}
