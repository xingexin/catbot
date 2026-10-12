package agent

import (
	"errors"
	"strings"
)

type ModelCatalogSource int

const (
	ModelCatalogUnknown ModelCatalogSource = 0
	ModelCatalogLive    ModelCatalogSource = 1
)

type ModelOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ModelCatalog struct {
	Models []ModelOption      `json:"models"`
	Source ModelCatalogSource `json:"source"`
}

// Validate never substitutes a static catalog or invents model capabilities.
func (c *ModelCatalog) Validate() error {
	if c.Source != ModelCatalogLive || len(c.Models) == 0 || len(c.Models) > 5000 {
		return errors.New("未取得有效的模型列表，请检查接入配置或手动填写模型名称")
	}
	seen := map[string]bool{}
	models := make([]ModelOption, 0, len(c.Models))
	for _, model := range c.Models {
		if strings.TrimSpace(model.ID) == "" || len(model.ID) > 1024 || len(model.Name) > 2048 || len(model.Description) > 8192 {
			return errors.New("模型列表格式异常，请手动填写模型名称")
		}
		if seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		if model.Name == "" {
			model.Name = model.ID
		}
		models = append(models, model)
	}
	c.Models = models
	return nil
}
