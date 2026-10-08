package httptransport

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/biz/conversation"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) findRun(w http.ResponseWriter, r *http.Request) {
	value, err := s.services.Conversations.FindRun(r.Context(), r.PathValue("id"))
	respond(w, 200, value, err)
}
func (s *Server) saveSession(w http.ResponseWriter, r *http.Request) {
	var in conversation.SessionInput
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	value, err := s.services.Conversations.SaveSession(r.Context(), in)
	respond(w, 200, value, err)
}
func (s *Server) submitMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Message   string `json:"message"`
		RequestID string `json:"requestId"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	value, err := s.services.Conversations.Submit(r.Context(), r.PathValue("id"), in.Message, in.RequestID)
	respond(w, 202, value, err)
}
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	err := s.services.Conversations.CancelRun(r.Context(), r.PathValue("id"))
	respond(w, 200, map[string]bool{"ok": true}, err)
}
func (s *Server) retryRun(w http.ResponseWriter, r *http.Request) {
	value, err := s.services.Conversations.Retry(r.Context(), r.PathValue("id"))
	respond(w, 202, value, err)
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.services.Conversations.FindRun(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, errors.New("streaming unavailable"))
		return
	}
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if query := r.URL.Query().Get("after"); query != "" {
		after, _ = strconv.ParseInt(query, 10, 64)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	for {
		events, err := s.services.Conversations.Events(r.Context(), id, after)
		if err != nil {
			return
		}
		for _, event := range events {
			data, _ := json.Marshal(event)
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.Sequence, data); err != nil {
				return
			}
			after = event.Sequence
		}
		flusher.Flush()
		run, err = s.services.Conversations.FindRun(r.Context(), id)
		if err != nil {
			return
		}
		if run.Status != "running" && run.Status != "queued" && len(events) < 500 {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
	}
}
