package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// s3Palsu adalah server S3 minimal in-process: cukup untuk membuktikan klien
// S3 kita (Put/Get/Delete/Health/presign) benar-benar mengirim permintaan yang
// sah. Ia TIDAK memverifikasi tanda tangan — verifikasi tanda tangan adalah
// tugas server S3 sungguhan (MinIO/R2).
type s3Palsu struct {
	mu       sync.Mutex
	objek    map[string][]byte
	tipe     map[string]string
	bucket   string
	requests []string
}

func newS3Palsu(bucket string) *s3Palsu {
	return &s3Palsu{objek: map[string][]byte{}, tipe: map[string]string{}, bucket: bucket}
}

func (s *s3Palsu) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	s.mu.Unlock()

	// Path style: /bucket/key...
	bagian := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(bagian) == 0 || bagian[0] != s.bucket {
		http.Error(w, "bucket salah", http.StatusNotFound)
		return
	}
	if len(bagian) == 1 || bagian[1] == "" {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "metode tidak didukung", http.StatusMethodNotAllowed)
		return
	}
	key := bagian[1]
	// Nilai opsi presign juga bisa datang lewat query (response-content-...),
	// tapi tidak dipakai server palsu ini.

	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		isi, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.objek[key] = isi
		s.tipe[key] = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		isi, ok := s.objek[key]
		if !ok {
			http.Error(w, "tidak ada", http.StatusNotFound)
			return
		}
		if tipe := s.tipe[key]; tipe != "" {
			w.Header().Set("Content-Type", tipe)
		}
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(isi)
		}
	case http.MethodDelete:
		delete(s.objek, key)
		delete(s.tipe, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "metode tidak didukung", http.StatusMethodNotAllowed)
	}
}

func newS3UntukUji(t *testing.T) (*S3, *s3Palsu) {
	t.Helper()
	palsu := newS3Palsu("grahita")
	srv := httptest.NewServer(palsu)
	t.Cleanup(srv.Close)
	store, err := NewS3(S3Config{
		Endpoint:     srv.URL,
		Region:       "us-east-1",
		Bucket:       "grahita",
		AccessKey:    "uji-akses",
		SecretKey:    "uji-rahasia",
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	return store, palsu
}

func TestS3PutGetDeleteHealth(t *testing.T) {
	store, palsu := newS3UntukUji(t)
	ctx := context.Background()

	isi := []byte("isi bukti transfer")
	if err := store.Put(ctx, "proofs/ord-1/acak.jpg", bytes.NewReader(isi), int64(len(isi)), "image/jpeg"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := palsu.objek["proofs/ord-1/acak.jpg"]; !bytes.Equal(got, isi) {
		t.Fatalf("isi tersimpan berbeda: %q", got)
	}
	if got := palsu.tipe["proofs/ord-1/acak.jpg"]; got != "image/jpeg" {
		t.Errorf("Content-Type tersimpan %q, mau image/jpeg", got)
	}

	rc, err := store.Get(ctx, "proofs/ord-1/acak.jpg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	balik, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("baca: %v", err)
	}
	if !bytes.Equal(balik, isi) {
		t.Errorf("isi baca ulang berbeda: %q", balik)
	}

	if err := store.Health(ctx); err != nil {
		t.Errorf("Health: %v", err)
	}
	if err := store.Delete(ctx, "proofs/ord-1/acak.jpg"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := palsu.objek["proofs/ord-1/acak.jpg"]; ok {
		t.Error("objek harus terhapus setelah Delete")
	}
	if _, err := store.Get(ctx, "proofs/ord-1/acak.jpg"); err == nil {
		t.Error("Get objek terhapus harus gagal")
	}
}

func TestS3PresignGetMenghasilkanTautanBertandaTangan(t *testing.T) {
	store, _ := newS3UntukUji(t)
	ctx := context.Background()

	raw, err := store.PresignGet(ctx, "proofs/ord-1/acak.jpg", 5*time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("URL presign tidak valid: %v", err)
	}
	if !strings.HasSuffix(u.Path, "/grahita/proofs/ord-1/acak.jpg") {
		t.Errorf("path presign salah: %q", u.Path)
	}
	q := u.Query()
	if q.Get("X-Amz-Signature") == "" {
		t.Error("presign harus memuat X-Amz-Signature")
	}
	if got := q.Get("X-Amz-Expires"); got != "300" {
		t.Errorf("X-Amz-Expires = %q, mau 300", got)
	}
	if got := q.Get("X-Amz-Algorithm"); got != "AWS4-HMAC-SHA256" {
		t.Errorf("X-Amz-Algorithm = %q", got)
	}
	if !strings.Contains(q.Get("X-Amz-Credential"), "uji-akses") {
		t.Errorf("X-Amz-Credential tidak memuat access key: %q", q.Get("X-Amz-Credential"))
	}
}

func TestS3PresignDownloadMemaksaAttachment(t *testing.T) {
	store, _ := newS3UntukUji(t)
	raw, err := store.PresignDownload(context.Background(), "proofs/ord-1/acak.pdf", "bukti transfer.pdf", time.Minute)
	if err != nil {
		t.Fatalf("PresignDownload: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("URL presign tidak valid: %v", err)
	}
	disp := u.Query().Get("response-content-disposition")
	if !strings.Contains(disp, "attachment") || !strings.Contains(disp, "bukti transfer.pdf") {
		t.Errorf("presign unduh harus menyetel attachment + nama berkas: %q", disp)
	}
	// Nama berbahaya disanitasi (tidak bisa memecah header).
	raw, err = store.PresignDownload(context.Background(), "k", "a\"b\r\nc.pdf", time.Minute)
	if err != nil {
		t.Fatalf("PresignDownload nama berbahaya: %v", err)
	}
	u, _ = url.Parse(raw)
	disp = u.Query().Get("response-content-disposition")
	if strings.ContainsAny(disp, "\r\n") {
		t.Errorf("nama berkas pada header belum disanitasi: %q", disp)
	}
	if !strings.Contains(disp, `filename="a_b__c.pdf"`) {
		t.Errorf("nama berkas berbahaya harus disanitasi, dapat %q", disp)
	}
}

func TestKonfigurasiS3WajibLengkap(t *testing.T) {
	kasus := []struct {
		nama string
		cfg  S3Config
	}{
		{"endpoint kosong", S3Config{Bucket: "b", AccessKey: "a", SecretKey: "s"}},
		{"bucket kosong", S3Config{Endpoint: "http://127.0.0.1:9000", AccessKey: "a", SecretKey: "s"}},
		{"kredensial kosong", S3Config{Endpoint: "http://127.0.0.1:9000", Bucket: "b"}},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			if _, err := NewS3(k.cfg); err == nil {
				t.Error("konfigurasi tidak lengkap harus ditolak, bukan diam-diam dipakai")
			}
		})
	}
	if _, err := NewS3(S3Config{Endpoint: "http://127.0.0.1:9000", Bucket: "b", AccessKey: "a", SecretKey: "s"}); err != nil {
		t.Errorf("konfigurasi lengkap harus diterima: %v", err)
	}
}

// Pastikan Memory juga memenuhi kedua kontrak presign yang dipakai domain.
func TestMemoryMemenuhiKontrakPresign(t *testing.T) {
	m := NewMemory("/files")
	ctx := context.Background()
	if err := m.Put(ctx, "proofs/x/a.jpg", bytes.NewReader([]byte("x")), 1, "image/jpeg"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	u, err := m.PresignDownload(ctx, "proofs/x/a.jpg", "a.jpg", time.Minute)
	if err != nil {
		t.Fatalf("PresignDownload: %v", err)
	}
	if !strings.Contains(u, "download=a.jpg") {
		t.Errorf("tautan unduh Memory harus membawa nama berkas: %q", u)
	}
	var _ interface {
		PresignGet(context.Context, string, time.Duration) (string, error)
	} = m
	var _ interface {
		PresignDownload(context.Context, string, string, time.Duration) (string, error)
	} = m
}

func TestDisabledStoreMenolakDenganJelas(t *testing.T) {
	var d Store = Disabled{}
	if err := d.Put(context.Background(), "k", bytes.NewReader(nil), 0, "image/jpeg"); err == nil {
		t.Error("Put pada storage belum dikonfigurasi harus gagal")
	}
	if _, err := d.PresignGet(context.Background(), "k", time.Minute); err == nil {
		t.Error("PresignGet pada storage belum dikonfigurasi harus gagal")
	}
}
