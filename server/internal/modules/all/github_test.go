package all

import (
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/extmgr/githubport"
)

// The GitHub module is provided to the extension manager exactly when the build tags keep it. Run with -tags
// persona_without_github or persona_core_only as well (scripts/ci/server.sh).
func TestGitHubFollowsTheBuildTags(t *testing.T) {
	if got, want := githubport.Factory() != nil, slices.Contains(wantModules, "github"); got != want {
		t.Fatalf("github provided: %v, want %v for this build's tags", got, want)
	}
}
