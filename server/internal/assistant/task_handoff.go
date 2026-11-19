package assistant

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// taskHandoff is rendered after placement and launcher fitting. An optional node.info read must never turn a
// usable assignment into a failed or indefinitely waiting start merely because its handoff hint is unavailable.
func (s *Service) taskHandoff(ctx context.Context, machine string, fit startAgent) string {
	source := compat.Source(fit.source)
	// Empty is a legacy shell launcher (proto.Launcher). A runtime or app's same-family agent does not establish
	// terminal caller identity. Paseo injects the existing Persona MCP server into its Claude and Codex providers.
	terminal := source == "" || source == "zshrc" || source == "path" || source == "persona" || source == "builtin" && fit.agent == "claude"
	if source == "persona" && fit.agent == "agy" {
		terminal = false // managed agy starts on its runtime (node/runtimes_start.go), not a pane
	}
	mcp := terminal && fit.agent == "claude" || source == proto.EnginePaseo && (fit.agent == "claude" || fit.agent == "codex")
	h := prompts.TaskHandoff{Language: answerLanguage(s.hello(ctx).Locale), MCP: mcp}
	if terminal && (fit.agent == "codex" || proto.IsWorkAgentType(fit.agent) || proto.IsNoModeAgentType(fit.agent)) {
		cctx, cancel := context.WithTimeout(ctx, min(agentStartListTimeout, time.Second))
		defer cancel()
		var info proto.NodeInfo
		if s.call(cctx, machine, "node.info", map[string]any{}, &info) == nil && proto.NodeAbs(info.Executable) {
			// Keep paths, including quotes and line breaks, as JSON data rather than constructing a shell command.
			encoded, _ := json.Marshal(info.Executable)
			h.Executable = string(encoded)
		}
	}
	return prompts.RenderTaskHandoff(h)
}
