package httptransport

import (
	"errors"
	"github.com/xingexin/catbot/internal/biz/system"
	"net"
	"net/http"
)

const sessionCookie = "secretary_session"

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	id, err := s.services.System.LoginWithInput(r.Context(), ip, func() (string, error) {
		var in struct {
			Password string `json:"password"`
		}
		if err := decode(w, r, &in); err != nil {
			return "", err
		}
		return in.Password, nil
	})
	if err != nil {
		switch {
		case errors.Is(err, system.ErrRateLimited):
			JSON(w, 429, map[string]string{"error": err.Error()})
		case errors.Is(err, system.ErrInvalidPassword):
			JSON(w, 401, map[string]string{"error": err.Error()})
		default:
			fail(w, err)
		}
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: id, Path: "/", HttpOnly: true, Secure: s.options.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
	JSON(w, 200, map[string]string{"username": "admin"})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.services.System.Logout(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.options.CookieSecure})
	JSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			JSON(w, 401, map[string]string{"error": "login required"})
			return
		}
		if !s.services.System.Authorize(r.Context(), cookie.Value) {
			JSON(w, 401, map[string]string{"error": "session expired"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
