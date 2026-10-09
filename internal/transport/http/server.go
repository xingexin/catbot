package httptransport

import (
	"net/http"
	"net/url"

	"github.com/xingexin/catbot/internal/biz/agent"
	"github.com/xingexin/catbot/internal/biz/artifact"
	"github.com/xingexin/catbot/internal/biz/conversation"
	"github.com/xingexin/catbot/internal/biz/lifecycle"
	"github.com/xingexin/catbot/internal/biz/mail"
	"github.com/xingexin/catbot/internal/biz/messaging"
	"github.com/xingexin/catbot/internal/biz/persona"
	"github.com/xingexin/catbot/internal/biz/plugin"
	"github.com/xingexin/catbot/internal/biz/system"
	"github.com/xingexin/catbot/internal/biz/task"
)

// Services explicitly lists application dependencies; transport never owns state.
type Services struct {
	Lifecycle     *lifecycle.Service
	Conversations *conversation.Service
	Personas      *persona.Service
	Agent         *agent.Service
	Tasks         *task.Commands
	Plugins       *plugin.Manager
	Messaging     *messaging.Service
	Artifacts     *artifact.Service
	Mail          *mail.Service
	System        *system.Service
}
type Options struct {
	CookieSecure bool
	MaxUploadMB  int
}
type Server struct {
	services Services
	options  Options
	internal http.Handler
}

func New(services Services, options Options, internal http.Handler) *Server {
	if internal == nil {
		internal = http.NotFoundHandler()
	}
	return &Server{services: services, options: options, internal: internal}
}
func (s *Server) Handler() http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", s.health)
	root.HandleFunc("POST /api/login", s.login)
	root.HandleFunc("POST /qq/webhook", s.webhook("official"))
	root.HandleFunc("POST /qq/onebot/events", s.webhook("onebot"))
	root.Handle("/internal/", s.internal)
	api := http.NewServeMux()
	api.HandleFunc("POST /api/logout", s.logout)
	api.HandleFunc("POST /api/lifecycle", s.lifecycle)
	api.HandleFunc("POST /api/lifecycle/purge-preview", s.purgePreview)
	api.HandleFunc("POST /api/lifecycle/purge-confirm", s.purgeConfirm)
	api.HandleFunc("GET /api/archives", s.archives)
	api.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) { JSON(w, 200, map[string]string{"username": "admin"}) })
	api.HandleFunc("GET /api/status", s.status)
	api.HandleFunc("GET /api/qq", s.qqConnections)
	api.HandleFunc("PUT /api/qq/onebot", s.saveOneBotBinding)
	api.HandleFunc("POST /api/mail/connection", s.mailConnection)
	api.HandleFunc("POST /api/mail/watch", s.saveMailWatch)
	api.HandleFunc("POST /api/notifications/{id}/retry", s.retryNotification)
	for path, kind := range map[string]string{"configs": "config", "personas": "persona", "sessions": "session", "runs": "run", "tasks": "task", "executions": "execution", "plugins": "plugin", "artifacts": "artifact", "notifications": "notification", "deliveries": "delivery", "model-calls": "model-call"} {
		api.HandleFunc("GET /api/"+path, func(w http.ResponseWriter, r *http.Request) {
			value, err := s.services.System.List(r.Context(), kind)
			respond(w, 200, value, err)
		})
	}
	api.HandleFunc("GET /api/runs/{id}", s.findRun)
	api.HandleFunc("GET /api/runs/{id}/events", s.events)
	api.HandleFunc("POST /api/runs/{id}/cancel", s.cancelRun)
	api.HandleFunc("POST /api/runs/{id}/retry", s.retryRun)
	api.HandleFunc("POST /api/configs", s.saveConfig)
	api.HandleFunc("POST /api/configs/{id}/test", s.testConfig)
	api.HandleFunc("DELETE /api/configs/{id}", s.deleteReferenced("config"))
	api.HandleFunc("GET /api/secrets", s.credentials)
	api.HandleFunc("POST /api/secrets", s.saveCredential)
	api.HandleFunc("POST /api/personas", s.savePersona)
	api.HandleFunc("DELETE /api/personas/{id}", s.deleteReferenced("persona"))
	api.HandleFunc("POST /api/sessions", s.saveSession)
	api.HandleFunc("POST /api/sessions/{id}/messages", s.submitMessage)
	api.HandleFunc("POST /api/plugins/register", s.registerPlugin)
	api.HandleFunc("POST /api/plugins/{id}/configure", s.configurePlugin)
	api.HandleFunc("POST /api/plugins/{id}/enable", s.enablePlugin)
	api.HandleFunc("POST /api/plugins/{id}/health", s.pluginHealth)
	api.HandleFunc("GET /api/plugins/{id}/logs", s.pluginLogs)
	api.HandleFunc("GET /api/tools", s.tools)
	api.HandleFunc("POST /api/tasks", s.saveTask)
	api.HandleFunc("POST /api/tasks/{id}/{action}", s.controlTask)
	api.HandleFunc("POST /api/files", s.upload)
	api.HandleFunc("GET /api/files/{id}", s.download)
	root.Handle("/api/", s.auth(api))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != "" {
			u, err := url.Parse(r.Header.Get("Origin"))
			if err != nil || u.Host != r.Host {
				JSON(w, 403, map[string]string{"error": "origin not allowed"})
				return
			}
		}
		root.ServeHTTP(w, r)
	})
}
func (s *Server) webhook(route string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channel, ok := s.services.Messaging.Channel(route)
		if !ok || channel.Receive == nil {
			JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "message receiver is not configured"})
			return
		}
		channel.Receive.ServeHTTP(w, r)
	}
}
