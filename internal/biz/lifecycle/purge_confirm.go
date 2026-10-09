package lifecycle

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	domain "github.com/xingexin/catbot/internal/domain/lifecycle"
	repository "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
	"github.com/xingexin/catbot/internal/infra/store"
)

type PurgeResult struct {
	Deleted []PurgeItem    `json:"deleted"`
	Failed  []PurgeBlocker `json:"failed"`
}

// ConfirmPurge validates the exact preview before any mutation, then uses the
// normal lifecycle services so schedule cancellation and replay guards survive.
// This is deliberately not a transaction across PostgreSQL, files and Temporal:
// an error stops the cascade and returns the exact completed prefix.
func (s *Service) ConfirmPurge(ctx context.Context, in PurgeRequest) (PurgeResult, error) {
	result := PurgeResult{Deleted: []PurgeItem{}, Failed: []PurgeBlocker{}}
	if strings.TrimSpace(in.Token) == "" {
		return result, errors.New("请先预览关联内容，再确认永久删除")
	}
	unlock, err := s.Store.Lock(ctx, "lifecycle:purge-confirm")
	if err != nil {
		return result, err
	}
	defer unlock()
	plan, err := s.PreviewPurge(ctx, in)
	if err != nil {
		return result, err
	}
	if subtle.ConstantTimeCompare([]byte(in.Token), []byte(plan.Token)) != 1 {
		return result, repository.ErrPurgeScopeChanged
	}
	if len(plan.Blockers) != 0 {
		return result, errors.New("关联内容仍有未完成的操作，请处理弹窗中的提示后重试")
	}
	approved := map[domain.Resource][]string{}
	for _, item := range plan.Items {
		approved[item.Resource] = append(approved[item.Resource], item.ID)
	}
	ctx = repository.WithPurgeScope(ctx, approved)
	for index, item := range plan.Items {
		operationCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.purgeConfirmedItem(operationCtx, item)
		cancel()
		if err == nil {
			result.Deleted = append(result.Deleted, item)
			continue
		}
		result.Failed = append(result.Failed, PurgeBlocker{Resource: item.Resource, ID: item.ID, Name: item.Name, Reason: err.Error()})
		for _, remaining := range plan.Items[index+1:] {
			result.Failed = append(result.Failed, PurgeBlocker{Resource: remaining.Resource, ID: remaining.ID, Name: remaining.Name, Reason: "前面的关联内容未能删除，本项尚未删除，请重新预览后重试"})
		}
		break
	}
	return result, nil
}

func (s *Service) purgeConfirmedItem(ctx context.Context, item PurgeItem) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Plugin cleanup can remove an explicitly listed private credential first.
	purged, err := store.Purged(ctx, s.Store, item.Resource.StorageKind(), item.ID)
	if err != nil || purged {
		return err
	}
	if err := s.apply(ctx, item.Resource, domain.ActionArchive, item.ID); err != nil {
		return err
	}
	// Recheck scope after stopping schedules/plugins. Other requests may have
	// introduced dependencies since the initial confirmation was validated.
	plan, err := s.PreviewPurge(ctx, PurgeRequest{Items: []PurgeTarget{{Resource: item.Resource, ID: item.ID}}})
	if err != nil {
		return err
	}
	refs := make([]store.RecordRef, 0, len(plan.Items))
	for _, related := range plan.Items {
		refs = append(refs, store.RecordRef{Kind: related.Resource.StorageKind(), ID: related.ID})
	}
	if err := repository.RequirePurgeScope(ctx, refs); err != nil {
		return err
	}
	if len(plan.Blockers) != 0 {
		return errors.New(plan.Blockers[0].Reason)
	}
	return s.apply(ctx, item.Resource, domain.ActionPurge, item.ID)
}
