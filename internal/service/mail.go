package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agentTest/internal/domain"
	"agentTest/internal/plugin"
	"agentTest/internal/store"
)

// Mail setup composes the same plugin and scheduler used by conversations.
// No IMAP client or mailbox credentials are exposed to the application layer.
func (a *App) mailConnection(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Folder string `json:"folder"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	if in.Folder == "" {
		in.Folder = "INBOX"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var p domain.Plugin
	if err := a.Store.Get(ctx, "plugin", "mail", &p); err != nil {
		fail(w, err)
		return
	}
	if !p.Enabled {
		fail(w, errors.New("请先配置并启用邮箱插件"))
		return
	}
	key, err := plugin.Pin(ctx, a.Store, p)
	if err != nil {
		fail(w, err)
		return
	}
	result, err := a.Plugins.CallPinned(ctx, key, "test_connection", map[string]any{"folder": in.Folder}, "mail-test:"+domain.ID())
	if err != nil {
		fail(w, err)
		return
	}
	JSON(w, 200, result)
}

type mailWatchRequest struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Folder          string `json:"folder"`
	IntervalMinutes int    `json:"intervalMinutes"`
	SessionID       string `json:"sessionId"`
	ConfigID        string `json:"configId"`
	PersonaID       string `json:"personaId"`
	IncludeExisting bool   `json:"includeExisting"`
}

func (a *App) saveMailWatch(w http.ResponseWriter, r *http.Request) {
	var in mailWatchRequest
	if err := decode(w, r, &in); err != nil {
		fail(w, err)
		return
	}
	task, err := a.SaveMailWatch(r.Context(), in)
	if err != nil {
		fail(w, err)
		return
	}
	JSON(w, 200, task)
}

func (a *App) SaveMailWatch(ctx context.Context, in mailWatchRequest) (domain.Task, error) {
	t := domain.Task{}
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
	var s domain.Session
	if in.SessionID == "" {
		return t, errors.New("请选择接收提醒的会话；QQ 会话需先向机器人发送一次消息")
	}
	if err := a.Store.Get(ctx, "session", in.SessionID, &s); err != nil {
		return t, err
	}
	if s.Channel == "qq" {
		route := s.ChannelProvider
		if route == "" {
			route = "official"
		}
		channel, ok := a.channels[route]
		if !ok {
			return t, errors.New("提醒通道未注册")
		}
		b, err := channel.Binding(ctx)
		if err != nil {
			return t, err
		}
		if !b.allows(s.ChannelAccount, s.Recipient, s.ChannelRoom, true) {
			return t, errors.New("提醒接收人或群未授权，请先在 QQ 设置中启用绑定")
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
	t.Steps = []domain.Step{{ID: "watch", Kind: "tool", Tool: "mail__watch", Arguments: map[string]any{"folder": in.Folder, "limit": 20, "includeExisting": in.IncludeExisting, "monitorId": t.ID}}}
	first := t.Revision == 0 || t.Status == "cancelled"
	restarting := t.Status == "cancelled"
	if restarting {
		t.Paused = false
	}
	saved, err := a.SaveTask(ctx, t)
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
		if _, err := a.Scheduler.Trigger(ctx, saved, op); err != nil {
			saved.Error = "监听已保存，首次检查未提交；下一周期会自动检查：" + err.Error()
			_ = a.Store.Put(ctx, "task", saved.ID, saved)
		}
	}
	return saved, nil
}

// Validate model references before saving a plugin configuration so missing
// modalities are caught before expensive media processing begins.
func (a *App) validatePluginModels(ctx context.Context, id string, cfg map[string]any) error {
	fields := []string{}
	if id == "video" {
		fields = []string{"transcriptionConfigId", "visionConfigId"}
	}
	if id == "mail" {
		fields = []string{"summaryConfigId"}
	}
	for _, field := range fields {
		configID, _ := cfg[field].(string)
		if configID == "" {
			continue
		}
		var c domain.Config
		if err := a.Store.Get(ctx, "config", configID, &c); err != nil {
			return fmt.Errorf("%s: 模型配置不存在", field)
		}
		if c.Kind != "api" {
			return fmt.Errorf("%s: 此插件能力需要 API 模型配置", field)
		}
		if field == "visionConfigId" && !c.Capabilities.Images {
			return errors.New("视觉模型配置必须启用图片输入能力")
		}
		if field == "transcriptionConfigId" && c.Protocol != "openai-chat" && c.Protocol != "openai-responses" {
			return errors.New("语音转写需要支持音频转写接口的 OpenAI 兼容服务")
		}
	}
	return nil
}
