// Package aiwire is the model configuration as the contract carries it (docs/server/architecture.md "Contract packages"):
// the manager's model settings travel over the bus in it. Types and pure helpers only; internal/ai, which calls the
// models, aliases it.
package aiwire

// Config is a model to call: the gateway, its API, the key and the model (manager.json's "ai" and the per-task models).
type Config struct {
	// API is "messages" (Anthropic Messages, the default) "responses" (OpenAI Responses), or "chat" (OpenAI Chat Completions).
	API        string `json:"api"`
	BaseURL    string `json:"base_url"`
	APIKeyFile string `json:"api_key_file"`
	// APIKey is an in-memory credential supplied by the hub's encrypted provider store.
	APIKey string `json:"-"`
	// Headers contains provider-specific HTTP fields in memory. Values are credentials too: never serialize them, and
	// drop them on any redirect chain that leaves the original origin.
	Headers map[string]string `json:"-"`
	// UserAgent overrides the HTTP default when non-nil, including when it points to an empty string.
	UserAgent *string `json:"-"`
	Model     string  `json:"model"`
	// ReasoningEffort is sent to Chat Completions as reasoning_effort, Responses as reasoning.effort and Messages as
	// output_config.effort. "off" maps to Chat/Responses "none" and the model's supported Messages no-thinking mode,
	// or low effort when it cannot disable it.
	ReasoningEffort string `json:"reasoning_effort"`
	// Bearer sends the key to the Messages API as Authorization: Bearer instead of x-api-key, for gateways that take
	// only that (what Claude Code does with ANTHROPIC_AUTH_TOKEN). Responses and Chat Completions always use Bearer.
	Bearer bool `json:"bearer,omitempty"`
	// ViaMachine is the computer whose node sends the requests (an API connection's "Send through"); empty: the
	// manager sends them itself. The manager's client is built for it (assistant/models_via.go).
	ViaMachine string `json:"-"`
	// RunLauncher is a launcher on ViaMachine whose CLI answers each request headless there instead of an HTTP API: no
	// address or key here (assistant/models_run.go, docs/assistant/model-config.md "Running API purposes on a launcher").
	RunLauncher string `json:"-"`
}

// Over is c with its empty fields taken from base: a task that uses another model names only what differs.
func (c Config) Over(base Config) Config {
	if c.API == "" {
		c.API = base.API
	}
	if c.BaseURL == "" { // the gateway, and how it takes the key
		c.BaseURL, c.Bearer = base.BaseURL, base.Bearer
	}
	if c.APIKeyFile == "" {
		c.APIKeyFile = base.APIKeyFile
	}
	if c.Model == "" {
		c.Model = base.Model
	}
	if c.ReasoningEffort == "" {
		c.ReasoningEffort = base.ReasoningEffort
	}
	return c
}

// Configured says whether there is a model to call: an address, a model and a key (or a key file). Nothing is
// assumed (it used to default to one person's gateway and key file): without a model set up, the helpers that need
// one say so.
func (c Config) Configured() bool {
	if c.RunLauncher != "" {
		return c.ViaMachine != ""
	}
	return c.BaseURL != "" && c.Model != "" && (c.APIKeyFile != "" || c.APIKey != "")
}
