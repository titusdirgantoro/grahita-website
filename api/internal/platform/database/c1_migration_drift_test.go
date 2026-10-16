package database_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/platform/database"
)

func TestMigrasiC1MenolakDriftCheckSebelumMengubahSkema(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := database.Up(ctx, url); err != nil {
		t.Fatalf("siapkan seluruh migrasi: %v", err)
	}
	if err := database.DownTo(ctx, url, 11); err != nil {
		t.Fatalf("turun ke 0011: %v", err)
	}
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `ALTER TABLE orders DROP CONSTRAINT done_punya_waktu;
ALTER TABLE orders ADD CONSTRAINT done_punya_waktu CHECK (total >= 0)`); err != nil {
		t.Fatalf("pasang drift: %v", err)
	}
	err = database.Up(ctx, url)
	if err == nil || !strings.Contains(err.Error(), "ekspresi asing") {
		t.Fatalf("migrasi drift err=%v", err)
	}
	var wrongStillThere, depositTable bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM pg_constraint WHERE conrelid='orders'::regclass
AND conname='done_punya_waktu' AND pg_get_expr(conbin,conrelid,false) LIKE '%total%')`).Scan(&wrongStillThere); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('branch_deposit_transactions') IS NOT NULL`).Scan(&depositTable); err != nil {
		t.Fatal(err)
	}
	if !wrongStillThere || depositTable {
		t.Fatalf("rollback drift tidak utuh: wrong=%v deposit_table=%v", wrongStillThere, depositTable)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE orders DROP CONSTRAINT done_punya_waktu;
ALTER TABLE orders ADD CONSTRAINT done_punya_waktu CHECK (status <> 'done' OR done_at IS NOT NULL)`); err != nil {
		t.Fatalf("pulihkan baseline: %v", err)
	}
	if err := database.Up(ctx, url); err != nil {
		t.Fatalf("migrasi setelah pemulihan: %v", err)
	}
}
