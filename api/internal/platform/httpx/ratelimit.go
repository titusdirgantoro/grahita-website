package httpx

import (
	"net/http"
	"sync"
	"time"

	"github.com/grahita/portal/api/internal/platform/apierr"
)

// RateLimiter adalah pembatas laju sederhana berbasis jendela tetap.
//
// Dipakai untuk login, lupa password, validasi token, dan unggahan
// (Architecture.md §6). Karena hanya ada satu proses API, penyimpanan di
// memori cukup; bila kelak ada beberapa instance, ganti ke Redis.
type RateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	count int
	reset time.Time
}

// NewRateLimiter membuat limiter "limit request per window".
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
}

// Allow mencatat satu percobaan untuk key dan melaporkan apakah masih diizinkan.
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	// Bersihkan bucket kedaluwarsa agar peta tidak tumbuh tanpa batas.
	for k, b := range l.buckets {
		if now.After(b.reset) {
			delete(l.buckets, k)
		}
	}

	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		l.buckets[key] = &bucket{count: 1, reset: now.Add(l.window)}
		return true
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	return true
}

// Reset menghapus penghitung untuk key (dipanggil setelah login berhasil).
func (l *RateLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// Middleware membatasi request per key yang dihasilkan keyFn.
func (l *RateLimiter) Middleware(keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(keyFn(r)) {
				Fail(w, r, apierr.RateLimited("Terlalu banyak percobaan. Coba lagi beberapa saat lagi."))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ByIP menandai limit per alamat IP; selalu dipakai bersama limit per akun.
func ByIP(r *http.Request) string { return "ip:" + ClientIP(r) }
