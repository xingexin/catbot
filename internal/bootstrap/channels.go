package bootstrap

import (
	"github.com/xingexin/catbot/internal/config"
	"net/http"
	"net/url"
	"strings"

	service "github.com/xingexin/catbot/internal/biz/messaging"
	"github.com/xingexin/catbot/internal/infra/messaging/onebot"
	"github.com/xingexin/catbot/internal/infra/messaging/qqofficial"
)

// This is the composition point for message transports. Keep routing keys stable
// when replacing an implementation to preserve bindings, sessions and jobs.
func registerChannels(a *App, c config.Environment) error {
	official := qqofficial.New(qqofficial.Options{
		AppID: c.App.QQAppID, Secret: c.QQSecret, BaseURL: c.QQBaseURL,
	}, qqofficial.QQTokenCache{Store: a.Store, Vault: a.Vault}, a.Messaging.IncomingHandler("official"))
	if err := a.Messaging.RegisterChannel("official", service.Channel{
		Title: "QQ 官方私聊", Implementation: "QQ 官方 API", Sender: official,
		Status: official, Receive: http.HandlerFunc(official.Receive), Binding: a.Messaging.OfficialChannelBinding,
	}); err != nil {
		return err
	}

	endpoint := c.OneBotURL
	personal := onebot.New(onebot.Options{URL: endpoint, Token: c.OneBotToken}, a.Messaging.IncomingHandler("onebot"))
	implementation, loginURL := "外部 OneBot 服务", ""
	if usesLocalNapCat(c.NapCatEnabled, endpoint) {
		implementation, loginURL = "NapCat", c.NapCatWebURL
	}
	return a.Messaging.RegisterChannel("onebot", service.Channel{
		Title: "QQ 个人号", Implementation: implementation, LoginURL: loginURL,
		Sender: personal, Status: personal, Receive: http.HandlerFunc(personal.Receive), Binding: a.Messaging.OneBotChannelBinding,
	})
}

func usesLocalNapCat(enabled, endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && strings.EqualFold(enabled, "true") && u.Scheme == "http" && u.Host == "napcat:3000" && (u.Path == "" || u.Path == "/")
}
