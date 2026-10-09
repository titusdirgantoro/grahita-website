package httpapi

import (
	"errors"
	"net/http"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/bank"
	"github.com/grahita/portal/api/internal/branch"
	"github.com/grahita/portal/api/internal/catalog"
	"github.com/grahita/portal/api/internal/dashboard"
	"github.com/grahita/portal/api/internal/document"
	"github.com/grahita/portal/api/internal/notification"
	"github.com/grahita/portal/api/internal/order"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/platform/apierr"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/registration"
	"github.com/grahita/portal/api/internal/report"
	"github.com/grahita/portal/api/internal/settings"
	"github.com/grahita/portal/api/internal/tutorial"
	"github.com/grahita/portal/api/internal/users"
)

// httpErr menuliskan error apa pun lewat bentuk seragam (dipakai middleware
// dan router yang tidak punya akses ke httpx.Fail).
func httpErr(w http.ResponseWriter, r *http.Request, err error) {
	httpx.Fail(w, r, err)
}

func notFoundErr() error {
	return apierr.NotFound("Endpoint tidak ditemukan.")
}

func methodNotAllowedErr() error {
	return apierr.New(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Metode HTTP tidak didukung untuk endpoint ini.")
}

// mapDomainErr menerjemahkan sentinel error domain ke bentuk HTTP.
// Semua sentinel dipetakan di satu tempat supaya kode status konsisten.
func mapDomainErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrInvalidCredentials):
		return apierr.Unauthenticated("Email atau kata sandi salah.")
	case errors.Is(err, auth.ErrInactiveAccount):
		return apierr.Forbidden("Akun Anda sedang tidak aktif. Hubungi admin Pusat.")
	case errors.Is(err, auth.ErrSessionInvalid):
		return apierr.Unauthenticated("Sesi Anda sudah berakhir. Silakan masuk kembali.")
	case errors.Is(err, auth.ErrResetInvalid):
		return apierr.New(http.StatusBadRequest, "RESET_INVALID",
			"Tautan atur ulang tidak valid, sudah dipakai, atau kedaluwarsa. Minta tautan baru.")
	case errors.Is(err, auth.ErrWrongPassword):
		return apierr.Validation("Kata sandi lama tidak cocok.",
			map[string]string{"password_lama": "kata sandi lama salah"})
	case errors.Is(err, auth.ErrPasswordTooWeak):
		return apierr.Validation("Kata sandi belum memenuhi syarat.",
			map[string]string{"password": err.Error()})
	case errors.Is(err, auth.ErrBranchForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke data cabang ini.")
	case errors.Is(err, auth.ErrProfileNotEditable):
		return apierr.Forbidden("Profil ini dikelola admin Pusat dan tidak dapat diubah sendiri.")
	case errors.Is(err, auth.ErrNamaInvalid):
		return apierr.Validation("Nama belum benar.", map[string]string{"nama": err.Error()})
	case errors.Is(err, settings.ErrNotFound), errors.Is(err, catalog.ErrNotFound):
		return apierr.NotFound("Data yang diminta tidak ditemukan.")
	case errors.Is(err, settings.ErrInvalidValue):
		var ve *settings.ValidationError
		if errors.As(err, &ve) {
			return apierr.Validation("Nilai pengaturan tidak valid.", ve.Fields)
		}
		return apierr.Validation("Nilai pengaturan tidak valid.", nil)
	case errors.Is(err, settings.ErrForbidden), errors.Is(err, catalog.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke tindakan ini.")
	// Konten tutorial cabang (F2-27): hanya Superadmin yang boleh mengubah
	// (`admin.tutorial`), dan seluruh konten divalidasi/disanitasi di layer
	// domain. Pesan per bagian dikembalikan supaya UI bisa menempelkannya.
	case errors.Is(err, tutorial.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses untuk mengubah konten tutorial.")
	case errors.Is(err, tutorial.ErrNotFound):
		return apierr.NotFound("Topik tutorial yang diminta tidak ditemukan.")
	case errors.Is(err, tutorial.ErrInvalidContent):
		var ve *tutorial.ValidationError
		if errors.As(err, &ve) {
			return apierr.Validation("Konten tutorial tidak valid.", ve.Fields)
		}
		return apierr.Validation("Konten tutorial tidak valid.", nil)
	// Audit log (F2-28): hanya Superadmin yang boleh membacanya, dan filter
	// yang tidak valid dijawab 400 supaya salah ketik tidak diam-diam
	// menghilangkan baris dari hasil.
	case errors.Is(err, audit.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke audit log.")
	case errors.Is(err, audit.ErrFilterInvalid):
		return apierr.Validation("Filter audit log tidak valid.",
			map[string]string{"filter": err.Error()})
	case errors.Is(err, catalog.ErrNoBranch):
		return apierr.New(http.StatusConflict, "BRANCH_MISSING", "Akun ini belum terhubung ke cabang mana pun.")
	case errors.Is(err, catalog.ErrKodeTaken):
		return apierr.Conflict("KODE_TAKEN", "Kode produk tersebut sudah dipakai produk lain.")
	case errors.Is(err, catalog.ErrProductUsed):
		return apierr.Conflict("PRODUCT_IN_USE",
			"Produk ini sudah dipakai pada pesanan sehingga tidak bisa dihapus; nonaktifkan saja produknya.")
	case errors.Is(err, catalog.ErrInvalidData):
		return apierr.Validation("Data produk atau harga tidak valid.",
			map[string]string{"katalog": err.Error()})
	case errors.Is(err, order.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke pesanan ini.")
	case errors.Is(err, order.ErrNotFound):
		return apierr.NotFound("Pesanan yang diminta tidak ditemukan.")
	case errors.Is(err, order.ErrLocked):
		return apierr.Conflict("ORDER_LOCKED",
			"Pesanan yang sudah dikirim tidak dapat diubah atau dihapus. Hubungi admin Pusat.")
	case errors.Is(err, order.ErrProductNotSold):
		return apierr.Validation("Produk tidak dijual ke cabang ini.",
			map[string]string{"lines": err.Error()})
	case errors.Is(err, order.ErrEmptyLines):
		return apierr.Validation("Pesanan belum punya baris produk.",
			map[string]string{"lines": "minimal satu baris produk"})
	case errors.Is(err, order.ErrNoBranch):
		return apierr.New(http.StatusConflict, "BRANCH_MISSING", "Akun ini belum terhubung ke cabang mana pun.")
	case errors.Is(err, order.ErrCNCNotAllowed):
		// Cabang tanpa flag Cash & Carry tidak boleh memakai C&C (CP-CNC-01).
		// Jawabannya 400 VALIDATION_FAILED seperti aturan domain lain, dengan
		// penjelasan yang bisa ditampilkan UI.
		return apierr.Validation("Cash & Carry tidak tersedia untuk cabang ini.",
			map[string]string{"is_cnc": "cabang ini tidak berflag Cash & Carry"})
	case errors.Is(err, order.ErrProofRequired):
		// Pesanan non-C&C wajib disertai bukti transfer (PRD CP-ORD-04).
		return apierr.Validation("Pesanan belum disertai bukti transfer.",
			map[string]string{"bukti": "minimal satu bukti transfer, kecuali pesanan Cash & Carry"})
	// Revisi invoice berversi (F2-25). Diperiksa SEBELUM ErrInvalidData karena
	// ErrAlasanWajib membungkusnya.
	case errors.Is(err, order.ErrAlasanWajib):
		return apierr.Validation("Alasan revisi wajib diisi.",
			map[string]string{"alasan": "alasan revisi wajib diisi"})
	case errors.Is(err, order.ErrNotDone):
		return apierr.Conflict("ORDER_NOT_DONE",
			"Revisi hanya untuk pesanan yang sudah selesai (Done). Selesaikan verifikasi pembayaran terlebih dahulu.")
	case errors.Is(err, order.ErrRevisiTidakDiizinkan):
		return apierr.Forbidden("Manager tidak diizinkan merevisi invoice saat ini (setelan manager_boleh_revisi nonaktif).")
	case errors.Is(err, order.ErrQueueUnavailable):
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable,
			"Penjadwal pekerjaan invoice belum siap sehingga revisi belum dapat diselesaikan.")
	case errors.Is(err, order.ErrInvoiceVersiBelumAda):
		// Fail closed (temuan QA putaran 1): invoice versi berjalan belum
		// dirender, jadi revisi belum boleh menaikkan versi — setiap revisi
		// yang dijawab 200 harus punya invoice sendiri.
		return apierr.Conflict("INVOICE_NOT_READY",
			"Invoice versi ini belum selesai dirender sehingga revisi belum dapat dilakukan. Coba lagi setelah proses dokumennya selesai.")
	case errors.Is(err, order.ErrInvalidData):
		return apierr.Validation("Data pesanan tidak valid.",
			map[string]string{"pesanan": err.Error()})
	case errors.Is(err, order.ErrAdminFilter):
		// Parameter daftar riwayat admin (F1-15) tidak valid.
		return apierr.Validation("Filter daftar pesanan tidak valid.",
			map[string]string{"filter": err.Error()})
	// Bukti transfer (F1-13). Tabel keputusan ada di handlers_proofs.go.
	case errors.Is(err, payment.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke bukti pesanan ini.")
	case errors.Is(err, payment.ErrNotFound):
		return apierr.NotFound("Pesanan atau bukti yang diminta tidak ditemukan.")
	case errors.Is(err, payment.ErrNoBranch):
		return apierr.New(http.StatusConflict, "BRANCH_MISSING", "Akun ini belum terhubung ke cabang mana pun.")
	case errors.Is(err, payment.ErrLocked):
		return apierr.Conflict("ORDER_LOCKED",
			"Pesanan yang sudah selesai tidak dapat ditambah bukti oleh cabang. Hubungi admin Pusat.")
	case errors.Is(err, payment.ErrStorageUnavailable):
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable,
			"Penyimpanan berkas belum siap sehingga bukti tidak dapat disimpan.")
	// Verifikasi pembayaran (F1-18). Keputusannya eksplisit: sudah selesai
	// berarti 409 tanpa efek samping, dan pesanan yang belum dikirim tidak
	// pernah bisa diselesaikan.
	case errors.Is(err, payment.ErrAlreadyDone):
		return apierr.Conflict("ORDER_ALREADY_DONE",
			"Pesanan ini sudah selesai diverifikasi. Tidak ada perubahan yang dilakukan.")
	case errors.Is(err, payment.ErrNotSubmitted):
		return apierr.Conflict("ORDER_NOT_SUBMITTED",
			"Verifikasi hanya untuk pesanan yang sudah dikirim. Pesanan draf belum punya nomor/tanggal untuk diselesaikan.")
	case errors.Is(err, payment.ErrAlasanInvalid), errors.Is(err, payment.ErrHasilInvalid),
		errors.Is(err, payment.ErrNominalInvalid), errors.Is(err, payment.ErrCatatanInvalid),
		errors.Is(err, payment.ErrMetodeInvalid), errors.Is(err, payment.ErrInvalidData):
		return apierr.Validation("Data verifikasi tidak valid.",
			map[string]string{"verifikasi": err.Error()})
	case errors.Is(err, payment.ErrQueueUnavailable), errors.Is(err, payment.ErrVerifierUnavailable):
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable,
			"Penjadwal pekerjaan dokumen belum siap sehingga verifikasi belum dapat diselesaikan.")
	case errors.Is(err, payment.ErrTooLarge), errors.Is(err, payment.ErrTooMany),
		errors.Is(err, payment.ErrDuplicate), errors.Is(err, payment.ErrUnsupported),
		errors.Is(err, payment.ErrFakeExt), errors.Is(err, payment.ErrNoFile):
		status, code, pesan, fields := buktiKodeValidasi(err)
		return apierr.New(status, code, pesan).WithFields(fields)
	case errors.Is(err, branch.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke master cabang.")
	case errors.Is(err, branch.ErrNotFound):
		return apierr.NotFound("Cabang yang diminta tidak ditemukan.")
	case errors.Is(err, branch.ErrEmailTaken):
		return apierr.Conflict("EMAIL_TAKEN", "Email tersebut sudah dipakai cabang atau akun admin lain.")
	case errors.Is(err, branch.ErrHasOrders):
		return apierr.Conflict("BRANCH_HAS_ORDERS",
			"Cabang ini sudah punya pesanan sehingga tidak bisa dihapus; nonaktifkan saja cabangnya.")
	case errors.Is(err, branch.ErrHasAccount):
		return apierr.Conflict("BRANCH_HAS_ACCOUNT",
			"Cabang ini sudah punya akun, jadi token registrasi tidak diperlukan lagi. Pemulihan akses lewat lupa kata sandi.")
	case errors.Is(err, branch.ErrInactive):
		return apierr.Conflict("BRANCH_INACTIVE",
			"Cabang sedang nonaktif; aktifkan dulu sebelum membuat token registrasi.")
	case errors.Is(err, branch.ErrInvalidData):
		return apierr.Validation("Data cabang tidak valid.", map[string]string{"cabang": err.Error()})
	case errors.Is(err, bank.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke rekening bank.")
	case errors.Is(err, bank.ErrNotFound):
		return apierr.NotFound("Rekening yang diminta tidak ditemukan.")
	case errors.Is(err, bank.ErrDuplicate):
		return apierr.Conflict("BANK_DUPLICATE", "Rekening dengan bank dan nomor tersebut sudah terdaftar.")
	case errors.Is(err, bank.ErrInvalidData):
		return apierr.Validation("Data rekening tidak valid.", map[string]string{"rekening": err.Error()})
	case errors.Is(err, users.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke tindakan ini.")
	case errors.Is(err, users.ErrNotFound):
		return apierr.NotFound("Akun yang diminta tidak ditemukan.")
	case errors.Is(err, users.ErrEmailTaken):
		return apierr.Conflict("EMAIL_TAKEN", "Email tersebut sudah dipakai akun lain.")
	case errors.Is(err, users.ErrLastSuperadmin):
		return apierr.Conflict("LAST_SUPERADMIN",
			"Superadmin aktif terakhir tidak boleh dinonaktifkan atau diturunkan menjadi Manager.")
	case errors.Is(err, users.ErrNoAccount):
		return apierr.Conflict("NO_ACCOUNT",
			"Cabang ini belum punya akun. Buat token registrasi lebih dahulu di halaman Cabang.")
	case errors.Is(err, users.ErrNotInvited):
		return apierr.Conflict("NOT_INVITED",
			"Akun admin ini bukan undangan yang menunggu aktivasi. Gunakan lupa kata sandi bila akunnya sudah aktif.")
	case errors.Is(err, users.ErrInvalidData):
		return apierr.Validation("Data pengguna tidak valid.", nil)
	// Email transaksional (F1-16). Kirim ulang adalah pemulihan: admin hanya
	// bisa memicu email yang memang pernah dibuat untuk pesanan itu.
	case errors.Is(err, notification.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke email pesanan.")
	case errors.Is(err, notification.ErrNotFound):
		return apierr.NotFound("Email yang diminta tidak ditemukan untuk pesanan ini.")
	case errors.Is(err, notification.ErrKindInvalid):
		return apierr.New(http.StatusBadRequest, "EMAIL_KIND_INVALID",
			"Jenis email tidak dikenal. Pilihan: invoice_cabang, invoice_admin, surat_jalan_admin.")
	case errors.Is(err, notification.ErrNoRecipient):
		return apierr.New(http.StatusConflict, "NO_RECIPIENT",
			"Tidak ada penerima yang berhak menerima email ini. Periksa status aktif admin dan flag notifikasinya.")
	case errors.Is(err, notification.ErrDocumentNotReady):
		return apierr.New(http.StatusConflict, "DOCUMENT_NOT_READY",
			"Dokumen pesanan belum selesai dirender sehingga email belum bisa dikirim. Coba lagi setelah proses dokumen selesai.")
	case errors.Is(err, notification.ErrAlreadyQueued):
		return apierr.Conflict("EMAIL_ALREADY_QUEUED",
			"Email jenis ini masih dalam antrean. Tunggu prosesnya selesai sebelum mengirim ulang.")
	case errors.Is(err, notification.ErrAttachmentUnavailable):
		return apierr.New(http.StatusConflict, "ATTACHMENT_UNAVAILABLE",
			"Lampiran dokumen tidak dapat dibaca. Hubungi admin Pusat.")
	case errors.Is(err, notification.ErrInvalidData):
		return apierr.Validation("Data email tidak valid.", map[string]string{"email": err.Error()})
	// Laporan & ekspor XLSX (F2-24). Otorisasi Superadmin ditegakkan router dan
	// service; unduhan hasil hanya boleh dari pekerjaan yang sudah selesai.
	case errors.Is(err, report.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke laporan ini.")
	case errors.Is(err, report.ErrNotFound):
		return apierr.NotFound("Laporan atau pekerjaan ekspor yang diminta tidak ditemukan.")
	case errors.Is(err, report.ErrFilterInvalid):
		return apierr.Validation("Filter laporan tidak valid.",
			map[string]string{"filter": err.Error()})
	case errors.Is(err, report.ErrJobNotReady):
		return apierr.Conflict("REPORT_NOT_READY",
			"Hasil ekspor belum selesai. Tunggu sampai pekerjaan berstatus selesai sebelum mengunduh.")
	case errors.Is(err, report.ErrStorageUnavailable), errors.Is(err, report.ErrQueueUnavailable):
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable,
			"Layanan ekspor laporan belum siap sehingga permintaan tidak dapat diproses.")
	case errors.Is(err, report.ErrDataInvalid):
		return apierr.New(http.StatusUnprocessableEntity, "REPORT_DATA_INVALID",
			"Data laporan tidak konsisten sehingga ekspor tidak dapat dilanjutkan.")
	case errors.Is(err, document.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke dokumen pesanan ini.")
	case errors.Is(err, document.ErrNotFound):
		return apierr.NotFound("Pesanan atau dokumen yang diminta tidak ditemukan.")
	case errors.Is(err, document.ErrNotDone):
		return apierr.Conflict("ORDER_NOT_DONE",
			"Dokumen hanya dibuat untuk pesanan yang sudah selesai (Done). Selesaikan verifikasi pembayaran terlebih dahulu.")
	case errors.Is(err, document.ErrRenderUnavailable):
		return apierr.New(http.StatusServiceUnavailable, "DOCUMENT_RENDER_UNAVAILABLE",
			"Mesin pembuat PDF belum siap sehingga invoice/surat jalan belum bisa dibuat. Coba lagi beberapa saat lagi.")
	case errors.Is(err, document.ErrStorageUnavailable):
		return apierr.New(http.StatusServiceUnavailable, apierr.CodeUnavailable,
			"Penyimpanan dokumen belum siap sehingga dokumen tidak dapat disimpan.")
	case errors.Is(err, document.ErrInvalidData):
		return apierr.Validation("Data pesanan belum cukup untuk membuat dokumen.",
			map[string]string{"dokumen": err.Error()})
	// Riwayat invoice cabang (F1-19): filter periode yang tidak terbaca
	// dijawab 400, bukan daftar kosong; akun tanpa cabang dijawab 409 sama
	// seperti domain lain.
	case errors.Is(err, dashboard.ErrForbidden):
		return apierr.Forbidden("Anda tidak memiliki akses ke dashboard admin.")
	case errors.Is(err, dashboard.ErrFilterInvalid):
		// Filter dashboard (F1-20) yang tidak dikenal ditolak 400 supaya salah
		// ketik tidak diam-diam menghilangkan filter.
		return apierr.Validation("Filter dashboard tidak valid.",
			map[string]string{"filter": err.Error()})
	case errors.Is(err, document.ErrFilterInvalid):
		return apierr.Validation("Filter periode tidak valid.",
			map[string]string{"periode": err.Error()})
	case errors.Is(err, document.ErrNoBranch):
		return apierr.New(http.StatusConflict, "BRANCH_MISSING", "Akun ini belum terhubung ke cabang mana pun.")
	// Pendaftaran akun cabang lewat token (F1-07). Pesan sengaja spesifik per
	// keadaan supaya pengguna tahu harus berbuat apa (PRD CP-REG-07).
	case errors.Is(err, registration.ErrTokenInvalid):
		return apierr.New(http.StatusBadRequest, "TOKEN_INVALID",
			"Token registrasi tidak valid. Periksa kembali penulisannya, atau minta token baru ke admin Pusat.")
	case errors.Is(err, registration.ErrTokenExpired):
		return apierr.New(http.StatusBadRequest, "TOKEN_EXPIRED",
			"Token registrasi sudah kedaluwarsa. Minta token baru ke admin Pusat.")
	case errors.Is(err, registration.ErrTokenUsed):
		return apierr.Conflict("TOKEN_USED",
			"Token registrasi sudah dipakai. Satu cabang hanya boleh punya satu akun; gunakan lupa kata sandi bila perlu masuk kembali.")
	case errors.Is(err, registration.ErrBranchInactive):
		return apierr.Conflict("BRANCH_INACTIVE",
			"Cabang ini sedang nonaktif. Hubungi admin Pusat untuk mengaktifkannya kembali.")
	case errors.Is(err, registration.ErrBranchHasAccount):
		return apierr.Conflict("BRANCH_HAS_ACCOUNT",
			"Cabang ini sudah punya akun. Gunakan lupa kata sandi bila perlu masuk kembali.")
	case errors.Is(err, registration.ErrEmailTaken):
		return apierr.Conflict("EMAIL_TAKEN",
			"Email cabang sudah dipakai akun lain. Hubungi admin Pusat.")
	case errors.Is(err, registration.ErrTeleponInvalid):
		return apierr.Validation("Nomor telepon belum benar.",
			map[string]string{"telepon": err.Error()})
	case errors.Is(err, registration.ErrPasswordMismatch):
		return apierr.Validation("Konfirmasi kata sandi tidak sama.",
			map[string]string{"password_konfirmasi": "konfirmasi kata sandi harus sama dengan kata sandi"})
	default:
		return err
	}
}
