package httpapi

// Uji integrasi autentikasi pada PostgreSQL nyata.
//
// Berbeda dari router_auth_test.go yang memakai penyimpanan in-memory,
// berkas ini menjalankan migrasi sungguhan lalu menggerakkan router lengkap
// dengan repository asli (auth/postgres, settings/postgres, catalog/postgres)
// sehingga perilaku SQL — token sekali pakai, pencabutan sesi, filter
// branch_id, dan aturan boleh_login — benar-benar dieksekusi.
//
// Uji dilewati bila TEST_DATABASE_URL tidak diisi (CI "migrasi" yang
// menyediakannya). Database yang dipakai HARUS khusus untuk pengujian:
// seluruh tabel domain dikosongkan sebelum tiap uji.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grahita/portal/api/internal/auth"
	authpg "github.com/grahita/portal/api/internal/auth/postgres"
	"github.com/grahita/portal/api/internal/bank"
	bankpg "github.com/grahita/portal/api/internal/bank/postgres"
	"github.com/grahita/portal/api/internal/branch"
	branchpg "github.com/grahita/portal/api/internal/branch/postgres"
	"github.com/grahita/portal/api/internal/catalog"
	catalogpg "github.com/grahita/portal/api/internal/catalog/postgres"
	"github.com/grahita/portal/api/internal/dashboard"
	dashboardpg "github.com/grahita/portal/api/internal/dashboard/postgres"
	"github.com/grahita/portal/api/internal/document"
	documentpg "github.com/grahita/portal/api/internal/document/postgres"
	"github.com/grahita/portal/api/internal/fulfillment"
	"github.com/grahita/portal/api/internal/notification"
	notificationpg "github.com/grahita/portal/api/internal/notification/postgres"
	"github.com/grahita/portal/api/internal/order"
	orderpg "github.com/grahita/portal/api/internal/order/postgres"
	"github.com/grahita/portal/api/internal/payment"
	paymentpg "github.com/grahita/portal/api/internal/payment/postgres"
	"github.com/grahita/portal/api/internal/platform/clock"
	"github.com/grahita/portal/api/internal/platform/database"
	"github.com/grahita/portal/api/internal/platform/httpx"
	"github.com/grahita/portal/api/internal/platform/storage"
	"github.com/grahita/portal/api/internal/registration"
	registrationpg "github.com/grahita/portal/api/internal/registration/postgres"
	"github.com/grahita/portal/api/internal/settings"
	settingspg "github.com/grahita/portal/api/internal/settings/postgres"
	"github.com/grahita/portal/api/internal/users"
	userspg "github.com/grahita/portal/api/internal/users/postgres"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// objekHitung membungkus storage.Memory sekaligus menghitung objek yang
// tersimpan, sehingga pengujian integrasi bisa memastikan tidak ada objek
// yatim (mis. unggahan bukti yang gagal di tengah jalan).
type objekHitung struct {
	*storage.Memory
	mu    sync.Mutex
	kunci map[string]bool
}

func newObjekHitung() *objekHitung {
	return &objekHitung{Memory: storage.NewMemory("/files"), kunci: map[string]bool{}}
}

// Put menyimpan objek lalu mencatat kuncinya.
func (o *objekHitung) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if err := o.Memory.Put(ctx, key, body, size, contentType); err != nil {
		return err
	}
	o.mu.Lock()
	o.kunci[key] = true
	o.mu.Unlock()
	return nil
}

// Delete membuang objek dan kuncinya.
func (o *objekHitung) Delete(ctx context.Context, key string) error {
	if err := o.Memory.Delete(ctx, key); err != nil {
		return err
	}
	o.mu.Lock()
	delete(o.kunci, key)
	o.mu.Unlock()
	return nil
}

// jumlah mengembalikan banyaknya objek yang saat ini tersimpan.
func (o *objekHitung) jumlah() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.kunci)
}

// pgFixture adalah satu dunia uji lengkap di atas database nyata.
type pgFixture struct {
	srv  *httptest.Server
	pool *database.Pool
	mail *resetSpy

	// users adalah service pengelolaan akun (F1-05); queue menangkap email
	// undangan admin yang diantre. branches adalah service master cabang
	// (F1-06). bank adalah service rekening bank (F1-09). registration adalah
	// service pendaftaran akun cabang lewat token (F1-07).
	users        *users.Service
	queue        *queueSpy
	branches     *branch.Service
	bank         *bank.Service
	registration *registration.Service

	// Email outbox (F1-16): notif adalah service outbox produksi, kirim
	// menangkap email yang benar-benar dikirim worker, objek adalah object
	// storage in-memory untuk lampiran dokumen (sekaligus menghitung objek
	// supaya kebocoran storage terlihat).
	notif *notification.Service
	kirim *fakeSender
	objek *objekHitung

	// Bukti transfer (F1-13): repository dan service nyata di atas PostgreSQL,
	// memakai object storage in-memory yang sama.
	payments     *payment.Service
	paymentsRepo *paymentpg.Repo

	// Dokumen pesanan (F1-17): repository + service nyata di atas PostgreSQL
	// dengan object storage in-memory yang sama dan renderer uji (tanpa mesin
	// PDF). Pengujian yang butuh PDF nyata membangun router sendiri dari
	// `deps` dengan renderer Chromium (integration_documents_pdf_test.go).
	documents *document.Service
	docRepo   *documentpg.Repo
	deps      Deps

	// Verifikasi pembayaran (F1-18): klien River insert-only yang menitipkan
	// job render dokumen pada transaksi verifikasi, dan service pemenuhan yang
	// menjalankan pekerjaan itu (peran worker di produksi).
	jobs    *river.Client[pgx.Tx]
	selesai *fulfillment.Service

	// Revisi invoice berversi (F2-25): service revisi di atas repository
	// pesanan yang sama (dengan penjadwal River).
	revise *order.RevisionService

	branchA, branchB, branchC string
	userA, userB, userC       string
	adminSA, adminMgr         string
}

// pgEnv membaca DSN database uji; uji dilewati bila tidak diisi.
func pgEnv(t *testing.T) string {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL tidak diisi — uji integrasi auth di PostgreSQL dilewati")
	}
	return url
}

// denganSkema menambahkan search_path skema uji ke DSN sehingga seluruh
// koneksi (pgx pool maupun goose) bekerja di skema tersebut. `public` tetap
// disertakan agar ekstensi yang sudah terpasang di sana (citext, pg_trgm)
// tetap bisa diresolusi, sementara objek BARU selalu dibuat di skema uji
// karena ia berada di urutan pertama.
func denganSkema(t *testing.T, dsn, skema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN tidak valid: %v", err)
	}
	q := u.Query()
	q.Set("search_path", skema+",public")
	u.RawQuery = q.Encode()
	return u.String()
}

// newPGFixture menyiapkan satu dunia uji di atas PostgreSQL nyata.
//
// Seluruh skema ditempatkan pada SKEMA database tersendiri yang dibuat unik
// per pemanggilan. Ini penting karena uji integrasi lain pada database yang
// sama (mis. internal/platform/database yang menurunkan seluruh migrasi) bisa
// berjalan bersamaan; dengan skema terpisah keduanya tidak saling merusak.
func newPGFixture(t *testing.T) *pgFixture {
	t.Helper()
	url := pgEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	adminPool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatalf("koneksi database uji: %v", err)
	}
	t.Cleanup(adminPool.Close)

	skema := fmt.Sprintf("f104_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+skema); err != nil {
		t.Fatalf("membuat skema uji %s: %v", skema, err)
	}
	// goose menyimpan versi migrasi di tabel goose_db_version. Bila tabel itu
	// tidak ada di skema uji, goose akan memakai tabel versi milik skema
	// `public` pada database yang sama (dipakai uji migrasi lain yang berjalan
	// bersamaan) dan menyimpulkan migrasi sudah diterapkan — akibatnya skema
	// uji tetap kosong. Tabel versi tersendiri (berisi baris awal 0) di skema
	// uji memaksa goose menerapkan seluruh migrasi di sana, apa pun keadaan
	// public.
	if _, err := adminPool.Exec(ctx, `CREATE TABLE `+skema+`.goose_db_version (
		id serial NOT NULL,
		version_id bigint NOT NULL,
		is_applied boolean NOT NULL,
		tstamp timestamp NULL DEFAULT now(),
		PRIMARY KEY (id)
	)`); err != nil {
		t.Fatalf("membuat tabel versi goose pada skema %s: %v", skema, err)
	}
	// goose membutuhkan baris awal (version_id 0) di tabel versinya — tanpa
	// baris itu ia menganggap riwayat migrasi rusak. Isi awal ini sama dengan
	// yang ditulis goose sendiri saat membuat tabel versi.
	if _, err := adminPool.Exec(ctx, `INSERT INTO `+skema+`.goose_db_version (version_id, is_applied) VALUES (0, true)`); err != nil {
		t.Fatalf("mengisi baris awal goose_db_version pada skema %s: %v", skema, err)
	}
	t.Cleanup(func() {
		bersihCtx, batal := context.WithTimeout(context.Background(), 30*time.Second)
		defer batal()
		if _, err := adminPool.Exec(bersihCtx, `DROP SCHEMA IF EXISTS `+skema+` CASCADE`); err != nil {
			t.Logf("gagal menghapus skema uji %s: %v", skema, err)
		}
	})

	scoped := denganSkema(t, url, skema)
	pool, err := database.Connect(ctx, scoped)
	if err != nil {
		t.Fatalf("koneksi pool skema uji: %v", err)
	}
	t.Cleanup(pool.Close)

	pgEnsureSchema(t, ctx, scoped, pool)
	// Tabel internal River (river_job, dll) pada skema uji: transisi verifikasi
	// (F1-18) menitipkan pekerjaan dokumen pada transaksi yang sama, jadi tabel
	// itu harus ada di skema yang sedang dipakai.
	pgEnsureRiver(t, ctx, pool)
	if err := kosongkanDomain(ctx, pool); err != nil {
		t.Fatalf("mengosongkan tabel domain: %v", err)
	}

	f := &pgFixture{pool: pool}
	hash, err := auth.HashPassword("rahasia123")
	if err != nil {
		t.Fatalf("hash kata sandi uji: %v", err)
	}

	f.branchA = insertBranch(t, ctx, pool, "Grahita Cabang A", "Kepala A", "cabang-a@example.test", true)
	f.branchB = insertBranch(t, ctx, pool, "Grahita Cabang B", "Kepala B", "cabang-b@example.test", true)
	f.branchC = insertBranch(t, ctx, pool, "Grahita Cabang C", "Kepala C", "cabang-c@example.test", false)

	f.userA = insertBranchUser(t, ctx, pool, f.branchA, "a@example.test", hash, true)
	f.userB = insertBranchUser(t, ctx, pool, f.branchB, "b@example.test", hash, true)
	f.userC = insertBranchUser(t, ctx, pool, f.branchC, "c@example.test", hash, true)

	f.adminSA = insertAdmin(t, ctx, pool, "Rina", "rina@example.test", hash, "superadmin", true, true)
	f.adminMgr = insertAdmin(t, ctx, pool, "Budi", "budi@example.test", hash, "manager", true, true)
	// Undangan: belum diaktivasi (password_hash dan activated_at NULL).
	insertAdmin(t, ctx, pool, "Undangan", "undang@example.test", "", "manager", true, false)
	// Nonaktif: pernah aktif, lalu dimatikan.
	insertAdmin(t, ctx, pool, "Mantan", "mantan@example.test", hash, "manager", false, true)

	// Matriks harga per cabang: harga berbeda supaya kebocoran langsung terlihat.
	productID := insertProduct(t, ctx, pool, "P-01", "Bio Magnetik", 350000)
	for branch, harga := range map[string]int64{f.branchA: 100000, f.branchB: 200000, f.branchC: 300000} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO branch_prices (branch_id, product_id, harga) VALUES ($1, $2, $3)`,
			branch, productID, harga); err != nil {
			t.Fatalf("seed harga cabang: %v", err)
		}
	}

	authRepo := authpg.New(pool)
	svc := auth.NewService(authRepo, authRepo, authRepo, clock.System(), 8*time.Hour, 30*24*time.Hour)

	// Email outbox produksi (F1-16) di atas PostgreSQL nyata: service outbox,
	// pengirim palsu yang menangkap email, dan object storage in-memory untuk
	// lampiran dokumen.
	notifStore := notificationpg.New(pool)
	objek := newObjekHitung()
	kirim := &fakeSender{}
	notifSvc := notification.New(notifStore, kirim,
		notification.WithObjectStore(objek),
		notification.WithOrderReader(notifStore),
		notification.WithAdminRecipients(userspg.New(pool)),
	)
	f.notif = notifSvc
	f.kirim = kirim
	f.objek = objek
	// Tautan reset/undangan tetap harus menunjuk portal yang benar; spy
	// mencatat permintaan lalu meneruskannya ke outbox produksi.
	f.mail = &resetSpy{delegat: notification.NewResetMailer(
		notifSvc, testConfig().CustomerBaseURL(), testConfig().AdminBaseURL(), testConfig().PasswordResetPath)}
	svc.SetResetMailer(f.mail)

	// Pengelolaan akun pengguna (F1-05): repository nyata di atas PostgreSQL,
	// email undangan diantre lewat outbox produksi, tautan diarahkan ke portal
	// admin.
	queue := &queueSpy{delegat: notification.NewQueue(notifSvc)}
	usersSvc := users.NewService(userspg.New(pool), svc, svc,
		users.WithEmailQueue(queue),
		users.WithResetURLBuilder(func(kind auth.Kind, token string) string {
			return "https://grahita-admin.tiusclaw.com/lupa?token=" + token
		}),
	)
	f.queue = queue
	f.users = usersSvc

	branchSvc := branch.NewService(branchpg.New(pool), clock.System())
	f.branches = branchSvc

	bankSvc := bank.NewService(bankpg.New(pool))
	f.bank = bankSvc

	// Pendaftaran akun cabang lewat token registrasi (F1-07).
	registrationSvc := registration.NewService(registrationpg.New(pool), clock.System())
	f.registration = registrationSvc

	// Katalog sekaligus menjadi sumber harga order line domain pesanan:
	// harga selalu diambil dari branch_prices cabang sesi (F1-11), dan setelan
	// server menjadi snapshot biaya packaging pesanan Sertifikat (F1-12).
	catalogSvc := catalog.NewService(catalogpg.New(pool))
	settingsSvc := settings.NewService(settingspg.New(pool))

	// Bukti transfer (F1-13): repository nyata di atas PostgreSQL, object
	// storage in-memory, batas unggah dari setelan yang sama dengan produksi.
	// Repository yang sama menyimpan transaksi verifikasi (F1-18); klien River
	// di bawah hanya menitipkan job (insert-only), worker-lah yang menjalankan.
	jobQueue, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatalf("siapkan penjadwal pekerjaan: %v", err)
	}
	payRepo := paymentpg.New(pool, paymentpg.WithJobQueue(jobQueue))
	// Sumber setelan AI dipasang (F2-26): unggahan bukti pada pesanan yang
	// sudah dikirim menitipkan pekerjaan pembacaan bukti pada transaksi yang
	// sama, sama seperti produksi (cmd/server).
	paySvc := payment.NewService(payRepo, payRepo, objek, settingsSvc, payment.WithAISettings(settingsSvc))
	f.payments = paySvc
	f.paymentsRepo = payRepo
	f.jobs = jobQueue

	orderRepo := orderpg.New(pool, orderpg.WithJobQueue(jobQueue))
	docRepo := documentpg.New(pool)
	docSvc := document.NewService(docRepo, objek, rendererUji{})
	f.docRepo = docRepo
	f.documents = docSvc
	// Pemenuhan pesanan Done (F1-18) — peran worker di produksi.
	selesaiSvc := fulfillment.NewService(docSvc, notifSvc)
	f.selesai = selesaiSvc
	f.revise = order.NewRevisionService(orderRepo, settingsSvc)

	deps := Deps{
		Config:   testConfig(),
		Version:  "test-pg",
		Health:   fakeHealth{migrasi: 9},
		Auth:     svc,
		Settings: settingsSvc,
		Catalog:  catalogSvc,
		Orders: order.NewService(orderRepo, catalogSvc, settingsSvc, order.WithBranchFlags(branchSvc),
			order.WithClock(clock.System()), order.WithAISettings(settingsSvc)),
		AdminOrders: order.NewAdminService(orderRepo, order.WithEmailGagal(notifSvc)),
		Revise:      f.revise,
		Dashboard: dashboard.NewService(dashboardpg.New(pool),
			dashboard.WithEmailGagal(notifSvc)),
		Payments:            paySvc,
		Verify:              payment.NewVerificationService(payRepo),
		Documents:           docSvc,
		Users:               usersSvc,
		Branches:            branchSvc,
		Bank:                bankSvc,
		Registration:        registrationSvc,
		Emails:              notifSvc,
		Storage:             objek,
		LoginLimiter:        httpx.NewRateLimiter(5, 15*time.Minute),
		ForgotLimiter:       httpx.NewRateLimiter(5, 15*time.Minute),
		RegistrationLimiter: httpx.NewRateLimiter(50, 15*time.Minute),
		ProofLimiter:        httpx.NewRateLimiter(500, 15*time.Minute),
	}
	f.deps = deps
	f.srv = httptest.NewServer(NewRouter(deps))
	t.Cleanup(f.srv.Close)
	return f
}

// pgEnsureSchema menjalankan migrasi pada skema uji yang sudah aktif
// (search_path DSN). Pada PostgreSQL < 18 fungsi uuidv7() belum ada sehingga
// shim pengembangan dipasang lebih dulu — persis seperti pengujian lokal F1-02.
func pgEnsureSchema(t *testing.T, ctx context.Context, scopedURL string, pool *database.Pool) {
	t.Helper()
	var ada bool
	if err := pool.QueryRow(ctx, `SELECT to_regprocedure('uuidv7()') IS NOT NULL`).Scan(&ada); err != nil {
		t.Fatalf("memeriksa fungsi uuidv7: %v", err)
	}
	if !ada {
		shim, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "pg-dev-shim.sql"))
		if err != nil {
			t.Fatalf("membaca scripts/pg-dev-shim.sql: %v", err)
		}
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("mengambil koneksi untuk shim: %v", err)
		}
		// PgConn.Exec memakai protokol sederhana sehingga satu berkas berisi
		// banyak pernyataan bisa dijalankan sekaligus.
		_, err = conn.Conn().PgConn().Exec(ctx, string(shim)).ReadAll()
		conn.Release()
		if err != nil {
			t.Fatalf("memasang shim uuidv7: %v", err)
		}
	}
	if err := database.Up(ctx, scopedURL); err != nil {
		t.Fatalf("menjalankan migrasi pada skema uji: %v", err)
	}
}

// pgEnsureRiver membuat/memperbarui tabel internal River (river_job, dll) pada
// skema uji yang sedang aktif — sama seperti `--mode=migrate` di produksi.
// Driver River memakai CURRENT_SCHEMA bila skema tidak disebut eksplisit,
// sehingga tabelnya lahir di skema uji (search_path terdepan).
func pgEnsureRiver(t *testing.T, ctx context.Context, pool *database.Pool) {
	t.Helper()
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		t.Fatalf("siapkan migrasi River: %v", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatalf("menjalankan migrasi River pada skema uji: %v", err)
	}
}

func kosongkanDomain(ctx context.Context, pool *database.Pool) error {
	_, err := pool.Exec(ctx, `TRUNCATE branches, branch_users, admin_users, products, sessions,
		registration_tokens, password_reset_tokens, audit_logs, orders, order_lines,
		documents, order_events, email_outbox, cnc_reminders RESTART IDENTITY CASCADE`)
	return err
}

func insertBranch(t *testing.T, ctx context.Context, pool *database.Pool, nama, kepala, email string, aktif bool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx,
		`INSERT INTO branches (nama, nama_kepala, email, is_active) VALUES ($1,$2,$3,$4) RETURNING id`,
		nama, kepala, email, aktif).Scan(&id)
	if err != nil {
		t.Fatalf("seed cabang %s: %v", nama, err)
	}
	return id
}

func insertBranchUser(t *testing.T, ctx context.Context, pool *database.Pool, branchID, email, hash string, aktif bool) string {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx,
		`INSERT INTO branch_users (branch_id, email, password_hash, is_active) VALUES ($1,$2,$3,$4) RETURNING id`,
		branchID, email, hash, aktif).Scan(&id)
	if err != nil {
		t.Fatalf("seed akun cabang %s: %v", email, err)
	}
	return id
}

func insertAdmin(t *testing.T, ctx context.Context, pool *database.Pool, nama, email, hash, role string, aktif, activated bool) string {
	t.Helper()
	var password any
	if hash != "" {
		password = hash
	}
	var activatedAt any
	if activated {
		activatedAt = time.Now().UTC()
	}
	var id string
	err := pool.QueryRow(ctx,
		`INSERT INTO admin_users (nama, email, password_hash, role, is_active, activated_at, invited_at)
		 VALUES ($1,$2,$3,$4::admin_role,$5,$6,now()) RETURNING id`,
		nama, email, password, role, aktif, activatedAt).Scan(&id)
	if err != nil {
		t.Fatalf("seed admin %s: %v", email, err)
	}
	return id
}

func insertProduct(t *testing.T, ctx context.Context, pool *database.Pool, kode, nama string, harga int64) string {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx,
		`INSERT INTO products (kode, nama, jenis, satuan, harga_default, urutan)
		 VALUES ($1,$2,'reguler','pcs',$3,1) RETURNING id`,
		kode, nama, harga).Scan(&id)
	if err != nil {
		t.Fatalf("seed produk %s: %v", kode, err)
	}
	return id
}

func pgLogin(t *testing.T, srv *httptest.Server, email, password string) ([]*http.Cookie, string) {
	t.Helper()
	status, body, cookies := apiReq(t, http.DefaultClient, http.MethodPost, srv.URL+"/api/v1/auth/login",
		`{"email":"`+email+`","password":"`+password+`"}`, nil, "")
	if status != http.StatusOK {
		t.Fatalf("login %s gagal: %d %v", email, status, body)
	}
	return cookies, csrfFromCookies(cookies)
}

func (f *pgFixture) sesiAktif(t *testing.T, userID string) int {
	t.Helper()
	var n int
	err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL`, userID).Scan(&n)
	if err != nil {
		t.Fatalf("hitung sesi aktif: %v", err)
	}
	return n
}

func TestPGSesiLoginLogoutDanRotasi(t *testing.T) {
	f := newPGFixture(t)

	cookies1, csrf1 := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	if f.sesiAktif(t, f.userA) != 1 {
		t.Fatalf("harus ada 1 sesi tersimpan, dapat %d", f.sesiAktif(t, f.userA))
	}

	status, me, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookies1, "")
	if status != http.StatusOK {
		t.Fatalf("me harus 200, dapat %d", status)
	}
	user := me["data"].(map[string]any)["user"].(map[string]any)
	if user["branch_id"] != f.branchA || user["role"] != "branch" {
		t.Errorf("profil sesi tidak sesuai: %v", user)
	}

	// Login kedua membuat sesi baru (rotasi ID), bukan memakai ulang yang lama.
	cookies2, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	if cookies1[0].Value == cookies2[0].Value {
		t.Error("login ulang harus menerbitkan token sesi baru")
	}
	if f.sesiAktif(t, f.userA) != 2 {
		t.Errorf("harus ada 2 sesi, dapat %d", f.sesiAktif(t, f.userA))
	}

	// Logout hanya mencabut sesi yang dipakai.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/logout", "", cookies1, csrf1)
	if status != http.StatusNoContent {
		t.Fatalf("logout harus 204, dapat %d", status)
	}
	if f.sesiAktif(t, f.userA) != 1 {
		t.Errorf("harus tersisa 1 sesi, dapat %d", f.sesiAktif(t, f.userA))
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookies1, "")
	if status != http.StatusUnauthorized {
		t.Errorf("sesi yang logout harus 401, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookies2, "")
	if status != http.StatusOK {
		t.Errorf("sesi lain harus tetap hidup, dapat %d", status)
	}
}

func TestPGAturUlangPasswordTersimpanDanSekaliPakai(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	cookies, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")

	var hashLama string
	if err := f.pool.QueryRow(ctx, `SELECT password_hash FROM branch_users WHERE id = $1`, f.userA).Scan(&hashLama); err != nil {
		t.Fatalf("baca hash lama: %v", err)
	}

	status, _, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/password/forgot",
		`{"email":"a@example.test"}`, nil, "")
	if status != http.StatusOK {
		t.Fatalf("lupa kata sandi harus 200, dapat %d", status)
	}
	token := f.mail.tokenTerakhir(t)

	// Yang tersimpan hanya SHA-256 token, bukan nilai aslinya.
	var hashDB []byte
	var usedAt *time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT token_hash, used_at FROM password_reset_tokens WHERE user_id = $1`, f.userA).Scan(&hashDB, &usedAt); err != nil {
		t.Fatalf("baca token atur ulang: %v", err)
	}
	if !bytes.Equal(hashDB, auth.HashToken(token)) {
		t.Error("token_hash di database harus SHA-256 dari token yang dikirim")
	}
	if bytes.Contains(hashDB, []byte(token)) {
		t.Error("nilai token mentah tidak boleh tersimpan di database")
	}
	if usedAt != nil {
		t.Error("token belum boleh ditandai terpakai sebelum dipakai")
	}

	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+token+`","password":"sandibaru123"}`, nil, "")
	if status != http.StatusNoContent {
		t.Fatalf("atur ulang harus 204, dapat %d", status)
	}

	var hashBaru string
	if err := f.pool.QueryRow(ctx, `SELECT password_hash FROM branch_users WHERE id = $1`, f.userA).Scan(&hashBaru); err != nil {
		t.Fatalf("baca hash baru: %v", err)
	}
	if hashBaru == hashLama || !auth.VerifyPassword(hashBaru, "sandibaru123") {
		t.Error("password_hash di database harus sudah memakai kata sandi baru (Argon2id)")
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT used_at FROM password_reset_tokens WHERE user_id = $1`, f.userA).Scan(&usedAt); err != nil {
		t.Fatalf("baca used_at: %v", err)
	}
	if usedAt == nil {
		t.Error("token harus ditandai terpakai di database")
	}
	if f.sesiAktif(t, f.userA) != 0 {
		t.Errorf("seluruh sesi harus dicabut setelah atur ulang, sisa %d", f.sesiAktif(t, f.userA))
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookies, "")
	if status != http.StatusUnauthorized {
		t.Errorf("sesi lama harus 401, dapat %d", status)
	}

	// Sekali pakai, walau token dikirim dua kali.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+token+`","password":"sandilain123"}`, nil, "")
	if status != http.StatusBadRequest {
		t.Fatalf("token sekali pakai harus 400, dapat %d", status)
	}
	assertErrorShape(t, body, "RESET_INVALID")

	pgLogin(t, f.srv, "a@example.test", "sandibaru123")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"a@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusUnauthorized {
		t.Errorf("kata sandi lama harus ditolak, dapat %d (%v)", status, body)
	}
}

func TestPGGantiPasswordCabutSesiLainDiDatabase(t *testing.T) {
	f := newPGFixture(t)

	cookies1, csrf1 := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	cookies2, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	if f.sesiAktif(t, f.userA) != 2 {
		t.Fatalf("prasyarat: harus ada 2 sesi, dapat %d", f.sesiAktif(t, f.userA))
	}

	status, _, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/password/change",
		`{"password_lama":"rahasia123","password_baru":"sandibaru123"}`, cookies1, csrf1)
	if status != http.StatusNoContent {
		t.Fatalf("ganti kata sandi harus 204, dapat %d", status)
	}
	if f.sesiAktif(t, f.userA) != 1 {
		t.Errorf("harus tersisa tepat 1 sesi, dapat %d", f.sesiAktif(t, f.userA))
	}
	// Sesi pemanggil tetap hidup, sesi lain mati.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookies1, "")
	if status != http.StatusOK {
		t.Errorf("sesi pemanggil harus hidup, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookies2, "")
	if status != http.StatusUnauthorized {
		t.Errorf("sesi lain harus 401, dapat %d", status)
	}
	pgLogin(t, f.srv, "a@example.test", "sandibaru123")
}

func TestPGOtorisasiLintasCabangDanRole(t *testing.T) {
	f := newPGFixture(t)

	// Cabang A hanya melihat harganya sendiri; B hanya miliknya.
	cookiesA, _ := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/catalog", "", cookiesA, "")
	if status != http.StatusOK {
		t.Fatalf("katalog cabang A harus 200, dapat %d", status)
	}
	if item := body["data"].([]any)[0].(map[string]any); item["harga"] != float64(100000) {
		t.Errorf("cabang A harus melihat harga cabangnya sendiri, dapat %v", item["harga"])
	}

	cookiesB, _ := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	_, bodyB, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/catalog", "", cookiesB, "")
	if item := bodyB["data"].([]any)[0].(map[string]any); item["harga"] != float64(200000) {
		t.Errorf("cabang B harus melihat harga cabangnya sendiri, dapat %v", item["harga"])
	}

	// /me/branch memakai branch_id dari sesi, bukan dari parameter klien.
	_, me, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/me/branch", "", cookiesA, "")
	if got := me["data"].(map[string]any)["id"]; got != f.branchA {
		t.Errorf("me/branch harus cabang sesi (%s), dapat %v", f.branchA, got)
	}

	// Permintaan API langsung dari cabang ke area admin ditolak.
	for _, path := range []string{"/api/v1/admin/branches", "/api/v1/admin/branches/" + f.branchB, "/api/v1/admin/settings"} {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+path, "", cookiesA, "")
		if status != http.StatusForbidden {
			t.Errorf("cabang pada %s harus 403, dapat %d (%v)", path, status, body)
		}
	}

	// Manager boleh master cabang, dilarang rekening/reporting/pengaturan/user admin.
	cookiesMgr, _ := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/branches", "", cookiesMgr, "")
	if status != http.StatusOK {
		t.Errorf("manager harus boleh daftar cabang, dapat %d", status)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/settings", "", cookiesMgr, "")
	if status != http.StatusForbidden {
		t.Errorf("manager harus 403 pada pengaturan, dapat %d", status)
	}

	// Superadmin boleh pengaturan (dan dengan itu rekening/reporting/user admin).
	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/admin/settings", "", cookiesSA, "")
	if status != http.StatusOK {
		t.Errorf("superadmin harus boleh pengaturan, dapat %d", status)
	}

	// CSRF tetap wajib pada mutasi superadmin, dan berhasil bila token benar.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/settings",
		`{"key":"biaya_packaging","value":4000}`, cookiesSA, "")
	if status != http.StatusForbidden {
		t.Fatalf("mutasi tanpa CSRF harus 403, dapat %d", status)
	}
	assertErrorShape(t, body, "CSRF_INVALID")
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPut, f.srv.URL+"/api/v1/admin/settings",
		`{"key":"biaya_packaging","value":4000}`, cookiesSA, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("mutasi dengan CSRF harus 200, dapat %d", status)
	}
}

func TestPGAkunNonaktifDanAdminUndanganDitolak(t *testing.T) {
	f := newPGFixture(t)

	// Cabang nonaktif: kredensial benar tetapi cabangnya dimatikan.
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"c@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusForbidden {
		t.Fatalf("cabang nonaktif harus 403, dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "FORBIDDEN")

	// Admin undangan: password_hash dan activated_at masih NULL sehingga tidak
	// ada kredensial yang bisa diverifikasi → jawabannya SAMA dengan akun tidak
	// dikenal (401, pesan generik). Ini keputusan sadar: 403 akan membuat email
	// yang masih menganggur bisa dienumerasi.
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"undang@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("admin undangan harus ditolak sebagai kredensial salah (401), dapat %d (%v)", status, body)
	}
	assertErrorShape(t, body, "UNAUTHENTICATED")

	// Admin yang dinonaktifkan juga ditolak.
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"mantan@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusForbidden {
		t.Errorf("admin nonaktif harus 403, dapat %d", status)
	}

	// Sesudah cabang dinonaktifkan, sesi yang sudah ada ikut dicabut.
	cookies, _ := pgLogin(t, f.srv, "b@example.test", "rahasia123")
	if _, err := f.pool.Exec(context.Background(), `UPDATE branches SET is_active = false WHERE id = $1`, f.branchB); err != nil {
		t.Fatalf("menonaktifkan cabang: %v", err)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodGet, f.srv.URL+"/api/v1/auth/me", "", cookies, "")
	if status != http.StatusUnauthorized {
		t.Errorf("sesi cabang nonaktif harus 401, dapat %d", status)
	}
}

func TestPGKredensialSalahTidakMembocorkanAkun(t *testing.T) {
	f := newPGFixture(t)

	pesan := func(email, password string) (int, string) {
		status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
			`{"email":"`+email+`","password":"`+password+`"}`, nil, "")
		msg, _ := body["error"].(map[string]any)["message"].(string)
		return status, msg
	}

	statusSalah, pesanSalah := pesan("a@example.test", "salahsekali")
	statusTidakAda, pesanTidakAda := pesan("tidak-ada@example.test", "rahasia123")
	if statusSalah != http.StatusUnauthorized || statusTidakAda != http.StatusUnauthorized {
		t.Fatalf("keduanya harus 401, dapat %d dan %d", statusSalah, statusTidakAda)
	}
	if pesanSalah != pesanTidakAda {
		t.Errorf("pesan harus identik agar tidak membocorkan akun: %q vs %q", pesanSalah, pesanTidakAda)
	}
	for _, m := range []string{pesanSalah, pesanTidakAda} {
		if m == "" || bytes.Contains([]byte(m), []byte("terdaftar")) {
			t.Errorf("pesan tidak boleh menyiratkan keberadaan akun: %q", m)
		}
	}
}

func TestPGLupaPasswordAkunTidakAdaTetapSukses(t *testing.T) {
	f := newPGFixture(t)

	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/password/forgot",
		`{"email":"tidak-ada@example.test"}`, nil, "")
	if status != http.StatusOK {
		t.Fatalf("akun tidak ada harus tetap 200, dapat %d (%v)", status, body)
	}
	if f.mail.jumlah() != 0 {
		t.Errorf("tidak boleh ada email untuk akun tak dikenal, dapat %d", f.mail.jumlah())
	}
	var jumlah int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM password_reset_tokens`).Scan(&jumlah); err != nil {
		t.Fatalf("hitung token: %v", err)
	}
	if jumlah != 0 {
		t.Errorf("tidak boleh ada token untuk akun tak dikenal, dapat %d", jumlah)
	}
}
