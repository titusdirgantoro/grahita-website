// Package storage adalah abstraksi object storage S3-compatible
// (MinIO saat lokal, R2/S3 saat produksi) — Architecture.md §1.
//
// Implementasi S3 asli menyusul bersama domain dokumen; paket ini menyediakan
// kontrak, implementasi in-memory untuk pengujian, dan penanda
// "belum dikonfigurasi" agar /readyz tidak berbohong.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotConfigured dikembalikan bila kredensial storage belum diisi.
var ErrNotConfigured = errors.New("object storage belum dikonfigurasi")

// Object adalah metadata objek yang disimpan.
type Object struct {
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// Info adalah metadata satu objek hasil pendaftaran (dipakai sinkronisasi
// cadangan dan rekonsiliasi DB↔object storage, Architecture.md §7).
type Info struct {
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	ModTime     time.Time `json:"mod_time"`
}

// Store adalah kontrak penyimpanan objek. Semua akses baca ke pengguna akhir
// memakai presigned URL berumur pendek (Architecture.md §6).
//
// `List` bersifat baca-saja dan WAJIB ada: cadangan dan rekonsiliasi perlu
// tahu objek apa saja yang benar-benar ada di storage (tanpa itu, objek yatim
// tidak mungkin ditemukan dan sinkronisasi tidak mungkin inkremental).
type Store interface {
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]Info, error)
	Health(ctx context.Context) error
}

// Disabled dipakai saat S3_ENDPOINT/S3_BUCKET kosong.
type Disabled struct{}

// Put selalu gagal karena storage belum dikonfigurasi.
func (Disabled) Put(context.Context, string, io.Reader, int64, string) error {
	return ErrNotConfigured
}

// Get selalu gagal karena storage belum dikonfigurasi.
func (Disabled) Get(context.Context, string) (io.ReadCloser, error) { return nil, ErrNotConfigured }

// PresignGet selalu gagal karena storage belum dikonfigurasi.
func (Disabled) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", ErrNotConfigured
}

// Delete selalu gagal karena storage belum dikonfigurasi.
func (Disabled) Delete(context.Context, string) error { return ErrNotConfigured }

// List selalu gagal karena storage belum dikonfigurasi.
func (Disabled) List(context.Context, string) ([]Info, error) { return nil, ErrNotConfigured }

// Health melaporkan bahwa storage memang belum aktif.
func (Disabled) Health(context.Context) error { return ErrNotConfigured }

// Memory adalah penyimpanan in-memory untuk pengujian dan mode lokal tanpa
// MinIO. Bukan untuk produksi: isi hilang saat proses berhenti.
type Memory struct {
	mu      sync.RWMutex
	objects map[string][]byte
	types   map[string]string
	mod     map[string]time.Time
	baseURL string
}

// NewMemory membuat penyimpanan in-memory.
func NewMemory(baseURL string) *Memory {
	if baseURL == "" {
		baseURL = "/files"
	}
	return &Memory{
		objects: map[string][]byte{},
		types:   map[string]string{},
		mod:     map[string]time.Time{},
		baseURL: baseURL,
	}
}

// Put menyalin isi objek ke memori.
func (m *Memory) Put(_ context.Context, key string, body io.Reader, size int64, contentType string) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("baca body: %w", err)
	}
	if size > 0 && int64(len(data)) != size {
		return fmt.Errorf("ukuran tidak sesuai: diharapkan %d, diterima %d", size, len(data))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	m.types[key] = contentType
	m.mod[key] = time.Now()
	return nil
}

// Get mengembalikan pembaca atas objek.
func (m *Memory) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("objek %q tidak ada", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// PresignGet mengembalikan URL statis (hanya untuk pengujian).
func (m *Memory) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.objects[key]; !ok {
		return "", fmt.Errorf("objek %q tidak ada", key)
	}
	return m.baseURL + "/" + key, nil
}

// PresignDownload memenuhi kontrak payment.DownloadSigner: tautan unduh
// dengan paksaan nama berkas (Content-Disposition attachment). URL yang
// dikembalikan tetap URL statis in-memory; parameter nama hanya memengaruhi
// bentuk tautan agar bisa diperiksa pengujian.
func (m *Memory) PresignDownload(_ context.Context, key, namaFile string, ttl time.Duration) (string, error) {
	u, err := m.PresignGet(context.Background(), key, ttl)
	if err != nil {
		return "", err
	}
	if namaFile == "" {
		return u, nil
	}
	return u + "?download=" + url.QueryEscape(namaFile), nil
}

// Delete menghapus objek.
func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	delete(m.types, key)
	delete(m.mod, key)
	return nil
}

// List mendaftarkan objek yang kuncinya berawalan prefix, terurut menurut
// kunci supaya hasilnya deterministik untuk pembandingan di pengujian.
func (m *Memory) List(_ context.Context, prefix string) ([]Info, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Info, 0, len(m.objects))
	for key, data := range m.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, Info{
			Key:         key,
			Size:        int64(len(data)),
			ContentType: m.types[key],
			ModTime:     m.mod[key],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Health selalu sehat.
func (m *Memory) Health(context.Context) error { return nil }
