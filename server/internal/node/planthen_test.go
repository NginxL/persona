package node

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestPlanThenCardApprovalUsesRecordedMode(t *testing.T) {
	for _, tc := range []struct {
		name, recorded, clientMode, want string
	}{
		{"recorded edits beats an older client's bypass", "acceptEdits", proto.ModeBypass, "acceptEdits"},
		{"recorded bypass beats an older client's edits", proto.ModeBypass, "acceptEdits", proto.ModeBypass},
		{"unknown recorded mode keeps the client's choice", "futureMode", "default", "default"},
		{"recorded plan keeps the client's choice", "plan", "acceptEdits", "acceptEdits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			n.state.put(decidedSession, sessionState{PlanThen: tc.recorded})
			fh := withFakeHub(t, n)
			fh.answer = func(ix proto.Interaction) proto.Resolution {
				if ix.Kind != proto.IxPlan || ix.ToolName != "ExitPlanMode" {
					t.Errorf("plan approval interaction = %+v", ix)
				}
				if ix.PlanThen != tc.recorded {
					t.Errorf("plan_then on the wire = %q, want %q", ix.PlanThen, tc.recorded)
				}
				approve := true
				return proto.Resolution{Approve: &approve, Mode: tc.clientMode}
			}
			out, _ := runPermissionHook(t, n, hookInput{HookEventName: "PermissionRequest", SessionID: decidedSession,
				PermissionMode: "plan", ToolName: "ExitPlanMode", ToolInput: json.RawMessage(`{"plan":"Ship the fix."}`)})
			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Output struct {
					Event    string `json:"hookEventName"`
					Decision struct {
						Behavior     string `json:"behavior"`
						UpdatedInput struct {
							Plan string `json:"plan"`
						} `json:"updatedInput"`
						UpdatedPermissions []struct {
							Type, Mode, Destination string
						} `json:"updatedPermissions"`
					} `json:"decision"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			d := response.Output.Decision
			if response.Output.Event != "PermissionRequest" || d.Behavior != "allow" || d.UpdatedInput.Plan != "Ship the fix." {
				t.Fatalf("plan approval output = %s", raw)
			}
			if len(d.UpdatedPermissions) != 1 {
				t.Fatalf("updatedPermissions = %+v, want one recorded mode update", d.UpdatedPermissions)
			}
			update := d.UpdatedPermissions[0]
			if update.Type != "setMode" || update.Mode != tc.want || update.Destination != "session" {
				t.Fatalf("mode update = %+v, want setMode %q for the session; client answered %q", update, tc.want, tc.clientMode)
			}
		})
	}
}

func TestPlanThenOfAndFlags(t *testing.T) {
	for _, tc := range []struct{ mode, then, want, flags string }{
		{"plan", proto.ModeBypass, proto.ModeBypass, " --allow-dangerously-skip-permissions"},
		{"plan", "acceptEdits", "acceptEdits", ""},
		{"plan", "", "", ""},
		{"plan", "plan", "", ""},
		{"plan", "made-up", "", ""},
		{"default", proto.ModeBypass, "", ""}, // only a start in plan mode has a plan to follow
		{"", proto.ModeBypass, "", ""},
	} {
		if got := planThenOf(tc.mode, tc.then); got != tc.want {
			t.Errorf("planThenOf(%q, %q) = %q, want %q", tc.mode, tc.then, got, tc.want)
		}
		if got := planThenFlags(tc.mode, tc.then); got != tc.flags {
			t.Errorf("planThenFlags(%q, %q) = %q, want %q", tc.mode, tc.then, got, tc.flags)
		}
	}
	// What the start adds is what puts bypass in the running session's cycle.
	args := strings.Fields("claude --session-id x" + modeFlags("plan", "") + planThenFlags("plan", proto.ModeBypass))
	if !argsCanBypass(args) {
		t.Fatalf("a plan start that goes on in bypass cannot reach it: %v", args)
	}
	if argsCanBypass(strings.Fields("claude --session-id x" + modeFlags("plan", "") + planThenFlags("plan", "auto"))) {
		t.Fatal("a plan start that goes on in auto was given bypass")
	}
}

func fastPlanThen(t *testing.T) {
	t.Helper()
	watch, every := planThenWatch, planThenEvery
	planThenWatch, planThenEvery = 400*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { planThenWatch, planThenEvery = watch, every })
}

// A plan approved in the terminal leaves the session in the mode its dialog picked; the node moves it to the mode
// its start recorded.
func TestPlanThenAfterTerminalMovesToTheRecordedMode(t *testing.T) {
	fastPlanThen(t)
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/m", ""))
	n.state.put(sidA, sessionState{Pane: "w1:p1", Launcher: "claude", Cwd: "/tmp/m", PlanThen: "acceptEdits"})
	cycleModes(t, f, "w1:p1", "auto")
	f.reset()
	n.planAfterTerminal(sidA, nil)
	waitWithin(t, 3*time.Second, "the recorded mode", func() bool {
		mode, _ := n.modesOf(sidA)
		return mode == "acceptEdits"
	})
	if got := strings.Join(f.trace(), "\n"); !strings.Contains(got, sendKeys("w1:p1", "shift+tab")) {
		t.Fatalf("no key pressed: %v", f.trace())
	}
}

// A plan sent back keeps the footer on plan, and a session whose start recorded no mode is not followed: no key.
func TestPlanThenAfterTerminalLeavesAlone(t *testing.T) {
	fastPlanThen(t)
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f,
		claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/m", ""),
		claudeAgent(sidB, proto.StatusIdle, "w1:p2", "w1:t2", "w1", "/tmp/m", ""))
	n.state.put(sidA, sessionState{Pane: "w1:p1", Launcher: "claude", Cwd: "/tmp/m", PlanThen: "acceptEdits"})
	n.state.put(sidB, sessionState{Pane: "w1:p2", Launcher: "claude", Cwd: "/tmp/m"})
	cycleModes(t, f, "w1:p1", "plan")
	cycleModes(t, f, "w1:p2", "auto")
	f.reset()
	n.planAfterTerminal(sidA, nil)
	n.planAfterTerminal(sidB, nil)
	// Observe the full watch window before asserting that no key was sent.
	timer := time.NewTimer(planThenWatch + 200*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	for _, c := range f.trace() {
		if strings.Contains(c, "shift+tab") {
			t.Fatalf("a key was pressed: %v", f.trace())
		}
	}
}

// A card answer ends the watch: the hook's answer set the mode, nothing is pressed afterwards.
func TestPlanThenWatchStopsWhenTheCardAnswers(t *testing.T) {
	fastPlanThen(t)
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/m", ""))
	n.state.put(sidA, sessionState{Pane: "w1:p1", Launcher: "claude", Cwd: "/tmp/m", PlanThen: "acceptEdits"})
	cycleModes(t, f, "w1:p1", "plan")
	n.planAfterTerminal(sidA, nil)() // answered at once
	cycleModes(t, f, "w1:p1", "auto")
	f.reset()
	// Observe the full watch window before asserting that no key was sent.
	timer := time.NewTimer(planThenWatch + 100*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	for _, c := range f.trace() {
		if strings.Contains(c, "shift+tab") {
			t.Fatalf("a key was pressed after the card answered: %v", f.trace())
		}
	}
}

// The footer coming back means the terminal decided the plan: the waiting card is told once, whether the plan was
// approved (another mode) or sent back (still plan), and for a session with no recorded mode too.
func TestPlanAfterTerminalWithdrawsTheCard(t *testing.T) {
	fastPlanThen(t)
	for name, footer := range map[string]string{"approved": "auto", "sent back": "plan"} {
		n, f, _ := herdrNode(t)
		seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/m", ""))
		n.state.put(sidA, sessionState{Pane: "w1:p1", Launcher: "claude", Cwd: "/tmp/m"}) // no recorded mode
		f.setScreen("w1:p1", screenFixture(t, "claude-2.1.312-trust-162.txt"))            // a dialog: no footer
		var told atomic.Int32
		f.reset()
		stop := n.planAfterTerminal(sidA, func() { told.Add(1) })
		waitWithin(t, 3*time.Second, "a few looks at the dialog", func() bool { return len(f.trace()) >= 4 })
		if told.Load() != 0 {
			t.Fatalf("%s: told while the dialog is up", name)
		}
		cycleModes(t, f, "w1:p1", footer)
		waitWithin(t, 3*time.Second, "the card to be told", func() bool { return told.Load() > 0 })
		stop()
		if told.Load() != 1 {
			t.Fatalf("%s: the card was told %d times, want 1", name, told.Load())
		}
	}
}
