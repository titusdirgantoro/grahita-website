package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Batas panjang nama tampilan yang boleh diubah sendiri oleh admin
// (PRD §4.10). Nilainya sama dengan batas nama pada master data admin
// (users.AdminPatch / BranchInput) supaya satu nama tidak pernah ditolak di
// satu tempat dan diterima di tempat lain.
const (
	MinNama = 2
	MaxNama = 120
)

var (
	// ErrProfileNotEditable dikembalikan bila aktor tidak berhak mengubah
	// profilnya sendiri. Akun cabang mengalaminya: nama, kepala cabang, email,
	// dan telepon cabang adalah data master yang dikelola admin Pusat dan
	// read-only di portal cabang (PRD §3.8).
	ErrProfileNotEditable = errors.New("profil ini dikelola admin Pusat dan tidak dapat diubah sendiri")

	// ErrNamaInvalid dikembalikan bila nama yang dikirim tidak memenuhi batas.
	ErrNamaInvalid = errors.New("nama tidak valid")
)

// NormalizeNama merapikan spasi berlebih dan spasi tepi pada nama. Nama yang
// hanya berisi spasi akan menjadi string kosong sehingga tertolak sebagai
// ErrNamaInvalid, bukan tersimpan sebagai nama kosong.
func NormalizeNama(nama string) string {
	return strings.Join(strings.Fields(nama), " ")
}

// UpdateOwnProfile mengubah nama tampilan akun yang sedang masuk.
//
// Kebijakan yang sengaja dikunci di service (bukan hanya di handler):
//   - hanya aktor ADMIN yang boleh mengubah namanya sendiri; akun cabang
//     ditolak karena nama cabang berasal dari master data Pusat;
//   - email dan role TIDAK pernah ikut berubah — keduanya read-only dan hanya
//     Superadmin yang bisa mengubahnya lewat user management (PRD §4.10).
//
// Aktor yang dikembalikan sudah memuat nama baru sehingga handler bisa
// langsung mengirimkannya tanpa membaca ulang database.
func (s *Service) UpdateOwnProfile(ctx context.Context, actor Actor, nama string) (Actor, error) {
	if !actor.IsAuthenticated() {
		return Actor{}, ErrSessionInvalid
	}
	if actor.Kind != KindAdmin {
		return Actor{}, ErrProfileNotEditable
	}
	nama = NormalizeNama(nama)
	if n := utf8.RuneCountInString(nama); n < MinNama || n > MaxNama {
		return Actor{}, fmt.Errorf("%w: nama harus %d-%d karakter", ErrNamaInvalid, MinNama, MaxNama)
	}
	if err := s.users.UpdateName(ctx, actor.Kind, actor.UserID, nama); err != nil {
		return Actor{}, err
	}
	actor.Nama = nama
	return actor, nil
}
