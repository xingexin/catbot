package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/biz/plugin"
	"github.com/xingexin/catbot/internal/domain/conversation"
	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
	"github.com/xingexin/catbot/internal/infra/store"
	"strings"
)

type TaskCommands interface {
	SaveTask(context.Context, taskentity.Task) (taskentity.Task, error)
	ControlTaskWithOperation(context.Context, string, string, string) (any, error)
}
type Service struct {
	Store                 store.Store
	Tasks                 TaskCommands
	Plugins               *plugin.Manager
	AuthorizeNotification func(context.Context, conversation.Session) error
}
type WatchRequest struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Folder          string `json:"folder"`
	IntervalMinutes int    `json:"intervalMinutes"`
	SessionID       string `json:"sessionId"`
	ConfigID        string `json:"configId"`
	PersonaID       string `json:"personaId"`
	IncludeExisting bool   `json:"includeExisting"`
}

func (a *Service) SaveMailWatch(ctx context.Context, in WatchRequest) (taskentity.Task, error) {
	t := taskentity.Task{}
	if in.IntervalMinutes == 0 {
		in.IntervalMinutes = 5
	}
	switch in.IntervalMinutes {
	case 1, 2, 5, 10, 15, 30, 60:
	default:
		return t, errors.New("检查间隔须为 1、2、5、10、15、30 或 60 分钟")
	}
	if in.Folder == "" {
		in.Folder = "INBOX"
	}
	if in.ID == "" {
		sum := sha256.Sum256([]byte(in.SessionID + "\x00" + in.Folder))
		in.ID = "mail-watch-" + hex.EncodeToString(sum[:12])
	}
	err := a.Store.Get(ctx, "task", in.ID, &t)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return t, err
	}
	if err == nil && (len(t.Steps) != 1 || t.Steps[0].Tool != "mail__watch") {
		return t, errors.New("只能在此修改邮箱监听任务")
	}
	var s conversation.Session
	if in.SessionID == "" {
		return t, errors.New("请选择接收提醒的会话；QQ 会话需先向机器人发送一次消息")
	}
	if err := a.Store.Get(ctx, "session", in.SessionID, &s); err != nil {
		return t, err
	}
	if s.Channel == "qq" {
		if a.AuthorizeNotification == nil {
			return t, errors.New("提醒通道未注册")
		}
		if err := a.AuthorizeNotification(ctx, s); err != nil {
			return t, err
		}
	}
	if in.ConfigID == "" {
		in.ConfigID = s.ConfigID
	}
	if in.PersonaID == "" {
		in.PersonaID = s.PersonaID
	}
	if in.Folder == "" {
		in.Folder = "INBOX"
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "新邮件提醒 · " + in.Folder
	}
	cron := fmt.Sprintf("*/%d * * * *", in.IntervalMinutes)
	if in.IntervalMinutes == 60 {
		cron = "0 * * * *"
	}
	t.ID, t.Name, t.Kind, t.Cron, t.TimeZone = in.ID, in.Name, "recurring", cron, "Asia/Shanghai"
	t.ConfigID, t.PersonaID, t.SessionID = in.ConfigID, in.PersonaID, in.SessionID
	t.CatchupSec, t.Notify, t.NotifyWhen, t.NotifyText = 60, true, "${steps.watch.changed}", "${steps.watch.notificationText}"
	t.Steps = []taskentity.Step{{ID: "watch", Kind: "tool", Tool: "mail__watch", Arguments: map[string]any{"folder": in.Folder, "limit": 20, "includeExisting": in.IncludeExisting, "monitorId": t.ID}}}
	first := t.Revision == 0 || t.Status == "cancelled"
	restarting := t.Status == "cancelled"
	if restarting {
		t.Paused = false
	}
	saved, err := a.Tasks.SaveTask(ctx, t)
	if err != nil {
		return saved, err
	}
	// Establish a baseline immediately; the Schedule remains authoritative for
	// future checks. A lost response is safe: Trigger has a stable operation ID.
	if first {
		op := "initialize:" + saved.ID
		if restarting {
			op = fmt.Sprintf("restart:%s:r%d", saved.ID, saved.Revision)
		}
		if _, err := a.Tasks.ControlTaskWithOperation(ctx, saved.ID, "trigger", op); err != nil {
			saved.Error = "监听已保存，首次检查未提交；下一周期会自动检查：" + err.Error()
			_ = a.Store.Put(ctx, "task", saved.ID, saved)
		}
	}
	return saved, nil
}
