package bootstrap

import (
	hostbiz "github.com/xingexin/catbot/internal/biz/pluginhost"
	httptransport "github.com/xingexin/catbot/internal/transport/http"
	"github.com/xingexin/catbot/internal/transport/mcp"
	"github.com/xingexin/catbot/internal/transport/pluginhost"
	"net/http"
)

func (a *App) internalHandler() http.Handler {
	internal := http.NewServeMux()
	internal.Handle("/internal/mcp", mcp.New(&a.Tokens, a.Conversation, a.Tools, a.Conversation))
	host := &hostbiz.Service{Store: a.Store, Tasks: a.Tasks, Notifications: a.Messaging, Artifacts: a.Artifacts}
	internal.Handle("/internal/", pluginhost.New(&a.Tokens, host, a.Agents, a.Artifacts, a.Options.MaxUploadMB))
	return internal
}
func (a *App) Handler() http.Handler {
	return httptransport.New(httptransport.Services{Lifecycle: a.Lifecycle, Conversations: a.Conversation, Personas: a.Personas, Agent: a.Agents, Tasks: a.Tasks, Plugins: a.Plugins, Messaging: a.Messaging, Artifacts: a.Artifacts, Mail: a.Mail, System: a.System}, httptransport.Options{CookieSecure: a.Options.CookieSecure, MaxUploadMB: a.Options.MaxUploadMB}, a.internalHandler()).Handler()
}
