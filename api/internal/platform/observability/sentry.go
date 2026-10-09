package observability

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"
)

// SentryOptions adalah parameter inisialisasi Sentry (Architecture.md §8).
//
// DSN selalu berasal dari environment (SENTRY_DSN) dan tidak pernah ada di
// repo (Architecture.md §6, scripts/check-secrets.sh).
type SentryOptions struct {
	DSN              string
	Environment      string
	Release          string
	TracesSampleRate float64
	Logger           *slog.Logger
}

// sentryAktif menandai apakah client Sentry benar-benar terpasang. Tanpa DSN
// seluruh fungsi pelaporan menjadi no-op, sehingga lingkungan pengembangan dan
// pengujian tidak mengirim apa pun.
var sentryAktif atomic.Bool

// InitSentry memasang client Sentry bila DSN diisi.
//
// Mengembalikan fungsi penutup yang membuang (flush) peristiwa yang masih
// dalam antrean; panggil saat proses berhenti. Tanpa DSN, mengembalikan no-op
// dan tidak ada error — Sentry adalah pelengkap, bukan syarat jalan.
func InitSentry(o SentryOptions) (func(), error) {
	if strings.TrimSpace(o.DSN) == "" {
		return func() {}, nil
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              o.DSN,
		Environment:      o.Environment,
		Release:          o.Release,
		TracesSampleRate: o.TracesSampleRate,
		EnableTracing:    o.TracesSampleRate > 0,
		AttachStacktrace: true,
	})
	if err != nil {
		return nil, err
	}
	sentryAktif.Store(true)
	if o.Logger != nil {
		o.Logger.Info("Sentry aktif", "environment", o.Environment, "release", o.Release)
	}
	return func() { sentry.Flush(3 * time.Second) }, nil
}

// SentryAktif melaporkan apakah client Sentry terpasang (dipakai pengujian dan
// /readyz opsional).
func SentryAktif() bool { return sentryAktif.Load() }

// TangkapError mengirim satu error ke Sentry dengan tag tambahan.
//
// Selalu aman dipanggil walau Sentry tidak dikonfigurasi. Tidak pernah
// memuat data rahasia: pemanggil hanya boleh mengirim penanda non-rahasia
// (request_id, rute, peran).
func TangkapError(_ context.Context, err error, extra map[string]string) {
	if err == nil || !sentryAktif.Load() {
		return
	}
	hub := sentry.CurrentHub().Clone()
	for k, v := range extra {
		hub.Scope().SetTag(k, v)
	}
	hub.CaptureException(err)
}

// TangkapPanic mengirim nilai panic ke Sentry.
func TangkapPanic(_ context.Context, nilai any, extra map[string]string) {
	if !sentryAktif.Load() {
		return
	}
	hub := sentry.CurrentHub().Clone()
	for k, v := range extra {
		hub.Scope().SetTag(k, v)
	}
	hub.Recover(nilai)
}
