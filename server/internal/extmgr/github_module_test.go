package extmgr

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extmgr/githubport"
	"github.com/nwverse/persona/server/internal/githubwire"
)

// needsGitHubModule is the GitHub module's factory, as the manager gets it (githubport, filled by internal/modules/all,
// which modules_link_test.go links); the test is skipped in a build without the module.
func needsGitHubModule(t *testing.T) githubwire.Factory {
	t.Helper()
	f := githubport.Factory()
	if f == nil {
		t.Skip("this build has no GitHub module (persona_without_github or persona_core_only)")
	}
	return f
}

// newTestGitHub is the module's GitHub connector for cfg, reading machine's token through fetch (nil: the device flow).
func newTestGitHub(t *testing.T, cfg githubwire.Config, machine string, fetch githubwire.TokenSource) githubwire.Connector {
	t.Helper()
	return needsGitHubModule(t)(cfg, machine, fetch)
}

// Without the GitHub module (persona_without_github, persona_core_only) the manager registers no built-in GitHub
// connector: none is listed, its sign-in route answers 404, and no github_* tool reaches the models. An extension's
// GitHub connector can still take the id.
func TestManagerWithoutGitHub(t *testing.T) {
	if githubport.Factory() != nil {
		t.Skip("this build has the GitHub module")
	}
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	h.registerConnectors()
	if c := h.connectors.Get(githubwire.ID); c != nil || h.githubConnector() != nil || h.ownGitHub() != nil {
		t.Fatalf("a GitHub connector without the module: %v", c)
	}
	for _, tool := range []string{"github_read", "github_search", "github_comment"} {
		if h.connectors.Owner(tool) != nil || h.connectors.Offers(tool) {
			t.Errorf("%s has an owner without the GitHub module", tool)
		}
	}
	mux := http.NewServeMux()
	h.githubAuthRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/connectors/github/auth", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/connectors/github/auth: %d, want 404", rec.Code)
	}
}
