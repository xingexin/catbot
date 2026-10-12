package agent

import (
	"errors"
)

func ValidateConfig(c *Config) error {
	if c.Name == "" || c.Model == "" {
		return errors.New("name and model are required")
	}
	if err := ValidateConnection(c); err != nil {
		return err
	}
	if ParseExecutionKind(c.Kind) == ExecutionSDK {
		if c.Capabilities.Images {
			return errors.New("Agent SDK image input is not supported; use an image-capable API configuration")
		}
		// Tool access stays opt-in while stream/resume are SDK capabilities.
		c.Capabilities.Stream = true
		c.Capabilities.Resume = true
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
