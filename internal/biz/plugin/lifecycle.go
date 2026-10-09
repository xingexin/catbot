package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	domainplugin "github.com/xingexin/catbot/internal/domain/plugin"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
)

func (m *Manager) Archive(ctx context.Context, id string) error {
	unlock, err := m.Store.Lock(ctx, "plugin-config:"+id)
	if err != nil {
		return err
	}
	defer unlock()
	var p domainplugin.Plugin
	if err := m.Store.Get(ctx, "plugin", id, &p); err != nil {
		return err
	}
	archived, err := lifecycleRepository.Archived(ctx, m.Store, lifecycle.ResourcePlugin, id)
	if err != nil || archived {
		return err
	}
	if err := m.setPluginEnabledLocked(ctx, p, false); err != nil {
		return err
	}
	return lifecycleRepository.Mark(ctx, m.Store, lifecycle.ArchiveRecord{Resource: lifecycle.ResourcePlugin, RecordID: id, Name: p.Manifest.Name, ArchivedAt: time.Now().UTC()})
}

func (m *Manager) Restore(ctx context.Context, id string) error {
	unlock, err := m.Store.Lock(ctx, "plugin-config:"+id)
	if err != nil {
		return err
	}
	defer unlock()
	var p domainplugin.Plugin
	if err := m.Store.Get(ctx, "plugin", id, &p); err != nil {
		return err
	}
	archived, err := lifecycleRepository.Archived(ctx, m.Store, lifecycle.ResourcePlugin, id)
	if err != nil || !archived {
		return err
	}
	p.Enabled = false
	if err := m.Store.Put(ctx, "plugin", id, p); err != nil {
		return err
	}
	return lifecycleRepository.Unmark(ctx, m.Store, lifecycle.ResourcePlugin, id)
}

func (m *Manager) Purge(ctx context.Context, id string) error {
	unlock, err := m.Store.Lock(ctx, "plugin-config:"+id)
	if err != nil {
		return err
	}
	defer unlock()
	callsUnlock, err := m.Store.Lock(ctx, "plugin-calls:"+id)
	if err != nil {
		return err
	}
	defer callsUnlock()
	referenceUnlock, err := m.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return err
	}
	defer referenceUnlock()
	purged, err := store.Purged(ctx, m.Store, "plugin", id)
	if err != nil || purged {
		return err
	}
	archived, err := lifecycleRepository.Archived(ctx, m.Store, lifecycle.ResourcePlugin, id)
	if err != nil {
		return err
	}
	if !archived {
		return errors.New("请先归档插件，再永久删除")
	}
	var p domainplugin.Plugin
	if err := m.Store.Get(ctx, "plugin", id, &p); err != nil {
		return err
	}
	if err := m.requireUnreferencedPlugin(ctx, id); err != nil {
		return err
	}
	refs := []store.RecordRef{{Kind: "plugin", ID: id}, {Kind: lifecycle.ArchiveStorageKind, ID: lifecycle.ArchiveKey(lifecycle.ResourcePlugin, id), Reusable: true}}
	versions, err := store.RecordRefs(ctx, m.Store, "plugin-version")
	if err != nil {
		return err
	}
	keys := []string{}
	ownedSecrets := map[string]bool{}
	for _, secret := range p.Secrets {
		ownedSecrets[secret] = true
	}
	for _, ref := range versions {
		var snapshot domainplugin.Plugin
		if err := m.Store.Get(ctx, ref.Kind, ref.ID, &snapshot); err != nil {
			return err
		}
		if snapshot.ID != id {
			continue
		}
		ref.Reusable = true
		refs = append(refs, ref)
		keys = append(keys, ref.ID)
		for _, secret := range snapshot.Secrets {
			ownedSecrets[secret] = true
		}
	}
	data, err := store.RecordRefs(ctx, m.Store, "plugin-data:"+id)
	if err != nil {
		return err
	}
	for _, ref := range data {
		ref.Reusable = true
		refs = append(refs, ref)
	}
	// Only erase credentials created privately for this plugin and no longer
	// referenced by another configuration. User-managed shared keys survive.
	for secret := range ownedSecrets {
		if !strings.HasPrefix(secret, "plugin-"+id+"-") {
			continue
		}
		used, err := m.secretUsedElsewhere(ctx, secret, id)
		if err != nil {
			return err
		}
		if !used {
			refs = append(refs, store.RecordRef{Kind: "secret", ID: secret}, store.RecordRef{Kind: lifecycle.ArchiveStorageKind, ID: lifecycle.ArchiveKey(lifecycle.ResourceSecret, secret), Reusable: true})
		}
	}
	if closer, ok := m.runtime.(interface{ CloseVersions([]string) }); ok {
		closer.CloseVersions(keys)
	}
	return store.Purge(ctx, m.Store, refs, nil)
}

func (m *Manager) requireUnreferencedPlugin(ctx context.Context, id string) error {
	tasks, err := store.All[taskentity.Task](ctx, m.Store, "task")
	if err != nil {
		return err
	}
	for _, task := range tasks {
		_, used := task.Versions[id]
		for _, step := range task.Steps {
			used = used || strings.HasPrefix(step.Tool, id+"__")
		}
		if used {
			return fmt.Errorf("插件仍被任务「%s」引用，请先永久删除相关任务", task.Name)
		}
	}
	runs, err := store.All[conversation.Run](ctx, m.Store, "run")
	if err != nil {
		return err
	}
	for _, run := range runs {
		if _, used := run.Versions[id]; used {
			return errors.New("插件仍被运行记录引用，请先永久删除相关记录")
		}
	}
	executions, err := store.All[taskentity.TaskExecution](ctx, m.Store, "execution")
	if err != nil {
		return err
	}
	for _, execution := range executions {
		if _, used := execution.Versions[id]; used {
			return errors.New("插件仍被任务执行记录引用，请先永久删除相关记录")
		}
	}
	snapshots, err := store.All[taskentity.Snapshot](ctx, m.Store, "execution-snapshot")
	if err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		if _, used := snapshot.Task.Versions[id]; used {
			return errors.New("插件仍被任务执行快照引用，请先永久删除相关执行记录")
		}
	}
	return nil
}

func (m *Manager) secretUsedElsewhere(ctx context.Context, secret, pluginID string) (bool, error) {
	for _, scan := range []struct {
		kind         string
		excludeOwner bool
	}{
		{kind: "config"}, {kind: "plugin", excludeOwner: true}, {kind: "plugin-version", excludeOwner: true},
		{kind: "execution-snapshot"}, {kind: "run"}, {kind: "execution"},
	} {
		records, err := m.Store.List(ctx, scan.kind)
		if err != nil {
			return false, err
		}
		for _, raw := range records {
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				return false, err
			}
			if scan.excludeOwner && value["id"] == pluginID {
				continue
			}
			if containsSecret(value, secret) {
				return true, nil
			}
		}
	}
	return false, nil
}
func containsSecret(value any, secret string) bool {
	switch v := value.(type) {
	case string:
		return v == secret
	case []any:
		for _, child := range v {
			if containsSecret(child, secret) {
				return true
			}
		}
	case map[string]any:
		for _, child := range v {
			if containsSecret(child, secret) {
				return true
			}
		}
	}
	return false
}
