package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

func TestDispatchGuardScopeAuthorization(t *testing.T) {
	h := newTestHub(t)
	_ = h.routes()
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		pattern := method + " /api/v1/assistant/dispatch-guard"
		path := "/api/v1/assistant/dispatch-guard"
		req := httptest.NewRequest(method, path, nil)
		if _, routePattern := h.apiMux.Handler(req); routePattern != pattern {
			t.Fatalf("mux pattern = %q, want %q", routePattern, pattern)
		}
		if got := extwire.RouteScopes[pattern]; got != extension.ScopeDispatchGuard {
			t.Fatalf("%s scope = %q", pattern, got)
		}
		if ok, scope := fakeScopeOpens([]string{extension.ScopeDispatchGuard}, pattern, method, path); !ok || scope != extension.ScopeDispatchGuard {
			t.Fatalf("%s with dispatch.guard: ok=%v scope=%q", method, ok, scope)
		}
		if ok, scope := fakeScopeOpens([]string{extension.ScopeAgentsControl}, pattern, method, path); ok || scope != extension.ScopeDispatchGuard {
			t.Fatalf("%s without dispatch.guard: ok=%v scope=%q", method, ok, scope)
		}
	}
	if title, ok := extension.ScopeTitle(extension.ScopeDispatchGuard); !ok || title == "" {
		t.Fatalf("scope title: %q %v", title, ok)
	}
	if extension.InEveryScope(extension.ScopeDispatchGuard) {
		t.Fatal("the legacy every-scope grant includes dispatch.guard")
	}
}
