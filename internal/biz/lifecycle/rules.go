package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xingexin/catbot/internal/domain/conversation"
	domain "github.com/xingexin/catbot/internal/domain/lifecycle"
	repository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/domain/persona"
	task "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

func (s *Service) check(ctx context.Context, r domain.Resource, id string, record map[string]any, purge bool) error {
	switch r {
	case domain.ResourceRun, domain.ResourceExecution, domain.ResourceNotification, domain.ResourceDelivery, domain.ResourceModelCall:
		if err := checkSettled(record); err != nil {
			return err
		}
	case domain.ResourceSession:
		runs, err := store.All[conversation.Run](ctx, s.Store, "run")
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.SessionID == id {
				if err := checkRun(run); err != nil {
					return err
				}
			}
		}
		if purge {
			tasks, err := store.All[task.Task](ctx, s.Store, "task")
			if err != nil {
				return err
			}
			for _, t := range tasks {
				if t.SessionID == id {
					return fmt.Errorf("会话仍被任务「%s」引用，请先永久删除任务或更换通知会话", t.Name)
				}
			}
			sessions, err := store.All[conversation.Session](ctx, s.Store, "session")
			if err != nil {
				return err
			}
			for _, other := range sessions {
				if other.ID != id && other.OriginSessionID == id {
					return errors.New("会话仍有关联的后台会话，请先清理后台会话")
				}
			}
		}
	case domain.ResourceConfig, domain.ResourcePersona:
		if s.CheckBindings != nil {
			if err := s.CheckBindings(ctx, r.StorageKind(), id); err != nil {
				return err
			}
		}
		if r == domain.ResourcePersona {
			p, err := decodeRecord[persona.Persona](record)
			if err != nil {
				return err
			}
			if p.Default {
				return errors.New("默认人格不能归档或永久删除，请先设置其他默认人格")
			}
		}
		sessions, err := store.All[conversation.Session](ctx, s.Store, "session")
		if err != nil {
			return err
		}
		for _, session := range sessions {
			used := (r == domain.ResourceConfig && session.ConfigID == id) || (r == domain.ResourcePersona && session.PersonaID == id)
			if used {
				return fmt.Errorf("仍被会话「%s」引用，请先清理该会话或修改绑定", session.Title)
			}
		}
		tasks, err := store.All[task.Task](ctx, s.Store, "task")
		if err != nil {
			return err
		}
		for _, t := range tasks {
			if (r == domain.ResourceConfig && t.ConfigID == id) || (r == domain.ResourcePersona && t.PersonaID == id) {
				return fmt.Errorf("仍被任务「%s」引用", t.Name)
			}
		}
		if r == domain.ResourceConfig {
			if err := s.checkReferences(ctx, id, []string{"plugin", "plugin-version"}); err != nil {
				return err
			}
		}
	case domain.ResourceSecret:
		if err := s.checkReferences(ctx, id, []string{"config", "plugin", "plugin-version", "run", "execution", "execution-snapshot"}); err != nil {
			return err
		}
	case domain.ResourceArtifact:
		if purge {
			for _, kind := range []string{"run", "execution"} {
				records, err := s.Store.List(ctx, kind)
				if err != nil {
					return err
				}
				for _, raw := range records {
					var entry struct {
						Status string `json:"status"`
					}
					if err := json.Unmarshal(raw, &entry); err != nil {
						return err
					}
					if domain.ParseActivityState(entry.Status).Mutable() {
						return errors.New("仍有正在执行的请求，请结束后再永久删除文件")
					}
				}
			}
			if err := s.checkReferences(ctx, id, []string{"task", "session", "run", "execution", "execution-snapshot"}); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkSettled(record map[string]any) error {
	raw, _ := record["status"].(string)
	state := domain.ParseActivityState(raw)
	if state == domain.ActivityUnknown || state.Mutable() {
		return errors.New("记录仍在执行或等待处理，请结束后再操作")
	}
	if pending, _ := record["replyPending"].(bool); pending {
		return errors.New("回复尚未投递完成，请先处理通知")
	}
	return nil
}
func checkRun(run conversation.Run) error {
	state := domain.ParseActivityState(run.Status)
	if state == domain.ActivityUnknown || state.Mutable() || run.ReplyPending {
		return errors.New("会话仍有执行中、排队中或等待通知的请求，请稍后再试")
	}
	return nil
}
func (s *Service) checkReferences(ctx context.Context, id string, kinds []string) error {
	for _, kind := range kinds {
		rows, err := s.Store.List(ctx, kind)
		if err != nil {
			return err
		}
		for _, raw := range rows {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return err
			}
			if references(value, id) {
				return fmt.Errorf("记录仍被 %s 引用，请先解除关联", kind)
			}
		}
	}
	return nil
}
func references(value any, id string) bool {
	switch v := value.(type) {
	case string:
		return v == id || strings.HasSuffix(v, "/"+id) || strings.Contains(v, `"`+id+`"`)
	case []any:
		for _, entry := range v {
			if references(entry, id) {
				return true
			}
		}
	case map[string]any:
		for _, entry := range v {
			if references(entry, id) {
				return true
			}
		}
	}
	return false
}
func (s *Service) checkRestore(ctx context.Context, r domain.Resource, record map[string]any) error {
	if r != domain.ResourceSession {
		return nil
	}
	for _, item := range []struct {
		field    string
		resource domain.Resource
	}{{"configId", domain.ResourceConfig}, {"personaId", domain.ResourcePersona}} {
		id, _ := record[item.field].(string)
		var raw json.RawMessage
		if err := s.Store.Get(ctx, item.resource.StorageKind(), id, &raw); err != nil {
			return fmt.Errorf("恢复失败，关联的 %s 不存在", item.resource.WireName())
		}
		if err := repository.RequireActive(ctx, s.Store, item.resource, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) purgeRefs(ctx context.Context, r domain.Resource, id string, record map[string]any) ([]store.RecordRef, []string, error) {
	refs := recordRefs(r, id)
	runs := []string{}
	switch r {
	case domain.ResourceRun:
		refs = append(refs, store.RecordRef{Kind: "qq-receipt", ID: id})
		runs = append(runs, id)
	case domain.ResourceSession:
		all, err := store.All[conversation.Run](ctx, s.Store, "run")
		if err != nil {
			return nil, nil, err
		}
		for _, run := range all {
			if run.SessionID == id {
				refs = append(refs, recordRefs(domain.ResourceRun, run.ID)...)
				refs = append(refs, store.RecordRef{Kind: "qq-receipt", ID: run.ID})
				runs = append(runs, run.ID)
			}
		}
		notifications, err := store.All[messaging.Notification](ctx, s.Store, "notification")
		if err != nil {
			return nil, nil, err
		}
		for _, n := range notifications {
			if n.SessionID == id {
				state := domain.ParseActivityState(n.Status)
				if state == domain.ActivityUnknown || state.Mutable() {
					return nil, nil, errors.New("会话仍有待处理的通知")
				}
				refs = append(refs, recordRefs(domain.ResourceNotification, n.ID)...)
			}
		}
		deliveries, err := store.All[messaging.Delivery](ctx, s.Store, "delivery")
		if err != nil {
			return nil, nil, err
		}
		for _, d := range deliveries {
			if d.SessionID == id {
				state := domain.ParseActivityState(d.Status)
				if state == domain.ActivityUnknown || state.Mutable() || state == domain.ActivityUncertain {
					return nil, nil, errors.New("会话仍有结果不明的发送记录，请先单独处理投递记录")
				}
				refs = append(refs, recordRefs(domain.ResourceDelivery, d.ID)...)
			}
		}
	case domain.ResourceExecution:
		refs = append(refs, store.RecordRef{Kind: "execution-snapshot", ID: id})
		var snapshot task.Snapshot
		err := s.Store.Get(ctx, "execution-snapshot", id, &snapshot)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, nil, err
		}
		stepIDs := map[string]bool{}
		for _, step := range snapshot.Task.Steps {
			stepIDs[step.ID] = true
		}
		if results, ok := record["results"].(map[string]any); ok {
			for key := range results {
				stepIDs[key] = true
			}
		}
		for stepID := range stepIDs {
			refs = append(refs, store.RecordRef{Kind: "step-result", ID: id + ":" + stepID})
		}
	}
	cacheRefs, err := CachePurgeRefs(ctx, s.Store, refs)
	if err != nil {
		return nil, nil, err
	}
	refs = append(refs, cacheRefs...)
	return refs, runs, nil
}
func recordRefs(r domain.Resource, id string) []store.RecordRef {
	return []store.RecordRef{
		{Kind: r.StorageKind(), ID: id, Reusable: r == domain.ResourceSession}, {Kind: domain.ArchiveStorageKind, ID: domain.ArchiveKey(r, id), Reusable: true},
	}
}
