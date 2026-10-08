package service

import (
	"fmt"
	"slices"
	"strings"
)

// Reject contradictions with declared output types before scheduling. Open or
// unspecified schemas still require the runtime checks in job.Notification.
// The caller has already checked the reference syntax and step existence.
func validateNotificationOutput(expression string, schema map[string]any, want string) error {
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
