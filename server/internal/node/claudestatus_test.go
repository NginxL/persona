package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// boxed puts rows above a Claude Code input box and its footer, as a screen shows them.
func boxed(rows ...string) string {
	rule := strings.Repeat("─", 60)
	return strings.Join(rows, "\n") + "\n" + rule + "\n❯ \n" + rule + "\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
}

// none is the zero live state, used for "no status line".
var none proto.SessionLive

func TestParseClaudeStatus(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   proto.SessionLive
	}{
		{"thinking", boxed("✻ Frosting… (16m 15s · ↓ 22.8k tokens · thinking with xhigh effort)"),
			proto.SessionLive{Verb: "Frosting", ElapsedS: 975, TokensDown: 22800, Thinking: true}},
		{"thought", boxed("✽ Frosting… (16m 17s · ↓ 23.1k tokens · thought for 1s)"),
			proto.SessionLive{Verb: "Frosting", ElapsedS: 977, TokensDown: 23100, ThoughtS: 1}},
		{"plain", boxed("✢ Frosting… (16m 20s · ↓ 23.1k tokens)"),
			proto.SessionLive{Verb: "Frosting", ElapsedS: 980, TokensDown: 23100}},
		{"input tokens are not output", boxed("· Frosting… (2s · ↑ 1.2k tokens)"), proto.SessionLive{Verb: "Frosting", ElapsedS: 2}},
		{"spinner ✳", boxed("✳ Cogitating… (1m 5s · ↓ 900 tokens)"),
			proto.SessionLive{Verb: "Cogitating", ElapsedS: 65, TokensDown: 900}},
		{"spinner ✶, hours, M", boxed("✶ Baking… (1h 2m 3s · ↓ 1.5M tokens)"),
			proto.SessionLive{Verb: "Baking", ElapsedS: 3723, TokensDown: 1500000}},
		{"spinner *, comma count", boxed("* Brewing… (5s · ↓ 1,234 tokens)"),
			proto.SessionLive{Verb: "Brewing", ElapsedS: 5, TokensDown: 1234}},
		{"order changed, esc hint", boxed("✻ Herding… (↓ 3.2k tokens · esc to interrupt · 12s)"),
			proto.SessionLive{Verb: "Herding", ElapsedS: 12, TokensDown: 3200}},
		{"only the hint", boxed("✻ Herding… (esc to interrupt)"), proto.SessionLive{Verb: "Herding"}},
		{"no parenthesis yet", boxed("✻ Herding…"), proto.SessionLive{Verb: "Herding"}},
		{"three dots", boxed("✻ Herding... (3s)"), proto.SessionLive{Verb: "Herding", ElapsedS: 3}},
		{"tip and todos under it", boxed("✻ Herding… (4s)", "  ⎿  Tip: something", "     ☐ a task", "",
			strings.Repeat(" ", 40)+"◎ /goal active (1h)"), proto.SessionLive{Verb: "Herding", ElapsedS: 4}},
		{"no status line", boxed("⏺ Done.", "", "✻ Worked for 27m 30s"), none},
		{"reply right above the box", boxed("✻ Herding… (4s)", "", "⏺ All done."), none},
		{"quoted in the reply", boxed("⏺ It reads:", "", "  ✻ Frosting… (16m 15s · ↓ 22.8k tokens)", ""), none},
		{"quoted in the prompt", boxed("❯ ✻ Frosting… (16m 15s · ↓ 22.8k tokens)"), none},
		{"one in the box is not above it", strings.Repeat("─", 60) + "\n✻ Frosting… (3s)\n" + strings.Repeat("─", 60) + "\n", none},
		{"not a parenthesis after the verb", boxed("✻ Herding… and more"), none},
		{"empty", "", none},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseClaudeStatus(c.screen)
			if ok != (c.want != none) || got != c.want {
				t.Fatalf("got %+v %v, want %+v", got, ok, c.want)
			}
		})
	}
}

// Screens read off real Claude Code panes (herdr pane read --source visible), with their content replaced.
func TestParseClaudeStatusRealScreens(t *testing.T) {
	for name, want := range map[string]proto.SessionLive{
		"working.txt":       {Verb: "Frosting", ElapsedS: 27*60 + 16, TokensDown: 41500},
		"todos.txt":         {Verb: "Frosting", ElapsedS: 3723, ThoughtS: 3},
		"done_quoted.txt":   none,
		"retry_429.txt":     {RetryError: "429 Synthetic rate limit for retry capture", RetryAttempt: 5, RetryMax: 10},
		"retry_500.txt":     {RetryError: "500 Synthetic upstream failure for retry capture", RetryAttempt: 4, RetryMax: 10},
		"retry_generic.txt": {RetryError: "API Error", RetryAttempt: 1, RetryMax: 10},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", "claudestatus", name))
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := parseClaudeStatus(string(b)); ok != (want != none) || got != want {
			t.Errorf("%s: got %+v %v, want %+v", name, got, ok, want)
		}
	}
}

func TestSameLiveIgnoresWhenItWasRead(t *testing.T) {
	a := &proto.SessionLive{Verb: "Frosting", ElapsedS: 3, At: "2026-01-02T03:04:05Z"}
	b := &proto.SessionLive{Verb: "Frosting", ElapsedS: 3, At: "2026-01-02T03:04:06Z"}
	c := &proto.SessionLive{Verb: "Frosting", ElapsedS: 4, At: "2026-01-02T03:04:06Z"}
	if !sameLive(a, b) || sameLive(a, c) || sameLive(a, nil) || !sameLive(nil, nil) {
		t.Fatal("sameLive")
	}
}

// liveNode is a node with herdr reporting agent on pane w1:p1 showing screen, reading every 20 ms, and a hub
// connection whose session.live pushes come back on the channel.
func liveNode(t *testing.T, agent map[string]any, screen string) (*Node, *fakeHerdr, <-chan json.RawMessage) {
	t.Helper()
	old := liveEvery
	liveEvery = 20 * time.Millisecond
	t.Cleanup(func() { liveEvery = old })
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, agent)
	f.setScreen("w1:p1", screen)
	all, pw := hubPushes(n)
	t.Cleanup(func() { pw.Close() })
	out := make(chan json.RawMessage, 64)
	go func() {
		for m := range all {
			if m.Event == "session.live" {
				out <- m.Params
			}
		}
	}()
	return n, f, out
}

// startLive runs sidA's status line as the watch pump does, every liveEvery; stop ends the watch as an unwatch does
// (endLive) and waits until it has.
func startLive(t *testing.T, n *Node) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &watch{cancel: cancel, ctx: ctx, id: sidA}
	done := make(chan struct{})
	go func() {
		defer close(done)
		tk := time.NewTicker(liveEvery)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				n.endLive(w)
				return
			case <-tk.C:
				n.pumpLive(w)
			}
		}
	}()
	t.Cleanup(cancel)
	return func() { cancel(); <-done }
}

func workingClaude() map[string]any {
	return claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", "/tmp/s", "")
}

// nextLive is the next session.live's live (nil for null), failing after a second.
func nextLive(t *testing.T, ch <-chan json.RawMessage) *proto.SessionLive {
	t.Helper()
	select {
	case raw := <-ch:
		var p struct {
			SessionID string             `json:"session_id"`
			Live      *proto.SessionLive `json:"live"`
		}
		if err := json.Unmarshal(raw, &p); err != nil || p.SessionID != sidA {
			t.Fatalf("session.live %s", raw)
		}
		return p.Live
	case <-time.After(time.Second):
		t.Fatal("no session.live")
		return nil
	}
}

func noLive(t *testing.T, ch <-chan json.RawMessage, within time.Duration) {
	t.Helper()
	select {
	case raw := <-ch:
		t.Fatalf("unexpected session.live %s", raw)
	case <-time.After(within):
	}
}

func setAgentStatus(n *Node, id, status string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	a := n.agents[id]
	a.AgentStatus = status
	n.agents[id] = a
}

// A working Claude's line goes out when it changes (not when only the read time does), and null once it stops.
func TestWatchLivePushesChangesAndEnds(t *testing.T) {
	n, f, pushes := liveNode(t, workingClaude(), boxed("✻ Frosting… (3s · ↓ 1.2k tokens)"))
	stop := startLive(t, n)
	if l := nextLive(t, pushes); l == nil || l.ElapsedS != 3 || l.TokensDown != 1200 || l.At == "" {
		t.Fatalf("first: %+v", l)
	}
	noLive(t, pushes, 150*time.Millisecond) // read again and again, the same line
	f.setScreen("w1:p1", boxed("✻ Frosting… (4s · ↓ 1.3k tokens · thinking)"))
	if l := nextLive(t, pushes); l == nil || l.ElapsedS != 4 || !l.Thinking {
		t.Fatalf("changed: %+v", l)
	}
	setAgentStatus(n, sidA, proto.StatusIdle)
	if l := nextLive(t, pushes); l != nil {
		t.Fatalf("stopped: %+v, want null", l)
	}
	noLive(t, pushes, 100*time.Millisecond)
	stop()
	noLive(t, pushes, 50*time.Millisecond) // null was sent already
}

// An early generic retry becomes detailed when Claude knows the error. Later attempt numbers go out even though the
// retry has no elapsed-time field to change.
func TestWatchLivePublishesGenericThenDetailedRetryAttempts(t *testing.T) {
	n, f, pushes := liveNode(t, workingClaude(), boxed("✻ API Error · retrying in 1s · attempt 1/10"))
	stop := startLive(t, n)
	defer stop()
	wantRetry := func(message string, attempt int) {
		t.Helper()
		l := nextLive(t, pushes)
		if l == nil {
			t.Fatal("want retry, got null")
		}
		got, at := *l, l.At
		got.At = ""
		want := proto.SessionLive{RetryError: message, RetryAttempt: attempt, RetryMax: 10}
		if at == "" || got != want {
			t.Fatalf("retry: got %+v at %q, want %+v", got, at, want)
		}
	}
	wantRetry("API Error", 1)
	for _, attempt := range []int{5, 6, 7} {
		f.setScreen("w1:p1", boxed("✻ 429 Synthetic rate limit for retry capture · retrying in 4s · attempt "+strconv.Itoa(attempt)+"/10"))
		wantRetry("429 Synthetic rate limit for retry capture", attempt)
	}
}

// Ordinary work replaces the retry instead of retaining its error or counts.
func TestWatchLiveNormalWorkReplacesRetry(t *testing.T) {
	n, f, pushes := liveNode(t, workingClaude(), boxed("✻ 500 Synthetic upstream failure for retry capture · retrying in 5s · attempt 4/10"))
	stop := startLive(t, n)
	defer stop()
	if l := nextLive(t, pushes); l == nil || l.RetryError != "500 Synthetic upstream failure for retry capture" {
		t.Fatalf("retry: %+v", l)
	}
	f.setScreen("w1:p1", boxed("✻ Frosting… (8s · ↓ 1.2k tokens)"))
	l := nextLive(t, pushes)
	if l == nil {
		t.Fatal("want ordinary work, got null")
	}
	got := *l
	got.At = ""
	want := proto.SessionLive{Verb: "Frosting", ElapsedS: 8, TokensDown: 1200}
	if got != want {
		t.Fatalf("ordinary work: got %+v, want %+v", got, want)
	}
}

// A retry keeps the last ordinary metrics even though its banner has no counters of its own.
func TestWatchLiveRetryKeepsOrdinaryMetrics(t *testing.T) {
	n, f, pushes := liveNode(t, workingClaude(), boxed("✻ Frosting… (16s · ↓ 22.8k tokens · thought for 3s · thinking)"))
	stop := startLive(t, n)
	defer stop()
	if l := nextLive(t, pushes); l == nil || l.TokensDown != 22800 || !l.Thinking {
		t.Fatalf("ordinary: %+v", l)
	}
	f.setScreen("w1:p1", boxed("✻ 429 Synthetic rate limit for retry capture · retrying in 30s · attempt 5/10"))
	l := nextLive(t, pushes)
	if l == nil {
		t.Fatal("want retry, got null")
	}
	got := *l
	got.At = ""
	want := proto.SessionLive{Verb: "Frosting", ElapsedS: 16, TokensDown: 22800, ThoughtS: 3,
		RetryError: "429 Synthetic rate limit for retry capture", RetryAttempt: 5, RetryMax: 10}
	if got != want {
		t.Fatalf("retry metrics: got %+v, want %+v", got, want)
	}
}

// Interrupting the retry, ending its turn, and ending the watch all clear a retry that was published.
func TestWatchLiveClearsRetry(t *testing.T) {
	for _, how := range []string{"line gone", "turn end", "unwatch"} {
		t.Run(how, func(t *testing.T) {
			n, f, pushes := liveNode(t, workingClaude(), boxed("✻ 429 Synthetic rate limit for retry capture · retrying in 4s · attempt 5/10"))
			stop := startLive(t, n)
			defer stop()
			if l := nextLive(t, pushes); l == nil || l.RetryError == "" {
				t.Fatalf("retry: %+v", l)
			}
			switch how {
			case "line gone":
				f.setScreen("w1:p1", boxed("⏺ Done."))
			case "turn end":
				setAgentStatus(n, sidA, proto.StatusIdle)
			case "unwatch":
				stop()
			}
			if l := nextLive(t, pushes); l != nil {
				t.Fatalf("%s: %+v, want null", how, l)
			}
		})
	}
}

// Retry support keeps the existing watched-session read: one-second cadence, a three-second read timeout, and 60
// visible lines. It needs no other screen-reading loop.
func TestWatchLiveRetryKeepsReadBounds(t *testing.T) {
	if liveEvery != time.Second || liveReadTimeout != 3*time.Second || liveLines != 60 {
		t.Fatalf("live bounds: every %v, timeout %v, lines %d", liveEvery, liveReadTimeout, liveLines)
	}
	n, f, pushes := liveNode(t, workingClaude(), boxed("✻ API Error · retrying in 1s · attempt 1/10"))
	f.reset()
	stop := startLive(t, n)
	defer stop()
	if l := nextLive(t, pushes); l == nil {
		t.Fatal("want retry, got null")
	}
	stop()
	reads := 0
	for _, c := range f.calls() {
		if c.Method != "pane.read" {
			continue
		}
		reads++
		if c.Params["pane_id"] != "w1:p1" || c.Params["source"] != "visible" || c.Params["lines"] != float64(60) {
			t.Fatalf("unbounded live read: %s", c.String())
		}
	}
	if reads == 0 {
		t.Fatal("retry did not use the watched-session screen read")
	}
}

// The line going away is null; a watch that ends with a line out ends it with null.
func TestWatchLiveNullWhenLineGoesAndWatchEnds(t *testing.T) {
	n, f, pushes := liveNode(t, workingClaude(), boxed("✻ Frosting… (3s)"))
	stop := startLive(t, n)
	if nextLive(t, pushes) == nil {
		t.Fatal("want the line")
	}
	f.setScreen("w1:p1", boxed("⏺ Done."))
	if l := nextLive(t, pushes); l != nil {
		t.Fatalf("line gone: %+v", l)
	}
	f.setScreen("w1:p1", boxed("✻ Frosting… (5s)"))
	if nextLive(t, pushes) == nil {
		t.Fatal("want the line back")
	}
	stop()
	if l := nextLive(t, pushes); l != nil {
		t.Fatalf("watch ended: %+v, want null", l)
	}
}

// Only a Claude in herdr that is working is read: no read and no push for the others.
func TestWatchLiveReadsOnlyWorkingClaude(t *testing.T) {
	for name, agent := range map[string]map[string]any{
		"codex":       agentOf("codex", sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", "/tmp/s", ""),
		"idle claude": claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/s", ""),
	} {
		t.Run(name, func(t *testing.T) {
			n, f, pushes := liveNode(t, agent, boxed("✻ Frosting… (3s)"))
			stop := startLive(t, n)
			time.Sleep(150 * time.Millisecond)
			stop()
			if reads := f.trace("pane.read"); len(reads) != 0 {
				t.Fatalf("read the pane: %v", reads)
			}
			noLive(t, pushes, 50*time.Millisecond)
		})
	}
}

// A pane that cannot be read keeps what was sent and says nothing until it can.
func TestWatchLiveReadFailureKeepsLast(t *testing.T) {
	n, f, pushes := liveNode(t, workingClaude(), boxed("✻ Frosting… (3s)"))
	startLive(t, n)
	if nextLive(t, pushes) == nil {
		t.Fatal("want the line")
	}
	f.handle("pane.read", func(map[string]any) any { return herdrFail{Code: "internal", Message: "boom"} })
	noLive(t, pushes, 150*time.Millisecond)
}

// session.watch starts the reads and session.unwatch ends them with null.
func TestWatchStartsAndEndsLive(t *testing.T) {
	n, _, pushes := liveNode(t, workingClaude(), boxed("✻ Frosting… (3s)"))
	writeTranscript(t, n.home, "/tmp/s", sidA, time.Now(), `{"type":"ai-title","aiTitle":"t"}`)
	if err := n.watch(sidA, 0); err != nil {
		t.Fatal(err)
	}
	if l := nextLive(t, pushes); l == nil || l.Verb != "Frosting" {
		t.Fatalf("watched: %+v", l)
	}
	n.unwatch(sidA)
	if l := nextLive(t, pushes); l != nil {
		t.Fatalf("unwatched: %+v, want null", l)
	}
}

// A working Claude in the Herdr app (herdr-core, plain-text screens) gets its status line too (#3904).
func TestWatchLiveOnHerdrCore(t *testing.T) {
	old := liveEvery
	liveEvery = 20 * time.Millisecond
	t.Cleanup(func() { liveEvery = old })
	n, _, hc, home := hcNode(t)
	writeTranscript(t, home, hcCwd, sidA, time.Now().Add(-time.Minute), `{"type":"custom-title","customTitle":"x"}`)
	writeClaudePID(t, home, 4242, sidA)
	hc.setTerminals(hcTerminal("t1", "claude-code", "working", 4242, "claude", hcCwd, "", "", ""))
	if m := byID(listSessions(t, n)); m[sidA].Status != proto.StatusWorking {
		t.Fatalf("not working: %+v", m[sidA])
	}
	hc.setScreen("t1", boxed("✻ Frosting… (7s · ↓ 2.1k tokens · thinking)"))
	all, pw := hubPushes(n)
	t.Cleanup(func() { pw.Close() })
	pushes := make(chan json.RawMessage, 64)
	go func() {
		for m := range all {
			if m.Event == "session.live" {
				pushes <- m.Params
			}
		}
	}()
	stop := startLive(t, n)
	defer stop()
	if l := nextLive(t, pushes); l == nil || l.Verb != "Frosting" || l.ElapsedS != 7 || l.TokensDown != 2100 || !l.Thinking {
		t.Fatalf("herdr-core line: %+v", l)
	}
}
