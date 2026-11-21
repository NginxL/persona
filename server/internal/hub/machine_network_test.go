package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func callMachineNetworkRoute(h *Hub, role, target string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, strings.NewReader(body))
	r.SetPathValue("mid", "m1")
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: role, ID: "dev"}))
	w := httptest.NewRecorder()
	h.machineNetwork()(w, r)
	return w
}

func TestMachineNetworkRouteNeedsFullDeviceAndIgnoresCallerParams(t *testing.T) {
	const sampleReport = `{"checked_at":1700000000,"interfaces":[{"name":"eth0","up":true,"addresses":["192.0.2.10"]}],"proxy":{"https_proxy":"http://[redacted]@proxy.example.com:8080","active":true},"probes":[{"id":"github_https","name":"GitHub HTTPS","target":"https://github.com","protocol":"https","via_proxy":true,"reachable":true,"status_code":200,"latency_ms":42}]}`
	tn := newTermNode(t)
	tn.answers = map[string]any{proto.MethodMachineNetwork: json.RawMessage(sampleReport)}
	h := tn.hub

	for _, role := range []string{roleAnswer, roleExtension, roleLink} {
		w := callMachineNetworkRoute(h, role, "/api/v1/machines/m1/network", "")
		if w.Code != http.StatusForbidden {
			t.Errorf("role %s: status = %d, want 403", role, w.Code)
		}
	}
	if got := tn.await(t, 0); len(got) != 0 {
		t.Fatalf("refused request reached node: %q", got)
	}

	// Query parameters and request body must never be forwarded to the node (no SSRF targets).
	w := callMachineNetworkRoute(h, roleFull, "/api/v1/machines/m1/network?target=https://custom.example.com", `{"target":"http://evil.example"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("full device status = %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != sampleReport {
		t.Errorf("body = %s, want %s", w.Body.String(), sampleReport)
	}
	got := tn.await(t, 1)
	if len(got) != 1 || got[0] != proto.MethodMachineNetwork {
		t.Fatalf("node log = %q, want [%s]", got, proto.MethodMachineNetwork)
	}
	tn.mu.Lock()
	args := tn.args[proto.MethodMachineNetwork]
	tn.mu.Unlock()
	if args != `{}` {
		t.Errorf("node params = %s, want {}", args)
	}
}

func TestMachineNetworkRouteIsFullCredentialsOnly(t *testing.T) {
	data, err := os.ReadFile("testdata/route_scopes.txt")
	if err != nil {
		t.Fatal(err)
	}
	const urlPath = "/api/v1/machines/m1/network"
	for _, p := range []principal{{ID: "dev_watch", Role: roleAnswer}, {ID: "dev_link", Role: roleLink}} {
		if allowed(p, http.MethodGet, urlPath) {
			t.Errorf("%s may GET %s", p.Role, urlPath)
		}
	}
	const line = "-                    GET /api/v1/machines/{mid}/network\n"
	if !strings.Contains(string(data), line) {
		t.Errorf("route_scopes.txt is missing full-credentials-only entry %q", strings.TrimSpace(line))
	}
}
