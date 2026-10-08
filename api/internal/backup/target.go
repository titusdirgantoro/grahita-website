package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Berkas adalah satu berkas yang ditemukan di tujuan cadangan.
type Berkas struct {
	Nama    string    `json:"nama"`
	Ukuran  int64     `json:"ukuran"`
	ModTime time.Time `json:"mod_time"`
}

// Target adalah tujuan penyimpanan cadangan. Dua implementasi dipakai:
// `Rclone` (Google Drive saat produksi, sesuai Architecture.md §7) dan `Dir`
// (direktori lokal — dipakai uji restore/CI dan lingkungan yang menyalin
// direktorinya sendiri ke Drive).
type Target interface {
	// Nama mengidentifikasi tujuan (ditulis ke backup_runs.tujuan).
	Nama() string
	// Upload menulis satu berkas (ukuran -1 bila tidak diketahui).
	Upload(ctx context.Context, nama string, isi io.Reader, ukuran int64) error
	// Buka membaca kembali satu berkas.
	Buka(ctx context.Context, nama string) (io.ReadCloser, error)
	// Daftar mendaftarkan berkas dengan awalan prefix.
	Daftar(ctx context.Context, prefix string) ([]Berkas, error)
	// Hapus menghapus satu berkas (dipakai retensi, bukan untuk bukti).
	Hapus(ctx context.Context, nama string) error
}

// Nol adalah target kosong: seluruh operasi menolak dengan jelas.
type Nol struct{}

// Nama mengembalikan string kosong (tujuan belum diatur).
func (Nol) Nama() string { return "" }

// Upload selalu menolak.
func (Nol) Upload(context.Context, string, io.Reader, int64) error { return ErrTujuanBelumDiatur }

// Buka selalu menolak.
func (Nol) Buka(context.Context, string) (io.ReadCloser, error) { return nil, ErrTujuanBelumDiatur }

// Daftar selalu menolak.
func (Nol) Daftar(context.Context, string) ([]Berkas, error) { return nil, ErrTujuanBelumDiatur }

// Hapus selalu menolak.
func (Nol) Hapus(context.Context, string) error { return ErrTujuanBelumDiatur }

// Dir adalah target berbasis direktori lokal.
//
// Direktori ini adalah tempat cadangan MENTAH (terenkripsi) mendarat; di
// produksi ia bisa berupa mount Google Drive atau direktori yang
// disinkronkan rclone oleh timer terpisah. Berkas ditulis lewat berkas
// sementara lalu di-rename supaya cadangan setengah jadi tidak pernah
// terlihat sebagai cadangan utuh.
type Dir struct {
	Root string
}

// NewDir membuat target direktori, membuatnya bila belum ada.
func NewDir(root string) (*Dir, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrTujuanBelumDiatur
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("jalur tujuan cadangan tidak valid: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("buat direktori tujuan cadangan: %w", err)
	}
	return &Dir{Root: abs}, nil
}

// Nama mengidentifikasi direktori tujuan.
func (d *Dir) Nama() string { return "dir:" + d.Root }

// jalur menerjemahkan nama logis menjadi jalur berkas, menolak keluar direktori.
func (d *Dir) jalur(nama string) (string, error) {
	bersih := path.Clean(strings.TrimPrefix(strings.TrimSpace(nama), "/"))
	if bersih == "." || bersih == "" || strings.HasPrefix(bersih, "..") {
		return "", fmt.Errorf("nama berkas cadangan tidak valid: %q", nama)
	}
	p := filepath.Join(d.Root, filepath.FromSlash(bersih))
	if p != d.Root && !strings.HasPrefix(p, d.Root+string(os.PathSeparator)) {
		return "", fmt.Errorf("nama berkas cadangan keluar direktori tujuan: %q", nama)
	}
	return p, nil
}

// Upload menulis berkas secara atomik (tulis .tmp lalu rename).
func (d *Dir) Upload(_ context.Context, nama string, isi io.Reader, _ int64) error {
	tujuan, err := d.jalur(nama)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(tujuan), 0o700); err != nil {
		return fmt.Errorf("buat subdirektori cadangan: %w", err)
	}
	tmp := tujuan + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("buat berkas cadangan sementara: %w", err)
	}
	if _, err := io.Copy(f, isi); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("tulis berkas cadangan: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("tutup berkas cadangan: %w", err)
	}
	if err := os.Rename(tmp, tujuan); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("pindahkan berkas cadangan: %w", err)
	}
	return nil
}

// Buka membuka berkas cadangan untuk dibaca.
func (d *Dir) Buka(_ context.Context, nama string) (io.ReadCloser, error) {
	tujuan, err := d.jalur(nama)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(tujuan)
	if err != nil {
		return nil, fmt.Errorf("buka berkas cadangan %s: %w", nama, err)
	}
	return f, nil
}

// Daftar mendaftarkan berkas di bawah prefix, terurut menurut nama.
func (d *Dir) Daftar(_ context.Context, prefix string) ([]Berkas, error) {
	out := []Berkas{}
	akar := d.Root
	err := filepath.WalkDir(akar, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(akar, p)
		if err != nil {
			return err
		}
		nama := filepath.ToSlash(rel)
		if !strings.HasPrefix(nama, prefix) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out = append(out, Berkas{Nama: nama, Ukuran: info.Size(), ModTime: info.ModTime().UTC()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("daftar berkas cadangan: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Nama < out[j].Nama })
	return out, nil
}

// Hapus menghapus satu berkas cadangan.
func (d *Dir) Hapus(_ context.Context, nama string) error {
	tujuan, err := d.jalur(nama)
	if err != nil {
		return err
	}
	if err := os.Remove(tujuan); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("hapus berkas cadangan %s: %w", nama, err)
	}
	return nil
}

// Rclone adalah target Google Drive (atau remote lain) lewat biner rclone
// (Architecture.md §7). Kredensial rclone TIDAK PERNAH ada di repo: berkas
// konfigurasinya diberikan lewat environment (BACKUP_RCLONE_CONFIG) dan
// dimiliki operator.
type Rclone struct {
	Biner   string
	Remote  string
	Config  string
	Timeout time.Duration
}

// Nama mengidentifikasi remote tujuan (tanpa mengungkap kredensial).
func (r *Rclone) Nama() string { return "rclone:" + r.Remote }

// args menyusun argumen rclone dengan opsi bersama.
func (r *Rclone) args(args ...string) []string {
	out := make([]string, 0, len(args)+6)
	if r.Config != "" {
		out = append(out, "--config", r.Config)
	}
	out = append(out, args...)
	if r.Timeout > 0 {
		out = append(out, "--timeout", r.Timeout.String())
	}
	return append(out, "--log-level", "ERROR")
}

// remotePath menyusun jalur remote untuk satu nama logis.
func (r *Rclone) remotePath(nama string) string {
	return strings.TrimSuffix(r.Remote, "/") + "/" + strings.TrimPrefix(strings.TrimSpace(nama), "/")
}

// jalankan menjalankan rclone tanpa aliran stdin/stdout.
func (r *Rclone) jalankan(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.Biner, r.args(args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rclone %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Upload menyalurkan isi berkas ke remote lewat `rclone rcat` (isi tidak
// pernah ditulis polos ke disk tujuan).
func (r *Rclone) Upload(ctx context.Context, nama string, isi io.Reader, _ int64) error {
	cmd := exec.CommandContext(ctx, r.Biner, r.args("rcat", r.remotePath(nama))...)
	cmd.Stdin = isi
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rclone rcat %s: %w (stderr: %s)", nama, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Buka membaca berkas remote lewat `rclone cat`.
func (r *Rclone) Buka(ctx context.Context, nama string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, r.Biner, r.args("cat", r.remotePath(nama))...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("siapkan rclone cat: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("jalankan rclone cat: %w", err)
	}
	return &pipeRclone{stdout: stdout, cmd: cmd, stderr: &stderr}, nil
}

// pipeRclone menutup proses rclone saat pembaca ditutup.
type pipeRclone struct {
	stdout io.ReadCloser
	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

// Read meneruskan pembacaan.
func (p *pipeRclone) Read(b []byte) (int, error) { return p.stdout.Read(b) }

// Close menunggu proses rclone selesai dan melaporkan kegagalannya.
func (p *pipeRclone) Close() error {
	_ = p.stdout.Close()
	if err := p.cmd.Wait(); err != nil {
		return fmt.Errorf("rclone cat: %w (stderr: %s)", err, strings.TrimSpace(p.stderr.String()))
	}
	return nil
}

// entriLsJSON adalah satu baris keluaran `rclone lsjson`.
type entriLsJSON struct {
	Path    string `json:"Path"`
	Size    int64  `json:"Size"`
	ModTime string `json:"ModTime"`
	IsDir   bool   `json:"IsDir"`
}

// Daftar mendaftarkan berkas remote memakai `rclone lsjson` (metadata saja,
// tanpa mengunduh isi).
func (r *Rclone) Daftar(ctx context.Context, prefix string) ([]Berkas, error) {
	base := strings.TrimSuffix(r.Remote, "/")
	if prefix != "" {
		base = base + "/" + strings.TrimPrefix(prefix, "/")
	}
	raw, err := r.jalankan(ctx, "lsjson", "--recursive", "--files-only", base)
	if err != nil {
		// Direktori yang belum ada bukan kegagalan: berarti belum ada cadangan.
		if strings.Contains(err.Error(), "error listing") || strings.Contains(err.Error(), "directory not found") {
			return []Berkas{}, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return []Berkas{}, nil
	}
	var entri []entriLsJSON
	if err := json.Unmarshal(raw, &entri); err != nil {
		return nil, fmt.Errorf("urai keluaran rclone lsjson: %w", err)
	}
	out := make([]Berkas, 0, len(entri))
	for _, e := range entri {
		if e.IsDir {
			continue
		}
		nama := e.Path
		if prefix != "" {
			nama = strings.TrimSuffix(strings.TrimPrefix(prefix, "/"), "/") + "/" + e.Path
		}
		b := Berkas{Nama: nama, Ukuran: e.Size}
		if e.ModTime != "" {
			if t, err := time.Parse(time.RFC3339Nano, e.ModTime); err == nil {
				b.ModTime = t.UTC()
			}
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Nama < out[j].Nama })
	return out, nil
}

// Hapus menghapus satu berkas remote.
func (r *Rclone) Hapus(ctx context.Context, nama string) error {
	if _, err := r.jalankan(ctx, "deletefile", r.remotePath(nama)); err != nil {
		return err
	}
	return nil
}
