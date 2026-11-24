package node

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/screen"
)

// startDialog is one screen an agent CLI shows on its way up that a start answers by itself, without a person. The
// tables below are every such screen the node knows: a start that meets another dialog does not wait for its timeout
// on it but reports it with the whole screen (unknownStartDialog). Adding a dialog is adding a row.
type startDialog struct {
	// Name says which dialog it is, in logs.
	Name string
	// Keys are the keys that answer it, nil when the screen does not show it (or not yet with what its answer needs).
	Keys func(screen string) []string
	// Shows says the screen is this dialog even while Keys cannot answer it yet (its cursor not drawn): such a screen
	// is a known dialog, never an unrecognized one. nil: the dialog is known only by Keys.
	Shows func(screen string) bool
	// ChangesUserSettings records whether the answer the node gives writes a setting of the user's that outlives the
	// start (a folder's trust, a theme, an accepted mode). Only for the record and its log line: the table's rows that
	// do are the ones the user's own choice for this start stands for.
	ChangesUserSettings bool
	// Started: answering it shows the agent itself runs in the pane, so the wait for its input box starts (a trust
	// dialog, which herdr reports as idle and names no agent for).
	Started bool
	// OnlyAcceptBypass: answered only on a start Persona itself asked for Bypass Permissions mode (probe.acceptBypass,
	// the user chose Allow all); any other start meets it as firstRunScreen's refusal.
	OnlyAcceptBypass bool
}

// The names of the rows a caller treats specially.
const (
	startDialogBypass   = "claude_bypass_confirm"
	startDialogAutoMode = "claude_auto_mode_default"
)

// claudeStartDialogs are Claude Code's start-up dialogs, in the order a start looks for them (claudeready.go
// waitReady, assistant_tui_term.go awaitReady): the folder's trust, the first-run style picker and security notes, the
// Bypass Permissions confirmation on a start that asked for it, and the offer to make auto mode the default.
var claudeStartDialogs = []startDialog{
	{Name: "claude_folder_trust", Keys: trustKeys, ChangesUserSettings: true, Started: true,
		Shows: func(s string) bool {
			return strings.Contains(s, "trust this folder") || strings.Contains(s, "Do you trust")
		}},
	{Name: "claude_theme_picker", Keys: themePickerKeys, ChangesUserSettings: true,
		Shows: func(s string) bool { return strings.Contains(unframed(s), themePickerQuestion) }},
	{Name: "claude_security_notes", Keys: securityNotesKeys},
	{Name: startDialogBypass, Keys: bypassConfirmKeys, ChangesUserSettings: true, Started: true, OnlyAcceptBypass: true},
	{Name: startDialogAutoMode, Keys: autoModeDefaultKeys, Started: true, Shows: autoModeDefaultShown},
}

// codexStartDialogs are Codex's: its folder trust dialog and its update offer, skipped (codex_start.go,
// codex_waiting.go).
var codexStartDialogs = []startDialog{
	{Name: "codex_folder_trust", Keys: codexTrustKeys, ChangesUserSettings: true, Started: true},
	{Name: "codex_skip_update", Keys: codexSkipUpdateKeys},
}

// matchStartDialog is the first dialog of table on screen and the keys that answer it, nil when none is. A row only
// for starts that asked for bypass is passed over unless acceptBypass.
func matchStartDialog(table []startDialog, screen string, acceptBypass bool) (*startDialog, []string) {
	for i := range table {
		d := &table[i]
		if d.OnlyAcceptBypass && !acceptBypass {
			continue
		}
		if keys := d.Keys(screen); keys != nil {
			return d, keys
		}
	}
	return nil, nil
}

// knownStartDialog: the screen shows one of table's dialogs, answerable now or not.
func knownStartDialog(table []startDialog, screen string) bool {
	for i := range table {
		d := &table[i]
		if d.Shows != nil && d.Shows(screen) || d.Keys(screen) != nil {
			return true
		}
	}
	return strings.Contains(screen, bypassConfirmQuestion) // refused at once by firstRunScreen when not answered
}

// unknownDialogLooks is how many looks in a row (500 ms apart: about 3 seconds) a start sees a dialog it does not know
// before it gives up on it and reports the screen, instead of waiting out claudeReadyWait.
const unknownDialogLooks = 6

// unknownStartDialog is the dialog on a starting Claude's screen that none of claudeStartDialogs is, nil when there is
// none: a list of choices the screen parser reads, with claude's input box not drawn. Claude's own screens on the way
// to its input box (loading, the input box with a banner above it) have no choices; a known dialog is answered before.
func unknownStartDialog(s string) *screen.Dialog {
	if inputBoxVisible(s) || knownStartDialog(claudeStartDialogs, s) {
		return nil
	}
	d := screen.ParseDialog(s)
	if d == nil || len(d.Options) < 2 {
		return nil
	}
	return d
}

// unknownDialogError is why a start ended on dialog d: what it asks and offers. It is a claudeStalled, so the start
// keeps its tab and reports the whole screen with agent_start_stalled (control.go startTabFailed).
func unknownDialogError(d *screen.Dialog) error {
	labels := make([]string, 0, len(d.Options))
	for _, o := range d.Options {
		labels = append(labels, o.Label)
	}
	q := strings.TrimSpace(d.Question)
	if q == "" {
		q = strings.TrimSpace(d.Header)
	}
	return &claudeStalled{fmt.Errorf("an unrecognized dialog blocks the start: %q (choices: %s); "+
		"the tab is kept: read its screen and answer it, or retry", q, strings.Join(labels, " / "))}
}

// autoModeDefaultShown: the screen offers to make auto mode the default permission mode (Claude Code 2.1.330 asks "Make
// auto mode your default permission mode?" the first time it starts after the update). Matched loosely, in any case
// and across a frame's wrapped rows: its exact wording was not captured from a real screen.
func autoModeDefaultShown(s string) bool {
	low := strings.ToLower(unframed(s))
	return strings.Contains(low, "auto mode") && strings.Contains(low, "default permission mode")
}

// keepSettingRE is a choice that leaves the user's setting as it is: "No", "No, keep current mode", "Not now",
// "Keep …", "Don't …", "Skip", "Maybe later".
var keepSettingRE = lazyre.New(`(?i)^(?:no\b|nope\b|not now\b|keep\b|don['’]?t\b|do not\b|skip\b|maybe later\b|later\b|ask me later\b|not yet\b)`)

// autoModeDefaultKeys answer the offer to make auto mode the default permission mode by declining it: the node does
// not change a user's settings on its own (the user's rule: 不改用户设置), and this start's mode is the one Persona
// passes on its command line either way. The choice is found by what it says, not by where it is, since the dialog's
// wording and order were not captured: the cursor is walked to it and Enter pressed. With no such choice on screen (or
// one the parser does not read), Esc dismisses the offer, which leaves the setting as it is too.
func autoModeDefaultKeys(s string) []string {
	if !autoModeDefaultShown(s) {
		return nil
	}
	d := screen.ParseDialog(s)
	if d == nil || len(d.Options) == 0 {
		if inputBoxVisible(s) {
			return nil // the words in a banner or the conversation above claude's input box: no dialog to answer
		}
		return []string{"esc"}
	}
	for i, o := range d.Options {
		if keepSettingRE.MatchString(optionText(o.Label)) {
			return dialogKeysTo(d, i)
		}
	}
	return []string{"esc"}
}

// optionText is a choice's label without a check mark or a number the parser left on it.
func optionText(label string) string {
	t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(label), "❯>›✔✓ "))
	if i := strings.Index(t, ". "); i > 0 && i <= 2 && strings.Trim(t[:i], "0123456789") == "" {
		t = strings.TrimSpace(t[i+2:])
	}
	return t
}

// arrowsOnly: keys are all up or down moves (none: the option is under the cursor).
func arrowsOnly(keys []string) bool {
	for _, k := range keys {
		if k != "up" && k != "down" {
			return false
		}
	}
	return true
}

// dialogKeysTo are the arrow moves from d's cursor (its first option when none is drawn) to its option target, then
// Enter. Arrows rather than an option's number: a number alone picks at once in some lists and only moves in others.
func dialogKeysTo(d *screen.Dialog, target int) []string {
	if d.Cursor == "" {
		// An unnumbered list names no cursor: its options' own keys are already the moves from it (screen.unnumbered).
		if k := d.Options[target].Keys; len(k) > 0 && k[len(k)-1] == "enter" && arrowsOnly(k[:len(k)-1]) {
			return slices.Clone(k)
		}
	}
	cur := 0
	for i, o := range d.Options {
		if o.Key == d.Cursor {
			cur = i
			break
		}
	}
	var keys []string
	for i := cur; i < target; i++ {
		keys = append(keys, "down")
	}
	for i := cur; i > target; i-- {
		keys = append(keys, "up")
	}
	return append(keys, "enter")
}
