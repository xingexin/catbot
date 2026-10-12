package modelapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xingexin/catbot/internal/domain/agent"
)

const (
	catalogTimeout      = 15 * time.Second
	catalogMaxPageBytes = 1 << 20
	catalogMaxPages     = 20
	catalogMaxModels    = 5000
	catalogManualHint   = "；请确认地址和凭证，或手动填写模型 ID"
)

type catalogPage struct {
	Data []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

// ListModels discovers names only; it never generates content or infers model capabilities.
func (m *Model) ListModels(ctx context.Context, c agent.Config, key string) (agent.ModelCatalog, error) {
	protocol := agent.ParseProtocol(c.Protocol)
	endpoint, err := catalogEndpoint(c.BaseURL, protocol)
	if err != nil {
		return agent.ModelCatalog{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()

	client := http.Client{}
	if m.HTTP != nil {
		client = *m.HTTP
	}
	// An injected client must not forward a configured credential to a redirect target.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	catalog := agent.ModelCatalog{Source: agent.ModelCatalogLive, Models: []agent.ModelOption{}}
	seenModels := map[string]bool{}
	seenCursors := map[string]bool{}
	for pageNumber := 0; pageNumber < catalogMaxPages; pageNumber++ {
		page, err := fetchCatalogPage(ctx, &client, endpoint, protocol, key)
		if err != nil {
			return agent.ModelCatalog{}, err
		}
		for _, model := range page.Data {
			id := strings.TrimSpace(model.ID)
			if id == "" || seenModels[id] {
				continue
			}
			if len(catalog.Models) >= catalogMaxModels {
				return agent.ModelCatalog{}, errors.New("模型列表超过数量限制" + catalogManualHint)
			}
			name := strings.TrimSpace(model.DisplayName)
			if name == "" {
				name = strings.TrimSpace(model.Name)
			}
			if name == "" {
				name = id
			}
			catalog.Models = append(catalog.Models, agent.ModelOption{ID: id, Name: name, Description: model.Description})
			seenModels[id] = true
		}
		if protocol != agent.ProtocolAnthropic || !page.HasMore {
			if len(catalog.Models) == 0 {
				return agent.ModelCatalog{}, errors.New("服务未返回可用模型列表" + catalogManualHint)
			}
			return catalog, nil
		}
		if page.LastID == "" || seenCursors[page.LastID] {
			return agent.ModelCatalog{}, errors.New("模型列表分页游标无效" + catalogManualHint)
		}
		seenCursors[page.LastID] = true
		query := endpoint.Query()
		query.Set("after_id", page.LastID)
		endpoint.RawQuery = query.Encode()
	}
	return agent.ModelCatalog{}, errors.New("模型列表超过分页限制" + catalogManualHint)
}

func catalogEndpoint(base string, protocol agent.Protocol) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("模型列表需要不含凭证、查询参数或片段的 HTTP(S) Base URL" + catalogManualHint)
	}
	base = strings.TrimRight(u.Path, "/")
	switch protocol {
	case agent.ProtocolOpenAIChat, agent.ProtocolOpenAIResponses:
		u.Path = base + "/models"
	case agent.ProtocolAnthropic:
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		u.Path = base + "/models"
		query := u.Query()
		query.Set("limit", "1000")
		u.RawQuery = query.Encode()
	default:
		return nil, errors.New("当前协议不支持获取模型列表，请手动填写模型 ID")
	}
	u.RawPath = ""
	return u, nil
}

func fetchCatalogPage(ctx context.Context, client *http.Client, endpoint *url.URL, protocol agent.Protocol, key string) (catalogPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return catalogPage{}, errors.New("无法创建模型列表请求" + catalogManualHint)
	}
	req.Header.Set("Accept", "application/json")
	if protocol == agent.ProtocolAnthropic {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		// URLs and transport errors can contain credentials supplied by a proxy or endpoint.
		// Only safe cancellation causes and fixed messages cross this boundary.
		if ctx.Err() != nil {
			return catalogPage{}, fmt.Errorf("获取模型列表已取消或超时%s: %w", catalogManualHint, ctx.Err())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return catalogPage{}, fmt.Errorf("获取模型列表超时%s: %w", catalogManualHint, context.DeadlineExceeded)
		}
		return catalogPage{}, errors.New("模型列表请求失败" + catalogManualHint)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return catalogPage{}, fmt.Errorf("获取模型列表失败%s: %w", catalogManualHint, &HTTPError{Status: resp.StatusCode})
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, catalogMaxPageBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return catalogPage{}, fmt.Errorf("读取模型列表已取消或超时%s: %w", catalogManualHint, ctx.Err())
		}
		return catalogPage{}, errors.New("无法读取模型列表响应" + catalogManualHint)
	}
	if len(body) > catalogMaxPageBytes {
		return catalogPage{}, errors.New("模型列表响应超过大小限制" + catalogManualHint)
	}
	var page catalogPage
	if err := json.Unmarshal(body, &page); err != nil {
		return catalogPage{}, errors.New("服务返回的模型列表格式无效" + catalogManualHint)
	}
	return page, nil
}
