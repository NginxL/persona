package node

import (
	"context"
	"encoding/json"
)

type agentPromptLanguageKey struct{}

// promptLanguageContext is per call, never node-global: concurrent sessions and
// reconnects cannot reuse another send's language. Missing old-manager fields
// retain the historical Chinese wrapper. This does not carry authority.
func promptLanguageContext(ctx context.Context, raw json.RawMessage) context.Context {
	var p struct {
		Language string `json:"prompt_language"`
	}
	_ = json.Unmarshal(raw, &p)
	return context.WithValue(ctx, agentPromptLanguageKey{}, p.Language)
}

func agentPromptLanguage(ctx context.Context) string {
	language, _ := ctx.Value(agentPromptLanguageKey{}).(string)
	return language
}
