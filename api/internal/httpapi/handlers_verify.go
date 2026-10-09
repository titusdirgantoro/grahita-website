package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
)

// verifyUnavailable adalah jawaban bila layanan verifikasi belum terpasang.
func verifyUnavailable() error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable, "Layanan verifikasi pembayaran belum tersedia.")
}

// verifyRequest adalah body POST /api/v1/admin/orders/{id}/verify
// (PRD §4.2 AP-ORD-07, §5).
//
// `metode` SENGAJA tidak diterima dari klien: endpoint ini adalah verifikasi
// MANUAL, jadi servernya menetapkan `manual`. Jalur AI (Fase 2) memanggil
// service yang sama dengan metode `ai` dari worker OCR, bukan lewat endpoint ini.
type verifyRequest struct {
	// Hasil: "cocok" (setujui → Done) atau "perlu_review" (tandai review).
	Hasil string `json:"hasil"`
	// TotalTerbaca adalah nominal yang dibaca admin dari bukti (atau diisi
	// manual untuk bukti non-OCR). Kosong berarti tidak ada nominal terbaca.
	TotalTerbaca *int64 `json:"total_terbaca"`
	// Alasan wajib bila Hasil = perlu_review: kurang_bayar, lebih_bayar,
	// tidak_terbaca, rekening_beda, lainnya.
	Alasan string `json:"alasan"`
	// Catatan bebas opsional.
	Catatan string `json:"catatan"`
}

// adminOrderVerify memverifikasi pembayaran satu pesanan: menyetujui
// (Submitted → Done, lengkap dengan dokumen + email) atau menandainya perlu
// review dengan alasan (pesanan TETAP Submitted).
//
// Otorisasi ditegakkan DUA lapis dengan kapabilitas yang sama: rute dijaga
// requireCap(CapAdminVerify) dan layer service memeriksa `admin.verify`
// (Manager maupun Superadmin, PRD §2.1); akun cabang ditolak, dan id yang
// bukan UUID dijawab 404.
func (a *API) adminOrderVerify(w http.ResponseWriter, r *http.Request) error {
	if a.d.Verify == nil {
		return verifyUnavailable()
	}
	var req verifyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	out, err := a.d.Verify.MarkVerified(r.Context(), auth.ActorFrom(r.Context()), chi.URLParam(r, "id"), payment.VerifyInput{
		Hasil:        req.Hasil,
		Metode:       payment.MetodeManual,
		TotalTerbaca: req.TotalTerbaca,
		Alasan:       req.Alasan,
		Catatan:      req.Catatan,
	})
	if err != nil {
		return mapDomainErr(err)
	}
	httpx.OK(w, data(out))
	return nil
}
