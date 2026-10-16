// Command s3fixture menyediakan object storage S3-compatible minimal khusus
// fixture regresi lokal. Server hanya boleh bind ke alamat loopback dan seluruh
// objek hilang ketika proses dihentikan.
package main

import (
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type object struct {
	body        []byte
	contentType string
	modified    time.Time
}

type fixture struct {
	mu      sync.RWMutex
	bucket  string
	objects map[string]object
}

func newFixture(bucket string) *fixture {
	return &fixture{bucket: bucket, objects: make(map[string]object)}
}

func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) == 0 || parts[0] != f.bucket {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 || parts[1] == "" {
		f.serveBucket(w, r)
		return
	}
	f.serveObject(w, r, parts[1])
}

func (f *fixture) serveBucket(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		f.list(w, r.URL.Query().Get("prefix"))
	default:
		http.Error(w, "metode tidak didukung", http.StatusMethodNotAllowed)
	}
}

func (f *fixture) serveObject(w http.ResponseWriter, r *http.Request, key string) {
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "gagal membaca objek", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.objects[key] = object{body: body, contentType: r.Header.Get("Content-Type"), modified: time.Now().UTC()}
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		f.mu.RLock()
		obj, ok := f.objects[key]
		f.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if obj.contentType != "" {
			w.Header().Set("Content-Type", obj.contentType)
		}
		if disposition := r.URL.Query().Get("response-content-disposition"); disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(obj.body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(obj.body)
		}
	case http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "metode tidak didukung", http.StatusMethodNotAllowed)
	}
}

type listBucketResult struct {
	XMLName     xml.Name      `xml:"ListBucketResult"`
	Xmlns       string        `xml:"xmlns,attr"`
	Name        string        `xml:"Name"`
	Prefix      string        `xml:"Prefix"`
	KeyCount    int           `xml:"KeyCount"`
	MaxKeys     int           `xml:"MaxKeys"`
	IsTruncated bool          `xml:"IsTruncated"`
	Contents    []listContent `xml:"Contents"`
}

type listContent struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int    `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

func (f *fixture) list(w http.ResponseWriter, prefix string) {
	f.mu.RLock()
	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	result := listBucketResult{Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/", Name: f.bucket, Prefix: prefix, KeyCount: len(keys), MaxKeys: 1000}
	for _, key := range keys {
		obj := f.objects[key]
		result.Contents = append(result.Contents, listContent{Key: key, LastModified: obj.modified.Format(time.RFC3339Nano), ETag: `"fixture"`, Size: len(obj.body), StorageClass: "STANDARD"})
	}
	f.mu.RUnlock()

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(result)
}

func validate(addr, bucket string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("alamat wajib berbentuk IP:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("alamat fixture S3 wajib memakai IP loopback eksplisit")
	}
	if port == "" || port == "0" {
		return errors.New("port fixture S3 wajib eksplisit dan bukan nol")
	}
	if !strings.HasPrefix(bucket, "grahita-browser-fixture-") {
		return errors.New("bucket fixture wajib diawali grahita-browser-fixture-")
	}
	return nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:19090", "alamat dengar loopback")
	bucket := flag.String("bucket", "grahita-browser-fixture-local", "nama bucket sintetis")
	flag.Parse()
	if err := validate(*addr, *bucket); err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: *addr, Handler: newFixture(*bucket), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("fixture S3 sintetis aktif pada %s", *addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
