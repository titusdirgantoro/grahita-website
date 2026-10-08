package notification

import "time"

// MaxRetry adalah jumlah percobaan ULANG maksimum untuk satu email.
//
// PRD EM-01 dan SystemDesign.md §5.1 menulisnya sebagai "retry berjenjang
// (1m, 5m, 30m, 2j, 6j) ... maks 5 kali": lima jeda berarti lima percobaan
// ulang SETELAH percobaan pertama.
const MaxRetry = 5

// MaxAttempts adalah jumlah percobaan KIRIM maksimum untuk satu email: satu
// percobaan pertama + MaxRetry percobaan ulang. Dengan begitu seluruh jeda
// 1m, 5m, 30m, 2j, 6j benar-benar terpakai sebelum email dinyatakan `failed`.
const MaxAttempts = MaxRetry + 1

// DefaultLease adalah masa sewa satu klaim email. Baris berstatus `sending`
// yang proses pengirimnya mati di tengah jalan menjadi bisa diklaim ulang
// setelah sewa ini habis — tanpa ini, email bisa tersangkut selamanya.
const DefaultLease = 15 * time.Minute

// RetrySchedule adalah jeda berjenjang antar percobaan: 1m, 5m, 30m, 2j, 6j
// (SystemDesign.md §5.1). Indeks 0 adalah jeda SETELAH percobaan pertama gagal.
var RetrySchedule = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
}

// RetryDelay mengembalikan jeda sebelum percobaan berikutnya setelah
// `attempt` percobaan gagal (attempt >= 1). Bila percobaan sudah mencapai
// batasnya, jeda terakhir yang dikembalikan — pemanggil yang memutuskan
// menyerah, bukan fungsi ini.
func RetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(RetrySchedule) {
		attempt = len(RetrySchedule)
	}
	return RetrySchedule[attempt-1]
}
