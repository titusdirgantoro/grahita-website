// Package tutorial mengelola konten halaman Tutorial portal cabang
// (PRD §3.8 dan §10 fase 2).
//
// Konten disimpan TERSTRUKTUR di database — judul, ringkasan, daftar langkah,
// dan kunci ilustrasi bawaan — sehingga Superadmin dapat memperbaruinya lewat
// API dan portal cabang yang di-refresh langsung menampilkan versi terbaru
// TANPA build/deploy portal (kriteria kartu F2-27).
//
// Batas yang ditegakkan di layer ini (bukan hanya di router atau UI):
//   - Konten adalah TEKS BIASA. Tidak ada HTML arbitrer: seluruh teks
//     disanitasi (komentar + tag dibuang) SEBELUM divalidasi panjangnya, dan
//     ilustrasi hanya boleh memilih kunci bawaan aplikasi (IkonBawaan()) —
//     ilustrasi tetap di-render aplikasi, jadi tidak ada markup/SVG yang bisa
//     masuk lewat data.
//   - Hanya Superadmin (auth.CapAdminTutorial) yang boleh mengubah konten.
//     Manager dan akun cabang ditolak, dan pembacaan tidak pernah membuka
//     jalan tulis apa pun.
//   - Setiap perubahan dicatat ke audit_logs DI DALAM transaksi yang sama
//     (SystemDesign.md §5.4) per topik: tambah/ubah/hapus beserta snapshot
//     before/after; mengirim ulang konten yang sama tidak menulis apa pun.
package tutorial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// Sentinel error domain. Pemetaan ke kode HTTP ada di lapisan transport.
var (
	// ErrForbidden: aktor tidak berhak mengubah konten tutorial.
	ErrForbidden = errors.New("tidak berhak mengubah konten tutorial")
	// ErrInvalidContent: konten tidak lolos validasi/sanitasi. Detail per
	// bagian ada pada *ValidationError.
	ErrInvalidContent = errors.New("konten tutorial tidak valid")
	// ErrNotFound: topik yang dirujuk tidak ada.
	ErrNotFound = errors.New("topik tutorial tidak ditemukan")
)

// Kunci ilustrasi bawaan. Ilustrasi hidup di aplikasi (SVG inline), jadi
// kunci inilah batas antara konten di database dan tampilan: tidak ada berkas
// atau markup yang pernah datang dari data.
const (
	IkonMemesan    = "memesan"
	IkonBukti      = "bukti"
	IkonSertifikat = "sertifikat"
	IkonCNC        = "cnc"
	// IkonUmum adalah ilustrasi netral untuk topik baru yang tidak punya
	// ilustrasi khusus.
	IkonUmum = "umum"
)

// Batas konten. Dipilih cukup longgar untuk pemakaian nyata, tetapi tetap
// menolak nilai yang jelas salah (kosong atau tak terbatas) — sekaligus
// dijaga oleh CHECK constraint tabel tutorial_topics.
const (
	// MaxTopik adalah jumlah topik maksimum dalam satu halaman tutorial.
	MaxTopik = 12
	// MaxLangkah adalah jumlah butir langkah maksimum per topik.
	MaxLangkah = 24
	// MaxJudul / MaxRingkas / MaxLangkahTeks adalah panjang maksimum (dalam
	// karakter/runes, bukan byte) satu potong teks konten.
	MaxJudul       = 120
	MaxRingkas     = 300
	MaxLangkahTeks = 500
)

// langkahMaksBait membatasi besar JSON langkah sebelum diurai, supaya payload
// yang keterlaluan besar tidak pernah sampai ke database.
const langkahMaksBait = MaxLangkah * (MaxLangkahTeks*4 + 16)

// Topic adalah satu topik tutorial yang tersimpan.
type Topic struct {
	ID        string    `json:"id"`
	Ikon      string    `json:"ikon"`
	Judul     string    `json:"judul"`
	Ringkas   string    `json:"ringkas"`
	Langkah   []string  `json:"langkah"`
	Urutan    int       `json:"urutan"`
	UpdatedAt time.Time `json:"-"`
}

// Input adalah satu topik yang dikirim klien. ID kosong berarti topik baru.
type Input struct {
	ID      string   `json:"id"`
	Ikon    string   `json:"ikon"`
	Judul   string   `json:"judul"`
	Ringkas string   `json:"ringkas"`
	Langkah []string `json:"langkah"`
}

// ValidationError membawa pesan per bagian konten yang gagal divalidasi.
type ValidationError struct {
	Fields map[string]string
}

// Error memenuhi interface error dengan pesan yang stabil dan terurut.
func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pesan := make([]string, 0, len(keys))
	for _, k := range keys {
		pesan = append(pesan, k+": "+e.Fields[k])
	}
	return "konten tutorial tidak valid: " + strings.Join(pesan, "; ")
}

// Is membuat errors.Is(err, ErrInvalidContent) benar.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalidContent }

// Unwrap menjaga rantai error tetap dapat diperiksa.
func (e *ValidationError) Unwrap() error { return ErrInvalidContent }

// Repository menyimpan konten tutorial.
type Repository interface {
	// List mengembalikan seluruh topik terurut sesuai urutan tampil.
	List(ctx context.Context) ([]Topic, error)
	// Apply menyimpan tambah/ubah/hapus beserta catatan auditnya dalam SATU
	// transaksi: bila satu langkah gagal, tidak ada yang tersimpan dan tidak
	// ada event audit yang tertinggal.
	Apply(ctx context.Context, actorID string, tambah, ubah []Topic, hapus []string, recs []audit.Entry) error
}

// Service adalah logika bisnis konten tutorial.
type Service struct {
	repo Repository
}

// NewService membuat service konten tutorial.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// List mengembalikan konten tutorial yang berlaku untuk portal cabang.
//
// Pembacaan tidak membutuhkan kapabilitas khusus (halaman tutorial adalah
// halaman informasi), tetapi tetap menuntut aktor terautentikasi: tidak ada
// permukaan publik yang membocorkan konten internal sebelum login.
func (s *Service) List(ctx context.Context, actor auth.Actor) ([]Topic, error) {
	if !actor.IsAuthenticated() {
		return nil, ErrForbidden
	}
	return s.repo.List(ctx)
}

// Replace menggantikan SELURUH konten tutorial dengan daftar yang dikirim
// (Superadmin). Urutan tampil = urutan array yang dikirim; topik yang tidak
// disertakan tetapi ada di database DIHAPUS.
//
// Validasi & sanitasi dijalankan untuk seluruh topik LEBIH DAHULU: bila ada
// satu bagian yang tidak valid, tidak ada yang tersimpan (all-or-nothing).
// Hanya topik yang benar-benar berubah yang ditulis dan menghasilkan entri
// audit — mengirim ulang konten yang sama bersifat no-op.
func (s *Service) Replace(ctx context.Context, actor auth.Actor, in []Input) ([]Topic, error) {
	if !actor.Can(auth.CapAdminTutorial) {
		return nil, ErrForbidden
	}
	if len(in) > MaxTopik {
		return nil, &ValidationError{Fields: map[string]string{
			"topik": fmt.Sprintf("maksimal %d topik", MaxTopik),
		}}
	}
	if len(in) == 0 {
		return nil, &ValidationError{Fields: map[string]string{
			"topik": "minimal satu topik tutorial",
		}}
	}

	// Tahap 1: validasi + sanitasi seluruh topik.
	fields := map[string]string{}
	canon := make([]Topic, 0, len(in))
	for i, t := range in {
		topik, msgs := Check(t)
		for k, v := range msgs {
			fields[fmt.Sprintf("topik[%d].%s", i, k)] = v
		}
		canon = append(canon, topik)
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}

	// Tahap 2: bandingkan dengan isi yang berlaku → tambah / ubah / hapus.
	lama, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	adaLama := make(map[string]Topic, len(lama))
	for _, t := range lama {
		adaLama[t.ID] = t
	}

	tambah := make([]Topic, 0, len(canon))
	ubah := make([]Topic, 0, len(canon))
	recs := make([]audit.Entry, 0, len(lama)+len(canon))
	dipakai := make(map[string]bool, len(canon))
	for i := range canon {
		// Urutan tampil selalu ditentukan server dari posisi array (langkah
		// 10, 20, 30, ...) supaya klien tidak bisa mengirim urutan yang
		// bertabrakan.
		canon[i].Urutan = (i + 1) * 10
		if canon[i].ID == "" {
			tambah = append(tambah, canon[i])
			recs = append(recs, audit.NewEntry(ctx, actor, audit.EntityTutorial, "",
				audit.ActionCreate, nil, snapshot(canon[i])))
			continue
		}
		sebelum, ok := adaLama[canon[i].ID]
		if !ok {
			fields[fmt.Sprintf("topik[%d].id", i)] = "topik tidak ditemukan"
			continue
		}
		dipakai[canon[i].ID] = true
		if samaTopik(sebelum, canon[i]) {
			continue
		}
		ubah = append(ubah, canon[i])
		recs = append(recs, audit.NewEntry(ctx, actor, audit.EntityTutorial, canon[i].ID,
			audit.ActionUpdate, snapshot(sebelum), snapshot(canon[i])))
	}
	if len(fields) > 0 {
		// Ada id yang tidak dikenal: TIDAK ada perubahan yang disimpan.
		return nil, &ValidationError{Fields: fields}
	}

	hapus := make([]string, 0, len(lama))
	for _, t := range lama {
		if !dipakai[t.ID] {
			hapus = append(hapus, t.ID)
			recs = append(recs, audit.NewEntry(ctx, actor, audit.EntityTutorial, t.ID,
				audit.ActionDelete, snapshot(t), nil))
		}
	}

	if len(tambah) == 0 && len(ubah) == 0 && len(hapus) == 0 {
		// Tidak ada mutasi: tidak ada baris yang ditulis, tidak ada audit.
		return lama, nil
	}
	if err := s.repo.Apply(ctx, actor.UserID, tambah, ubah, hapus, recs); err != nil {
		return nil, fmt.Errorf("simpan konten tutorial: %w", err)
	}
	return s.repo.List(ctx)
}

// Check memvalidasi sekaligus menormalkan satu topik masukan. Peta kedua berisi
// pesan per nama bagian; peta kosong berarti topik valid dan nilai pertama
// adalah bentuk kanonik yang layak disimpan.
func Check(in Input) (Topic, map[string]string) {
	fields := map[string]string{}
	out := Topic{
		ID:      strings.TrimSpace(in.ID),
		Ikon:    strings.TrimSpace(in.Ikon),
		Judul:   Sanitize(in.Judul),
		Ringkas: Sanitize(in.Ringkas),
	}
	if out.ID != "" && !uuidSah(out.ID) {
		fields["id"] = "id topik tidak valid"
	}
	if !IkonSah(out.Ikon) {
		fields["ikon"] = "ilustrasi harus salah satu: " + strings.Join(IkonBawaan(), ", ")
	}
	if out.Judul == "" {
		fields["judul"] = "judul wajib diisi"
	} else if utf8.RuneCountInString(out.Judul) > MaxJudul {
		fields["judul"] = fmt.Sprintf("judul maksimal %d karakter", MaxJudul)
	}
	if out.Ringkas == "" {
		fields["ringkas"] = "ringkasan wajib diisi"
	} else if utf8.RuneCountInString(out.Ringkas) > MaxRingkas {
		fields["ringkas"] = fmt.Sprintf("ringkasan maksimal %d karakter", MaxRingkas)
	}
	out.Langkah = make([]string, 0, len(in.Langkah))
	for i, l := range in.Langkah {
		teks := Sanitize(l)
		if teks == "" {
			// Butir kosong (mis. baris kosong sisa editor) dilewati, bukan
			// ditolak: yang penting daftar akhirnya tetap terisi.
			continue
		}
		if utf8.RuneCountInString(teks) > MaxLangkahTeks {
			fields[fmt.Sprintf("langkah[%d]", i)] = fmt.Sprintf("langkah maksimal %d karakter", MaxLangkahTeks)
			continue
		}
		out.Langkah = append(out.Langkah, teks)
	}
	switch {
	case len(out.Langkah) == 0:
		fields["langkah"] = "minimal satu langkah"
	case len(out.Langkah) > MaxLangkah:
		fields["langkah"] = fmt.Sprintf("langkah maksimal %d butir", MaxLangkah)
	}
	if len(fields) > 0 {
		return Topic{}, fields
	}
	return out, map[string]string{}
}

// IkonBawaan adalah kunci ilustrasi yang dikenal aplikasi, terurut.
func IkonBawaan() []string {
	return []string{IkonBukti, IkonCNC, IkonMemesan, IkonSertifikat, IkonUmum}
}

// IkonSah melaporkan apakah satu kunci ilustrasi dikenal.
func IkonSah(k string) bool {
	for _, v := range IkonBawaan() {
		if v == k {
			return true
		}
	}
	return false
}

// Sanitize menyiapkan satu potong teks konten untuk disimpan.
//
// Aturan (keputusan kartu F2-27 — konten adalah teks biasa, bukan HTML):
//  1. Baris baru dan tab diperlakukan sebagai spasi sehingga tata letak
//     daftar tidak bisa diubah dari data.
//  2. Komentar HTML dibuang bersama isinya.
//  3. Tag HTML/SVG/XML (`<` yang diikuti huruf, `/`, `!`, atau `?`) dibuang
//     sampai tidak ada lagi — termasuk bentuk bersarang seperti `<<script>>`.
//     Tag yang tidak pernah ditutup membuang sisa teksnya, sama seperti
//     perilaku parser HTML.
//  4. Sisa karakter `<` dan `>` dibuang supaya konten yang tersimpan dijamin
//     tidak pernah memuat rangkaian markup.
//  5. Karakter kendali dibuang dan spasi berturut-turut diciutkan.
//
// Teks biasa yang tidak mengandung markup (termasuk aksen dan huruf non-Latin)
// tidak diubah selain perapian spasi.
func Sanitize(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "	", " ").Replace(s)
	// Isi blok <script>/<style> dibuang bersama penandanya: teks skrip tetap
	// adalah teks berbahaya bila sampai ke klien yang salah merender.
	for _, tag := range []string{"script", "style"} {
		s = buangBlok(s, tag)
	}
	for {
		i := strings.Index(s, "<!--")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "-->")
		if j < 0 {
			s = s[:i]
			break
		}
		s = s[:i] + " " + s[i+j+3:]
	}
	for iter := 0; iter < 8; iter++ {
		out, potong := buangTag(s)
		s = out
		if !potong {
			break
		}
	}
	s = strings.NewReplacer("<", " ", ">", " ").Replace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// buangBlok membuang <tag …>…</tag> beserta isinya (tanpa membedakan huruf
// besar/kecil). Bila penutupnya tidak ada, sisa teks ikut dibuang.
func buangBlok(s, tag string) string {
	buka := "<" + tag
	tutup := "</" + tag
	pos := 0
	for {
		i := indexFold(s[pos:], buka)
		if i < 0 {
			return s
		}
		awal := pos + i
		akhirNama := awal + len(buka)
		if akhirNama < len(s) && !isPemisah(s[akhirNama]) {
			// "<scriptx" bukan tag <script>: cari kemunculan berikutnya.
			pos = akhirNama
			continue
		}
		j := indexFold(s[awal:], tutup)
		if j < 0 {
			// Blok tidak pernah ditutup: sisa teks dibuang, sama seperti
			// perilaku parser HTML.
			return s[:awal]
		}
		akhir := awal + j + len(tutup)
		if k := strings.IndexByte(s[akhir:], '>'); k >= 0 {
			akhir += k + 1
		}
		s = s[:awal] + " " + s[akhir:]
		pos = awal
	}
}

// indexFold mencari sub di dalam s tanpa membedakan huruf besar/kecil ASCII.
// Pencarian bekerja per byte sehingga indeks yang dikembalikan selalu indeks
// byte pada s (tidak bergeser oleh huruf non-ASCII seperti strings.ToLower).
func indexFold(s, sub string) int {
	n, m := len(s), len(sub)
	if m == 0 || m > n {
		return -1
	}
	for i := 0; i+m <= n; i++ {
		cocok := true
		for j := 0; j < m; j++ {
			if lowerASCII(s[i+j]) != lowerASCII(sub[j]) {
				cocok = false
				break
			}
		}
		if cocok {
			return i
		}
	}
	return -1
}

// lowerASCII menurunkan huruf besar ASCII menjadi huruf kecil.
func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// isPemisah melaporkan apakah satu byte mengakhiri nama tag (spasi, '>' atau
// '/').
func isPemisah(c byte) bool {
	return c == ' ' || c == '>' || c == '/' || c == '	' || c == '\n' || c == '\r'
}

// buangTag membuang satu penanda `<…>` dan melaporkan apakah ada yang dibuang.
func buangTag(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		if i+1 >= len(s) {
			return s, false
		}
		c := s[i+1]
		if c != '/' && c != '!' && c != '?' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') {
			// Bukan awal tag (mis. "biaya < 5000"): dibiarkan sebagai teks.
			continue
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			// Tag tidak pernah ditutup: sisa teks ikut dibuang.
			return s[:i], true
		}
		s = s[:i] + " " + s[i+j+1:]
		i--
	}
	return s, false
}

// samaTopik membandingkan dua topik dalam bentuk kanonik (abaikan waktu ubah).
func samaTopik(a, b Topic) bool {
	if a.Ikon != b.Ikon || a.Judul != b.Judul || a.Ringkas != b.Ringkas || a.Urutan != b.Urutan {
		return false
	}
	if len(a.Langkah) != len(b.Langkah) {
		return false
	}
	for i := range a.Langkah {
		if a.Langkah[i] != b.Langkah[i] {
			return false
		}
	}
	return true
}

// snapshot membungkus topik menjadi objek audit (tanpa waktu ubah/id internal
// lain yang tidak bermakna untuk pembaca log).
func snapshot(t Topic) any {
	return map[string]any{
		"ikon":    t.Ikon,
		"judul":   t.Judul,
		"ringkas": t.Ringkas,
		"langkah": t.Langkah,
		"urutan":  t.Urutan,
	}
}

// uuidSah memeriksa bentuk uuid tanpa menyentuh driver database, supaya id
// yang salah ketik dijawab 400 di layer ini (bukan 500 dari PostgreSQL).
func uuidSah(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			c := s[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// LangkahJSON menyusun bentuk jsonb kolom `langkah` dari daftar teks.
func LangkahJSON(langkah []string) ([]byte, error) {
	if langkah == nil {
		langkah = []string{}
	}
	b, err := json.Marshal(langkah)
	if err != nil {
		return nil, fmt.Errorf("menyusun langkah: %w", err)
	}
	if len(b) > langkahMaksBait {
		return nil, fmt.Errorf("%w: langkah terlalu besar", ErrInvalidContent)
	}
	return b, nil
}

// LangkahDariJSON membaca kembali kolom jsonb `langkah`. Nilai yang tidak
// berbentuk array teks dijawab error domain, bukan panic: database adalah
// sumber data yang bisa saja diubah manual.
func LangkahDariJSON(raw []byte) ([]string, error) {
	out := []string{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: kolom langkah bukan array teks", ErrInvalidContent)
	}
	return out, nil
}
