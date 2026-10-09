package payment

// ClaudeReader membaca bukti transfer memakai Claude API (vision) —
// Architecture.md §1 dan §11: provider OCR/AI di balik interface
// `PaymentReader` supaya bisa ditukar tanpa mengubah domain verifikasi.
//
// Batas yang dijaga di sini:
//
//   - Kredensial HANYA hidup di dalam pembaca ini dan TIDAK pernah masuk ke
//     hasil bacaan, jawaban mentah, maupun pesan galat. Galat provider
//     dikembalikan sebagai status + pesan `error.message` dari jawaban, bukan
//     badan jawaban apa adanya, supaya tidak ada kunci/permintaan yang bocor.
//   - Pembaca TIDAK memutuskan apa pun: bila model tidak yakin, ia
//     mengembalikan keyakinan rendah/nominal kosong dan keputusannya diserahkan
//     ke aturan domain (PRD §5).
//   - Kegagalan apa pun (kunci kosong, jaringan, status bukan 2xx, jawaban tak
//     terbaca) dijawab sebagai galat supaya pekerjaan River diulang — BUKAN
//     sebagai bacaan kosong yang "berhasil".

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Konfigurasi bawaan Claude reader.
const (
	// DefaultClaudeBaseURL adalah endpoint resmi Anthropic.
	DefaultClaudeBaseURL = "https://api.anthropic.com"
	// DefaultClaudeModel adalah model vision yang dipakai bila tidak diatur.
	// Selalu dapat ditimpa lewat konfigurasi (ANTHROPIC_MODEL) sehingga
	// pergantian model tidak menuntut perubahan kode.
	DefaultClaudeModel = "claude-sonnet-4-5"
	// ClaudeVersion adalah versi API yang dipakai (header anthropic-version).
	ClaudeVersion = "2023-06-01"
	// claudeMaxTokens membatasi panjang jawaban: yang diminta hanya satu objek
	// JSON pendek.
	claudeMaxTokens = 1024
	// batasJawabanBytes membatasi pembacaan badan jawaban provider.
	batasJawabanBytes = 256 << 10
)

// promptBacabukti meminta model mengembalikan SATU objek JSON tanpa penjelasan.
// Nilai yang tidak terlihat harus dikosongkan (null) dan keyakinan diturunkan —
// pembaca tidak boleh mengarang angka (PRD §5: bukti buram jatuh ke review).
const promptBacaBukti = `Anda membaca bukti transfer bank Indonesia (screenshot m-banking atau PDF). ` +
	`Balas HANYA dengan satu objek JSON tanpa penjelasan, dengan kunci:\n` +
	`{"nominal": <angka rupiah tanpa pemisah atau null>, "tanggal": "<YYYY-MM-DD atau null>", ` +
	`"bank": "<BCA|BRI|nama bank lain atau null>", "rek_akhir": "<nomor rekening tujuan atau null>", ` +
	`"keyakinan": <0..1>}\n` +
	`Gunakan null untuk nilai yang tidak terbaca dan turunkan "keyakinan" bila gambar buram, ` +
	`terpotong, atau nominalnya ambigu. Jangan menebak angka.`

// ClaudeConfig menyusun pembaca Claude.
type ClaudeConfig struct {
	// APIKey kosong berarti provider belum tersedia: Read menjawab
	// ErrReaderUnavailable (fail closed, bukan bacaan kosong).
	APIKey string
	// BaseURL adalah akar API (bawaan DefaultClaudeBaseURL).
	BaseURL string
	// Model adalah nama model vision (bawaan DefaultClaudeModel).
	Model string
	// Timeout membatasi satu panggilan (bawaan 60 detik).
	Timeout time.Duration
	// HTTP dipakai pengujian (mis. httptest.Server); nil = klien bawaan.
	HTTP *http.Client
}

// NewClaudeReader membuat pembaca bukti berbasis Claude vision.
func NewClaudeReader(cfg ClaudeConfig) *ClaudeReader {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = DefaultClaudeBaseURL
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultClaudeModel
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	client := cfg.HTTP
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &ClaudeReader{
		apiKey:  strings.TrimSpace(cfg.APIKey),
		baseURL: base,
		model:   model,
		http:    client,
	}
}

// ClaudeReader memenuhi PaymentReader.
var _ PaymentReader = (*ClaudeReader)(nil)

// ClaudeReader membaca bukti memakai Claude vision (Messages API).
type ClaudeReader struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

// Tersedia melaporkan apakah kunci API sudah diisi.
func (c *ClaudeReader) Tersedia() bool { return c.apiKey != "" }

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []claudeMessage `json:"messages"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type claudeImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type claudeTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeResponse struct {
	Content []claudeTextBlock `json:"content"`
	Error   *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// bacaanModel adalah bentuk JSON yang diminta dari model.
type bacaanModel struct {
	Nominal   *float64 `json:"nominal"`
	Tanggal   *string  `json:"tanggal"`
	Bank      *string  `json:"bank"`
	RekAkhir  *string  `json:"rek_akhir"`
	Keyakinan *float64 `json:"keyakinan"`
}

// Read mengirim satu bukti ke Claude dan mengembalikan bacaan terstruktur.
func (c *ClaudeReader) Read(ctx context.Context, in ReadInput) (ReadResult, error) {
	if c.apiKey == "" {
		return ReadResult{}, fmt.Errorf("%w: kunci API belum dikonfigurasi", ErrReaderUnavailable)
	}
	blok, err := blokIsi(in)
	if err != nil {
		return ReadResult{}, err
	}
	payload := claudeRequest{
		Model:     c.model,
		MaxTokens: claudeMaxTokens,
		Messages: []claudeMessage{{
			Role:    "user",
			Content: []any{blok, claudeTextBlock{Type: "text", Text: promptBacaBukti}},
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ReadResult{}, fmt.Errorf("%w: susun permintaan", ErrReaderGagal)
	}
	endpoint := c.baseURL + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ReadResult{}, fmt.Errorf("%w: susun permintaan", ErrReaderGagal)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", ClaudeVersion)
	req.Header.Set("x-api-key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		// Galat jaringan: TIDAK memuat kunci (http.Client tidak menyalin
		// header ke pesan galat).
		return ReadResult{}, fmt.Errorf("%w: %v", ErrReaderUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	isi, err := io.ReadAll(io.LimitReader(resp.Body, batasJawabanBytes))
	if err != nil {
		return ReadResult{}, fmt.Errorf("%w: baca jawaban provider", ErrReaderGagal)
	}
	var jawab claudeResponse
	_ = json.Unmarshal(isi, &jawab)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if jawab.Error != nil && jawab.Error.Message != "" {
			// Pesan galat provider aman: ia menjelaskan permintaan yang
			// ditolak, bukan menyalin kredensial.
			return ReadResult{}, fmt.Errorf("%w: provider menjawab %d: %s", ErrReaderGagal, resp.StatusCode, ringkas(jawab.Error.Message))
		}
		return ReadResult{}, fmt.Errorf("%w: provider menjawab %d", ErrReaderGagal, resp.StatusCode)
	}
	teks := ""
	for _, b := range jawab.Content {
		if b.Type == "text" && b.Text != "" {
			teks = b.Text
			break
		}
	}
	if teks == "" {
		return ReadResult{}, fmt.Errorf("%w: jawaban provider tanpa teks", ErrReaderGagal)
	}
	mentah := ambilJSON(teks)
	if mentah == nil {
		return ReadResult{}, fmt.Errorf("%w: jawaban provider bukan JSON", ErrReaderGagal)
	}
	var bm bacaanModel
	if err := json.Unmarshal(mentah, &bm); err != nil {
		return ReadResult{}, fmt.Errorf("%w: jawaban provider tidak dapat dibaca", ErrReaderGagal)
	}
	return bm.keBacaan(mentah), nil
}

// blokIsi menyusun blok berkas untuk Messages API: gambar untuk JPG/PNG,
// dokumen untuk PDF. Jenis lain ditolak (server sudah membatasi unggahan,
// jadi ini hanya jaring pengaman).
func blokIsi(in ReadInput) (any, error) {
	if len(in.Content) == 0 {
		return nil, fmt.Errorf("%w: berkas kosong", ErrReaderJenis)
	}
	jenis := strings.ToLower(strings.TrimSpace(in.MimeType))
	var tipe string
	switch jenis {
	case MimeJPEG, MimePNG, "image/webp":
		tipe = "image"
	case MimePDF:
		tipe = "document"
	case "":
		// Jenis tidak diketahui: serahkan ke gambar (Claude mengenali isi
		// dari media_type pada blok source, jadi tetap harus diisi).
		jenis = MimePNG
		tipe = "image"
	default:
		return nil, fmt.Errorf("%w: %s", ErrReaderJenis, jenis)
	}
	return map[string]any{
		"type": tipe,
		"source": claudeImageSource{
			Type:      "base64",
			MediaType: jenis,
			Data:      base64.StdEncoding.EncodeToString(in.Content),
		},
	}, nil
}

// keBacaan mengubah jawaban model menjadi bacaan domain; nilai yang tidak
// masuk akal dibiarkan kosong.
func (b bacaanModel) keBacaan(mentah json.RawMessage) ReadResult {
	out := ReadResult{Raw: append(json.RawMessage(nil), mentah...)}
	if b.Nominal != nil {
		n := int64(math.Round(*b.Nominal))
		if n >= 0 {
			out.Nominal = &n
		}
	}
	if b.Tanggal != nil {
		if t, ok := parseTanggalBacaan(*b.Tanggal); ok {
			out.Tanggal = &t
		}
	}
	if b.Bank != nil {
		out.Bank = strings.TrimSpace(*b.Bank)
	}
	if b.RekAkhir != nil {
		out.RekAkhir = strings.TrimSpace(*b.RekAkhir)
	}
	if b.Keyakinan != nil {
		out.Confidence = batasiKeyakinan(*b.Keyakinan)
	}
	return out
}

// parseTanggalBacaan menerima YYYY-MM-DD atau RFC3339.
func parseTanggalBacaan(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// batasiKeyakinan menjepit keyakinan ke 0..1 dan membulatkannya ke 3 desimal
// (kolom ocr_confidence numeric(4,3)).
func batasiKeyakinan(f float64) float64 {
	if math.IsNaN(f) || f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return math.Round(f*1000) / 1000
}

// ambilJSON mengambil objek JSON pertama dari teks jawaban (model sering
// membungkusnya dengan pagar kode). Mengembalikan nil bila tidak ada.
func ambilJSON(teks string) json.RawMessage {
	awal := strings.Index(teks, "{")
	akhir := strings.LastIndex(teks, "}")
	if awal < 0 || akhir <= awal {
		return nil
	}
	kandidat := []byte(strings.TrimSpace(teks[awal : akhir+1]))
	if !json.Valid(kandidat) {
		return nil
	}
	return json.RawMessage(kandidat)
}

// ringkas memotong pesan pihak ketiga supaya entri log tetap pendek.
func ringkas(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
