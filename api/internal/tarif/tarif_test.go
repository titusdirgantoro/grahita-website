package tarif

import "testing"

// TestFinalMembulatkanSekaliHalfUp mengunci contoh normatif
// master-impact-import.md §7: pembulatan sekali setelah ketiga komponen
// dijumlahkan, pecahan 0,5 menjauhi nol.
func TestFinalMembulatkanSekaliHalfUp(t *testing.T) {
	cases := []struct {
		hjm  int64
		p1   string
		spv  int64
		want int64
	}{
		{100000, "0.1", 2500, 112500},
		{10001, "0.05", 0, 10501},
		{10005, "0.1", 0, 11006},
		{34100, "", 2500, 36600},
	}
	for _, c := range cases {
		p1, err := ParseRasio(c.p1)
		if err != nil {
			t.Fatalf("ParseRasio(%q): %v", c.p1, err)
		}
		if got, ok := Final(c.hjm, p1, c.spv); !ok || got != c.want {
			t.Errorf("Final(%d, %s, %d) = %d %v, mau %d", c.hjm, c.p1, c.spv, got, ok, c.want)
		}
	}
	for _, salah := range []string{"1.5", "-0.1", "sepuluh"} {
		if _, err := ParseRasio(salah); err == nil {
			t.Errorf("ParseRasio(%q) harus ditolak", salah)
		}
	}
}
