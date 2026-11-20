package hub

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestBackendWorkspaceRoutesBindAuthenticatedIdentity(t *testing.T) {
	h, _ := conversationsHub(t)
	link := assistantLink{h}
	for _, body := range []string{
		`{"plan":{"source":{"manager":"foreign","machine_id":"server"}}}`,
		`{"plan":{"source":{"manager":"assistant","machine_id":"elsewhere"}}}`,
		`{"plan":null}`,
	} {
		_, err := link.deviceCallFrom(t.Context(), assistantwire.AssistantID, assistantwire.DeviceCall{Device: "server", Method: "task.workspace.prepare", Params: json.RawMessage(body)})
		var problem *rpc.Error
		if !errors.As(err, &problem) || problem.Code != "invalid_params" {
			t.Fatal("unbound request reached the Node", err)
		}
	}
	// A correctly bound request reaches the fixture Node (which intentionally
	// has no implementation of this new method), rather than failing the route.
	body, _ := json.Marshal(map[string]any{"plan": map[string]any{"source": map[string]string{"manager": assistantwire.AssistantID, "machine_id": "server"}}})
	_, err := link.deviceCallFrom(t.Context(), assistantwire.AssistantID, assistantwire.DeviceCall{Device: "server", Method: "task.workspace.prepare", Params: body})
	if err == nil || !strings.Contains(err.Error(), "unexpected task.workspace.prepare") {
		t.Fatal(err)
	}
	_, err = link.deviceCallFrom(t.Context(), assistantwire.AssistantID, assistantwire.DeviceCall{Device: "server", Method: "task.workspace.capabilities"})
	if err == nil || !strings.Contains(err.Error(), "unexpected task.workspace.capabilities") {
		t.Fatal(err)
	}
}

func TestBackendWorkspaceExtensionRoutesStayInTheirManager(t *testing.T) {
	_, url, a, _ := reachHub(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/todos/example/backend-workspace", ""},
		{"GET", "/api/v1/todos/example/backend-workspace/operations/op_example", ""},
		{"POST", "/api/v1/todos/example/backend-workspace/prepare", `{"operation_id":"op_example","machine_id":"ma"}`},
		{"POST", "/api/v1/todos/example/backend-workspace/cancel", `{"operation_id":"op_example"}`},
		{"POST", "/api/v1/todos/example/backend-workspace/cleanup", `{"operation_id":"op_example"}`},
	} {
		before := len(reached(a))
		if code, out := extDo(t, url, c.method, c.path, c.body, map[string]string{ManagerHeader: assistantwire.AssistantID}); code != 403 {
			t.Fatal(code, out)
		}
		if code, out := extDo(t, url, c.method, c.path, c.body, nil); code != 200 || !strings.Contains(out, `"who":"b"`) {
			t.Fatal(code, out)
		}
		if len(reached(a)) != before {
			t.Fatal("another Manager was reached")
		}
	}
}
