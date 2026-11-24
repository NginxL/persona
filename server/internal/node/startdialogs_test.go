package node

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// autoModeOffer is a guess at Claude Code 2.1.330's first-run offer to make auto mode the default, with choices as
// given (each a whole row, cursor included): no real screen of it was captured, so the tests try several wordings.
func autoModeOffer(choices ...string) string {
	return "──────────────────────────────────────────────────────────────\n" +
		" Make auto mode your default permission mode?\n\n" +
		" Auto mode lets Claude decide which actions are safe to run without asking.\n\n" +
		strings.Join(choices, "\n") + "\n\n" +
		" Enter to confirm · Esc to cancel\n"
}

// dialogName is d's name for a test's message, "none" for nil.
func dialogName(d *startDialog) string {
	if d == nil {
		return "none"
	}
	return d.Name
}

// The offer to make auto mode the default is declined by what its choices say, wherever the cursor and the declining
// choice are; with no such choice it is dismissed with Esc. It never changes the user's setting.
func TestAutoModeDefaultIsDeclined(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen string
		want   []string
	}{
		{"numbered, cursor on Yes", autoModeOffer(" ❯ 1. Yes, make auto mode my default", "   2. No, keep current mode"),
			[]string{"down", "enter"}},
		{"numbered, cursor on No", autoModeOffer("   1. Yes, make auto mode my default", " ❯ 2. No, keep current mode"),
			[]string{"enter"}},
		{"numbered, no cursor drawn", autoModeOffer("   1. Yes", "   2. No"), []string{"down", "enter"}},
		{"unnumbered Yes/No", autoModeOffer("  ❯ Yes", "    No"), []string{"down", "enter"}},
		{"three choices, Not now", autoModeOffer(" ❯ 1. Yes, use auto mode by default", "   2. Not now", "   3. Never ask again"),
			[]string{"down", "enter"}},
		{"keep first", autoModeOffer(" ❯ 1. Keep my current default", "   2. Switch to auto mode"), []string{"enter"}},
		{"declining choice last, cursor below it", autoModeOffer("   1. Yes", "   2. Maybe later", " ❯ 3. Learn more"),
			[]string{"up", "enter"}},
		{"no declining choice", autoModeOffer(" ❯ 1. Yes, make it my default", "   2. Learn more"), []string{"esc"}},
		{"unread choices", strings.ReplaceAll(autoModeOffer("  [Yes]   [No]"), "Enter to confirm · Esc to cancel", ""),
			[]string{"esc"}},
		{"wrapped in a frame, other case", "╭────────────────────────────╮\n│ Make Auto Mode your       │\n│ Default Permission Mode? │\n" +
			"│                           │\n│ ❯ 1. Yes                  │\n│   2. No                   │\n╰────────────────────────────╯\n" +
			" Enter to confirm · Esc to cancel\n", []string{"esc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, keys := matchStartDialog(claudeStartDialogs, tc.screen, false)
			if d == nil || d.Name != startDialogAutoMode || d.ChangesUserSettings || !slices.Equal(keys, tc.want) {
				t.Fatalf("got %s %v, want %v", dialogName(d), keys, tc.want)
			}
		})
	}
}

// Only the offer is answered: claude's input box with the words in a banner, other screens and other dialogs are not.
func TestAutoModeDefaultLeavesOtherScreens(t *testing.T) {
	banner := "auto mode can now be your default permission mode: see /config\n" + screenFixture(t, "claude-2.1.312-empty-162.txt")
	for name, s := range map[string]string{
		"banner over the input box": banner,
		"empty input box":           screenFixture(t, "claude-2.1.312-empty-162.txt"),
		"model picker":              screenFixture(t, "claude-2.1.312-model-picker-162.txt"),
		"auto mode on footer":       "> \n  ⏵⏵ auto mode on (shift+tab to cycle)",
	} {
		if keys := autoModeDefaultKeys(s); keys != nil {
			t.Errorf("%s: %v", name, keys)
		}
	}
}

// The table keeps the answers the matchers gave before it: trust first, then the first-run screens, the Bypass
// Permissions confirmation only for a start that asked for bypass.
func TestClaudeStartDialogsKeepTheirAnswers(t *testing.T) {
	trust := screenFixture(t, "claude-2.1.312-trust-162.txt")
	if d, keys := matchStartDialog(claudeStartDialogs, trust, false); d == nil || !d.Started || !slices.Equal(keys, trustKeys(trust)) {
		t.Errorf("trust: %s %v", dialogName(d), keys)
	}
	notes := "Security notes:\n 1. Claude can make mistakes\n\nPress Enter to continue…"
	if d, keys := matchStartDialog(claudeStartDialogs, notes, false); d == nil || d.Started || !slices.Equal(keys, []string{"enter"}) {
		t.Errorf("security notes: %s %v", dialogName(d), keys)
	}
	if d, _ := matchStartDialog(claudeStartDialogs, bypassConfirm(t), false); d != nil {
		t.Errorf("bypass confirmation answered on a start that did not ask for bypass: %v", d.Name)
	}
	if d, keys := matchStartDialog(claudeStartDialogs, bypassConfirm(t), true); d == nil || d.Name != startDialogBypass ||
		!slices.Equal(keys, []string{"down", "enter"}) {
		t.Errorf("bypass: %s %v", dialogName(d), keys)
	}
	if d, _ := matchStartDialog(claudeStartDialogs, screenFixture(t, "claude-2.1.312-empty-162.txt"), true); d != nil {
		t.Errorf("input box answered as %s", d.Name)
	}
}

// An unrecognized dialog is a list of choices with claude's input box not drawn that none of the table's rows is:
// never claude's input box, its loading screen, nor a known dialog (answerable yet or not).
func TestUnknownStartDialog(t *testing.T) {
	if d := unknownStartDialog(screenFixture(t, "claude-2.1.312-model-picker-162.txt")); d == nil || len(d.Options) != 6 {
		t.Errorf("model picker: %+v", d)
	}
	for name, s := range map[string]string{
		"input box":          screenFixture(t, "claude-2.1.312-empty-162.txt"),
		"loading":            "Claude Code\n loading…",
		"trust":              screenFixture(t, "claude-2.1.312-trust-162.txt"),
		"bypass":             bypassConfirm(t),
		"auto mode":          autoModeOffer(" ❯ 1. Yes", "   2. No"),
		"theme, no cursor":   "Choose the text style that looks best with your terminal\n  Auto (match terminal)\n  Dark mode",
		"shell":              "~/proj % ",
		"agent list in chat": "⏺ I will:\n  1. read\n  2. fix\n\n> \n  ? for shortcuts",
	} {
		if d := unknownStartDialog(s); d != nil {
			t.Errorf("%s: read as unrecognized: %+v", name, d)
		}
	}
}

// A start whose claude stops on a dialog the node does not know ends within seconds, not claudeReadyWait later: as
// agent_start_stalled, saying so, with the whole screen (every choice) in its text and data, and the tab kept.
func TestHerdrStartOnAnUnknownDialogReportsItsScreen(t *testing.T) {
	n, f, home := herdrNode(t)
	withStartWaits(n, time.Minute, time.Minute)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	startHerdr(f, "w1:p2", proto.StatusIdle)
	f.reply("pane.list", map[string]any{"panes": []any{map[string]any{"pane_id": "w1:p2", "tab_id": "w1:t2"}}})
	f.setScreen("w1:p2", screenFixture(t, "claude-2.1.312-model-picker-162.txt"))
	began := time.Now()
	_, err := request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "prompt": "hi"})
	if err == nil || !strings.Contains(err.Error(), "an unrecognized dialog blocks the start:") || rpcCode(err) != proto.CodeAgentStartStalled {
		t.Fatalf("start: %v", err)
	}
	if d := time.Since(began); d > 20*time.Second {
		t.Errorf("waited %s for an unknown dialog", d)
	}
	for _, want := range []string{"Select model", "1. Default (recommended)", "6. Fable 5.1 (disabled)", "Enter to set as default"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if data := refusalData(t, err); !strings.Contains(data, "Select model") || !strings.Contains(data, "Esc to cancel") ||
		!strings.Contains(data, `"pane":"w1:p2"`) || !strings.Contains(data, `"kept_until":`) {
		t.Errorf("data %s", data)
	}
	if got := f.trace("pane.send_keys"); len(got) != 0 {
		t.Errorf("pressed keys on an unknown dialog: %v", got)
	}
	if got := f.trace("tab.close"); len(got) != 0 {
		t.Errorf("closed the tab: %v", got)
	}
}

// A start that meets the offer to make auto mode the default declines it ("No", down and enter) and goes on: the
// first message goes in once claude's input box shows. Before, it waited out claudeReadyWait on the offer.
func TestHerdrStartDeclinesTheAutoModeOffer(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	startHerdr(f, "w1:p2", proto.StatusIdle)
	f.screenSteps("w1:p2", isKeys(), autoModeOffer(" ❯ 1. Yes, make auto mode my default", "   2. No, keep current mode"),
		screenFixture(t, "claude-2.1.312-empty-162.txt"))
	promptLands(t, f, home, cwd)
	if _, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "workspace_id": "w1", "prompt": "fix the build"}); err != nil {
		t.Fatal(err)
	}
	n.firstWait.Wait()
	got := f.trace("pane.send_keys", "agent.prompt")
	if len(got) != 2 || got[0] != sendKeys("w1:p2", "down", "enter") ||
		got[1] != call("agent.prompt", map[string]any{"target": "w1:p2", "text": "fix the build"}) {
		t.Errorf("trace %v", got)
	}
}
