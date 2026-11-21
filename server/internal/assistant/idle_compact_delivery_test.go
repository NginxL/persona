package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestIdleCompactPolicyReachesMainDeliveryPaths(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	main, err := s.start(ctx, StartParams{Main: true, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	want := assistantwire.IdleCompaction{Enabled: true, IdleMinutes: 30, ContextTokens: 300000}
	wantWindow := defaultMainCompactWindow
	check := func(params map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(params["idle_compaction"])
		var got assistantwire.IdleCompaction
		if err := json.Unmarshal(raw, &got); err != nil || got != want {
			t.Fatalf("idle compaction policy = %s (%v), want %+v", raw, err, want)
		}
		raw, _ = json.Marshal(params["main_compact_window"])
		var window int
		if err := json.Unmarshal(raw, &window); err != nil || window != wantWindow {
			t.Fatalf("main compact window = %s (%v), want %d alongside idle policy", raw, err, wantWindow)
		}
	}
	check(f.lastSend(t))
	f.setSessions("server", proto.Session{SessionID: main.SessionID, Assistant: true})
	// Apps use sendModel/SendParams, rather than dispatching through Service.call.
	params, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: main.SessionID, Params: map[string]any{"text": "next"}})
	if err != nil {
		t.Fatal(err)
	}
	check(params)
	// Supervision and conversation relays dispatch through Service.call.
	if err := s.deliverRelay(ctx, "server", main.SessionID, relayMsg{Tool: superviseTool, Text: "check tasks", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	check(f.lastSend(t))

	off, minutes, tokens, release := false, 45, 450000, 40
	wantWindow = 250000
	setSettings(t, s, func(v *Settings) {
		v.IdleAutoCompact, v.IdleAutoCompactMinutes, v.IdleAutoCompactTokens = &off, &minutes, &tokens
		v.IdleReleaseMinutes = &release
		v.MainCompactWindow = &wantWindow
	})
	// Saved, the tokens are also the release-time threshold (#12008), with the switch off too.
	want = assistantwire.IdleCompaction{Enabled: false, IdleMinutes: minutes, ContextTokens: tokens, ReleaseMinutes: release, ReleaseTokens: tokens}
	params, err = s.SendParams(ctx, SendParams{MachineID: "server", SessionID: main.SessionID, Params: map[string]any{"text": "next"}})
	if err != nil {
		t.Fatal(err)
	}
	check(params)
	if err := s.deliverRelay(ctx, "server", main.SessionID, relayMsg{Tool: "conversation_send", Text: "note", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	check(f.lastSend(t))
	if _, err := s.start(ctx, StartParams{Text: "side chat"}); err != nil {
		t.Fatal(err)
	}
	if _, exists := f.lastSend(t)["idle_compaction"]; exists {
		t.Fatal("side chat received the main chat's idle compaction policy")
	}
	if _, exists := f.lastSend(t)["main_compact_window"]; exists {
		t.Fatal("side chat received the main chat's compact window")
	}
}

func TestIdleCompactRefusalKeepsOriginalRelayQueued(t *testing.T) {
	s, f := newQuietService(t)
	gap := relayRetryGap
	relayRetryGap = 0 // Exercise repeated compaction refusals without the normal retry gap.
	t.Cleanup(func() { relayRetryGap = gap })
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	msg := relayMsg{From: "conv2", Tool: "conversation_send", Text: "original relay", At: time.Now().UTC()}
	s.queueRelay(idle.SessionID, msg)
	tries := relayTries(f, idle.SessionID)
	pending := &rpc.Error{Code: "compacting", Message: "The conversation is still compacting."}
	if relayInDoubt(pending) {
		t.Fatal("an explicit compaction refusal was mistaken for an ambiguous delivery")
	}
	f.mu.Lock()
	f.failAssistant = pending
	f.mu.Unlock()
	for attempt := 1; attempt <= 2; attempt++ {
		s.drainRelays("server", []proto.Session{idle})
		waitFor(t, "compacting relay refusal", func() bool {
			return len(triesOf(f, tries)) == attempt && !drainingNow(s, idle.SessionID)
		})
		if s.queuedFor(idle.SessionID) != 1 || f.sentTo(idle.SessionID) != 0 {
			t.Fatal("a compacting refusal dropped or delivered the original relay")
		}
	}
	f.mu.Lock()
	f.failAssistant = nil
	f.mu.Unlock()
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "original relay after compaction", func() bool {
		return s.queuedFor(idle.SessionID) == 0 && f.sentTo(idle.SessionID) == 1 && !drainingNow(s, idle.SessionID)
	})
	got := triesOf(f, tries)
	if len(got) != 3 || got[0] != relayMsgID(idle.SessionID, msg) || got[1] != got[0] || got[2] != got[0] {
		t.Fatalf("the original relay lost its retry identity: %q", got)
	}
}
