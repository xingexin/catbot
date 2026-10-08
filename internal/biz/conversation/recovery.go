package conversation

import (
	"context"
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/domain/conversation"
	convrepo "github.com/xingexin/catbot/internal/domain/conversation/repository"
	"github.com/xingexin/catbot/internal/infra/store"
	"time"
)

func (a *Service) Recover(ctx context.Context) error {
	runs, err := convrepo.New(a.Store).Runs(ctx)
	if err != nil {
		return err
	}
	recoveredSessions := make(map[string]bool)
	for _, r := range runs {
		if r.Status == "running" {
			if !recoveredSessions[r.SessionID] {
				var session conversation.Session
				err := convrepo.New(a.Store).GetSession(ctx, r.SessionID, &session)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("load interrupted run session: %w", err)
				}
				if err == nil && session.ActiveConfig != "" {
					// Persist this before finalizing the run so a failed recovery
					// can retry without resuming an ambiguous native SDK turn.
					session.ActiveConfig = ""
					if err := convrepo.New(a.Store).SaveSession(ctx, session); err != nil {
						return fmt.Errorf("clear interrupted native session: %w", err)
					}
				}
				recoveredSessions[r.SessionID] = true
			}
			r.Interrupt(time.Now())
			if err := convrepo.New(a.Store).SaveRun(ctx, r); err != nil {
				return err
			}
		}
	}

	return nil
}
