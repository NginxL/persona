package extmgr

import (
	"testing"

	"github.com/nwverse/persona/server/internal/botport"
)

// needsLarkModule skips a test of the Feishu / Lark bot in a build without the lark module (persona_without_lark,
// persona_core_only): the bot's client is the module's (internal/botport).
func needsLarkModule(t testing.TB) {
	t.Helper()
	if !botport.Available() {
		t.Skip("the lark module is not in this build")
	}
}
