package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xingexin/catbot/internal/domain/agent"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
)

func ValidateDefinition(t *taskentity.Task, now time.Time) error {
	if t.Name == "" || len(t.Steps) == 0 || len(t.Steps) > 20 {
		return errors.New("task needs a name and 1..20 steps")
	}
	if t.TimeZone == "" {
		t.TimeZone = "Asia/Shanghai"
	}
	if _, err := time.LoadLocation(t.TimeZone); err != nil {
		return errors.New("invalid time zone")
	}
	switch t.Kind {
	case "once":
		if t.RunAt == nil || t.RunAt.Before(now.Add(-time.Minute)) {
			return errors.New("once task requires a future runAt")
		}
	case "recurring":
		if t.Cron == "" {
			return errors.New("recurring task requires cron")
		}
	case "manual":
	default:
		return errors.New("task kind must be once, recurring or manual")
	}
	if t.CatchupSec == 0 {
		t.CatchupSec = 3600
	}
	if t.CatchupSec < 10 {
		return errors.New("catchupSec must be at least 10")
	}

	return nil
}
func ValidateSteps(t *taskentity.Task, versions map[string]string, tools []agent.Tool) error {
	allowed := map[string]bool{}
	toolOutputs := map[string]map[string]any{}
	for _, tool := range tools {
		allowed[tool.Name] = true
		toolOutputs[tool.Name] = tool.OutputSchema
	}
	seen := map[string]bool{}
	stepOutputs := map[string]map[string]any{}
	dependencies := map[string]string{}
	for _, step := range t.Steps {
		if step.DelaySec < 0 || step.DelaySec > 31*86400 {
			return errors.New("step delay must be 0..2678400 seconds")
		}
		if step.ID == "" || seen[step.ID] {
			return errors.New("step IDs must be unique and nonempty")
		}
		seen[step.ID] = true
		switch step.Kind {
		case "tool":
			if !allowed[step.Tool] {
				return fmt.Errorf("tool not available: %s", step.Tool)
			}
			id := strings.SplitN(step.Tool, "__", 2)[0]
			dependencies[id] = versions[id]
			stepOutputs[step.ID] = toolOutputs[step.Tool]
		case "agent":
			if step.Prompt == "" {
				return errors.New("agent step prompt is required")
			}
			stepOutputs[step.ID] = objectSchema(map[string]any{
				"text": map[string]any{"type": "string"}, "runId": map[string]any{"type": "string"},
			}, "text", "runId")
			for _, tool := range tools {
				dependencies[tool.PluginID] = versions[tool.PluginID]
			}
		default:
			return errors.New("step kind must be tool or agent")
		}
	}
	t.Versions = dependencies
	for _, field := range []struct{ name, expression, kind string }{
		{"notifyWhen", t.NotifyWhen, "boolean"}, {"notifyText", t.NotifyText, "string"},
	} {
		expression := field.expression
		if expression == "" {
			continue
		}
		stepID, err := ReferenceStep(expression)
		if err != nil || len(expression) > 200 {
			return errors.New("notification fields must be a single step reference, for example ${steps.watch.changed}")
		}
		if !seen[stepID] {
			return errors.New("notification references an unknown step")
		}
		if err := ValidateNotificationOutput(expression, stepOutputs[stepID], field.kind); err != nil {
			return fmt.Errorf("%s: %w; omit notifyWhen for unconditional reminders", field.name, err)
		}
	}

	return nil
}
func objectSchema(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
