package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"

	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise the same list, discovery and original call handlers that a native MCP client uses.
func TestWorkGuideCatalogDiscoveryAndRollback(t *testing.T) {
	t.Setenv("PERSONA_ASSISTANT", "1")
	t.Setenv("PERSONA_SESSION_ID", "assistant-test")
	t.Chdir(t.TempDir())
	tools := []proto.Tool{
		{Name: "todo_get", Description: "get item", InputSchema: json.RawMessage(`{"type":"object","required":["id"]}`)},
		{Name: "todo_list", Description: "list items", DeferLoading: true},
		{Name: "other", Description: "unrelated"},
	}
	var call map[string]any
	saved := dialNode
	t.Cleanup(func() { dialNode = saved })
	dialNode = func(_ context.Context, req any) ([]byte, error) {
		r := req.(map[string]any)
		if r["kind"] == "mcp.tools" {
			return json.Marshal(map[string]any{"tools": tools})
		}
		call = r
		return json.Marshal(proto.TextResult("original handler declined", true))
	}

	notifications := 0
	conn := func() context.Context {
		return context.WithValue(t.Context(), workCatalogKey{}, &workCatalog{notify: func() { notifications++ }})
	}
	ctx, other := conn(), conn()
	list := func(ctx context.Context) []map[string]any {
		r, err := handle(ctx, "test", listRequest())
		if err != nil {
			t.Fatal(err)
		}
		return r.(map[string]any)["tools"].([]map[string]any)
	}
	invoke := func(name, args string) string {
		r, err := handle(ctx, "test", request{Method: "tools/call", Params: json.RawMessage(`{"name":"` + name + `","arguments":` + args + `}`)})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r)
		return string(b)
	}
	baseline := list(ctx)
	tools[0].WorkGuideTool, tools[1].WorkGuideTool = true, true
	if got := list(ctx); !reflect.DeepEqual(got, baseline) {
		t.Fatal("missing managed guide must retain the full catalog", got)
	}
	for _, dir := range []string{".claude", ".agents"} {
		if err := assistantskills.Sync(filepath.Join(dir, "skills"), t.TempDir(), assistantskills.Selection{WorkGuide: true}, t.Logf); err != nil {
			t.Fatal(err)
		}
	}
	initial := list(ctx)
	if len(initial) != 2 || !reflect.DeepEqual(initial[0], baseline[2]) || initial[1]["name"] != workSearchName {
		t.Fatalf("initial catalog %v", initial)
	}
	if out := invoke(workSearchName, `{"query":"todo_"}`); !strings.Contains(out, "todo_get, todo_list") || notifications != 0 || call != nil {
		t.Fatalf("fragment %s, notifications %d, call %v", out, notifications, call)
	}
	if out := invoke(workSearchName, `{"query":"todo_get"}`); !strings.Contains(out, "Loaded: todo_get") || notifications != 1 {
		t.Fatalf("discovery %s, notifications %d", out, notifications)
	}
	selected := list(ctx)
	if len(selected) != 3 || !reflect.DeepEqual(selected[0], baseline[0]) || !reflect.DeepEqual(list(other), initial) {
		t.Fatalf("selected %v", selected)
	}
	out := invoke("todo_get", `{"id":"synthetic"}`)
	if !strings.Contains(out, "original handler declined") || !strings.Contains(out, `"isError":true`) {
		t.Fatalf("original decision lost: %s", out)
	}
	if call["kind"] != "mcp.call" || call["name"] != "todo_get" || call["session_id"] != "assistant-test" ||
		string(call["args"].(json.RawMessage)) != `{"id":"synthetic"}` {
		t.Fatalf("original call changed: %v", call)
	}
	tools[0].WorkGuideTool, tools[1].WorkGuideTool = false, false
	if got := list(ctx); !reflect.DeepEqual(got, baseline) {
		t.Fatal("rollback changed original schema or loading metadata", got)
	}
	tools[0].WorkGuideTool, tools[1].WorkGuideTool = true, true
	if got := list(ctx); !reflect.DeepEqual(got, initial) {
		t.Fatal("re-enabled with stale selections", got)
	}
	tools = append(tools, proto.Tool{Name: workSearchName, Description: "extension"})
	if got := list(ctx); len(got) != len(tools) || got[len(got)-1]["description"] != "extension" {
		t.Fatal("extension name collision", got)
	}
	invoke(workSearchName, `{}`)
	if call["name"] != workSearchName {
		t.Fatal("extension tool was intercepted", call)
	}
}
