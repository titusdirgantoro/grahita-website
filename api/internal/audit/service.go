package audit

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/clock"
)

// Sentinel error domain pembacaan audit. Pemetaan ke kode HTTP ada di lapisan
// transport.
var (
	// ErrForbidden: aktor bukan Superadmin (kapabilitas `admin.audit`).
	ErrForbidden = errors.New("hanya Superadmin yang boleh membaca audit log")
	// ErrFilterInvalid: parameter filter/paginasi tidak valid.
	ErrFilterInvalid = errors.New("filter audit log tidak valid")
)

// Batas paginasi daftar audit.
const (
	// DefaultPerHalaman adalah jumlah baris per halaman bila klien tidak
	// mengirim `per_halaman`.
	DefaultPerHalaman = 25
	// MaxPerHalaman memagari permintaan besar: audit_logs tumbuh terus dan
	// tidak boleh dibaca seluruhnya dalam satu permintaan.
	MaxPerHalaman = 100
)

// LayoutTanggal adalah format tanggal filter (tanggal bisnis WIB).
const LayoutTanggal = "2006-01-02"

// EntitasSah adalah seluruh nilai kolom audit_logs.entity yang dikenal.
//
// Daftarnya sengaja eksplisit: filter dengan entitas tak dikenal dijawab 400
// supaya salah ketik tidak diam-diam menghilangkan filter (dan membuat admin
// mengira tidak ada aktivitas). Produsen entri baru WAJIB menambahkan
// konstantanya di sini agar entrinya bisa disaring.
func EntitasSah() []string {
	return []string{
		EntityAdminUser,
		EntityBankAccount,
		EntityBranch,
		EntityBranchPrice,
		EntityBranchUser,
		EntityCNCReminder,
		EntityEmail,
		EntityOrder,
		EntityProduct,
		EntitySetting,
		EntityTutorial,
	}
}

// AksiSah adalah seluruh nilai kolom audit_logs.action yang dikenal.
func AksiSah() []string {
	return []string{
		ActionBacaBukti,
		ActionCopy,
		ActionCreate,
		ActionDelete,
		ActionRegister,
		ActionRemind,
		ActionResend,
		ActionReset,
		ActionRevise,
		ActionTokenGenerate,
		ActionUpdate,
		ActionVerify,
	}
}

// Filter adalah permintaan pembacaan audit log dari klien. Seluruh nilainya
// datang dari query string sehingga bisa di-bookmark/di-share.
type Filter struct {
	// Entitas menyaring kolom entity; boleh diulang atau dipisah koma.
	Entitas []string `json:"entitas,omitempty"`
	// Aksi menyaring kolom action.
	Aksi string `json:"aksi,omitempty"`
	// Aktor menyaring pelaku: UUID berarti id aktor, selain itu pencarian nama.
	Aktor string `json:"aktor,omitempty"`
	// Dari/Sampai adalah rentang tanggal bisnis WIB (YYYY-MM-DD, inklusif).
	Dari   string `json:"dari,omitempty"`
	Sampai string `json:"sampai,omitempty"`
	// Halaman dimulai dari 1; PerHalaman dibatasi MaxPerHalaman.
	Halaman    int `json:"halaman,omitempty"`
	PerHalaman int `json:"per_halaman,omitempty"`
}

// Query adalah filter yang sudah dinormalkan dan siap dieksekusi repository.
type Query struct {
	Entitas    []string
	Aksi       string
	ActorID    string
	ActorNama  string
	Dari       string
	Sampai     string
	Offset     int
	Batch      int
	Halaman    int
	PerHalaman int
}

// Log adalah satu baris audit yang sudah aman ditampilkan.
type Log struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	ActorKind string    `json:"actor_kind"`
	ActorID   string    `json:"actor_id,omitempty"`
	ActorNama string    `json:"actor_nama,omitempty"`
	IP        string    `json:"ip,omitempty"`
	Entity    string    `json:"entity"`
	EntityID  string    `json:"entity_id,omitempty"`
	Action    string    `json:"action"`
	Before    any       `json:"before"`
	After     any       `json:"after"`
}

// Page adalah satu halaman audit log beserta ringkasan filter yang dipakai.
type Page struct {
	Items        []Log  `json:"items"`
	Total        int64  `json:"total"`
	Halaman      int    `json:"halaman"`
	PerHalaman   int    `json:"per_halaman"`
	TotalHalaman int    `json:"total_halaman"`
	Filter       Filter `json:"filter"`
}

// Reader adalah penyimpanan pembacaan audit (implementasi produksi:
// audit/postgres). Keduanya menerima Query yang sama sehingga total dan isi
// halaman tidak pernah berbeda definisi filter.
type Reader interface {
	List(ctx context.Context, q Query) ([]Log, error)
	Count(ctx context.Context, q Query) (int64, error)
}

// Recorder menulis satu entri audit di luar transaksi domain pemanggil.
//
// Dipakai HANYA untuk mutasi yang perubahannya hidup di repositori domain lain
// (mis. penerbitan tautan atur ulang akun oleh paket auth): entri ditulis
// setelah perubahan itu berhasil, dan entri tetap tidak pernah memuat nilai
// rahasianya. Mutasi yang transaksinya milik paket pemanggil wajib memakai
// jalur repository masing-masing agar entri dan perubahan berada di transaksi
// yang sama.
type Recorder interface {
	Record(ctx context.Context, e Entry) error
}

// Service adalah logika bisnis pembacaan audit log.
type Service struct {
	repo Reader
}

// NewService membuat service audit log.
func NewService(repo Reader) *Service { return &Service{repo: repo} }

// List mengembalikan satu halaman audit log untuk Superadmin.
//
// Otorisasi ditegakkan di dua tempat (router `admin.audit` dan fungsi ini) dan
// seluruh parameter divalidasi sebelum menyentuh database.
func (s *Service) List(ctx context.Context, actor auth.Actor, f Filter) (Page, error) {
	if !actor.Can(auth.CapAdminAudit) {
		return Page{}, ErrForbidden
	}
	norm, q, err := normalize(f)
	if err != nil {
		return Page{}, err
	}
	items, err := s.repo.List(ctx, q)
	if err != nil {
		return Page{}, err
	}
	total, err := s.repo.Count(ctx, q)
	if err != nil {
		return Page{}, err
	}
	if items == nil {
		items = []Log{}
	}
	// Redaksi saat menampilkan: baris yang ditulis sebelum F2-28 (atau oleh
	// produsen yang lupa memakai audit.NewEntry) tetap tidak bisa membocorkan
	// kata sandi/token/URL bukti ke layar.
	for i := range items {
		items[i].Before = Redact(items[i].Before)
		items[i].After = Redact(items[i].After)
	}
	totalHalaman := int((total + int64(q.Batch) - 1) / int64(q.Batch))
	return Page{
		Items:        items,
		Total:        total,
		Halaman:      q.Halaman,
		PerHalaman:   q.Batch,
		TotalHalaman: totalHalaman,
		Filter:       norm,
	}, nil
}

// uuidRe mengenali nilai UUID (aktor berbentuk id) tanpa memaksakan pustaka
// UUID di lapisan domain.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// normalize memvalidasi + merapikan filter klien menjadi Query.
func normalize(f Filter) (Filter, Query, error) {
	norm := Filter{
		Entitas: normEntitas(f.Entitas),
		Aksi:    strings.ToLower(strings.TrimSpace(f.Aksi)),
		Aktor:   strings.TrimSpace(f.Aktor),
	}
	if err := normEntitasSah(norm.Entitas); err != nil {
		return Filter{}, Query{}, err
	}
	if norm.Aksi != "" && !sah(norm.Aksi, AksiSah()) {
		return Filter{}, Query{}, fmt.Errorf("%w: aksi %q tidak dikenal", ErrFilterInvalid, f.Aksi)
	}

	dari, err := tanggal(f.Dari, "dari")
	if err != nil {
		return Filter{}, Query{}, err
	}
	sampai, err := tanggal(f.Sampai, "sampai")
	if err != nil {
		return Filter{}, Query{}, err
	}
	if dari != "" && sampai != "" && dari > sampai {
		return Filter{}, Query{}, fmt.Errorf("%w: tanggal awal melewati tanggal akhir", ErrFilterInvalid)
	}
	norm.Dari, norm.Sampai = dari, sampai

	q := Query{
		Entitas: norm.Entitas,
		Aksi:    norm.Aksi,
		Dari:    dari,
		Sampai:  sampai,
	}
	// Aktor: UUID berarti id akun; selain itu dianggap pencarian nama. Nama
	// kosong setelah dirapikan tidak menjadi filter apa pun.
	if norm.Aktor != "" {
		if uuidRe.MatchString(norm.Aktor) {
			q.ActorID = strings.ToLower(norm.Aktor)
		} else {
			q.ActorNama = norm.Aktor
		}
	}

	per := f.PerHalaman
	if per <= 0 {
		per = DefaultPerHalaman
	}
	if per > MaxPerHalaman {
		per = MaxPerHalaman
	}
	halaman := f.Halaman
	if halaman <= 0 {
		halaman = 1
	}
	q.Batch = per
	q.Halaman = halaman
	q.PerHalaman = per
	q.Offset = (halaman - 1) * per
	norm.Halaman = halaman
	norm.PerHalaman = per
	return norm, q, nil
}

// normEntitas memecah nilai yang boleh diulang maupun dipisah koma, merapikan
// huruf kecil, dan membuang duplikat dengan urutan yang stabil.
func normEntitas(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		for _, bagian := range strings.Split(raw, ",") {
			v := strings.ToLower(strings.TrimSpace(bagian))
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// normEntitasSah menolak entitas yang tidak dikenal.
func normEntitasSah(list []string) error {
	for _, e := range list {
		if !sah(e, EntitasSah()) {
			return fmt.Errorf("%w: entitas %q tidak dikenal", ErrFilterInvalid, e)
		}
	}
	return nil
}

// tanggal memvalidasi satu batas tanggal bisnis WIB (YYYY-MM-DD) dan
// mengembalikan bentuk kanoniknya; kosong berarti tanpa batas.
func tanggal(v, nama string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	t, err := time.ParseInLocation(LayoutTanggal, v, clock.Jakarta())
	if err != nil {
		return "", fmt.Errorf("%w: tanggal %s harus berformat YYYY-MM-DD", ErrFilterInvalid, nama)
	}
	return t.Format(LayoutTanggal), nil
}

// sah melaporkan apakah nilai ada pada daftar nilai yang dikenal.
func sah(v string, daftar []string) bool {
	for _, k := range daftar {
		if v == k {
			return true
		}
	}
	return false
}
