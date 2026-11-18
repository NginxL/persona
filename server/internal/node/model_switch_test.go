package node

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestModelCommand(t *testing.T) {
	for in, want := range map[string]string{
		"opus":                 "/model opus",
		" sonnet ":             "/model sonnet",
		"default":              "/model default",
		"opus[1m]":             "/model opus[1m]",
		"claude-opus-4-1":      "/model claude-opus-4-1",
		"gateway/model-x:free": "/model gateway/model-x:free",
		"gpt-5.1-codex":        "/model gpt-5.1-codex",
		"claude-x@20250101":    "/model claude-x@20250101", // a typed id (#1644): the same rule as a start's
		"qwen3:32b":            "/model qwen3:32b",
		"~vendor/model-latest": "/model ~vendor/model-latest", // a gateway's latest alias
	} {
		got, err := modelCommand(in)
		if err != nil || got != want {
			t.Errorf("modelCommand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// Nothing that is more than one word, a flag, another command, or a bare /model (which opens the picker).
	for _, bad := range []string{"", "  ", "-p", "--model", "/clear", "opus; rm -rf ~", "opus\n/clear", "a b", "opus\x1b[A", strings.Repeat("a", proto.MaxModelIDLen+1), "$(id)", "`id`", "~", "~/x"} {
		if got, err := modelCommand(bad); err == nil {
			t.Errorf("modelCommand(%q) = %q, want an error", bad, got)
		}
	}
}

func TestModelOutcome(t *testing.T) {
	if err := modelOutcome("❯ /model opus\n  ⎿  Set model to Opus 4.1 (claude-opus-4-1)\n", "opus"); err != nil {
		t.Errorf("set: %v", err)
	}
	// An earlier switch's line above the command we typed does not count.
	old := "  ⎿  Set model to Sonnet 4\n❯ /model opus\n"
	if err := modelOutcome(old, "opus"); err == nil || !strings.Contains(err.Error(), "可能没切到") {
		t.Errorf("stale line: %v", err)
	}
	if err := modelOutcome("❯ /model nope\n  ⎿  Model 'nope' not found\n", "nope"); err == nil || !strings.Contains(err.Error(), "没接受") {
		t.Errorf("not found: %v", err)
	}
	if err := modelOutcome("", "opus"); err == nil {
		t.Error("empty screen claimed as done")
	}
}

func TestModelPicker(t *testing.T) {
	if !modelPicker("Select model\n ❯ 1. Default\n") {
		t.Error("picker not recognised")
	}
	if modelPicker("❯ /model opus\n  ⎿  Set model to Opus\n") {
		t.Error("a finished switch is not a picker")
	}
}

func TestShownModelHoldsThePickUntilTheTranscriptMovesOn(t *testing.T) {
	n := &Node{modes: map[string]sessionModes{}}
	if got := n.shownModel("a", "claude-sonnet-4"); got != "claude-sonnet-4" {
		t.Errorf("no pick: %q", got)
	}
	n.noteModel("a", "opus", "claude-sonnet-4")
	if got := n.shownModel("a", "claude-sonnet-4"); got != "opus" {
		t.Errorf("pick stands: %q", got)
	}
	// A reply wrote the model it really ran: the transcript wins.
	if got := n.shownModel("a", "claude-opus-4-1"); got != "claude-opus-4-1" {
		t.Errorf("transcript moved on: %q", got)
	}
	n.mu.Lock()
	m := n.modes["a"]
	m.Model.At = time.Now().Add(-2 * pendingModelTTL)
	n.modes["a"] = m
	n.mu.Unlock()
	if got := n.shownModel("a", "claude-sonnet-4"); got != "claude-sonnet-4" {
		t.Errorf("expired pick: %q", got)
	}
	// Efforts and modes set later keep the model note.
	n.noteModel("b", "opus", "x")
	n.noteModes("b", "plan", "high")
	if got := n.shownModel("b", "x"); got != "opus" {
		t.Errorf("modes dropped the model: %q", got)
	}
}

func setModel(t *testing.T, n *Node, id, model string) error {
	t.Helper()
	_, err := request(t, n, "session.modes", map[string]any{"session_id": id, "model": model})
	return err
}

// The switch types /model <id> after the same checks as a message, reads the screen again, and reports the pick.
func TestHerdrSetModelTypesTheCommandAndChecksTheScreen(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/m", ""))
	empty, emptyANSI := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	f.setScreens("w1:p1", empty, emptyANSI)
	f.after(func(c herdrCall) {
		if c.Method == "agent.prompt" {
			f.setScreens("w1:p1", empty+"\n❯ /model opus\n  ⎿  Set model to Opus 4.1 (claude-opus-4-1)\n", emptyANSI)
		}
	})
	if err := setModel(t, n, sidA, "opus"); err != nil {
		t.Fatal(err)
	}
	assertTrace(t, squeeze(f.trace()), []string{
		readANSI("w1:p1"), // the same box check as a message
		call("agent.prompt", map[string]any{"target": "w1:p1", "text": "/model opus"}),
		readVisible("w1:p1"),
	})
	if s := byID(listSessions(t, n))[sidA]; s.Model != "" && s.Model != "opus" {
		t.Errorf("model shown: %q", s.Model)
	}
}

// A typed /model on a managed provider behind a gateway gets what a start's --model gets (#10365): a tier alias is the
// provider's model for that tier, and one it names none for is left out, `/model default` (its default) typed instead
// of an alias Claude Code would send to the gateway as a model's name.
func TestHerdrSetModelMapsATierAliasOnAGatewayProvider(t *testing.T) {
	for _, tc := range []struct {
		name  string
		l     proto.ManagedLauncher
		model string
		typed string
	}{
		{"mapped", proto.ManagedLauncher{Name: "gw", Kind: "api", BaseURL: "https://gw.example.com", Model: "m1", OpusModel: "route/claude-opus-4-6"}, "opus[1m]", "route/claude-opus-4-6[1m]"},
		{"left out", proto.ManagedLauncher{Name: "gw", Kind: "api", BaseURL: "https://gw.example.com"}, "opus", "default"},
		{"a model ID", proto.ManagedLauncher{Name: "gw", Kind: "api", BaseURL: "https://gw.example.com"}, "vendor/model-x", "vendor/model-x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, _ := herdrNode(t)
			n.setManaged([]proto.ManagedLauncher{tc.l})
			seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/m", ""))
			n.state.put(sidA, sessionState{Launcher: "gw", Cwd: "/tmp/m"})
			empty, emptyANSI := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
			f.setScreens("w1:p1", empty, emptyANSI)
			f.after(func(c herdrCall) {
				if c.Method == "agent.prompt" {
					f.setScreens("w1:p1", empty+"\n❯ /model "+tc.typed+"\n  ⎿  Set model to "+tc.typed+"\n", emptyANSI)
				}
			})
			if err := setModel(t, n, sidA, tc.model); err != nil {
				t.Fatal(err)
			}
			assertTrace(t, squeeze(f.trace()), []string{
				readANSI("w1:p1"),
				call("agent.prompt", map[string]any{"target": "w1:p1", "text": "/model " + tc.typed}),
				readVisible("w1:p1"),
			})
		})
	}
}

func TestHerdrSetModelWaitsForConfirmationAfterTheLatestCommand(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/m", ""))
	empty, emptyANSI := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	previous := "❯ /model sonnet\n  ⎿  Set model to Sonnet 4\n"
	pending := empty + previous + "❯ /model opus\n"
	f.setScreens("w1:p1", empty+previous, emptyANSI)
	reads := 0
	f.after(func(c herdrCall) {
		if c.Method == "agent.prompt" {
			f.setScreens("w1:p1", pending, emptyANSI)
		}
		if c.Method == "pane.read" && c.Params["format"] != "ansi" {
			reads++
			if reads == 1 {
				f.setScreens("w1:p1", pending+"  ⎿  Set model to Opus 4.1 (claude-opus-4-1)\n", emptyANSI)
			}
		}
	})
	if err := setModel(t, n, sidA, "opus"); err != nil {
		t.Fatalf("an older confirmation cut the read short: %v", err)
	}
	if reads != 2 {
		t.Fatalf("visible reads = %d, want the pending read and the confirmed read", reads)
	}
}

func TestHerdrSetModelRefusals(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f,
		claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/tmp/m", ""),
		claudeAgent(sidB, proto.StatusWorking, "w1:p2", "w1:t2", "w1", "/tmp/m", ""),
		claudeAgent(sidC, proto.StatusIdle, "w1:p3", "w1:t3", "w1", "/tmp/m", ""))

	// A draft typed at the computer: input_draft, nothing typed.
	f.setScreens("w1:p1", screenFixture(t, "claude-2.1.312-typed-162.txt"), screenFixture(t, "claude-2.1.312-typed-162.ansi"))
	err := setModel(t, n, sidA, "opus")
	if code, _ := refusalOf(t, err); code != "input_draft" {
		t.Fatalf("over a draft: %v", err)
	}
	assertTrace(t, f.trace(), []string{readANSI("w1:p1")})

	// A dialog with the keyboard: dialog_open, nothing typed.
	f.reset()
	f.setScreens("w1:p3", screenFixture(t, "claude-2.1.312-trust-162.txt"), screenFixture(t, "claude-2.1.312-trust-162.ansi"))
	err = setModel(t, n, sidC, "opus")
	if code, _ := refusalOf(t, err); code != "dialog_open" {
		t.Fatalf("over a dialog: %v", err)
	}
	for _, c := range f.trace() {
		if strings.Contains(c, "agent.prompt") || strings.Contains(c, "send_text") || strings.Contains(c, "send_keys") {
			t.Fatalf("typed into a dialog: %s", c)
		}
	}

	// Working: not switched, no herdr call at all.
	f.reset()
	if err := setModel(t, n, sidB, "opus"); err == nil || !strings.Contains(err.Error(), "正在工作") {
		t.Fatalf("while working: %v", err)
	}
	assertTrace(t, f.trace(), nil)

	// A model that is not one word never gets near the pane.
	f.reset()
	if err := setModel(t, n, sidA, "opus\n/clear"); err == nil {
		t.Fatal("accepted a multi-line model")
	}
	assertTrace(t, f.trace(), nil)
}
