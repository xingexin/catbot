package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	toolcallbiz "github.com/xingexin/catbot/internal/biz/toolcall"
	"github.com/xingexin/catbot/internal/domain/conversation"
	domain "github.com/xingexin/catbot/internal/domain/lifecycle"
	repository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	task "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
	"strings"
)

// CachePurgeRefs is called while the caller holds ReferenceLock. It returns
// records for the same atomic purge; it never deletes a shared cache or infers
// ownership from result contents or an arbitrary operation ID prefix.
func CachePurgeRefs(ctx context.Context, s store.Store, base []store.RecordRef) ([]store.RecordRef, error) {
	targets := map[repository.CacheOwner]bool{}
	for _, ref := range base {
		resource, err := domain.ParseResourceName(ref.Kind)
		if err == nil && (resource == domain.ResourceRun || resource == domain.ResourceExecution) {
			targets[repository.CacheOwner{Resource: resource, RecordID: ref.ID}] = true
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}
	all, err := store.All[repository.CacheOwnership](ctx, s, repository.CacheOwnerStorageKind)
	if err != nil {
		return nil, err
	}
	candidates := map[store.RecordRef]bool{}
	shared := map[store.RecordRef]bool{}
	result := []store.RecordRef{}
	for _, record := range all {
		if targets[record.Owner] {
			result = append(result, store.RecordRef{Kind: repository.CacheOwnerStorageKind, ID: record.ID})
			for _, ref := range record.References {
				ref.Reusable = false
				candidates[ref] = true
			}
		} else {
			for _, ref := range record.References {
				ref.Reusable = false
				shared[ref] = true
			}
		}
	}
	for owner := range targets {
		var refs []store.RecordRef
		var err error
		switch owner.Resource {
		case domain.ResourceExecution:
			refs, err = legacyExecutionCaches(ctx, s, owner.RecordID)
		case domain.ResourceRun:
			refs, err = legacyRunCaches(ctx, s, owner.RecordID)
		}
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			candidates[ref] = true
		}
	}
	for ref := range candidates {
		if !shared[ref] {
			result = append(result, ref)
		}
	}
	return result, nil
}

type persistedStepKind int

const (
	stepUnknown persistedStepKind = 0
	stepTool    persistedStepKind = 1
	stepAgent   persistedStepKind = 2
)

func parsePersistedStepKind(value string) persistedStepKind {
	switch value {
	case "tool":
		return stepTool
	case "agent":
		return stepAgent
	default:
		return stepUnknown
	}
}
func legacyExecutionCaches(ctx context.Context, s store.Store, id string) ([]store.RecordRef, error) {
	var snapshot task.Snapshot
	if err := s.Get(ctx, "execution-snapshot", id, &snapshot); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	refs := []store.RecordRef{}
	for _, step := range snapshot.Task.Steps {
		if step.ID != "" && parsePersistedStepKind(step.Kind) == stepTool {
			refs = append(refs, store.RecordRef{Kind: repository.CacheOperationStorageKind, ID: id + ":" + step.ID})
		}
	}
	return refs, nil
}

func legacyRunCaches(ctx context.Context, s store.Store, id string) ([]store.RecordRef, error) {
	var run conversation.Run
	if err := s.Get(ctx, "run", id, &run); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if parsePersistedExecutionKind(run.Config.Kind) != executionSDK {
		return nil, nil
	}
	source, sourceErr := (&toolcallbiz.Service{Store: s}).ToolSourceSession(ctx, run.SessionID)
	refs := []store.RecordRef{}
	var after int64
	for {
		events, err := s.Events(ctx, id, after)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			name, operationID := cachedCallFromEvent(event, id)
			if name == "" {
				continue
			}
			if !strings.HasPrefix(name, "system__") {
				refs = append(refs, store.RecordRef{Kind: repository.CacheOperationStorageKind, ID: operationID})
				continue
			}
			if !cachedBuiltinName(name) || sourceErr != nil || source.Channel == "" {
				continue
			}
			if source.ChannelRoom != "" {
				encoded, _ := json.Marshal([]string{source.ID, name, operationID})
				sum := sha256.Sum256(encoded)
				operationID = "group-" + hex.EncodeToString(sum[:])
			}
			refs = append(refs, store.RecordRef{Kind: repository.CacheResultStorageKind, ID: operationID}, store.RecordRef{Kind: repository.CacheStartedStorageKind, ID: operationID})
		}
		if len(events) < 500 {
			return refs, nil
		}
		next := events[len(events)-1].Sequence
		if next <= after {
			return refs, nil
		}
		after = next
	}
}

// Legacy API events only contain provider call IDs, so they cannot establish a
// cache key. SDK events record the exact run-prefixed JSON-RPC operation identity.
func cachedCallFromEvent(event store.EventRecord, runID string) (string, string) {
	switch event.Type {
	case "tool.started", "tool.completed":
	default:
		return "", ""
	}
	name, _ := event.Data["name"].(string)
	id, _ := event.Data["callId"].(string)
	prefix := runID + ":mcp:"
	if name == "" || !strings.HasPrefix(id, prefix) || !json.Valid([]byte(strings.TrimPrefix(id, prefix))) {
		return "", ""
	}
	return name, id
}
func cachedBuiltinName(name string) bool {
	switch name {
	case "system__task_create", "system__task_update", "system__task_control":
		return true
	default:
		return false
	}
}

type persistedExecutionKind int

const (
	executionUnknown persistedExecutionKind = 0
	executionAPI     persistedExecutionKind = 1
	executionSDK     persistedExecutionKind = 2
)

func parsePersistedExecutionKind(value string) persistedExecutionKind {
	switch value {
	case "api":
		return executionAPI
	case "sdk":
		return executionSDK
	default:
		return executionUnknown
	}
}
