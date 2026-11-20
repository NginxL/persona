package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A saved visibility transition must update a client that stays connected, even
// when a previous manager version omitted the card from its reconnect snapshot.
func TestHiddenDirectoryPendingCardTransportTransitions(t *testing.T) {
	ft := newFleetTest(t)
	ft.d.setSessions("mac", []proto.Session{{SessionID: "blocked", Cwd: "/work/noisy", Status: proto.StatusBlocked}})
	ix := proto.Interaction{ID: "ix_hidden", MachineID: "mac", SessionID: "blocked", Kind: proto.IxPermission, Summary: "Approve work", CreatedAt: time.Now()}
	ft.s.fleet.mu.Lock()
	ft.s.fleet.interactions = map[string]*pending{ix.ID: {ix: ix, told: false}}
	ft.s.fleet.mu.Unlock()
	dirs := useAgentDirs(ft.s)
	on := []proto.HiddenAgentDir{{Pattern: "/work/noisy", Enabled: true}}
	off := []proto.HiddenAgentDir{{Pattern: "/work/noisy", Enabled: false}}
	for _, step := range []struct {
		name  string
		rules []proto.HiddenAgentDir
	}{{"add", on}, {"disable", off}, {"enable", on}, {"remove", nil}} {
		t.Run(step.name, func(t *testing.T) {
			ft.f.mu.Lock()
			ft.f.told = nil
			ft.f.mu.Unlock()
			// Saved as the settings page saves: the devices store the rules, then tell the assistant.
			dirs.set(t, "mac", step.rules...)
			if len(ft.s.pendingInteractions()) != 1 {
				t.Fatal("authoritative inventory lost the pending card")
			}
			hidden := step.name == "add" || step.name == "enable"
			raw, handled, err := ft.s.localFleetDeviceCall(context.Background(), DeviceCall{Device: DeviceAll, Method: DeviceInteractionsList})
			if err != nil || !handled {
				t.Fatalf("projection: %v %v", handled, err)
			}
			var projection struct {
				Interactions []proto.Interaction `json:"interactions"`
			}
			if err := json.Unmarshal(raw, &projection); err != nil {
				t.Fatal(err)
			}
			if got := len(projection.Interactions); (got == 0) != hidden {
				t.Fatalf("visible projection: hidden=%v cards=%d", hidden, got)
			}
			ft.s.fleet.mu.Lock()
			told := ft.s.fleet.interactions[ix.ID].told
			ft.s.fleet.mu.Unlock()
			if told {
				t.Fatal("refresh changed delegation/told state")
			}
			if len(ft.f.pushed()) != 0 {
				t.Fatal("visibility refresh sent a notification")
			}
			ft.f.mu.Lock()
			defer ft.f.mu.Unlock()
			delivered, sessionsDelivered := false, false
			for _, event := range ft.f.told {
				if event.event == assistantwire.EventFleetSessions {
					sessionsDelivered = true
				}
				if event.event == assistantwire.EventFleetInteraction {
					if !sessionsDelivered {
						t.Fatal("card replay preceded the new session visibility")
					}
					var p assistantwire.FleetInteraction
					_ = json.Unmarshal(event.params, &p)
					delivered = delivered || p.Interaction.ID == ix.ID
				}
				if event.event == assistantwire.EventFleetResolved {
					t.Fatal("visibility resolved a pending card")
				}
			}
			if !delivered {
				t.Fatal("pending card was not delivered to an already-connected client")
			}
		})
	}
}

// stallingHub is the fake hub whose fleet.interaction writes wait for release, as a stalled hub connection would.
type stallingHub struct {
	*fakeHub
	entered chan struct{}
	release chan struct{}
}

func (h *stallingHub) Tell(event string, params any) error {
	if event == assistantwire.EventFleetInteraction {
		select {
		case h.entered <- struct{}{}:
		default:
		}
		<-h.release
	}
	return h.fakeHub.Tell(event, params)
}

// A visibility replay stalled on the hub's connection must not hold fleet.mu: card lookups and answers go on, and the
// replay still arrives once the write is released (review of #8584).
func TestHiddenDirectoryReplayStallDoesNotBlockCards(t *testing.T) {
	ft := newFleetTest(t)
	ft.d.setSessions("mac", []proto.Session{{SessionID: "blocked", Cwd: "/work/noisy", Status: proto.StatusBlocked}})
	ix := proto.Interaction{ID: "ix_stall", MachineID: "mac", SessionID: "blocked", Kind: proto.IxPermission, Summary: "Approve work", CreatedAt: time.Now()}
	ft.s.fleet.mu.Lock()
	ft.s.fleet.interactions = map[string]*pending{ix.ID: {ix: ix}}
	ft.s.fleet.mu.Unlock()
	dirs := useAgentDirs(ft.s)
	hub := &stallingHub{fakeHub: ft.f, entered: make(chan struct{}, 1), release: make(chan struct{})}
	ft.s.hub = hub
	released := false
	defer func() {
		if !released {
			close(hub.release)
		}
	}()

	replayed := make(chan struct{})
	go func() {
		defer close(replayed)
		dirs.set(t, "mac", proto.HiddenAgentDir{Pattern: "/work/other", Enabled: true})
	}()
	select {
	case <-hub.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the replay never wrote the pending card")
	}

	within := func(what string, f func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { defer close(done); f() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s waited for the stalled replay write", what)
		}
	}
	within("pendingIx", func() {
		if _, ok := ft.s.pendingIx(ix.ID); !ok {
			t.Error("pendingIx lost the card")
		}
	})
	deny := false
	within("resolve", func() { _ = ft.s.resolve(ix.ID, proto.Resolution{Allow: &deny}) })

	close(hub.release)
	released = true
	select {
	case <-replayed:
	case <-time.After(5 * time.Second):
		t.Fatal("the replay did not finish after its write was released")
	}
	ft.f.mu.Lock()
	defer ft.f.mu.Unlock()
	delivered := false
	for _, event := range ft.f.told {
		if event.event == assistantwire.EventFleetInteraction {
			var p assistantwire.FleetInteraction
			_ = json.Unmarshal(event.params, &p)
			delivered = delivered || p.Interaction.ID == ix.ID
		}
	}
	if !delivered {
		t.Fatal("the replay did not arrive once its write was released")
	}
}
