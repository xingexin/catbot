package system

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/xingexin/catbot/internal/config"
	"github.com/xingexin/catbot/internal/infra/store"
	secret "github.com/xingexin/catbot/internal/infra/vault"
	"time"
)

type Service struct {
	Store         store.Store
	Options       config.Options
	Vault         *secret.Vault
	CheckBindings func(context.Context, string, string) error
}
type LoginSession struct {
	Expires time.Time `json:"expires"`
}
type LoginRate struct {
	Attempts int       `json:"attempts"`
	Since    time.Time `json:"since"`
}

var ErrRateLimited = errors.New("too many login attempts; retry after five minutes")
var ErrInvalidPassword = errors.New("invalid password")

func (a *Service) Login(ctx context.Context, ip, password string) (string, error) {
	return a.LoginWithInput(ctx, ip, func() (string, error) { return password, nil })
}
func (a *Service) Authorize(ctx context.Context, id string) bool {
	var s LoginSession
	return a.Store.Get(ctx, "login", id, &s) == nil && !s.Expires.Before(time.Now())
}
func (a *Service) Logout(ctx context.Context, id string) { _ = a.Store.Delete(ctx, "login", id) }
func (a *Service) List(ctx context.Context, kind string) ([]json.RawMessage, error) {
	return a.Store.List(ctx, kind)
}
func (a *Service) Ping(ctx context.Context) error { return a.Store.Ping(ctx) }
