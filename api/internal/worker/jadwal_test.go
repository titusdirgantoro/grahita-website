package worker_test

// Uji jadwal pengingat C&C 07:00 WIB (F2-23, SystemDesign.md §4.6).

import (
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/worker"
)

func TestJadwalHarianWIBNextSelaluTujuhPagiWIB(t *testing.T) {
	j := worker.JadwalHarianWIB{Jam: 7, Menit: 0}
	for _, tc := range []struct {
		nama    string
		current time.Time
		mauWIB  time.Time
	}{
		{
			nama:    "sebelum jam 07:00 WIB -> hari yang sama",
			current: time.Date(2026, 10, 7, 6, 0, 0, 0, clock.Jakarta()),
			mauWIB:  time.Date(2026, 10, 7, 7, 0, 0, 0, clock.Jakarta()),
		},
		{
			nama:    "tepat 07:00 WIB -> besok (harus SETELAH current)",
			current: time.Date(2026, 10, 7, 7, 0, 0, 0, clock.Jakarta()),
			mauWIB:  time.Date(2026, 10, 8, 7, 0, 0, 0, clock.Jakarta()),
		},
		{
			nama:    "jam server UTC (06:00 WIB) tetap memakai jam dinding WIB",
			current: time.Date(2026, 10, 6, 23, 0, 0, 0, time.UTC), // 2026-10-07 06:00 WIB
			mauWIB:  time.Date(2026, 10, 7, 7, 0, 0, 0, clock.Jakarta()),
		},
		{
			nama:    "23:00 WIB -> besok 07:00 WIB",
			current: time.Date(2026, 10, 7, 23, 0, 0, 0, clock.Jakarta()),
			mauWIB:  time.Date(2026, 10, 8, 7, 0, 0, 0, clock.Jakarta()),
		},
	} {
		t.Run(tc.nama, func(t *testing.T) {
			dapat := j.Next(tc.current)
			if !dapat.Equal(tc.mauWIB) {
				t.Fatalf("Next = %s (WIB %s), mau %s",
					dapat.UTC(), dapat.In(clock.Jakarta()), tc.mauWIB)
			}
			if !dapat.After(tc.current) {
				t.Errorf("Next harus SETELAH current, dapat %s", dapat)
			}
			if jam := dapat.In(clock.Jakarta()).Hour(); jam != 7 {
				t.Errorf("jam dinding WIB harus 7, dapat %d", jam)
			}
		})
	}
}
