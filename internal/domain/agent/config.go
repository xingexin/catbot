package agent

import (
	"errors"
	"net/url"
)

func ValidateConfig(c *Config) error {
	if c.Name == "" || c.Model == "" {
		return errors.New("name and model are required")
	}
	if c.Kind != "sdk" && c.Kind != "api" {
		return errors.New("kind must be sdk or api")
	}
	if c.Kind == "sdk" {
		if c.Provider != "codebuddy" && c.Provider != "claude" && c.Provider != "codex" {
			return errors.New("unsupported agent SDK")
		}
		if c.Capabilities.Images {
			return errors.New("Agent SDK image input is not supported; use an image-capable API configuration")
		}
		// Tools is an administrator opt-in, unlike the SDK's fixed transport
		// capabilities. Preserve an explicit opt-out when validating a save.
		c.Capabilities.Stream = true
		c.Capabilities.Resume = true
	} else {
		switch c.Protocol {
		case "openai-chat", "openai-responses", "anthropic":
		default:
			return errors.New("unsupported API protocol")
		}
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("baseUrl must be an HTTP(S) URL without credentials, query or fragment")
		}
	} else if c.Kind == "api" {
		return errors.New("baseUrl is required")
	}
	if c.MaxSteps == 0 {
		c.MaxSteps = 12
	}
	if c.MaxSteps < 1 || c.MaxSteps > 50 {
		return errors.New("maxSteps must be 1..50")
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 4096
	}
	if c.MaxTokens < 1 || c.MaxTokens > 32768 {
		return errors.New("maxTokens must be 1..32768")
	}
	if c.MaxInputBytes == 0 {
		c.MaxInputBytes = DefaultMaxInputBytes
	}
	if c.MaxInputBytes < 8<<10 || c.MaxInputBytes > 2<<20 {
		return errors.New("maxInputBytes must be 8192..2097152 (serialized API conversation bytes, not tokens)")
	}
	if c.TimeoutSec == 0 {
		c.TimeoutSec = 180
	}
	if c.TimeoutSec < 1 || c.TimeoutSec > 3600 {
		return errors.New("timeoutSec must be 1..3600")
	}
	return nil
}
