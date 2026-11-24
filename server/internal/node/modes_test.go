package node

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTuiMode(t *testing.T) {
	const box = "────\n❯ \n────\n"
	cases := map[string]string{
		box + "  ⏸ manual mode on · ? for shortcuts · ← for agents": "default",
		box + "  ? for shortcuts":                               "default",
		box + "  ⏵⏵ accept edits on (shift+tab to cycle)":       "acceptEdits",
		box + "  ⏸ plan mode on (shift+tab to cycle)\n\n":       "plan",
		box + "  ⏵⏵ auto mode on (shift+tab to cycle)":          "auto",
		box + "  ⏵⏵ don't ask on (shift+tab to cycle)":          "dontAsk",
		box + "  ⏵⏵ bypass permissions on (shift+tab to cycle)": "bypassPermissions",
		// Chat text mentioning a mode, far above the footer, does not count.
		"I turned plan mode on for you\n" + box + "\n\n\n\n\n": "",
	}
	for screen, want := range cases {
		if got := tuiMode(screen); got != want {
			t.Errorf("tuiMode(%q) = %q, want %q", screen, got, want)
		}
	}
}

func TestTuiPromptEmpty(t *testing.T) {
	for screen, want := range map[string]bool{
		"────\n❯ \n────\n  ? for shortcuts":                                         true,
		"────\n❯ Try \"create a util logging.py that...\"\n────\n  ? for shortcuts": true,
		"────\n❯ half-typed draft\n────\n  ? for shortcuts":                         false,
		"no prompt here": false,
	} {
		if got := tuiPromptEmpty(screen); got != want {
			t.Errorf("tuiPromptEmpty(%q) = %v", screen, got)
		}
	}
}

func TestValidModesAndFlags(t *testing.T) {
	if err := validModes("plan", "xhigh"); err != nil {
		t.Fatal(err)
	}
	if err := validModes("", "auto"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][2]string{{"yolo", ""}, {"", "ultra"}, {"plan; rm -rf ~", ""}} {
		if validModes(bad[0], bad[1]) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if got := modeFlags("acceptEdits", "low"); got != " --permission-mode acceptEdits --effort low" {
		t.Errorf("flags %q", got)
	}
	// --effort rejects auto (Claude Code 2.1.316 warns and uses the default): auto is starting without the flag.
	if got := modeFlags("plan", "auto"); got != " --permission-mode plan" {
		t.Errorf("auto flags %q", got)
	}
	if modeFlags("", "") != "" {
		t.Error("empty flags")
	}
}

func TestCurrentModesPrefersNewer(t *testing.T) {
	n := &Node{modes: map[string]sessionModes{}}
	old := time.Now().Add(-time.Minute)
	m := quickMeta{mode: "default", modeAt: old, effort: "high", effortAt: old}
	if mode, effort := n.currentModes("s", m); mode != "default" || effort != "high" {
		t.Fatalf("transcript only: %s %s", mode, effort)
	}
	n.noteModes("s", "plan", "") // set from the app after the transcript's last word
	if mode, effort := n.currentModes("s", m); mode != "plan" || effort != "high" {
		t.Fatalf("app wins: %s %s", mode, effort)
	}
	m.mode, m.modeAt = "auto", time.Now().Add(time.Second) // then the terminal changed it again
	if mode, _ := n.currentModes("s", m); mode != "auto" {
		t.Fatalf("newer transcript wins: %s", mode)
	}
}

// Claude Code 2.1.206's /effort is a slider (←/→, Enter), not the list with "for this session only".
func TestEffortPickerAndOutcome(t *testing.T) {
	b, err := os.ReadFile("testdata/claude-2.1.206-effort-slider-46.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := effortPicker(string(b)); got != "slider" {
		t.Fatalf("slider: %q", got)
	}
	if got := effortPicker("Choose effort\n  ● high  for this session only (s)"); got != "list" {
		t.Fatalf("list: %q", got)
	}
	if got := effortPicker("❯ \n  ⏸ manual mode on"); got != "" {
		t.Fatalf("none: %q", got)
	}
	// Claude Code 2.1.330 (#13534): Enter saves the level as the default for new sessions, "s" keeps it to this session.
	for name, footer := range map[string]string{
		"one line":   "  ←/→ to adjust · s to use this session only · Enter to save · Esc to cancel",
		"wrapped":    "  ←/→ to adjust · s to use this session\n  only · Enter to save · Esc to\n  cancel",
		"bare hints": "  ←/→ adjust · s use this session only · enter save · esc cancel",
	} {
		if got := effortPicker(effortSlider330(footer)); got != "session-slider" {
			t.Errorf("2.1.330 %s: %q", name, got)
		}
	}
	// Already on the session's own level: Enter says it only uses it for this session, and there is no "s".
	if got := effortPicker(effortSlider330("  ←/→ to adjust · Enter to use this session only · Esc to cancel")); got != "slider" {
		t.Fatalf("2.1.330 Enter for this session: %q", got)
	}
	// After it closes, the confirmation is not the picker.
	if got := effortPicker("❯ /effort\n  ⎿  Set effort level to medium (this session only)\n\n❯ "); got != "" {
		t.Fatalf("closed: %q", got)
	}
	// A launcher's CLAUDE_CODE_EFFORT_LEVEL wins over the session's: told, not claimed as done.
	env := "❯ /effort\n  ⎿  CLAUDE_CODE_EFFORT_LEVEL=max overrides\n     this session — clear it and xhigh takes\n     over"
	if err := effortOutcome(env, "xhigh"); err == nil || !strings.Contains(err.Error(), "CLAUDE_CODE_EFFORT_LEVEL=max") {
		t.Fatalf("env override: %v", err)
	}
	if err := effortOutcome("  ⎿  Set effort level to high (this session)", "high"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := effortOutcome("  ⎿  Set effort level to medium (this session only): Balanced", "auto"); err != nil {
		t.Fatalf("auto takes whichever level is the default: %v", err)
	}
	if err := effortOutcome("❯ /effort\n  ⎿  Cancelled\n", "auto"); err == nil {
		t.Fatal("cancelled should not count as auto confirmed")
	}
	if err := effortOutcome("❯ \n", "low"); err == nil {
		t.Fatal("nothing said: should not count as changed")
	}
}

func TestEffortPicker330(t *testing.T) {
	for _, tt := range []struct {
		name, screen, key string
	}{
		{"offers a session-only key", slider330Save, "s"},
		{"enter is session-only", slider330Enter, "enter"},
		{"wrapped footer", strings.Replace(slider330Save, "only · Enter", "\n  only · Enter", 1), "s"},
		{"ordinary output", "Effort\n  Use the arrows to adjust or save for later.\n❯ ", ""},
		{"footer in scrollback", slider330Save + "\n❯ ", ""},
		{"another dialog", strings.Replace(slider330Save, "Effort", "Volume", 1), ""},
		{"no session binding", slider330Body + "  ←/→ to adjust · Enter to save · Esc to cancel\n", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := effortSliderConfirmKey(tt.screen); got != tt.key {
				t.Errorf("confirmation key = %q, want %q", got, tt.key)
			}
			want := ""
			switch tt.key {
			case "s":
				want = "session-slider"
			case "enter":
				want = "slider"
			}
			if got := effortPicker(tt.screen); got != want {
				t.Errorf("picker = %q, want %q", got, want)
			}
		})
	}
}

func TestEffortOutcome330(t *testing.T) {
	for _, tt := range []struct {
		screen, effort string
	}{
		{"⎿  Set effort level to medium (this session\n  only): Balanced approach", "medium"},
		{"  ○ low · /effort\n────\n❯ ", "low"},
		{"  ◐ medium · /effort\n────\n❯ ", "medium"},
		{"  ● high · /effort\n────\n❯ ", "high"},
		{"  ◉ xhigh · /effort\n────\n❯ ", "xhigh"},
		{"  ◈ max · /effort\n────\n❯ ", "max"},
		{"  ◐ medium · /effort\n────\n❯ ", "auto"},
	} {
		if err := effortOutcome(tt.screen, tt.effort); err != nil {
			t.Errorf("effortOutcome(%q, %q) = %v", tt.screen, tt.effort, err)
		}
	}
	for _, screen := range []string{"  ◐ medium · /effort\n────\n❯ ", "  ◉ xhigh · working"} {
		if err := effortOutcome(screen, "xhigh"); err == nil {
			t.Errorf("effortOutcome(%q, xhigh) accepted an unconfirmed change", screen)
		}
	}
}

// effortSlider330 is Claude Code 2.1.330's /effort slider with `footer` as its key hints.
func effortSlider330(footer string) string {
	return "❯ /effort\n\n  Effort\n  ──────────▲\n  low     medium     high     xhigh      max     ultracode\n  " + effortPickerDefault +
		"\n\n" + footer + "\n"
}

// tuiSetEffort confirms with "s" where 2.1.330's slider offers it (Enter would save the level as the default for new
// sessions, #13534), and with Enter on the 2.1.2xx slider.
func TestTuiSetEffortConfirmsForThisSession(t *testing.T) {
	empty := screenFixture(t, "claude-2.1.312-empty-162.ansi")
	done := "❯ /effort\n  ⎿  Set effort level to medium (this session only)\n\n❯ \n"
	for name, c := range map[string]struct{ footer, confirm string }{
		"2.1.330 with s": {"  ←/→ to adjust · s to use this session only · Enter to save · Esc to cancel", "s"},
		"2.1.330 Enter":  {"  ←/→ to adjust · Enter to use this session only · Esc to cancel", "enter"},
		"2.1.2xx":        {"  ←/→ to adjust · Enter to confirm · Esc to cancel", "enter"},
	} {
		picker := effortSlider330(c.footer)
		// The empty prompt; /effort and Enter open the picker; the level's keys leave it open; the confirm closes it.
		f := &fakePane{screens: []string{empty, picker, picker, done}}
		n := &Node{hosts: herdrHosts(f.serve(t))}
		if err := n.tuiSetEffort(context.Background(), "w1:p1", "medium"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(f.keys) != 3 || !slices.Equal(f.keys[0], []string{"enter"}) || !slices.Equal(f.keys[1], effortPickerKeys("medium")) ||
			!slices.Equal(f.keys[2], []string{c.confirm}) {
			t.Fatalf("%s: keys %v, want the confirm %q", name, f.keys, c.confirm)
		}
	}
}

func TestEffortPickerKeys(t *testing.T) {
	// The picker opens on the session's level; 6 lefts reach low (past ultracode on the slider), then one right per slot.
	for level, rights := range map[string]int{"low": 0, "medium": 1, "high": 2, "xhigh": 3, "max": 4} {
		keys := effortPickerKeys(level)
		if len(keys) != 6+rights || slices.ContainsFunc(keys[:6], func(k string) bool { return k != "left" }) ||
			slices.ContainsFunc(keys[6:], func(k string) bool { return k != "right" }) {
			t.Errorf("%s: %v", level, keys)
		}
	}
	if !slices.Contains(effortLevels, effortAuto) || slices.Contains(effortPickerLevels, effortAuto) {
		t.Fatal("auto is a level the app offers, not a picker slot")
	}
}

func TestTuiPickDefaultEffort(t *testing.T) {
	picker := func(marker int) string {
		hint := "~1.5x the estimated cost of medium (the default)"
		if marker == 1 {
			hint = effortPickerDefault
		}
		return "  Effort\n  " + strings.Repeat("─", marker*10) + "▲\n  low     medium     high     xhigh      max\n  " + hint +
			"\n  ←/→ to adjust · Enter to confirm · s for this session only · Esc to cancel"
	}
	// Opened on xhigh; lefts land on low, one right reaches medium, the model's default.
	f := &fakePane{screens: []string{picker(3), picker(0), picker(1), picker(2)}}
	n := &Node{hosts: herdrHosts(f.serve(t))}
	if err := n.tuiPickDefaultEffort(context.Background(), "w1:p1"); err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 2 || len(f.keys[0]) != 6 || f.keys[0][0] != "left" || !slices.Equal(f.keys[1], []string{"right"}) {
		t.Fatalf("keys %v", f.keys)
	}
	// No slot marked as the default (an organization's setting): give up instead of guessing.
	f = &fakePane{screens: []string{picker(0), picker(0), picker(2), picker(2), picker(2), picker(2)}}
	n = &Node{hosts: herdrHosts(f.serve(t))}
	if err := n.tuiPickDefaultEffort(context.Background(), "w1:p1"); err == nil {
		t.Fatal("no default found but no error")
	}
}

type nopWriteCloser struct{ bytes.Buffer }

func (*nopWriteCloser) Close() error { return nil }

func TestSyncAssistantModesAuto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	n := &Node{modes: map[string]sessionModes{}, paths: map[string]string{"s": path},
		metas: map[string]quickMeta{path: {size: st.Size(), mtime: st.ModTime()}}} // no reply yet
	out := &nopWriteCloser{}
	p := &assistantProc{id: "s"}
	p.stdin = &ackControlWriter{WriteCloser: out, p: p}
	sent := func() []any {
		var got []any
		for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			if l == "" {
				continue
			}
			var r struct{ Request map[string]any }
			_ = json.Unmarshal([]byte(l), &r)
			got = append(got, r.Request["settings"].(map[string]any)["effortLevel"])
		}
		out.Reset()
		return got
	}

	n.noteModes("s", "", "high")
	if err := n.syncAssistantModes(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if got := sent(); len(got) != 1 || got[0] != "high" {
		t.Fatalf("high: %v", got)
	}
	n.noteModes("s", "", "auto")
	if err := n.syncAssistantModes(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if got := sent(); len(got) != 1 || got[0] != nil { // effortLevel: null
		t.Fatalf("auto: %v", got)
	}
	// The next reply runs at the model's default and the transcript says so; that must not pin it to medium.
	n.metas[path] = quickMeta{size: st.Size(), mtime: st.ModTime(), effort: "medium", effortAt: time.Now().Add(time.Minute)}
	if err := n.syncAssistantModes(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if got := sent(); len(got) != 0 {
		t.Fatalf("re-sent after the reply: %v", got)
	}
}

// lineSink records what the node writes to an assistant process's stdin.
type lineSink struct {
	mu     sync.Mutex
	b      strings.Builder
	closed bool
}

func (s *lineSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *lineSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *lineSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeAssistant puts a running Claude process (no real one) in the node's table.
func fakeAssistant(n *Node, id, startMode string) (*assistantProc, *lineSink) {
	sink := &lineSink{}
	p := &assistantProc{id: id, agent: "claude", launcher: "claude", cmd: &exec.Cmd{}, stdin: sink, exited: make(chan struct{}),
		ready: make(chan struct{}), mode: startMode, canBypass: startMode == proto.ModeBypass}
	p.stdin = &ackControlWriter{WriteCloser: sink, p: p}
	n.mu.Lock()
	n.procs[id] = p
	n.mu.Unlock()
	return p, sink
}

// The mode the app set on an assistant conversation wins over its transcript, however new the transcript's word,
// and survives the node (persisted with the session state): an old "default" there locked the main one in (#2416).
func TestAssistantAppModeWinsOverTranscript(t *testing.T) {
	n := assistantNode(t)
	n.modes = map[string]sessionModes{}
	const id = "11111111-2222-3333-4444-555555555555"
	var saved []sessionState
	n.state.persist = func(_ string, v sessionState) { saved = append(saved, v) }
	n.state.put(id, sessionState{Launcher: "claude", Cwd: "/home/u/.persona/assistant"})

	if err := n.setModes(context.Background(), id, "acceptEdits", "high"); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 || saved[1].AppMode != "acceptEdits" || saved[1].AppEffort != "high" || saved[1].Launcher != "claude" {
		t.Fatalf("not persisted with the session state: %+v", saved)
	}
	n.modes = map[string]sessionModes{} // the node restarted: only the hub's state is left
	m := quickMeta{mode: "default", modeAt: time.Now().Add(time.Hour), effort: "low", effortAt: time.Now().Add(time.Hour)}
	if mode, effort := n.currentModes(id, m); mode != "acceptEdits" || effort != "high" {
		t.Fatalf("transcript won over the app: %s %s", mode, effort)
	}
	// Setting the same again writes nothing.
	n.persistModes(id, "acceptEdits", "")
	if len(saved) != 2 {
		t.Fatalf("rewrote an unchanged mode: %d", len(saved))
	}
}

// bypassPermissions can't be set with a control request in a process started without it: an idle one ends, so the
// next message resumes in that mode; a busy one is left alone and no control request claims the switch.
func TestSetModesBypassOnAssistant(t *testing.T) {
	n := assistantNode(t)
	n.modes = map[string]sessionModes{}
	const id = "11111111-2222-3333-4444-555555555555"
	n.state.put(id, sessionState{Launcher: "claude", Cwd: "/home/u/.persona/assistant"})

	// Other modes: a control request to the running process.
	p, sink := fakeAssistant(n, id, "")
	if err := n.setModes(context.Background(), id, "acceptEdits", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sink.String(), `"mode":"acceptEdits"`) || !strings.Contains(sink.String(), "set_permission_mode") {
		t.Fatalf("no control request: %q", sink.String())
	}

	// Busy: kept, and nothing sent for bypassPermissions.
	p.mu.Lock()
	p.busy = true
	p.mu.Unlock()
	before := sink.String()
	if err := n.setModes(context.Background(), id, proto.ModeBypass, ""); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	kept := n.procs[id] == p
	n.mu.Unlock()
	if !kept || sink.String() != before {
		t.Fatalf("busy process: kept %v, sent %q", kept, strings.TrimPrefix(sink.String(), before))
	}
	if !p.needsRestartFor(proto.ModeBypass) {
		t.Fatal("the switch should wait for a new process")
	}
	if mode, _ := n.modesOf(id); mode != proto.ModeBypass {
		t.Fatalf("mode shown: %s", mode)
	}

	// Idle: the process ends; the next message starts one with --permission-mode bypassPermissions (spawnClaude reads
	// modesOf).
	p.mu.Lock()
	p.busy = false
	p.mu.Unlock()
	if err := n.setModes(context.Background(), id, proto.ModeBypass, ""); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	_, running := n.procs[id]
	n.mu.Unlock()
	if running || !sink.closed {
		t.Fatalf("idle process not stopped: running %v, stdin closed %v", running, sink.closed)
	}
	if st, _ := n.state.get(id); st.AppMode != proto.ModeBypass {
		t.Fatalf("not persisted: %+v", st)
	}

	// A process started in bypassPermissions switches away and back with control requests.
	q, sink2 := fakeAssistant(n, id, proto.ModeBypass)
	if err := n.setModes(context.Background(), id, "default", ""); err != nil {
		t.Fatal(err)
	}
	if err := n.setModes(context.Background(), id, proto.ModeBypass, ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(sink2.String(), "set_permission_mode"); got != 2 || q.needsRestartFor(proto.ModeBypass) {
		t.Fatalf("started in bypass: %d control requests %q", got, sink2.String())
	}
}

// ackControlWriter makes in-memory assistant fakes acknowledge the settings they accept.
type ackControlWriter struct {
	io.WriteCloser
	p *assistantProc
}

func (w *ackControlWriter) Write(b []byte) (int, error) {
	n, err := w.WriteCloser.Write(b)
	var request struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
	}
	if err == nil && json.Unmarshal(b, &request) == nil && request.Type == "control_request" {
		w.p.controlResponse(assistantControlResponse{RequestID: request.RequestID, Subtype: "success"})
	}
	return n, err
}
