package node

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/screen"
)

// A start in an Orca tab does not take Orca's word that claude runs there (#10588). Orca names a pane's agent from a
// scan of the processes carrying its pane key, and when that cannot be read from its own agent row and terminal
// identity, which it keeps after the agent exits (hostorca.go); its terminal.send does not say whether the text reached
// the tab's shell either. A start whose line never arrived, in a tab showing nothing but empty prompts, was named claude
// there: it waited for claude's input box instead of the shell, ended as a Claude stall (agent_start_stalled), and never
// read the shell's answers (the not-found and parse-error lines of #1642 and #2570), which are read only while claude
// has not come. herdr names the program in front of the pane and stops once it exits (claudeGoneWait relies on that),
// and reports a line it could not send (pane_send_failed): starts on the other hosts keep the host's word.

// startNeedsEvidence: a start in a pane of h takes claude as come only with evidence of its own (claudeCame), and one
// whose line never showed there and never ran ends as not delivered (startNotDelivered).
func startNeedsEvidence(h host.Host) bool { return h != nil && h.Kind() == host.Orca }

// echoed: the start's typed line shows on screen, by the anchor it carries (startLine).
func (p *startProbe) echoed(shown string) bool {
	return p != nil && p.anchor != "" && strings.Contains(shown, p.anchor)
}

// ran: the tab's shell ran the start's line. Its start file is gone (the file removes itself first, startScript); a
// command typed out has only its echo to go by.
func (p *startProbe) ran(shown string) bool {
	if p.startFile != "" {
		_, err := os.Stat(p.startFile)
		return errors.Is(err, fs.ErrNotExist)
	}
	return p.echoed(shown)
}

// unread: the start's file is still there, so nothing of its line ran. false for a command typed out: unknown.
func (p *startProbe) unread() bool {
	if p == nil || p.startFile == "" {
		return false
	}
	_, err := os.Stat(p.startFile)
	return err == nil
}

// claudeCame: the screen backs the host naming claude in the pane. Claude Code's own screen does, and so does the
// start's line having run with nothing on screen saying the shell could not run it. The echo alone does not: a line can
// sit unrun after the prompt (#5450), and one the shell answered for (a cut head, a command not found) shows it too.
func (p *startProbe) claudeCame(shown string) bool {
	if claudeShown(shown) {
		return true
	}
	return p.ran(shown) && !mayHaveShellMiss(shown) && !p.mayBeRejected(shown)
}

// claudeShown: shown is Claude Code's own: its banner, its input box, or one of its dialogs at the bottom.
func claudeShown(shown string) bool {
	return inputBoxVisible(shown) || strings.Contains(shown, "Claude Code v") ||
		strings.Contains(shown, "Welcome to Claude Code") || screen.Choosing(shown)
}

// startNotDelivered is the refusal of a start whose line never showed in its new tab within wait, its start file still
// unread: the terminal did not deliver it, or the shell never read it. Nothing ran (the file goes with the start,
// typedStart.done), so it is terminal_not_ready, as for a line the terminal refused: the tab is closed, and a retry,
// in a new tab, may work.
func startNotDelivered(wait time.Duration) error {
	return refusal(terminalNotReady, fmt.Sprintf("claude did not start within %s: the start line never showed in the new tab "+
		"and its shell never ran it, so the terminal did not deliver it (nothing ran)", wait), map[string]any{"retryable": true})
}
