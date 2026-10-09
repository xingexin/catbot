package repository

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	"github.com/xingexin/catbot/internal/infra/store"
)

var ErrPurged = errors.New("记录已永久删除，不能重新执行或覆盖")
var ErrArchived = errors.New("记录已归档，请先在归档栏恢复")

// RequireActive also permits a new identity, but refuses recycled deleted IDs.
func RequireActive(ctx context.Context, s store.Store, resource lifecycle.Resource, id string) error {
	if id == "" {
		return nil
	}
	purged, err := store.Purged(ctx, s, resource.StorageKind(), id)
	if err != nil {
		return err
	}
	if purged {
		return ErrPurged
	}
	archived, err := Archived(ctx, s, resource, id)
	if err != nil {
		return err
	}
	if archived {
		return ErrArchived
	}
	return nil
}
