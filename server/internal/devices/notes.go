package devices

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The hub's one-way events for a node (devices.notify: terminal input, a forwarded port's bytes) come in on the bus's
// read loop, which must not wait for any one computer: they are queued per machine and written by one writer each, in
// the order they came, so a computer slow to take them holds up only its own. One whose write stalls noteStall, or
// that lets maxNoteBytes pile up in both queues together, is connected again (what ran on the connection ends, as with
// any dropped one). A forwarded port's streams queue apart from the rest (portNote): on a framed connection their bytes
// wait for credit, and terminal input behind them would wait with them. A stall is no progress at all: on a framed
// connection a note can wait for credit while the connection writes everything else.
var (
	noteStall    = 30 * time.Second
	maxNoteBytes = 32 << 20
)

type nodeNotes struct {
	mu      sync.Mutex
	q       []nodeNote
	bytes   int
	writing bool
}

type nodeNote struct {
	event  string
	params json.RawMessage
}

// portNote: event belongs to a forwarded port's stream and stays in order with its bytes, including EOF and close. Its
// acknowledgements (stream.ack) go with the other events: they wait for no credit, and nothing waits behind them.
func portNote(event string) bool { return strings.HasPrefix(event, "stream.") && event != "stream.ack" }

// queueNote queues an event for the node.
func (m *Machine) queueNote(event string, params json.RawMessage) {
	n := &m.notes
	if portNote(event) {
		n = &m.bulkNotes
	}
	if m.noteBytes.Add(int64(len(params))) > int64(maxNoteBytes) {
		dropped := m.notes.drop(&m.noteBytes) + m.bulkNotes.drop(&m.noteBytes)
		m.noteBytes.Add(-int64(len(params)))
		m.d.logf("[%s] its node takes no events (%d waiting): connecting it again", m.cfg.ID, dropped)
		m.reconnect()
		return
	}
	n.mu.Lock()
	n.q = append(n.q, nodeNote{event: event, params: params})
	n.bytes += len(params)
	start := !n.writing
	n.writing = true
	n.mu.Unlock()
	if start {
		go m.writeNotes(n)
	}
}

// writeNotes writes the events queued on n to the node's connection of the moment until none is left.
func (m *Machine) writeNotes(n *nodeNotes) {
	for {
		n.mu.Lock()
		if len(n.q) == 0 {
			n.q, n.writing = nil, false
			n.mu.Unlock()
			return
		}
		e := n.q[0]
		n.q[0] = nodeNote{}
		n.q = n.q[1:]
		n.bytes -= len(e.params)
		m.noteBytes.Add(-int64(len(e.params)))
		n.mu.Unlock()
		m.mu.Lock()
		p := m.peer
		m.mu.Unlock()
		if p == nil {
			continue
		}
		// While the connection waits to be resumed nothing is written (resume.go): the note is watched again from the
		// resume, which counts as a write.
		watch := watchNote(p.LastWrite, p.Lost, func() {
			stalled := errors.New("the node stopped taking events")
			if p.Lose(stalled) { // the node keeps the connection: a new transport takes it back
				m.d.logf("[%s] its node took nothing for %s: resuming on a new transport", m.cfg.ID, noteStall)
				return
			}
			m.d.logf("[%s] its node took nothing for %s: connecting it again", m.cfg.ID, noteStall)
			p.Close(stalled)
		})
		_ = p.Notify(e.event, e.params)
		watch.stop()
	}
}

// drop lets everything queued on n go, takes its bytes off budget (the machine's noteBytes), and says how many
// notes that was.
func (n *nodeNotes) drop(budget *atomic.Int64) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	dropped := len(n.q)
	budget.Add(-int64(n.bytes))
	n.q = nil
	n.bytes = 0
	return dropped
}

// noteWatch closes a connection (stalled) once nothing it writes has gone through for noteStall (written: its last
// write) while a note waits to be written. While waiting says the connection waits for something else (to be resumed),
// it looks again a noteStall later.
type noteWatch struct {
	mu   sync.Mutex
	t    *time.Timer
	done bool
}

func watchNote(written func() time.Time, waiting func() bool, stalled func()) *noteWatch {
	w := &noteWatch{}
	var check func()
	check = func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.done {
			return
		}
		if idle := time.Since(written()); idle < noteStall {
			w.t = time.AfterFunc(noteStall-idle, check) // it moves: look again when it would have stalled
			return
		}
		if waiting() {
			w.t = time.AfterFunc(noteStall, check)
			return
		}
		w.done = true
		go stalled()
	}
	w.mu.Lock()
	w.t = time.AfterFunc(noteStall, check)
	w.mu.Unlock()
	return w
}

func (w *noteWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
	w.t.Stop()
}
