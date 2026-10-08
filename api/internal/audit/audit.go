// Package audit mencatat setiap mutasi admin ke tabel audit_logs
// (SystemDesign.md §5.4, Database.md §4.7).
//
// Entri audit ditulis DI DALAM transaksi yang sama dengan perubahan datanya:
// service menyusun audit.Entry lalu menyerahkannya ke repository, dan
// repository menyimpannya sebelum COMMIT. Dengan begitu tidak mungkin ada
// mutasi yang tercatat tanpa perubahannya, atau sebaliknya.
package audit

import (
	"context"

	"github.com/grahita/portal/api/internal/auth"
)

// Entitas yang dicatat (kolom audit_logs.entity).
const (
	EntityBranch      = "branch"
	EntityBankAccount = "bank_account"
	EntitySetting     = "setting"
)

// Aksi yang dicatat (kolom audit_logs.action).
const (
	ActionCreate        = "create"
	ActionUpdate        = "update"
	ActionDelete        = "delete"
	ActionTokenGenerate = "token_generate"
)

// Entry adalah satu baris audit_logs yang belum disimpan.
//
// Before dan After berisi snapshot JSON dari data sebelum/sesudah mutasi;
// nil berarti tidak ada (mis. pada create, Before nil).
type Entry struct {
	ActorKind auth.Kind
	ActorID   string
	ActorNama string
	IP        string
	Entity    string
	EntityID  string
	Action    string
	Before    any
	After     any
}

// RequestMeta adalah konteks request yang perlu dicatat pada audit log.
type RequestMeta struct {
	IP string
}

type metaKey struct{}

// WithRequestMeta menitipkan metadata request (IP klien) ke context.
func WithRequestMeta(ctx context.Context, m RequestMeta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

// RequestMetaFrom mengambil metadata request; kosong bila tidak ada.
func RequestMetaFrom(ctx context.Context) RequestMeta {
	if m, ok := ctx.Value(metaKey{}).(RequestMeta); ok {
		return m
	}
	return RequestMeta{}
}

// NewEntry menyusun entri audit untuk satu mutasi.
//
// actor diambil eksplisit dari pemanggil (service) supaya otorisasi tetap
// terlihat di tanda tangan fungsi; IP request dibaca dari context karena ia
// metadata transport, bukan bagian dari domain.
func NewEntry(ctx context.Context, actor auth.Actor, entity, entityID, action string, before, after any) Entry {
	return Entry{
		ActorKind: actor.Kind,
		ActorID:   actor.UserID,
		ActorNama: actor.Nama,
		IP:        RequestMetaFrom(ctx).IP,
		Entity:    entity,
		EntityID:  entityID,
		Action:    action,
		Before:    before,
		After:     after,
	}
}
