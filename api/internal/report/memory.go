package report

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/grahita/portal/api/internal/platform/clock"
)

// Memory adalah repository laporan in-memory: dipakai pengujian unit dan
// pengujian router tanpa PostgreSQL. Semantik filternya SENGAJA dibuat sama
// dengan query SQL (report.sql) supaya perilaku yang diuji di sini tidak
// berbeda dari produksi.
type Memory struct {
	mu sync.Mutex

	lines []Line
	jobs  map[string]Job
	urut  []string
	seq   int

	// kueri mencatat setiap permintaan baris (untuk membuktikan ekspor berjalan
	// bertahap, bukan sekali ambil seluruh data).
	kueri []Query
	// puts menghitung objek hasil ekspor yang ditulis (uji idempotensi).
	TanpaAntrean bool
}

// NewMemory membuat repository laporan in-memory.
func NewMemory() *Memory { return &Memory{jobs: map[string]Job{}} }

// TambahLines menambahkan baris laporan ke penyimpanan (bantuan pengujian).
func (m *Memory) TambahLines(lines ...Line) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lines = append(m.lines, lines...)
}

// Kueri mengembalikan seluruh permintaan baris yang pernah diterima.
func (m *Memory) Kueri() []Query {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Query, len(m.kueri))
	copy(out, m.kueri)
	return out
}

// Lines mengembalikan seluruh baris tersimpan.
func (m *Memory) Lines() []Line {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Line, len(m.lines))
	copy(out, m.lines)
	return out
}

// ListLines mengembalikan satu batch baris sesuai Query.
func (m *Memory) ListLines(_ context.Context, q Query) ([]Line, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kueri = append(m.kueri, q)

	cocok := make([]Line, 0, len(m.lines))
	for _, l := range m.lines {
		if barisCocok(l, q) {
			cocok = append(cocok, l)
		}
	}
	sort.SliceStable(cocok, func(i, j int) bool { return lebihAwal(cocok[i], cocok[j]) })

	if q.Offset > 0 {
		if q.Offset >= len(cocok) {
			return []Line{}, nil
		}
		cocok = cocok[q.Offset:]
	}
	if q.Batch > 0 && len(cocok) > q.Batch {
		cocok = cocok[:q.Batch]
	}
	return cocok, nil
}

// CountLines menghitung baris dan total subtotal filter yang sama.
func (m *Memory) CountLines(_ context.Context, q Query) (int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var jumlah, total int64
	for _, l := range m.lines {
		if barisCocok(l, q) {
			jumlah++
			total += l.Subtotal
		}
	}
	return jumlah, total, nil
}

// InsertJob membuat pekerjaan ekspor.
func (m *Memory) InsertJob(_ context.Context, req JobRequest) (Job, error) {
	if m.TanpaAntrean {
		return Job{}, ErrQueueUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	job := Job{
		ID:          fmt.Sprintf("00000000-0000-4000-8000-%012d", m.seq),
		Jenis:       req.Jenis,
		Status:      StatusQueued,
		DimintaNama: req.DimintaNama,
		Filter:      req.Filter,
		CreatedAt:   time.Now().UTC(),
	}
	m.jobs[job.ID] = job
	m.urut = append(m.urut, job.ID)
	return job, nil
}

// GetJob mengembalikan satu pekerjaan.
func (m *Memory) GetJob(_ context.Context, id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	return job, nil
}

// MarkJobProses menandai pekerjaan sedang dikerjakan.
func (m *Memory) MarkJobProses(_ context.Context, id string) error {
	return m.ubah(id, func(j *Job) {
		j.Status = StatusProses
		sekarang := time.Now().UTC()
		if j.StartedAt == nil {
			j.StartedAt = &sekarang
		}
	})
}

// MarkJobSelesai menandai pekerjaan selesai beserta metadata berkasnya.
func (m *Memory) MarkJobSelesai(_ context.Context, id, storageKey, namaFile string, jumlahBaris int64) error {
	return m.ubah(id, func(j *Job) {
		j.Status = StatusSelesai
		j.StorageKey = storageKey
		j.NamaFile = namaFile
		j.JumlahBaris = jumlahBaris
		j.Pesan = ""
		sekarang := time.Now().UTC()
		j.FinishedAt = &sekarang
	})
}

// MarkJobGagal menandai pekerjaan gagal.
func (m *Memory) MarkJobGagal(_ context.Context, id, pesan string) error {
	return m.ubah(id, func(j *Job) {
		if j.Status == StatusSelesai {
			return
		}
		j.Status = StatusGagal
		j.Pesan = pesan
	})
}

func (m *Memory) ubah(id string, fn func(*Job)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	fn(&job)
	m.jobs[id] = job
	return nil
}

// barisCocok meniru WHERE pada report.sql.
func barisCocok(l Line, q Query) bool {
	if l.Status == "" || l.Status == "draft" {
		return false
	}
	tanggal := l.TanggalWaktu.In(clock.Jakarta()).Format(LayoutTanggal)
	if q.Dari != nil && tanggal < q.Dari.Format(LayoutTanggal) {
		return false
	}
	if q.Sampai != nil && tanggal > q.Sampai.Format(LayoutTanggal) {
		return false
	}
	if len(q.Cabang) > 0 && !berisi(q.Cabang, l.BranchID) {
		return false
	}
	if q.Status != "" && l.Status != q.Status {
		return false
	}
	if q.Jenis != "" && l.Jenis != q.Jenis {
		return false
	}
	if q.IsCNC != nil && l.IsCNC != *q.IsCNC {
		return false
	}
	if q.IsSpecial != nil && l.IsSpecial != *q.IsSpecial {
		return false
	}
	if q.PajakIn != nil && l.PajakIn != *q.PajakIn {
		return false
	}
	if q.SetelahTanggal != nil {
		kursor := Line{
			Nomor:        q.SetelahNomor,
			LineNo:       q.SetelahLineNo,
			TanggalWaktu: *q.SetelahTanggal,
		}
		if !lebihAwal(kursor, l) {
			return false
		}
	}
	return true
}

// lebihAwal membandingkan urutan (tanggal, nomor, line_no) seperti ORDER BY.
func lebihAwal(a, b Line) bool {
	ta := a.TanggalWaktu.In(clock.Jakarta()).Format(LayoutTanggal)
	tb := b.TanggalWaktu.In(clock.Jakarta()).Format(LayoutTanggal)
	if ta != tb {
		return ta < tb
	}
	if a.Nomor != b.Nomor {
		return a.Nomor < b.Nomor
	}
	return a.LineNo < b.LineNo
}

func berisi(daftar []string, nilai string) bool {
	for _, v := range daftar {
		if v == nilai {
			return true
		}
	}
	return false
}

var _ Repository = (*Memory)(nil)
