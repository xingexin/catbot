package messaging

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	message "github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/domain/persona"
)

func (a *Service) Connections(ctx context.Context) (map[string]any, error) {
	b, err := a.OneBotBinding(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"strategies": []ChannelStatus{a.ChannelStatus(ctx, "official"), a.ChannelStatus(ctx, "onebot")}, "onebot": b, "napcatWebUrl": a.channels["onebot"].LoginURL}, nil
}

func (a *Service) SaveOneBotBinding(ctx context.Context, b OneBotBinding) (OneBotBinding, error) {
	if b.Enabled {
		if !message.ValidQQID(b.SelfID) || len(b.AllowedUserIDs)+len(b.AllowedGroupIDs) == 0 || len(b.AllowedUserIDs) > 20 || len(b.AllowedGroupIDs) > 20 {
			return b, errors.New("填写登录的 QQ 号，以及允许联系人或群号；联系人和群各最多 20 个")
		}
		for _, id := range b.AllowedUserIDs {
			if !message.ValidQQID(id) || id == b.SelfID {
				return b, errors.New("联系人必须是其他有效 QQ 号")
			}
		}
		for _, id := range b.AllowedGroupIDs {
			if !message.ValidQQID(id) {
				return b, errors.New("群号必须是有效的 QQ 群号")
			}
		}
		var c agent.Config
		if err := a.Store.Get(ctx, "config", b.ConfigID, &c); err != nil {
			return b, errors.New("请选择有效执行配置")
		}
		if len(b.AllowedUserIDs) > 0 {
			var p persona.Persona
			if err := a.Store.Get(ctx, "persona", b.PersonaID, &p); err != nil {
				return b, errors.New("请选择有效私聊人格")
			}
		}
		if len(b.AllowedGroupIDs) > 0 {
			var p persona.Persona
			if b.GroupPersonaID == "" {
				return b, errors.New("请选择独立的群聊人格，并明确设置允许的工具")
			}
			if err := a.Store.Get(ctx, "persona", b.GroupPersonaID, &p); err != nil {
				return b, errors.New("请选择有效群聊人格")
			}
			if p.Tools == nil {
				return b, errors.New("群聊人格必须使用明确的工具清单，可设为空列表，不能默认开放全部工具")
			}
		}
		channel, registered := a.channels["onebot"]
		if !registered {
			return b, errors.New("消息通道尚未注册")
		}
		status := a.ChannelStatus(ctx, "onebot")
		if channel.Status != nil && status.Account != b.SelfID {
			return b, errors.New("请先在消息接入服务登录，并绑定当前登录的 QQ 号")
		}
	}
	if err := a.Store.Put(ctx, "qq-binding", "onebot", b); err != nil {
		return b, err
	}
	return b, nil
}

func (a *Service) AuthorizeNotification(ctx context.Context, s conversation.Session) error {
	if s.Channel != "qq" {
		return nil
	}
	route := s.ChannelProvider
	if route == "" {
		route = "official"
	}
	channel, ok := a.channels[route]
	if !ok {
		return errors.New("提醒通道未注册")
	}
	binding, err := channel.Binding(ctx)
	if err != nil {
		return err
	}
	if !binding.Allows(s.ChannelAccount, s.Recipient, s.ChannelRoom, true) {
		return errors.New("提醒接收人或群未授权，请先在 QQ 设置中启用绑定")
	}
	return nil
}

func (a *Service) RetryNotification(ctx context.Context, id string) (message.Notification, error) {
	n := message.Notification{ID: id}
	if err := a.Store.Get(ctx, "notification", id, &n); err != nil {
		return n, err
	}
	if n.Status != "failed" {
		return n, errors.New("仅可重试已明确失败的通知，结果不确定的投递需人工核对")
	}
	if err := a.NotifyRecord(ctx, n, true); err != nil {
		return n, err
	}
	_ = a.Store.Get(ctx, "notification", id, &n)
	return n, nil
}
