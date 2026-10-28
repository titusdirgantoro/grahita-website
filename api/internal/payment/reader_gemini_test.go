package payment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type geminiTestKey string

func (k geminiTestKey) KeyForRead(context.Context) (string, error) { return string(k), nil }

func TestGeminiReaderMemakaiModelDanTidakMembocorkanKunci(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("x-goog-api-key") != "kunci-uji-rahasia" {
			t.Error("API key header salah")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if strings.Contains(r.URL.String(), "kunci-uji-rahasia") {
			t.Error("API key bocor di URL")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"nominal\":125000,\"tanggal\":\"2026-10-28\",\"bank\":\"BCA\",\"rek_akhir\":\"1234\",\"keyakinan\":0.97}"}]}}]}`))
	}))
	defer server.Close()
	r := NewGeminiReader(GeminiConfig{Keys: geminiTestKey("kunci-uji-rahasia"), BaseURL: server.URL})
	if err := r.TestKey(context.Background(), "kunci-uji-rahasia"); err != nil {
		t.Fatal(err)
	}
	got, err := r.Read(context.Background(), ReadInput{Content: []byte("fake-image"), MimeType: MimePNG})
	if err != nil {
		t.Fatal(err)
	}
	if got.Nominal == nil || *got.Nominal != 125000 {
		t.Fatalf("nominal: %+v", got)
	}
	if len(paths) != 2 || paths[0] != "/models/"+GeminiModel+":generateContent" {
		t.Fatalf("path: %v", paths)
	}
}

func TestGeminiReaderErrorProviderTidakMemuatResponsAtauKunci(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"kunci-uji-rahasia"}`))
	}))
	defer server.Close()
	r := NewGeminiReader(GeminiConfig{Keys: geminiTestKey("kunci-uji-rahasia"), BaseURL: server.URL})
	err := r.TestKey(context.Background(), "kunci-uji-rahasia")
	if err == nil || strings.Contains(err.Error(), "kunci-uji-rahasia") {
		t.Fatalf("galat tidak aman: %v", err)
	}
}
