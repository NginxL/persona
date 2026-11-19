package aiwire

// Usage is what one request spent, as the upstream counted it (zero where it did not say).
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// Add is u and v together (a call made of several requests).
func (u Usage) Add(v Usage) Usage {
	return Usage{InputTokens: u.InputTokens + v.InputTokens, OutputTokens: u.OutputTokens + v.OutputTokens,
		CacheReadInputTokens: u.CacheReadInputTokens + v.CacheReadInputTokens, CacheCreationInputTokens: u.CacheCreationInputTokens + v.CacheCreationInputTokens}
}
