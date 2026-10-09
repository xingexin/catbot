// Package pluginhost implements the host capabilities exposed to a pinned
// plugin execution. HTTP tokens and request decoding belong to transport.
package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/xingexin/catbot/internal/domain/artifact"
	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/domain/plugin"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/idgen"
	"github.com/xingexin/catbot/internal/infra/store"
)

type TaskSaver interface {
	SaveTask(context.Context, taskentity.Task) (taskentity.Task, error)
}
type Notifier interface {
	NotifyRecord(context.Context, messaging.Notification, bool) error
}
type ResultSaver interface {
	SaveResult(context.Context, artifact.Artifact) (artifact.Artifact, error)
}
type Service struct {
	Store         store.Store
	Tasks         TaskSaver
	Notifications Notifier
	Artifacts     ResultSaver
}

type TaskInput struct {
	taskentity.Task
	OperationID string `json:"operationId"`
}
type NotificationInput struct {
	SessionID   string `json:"sessionId"`
	Text        string `json:"text"`
	OperationID string `json:"operationId"`
}
type ResultInput struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

func (s *Service) Snapshot(ctx context.Context, key string) (plugin.Plugin, error) {
	var p plugin.Plugin
	err := s.Store.Get(ctx, "plugin-version", key, &p)
	return p, err
}
func (s *Service) Get(ctx context.Context, p plugin.Plugin, key string) (any, error) {
	var value any
	err := s.Store.Get(ctx, "plugin-data:"+p.ID, key, &value)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return value, err
}
func (s *Service) Put(ctx context.Context, p plugin.Plugin, key string, value any) error {
	unlock, err := s.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return err
	}
	defer unlock()
	purged, err := store.Purged(ctx, s.Store, "plugin", p.ID)
	if err != nil {
		return err
	}
	if purged {
		return lifecycleRepository.ErrPurged
	}
	// Authentication may have loaded this snapshot before a concurrent purge.
	// Archived/disabled snapshots remain valid for already started calls.
	var current plugin.Plugin
	if err := s.Store.Get(ctx, "plugin-version", plugin.SnapshotKey(p), &current); err != nil {
		return err
	}
	return s.Store.Put(ctx, "plugin-data:"+p.ID, key, value)
}
func (s *Service) SaveResult(ctx context.Context, p plugin.Plugin, in ResultInput) (artifact.Artifact, error) {
	v := artifact.Artifact{ID: idgen.New(), Name: in.Name, Mime: "application/json", Data: in.Data, PluginID: p.ID, CreatedAt: time.Now().UTC()}
	return s.Artifacts.SaveResult(ctx, v)
}
func (s *Service) SaveTask(ctx context.Context, p plugin.Plugin, in TaskInput) (taskentity.Task, bool, error) {
	if in.OperationID == "" || len(in.OperationID) > 512 {
		return taskentity.Task{}, false, errors.New("stable operationId is required")
	}
	sum := sha256.Sum256([]byte(p.ID + ":" + in.OperationID))
	in.Task.ID = "plugin-task-" + hex.EncodeToString(sum[:12])
	unlock, err := s.Store.Lock(ctx, "plugin-task:"+in.Task.ID)
	if err != nil {
		return taskentity.Task{}, false, err
	}
	defer unlock()
	var prior taskentity.Task
	if err := s.Store.Get(ctx, "task", in.Task.ID, &prior); err == nil {
		return prior, false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return prior, false, err
	}
	value, err := s.Tasks.SaveTask(ctx, in.Task)
	return value, true, err
}
func (s *Service) Notify(ctx context.Context, p plugin.Plugin, in NotificationInput) (messaging.Notification, error) {
	var n messaging.Notification
	if in.SessionID == "" || strings.TrimSpace(in.Text) == "" || len(in.Text) > 128<<10 || strings.ContainsRune(in.Text, '\x00') || in.OperationID == "" || len(in.OperationID) > 512 {
		return n, errors.New("notification requires sessionId, nonempty text up to 128 KiB and stable operationId up to 512 bytes")
	}
	var session conversation.Session
	if err := s.Store.Get(ctx, "session", in.SessionID, &session); err != nil {
		return n, err
	}
	if session.Channel != "" && session.Channel != "web" && session.Channel != "qq" {
		return n, errors.New("notifications require a user Web or QQ conversation")
	}
	sum := sha256.Sum256([]byte(p.ID + "\x00" + in.OperationID))
	operationID := "plugin-notify:" + hex.EncodeToString(sum[:])
	n = messaging.Notification{ID: "notification-" + operationID, PluginID: p.ID, SessionID: in.SessionID, Text: in.Text, OperationID: operationID}
	if err := s.Notifications.NotifyRecord(ctx, n, false); err != nil {
		return n, err
	}
	err := s.Store.Get(ctx, "notification", n.ID, &n)
	return n, err
}
