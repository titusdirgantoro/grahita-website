package payment

// GeminiReader membaca bukti transfer dengan Gemini 2.5 Flash-Lite. API key
// diambil dari sumber terenkripsi setiap pekerjaan, bukan dari browser/job.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	GeminiModel          = "gemini-2.5-flash-lite"
	DefaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	geminiResponseLimit  = 256 << 10
	geminiInputLimit     = 19 << 20
)

type GeminiKeySource interface {
	KeyForRead(context.Context) (string, error)
}

type GeminiConfig struct {
	Keys    GeminiKeySource
	BaseURL string
	Timeout time.Duration
	HTTP    *http.Client
}

type GeminiReader struct {
	keys    GeminiKeySource
	baseURL string
	http    *http.Client
}

var _ PaymentReader = (*GeminiReader)(nil)

func NewGeminiReader(cfg GeminiConfig) *GeminiReader {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = DefaultGeminiBaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	client := cfg.HTTP
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &GeminiReader{keys: cfg.Keys, baseURL: base, http: client}
}

type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inline_data,omitempty"`
}
type geminiInlineData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}
type geminiRequest struct {
	Contents []struct {
		Parts []geminiPart `json:"parts"`
	} `json:"contents"`
	GenerationConfig *struct {
		ResponseMimeType string `json:"responseMimeType"`
	} `json:"generationConfig,omitempty"`
}
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func (r *GeminiReader) Read(ctx context.Context, in ReadInput) (ReadResult, error) {
	if r.keys == nil {
		return ReadResult{}, ErrReaderUnavailable
	}
	key, err := r.keys.KeyForRead(ctx)
	if err != nil || key == "" {
		return ReadResult{}, fmt.Errorf("%w: kunci Gemini belum siap", ErrReaderUnavailable)
	}
	if len(in.Content) == 0 || len(in.Content) > geminiInputLimit {
		return ReadResult{}, fmt.Errorf("%w: berkas kosong atau terlalu besar", ErrReaderJenis)
	}
	mime := strings.ToLower(strings.TrimSpace(in.MimeType))
	switch mime {
	case MimeJPEG, MimePNG, MimePDF, "image/webp":
	default:
		return ReadResult{}, ErrReaderJenis
	}
	request := geminiRequest{}
	request.Contents = append(request.Contents, struct {
		Parts []geminiPart `json:"parts"`
	}{Parts: []geminiPart{
		{InlineData: &geminiInlineData{MimeType: mime, Data: base64.StdEncoding.EncodeToString(in.Content)}},
		{Text: promptBacaBukti},
	}})
	request.GenerationConfig = &struct {
		ResponseMimeType string `json:"responseMimeType"`
	}{ResponseMimeType: "application/json"}
	teks, err := r.generate(ctx, key, request)
	if err != nil {
		return ReadResult{}, err
	}
	mentah := ambilJSON(teks)
	if mentah == nil {
		return ReadResult{}, fmt.Errorf("%w: jawaban Gemini bukan JSON", ErrReaderGagal)
	}
	var bacaan bacaanModel
	if err := json.Unmarshal(mentah, &bacaan); err != nil {
		return ReadResult{}, fmt.Errorf("%w: jawaban Gemini tidak dapat dibaca", ErrReaderGagal)
	}
	return bacaan.keBacaan(mentah), nil
}

// TestKey menguji jalur gambar + JSON pada model sungguhan dengan gambar
// sintetis kecil. Ini menguji akses multimodal, bukan akurasi OCR bank nyata.
func (r *GeminiReader) TestKey(ctx context.Context, key string) error {
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			shade := uint8(245)
			if y >= 110 && y < 146 && x >= 32 && x < 224 {
				shade = 25
			}
			img.SetRGBA(x, y, color.RGBA{R: shade, G: shade, B: shade, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return fmt.Errorf("%w: gambar uji gagal dibuat", ErrReaderGagal)
	}
	request := geminiRequest{}
	request.Contents = append(request.Contents, struct {
		Parts []geminiPart `json:"parts"`
	}{
		Parts: []geminiPart{
			{InlineData: &geminiInlineData{MimeType: MimePNG, Data: base64.StdEncoding.EncodeToString(encoded.Bytes())}},
			{Text: `Balas hanya objek JSON {"ok":true}.`},
		},
	})
	request.GenerationConfig = &struct {
		ResponseMimeType string `json:"responseMimeType"`
	}{ResponseMimeType: "application/json"}
	answer, err := r.generate(ctx, key, request)
	if err != nil {
		return err
	}
	if !json.Valid(ambilJSON(answer)) {
		return fmt.Errorf("%w: model tidak mengembalikan JSON untuk gambar uji", ErrReaderGagal)
	}
	return nil
}

func (r *GeminiReader) generate(ctx context.Context, key string, payload geminiRequest) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: susun permintaan Gemini", ErrReaderGagal)
	}
	url := r.baseURL + "/models/" + GeminiModel + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("%w: buat permintaan Gemini", ErrReaderGagal)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	resp, err := r.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: jaringan Gemini tidak tersedia", ErrReaderUnavailable)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err = io.ReadAll(io.LimitReader(resp.Body, geminiResponseLimit+1))
	if err != nil || len(data) > geminiResponseLimit {
		return "", fmt.Errorf("%w: jawaban Gemini tidak dapat dibaca", ErrReaderGagal)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("%w: Gemini menjawab HTTP %d", ErrReaderGagal, resp.StatusCode)
	}
	var out geminiResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("%w: jawaban Gemini tidak valid", ErrReaderGagal)
	}
	for _, candidate := range out.Candidates {
		for _, part := range candidate.Content.Parts {
			if strings.TrimSpace(part.Text) != "" {
				return part.Text, nil
			}
		}
	}
	return "", fmt.Errorf("%w: Gemini tidak mengembalikan teks", ErrReaderGagal)
}
