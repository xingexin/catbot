package httptransport

import (
	"errors"
	lifecycleBiz "github.com/xingexin/catbot/internal/biz/lifecycle"
	"github.com/xingexin/catbot/internal/biz/system"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	"github.com/xingexin/catbot/internal/domain/persona"
	"net/http"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.services.System.Ping(r.Context()); err != nil {
		JSON(w, 503, map[string]any{"status": "unhealthy"})
		return
	}
	JSON(w, 200, map[string]any{"status": "ok"})
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	configured := s.services.Messaging.ChannelStatus(r.Context(), "official").Configured
	JSON(w, 200, s.services.System.Status(r.Context(), s.services.Tasks.Ping, configured))
}
func (s *Server) credentials(w http.ResponseWriter, r *http.Request) {
	value, err := s.services.System.Credentials(r.Context())
	respond(w, 200, value, err)
}
func (s *Server) saveCredential(w http.ResponseWriter, r *http.Request) {
	var in system.CredentialInput
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	value, err := s.services.System.SaveCredential(r.Context(), in)
	respond(w, 200, value, err)
}
func (s *Server) saveConfig(w http.ResponseWriter, r *http.Request) {
	var in agent.Config
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	value, err := s.services.Agent.SaveConfig(r.Context(), in)
	respond(w, 200, value, err)
}
func (s *Server) testConfig(w http.ResponseWriter, r *http.Request) {
	value, err := s.services.Agent.TestConfig(r.Context(), r.PathValue("id"))
	respond(w, 200, value, err)
}
func (s *Server) savePersona(w http.ResponseWriter, r *http.Request) {
	var in persona.Persona
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	value, err := s.services.Personas.Save(r.Context(), in)
	respond(w, 200, value, err)
}
func (s *Server) deleteReferenced(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.services.Lifecycle == nil {
			JSON(w, 503, map[string]string{"error": "归档服务不可用"})
			return
		}
		resource, err := lifecycle.ParseResourceName(kind)
		if err == nil {
			var result lifecycleBiz.Result
			result, err = s.services.Lifecycle.Batch(r.Context(), lifecycleBiz.Request{Resource: resource, Action: lifecycle.ActionArchive, IDs: []string{r.PathValue("id")}})
			if err == nil && len(result.Failed) > 0 {
				err = errors.New(result.Failed[0].Error)
			}
		}
		respond(w, 200, map[string]bool{"ok": true}, err)
	}
}
