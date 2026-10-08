package main

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/xingexin/catbot/internal/service"
	"github.com/xingexin/catbot/internal/transport/onebot"
	"github.com/xingexin/catbot/internal/transport/qqofficial"
)

// This is the composition point for message transports. Keep routing keys stable
// when replacing an implementation to preserve bindings, sessions and jobs.
func registerChannels(a *service.App) error {
	official := qqofficial.New(qqofficial.Options{
		AppID: os.Getenv("QQ_APP_ID"), Secret: os.Getenv("QQ_SECRET"), BaseURL: os.Getenv("QQ_BASE_URL"),
	}, service.QQTokenCache{Store: a.Store, Vault: a.Vault}, a.IncomingHandler("official"))
	if err := a.RegisterChannel("official", service.Channel{
		Title: "QQ 官方私聊", Implementation: "QQ 官方 API", Sender: official,
		Status: official, Receive: http.HandlerFunc(official.Receive), Binding: a.OfficialChannelBinding,
	}); err != nil {
		return err
	}

	endpoint := os.Getenv("ONEBOT_URL")
	personal := onebot.New(onebot.Options{URL: endpoint, Token: os.Getenv("ONEBOT_TOKEN")}, a.IncomingHandler("onebot"))
	implementation, loginURL := "外部 OneBot 服务", ""
	if usesLocalNapCat(env("NAPCAT_ENABLED", "true"), endpoint) {
		implementation, loginURL = "NapCat", os.Getenv("NAPCAT_WEB_URL")
	}
	return a.RegisterChannel("onebot", service.Channel{
		Title: "QQ 个人号", Implementation: implementation, LoginURL: loginURL,
		Sender: personal, Status: personal, Receive: http.HandlerFunc(personal.Receive), Binding: a.OneBotChannelBinding,
	})
}

func usesLocalNapCat(enabled, endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && strings.EqualFold(enabled, "true") && u.Scheme == "http" && u.Host == "napcat:3000" && (u.Path == "" || u.Path == "/")
}
