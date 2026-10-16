package worker

// Uji unit pekerja pembacaan bukti AI/OCR (kartu F2-26): pekerja hanya
// meneruskan pesanan ke layanan pembacaan dan tidak pernah menelan kegagalan
// (supaya River mengulanginya). Penyimpanan & keputusannya diuji di paket
// payment dan pada uji integrasi PostgreSQL.

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/jobs"
)

// pembacaBuktiPalsu mencatat panggilan dan mengembalikan hasil/galat tetap.
type pembacaBuktiPalsu struct {
	dipanggil      int
	dipanggilUlang int
	orderID        string
	version        int64
	fingerprint    string
	hasil          payment.OCRHasil
	err            error
}

func (p *pembacaBuktiPalsu) Baca(_ context.Context, orderID string) (payment.OCRHasil, error) {
	p.dipanggil++
	p.orderID = orderID
	if p.err != nil {
		return payment.OCRHasil{}, p.err
	}
	return p.hasil, nil
}

func (p *pembacaBuktiPalsu) BacaUlang(_ context.Context, orderID string, version int64, fingerprint string) (payment.OCRHasil, error) {
	p.dipanggilUlang++
	p.orderID = orderID
	p.version = version
	p.fingerprint = fingerprint
	if p.err != nil {
		return payment.OCRHasil{}, p.err
	}
	return p.hasil, nil
}

func pekerjaanOCR(orderID string) *river.Job[jobs.OCRBuktiArgs] {
	return &river.Job[jobs.OCRBuktiArgs]{
		JobRow: &rivertype.JobRow{ID: 1},
		Args:   jobs.OCRBuktiArgs{OrderID: orderID},
	}
}

func TestOCRBuktiWorkerMeneruskanPesanan(t *testing.T) {
	pembaca := &pembacaBuktiPalsu{hasil: payment.OCRHasil{
		OrderID:     "order-1",
		JumlahBukti: 2,
		Dibaca:      1,
		Keputusan:   &payment.Keputusan{Hasil: payment.VerifCocok},
	}}
	w := NewOCRBuktiWorker(pembaca)

	if w.Kind() != jobs.KindOCRBukti {
		t.Fatalf("kind harus %q, dapat %q", jobs.KindOCRBukti, w.Kind())
	}
	if err := w.Work(context.Background(), pekerjaanOCR("order-1")); err != nil {
		t.Fatalf("pekerjaan sah tidak boleh gagal: %v", err)
	}
	if pembaca.dipanggil != 1 || pembaca.orderID != "order-1" {
		t.Errorf("layanan harus dipanggil sekali untuk order-1, dapat %d/%q", pembaca.dipanggil, pembaca.orderID)
	}
}

func TestOCRBuktiWorkerMeneruskanSnapshotReevaluate(t *testing.T) {
	pembaca := &pembacaBuktiPalsu{hasil: payment.OCRHasil{OrderID: "order-4"}}
	w := NewOCRBuktiWorker(pembaca)
	job := pekerjaanOCR("order-4")
	job.Args.AllowlistVersion = 7
	job.Args.ProofSetFingerprint = "abcd"
	if err := w.Work(context.Background(), job); err != nil {
		t.Fatalf("reevaluate sah tidak boleh gagal: %v", err)
	}
	if pembaca.dipanggil != 0 || pembaca.dipanggilUlang != 1 || pembaca.version != 7 || pembaca.fingerprint != "abcd" {
		t.Fatalf("snapshot reevaluate tidak diteruskan utuh: %+v", pembaca)
	}
}

func TestOCRBuktiWorkerMeneruskanGalatSupayaDiulang(t *testing.T) {
	sebab := errors.New("provider tumbang")
	pembaca := &pembacaBuktiPalsu{err: sebab}
	w := NewOCRBuktiWorker(pembaca)
	if err := w.Work(context.Background(), pekerjaanOCR("order-2")); !errors.Is(err, sebab) {
		t.Fatalf("galat layanan harus diteruskan apa adanya, dapat %v", err)
	}
}

func TestOCRBuktiWorkerTanpaLayananGagalTerangTerangan(t *testing.T) {
	w := NewOCRBuktiWorker(nil)
	if err := w.Work(context.Background(), pekerjaanOCR("order-3")); err == nil {
		t.Fatal("tanpa layanan pekerjaan harus gagal supaya terlihat, bukan dilepas")
	}
}
