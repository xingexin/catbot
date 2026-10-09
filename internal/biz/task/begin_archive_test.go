package task

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xingexin/catbot/internal/domain/lifecycle"
	archive "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

func archiveBeginConfig(t *testing.T, s store.Store) {
	t.Helper()
	if err := archive.Mark(t.Context(), s, lifecycle.ArchiveRecord{Resource: lifecycle.ResourceConfig, RecordID: "config", Name: "model", ArchivedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func TestBeginRejectsArchivedConfigurationForNewExecution(t *testing.T) {
	for _, manual := range []bool{false, true} {
		name := "scheduled"
		if manual {
			name = "manual"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := store.NewMemory()
			task := seedBegin(t, s)
			engine := newTestExecution(s, &fakeHost{})
			in := taskentity.Input{TaskID: task.ID, Revision: task.Revision, Manual: manual}
			archiveBeginConfig(t, s)
			_, err := engine.Begin(t.Context(), in, "archived-config-execution")
			var stopped *taskentity.ExecutionError
			if !errors.Is(err, archive.ErrArchived) || !errors.As(err, &stopped) {
				t.Fatalf("archived config must stop new execution without retry: %v", err)
			}
			var execution taskentity.TaskExecution
			if err := s.Get(t.Context(), "execution", "archived-config-execution", &execution); err != nil || execution.Status != "skipped" || !strings.Contains(execution.Error, "模型配置") {
				t.Fatalf("skipped execution must retain actionable reason: %+v %v", execution, err)
			}
			var snapshot taskentity.Snapshot
			if err := s.Get(t.Context(), "execution-snapshot", "archived-config-execution", &snapshot); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("archived config created an executable snapshot: %v", err)
			}
			if err := archive.Unmark(t.Context(), s, lifecycle.ResourceConfig, "config"); err != nil {
				t.Fatal(err)
			}
			if _, err := engine.Begin(t.Context(), in, "restored-config-execution"); err != nil {
				t.Fatalf("restoring configuration did not permit next execution: %v", err)
			}
		})
	}
}

func TestBeginSnapshotRecoverySurvivesConfigurationArchive(t *testing.T) {
	t.Parallel()
	s := &beginFaultStore{Store: store.NewMemory(), kind: "execution"}
	s.remaining.Store(1)
	task := seedBegin(t, s)
	host := &fakeHost{}
	engine := newTestExecution(s, host)
	in := taskentity.Input{TaskID: task.ID, Revision: task.Revision}
	if _, err := engine.Begin(t.Context(), in, "saved-snapshot"); err == nil {
		t.Fatal("expected execution persistence failure after snapshot was saved")
	}
	archiveBeginConfig(t, s)
	snapshot, err := engine.Begin(t.Context(), in, "saved-snapshot")
	if err != nil {
		t.Fatalf("saved snapshot recovery rejected archived configuration: %v", err)
	}
	if snapshot.Config.Model != "original" {
		t.Fatalf("snapshot changed: %+v", snapshot.Config)
	}
	if _, err := engine.ExecuteStep(t.Context(), taskentity.StepInput{Snapshot: snapshot, Step: snapshot.Task.Steps[0], ExecutionID: "saved-snapshot"}); err != nil {
		t.Fatalf("already-started execution did not continue: %v", err)
	}
	if len(host.steps) != 1 {
		t.Fatalf("expected one resumed step, got %d", len(host.steps))
	}
}
