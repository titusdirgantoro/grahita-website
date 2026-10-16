package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/platform/storage"
)

func TestValidateMenolakBindDanBucketTidakAman(t *testing.T) {
	cases := []struct {
		name   string
		addr   string
		bucket string
	}{
		{"wildcard IPv4", "0.0.0.0:19090", "grahita-browser-fixture-uji"},
		{"wildcard pendek", ":19090", "grahita-browser-fixture-uji"},
		{"hostname", "localhost:19090", "grahita-browser-fixture-uji"},
		{"port nol", "127.0.0.1:0", "grahita-browser-fixture-uji"},
		{"bucket umum", "127.0.0.1:19090", "grahita"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validate(tc.addr, tc.bucket); err == nil {
				t.Fatal("konfigurasi tidak aman harus ditolak")
			}
		})
	}
	if err := validate("127.0.0.1:19090", "grahita-browser-fixture-uji"); err != nil {
		t.Fatalf("konfigurasi loopback sintetis harus diterima: %v", err)
	}
}

func TestFixturePutGetHeadListDeleteDanDisposition(t *testing.T) {
	srv := httptest.NewServer(newFixture("grahita-browser-fixture-uji"))
	t.Cleanup(srv.Close)
	url := srv.URL + "/grahita-browser-fixture-uji/proofs/order-1/bukti.pdf"

	req, _ := http.NewRequest(http.MethodPut, url, strings.NewReader("PDF-UJI"))
	req.Header.Set("Content-Type", "application/pdf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}

	resp, err = http.Get(url + "?response-content-disposition=attachment%3B+filename%3Duji.pdf")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !bytes.Equal(body, []byte("PDF-UJI")) || resp.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("GET tidak mengembalikan objek asli: type=%q body=%q", resp.Header.Get("Content-Type"), body)
	}
	if got := resp.Header.Get("Content-Disposition"); got != "attachment; filename=uji.pdf" {
		t.Fatalf("Content-Disposition = %q", got)
	}

	resp, err = http.Head(url)
	if err != nil {
		t.Fatalf("HEAD objek: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength != 7 {
		t.Fatalf("HEAD status=%d length=%d", resp.StatusCode, resp.ContentLength)
	}

	resp, err = http.Get(srv.URL + "/grahita-browser-fixture-uji?list-type=2&prefix=proofs%2F")
	if err != nil {
		t.Fatalf("LIST: %v", err)
	}
	listing, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !bytes.Contains(listing, []byte("proofs/order-1/bukti.pdf")) {
		t.Fatalf("LIST tidak memuat objek: %s", listing)
	}

	req, _ = http.NewRequest(http.MethodDelete, url, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = resp.Body.Close()
	resp, err = http.Get(url)
	if err != nil {
		t.Fatalf("GET sesudah DELETE: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET sesudah DELETE status=%d", resp.StatusCode)
	}
}

func TestFixtureKompatibelDenganKlienS3ProduksiDanUnduhBrowser(t *testing.T) {
	srv := httptest.NewServer(newFixture("grahita-browser-fixture-uji"))
	t.Cleanup(srv.Close)
	store, err := storage.NewS3(storage.S3Config{
		Endpoint: srv.URL, AccessKey: "akses-uji", SecretKey: "rahasia-uji",
		Bucket: "grahita-browser-fixture-uji", Region: "us-east-1", UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}

	ctx := context.Background()
	want := []byte("PDF-NYATA-FIXTURE")
	if err := store.Put(ctx, "proofs/order-1/bukti.pdf", bytes.NewReader(want), int64(len(want)), "application/pdf"); err != nil {
		t.Fatalf("Put production client: %v", err)
	}
	if err := store.Health(ctx); err != nil {
		t.Fatalf("Health production client: %v", err)
	}
	url, err := store.PresignDownload(ctx, "proofs/order-1/bukti.pdf", "bukti UJI.pdf", time.Minute)
	if err != nil {
		t.Fatalf("PresignDownload: %v", err)
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("unduh browser: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !bytes.Equal(got, want) {
		t.Fatalf("isi unduhan berbeda: %q", got)
	}
	if disposition := resp.Header.Get("Content-Disposition"); !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, "bukti UJI.pdf") {
		t.Fatalf("Content-Disposition unduhan = %q", disposition)
	}
}
