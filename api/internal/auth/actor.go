// Package auth berisi model aktor, sesi, dan otorisasi berbasis role.
//
// Otorisasi SELALU diperiksa di layer ini (bukan hanya disembunyikan di UI):
// hostname portal tidak pernah menjadi dasar keputusan (Architecture.md §6).
package auth

import (
	"context"
	"errors"
)

// ErrBranchForbidden dikembalikan service bila aktor mencoba menyentuh data
// cabang yang bukan miliknya (isolasi data cabang, Architecture.md §6).
var ErrBranchForbidden = errors.New("tidak berhak mengakses data cabang lain")

// Kind membedakan asal akun.
type Kind string

// Jenis aktor.
const (
	KindBranch Kind = "branch"
	KindAdmin  Kind = "admin"
	KindSystem Kind = "system"
)

// Role adalah peran yang dikenal sistem (PRD §2).
type Role string

// Peran yang dikenal.
const (
	RoleBranch     Role = "branch"
	RoleManager    Role = "manager"
	RoleSuperadmin Role = "superadmin"
)

// Capability adalah satu satuan hak akses yang diperiksa service.
type Capability string

// Daftar kapabilitas — dipetakan langsung dari matriks hak akses PRD §2.1.
const (
	CapCatalogSelf         Capability = "catalog.self"          // lihat katalog + harga cabang sendiri
	CapOrderWrite          Capability = "order.write"           // buat/ubah/submit pesanan
	CapInvoiceOwn          Capability = "invoice.own"           // unduh invoice cabang sendiri
	CapAdminDashboard      Capability = "admin.dashboard"       // dashboard admin (jumlah pesanan)
	CapAdminDashboardValue Capability = "admin.dashboard.value" // nilai rupiah di dashboard
	CapAdminOrders         Capability = "admin.orders"          // riwayat semua cabang
	CapAdminVerify         Capability = "admin.verify"          // verifikasi pembayaran
	CapAdminRevise         Capability = "admin.revise"          // revisi invoice
	CapAdminBranches       Capability = "admin.branches"        // master cabang + token
	CapAdminCatalog        Capability = "admin.catalog"         // produk + matriks harga
	CapAdminBank           Capability = "admin.bank"            // rekening bank
	CapAdminReports        Capability = "admin.reports"         // reporting + ekspor
	CapAdminUsersBranch    Capability = "admin.users.branch"    // kelola akun cabang
	CapAdminUsersAdmin     Capability = "admin.users.admin"     // kelola akun admin
	CapAdminSettings       Capability = "admin.settings"        // pengaturan sistem
	// CapAdminTutorial hanya dimiliki Superadmin: konten halaman tutorial
	// portal cabang (F2-27, PRD §3.8) — Manager dan akun cabang tidak boleh
	// mengubahnya.
	CapAdminTutorial     Capability = "admin.tutorial"      // konten tutorial cabang
	CapAdminAudit        Capability = "admin.audit"         // audit log
	CapAdminMasterImport Capability = "admin.master_import" // preview/apply master Impact
)

var capabilities = map[Role]map[Capability]bool{
	RoleBranch: {
		CapCatalogSelf: true,
		CapOrderWrite:  true,
		CapInvoiceOwn:  true,
	},
	RoleManager: {
		CapAdminDashboard:   true,
		CapAdminOrders:      true,
		CapAdminVerify:      true,
		CapAdminRevise:      true, // tetap dibatasi setting manager_boleh_revisi di service
		CapAdminBranches:    true,
		CapAdminCatalog:     true,
		CapAdminUsersBranch: true,
	},
	RoleSuperadmin: {
		CapAdminDashboard:      true,
		CapAdminDashboardValue: true,
		CapAdminOrders:         true,
		CapAdminVerify:         true,
		CapAdminRevise:         true,
		CapAdminBranches:       true,
		CapAdminCatalog:        true,
		CapAdminBank:           true,
		CapAdminReports:        true,
		CapAdminUsersBranch:    true,
		CapAdminUsersAdmin:     true,
		CapAdminSettings:       true,
		CapAdminTutorial:       true,
		CapAdminAudit:          true,
		CapAdminMasterImport:   true,
	},
}

// Actor adalah pelaku yang sudah terautentikasi. Nilai ini selalu berasal
// dari sesi di database, tidak pernah dari input klien.
type Actor struct {
	UserID    string `json:"user_id"`
	Kind      Kind   `json:"kind"`
	Role      Role   `json:"role"`
	BranchID  string `json:"branch_id,omitempty"`
	Nama      string `json:"nama"`
	Email     string `json:"email"`
	SessionID string `json:"-"`
}

// Anonymous adalah aktor kosong (belum login).
func Anonymous() Actor { return Actor{} }

// IsAuthenticated melaporkan apakah aktor berasal dari sesi yang sah.
func (a Actor) IsAuthenticated() bool { return a.UserID != "" }

// IsAdmin melaporkan apakah aktor adalah staf Pusat.
func (a Actor) IsAdmin() bool { return a.Kind == KindAdmin && a.IsAuthenticated() }

// IsBranch melaporkan apakah aktor adalah akun cabang.
func (a Actor) IsBranch() bool { return a.Kind == KindBranch && a.IsAuthenticated() }

// IsSuperadmin melaporkan apakah aktor punya seluruh akses Manager + khusus.
func (a Actor) IsSuperadmin() bool { return a.Role == RoleSuperadmin }

// Can memeriksa kapabilitas aktor.
func (a Actor) Can(c Capability) bool {
	if !a.IsAuthenticated() {
		return false
	}
	return capabilities[a.Role][c]
}

// CanAny melaporkan apakah aktor punya minimal satu dari kapabilitas yang diminta.
func (a Actor) CanAny(caps ...Capability) bool {
	for _, c := range caps {
		if a.Can(c) {
			return true
		}
	}
	return false
}

// OwnsBranch melaporkan apakah aktor adalah akun cabang pemilik branchID.
//
// Kepemilikan SELALU diambil dari sesi (Actor.BranchID), tidak pernah dari
// parameter yang dikirim klien — inilah validasi ownership branch_id yang
// diwajibkan Architecture.md §6.
func (a Actor) OwnsBranch(branchID string) bool {
	return a.IsBranch() && a.BranchID != "" && branchID != "" && a.BranchID == branchID
}

// RequireBranch memastikan aktor berhak bertindak atas branchID: akun cabang
// hanya untuk cabangnya sendiri, sedangkan staf Pusat yang punya kapabilitas
// master cabang boleh atas nama cabang mana pun.
func (a Actor) RequireBranch(branchID string) error {
	if a.OwnsBranch(branchID) {
		return nil
	}
	if a.Can(CapAdminBranches) {
		return nil
	}
	return ErrBranchForbidden
}

type actorCtxKey struct{}

// WithActor menitipkan aktor ke context request.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, a)
}

// ActorFrom mengambil aktor dari context; aman dipanggil kapan saja.
func ActorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorCtxKey{}).(Actor); ok {
		return a
	}
	return Anonymous()
}
