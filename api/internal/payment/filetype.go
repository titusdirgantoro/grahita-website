package payment

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/grahita/portal/api/internal/settings"
)

// Kunci setelan yang dipakai batas unggah (PRD §4.9). Nilainya dibaca lewat
// settings.Service.Int sehingga `max_file`/`max_mb` yang diubah Superadmin
// langsung berlaku — tidak ada angka batas yang ditulis ulang di kode.
const (
	keyMaxFile = settings.MaxFile
	keyMaxMB   = settings.MaxMB
)

// berkasKind adalah jenis berkas bukti yang diterima (PRD CP-ORD-04).
type berkasKind struct {
	nama string
	mime string
	// ext adalah ekstensi yang dipakai pada kunci object storage (selalu
	// diturunkan dari isi berkas, bukan dari nama kiriman klien).
	ext string
	// extCocok adalah ekstensi yang SAH untuk jenis ini. Nama berkas wajib
	// memakai salah satunya sehingga ekstensi palsu (mis. `.jpg` atas isi PDF,
	// atau `.exe` atas isi JPEG) tertolak.
	extCocok map[string]bool
}

var (
	kindJPEG = berkasKind{
		nama:     "JPEG",
		mime:     MimeJPEG,
		ext:      "jpg",
		extCocok: map[string]bool{".jpg": true, ".jpeg": true},
	}
	kindPNG = berkasKind{
		nama:     "PNG",
		mime:     MimePNG,
		ext:      "png",
		extCocok: map[string]bool{".png": true},
	}
	kindPDF = berkasKind{
		nama:     "PDF",
		mime:     MimePDF,
		ext:      "pdf",
		extCocok: map[string]bool{".pdf": true},
	}
)

// kenali menentukan jenis berkas dari MAGIC BYTES isinya — bukan dari nama
// berkas maupun Content-Type kiriman klien. Hanya tiga jenis ini yang diterima
// sebagai bukti transfer (PRD CP-ORD-04).
func kenali(isi []byte) (berkasKind, bool) {
	switch {
	case bytes.HasPrefix(isi, []byte{0xFF, 0xD8, 0xFF}):
		return kindJPEG, true
	case bytes.HasPrefix(isi, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return kindPNG, true
	case bytes.HasPrefix(isi, []byte("%PDF-")):
		return kindPDF, true
	default:
		return berkasKind{}, false
	}
}

// ekstensiBerkas mengembalikan ekstensi nama berkas dalam huruf kecil
// (termasuk titik), atau "" bila tidak ada.
func ekstensiBerkas(nama string) string {
	return strings.ToLower(filepath.Ext(nama))
}
