package receivable

import (
	"testing"
	"time"
)

func TestDueDateMemakaiHariSubmitWIBdanMelewatiBatasTahun(t *testing.T) {
	for _, tc := range []struct {
		name, submitted, approved, due string
		payDay                         int
	}{
		{"akhir_bulan_sebelum_tengah_malam", "2026-10-31T16:59:59Z", "2026-11-02T03:00:00Z", "2026-11-05", 5},
		{"awal_bulan_setelah_tengah_malam", "2026-10-31T17:00:00Z", "2026-11-02T03:00:00Z", "2026-12-05", 5},
		{"approval_setelah_jatuh_tempo", "2026-10-31T16:59:59Z", "2026-11-05T17:00:00Z", "2026-12-05", 5},
		{"tahun_baru", "2026-12-31T16:59:59Z", "2027-01-02T03:00:00Z", "2027-01-05", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			submitted, err := time.Parse(time.RFC3339, tc.submitted)
			if err != nil {
				t.Fatal(err)
			}
			approved, err := time.Parse(time.RFC3339, tc.approved)
			if err != nil {
				t.Fatal(err)
			}
			if got := DueDate(submitted, approved, tc.payDay).Format("2006-01-02"); got != tc.due {
				t.Fatalf("jatuh tempo = %s, mau %s", got, tc.due)
			}
		})
	}
}

func TestGraceEndDuaHariKerjaTanpaMemperpanjangAkhirPekan(t *testing.T) {
	for _, tc := range []struct{ start, end string }{
		{"2026-10-30", "2026-11-03"}, // Jumat → Selasa.
		{"2026-11-02", "2026-11-04"}, // Senin → Rabu.
		{"2026-11-07", "2026-11-10"}, // Sabtu → Selasa.
	} {
		start, err := time.ParseInLocation("2006-01-02", tc.start, jakarta)
		if err != nil {
			t.Fatal(err)
		}
		if got := GraceEnd(start).Format("2006-01-02"); got != tc.end {
			t.Errorf("mulai %s: grace berakhir %s, mau %s", tc.start, got, tc.end)
		}
	}
}
