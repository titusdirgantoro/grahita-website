// Package pdf adalah abstraksi pembuat PDF berbasis Gotenberg
// (HTML/CSS -> PDF lewat Chromium headless) — Architecture.md §1.
package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// ErrNotConfigured dikembalikan bila GOTENBERG_URL belum diisi.
var ErrNotConfigured = errors.New("gotenberg belum dikonfigurasi")

// Renderer mengubah dokumen HTML menjadi PDF A4.
type Renderer interface {
	HTMLToPDF(ctx context.Context, html []byte) ([]byte, error)
	Health(ctx context.Context) error
}

// Gotenberg adalah klien HTTP untuk service Gotenberg.
type Gotenberg struct {
	baseURL string
	client  *http.Client
}

// NewGotenberg membuat klien Gotenberg.
func NewGotenberg(baseURL string) *Gotenberg {
	return &Gotenberg{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// HTMLToPDF mengirim index.html ke endpoint Chromium Gotenberg.
func (g *Gotenberg) HTMLToPDF(ctx context.Context, html []byte) ([]byte, error) {
	if g.baseURL == "" {
		return nil, ErrNotConfigured
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("files", "index.html")
	if err != nil {
		return nil, fmt.Errorf("siapkan multipart: %w", err)
	}
	if _, err := part.Write(html); err != nil {
		return nil, fmt.Errorf("tulis html: %w", err)
	}
	// Opsi A4 agar hasil konsisten dengan template invoice.
	_ = mw.WriteField("paperWidth", "8.27")
	_ = mw.WriteField("paperHeight", "11.7")
	_ = mw.WriteField("marginTop", "0.4")
	_ = mw.WriteField("marginBottom", "0.4")
	_ = mw.WriteField("marginLeft", "0.4")
	_ = mw.WriteField("marginRight", "0.4")
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("tutup multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/forms/chromium/convert/html", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hubungi gotenberg: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("gotenberg menolak (%d): %s", resp.StatusCode, string(msg))
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("baca pdf: %w", err)
	}
	return out, nil
}

// Health memeriksa kesiapan Gotenberg.
func (g *Gotenberg) Health(ctx context.Context) error {
	if g.baseURL == "" {
		return ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gotenberg tidak sehat: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Disabled dipakai bila tidak ada Gotenberg di lingkungan tersebut.
type Disabled struct{}

// HTMLToPDF selalu gagal karena Gotenberg tidak dikonfigurasi.
func (Disabled) HTMLToPDF(context.Context, []byte) ([]byte, error) { return nil, ErrNotConfigured }

// Health melaporkan bahwa Gotenberg memang belum aktif.
func (Disabled) Health(context.Context) error { return ErrNotConfigured }
