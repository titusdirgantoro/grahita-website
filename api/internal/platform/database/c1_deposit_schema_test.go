package database_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type settlementFixture struct {
	branchID, adminID, orderID, verificationID string
	doneAt                                     time.Time
}

func buatSettlementDoneC1(ctx context.Context, t *testing.T, tx pgx.Tx, suffix string, total int64) settlementFixture {
	t.Helper()
	var f settlementFixture
	if err := tx.QueryRow(ctx, `INSERT INTO branches(nama,nama_kepala,email)
VALUES($1,'Kepala UJI',$2) RETURNING id`, "UJI-C1-"+suffix, "uji-c1-"+suffix+"@example.test").Scan(&f.branchID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO admin_users(nama,email,role)
VALUES('UJI Admin C1',$1,'manager') RETURNING id`, "uji-admin-c1-"+suffix+"@example.test").Scan(&f.adminID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO orders(
branch_id,nomor,versi,status,verif_status,tanggal,total,submitted_at)
VALUES($1,$2,0,'submitted','belum',current_date,$3,now()) RETURNING id`,
		f.branchID, "UJI-C1-ORD-"+suffix, total).Scan(&f.orderID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO verification_logs(
order_id,metode,total_pesanan,total_terbaca,hasil,order_version,deposit_amount,transfer_amount,admin_id)
VALUES($1,'manual',$2,$2,'cocok',0,0,$2,$3) RETURNING id`,
		f.orderID, total, f.adminID).Scan(&f.verificationID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `UPDATE orders SET status='done',verif_status='cocok',done_at=now(),
done_verification_log_id=$2,done_order_version=0,done_deposit_amount=0,done_transfer_amount=$3
WHERE id=$1 RETURNING done_at`, f.orderID, f.verificationID, total).Scan(&f.doneAt); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestC1ConstraintRevisiTotalTetapNaikTurunDanHistoriTyped(t *testing.T) {
	ctx, pool := siapkanSkema(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	s := fmt.Sprintf("%d", time.Now().UnixNano())
	up := buatSettlementDoneC1(ctx, t, tx, s+"-up", 1000)

	// Total tetap: versi dokumen naik, pointer settlement versi 0 tetap sah.
	if _, err := tx.Exec(ctx, `UPDATE orders SET versi=1 WHERE id=$1`, up.orderID); err != nil {
		t.Fatalf("total tetap: %v", err)
	}
	// Total naik tidak boleh mempertahankan state Done/pointer lama.
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx, `UPDATE orders SET versi=2,total=1200 WHERE id=$1`, up.orderID)
		return err
	})
	// Jalur pending sah mengosongkan pointer aktif tetapi mempertahankan log lama.
	if _, err := tx.Exec(ctx, `UPDATE orders SET versi=2,total=1200,status='submitted',
revision_payment_state='revision_underpayment',revision_payment_started_at=now(),
verif_status='perlu_review',alasan_review='kurang_bayar',done_at=NULL,
done_verification_log_id=NULL,done_order_version=NULL,done_deposit_amount=NULL,done_transfer_amount=NULL
WHERE id=$1`, up.orderID); err != nil {
		t.Fatalf("pending naik: %v", err)
	}
	var logAda bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM verification_logs WHERE id=$1)`, up.verificationID).Scan(&logAda); err != nil || !logAda {
		t.Fatalf("histori hilang=%v err=%v", logAda, err)
	}
	// Pointer parsial pada pending selalu ditolak oleh CHECK three-valued-logic-safe.
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx, `UPDATE orders SET done_order_version=0 WHERE id=$1`, up.orderID)
		return err
	})
	// Snapshot histori typed lengkap dengan FK tepat berhasil.
	if _, err := tx.Exec(ctx, `INSERT INTO invoice_revisions(
order_id,versi_lama,versi_baru,alasan,snapshot,delta_total,admin_id,
payment_transition,previous_done_verification_log_id,previous_done_order_version,
previous_done_total,previous_done_deposit_amount,previous_done_transfer_amount,previous_done_at)
VALUES($1,1,2,'UJI-naik','{}',200,$2,'underpayment',$3,0,1000,0,1000,$4)`,
		up.orderID, up.adminID, up.verificationID, up.doneAt); err != nil {
		t.Fatalf("histori typed: %v", err)
	}
	harusGagal(ctx, t, tx, "23503", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx, `INSERT INTO invoice_revisions(
order_id,versi_lama,versi_baru,alasan,snapshot,delta_total,admin_id,
payment_transition,previous_done_verification_log_id,previous_done_order_version,
previous_done_total,previous_done_deposit_amount,previous_done_transfer_amount,previous_done_at)
VALUES($1,2,3,'UJI-salah-total','{}',1,$2,'underpayment',$3,0,999,0,999,$4)`,
			up.orderID, up.adminID, up.verificationID, up.doneAt)
		return err
	})

	down := buatSettlementDoneC1(ctx, t, tx, s+"-down", 1000)
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx, `UPDATE orders SET versi=1,total=800 WHERE id=$1`, down.orderID)
		return err
	})
	if _, err := tx.Exec(ctx, `UPDATE orders SET versi=1,total=800,status='submitted',
revision_payment_state='revision_overpayment_pending',revision_payment_started_at=now(),
verif_status='perlu_review',alasan_review='lebih_bayar',done_at=NULL,
done_verification_log_id=NULL,done_order_version=NULL,done_deposit_amount=NULL,done_transfer_amount=NULL
WHERE id=$1`, down.orderID); err != nil {
		t.Fatalf("pending turun: %v", err)
	}
}

func TestC1JournalDanSnapshotImmutable(t *testing.T) {
	ctx, pool := siapkanSkema(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	s := fmt.Sprintf("%d", time.Now().UnixNano())
	branch := buatCabang(ctx, t, tx, s)
	if _, err := tx.Exec(ctx, `INSERT INTO branch_deposit_accounts(branch_id) VALUES($1)`, branch); err != nil {
		t.Fatal(err)
	}
	var id string
	harusGagal(ctx, t, tx, "23514", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx, `INSERT INTO branch_deposit_transactions(
branch_id,kind,amount,order_id,order_version,idempotency_key_hash,request_fingerprint,actor_kind)
VALUES($1,'order_allocation_debit',-1,NULL,0,'a'::bytea,'b'::bytea,'system')`, branch)
		return err
	})
	// Trigger immutable diuji memakai row reversal-independent yang valid via fixture minimal.
	var orderID string
	if err := tx.QueryRow(ctx, `INSERT INTO orders(branch_id,nomor,status,verif_status,tanggal,total,submitted_at)
VALUES($1,$2,'submitted','belum',current_date,10,now()) RETURNING id`, branch, "UJI-IMM-"+s).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO branch_deposit_transactions(
branch_id,kind,amount,order_id,order_version,idempotency_key_hash,request_fingerprint,actor_kind)
VALUES($1,'order_allocation_debit',-1,$2,0,'a'::bytea,'b'::bytea,'system') RETURNING id`, branch, orderID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	harusGagal(ctx, t, tx, "55000", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx, `UPDATE branch_deposit_transactions SET reason='ubah' WHERE id=$1`, id)
		return err
	})
	harusGagal(ctx, t, tx, "55000", func(sp pgx.Tx) error {
		_, err := sp.Exec(ctx, `DELETE FROM branch_deposit_transactions WHERE id=$1`, id)
		return err
	})
}
