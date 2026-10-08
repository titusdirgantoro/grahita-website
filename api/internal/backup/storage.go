package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/platform/storage"
)

// Manifest adalah indeks salinan object storage di tujuan cadangan.
//
// Manifest membuat sinkronisasi INKREMENTAL tanpa mengunduh ulang seluruh
// objek setiap hari, dan sekaligus bukti apa yang pernah dicadangkan
// (ukuran + sha256 objek asli). Ia hanya metadata — isi berkas tetap
// terenkripsi age satu per satu.
type Manifest struct {
	Versi      int                      `json:"versi"`
	Diperbarui time.Time                `json:"diperbarui"`
	Objek      map[string]EntriManifest `json:"objek"`
}

// EntriManifest adalah satu objek yang sudah dicadangkan.
type EntriManifest struct {
	// Ukuran adalah ukuran objek ASLI (plaintext) menurut storage.
	Ukuran int64 `json:"ukuran"`
	// Sha256 adalah sidik objek asli (hex) saat dicadangkan.
	Sha256 string `json:"sha256"`
	// Nama adalah nama berkas terenkripsi di tujuan.
	Nama string `json:"nama"`
	// Pada adalah waktu pencadangan.
	Pada time.Time `json:"pada"`
}

// manifestKosong membuat manifest baru.
func manifestKosong() Manifest {
	return Manifest{Versi: 1, Objek: map[string]EntriManifest{}}
}

// MuatManifest membaca manifest dari tujuan; tujuan tanpa manifest dianggap
// kosong (bukan kegagalan) supaya cadangan pertama bisa berjalan.
func MuatManifest(ctx context.Context, t Target) (Manifest, error) {
	rc, err := t.Buka(ctx, NamaManifest)
	if err != nil {
		return manifestKosong(), nil
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return manifestKosong(), fmt.Errorf("baca manifest cadangan: %w", err)
	}
	m := manifestKosong()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return manifestKosong(), fmt.Errorf("urai manifest cadangan: %w", err)
	}
	if m.Objek == nil {
		m.Objek = map[string]EntriManifest{}
	}
	return m, nil
}

// SimpanManifest menulis manifest ke tujuan.
func SimpanManifest(ctx context.Context, t Target, m Manifest) error {
	m.Versi = 1
	if m.Objek == nil {
		m.Objek = map[string]EntriManifest{}
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("susun manifest cadangan: %w", err)
	}
	if err := t.Upload(ctx, NamaManifest, strings.NewReader(string(raw)), int64(len(raw))); err != nil {
		return fmt.Errorf("unggah manifest cadangan: %w", err)
	}
	return nil
}

// HasilSinkron merangkum satu siklus sinkronisasi objek.
type HasilSinkron struct {
	Diperiksa      int               `json:"diperiksa"`
	Baru           int               `json:"baru"`
	Dilewati       int               `json:"dilewati"`
	BytesBaru      int64             `json:"bytes_baru"`
	BaruKunci      []string          `json:"baru_kunci,omitempty"`
	Hilang         []string          `json:"hilang,omitempty"`
	TanpaBaris     []string          `json:"tanpa_baris,omitempty"`
	KesalahanKunci map[string]string `json:"kesalahan_kunci,omitempty"`
}

// kerjaStorage menyinkronkan seluruh bukti transfer dan dokumen yang DIRUJUK
// database ke tujuan cadangan dalam bentuk terenkripsi age, lalu menyimpan
// manifest inkremental. Tidak ada penghapusan: bukti transfer beretensi tanpa
// batas (keputusan pemilik) sehingga salinan pun tidak pernah dihapus.
func (s *Service) kerjaStorage(ctx context.Context) (int64, string, string, error) {
	if err := s.targetSiap(); err != nil {
		return 0, "", "", err
	}
	if err := s.kripto.SiapEnkripsi(); err != nil {
		return 0, "", "", err
	}
	if s.objek == nil {
		return 0, "", "", errCadanganTanpaObjek
	}
	if err := s.objek.Health(ctx); err != nil {
		return 0, "", "", fmt.Errorf("%w: %v", errCadanganTanpaObjek, err)
	}
	if err := os.MkdirAll(s.cfg.Kerja, 0o700); err != nil {
		return 0, "", "", fmt.Errorf("siapkan direktori kerja: %w", err)
	}

	proofs, err := s.repo.KunciProofs(ctx)
	if err != nil {
		return 0, "", "", fmt.Errorf("baca kunci bukti transfer: %w", err)
	}
	dokumen, err := s.repo.KunciDokumen(ctx)
	if err != nil {
		return 0, "", "", fmt.Errorf("baca kunci dokumen: %w", err)
	}
	kunci := urutkanKunci(append(append([]string{}, proofs...), dokumen...))

	manifest, err := MuatManifest(ctx, s.target)
	if err != nil {
		return 0, "", "", err
	}
	hasil, err := s.sinkronObjek(ctx, kunci, &manifest)
	if err != nil {
		return 0, "", "", err
	}
	manifest.Diperbarui = s.clk.Now().UTC()
	if err := SimpanManifest(ctx, s.target, manifest); err != nil {
		return 0, "", "", err
	}

	pesan := fmt.Sprintf("objek dirujuk: %d bukti + %d dokumen; baru=%d dilewati=%d byte_baru=%d",
		len(proofs), len(dokumen), hasil.Baru, hasil.Dilewati, hasil.BytesBaru)
	if len(hasil.Hilang) > 0 {
		pesan += fmt.Sprintf("; PERINGATAN %d kunci dirujuk DB tapi tidak ada di storage: %s",
			len(hasil.Hilang), strings.Join(batasi(hasil.Hilang, 5), ", "))
	}
	if len(hasil.KesalahanKunci) > 0 {
		return hasil.BytesBaru, s.target.Nama() + ":" + NamaManifest, pesan,
			fmt.Errorf("%d objek gagal disalin: %s", len(hasil.KesalahanKunci), ringkasPeta(hasil.KesalahanKunci))
	}
	return hasil.BytesBaru, s.target.Nama() + ":" + NamaManifest, pesan, nil
}

// sinkronObjek menyalin objek yang belum ada/berubah ke tujuan.
func (s *Service) sinkronObjek(ctx context.Context, kunci []string, manifest *Manifest) (HasilSinkron, error) {
	hasil := HasilSinkron{KesalahanKunci: map[string]string{}, Diperiksa: len(kunci)}
	if len(kunci) == 0 {
		return hasil, nil
	}

	// Peta objek nyata di storage (metadata saja).
	infoDiStorage := map[string]storage.Info{}
	for _, prefix := range []string{"proofs/", "documents/"} {
		daftar, err := s.objek.List(ctx, prefix)
		if err != nil {
			return hasil, fmt.Errorf("daftar objek %s: %w", prefix, err)
		}
		for _, info := range daftar {
			infoDiStorage[info.Key] = info
		}
	}
	// Berkas yang sudah ada di tujuan.
	adaDiTujuan := map[string]Berkas{}
	berkasTujuan, err := s.target.Daftar(ctx, PrefixStorage)
	if err != nil {
		return hasil, fmt.Errorf("daftar salinan objek: %w", err)
	}
	for _, b := range berkasTujuan {
		adaDiTujuan[b.Nama] = b
	}

	for _, key := range kunci {
		info, ada := infoDiStorage[key]
		if !ada {
			hasil.Hilang = append(hasil.Hilang, key)
			continue
		}
		nama := NamaCadanganObjek(key)
		entri, punyaEntri := manifest.Objek[key]
		if _, adaBerkas := adaDiTujuan[nama]; punyaEntri && adaBerkas && entri.Ukuran == info.Size {
			hasil.Dilewati++
			continue
		}
		ukuran, sha, err := s.salinObjek(ctx, key, nama)
		if err != nil {
			hasil.KesalahanKunci[key] = err.Error()
			continue
		}
		manifest.Objek[key] = EntriManifest{
			Ukuran: ukuran,
			Sha256: sha,
			Nama:   nama,
			Pada:   s.clk.Now().UTC(),
		}
		hasil.Baru++
		hasil.BytesBaru += ukuran
		hasil.BaruKunci = append(hasil.BaruKunci, key)
		// Manifest ditulis ulang di akhir; berkas ini memastikan objek yang
		// sudah disalin tidak disalin ulang bila proses mati di tengah jalan
		// pada pengujian berikutnya.
		manifest.Diperbarui = s.clk.Now().UTC()
		if err := SimpanManifest(ctx, s.target, *manifest); err != nil {
			return hasil, err
		}
	}
	return hasil, nil
}

// salinObjek menyalin satu objek dari storage ke tujuan dalam bentuk
// terenkripsi, mengembalikan ukuran asli dan sha256 objek asli.
func (s *Service) salinObjek(ctx context.Context, key, nama string) (int64, string, error) {
	sumber, err := s.objek.Get(ctx, key)
	if err != nil {
		return 0, "", fmt.Errorf("baca objek %s: %w", key, err)
	}
	defer func() { _ = sumber.Close() }()

	tmp := filepath.Join(s.cfg.Kerja, "objek-"+strconvNano(time.Now())+".age")
	defer func() { _ = os.Remove(tmp) }()
	out, err := os.Create(tmp)
	if err != nil {
		return 0, "", fmt.Errorf("buat berkas salinan objek: %w", err)
	}

	hash := sha256.New()
	teed := io.TeeReader(sumber, hash)
	ukuran, err := s.kripto.Enkripsi(out, teed)
	if cerr := out.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("tutup berkas salinan objek: %w", cerr)
	}
	if err != nil {
		return 0, "", err
	}

	f, err := os.Open(tmp)
	if err != nil {
		return 0, "", fmt.Errorf("buka berkas salinan objek: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return 0, "", fmt.Errorf("periksa berkas salinan objek: %w", err)
	}
	errUnggah := s.target.Upload(ctx, nama, f, info.Size())
	_ = f.Close()
	if errUnggah != nil {
		return 0, "", fmt.Errorf("unggah salinan objek %s: %w", key, errUnggah)
	}
	return ukuran, hex.EncodeToString(hash.Sum(nil)), nil
}

// NamaCadanganObjek menyusun nama salinan satu objek di tujuan.
func NamaCadanganObjek(key string) string {
	return PrefixStorage + key + SufiksAge
}

// StatusObjek adalah hasil pemeriksaan satu objek (DB vs salinan cadangan).
type StatusObjek struct {
	Kunci   string `json:"kunci"`
	Cocok   bool   `json:"cocok"`
	Masalah string `json:"masalah,omitempty"`
	Ukuran  int64  `json:"ukuran"`
	Sha256  string `json:"sha256,omitempty"`
}

// PeriksaObjek memverifikasi byte setiap objek terhadap salinan terenkripsi di
// tujuan: keduanya di-hash dan dibandingkan, dan dicocokkan dengan manifest
// bila entri ada. Inilah bukti bahwa "bukti/dokumen" benar-benar dapat
// dipulihkan utuh, bukan sekadar "tautan HTTP 200".
func (s *Service) PeriksaObjek(ctx context.Context, kunci []string, manifest Manifest) ([]StatusObjek, error) {
	if s.objek == nil {
		return nil, errCadanganTanpaObjek
	}
	out := make([]StatusObjek, 0, len(kunci))
	for _, key := range urutkanKunci(kunci) {
		status := StatusObjek{Kunci: key}
		sumber, err := s.objek.Get(ctx, key)
		if err != nil {
			status.Masalah = "objek tidak ada di storage: " + err.Error()
			out = append(out, status)
			continue
		}
		hashSumber := sha256.New()
		n, err := io.Copy(hashSumber, sumber)
		_ = sumber.Close()
		if err != nil {
			status.Masalah = "baca objek storage: " + err.Error()
			out = append(out, status)
			continue
		}
		status.Ukuran = n
		status.Sha256 = hex.EncodeToString(hashSumber.Sum(nil))

		nama := NamaCadanganObjek(key)
		rc, err := s.target.Buka(ctx, nama)
		if err != nil {
			status.Masalah = "salinan cadangan tidak ada: " + err.Error()
			out = append(out, status)
			continue
		}
		polos, err := s.kripto.DekripsiPembaca(rc)
		if err != nil {
			_ = rc.Close()
			status.Masalah = "dekripsi salinan: " + err.Error()
			out = append(out, status)
			continue
		}
		hashSalinan := sha256.New()
		_, errSalin := io.Copy(hashSalinan, polos)
		_ = rc.Close()
		if errSalin != nil {
			status.Masalah = "baca salinan: " + errSalin.Error()
			out = append(out, status)
			continue
		}
		shaSalinan := hex.EncodeToString(hashSalinan.Sum(nil))
		if shaSalinan != status.Sha256 {
			status.Masalah = fmt.Sprintf("byte salinan berbeda (sha256 %s != %s)", shaSalinan, status.Sha256)
			out = append(out, status)
			continue
		}
		if entri, ada := manifest.Objek[key]; ada && entri.Sha256 != "" && entri.Sha256 != status.Sha256 {
			status.Masalah = fmt.Sprintf("isi objek berubah setelah dicadangkan (manifest %s != %s)", entri.Sha256, status.Sha256)
			out = append(out, status)
			continue
		}
		status.Cocok = true
		out = append(out, status)
	}
	return out, nil
}

// PulihkanObjek memulihkan seluruh salinan objek ke direktori uji (staging)
// dalam bentuk POLOS, lalu mengembalikan jumlah objek yang berhasil berikut
// daftar masalahnya. Inilah "restore object storage" untuk uji kuartalan.
func (s *Service) PulihkanObjek(ctx context.Context, tujuan string, manifest Manifest) (int, []string, error) {
	if err := s.targetSiap(); err != nil {
		return 0, nil, err
	}
	if strings.TrimSpace(tujuan) == "" {
		return 0, nil, fmt.Errorf("direktori tujuan pemulihan objek kosong")
	}
	if err := os.MkdirAll(tujuan, 0o700); err != nil {
		return 0, nil, fmt.Errorf("siapkan direktori pemulihan objek: %w", err)
	}
	kunci := make([]string, 0, len(manifest.Objek))
	for key := range manifest.Objek {
		kunci = append(kunci, key)
	}
	sort.Strings(kunci)

	dipulihkan := 0
	masalah := []string{}
	for _, key := range kunci {
		entri := manifest.Objek[key]
		nama := entri.Nama
		if nama == "" {
			nama = NamaCadanganObjek(key)
		}
		rc, err := s.target.Buka(ctx, nama)
		if err != nil {
			masalah = append(masalah, fmt.Sprintf("%s: salinan tidak ada (%v)", key, err))
			continue
		}
		polos, err := s.kripto.DekripsiPembaca(rc)
		if err != nil {
			_ = rc.Close()
			masalah = append(masalah, fmt.Sprintf("%s: dekripsi gagal (%v)", key, err))
			continue
		}
		keluaran, err := s.tulisObjekPolos(tujuan, key, polos)
		_ = rc.Close()
		if err != nil {
			masalah = append(masalah, fmt.Sprintf("%s: %v", key, err))
			continue
		}
		if entri.Ukuran > 0 && keluaran != entri.Ukuran {
			masalah = append(masalah, fmt.Sprintf("%s: ukuran hasil %d != manifest %d", key, keluaran, entri.Ukuran))
			continue
		}
		if entri.Sha256 != "" {
			sha, err := sha256Berkas(filepath.Join(tujuan, filepath.FromSlash(key)))
			if err != nil {
				masalah = append(masalah, fmt.Sprintf("%s: %v", key, err))
				continue
			}
			if sha != entri.Sha256 {
				masalah = append(masalah, fmt.Sprintf("%s: sha256 hasil %s != manifest %s", key, sha, entri.Sha256))
				continue
			}
		}
		dipulihkan++
	}
	return dipulihkan, masalah, nil
}

// tulisObjekPolos menulis objek polos ke direktori tujuan memakai struktur
// kunci sebagai jalur, dan mengembalikan jumlah byte yang ditulis.
func (s *Service) tulisObjekPolos(tujuan, key string, isi io.Reader) (int64, error) {
	jalur := filepath.Join(tujuan, filepath.FromSlash(path.Clean(key)))
	if !strings.HasPrefix(jalur, filepath.Clean(tujuan)+string(os.PathSeparator)) {
		return 0, fmt.Errorf("kunci objek keluar direktori pemulihan: %q", key)
	}
	if err := os.MkdirAll(filepath.Dir(jalur), 0o700); err != nil {
		return 0, fmt.Errorf("buat subdirektori pemulihan: %w", err)
	}
	f, err := os.Create(jalur)
	if err != nil {
		return 0, fmt.Errorf("buat berkas pemulihan: %w", err)
	}
	n, err := io.Copy(f, isi)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("tulis berkas pemulihan: %w", err)
	}
	return n, nil
}

// sha256Berkas menghitung sidik satu berkas.
func sha256Berkas(jalur string) (string, error) {
	f, err := os.Open(jalur)
	if err != nil {
		return "", fmt.Errorf("buka berkas %s: %w", jalur, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash berkas %s: %w", jalur, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// batasi memotong daftar untuk pesan agar tetap ringkas.
func batasi(daftar []string, n int) []string {
	if len(daftar) <= n {
		return daftar
	}
	return append(append([]string{}, daftar[:n]...), fmt.Sprintf("(+%d lagi)", len(daftar)-n))
}

// ringkasPeta merangkum peta kesalahan menjadi satu baris.
func ringkasPeta(m map[string]string) string {
	kunci := make([]string, 0, len(m))
	for k := range m {
		kunci = append(kunci, k)
	}
	sort.Strings(kunci)
	bagian := make([]string, 0, len(kunci))
	for _, k := range batasi(kunci, 3) {
		bagian = append(bagian, k+"="+m[k])
	}
	return strings.Join(bagian, "; ")
}
