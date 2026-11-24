package hub

import (
	"net/http"
	"path"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant ("Persona" tab) is a process of its own (internal/assistant): its conversations are headless ones a
// node runs in a dedicated directory on one machine; the hub tells them apart from the agents by that directory, and
// relays the assistant's routes to it (assistant_link.go).

// assistantCwdOf is manager id's assistant directory on m (identity.AssistantDirName: the built-in's "assistant",
// another's "assistant-<id>"), or "" while m has not reported its home.
func (m *Machine) assistantCwdOf(id string) string {
	name := identity.AssistantDirName(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.info.Home == "" {
		return ""
	}
	if m.info.DataDir != "" {
		return path.Join(m.info.DataDir, name)
	}
	return path.Join(m.info.Home, ".persona", name)
}

// isAssistant: s is one of the headless conversations of m's manager's assistant (its own directory or its health
// space's). Another manager's, left on m when m moved away from it, is not: the new manager does not read it
// (docs/assistant/managers.md "Directories on a computer").
func (m *Machine) isAssistant(s proto.Session) bool {
	owner := m.hub.machineOwner(m.cfg.ID)
	return isAssistantOf(s, m.assistantCwdOf(owner), owner)
}

// isMeetingRoomCwd checks whether cwd is a meeting room conversation's working directory (<state dir>/guest-meeting/<id>).
func isMeetingRoomCwd(cwd string) bool {
	if cwd == "" {
		return false
	}
	clean := path.Clean(path.Clean(cwd))
	parent := path.Base(path.Dir(clean))
	if parent != "guest-meeting" {
		return false
	}
	name := path.Base(clean)
	return name != "" && name != "." && name != "/"
}

// isAssistantOf identifies manager owner's headless conversations in its directory dir, its health space's,
// or a meeting room guest conversation (guest-meeting/<id>, #6048). A host pane is stronger evidence than
// a shared working directory.
func isAssistantOf(s proto.Session, dir, owner string) bool {
	if s.PaneID != "" || s.Cwd == "" {
		return false
	}
	if isMeetingRoomCwd(s.Cwd) || s.Origin == "meeting" {
		return true
	}
	if dir == "" {
		return false
	}
	// The working directory may go through a symlinked home; runtimes and offline assistants have no host pane.
	return s.Cwd == dir || identity.IsAssistantDirOf(s.Cwd, owner)
}

// assistantRoutes are the skills the assistant's machine has and the assistant's own routes, relayed to it.
func (h *Hub) assistantRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/skills", h.serveSkills)
	h.assistantRoutesRelayed(api)
	h.assistantSocketRoutesRelayed(api)
}

// assistantSpace is the space of an assistant conversation working in cwd: "health" for the health space's
// (docs/assistant/health.md), another space's slug for its directory, "" for the assistant's own.
func assistantSpace(cwd string) string { return identity.SpaceOf(cwd) }

// inSpace: an assistant conversation working in cwd is in a space (the hub cannot see the manager's registry, so any
// space counts, the health space included). What it says leaves the space only with
// the user knowing (docs/assistant/health.md "Outbound"): its pushes say there is news, not the news, and its writes are confirmed
// on the phone each time, whatever the gate rules allow. A directory inside the space counts too (a card's cwd can be
// where a `cd` left the model's shell, #2368): this only hides, it never classifies (assistantSpace stays exact).
func inSpace(cwd string) bool { return cwd != "" && identity.InSpace(cwd) }
