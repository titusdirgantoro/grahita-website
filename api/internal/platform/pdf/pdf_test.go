package pdf

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGotenbergMenghormatiUkuranHalamanCSS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/forms/chromium/convert/html" {
			t.Errorf("path = %q", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			return
		}
		if got := r.FormValue("preferCssPageSize"); got != "true" {
			t.Errorf("preferCssPageSize = %q", got)
		}
		file, header, err := r.FormFile("files")
		if err != nil {
			t.Errorf("file: %v", err)
			return
		}
		defer file.Close()
		if header.Filename != "index.html" {
			t.Errorf("nama file = %q", header.Filename)
		}
		body, _ := io.ReadAll(file)
		if string(body) != "<html></html>" {
			t.Errorf("isi HTML = %q", body)
		}
		_, _ = w.Write([]byte("%PDF-1.7"))
	}))
	defer server.Close()

	got, err := NewGotenberg(server.URL).HTMLToPDF(context.Background(), []byte("<html></html>"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "%PDF-1.7" {
		t.Errorf("hasil = %q", got)
	}
}
