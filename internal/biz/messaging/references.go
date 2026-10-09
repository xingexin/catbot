package messaging

import (
	"context"
	"errors"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
)

func (a *Service) CheckBindingReference(ctx context.Context, kind, id string) error {
	resource, err := lifecycle.ParseResourceName(kind)
	if err != nil {
		return err
	}
	binding, err := a.OneBotBinding(ctx)
	if err != nil {
		return err
	}
	if (resource == lifecycle.ResourceConfig && binding.ConfigID == id) || (resource == lifecycle.ResourcePersona && (binding.PersonaID == id || binding.GroupPersonaID == id)) {
		return errors.New("记录仍用于 QQ 连接配置，请先修改绑定")
	}
	for _, channel := range a.channels {
		if channel.Binding == nil {
			continue
		}
		value, err := channel.Binding(ctx)
		if err != nil {
			return err
		}
		if (resource == lifecycle.ResourceConfig && value.ConfigID == id) || (resource == lifecycle.ResourcePersona && (value.PersonaID == id || value.RoomPersonaID == id)) {
			return errors.New("记录仍用于消息通道绑定，请先修改绑定")
		}
	}
	return nil
}
