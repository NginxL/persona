package proto

import "github.com/nwverse/persona/server/internal/aiwire"

// model.run: one request of an API purpose answered by a launcher's CLI run headless on a computer
// (docs/assistant/model-config.md "Running API purposes on a launcher", docs/protocol/protocol.md "hub ↔ node RPC").

// MethodModelRun is the node method.
const MethodModelRun = "model.run"

// model.run's error codes (rpc.Error.Code), for the manager to say in the user's language.
const (
	CodeModelRunTimeout   = "model_run_timeout"    // no answer within timeout_ms
	CodeModelRunLoggedOut = "model_run_logged_out" // the CLI is not logged in there
)

// ModelRunParams are model.run's params. System and Prompt are the request's text: the node neither keeps nor logs them.
type ModelRunParams struct {
	Launcher   string `json:"launcher"`
	Configured bool   `json:"configured,omitempty"` // the manager vouches for a launcher its config names (launchers.* "configured")
	Model      string `json:"model,omitempty"`      // "": the launcher's own
	Effort     string `json:"effort,omitempty"`     // "": the launcher's own
	System     string `json:"system,omitempty"`
	Prompt     string `json:"prompt"`
	TimeoutMS  int64  `json:"timeout_ms,omitempty"` // the whole run, waiting for a slot included; the node caps it
}

// ModelRunAnswer is the final text, model and usage reported by the CLI.
type ModelRunAnswer struct {
	Text  string       `json:"text"`
	Model string       `json:"model,omitempty"`
	Usage aiwire.Usage `json:"usage,omitzero"`
}
