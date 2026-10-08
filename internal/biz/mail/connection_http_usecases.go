package mail

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/biz/plugin"
	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"time"
)

func (a *Service) TestConnection(ctx context.Context, folder string) (any, error) {
	if folder == "" {
		folder = "INBOX"
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var p domainplugin.Plugin
	if err := a.Store.Get(ctx, "plugin", "mail", &p); err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, errors.New("请先配置并启用邮箱插件")
	}
	key, err := plugin.Pin(ctx, a.Store, p)
	if err != nil {
		return nil, err
	}
	return a.Plugins.CallPinned(ctx, key, "test_connection", map[string]any{"folder": folder}, "mail-test:"+idgen.New())
}
