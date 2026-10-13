package conversation

import (
	"context"
	"fmt"

	"github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/conversation"
	convrepo "github.com/xingexin/catbot/internal/domain/conversation/repository"
)

func (a *Service) groupReference(ctx context.Context, run conversation.Run) (string, error) {
	if a.GroupContext == nil {
		return "", nil
	}
	var source conversation.Session
	var err error
	if resolver, ok := a.ToolSource.(SourceSessionResolver); ok {
		source, err = resolver.ToolSourceSession(ctx, run.SessionID)
	} else {
		err = convrepo.New(a.Store).GetSession(ctx, run.SessionID, &source)
	}
	if err != nil {
		return "", fmt.Errorf("group context source: %w", err)
	}
	if source.ChannelRoom == "" {
		return "", nil
	}
	cutoff, err := convrepo.RunCutoff(ctx, a.Store, run)
	if err != nil {
		return "", err
	}
	reference, err := a.GroupContext.GroupContext(ctx, source, cutoff)
	if err != nil {
		return "", fmt.Errorf("group context: %w", err)
	}
	return agent.BoundReference(reference), nil
}
