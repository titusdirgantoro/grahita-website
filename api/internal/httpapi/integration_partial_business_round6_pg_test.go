package httpapi

// Pengujian komposit untuk menutup bukti end-to-end skenario PARTIAL putaran
// keenam. Seluruh test di berkas ini memakai fixture PostgreSQL nyata dan
// router produksi, bukan repository in-memory.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/payment"
	"github.com/grahita/portal/api/internal/receivable"
)

// AUT-07: permintaan publik tidak mengungkap keberadaan akun, TTL mengikuti
// setting, email benar-benar melewati outbox/worker, dan token kedaluwarsa serta
// token yang sudah dipakai tidak dapat dipakai ulang.
func TestPGAUT07LupaPasswordPublikTTLWorkerDanSekaliPakai(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	setSetting(t, f, "reset_menit", "7")

	statusAda, bodyAda, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/password/forgot", `{"email":"a@example.test"}`, nil, "")
	statusKosong, bodyKosong, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/password/forgot", `{"email":"tidak-ada@example.test"}`, nil, "")
	if statusAda != http.StatusOK || statusKosong != http.StatusOK || !reflect.DeepEqual(bodyAda, bodyKosong) {
		t.Fatalf("respons akun ada/tidak ada harus identik: %d %+v vs %d %+v", statusAda, bodyAda, statusKosong, bodyKosong)
	}
	if f.mail.jumlah() != 1 || f.hitungEmail(t, "reset_password") != 1 {
		t.Fatalf("hanya akun terdaftar yang boleh menghasilkan satu reset/outbox: mail=%d outbox=%d",
			f.mail.jumlah(), f.hitungEmail(t, "reset_password"))
	}

	var ttl time.Duration
	if err := f.pool.QueryRow(ctx, `SELECT expires_at-created_at FROM password_reset_tokens WHERE user_id=$1`, f.userA).Scan(&ttl); err != nil {
		t.Fatalf("baca TTL reset: %v", err)
	}
	if ttl < 7*time.Minute-time.Second || ttl > 7*time.Minute+time.Second {
		t.Fatalf("TTL harus mengikuti setting 7 menit, dapat %s", ttl)
	}
	if hasil := f.kirimAntrean(t); hasil.Terkirim != 1 || f.kirim.jumlah() != 1 {
		t.Fatalf("worker harus mengirim tepat satu email reset: hasil=%+v terkirim=%d", hasil, f.kirim.jumlah())
	}

	tokenKedaluwarsa := f.mail.tokenTerakhir(t)
	if _, err := f.pool.Exec(ctx, `UPDATE password_reset_tokens SET expires_at=now()-interval '1 second' WHERE user_id=$1`, f.userA); err != nil {
		t.Fatal(err)
	}
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+tokenKedaluwarsa+`","password":"sandibaru123"}`, nil, "")
	if status != http.StatusBadRequest || kodeErrorVerifikasi(body) != "RESET_INVALID" {
		t.Fatalf("token kedaluwarsa harus ditolak: %d %+v", status, body)
	}

	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/password/forgot", `{"email":"a@example.test"}`, nil, "")
	if status != http.StatusOK {
		t.Fatalf("terbitkan reset pengganti: %d", status)
	}
	tokenSah := f.mail.tokenTerakhir(t)
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+tokenSah+`","password":"sandibaru123"}`, nil, "")
	if status != http.StatusNoContent {
		t.Fatalf("token sah harus berhasil: %d %+v", status, body)
	}
	pgLogin(t, f.srv, "a@example.test", "sandibaru123")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/auth/password/reset",
		`{"token":"`+tokenSah+`","password":"sandilain123"}`, nil, "")
	if status != http.StatusBadRequest || kodeErrorVerifikasi(body) != "RESET_INVALID" {
		t.Fatalf("token terpakai harus ditolak: %d %+v", status, body)
	}
}

// CAB-06: cabang dengan order dan dokumen historis dapat dinonaktifkan. Sesi,
// login, dan submit baru langsung ditolak sementara snapshot historis utuh.
func TestPGCAB06NonaktifMenolakLoginSubmitDanMenjagaDokumenHistoris(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5 WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	cookiesCabang, csrfCabang := pgLogin(t, f.srv, "a@example.test", "rahasia123")
	product := produkKode(t, f, "P-01")
	historis := buatDraftHTTP(t, f, cookiesCabang, csrfCabang, product, 2)["id"].(string)
	resp, bodySubmit := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+historis+"/submit", cookiesCabang, csrfCabang, nil, "", "tempo")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("submit historis: %d %+v", resp.StatusCode, bodySubmit)
	}
	cookiesMgr, csrfMgr := pgLogin(t, f.srv, "budi@example.test", "rahasia123")
	status, body, _ := apiReq(t, http.DefaultClient, http.MethodPost,
		f.srv.URL+"/api/v1/admin/orders/"+historis+"/approve-tempo", `{}`, cookiesMgr, csrfMgr)
	if status != http.StatusOK {
		t.Fatalf("approve historis: %d %+v", status, body)
	}
	if _, err := f.selesai.PesananSelesai(ctx, historis); err != nil {
		t.Fatalf("render dokumen historis: %v", err)
	}

	draftBaru := buatDraftHTTP(t, f, cookiesCabang, csrfCabang, product, 1)["id"].(string)
	var statusLama string
	var totalLama int64
	var barisLama, dokumenLama int
	if err := f.pool.QueryRow(ctx, `SELECT status::text,total FROM orders WHERE id=$1`, historis).Scan(&statusLama, &totalLama); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM order_lines WHERE order_id=$1`, historis).Scan(&barisLama); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM documents WHERE order_id=$1`, historis).Scan(&dokumenLama); err != nil {
		t.Fatal(err)
	}
	if statusLama != "approved_unpaid" || totalLama != 200000 || barisLama != 1 || dokumenLama != 3 {
		t.Fatalf("prasyarat histori salah: status=%s total=%d lines=%d docs=%d", statusLama, totalLama, barisLama, dokumenLama)
	}

	cookiesSA, csrfSA := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodPut,
		f.srv.URL+"/api/v1/admin/branches/"+f.branchA, `{"is_active":false}`, cookiesSA, csrfSA)
	if status != http.StatusOK {
		t.Fatalf("nonaktifkan cabang: %d %+v", status, body)
	}
	resp, bodySubmit = multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+draftBaru+"/submit", cookiesCabang, csrfCabang, nil, "", "tempo")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sesi lama tidak boleh submit setelah nonaktif: %d %+v", resp.StatusCode, bodySubmit)
	}
	status, _, _ = apiReq(t, http.DefaultClient, http.MethodPost, f.srv.URL+"/api/v1/auth/login",
		`{"email":"a@example.test","password":"rahasia123"}`, nil, "")
	if status != http.StatusForbidden {
		t.Fatalf("login cabang nonaktif harus 403, dapat %d", status)
	}
	status, body, _ = apiReq(t, http.DefaultClient, http.MethodGet,
		f.srv.URL+"/api/v1/admin/orders/"+historis, "", cookiesSA, "")
	if status != http.StatusOK {
		t.Fatalf("admin tetap harus dapat membaca histori: %d %+v", status, body)
	}
	var statusSesudah string
	var totalSesudah int64
	var barisSesudah, dokumenSesudah int
	_ = f.pool.QueryRow(ctx, `SELECT status::text,total FROM orders WHERE id=$1`, historis).Scan(&statusSesudah, &totalSesudah)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM order_lines WHERE order_id=$1`, historis).Scan(&barisSesudah)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM documents WHERE order_id=$1`, historis).Scan(&dokumenSesudah)
	if statusSesudah != statusLama || totalSesudah != totalLama || barisSesudah != barisLama || dokumenSesudah != dokumenLama {
		t.Fatalf("histori berubah setelah nonaktif: %s/%d/%d/%d -> %s/%d/%d/%d",
			statusLama, totalLama, barisLama, dokumenLama, statusSesudah, totalSesudah, barisSesudah, dokumenSesudah)
	}
}

// VER-06: kebijakan tujuan dibaca ulang saat keputusan. Rekening cabang lain,
// rekening yang baru dinonaktifkan, dan assignment yang baru dicabut tidak
// boleh lolos sebagai cocok; operator masih dapat memilih jalur perlu-review.
func TestPGVER06TujuanRekeningDievaluasiUlangSaatVerifikasi(t *testing.T) {
	for _, tc := range []struct {
		nama       string
		ubah       func(t *testing.T, f *pgFixture) string
		wantStatus string
	}{
		{
			nama: "aktif tetapi milik cabang lain",
			ubah: func(t *testing.T, f *pgFixture) string {
				var id string
				if err := f.pool.QueryRow(t.Context(), `INSERT INTO bank_accounts(bank,nama_pemilik,nomor,is_active) VALUES('BRI','Cabang Lain','88770011',true) RETURNING id`).Scan(&id); err != nil {
					t.Fatal(err)
				}
				if _, err := f.pool.Exec(t.Context(), `INSERT INTO branch_bank_accounts(branch_id,bank_account_id,is_allowed,assigned_by) VALUES($1,$2,true,$3)`, f.branchB, id, f.adminSA); err != nil {
					t.Fatal(err)
				}
				return id
			},
			wantStatus: "not_allowed",
		},
		{
			nama: "dinonaktifkan setelah bukti dibuka",
			ubah: func(t *testing.T, f *pgFixture) string {
				if _, err := f.pool.Exec(t.Context(), `UPDATE bank_accounts SET is_active=false WHERE id=$1`, f.bankDefault); err != nil {
					t.Fatal(err)
				}
				return f.bankDefault
			},
			// Rekening nonaktif keluar dari himpunan rekening efektif sehingga
			// domain menyimpannya sebagai not_allowed. Keputusan tetap fail-closed
			// dan jalur review menyimpan status tujuan aktual tersebut.
			wantStatus: "not_allowed",
		},
		{
			nama: "assignment dicabut setelah bukti dibuka",
			ubah: func(t *testing.T, f *pgFixture) string {
				if _, err := f.pool.Exec(t.Context(), `UPDATE branch_bank_accounts SET is_allowed=false,revoked_by=$3,revoked_at=now() WHERE branch_id=$1 AND bank_account_id=$2`, f.branchA, f.bankDefault, f.adminSA); err != nil {
					t.Fatal(err)
				}
				if _, err := f.pool.Exec(t.Context(), `UPDATE branches SET bank_allowlist_version=bank_allowlist_version+1 WHERE id=$1`, f.branchA); err != nil {
					t.Fatal(err)
				}
				return f.bankDefault
			},
			wantStatus: "not_allowed",
		},
	} {
		t.Run(tc.nama, func(t *testing.T) {
			f := newPGFixture(t)
			id, total := seedPesananSubmitted(t, f, f.branchA, "2026-10-"+fmt.Sprintf("%03d", time.Now().UnixNano()%900+100))
			_ = bodyVerifikasiCocok(t, f, id, &total, "")
			var proofID string
			if err := f.pool.QueryRow(t.Context(), `SELECT id::text FROM payment_proofs WHERE order_id=$1`, id).Scan(&proofID); err != nil {
				t.Fatal(err)
			}
			bankID := tc.ubah(t, f)
			cookies, csrf := pgLogin(t, f.srv, "rina@example.test", "rahasia123")
			payloadCocok, _ := json.Marshal(map[string]any{
				"hasil": "cocok", "total_terbaca": total,
				"proof_destinations": []payment.ProofDestination{{PaymentProofID: proofID, BankAccountID: bankID}},
			})
			status, body := verifyHTTP(t, f, cookies, csrf, id, string(payloadCocok))
			if status != http.StatusConflict || kodeErrorVerifikasi(body) != "BANK_ACCOUNT_NOT_ALLOWED" {
				t.Fatalf("kebijakan terbaru harus menolak cocok: %d %+v", status, body)
			}
			payloadReview, _ := json.Marshal(map[string]any{
				"hasil": "perlu_review", "alasan": "rekening_beda", "catatan": "Kebijakan tujuan berubah saat pemeriksaan.",
				"proof_destinations": []payment.ProofDestination{{PaymentProofID: proofID, BankAccountID: bankID}},
			})
			status, body = verifyHTTP(t, f, cookies, csrf, id, string(payloadReview))
			if status != http.StatusOK {
				t.Fatalf("jalur perlu-review harus tetap tersedia: %d %+v", status, body)
			}
			var orderStatus, hasil string
			var raw []byte
			if err := f.pool.QueryRow(t.Context(), `SELECT o.status::text,v.hasil::text,v.destination_results FROM orders o JOIN verification_logs v ON v.order_id=o.id WHERE o.id=$1`, id).Scan(&orderStatus, &hasil, &raw); err != nil {
				t.Fatal(err)
			}
			var tujuan []payment.DestinationResult
			if err := json.Unmarshal(raw, &tujuan); err != nil {
				t.Fatal(err)
			}
			if orderStatus != "submitted" || hasil != "perlu_review" || len(tujuan) != 1 || tujuan[0].Status != tc.wantStatus {
				t.Fatalf("snapshot review salah: order=%s hasil=%s tujuan=%+v", orderStatus, hasil, tujuan)
			}
		})
	}
}

// TMP-28: total besar tidak dikenai plafon tersembunyi. Hold hanya berlaku
// bila toggle aktif DAN due sudah lewat.
func TestPGTMP28TanpaBatasKreditTersembunyiPadaPiutangBesar(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5,tahan_bila_telat=true WHERE id=$1`, f.branchA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE branches SET is_tempo=true,allow_transfer=false,tanggal_bayar=5,tahan_bila_telat=false WHERE id=$1`, f.branchB); err != nil {
		t.Fatal(err)
	}
	svc := receivable.New(f.pool, f.jobs)
	mgr := auth.Actor{UserID: f.adminMgr, Kind: auth.KindAdmin, Role: auth.RoleManager, Nama: "Budi"}
	product := produkKode(t, f, "P-01")

	ujiCabang := func(branchID, email string, dueLewat bool, wantTotal int64) {
		t.Helper()
		cookies, csrf := pgLogin(t, f.srv, email, "rahasia123")
		var ids []string
		for i := 0; i < 3; i++ {
			id := buatDraftHTTP(t, f, cookies, csrf, product, 1_000_000)["id"].(string)
			resp, body := multipartSubmit(t, f.srv.URL+"/api/v1/orders/"+id+"/submit", cookies, csrf, nil, "", "tempo")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("submit besar #%d cabang %s: %d %+v", i+1, branchID, resp.StatusCode, body)
			}
			if _, err := svc.Approve(ctx, mgr, id); err != nil {
				t.Fatalf("approve besar #%d cabang %s: %v", i+1, branchID, err)
			}
			ids = append(ids, id)
			if i == 0 {
				arah := "+ interval '1 day'"
				if dueLewat {
					arah = "- interval '1 day'"
				}
				if _, err := f.pool.Exec(ctx, `UPDATE tempo_charges SET due_date=(CURRENT_DATE `+arah+`)::date WHERE order_id=$1`, id); err != nil {
					t.Fatal(err)
				}
			}
		}
		var jumlah int
		var total int64
		if err := f.pool.QueryRow(ctx, `SELECT count(*),sum(amount) FROM tempo_charges WHERE branch_id=$1`, branchID).Scan(&jumlah, &total); err != nil {
			t.Fatal(err)
		}
		if jumlah != 3 || total != wantTotal {
			t.Fatalf("piutang besar cabang %s salah: count=%d total=%d want=%d ids=%v", branchID, jumlah, total, wantTotal, ids)
		}
	}

	// Harga cabang A Rp100.000 × 1.000.000 × 3 = Rp300 miliar; due pertama
	// belum lewat sehingga toggle hold aktif tidak memblokir order berikutnya.
	ujiCabang(f.branchA, "a@example.test", false, 300_000_000_000)
	// Harga cabang B Rp200.000 × 1.000.000 × 3 = Rp600 miliar; due pertama
	// sudah lewat tetapi toggle hold mati sehingga order berikutnya tetap sah.
	ujiCabang(f.branchB, "b@example.test", true, 600_000_000_000)
}
