package node

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// agyTrustScreen is agy's folder-trust dialog as the Antigravity CLI draws it in a folder it has not trusted (#11424).
const agyTrustScreen = "Accessing workspace:\n\n/work/example-project\n\nDo you trust the contents of this project?\n\n" +
	"Antigravity CLI requires permission to read, edit, and execute files here.\n\n> Yes, I trust this folder\n  No, exit\n\n" +
	"  ↑/↓ Navigate · enter Confirm\n                                                   auto · Example model · medium\n"

// withAgyTrustWait shortens how long a start watches for agy's trust dialog.
func withAgyTrustWait(t *testing.T, wait time.Duration) {
	t.Helper()
	oldWait, oldPoll := agyTrustWait, agyTrustPoll
	agyTrustWait, agyTrustPoll = wait, 10*time.Millisecond
	t.Cleanup(func() { agyTrustWait, agyTrustPoll = oldWait, oldPoll })
}

// isEnter: c is a send_keys whose keys include enter.
func isEnter(c herdrCall) bool {
	return c.Method == "pane.send_keys" && slices.ContainsFunc(keysOf(c), func(k string) bool { return strings.EqualFold(k, "enter") })
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
	x.f.screenSteps("w1:p2", isEnter, agyTrustScreen, agyIdleScreen)
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
