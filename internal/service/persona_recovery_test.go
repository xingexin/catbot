package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"agentTest/internal/agent"
	"agentTest/internal/domain"
	"agentTest/internal/store"
)

func TestPersonaNativeRecoveryAfterRestart(t *testing.T) {
	t.Parallel()
	for _, interrupted := range []bool{false, true} {
		name := "idle restart resumes native session"
		if interrupted {
			name = "interrupted restart rebuilds from public history"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			var config domain.Config
			if err := a.Store.Get(t.Context(), "config", "config", &config); err != nil {
				t.Fatal(err)
			}
			config.Kind, config.Provider = "sdk", "codebuddy"
			if err := a.Store.Put(t.Context(), "config", config.ID, config); err != nil {
				t.Fatal(err)
			}
			var received []agent.Request
			a.SDK = execFunc(func(_ context.Context, request agent.Request, _ agent.Emit) (agent.Result, error) {
				received = append(received, request)
				return agent.Result{Text: "你好，伙伴", NativeID: "native-persona"}, nil
			})
			first, err := a.Submit(t.Context(), "session", "记住重启前的聊天", "first")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Execute(t.Context(), first.ID); err != nil {
				t.Fatal(err)
			}
			if interrupted {
				crashed, err := a.Submit(t.Context(), "session", "尚未确认的请求", "crashed")
				if err != nil {
					t.Fatal(err)
				}
				crashed.Status = "running"
				if err := a.Store.Put(t.Context(), "run", crashed.ID, crashed); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Bootstrap(t.Context()); err != nil {
				t.Fatal(err)
			}
			next, err := a.Submit(t.Context(), "session", "还记得我吗", "next")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Execute(t.Context(), next.ID); err != nil {
				t.Fatal(err)
			}
			wantNative := "native-persona"
			if interrupted {
				wantNative = ""
			}
			if len(received) != 2 || received[1].NativeID != wantNative {
				t.Fatalf("incorrect native recovery: %#v", received)
			}
			if !reflect.DeepEqual(received[0].Run.Persona, received[1].Run.Persona) {
				t.Fatal("restart changed persona snapshot")
			}
			wantHistory := []domain.Message{{Role: "user", Content: first.Prompt}, {Role: "assistant", Content: "你好，伙伴"}}
			if !reflect.DeepEqual(received[1].History, wantHistory) {
				t.Fatalf("lost completed history or included ambiguous request: %#v", received[1].History)
			}
		})
	}
}

type personaRecoveryStore struct {
	store.Store
	failOperation string
	failure       error
	sessionWrites map[string]int
}

func (s *personaRecoveryStore) Get(ctx context.Context, kind, id string, out any) error {
	if s.failOperation == "get-session" && kind == "session" {
		return s.failure
	}
	return s.Store.Get(ctx, kind, id, out)
}

func (s *personaRecoveryStore) Put(ctx context.Context, kind, id string, value any) error {
	if kind == "session" {
		if s.failOperation == "put-session" {
			return s.failure
		}
		s.sessionWrites[id]++
	}
	if s.failOperation == "put-run" && kind == "run" {
		return s.failure
	}
	return s.Store.Put(ctx, kind, id, value)
}

func TestPersonaRecoveryOnlyClearsInterruptedSessions(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	before := map[string]domain.Session{}
	for _, id := range []string{"affected", "idle"} {
		session := domain.Session{
			ID: id, PersonaID: "secretary", ConfigID: "config", Title: id,
			ActiveConfig: "active", Native: map[string]string{"active": "native-" + id},
			Messages: []domain.Message{{Role: "user", Content: "preserve this conversation"}},
		}
		before[id] = session
		if err := a.Store.Put(t.Context(), "session", id, session); err != nil {
			t.Fatal(err)
		}
	}
	for _, run := range []domain.Run{
		{ID: "running-one", SessionID: "affected", Status: "running"},
		{ID: "running-two", SessionID: "affected", Status: "running"},
		{ID: "orphan-running", SessionID: "missing", Status: "running"},
		{ID: "old-interrupted", SessionID: "idle", Status: "interrupted"},
		{ID: "completed", SessionID: "idle", Status: "completed"},
		{ID: "queued", SessionID: "idle", Status: "queued"},
	} {
		if err := a.Store.Put(t.Context(), "run", run.ID, run); err != nil {
			t.Fatal(err)
		}
	}
	spy := &personaRecoveryStore{Store: a.Store, sessionWrites: map[string]int{}}
	a.Store = spy
	for range 2 {
		if err := a.Bootstrap(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range before {
		if id == "affected" {
			want.ActiveConfig = ""
		}
		var got domain.Session
		if err := a.Store.Get(t.Context(), "session", id, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected changes in %s: %#v", id, got)
		}
	}
	if spy.sessionWrites["affected"] != 1 || spy.sessionWrites["idle"] != 0 || spy.sessionWrites["missing"] != 0 {
		t.Fatalf("unexpected session writes: %v", spy.sessionWrites)
	}
	for _, id := range []string{"running-one", "running-two", "orphan-running"} {
		var run domain.Run
		if err := a.Store.Get(t.Context(), "run", id, &run); err != nil {
			t.Fatal(err)
		}
		if run.Status != "interrupted" || run.FinishedAt == nil {
			t.Fatalf("running request not recovered: %#v", run)
		}
	}
}

func TestPersonaRecoveryStorageFailureCanRetry(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"get-session", "put-session", "put-run"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			a := testApp(t)
			var session domain.Session
			if err := a.Store.Get(t.Context(), "session", "session", &session); err != nil {
				t.Fatal(err)
			}
			session.ActiveConfig = "active"
			session.Native = map[string]string{"active": "ambiguous-native"}
			if err := a.Store.Put(t.Context(), "session", session.ID, session); err != nil {
				t.Fatal(err)
			}
			run := domain.Run{ID: "crashed", SessionID: session.ID, Status: "running"}
			if err := a.Store.Put(t.Context(), "run", run.ID, run); err != nil {
				t.Fatal(err)
			}
			fault := &personaRecoveryStore{Store: a.Store, failOperation: operation, failure: errors.New("store unavailable"), sessionWrites: map[string]int{}}
			a.Store = fault
			if err := a.Bootstrap(t.Context()); !errors.Is(err, fault.failure) {
				t.Fatalf("storage failure not propagated: %v", err)
			}
			if err := fault.Store.Get(t.Context(), "run", run.ID, &run); err != nil {
				t.Fatal(err)
			}
			if run.Status != "running" {
				t.Fatal("failed recovery finalized the run, preventing safe retry")
			}
			fault.failOperation = ""
			if err := a.Bootstrap(t.Context()); err != nil {
				t.Fatal(err)
			}
			session = domain.Session{ID: session.ID}
			if err := a.Store.Get(t.Context(), "session", session.ID, &session); err != nil {
				t.Fatal(err)
			}
			if err := a.Store.Get(t.Context(), "run", run.ID, &run); err != nil {
				t.Fatal(err)
			}
			if session.ActiveConfig != "" || session.Native["active"] != "ambiguous-native" || run.Status != "interrupted" {
				t.Fatal("retry did not safely settle recovery", session, run)
			}
		})
	}
}
