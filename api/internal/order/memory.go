package order

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Memory adalah implementasi Repository in-memory untuk pengujian.
//
// Ia meniru perilaku penting repository PostgreSQL: pesanan difilter dengan
// branch_id, hanya draft yang bisa diubah/dihapus, total dan jumlah item
// dihitung dari barisnya (bukan dari klien), dan draft selalu tanpa nomor,
// tanggal, atau serial number.
//
// Submit (F1-14) juga ditiru: urutan nomor invoice/serial dari tabel counter,
// snapshot harga terbaru dari master yang disuntikkan lewat SetHargaCabang,
// kewajiban bukti untuk pesanan non-C&C (SetProofs), dan timeline "Pesanan
// dikirim". Nilai yang dihasilkan TestSubmit* di paket ini memakai Memory;
// kebenaran penomoran/trigger sebenarnya diuji di uji integrasi PostgreSQL.
type Memory struct {
	mu     sync.Mutex
	orders []Order
	seq    int

	// hargaCabang adalah keadaan master+harga per cabang yang dipakai saat
	// submit; cabang/produk yang tidak ada di peta ini diperlakukan sebagai
	// "tidak dijual".
	hargaCabang map[string]map[string]MemoryProduct
	// proofs adalah jumlah bukti transfer per pesanan (0 = belum ada).
	proofs map[string]int
	// counters adalah nilai terakhir tabel numbering_counters per
	// "scope|periode", cermin Database.md §6.
	counters map[string]int
	// events adalah teks timeline per pesanan.
	events map[string][]string
}

// MemoryProduct adalah keadaan satu produk di mata Memory: master (nama,
// satuan, jenis, aktif) beserta harga untuk cabang tersebut.
type MemoryProduct struct {
	Nama   string
	Satuan string
	Jenis  string // "" diperlakukan sebagai produk reguler
	Aktif  bool
	Harga  int64
}

// NewMemory membuat penyimpanan pesanan in-memory kosong.
func NewMemory() *Memory {
	return &Memory{
		hargaCabang: map[string]map[string]MemoryProduct{},
		proofs:      map[string]int{},
		counters:    map[string]int{},
		events:      map[string][]string{},
	}
}

// SetHargaCabang menetapkan master + harga produk yang berlaku untuk satu
// cabang (pengganti branch_prices + products).
func (m *Memory) SetHargaCabang(branchID string, produk map[string]MemoryProduct) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hargaCabang[branchID] = produk
}

// SetProofs menetapkan jumlah bukti transfer tersimpan satu pesanan.
func (m *Memory) SetProofs(orderID string, jumlah int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.proofs[orderID] = jumlah
}

// Proofs mengembalikan jumlah bukti satu pesanan.
func (m *Memory) Proofs(orderID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.proofs[orderID]
}

// Events mengembalikan salinan teks timeline satu pesanan.
func (m *Memory) Events(orderID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.events[orderID]))
	copy(out, m.events[orderID])
	return out
}

// Counter mengembalikan nilai terakhir satu counter (scope, periode).
func (m *Memory) Counter(scope, periode string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[scope+"|"+periode]
}

// Put menyisipkan pesanan yang sudah ada (untuk menguji isolasi cabang,
// pesanan yang terkunci, dan pesanan yang sudah dikirim). ID dibuat otomatis
// bila kosong dan total jumlah item dihitung dari barisnya.
func (m *Memory) Put(o Order) Order {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.putLocked(o)
}

func (m *Memory) putLocked(o Order) Order {
	if o.ID == "" {
		o.ID = m.newID()
	}
	if o.Status == "" {
		o.Status = StatusDraft
	}
	if o.VerifStatus == "" {
		o.VerifStatus = "belum"
	}
	if o.CreatedAt.IsZero() {
		now := time.Now().UTC()
		o.CreatedAt, o.UpdatedAt = now, now
	}
	o.Total, o.JumlahItem = hitung(o.Lines)
	// is_sertifikat diturunkan dari isi baris, sama seperti trigger
	// order_lines_recalc di PostgreSQL — bukan disetel dari luar.
	o.IsSertifikat = adaPackaging(o.Lines)
	m.orders = append(m.orders, o)
	return o
}

// newID menerbitkan id berbentuk UUID (seperti uuidv7 di PostgreSQL) supaya
// pengujian in-memory melewati pemeriksaan format id di service.
func (m *Memory) newID() string {
	m.seq++
	return fmt.Sprintf("11111111-1111-4111-8111-%012x", m.seq)
}

// Count mengembalikan jumlah pesanan tersimpan (dipakai pengujian).
func (m *Memory) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.orders)
}

// Lines mengembalikan baris satu pesanan (dipakai pengujian).
func (m *Memory) Lines(id string) []Line {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.orders {
		if o.ID == id {
			out := make([]Line, len(o.Lines))
			copy(out, o.Lines)
			return out
		}
	}
	return nil
}

// List mengembalikan pesanan satu cabang, terbaru lebih dahulu.
func (m *Memory) List(_ context.Context, branchID, status string) ([]Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Order, 0, len(m.orders))
	for _, o := range m.orders {
		if o.BranchID != branchID {
			continue
		}
		if status != "" && o.Status != status {
			continue
		}
		out = append(out, m.publik(o))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return kunciUrut(out[i]).After(kunciUrut(out[j]))
	})
	return out, nil
}

// Get mengembalikan satu pesanan milik cabang tersebut.
func (m *Memory) Get(_ context.Context, id, branchID string) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.cari(id, branchID)
	if idx < 0 {
		return Order{}, ErrNotFound
	}
	return m.publik(m.orders[idx]), nil
}

// CreateDraft menyimpan draft baru.
func (m *Memory) CreateDraft(_ context.Context, branchID, createdBy, catatan string, lines []Line) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.putLocked(Order{
		BranchID:  branchID,
		Status:    StatusDraft,
		Catatan:   catatan,
		CreatedBy: createdBy,
		Lines:     lines,
	})
	return m.publik(o), nil
}

// UpdateDraft mengganti catatan dan seluruh baris draft.
func (m *Memory) UpdateDraft(_ context.Context, id, branchID, catatan string, lines []Line) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.cari(id, branchID)
	if idx < 0 || m.orders[idx].Status != StatusDraft {
		return Order{}, ErrNotFound
	}
	m.orders[idx].Catatan = catatan
	m.orders[idx].Lines = lines
	m.orders[idx].Total, m.orders[idx].JumlahItem = hitung(lines)
	m.orders[idx].IsSertifikat = adaPackaging(lines)
	m.orders[idx].UpdatedAt = time.Now().UTC()
	return m.publik(m.orders[idx]), nil
}

// DeleteDraft menghapus draft milik cabang tersebut.
func (m *Memory) DeleteDraft(_ context.Context, id, branchID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.cari(id, branchID)
	if idx < 0 || m.orders[idx].Status != StatusDraft {
		return ErrNotFound
	}
	m.orders = append(m.orders[:idx], m.orders[idx+1:]...)
	return nil
}

// Submit meniru transaksi submit PostgreSQL: validasi ulang isi draft
// (SiapkanSubmit), kewajiban bukti non-C&C, penomoran invoice + serial, lalu
// penulisan status/tanggal/tenggat/timeline.
//
// Seluruh pemeriksaan yang bisa gagal dilakukan SEBELUM counter bergerak,
// sehingga submit yang ditolak tidak memakai nomor — sama seperti rollback
// transaksi di PostgreSQL (BR-09).
func (m *Memory) Submit(_ context.Context, req SubmitRequest) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.cari(req.ID, req.BranchID)
	if idx < 0 {
		return Order{}, ErrNotFound
	}
	o := &m.orders[idx]
	if o.Status != StatusDraft {
		return Order{}, ErrLocked
	}
	if !req.IsCNC && m.proofs[o.ID] < 1 {
		return Order{}, ErrProofRequired
	}

	// Baris draft + keadaan master/harga cabang saat submit.
	dijual := m.hargaCabang[o.BranchID]
	lines := make([]SubmitLine, 0, len(o.Lines))
	for _, l := range o.Lines {
		sl := SubmitLine{
			LineNo:     l.LineNo,
			Jenis:      l.Jenis,
			Qty:        l.Qty,
			NamaLama:   l.Nama,
			SatuanLama: l.Satuan,
		}
		if l.Jenis == JenisProduct {
			if p, ok := dijual[l.ProductID]; ok {
				sl.ProdukAda = true
				sl.ProdukAktif = p.Aktif
				sl.ProdukNama = p.Nama
				sl.ProdukSatuan = p.Satuan
				sl.ProdukJenis = p.Jenis
				sl.HargaCabang = p.Harga
				sl.AdaHargaCabang = true
			}
		}
		lines = append(lines, sl)
	}

	snapshots, err := SiapkanSubmit(lines, req.BiayaPackaging)
	if err != nil {
		return Order{}, err
	}

	// Penomoran: satu rentang serial untuk seluruh baris produk (urutan
	// baris = urutan serial, sama seperti rentang next_numbers di PostgreSQL).
	periodeInvoice := req.Tanggal.Format("2006-01")
	urutInvoice := m.naikkanCounter("invoice", periodeInvoice, 1)
	nomor := formatInvoiceNumber(req.Tanggal, urutInvoice, req.DigitInvoice)

	var productLines []string // product_id, urut baris
	for _, l := range o.Lines {
		if l.Jenis == JenisProduct {
			productLines = append(productLines, l.ProductID)
		}
	}
	serial := map[int]string{}
	if len(productLines) > 0 {
		periodeSerial := req.Tanggal.Format("20060102")
		pertama := m.naikkanCounter("serial", periodeSerial, len(productLines))
		i := 0
		for _, l := range o.Lines {
			if l.Jenis != JenisProduct {
				continue
			}
			serial[l.LineNo] = formatSerialNumber(req.Tanggal, pertama+i)
			i++
		}
	}

	// Penulisan: snapshot, serial, lalu status/tanggal/tenggat.
	for _, s := range snapshots {
		for i := range o.Lines {
			if o.Lines[i].LineNo != s.LineNo {
				continue
			}
			o.Lines[i].Nama = s.Nama
			o.Lines[i].Satuan = s.Satuan
			o.Lines[i].HargaSatuan = s.HargaSatuan
			o.Lines[i].Subtotal = int64(o.Lines[i].Qty) * s.HargaSatuan
			o.Lines[i].SerialNumber = serial[s.LineNo]
		}
	}

	now := time.Now().UTC()
	o.Nomor = nomor
	o.Status = StatusSubmitted
	o.IsCNC = req.IsCNC
	o.Tanggal = req.Tanggal.Format(TanggalLayout)
	o.JatuhTempo = ""
	if req.IsCNC && !req.JatuhTempo.IsZero() {
		o.JatuhTempo = req.JatuhTempo.Format(TanggalLayout)
	}
	o.SubmittedAt = now.Format(time.RFC3339)
	o.UpdatedAt = now
	o.Total, o.JumlahItem = hitung(o.Lines)
	o.IsSertifikat = adaPackaging(o.Lines)
	m.events[o.ID] = append(m.events[o.ID], TeksEventSubmit)
	return m.publik(*o), nil
}

// naikkanCounter meminta n nomor berurutan dari counter (scope, periode) dan
// mengembalikan nomor pertama — cermin next_numbers() di migrasi 0002.
func (m *Memory) naikkanCounter(scope, periode string, n int) int {
	kunci := scope + "|" + periode
	m.counters[kunci] += n
	return m.counters[kunci] - n + 1
}

// formatInvoiceNumber mencerminkan format_invoice_number() migrasi 0004:
// YYYY-MM-XX, digit melebar bila urutan melebihi `digit`.
func formatInvoiceNumber(tanggal time.Time, urut, digit int) string {
	return tanggal.Format("2006-01") + "-" + pad(urut, digit)
}

// formatSerialNumber mencerminkan format_serial_number() migrasi 0004:
// YYYYMMDDXXX, minimal 3 digit.
func formatSerialNumber(tanggal time.Time, urut int) string {
	return tanggal.Format("20060102") + pad(urut, 3)
}

// pad melengkapi angka dengan nol di depan sampai `minimal` digit; bila
// angkanya sudah lebih panjang, angkanya dibiarkan apa adanya (digit melebar).
func pad(n, minimal int) string {
	s := strconv.Itoa(n)
	for len(s) < minimal {
		s = "0" + s
	}
	return s
}

// cari mengembalikan indeks pesanan id milik branchID (-1 bila tidak ada).
func (m *Memory) cari(id, branchID string) int {
	for i, o := range m.orders {
		if o.ID == id && o.BranchID == branchID {
			return i
		}
	}
	return -1
}

// publik mengembalikan salinan dengan barisnya agar pemanggil tidak bisa
// mengubah isi penyimpanan.
func (m *Memory) publik(o Order) Order {
	out := o
	out.Lines = make([]Line, len(o.Lines))
	copy(out.Lines, o.Lines)
	return out
}

// hitung mengembalikan total dan jumlah item dari baris pesanan.
func hitung(lines []Line) (int64, int64) {
	var total, item int64
	for _, l := range lines {
		total += int64(l.Qty) * l.HargaSatuan
		if l.Jenis == JenisProduct {
			item += int64(l.Qty)
		}
	}
	return total, item
}

// adaPackaging melaporkan apakah baris pesanan memuat baris biaya packaging.
// Baris packaging hanya pernah lahir untuk pesanan Sertifikat (CP-SER-02),
// jadi keberadaannya menjadi penanda is_sertifikat yang sama dengan yang
// dihitung trigger order_lines_recalc dari products.jenis di PostgreSQL.
func adaPackaging(lines []Line) bool {
	for _, l := range lines {
		if l.Jenis == JenisPackaging {
			return true
		}
	}
	return false
}

// kunciUrut adalah waktu pengurutan daftar: tanggal submit bila ada (draft
// tidak punya tanggal), selain itu created_at — sama seperti query PostgreSQL.
func kunciUrut(o Order) time.Time {
	if o.Tanggal != "" {
		if t, err := time.Parse(TanggalLayout, o.Tanggal); err == nil {
			return t
		}
	}
	return o.CreatedAt
}
