package node

import (
	"sync"
)

// Claude Code's tool search for the assistant's claude (#5838, #7317, #10365). Persona marks the tools a conversation
// uses all the time alwaysLoad and leaves the rest, and other MCP servers' tools, behind ToolSearch. Claude Code itself
// keeps tool search off behind a Base URL that is not Anthropic's API, but any ENABLE_TOOL_SEARCH value lifts that
// rule: with it on, requests carry defer_loading tools, tool_reference blocks and the ToolSearch tool, which a gateway
// may not represent (400 "request is not representable on this edge: ToolSearch is not supported on this endpoint").
// So a start decides per endpoint (toolSearchFor), and a refusal is retried once without it and remembered.

// toolSearchEnv is Claude Code's switch: "true" (or unset) defers, "false" loads every tool up front.
const toolSearchEnv = "ENABLE_TOOL_SEARCH"

// toolSearchStart is what a claude start does about tool search: env, the ENABLE_TOOL_SEARCH its process gets ("" none,
// Claude Code decides), and forceOff, "false" in the start's own settings too, which win over every other settings env
// (the provider's, the launcher's, the user's).
type toolSearchStart struct {
	env      string
	forceOff bool
}

// toolSearchFor is the tool search of a start whose requests go to endpoint (an ANTHROPIC_BASE_URL, "" Anthropic's own
// API), known when the start could tell where they go (a managed provider, a launcher the probe saw), own when the
// launcher sets ENABLE_TOOL_SEARCH itself, refused when this endpoint refused tool search for the launcher and model:
//
//   - refused: off, also in the start's settings (the endpoint said no; a second refusal is the user's to see).
//   - own: nothing written, the launcher's value stands.
//   - not known: nothing written, Claude Code decides (its own rule for a gateway).
//   - Anthropic's own API: true, which also lifts Claude Code's hold-off for a conversation that once ran without
//     ToolSearch (#7317).
//   - a gateway: false, every tool loaded up front. Never true there: it would lift Claude Code's gateway rule.
func toolSearchFor(endpoint string, known, own, refused bool) toolSearchStart {
	switch {
	case refused:
		return toolSearchStart{env: "false", forceOff: true}
	case own, !known:
		return toolSearchStart{}
	case anthropicEndpoint(endpoint):
		return toolSearchStart{env: "true"}
	}
	return toolSearchStart{env: "false"}
}

// toolSearchRefusals are the launcher and model pairs whose endpoint refused tool search, with that endpoint, by the
// node's directory: the Nodes of one process (one a connection) share them. In memory only (no state files in
// ~/.persona): a restarted node learns a refusal again, with one invisible retry.
var toolSearchRefusals = struct {
	sync.Mutex
	got map[string]string
}{got: map[string]string{}}

func (n *Node) toolSearchKey(launcher, model string) string {
	return n.dir + "\x00" + launcher + "\x00" + model
}

// noteToolSearchRefused remembers that endpoint refused tool search for launcher running model; it says whether that is
// news.
func (n *Node) noteToolSearchRefused(launcher, model, endpoint string) bool {
	k := n.toolSearchKey(launcher, model)
	toolSearchRefusals.Lock()
	defer toolSearchRefusals.Unlock()
	old, had := toolSearchRefusals.got[k]
	toolSearchRefusals.got[k] = endpoint
	return !had || old != endpoint
}

// toolSearchRefused: endpoint refused tool search for launcher running model. One remembered for another endpoint (the
// provider's Base URL changed since) does not count.
func (n *Node) toolSearchRefused(launcher, model, endpoint string) bool {
	toolSearchRefusals.Lock()
	defer toolSearchRefusals.Unlock()
	got, ok := toolSearchRefusals.got[n.toolSearchKey(launcher, model)]
	return ok && got == endpoint
}

// assistantToolSearch is the tool search of a claude start of launcher running model, its endpoint as the compaction
// policy read it (c).
func (n *Node) assistantToolSearch(launcher, model string, c assistantCompaction) toolSearchStart {
	return toolSearchFor(c.endpoint, c.endpointKnown, c.ownToolSearch, n.toolSearchRefused(launcher, model, c.endpoint))
}

// toolSearchStale: p, a claude process not started with tool search forced off, runs a launcher and model whose
// endpoint has refused it since. Its next start is: an idle conversation restarts on its next message (assistantSend),
// a spare is not adopted.
func (n *Node) toolSearchStale(p *assistantProc) bool {
	return p != nil && p.agent == "claude" && !p.toolSearch.forceOff && n.toolSearchRefused(p.launcher, p.model.Model, p.endpoint)
}
