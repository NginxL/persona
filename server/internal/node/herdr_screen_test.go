package node

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Characterization tests of the TUI driving (session.modes: permission mode, effort) and the screen reads
// (session.screen, session.screen.before, the suggestion shown in the list) against herdr: the keys pressed and the
// reads made, in order.

// footerScreens are the empty-box fixture with its footer showing each permission mode.
func footerScreens(t *testing.T) map[string]string {
	t.Helper()
	base := screenFixture(t, "claude-2.1.312-empty-162.txt")
	const auto = "⏵⏵ auto mode on"
	if !strings.Contains(base, auto) {
		t.Fatal("fixture footer changed")
	}
	return map[string]string{
		"default":     strings.Replace(base, auto, "? for shortcuts", 1),
		"acceptEdits": strings.Replace(base, auto, "⏵⏵ accept edits on", 1),
		"plan":        strings.Replace(base, auto, "⏸ plan mode on", 1),
		"auto":        base,
	}
}

// cycleModes makes shift+tab on pane walk default → acceptEdits → plan → auto → default (no bypass in the cycle).
func cycleModes(t *testing.T, f *fakeHerdr, pane, from string) {
	screens := footerScreens(t)
	order := []string{"default", "acceptEdits", "plan", "auto"}
	var mu sync.Mutex
	at := 0
	for i, m := range order {
		if m == from {
			at = i
		}
	}
	f.setScreen(pane, screens[order[at]])
	f.after(func(c herdrCall) {
		if !isKeys("shift+tab")(c) || str(c.Params["pane_id"]) != pane {
			return
		}
		mu.Lock()
		at = (at + 1) % len(order)
		s := screens[order[at]]
		mu.Unlock()
		f.setScreen(pane, s)
	})
}

func setModes(t *testing.T, n *Node, id, mode, effort string) error {
	t.Helper()
	_, err := request(t, n, "session.modes", map[string]any{"session_id": id, "permission_mode": mode, "effort": effort})
	return err
}

func TestHerdrTUISetModePressesShiftTabUntilTheFooterShowsIt(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f,
		claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/m", ""),
		claudeAgent(sidB, proto.StatusIdle, "w1:p2", "w1:t2", "w1", "/tmp/m", ""))
	cycleModes(t, f, "w1:p1", "default")
	if err := setModes(t, n, sidA, "plan", ""); err != nil {
		t.Fatal(err)
	}
	assertTrace(t, f.trace(), []string{
		readVisible("w1:p1"), sendKeys("w1:p1", "shift+tab"),
		readVisible("w1:p1"), sendKeys("w1:p1", "shift+tab"),
		readVisible("w1:p1"),
	})

	// Already there: one read.
	f.reset()
	if err := setModes(t, n, sidA, "plan", ""); err != nil {
		t.Fatal(err)
	}
	assertTrace(t, f.trace(), []string{readVisible("w1:p1")})

	// No footer (a dialog in front): refused after one read, nothing pressed.
	f.reset()
	f.setScreen("w1:p2", screenFixture(t, "claude-2.1.312-trust-162.txt"))
	if err := setModes(t, n, sidB, "plan", ""); err == nil || !strings.Contains(err.Error(), "输入框") {
		t.Fatalf("err %v", err)
	}
	assertTrace(t, f.trace(), []string{readVisible("w1:p2")})
}

// bypassPermissions is not in a running session's cycle: len(cycle)+1 tries, then the error says so (the TUI is
// left in whatever mode the last press reached).
func TestHerdrTUISetModeGivesUpOnBypass(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/m", ""))
	cycleModes(t, f, "w1:p1", "auto")
	err := setModes(t, n, sidA, "bypassPermissions", "")
	if err == nil || !strings.Contains(err.Error(), "全部放行") {
		t.Fatalf("err %v", err)
	}
	var want []string
	for range 6 { // len(tuiModeCycle) + 1
		want = append(want, readVisible("w1:p1"), sendKeys("w1:p1", "shift+tab"))
	}
	assertTrace(t, f.trace(), want)
}

const (
	listPicker = "\n Effort\n\n   low\n ❯ medium\n   high\n\n Enter to set as default · s for this session only · Esc to cancel\n"
	slider     = "\n Effort  low ──●──── max\n\n ←/→ to adjust · Enter to confirm · Esc to cancel\n"
)

func effortSent(pane string, rights int) []string {
	keys := []string{"left", "left", "left", "left", "left", "left"}
	for range rights {
		keys = append(keys, "right")
	}
	return []string{
		readANSI(pane), // the box, colored: Claude's grey suggestion is not a draft
		call("pane.send_text", map[string]any{"pane_id": pane, "text": "/effort"}),
		sendKeys(pane, "enter"),
		readVisible(pane), // the picker
		sendKeys(pane, keys...),
	}
}

// The older list picker: to the left end, one right per level, "s" for this session only.
func TestHerdrTUISetEffortWithTheListPicker(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/e", ""))
	empty := screenFixture(t, "claude-2.1.312-empty-162.txt")
	done := empty + "\n  ⎿  Set effort level to high (this session only)\n"
	f.screenSteps("w1:p1", func(c herdrCall) bool { return isKeys("enter")(c) || isKeys("s")(c) }, empty, listPicker, done)
	if err := setModes(t, n, sidA, "", "high"); err != nil {
		t.Fatal(err)
	}
	assertTrace(t, f.trace(), append(effortSent("w1:p1", 2), sendKeys("w1:p1", "s"), readVisible("w1:p1")))
}

// The slider (Claude Code 2.1.2xx): the same walk, Enter confirms. An effort set in the environment wins: an error.
func TestHerdrTUISetEffortWithTheSlider(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/e", ""))
	empty := screenFixture(t, "claude-2.1.312-empty-162.txt")
	overridden := empty + "\n  ⎿  CLAUDE_CODE_EFFORT_LEVEL=low overrides this session's effort\n"
	f.screenSteps("w1:p1", isKeys("enter"), empty, slider, overridden)
	err := setModes(t, n, sidA, "", "max")
	if err == nil || !strings.Contains(err.Error(), "CLAUDE_CODE_EFFORT_LEVEL=low") {
		t.Fatalf("err %v", err)
	}
	assertTrace(t, f.trace(), append(effortSent("w1:p1", 4), sendKeys("w1:p1", "enter"), readVisible("w1:p1")))
}

// A draft in the box: refused after one read. A picker that never opens: 15 reads, then esc.
func TestHerdrTUISetEffortRefusesADraftAndEscapesWithoutAPicker(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f,
		claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/e", ""),
		claudeAgent(sidB, proto.StatusIdle, "w1:p2", "w1:t2", "w1", "/tmp/e", ""))
	f.setScreen("w1:p1", screenFixture(t, "claude-2.1.312-typed-162.txt"))
	if err := setModes(t, n, sidA, "", "low"); err == nil {
		t.Fatal("typed /effort after a draft")
	}
	assertTrace(t, f.trace(), []string{readANSI("w1:p1")})

	f.reset()
	f.setScreen("w1:p2", screenFixture(t, "claude-2.1.312-empty-162.txt"))
	if err := setModes(t, n, sidB, "", "low"); err == nil || !strings.Contains(err.Error(), "/effort") {
		t.Fatalf("err %v", err)
	}
	want := []string{readANSI("w1:p2"),
		call("pane.send_text", map[string]any{"pane_id": "w1:p2", "text": "/effort"}), sendKeys("w1:p2", "enter")}
	for range 15 {
		want = append(want, readVisible("w1:p2"))
	}
	assertTrace(t, f.trace(), append(want, sendKeys("w1:p2", "esc")))
}

// The grey next-message suggestion Claude Code shows after a turn is not a draft: the effort changes over it (#1434).
// Words typed over it are, and are left alone.
func TestHerdrTUISetEffortOverASuggestion(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f,
		claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/e", ""),
		claudeAgent(sidB, proto.StatusIdle, "w1:p2", "w1:t2", "w1", "/tmp/e", ""))
	f.setScreens("w1:p1", screenFixture(t, "claude-2.1.312-suggestion-171.txt"), screenFixture(t, "claude-2.1.312-suggestion-171.ansi"))
	done := screenFixture(t, "claude-2.1.312-empty-162.txt") + "\n  ⎿  Set effort level to high (this session only)\n"
	steps := []string{listPicker, done}
	f.after(func(c herdrCall) {
		if str(c.Params["pane_id"]) == "w1:p1" && (isKeys("enter")(c) || isKeys("s")(c)) && len(steps) > 0 {
			f.setScreen("w1:p1", steps[0])
			steps = steps[1:]
		}
	})
	if err := setModes(t, n, sidA, "", "high"); err != nil {
		t.Fatal(err)
	}
	assertTrace(t, f.trace(), append(effortSent("w1:p1", 2), sendKeys("w1:p1", "s"), readVisible("w1:p1")))

	f.reset()
	f.setScreens("w1:p2", screenFixture(t, "claude-2.1.312-suggestion-typed-171.txt"), screenFixture(t, "claude-2.1.312-suggestion-typed-171.ansi"))
	if err := setModes(t, n, sidB, "", "low"); err == nil || !strings.Contains(err.Error(), "还没发出的文字") {
		t.Fatalf("err %v", err)
	}
	assertTrace(t, f.trace(), []string{readANSI("w1:p2")})
}

// While Persona drives the effort picker herdr reports the agent blocked; the list shows it idle until the change is
// done, then as herdr says.
func TestHerdrTUIDrivingShowsBlockedAsIdle(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/e", ""))
	empty := screenFixture(t, "claude-2.1.312-empty-162.txt")
	f.screenSteps("w1:p1", func(c herdrCall) bool { return isKeys("enter")(c) || isKeys("s")(c) }, empty, listPicker,
		empty+"\n  ⎿  Set effort level to low (this session only)\n")
	opened := make(chan struct{})
	var once sync.Once
	f.after(func(c herdrCall) {
		if isKeys("enter")(c) {
			f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusBlocked, "w1:p1", "w1:t1", "w1", "/tmp/e", "")}, nil))
			once.Do(func() { close(opened) })
		}
	})
	done := make(chan error, 1)
	go func() { done <- setModes(t, n, sidA, "", "low") }()
	select {
	case <-opened:
	case <-time.After(10 * time.Second):
		t.Fatal("the picker was never opened")
	}
	if s := byID(listSessions(t, n))[sidA]; s.Status != proto.StatusIdle {
		t.Errorf("while driving: %s", s.Status)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Status != proto.StatusBlocked {
		t.Errorf("after: %s", s.Status)
	}
}

// While the agent works or waits on a dialog the effort is queued, not typed: the node only refreshes herdr's snapshot
// and never touches the pane. Codex and compatible variants sessions and unknown modes are refused without a herdr call.
func TestHerdrModesRefusals(t *testing.T) {
	n, f, home := herdrNode(t)
	writeRollout(t, home, sidC, "/tmp/x", time.Now(), time.Now())
	seedAgents(t, n, f,
		claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", "/tmp/e", ""),
		claudeAgent(sidB, proto.StatusBlocked, "w1:p2", "w1:t2", "w1", "/tmp/e", ""))
	for _, id := range []string{sidA, sidB} {
		if err := setModes(t, n, id, "", "high"); err == nil || !strings.Contains(err.Error(), "Agent 正在工作") {
			t.Errorf("%s: legacy busy refusal = %v", id, err)
		}
		if got := n.switches().effortChange(id).target; got != "" {
			t.Errorf("%s: legacy request queued %q", id, got)
		}
		reply, err := request(t, n, "session.modes", map[string]any{"session_id": id, "effort": "high", "queue_effort": true})
		if err != nil || reply["pending_effort"] != "high" {
			t.Errorf("%s: queued effort reply = %v, %v", id, reply, err)
		}
	}
	if err := setModes(t, n, sidC, "plan", ""); err == nil || !strings.Contains(err.Error(), "Codex / TRAE CLI") {
		t.Errorf("codex: %v", err)
	}
	if err := setModes(t, n, sidA, "sideways", ""); err == nil {
		t.Error("unknown mode")
	}
	assertTrace(t, f.trace(), []string{call("session.snapshot", nil), call("session.snapshot", nil)})
	for _, id := range []string{sidA, sidB} {
		if got := n.switches().effortChange(id).target; got != "high" {
			t.Errorf("%s: pending effort %q", id, got)
		}
	}
}

// session.screen: source is visible unless "recent"; lines are clamped (visible ≤ 400, recent ≤ 20000, ≤ 0 → 60);
// ansi reads with the escapes kept; the status is herdr's for the agent. A session herdr does not have: offline.
func TestHerdrScreenReads(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", "/tmp/s", ""))
	f.setScreen("w1:p1", "hello")
	for _, c := range []struct {
		lines  int
		source string
		ansi   bool
	}{{0, "", false}, {10, "visible", false}, {1000, "visible", false}, {0, "recent", false}, {50000, "recent", false},
		{-5, "scrollback", false}, {30, "recent", true}} {
		format := ""
		if c.ansi {
			format = "ansi"
		}
		res, err := request(t, n, "session.screen", map[string]any{"session_id": sidA, "lines": c.lines, "source": c.source, "format": format})
		if err != nil || res["text"] != "hello" || res["status"] != proto.StatusWorking {
			t.Fatalf("%+v: %v %v", c, res, err)
		}
	}
	read := func(source string, lines int) string {
		return call("pane.read", map[string]any{"pane_id": "w1:p1", "source": source, "lines": lines})
	}
	assertTrace(t, f.trace(), []string{
		read("visible", 60), read("visible", 10), read("visible", 400), read("recent", 60), read("recent", 20000),
		read("visible", 60),
		call("pane.read", map[string]any{"pane_id": "w1:p1", "source": "recent", "lines": 30, "format": "ansi", "strip_ansi": false}),
	})

	f.reset()
	res, err := request(t, n, "session.screen", map[string]any{"session_id": sidB})
	if err != nil || res["text"] != "" || res["status"] != proto.StatusOffline {
		t.Fatalf("offline: %v %v", res, err)
	}
	assertTrace(t, f.trace(), []string{call("session.snapshot", nil)})
}

func numbered(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// session.screen.before: one read of herdr's whole scrollback (1000 lines), the page right before the anchor (the
// match nearest len-loaded when it repeats); at_limit once the page reaches the start of a full scrollback; with ansi
// a second read gives the same rows with colors, used only when the row counts agree.
func TestHerdrScreenBefore(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/s", ""))
	before := func(anchor []string, lines, loaded int, ansi bool) map[string]any {
		t.Helper()
		format := ""
		if ansi {
			format = "ansi"
		}
		res, err := request(t, n, "session.screen.before", map[string]any{"session_id": sidA, "anchor": anchor, "lines": lines,
			"loaded": loaded, "format": format})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	recent := call("pane.read", map[string]any{"pane_id": "w1:p1", "source": "recent", "lines": 1000})
	recentANSI := call("pane.read", map[string]any{"pane_id": "w1:p1", "source": "recent", "lines": 1000, "format": "ansi", "strip_ansi": false})

	f.setScreen("w1:p1", numbered(1, 30))
	got := before([]string{"line 21", "line 22"}, 5, 10, false)
	if got["text"] != "line 16\nline 17\nline 18\nline 19\nline 20" || got["has_more"] != true || got["at_limit"] != false {
		t.Fatalf("page %v", got)
	}
	// Default 200 lines: from the top, nothing more, a short pane is not at herdr's limit.
	got = before([]string{"line 3"}, 0, 28, false)
	if got["text"] != "line 1\nline 2" || got["has_more"] != false || got["at_limit"] != false {
		t.Fatalf("top %v", got)
	}
	assertTrace(t, f.trace(), []string{recent, recent})

	// A full scrollback: reaching its top is at_limit; an anchor not found is an empty page. Redraws repeat the
	// anchor: the copy nearest len-loaded is the one.
	f.reset()
	f.setScreen("w1:p1", numbered(1, 1000))
	got = before([]string{"line 5"}, 200, 996, false)
	if got["has_more"] != false || got["at_limit"] != true || !strings.HasPrefix(str(got["text"]), "line 1\n") {
		t.Fatalf("limit %v", got)
	}
	got = before([]string{"not there"}, 200, 0, false)
	if got["text"] != "" || got["has_more"] != false || got["at_limit"] != true {
		t.Fatalf("missing anchor %v", got)
	}
	f.setScreen("w1:p1", "a\nX\nb\nc\nX\nd\n")
	if got = before([]string{"X"}, 1, 5, false); got["text"] != "a" {
		t.Fatalf("near the top: %v", got)
	}
	if got = before([]string{"X"}, 1, 2, false); got["text"] != "c" {
		t.Fatalf("near the end: %v", got)
	}
	assertTrace(t, f.trace(), []string{recent, recent, recent, recent})

	// ansi: the colored rows when they line up with the plain ones.
	f.reset()
	f.setScreens("w1:p1", "a\nb\nc\n", "\x1b[1ma\x1b[0m\r\n\x1b[2mb\x1b[0m\r\nc\r\n")
	if got = before([]string{"c"}, 5, 1, true); got["text"] != "\x1b[1ma\x1b[0m\n\x1b[2mb\x1b[0m" {
		t.Fatalf("ansi %q", got["text"])
	}
	f.setScreens("w1:p1", "a\nb\nc\n", "new\r\na\r\nb\r\nc\r\n") // output arrived between the reads
	if got = before([]string{"c"}, 5, 1, true); got["text"] != "a\nb" {
		t.Fatalf("misaligned ansi %q", got["text"])
	}
	// The same count of rows, shifted (a full scrollback that dropped its oldest row as output arrived, or a host that
	// trimmed a colored blank last row only in one read): the rows' text differs, so the plain page stands.
	f.setScreens("w1:p1", "a\nb\nc\n", "\x1b[1mb\x1b[0m\r\nc\r\nd\r\n")
	if got = before([]string{"c"}, 5, 1, true); got["text"] != "a\nb" {
		t.Fatalf("shifted ansi %q", got["text"])
	}
	// Trailing white space herdr's plain read drops (the no-break space after an empty box's prompt mark, an
	// ideographic space) does not make aligned rows look shifted: the colors stay.
	f.setScreens("w1:p1", "a\n❯\nc\n", "\x1b[1ma　\x1b[0m\r\n\x1b[2m❯ \x1b[0m\r\nc\r\n")
	if got = before([]string{"c"}, 5, 1, true); got["text"] != "\x1b[1ma　\x1b[0m\n\x1b[2m❯ \x1b[0m" {
		t.Fatalf("trailing unicode space ansi %q", got["text"])
	}
	assertTrace(t, f.trace(), []string{recent, recentANSI, recent, recentANSI, recent, recentANSI, recent, recentANSI})

	// A session herdr does not have: looked for once, refused.
	f.reset()
	if _, err := request(t, n, "session.screen.before", map[string]any{"session_id": sidB, "anchor": []string{"x"}}); err == nil {
		t.Fatal("paged a session that is not running")
	}
	assertTrace(t, f.trace(), []string{call("session.snapshot", nil)})
}

// A turn that ends (working → idle in the list) reads the screen with colors until Claude's grey suggestion shows,
// and the list carries it; a draft in the box stops the reads at once.
func TestHerdrSuggestionAfterATurnIsListed(t *testing.T) {
	withSuggestReads(t, 0, 10*time.Millisecond, 20*time.Millisecond)
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f,
		claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", "/tmp/g", ""),
		claudeAgent(sidB, proto.StatusWorking, "w1:p2", "w1:t2", "w1", "/tmp/g", ""))
	empty := screenFixture(t, "claude-2.1.312-empty-162.ansi")
	f.setScreens("w1:p1", "", empty)
	f.setScreens("w1:p2", "", screenFixture(t, "claude-2.1.312-suggestion-typed-171.ansi"))
	f.after(func(c herdrCall) { // the suggestion shows after the first read
		if c.Method == "pane.read" && c.Params["pane_id"] == "w1:p1" {
			f.setScreens("w1:p1", "", screenFixture(t, "claude-2.1.312-suggestion-171.ansi"))
		}
	})
	f.reply("session.snapshot", snapshot(nil, nil, []any{
		claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/g", ""),
		claudeAgent(sidB, proto.StatusIdle, "w1:p2", "w1:t2", "w1", "/tmp/g", ""),
	}, nil))
	if s := byID(listSessions(t, n))[sidA]; s.Suggestion != "" { // the reads start
		t.Fatalf("at once: %q", s.Suggestion)
	}
	deadline := time.Now().Add(10 * time.Second)
	for byID(listSessions(t, n))[sidA].Suggestion != "add all three to the README" {
		if time.Now().After(deadline) {
			t.Fatalf("no suggestion; reads: %v", f.trace("pane.read"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // past every scheduled read: none may follow
	if s := byID(listSessions(t, n))[sidB]; s.Suggestion != "" {
		t.Errorf("over a draft: %q", s.Suggestion)
	}
	var p1, p2 []string
	for _, c := range f.calls() {
		switch {
		case c.Method == "pane.read" && c.Params["pane_id"] == "w1:p1":
			p1 = append(p1, c.String())
		case c.Method == "pane.read":
			p2 = append(p2, c.String())
		}
	}
	assertTrace(t, p1, []string{readANSI("w1:p1"), readANSI("w1:p1")})
	assertTrace(t, p2, []string{readANSI("w1:p2")})
}
