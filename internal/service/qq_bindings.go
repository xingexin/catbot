package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"agentTest/internal/domain"
	"agentTest/internal/store"
)

type oneBotBinding struct {
	Enabled         bool     `json:"enabled"`
	SelfID          string   `json:"selfId"`
	AllowedUserIDs  []string `json:"allowedUserIds"`
	AllowedGroupIDs []string `json:"allowedGroupIds"`
	ConfigID        string   `json:"configId"`
	PersonaID       string   `json:"personaId"`
	GroupPersonaID  string   `json:"groupPersonaId"`
}

func (a *App) oneBotBinding(ctx context.Context) (oneBotBinding, error) {
	b := oneBotBinding{AllowedUserIDs: []string{}, AllowedGroupIDs: []string{}, ConfigID: a.Options.QQConfigID, PersonaID: a.Options.QQPersonaID}
	if b.PersonaID == "" {
		b.PersonaID = "secretary"
	}
	err := a.Store.Get(ctx, "qq-binding", "onebot", &b)
	if errors.Is(err, store.ErrNotFound) {
		err = nil
	}
	return b, err
}

func validQQID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func (a *App) qqConnections(w http.ResponseWriter, r *http.Request) {
	b, err := a.oneBotBinding(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	JSON(w, 200, map[string]any{
		"strategies": []channelStatus{a.channelStatus(r.Context(), "official"), a.channelStatus(r.Context(), "onebot")},
		"onebot":     b, "napcatWebUrl": a.channels["onebot"].LoginURL,
	})
}

func (a *App) saveOneBotBinding(w http.ResponseWriter, r *http.Request) {
	var b oneBotBinding
	if err := decode(w, r, &b); err != nil {
		fail(w, err)
		return
	}
	if b.Enabled {
		if !validQQID(b.SelfID) || len(b.AllowedUserIDs)+len(b.AllowedGroupIDs) == 0 || len(b.AllowedUserIDs) > 20 || len(b.AllowedGroupIDs) > 20 {
			fail(w, errors.New("填写登录的 QQ 号，以及允许联系人或群号；联系人和群各最多 20 个"))
			return
		}
		for _, id := range b.AllowedUserIDs {
			if !validQQID(id) || id == b.SelfID {
				fail(w, errors.New("联系人必须是其他有效 QQ 号"))
				return
			}
		}
		for _, id := range b.AllowedGroupIDs {
			if !validQQID(id) {
				fail(w, errors.New("群号必须是有效的 QQ 群号"))
				return
			}
		}
		var c domain.Config
		if err := a.Store.Get(r.Context(), "config", b.ConfigID, &c); err != nil {
			fail(w, errors.New("请选择有效执行配置"))
			return
		}
		if len(b.AllowedUserIDs) > 0 {
			var p domain.Persona
			if err := a.Store.Get(r.Context(), "persona", b.PersonaID, &p); err != nil {
				fail(w, errors.New("请选择有效私聊人格"))
				return
			}
		}
		if len(b.AllowedGroupIDs) > 0 {
			var p domain.Persona
			if b.GroupPersonaID == "" {
				fail(w, errors.New("请选择独立的群聊人格，并明确设置允许的工具"))
				return
			}
			if err := a.Store.Get(r.Context(), "persona", b.GroupPersonaID, &p); err != nil {
				fail(w, errors.New("请选择有效群聊人格"))
				return
			}
			if p.Tools == nil {
				fail(w, errors.New("群聊人格必须使用明确的工具清单，可设为空列表，不能默认开放全部工具"))
				return
			}
		}
		channel, registered := a.channels["onebot"]
		if !registered {
			fail(w, errors.New("消息通道尚未注册"))
			return
		}
		status := a.channelStatus(r.Context(), "onebot")
		if channel.Status != nil && status.Account != b.SelfID {
			fail(w, errors.New("请先在消息接入服务登录，并绑定当前登录的 QQ 号"))
			return
		}
	}
	if err := a.Store.Put(r.Context(), "qq-binding", "onebot", b); err != nil {
		fail(w, err)
		return
	}
	JSON(w, 200, b)
}

// OneBotChannelBinding reads the existing logical channel policy, independently of its sender.
func (a *App) OneBotChannelBinding(ctx context.Context) (ChannelBinding, error) {
	b, err := a.oneBotBinding(ctx)
	return ChannelBinding{Enabled: b.Enabled, Account: b.SelfID, AllowedPeers: b.AllowedUserIDs, AllowedRooms: b.AllowedGroupIDs, ConfigID: b.ConfigID, PersonaID: b.PersonaID, RoomPersonaID: b.GroupPersonaID}, err
}
func (a *App) OfficialChannelBinding(context.Context) (ChannelBinding, error) {
	return ChannelBinding{Enabled: a.Options.QQUser != "", Account: a.Options.QQAppID, AllowedPeers: []string{a.Options.QQUser}, ConfigID: a.Options.QQConfigID, PersonaID: a.Options.QQPersonaID, AllowLegacyAccount: true}, nil
}
