package assistantwire

// IdleCompaction applies only to unattended messages in the main conversation.
// A missing policy keeps older managers' delivery behavior unchanged.
type IdleCompaction struct {
	Enabled       bool `json:"enabled"`
	IdleMinutes   int  `json:"idle_minutes"`
	ContextTokens int  `json:"context_tokens"`
}
