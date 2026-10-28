package httpapi

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/document"
	"github.com/grahita/portal/api/internal/fulfillment"
	"github.com/grahita/portal/api/internal/platform/jobs"
	"github.com/grahita/portal/api/internal/platform/pdf"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/grahita/portal/api/internal/receivable"
	"github.com/grahita/portal/api/internal/worker"
)

type rendererGangguan struct{ gagal bool }

func (r *rendererGangguan) HTMLToPDF(ctx context.Context, html []byte) ([]byte, error) {
	if r.gagal {
		return nil, pdf.ErrNotConfigured
	}
	return (rendererUji{}).HTMLToPDF(ctx, html)
}

func (r *rendererGangguan) Health(context.Context) error {
	if r.gagal {
		return pdf.ErrNotConfigured
	}
	return nil
}

type storageGangguan struct {
	storage.Store
	gagal bool
}

func (s *storageGangguan) Put(ctx context.Context, key string, content io.Reader, size int64, contentType string) error {
	if s.gagal {
		return errors.New("storage QA terputus")
	}
	return s.Store.Put(ctx, key, content, size, contentType)
}

// TestPGDOC16PemulihanDokumenMenahanEmail menguji dua status bisnis yang
// menghasilkan dokumen: Transfer done dan Tempo approved_unpaid. Pekerjaan
// yang gagal dapat dijalankan ulang setelah dependency pulih; email tidak
// diantre sebelum seluruh berkas ada, lalu tidak diduplikasi pada retry.
func TestPGDOC16PemulihanDokumenMenahanEmail(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	f.setAdminFlag(t, f.adminSA, true, true)
	renderer := &rendererGangguan{gagal: true}
	store := &storageGangguan{Store: f.objek}
	selesai := fulfillment.NewService(document.NewService(f.docRepo, store, renderer), f.notif)
	w := worker.NewRenderDokumenWorker(selesai)

	jalankan := func(orderID string) error {
		job := &river.Job[jobs.RenderDokumenArgs]{
			JobRow: &rivertype.JobRow{ID: 1},
			Args:   jobs.RenderDokumenArgs{OrderID: orderID},
		}
		return w.Work(ctx, job)
	}
	cekBelumAda := func(orderID, statusMau string) {
		t.Helper()
		var status string
		if err := f.pool.QueryRow(ctx, `SELECT status::text FROM orders WHERE id=$1`, orderID).Scan(&status); err != nil || status != statusMau {
			t.Fatalf("status bisnis berubah saat render gagal: %s err=%v, mau %s", status, err, statusMau)
		}
		if n := hitungBaris(t, f, "documents", "order_id", orderID); n != 0 {
			t.Fatalf("dokumen belum boleh diklaim siap: %d", n)
		}
		if n := hitungBaris(t, f, "email_outbox", "order_id", orderID); n != 0 {
			t.Fatalf("email belum boleh diantre: %d", n)
		}
	}
	cekPulih := func(orderID string) {
		t.Helper()
		if err := jalankan(orderID); err != nil {
			t.Fatalf("retry setelah dependency pulih: %v", err)
		}
		if n := hitungBaris(t, f, "documents", "order_id", orderID); n != 3 {
			t.Fatalf("retry harus menghasilkan tiga dokumen: %d", n)
		}
		if n := hitungBaris(t, f, "email_outbox", "order_id", orderID); n != 3 {
			t.Fatalf("email baru boleh diantre sesudah dokumen siap: %d", n)
		}
		if err := jalankan(orderID); err != nil {
			t.Fatalf("retry idempoten kedua: %v", err)
		}
		if n := hitungBaris(t, f, "email_outbox", "order_id", orderID); n != 3 {
			t.Fatalf("retry ulang menggandakan email: %d", n)
		}
	}

	transfer := f.seedPesananDone(t, f.branchA, "2026-10-0088", false)
	if err := jalankan(transfer.ID); err == nil {
		t.Fatal("renderer terputus harus membuat job gagal agar dapat diulang")
	}
	cekBelumAda(transfer.ID, "done")
	renderer.gagal = false
	store.gagal = true
	if err := jalankan(transfer.ID); err == nil {
		t.Fatal("storage terputus harus membuat job gagal agar dapat diulang")
	}
	cekBelumAda(transfer.ID, "done")
	store.gagal = false
	cekPulih(transfer.ID)

	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	tempoID := tempoSubmitted(t, f, "2026-10-20", 100000)
	manager := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	if _, err := receivable.New(f.pool, f.jobs).Approve(ctx, manager, tempoID); err != nil {
		t.Fatalf("approve Tempo: %v", err)
	}
	renderer.gagal = true
	if err := jalankan(tempoID); err == nil {
		t.Fatal("renderer terputus harus membuat job Tempo gagal")
	}
	cekBelumAda(tempoID, "approved_unpaid")
	renderer.gagal = false
	cekPulih(tempoID)
	var charge int64
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount-paid_amount),0)::bigint FROM tempo_charges WHERE order_id=$1`, tempoID).Scan(&charge); err != nil || charge != 100000 {
		t.Fatalf("render/retry mengubah piutang Tempo: sisa=%d err=%v", charge, err)
	}
	if hasil := f.kirimAntrean(t); hasil.Terkirim != 6 {
		t.Fatalf("setelah pulih enam email berlampiran harus terkirim, hasil %+v", hasil)
	}
}
