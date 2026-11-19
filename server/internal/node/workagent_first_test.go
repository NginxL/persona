package node

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// agyIdleScreen is an agy at rest with an empty input box: what a start that lost its first message left (#11345).
const agyIdleScreen = " Antigravity CLI\n╭──────────────────────────────────────╮\n│ >                                    │\n╰──────────────────────────────────────╯\n ? for shortcuts\n"

const agyFirstMessage = "Review the build script and list what breaks on Windows."

// withWorkFirstWaits shortens how long a work agent's first message is watched. Call it before herdrNode, so the
// node's cleanup (which waits for the check) runs before the waits are restored.
func withWorkFirstWaits(t *testing.T, wait time.Duration) {
	t.Helper()
	oldWait, oldPoll := workFirstWait, workFirstPoll
	workFirstWait, workFirstPoll = wait, 10*time.Millisecond
	t.Cleanup(func() { workFirstWait, workFirstPoll = oldWait, oldPoll })
}

// workFirstFixture starts an agy in a fake herdr whose agent.get answers status(submissions so far): a submission is
// an agent.prompt, or on Windows the Enter after the paste (hostherdr.go).
type workFirstFixture struct {
	n           *Node
	f           *fakeHerdr
	home        string
	submissions atomic.Int32
}

func newWorkFirstFixture(t *testing.T, kind string, agent func(submissions int, home string) any) *workFirstFixture {
	t.Helper()
	withWorkFirstWaits(t, 150*time.Millisecond)
	x := &workFirstFixture{}
	x.n, x.f, x.home = herdrNode(t)
	x.f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	x.f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	x.f.reply("agent.prompt", map[string]any{})
	x.f.setScreen("w1:p2", agyIdleScreen)
	x.f.handle("agent.get", func(map[string]any) any { return agent(int(x.submissions.Load()), x.home) })
	_, program, _ := workAgent(kind)
	x.f.after(func(c herdrCall) {
		switch {
		// The start line: the command itself, or (a PowerShell pane on Windows) the line that runs its start file.
		case c.Method == "pane.send_input" && (strings.Contains(c.String(), program) || strings.Contains(c.String(), startScriptPrefix)):
			x.f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf(kind, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", x.home, kind)}, nil))
		case c.Method == "agent.prompt",
			c.Method == "pane.send_keys" && slices.ContainsFunc(keysOf(c), func(k string) bool { return strings.EqualFold(k, "enter") }):
			x.submissions.Add(1)
		}
	})
	return x
}

// start runs the start and waits for its first-message check to finish; it answers the session's id.
func (x *workFirstFixture) start(t *testing.T, kind string) string {
	t.Helper()
	began := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := x.n.startWorkAgent(ctx, x.n.hosts.Get(host.Herdr).(*herdrHost), x.home, kind, kind, "", agyFirstMessage, "w1", "")
	if err != nil {
		t.Fatalf("start: %v\nherdr calls:\n%s", err, strings.Join(x.f.trace(), "\n"))
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("the start waited %s for the check; it answers first", took)
	}
	x.n.firstWait.Wait()
	return s.SessionID
}

func agentWithStatus(kind, status string) func(int, string) any {
	return func(_ int, home string) any {
		return map[string]any{"agent": agentOf(kind, "", status, "w1:p2", "w1:t2", "w1", home, kind)}
	}
}

// agy took nothing and its box is empty: the first message is typed again, and taken then.
func TestWorkAgentFirstMessageTypedAgainWhenDropped(t *testing.T) {
	for _, kind := range []string{agyAgent, cursorAgent} {
		t.Run(kind, func(t *testing.T) {
			x := newWorkFirstFixture(t, kind, func(submissions int, home string) any {
				status := proto.StatusIdle
				if submissions >= 2 {
					status = proto.StatusWorking
				}
				return map[string]any{"agent": agentOf(kind, "", status, "w1:p2", "w1:t2", "w1", home, kind)}
			})
			id := x.start(t, kind)
			if got := x.submissions.Load(); got != 2 {
				t.Errorf("submitted %d times, want 2 (the dropped one and once more)", got)
			}
			if p, ok := x.n.lostFirst.get(id); ok {
				t.Errorf("listed as lost after it was taken: %q", p.why)
			}
		})
	}
}

// Taken on the first try (herdr says working, or herdr's Antigravity integration reports the conversation, which its
// hook does only after a submission): never typed again.
func TestWorkAgentFirstMessageTakenIsNotTypedAgain(t *testing.T) {
	conversation := func(submissions int, home string) any {
		a := agentOf(agyAgent, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, agyAgent)
		if submissions >= 1 {
			a["agent_session"] = map[string]any{"source": "herdr:antigravity_cli", "agent": agyAgent, "kind": "id",
				"value": "0b6f6f5e-3c55-4a43-9a52-1f3b2c9d7e10"}
		}
		return map[string]any{"agent": a}
	}
	afterFirst := func(status string) func(int, string) any {
		return func(submissions int, home string) any {
			if submissions >= 1 {
				return agentWithStatus(agyAgent, status)(submissions, home)
			}
			return agentWithStatus(agyAgent, proto.StatusIdle)(submissions, home)
		}
	}
	for name, agent := range map[string]func(int, string) any{
		"working":                 afterFirst(proto.StatusWorking),
		"blocked":                 afterFirst(proto.StatusBlocked),
		"done":                    afterFirst(proto.StatusDone),
		"conversation reported":   conversation,
		"left the pane meanwhile": func(submissions int, home string) any { return leftAfterFirst(submissions, home) },
	} {
		t.Run(name, func(t *testing.T) {
			x := newWorkFirstFixture(t, agyAgent, agent)
			id := x.start(t, agyAgent)
			if got := x.submissions.Load(); got != 1 {
				t.Errorf("submitted %d times, want 1", got)
			}
			if p, ok := x.n.lostFirst.get(id); ok {
				t.Errorf("listed as lost: %q", p.why)
			}
		})
	}
}

// leftAfterFirst is an agy that exits once its first message was submitted: herdr no longer finds an agent there.
func leftAfterFirst(submissions int, home string) any {
	if submissions >= 1 {
		return herdrFail{Code: "agent_not_found", Message: "no agent in pane"}
	}
	return agentWithStatus(agyAgent, proto.StatusIdle)(submissions, home)
}

// Not taken, but the screen shows the message (still in the box, wrapped in its border) or agy's chip for a long
// paste: typing it again would add a second copy to that draft. The session is listed as needing the user instead.
func TestWorkAgentFirstMessageOnScreenIsNotTypedAgain(t *testing.T) {
	for name, screen := range map[string]string{
		"in the box, wrapped": " Antigravity CLI\n╭──────────────────────────────╮\n│ > Review the build script and │\n│ list what breaks on Windows.  │\n╰──────────────────────────────╯\n ? for shortcuts\n",
		"paste chip":          " Antigravity CLI\n╭──────────────────────────────╮\n│ > [Pasted text #1 +12 lines]  │\n╰──────────────────────────────╯\n ? for shortcuts\n",
	} {
		t.Run(name, func(t *testing.T) {
			x := newWorkFirstFixture(t, agyAgent, agentWithStatus(agyAgent, proto.StatusIdle))
			x.f.setScreen("w1:p2", screen)
			id := x.start(t, agyAgent)
			if got := x.submissions.Load(); got != 1 {
				t.Errorf("submitted %d times, want 1", got)
			}
			if p, ok := x.n.lostFirst.get(id); !ok || p.why != lostTimeout {
				t.Errorf("lost reason %q (listed %v), want %q", p.why, ok, lostTimeout)
			}
		})
	}
}

// herdr cannot say agy is idle (unknown): nothing proves the message was dropped, so it is not typed again.
func TestWorkAgentFirstMessageUnknownStatusIsNotTypedAgain(t *testing.T) {
	x := newWorkFirstFixture(t, agyAgent, func(submissions int, home string) any {
		if submissions >= 1 {
			return agentWithStatus(agyAgent, proto.StatusUnknown)(submissions, home)
		}
		return agentWithStatus(agyAgent, proto.StatusIdle)(submissions, home)
	})
	id := x.start(t, agyAgent)
	if got := x.submissions.Load(); got != 1 {
		t.Errorf("submitted %d times, want 1", got)
	}
	if p, ok := x.n.lostFirst.get(id); !ok || p.why != lostTimeout {
		t.Errorf("lost reason %q (listed %v), want %q", p.why, ok, lostTimeout)
	}
}

// agy never takes it: typed firstPromptTries times in all, then the session needs the user, and sessions.list says
// so instead of idle. A message sent to it afterwards clears that.
func TestWorkAgentFirstMessageGivesUpAfterTries(t *testing.T) {
	x := newWorkFirstFixture(t, agyAgent, agentWithStatus(agyAgent, proto.StatusIdle))
	id := x.start(t, agyAgent)
	if got := x.submissions.Load(); got != firstPromptTries {
		t.Errorf("submitted %d times, want %d", got, firstPromptTries)
	}
	want := fmt.Sprintf(lostTries, firstPromptTries)
	if p, ok := x.n.lostFirst.get(id); !ok || p.why != want {
		t.Fatalf("lost reason %q (listed %v), want %q", p.why, ok, want)
	}
	x.f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf(agyAgent, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", x.home, agyAgent)}, nil))
	var row *proto.Session
	for _, s := range listSessions(t, x.n) {
		if s.SessionID == id {
			row = &s
		}
	}
	if row == nil || row.Status != proto.StatusBlocked || row.AttentionReason != want {
		t.Fatalf("listed as %+v, want blocked with %q", row, want)
	}
	a, _ := x.n.agent(id)
	params, _ := json.Marshal(map[string]any{"session_id": id, "text": "Please start on the task above."})
	if _, err, handled := x.n.workAgentCalls(context.Background(), "session.send", id, a, params); !handled || err != nil {
		t.Fatalf("send: handled %v, %v", handled, err)
	}
	if p, ok := x.n.lostFirst.get(id); ok {
		t.Errorf("still listed as lost after a message went in: %q", p.why)
	}
}

func TestPromptShown(t *testing.T) {
	long := "请按 docs/review.md 里的要求做一份独立评审，评审写成中文 Markdown，存到 reviews 目录下，最后只回复文件路径。"
	for _, tc := range []struct {
		name, screen, prompt string
		want                 bool
	}{
		{"empty box", agyIdleScreen, agyFirstMessage, false},
		{"wrapped in a border", "│ > Review the build script and │\n│ list what breaks on Windows. │", agyFirstMessage, true},
		{"only its end in a scrolled box", "│ … list what breaks on Windows. │", agyFirstMessage, true},
		{"CJK, wrapped", "│ > 请按 docs/review.md 里的要求做一份独立 │\n│ 评审，评审写成中文 Markdown，存到 │", long, true},
		{"CJK, empty box", agyIdleScreen, long, false},
		{"paste chip", "│ > [Pasted text #2 +40 lines] │", long, true},
		{"short message", "│ > hi │", "hi", true},
		{"short message absent", agyIdleScreen, "go on", false},
	} {
		if got := promptShown(tc.screen, tc.prompt); got != tc.want {
			t.Errorf("%s: promptShown = %v, want %v", tc.name, got, tc.want)
		}
	}
}
