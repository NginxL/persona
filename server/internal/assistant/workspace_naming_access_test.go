package assistant

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestWorkspaceNamingAccessChecksHaveNoEffects(t *testing.T) {
	s, store, calls := namingHTTPFixture(t)
	todo, err := s.createPlainTodo(t.Context(), "Example task", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/todos/" + todo.ID
	body := `{"machine_id":"box","layout":"container","selected_repo_ids":[],"operation_id":"access-operation"}`
	for _, route := range []struct{ method, path, body string }{
		{"PUT", path + "/workspace-rules", `{"expected_revision":0,"workspace_rules":{"branch_template":"custom/{task-id}"}}`},
		{"POST", "/api/v1/assistant/workspace-naming/preview", body},
		{"POST", path + "/workspace-naming/preview", body},
		{"POST", path + "/workspace-naming/plans", body},
	} {
		if w := placeDo(s, "read", route.method, route.path, route.body); w.Code != 403 {
			t.Errorf("read-only %s: %d %s", route.path, w.Code, w.Body)
		}
		if route.method == "POST" {
			// The manager's device list contains only box. Even a writer cannot name another machine.
			foreign := strings.ReplaceAll(route.body, `"box"`, `"another-managers-machine"`)
			if w := placeDo(s, roleExtension, route.method, route.path, foreign); w.Code != 404 {
				t.Errorf("foreign machine %s: %d %s", route.path, w.Code, w.Body)
			}
		}
	}
	record, err := store.TodoWorkspaceRules(t.Context(), todo.ID)
	if err != nil || record.Revision != 0 || record.Rules != (workspacenaming.Rules{}) || calls.Load() != 0 {
		t.Fatalf("refused requests had effects: %+v, calls %d, error %v", record, calls.Load(), err)
	}
	if plan, err := store.WorkspaceNamingPlan(t.Context(), todo.ID, "access-operation"); err != nil || plan != nil {
		t.Fatal("refused request saved a plan", plan, err)
	}
	if w := placeDo(s, roleExtension, "POST", path+"/workspace-naming/plans", body); w.Code != 200 || calls.Load() != 1 {
		t.Fatal("own machine with write access", w.Code, w.Body, calls.Load())
	}
}
