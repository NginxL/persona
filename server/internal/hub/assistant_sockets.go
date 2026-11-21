package hub

import (
	"errors"
	"net/http"
	"slices"
	"sync"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// assistantSocketRoutesRelayed relays the assistant's WebSocket routes (assistantwire.SocketRoutes: a meeting's audio
// socket, docs/assistant/meeting-minutes.md "Audio from the phone") to its manager as the extensions' sockets are
// (relayToManager): over a relay stream, never as http.request, which cannot upgrade. Who asked goes in the hub's
// headers, the credential (Bearer or ?token=) stays here, revoking the device ends the socket (trackRelayed), and
// while the manager is away the upgrade is refused 503 assistant_offline. Full credentials only: the auth already
// refuses an answer-only device and an extension's token (no extwire.RouteScopes entry opens these), and so does this,
// before anything goes to the manager.
func (h *Hub) assistantSocketRoutesRelayed(api *http.ServeMux) {
	for _, pattern := range assistantwire.SocketRoutes {
		api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			p := principalOf(r.Context())
			a2a := pattern == assistantwire.A2ARPC || pattern == assistantwire.A2ACard
			if p.Role != roleFull && !(a2a && p.Role == roleScoped && slices.Contains(p.Scopes, assistantwire.ScopeA2A)) {
				writeErr(w, http.StatusForbidden, errors.New(http.StatusText(http.StatusForbidden)))
				return
			}
			h.relayToManager(w, r, false)
		})
	}
}

// assistantSockets matches assistantwire.SocketRoutes, as the API mux does.
var assistantSockets = sync.OnceValue(func() *http.ServeMux {
	mux := http.NewServeMux()
	for _, pattern := range assistantwire.SocketRoutes {
		mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	return mux
})

// isAssistantSocket says whether r is one of assistantwire.SocketRoutes: open for as long as a meeting records
// (longLived).
func isAssistantSocket(r *http.Request) bool {
	_, pattern := assistantSockets().Handler(r)
	return pattern != ""
}
