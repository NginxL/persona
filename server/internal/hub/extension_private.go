package hub

import (
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// What an extension's token never sees of the conversations, whatever scopes it was granted (docs/extensions/extensions.md
// "Never shown to extensions"): the health space's conversations are not listed (the legacy list, the paged
// list, its totals and counts) and their items, images, info and screen are not read. A conversation the hub does not
// know is not read either: its space cannot be told.

// healthSession: s runs in a health space, by its own Space or its directory (identity.InHealthSpace counts a folder
// inside one, which only hides more).
func healthSession(s *proto.Session) bool {
	return s.Space == identity.SpaceHealth || identity.InHealthSpace(s.Cwd)
}

// extensionToken: r carries an extension's token.
func extensionToken(r *http.Request) bool { return principalOf(r.Context()).Role == roleExtension }

func restrictedAgentData(r *http.Request) bool {
	role := principalOf(r.Context()).Role
	return role == roleExtension || role == roleScoped
}

// withoutHealthSessions drops the health space's conversations from list for an extension's token (list itself
// otherwise, and when none is dropped). list is never changed.
func withoutHealthSessions(r *http.Request, list []proto.Session) []proto.Session {
	if !restrictedAgentData(r) {
		return list
	}
	for i := range list {
		if !healthSession(&list[i]) {
			continue
		}
		out := make([]proto.Session, i, len(list)-1)
		copy(out, list[:i])
		for j := i + 1; j < len(list); j++ {
			if !healthSession(&list[j]) {
				out = append(out, list[j])
			}
		}
		return out
	}
	return list
}

// hiddenFromExtensions: an extension's token is not shown card ix: a private one (a Just me turn's), one of the health
// space (its directory or its conversation's), or one whose space cannot be told (no directory, a conversation the hub
// does not list).
func (h *Hub) hiddenFromExtensions(ix proto.Interaction) bool {
	if ix.Private || identity.InHealthSpace(ix.Cwd) {
		return true
	}
	if m := h.localMachine(ix.MachineID); m != nil {
		if s, ok := m.session(ix.SessionID); ok {
			return healthSession(&s)
		}
	}
	return ix.Cwd == ""
}

// extensionReadable wraps a route of one conversation ({mid}/{sid}): an extension's token is answered 404, as for a
// conversation there is not, when it is a health-space conversation or one the hub does not list (unless it was given
// it as a history row lately, sessions_history.go). Anyone else, and an
// unknown machine (the route's own 404), go on to next.
func (h *Hub) extensionReadable(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if restrictedAgentData(r) {
			if m := h.localMachine(r.PathValue("mid")); m != nil {
				s, ok := m.session(r.PathValue("sid"))
				if !ok && h.historyReads.readable(m.cfg.ID, r.PathValue("sid"), time.Now()) {
					next(w, r) // a history row it was given (sessions_history.go), never a health-space one
					return
				}
				if !ok || healthSession(&s) {
					writeErr(w, http.StatusNotFound, errDeleteUnknown) // "No such session"
					return
				}
			}
		}
		next(w, r)
	}
}
