package notification

import (
	"context"
	"fmt"
	"strings"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/users"
)

// docKindsFor mengembalikan daftar doc_kind yang WAJIB sudah ada sebelum email
// dokumen jenis tersebut boleh diantre (Database.md §3, PRD §6.2).
//
// Urutannya menentukan urutan lampiran pada email.
func docKindsFor(jenis string) []string {
	switch jenis {
	case JenisInvoiceCabang, JenisInvoiceAdmin:
		return []string{"invoice_pdf"}
	case JenisSuratJalanAdmin:
		return []string{"surat_jalan_pdf", "surat_jalan_xlsx"}
	default:
		return nil
	}
}

// namaLampiran menyusun nama berkas lampiran dari jenis dokumen + nomor pesanan.
func namaLampiran(docKind, nomor string) string {
	nomor = strings.TrimSpace(nomor)
	switch docKind {
	case "invoice_pdf":
		return "Invoice-" + nomor + ".pdf"
	case "surat_jalan_pdf":
		return "Surat-Jalan-" + nomor + ".pdf"
	case "surat_jalan_xlsx":
		return "Surat-Jalan-" + nomor + ".xlsx"
	default:
		return nomor + "-" + docKind
	}
}

func contentTypeDokumen(docKind, dariDB string) string {
	if dariDB != "" {
		return dariDB
	}
	switch docKind {
	case "invoice_pdf", "surat_jalan_pdf":
		return "application/pdf"
	case "surat_jalan_xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		return "application/octet-stream"
	}
}

// OrderEmailInput adalah permintaan mengantre email dokumen untuk satu pesanan.
type OrderEmailInput struct {
	OrderID string
	Jenis   string
	// Versi dokumen yang dipakai; 0 berarti versi pesanan saat ini.
	Versi int
}

// buildOrderEnvelope menyusun email dokumen lengkap dengan penerima yang
// dihitung ULANG dari keadaan sekarang dan lampiran dokumen yang sudah ada.
//
// Dipakai bersama oleh pengantrean pertama (F1-18) dan kirim ulang admin,
// sehingga aturan penerima dan "menunggu render selesai" tidak pernah berbeda
// antar jalur.
func (s *Service) buildOrderEnvelope(ctx context.Context, in OrderEmailInput) (Envelope, OrderRef, error) {
	if !jenisDokumen[in.Jenis] {
		return Envelope{}, OrderRef{}, fmt.Errorf("%w: %q bukan email dokumen", ErrKindInvalid, in.Jenis)
	}
	orderID := strings.TrimSpace(in.OrderID)
	if !uuidRe.MatchString(orderID) {
		// Id pesanan selalu UUID (uuidv7); bentuk lain dijawab 404, bukan 500
		// dari SQLSTATE 22P02 saat query PostgreSQL (temuan QA F1-15).
		return Envelope{}, OrderRef{}, fmt.Errorf("%w: id pesanan %q bukan UUID", ErrNotFound, in.OrderID)
	}
	if s.orders == nil {
		return Envelope{}, OrderRef{}, fmt.Errorf("%w: pembaca pesanan belum terpasang", ErrInvalidData)
	}
	ref, err := s.orders.OrderByID(ctx, orderID)
	if err != nil {
		return Envelope{}, OrderRef{}, err
	}
	versi := in.Versi
	if versi <= 0 {
		versi = ref.Versi
	}

	docs, err := s.orders.Documents(ctx, ref.ID)
	if err != nil {
		return Envelope{}, OrderRef{}, err
	}
	perJenis := map[string]Dokumen{}
	for _, d := range docs {
		// Dokumen versi lain diabaikan: email harus memakai berkas versi yang
		// diminta, bukan sisa revisi sebelumnya.
		if d.Jenis == "" || d.Versi != versi {
			continue
		}
		perJenis[d.Jenis] = d
	}

	lampiran := make([]Lampiran, 0, 2)
	for _, kind := range docKindsFor(in.Jenis) {
		d, ok := perJenis[kind]
		if !ok || d.StorageKey == "" {
			return Envelope{}, OrderRef{}, fmt.Errorf("%w: %s belum tersedia untuk pesanan %s",
				ErrDocumentNotReady, kind, ref.Nomor)
		}
		lampiran = append(lampiran, Lampiran{
			StorageKey:  d.StorageKey,
			Nama:        namaLampiran(kind, ref.Nomor),
			ContentType: contentTypeDokumen(kind, d.ContentType),
		})
	}

	to, err := s.orderRecipients(ctx, in.Jenis, ref)
	if err != nil {
		return Envelope{}, OrderRef{}, err
	}

	payload := map[string]any{
		"template": in.Jenis,
		"nomor":    ref.Nomor,
		"cabang":   ref.Cabang,
		"total":    ref.Total,
		"versi":    versi,
		"lampiran": lampiran,
	}
	if !ref.Tanggal.IsZero() {
		payload["tanggal"] = tanggalID(ref.Tanggal)
	}
	if ref.JatuhTempo != nil {
		payload["jatuh_tempo"] = tanggalID(*ref.JatuhTempo)
	}
	if in.Jenis == JenisInvoiceCabang {
		payload["nama"] = ref.NamaKepala
	}

	return Envelope{
		OrderID: ref.ID,
		Jenis:   in.Jenis,
		To:      to,
		Subject: subjectDokumen(in.Jenis, ref, versi),
		Payload: payload,
	}, ref, nil
}

// orderRecipients menghitung penerima satu email dokumen: cabang memakai email
// akun cabang, sedangkan email Pusat memakai flag per admin — admin undangan
// atau nonaktif TIDAK pernah masuk daftar ini, dan tidak ada alamat tambahan
// di luar flag admin (PRD §6.2, AP-USR-01/02, §8 no.5).
func (s *Service) orderRecipients(ctx context.Context, jenis string, ref OrderRef) ([]string, error) {
	if jenis == JenisInvoiceCabang {
		if strings.TrimSpace(ref.CabangEmail) == "" {
			return nil, ErrNoRecipient
		}
		return NormalizeAddress([]string{ref.CabangEmail}), nil
	}
	if s.admins == nil {
		return nil, fmt.Errorf("%w: sumber penerima admin belum terpasang", ErrInvalidData)
	}
	var docKind users.DocKind
	switch jenis {
	case JenisInvoiceAdmin:
		docKind = users.DocInvoice
	case JenisSuratJalanAdmin:
		docKind = users.DocSuratJalan
	default:
		return nil, fmt.Errorf("%w: %q bukan email dokumen", ErrKindInvalid, jenis)
	}
	emails, err := s.admins.DocumentRecipients(ctx, docKind)
	if err != nil {
		return nil, err
	}
	out := NormalizeAddress(emails)
	if len(out) == 0 {
		return nil, ErrNoRecipient
	}
	return out, nil
}

// subjectDokumen menyusun subjek email dokumen. `versi` adalah versi dokumen
// yang benar-benar dilampirkan (bukan selalu versi pesanan berjalan), supaya
// email yang dipatok ke satu versi tidak menyebut versi yang lain.
func subjectDokumen(jenis string, ref OrderRef, versi int) string {
	switch jenis {
	case JenisInvoiceCabang, JenisInvoiceAdmin:
		subjek := "Invoice " + ref.Nomor
		if ref.Cabang != "" {
			subjek += " — " + ref.Cabang
		}
		if versi > 0 {
			subjek += fmt.Sprintf(" (revisi v%d)", versi)
		}
		return subjek
	case JenisSuratJalanAdmin:
		subjek := "Surat Jalan " + ref.Nomor
		if ref.Cabang != "" {
			subjek += " — " + ref.Cabang
		}
		return subjek
	default:
		return DefaultSubject(jenis)
	}
}

// EnqueueOrderEmail mengantre email dokumen satu pesanan. Ia menolak email
// yang dokumennya belum selesai dirender (ErrDocumentNotReady) maupun yang
// penerimanya tidak berhak (ErrNoRecipient).
//
// Dipakai domain pesanan/dokumen (F1-18, F2-25) di dalam transaksi bisnisnya;
// jalur ini TIDAK boleh dipakai untuk mengirim email dari handler HTTP.
func (s *Service) EnqueueOrderEmail(ctx context.Context, in OrderEmailInput) (Email, error) {
	env, _, err := s.buildOrderEnvelope(ctx, in)
	if err != nil {
		return Email{}, err
	}
	return s.Enqueue(ctx, env)
}

// ListByOrder mengembalikan riwayat email satu pesanan (PRD EM-02).
//
// Id yang bukan UUID dijawab ErrNotFound (404), bukan 500 dari SQLSTATE 22P02
// (temuan QA F1-15 pada GET /api/v1/admin/orders/{id}/emails).
func (s *Service) ListByOrder(ctx context.Context, actor auth.Actor, orderID string) ([]Email, error) {
	if !actor.Can(auth.CapAdminOrders) {
		return nil, ErrForbidden
	}
	orderID = strings.TrimSpace(orderID)
	if !uuidRe.MatchString(orderID) {
		return nil, ErrNotFound
	}
	rows, err := s.store.ByOrder(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("riwayat email pesanan: %w", err)
	}
	return rows, nil
}

// ResendOrder mengirim ulang email dokumen satu pesanan atas permintaan admin
// (POST /api/v1/admin/orders/{id}/emails/{kind}/resend, SystemDesign.md §6).
//
// Sifatnya:
//   - hanya untuk email yang MEMANG pernah dibuat untuk pesanan itu
//     (ErrNotFound bila belum pernah) — admin tidak bisa memakai endpoint ini
//     untuk memicu email baru ke alamat sembarang;
//   - penerima dan lampiran dihitung ULANG dari keadaan sekarang, sehingga
//     admin nonaktif/undangan tidak ikut serta dan dokumen harus sudah ada;
//   - ditolak dengan ErrAlreadyQueued bila masih ada email jenis yang sama
//     dalam antrean, sehingga klik ganda tidak menggandakan email;
//   - baris outbox baru + entri audit ditulis dalam SATU transaksi.
func (s *Service) ResendOrder(ctx context.Context, actor auth.Actor, orderID, jenis string) (Email, error) {
	if !actor.Can(auth.CapAdminOrders) {
		return Email{}, ErrForbidden
	}
	orderID = strings.TrimSpace(orderID)
	jenis = strings.TrimSpace(jenis)
	if orderID == "" || !jenisDokumen[jenis] {
		return Email{}, fmt.Errorf("%w: %q", ErrKindInvalid, jenis)
	}
	if !uuidRe.MatchString(orderID) {
		// Bentuk id yang bukan UUID dijawab 404 supaya tidak menjadi 500
		// (SQLSTATE 22P02) — sama seperti paket order (temuan QA F1-15).
		return Email{}, ErrNotFound
	}

	// Harus ada email sebelumnya: kirim ulang adalah pemulihan, bukan cara
	// membuat email baru.
	if _, err := s.store.Latest(ctx, orderID, jenis); err != nil {
		return Email{}, err
	}
	// Cek cepat (non-otoritatif) supaya permintaan yang jelas masih antre tidak
	// perlu membangun lampiran. Yang mengikat adalah penjaga atomik di dalam
	// store.Resend, yang memeriksa ulang di transaksi yang sama.
	aktif, err := s.store.ActiveExists(ctx, orderID, jenis)
	if err != nil {
		return Email{}, err
	}
	if aktif {
		return Email{}, ErrAlreadyQueued
	}

	env, ref, err := s.buildOrderEnvelope(ctx, OrderEmailInput{OrderID: orderID, Jenis: jenis})
	if err != nil {
		return Email{}, err
	}
	rec := audit.NewEntry(ctx, actor, audit.EntityEmail, "", audit.ActionResend,
		map[string]any{"order_id": orderID, "jenis": jenis},
		map[string]any{
			"order_id":        orderID,
			"jenis":           jenis,
			"nomor":           ref.Nomor,
			"jumlah_penerima": len(env.To),
			"jumlah_lampiran": jumlahLampiran(env.Payload),
		})
	return s.store.Resend(ctx, env, rec)
}

// jumlahLampiran menghitung lampiran payload tanpa panik bila bentuknya lain.
func jumlahLampiran(payload map[string]any) int {
	lampiran, err := PayloadAttachments(payload)
	if err != nil {
		return 0
	}
	return len(lampiran)
}
