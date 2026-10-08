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
	taskReplyHint := " IDs in results are internal references for subsequent tool calls. In normal replies use task names, readable times and statuses; show IDs only if the user explicitly asks."
	stepSchema := objectSchema(map[string]any{"id": text, "kind": map[string]any{"type": "string", "enum": []string{"tool", "agent"}}, "tool": text, "arguments": map[string]any{"type": "object"}, "prompt": text, "delaySec": map[string]any{"type": "integer", "minimum": 0, "maximum": 2678400}}, "id", "kind")
	taskSchema := objectSchema(map[string]any{
		"name": text, "kind": map[string]any{"type": "string", "enum": []string{"once", "recurring", "manual"}},
		"cron": text, "timeZone": text, "runAt": text, "notify": map[string]any{"type": "boolean"},
		"notifyWhen": map[string]any{"type": "string", "description": "Optional condition referencing a BOOLEAN step output, e.g. ${steps.watch.changed}. Omit for unconditional reminders. Never reference reminder text here."},
		"notifyText": map[string]any{"type": "string", "description": "Optional notification body referencing a STRING step output, e.g. ${steps.reminder.text}."},
		"steps":      map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": stepSchema},
	}, "name", "kind", "steps")
	return []domain.Tool{
		{Name: "system__task_create", Description: "Create a persistent task only when the current user explicitly requests a reminder, notification, timed execution or automation. Advice, plans and proposed schedules alone do not authorize creating reminders; ask first if intent is unclear. Report task status only from actual tool results. kind=once requires runAt RFC3339 with timezone; recurring requires cron and timeZone. Steps call tools or an agent. For a simple text reminder, use example__echo if available with arguments.text, notify=true, notifyText=${steps.reminder.text}, and OMIT notifyWhen. Otherwise use an agent step. notifyWhen is only for BOOLEAN conditions. For new email alerts use one mail__watch step, notifyWhen=${steps.watch.changed}, notifyText=${steps.watch.notificationText}; polling every 5 minutes uses cron= */5 * * * *. First check sets a baseline, no historical alerts." + taskReplyHint, InputSchema: taskSchema, RetrySafe: true},
		{Name: "system__task_list", Description: "List tasks and execution status." + taskReplyHint, InputSchema: objectSchema(map[string]any{}), RetrySafe: true},
		{Name: "system__task_update", Description: "Update an existing task, including rescheduling. Supply id and changed fields. No new duplicate task is created." + taskReplyHint, InputSchema: objectSchema(map[string]any{"id": text, "name": text, "cron": text, "timeZone": text, "runAt": text, "paused": map[string]any{"type": "boolean"}}, "id"), RetrySafe: true},
		{Name: "system__task_control", Description: "Pause, resume, cancel, or immediately trigger a task." + taskReplyHint, InputSchema: objectSchema(map[string]any{"id": text, "action": map[string]any{"type": "string", "enum": []string{"pause", "resume", "cancel", "trigger"}}}, "id", "action"), RetrySafe: true},
		{Name: "system__artifact_list", Description: "List uploaded files and completed analysis artifacts.", InputSchema: objectSchema(map[string]any{}), RetrySafe: true},
		{Name: "system__artifact_read", Description: "Read a saved analysis result by artifact ID.", InputSchema: objectSchema(map[string]any{"id": text}, "id"), RetrySafe: true},
	}
}
func (a *App) Tools(ctx context.Context, r domain.Run) ([]domain.Tool, error) {
	if !r.Config.Capabilities.Tools {
		return []domain.Tool{}, nil
	}
	source, err := a.toolSourceSession(ctx, r.SessionID)
	if err != nil {
		return nil, err
	}
	return a.toolsForSource(ctx, r, source)
}

// Background runs inherit their original conversation's access boundary.
func (a *App) toolSourceSession(ctx context.Context, sessionID string) (domain.Session, error) {
	seen := map[string]bool{}
	for {
		if seen[sessionID] {
			return domain.Session{}, errors.New("task source session contains a cycle")
		}
		seen[sessionID] = true
		var source domain.Session
		if err := a.Store.Get(ctx, "session", sessionID, &source); err != nil {
			// Older internal callers may supply a run without a persisted session.
			// A declared origin must exist: losing it must not expand access.
			if len(seen) == 1 && errors.Is(err, store.ErrNotFound) {
				return domain.Session{ID: sessionID}, nil
			}
			return domain.Session{}, fmt.Errorf("tool source session: %w", err)
		}
		if source.Channel != "task" || source.OriginSessionID == "" {
			return source, nil
		}
		sessionID = source.OriginSessionID
	}
}

func (a *App) toolsForSource(ctx context.Context, r domain.Run, source domain.Session) ([]domain.Tool, error) {
	if !r.Config.Capabilities.Tools || (source.ChannelRoom != "" && r.Persona.Tools == nil) {
		return []domain.Tool{}, nil
	}
	tools, err := a.Plugins.Tools(ctx, r.Versions, r.Persona.Tools)
	if err != nil {
		return nil, err
	}
	for _, t := range builtinTools() {
		// Artifacts have no owner field yet, so none can be disclosed in groups.
		if source.ChannelRoom != "" && (t.Name == "system__artifact_list" || t.Name == "system__artifact_read") {
			continue
		}
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
	source, err := a.toolSourceSession(ctx, r.SessionID)
	if err != nil {
		return nil, err
	}
	tools, err := a.toolsForSource(ctx, r, source)
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
	if source.ChannelRoom != "" {
		if name == "system__task_update" || name == "system__task_control" {
			id, _ := args["id"].(string)
			var task domain.Task
			if err := a.Store.Get(ctx, "task", id, &task); err != nil {
				return nil, err
			}
			// Authorize before consulting cached results or recording a mutation.
			if task.SessionID != source.ID {
				return nil, errors.New("task not authorized for this group conversation")
			}
		}
		// Operation IDs are caller-supplied. Never share a cached group result
		// with another conversation or another built-in tool.
		b, _ := json.Marshal([]string{source.ID, name, opID})
		sum := sha256.Sum256(b)
		opID = "group-" + hex.EncodeToString(sum[:])
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
		var current domain.Session
		if err := a.Store.Get(ctx, "session", r.SessionID, &current); err != nil {
			return nil, err
		}
		if current.Channel == "task" {
			t.SessionID = current.OriginSessionID
			if t.SessionID != "" {
				t.SessionID = source.ID
			}
		}
		if _, provided := args["notify"]; !provided {
			t.Notify = true
		}
		var existing domain.Task
		if getErr := a.Store.Get(ctx, "task", t.ID, &existing); getErr == nil {
			if source.ChannelRoom != "" && existing.SessionID != source.ID {
				return nil, errors.New("task not authorized for this group conversation")
			}
			value = existing
		} else if errors.Is(getErr, store.ErrNotFound) {
			value, err = a.SaveTask(ctx, t)
		} else {
			err = getErr
		}
	case "system__task_list":
		var tasks []domain.Task
		tasks, err = store.All[domain.Task](ctx, a.Store, "task")
		if err == nil && source.ChannelRoom != "" {
			tasks = slices.DeleteFunc(tasks, func(task domain.Task) bool { return task.SessionID != source.ID })
		}
		value = tasks
	case "system__task_update":
		id, _ := args["id"].(string)
		if source.ChannelRoom != "" {
			if a.Scheduler == nil {
				return nil, errors.New("Temporal is not connected")
			}
			unlockTask, lockErr := a.Store.Lock(ctx, "task:"+id)
			if lockErr != nil {
				return nil, lockErr
			}
			defer unlockTask()
		}
		var t domain.Task
		if err = a.Store.Get(ctx, "task", id, &t); err != nil {
			return nil, err
		}
		if source.ChannelRoom != "" && t.SessionID != source.ID {
			return nil, errors.New("task not authorized for this group conversation")
		}
		b, _ := json.Marshal(args)
		if err = json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		if source.ChannelRoom != "" {
			value, err = a.saveTaskLocked(ctx, t)
		} else {
			value, err = a.SaveTask(ctx, t)
		}
	case "system__task_control":
		id, _ := args["id"].(string)
		action, _ := args["action"].(string)
		if source.ChannelRoom != "" {
			value, err = a.controlTaskForSession(ctx, id, action, opID, source.ID)
		} else {
			value, err = a.controlTask(ctx, id, action, opID)
		}
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
