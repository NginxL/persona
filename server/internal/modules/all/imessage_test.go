package all

import (
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/extmgr/imessageport"
)

// The iMessage module is provided to the extension manager exactly when the build tags keep it. Run with -tags
// persona_without_imessage or persona_core_only as well (scripts/ci/server.sh).
func TestIMessageFollowsTheBuildTags(t *testing.T) {
	if got, want := imessageport.Factory() != nil, slices.Contains(wantModules, "imessage"); got != want {
		t.Fatalf("imessage provided: %v, want %v for this build's tags", got, want)
	}
}
