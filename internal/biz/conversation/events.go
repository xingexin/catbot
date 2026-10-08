package conversation

import (
	"context"
	"github.com/xingexin/catbot/internal/domain/conversation"
	convrepo "github.com/xingexin/catbot/internal/domain/conversation/repository"
)

func (a *Service) Events(ctx context.Context, id string, after int64) ([]conversation.Event, error) {
	return convrepo.New(a.Store).Events(ctx, id, after)
}
