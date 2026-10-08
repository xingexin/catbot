package messaging

import (
	"encoding/json"
	"net/http"
)

func (a *Service) Webhook(route string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := a.channels[route]
		if !ok || c.Receive == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "message receiver is not configured"})
			return
		}
		c.Receive.ServeHTTP(w, r)
	}
}
