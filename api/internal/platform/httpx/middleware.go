package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/grahita/portal/api/internal/platform/apierr"
)

type ctxKey string

const ctxKeyRequestID ctxKey = "request_id"

// HeaderRequestID adalah header yang dipakai untuk korelasi log.
const HeaderRequestID = "X-Request-Id"

// RequestID memastikan setiap request punya ID yang ikut tercatat di log
// (Architecture.md §8).
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if id == "" || len(id) > 64 {
			buf := make([]byte, 8)
			if _, err := rand.Read(buf); err != nil {
				id = "unknown"
			} else {
				id = hex.EncodeToString(buf)
			}
		}
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, id)))
	})
}

// RequestIDFrom mengambil request ID dari context (kosong bila tidak ada).
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Unwrap memungkinkan http.ResponseController menjangkau ResponseWriter asli.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Logger mencatat satu baris JSON per request.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}

		level := slog.LevelInfo
		switch {
		case sw.status >= 500:
			level = slog.LevelError
		case r.URL.Path == "/healthz" && sw.status < 400:
			level = slog.LevelDebug // dipanggil healthcheck tiap 10 detik
		}
		slog.Log(r.Context(), level, "http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"bytes", sw.bytes,
			"durasi_ms", time.Since(start).Milliseconds(),
			"request_id", RequestIDFrom(r.Context()),
		)
	})
}

// Recoverer mengubah panic menjadi 500 dengan bentuk error yang seragam dan
// menjaga proses tetap hidup.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.ErrorContext(r.Context(), "panic pada handler",
					"panic", rec,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
					"request_id", RequestIDFrom(r.Context()),
				)
				Fail(w, r, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "Terjadi kesalahan pada server."))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
