package pluginhost

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	agentbiz "github.com/xingexin/catbot/internal/biz/agent"
	artifactbiz "github.com/xingexin/catbot/internal/biz/artifact"
	"github.com/xingexin/catbot/internal/domain/artifact"
	"github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/auth"
	"github.com/xingexin/catbot/internal/infra/filestore"
	"github.com/xingexin/catbot/internal/infra/store"
)

type hostFixture struct {
	Host
	p plugin.Plugin
}

func (h hostFixture) Snapshot(_ context.Context, key string) (plugin.Plugin, error) {
	if key != "pinned-version" {
		return plugin.Plugin{}, errors.New("missing")
	}
	return h.p, nil
}

type modelFixture struct {
	Models
	calls       int
	p           plugin.Plugin
	operationID string
}

func (m *modelFixture) Generate(_ context.Context, p plugin.Plugin, operationID string, in agentbiz.GenerateInput) (map[string]any, error) {
	m.calls++
	m.p = p
	m.operationID = operationID
	return map[string]any{"text": in.Prompt}, nil
}
func TestHostPassesExplicitSnapshotAndOperationOnlyAfterAuthorization(t *testing.T) {
	tokens := &auth.Tokens{Key: "test-key"}
	p := plugin.Plugin{ID: "video", Manifest: plugin.Manifest{Version: "1.2.3"}, Grants: []string{"models"}}
	models := &modelFixture{}
	for _, tc := range []struct {
		name, token string
		grants      []string
		status      int
	}{
		{"invalid", "invalid", p.Grants, 401},
		{"wrong scope", tokens.Token("run:running"), p.Grants, 401},
		{"unknown snapshot", tokens.Token("plugin:missing"), p.Grants, 403},
		{"missing grant", tokens.Token("plugin:pinned-version"), []string{"storage"}, 403},
		{"authorized", tokens.Token("plugin:pinned-version"), p.Grants, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := p
			snapshot.Grants = tc.grants
			h := New(tokens, hostFixture{p: snapshot}, models, nil, 100)
			r := httptest.NewRequest("POST", "/internal/plugin/generate", strings.NewReader(`{"configId":"vision","prompt":"content"}`))
			r.Header.Set("Authorization", "Bearer "+tc.token)
			r.Header.Set("X-Secretary-Operation-ID", "run:step:tool")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if models.calls != 1 || models.p.ID != "video" || models.p.Manifest.Version != "1.2.3" || models.operationID != "run:step:tool" {
		t.Fatalf("lost explicit invocation context: %+v", models)
	}
}
func TestHostRejectsMalformedJSONBeforeModelInvocation(t *testing.T) {
	tokens := &auth.Tokens{Key: "test-key"}
	models := &modelFixture{}
	p := plugin.Plugin{ID: "video", Grants: []string{"models"}}
	h := New(tokens, hostFixture{p: p}, models, nil, 100)
	for _, body := range []string{"{", `{"prompt":"first"} {"prompt":"second"}`} {
		r := httptest.NewRequest("POST", "/internal/plugin/generate", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tokens.Token("plugin:pinned-version"))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if models.calls != 0 {
		t.Fatal("invalid request reached the provider")
	}
}

func TestDownloadMissingRegisteredFileReturnsNotFound(t *testing.T) {
	state := store.NewMemory()
	metadata := artifact.Artifact{ID: "missing-file", Name: "recorded.txt", Mime: "text/plain", Size: 10}
	if err := state.Put(t.Context(), "artifact", metadata.ID, metadata); err != nil {
		t.Fatal(err)
	}
	files := &artifactbiz.Service{Store: state, Files: filestore.Store{Root: t.TempDir()}}
	tokens := &auth.Tokens{Key: "fixture"}
	p := plugin.Plugin{ID: "reader", Grants: []string{"files"}}
	h := New(tokens, hostFixture{p: p}, nil, files, 100)
	r := httptest.NewRequest("GET", "/internal/plugin/files/"+metadata.ID, nil)
	r.Header.Set("Authorization", "Bearer "+tokens.Token("plugin:pinned-version"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("missing file returned %d: %s", w.Code, w.Body.String())
	}
}
