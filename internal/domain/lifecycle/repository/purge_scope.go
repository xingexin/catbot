package repository

import (
	"context"
	"errors"

	"github.com/xingexin/catbot/internal/domain/lifecycle"
	"github.com/xingexin/catbot/internal/infra/store"
)

var ErrPurgeScopeChanged = errors.New("关联内容已变化，请重新预览并确认永久删除")

type purgeScopeKey struct{}
type purgeScope map[string]bool

// WithPurgeScope attaches the administrator's confirmed resource identities to
// this request. Copying the input keeps downstream cleanup from widening it.
func WithPurgeScope(ctx context.Context, approved map[lifecycle.Resource][]string) context.Context {
	scope := purgeScope{}
	for resource, ids := range approved {
		for _, id := range ids {
			scope[lifecycle.ArchiveKey(resource, id)] = true
		}
	}
	return context.WithValue(ctx, purgeScopeKey{}, scope)
}

// RequirePurgeScope is checked while each owner's mutation locks are held.
// Internal caches and snapshots follow their owner; visible resources, including
// privately owned credentials, must have appeared in the confirmation dialog.
// Legacy single-resource callers have no cascade scope and keep their guards.
func RequirePurgeScope(ctx context.Context, refs []store.RecordRef) error {
	scope, scoped := ctx.Value(purgeScopeKey{}).(purgeScope)
	if !scoped {
		return nil
	}
	for _, ref := range refs {
		for resource := lifecycle.ResourceSession; resource <= lifecycle.ResourceSecret; resource++ {
			if ref.Kind == resource.StorageKind() && !scope[lifecycle.ArchiveKey(resource, ref.ID)] {
				return ErrPurgeScopeChanged
			}
		}
	}
	return nil
}
