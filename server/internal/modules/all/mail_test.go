package all

import (
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/extmgr/mailport"
)

// The mail module is provided to the extension manager exactly when the build tags keep it. Run with -tags
// persona_without_mail or persona_core_only as well (scripts/ci/server.sh).
func TestMailFollowsTheBuildTags(t *testing.T) {
	if got, want := mailport.Factory() != nil, slices.Contains(wantModules, "mail"); got != want {
		t.Fatalf("mail provided: %v, want %v for this build's tags", got, want)
	}
}
