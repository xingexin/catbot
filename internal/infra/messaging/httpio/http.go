// Package httpio provides bounded HTTP I/O shared by the transport adapters.
package httpio

import (
	"encoding/json"
	"net/http"
	"time"
)

func Client() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func Fail(w http.ResponseWriter, err error) { JSON(w, 400, map[string]string{"error": err.Error()}) }
