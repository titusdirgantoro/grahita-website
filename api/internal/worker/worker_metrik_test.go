package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"
)

type catatanJob struct {
	kind   string
	durasi time.Duration
	err    error
}

type pencatatPalsu struct {
	mu      sync.Mutex
	catatan []catatanJob
}

func (p *pencatatPalsu) CatatJob(kind string, durasi time.Duration, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.catatan = append(p.catatan, catatanJob{kind: kind, durasi: durasi, err: err})
}

func (p *pencatatPalsu) semua() []catatanJob {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]catatanJob, len(p.catatan))
	copy(out, p.catatan)
	return out
}

// TestHookMetrikJobMencatatHasil membuktikan hook River mencatat jenis, durasi,
// dan hasil pekerjaan, serta meneruskan error apa adanya (Architecture.md §8).
func TestHookMetrikJobMencatatHasil(t *testing.T) {
	pencatat := &pencatatPalsu{}
	hooks := HookMetrikJob(pencatat)
	if len(hooks) != 2 {
		t.Fatalf("jumlah hook = %d, ingin 2 (WorkBegin + WorkEnd)", len(hooks))
	}

	job := &rivertype.JobRow{ID: 17, Kind: "render_dokumen_pesanan"}
	ctx := context.Background()

	var adaBegin bool
	for _, h := range hooks {
		if hb, ok := h.(rivertype.HookWorkBegin); ok {
			adaBegin = true
			if err := hb.WorkBegin(ctx, job); err != nil {
				t.Fatalf("WorkBegin: %v", err)
			}
		}
	}
	if !adaBegin {
		t.Fatal("tidak ada hook WorkBegin")
	}

	// Beri jeda kecil supaya durasi terbaca > 0.
	time.Sleep(2 * time.Millisecond)

	penyebab := errors.New("gotenberg belum siap")
	var diteruskan error
	for _, h := range hooks {
		if he, ok := h.(rivertype.HookWorkEnd); ok {
			diteruskan = he.WorkEnd(ctx, job, penyebab)
		}
	}
	if !errors.Is(diteruskan, penyebab) {
		t.Fatalf("WorkEnd mengembalikan %v, ingin error yang sama diteruskan", diteruskan)
	}

	catatan := pencatat.semua()
	if len(catatan) != 1 {
		t.Fatalf("jumlah catatan = %d, ingin 1", len(catatan))
	}
	if catatan[0].kind != "render_dokumen_pesanan" {
		t.Errorf("kind = %q", catatan[0].kind)
	}
	if catatan[0].durasi <= 0 {
		t.Errorf("durasi = %v, ingin > 0", catatan[0].durasi)
	}
	if !errors.Is(catatan[0].err, penyebab) {
		t.Errorf("error tercatat = %v", catatan[0].err)
	}

	// Job kedua tanpa WorkBegin: tetap dicatat tanpa durasi (tidak panic).
	for _, h := range hooks {
		if he, ok := h.(rivertype.HookWorkEnd); ok {
			_ = he.WorkEnd(ctx, &rivertype.JobRow{ID: 99, Kind: "ping"}, nil)
		}
	}
	if got := pencatat.semua(); len(got) != 2 || got[1].durasi != 0 || got[1].err != nil {
		t.Fatalf("catatan job kedua = %+v", got)
	}
}

func TestHookMetrikJobNilTidakAdaHook(t *testing.T) {
	if hooks := HookMetrikJob(nil); hooks != nil {
		t.Fatalf("HookMetrikJob(nil) = %v, ingin nil", hooks)
	}
}
