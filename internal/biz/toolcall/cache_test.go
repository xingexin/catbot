package toolcall

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/domain/conversation"
	repo "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/infra/store"
	"testing"
)

type cacheGuardTasks struct {
	TaskCommands
	calls int
}

func (s *cacheGuardTasks) ControlTaskWithOperation(context.Context, string, string, string) (any, error) {
	s.calls++
	return map[string]string{"result": "triggered"}, nil
}

type purgeBeforeBuiltinLock struct {
	store.Store
	memory    *store.Memory
	operation string
}

func (s purgeBeforeBuiltinLock) Lock(ctx context.Context, key string) (func(), error) {
	if key == "builtin:"+s.operation {
		if err := store.Purge(ctx, s.memory, []store.RecordRef{{Kind: repo.CacheResultStorageKind, ID: s.operation}}, nil); err != nil {
			return nil, err
		}
	}
	return s.Store.Lock(ctx, key)
}
func TestBuiltinCacheTombstoneIsCheckedBeforeExternalTaskControl(t *testing.T) {
	memory := store.NewMemory()
	s := purgeBeforeBuiltinLock{Store: memory, memory: memory, operation: "stable-trigger"}
	service := seedListToolRun(t, s, false)
	var run conversation.Run
	if err := s.Get(t.Context(), "run", "run", &run); err != nil {
		t.Fatal(err)
	}
	run.Persona.Tools = []string{"system__task_control"}
	if err := s.Put(t.Context(), "run", run.ID, run); err != nil {
		t.Fatal(err)
	}
	tasks := &cacheGuardTasks{}
	service.Tasks = tasks
	_, err := service.Call(t.Context(), run.ID, "system__task_control", map[string]any{"id": "task", "action": "trigger"}, s.operation)
	if !errors.Is(err, store.ErrPurgedRecord) || tasks.calls != 0 {
		t.Fatal("side effect occurred before tombstone check", tasks.calls, err)
	}
}
