package agent

import (
	"encoding/json"
	"testing"
)

func TestCodeBuddyEnvironmentCompatibility(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   CodeBuddyEnvironment
		valid  bool
	}{
		{"legacy absence inherits deployment", Config{Kind: "sdk", Provider: "codebuddy"}, CodeBuddyEnvironmentInherit, true},
		{"ioa", Config{Kind: "sdk", Provider: "codebuddy", CodeBuddyEnvironment: CodeBuddyEnvironmentIOA}, CodeBuddyEnvironmentIOA, true},
		{"cn", Config{Kind: "sdk", Provider: "codebuddy", CodeBuddyEnvironment: CodeBuddyEnvironmentCN}, CodeBuddyEnvironmentCN, true},
		{"international", Config{Kind: "sdk", Provider: "codebuddy", CodeBuddyEnvironment: CodeBuddyEnvironmentInternational}, CodeBuddyEnvironmentInternational, true},
		{"unknown enum", Config{Kind: "sdk", Provider: "codebuddy", CodeBuddyEnvironment: 99}, 0, false},
		{"other SDK rejects environment", Config{Kind: "sdk", Provider: "claude", CodeBuddyEnvironment: CodeBuddyEnvironmentIOA}, 0, false},
		{"API rejects environment", Config{Kind: "api", Protocol: "openai-chat", BaseURL: "https://example.invalid/v1", CodeBuddyEnvironment: CodeBuddyEnvironmentIOA}, 0, false},
		{"API draft needs no model", Config{Kind: "api", Protocol: "anthropic", BaseURL: "https://example.invalid"}, 0, true},
		{"unknown protocol", Config{Kind: "api", Protocol: "codebuddy", BaseURL: "https://example.invalid"}, 0, false},
		{"URL cannot carry credentials", Config{Kind: "sdk", Provider: "codebuddy", BaseURL: "https://key@example.invalid"}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.config
			err := ValidateConnection(&c)
			if (err == nil) != tt.valid {
				t.Fatalf("validation error = %v, want valid %v", err, tt.valid)
			}
			if tt.valid && c.CodeBuddyEnvironment != tt.want {
				t.Fatalf("environment = %d, want %d", c.CodeBuddyEnvironment, tt.want)
			}
		})
	}
}

func TestCodeBuddyEnvironmentWireTypeAndLegacyOmission(t *testing.T) {
	var config Config
	if err := json.Unmarshal([]byte(`{"kind":"sdk","provider":"codebuddy","codebuddyEnvironment":"ioa"}`), &config); err == nil {
		t.Fatal("string environment accepted")
	}
	encoded, err := json.Marshal(Config{Kind: "sdk", Provider: "codebuddy"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["codebuddyEnvironment"]; ok {
		t.Fatal("legacy config hash would change: zero environment must remain omitted")
	}
}
