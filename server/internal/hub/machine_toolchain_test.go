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

func callToolchainRoute(h *Hub, role, rawQuery string) *httptest.ResponseRecorder {
	target := "/api/v1/machines/m1/toolchain"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.SetPathValue("mid", "m1")
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: role, ID: "dev"}))
	w := httptest.NewRecorder()
	h.machineToolchain(w, r)
	return w
}

func TestMachineToolchainRouteNeedsAFullDevice(t *testing.T) {
	tn := newTermNode(t)
	h := tn.hub
	for _, role := range []string{roleAnswer, roleExtension, roleLink} {
		w := callToolchainRoute(h, role, "")
		if w.Code != http.StatusForbidden {
			t.Errorf("GET toolchain: %s was let through: %d", role, w.Code)
		}
	}
	if got := tn.await(t, 0); len(got) != 0 {
		t.Fatalf("the node heard of a refused request: %q", got)
	}

	const sample = `{"os":"linux","arch":"amd64","cached":false,"cache_ttl_sec":60,"tools":[],"agent_clis":[],"environment":{}}`
	tn.answers = map[string]any{proto.MethodMachineToolchain: json.RawMessage(sample)}

	w := callToolchainRoute(h, roleFull, "")
	if w.Code != http.StatusOK || w.Body.String() != sample {
		t.Fatalf("GET toolchain = %d %s", w.Code, w.Body.String())
	}
	got := tn.await(t, 1)
	if len(got) != 1 || got[0] != proto.MethodMachineToolchain {
		t.Fatalf("node log = %q", got)
	}
	tn.mu.Lock()
	args := tn.args[proto.MethodMachineToolchain]
	tn.mu.Unlock()
	if args != `{}` {
		t.Errorf("node params without refresh = %s, want {}", args)
	}

	// refresh=1 forwards refresh:true to the node.
	w = callToolchainRoute(h, roleFull, "refresh=1")
	if w.Code != http.StatusOK {
		t.Fatalf("GET toolchain?refresh=1 = %d %s", w.Code, w.Body.String())
	}
	tn.await(t, 2)
	tn.mu.Lock()
	args = tn.args[proto.MethodMachineToolchain]
	tn.mu.Unlock()
	if args != `{"refresh":true}` {
		t.Errorf("node params with refresh=1 = %s, want {\"refresh\":true}", args)
	}
}

func TestMachineToolchainRouteIsFullCredentialsOnly(t *testing.T) {
	data, err := os.ReadFile("testdata/route_scopes.txt")
	if err != nil {
		t.Fatal(err)
	}
	url := "/api/v1/machines/m1/toolchain"
	for _, p := range []principal{{ID: "dev_watch", Role: roleAnswer}, {ID: "dev_link", Role: roleLink}} {
		if allowed(p, http.MethodGet, url) {
			t.Errorf("%s may GET %s", p.Role, url)
		}
	}
	line := "-                    GET /api/v1/machines/{mid}/toolchain\n"
	if !strings.Contains(string(data), line) {
		t.Errorf("route_scopes.txt has no full-credentials-only line for GET /api/v1/machines/{mid}/toolchain")
	}
}
