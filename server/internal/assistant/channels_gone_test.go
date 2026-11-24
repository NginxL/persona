package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/channel"
)

// A run whose machine left the configuration ends like any other: the owner is told why, the message's reaction
// turns to DONE, and the run is gone, instead of being dropped with the message left on "OnIt".
func TestChannelRunOnAMachineGoneIsAnswered(t *testing.T) {
	needsLarkLayer(t)
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.devices = []Device{{ID: "server", Name: "server", Online: true, Reachable: true}} // "box" is not configured any more
	f.devicesFull = true
	var reacts []string
	f.answerApp(AppFeishu, FeishuReact, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		reacts = append(reacts, m["emoji"].(string))
		return map[string]any{"reaction_id": "r2"}, nil
	})
	unreacted := 0
	f.answerApp(AppFeishu, FeishuUnreact, func(json.RawMessage) (any, error) { unreacted++; return map[string]any{}, nil })
	s := newTestService(t, f)
	r := &channel.Run{ID: "fb_om_m1", Channel: channel.Feishu, MachineID: "box", SessionID: "a1", ReplyTo: "om_m1", Label: "Claude",
		Started: time.Now(), Ext: map[string]string{"reaction": "r1"}}
	s.chans.mu.Lock()
	s.loadChannelStateLocked(t.Context())
	s.chans.st.Runs = append(s.chans.st.Runs, r)
	s.chans.mu.Unlock()

	s.watchChannelRun(r)

	if len(*posts) != 1 {
		t.Fatalf("posts %v", *posts)
	}
	if card, _ := (*posts)[0]["card"].(string); !strings.Contains(card, "不在 Persona 里") {
		t.Fatalf("card %s", card)
	}
	if unreacted != 1 || len(reacts) != 1 || reacts[0] != "DONE" {
		t.Fatalf("reactions: off %d, on %v", unreacted, reacts)
	}
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Fatal("the run is still waiting")
	}
}

// goneRun is a Feishu run on machine "box", waiting in the channels' state.
func goneRun(t *testing.T, s *Service) *channel.Run {
	t.Helper()
	r := &channel.Run{ID: "fb_om_m1", Channel: channel.Feishu, MachineID: "box", SessionID: "a1", ReplyTo: "om_m1", Label: "Claude",
		Started: time.Now(), Ext: map[string]string{"reaction": "r1"}}
	s.chans.mu.Lock()
	s.loadChannelStateLocked(t.Context())
	s.chans.st.Runs = append(s.chans.st.Runs, r)
	s.chans.mu.Unlock()
	return r
}

// The note of a run whose machine was removed that does not get out the first time is tried again until it does.
func TestChannelRunOnAMachineGoneTriesAgain(t *testing.T) {
	needsLarkLayer(t)
	old := channelGoneRetry
	channelGoneRetry = 10 * time.Millisecond
	t.Cleanup(func() { channelGoneRetry = old })
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.devices, f.devicesFull = []Device{{ID: "server", Name: "server", Online: true, Reachable: true}}, true
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { return map[string]any{"reaction_id": "r2"}, nil })
	f.answerApp(AppFeishu, FeishuUnreact, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	tries := 0
	f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		tries++
		if tries == 1 {
			return nil, bus.ErrOffline // the hub's connection dropped: nothing went out
		}
		return map[string]any{"message_id": "om_card"}, nil
	})
	s := newTestService(t, f)
	r := goneRun(t, s)
	done := make(chan struct{})
	go func() { s.watchChannelRun(r); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run was left waiting")
	}
	if tries != 2 {
		t.Fatalf("tries %d", tries)
	}
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Fatal("the run is still waiting")
	}
}

// Not confirmed removed (the hub's list is not complete yet, the machine is one it keeps out of the list for now, or
// it is listed but offline): the run is watched as before, not ended as gone.
func TestChannelRunMachineNotConfirmedGone(t *testing.T) {
	for _, c := range []struct {
		name    string
		devices []Device
		full    bool
		others  []string
	}{
		{"list not complete", []Device{{ID: "server", Online: true}}, false, nil},
		{"kept out (pending, absorbed)", []Device{{ID: "server", Online: true}}, true, []string{"box"}},
		{"listed but offline", []Device{{ID: "server", Online: true}, {ID: "box", Online: false}}, true, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, posts := feishuHub("ou_a", time.Unix(100, 0))
			f.devices, f.devicesFull, f.devicesOther = c.devices, c.full, c.others
			s := newTestService(t, f)
			if s.machineRemoved(t.Context(), "box") {
				t.Fatal("judged removed")
			}
			r := goneRun(t, s)
			go s.watchChannelRun(r)
			time.Sleep(200 * time.Millisecond)
			if len(*posts) != 0 {
				t.Fatalf("answered: %v", *posts)
			}
			if run, _ := s.channelRunByID(r.ID); run == nil {
				t.Fatal("the run was dropped")
			}
			s.chans.mu.Lock()
			if p := s.chans.live[r.ID]; p != nil {
				p.finished = true
				close(p.done)
			}
			s.chans.mu.Unlock()
		})
	}
}
