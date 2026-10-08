package httptransport

import (
	"encoding/json"
	"errors"
	"github.com/xingexin/catbot/internal/infra/store"
	"io"
	"net/http"
)

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	}
	JSON(w, status, map[string]any{"error": err.Error()})
}
func respond(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	JSON(w, status, value)
}
func decode(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(value); err != nil {
		return errors.New("invalid JSON request")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}
