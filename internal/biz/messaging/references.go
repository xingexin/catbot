package messaging

import (
	"context"
	"errors"
)

func (a *Service) CheckBindingReference(ctx context.Context, kind, id string) error {
	binding, err := a.OneBotBinding(ctx)
	if err != nil {
		return err
	}
	if (kind == "config" && binding.ConfigID == id) || (kind == "persona" && binding.PersonaID == id) {
		return errors.New("record is used by QQ connection defaults")
	}
	return nil
}
