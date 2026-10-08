package backup

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

// JenisDokumenSah adalah bagian `{jenis}` pada kunci dokumen
// `documents/{order_id}/{jenis}-{versi}.{ext}` (enum doc_kind, Database.md §3).
var JenisDokumenSah = map[string]bool{
	"invoice_pdf":      true,
	"surat_jalan_xlsx": true,
	"surat_jalan_pdf":  true,
	"report_xlsx":      true,
}

// OpsiRekonsiliasi mengatur pemeriksaan DB↔object storage.
type OpsiRekonsiliasi struct {
	// Hapus=false (bawaan) berarti HANYA MELAPORKAN (dry-run).
	Hapus bool
	// Grace adalah umur minimum objek yatim sebelum boleh disapu. Objek yang
	// lebih muda bisa jadi sedang ditulis oleh render yang belum commit.
	Grace time.Duration
	// Sekarang adalah acuan waktu (pengujian deterministik).
	Sekarang time.Time
}

// DilewatiSapu mencatat kandidat yang sengaja TIDAK disapu beserta alasannya.
type DilewatiSapu struct {
	Kunci  string `json:"kunci"`
	Alasan string `json:"alasan"`
}

// LaporanRekonsiliasi adalah hasil pemeriksaan DB↔object storage.
type LaporanRekonsiliasi struct {
	KunciDB          int               `json:"kunci_db"`
	ObjekStorage     int               `json:"objek_storage"`
	BarisTanpaObjek  []string          `json:"baris_tanpa_objek,omitempty"`
	ObjekTanpaBaris  []string          `json:"objek_tanpa_baris,omitempty"`
	ProofsTanpaBaris []string          `json:"proofs_tanpa_baris,omitempty"`
	KandidatSapu     []string          `json:"kandidat_sapu,omitempty"`
	Dilewati         []DilewatiSapu    `json:"dilewati,omitempty"`
	Disapu           []string          `json:"disapu,omitempty"`
	GagalHapus       map[string]string `json:"gagal_hapus,omitempty"`
	DryRun           bool              `json:"dry_run"`
	GraceJam         int               `json:"grace_jam"`
}

// Rekonsiliasi membandingkan kunci objek yang DIRUJUK database dengan objek
// yang benar-benar ada di storage, lalu — hanya bila diminta — menyapu objek
// yatim `documents/...` secara konservatif.
//
// Yang TIDAK pernah dilakukan:
//   - menghapus apa pun di bawah `proofs/` (retensi bukti transfer tanpa batas,
//     keputusan pemilik: "jangan hapus bukti asli");
//   - menghapus kunci yang tidak mengikuti pola `documents/{order}/{jenis}-{versi}.{ext}`
//     dengan jenis yang dikenal (versi lama dan berkas asing selalu aman);
//   - menghapus objek yang belum melewati grace period (kemungkinan sedang
//     ditulis render yang belum commit — risiko yang dibawa dari F1-17);
//   - menghapus objek yang masih dirujuk baris `documents` mana pun.
func (s *Service) Rekonsiliasi(ctx context.Context, opsi OpsiRekonsiliasi) (LaporanRekonsiliasi, error) {
	if s.objek == nil {
		return LaporanRekonsiliasi{}, errCadanganTanpaObjek
	}
	if err := s.objek.Health(ctx); err != nil {
		return LaporanRekonsiliasi{}, fmt.Errorf("%w: %v", errCadanganTanpaObjek, err)
	}
	if opsi.Grace <= 0 {
		opsi.Grace = s.cfg.SweepGrace
	}
	if opsi.Sekarang.IsZero() {
		opsi.Sekarang = s.clk.Now().UTC()
	}

	proofs, err := s.repo.KunciProofs(ctx)
	if err != nil {
		return LaporanRekonsiliasi{}, fmt.Errorf("baca kunci bukti transfer: %w", err)
	}
	dokumen, err := s.repo.KunciDokumen(ctx)
	if err != nil {
		return LaporanRekonsiliasi{}, fmt.Errorf("baca kunci dokumen: %w", err)
	}
	dirujuk := map[string]bool{}
	for _, k := range append(append([]string{}, proofs...), dokumen...) {
		dirujuk[k] = true
	}

	objekStorage := []string{}
	for _, prefix := range []string{"proofs/", "documents/"} {
		daftar, err := s.objek.List(ctx, prefix)
		if err != nil {
			return LaporanRekonsiliasi{}, fmt.Errorf("daftar objek %s: %w", prefix, err)
		}
		for _, info := range daftar {
			objekStorage = append(objekStorage, info.Key)
		}
	}
	sort.Strings(objekStorage)

	laporan := LaporanRekonsiliasi{
		KunciDB:      len(dirujuk),
		ObjekStorage: len(objekStorage),
		DryRun:       !opsi.Hapus,
		GraceJam:     int(opsi.Grace.Hours()),
		GagalHapus:   map[string]string{},
	}
	adaDStorage := map[string]bool{}
	for _, key := range objekStorage {
		adaDStorage[key] = true
	}
	for key := range dirujuk {
		if !adaDStorage[key] {
			laporan.BarisTanpaObjek = append(laporan.BarisTanpaObjek, key)
		}
	}
	sort.Strings(laporan.BarisTanpaObjek)

	// Objek tanpa baris DB.
	infoPerKunci := map[string]time.Time{}
	for _, prefix := range []string{"documents/"} {
		daftar, err := s.objek.List(ctx, prefix)
		if err == nil {
			for _, info := range daftar {
				infoPerKunci[info.Key] = info.ModTime
			}
		}
	}
	for _, key := range objekStorage {
		if dirujuk[key] {
			continue
		}
		laporan.ObjekTanpaBaris = append(laporan.ObjekTanpaBaris, key)
		if strings.HasPrefix(key, "proofs/") {
			// Retensi bukti tanpa batas: dilaporkan, TIDAK pernah disapu.
			laporan.ProofsTanpaBaris = append(laporan.ProofsTanpaBaris, key)
			laporan.Dilewati = append(laporan.Dilewati, DilewatiSapu{
				Kunci:  key,
				Alasan: "bukti transfer beretensi tanpa batas: objek yatim dilaporkan tetapi tidak pernah dihapus",
			})
			continue
		}
		if !strings.HasPrefix(key, "documents/") {
			laporan.Dilewati = append(laporan.Dilewati, DilewatiSapu{Kunci: key, Alasan: "di luar prefixes yang dikelola"})
			continue
		}
		if err := BentukKunciDokumen(key); err != nil {
			laporan.Dilewati = append(laporan.Dilewati, DilewatiSapu{Kunci: key, Alasan: "bentuk kunci tidak dikenal: " + err.Error()})
			continue
		}
		mod := infoPerKunci[key]
		if mod.IsZero() {
			laporan.Dilewati = append(laporan.Dilewati, DilewatiSapu{Kunci: key, Alasan: "waktu objek tidak diketahui"})
			continue
		}
		if opsi.Sekarang.Sub(mod) < opsi.Grace {
			laporan.Dilewati = append(laporan.Dilewati, DilewatiSapu{
				Kunci:  key,
				Alasan: fmt.Sprintf("umur objek belum melewati grace period %s (mungkin sedang ditulis render aktif)", opsi.Grace),
			})
			continue
		}
		laporan.KandidatSapu = append(laporan.KandidatSapu, key)
	}

	if !opsi.Hapus {
		s.auditSapuan(ctx, ActionSweepKering, laporan.KandidatSapu, "dry-run: kandidat objek yatim dilaporkan, tidak dihapus")
		return laporan, nil
	}

	for _, key := range laporan.KandidatSapu {
		if err := s.objek.Delete(ctx, key); err != nil {
			laporan.GagalHapus[key] = err.Error()
			continue
		}
		laporan.Disapu = append(laporan.Disapu, key)
	}
	s.auditSapuan(ctx, ActionSweepHapus, laporan.Disapu, "sapu objek yatim documents/ yang tidak dirujuk baris documents")
	return laporan, nil
}

// BentukKunciDokumen memvalidasi pola kunci dokumen
// `documents/{order_id}/{jenis}-{versi}.{ext}`.
func BentukKunciDokumen(key string) error {
	bersih := path.Clean(strings.TrimPrefix(key, "/"))
	bagian := strings.Split(bersih, "/")
	if len(bagian) != 3 || bagian[0] != "documents" {
		return fmt.Errorf("harus `documents/{order_id}/{jenis}-{versi}.{ext}`")
	}
	if strings.TrimSpace(bagian[1]) == "" {
		return fmt.Errorf("order_id kosong")
	}
	nama := bagian[2]
	ext := path.Ext(nama)
	if ext == "" {
		return fmt.Errorf("kunci tanpa ekstensi berkas")
	}
	nama = strings.TrimSuffix(nama, ext)
	idx := strings.LastIndex(nama, "-")
	if idx <= 0 || idx == len(nama)-1 {
		return fmt.Errorf("bagian `{jenis}-{versi}` tidak lengkap")
	}
	jenis := nama[:idx]
	versi := nama[idx+1:]
	if !JenisDokumenSah[jenis] {
		return fmt.Errorf("jenis dokumen %q tidak dikenal", jenis)
	}
	for _, r := range versi {
		if r < '0' || r > '9' {
			return fmt.Errorf("versi %q bukan bilangan", versi)
		}
	}
	return nil
}

// auditSapuan mencatat satu tindakan rekonsiliasi/sapu ke audit_logs.
//
// Aktornya `system` (UUID nol): sapu dijalankan pekerja latar, bukan manusia,
// tetapi tetap wajib terjejak (permintaan pemilik: "audit tindakan").
func (s *Service) auditSapuan(ctx context.Context, aksi string, kunci []string, pesan string) {
	if len(kunci) == 0 && aksi == ActionSweepHapus {
		return
	}
	if len(kunci) == 0 && aksi == ActionSweepKering {
		return
	}
	entri := audit.Entry{
		ActorKind: auth.KindSystem,
		ActorID:   AktorSistem,
		ActorNama: "backup",
		Entity:    EntityBackupSweep,
		Action:    aksi,
		After: map[string]any{
			"jumlah": len(kunci),
			"kunci":  batasi(kunci, 50),
			"pesan":  pesan,
		},
	}
	if err := s.repo.Audit(ctx, entri); err != nil {
		s.log.Error("catat audit sapu objek gagal", "aksi", aksi, "err", err)
	}
}
