// Package settings mengelola pengaturan sistem (PRD §4.9).
//
// Nilai disimpan sebagai jsonb sehingga bisa ditambah tanpa migrasi, tetapi
// setiap kunci yang dikenal punya SPESIFIKASI (tipe + rentang) yang ditegakkan
// di layer ini — bukan hanya dipercaya dari klien. Nilai uang Rupiah harus
// bilangan bulat (Database.md §4.7, BR-10: tanpa sen).
//
// Pengelolaan pengaturan (baca penuh + ubah) hanya untuk Superadmin
// (PRD §2.1). Perubahan dicatat ke audit_logs DI DALAM transaksi yang sama
// dengan penyimpanannya (SystemDesign.md §5.4).
//
// Catatan domain: mengubah `biaya_packaging` TIDAK mengubah pesanan lama.
// Harga/biaya di-snapshot ke order line saat submit (BR-05); paket ini tidak
// pernah menyentuh orders/order_lines.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// Sentinel error domain. Pemetaan ke kode HTTP ada di lapisan transport.
var (
	// ErrNotFound dikembalikan bila kunci pengaturan tidak dikenal atau kosong.
	ErrNotFound = errors.New("pengaturan tidak ditemukan")
	// ErrForbidden dikembalikan bila aktor tidak berhak mengubah/membaca pengaturan.
	ErrForbidden = errors.New("tidak berhak mengubah pengaturan")
	// ErrInvalidValue menandai nilai pengaturan yang tidak lolos validasi
	// (tipe atau rentang). Detail per kunci ada pada *ValidationError.
	ErrInvalidValue = errors.New("nilai pengaturan tidak valid")
)

// Kunci pengaturan yang dikenal (PRD §4.9). Konstanta dipakai lintas paket
// supaya tidak ada penulisan kunci yang salah ketik.
const (
	BiayaPackaging     = "biaya_packaging"
	CNCHari            = "cnc_hari"
	TokenHari          = "token_hari"
	ResetMenit         = "reset_menit"
	MaxMB              = "max_mb"
	MaxFile            = "max_file"
	DigitInvoice       = "digit_invoice"
	ManagerBolehRevisi = "manager_boleh_revisi"
	AI                 = "ai"
)

// aiDefault adalah nilai awal setelan AI sesuai PRD §4.9.
const aiDefault = `{"aktif":false,"min_keyakinan":0.9,"tol_kurang":0,"tol_lebih":0,"digit_rek":4,"mundur":1}`

// Value adalah satu baris pengaturan.
type Value struct {
	Key       string
	Value     []byte
	UpdatedAt time.Time
}

// Change adalah satu nilai pengaturan yang akan disimpan.
type Change struct {
	Key   string
	Value json.RawMessage
}

// ValidationError membawa pesan per kunci pengaturan yang gagal divalidasi.
type ValidationError struct {
	Fields map[string]string
}

// Error memenuhi interface error.
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
	return "nilai pengaturan tidak valid: " + strings.Join(pesan, "; ")
}

// Is membuat errors.Is(err, ErrInvalidValue) benar untuk setiap error ini.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalidValue }

// Unwrap menjaga rantai error tetap dapat diperiksa.
func (e *ValidationError) Unwrap() error { return ErrInvalidValue }

// Repository memuat dan menyimpan pengaturan.
type Repository interface {
	List(ctx context.Context) ([]Value, error)
	Get(ctx context.Context, key string) ([]byte, error)
	// Save menyimpan seluruh perubahan dalam SATU transaksi bersama catatan
	// auditnya; bila salah satu gagal, tidak ada yang tersimpan.
	Save(ctx context.Context, actorID string, changes []Change, recs []audit.Entry) error
}

// Service adalah logika bisnis pengaturan.
type Service struct {
	repo Repository
}

// NewService membuat service pengaturan.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// All mengembalikan seluruh pengaturan (default digabung nilai tersimpan)
// termasuk `ai`. Hanya Superadmin (PRD §2.1).
func (s *Service) All(ctx context.Context, actor auth.Actor) (map[string]json.RawMessage, error) {
	if !actor.Can(auth.CapAdminSettings) {
		return nil, ErrForbidden
	}
	return s.merged(ctx, nil)
}

// Runtime mengembalikan konstanta operasional yang perlu diketahui kedua
// portal. Kunci `ai` TIDAK termasuk: ia internal verifikasi bukti transfer
// (fase 2) dan hanya dibaca Superadmin lewat All. Endpoint ini tidak menuntut
// kapabilitas admin karena nilainya memang dipakai UI (mis. biaya packaging
// pada pesanan sertifikat, PRD CP-SER-02).
func (s *Service) Runtime(ctx context.Context) (map[string]json.RawMessage, error) {
	return s.merged(ctx, func(key string) bool { return key != AI })
}

// Get mengembalikan satu nilai pengaturan tersimpan. Kunci yang tidak dikenal
// atau belum pernah disimpan menghasilkan ErrNotFound. Dipakai lintas paket
// (mis. masa berlaku token/undangan) sehingga tidak menuntut kapabilitas.
func (s *Service) Get(ctx context.Context, key string) (json.RawMessage, error) {
	if _, ok := specs[key]; !ok {
		return nil, ErrNotFound
	}
	raw, err := s.repo.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// Default mengembalikan nilai bawaan satu kunci, atau ErrNotFound.
func Default(key string) (json.RawMessage, error) {
	sp, ok := specs[key]
	if !ok {
		return nil, ErrNotFound
	}
	return json.RawMessage(sp.defaultRaw), nil
}

// Update menyimpan satu atau beberapa pengaturan sekaligus (Superadmin).
//
// Seluruh nilai divalidasi LEBIH DAHULU: bila ada satu yang tidak valid, tidak
// ada yang tersimpan (all-or-nothing). Setiap kunci yang berubah menghasilkan
// satu entri audit di dalam transaksi penyimpanan.
func (s *Service) Update(ctx context.Context, actor auth.Actor, values map[string]json.RawMessage) error {
	if !actor.Can(auth.CapAdminSettings) {
		return ErrForbidden
	}
	if len(values) == 0 {
		return &ValidationError{Fields: map[string]string{"values": "tidak ada nilai yang dikirim"}}
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Tahap 1: validasi & normalisasi seluruh nilai.
	fields := map[string]string{}
	norm := make(map[string]json.RawMessage, len(keys))
	for _, k := range keys {
		nilai, msg := Check(k, values[k])
		if msg != "" {
			fields[k] = msg
			continue
		}
		norm[k] = nilai
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}

	// Tahap 2: susun perubahan + entri audit (before dari nilai berlaku).
	changes := make([]Change, 0, len(keys))
	recs := make([]audit.Entry, 0, len(keys))
	for _, k := range keys {
		before, err := s.effective(ctx, k)
		if err != nil {
			return err
		}
		after := norm[k]
		changes = append(changes, Change{Key: k, Value: after})
		recs = append(recs, audit.NewEntry(ctx, actor, audit.EntitySetting, "",
			audit.ActionUpdate, snapshot(k, before), snapshot(k, after)))
	}
	if err := s.repo.Save(ctx, actor.UserID, changes, recs); err != nil {
		return fmt.Errorf("simpan pengaturan: %w", err)
	}
	return nil
}

// effective mengembalikan nilai yang sedang berlaku untuk satu kunci:
// nilai tersimpan bila ada, selain itu nilai bawaan.
func (s *Service) effective(ctx context.Context, key string) (json.RawMessage, error) {
	raw, err := s.repo.Get(ctx, key)
	if err == nil {
		return json.RawMessage(raw), nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("baca pengaturan %s: %w", key, err)
	}
	return Default(key)
}

// merged menggabungkan nilai bawaan dengan nilai tersimpan; allow membatasi
// kunci yang dikembalikan (nil = semua kunci dikenal).
func (s *Service) merged(ctx context.Context, allow func(string) bool) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(specs))
	for k, sp := range specs {
		if allow == nil || allow(k) {
			out[k] = json.RawMessage(sp.defaultRaw)
		}
	}
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("baca pengaturan: %w", err)
	}
	for _, r := range rows {
		if _, ok := specs[r.Key]; !ok {
			continue // kunci tak dikenal di database diabaikan
		}
		if allow != nil && !allow(r.Key) {
			continue
		}
		out[r.Key] = json.RawMessage(r.Value)
	}
	return out, nil
}

// snapshot membungkus nilai menjadi objek audit {key, value}.
func snapshot(key string, raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return map[string]any{"key": key, "value": json.RawMessage(raw)}
}

// keyKind adalah jenis nilai yang diperbolehkan per kunci.
type keyKind int

const (
	kindInt keyKind = iota
	kindBool
	kindAI
)

// keySpec adalah spesifikasi satu kunci: jenis, rentang (khusus bilangan
// bulat), dan nilai bawaan.
type keySpec struct {
	kind       keyKind
	min, max   int64
	defaultRaw string
	// isFloat menandai nilai pecahan (mis. keyakinan AI 0..1) yang rentangnya
	// tetap diperiksa tetapi tidak boleh dibulatkan.
	isFloat bool
}

// specs memetakan setiap kunci yang dikenal ke spesifikasinya. Rentang dipilih
// cukup longgar untuk pemakaian nyata tetapi tetap menolak nilai yang jelas
// salah (negatif, nol, atau tidak masuk akal) — "validasi rentang nilai".
var specs = map[string]keySpec{
	BiayaPackaging:     {kind: kindInt, min: 0, max: 100_000_000, defaultRaw: "3000"},
	CNCHari:            {kind: kindInt, min: 1, max: 3650, defaultRaw: "7"},
	TokenHari:          {kind: kindInt, min: 1, max: 3650, defaultRaw: "14"},
	ResetMenit:         {kind: kindInt, min: 1, max: 43200, defaultRaw: "60"},
	MaxMB:              {kind: kindInt, min: 1, max: 1024, defaultRaw: "5"},
	MaxFile:            {kind: kindInt, min: 1, max: 100, defaultRaw: "5"},
	DigitInvoice:       {kind: kindInt, min: 1, max: 10, defaultRaw: "2"},
	ManagerBolehRevisi: {kind: kindBool, defaultRaw: "true"},
	AI:                 {kind: kindAI, defaultRaw: aiDefault},
}

// aiSpecs adalah spesifikasi field di dalam objek `ai` (PRD §4.9).
var aiSpecs = map[string]keySpec{
	"aktif":         {kind: kindBool, defaultRaw: "false"},
	"min_keyakinan": {kind: kindInt, min: 0, max: 1, defaultRaw: "0.9", isFloat: true},
	"tol_kurang":    {kind: kindInt, min: 0, max: 1_000_000_000_000, defaultRaw: "0"},
	"tol_lebih":     {kind: kindInt, min: 0, max: 1_000_000_000_000, defaultRaw: "0"},
	"digit_rek":     {kind: kindInt, min: 1, max: 20, defaultRaw: "4"},
	"mundur":        {kind: kindInt, min: 0, max: 3650, defaultRaw: "1"},
}

// KnownKeys adalah kunci pengaturan yang dikenal, terurut.
func KnownKeys() []string {
	out := make([]string, 0, len(specs))
	for k := range specs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Check memvalidasi sekaligus menormalkan satu nilai pengaturan.
// Pesan kesalahan kosong berarti nilai valid; nilai yang dikembalikan adalah
// bentuk kanonik yang layak disimpan (bilangan bulat tanpa pecahan, objek ai
// yang lengkap).
func Check(key string, raw json.RawMessage) (json.RawMessage, string) {
	sp, ok := specs[key]
	if !ok {
		return nil, "bukan kunci pengaturan yang dikenal"
	}
	if len(raw) == 0 {
		return nil, "nilai wajib diisi"
	}
	switch sp.kind {
	case kindBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, "harus bernilai true atau false"
		}
		return json.RawMessage(strconv.FormatBool(b)), ""
	case kindInt:
		n, err := number(raw)
		if err != nil {
			return nil, "harus berupa angka"
		}
		if sp.isFloat {
			f, err := n.Float64()
			if err != nil {
				return nil, "harus berupa angka"
			}
			if f < float64(sp.min) || f > float64(sp.max) {
				return nil, fmt.Sprintf("harus di antara %d dan %d", sp.min, sp.max)
			}
			return json.RawMessage(formatFloat(f)), ""
		}
		v, msg := integerInRange(n, sp.min, sp.max)
		if msg != "" {
			return nil, msg
		}
		return json.RawMessage(strconv.FormatInt(v, 10)), ""
	case kindAI:
		return checkAI(raw)
	default:
		return nil, "jenis pengaturan tidak dikenal"
	}
}

// checkAI memvalidasi objek `ai`, menolak field tak dikenal, dan mengembalikan
// objek lengkap (field yang tidak dikirim memakai nilai bawaan).
func checkAI(raw json.RawMessage) (json.RawMessage, string) {
	var in map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(&in); err != nil {
		return nil, "harus berupa objek"
	}
	out := map[string]any{}
	for k, sp := range aiSpecs {
		var def any
		if err := json.Unmarshal([]byte(sp.defaultRaw), &def); err != nil {
			return nil, "nilai bawaan ai tidak terbaca"
		}
		out[k] = def
	}
	for k, v := range in {
		sp, ok := aiSpecs[k]
		if !ok {
			return nil, fmt.Sprintf("field ai tidak dikenal: %s", k)
		}
		switch sp.kind {
		case kindBool:
			var b bool
			if err := json.Unmarshal(v, &b); err != nil {
				return nil, fmt.Sprintf("ai.%s harus true atau false", k)
			}
			out[k] = b
		default:
			n, err := number(v)
			if err != nil {
				return nil, fmt.Sprintf("ai.%s harus berupa angka", k)
			}
			if sp.isFloat {
				f, err := n.Float64()
				if err != nil {
					return nil, fmt.Sprintf("ai.%s harus berupa angka", k)
				}
				if f < float64(sp.min) || f > float64(sp.max) {
					return nil, fmt.Sprintf("ai.%s harus di antara %d dan %d", k, sp.min, sp.max)
				}
				out[k] = f
				continue
			}
			val, msg := integerInRange(n, sp.min, sp.max)
			if msg != "" {
				return nil, fmt.Sprintf("ai.%s %s", k, msg)
			}
			out[k] = val
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, "objek ai tidak bisa disimpan"
	}
	return b, ""
}

// number mengurai nilai JSON menjadi json.Number (menolak string).
func number(raw json.RawMessage) (json.Number, error) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return "", err
	}
	return n, nil
}

// integerInRange memastikan bilangan bulat dan berada dalam rentang.
func integerInRange(n json.Number, min, max int64) (int64, string) {
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		return 0, "harus bilangan bulat (tanpa pecahan)"
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, "harus berupa angka"
	}
	if v < min || v > max {
		return 0, fmt.Sprintf("harus di antara %d dan %d", min, max)
	}
	return v, ""
}

// formatFloat menulis bilangan pecahan tanpa nol berekor (0.9, bukan 0.90).
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
