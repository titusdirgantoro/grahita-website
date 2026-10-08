// Package clock menyediakan sumber waktu yang bisa disuntikkan.
//
// Seluruh logika bisnis (penomoran, jatuh tempo C&C, reminder) memakai
// interface ini supaya bisa diuji secara deterministik (SystemDesign.md §5.5).
package clock

import (
	"sync"
	"time"
)

// JakartaTZ adalah zona waktu bisnis. Diambil dari tzdata; bila tidak
// tersedia (image tanpa tzdata) dipakai offset tetap UTC+7.
var jakarta = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Jakarta"); err == nil {
		return loc
	}
	return time.FixedZone("WIB", 7*60*60)
}()

// Jakarta mengembalikan zona waktu bisnis (WIB, UTC+7).
func Jakarta() *time.Location { return jakarta }

// Clock adalah sumber waktu aplikasi.
type Clock interface {
	// Now mengembalikan waktu saat ini (UTC adalah konvensi penyimpanan).
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// System mengembalikan clock sungguhan.
func System() Clock { return systemClock{} }

type fixedClock struct {
	mu sync.RWMutex
	t  time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.t
}

// Set memindahkan waktu (hanya untuk uji).
func (c *fixedClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// Fixed membuat clock yang berhenti pada satu titik waktu.
func Fixed(t time.Time) *fixedClock { return &fixedClock{t: t.UTC()} }

// NowWIB mengembalikan waktu lokal WIB dari sebuah clock.
func NowWIB(c Clock) time.Time { return c.Now().In(jakarta) }

// TodayWIB mengembalikan tanggal bisnis (tengah malam WIB) dari sebuah clock.
func TodayWIB(c Clock) time.Time {
	now := c.Now().In(jakarta)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, jakarta)
}

// PeriodeBulan mengembalikan "YYYY-MM" dalam WIB — dipakai counter invoice.
func PeriodeBulan(t time.Time) string { return t.In(jakarta).Format("2006-01") }

// PeriodeHari mengembalikan "YYYYMMDD" dalam WIB — dipakai serial number.
func PeriodeHari(t time.Time) string { return t.In(jakarta).Format("20060102") }
