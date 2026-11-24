package extwire

const ScopeAssistantEntriesRead = "assistant.entries.read"

func init() {
	addRouteScopes(map[string]string{"GET /api/v1/assistant/entries": ScopeAssistantEntriesRead})
}
