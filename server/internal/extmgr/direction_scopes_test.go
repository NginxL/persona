package extmgr

import (
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

// The write scope opens only the two direction writes. leaders.read neither writes nor is implied by it; the hub
// refuses a missing grant before forwarding a request to the assistant manager.
func TestDirectionWriteScopes(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	// The synthetic manager does not mount the assistant service. Mount successful endpoints so these requests test
	// their scope decision rather than being refused as unknown routes before the grant can matter.
	for _, pattern := range []string{
		"POST /api/v1/assistant/directions",
		"POST /api/v1/assistant/directions/{id}/progress",
		"GET /api/v1/assistant/leaders",
	} {
		h.apiMux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	}
	reader := h.extTokens.issue("direction-reader", "Direction reader", []string{extension.ScopeLeadersRead})
	writer := h.extTokens.issue("direction-writer", "Direction writer", []string{extension.ScopeLeadersWrite})
	for _, c := range []struct {
		name, token, method, path, body string
	}{
		{"reader cannot create", reader, http.MethodPost, "/api/v1/assistant/directions", `{"name":"Release","project":"release"}`},
		{"reader cannot report", reader, http.MethodPost, "/api/v1/assistant/directions/dir_1/progress", `{"doing":"x"}`},
		{"writer cannot read", writer, http.MethodGet, "/api/v1/assistant/leaders", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out := call(t, mux, c.method, c.path, c.token, c.body)
			if code != http.StatusForbidden || out["code"] != "permission_denied" {
				t.Fatalf("%s %s: %d %v, want 403 permission_denied", c.method, c.path, code, out)
			}
		})
	}
}
