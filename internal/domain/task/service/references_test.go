package service

import (
	"testing"
)

func TestTypedTaskReferences(t *testing.T) {
	result, err := Resolve(map[string]any{"id": "${steps.read.id}", "literal": "hello"}, map[string]any{"read": map[string]any{"id": 42}})
	if err != nil || result.(map[string]any)["id"] != 42 {
		t.Fatal(result, err)
	}
	if _, err := Resolve("${steps.absent.id}", map[string]any{}); err == nil {
		t.Fatal("missing reference accepted")
	}
}
