// Package pluginhost translates authenticated plugin HTTP requests into use cases.
package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	agentbiz "github.com/xingexin/catbot/internal/biz/agent"
	hostbiz "github.com/xingexin/catbot/internal/biz/pluginhost"
	"github.com/xingexin/catbot/internal/domain/artifact"
	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/domain/plugin"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

type TokenVerifier interface{ VerifyToken(string) (string, bool) }
type Host interface {
	Snapshot(context.Context, string) (plugin.Plugin, error)
	Get(context.Context, plugin.Plugin, string) (any, error)
	Put(context.Context, plugin.Plugin, string, any) error
	SaveResult(context.Context, plugin.Plugin, hostbiz.ResultInput) (artifact.Artifact, error)
	SaveTask(context.Context, plugin.Plugin, hostbiz.TaskInput) (taskentity.Task, bool, error)
	Notify(context.Context, plugin.Plugin, hostbiz.NotificationInput) (messaging.Notification, error)
}
type Models interface {
	Generate(context.Context, plugin.Plugin, string, agentbiz.GenerateInput) (map[string]any, error)
	Transcribe(context.Context, plugin.Plugin, string, agentbiz.TranscribeInput) (map[string]any, error)
}
type Files interface {
	Upload(context.Context, string, string, string, io.Reader) (artifact.Artifact, error)
	Open(context.Context, string) (artifact.Artifact, io.ReadSeekCloser, error)
}

type Handler struct {
	tokens      TokenVerifier
	host        Host
	models      Models
	files       Files
	maxUploadMB int
	routes      *http.ServeMux
}
type pluginKey struct{}

func New(tokens TokenVerifier, host Host, models Models, files Files, maxUploadMB int) *Handler {
	h := &Handler{tokens: tokens, host: host, models: models, files: files, maxUploadMB: maxUploadMB, routes: http.NewServeMux()}
	h.routes.HandleFunc("GET /internal/plugin/kv/{key}", h.get)
	h.routes.HandleFunc("PUT /internal/plugin/kv/{key}", h.put)
	h.routes.HandleFunc("POST /internal/plugin/artifacts", h.saveResult)
	h.routes.HandleFunc("GET /internal/plugin/files/{id}", h.download)
	h.routes.HandleFunc("POST /internal/plugin/files", h.upload)
	h.routes.HandleFunc("POST /internal/plugin/generate", h.generate)
	h.routes.HandleFunc("POST /internal/plugin/transcribe", h.transcribe)
	h.routes.HandleFunc("POST /internal/plugin/tasks", h.task)
	h.routes.HandleFunc("POST /internal/plugin/notifications", h.notify)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.tokens.VerifyToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if !ok || !strings.HasPrefix(scope, "plugin:") {
		writeJSON(w, 401, map[string]string{"error": "invalid plugin token"})
		return
	}
	p, err := h.host.Snapshot(r.Context(), strings.TrimPrefix(scope, "plugin:"))
	if err != nil {
		writeJSON(w, 403, map[string]string{"error": "unknown plugin snapshot"})
		return
	}
	permission := "storage"
	switch {
	case strings.Contains(r.URL.Path, "/files"):
		permission = "files"
	case strings.HasSuffix(r.URL.Path, "/generate"), strings.HasSuffix(r.URL.Path, "/transcribe"):
		permission = "models"
	case strings.HasSuffix(r.URL.Path, "/tasks"):
		permission = "tasks"
	case strings.HasSuffix(r.URL.Path, "/notifications"):
		permission = "notifications"
	}
	if !slices.Contains(p.Grants, permission) {
		writeJSON(w, 403, map[string]string{"error": "host permission denied: " + permission})
		return
	}
	h.routes.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), pluginKey{}, p)))
}
func snapshot(r *http.Request) plugin.Plugin { return r.Context().Value(pluginKey{}).(plugin.Plugin) }

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	v, err := h.host.Get(r.Context(), snapshot(r), r.PathValue("key"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"value": v})
}
func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	var v any
	if err := decode(w, r, &v, 2<<20); err != nil {
		fail(w, err)
		return
	}
	if err := h.host.Put(r.Context(), snapshot(r), r.PathValue("key"), v); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (h *Handler) saveResult(w http.ResponseWriter, r *http.Request) {
	var in hostbiz.ResultInput
	if err := decode(w, r, &in, 2<<20); err != nil {
		fail(w, err)
		return
	}
	v, err := h.host.SaveResult(r.Context(), snapshot(r), in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, v)
}
func (h *Handler) generate(w http.ResponseWriter, r *http.Request) {
	var in agentbiz.GenerateInput
	// Frame batches have a separate bound from regular management JSON.
	if err := decode(w, r, &in, 16<<20); err != nil {
		fail(w, errors.New("invalid generation request"))
		return
	}
	result, err := h.models.Generate(r.Context(), snapshot(r), r.Header.Get("X-Secretary-Operation-ID"), in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (h *Handler) transcribe(w http.ResponseWriter, r *http.Request) {
	var in agentbiz.TranscribeInput
	if err := decode(w, r, &in, 2<<20); err != nil {
		fail(w, err)
		return
	}
	result, err := h.models.Transcribe(r.Context(), snapshot(r), r.Header.Get("X-Secretary-Operation-ID"), in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (h *Handler) task(w http.ResponseWriter, r *http.Request) {
	var in hostbiz.TaskInput
	if err := decode(w, r, &in, 2<<20); err != nil {
		fail(w, err)
		return
	}
	v, created, err := h.host.SaveTask(r.Context(), snapshot(r), in)
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, v)
}
func (h *Handler) notify(w http.ResponseWriter, r *http.Request) {
	var in hostbiz.NotificationInput
	if err := decode(w, r, &in, 2<<20); err != nil {
		fail(w, err)
		return
	}
	v, err := h.host.Notify(r.Context(), snapshot(r), in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	limit := int64(h.maxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fail(w, fmt.Errorf("invalid upload or file exceeds %d MB", h.maxUploadMB))
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, err)
		return
	}
	defer file.Close()
	v, err := h.files.Upload(r.Context(), header.Filename, header.Header.Get("Content-Type"), snapshot(r).ID, file)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, v)
}
func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	v, file, err := h.files.Open(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		fail(w, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", strings.ReplaceAll(v.Name, "\"", "")))
	http.ServeContent(w, r, v.Name, v.CreatedAt, file)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON request")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}
