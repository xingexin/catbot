package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agentTest/internal/agent"
	"agentTest/internal/domain"
	"agentTest/internal/secret"
	"agentTest/internal/store"
)

type loginSession struct {
	Expires time.Time `json:"expires"`
}
type loginRate struct {
	Attempts int       `json:"attempts"`
	Since    time.Time `json:"since"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, store.ErrNotFound) {
		status = 404
	}
	JSON(w, status, map[string]any{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
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
func (a *App) Handler() http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Store.Ping(r.Context()); err != nil {
			JSON(w, 503, map[string]any{"status": "unhealthy"})
			return
		}
		JSON(w, 200, map[string]any{"status": "ok"})
	})
	root.HandleFunc("POST /api/login", a.login)
	root.HandleFunc("POST /qq/webhook", a.channelWebhook("official"))
	root.HandleFunc("POST /qq/onebot/events", a.channelWebhook("onebot"))
	root.Handle("/internal/", a.internalHandler())
	api := http.NewServeMux()
	api.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("secretary_session"); err == nil {
			_ = a.Store.Delete(r.Context(), "login", c.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: "secretary_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: a.Options.CookieSecure})
		JSON(w, 200, map[string]bool{"ok": true})
	})
	api.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) { JSON(w, 200, map[string]string{"username": "admin"}) })
	api.HandleFunc("GET /api/status", a.status)
	api.HandleFunc("GET /api/qq", a.qqConnections)
	api.HandleFunc("POST /api/mail/connection", a.mailConnection)
	api.HandleFunc("POST /api/mail/watch", a.saveMailWatch)
	api.HandleFunc("POST /api/notifications/{id}/retry", a.retryNotification)
	api.HandleFunc("PUT /api/qq/onebot", a.saveOneBotBinding)
	for path, kind := range map[string]string{"configs": "config", "personas": "persona", "sessions": "session", "runs": "run", "tasks": "task", "executions": "execution", "plugins": "plugin", "artifacts": "artifact", "notifications": "notification", "deliveries": "delivery", "model-calls": "model-call"} {
		api.HandleFunc("GET /api/"+path, func(w http.ResponseWriter, r *http.Request) {
			list, err := a.Store.List(r.Context(), kind)
			if err != nil {
				fail(w, err)
				return
			}
			JSON(w, 200, list)
		})
	}
	api.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		var run domain.Run
		if err := a.Store.Get(r.Context(), "run", r.PathValue("id"), &run); err != nil {
			fail(w, err)
			return
		}
		JSON(w, 200, run)
	})
	api.HandleFunc("POST /api/configs", a.saveConfig)
	api.HandleFunc("POST /api/configs/{id}/test", a.testConfig)
	api.HandleFunc("DELETE /api/configs/{id}", func(w http.ResponseWriter, r *http.Request) { a.deleteReferenced(w, r, "config") })
	api.HandleFunc("GET /api/secrets", func(w http.ResponseWriter, r *http.Request) {
		rs, err := store.All[secret.Record](r.Context(), a.Store, "secret")
		if err != nil {
			fail(w, err)
			return
		}
		out := []map[string]string{}
		for _, v := range rs {
			out = append(out, map[string]string{"id": v.ID, "name": v.Name})
		}
		JSON(w, 200, out)
	})
	api.HandleFunc("POST /api/secrets", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := decode(w, r, &in); err != nil {
			fail(w, err)
			return
		}
		if in.Name == "" || in.Value == "" {
			fail(w, errors.New("name and value are required"))
			return
		}
		if in.ID == "" {
			in.ID = domain.ID()
		}
		if err := a.Vault.Set(r.Context(), in.ID, in.Name, in.Value); err != nil {
			fail(w, err)
			return
		}
		JSON(w, 200, map[string]string{"id": in.ID, "name": in.Name})
	})
	api.HandleFunc("POST /api/personas", a.savePersona)
	api.HandleFunc("DELETE /api/personas/{id}", func(w http.ResponseWriter, r *http.Request) { a.deleteReferenced(w, r, "persona") })
	api.HandleFunc("POST /api/sessions", a.saveSession)
	api.HandleFunc("POST /api/sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Message   string `json:"message"`
			RequestID string `json:"requestId"`
		}
		if err := decode(w, r, &in); err != nil {
			fail(w, err)
			return
		}
		run, err := a.Submit(r.Context(), r.PathValue("id"), in.Message, in.RequestID)
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, 202, run)
	})
	api.HandleFunc("GET /api/runs/{id}/events", a.events)
	api.HandleFunc("POST /api/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := a.CancelRun(r.Context(), r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		JSON(w, 200, map[string]bool{"ok": true})
	})
	api.HandleFunc("POST /api/runs/{id}/retry", a.retryRun)
	api.HandleFunc("POST /api/plugins/register", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Directory string `json:"directory"`
		}
		if err := decode(w, r, &in); err != nil {
			fail(w, err)
			return
		}
		p, err := a.Plugins.Register(r.Context(), in.Directory)
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, 201, p)
	})
	api.HandleFunc("POST /api/plugins/{id}/configure", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Config map[string]any `json:"config"`
			Grants []string       `json:"grants"`
		}
		if err := decode(w, r, &in); err != nil {
			fail(w, err)
			return
		}
		if err := a.validatePluginModels(r.Context(), r.PathValue("id"), in.Config); err != nil {
			fail(w, err)
			return
		}
		p, err := a.Plugins.Configure(r.Context(), r.PathValue("id"), in.Config, in.Grants)
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, 200, p)
	})
	api.HandleFunc("POST /api/plugins/{id}/enable", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if err := decode(w, r, &in); err != nil {
			fail(w, err)
			return
		}
		if err := a.SetPluginEnabled(r.Context(), r.PathValue("id"), in.Enabled); err != nil {
			fail(w, err)
			return
		}
		JSON(w, 200, map[string]bool{"ok": true})
	})
	api.HandleFunc("POST /api/plugins/{id}/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := a.Plugins.Health(ctx, r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		JSON(w, 200, map[string]string{"status": "ok"})
	})
	api.HandleFunc("GET /api/plugins/{id}/logs", a.pluginLogs)
	api.HandleFunc("GET /api/tools", func(w http.ResponseWriter, r *http.Request) {
		vs, err := a.Plugins.Snapshots(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		ts, err := a.Plugins.Tools(r.Context(), vs, nil)
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, 200, append(ts, builtinTools()...))
	})
	api.HandleFunc("POST /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		var in domain.Task
		if err := decode(w, r, &in); err != nil {
			fail(w, err)
			return
		}
		task, err := a.SaveTask(r.Context(), in)
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, 200, task)
	})
	api.HandleFunc("POST /api/tasks/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		value, err := a.ControlTask(r.Context(), r.PathValue("id"), r.PathValue("action"))
		if err != nil {
			fail(w, err)
			return
		}
		JSON(w, 200, value)
	})
	api.HandleFunc("POST /api/files", func(w http.ResponseWriter, r *http.Request) { a.upload(w, r, "") })
	api.HandleFunc("GET /api/files/{id}", a.download)
	root.Handle("/api/", a.auth(api))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != "" {
			u, err := url.Parse(r.Header.Get("Origin"))
			if err != nil || u.Host != r.Host {
				JSON(w, 403, map[string]string{"error": "origin not allowed"})
				return
			}
		}
		root.ServeHTTP(w, r)
	})
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	unlock, err := a.Store.Lock(r.Context(), "login-rate:"+ip)
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	var rate loginRate
	if err := a.Store.Get(r.Context(), "login-rate", ip, &rate); err != nil && !errors.Is(err, store.ErrNotFound) {
		fail(w, err)
		return
	}
	if time.Since(rate.Since) > 5*time.Minute {
		rate = loginRate{Since: time.Now()}
	}
	if rate.Attempts >= 20 {
		JSON(w, 429, map[string]string{"error": "too many login attempts; retry after five minutes"})
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	want := sha256.Sum256([]byte(a.Options.AdminPassword))
	got := sha256.Sum256([]byte(in.Password))
	if a.Options.AdminPassword == "" || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		rate.Attempts++
		_ = a.Store.Put(r.Context(), "login-rate", ip, rate)
		JSON(w, 401, map[string]string{"error": "invalid password"})
		return
	}
	id := domain.ID()
	_ = a.Store.Delete(r.Context(), "login-rate", ip)
	if err := a.Store.Put(r.Context(), "login", id, loginSession{Expires: time.Now().Add(24 * time.Hour)}); err != nil {
		fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "secretary_session", Value: id, Path: "/", HttpOnly: true, Secure: a.Options.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
	JSON(w, 200, map[string]string{"username": "admin"})
}
func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("secretary_session")
		if err != nil {
			JSON(w, 401, map[string]string{"error": "login required"})
			return
		}
		var session loginSession
		if err := a.Store.Get(r.Context(), "login", c.Value, &session); err != nil || session.Expires.Before(time.Now()) {
			JSON(w, 401, map[string]string{"error": "session expired"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (a *App) status(w http.ResponseWriter, r *http.Request) {
	result := map[string]any{"database": "ok", "temporal": "unavailable", "runtime": "unavailable", "qqConfigured": a.channelStatus(r.Context(), "official").Configured}
	if err := a.Store.Ping(r.Context()); err != nil {
		result["database"] = "unavailable"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if a.Scheduler != nil && a.Scheduler.Ping(ctx) == nil {
		result["temporal"] = "ok"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", a.Options.RuntimeURL+"/health", nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+a.Options.RuntimeToken)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				var v any
				if json.NewDecoder(resp.Body).Decode(&v) == nil {
					result["runtime"] = v
				}
			}
		}
	}
	JSON(w, 200, result)
}
func (a *App) saveConfig(w http.ResponseWriter, r *http.Request) {
	var c domain.Config
	if err := decode(w, r, &c); err != nil {
		fail(w, err)
		return
	}
	if err := agent.ValidateConfig(&c); err != nil {
		fail(w, err)
		return
	}
	if c.ID == "" {
		c.ID = domain.ID()
	}
	if c.CredentialID != "" {
		if _, err := a.Vault.Get(r.Context(), c.CredentialID); err != nil {
			fail(w, errors.New("credential does not exist"))
			return
		}
	}
	if err := a.Store.Put(r.Context(), "config", c.ID, c); err != nil {
		fail(w, err)
		return
	}
	JSON(w, 200, c)
}
func (a *App) testConfig(w http.ResponseWriter, r *http.Request) {
	var c domain.Config
	if err := a.Store.Get(r.Context(), "config", r.PathValue("id"), &c); err != nil {
		fail(w, err)
		return
	}
	if c.Kind == "sdk" {
		JSON(w, 200, map[string]string{"status": "requires_conversation_test", "message": "Use the conversation page to test this SDK and its authentication."})
		return
	}
	key, err := a.Vault.Get(r.Context(), c.CredentialID)
	if err != nil {
		fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	t, err := (&agent.Model{}).Step(ctx, c, key, "Reply briefly.", []agent.Entry{{Role: "user", Text: "Reply with OK."}}, nil, func(string, map[string]any) error { return nil })
	if err != nil {
		fail(w, err)
		return
	}
	JSON(w, 200, map[string]any{"status": "ok", "reply": t.Text, "usage": t.Usage})
}
func (a *App) savePersona(w http.ResponseWriter, r *http.Request) {
	var p domain.Persona
	if err := decode(w, r, &p); err != nil {
		fail(w, err)
		return
	}
	if p.Name == "" || p.SystemPrompt == "" {
		fail(w, errors.New("name and systemPrompt are required"))
		return
	}
	for _, m := range p.Examples {
		if m.Role != "user" && m.Role != "assistant" {
			fail(w, errors.New("example role must be user or assistant"))
			return
		}
	}
	unlock, err := a.Store.Lock(r.Context(), "personas")
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	if p.ID == "" {
		p.ID = domain.ID()
	}
	var old domain.Persona
	if err := a.Store.Get(r.Context(), "persona", p.ID, &old); err == nil {
		p.Version = old.Version + 1
	} else {
		p.Version = 1
	}
	if p.Default {
		all, err := store.All[domain.Persona](r.Context(), a.Store, "persona")
		if err != nil {
			fail(w, err)
			return
		}
		for _, other := range all {
			if other.ID != p.ID && other.Default {
				other.Default = false
				if err := a.Store.Put(r.Context(), "persona", other.ID, other); err != nil {
					fail(w, err)
					return
				}
			}
		}
	}
	if err := a.Store.Put(r.Context(), "persona", p.ID, p); err != nil {
		fail(w, err)
		return
	}
	JSON(w, 200, p)
}
func (a *App) saveSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		ConfigID  string `json:"configId"`
		PersonaID string `json:"personaId"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	if in.ID == "" {
		in.ID = domain.ID()
	}
	unlock, err := a.Store.Lock(r.Context(), "session:"+in.ID)
	if err != nil {
		fail(w, err)
		return
	}
	defer unlock()
	var c domain.Config
	var p domain.Persona
	if err := a.Store.Get(r.Context(), "config", in.ConfigID, &c); err != nil {
		fail(w, err)
		return
	}
	if err := a.Store.Get(r.Context(), "persona", in.PersonaID, &p); err != nil {
		fail(w, err)
		return
	}
	s := domain.Session{ID: in.ID, Channel: "web", Messages: []domain.Message{}, Native: map[string]string{}}
	if err := a.Store.Get(r.Context(), "session", in.ID, &s); err != nil && !errors.Is(err, store.ErrNotFound) {
		fail(w, err)
		return
	}
	s.Title = in.Title
	s.ConfigID = in.ConfigID
	s.PersonaID = in.PersonaID
	if err := a.Store.Put(r.Context(), "session", s.ID, s); err != nil {
		fail(w, err)
		return
	}
	JSON(w, 200, s)
}
func (a *App) deleteReferenced(w http.ResponseWriter, r *http.Request, kind string) {
	id := r.PathValue("id")
	binding, err := a.oneBotBinding(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if (kind == "config" && binding.ConfigID == id) || (kind == "persona" && binding.PersonaID == id) {
		fail(w, errors.New("record is used by QQ connection defaults"))
		return
	}
	sessions, err := store.All[domain.Session](r.Context(), a.Store, "session")
	if err != nil {
		fail(w, err)
		return
	}
	for _, s := range sessions {
		if (kind == "persona" && s.PersonaID == id) || (kind == "config" && s.ConfigID == id) {
			fail(w, errors.New("record is used by a conversation"))
			return
		}
	}
	tasks, err := store.All[domain.Task](r.Context(), a.Store, "task")
	if err != nil {
		fail(w, err)
		return
	}
	for _, t := range tasks {
		if (kind == "persona" && t.PersonaID == id) || (kind == "config" && t.ConfigID == id) {
			fail(w, errors.New("record is used by a task"))
			return
		}
	}
	if err := a.Store.Delete(r.Context(), kind, id); err != nil {
		fail(w, err)
		return
	}
	JSON(w, 200, map[string]bool{"ok": true})
}
func (a *App) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var run domain.Run
	if err := a.Store.Get(r.Context(), "run", id, &run); err != nil {
		fail(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, errors.New("streaming unavailable"))
		return
	}
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if q := r.URL.Query().Get("after"); q != "" {
		after, _ = strconv.ParseInt(q, 10, 64)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	for {
		events, err := a.Store.Events(r.Context(), id, after)
		if err != nil {
			return
		}
		for _, e := range events {
			b, _ := json.Marshal(e)
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Sequence, b); err != nil {
				return
			}
			after = e.Sequence
		}
		flusher.Flush()
		if err := a.Store.Get(r.Context(), "run", id, &run); err != nil {
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
func (a *App) retryRun(w http.ResponseWriter, r *http.Request) {
	var old domain.Run
	if err := a.Store.Get(r.Context(), "run", r.PathValue("id"), &old); err != nil {
		fail(w, err)
		return
	}
	if old.Status == "running" || old.Status == "queued" || old.Status == "completed" {
		fail(w, errors.New("only failed or interrupted runs may be retried"))
		return
	}
	run, err := a.Submit(r.Context(), old.SessionID, old.Prompt, domain.ID())
	if err != nil {
		fail(w, err)
		return
	}
	JSON(w, 202, run)
}
func (a *App) upload(w http.ResponseWriter, r *http.Request, pluginID string) {
	limit := int64(a.Options.MaxUploadMB) << 20
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fail(w, fmt.Errorf("invalid upload or file exceeds %d MB", a.Options.MaxUploadMB))
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, err)
		return
	}
	defer file.Close()
	id := domain.ID()
	path := filepath.Join(a.Options.DataDir, "files", id)
	dest, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		fail(w, err)
		return
	}
	size, err := io.Copy(dest, io.LimitReader(file, limit+1))
	closeErr := dest.Close()
	if err != nil || closeErr != nil || size > limit {
		_ = os.Remove(path)
		fail(w, fmt.Errorf("upload failed or file exceeds %d MB", a.Options.MaxUploadMB))
		return
	}
	artifact := domain.Artifact{ID: id, Name: filepath.Base(header.Filename), Mime: header.Header.Get("Content-Type"), Size: size, PluginID: pluginID, CreatedAt: time.Now().UTC()}
	if err := a.Store.Put(r.Context(), "artifact", id, artifact); err != nil {
		_ = os.Remove(path)
		fail(w, err)
		return
	}
	JSON(w, 201, artifact)
}
func (a *App) download(w http.ResponseWriter, r *http.Request) {
	var artifact domain.Artifact
	if err := a.Store.Get(r.Context(), "artifact", r.PathValue("id"), &artifact); err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", strings.ReplaceAll(artifact.Name, "\"", "")))
	http.ServeFile(w, r, filepath.Join(a.Options.DataDir, "files", artifact.ID))
}
func (a *App) pluginLogs(w http.ResponseWriter, r *http.Request) {
	var p domain.Plugin
	if err := a.Store.Get(r.Context(), "plugin", r.PathValue("id"), &p); err != nil {
		fail(w, err)
		return
	}
	paths, _ := filepath.Glob(filepath.Join(a.Options.DataDir, "plugin-logs", p.ID+"@*.log"))
	out := ""
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		info, _ := file.Stat()
		if info != nil && info.Size() > 32768 {
			_, _ = file.Seek(-32768, io.SeekEnd)
		}
		b, _ := io.ReadAll(io.LimitReader(file, 32768))
		_ = file.Close()
		out += string(b)
	}
	for _, id := range p.Secrets {
		value, err := a.Vault.Get(r.Context(), id)
		if err == nil && value != "" {
			out = strings.ReplaceAll(out, value, "[REDACTED]")
		}
	}
	JSON(w, 200, map[string]string{"text": out})
}
