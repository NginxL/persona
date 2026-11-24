package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/extwire"
)

// A bot uses the established personal-Agent scopes and device roles; no new chat route or broader credential is introduced.
func TestRegisteredBotEndpointKeepsAgentScopesAndDeviceRoles(t *testing.T) {
	src, err := agents.NewBotRegistry(nil, nil).Source(agents.BotRegistration{
		ID: "research", Provider: "orca", Title: "Research bot", MachineID: "machine-b", SessionID: "claude_exact_registered",
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &Hub{apiMux: http.NewServeMux()}
	h.extensionRoutesRelayed(h.apiMux)
	messages := "/api/v1/agents/sessions/bot_research%3Aendpoint/messages"
	for _, tc := range []struct {
		method, scope string
	}{
		{http.MethodGet, extwire.ScopeAgentsRead},
		{http.MethodPost, extwire.ScopeAgentsControl},
	} {
		r := httptest.NewRequest(tc.method, messages, nil)
		if got := h.scopedDeviceRoute(r); got != tc.scope {
			t.Errorf("%s scope %q, want %q", tc.method, got, tc.scope)
		}
		if allowed(principal{Role: roleAnswer}, tc.method, messages) {
			t.Errorf("%s answer-only device reached bot history or free-form send", tc.method)
		}
		if !allowed(principal{Role: roleFull}, tc.method, messages) {
			t.Errorf("%s full device lost existing Agents access", tc.method)
		}
	}
	if src.Name() != "bot_research" {
		t.Fatalf("source name %q", src.Name())
	}
}
