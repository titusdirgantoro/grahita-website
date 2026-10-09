// Package pgtypes berisi helper konversi antara tipe Go sederhana dan tipe
// nullable PostgreSQL dari pgx.
//
// Dipakai oleh seluruh repository agar tidak ada tempat lain yang menyentuh
// pgtype secara langsung.
package pgtypes

import (
	"github.com/jackc/pgx/v5/pgtype"
)

// UUID mengubah string menjadi pgtype.UUID (NULL bila kosong/tidak valid).
func UUID(s string) pgtype.UUID {
	var u pgtype.UUID
	if s == "" {
		return u
	}
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}
	}
	return u
}

// UUIDString mengubah kolom uuid menjadi string (kosong bila NULL).
func UUIDString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	v, err := u.Value()
	if err != nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// Text mengubah string menjadi pgtype.Text (NULL bila kosong).
func Text(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// TextOr mengembalikan string dari pgtype.Text (kosong bila NULL).
func TextOr(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

// Bool mengubah bool menjadi pgtype.Bool (bukan NULL).
func Bool(b bool) pgtype.Bool {
	return pgtype.Bool{Bool: b, Valid: true}
}

// BoolPtr mengubah *bool menjadi pgtype.Bool (NULL bila nil) — dipakai untuk
// argumen filter opsional sqlc.narg.
func BoolPtr(p *bool) pgtype.Bool {
	if p == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *p, Valid: true}
}

// Int8Ptr mengubah *int64 menjadi pgtype.Int8 (NULL bila nil) — dipakai untuk
// argumen sqlc.narg bilangan besar (mis. nominal yang belum terbaca).
func Int8Ptr(p *int64) pgtype.Int8 {
	if p == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *p, Valid: true}
}
