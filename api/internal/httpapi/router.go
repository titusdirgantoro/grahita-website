// Package httpapi berisi router dan handler HTTP.
// Saat ini baru endpoint kesehatan dan beberapa pembacaan master data,
// cukup untuk memverifikasi seluruh rantai Docker berjalan.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/grahita/portal/api/internal/platform/config"
	"github.com/grahita/portal/api/internal/platform/db"
)

type api struct {
	pool    *db.Pool
	cfg     config.Config
	version string
}

// NewRouter menyusun seluruh rute. Memakai pola routing bawaan net/http
// ("METHOD /path"), tersedia sejak Go 1.22 — belum perlu framework.
func NewRouter(pool *db.Pool, cfg config.Config, version string) http.Handler {
	a := &api{pool: pool, cfg: cfg, version: version}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /readyz", a.readyz)
	mux.HandleFunc("GET /api/v1/meta", a.meta)
	mux.HandleFunc("GET /api/v1/settings", a.settings)
	mux.HandleFunc("GET /api/v1/products", a.products)
	mux.HandleFunc("GET /api/v1/branches", a.branches)

	return chain(mux, requestID, recoverer, logging, cors(cfg.CORSOrigins), securityHeaders)
}

// healthz: proses hidup. Tidak menyentuh dependensi.
func (a *api) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": a.version})
}

// readyz: siap melayani — dependensi dicek.
func (a *api) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := map[string]string{}
	code := http.StatusOK

	if err := a.pool.Ping(ctx); err != nil {
		checks["database"] = "gagal: " + err.Error()
		code = http.StatusServiceUnavailable
	} else {
		checks["database"] = "ok"
	}

	var migrasi int
	if err := a.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrasi); err != nil {
		checks["migrations"] = "gagal: " + err.Error()
		code = http.StatusServiceUnavailable
	} else {
		checks["migrations"] = "ok"
	}

	writeJSON(w, code, map[string]any{
		"status":     map[bool]string{true: "ready", false: "not ready"}[code == http.StatusOK],
		"checks":     checks,
		"migrations": migrasi,
	})
}

func (a *api) meta(w http.ResponseWriter, r *http.Request) {
	var pgVersion string
	_ = a.pool.QueryRow(r.Context(), `SELECT current_setting('server_version')`).Scan(&pgVersion)

	writeJSON(w, http.StatusOK, map[string]any{
		"app":        "grahita-portal-api",
		"version":    a.version,
		"env":        a.cfg.AppEnv,
		"postgres":   pgVersion,
		"waktu_wib":  time.Now().In(wib()).Format("2006-01-02 15:04:05"),
		"keterangan": "Kerangka awal. Endpoint bisnis menyusul sesuai SystemDesign.md §6.",
	})
}

func (a *api) settings(w http.ResponseWriter, r *http.Request) {
	rows, err := a.pool.Query(r.Context(), `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", err.Error())
		return
	}
	defer rows.Close()

	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			writeError(w, http.StatusInternalServerError, "SCAN_FAILED", err.Error())
			return
		}
		out[k] = json.RawMessage(v)
	}
	writeJSON(w, http.StatusOK, out)
}

type productRow struct {
	ID        string `json:"id"`
	Nama      string `json:"nama"`
	Jenis     string `json:"jenis"`
	Satuan    string `json:"satuan"`
	Harga     int64  `json:"harga_default"`
	Urutan    int    `json:"urutan"`
	IsActive  bool   `json:"is_active"`
	Deskripsi string `json:"deskripsi,omitempty"`
}

func (a *api) products(w http.ResponseWriter, r *http.Request) {
	rows, err := a.pool.Query(r.Context(), `
		SELECT id::text, nama, jenis::text, satuan, harga_default, urutan, is_active,
		       coalesce(deskripsi,'')
		  FROM products
		 ORDER BY urutan, nama`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", err.Error())
		return
	}
	defer rows.Close()

	list := []productRow{}
	for rows.Next() {
		var p productRow
		if err := rows.Scan(&p.ID, &p.Nama, &p.Jenis, &p.Satuan, &p.Harga, &p.Urutan, &p.IsActive, &p.Deskripsi); err != nil {
			writeError(w, http.StatusInternalServerError, "SCAN_FAILED", err.Error())
			return
		}
		list = append(list, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type branchRow struct {
	ID        string `json:"id"`
	Nama      string `json:"nama"`
	Kepala    string `json:"nama_kepala"`
	Email     string `json:"email"`
	IsCNC     bool   `json:"is_cnc"`
	IsSpecial bool   `json:"is_special"`
	PajakIn   bool   `json:"pajak_in"`
	IsActive  bool   `json:"is_active"`
}

func (a *api) branches(w http.ResponseWriter, r *http.Request) {
	rows, err := a.pool.Query(r.Context(), `
		SELECT id::text, nama, nama_kepala, email::text, is_cnc, is_special, pajak_in, is_active
		  FROM branches ORDER BY nama`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "QUERY_FAILED", err.Error())
		return
	}
	defer rows.Close()

	list := []branchRow{}
	for rows.Next() {
		var b branchRow
		if err := rows.Scan(&b.ID, &b.Nama, &b.Kepala, &b.Email, &b.IsCNC, &b.IsSpecial, &b.PajakIn, &b.IsActive); err != nil {
			writeError(w, http.StatusInternalServerError, "SCAN_FAILED", err.Error())
			return
		}
		list = append(list, b)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func wib() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		slog.Warn("zona waktu Asia/Jakarta tidak tersedia, memakai UTC")
		return time.UTC
	}
	return loc
}
