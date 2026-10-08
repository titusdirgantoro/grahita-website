package httpapi

import (
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// proofsUnavailable adalah jawaban bila layanan bukti belum terpasang.
func proofsUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan bukti transfer belum tersedia.")
}

// memoryBerkas adalah berapa banyak yang ditahan di memori saat mengurai
// multipart; sisanya melimpah ke berkas sementara di luar repo.
const memoryBerkas = 1 << 20 // 1 MiB

// fieldCNC adalah nama field multipart penanda Cash & Carry pada submit
// pesanan (PRD CP-CNC-02).
const fieldCNC = "is_cnc"

// defaultMaxRequestBytes adalah batas badan permintaan bila domain bukti tidak
// terpasang (mis. pada pengujian yang hanya memasang domain pesanan): cukup
// besar untuk beberapa berkas kecil, tetap terbatas.
const defaultMaxRequestBytes = 4 << 20 // 4 MiB

// parseCNC membaca penanda Cash & Carry dari nilai form. Nilai yang tidak
// dikenal DITOLAK (bukan diam-diam dianggap false) supaya salah ketik tidak
// membuat pesanan kehilangan penanda C&C tanpa terlihat.
func parseCNC(nilai []string) (bool, error) {
	if len(nilai) == 0 {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(nilai[0])) {
	case "", "false", "0", "off":
		return false, nil
	case "true", "1", "on":
		return true, nil
	default:
		return false, apierr.Validation("Penanda Cash & Carry tidak valid.",
			map[string]string{fieldCNC: "harus true atau false"})
	}
}

// unggahBukti adalah alur bersama kedua portal: batasi laju → baca berkas →
// simpan lewat domain.
func (a *API) unggahBukti(w http.ResponseWriter, r *http.Request) error {
	if a.d.Payments == nil {
		return proofsUnavailable()
	}
	actor := auth.ActorFrom(r.Context())
	if a.d.ProofLimiter != nil && !a.d.ProofLimiter.Allow(string(actor.Kind)+":"+actor.UserID) {
		return apierr.RateLimited("Terlalu banyak unggahan bukti. Coba lagi beberapa saat lagi.")
	}

	files, tutup, err := a.bacaBerkas(w, r)
	if err != nil {
		return err
	}
	defer tutup()
	saved, err := a.d.Payments.Upload(r.Context(), actor, chi.URLParam(r, "id"), files)
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.Created(w, data(saved))
	return nil
}

// bacaBerkas mengurai badan multipart dan mengembalikan berkas-berkas bukti
// beserta penutupnya (berkas multipart wajib ditutup setelah dibaca).
//
// Seluruh badan permintaan dibatasi `max_file × max_mb + kelonggaran` (dibaca
// dari setelan yang sama dengan service) supaya tidak ada unggahan raksasa yang
// menahan memori/disk server sebelum validasi.
func (a *API) bacaBerkas(w http.ResponseWriter, r *http.Request) ([]payment.FileInput, func(), error) {
	files, tutup, err := a.bacaMultipart(w, r, true)
	return files, tutup, err
}

// bacaSubmit mengurai badan multipart submit pesanan: berkas bukti pada field
// `file` (boleh NOL berkas — bukti boleh sudah diunggah lewat
// /orders/{id}/proofs) dan penanda C&C pada field `is_cnc`.
func (a *API) bacaSubmit(w http.ResponseWriter, r *http.Request) ([]payment.FileInput, bool, func(), error) {
	files, tutup, err := a.bacaMultipart(w, r, false)
	if err != nil {
		return nil, false, tutup, err
	}
	isCNC, err := parseCNC(r.MultipartForm.Value[fieldCNC])
	if err != nil {
		return nil, false, tutup, err
	}
	return files, isCNC, tutup, nil
}

// bacaMultipart mengurai badan multipart permintaan bukti. wajibBerkas
// menentukan apakah minimal satu berkas dituntut (endpoint unggah) atau tidak
// (endpoint submit, yang boleh memakai bukti yang sudah tersimpan).
func (a *API) bacaMultipart(w http.ResponseWriter, r *http.Request, wajibBerkas bool) ([]payment.FileInput, func(), error) {
	batas := int64(defaultMaxRequestBytes)
	if a.d.Payments != nil {
		var err error
		batas, err = a.d.Payments.MaxRequestBytes(r.Context())
		if err != nil {
			return nil, func() {}, mapDomainErr(err)
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, batas)
	if err := r.ParseMultipartForm(memoryBerkas); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, func() {}, apierr.New(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE",
				"Total ukuran berkas bukti melebihi batas unggahan.")
		}
		return nil, func() {}, apierr.Validation("Permintaan unggah tidak valid.",
			map[string]string{payment.FieldBerkas: pesanMultipart(err)})
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	headers := r.MultipartForm.File[payment.FieldBerkas]
	if len(headers) == 0 {
		if wajibBerkas {
			return nil, func() {}, mapDomainErr(payment.ErrNoFile)
		}
		return nil, func() {}, nil
	}
	files := make([]payment.FileInput, 0, len(headers))
	tebuka := make([]multipart.File, 0, len(headers))
	for _, h := range headers {
		f, err := h.Open()
		if err != nil {
			tutupSemua(tebuka)
			return nil, func() {}, apierr.Validation("Berkas bukti tidak dapat dibaca.",
				map[string]string{payment.FieldBerkas: err.Error()})
		}
		tebuka = append(tebuka, f)
		files = append(files, payment.FileInput{NamaFile: h.Filename, Content: f, UkuranHint: h.Size})
	}
	return files, func() { tutupSemua(tebuka) }, nil
}

func tutupSemua(fs []multipart.File) {
	for _, f := range fs {
		_ = f.Close()
	}
}

// pesanMultipart menerjemahkan kesalahan penguraian multipart menjadi pesan
// yang bisa ditampilkan UI.
func pesanMultipart(err error) string {
	switch {
	case errors.Is(err, http.ErrNotMultipart), errors.Is(err, http.ErrMissingBoundary):
		return "permintaan harus berupa multipart/form-data"
	case errors.Is(err, multipart.ErrMessageTooLarge):
		return "berkas terlalu besar"
	default:
		return fmt.Sprintf("tidak dapat diurai (%v)", err)
	}
}

// daftarBukti menyajikan seluruh bukti satu pesanan.
func (a *API) daftarBukti(w http.ResponseWriter, r *http.Request) error {
	if a.d.Payments == nil {
		return proofsUnavailable()
	}
	list, err := a.d.Payments.List(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(list))
	return nil
}

// tautanBukti membuat tautan unduh berumur pendek untuk satu bukti.
func (a *API) tautanBukti(w http.ResponseWriter, r *http.Request) error {
	if a.d.Payments == nil {
		return proofsUnavailable()
	}
	dl, err := a.d.Payments.DownloadURL(r.Context(), auth.ActorFrom(r.Context()),
		chi.URLParam(r, "id"), chi.URLParam(r, "proofId"))
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(dl))
	return nil
}

// orderProofUpload menambah bukti transfer pada pesanan cabang sesi
// (PRD CP-ORD-07: cicilan/pembayaran terpisah).
func (a *API) orderProofUpload(w http.ResponseWriter, r *http.Request) error {
	return a.unggahBukti(w, r)
}

// orderProofs mengembalikan bukti milik satu pesanan cabang sesi.
func (a *API) orderProofs(w http.ResponseWriter, r *http.Request) error {
	return a.daftarBukti(w, r)
}

// orderProofDownload mengembalikan presigned URL unduh satu bukti. Kunci
// storage tidak pernah dikirim; umurnya pendek (Architecture.md §6).
func (a *API) orderProofDownload(w http.ResponseWriter, r *http.Request) error {
	return a.tautanBukti(w, r)
}

// adminOrderProofUpload mengunggah bukti ATAS NAMA cabang (PRD AP-ORD-06);
// pengunggahnya ditandai nama admin dan tercatat di timeline.
func (a *API) adminOrderProofUpload(w http.ResponseWriter, r *http.Request) error {
	return a.unggahBukti(w, r)
}

// adminOrderProofs menyajikan bukti satu pesanan untuk portal admin.
func (a *API) adminOrderProofs(w http.ResponseWriter, r *http.Request) error {
	return a.daftarBukti(w, r)
}

// adminOrderProofDownload mengembalikan tautan unduh bukti untuk admin.
func (a *API) adminOrderProofDownload(w http.ResponseWriter, r *http.Request) error {
	return a.tautanBukti(w, r)
}

// buktiKodeValidasi memetakan kode error domain bukti ke bentuk HTTP.
// Dipakai mapDomainErr (errors.go).
func buktiKodeValidasi(err error) (int, string, string, map[string]string) {
	pesan := err.Error()
	if i := strings.Index(pesan, ":"); i >= 0 {
		pesan = strings.TrimSpace(pesan[i+1:])
	}
	switch {
	case errors.Is(err, payment.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE",
			"Ukuran berkas bukti melebihi batas.", map[string]string{payment.FieldBerkas: pesan}
	case errors.Is(err, payment.ErrTooMany):
		return http.StatusConflict, "PROOF_LIMIT",
			"Jumlah bukti pada pesanan ini sudah mencapai batas.", nil
	case errors.Is(err, payment.ErrDuplicate):
		return http.StatusConflict, "PROOF_DUPLICATE",
			"Berkas bukti yang sama sudah pernah diunggah pada pesanan ini.", nil
	case errors.Is(err, payment.ErrUnsupported), errors.Is(err, payment.ErrFakeExt), errors.Is(err, payment.ErrNoFile):
		return http.StatusBadRequest, apierr.CodeValidationFailed,
			"Berkas bukti tidak valid.", map[string]string{payment.FieldBerkas: pesan}
	default:
		return 0, "", "", nil
	}
}
