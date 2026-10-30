package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"
)

// TestPGOPS12Daftar25Dan100CatatP95 mengukur endpoint daftar yang benar-benar
// melewati router, autentikasi, service, dan PostgreSQL. Angka ini adalah
// baseline QA dari mesin pengujian, bukan SLA produksi.
func TestPGOPS12Daftar25Dan100CatatP95(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	for i := 1; i <= 125; i++ {
		if _, err := f.pool.Exec(ctx, `
			INSERT INTO orders (branch_id, nomor, status, tanggal, total, submitted_at)
			VALUES ($1, $2, 'submitted', current_date, 100000, now())`,
			f.branchA, fmt.Sprintf("LOAD-%03d", i)); err != nil {
			t.Fatalf("seed pesanan %d: %v", i, err)
		}
	}

	cookies, csrf := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	for _, limit := range []int{25, 100} {
		var durations []time.Duration
		for run := 0; run < 30; run++ {
			start := time.Now()
			status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet,
				fmt.Sprintf("%s/api/v1/admin/orders?per_halaman=%d&halaman=1", f.srv.URL, limit),
				"", cookies, csrf)
			durations = append(durations, time.Since(start))
			if status != http.StatusOK {
				t.Fatalf("daftar %d run %d: HTTP %d (%v)", limit, run, status, body)
			}
			page := dataMap(t, body)
			items, _ := page["items"].([]any)
			if len(items) != limit || page["total"] != float64(125) {
				t.Fatalf("daftar %d run %d tidak lengkap: total=%v items=%d", limit, run, page["total"], len(items))
			}
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		p95 := durations[(len(durations)*95+99)/100-1]
		t.Logf("OPS-12 daftar %d baris: n=%d p50=%s p95=%s min=%s max=%s",
			limit, len(durations), durations[len(durations)/2], p95, durations[0], durations[len(durations)-1])
	}
}
