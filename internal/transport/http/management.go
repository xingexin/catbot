package httptransport

import (
	"github.com/xingexin/catbot/internal/biz/mail"
	"github.com/xingexin/catbot/internal/biz/messaging"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"net/http"
)

func (s *Server) saveTask(w http.ResponseWriter, r *http.Request) {
	var in taskentity.Task
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	value, err := s.services.Tasks.SaveTask(r.Context(), in)
	respond(w, 200, value, err)
}
func (s *Server) controlTask(w http.ResponseWriter, r *http.Request) {
	value, err := s.services.Tasks.ControlTask(r.Context(), r.PathValue("id"), r.PathValue("action"))
	respond(w, 200, value, err)
}
func (s *Server) qqConnections(w http.ResponseWriter, r *http.Request) {
	value, err := s.services.Messaging.Connections(r.Context())
	respond(w, 200, value, err)
}
func (s *Server) saveOneBotBinding(w http.ResponseWriter, r *http.Request) {
	var in messaging.OneBotBinding
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	value, err := s.services.Messaging.SaveOneBotBinding(r.Context(), in)
	respond(w, 200, value, err)
}
func (s *Server) retryNotification(w http.ResponseWriter, r *http.Request) {
	value, err := s.services.Messaging.RetryNotification(r.Context(), r.PathValue("id"))
	respond(w, 200, value, err)
}
func (s *Server) mailConnection(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Folder string `json:"folder"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	value, err := s.services.Mail.TestConnection(r.Context(), in.Folder)
	respond(w, 200, value, err)
}
func (s *Server) saveMailWatch(w http.ResponseWriter, r *http.Request) {
	var in mail.WatchRequest
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	value, err := s.services.Mail.SaveMailWatch(r.Context(), in)
	respond(w, 200, value, err)
}
