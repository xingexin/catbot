package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type budgetModel struct {
	count        int
	observations []Entry
	turns        int
}

func (m *budgetModel) InputSize(_ Config, _ string, h []Entry, _ []Tool) (int, error) {
	raw, err := json.Marshal(h)
	return len(raw), err
}
func (m *budgetModel) Step(_ context.Context, _ Config, _, _ string, h []Entry, _ []Tool, _ Emit) (Turn, error) {
	m.turns++
	if m.turns == 1 {
		calls := make([]Call, m.count)
		for i := range calls {
			calls[i] = Call{ID: fmt.Sprint(i), Name: "bounded_history", Arguments: "{}"}
		}
		return Turn{Calls: calls}, nil
	}
	for _, entry := range h {
		if entry.Role == "tool" {
			m.observations = append(m.observations, entry)
		}
	}
	return Turn{Text: "done"}, nil
}

type budgetTools struct {
	limits []int
	bodies []string
}

func (c *budgetTools) Call(ctx context.Context, _, _ string, _ map[string]any, _ string) (any, error) {
	limit := ToolResultLimit(ctx)
	c.limits = append(c.limits, limit)
	// A reader can fit its records to the supplied byte budget and keep all
	// structured metadata. No real group messages or external calls are used.
	result := map[string]any{"messages": []string{strings.Repeat("x", max(0, limit-300))}, "nextCursor": "opaque-page-2", "hasMore": true, "coverage": map[string]any{"localOnly": true}}
	raw, _ := json.Marshal(result)
	c.bodies = append(c.bodies, string(raw))
	return result, nil
}

func TestDirectPassesPerBatchBudgetBeforeToolCallAndPreservesPageMetadata(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                         string
		inputBytes, calls, wantLimit int
	}{
		{"default-single", 0, 1, 24 << 10},
		{"default-parallel", 0, 3, 8 << 10},
		{"small-single", 8 << 10, 1, 2 << 10},
		{"small-parallel", 8 << 10, 4, 512},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &budgetModel{count: tc.calls}
			caller := &budgetTools{}
			direct := Direct{Model: model, Tools: caller}
			req := Request{Config: Config{MaxSteps: 2, MaxInputBytes: tc.inputBytes, Capabilities: Capabilities{Tools: true}}, Tools: []Tool{{Name: "bounded_history"}}, Prompt: "read group history"}
			ctx := t.Context()
			_, err := direct.Run(ctx, req, func(string, map[string]any) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if len(caller.limits) != tc.calls || len(model.observations) != tc.calls {
				t.Fatal("tool batch lost", caller.limits, model.observations)
			}
			for i, limit := range caller.limits {
				if limit != tc.wantLimit || len(caller.bodies[i]) > limit {
					t.Fatalf("incorrect budget=%d size=%d", limit, len(caller.bodies[i]))
				}
				var observed map[string]any
				if err := json.Unmarshal([]byte(model.observations[i].Text), &observed); err != nil {
					t.Fatal(err)
				}
				if observed["preview"] != nil || observed["nextCursor"] != "opaque-page-2" || observed["hasMore"] != true || observed["coverage"] == nil {
					t.Fatalf("paginated metadata was replaced with preview: %+v", observed)
				}
			}
			if ToolResultLimit(ctx) != 0 {
				t.Fatal("per-tool budget leaked into caller context")
			}
		})
	}
}

func TestToolResultBudgetMetadataPreservesParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	first := WithToolResultLimit(parent, 2048)
	second := WithToolResultLimit(parent, 4096)
	if ToolResultLimit(first) != 2048 || ToolResultLimit(second) != 4096 || ToolResultLimit(parent) != 0 {
		t.Fatal("tool budgets mixed")
	}
	if ToolResultLimit(WithToolResultLimit(parent, -1)) != 0 {
		t.Fatal("negative budget must be absent")
	}
	cancel()
	if first.Err() != context.Canceled || second.Err() != context.Canceled {
		t.Fatal("tool contexts lost cancellation")
	}
}
