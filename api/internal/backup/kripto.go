package backup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
)

// ErrIdentitasKosong dikembalikan bila cadangan akan didekripsi tanpa berkas
// identitas age.
var ErrIdentitasKosong = errors.New("berkas identitas age belum dikonfigurasi")

// Kripto mengenkripsi dan mendekripsi berkas cadangan dengan age
// (Architecture.md §6/§7: "Backup | Terenkripsi sebelum diunggah ke Google
// Drive"; rahasia lewat SOPS+age, tidak pernah masuk repo).
//
// Kunci publik boleh ada di environment (bukan rahasia); kunci privat hanya
// hidup sebagai berkas milik operator (BACKUP_AGE_IDENTITY_FILE) dan tidak
// pernah dibaca untuk enkripsi.
type Kripto struct {
	penerima        []age.Recipient
	berkasIdentitas string
}

// NewKripto menyiapkan kripto dari kunci publik age dan (opsional) berkas
// identitas. Kunci yang tidak bisa diurai membuat penerima kosong sehingga
// seluruh pekerjaan cadangan menolak berjalan (fail closed).
func NewKripto(recipient, identity string) *Kripto {
	k := &Kripto{berkasIdentitas: strings.TrimSpace(identity)}
	if strings.TrimSpace(recipient) != "" {
		if recipients, err := age.ParseRecipients(strings.NewReader(recipient)); err == nil {
			k.penerima = recipients
		}
	}
	return k
}

// SiapEnkripsi melaporkan kesiapan enkripsi (tanpa kunci publik: tidak siap).
func (k *Kripto) SiapEnkripsi() error {
	if len(k.penerima) == 0 {
		return fmt.Errorf("%w: isi BACKUP_AGE_RECIPIENT dengan kunci publik age", ErrKunciEnkripsi)
	}
	return nil
}

// SiapDekripsi melaporkan kesiapan dekripsi (butuh berkas identitas).
func (k *Kripto) SiapDekripsi() error {
	if k.berkasIdentitas == "" {
		return fmt.Errorf("%w: isi BACKUP_AGE_IDENTITY_FILE", ErrIdentitasKosong)
	}
	if _, err := os.Stat(k.berkasIdentitas); err != nil {
		return fmt.Errorf("berkas identitas age tidak terbaca: %w", err)
	}
	return nil
}

// Enkripsi menyalin src ke dst dalam bentuk terenkripsi age dan mengembalikan
// jumlah byte PLAINTEXT yang diproses.
func (k *Kripto) Enkripsi(dst io.Writer, src io.Reader) (int64, error) {
	if err := k.SiapEnkripsi(); err != nil {
		return 0, err
	}
	w, err := age.Encrypt(dst, k.penerima...)
	if err != nil {
		return 0, fmt.Errorf("mulai enkripsi age: %w", err)
	}
	penghitung := &hitungPembaca{r: src}
	if _, err := io.Copy(w, penghitung); err != nil {
		return penghitung.n, fmt.Errorf("enkripsi cadangan: %w", err)
	}
	if err := w.Close(); err != nil {
		return penghitung.n, fmt.Errorf("tutup enkripsi age: %w", err)
	}
	return penghitung.n, nil
}

// EnkripsiBerkas mengenkripsi masukan menjadi keluaran.
func (k *Kripto) EnkripsiBerkas(keluaran, masukan string) (int64, error) {
	in, err := os.Open(masukan)
	if err != nil {
		return 0, fmt.Errorf("buka berkas cadangan polos: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(keluaran)
	if err != nil {
		return 0, fmt.Errorf("buat berkas cadangan terenkripsi: %w", err)
	}
	n, err := k.Enkripsi(out, in)
	if cerr := out.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("tutup berkas cadangan terenkripsi: %w", cerr)
	}
	if err != nil {
		_ = os.Remove(keluaran)
		return n, err
	}
	return n, nil
}

// Dekripsi menyalin cadangan terenkripsi src ke dst polos.
func (k *Kripto) Dekripsi(dst io.Writer, src io.Reader) (int64, error) {
	if err := k.SiapDekripsi(); err != nil {
		return 0, err
	}
	ids, err := os.Open(k.berkasIdentitas)
	if err != nil {
		return 0, fmt.Errorf("buka berkas identitas age: %w", err)
	}
	defer func() { _ = ids.Close() }()
	identitas, err := age.ParseIdentities(ids)
	if err != nil {
		return 0, fmt.Errorf("urai berkas identitas age: %w", err)
	}
	if len(identitas) == 0 {
		return 0, fmt.Errorf("%w: berkas identitas tidak memuat identitas", ErrIdentitasKosong)
	}
	r, err := age.Decrypt(src, identitas...)
	if err != nil {
		return 0, fmt.Errorf("mulai dekripsi age: %w", err)
	}
	n, err := io.Copy(dst, r)
	if err != nil {
		return n, fmt.Errorf("dekripsi cadangan: %w", err)
	}
	return n, nil
}

// DekripsiBerkas mendekripsi masukan menjadi keluaran dan mengembalikan
// jumlah byte hasil dekripsi.
func (k *Kripto) DekripsiBerkas(keluaran, masukan string) (int64, error) {
	in, err := os.Open(masukan)
	if err != nil {
		return 0, fmt.Errorf("buka berkas cadangan terenkripsi: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(keluaran)
	if err != nil {
		return 0, fmt.Errorf("buat berkas hasil dekripsi: %w", err)
	}
	n, err := k.Dekripsi(out, in)
	if cerr := out.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("tutup berkas hasil dekripsi: %w", cerr)
	}
	if err != nil {
		_ = os.Remove(keluaran)
		return n, err
	}
	return n, nil
}

// DekripsiPembaca mendekripsi src menjadi pembaca (dipakai verifikasi byte
// objek tanpa menulis berkas antara).
func (k *Kripto) DekripsiPembaca(src io.Reader) (io.Reader, error) {
	if err := k.SiapDekripsi(); err != nil {
		return nil, err
	}
	ids, err := os.Open(k.berkasIdentitas)
	if err != nil {
		return nil, fmt.Errorf("buka berkas identitas age: %w", err)
	}
	defer func() { _ = ids.Close() }()
	identitas, err := age.ParseIdentities(ids)
	if err != nil {
		return nil, fmt.Errorf("urai berkas identitas age: %w", err)
	}
	return age.Decrypt(src, identitas...)
}

// DekripsiPembacaKeBerkas mendekripsi pembaca cadangan menjadi berkas polos.
func (k *Kripto) DekripsiPembacaKeBerkas(src io.Reader, keluaran string) (int64, error) {
	polos, err := k.DekripsiPembaca(src)
	if err != nil {
		return 0, err
	}
	out, err := os.Create(keluaran)
	if err != nil {
		return 0, fmt.Errorf("buat berkas hasil dekripsi: %w", err)
	}
	n, err := io.Copy(out, polos)
	if cerr := out.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("tutup berkas hasil dekripsi: %w", cerr)
	}
	if err != nil {
		_ = os.Remove(keluaran)
		return n, fmt.Errorf("tulis hasil dekripsi: %w", err)
	}
	return n, nil
}

// hitungPembaca menghitung byte yang dibaca (untuk ukuran plaintext).
type hitungPembaca struct {
	r io.Reader
	n int64
}

// Read meneruskan pembacaan sambil menghitung.
func (h *hitungPembaca) Read(b []byte) (int, error) {
	n, err := h.r.Read(b)
	h.n += int64(n)
	return n, err
}
