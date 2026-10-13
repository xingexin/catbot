package plugin

import (
	"context"
	"github.com/xingexin/catbot/internal/domain/agent"
	"testing"
)

func TestGroupSummaryRequiresAPIModel(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		allowed bool
	}{{"api", true}, {"sdk", false}} {
		t.Run(tc.kind, func(t *testing.T) {
			err := ValidateModels(t.Context(), "group-chat", map[string]any{"summaryConfigId": "model"}, func(context.Context, string) (agent.Config, error) { return agent.Config{Kind: tc.kind}, nil })
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
		})
	}
}
