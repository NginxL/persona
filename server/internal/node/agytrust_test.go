package node

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// agyTrustScreen is agy's folder-trust dialog as the Antigravity CLI draws it in a folder it has not trusted (#11424).
const agyTrustScreen = "Accessing workspace:\n\n/work/example-project\n\nDo you trust the contents of this project?\n\n" +
	"Antigravity CLI requires permission to read, edit, and execute files here.\n\n> Yes, I trust this folder\n  No, exit\n\n" +
	"  ↑/↓ Navigate · enter Confirm\n                                                   auto · Example model · medium\n"

const (
	agyTestRule         = "────────────────────────────────────────"
	agyStatusLineScreen = agyTestRule + "\n>\n" + agyTestRule + "\nExample model │ Ctx 12%\n"
)

// withAgyTrustWait shortens how long a start watches for agy's trust dialog.
func withAgyTrustWait(t *testing.T, wait time.Duration) {
	t.Helper()
	oldWait, oldInit, oldPoll := agyTrustWait, agyInitWait, agyTrustPoll
	agyTrustWait, agyInitWait, agyTrustPoll = wait, wait, 10*time.Millisecond
	t.Cleanup(func() { agyTrustWait, agyInitWait, agyTrustPoll = oldWait, oldInit, oldPoll })
}

// isEnter: c is a send_keys whose keys include enter.
func isEnter(c herdrCall) bool {
	return c.Method == "pane.send_keys" && slices.ContainsFunc(keysOf(c), func(k string) bool { return strings.EqualFold(k, "enter") })
}

func TestAgyTrustFolderScreenReadiness(t *testing.T) {
	withAgyTrustWait(t, 5*time.Second)
	for _, tc := range []struct {
		name, frame string
		wantReads   int
	}{
		{"default footer", agyIdleScreen, 1},
		{"custom status line", agyStatusLineScreen, 1},
		{"custom status line with placeholder", agyTestRule + "\n> Auto mode (shift+tab to cycle)\n" + agyTestRule + "\nExample status\n", 1},
		{"empty status line", agyTestRule + "\n  >   \n" + agyTestRule + "  \n\n", 1},
		{"shell continuation", "~/work/example-project $ agy\n>\n", 2},
		{"shell command mentions hint", "$ echo '? for shortcuts'\n", 2},
		{"loading banner with rules", agyTestRule + "\nLoading Antigravity CLI…\n" + agyTestRule + "\n", 2},
		{"incomplete input box", agyTestRule + "\n>\n", 2},
		{"old input box above shell", agyStatusLineScreen + "$ agy\nLoading…\n", 2},
		{"dialog over ready input", agyTestRule + "\n>\n" + agyTestRule + "\nEnter to confirm · Esc to cancel\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			const pane = "w1:p2"
			f.reply("agent.get", agentWithStatus(agyAgent, proto.StatusIdle)(0, home))
			// A second read always sees a ready screen, so a missed or premature match changes the read count.
			f.screenSteps(pane, func(c herdrCall) bool { return c.Method == "pane.read" }, tc.frame, agyIdleScreen)
			if err := n.agyTrustFolder(t.Context(), pane); err != nil {
				t.Fatal(err)
			}
			if got := len(f.trace("pane.read")); got != tc.wantReads {
				t.Errorf("read the pane %d times, want %d", got, tc.wantReads)
			}
			if got := f.trace("pane.send_keys"); len(got) != 0 {
				t.Errorf("unexpected keys: %v", got)
			}
		})
	}
}

func TestAgyStartupErrorsPreemptReadiness(t *testing.T) {
	n, f, _ := herdrNode(t)
	const pane = "w1:p2"
	const diagnostic = `error: invalid model selection (--model "" --effort "max"): model has no "max" effort`
	f.setScreen(pane, diagnostic+"\n"+agyIdleScreen)
	if err := n.agyTrustFolder(t.Context(), pane); err == nil || err.Error() != diagnostic {
		t.Fatalf("readiness accepted an invalid selection: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, _, err := n.waitWorkAgent(ctx, pane, agyAgent, nil); err == nil || err.Error() != diagnostic {
		t.Fatalf("process discovery missed an exit before agy was detected: %v", err)
	}
}

func TestAgyClearOverlayWaitsForCustomStatusLine(t *testing.T) {
	withAgyTrustWait(t, 5*time.Second)
	n, f, _ := herdrNode(t)
	const pane = "w1:p2"
	// After Enter, the dialog can linger and briefly leave a loading screen before the input box appears.
	f.screenSteps(pane, func(c herdrCall) bool { return c.Method == "pane.read" },
		agyTrustScreen, "Loading Antigravity CLI…\n", agyStatusLineScreen, agyIdleScreen)
	if err := n.agyClearOverlay(t.Context(), pane, agyTrustScreen); err != nil {
		t.Fatal(err)
	}
	if got := len(f.trace("pane.read")); got != 3 {
		t.Errorf("read the pane %d times, want one read per frame through the custom status line", got)
	}
	calls := f.calls()
	if len(calls) == 0 || !isKeys("enter")(calls[0]) || len(f.trace("pane.send_keys")) != 1 {
		t.Fatalf("want one Enter before reading the pane, got:\n%s", strings.Join(f.trace(), "\n"))
	}
}

// The trust dialog is answered by trusting the folder the start chose, and only then does the first message go in.
func TestAgyStartTrustsTheFolderBeforeTheFirstMessage(t *testing.T) {
	withAgyTrustWait(t, 5*time.Second)
	x := newWorkFirstFixture(t, agyAgent, func(submissions int, home string) any {
		status := proto.StatusBlocked // herdr reads the dialog as blocked
		switch {
		case submissions >= 2: // the trust answer, then the first message
			status = proto.StatusWorking
		case submissions == 1:
			status = proto.StatusIdle
		}
		return agentWithStatus(agyAgent, status)(submissions, home)
	})
	x.f.screenSteps("w1:p2", isEnter, agyTrustScreen, agyStatusLineScreen)
	id := x.start(t, agyAgent)

	calls := x.f.trace("pane.send_keys", "agent.prompt")
	trusted, prompted := -1, -1
	for i, c := range calls {
		if trusted < 0 && strings.Contains(c, "pane.send_keys") && strings.Contains(strings.ToLower(c), "enter") {
			trusted = i
		}
		if prompted < 0 && strings.Contains(c, "agent.prompt") && strings.Contains(c, agyFirstMessage) {
			prompted = i
		}
	}
	if trusted < 0 || prompted < 0 || trusted > prompted {
		t.Fatalf("want the trust answer before the first message, got:\n%s", strings.Join(calls, "\n"))
	}
	if got := x.f.trace("pane.send_keys"); len(got) != 1 {
		t.Errorf("sent keys %d times, want once (the dialog answered once): %v", len(got), got)
	}
	if p, ok := x.n.lostFirst.get(id); ok {
		t.Errorf("listed as lost: %q", p.why)
	}
}

// With no dialog (a folder agy trusts already), nothing is typed besides the first message.
func TestAgyStartWithoutTrustDialogTypesNoKeys(t *testing.T) {
	withAgyTrustWait(t, 5*time.Second)
	x := newWorkFirstFixture(t, agyAgent, func(submissions int, home string) any {
		status := proto.StatusIdle
		if submissions >= 1 {
			status = proto.StatusWorking
		}
		return agentWithStatus(agyAgent, status)(submissions, home)
	})
	began := time.Now()
	x.start(t, agyAgent)
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("the start waited %s with agy's input box already up", took)
	}
	if got := x.f.trace("pane.send_keys"); len(got) != 0 {
		t.Errorf("keys sent with no dialog: %v", got)
	}
	if got := x.f.trace("agent.prompt"); len(got) != 1 {
		t.Errorf("prompted %d times, want 1", len(got))
	}
}

// A dialog that stays up (the answer did not take) answers once and does not hold the start past the bound.
func TestAgyStartTrustDialogThatStaysIsBounded(t *testing.T) {
	withAgyTrustWait(t, 300*time.Millisecond)
	x := newWorkFirstFixture(t, agyAgent, agentWithStatus(agyAgent, proto.StatusBlocked))
	x.f.setScreen("w1:p2", agyTrustScreen)
	began := time.Now()
	x.start(t, agyAgent)
	if took := time.Since(began); took > 4*time.Second {
		t.Errorf("the start took %s with the dialog up; the watch is bounded", took)
	}
	enters := 0
	for _, c := range x.f.calls() {
		if isEnter(c) {
			enters++
		}
	}
	// The trust answer once; the first-message check may press enter for its own retries, but the dialog is not
	// answered again while the same screen stays up.
	if enters < 1 {
		t.Errorf("the dialog was never answered")
	}
}

// agyTrustKeys picks "Yes, I trust this folder" wherever the cursor is, and answers no other dialog.
func TestAgyTrustKeys(t *testing.T) {
	moved := strings.Replace(strings.Replace(agyTrustScreen, "> Yes", "  Yes", 1), "  No, exit", "> No, exit", 1)
	other := strings.Replace(agyTrustScreen, "Do you trust the contents of this project?", "Run this command?", 1)
	for name, tc := range map[string]struct {
		screen string
		want   []string
	}{
		"cursor on yes":  {agyTrustScreen, []string{"enter"}},
		"cursor on no":   {moved, []string{"up", "enter"}},
		"another dialog": {other, nil},
		"input box":      {agyIdleScreen, nil},
	} {
		if got := agyTrustKeys(tc.screen); !slices.Equal(got, tc.want) {
			t.Errorf("%s: keys %v, want %v", name, got, tc.want)
		}
	}
}

// herdr reports agy by its process, and calls it idle, before agy's screen takes the terminal: the first message waits
// for agy's input box (#11437), or the terminal echoes it and agy drops it.
func TestAgyStartWaitsForAgyScreenBeforeTheFirstMessage(t *testing.T) {
	withAgyTrustWait(t, 5*time.Second)
	x := newWorkFirstFixture(t, agyAgent, func(submissions int, home string) any {
		status := proto.StatusIdle
		if submissions >= 1 {
			status = proto.StatusWorking
		}
		return agentWithStatus(agyAgent, status)(submissions, home)
	})
	const shell = "~/work/example-project ❯ . ~/.persona/agent-start-example.sh\n"
	x.f.setScreen("w1:p2", shell)
	var drawn atomic.Bool
	var reads atomic.Int32
	x.f.after(func(c herdrCall) {
		if c.Method != "pane.read" || drawn.Load() {
			return
		}
		if reads.Add(1) == 4 { // agy draws its screen a moment after herdr saw its process
			x.f.setScreen("w1:p2", agyIdleScreen)
			drawn.Store(true)
		}
	})
	var early atomic.Bool
	x.f.after(func(c herdrCall) {
		if c.Method == "agent.prompt" && !drawn.Load() {
			early.Store(true)
		}
	})
	x.start(t, agyAgent)
	if early.Load() {
		t.Fatal("the first message was typed while the pane still showed the shell")
	}
	if got := x.f.trace("agent.prompt"); len(got) != 1 {
		t.Errorf("prompted %d times, want 1", len(got))
	}
	if got := x.f.trace("pane.send_keys"); len(got) != 0 {
		t.Errorf("keys sent with no dialog: %v", got)
	}
}

// agyNoticeScreen is agy's input box under its start notice, which swallows typed input until Esc (#11437).
const agyNoticeScreen = " Antigravity CLI\n\n ┃ Auto is now the default agent mode\n ┃ Press Shift+Tab to switch agent modes, or change Agent Mode in /config.\n" +
	" ┃ esc to dismiss\n────────\n> Auto mode: edits & safe commands auto-approved (shift+tab to cycle)\n────────\n? for shortcuts\n"

// The start notice is dismissed with one Esc before the first message goes in.
func TestAgyStartDismissesTheNoticeBeforeTheFirstMessage(t *testing.T) {
	withAgyTrustWait(t, 5*time.Second)
	x := newWorkFirstFixture(t, agyAgent, func(submissions int, home string) any {
		status := proto.StatusIdle
		if submissions >= 1 {
			status = proto.StatusWorking
		}
		return agentWithStatus(agyAgent, status)(submissions, home)
	})
	x.f.screenSteps("w1:p2", func(c herdrCall) bool {
		return c.Method == "pane.send_keys" && slices.Contains(keysOf(c), "escape")
	}, agyNoticeScreen, agyIdleScreen)
	x.start(t, agyAgent)
	calls := x.f.trace("pane.send_keys", "agent.prompt")
	if len(calls) != 2 || !strings.Contains(calls[0], `"escape"`) || !strings.Contains(calls[1], "agent.prompt") {
		t.Fatalf("want one Esc, then the first message:\n%s", strings.Join(calls, "\n"))
	}
}

// The input box can appear while herdr still reports agy as unknown. A late notice is dismissed during this window,
// and the first message waits for agy to settle (#11345).
func TestAgyStartWaitsForUnknownStatusToSettle(t *testing.T) {
	for _, tc := range []struct{ name, ready, notice string }{
		{"default footer", agyIdleScreen, agyNoticeScreen},
		{"custom status line", agyStatusLineScreen, strings.Replace(agyNoticeScreen, "? for shortcuts", "Example model │ Ctx 12%", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withAgyTrustWait(t, 5*time.Second)
			var polls atomic.Int32
			var dismissed, settled, early atomic.Bool
			x := newWorkFirstFixture(t, agyAgent, func(submissions int, home string) any {
				status := proto.StatusUnknown
				switch {
				case submissions >= 1:
					status = proto.StatusWorking
				case settled.Load():
					status = proto.StatusIdle
				}
				return agentWithStatus(agyAgent, status)(submissions, home)
			})
			const pane = "w1:p2"
			x.f.setScreen(pane, tc.ready)
			x.f.after(func(c herdrCall) {
				switch c.Method {
				case "agent.get":
					switch polls.Add(1) {
					case 2:
						x.f.setScreen(pane, tc.notice)
					case 3:
						// The third reply still says unknown, even if the notice has been dismissed.
						settled.Store(dismissed.Load())
					}
				case "pane.send_keys":
					if slices.Contains(keysOf(c), "escape") {
						x.f.setScreen(pane, tc.ready)
						dismissed.Store(true)
					}
				case "agent.prompt":
					if !settled.Load() || polls.Load() < 4 {
						early.Store(true)
					}
				}
			})
			id := x.start(t, agyAgent)
			if early.Load() {
				t.Fatal("first message sent before herdr reported agy had settled")
			}
			calls := x.f.trace("pane.send_keys", "agent.prompt")
			if len(calls) != 2 || !strings.Contains(calls[0], `"escape"`) || !strings.Contains(calls[1], "agent.prompt") {
				t.Fatalf("want Esc for late notice then first message, got:\n%s", strings.Join(calls, "\n"))
			}
			if p, ok := x.n.lostFirst.get(id); ok {
				t.Errorf("listed as lost: %q", p.why)
			}
		})
	}
}

// Waiting for status unknown to clear is bounded by agyInitWait so a host that never reports idle does not stall starts.
func TestAgyStartUnknownStatusWaitIsBounded(t *testing.T) {
	withAgyTrustWait(t, 5*time.Second)
	agyInitWait = 120 * time.Millisecond
	x := newWorkFirstFixture(t, agyAgent, func(submissions int, home string) any {
		if submissions >= 1 {
			return agentWithStatus(agyAgent, proto.StatusWorking)(submissions, home)
		}
		return agentWithStatus(agyAgent, proto.StatusUnknown)(submissions, home)
	})
	began := time.Now()
	x.start(t, agyAgent)
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("start waited %s on permanent unknown status; want bounded by agyInitWait", took)
	}
	if got := x.f.trace("agent.prompt"); len(got) != 1 {
		t.Errorf("prompted %d times, want 1", len(got))
	}
}

// A message typed into an agy or Cursor pane keeps its row working until herdr shows the turn, for workSentGrace.
func TestWorkAgentStatusAfterASend(t *testing.T) {
	n, _, _ := herdrNode(t)
	now := time.Now()
	idle := host.Agent{Agent: agyAgent, PaneID: "w1:p1", AgentStatus: proto.StatusIdle}
	if got := n.liveStatus("agy-w1-p1", idle, now); got != proto.StatusIdle {
		t.Fatalf("before a send: %q", got)
	}
	n.workSent.mark("w1:p1", now)
	if got := n.liveStatus("agy-w1-p1", idle, now.Add(time.Second)); got != proto.StatusWorking {
		t.Errorf("right after a send: %q, want working", got)
	}
	blocked := idle
	blocked.AgentStatus = proto.StatusBlocked
	if got := n.liveStatus("agy-w1-p1", blocked, now.Add(time.Second)); got != proto.StatusBlocked {
		t.Errorf("blocked right after a send: %q, want blocked", got)
	}
	if got := n.liveStatus("agy-w1-p1", idle, now.Add(workSentGrace+time.Second)); got != proto.StatusIdle {
		t.Errorf("after the grace: %q, want idle", got)
	}
	other := host.Agent{Agent: "claude", PaneID: "w1:p2", AgentStatus: proto.StatusIdle}
	n.workSent.mark("w1:p2", now)
	if got := n.workAgentStatus(other, now); got != proto.StatusWorking {
		t.Errorf("workAgentStatus itself: %q", got)
	}
}

// session.send to an agy pane marks it, so the row the hub lists reads working right after (#11437).
func TestAgySendReadsWorkingUntilHerdrShowsTheTurn(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, ""))
	f.reply("agent.prompt", map[string]any{})
	if _, err := request(t, n, "session.send", map[string]any{"session_id": agyTestRow, "text": "count to 300"}); err != nil {
		t.Fatal(err)
	}
	a, ok := n.agent(agyTestRow)
	if !ok {
		t.Fatal("no agy row")
	}
	if got := n.liveStatus(agyTestRow, a, time.Now()); got != proto.StatusWorking {
		t.Errorf("right after the send: %q, want working", got)
	}
}
