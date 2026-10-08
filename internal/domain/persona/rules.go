package persona

import (
	"errors"
)

func Validate(p Persona) error {
	if p.Name == "" || p.SystemPrompt == "" {
		return errors.New("name and systemPrompt are required")
	}
	for _, m := range p.Examples {
		if m.Role != "user" && m.Role != "assistant" {
			return errors.New("example role must be user or assistant")
		}
	}

	return nil
}
