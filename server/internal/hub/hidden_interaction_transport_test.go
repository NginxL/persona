package hub

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/db/dbtest"
	"github.com/nwverse/persona/server/internal/proto"
)

// hubAgentDirs keeps hidden Agent folders for the assistant the tests connect, as the devices do: per machine, the
// assistant told after each change.
type hubAgentDirs struct {
	mu      sync.Mutex
	rules   map[string][]proto.HiddenAgentDir
	changed func(machine string)
}

func (d *hubAgentDirs) HiddenAgentDirs(_ context.Context, id string) ([]proto.HiddenAgentDir, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.rules[id]), "", nil
}

func (d *hubAgentDirs) EditHiddenAgentDirs(_ context.Context, id string, edit func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error)) ([]proto.HiddenAgentDir, error) {
	d.mu.Lock()
	next, err := edit(slices.Clone(d.rules[id]))
	if err == nil {
		d.rules[id] = slices.Clone(next)
	}
	changed := d.changed
	d.mu.Unlock()
	if err == nil && changed != nil {
		changed(id)
	}
	return next, err
}

// testAgentDirs are the hidden folders the tests' assistants read, by hub (dialAssistant passes them on).
var testAgentDirs sync.Map

func testAgentDirsOf(h *Hub) assistant.AgentDirs {
	if d, ok := testAgentDirs.Load(h); ok {
		return d.(*hubAgentDirs)
	}
	return nil
}

// withAgentDirs gives the assistant connected to h from now on hidden Agent folders the test changes.
func withAgentDirs(t *testing.T, h *Hub) *hubAgentDirs {
	t.Helper()
	d := &hubAgentDirs{rules: map[string][]proto.HiddenAgentDir{}}
	testAgentDirs.Store(h, d)
	t.Cleanup(func() { testAgentDirs.Delete(h) })
	return d
}

// Real manager and hub connected over the bus: omission from FleetUp is an
// authoritative resolution, so hiding must never remove the pending card there.
func TestHiddenDirectoryFleetUpRetainsPendingCard(t *testing.T) {
	hiddenDirectoryCardRoundTrip(t, false)
}

func TestHiddenDirectoryUnhideRestoresPreviouslyOmittedCard(t *testing.T) {
	hiddenDirectoryCardRoundTrip(t, true)
}

func hiddenDirectoryCardRoundTrip(t *testing.T, previouslyOmitted bool) {
	t.Helper()
	base := os.Getenv(dbtest.Env)
	if base == "" {
		t.Skip(dbtest.Env + " not set")
	}
	url, cleanup, err := dbtest.Own(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	database, err := db.Open(context.Background(), url, db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Computer"})
	registerMachine(h, m)
	m.sessions = []proto.Session{{MachineID: "box", SessionID: "blocked", Cwd: "/work/noisy", Status: proto.StatusBlocked}}
	dirs := withAgentDirs(t, h)
	a := connectAssistant(t, h, database)
	app := &client{out: make(chan []byte, 256), subs: map[[2]string]bool{}, role: roleFull}
	h.mu.Lock()
	h.clients[app] = struct{}{}
	h.mu.Unlock()
	t.Cleanup(func() { h.mu.Lock(); delete(h.clients, app); h.mu.Unlock() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ix := proto.Interaction{ID: "ix_hidden_transport", MachineID: "box", SessionID: "blocked", Kind: proto.IxPermission, Summary: "Approve work"}
	raw, _ := json.Marshal(map[string]any{"interaction": ix})
	done := make(chan struct{})
	result := make(chan proto.Resolution, 1)
	go func() {
		defer close(done)
		value, _, _ := a.svc.NodeRequest(ctx, "box", "interaction.create", raw)
		if r, ok := value.(proto.Resolution); ok {
			result <- r
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("node request did not stop")
		}
	})
	eventually(t, 5*time.Second, "pending card at hub", func() bool { return len(h.pendingInteractions()) == 1 })
	for len(app.out) > 0 {
		<-app.out
	}
	// Saved as the computer's settings page saves: the devices store the rules, then tell the assistant.
	save := func(rules ...proto.HiddenAgentDir) {
		t.Helper()
		if _, err := dirs.EditHiddenAgentDirs(ctx, "box", func([]proto.HiddenAgentDir) ([]proto.HiddenAgentDir, error) { return rules, nil }); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	save(proto.HiddenAgentDir{Pattern: "/work/noisy", Enabled: true})
	// The ordered bus places a following marker after FleetUp, avoiding a sleep
	// that could check hub state before the authoritative snapshot arrived.
	a.svc.FleetUp(ctx)
	marker := proto.Interaction{ID: "ix_transport_barrier", MachineID: "box", SessionID: "barrier", Kind: proto.IxPermission}
	markerRaw, _ := json.Marshal(map[string]any{"interaction": marker})
	markerCtx, markerCancel := context.WithCancel(ctx)
	defer markerCancel()
	go func() { _, _, _ = a.svc.NodeRequest(markerCtx, "box", "interaction.create", markerRaw) }()
	eventually(t, 5*time.Second, "FleetUp barrier", func() bool {
		for _, c := range h.pendingInteractions() {
			if c.ID == marker.ID {
				return true
			}
		}
		return false
	})
	retained := false
	for _, c := range h.pendingInteractions() {
		retained = retained || c.ID == ix.ID
	}
	if !retained {
		t.Fatal("FleetUp forgot a hidden pending card at the real hub")
	}
	for len(app.out) > 0 {
		event := <-app.out
		var p struct{ Type, ID string }
		_ = json.Unmarshal(event, &p)
		if p.Type == "interaction_resolved" && p.ID == ix.ID {
			t.Fatal("hiding/reconnect resolved the blocked agent's card")
		}
	}
	if previouslyOmitted {
		// State left behind by the older visibility-filtered reconnect snapshot.
		h.takeFleetInteractionsFrom(assistantwire.AssistantID, nil)
		if len(h.pendingInteractions()) != 0 {
			t.Fatal("legacy omission fixture did not remove the cards")
		}
	}
	save()
	received := false
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !received {
		select {
		case b := <-app.out:
			var p struct {
				Type        string
				Interaction proto.Interaction
			}
			_ = json.Unmarshal(b, &p)
			received = p.Type == "interaction" && p.Interaction.ID == ix.ID
		case <-deadline.C:
			t.Fatal("unhide did not deliver the pending card to the connected client")
		}
	}
	retained = false
	for _, card := range h.pendingInteractions() {
		retained = retained || card.ID == ix.ID
	}
	if !retained {
		t.Fatal("unhide lost the actionable card")
	}
	if err := h.resolve(ix.ID, proto.Resolution{Allow: new(true)}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-result:
		if r.Allow == nil || !*r.Allow {
			t.Fatalf("node did not receive approval: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retained card could not unblock the node")
	}
}
