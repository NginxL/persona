package node

import (
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// An agy in a terminal holds an exclusive flock on <agy dir>/presence/<conversation>.lock for as long as it has that
// conversation open; the companion's `agy acp` takes none. The file stays behind when agy exits, so only the lock says
// a terminal has the conversation. A message the companion took for such a conversation ran a second agy on it: the
// terminal never saw it, and the companion's turn overwrote the stored steps from where it had last loaded them (#13603).

// agyPresenceSource is where a herdr-core terminal's agy conversation comes from: the presence lock its agy holds open.
const agyPresenceSource = "herdr-core:agy-presence"

// agyPresenceOf is the conversation whose presence lock the process pid holds open, else "" (none, several, or a
// system the node cannot look into). A variable so tests can stand in for /proc.
var agyPresenceOf = agyPresenceLocks

// agyPresenceConversation picks the conversation from a process's open files: exactly one presence/<uuid>.lock.
func agyPresenceConversation(files []string) string {
	found := ""
	for _, f := range files {
		dir, name := filepath.Split(f)
		id, ok := strings.CutSuffix(name, ".lock")
		if !ok || filepath.Base(filepath.Clean(dir)) != "presence" || !uuidRe.MatchString(id) {
			continue
		}
		if found != "" && found != id {
			return ""
		}
		found = id
	}
	return found
}

// agyPresenceSession is what a herdr-core terminal running agy reports about its conversation, nil when unknown.
func agyPresenceSession(pid int) *host.AgentSession {
	if pid <= 0 {
		return nil
	}
	if id := agyPresenceOf(pid); id != "" {
		return &host.AgentSession{Source: agyPresenceSource, Agent: agyAgent, Kind: "id", Value: id}
	}
	return nil
}

// agyDrives: the calls that make the companion run agy on a conversation (or change it), as opposed to reading it.
func agyDrives(method string) bool {
	return !awayReads[method] && method != "session.unwatch"
}

// codeAgyInTerminal refuses a call that would drive a conversation an agy in a terminal has open.
const codeAgyInTerminal = "agy_in_terminal"

// agyOpenCall is agyOpenIn for a call that drives the session; reads stay the companion's.
func (n *Node) agyOpenCall(method, id string) (pane string, a host.Agent, held bool) {
	if !agyDrives(method) {
		return "", host.Agent{}, false
	}
	return n.agyOpenIn(id)
}

// agyOpenIn is where the companion's session agy-<conversation> is open, for a call that would drive it: the live pane
// running it (its session id and agent), else held when an agy outside any pane the node knows holds its presence lock.
func (n *Node) agyOpenIn(id string) (pane string, a host.Agent, held bool) {
	conversation, ok := strings.CutPrefix(id, agyAgent+"-")
	if !ok || !uuidRe.MatchString(conversation) {
		return "", host.Agent{}, false
	}
	live := map[string]host.Agent{}
	n.mu.Lock()
	for candidate, ag := range n.agents {
		if ag.Agent == agyAgent && ag.AgentStatus != proto.StatusOffline && ag.AgentStatus != proto.StatusInterrupted {
			live[candidate] = ag
		}
	}
	n.mu.Unlock()
	for candidate, ag := range live { // agyConversation takes n.mu itself and may read the transcript
		if n.agyConversation(candidate, ag.PaneID) == conversation {
			pane, a = candidate, ag
			break
		}
	}
	if pane != "" {
		return pane, a, true
	}
	return "", host.Agent{}, agyPresenceHeld(n.agyDataRoot(), conversation)
}
