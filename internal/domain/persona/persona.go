package persona

type Example struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Persona struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	SystemPrompt string    `json:"systemPrompt"`
	Examples     []Example `json:"examples"`
	Preferences  string    `json:"preferences"`
	Tools        []string  `json:"tools"`
	Version      int       `json:"version"`
	Default      bool      `json:"default"`
}
