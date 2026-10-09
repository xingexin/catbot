package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	archive "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/plugin"
	"github.com/xingexin/catbot/internal/infra/store"
)

func markModelConfig(ctx context.Context, s store.Store, id string) error {
	unlock, err := s.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return err
	}
	defer unlock()
	return archive.Mark(ctx, s, lifecycle.ArchiveRecord{Resource: lifecycle.ResourceConfig, RecordID: id, ArchivedAt: time.Now()})
}

func TestModelEntrypointsRejectArchivedConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Service, context.Context) error
	}{
		{"test", func(s *Service, ctx context.Context) error { _, err := s.TestConfig(ctx, "c"); return err }},
		{"generate", func(s *Service, ctx context.Context) error {
			_, err := s.Generate(ctx, plugin.Plugin{ID: "p"}, "operation", GenerateInput{ConfigID: "c", Prompt: "hello"})
			return err
		}},
		{"transcribe", func(s *Service, ctx context.Context) error {
			_, err := s.Transcribe(ctx, plugin.Plugin{ID: "p"}, "operation", TranscribeInput{ConfigID: "c", ArtifactID: "audio"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := store.NewMemory()
			if err := s.Put(t.Context(), "config", "c", agentdomain.Config{ID: "c", Kind: "api", Protocol: "openai-chat"}); err != nil {
				t.Fatal(err)
			}
			if err := markModelConfig(t.Context(), s, "c"); err != nil {
				t.Fatal(err)
			}
			if err := tc.call(&Service{Store: s}, t.Context()); !errors.Is(err, archive.ErrArchived) || !strings.Contains(err.Error(), "切换") {
				t.Fatalf("expected actionable archived configuration error, got %v", err)
			}
			if calls, err := store.All[agentdomain.ModelCall](t.Context(), s, "model-call"); err != nil || len(calls) != 0 {
				t.Fatalf("rejected model call recorded new intent: %+v, %v", calls, err)
			}
		})
	}
}

func TestModelCallRechecksArchiveBeforePersistingIntent(t *testing.T) {
	t.Parallel()
	s := store.NewMemory()
	service := &Service{Store: s}
	c := agentdomain.Config{ID: "c"}
	if err := s.Put(t.Context(), "config", c.ID, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.activeConfig(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := markModelConfig(t.Context(), s, c.ID); err != nil {
		t.Fatal(err)
	}
	_, err = service.withModelCall(t.Context(), plugin.Plugin{ID: "p"}, "op", loaded, "generate", "m", func() (map[string]any, json.RawMessage, error) {
		t.Fatal("archived model was called")
		return nil, nil, nil
	})
	if !errors.Is(err, archive.ErrArchived) {
		t.Fatalf("archive after config lookup was not enforced: %v", err)
	}
}

func TestModelCallCompletesIfConfigArchivedAfterAdmission(t *testing.T) {
	t.Parallel()
	s := store.NewMemory()
	service := &Service{Store: s}
	c := agentdomain.Config{ID: "c", Model: "m"}
	if err := s.Put(t.Context(), "config", c.ID, c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result, err := service.withModelCall(ctx, plugin.Plugin{ID: "p"}, "op", c, "generate", c.Model, func() (map[string]any, json.RawMessage, error) {
		calls, err := store.All[agentdomain.ModelCall](ctx, s, "model-call")
		if err != nil || len(calls) != 1 || calls[0].Status != "running" {
			t.Fatalf("provider called without persisted intent: %+v, %v", calls, err)
		}
		// Archiving during external work must not deadlock or discard its result.
		if err := markModelConfig(ctx, s, c.ID); err != nil {
			t.Fatalf("model I/O held the lifecycle reference lock: %v", err)
		}
		return map[string]any{"text": "done"}, json.RawMessage(`{"outputTokens":3}`), nil
	})
	if err != nil || result["text"] != "done" {
		t.Fatalf("accepted model call did not finish: %+v, %v", result, err)
	}
	calls, err := store.All[agentdomain.ModelCall](ctx, s, "model-call")
	if err != nil || len(calls) != 1 || calls[0].Status != "completed" || calls[0].FinishedAt == nil || string(calls[0].Usage) != `{"outputTokens":3}` {
		t.Fatalf("completed result was not preserved: %+v, %v", calls, err)
	}
}
