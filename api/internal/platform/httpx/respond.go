// Package httpx berisi helper HTTP yang dipakai lapisan transport:
// penulisan JSON, dekode body dengan validasi, dan pembacaan parameter.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/grahita/portal/api/internal/platform/apierr"
)

// maxBody adalah batas ukuran body JSON (unggahan file punya jalur sendiri).
const maxBody = 1 << 20 // 1 MiB

var validate = validator.New(validator.WithRequiredStructEnabled())

// init membuat nama field pada pesan validasi mengikuti tag json, bukan nama
// field Go, supaya frontend bisa mencocokkan langsung dengan form-nya.
func init() {
	validate.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "" || name == "-" {
			return strings.ToLower(fld.Name)
		}
		return name
	})
}

// JSON menulis nilai sebagai JSON dengan status tertentu.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("gagal menulis respons JSON", "err", err)
	}
}

// OK adalah JSON 200.
func OK(w http.ResponseWriter, v any) { JSON(w, http.StatusOK, v) }

// Created adalah JSON 201.
func Created(w http.ResponseWriter, v any) { JSON(w, http.StatusCreated, v) }

// NoContent adalah 204 tanpa body.
func NoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// Fail menerjemahkan error apa pun ke bentuk seragam dan menuliskannya.
//
// Error yang bukan *apierr.Error dianggap kesalahan internal: detail aslinya
// hanya masuk log, pesan ke klien tetap generik.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	e := apierr.From(err)
	if e.Status() >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "request gagal",
			"err", err, "code", e.Code(), "path", r.URL.Path, "request_id", RequestIDFrom(r.Context()))
	}
	JSON(w, e.Status(), e.JSON())
}

// DecodeJSON membaca body JSON ke dst lalu memvalidasi struct-nya.
//
// Body yang bukan JSON, terlalu besar, atau punya field tak dikenal
// menghasilkan VALIDATION_FAILED, bukan 500.
func DecodeJSON(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &maxErr):
			return apierr.Validation("Body permintaan terlalu besar.", map[string]string{"body": "maksimal 1 MB"})
		case errors.As(err, &typeErr):
			return apierr.Validation("Body permintaan tidak valid.", map[string]string{typeErr.Field: "tipe nilai tidak sesuai"})
		case errors.Is(err, io.EOF):
			return apierr.Validation("Body permintaan kosong.", map[string]string{"body": "wajib diisi"})
		default:
			return apierr.Validation("Body permintaan bukan JSON yang valid.", map[string]string{"body": err.Error()})
		}
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return apierr.Validation("Body permintaan harus berisi satu objek JSON.", map[string]string{"body": "ada data tambahan setelah objek"})
	}
	return Validate(dst)
}

// Validate menjalankan tag `validate` pada struct dan memetakan hasilnya ke
// bentuk fields yang dipakai klien (kunci = nama field JSON).
func Validate(dst any) error {
	if err := validate.Struct(dst); err != nil {
		var verrs validator.ValidationErrors
		if errors.As(err, &verrs) {
			fields := make(map[string]string, len(verrs))
			for _, fe := range verrs {
				fields[fe.Field()] = messageFor(fe)
			}
			return apierr.Validation("Data yang dikirim belum lengkap atau tidak valid.", fields)
		}
		return apierr.Validation("Data yang dikirim belum valid.", nil)
	}
	return nil
}

func messageFor(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "wajib diisi"
	case "email":
		return "format email tidak valid"
	case "min":
		return "minimal " + fe.Param()
	case "max":
		return "maksimal " + fe.Param()
	case "gt":
		return "harus lebih besar dari " + fe.Param()
	case "gte":
		return "harus minimal " + fe.Param()
	case "oneof":
		return "harus salah satu dari: " + fe.Param()
	default:
		return "tidak valid"
	}
}

// QueryInt membaca parameter query bilangan bulat dengan nilai default.
func QueryInt(r *http.Request, key string, def, min, max int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apierr.Validation("Parameter "+key+" harus berupa angka.", map[string]string{key: "bukan angka"})
	}
	if n < min {
		n = min
	}
	if max > 0 && n > max {
		n = max
	}
	return n, nil
}

// ClientIP mengambil alamat IP klien, menghormati header proxy yang dipasang
// Caddy (X-Forwarded-For), dan mengembalikan "" bila tidak bisa dipastikan.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if net.ParseIP(first) != nil {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	if net.ParseIP(host) == nil {
		return ""
	}
	return host
}
