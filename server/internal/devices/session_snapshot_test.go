package devices

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestReconnectSnapshotCannotOverwriteLiveSessions(t *testing.T) {
	for _, status := range []string{proto.StatusWorking, proto.StatusDone} {
		t.Run(status, func(t *testing.T) {
			peer := new(rpc.Peer)
			m := &Machine{cfg: MachineConfig{ID: "computer"}, peer: peer}
			revision := m.sessionsRevision()
			m.setSessions([]proto.Session{{SessionID: "agent", Status: status}})
			if m.setInitialSessions(peer, revision, []proto.Session{{SessionID: "agent", Status: proto.StatusIdle}}) {
				t.Fatal("a reconnect snapshot replaced a live notification")
			}
			if got := m.sessionList(); len(got) != 1 || got[0].Status != status {
				t.Fatalf("sessions = %+v", got)
			}
		})
	}
}

func TestReconnectSnapshotRequiresCurrentConnection(t *testing.T) {
	peer := new(rpc.Peer)
	m := &Machine{cfg: MachineConfig{ID: "computer"}, peer: peer}
	if m.setInitialSessions(new(rpc.Peer), m.sessionsRevision(), nil) {
		t.Fatal("accepted a disconnected owner's snapshot")
	}
	if !m.setInitialSessions(peer, m.sessionsRevision(), []proto.Session{{SessionID: "agent", Status: proto.StatusWorking}}) {
		t.Fatal("rejected the current connection's initial list")
	}
	if got := m.sessionList()[0]; got.MachineID != "computer" || got.Status != proto.StatusWorking {
		t.Fatalf("session = %+v", got)
	}
}

func TestEventsFromRetiredPeerCannotOverwriteReplacement(t *testing.T) {
	d := newTestDevices(t)
	n := &buildNode{rows: []proto.Session{{SessionID: "s1", Status: proto.StatusWorking}}}
	m := connectMachine(t, d, MachineConfig{ID: "computer", Name: "computer", Kind: kindLink}, n)
	m.mu.Lock()
	oldPeer := m.peer
	m.mu.Unlock()
	n.mu.Lock()
	n.rows = []proto.Session{{SessionID: "s1", Status: proto.StatusDone}}
	n.mu.Unlock()
	m.reconnect()
	eventually(t, 5*time.Second, "the replacement's initial list", func() bool { return n.seen("sessions.list") > 1 })
	m.mu.Lock()
	newPeer := m.peer
	m.mu.Unlock()
	metric := func(v int64) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"sample": proto.MetricSample{T: v}})
		return raw
	}
	newPeer.OnEvent("metrics", metric(200))

	forwarded := 0
	d.opts.OnEvent = func(_ string, event string, _ json.RawMessage) bool {
		if event == "sessions.changed" {
			forwarded++
		}
		return true
	}
	oldPeer.OnEvent("sessions.changed", []byte(`{"sessions":[{"session_id":"s1","status":"working"}]}`))
	oldPeer.OnEvent("metrics", metric(100))
	if got := m.sessionList(); len(got) != 1 || got[0].Status != proto.StatusDone {
		t.Fatalf("the retired session event overwrote the replacement: %+v", got)
	}
	_, latest, _ := d.MachineResources("computer")
	if latest == nil || latest.T != 200 {
		t.Fatalf("the retired metric overwrote the replacement: %+v", latest)
	}
	if forwarded != 0 {
		t.Fatalf("the retired session event was forwarded %d time(s)", forwarded)
	}
}
