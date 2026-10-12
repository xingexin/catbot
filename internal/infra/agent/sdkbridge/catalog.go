package sdkbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xingexin/catbot/internal/domain/agent"
)

func (b *Bridge) ListModels(ctx context.Context, c agent.Config, key string) (agent.ModelCatalog, error) {
	payload, err := json.Marshal(struct {
		Config agent.Config `json:"config"`
		APIKey string       `json:"apiKey,omitempty"`
	}{Config: c, APIKey: key})
	if err != nil {
		return agent.ModelCatalog{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.URL, "/")+"/models", bytes.NewReader(payload))
	if err != nil {
		return agent.ModelCatalog{}, errors.New("SDK 模型列表服务地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 33 * time.Second}
	if b.HTTP != nil {
		client = *b.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return agent.ModelCatalog{}, ctx.Err()
		}
		return agent.ModelCatalog{}, errors.New("SDK 模型列表服务不可达或请求超时，请稍后重试")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 {
		return agent.ModelCatalog{}, errors.New("SDK 模型列表响应过大或不完整")
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
			message := failure.Error
			for _, secret := range []string{key, b.Token} {
				if secret != "" {
					message = strings.ReplaceAll(message, secret, "[redacted]")
				}
			}
			if len(message) <= 2048 {
				return agent.ModelCatalog{}, fmt.Errorf("SDK 模型列表：%s", message)
			}
		}
		return agent.ModelCatalog{}, fmt.Errorf("SDK 模型列表服务返回 HTTP %d，请检查接入环境和凭证", resp.StatusCode)
	}
	var catalog agent.ModelCatalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return agent.ModelCatalog{}, errors.New("SDK 模型列表格式无效")
	}
	if err := catalog.Validate(); err != nil {
		return agent.ModelCatalog{}, err
	}
	return catalog, nil
}
