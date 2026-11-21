package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func callServicesRoute(h *Hub, role, method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "/api/v1/machines/m1/"+path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, "/api/v1/machines/m1/"+path, nil)
	}
	r.SetPathValue("mid", "m1")
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: role, ID: "dev"}))
	w := httptest.NewRecorder()
	switch {
	case method == http.MethodGet && strings.HasPrefix(path, "services/logs"):
		h.machineServicesLogs(w, r)
	case method == http.MethodGet && path == "services":
		h.machineServicesList(w, r)
	case method == http.MethodPost && path == "services/action":
		h.machineServicesAction(w, r)
	default:
		panic("unexpected route: " + method + " " + path)
	}
	return w
}

func TestMachineServicesRoutesNeedFullDevice(t *testing.T) {
	tn := newTermNode(t)
	h := tn.hub

	for _, role := range []string{roleAnswer, roleExtension, roleLink} {
		for _, tc := range []struct {
			method, path, body string
		}{
			{http.MethodGet, "services", ""},
			{http.MethodGet, "services/logs?kind=systemd&id=redis.service", ""},
			{http.MethodPost, "services/action", `{"kind":"systemd","id":"redis.service","action":"restart","confirmed":true}`},
		} {
			w := callServicesRoute(h, role, tc.method, tc.path, tc.body)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s with role %s: status = %d, want 403", tc.method, tc.path, role, w.Code)
			}
		}
	}
	if got := tn.await(t, 0); len(got) != 0 {
		t.Fatalf("node received refused requests: %q", got)
	}
}

func TestMachineServicesRoutesValidationAndRelay(t *testing.T) {
	tn := newTermNode(t)
	h := tn.hub

	// Invalid logs params rejected before contacting node.
	for _, q := range []string{
		"services/logs?kind=systemd&id=-u",
		"services/logs?kind=systemd&id=redis.service;id",
		"services/logs?kind=bad&id=redis.service",
		"services/logs?kind=systemd&id=redis.service&lines=-1",
		"services/logs?kind=systemd&id=redis.service&lines=abc",
	} {
		if w := callServicesRoute(h, roleFull, http.MethodGet, q, ""); w.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status = %d, want 400", q, w.Code)
		}
	}

	// Invalid or unconfirmed actions rejected before contacting node.
	for _, body := range []string{
		`{"kind":"systemd","id":"redis.service","action":"restart","confirmed":false}`,
		`{"kind":"systemd","id":"redis.service","action":"kill","confirmed":true}`,
		`{"kind":"systemd","id":"../bad","action":"restart","confirmed":true}`,
		`not-json`,
	} {
		if w := callServicesRoute(h, roleFull, http.MethodPost, "services/action", body); w.Code != http.StatusBadRequest {
			t.Errorf("POST services/action %s: status = %d, want 400", body, w.Code)
		}
	}
	if got := tn.await(t, 0); len(got) != 0 {
		t.Fatalf("node received invalid requests: %q", got)
	}

	// Valid requests relay cleanly and clamp lines to ServiceLogsMaxLines.
	tn.answers = map[string]any{
		proto.MethodMachineServices:      proto.MachineServicesReport{OS: "linux"},
		proto.MethodMachineServiceLogs:   proto.MachineServiceLogsResult{Kind: "systemd", ID: "redis.service", Lines: 500, Content: "hello"},
		proto.MethodMachineServiceAction: proto.MachineServiceActionResult{Kind: "systemd", ID: "redis.service", Action: "restart", OK: true},
	}

	if w := callServicesRoute(h, roleFull, http.MethodGet, "services", ""); w.Code != http.StatusOK {
		t.Fatalf("GET services = %d: %s", w.Code, w.Body.String())
	}
	if w := callServicesRoute(h, roleFull, http.MethodGet, "services/logs?kind=systemd&id=redis.service&lines=9999", ""); w.Code != http.StatusOK {
		t.Fatalf("GET services/logs = %d: %s", w.Code, w.Body.String())
	}
	if w := callServicesRoute(h, roleFull, http.MethodPost, "services/action", `{"kind":"systemd","id":"redis.service","action":"restart","confirmed":true}`); w.Code != http.StatusOK {
		t.Fatalf("POST services/action = %d: %s", w.Code, w.Body.String())
	}

	got := tn.await(t, 3)
	if len(got) != 3 || got[0] != proto.MethodMachineServices || got[1] != proto.MethodMachineServiceLogs || got[2] != proto.MethodMachineServiceAction {
		t.Fatalf("node log = %q", got)
	}
	tn.mu.Lock()
	logArgs := tn.args[proto.MethodMachineServiceLogs]
	tn.mu.Unlock()
	var parsedLogs proto.MachineServiceLogsParams
	if err := json.Unmarshal([]byte(logArgs), &parsedLogs); err != nil || parsedLogs.Lines != proto.ServiceLogsMaxLines {
		t.Errorf("forwarded logs params = %s, want lines=%d", logArgs, proto.ServiceLogsMaxLines)
	}
}

func TestMachineServicesProtectedErrorMapping(t *testing.T) {
	w := httptest.NewRecorder()
	if !handleServiceRPCErr(w)(&rpc.Error{Code: proto.CodeServiceProtected, Message: "protected"}) {
		t.Fatal("expected handleServiceRPCErr to handle service_protected")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestMachineServicesRoutesAreFullCredentialsOnly(t *testing.T) {
	data, err := os.ReadFile("testdata/route_scopes.txt")
	if err != nil {
		t.Fatal(err)
	}
	routes := [][2]string{
		{http.MethodGet, "/api/v1/machines/{mid}/services"},
		{http.MethodGet, "/api/v1/machines/{mid}/services/logs"},
		{http.MethodPost, "/api/v1/machines/{mid}/services/action"},
	}
	for _, rt := range routes {
		if scope, ok := extwire.RouteScopes[rt[0]+" "+rt[1]]; ok {
			t.Errorf("%s %s must not be open to extensions (got scope %q)", rt[0], rt[1], scope)
		}
		concrete := strings.ReplaceAll(rt[1], "{mid}", "m1")
		for _, p := range []principal{{ID: "watch", Role: roleAnswer}, {ID: "link", Role: roleLink}} {
			if allowed(p, rt[0], concrete) {
				t.Errorf("%s may %s %s", p.Role, rt[0], concrete)
			}
		}
		line := "-                    " + rt[0] + " " + rt[1] + "\n"
		if !strings.Contains(string(data), line) {
			t.Errorf("route_scopes.txt missing full-credentials-only line for %s %s", rt[0], rt[1])
		}
	}
}
