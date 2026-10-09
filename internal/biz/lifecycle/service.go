package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	domain "github.com/xingexin/catbot/internal/domain/lifecycle"
	repository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/infra/filestore"
	"github.com/xingexin/catbot/internal/infra/store"
)

type Handler interface {
	Archive(context.Context, string) error
	Restore(context.Context, string) error
	Purge(context.Context, string) error
}

type Service struct {
	Store         store.Store
	Files         filestore.Store
	Handlers      map[domain.Resource]Handler
	CheckBindings func(context.Context, string, string) error
}

type Request struct {
	Resource domain.Resource `json:"resource"`
	Action   domain.Action   `json:"action"`
	IDs      []string        `json:"ids"`
}
type Failure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}
type Result struct {
	Succeeded []string  `json:"succeeded"`
	Failed    []Failure `json:"failed"`
}

func (s *Service) Batch(ctx context.Context, in Request) (Result, error) {
	result := Result{Succeeded: []string{}, Failed: []Failure{}}
	if !in.Resource.Valid() || !in.Action.Valid() {
		return result, errors.New("无效的资源类型或操作枚举")
	}
	if len(in.IDs) == 0 || len(in.IDs) > 100 {
		return result, errors.New("每批请选择 1 至 100 项")
	}
	for _, id := range in.IDs {
		if strings.TrimSpace(id) == "" || len(id) > 512 || strings.ContainsRune(id, '\x00') {
			return result, errors.New("无效的记录 ID")
		}
	}
	seen := map[string]bool{}
	for _, id := range in.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		operationCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.apply(operationCtx, in.Resource, in.Action, id)
		cancel()
		if err != nil {
			result.Failed = append(result.Failed, Failure{ID: id, Error: err.Error()})
		} else {
			result.Succeeded = append(result.Succeeded, id)
		}
	}
	return result, nil
}
func (s *Service) Archives(ctx context.Context) ([]domain.ArchiveRecord, error) {
	return repository.List(ctx, s.Store)
}

func (s *Service) apply(ctx context.Context, r domain.Resource, action domain.Action, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h := s.Handlers[r]; h != nil {
		switch action {
		case domain.ActionArchive:
			return h.Archive(ctx, id)
		case domain.ActionRestore:
			return h.Restore(ctx, id)
		case domain.ActionPurge:
			return h.Purge(ctx, id)
		}
	}
	key := lockKey(r, id)
	if r == domain.ResourceExecution {
		var execution struct {
			TaskID string `json:"taskId"`
		}
		err := s.Store.Get(ctx, r.StorageKind(), id, &execution)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if execution.TaskID != "" {
			key = "task:" + execution.TaskID
		}
	}
	if (r == domain.ResourceTask || r == domain.ResourcePlugin) && s.Handlers[r] == nil {
		return errors.New("此资源的归档服务未配置")
	}
	unlock, err := s.Store.Lock(ctx, key)
	if err != nil {
		return err
	}
	defer unlock()
	referencesUnlock, err := s.Store.Lock(ctx, domain.ReferenceLock)
	if err != nil {
		return err
	}
	defer referencesUnlock()
	var record map[string]any
	if err := s.Store.Get(ctx, r.StorageKind(), id, &record); err != nil {
		if errors.Is(err, store.ErrNotFound) && action == domain.ActionPurge {
			purged, e := store.Purged(ctx, s.Store, r.StorageKind(), id)
			if e != nil {
				return e
			}
			if purged {
				return nil
			}
		}
		return err
	}
	archived, err := repository.Archived(ctx, s.Store, r, id)
	if err != nil {
		return err
	}
	switch action {
	case domain.ActionArchive:
		if archived {
			return nil
		}
		if err := s.check(ctx, r, id, record, false); err != nil {
			return err
		}
		return repository.Mark(ctx, s.Store, domain.ArchiveRecord{Resource: r, RecordID: id, Name: recordName(r, id, record), ArchivedAt: time.Now().UTC()})
	case domain.ActionRestore:
		if !archived {
			return nil
		}
		if err := s.checkRestore(ctx, r, record); err != nil {
			return err
		}
		return repository.Unmark(ctx, s.Store, r, id)
	case domain.ActionPurge:
		if !archived {
			return errors.New("只能永久删除已归档的记录")
		}
		if err := s.check(ctx, r, id, record, true); err != nil {
			return err
		}
		refs, runIDs, err := s.purgeRefs(ctx, r, id, record)
		if err != nil {
			return err
		}
		// File deletion precedes metadata removal. A failed unlink retains the archived
		// record for retry; a failed database commit is also safe to retry after ENOENT.
		if r == domain.ResourceArtifact {
			if filepath.Base(id) != id || id == "." || id == ".." {
				return errors.New("无效文件 ID")
			}
			if err := s.Files.Remove(id); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("删除附件失败: %w", err)
			}
		}
		return store.Purge(ctx, s.Store, refs, runIDs)
	}
	return errors.New("无效操作")
}
func lockKey(r domain.Resource, id string) string {
	switch r {
	case domain.ResourceSession:
		return "session-meta:" + id
	case domain.ResourceRun:
		return "run-state:" + id
	case domain.ResourceNotification:
		return "notification:" + id
	case domain.ResourceDelivery:
		return "delivery:" + id
	case domain.ResourcePersona:
		return "personas"
	default:
		return "lifecycle:" + domain.ArchiveKey(r, id)
	}
}
func recordName(r domain.Resource, id string, record map[string]any) string {
	for _, key := range []string{"name", "title"} {
		if value, ok := record[key].(string); ok && value != "" {
			return value
		}
	}
	// Archive indexes never copy prompts, message bodies, results, or credentials.
	return r.WireName() + " · " + id
}
func decodeRecord[T any](record map[string]any) (T, error) {
	var value T
	b, err := json.Marshal(record)
	if err == nil {
		err = json.Unmarshal(b, &value)
	}
	return value, err
}
