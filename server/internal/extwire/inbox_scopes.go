package extwire

// The Inbox's routes an extension's token may call, and the scope each needs, inbox.read or inbox.write (the scope table
// is in docs/extensions/extensions.md "Permissions"). They are the Inbox's own decisions, so they are made in the
// Inbox's file rather than in scopes.go: a new Inbox route opened to extensions gets its line here. The translate routes
// stay out, for full credentials only.
func init() {
	addRouteScopes(map[string]string{
		"GET /api/v1/inbox":                         ScopeInboxRead,
		"GET /api/v1/inbox/done":                    ScopeInboxRead,
		"GET /api/v1/inbox/done/days":               ScopeInboxRead,
		"GET /api/v1/inbox/{id}":                    ScopeInboxRead,
		"GET /api/v1/inbox/mutes":                   ScopeInboxRead,
		"GET /api/v1/inbox/policies":                ScopeInboxRead,
		"POST /api/v1/inbox/done":                   ScopeInboxWrite,
		"POST /api/v1/inbox/bulk":                   ScopeInboxWrite,
		"POST /api/v1/inbox/merge":                  ScopeInboxWrite,
		"POST /api/v1/inbox/merge/undo":             ScopeInboxWrite,
		"POST /api/v1/inbox/todo":                   ScopeInboxWrite,
		"POST /api/v1/inbox/todo/undo":              ScopeInboxWrite,
		"POST /api/v1/inbox/{id}/done":              ScopeInboxWrite,
		"POST /api/v1/inbox/{id}/draft":             ScopeInboxWrite,
		"POST /api/v1/inbox/{id}/reply":             ScopeInboxWrite,
		"POST /api/v1/inbox/{id}/retry":             ScopeInboxWrite,
		"POST /api/v1/inbox/{id}/suggestion":        ScopeInboxWrite,
		"POST /api/v1/inbox/{id}/handoff":           ScopeInboxWrite,
		"POST /api/v1/inbox/{id}/task-link/recheck": ScopeInboxWrite,
		"POST /api/v1/inbox/{id}/doc/status":        ScopeInboxWrite,
		"PUT /api/v1/inbox/{id}/parent":             ScopeInboxWrite,
		"PUT /api/v1/inbox/{id}/folder":             ScopeInboxWrite,
		"GET /api/v1/inbox/folders":                 ScopeInboxRead,
		"PUT /api/v1/inbox/folders":                 ScopeInboxWrite,
		"GET /api/v1/inbox/tree/{kind}/{id...}":     ScopeInboxRead,
		"POST /api/v1/inbox/mutes":                  ScopeInboxWrite,
		"DELETE /api/v1/inbox/mutes/{id}":           ScopeInboxWrite,
		"POST /api/v1/inbox/policies":               ScopeInboxWrite,
		"PATCH /api/v1/inbox/policies/{id}":         ScopeInboxWrite,
		"DELETE /api/v1/inbox/policies/{id}":        ScopeInboxWrite,
		"POST /api/v1/inbox/refresh":                ScopeInboxWrite,
	})
}
