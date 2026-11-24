package extmgr

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/githubwire"
)

// The user's choice of GitHub's source is kept and applied again at the next start; a machine the hub does not
// know is refused.
func TestGitHubSourceChoice(t *testing.T) {
	h := newTestManager(t)
	h.store = newKeepStore()
	h.connectors = connector.NewRegistry(nil)
	gh := newTestGitHub(t, githubwire.Config{Auth: githubwire.AuthConfig{FromMachine: "mac"}}, "示例 Mac", func(context.Context, string) (string, error) {
		return "", githubwire.ErrOffline
	})
	gh.UseSecrets(h.secretsFor("github"))
	if err := h.addConnector(gh); err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	code, out := call(t, mux, "GET", "/api/v1/connectors/github/auth", h.cfg.Token, "")
	if code != 200 || out["source"] != "machine" || out["machine"] != "mac" {
		t.Fatalf("hub.json's default: %d %v", code, out)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/connectors/github/auth", h.cfg.Token, `{"source":"machine","machine":"nope"}`); code != 400 {
		t.Fatalf("an unknown machine: %d", code)
	}
	code, out = call(t, mux, "PUT", "/api/v1/connectors/github/auth", h.cfg.Token, `{"source":"device"}`)
	if code != 200 || out["source"] != "device" || gh.Source() != githubwire.SourceDevice {
		t.Fatalf("device: %d %v", code, out)
	}
	// The next start: hub.json still says mac, the kept choice wins.
	gh2 := newTestGitHub(t, githubwire.Config{Auth: githubwire.AuthConfig{FromMachine: "mac"}}, "示例 Mac", nil)
	h.connectors = connector.NewRegistry(nil)
	_ = h.addConnector(gh2)
	h.restoreGitHubSource(context.Background())
	if gh2.Source() != githubwire.SourceDevice {
		t.Fatalf("restored %s", gh2.Source())
	}
}
