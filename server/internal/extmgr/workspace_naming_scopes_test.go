package extmgr

import (
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

func TestWorkspaceNamingTokenScopes(t *testing.T) {
	h := newTestManager(t)
	h.routes()
	for _, grants := range [][]string{nil, {extension.ScopeTodosRead}, {extension.ScopeTodosWrite}, {extension.ScopeTodosRead, extension.ScopeTodosWrite}} {
		token := h.extTokens.issue("example-naming", "Naming", grants)
		for _, route := range []struct{ pattern, scope string }{
			{"GET /api/v1/todos/{id}/workspace-rules", extension.ScopeTodosRead},
			{"GET /api/v1/todos/{id}/workspace-naming/plans/{operation_id}", extension.ScopeTodosRead},
			{"PUT /api/v1/todos/{id}/workspace-rules", extension.ScopeTodosWrite},
			{"POST /api/v1/todos/{id}/workspace-naming/preview", extension.ScopeTodosWrite},
			{"POST /api/v1/todos/{id}/workspace-naming/plans", extension.ScopeTodosWrite},
			{"POST /api/v1/assistant/workspace-naming/preview", extension.ScopeTodosWrite},
		} {
			method, path, _ := strings.Cut(route.pattern, " ")
			path = strings.NewReplacer("{id}", "todo-example", "{operation_id}", "example-operation").Replace(path)
			a := h.authorize(extwire.Authorize{Hash: hashToken(token), Pattern: route.pattern, Method: method, Path: path})
			if a.OK != slices.Contains(grants, route.scope) || a.Extension != "example-naming" || a.Scope != route.scope {
				t.Errorf("%v on %s: %+v, want scope %s", grants, route.pattern, a, route.scope)
			}
		}
	}
}
