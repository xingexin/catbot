package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "github.com/xingexin/catbot/internal/domain/agent"
	"github.com/xingexin/catbot/internal/domain/lifecycle"
	lifecycleRepo "github.com/xingexin/catbot/internal/domain/lifecycle/repository"
)

type ModelLister interface {
	ListModels(context.Context, domain.Config, string) (domain.ModelCatalog, error)
}

// ListModels reads the configured service without saving the editor draft or
// creating a conversation. Keys are resolved only on the server.
func (a *Service) ListModels(ctx context.Context, c domain.Config) (domain.ModelCatalog, error) {
	if err := domain.ValidateConnection(&c); err != nil {
		return domain.ModelCatalog{}, err
	}
	var lister ModelLister
	switch domain.ParseExecutionKind(c.Kind) {
	case domain.ExecutionAPI:
		lister = a.APIModels
	case domain.ExecutionSDK:
		switch domain.ParseSDKProvider(c.Provider) {
		case domain.SDKCodeBuddy, domain.SDKClaude:
		default:
			return domain.ModelCatalog{}, errors.New("该 Agent SDK 暂未提供模型列表，请手动填写模型名称")
		}
		lister = a.SDKModels
	}
	if lister == nil {
		return domain.ModelCatalog{}, errors.New("模型列表服务未配置，请手动填写模型名称")
	}
	key, err := a.catalogCredential(ctx, c)
	if err != nil {
		return domain.ModelCatalog{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	catalog, err := lister.ListModels(ctx, c, key)
	if err != nil {
		message := err.Error()
		if key != "" {
			message = strings.ReplaceAll(message, key, "[redacted]")
		}
		return domain.ModelCatalog{}, fmt.Errorf("获取模型列表失败：%s", message)
	}
	if err := catalog.Validate(); err != nil {
		return domain.ModelCatalog{}, err
	}
	for _, model := range catalog.Models {
		if key != "" && (strings.Contains(model.ID, key) || strings.Contains(model.Name, key) || strings.Contains(model.Description, key)) {
			return domain.ModelCatalog{}, errors.New("模型列表响应包含敏感信息，已拒绝展示")
		}
	}
	return catalog, nil
}

func (a *Service) catalogCredential(ctx context.Context, c domain.Config) (string, error) {
	unlock, err := a.Store.Lock(ctx, lifecycle.ReferenceLock)
	if err != nil {
		return "", err
	}
	defer unlock()
	if c.ID != "" {
		if err := a.requireActiveConfig(ctx, c.ID); err != nil {
			return "", err
		}
	}
	if c.CredentialID == "" {
		return "", nil
	}
	if err := lifecycleRepo.RequireActive(ctx, a.Store, lifecycle.ResourceSecret, c.CredentialID); err != nil {
		return "", err
	}
	if a.Vault == nil {
		return "", errors.New("凭证库未配置")
	}
	key, err := a.Vault.Get(ctx, c.CredentialID)
	if err != nil {
		return "", errors.New("无法读取所选 API Key，请检查凭证配置")
	}
	return key, nil
}
