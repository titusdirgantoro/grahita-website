package backup

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Koneksi adalah parameter koneksi PostgreSQL yang sudah dipecah.
//
// Parameter dikirim ke biner pg_dump/pg_restore lewat ENVIRONMENT anak
// (PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE), bukan lewat argumen proses:
// daftar proses di server tidak boleh membocorkan kata sandi database
// (Architecture.md §6).
type Koneksi struct {
	Host     string
	Port     uint16
	User     string
	Password string
	Database string
	SSLMode  string
	Params   map[string]string
}

// KoneksiDariDSN memecah DSN PostgreSQL (URL atau format kata kunci libpq).
func KoneksiDariDSN(dsn string) (Koneksi, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return Koneksi{}, fmt.Errorf("DSN database kosong")
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return Koneksi{}, fmt.Errorf("DSN database tidak valid: %w", err)
		}
		k := Koneksi{
			Host:   u.Hostname(),
			User:   u.User.Username(),
			Params: map[string]string{},
		}
		if p := u.Port(); p != "" {
			n, err := strconv.ParseUint(p, 10, 16)
			if err != nil {
				return Koneksi{}, fmt.Errorf("port pada DSN tidak valid: %w", err)
			}
			k.Port = uint16(n)
		}
		if k.Password, _ = u.User.Password(); k.Password == "" {
			k.Password = os.Getenv("PGPASSWORD")
		}
		k.Database = strings.TrimPrefix(u.Path, "/")
		q := u.Query()
		for key, vals := range q {
			if len(vals) == 0 {
				continue
			}
			if err := terapkanParam(&k, key, vals[0]); err != nil {
				return Koneksi{}, err
			}
		}
		if k.Database == "" {
			return Koneksi{}, fmt.Errorf("DSN database tidak menyebut nama database")
		}
		return k, nil
	}

	// Format kata kunci: host=... port=... user=... password=... dbname=...
	k := Koneksi{Params: map[string]string{}}
	for _, bagian := range strings.Fields(dsn) {
		key, nilai, ada := strings.Cut(bagian, "=")
		if !ada {
			return Koneksi{}, fmt.Errorf("DSN database tidak dikenali: %q", dsn)
		}
		if err := terapkanParam(&k, key, strings.Trim(nilai, "'\"")); err != nil {
			return Koneksi{}, err
		}
	}
	if k.Database == "" {
		return Koneksi{}, fmt.Errorf("DSN database tidak menyebut nama database")
	}
	return k, nil
}

// terapkanParam memetakan satu parameter DSN ke field Koneksi.
func terapkanParam(k *Koneksi, key, nilai string) error {
	switch strings.ToLower(key) {
	case "host":
		k.Host = nilai
	case "port":
		n, err := strconv.ParseUint(nilai, 10, 16)
		if err != nil {
			return fmt.Errorf("port pada DSN tidak valid: %w", err)
		}
		k.Port = uint16(n)
	case "user":
		k.User = nilai
	case "password":
		k.Password = nilai
	case "dbname", "database":
		k.Database = nilai
	case "sslmode":
		k.SSLMode = nilai
	default:
		if k.Params == nil {
			k.Params = map[string]string{}
		}
		k.Params[key] = nilai
	}
	return nil
}

// Env menyusun variabel lingkungan untuk biner klien PostgreSQL.
func (k Koneksi) Env() []string {
	env := []string{"PGDATABASE=" + k.Database}
	if k.Host != "" {
		env = append(env, "PGHOST="+k.Host)
	}
	if k.Port != 0 {
		env = append(env, "PGPORT="+strconv.Itoa(int(k.Port)))
	}
	if k.User != "" {
		env = append(env, "PGUSER="+k.User)
	}
	if k.Password != "" {
		env = append(env, "PGPASSWORD="+k.Password)
	}
	if k.SSLMode != "" {
		env = append(env, "PGSSLMODE="+k.SSLMode)
	}
	return env
}

// Samarkan mengembalikan deskripsi koneksi TANPA kata sandi (untuk log/pesan).
func (k Koneksi) Samarkan() string {
	s := ""
	if k.User != "" {
		s += k.User + "@"
	}
	host := k.Host
	if host == "" {
		host = "localhost"
	}
	s += host
	if k.Port != 0 {
		s += ":" + strconv.Itoa(int(k.Port))
	}
	return s + "/" + k.Database
}

// Dumper menjalankan biner klien PostgreSQL.
type Dumper struct {
	BinerDump    string
	BinerRestore string
	Log          *slog.Logger
	// Timeout membatasi satu operasi dump/restore (bawaan 60 menit).
	Timeout time.Duration
}

// timeout mengembalikan batas waktu efektif.
func (d *Dumper) timeout() time.Duration {
	if d.Timeout <= 0 {
		return 60 * time.Minute
	}
	return d.Timeout
}

// Dump menjalankan `pg_dump -Fc` ke berkas keluaran (format custom, tanpa
// owner/privileges supaya bisa dipulihkan ke peran lain).
func (d *Dumper) Dump(ctx context.Context, koneksi Koneksi, keluaran string) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	bin := d.BinerDump
	if bin == "" {
		bin = "pg_dump"
	}
	cmd := exec.CommandContext(ctx, bin,
		"--format=custom",
		"--no-owner",
		"--no-privileges",
		"--compress=6",
		"--file", keluaran,
	)
	cmd.Env = append(os.Environ(), koneksi.Env()...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump (%s) gagal: %w (stderr: %s)", koneksi.Samarkan(), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Restore memulihkan berkas dump ke database pada koneksi tujuan.
//
// Asumsi: database tujuan SUDAH ada dan masih kosong (uji restore selalu
// memakai database sekali pakai).
func (d *Dumper) Restore(ctx context.Context, koneksi Koneksi, berkas string) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	bin := d.BinerRestore
	if bin == "" {
		bin = "pg_restore"
	}
	cmd := exec.CommandContext(ctx, bin,
		"--no-owner",
		"--no-privileges",
		"--exit-on-error",
		"--dbname", koneksi.Database,
		berkas,
	)
	cmd.Env = append(os.Environ(), koneksi.Env()...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore ke %s gagal: %w (stderr: %s)", koneksi.Samarkan(), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
