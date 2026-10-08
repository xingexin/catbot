package conversation

import (
	"context"
	"github.com/xingexin/catbot/internal/domain/conversation"
	convrepo "github.com/xingexin/catbot/internal/domain/conversation/repository"
)

func (a *Service) FindRun(ctx context.Context, id string) (conversation.Run, error) {
	var r conversation.Run
	err := convrepo.New(a.Store).GetRun(ctx, id, &r)
	return r, err
}
func (a *Service) Emit(ctx context.Context, id, typ string, data map[string]any) error {
	return a.emit(ctx, id, typ, data)
}
