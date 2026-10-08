package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
	"github.com/grahita/portal/api/internal/platform/mailer"
)

// Notifier memberi tahu bahwa satu pekerjaan cadangan gagal
// (Architecture.md §7: notifikasi di dashboard + email ke Superadmin).
type Notifier interface {
	BackupGagal(ctx context.Context, run Run, sebab string) error
}

// Pemberitahu adalah Notifier bawaan paket ini: menulis entri audit lalu
// mengirim email ke SELURUH Superadmin aktif.
//
// Catatan desain (disengaja, dibawa ke F1-22): email alarm ini dikirim
// langsung lewat mailer.Mailer, bukan lewat email_outbox. Tabel email_outbox
// adalah antrean email TRANSAKSIONAL pesanan (jenisnya enum `email_kind` yang
// tidak punya nilai untuk alarm operasional) dan kartu F1-16 masih tertahan;
// menumpangkan alarm di sana berarti alarm cadangan ikut macet saat worker
// email bermasalah — justru saat operator paling butuh tahu. Pengiriman
// langsung juga TIDAK menutupi kegagalan: kegagalan kirim dicatat di log dan
// dikembalikan sebagai error, tetapi tidak pernah mengubah status backup_runs.
type Pemberitahu struct {
	repo         Repo
	mail         mailer.Mailer
	adminBaseURL string
	log          *slog.Logger
	timeout      time.Duration
}

// NewPemberitahu membuat pemberi tahu kegagalan cadangan.
func NewPemberitahu(repo Repo, mail mailer.Mailer, adminBaseURL string) *Pemberitahu {
	return &Pemberitahu{
		repo:         repo,
		mail:         mail,
		adminBaseURL: strings.TrimSuffix(adminBaseURL, "/"),
		log:          slog.Default(),
		timeout:      30 * time.Second,
	}
}

// BackupGagal mencatat kegagalan ke audit dan mengirim email ke Superadmin.
func (p *Pemberitahu) BackupGagal(ctx context.Context, run Run, sebab string) error {
	if p.repo == nil {
		return nil
	}
	var masalah []string

	entri := audit.Entry{
		ActorKind: auth.KindSystem,
		ActorID:   AktorSistem,
		ActorNama: "backup",
		Entity:    EntityBackupRun,
		EntityID:  run.ID,
		Action:    ActionBackupGagal,
		After: map[string]any{
			"jenis": run.Jenis,
			"mulai": run.Mulai.UTC(),
			"sebab": sebab,
		},
	}
	if err := p.repo.Audit(ctx, entri); err != nil {
		masalah = append(masalah, "audit: "+err.Error())
	}

	penerima, err := p.repo.SuperadminEmails(ctx)
	if err != nil {
		masalah = append(masalah, "baca penerima: "+err.Error())
	} else if len(penerima) == 0 {
		masalah = append(masalah, "tidak ada Superadmin aktif yang bisa diberi tahu")
	} else if p.mail == nil {
		p.log.Warn("pemberitahuan kegagalan cadangan tidak dikirim: mailer tidak tersedia",
			"jenis", run.Jenis, "penerima", len(penerima))
	} else {
		kirimCtx, cancel := context.WithTimeout(ctx, p.timeout)
		defer cancel()
		if err := p.mail.Send(kirimCtx, p.pesan(run, sebab, penerima)); err != nil {
			masalah = append(masalah, "kirim email: "+err.Error())
		} else {
			p.log.Info("pemberitahuan kegagalan cadangan terkirim",
				"jenis", run.Jenis, "penerima", len(penerima))
		}
	}

	if len(masalah) > 0 {
		return errors.New(strings.Join(masalah, "; "))
	}
	return nil
}

// pesan menyusun email alarm kegagalan cadangan.
func (p *Pemberitahu) pesan(run Run, sebab string, penerima []string) mailer.Message {
	tautan := p.adminBaseURL + "/pengaturan"
	body := fmt.Sprintf(`Cadangan harian GAGAL.

Jenis     : %s
Mulai     : %s
Tujuan    : %s
Penyebab  : %s

Semua hasil cadangan tercatat di halaman Pengaturan (bagian Backup) dan di
tabel backup_runs. Periksa kredensial tujuan (rclone/Drive), kunci age, dan
koneksi database, lalu jalankan cadangan lagi (mode --mode=backup).

Status   : %s
`, run.Jenis, run.Mulai.In(jakartaTZ()).Format("02 Jan 2006 15:04 MST"),
		run.Tujuan, sebab, tautan)

	return mailer.Message{
		To:      penerima,
		Subject: "[Grahita] Cadangan harian GAGAL — " + run.Jenis,
		Text:    body,
	}
}

// jakartaTZ mengembalikan zona WIB (dipakai format pesan).
func jakartaTZ() *time.Location {
	if loc, err := time.LoadLocation("Asia/Jakarta"); err == nil {
		return loc
	}
	return time.FixedZone("WIB", 7*60*60)
}
