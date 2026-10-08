package httptransport

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) registerPlugin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Directory string `json:"directory"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	value, err := s.services.Plugins.Register(r.Context(), in.Directory)
	respond(w, 201, value, err)
}
func (s *Server) configurePlugin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Config map[string]any `json:"config"`
		Grants []string       `json:"grants"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	value, err := s.services.Plugins.ConfigureManaged(r.Context(), r.PathValue("id"), in.Config, in.Grants)
	respond(w, 200, value, err)
}
func (s *Server) enablePlugin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	err := s.services.Plugins.SetPluginEnabled(r.Context(), r.PathValue("id"), in.Enabled)
	respond(w, 200, map[string]bool{"ok": true}, err)
}
func (s *Server) pluginHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err := s.services.Plugins.Health(ctx, r.PathValue("id"))
	respond(w, 200, map[string]string{"status": "ok"}, err)
}
func (s *Server) pluginLogs(w http.ResponseWriter, r *http.Request) {
	value, err := s.services.Plugins.Logs(r.Context(), r.PathValue("id"))
	respond(w, 200, map[string]string{"text": value}, err)
}
func (s *Server) tools(w http.ResponseWriter, r *http.Request) {
	value, err := s.services.Plugins.AvailableTools(r.Context())
	respond(w, 200, value, err)
}
