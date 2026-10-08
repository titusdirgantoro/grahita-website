// Package apierr menyediakan bentuk error API yang seragam untuk seluruh
// permukaan /api/v1:
//
//	{"error":{"code":"VALIDATION_FAILED","message":"...","fields":{"qty":"harus > 0"}}}
//
// Handler tidak pernah menulis pesan error sendiri; semuanya lewat paket ini
// agar klien frontend hanya menangani satu bentuk respons kegagalan
// (SystemDesign.md §6).
package apierr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// Kode error yang stabil — dipakai klien untuk bercabang, jangan diubah
// tanpa menaikkan versi API.
const (
	CodeValidationFailed = "VALIDATION_FAILED"
	CodeUnauthenticated  = "UNAUTHENTICATED"
	CodeForbidden        = "FORBIDDEN"
	CodeNotFound         = "NOT_FOUND"
	CodeConflict         = "CONFLICT"
	CodeRateLimited      = "RATE_LIMITED"
	CodeTimeout          = "TIMEOUT"
	CodeInternal         = "INTERNAL"
	CodeUnavailable      = "UNAVAILABLE"
)

// Error adalah error API bertipe. Status HTTP dan kode mesin selalu
// berdampingan supaya tidak ada handler yang mengarang status sendiri.
type Error struct {
	status  int
	code    string
	message string
	fields  map[string]string
	cause   error
}

// Error memenuhi interface error.
func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s (%s): %v", e.code, e.message, e.cause)
	}
	return fmt.Sprintf("%s (%s)", e.code, e.message)
}

// Unwrap memungkinkan errors.Is/As terhadap penyebab asli.
func (e *Error) Unwrap() error { return e.cause }

// Status adalah kode HTTP yang harus dikirim.
func (e *Error) Status() int { return e.status }

// Code adalah kode mesin yang stabil.
func (e *Error) Code() string { return e.code }

// Message adalah pesan yang aman ditampilkan ke pengguna akhir.
func (e *Error) Message() string { return e.message }

// Fields berisi kesalahan per-field untuk form. Boleh nil.
func (e *Error) Fields() map[string]string { return e.fields }

// Envelope adalah representasi JSON lengkap dari sebuah error.
type Envelope struct {
	Error Payload `json:"error"`
}

// Payload adalah isi objek "error".
type Payload struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// JSON mengembalikan representasi yang siap ditulis ke response body.
func (e *Error) JSON() Envelope {
	return Envelope{Error: Payload{Code: e.code, Message: e.message, Fields: e.fields}}
}

// New membuat error API baru.
func New(status int, code, message string) *Error {
	return &Error{status: status, code: code, message: message}
}

// Wrap melampirkan penyebab teknis (tidak pernah dikirim ke klien).
func Wrap(cause error, status int, code, message string) *Error {
	return &Error{status: status, code: code, message: message, cause: cause}
}

// Validation adalah 400 dengan detail per-field.
func Validation(message string, fields map[string]string) *Error {
	return &Error{status: http.StatusBadRequest, code: CodeValidationFailed, message: message, fields: fields}
}

// WithFields melampirkan detail per-field pada error yang sudah dibuat.
// Dipakai handler yang punya tabel keputusan status/kode sendiri.
func (e *Error) WithFields(fields map[string]string) *Error {
	if len(fields) > 0 {
		e.fields = fields
	}
	return e
}

// Unauthenticated adalah 401 — belum login atau sesi kedaluwarsa.
func Unauthenticated(message string) *Error {
	return New(http.StatusUnauthorized, CodeUnauthenticated, message)
}

// Forbidden adalah 403 — sudah login tetapi role-nya tidak diizinkan.
func Forbidden(message string) *Error {
	return New(http.StatusForbidden, CodeForbidden, message)
}

// NotFound adalah 404.
func NotFound(message string) *Error {
	return New(http.StatusNotFound, CodeNotFound, message)
}

// Conflict adalah 409 dengan kode yang bisa dipilih pemanggil.
func Conflict(code, message string) *Error {
	return New(http.StatusConflict, code, message)
}

// RateLimited adalah 429.
func RateLimited(message string) *Error {
	return New(http.StatusTooManyRequests, CodeRateLimited, message)
}

// Internal adalah 500; penyebab asli hanya masuk log, bukan ke klien.
func Internal(cause error) *Error {
	return &Error{status: http.StatusInternalServerError, code: CodeInternal, message: "Terjadi kesalahan pada server.", cause: cause}
}

// From memetakan error apa pun menjadi *Error. Error yang sudah bertipe
// dikembalikan apa adanya sehingga handler boleh mengembalikan error dari
// service tanpa membungkusnya ulang.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	switch {
	case errors.Is(err, context.Canceled):
		return Wrap(err, 499, "CLIENT_CLOSED_REQUEST", "Permintaan dibatalkan klien.")
	case errors.Is(err, context.DeadlineExceeded):
		return Wrap(err, http.StatusGatewayTimeout, CodeTimeout, "Permintaan melebihi batas waktu.")
	default:
		return Internal(err)
	}
}
