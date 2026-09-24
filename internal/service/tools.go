package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"agentTest/internal/domain"
	"agentTest/internal/plugin"
	"agentTest/internal/store"
)

func objectSchema(properties map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func builtinTools() []domain.Tool {
	text := map[string]any{"type": "string"}
	stepSchema := objectSchema(map[string]any{"id": text, "kind": map[string]any{"type": "string", "enum": []string{"tool", "agent"}}, "tool": text, "arguments": map[string]any{"type": "object"}, "prompt": text, "delaySec": map[string]any{"type": "integer", "minimum": 0, "maximum": 2678400}}, "id", "kind")
	taskSchema := objectSchema(map[string]any{
		"name": text, "kind": map[string]any{"type": "string", "enum": []string{"once", "recurring", "manual"}},
		"cron": text, "timeZone": text, "runAt": text, "notify": map[string]any{"type": "boolean"},
		"steps": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": stepSchema},
	}, "name", "kind", "steps")
	return []domain.Tool{
		{Name: "system__task_create", Description: "Create a persistent task. kind=once requires runAt RFC3339 with timezone; recurring requires cron and timeZone. Steps call tools or an agent. For a simple reminder use an agent step with the reminder text.", InputSchema: taskSchema, RetrySafe: true},
		{Name: "system__task_list", Description: "List tasks and execution status.", InputSchema: objectSchema(map[string]any{}), RetrySafe: true},
		{Name: "system__task_update", Description: "Update an existing task, including rescheduling. Supply id and changed fields. No new duplicate task is created.", InputSchema: objectSchema(map[string]any{"id": text, "name": text, "cron": text, "timeZone": text, "runAt": text, "paused": map[string]any{"type": "boolean"}}, "id"), RetrySafe: true},
		{Name: "system__task_control", Description: "Pause, resume, cancel, or immediately trigger a task.", InputSchema: objectSchema(map[string]any{"id": text, "action": map[string]any{"type": "string", "enum": []string{"pause", "resume", "cancel", "trigger"}}}, "id", "action"), RetrySafe: true},
		{Name: "system__artifact_list", Description: "List uploaded files and completed analysis artifacts.", InputSchema: objectSchema(map[string]any{}), RetrySafe: true},
		{Name: "system__artifact_read", Description: "Read a saved analysis result by artifact ID.", InputSchema: objectSchema(map[string]any{"id": text}, "id"), RetrySafe: true},
	}
}
func (a *App) Tools(ctx context.Context, r domain.Run) ([]domain.Tool, error) {
	tools, err := a.Plugins.Tools(ctx, r.Versions, r.Persona.Tools)
	if err != nil {
		return nil, err
	}
	for _, t := range builtinTools() {
		if r.Persona.Tools == nil || slices.Contains(r.Persona.Tools, t.Name) {
			tools = append(tools, t)
		}
	}
	return tools, nil
}
func (a *App) Call(ctx context.Context, runID, name string, args map[string]any, opID string) (any, error) {
	var r domain.Run
	if err := a.Store.Get(ctx, "run", runID, &r); err != nil {
		return nil, err
	}
	if r.Status != "running" {
		return nil, errors.New("run is not active")
	}
	tools, err := a.Tools(ctx, r)
	if err != nil {
		return nil, err
	}
	var spec *domain.Tool
	for _, t := range tools {
		if t.Name == name {
			copy := t
			spec = &copy
			break
		}
	}
	if spec == nil {
		return nil, errors.New("tool not authorized for this run")
	}
	if err := plugin.Validate(spec.InputSchema, args); err != nil {
		return nil, err
	}
	if opID == "" {
		b, _ := json.Marshal(args)
		sum := sha256.Sum256(append([]byte(runID+":"+name+":"), b...))
		opID = hex.EncodeToString(sum[:])
	}
	if !strings.HasPrefix(name, "system__") {
		parts := strings.SplitN(name, "__", 2)
		if len(parts) != 2 {
			return nil, errors.New("invalid tool name")
		}
		key, ok := r.Versions[parts[0]]
		if !ok {
			return nil, errors.New("plugin version not pinned")
		}
		return a.Plugins.CallPinned(ctx, key, parts[1], args, opID)
	}
	unlock, err := a.Store.Lock(ctx, "builtin:"+opID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Read-only tools intentionally observe fresh state on each invocation.
	cache := name == "system__task_create" || name == "system__task_update" || name == "system__task_control"
	var value any
	if cache {
		if err := a.Store.Get(ctx, "tool-result", opID, &value); err == nil {
			return value, nil
		}
	}
	guarded := name == "system__task_update" || (name == "system__task_control" && args["action"] != "trigger")
	if guarded {
		var began bool
		if err := a.Store.Get(ctx, "tool-started", opID, &began); err == nil {
			return nil, errors.New("previous task mutation outcome requires inspection; it will not be applied twice")
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err := a.Store.Put(ctx, "tool-started", opID, true); err != nil {
			return nil, err
		}
	}
	switch name {
	case "system__task_create":
		b, _ := json.Marshal(args)
		var t domain.Task
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(opID))
		t.ID = "task-" + hex.EncodeToString(sum[:12])
		t.ConfigID = r.Config.ID
		t.PersonaID = r.Persona.ID
		t.SessionID = r.SessionID
		if _, provided := args["notify"]; !provided {
			t.Notify = true
		}
		var existing domain.Task
		if getErr := a.Store.Get(ctx, "task", t.ID, &existing); getErr == nil {
			value = existing
		} else if errors.Is(getErr, store.ErrNotFound) {
			value, err = a.SaveTask(ctx, t)
		} else {
			err = getErr
		}
	case "system__task_list":
		value, err = store.All[domain.Task](ctx, a.Store, "task")
	case "system__task_update":
		id, _ := args["id"].(string)
		var t domain.Task
		if err = a.Store.Get(ctx, "task", id, &t); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(args)
		if err = json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		value, err = a.SaveTask(ctx, t)
	case "system__task_control":
		id, _ := args["id"].(string)
		action, _ := args["action"].(string)
		value, err = a.controlTask(ctx, id, action, opID)
	case "system__artifact_list":
		value, err = store.All[domain.Artifact](ctx, a.Store, "artifact")
	case "system__artifact_read":
		id, _ := args["id"].(string)
		var artifact domain.Artifact
		err = a.Store.Get(ctx, "artifact", id, &artifact)
		value = artifact
	default:
		err = fmt.Errorf("unknown built-in tool %s", name)
	}
	if err != nil {
		return nil, err
	}
	if cache {
		if err = a.Store.Put(ctx, "tool-result", opID, value); err != nil {
			return nil, err
		}
	}
	return value, nil
}
