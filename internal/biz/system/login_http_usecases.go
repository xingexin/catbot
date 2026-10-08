package system

import (
	"context"
	"errors"
	authinfra "github.com/xingexin/catbot/internal/infra/auth"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
	"time"
)

// LoginWithInput checks the persisted rate limit under its lock before reading
// credentials. Invalid input does not count as a failed password attempt.
func (a *Service) LoginWithInput(ctx context.Context, ip string, input func() (string, error)) (string, error) {
	unlock, err := a.Store.Lock(ctx, "login-rate:"+ip)
	if err != nil {
		return "", err
	}
	defer unlock()
	var rate LoginRate
	if err := a.Store.Get(ctx, "login-rate", ip, &rate); err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if time.Since(rate.Since) > 5*time.Minute {
		rate = LoginRate{Since: time.Now()}
	}
	if rate.Attempts >= 20 {
		return "", ErrRateLimited
	}
	password, err := input()
	if err != nil {
		return "", err
	}
	if !authinfra.PasswordMatches(a.Options.AdminPassword, password) {
		rate.Attempts++
		_ = a.Store.Put(ctx, "login-rate", ip, rate)
		return "", ErrInvalidPassword
	}
	id := idgen.New()
	_ = a.Store.Delete(ctx, "login-rate", ip)
	return id, a.Store.Put(ctx, "login", id, LoginSession{Expires: time.Now().Add(24 * time.Hour)})
}
