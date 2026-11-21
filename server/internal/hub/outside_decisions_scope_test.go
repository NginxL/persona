package hub

import (
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

// Raising an owner interruption needs its own named grant. Broad to-do access
// and a legacy every-scope token do not implicitly include it; the scope never
// opens the owner's answer routes.
func TestOutsideDecisionRoutesRequireTheirScope(t *testing.T) {
	h := newTestHub(t)
	mux := h.routes()
	tokens := extTokensOf(t, h)
	for name, grants := range map[string][]string{
		"none":         nil,
		"todos":        {extension.ScopeTodosRead, extension.ScopeTodosWrite},
		"legacy-every": extension.EveryScope(),
	} {
		t.Run(name, func(t *testing.T) {
			token := tokens.issue("outside-"+name, "Outside "+name, grants)
			for _, route := range []struct{ method, path string }{
				{"POST", "/api/v1/decisions"},
				{"GET", "/api/v1/decisions/odc_example"},
				{"POST", "/api/v1/decisions/odc_example/withdraw"},
			} {
				status, body := call(t, mux, route.method, route.path, token, `{}`)
				if status != http.StatusForbidden || body["code"] != "permission_denied" || body["scope"] != extension.ScopeDecisionsRaise {
					t.Fatalf("%s %s: %d %v", route.method, route.path, status, body)
				}
			}
		})
	}

	granted := tokens.issue("outside-granted", "Outside granted", []string{extension.ScopeDecisionsRaise})
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/v1/decisions"},
		{"GET", "/api/v1/decisions/odc_example"},
		{"POST", "/api/v1/decisions/odc_example/withdraw"},
	} {
		status, body := call(t, mux, route.method, route.path, granted, `{}`)
		if status == http.StatusForbidden && body["code"] == "permission_denied" {
			t.Fatalf("an explicit grant did not open %s %s: %d %v", route.method, route.path, status, body)
		}
	}
	if status, body := call(t, mux, "POST", "/api/v1/todos/todo_example/accept", granted, `{}`); status != http.StatusForbidden || body["code"] != "permission_denied" {
		t.Fatalf("the outside scope answers as the owner: %d %v", status, body)
	}
}
