package service

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	taskentity "github.com/xingexin/catbot/internal/domain/task/entity"
)

func Notification(t taskentity.Task, status, failure string, results map[string]any) (string, bool, error) {
	if status != "completed" {
		return t.Name + "：" + status + "\n" + failure, true, nil
	}
	if t.NotifyWhen != "" {
		v, err := Resolve(t.NotifyWhen, results)
		if err != nil {
			return "", false, err
		}
		changed, ok := v.(bool)
		if !ok {
			return "", false, errors.New("notification condition must resolve to a boolean")
		}
		if !changed {
			return "", false, nil
		}
	}
	if t.NotifyText != "" {
		v, err := Resolve(t.NotifyText, results)
		if err != nil {
			return "", false, err
		}
		text, ok := v.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return "", false, errors.New("notification text must resolve to nonempty text")
		}
		return text, true, nil
	}
	if len(t.Steps) > 0 {
		last, _ := results[t.Steps[len(t.Steps)-1].ID].(map[string]any)
		for _, key := range []string{"notificationText", "text"} {
			if text, ok := last[key].(string); ok && strings.TrimSpace(text) != "" {
				return text, true, nil
			}
		}
	}
	return t.Name + "已完成，详细结果可在管理端查看。", true, nil
}

var reference = regexp.MustCompile(`^\$\{steps\.([a-zA-Z0-9_-]+)(?:\.([^}]+))?\}$`)

func ReferenceStep(expression string) (string, error) {
	match := reference.FindStringSubmatch(expression)
	if match == nil || strings.HasSuffix(expression, ".}") {
		return "", errors.New("notification must be a single step result reference")
	}
	return match[1], nil
}

func Resolve(value any, results map[string]any) (any, error) {
	switch x := value.(type) {
	case string:
		m := reference.FindStringSubmatch(x)
		if m == nil {
			return x, nil
		}
		cur, ok := results[m[1]]
		if !ok {
			return nil, fmt.Errorf("step reference not found: %s", m[1])
		}
		if m[2] != "" {
			for _, key := range strings.Split(m[2], ".") {
				obj, ok := cur.(map[string]any)
				if !ok {
					return nil, errors.New("step reference is not an object")
				}
				cur, ok = obj[key]
				if !ok {
					return nil, fmt.Errorf("step reference field not found: %s", key)
				}
			}
		}
		return cur, nil
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			r, err := Resolve(v, results)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := []any{}
		for _, v := range x {
			r, err := Resolve(v, results)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, nil
	default:
		return value, nil
	}
}
func ValidateNotificationOutput(expression string, schema map[string]any, want string) error {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(expression, "${steps."), "}"), ".")
	for _, field := range parts[1:] {
		if schema == nil || complexNotificationSchema(schema) {
			return nil
		}
		if !notificationSchemaAllows(schema, "object") {
			return fmt.Errorf("%s traverses a non-object output", expression)
		}
		properties, _ := schema["properties"].(map[string]any)
		if child, ok := properties[field]; ok {
			schema, _ = child.(map[string]any)
			continue
		}
		if _, ok := schema["patternProperties"]; ok {
			return nil
		}
		if schema["additionalProperties"] == false {
			return fmt.Errorf("%s references an undeclared output field", expression)
		}
		// Unknown fields may have a declared additional-properties schema.
		schema, _ = schema["additionalProperties"].(map[string]any)
	}
	if schema != nil && !complexNotificationSchema(schema) && !notificationSchemaAllows(schema, want) {
		return fmt.Errorf("%s must reference a %s output, not %v", expression, want, schema["type"])
	}
	return nil
}

func complexNotificationSchema(schema map[string]any) bool {
	for _, key := range []string{"$ref", "allOf", "anyOf", "oneOf", "if", "not"} {
		if _, ok := schema[key]; ok {
			return true
		}
	}
	return false
}

func notificationSchemaAllows(schema map[string]any, want string) bool {
	switch kind := schema["type"].(type) {
	case string:
		return kind == want
	case []string:
		return slices.Contains(kind, want)
	case []any:
		return slices.Contains(kind, any(want))
	default:
		return true
	}
}
