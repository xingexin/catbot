package httptransport

import (
	"github.com/xingexin/catbot/internal/biz/lifecycle"
	"net/http"
)

func (s *Server) lifecycle(w http.ResponseWriter, r *http.Request) {
	if s.services.Lifecycle == nil {
		JSON(w, 503, map[string]string{"error": "归档服务不可用"})
		return
	}
	var in lifecycle.Request
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	result, err := s.services.Lifecycle.Batch(r.Context(), in)
	respond(w, 200, result, err)
}
func (s *Server) archives(w http.ResponseWriter, r *http.Request) {
	if s.services.Lifecycle == nil {
		JSON(w, 503, map[string]string{"error": "归档服务不可用"})
		return
	}
	result, err := s.services.Lifecycle.Archives(r.Context())
	respond(w, 200, result, err)
}

func (s *Server) purgePreview(w http.ResponseWriter, r *http.Request) {
	if s.services.Lifecycle == nil {
		JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "归档服务不可用"})
		return
	}
	var in lifecycle.PurgeRequest
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	result, err := s.services.Lifecycle.PreviewPurge(r.Context(), in)
	respond(w, http.StatusOK, result, err)
}

func (s *Server) purgeConfirm(w http.ResponseWriter, r *http.Request) {
	if s.services.Lifecycle == nil {
		JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "归档服务不可用"})
		return
	}
	var in lifecycle.PurgeRequest
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	result, err := s.services.Lifecycle.ConfirmPurge(r.Context(), in)
	respond(w, http.StatusOK, result, err)
}
