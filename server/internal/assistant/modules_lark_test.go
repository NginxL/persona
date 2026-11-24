package assistant

import (
	"testing"

	"github.com/nwverse/persona/server/internal/botport"
	"github.com/nwverse/persona/server/internal/larkhost"
)

// needsLarkModule skips a test that drives the Feishu / Lark bot's real client in a build without the lark module
// (persona_without_lark, persona_core_only): the client is the module's (internal/botport).
func needsLarkModule(t testing.TB) {
	t.Helper()
	if !botport.Available() {
		t.Skip("the lark module is not in this build")
	}
}

// needsLarkLayer skips a test of what the assistant shows in the Feishu chat (its cards, notes and thread lines) in a
// build without the lark module: the outbound channel port is then larkhost.None, a bot switched off.
func needsLarkLayer(t testing.TB) {
	t.Helper()
	if !larkhost.InBuild() {
		t.Skip("the lark module is not in this build")
	}
}
