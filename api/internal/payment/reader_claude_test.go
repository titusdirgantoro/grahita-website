package payment

// Uji pembaca Claude vision (kartu F2-26) memakai server HTTP tiruan: tidak ada
// panggilan ke jaringan nyata, sehingga bentuk permintaan (blok gambar/dokumen,
// header) dan penanganan jawaban bisa diperiksa deterministik — termasuk janji
// bahwa KREDENSIAL tidak pernah bocor ke hasil bacaan, entri audit, maupun
// pesan galat.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serverClaude menangkap permintaan terakhir lalu mengembalikan jawaban yang
// ditentukan pengujian.
type serverClaude struct {
	status   int
	body     string
	diterima []byte
	header   http.Header
	panggil  int
	url      string
}

func (s *serverClaude) start(t *testing.T) *ClaudeReader {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.panggil++
		s.header = r.Header.Clone()
		s.url = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		s.diterima = b
		w.Header().Set("content-type", "application/json")
		status := s.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, s.body)
	}))
	t.Cleanup(srv.Close)
	return NewClaudeReader(ClaudeConfig{APIKey: "sk-uji-rahasia", BaseURL: srv.URL, Model: "claude-uji", HTTP: srv.Client()})
}

func jawabanModel(teks string) string {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": teks}},
	})
	return string(b)
}

func TestClaudeTanpaKunciTidakMemanggilJaringan(t *testing.T) {
	c := NewClaudeReader(ClaudeConfig{BaseURL: "http://127.0.0.1:1", Model: "x"})
	if _, err := c.Read(context.Background(), ReadInput{MimeType: MimePNG, Content: []byte("x")}); !errors.Is(err, ErrReaderUnavailable) {
		t.Fatalf("tanpa kunci harus ErrReaderUnavailable, dapat %v", err)
	}
	if c.Tersedia() {
		t.Error("Tersedia harus false tanpa kunci")
	}
}

func TestClaudeMembacaJawabanBerpagarKode(t *testing.T) {
	s := &serverClaude{body: jawabanModel("```json\n{\"nominal\":300000,\"tanggal\":\"2026-10-09\",\"bank\":\"BCA\",\"rek_akhir\":\"123 456 7890\",\"keyakinan\":0.97}\n```")}
	c := s.start(t)

	hasil, err := c.Read(context.Background(), ReadInput{
		OrderID: "o1", ProofID: "p1", NamaFile: "a.png", MimeType: MimePNG, Content: []byte("gambar"),
	})
	if err != nil {
		t.Fatalf("baca harus berhasil: %v", err)
	}
	if hasil.Nominal == nil || *hasil.Nominal != 300000 {
		t.Errorf("nominal harus terbaca, dapat %v", hasil.Nominal)
	}
	if hasil.Tanggal == nil || hasil.Tanggal.Format("2006-01-02") != "2026-10-09" {
		t.Errorf("tanggal harus terbaca, dapat %v", hasil.Tanggal)
	}
	if hasil.Bank != "BCA" || hasil.RekAkhir != "123 456 7890" {
		t.Errorf("bank/rekening harus terbaca, dapat %q/%q", hasil.Bank, hasil.RekAkhir)
	}
	if hasil.Confidence != 0.97 {
		t.Errorf("keyakinan harus 0.97, dapat %v", hasil.Confidence)
	}
	if len(hasil.Raw) == 0 || !json.Valid(hasil.Raw) {
		t.Errorf("jawaban mentah harus tersimpan sebagai JSON valid: %q", hasil.Raw)
	}

	// Bentuk permintaan: endpoint, header versi + kunci, blok gambar base64.
	if s.url != "/v1/messages" {
		t.Errorf("endpoint harus /v1/messages, dapat %q", s.url)
	}
	if got := s.header.Get("anthropic-version"); got != ClaudeVersion {
		t.Errorf("header anthropic-version harus %q, dapat %q", ClaudeVersion, got)
	}
	if got := s.header.Get("x-api-key"); got != "sk-uji-rahasia" {
		t.Errorf("kunci API harus dikirim lewat header x-api-key, dapat %q", got)
	}
	var kirim map[string]any
	if err := json.Unmarshal(s.diterima, &kirim); err != nil {
		t.Fatalf("badan permintaan harus JSON: %v", err)
	}
	if kirim["model"] != "claude-uji" {
		t.Errorf("model harus diteruskan, dapat %v", kirim["model"])
	}
	pesan := kirim["messages"].([]any)[0].(map[string]any)
	blok := pesan["content"].([]any)[0].(map[string]any)
	if blok["type"] != "image" {
		t.Errorf("blok pertama harus gambar, dapat %v", blok["type"])
	}
	src := blok["source"].(map[string]any)
	if src["media_type"] != MimePNG || src["data"] != base64.StdEncoding.EncodeToString([]byte("gambar")) {
		t.Errorf("sumber gambar harus base64 sesuai isi, dapat %v", src)
	}
}

func TestClaudeMemakaiBlokDokumenUntukPDF(t *testing.T) {
	s := &serverClaude{body: jawabanModel(`{"nominal":null,"tanggal":null,"bank":null,"rek_akhir":null,"keyakinan":0.1}`)}
	c := s.start(t)
	if _, err := c.Read(context.Background(), ReadInput{MimeType: MimePDF, NamaFile: "b.pdf", Content: []byte("%PDF-1.4")}); err != nil {
		t.Fatalf("baca PDF tidak boleh gagal: %v", err)
	}
	var kirim map[string]any
	_ = json.Unmarshal(s.diterima, &kirim)
	blok := kirim["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if blok["type"] != "document" {
		t.Errorf("PDF harus dikirim sebagai dokumen, dapat %v", blok["type"])
	}
}

func TestClaudeMenolakJenisYangTidakDidukung(t *testing.T) {
	s := &serverClaude{body: jawabanModel(`{}`)}
	c := s.start(t)
	if _, err := c.Read(context.Background(), ReadInput{MimeType: "text/csv", Content: []byte("a,b")}); !errors.Is(err, ErrReaderJenis) {
		t.Fatalf("jenis tidak didukung harus ErrReaderJenis, dapat %v", err)
	}
	if s.panggil != 0 {
		t.Error("jenis tidak didukung tidak boleh dikirim ke provider")
	}
}

func TestClaudeGalatProviderTidakMembocorkanKunci(t *testing.T) {
	s := &serverClaude{status: http.StatusUnauthorized, body: `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`}
	c := s.start(t)
	_, err := c.Read(context.Background(), ReadInput{MimeType: MimePNG, Content: []byte("gambar")})
	if !errors.Is(err, ErrReaderGagal) {
		t.Fatalf("status bukan 2xx harus ErrReaderGagal, dapat %v", err)
	}
	if strings.Contains(err.Error(), "sk-uji-rahasia") {
		t.Errorf("pesan galat tidak boleh memuat kunci: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("pesan galat provider harus diteruskan untuk diagnosa: %v", err)
	}
}

func TestClaudeJawabanTakTerbacaAdalahGalat(t *testing.T) {
	kasus := map[string]string{
		"tanpa teks": `{"content":[]}`,
		"bukan JSON": jawabanModel("saya tidak bisa membaca gambar ini"),
		"JSON rusak": jawabanModel(`{"nominal": }`),
	}
	for nama, body := range kasus {
		t.Run(nama, func(t *testing.T) {
			s := &serverClaude{body: body}
			c := s.start(t)
			if _, err := c.Read(context.Background(), ReadInput{MimeType: MimePNG, Content: []byte("gambar")}); !errors.Is(err, ErrReaderGagal) {
				t.Fatalf("jawaban tak terbaca harus ErrReaderGagal, dapat %v", err)
			}
		})
	}
}

func TestClaudeMenjepitNilaiYangTidakMasukAkal(t *testing.T) {
	s := &serverClaude{body: jawabanModel(`{"nominal":-5000,"tanggal":"bukan tanggal","bank":"  BCA  ","rek_akhir":"7890","keyakinan":1.7}`)}
	c := s.start(t)
	hasil, err := c.Read(context.Background(), ReadInput{MimeType: MimePNG, Content: []byte("gambar")})
	if err != nil {
		t.Fatalf("baca harus berhasil: %v", err)
	}
	if hasil.Nominal != nil {
		t.Errorf("nominal negatif harus dibiarkan kosong, dapat %v", *hasil.Nominal)
	}
	if hasil.Tanggal != nil {
		t.Errorf("tanggal tak terbaca harus nil, dapat %v", hasil.Tanggal)
	}
	if hasil.Confidence != 1 {
		t.Errorf("keyakinan harus dijepit ke 1, dapat %v", hasil.Confidence)
	}
	if hasil.Bank != "BCA" {
		t.Errorf("bank harus dirapikan, dapat %q", hasil.Bank)
	}
}

func TestClaudeMenerimaTanggalRFC3339DanMembulatkanKeyakinan(t *testing.T) {
	s := &serverClaude{body: jawabanModel(`{"nominal":1000.4,"tanggal":"2026-10-09T10:00:00Z","rek_akhir":"7890","keyakinan":0.91234}`)}
	c := s.start(t)
	hasil, err := c.Read(context.Background(), ReadInput{MimeType: MimePNG, Content: []byte("gambar")})
	if err != nil {
		t.Fatalf("baca harus berhasil: %v", err)
	}
	if hasil.Nominal == nil || *hasil.Nominal != 1000 {
		t.Errorf("nominal harus dibulatkan ke rupiah bulat, dapat %v", hasil.Nominal)
	}
	if hasil.Confidence != 0.912 {
		t.Errorf("keyakinan harus dibulatkan 3 desimal (kolom numeric(4,3)), dapat %v", hasil.Confidence)
	}
	if hasil.Tanggal == nil {
		t.Error("RFC3339 harus diterima")
	}
}

func TestClaudeGalatJaringanTidakMembocorkanKunci(t *testing.T) {
	c := NewClaudeReader(ClaudeConfig{
		APIKey:  "sk-jaringan-rahasia",
		BaseURL: "http://127.0.0.1:1",
		Model:   "x",
		HTTP:    &http.Client{Timeout: time.Second},
	})
	_, err := c.Read(context.Background(), ReadInput{MimeType: MimePNG, Content: []byte("gambar")})
	if !errors.Is(err, ErrReaderUnavailable) {
		t.Fatalf("galat jaringan harus ErrReaderUnavailable, dapat %v", err)
	}
	if strings.Contains(err.Error(), "sk-jaringan-rahasia") {
		t.Errorf("pesan galat tidak boleh memuat kunci: %v", err)
	}
}

func TestBawaanKonfigurasiPembaca(t *testing.T) {
	c := NewClaudeReader(ClaudeConfig{})
	if c.baseURL != DefaultClaudeBaseURL || c.model != DefaultClaudeModel {
		t.Errorf("bawaan harus dipakai, dapat %q/%q", c.baseURL, c.model)
	}
	c2 := NewClaudeReader(ClaudeConfig{BaseURL: "https://contoh.test/", Model: "m"})
	if c2.baseURL != "https://contoh.test" {
		t.Errorf("garis miring akhir harus dibuang, dapat %q", c2.baseURL)
	}
}
