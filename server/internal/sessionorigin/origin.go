// Package sessionorigin classifies where runtime sessions were opened.
package sessionorigin

import (
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// Of returns where a runtime session was opened, matching the clients'
// sessionSource rule. SourceKind describes transport, not pane/host origin.
// Desktop markers take precedence even when a row also carries a terminal pane.
func Of(s proto.Session) string {
	if s.SpaceKind == "codex_app" || s.Host == "paseo" || s.Host == "cindy" || s.Host == "t3code" || (s.SourceKind == "runtime" && s.SourceInstanceID == "orca") {
		return "desktop"
	}
	if s.PaneID == "" {
		return "unknown"
	}
	// A stored pane names its host before ":" (host.ParsePane). Read here so the hub, which links this package, does
	// not link the manager-side host package (depguard).
	if kind, _, ok := strings.Cut(s.PaneID, ":"); ok && kind == "botmux" {
		return "feishu"
	}
	return "terminal"
}

// ChatBridge: s runs in a Lark chat bridge, which answers in the topic each turn was asked in (the finished push leaves
// such a turn alone, assistant/fleet_push.go chatBridgeFinish): a pane of the bridge itself (Of: "feishu"), or a pane
// of the herdr session the bridge runs its agents in when its backend is herdr. The bridge names that session after
// itself, as its own panes are named (its managed session), or "bmx-" and the first characters of its session id (older
// versions); the node does not list such an agent as the bridge's but as a herdr pane of a named session
// ("@<session>:<id>", herdr.QualifyID), which Of calls a terminal, as the clients do: it is one, and can be opened.
func ChatBridge(s proto.Session) bool {
	switch Of(s) {
	case "feishu":
		return true
	case "terminal":
	default:
		return false
	}
	rest, named := strings.CutPrefix(s.PaneID, "@")
	session, _, ok := strings.Cut(rest, ":")
	if !named || !ok || session == "" {
		return false
	}
	return Of(proto.Session{PaneID: session + ":"}) == "feishu" || strings.HasPrefix(session, "bmx-")
}
