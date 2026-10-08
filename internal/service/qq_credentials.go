package service

import (
	"context"
	"errors"
	"time"

	"github.com/xingexin/catbot/internal/secret"
	"github.com/xingexin/catbot/internal/store"
)

type qqAccess struct {
	ID      string    `json:"id"`
	Expires time.Time `json:"expires"`
}

// QQTokenCache preserves encrypted credentials and existing cache records.
// It exposes only the operations needed to refresh an official QQ token.
type QQTokenCache struct {
	Store store.Store
	Vault *secret.Vault
}

func (c QQTokenCache) Lock(ctx context.Context) (func(), error) {
	return c.Store.Lock(ctx, "qq-access")
}
func (c QQTokenCache) Get(ctx context.Context) (string, time.Time, error) {
	var entry qqAccess
	err := c.Store.Get(ctx, "qq-access", "current", &entry)
	if errors.Is(err, store.ErrNotFound) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if time.Until(entry.Expires) <= time.Minute {
		return "", entry.Expires, nil
	}
	token, err := c.Vault.Get(ctx, entry.ID)
	return token, entry.Expires, err
}
func (c QQTokenCache) Put(ctx context.Context, token string, expires time.Time) error {
	if err := c.Vault.Set(ctx, "qq-access-token", "QQ access token", token); err != nil {
		return err
	}
	return c.Store.Put(ctx, "qq-access", "current", qqAccess{ID: "qq-access-token", Expires: expires})
}
