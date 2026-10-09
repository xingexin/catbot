package httptransport

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	lifecyclebiz "github.com/xingexin/catbot/internal/biz/lifecycle"
	agentdomain "github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	"github.com/xingexin/catbot/internal/infra/store"
	"github.com/xingexin/catbot/internal/infra/vault"
)

func TestHTTPLifecycleRequiresAuthentication(t *testing.T) {
	f := newFixture(t)
	f.cookie = nil
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/archives", ""},
		{http.MethodPost, "/api/lifecycle", `{"resource":1,"action":1,"ids":["session"]}`},
	} {
		response := f.request(request.method, request.path, request.body)
		if response.Code != http.StatusUnauthorized {
			t.Fatal(request.path, response.Code, response.Body.String())
		}
	}
}

func TestHTTPLifecycleRejectsUnknownOrNonNumericEnumsAndInvalidBatches(t *testing.T) {
	f := newFixture(t)
	tooManyIDs := make([]string, 101)
	for i := range tooManyIDs {
		tooManyIDs[i] = "same-id"
	}
	overflow, err := json.Marshal(lifecyclebiz.Request{Resource: lifecycle.ResourceSession, Action: lifecycle.ActionArchive, IDs: tooManyIDs})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, body string }{
		{"unknown resource", `{"resource":0,"action":1,"ids":["id"]}`},
		{"future resource", `{"resource":13,"action":1,"ids":["id"]}`},
		{"negative resource", `{"resource":-1,"action":1,"ids":["id"]}`},
		{"string resource", `{"resource":"sessions","action":1,"ids":["id"]}`},
		{"fractional resource", `{"resource":1.5,"action":1,"ids":["id"]}`},
		{"unknown action", `{"resource":1,"action":0,"ids":["id"]}`},
		{"future action", `{"resource":1,"action":4,"ids":["id"]}`},
		{"string action", `{"resource":1,"action":"archive","ids":["id"]}`},
		{"empty batch", `{"resource":1,"action":1,"ids":[]}`},
		{"empty ID", `{"resource":1,"action":1,"ids":[" "]}`},
		{"too many IDs", string(overflow)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := f.request(http.MethodPost, "/api/lifecycle", tc.body)
			if response.Code != http.StatusBadRequest {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
	if rows := lifecycleArchives(t, f); len(rows) != 0 {
		t.Fatal("rejected request changed archive state", rows)
	}
}

func TestHTTPLifecycleSessionArchiveRestoreAndPermanentDelete(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	for _, identity := range []store.RecordRef{{Kind: "config", ID: "fixture-model"}, {Kind: "persona", ID: "fixture-persona"}} {
		if err := f.store.Put(ctx, identity.Kind, identity.ID, map[string]string{"id": identity.ID, "name": identity.ID}); err != nil {
			t.Fatal(err)
		}
	}
	session := conversation.Session{
		ID: "archive-session", Title: "HTTP archive fixture", ConfigID: "fixture-model", PersonaID: "fixture-persona", Channel: "web",
		Summary: "private summary", Messages: []agentdomain.Message{{Role: "user", Content: "private conversation"}},
	}
	if err := f.store.Put(ctx, "session", session.ID, session); err != nil {
		t.Fatal(err)
	}
	run := conversation.Run{ID: "archive-run", SessionID: session.ID, Status: "completed", Prompt: "private prompt", Result: "private result"}
	if err := f.store.Put(ctx, "run", run.ID, run); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Append(ctx, store.EventRecord{RunID: run.ID, Type: "completed", Data: map[string]any{"text": run.Result}, Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	denied := lifecycleRequest(t, f, lifecycle.ResourceSession, lifecycle.ActionPurge, session.ID)
	if len(denied.Succeeded) != 0 || len(denied.Failed) != 1 {
		t.Fatal("active record was permanently deleted", denied)
	}
	archived := lifecycleRequest(t, f, lifecycle.ResourceSession, lifecycle.ActionArchive, session.ID, session.ID)
	if !reflect.DeepEqual(archived.Succeeded, []string{session.ID}) || len(archived.Failed) != 0 {
		t.Fatal("duplicate IDs did not produce one successful operation", archived)
	}
	if rows := lifecycleList(t, f, "/api/sessions"); len(rows) != 0 {
		t.Fatal("archived session remained in normal list", rows)
	}
	var retained conversation.Session
	if err := f.store.Get(ctx, "session", session.ID, &retained); err != nil || !reflect.DeepEqual(retained, session) {
		t.Fatal("archive destroyed or changed conversation contents", retained, err)
	}
	archives := lifecycleArchives(t, f)
	if len(archives) != 1 || archives[0].Resource != lifecycle.ResourceSession || archives[0].RecordID != session.ID || archives[0].Name != session.Title || archives[0].ArchivedAt.IsZero() {
		t.Fatal(archives)
	}
	firstArchive := archives[0]
	if again := lifecycleRequest(t, f, lifecycle.ResourceSession, lifecycle.ActionArchive, session.ID); len(again.Failed) != 0 {
		t.Fatal(again)
	}
	if again := lifecycleArchives(t, f); len(again) != 1 || !again[0].ArchivedAt.Equal(firstArchive.ArchivedAt) {
		t.Fatal("repeat archive changed first timestamp", again)
	}
	if result := lifecycleRequest(t, f, lifecycle.ResourceSession, lifecycle.ActionRestore, session.ID); len(result.Failed) != 0 {
		t.Fatal(result)
	}
	if rows := lifecycleList(t, f, "/api/sessions"); len(rows) != 1 {
		t.Fatal("restored session missing from normal list", rows)
	}
	if rows := lifecycleArchives(t, f); len(rows) != 0 {
		t.Fatal("restore retained archive index", rows)
	}
	if result := lifecycleRequest(t, f, lifecycle.ResourceSession, lifecycle.ActionArchive, session.ID); len(result.Failed) != 0 {
		t.Fatal(result)
	}
	result := lifecycleRequest(t, f, lifecycle.ResourceSession, lifecycle.ActionPurge, session.ID)
	if len(result.Failed) != 0 || !reflect.DeepEqual(result.Succeeded, []string{session.ID}) {
		t.Fatal("purge failed", result)
	}
	// Completed operations retain replay guards even when a QQ session identity
	// is reusable. Repeating deletion of an already purged run is harmless.
	if again := lifecycleRequest(t, f, lifecycle.ResourceRun, lifecycle.ActionPurge, run.ID); len(again.Failed) != 0 {
		t.Fatal("purged operation retry failed", again)
	}
	for _, identity := range []store.RecordRef{{Kind: "session", ID: session.ID}, {Kind: "run", ID: run.ID}} {
		var payload json.RawMessage
		if err := f.store.Get(ctx, identity.Kind, identity.ID, &payload); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("permanent deletion retained payload", identity, err)
		}
	}
	events, err := f.store.Events(ctx, run.ID, 0)
	if err != nil || len(events) != 0 {
		t.Fatal("permanent deletion retained run events", events, err)
	}
	if rows := lifecycleArchives(t, f); len(rows) != 0 {
		t.Fatal("permanent deletion retained archive index", rows)
	}
	if result := lifecycleRequest(t, f, lifecycle.ResourceSession, lifecycle.ActionRestore, session.ID); len(result.Succeeded) != 0 || len(result.Failed) != 1 {
		t.Fatal("purged conversation was restored", result)
	}
}

func TestHTTPLifecycleBatchReportsMissingItemWithoutLosingSuccessfulItem(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Put(t.Context(), "session", "exists", conversation.Session{ID: "exists", Title: "kept"}); err != nil {
		t.Fatal(err)
	}
	result := lifecycleRequest(t, f, lifecycle.ResourceSession, lifecycle.ActionArchive, "exists", "missing", "exists", "missing")
	if !reflect.DeepEqual(result.Succeeded, []string{"exists"}) || len(result.Failed) != 1 || result.Failed[0].ID != "missing" || result.Failed[0].Error == "" {
		t.Fatal("batch did not preserve per-item outcomes", result)
	}
	archives := lifecycleArchives(t, f)
	if len(archives) != 1 || archives[0].RecordID != "exists" {
		t.Fatal(archives)
	}
}

func TestHTTPLifecycleCredentialArchiveIndexNeverContainsCredentialContents(t *testing.T) {
	f := newFixture(t)
	response := f.request(http.MethodPost, "/api/secrets", `{"id":"archive-secret","name":"Mail password","value":"fixture-private-password"}`)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var credential vault.Record
	if err := f.store.Get(t.Context(), "secret", "archive-secret", &credential); err != nil || credential.Ciphertext == "" {
		t.Fatal(credential, err)
	}
	if result := lifecycleRequest(t, f, lifecycle.ResourceSecret, lifecycle.ActionArchive, credential.ID); len(result.Failed) != 0 {
		t.Fatal(result)
	}
	response = f.request(http.MethodGet, "/api/archives", "")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), credential.Ciphertext) || strings.Contains(response.Body.String(), "fixture-private-password") || strings.Contains(response.Body.String(), "ciphertext") {
		t.Fatal("archive response leaked credential contents", response.Code)
	}
	rows := lifecycleArchives(t, f)
	if len(rows) != 1 || rows[0].Resource != lifecycle.ResourceSecret || rows[0].Name != credential.Name {
		t.Fatal(rows)
	}
	if rows := lifecycleList(t, f, "/api/secrets"); len(rows) != 0 {
		t.Fatal("archived credential remained selectable", rows)
	}
	if result := lifecycleRequest(t, f, lifecycle.ResourceSecret, lifecycle.ActionRestore, credential.ID); len(result.Failed) != 0 {
		t.Fatal(result)
	}
	if rows := lifecycleList(t, f, "/api/secrets"); len(rows) != 1 {
		t.Fatal("restored credential disappeared", rows)
	}
}

func TestHTTPLegacyConfigAndPersonaDeleteRoutesArchiveInsteadOfDestroying(t *testing.T) {
	for _, resource := range []lifecycle.Resource{lifecycle.ResourceConfig, lifecycle.ResourcePersona} {
		t.Run(resource.WireName(), func(t *testing.T) {
			f := newFixture(t)
			const id = "legacy-delete-fixture"
			if err := f.store.Put(t.Context(), resource.StorageKind(), id, map[string]string{"id": id, "name": "legacy record", "systemPrompt": "retained content"}); err != nil {
				t.Fatal(err)
			}
			response := f.request(http.MethodDelete, "/api/"+resource.WireName()+"/"+id, "")
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body.String())
			}
			var record map[string]string
			if err := f.store.Get(t.Context(), resource.StorageKind(), id, &record); err != nil || record["systemPrompt"] != "retained content" {
				t.Fatal("legacy DELETE still hard-deletes", record, err)
			}
			if rows := lifecycleList(t, f, "/api/"+resource.WireName()); len(rows) != 0 {
				t.Fatal("legacy DELETE did not hide archived item", rows)
			}
			rows := lifecycleArchives(t, f)
			if len(rows) != 1 || rows[0].Resource != resource || rows[0].RecordID != id {
				t.Fatal(rows)
			}
		})
	}
}

func lifecycleRequest(t *testing.T, f fixture, resource lifecycle.Resource, action lifecycle.Action, ids ...string) lifecyclebiz.Result {
	t.Helper()
	body, err := json.Marshal(lifecyclebiz.Request{Resource: resource, Action: action, IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	response := f.request(http.MethodPost, "/api/lifecycle", string(body))
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var result lifecyclebiz.Result
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Succeeded == nil || result.Failed == nil {
		t.Fatal("invalid per-item result contract", result, err)
	}
	return result
}

func lifecycleArchives(t *testing.T, f fixture) []lifecycle.ArchiveRecord {
	t.Helper()
	response := f.request(http.MethodGet, "/api/archives", "")
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var rows []lifecycle.ArchiveRecord
	if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil || rows == nil {
		t.Fatal("archives must return an array with numeric resource values", err)
	}
	return rows
}

func lifecycleList(t *testing.T, f fixture, path string) []json.RawMessage {
	t.Helper()
	response := f.request(http.MethodGet, path, "")
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil || rows == nil {
		t.Fatal("list must return a JSON array", err)
	}
	return rows
}
