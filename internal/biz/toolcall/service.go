package toolcall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/biz/plugin"
	"github.com/xingexin/catbot/internal/domain/agent"
	artifactdomain "github.com/xingexin/catbot/internal/domain/artifact"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	plugindomain "github.com/xingexin/catbot/internal/domain/plugin"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
	"slices"
	"strings"
)

type TaskCommands interface {
	SaveTask(context.Context, taskentity.Task) (taskentity.Task, error)
	SaveTaskLocked(context.Context, taskentity.Task) (taskentity.Task, error)
	ControlTaskForSession(context.Context, string, string, string, string) (any, error)
	ControlTaskWithOperation(context.Context, string, string, string) (any, error)
	Connected() bool
}
type Service struct {
	Store   store.Store
	Plugins *plugin.Manager
	Tasks   TaskCommands
}

func (a *Service) Tools(ctx context.Context, r conversation.Run) ([]agent.Tool, error) {
	if !r.Config.Capabilities.Tools {
		return []agent.Tool{}, nil
	}
	source, err := a.ToolSourceSession(ctx, r.SessionID)
	if err != nil {
		return nil, err
	}
	return a.ToolsForSource(ctx, r, source)
}

func (a *Service) ToolSourceSession(ctx context.Context, sessionID string) (conversation.Session, error) {
	seen := map[string]bool{}
	for {
		if seen[sessionID] {
			return conversation.Session{}, errors.New("task source session contains a cycle")
		}
		seen[sessionID] = true
		var source conversation.Session
		if err := a.Store.Get(ctx, "session", sessionID, &source); err != nil {
			// Older internal callers may supply a run without a persisted session.
			// A declared origin must exist: losing it must not expand access.
			if len(seen) == 1 && errors.Is(err, store.ErrNotFound) {
				return conversation.Session{ID: sessionID}, nil
			}
			return conversation.Session{}, fmt.Errorf("tool source session: %w", err)
		}
		if source.Channel != "task" || source.OriginSessionID == "" {
			return source, nil
		}
		sessionID = source.OriginSessionID
	}
}

func (a *Service) ToolsForSource(ctx context.Context, r conversation.Run, source conversation.Session) ([]agent.Tool, error) {
	if !r.Config.Capabilities.Tools || (source.ChannelRoom != "" && r.Persona.Tools == nil) {
		return []agent.Tool{}, nil
	}
	tools, err := a.Plugins.Tools(ctx, r.Versions, r.Persona.Tools)
	if err != nil {
		return nil, err
	}
	for _, t := range agent.BuiltinTools() {
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

func (a *Service) Call(ctx context.Context, runID, name string, args map[string]any, opID string) (any, error) {
	var r conversation.Run
	if err := a.Store.Get(ctx, "run", runID, &r); err != nil {
		return nil, err
	}
	if r.Status != "running" {
		return nil, errors.New("run is not active")
	}
	source, err := a.ToolSourceSession(ctx, r.SessionID)
	if err != nil {
		return nil, err
	}
	tools, err := a.ToolsForSource(ctx, r, source)
	if err != nil {
		return nil, err
	}
	var spec *agent.Tool
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
	if err := plugindomain.Validate(spec.InputSchema, args); err != nil {
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
		if err := lifecycleRepository.OwnCaches(ctx, a.Store, lifecycleRepository.CacheOwner{Resource: lifecycle.ResourceRun, RecordID: runID}, store.RecordRef{Kind: lifecycleRepository.CacheOperationStorageKind, ID: opID}); err != nil {
			return nil, err
		}
		return a.Plugins.CallPinned(ctx, key, parts[1], args, opID)
	}
	if source.ChannelRoom != "" {
		if name == "system__task_update" || name == "system__task_control" {
			id, _ := args["id"].(string)
			var task taskentity.Task
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
	cache := name == "system__task_create" || name == "system__task_update" || name == "system__task_control"
	if cache {
		if err := lifecycleRepository.OwnCaches(ctx, a.Store, lifecycleRepository.CacheOwner{Resource: lifecycle.ResourceRun, RecordID: runID}, store.RecordRef{Kind: lifecycleRepository.CacheResultStorageKind, ID: opID}, store.RecordRef{Kind: lifecycleRepository.CacheStartedStorageKind, ID: opID}); err != nil {
			return nil, err
		}
	}
	unlock, err := a.Store.Lock(ctx, "builtin:"+opID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Read-only tools intentionally observe fresh state on each invocation.
	if cache {
		for _, kind := range []string{lifecycleRepository.CacheResultStorageKind, lifecycleRepository.CacheStartedStorageKind} {
			gone, err := store.Purged(ctx, a.Store, kind, opID)
			if err != nil {
				return nil, err
			}
			if gone {
				return nil, store.ErrPurgedRecord
			}
		}
	}
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
		var t taskentity.Task
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(opID))
		t.ID = "task-" + hex.EncodeToString(sum[:12])
		t.ConfigID = r.Config.ID
		t.PersonaID = r.Persona.ID
		t.SessionID = r.SessionID
		var current conversation.Session
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
		var existing taskentity.Task
		if getErr := a.Store.Get(ctx, "task", t.ID, &existing); getErr == nil {
			if source.ChannelRoom != "" && existing.SessionID != source.ID {
				return nil, errors.New("task not authorized for this group conversation")
			}
			value = existing
		} else if errors.Is(getErr, store.ErrNotFound) {
			value, err = a.Tasks.SaveTask(ctx, t)
		} else {
			err = getErr
		}
	case "system__task_list":
		var tasks []taskentity.Task
		tasks, err = activeToolRecords(ctx, a.Store, lifecycle.ResourceTask, func(task taskentity.Task) string { return task.ID })
		if err == nil && source.ChannelRoom != "" {
			tasks = slices.DeleteFunc(tasks, func(task taskentity.Task) bool { return task.SessionID != source.ID })
		}
		value = tasks
	case "system__task_update":
		id, _ := args["id"].(string)
		if source.ChannelRoom != "" {
			if !a.Tasks.Connected() {
				return nil, errors.New("Temporal is not connected")
			}
			unlockTask, lockErr := a.Store.Lock(ctx, "task:"+id)
			if lockErr != nil {
				return nil, lockErr
			}
			defer unlockTask()
		}
		var t taskentity.Task
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
			value, err = a.Tasks.SaveTaskLocked(ctx, t)
		} else {
			value, err = a.Tasks.SaveTask(ctx, t)
		}
	case "system__task_control":
		id, _ := args["id"].(string)
		action, _ := args["action"].(string)
		if source.ChannelRoom != "" {
			value, err = a.Tasks.ControlTaskForSession(ctx, id, action, opID, source.ID)
		} else {
			value, err = a.Tasks.ControlTaskWithOperation(ctx, id, action, opID)
		}
	case "system__artifact_list":
		value, err = activeToolRecords(ctx, a.Store, lifecycle.ResourceArtifact, func(artifact artifactdomain.Artifact) string { return artifact.ID })
	case "system__artifact_read":
		id, _ := args["id"].(string)
		var artifact artifactdomain.Artifact
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

// Read-only lists use the same archive identity boundary as the management API.
// An index read failure must fail the tool rather than expose hidden records.
func activeToolRecords[T any](ctx context.Context, s store.Store, resource lifecycle.Resource, recordID func(T) string) ([]T, error) {
	records, err := store.All[T](ctx, s, resource.StorageKind())
	if err != nil {
		return nil, err
	}
	active := make([]T, 0, len(records))
	for _, record := range records {
		archived, err := lifecycleRepository.Archived(ctx, s, resource, recordID(record))
		if err != nil {
			return nil, err
		}
		if !archived {
			active = append(active, record)
		}
	}
	return active, nil
}
