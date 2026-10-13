package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xingexin/catbot/internal/domain/conversation"
	"github.com/xingexin/catbot/internal/domain/messaging"
	"github.com/xingexin/catbot/internal/infra/store"
)

// RunCutoff anchors all group reads in a run to its received message time.
// Background runs without a receipt use their own creation time, not wall time.
func RunCutoff(ctx context.Context, s store.Store, run conversation.Run) (time.Time, error) {
	var receipt messaging.ReplyReference
	err := s.Get(ctx, "qq-receipt", run.ID, &receipt)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return time.Time{}, fmt.Errorf("group context cutoff: %w", err)
	}
	if err == nil && !receipt.ReceivedAt.IsZero() {
		return receipt.ReceivedAt, nil
	}
	return run.CreatedAt, nil
}
