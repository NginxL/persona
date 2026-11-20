package node

import (
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// A message typed into a Cursor or agy pane through herdr (workAgentCalls session.send) takes a moment to show as a
// turn: herdr reads the screen and still calls the agent idle right after. An agent_interrupt in that moment found
// the agent "not working", pressed nothing and queued its instruction behind the whole turn (#11437). So for
// workSentGrace after a send the pane's row reads as working unless herdr already says working or blocked.

// workSentGrace is how long a sent message keeps an idle-looking Cursor or agy row working (a variable for tests).
var workSentGrace = 8 * time.Second

// workSent is when a message last went into each work agent's pane.
type workSent struct {
	mu sync.Mutex
	at map[string]time.Time // pane -> time of the last message typed into it
}

func (w *workSent) mark(pane string, t time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.at == nil {
		w.at = map[string]time.Time{}
	}
	w.at[pane] = t
}

// recent: a message went into pane less than workSentGrace before now. Older marks are dropped.
func (w *workSent) recent(pane string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	t, ok := w.at[pane]
	if !ok {
		return false
	}
	if now.Sub(t) >= workSentGrace {
		delete(w.at, pane)
		return false
	}
	return true
}

// workAgentStatus is a Cursor or agy row's status: herdr's, or working while a message just typed into its pane has
// not shown as a turn yet.
func (n *Node) workAgentStatus(a host.Agent, now time.Time) string {
	switch a.AgentStatus {
	case proto.StatusIdle, proto.StatusDone, proto.StatusUnknown, "":
		if n.workSent.recent(a.PaneID, now) {
			return proto.StatusWorking
		}
	}
	return a.AgentStatus
}
