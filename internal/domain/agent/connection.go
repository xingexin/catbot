package agent

import (
	"errors"
	"net/url"
)

// These enums classify legacy wire strings at the configuration boundary.
type ExecutionKind int

const (
	ExecutionUnknown ExecutionKind = 0
	ExecutionAPI     ExecutionKind = 1
	ExecutionSDK     ExecutionKind = 2
)

func ParseExecutionKind(value string) ExecutionKind {
	switch value {
	case "api":
		return ExecutionAPI
	case "sdk":
		return ExecutionSDK
	default:
		return ExecutionUnknown
	}
}

type SDKProvider int

const (
	SDKUnknown   SDKProvider = 0
	SDKCodeBuddy SDKProvider = 1
	SDKClaude    SDKProvider = 2
	SDKCodex     SDKProvider = 3
)

func ParseSDKProvider(value string) SDKProvider {
	switch value {
	case "codebuddy":
		return SDKCodeBuddy
	case "claude":
		return SDKClaude
	case "codex":
		return SDKCodex
	default:
		return SDKUnknown
	}
}

type Protocol int

const (
	ProtocolUnknown         Protocol = 0
	ProtocolOpenAIChat      Protocol = 1
	ProtocolOpenAIResponses Protocol = 2
	ProtocolAnthropic       Protocol = 3
)

func ParseProtocol(value string) Protocol {
	switch value {
	case "openai-chat":
		return ProtocolOpenAIChat
	case "openai-responses":
		return ProtocolOpenAIResponses
	case "anthropic":
		return ProtocolAnthropic
	default:
		return ProtocolUnknown
	}
}

// Zero is accepted only as the legacy absence of a per-config environment.
// ValidateConnection makes new saved CodeBuddy configurations explicit.
type CodeBuddyEnvironment int

const (
	CodeBuddyEnvironmentUnknown       CodeBuddyEnvironment = 0
	CodeBuddyEnvironmentInherit       CodeBuddyEnvironment = 1
	CodeBuddyEnvironmentIOA           CodeBuddyEnvironment = 2
	CodeBuddyEnvironmentCN            CodeBuddyEnvironment = 3
	CodeBuddyEnvironmentInternational CodeBuddyEnvironment = 4
)

// ValidateConnection supports model discovery before a name/model is chosen.
func ValidateConnection(c *Config) error {
	kind := ParseExecutionKind(c.Kind)
	switch kind {
	case ExecutionSDK:
		if ParseSDKProvider(c.Provider) == SDKUnknown {
			return errors.New("unsupported agent SDK")
		}
	case ExecutionAPI:
		if ParseProtocol(c.Protocol) == ProtocolUnknown {
			return errors.New("unsupported API protocol")
		}
	default:
		return errors.New("kind must be sdk or api")
	}
	if kind == ExecutionSDK && ParseSDKProvider(c.Provider) == SDKCodeBuddy {
		switch c.CodeBuddyEnvironment {
		case CodeBuddyEnvironmentUnknown:
			c.CodeBuddyEnvironment = CodeBuddyEnvironmentInherit
		case CodeBuddyEnvironmentInherit, CodeBuddyEnvironmentIOA, CodeBuddyEnvironmentCN, CodeBuddyEnvironmentInternational:
		default:
			return errors.New("无效的 CodeBuddy 接入环境")
		}
	} else if c.CodeBuddyEnvironment != CodeBuddyEnvironmentUnknown {
		return errors.New("CodeBuddy 接入环境仅适用于 CodeBuddy Agent SDK")
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("baseUrl must be an HTTP(S) URL without credentials, query or fragment")
		}
	} else if kind == ExecutionAPI {
		return errors.New("baseUrl is required")
	}
	return nil
}
