package plugin

import (
	"context"
	"errors"
	"fmt"
	"github.com/xingexin/catbot/internal/domain/agent"
)

func ValidateModels(ctx context.Context, id string, cfg map[string]any, lookup func(context.Context, string) (agent.Config, error)) error {
	fields := []string{}
	if id == "video" {
		fields = []string{"transcriptionConfigId", "visionConfigId"}
	}
	if id == "mail" {
		fields = []string{"summaryConfigId"}
	}
	for _, field := range fields {
		configID, _ := cfg[field].(string)
		if configID == "" {
			continue
		}
		var c agent.Config
		if err := lookupConfig(ctx, configID, &c, lookup); err != nil {
			return fmt.Errorf("%s: 模型配置不存在", field)
		}
		if c.Kind != "api" {
			return fmt.Errorf("%s: 此插件能力需要 API 模型配置", field)
		}
		if field == "visionConfigId" && !c.Capabilities.Images {
			return errors.New("视觉模型配置必须启用图片输入能力")
		}
		if field == "transcriptionConfigId" && c.Protocol != "openai-chat" && c.Protocol != "openai-responses" {
			return errors.New("语音转写需要支持音频转写接口的 OpenAI 兼容服务")
		}
	}
	return nil
}
func lookupConfig(ctx context.Context, id string, c *agent.Config, lookup func(context.Context, string) (agent.Config, error)) error {
	v, err := lookup(ctx, id)
	*c = v
	return err
}
