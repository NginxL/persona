package extwire

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
)

// An extension's token (docs/extensions/extensions.md "Permissions") opens the routes of the scopes the user granted the extension and
// no other; the manager made the token and checks it (extensions.authorize), with this table of the hub's routes.

// RouteScopes are the routes an extension's token may call, by the pattern they are registered with, and the scope
// each needs. A route not here is for full credentials only. Deciding that a new route is open to extensions means
// giving it its scope from its domain's own file, extwire/<domain>_scopes.go, through addRouteScopes, as
// inbox_scopes.go does: the literal below keeps the entries it has and takes no new key (TestCentralListsTakeNoNewName
// in internal/archguard; docs/server/architecture.md "Registering a domain's entries"). TestRouteScopes keeps the list
// of every route and what was decided for it, server/internal/hub/testdata/route_scopes.txt.
var RouteScopes = map[string]string{
	"GET /api/v1/kb/{kind}":                                      ScopeKBRead,
	"GET /api/v1/kb/{kind}/{id}":                                 ScopeKBRead,
	"GET /api/v1/kb/chats/{id}/context":                          ScopeKBRead,
	"GET /api/v1/kb/chats/{id}/days/{day}":                       ScopeKBRead,
	"GET /api/v1/kb/moments/{id}":                                ScopeKBRead,
	"GET /api/v1/kb-search":                                      ScopeKBRead,
	"PUT /api/v1/kb/{kind}/{id}":                                 ScopeKBWrite,
	"GET /api/v1/assistant/leaders":                              ScopeLeadersRead,
	"POST /api/v1/assistant/directions":                          ScopeLeadersWrite,
	"POST /api/v1/assistant/directions/{id}/progress":            ScopeLeadersWrite,
	"GET /api/v1/assistant/dispatch-policy":                      ScopeAssistantDispatchPolicy,
	"PUT /api/v1/assistant/dispatch-policy":                      ScopeAssistantDispatchPolicy,
	"GET /api/v1/assistant/dispatch-guard":                       ScopeDispatchGuard,
	"PUT /api/v1/assistant/dispatch-guard":                       ScopeDispatchGuard,
	"GET /api/v1/assistant/worker-presets":                       ScopeAssistantStartDefaults,
	"GET /api/v1/assistant/start-defaults":                       ScopeAssistantStartDefaults,
	"PUT /api/v1/assistant/start-defaults":                       ScopeAssistantStartDefaults,
	"POST /api/v1/assistant/start-defaults/preview":              ScopeAssistantStartDefaults,
	"GET /api/v1/sessions/archived":                              ScopeAgentsRead,
	"GET /api/v1/sessions":                                       ScopeAgentsRead,
	"GET /api/v1/sessions/{mid}/{sid}/info":                      ScopeAgentsRead,
	"GET /api/v1/sessions/{mid}/{sid}/items":                     ScopeAgentsRead,
	"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/full":          ScopeAgentsRead,
	"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/image":         ScopeAgentsRead,
	"GET /api/v1/sessions/{mid}/{sid}/items/{iid}/visualization": ScopeAgentsRead,
	"GET /api/v1/sessions/{mid}/{sid}/screen":                    ScopeAgentsRead,
	"GET /api/v1/interactions":                                   ScopeAgentsRead,
	"GET /api/v1/interactions/history":                           ScopeAgentsRead,
	"GET /api/v1/agents":                                         ScopeAgentsRead,
	"GET /api/v1/agents/sessions":                                ScopeAgentsRead,
	"GET /api/v1/agents/sessions/{id}":                           ScopeAgentsRead,
	"GET /api/v1/agents/sessions/{id}/messages":                  ScopeAgentsRead,
	"GET /api/v1/agents/sessions/{id}/avatar":                    ScopeAgentsRead,
	"GET /api/v1/workspaces":                                     ScopeAgentsRead,
	"GET /api/v1/saved-agent-searches":                           ScopeAgentsRead,
	"GET /api/v1/workspace-board":                                ScopeAgentsRead,
	"GET /api/v1/workspace-board/activity":                       ScopeAgentsRead,
	"GET /api/v1/workspace-board/activity/{revision}":            ScopeAgentsRead,
	"POST /api/v1/sessions":                                      ScopeAgentsControl,
	"POST /api/v1/sessions/{mid}/{sid}/messages":                 ScopeAgentsControl,
	"POST /api/v1/sessions/{mid}/{sid}/interrupt":                ScopeAgentsControl,
	"POST /api/v1/sessions/{mid}/{sid}/queue":                    ScopeAgentsControl,
	"POST /api/v1/sessions/{mid}/{sid}/queue/send-now":           ScopeAgentsControl, // #6088
	"POST /api/v1/sessions/{mid}/{sid}/seen":                     ScopeAgentsControl,
	"POST /api/v1/sessions/{mid}/{sid}/rename":                   ScopeAgentsControl,
	"POST /api/v1/sessions/{mid}/{sid}/modes":                    ScopeAgentsPermissionMode,
	"POST /api/v1/interactions/{id}/choose":                      ScopeAgentsControl,
	"POST /api/v1/interactions/{id}/resolve":                     ScopeAgentsControl,
	"POST /api/v1/interactions/{id}/reanswer":                    ScopeAgentsControl, // "Change answer": a message to the agent
	"POST /api/v1/agents/sessions/{id}/messages":                 ScopeAgentsControl,
	"POST /api/v1/agents/sessions/{id}/read":                     ScopeAgentsControl,
	"GET /api/v1/machines":                                       ScopeMachinesRead,
	"GET /api/v1/machines/{id}/metrics":                          ScopeMachinesRead,
	"GET /api/v1/machines/{id}/usage":                            ScopeMachinesRead,
	"GET /api/v1/machines/{mid}/stat":                            ScopeMachinesRead,
	"POST /api/v1/notify":                                        ScopeNotify,
	"GET /api/v1/push/intercepts":                                ScopeNotify, // read-only: pushes ln mode held back, including an extension's own
	"GET /api/v1/push/ln-mode":                                   ScopeNotify, // read-only: the switch state; the PUT stays full credentials only
	"GET /api/v1/push/grading":                                   ScopeNotify, // read-only alias of the switch state; the PUT stays full credentials only
	"GET /api/v1/push/stats/daily":                               ScopeNotify, // read-only: delivered/held counts per day, an extension sees its own manager only
	"GET /api/v1/assistant":                                      ScopeAssistantAsk,
	"GET /api/v1/assistant/capabilities":                         ScopeAssistantAsk,
	"POST /api/v1/assistant/conversations":                       ScopeAssistantAsk,
	"GET /api/v1/assistant/conversations/{id}/feed":              ScopeAssistantAsk,
	"POST /api/v1/assistant/conversations/{id}/stop":             ScopeAssistantAsk, // /agent's /stop (#3308)
	"GET /api/v1/assistant/reporters":                            ScopeAssistantAsk,
	// The todos (the assistant's, relayed): reading them, adding, editing and attaching agents and spaces, and writing
	// outcome decisions (an extension writes no other source). Deleting a todo, superseding decisions, the done check, a
	// retry and the owner's own marks (seen, restored, Someday) stay with full credentials. Backend worktree
	// inspection and changes also require full credentials, as do the machine worktree routes.
	"GET /api/v1/todos/{id}/workspace-rules":                       ScopeTodosRead,
	"PUT /api/v1/todos/{id}/workspace-rules":                       ScopeTodosWrite,
	"POST /api/v1/todos/{id}/workspace-naming/preview":             ScopeTodosWrite,
	"POST /api/v1/todos/{id}/workspace-naming/plans":               ScopeTodosWrite,
	"GET /api/v1/todos/{id}/workspace-naming/plans/{operation_id}": ScopeTodosRead,
	"POST /api/v1/assistant/workspace-naming/preview":              ScopeTodosWrite,
	"GET /api/v1/todos/{id}/repositories":                          ScopeTodosRead,
	"PUT /api/v1/todos/{id}/repositories":                          ScopeTodosWrite,
	"PUT /api/v1/todos/{id}/repositories/{repo_id}/links":          ScopeTodosWrite,
	"GET /api/v1/todos":                                            ScopeTodosRead,
	"GET /api/v1/todos/places":                                     ScopeTodosRead,
	"GET /api/v1/todos/{id}":                                       ScopeTodosRead,
	"GET /api/v1/todos/{id}/decisions":                             ScopeTodosRead,
	"GET /api/v1/todos/{id}/timeline":                              ScopeTodosRead,
	"POST /api/v1/todos":                                           ScopeTodosWrite,
	"POST /api/v1/decisions":                                       ScopeDecisionsRaise,
	"GET /api/v1/decisions/{id}":                                   ScopeDecisionsRaise,
	"POST /api/v1/decisions/{id}/withdraw":                         ScopeDecisionsRaise,
	"PATCH /api/v1/todos/{id}":                                     ScopeTodosWrite,
	"POST /api/v1/todos/{id}/decisions":                            ScopeTodosWrite,
	"PUT /api/v1/todos/{id}/agents/{mid}/{sid}":                    ScopeTodosWrite,
	"DELETE /api/v1/todos/{id}/agents/{mid}/{sid}":                 ScopeTodosWrite,
	"PUT /api/v1/todos/{id}/spaces/{mid}/{space...}":               ScopeTodosWrite,
	"DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}":            ScopeTodosWrite,
	// The only market route an extension token may call: its own install record chooses the market, never the request.
	"POST /api/v1/extensions/-/self/market/refresh": ScopeMarketRefreshOwn,
	// Another extension's own HTTP: the scope names which (ExtensionCallScope).
	"/api/v1/extensions/{id}/": ScopeCallPrefix,
}

// ExtensionCallScope is the scope a request to an extension's own HTTP needs (extensions.call:<id>); "" for the hub's
// routes under /api/v1/extensions/<id>/-/, which are no extension's to open. path is the escaped path, the one the
// manager's mux routes the request by: its segments are read as the mux reads them (each one unescaped on its own), so
// an escaped "-" is the hub's "-" and an escaped "/" stays inside the id.
func ExtensionCallScope(path string) string {
	rest, ok := strings.CutPrefix(path, "/api/v1/extensions/")
	if !ok {
		return ""
	}
	rawID, after, _ := strings.Cut(rest, "/")
	rawNext, _, _ := strings.Cut(after, "/")
	id, err := url.PathUnescape(rawID)
	if err != nil {
		return ""
	}
	next, err := url.PathUnescape(rawNext)
	if err != nil || id == "" || id == "-" || next == "-" {
		return ""
	}
	return ScopeCallPrefix + id
}

// addRouteScopes opens a domain's routes to extension tokens: it gives each its scope in RouteScopes. A domain calls it
// from an init() in its own file, extwire/<domain>_scopes.go, with a map literal of its routes (inbox_scopes.go). It
// panics on an entry mergeRouteScopes refuses, so a route decided twice stops every program and test that imports
// extwire at start-up, at the line that decided it again.
func addRouteScopes(scopes map[string]string) {
	if err := mergeRouteScopes(RouteScopes, scopes); err != nil {
		panic(err)
	}
}

// The reasons mergeRouteScopes refuses an entry.
var (
	errRouteScopeTaken   = errors.New("the route has a scope already, and a route's scope is decided in one place")
	errRouteScopeNoRoute = errors.New("the route is no pattern: a method, a space and a path, or a path alone")
	errRouteScopeNoScope = errors.New("the route names no scope")
)

// mergeRouteScopes adds scopes to table when it can take every one of them. Otherwise it returns why the first it
// cannot take (in route order) is refused and leaves table as it was. Each route must be a pattern as routes are
// registered (isRoutePattern) that table does not have yet, and must name a scope.
func mergeRouteScopes(table, scopes map[string]string) error {
	for _, route := range slices.Sorted(maps.Keys(scopes)) {
		if !isRoutePattern(route) {
			return fmt.Errorf("extwire: route scope %q: %w", route, errRouteScopeNoRoute)
		}
		if scopes[route] == "" {
			return fmt.Errorf("extwire: route %q: %w", route, errRouteScopeNoScope)
		}
		if had, ok := table[route]; ok {
			return fmt.Errorf("extwire: route %q needs %q in RouteScopes: %w", route, had, errRouteScopeTaken)
		}
	}
	maps.Copy(table, scopes)
	return nil
}

// isRoutePattern says whether route is written as RouteScopes' keys are: a method, one space and a path ("GET
// /api/v1/inbox"), or a path alone, for a prefix ("/api/v1/extensions/{id}/"). A path starts with / and holds no space.
func isRoutePattern(route string) bool {
	method, path, ok := strings.Cut(route, " ")
	if !ok {
		path = method
	} else if method == "" {
		return false
	}
	return strings.HasPrefix(path, "/") && !strings.ContainsAny(path, " \t\r\n")
}
