package devices

import (
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A node that drops and comes straight back (a resident node restarting on a new binary) is not news: the machine
// shows online meanwhile and nothing goes to the apps; one that stays away shows offline once the grace is over.
func TestNodeReconnectGrace(t *testing.T) {
	h := newTestDevices(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "box"})
	m.reconnectGrace = 150 * time.Millisecond // this machine's own: no package var for another test's run to read
	h.addEntry(m)
	go h.reportLoop(h.ctx)
	m.online = true
	m.sessions = []proto.Session{{SessionID: "s1", Status: proto.StatusWorking}}
	h.report(false)
	seen := 0
	// machinesSent is how many reports since the last look said the machine is offline, and whether the last is online.
	machinesSent := func() (n int, online bool) {
		f := h.host()
		f.mu.Lock()
		defer f.mu.Unlock()
		online = true
		for _, st := range f.states[seen:] {
			if len(st.Machines) == 1 {
				online = st.Machines[0].Machine.Online
				if !online {
					n++
				}
			}
		}
		seen = len(f.states)
		return n, online
	}

	machinesSent()
	m.detach(errors.New("node exited"), false)
	if !m.snapshot().Online || m.sessionList()[0].Status != proto.StatusWorking {
		t.Fatal("a node that just disconnected shows offline at once")
	}
	time.Sleep(100 * time.Millisecond)
	if n, _ := machinesSent(); n != 0 {
		t.Fatalf("the apps were told about a disconnect within the grace (%d)", n)
	}
	time.Sleep(3 * m.reconnectGrace)
	if m.snapshot().Online || m.sessionList()[0].Status != proto.StatusUnknown {
		t.Fatal("still online after the grace")
	}
	if n, online := machinesSent(); n != 1 || online {
		t.Fatalf("offline after the grace: %d broadcasts, online %v", n, online)
	}
}

// Shown says whether a machine's list is its node's, and until when it is shown as before while its node is away
// (the assistant's direction sessions read it, #11458): no list yet on a connection is not an empty list, and the
// reconnect grace says when it ends.
func TestShownListedAndUntil(t *testing.T) {
	h := newTestDevices(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "box"})
	m.reconnectGrace = time.Hour // this machine's own; the test ends long before it
	h.addEntry(m)
	m.mu.Lock()
	m.online = true
	m.mu.Unlock()
	if s, ok := h.Shown("box"); !ok || s.Listed || !s.Until.IsZero() {
		t.Fatalf("connected with no list yet: %+v, %v", s, ok)
	}
	m.mu.Lock()
	m.sessions, m.sessionsListed = []proto.Session{}, true
	m.mu.Unlock()
	if s, _ := h.Shown("box"); !s.Listed || !s.Until.IsZero() {
		t.Fatalf("an empty list given: %+v", s)
	}
	m.detach(errors.New("node exited"), false)
	if s, _ := h.Shown("box"); !s.Listed || !s.Until.After(time.Now()) || !m.snapshot().Online {
		t.Fatalf("within the reconnect grace: %+v", s)
	}
	if _, ok := h.Shown("nobody"); ok {
		t.Fatal("a machine there is none of")
	}
}
