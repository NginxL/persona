package hub

import (
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
)

const skipNextTestPath = "/api/v1/assistant/tasks/example/skip-next"

func TestScheduleSkipNextRelayOwnerScope(t *testing.T) {
	h := newTestHub(t)
	api := h.routes()
	tokens := extTokensOf(t, h)
	token := tokens.issue("schedule-check", "Schedule check", []string{extwire.ScopeAgentsRead, extwire.ScopeAgentsControl})
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if allowed(principal{Role: roleAnswer}, method, skipNextTestPath) || allowed(principal{Role: roleLink}, method, skipNextTestPath) {
			t.Fatalf("%s widened a restricted device's task permissions", method)
		}
		if status, _ := call(t, api, method, skipNextTestPath, token, "{}"); status != http.StatusForbidden {
			t.Fatalf("%s skip-next accepted an extension token: %d", method, status)
		}
	}
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	if len(tokens.served) != 0 {
		t.Fatalf("denied writes reached a manager: %v", tokens.served)
	}
}

func TestScheduleSkipNextRelaysToTheSelectedManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	connectFakeManager(t, h, srv, assistantwire.AssistantID)
	const named = "m-0a0b0c"
	connectFakeManager(t, h, srv, named)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		code, out := ownershipGet(t, h, srv, method, skipNextTestPath, map[string]string{ManagerHeader: named})
		if code != http.StatusOK || out["who"] != named {
			t.Fatalf("%s skip-next reached the wrong manager: %d %v", method, code, out)
		}
	}
}
