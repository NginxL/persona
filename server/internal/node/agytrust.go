package node

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// agy's folder-trust dialog (#11424). In a folder it has not trusted yet (a new worktree, which task_plan makes by
// default), the Antigravity CLI first asks
//
//	Do you trust the contents of this project?
//	> Yes, I trust this folder
//	  No, exit
//
// and nothing reaches its input box until someone answers. Persona answers it for the folder the start chose, as
// Claude's start (claudeready.go trustKeys) and Codex's (codex_start.go codexTrustKeys: "the folder is trusted, they
// chose it") do. screen.ParseDialog reads the dialog (#10778); its unnumbered `>` list carries each option's keys.

// agyTrustWait bounds how long a start watches agy's pane for the dialog before the first message goes in; agyInitWait
// bounds how long it waits after the skeleton input box appears for herdr to stop reporting status unknown while agy
// finishes loading its model and start notices; agyTrustPoll is how often it looks (variables for tests). Past the
// bound the start goes on: the first-message check (workagent_first.go) flags a message it cannot confirm.
var (
	agyTrustWait = 15 * time.Second
	agyInitWait  = 8 * time.Second
	agyTrustPoll = 300 * time.Millisecond
)

// agyReadyHint is the default footer under agy's input box.
const agyReadyHint = "? for shortcuts"

// agyNoticeDismiss is the line under a notice agy draws above its input box on start, which holds the keyboard.
const agyNoticeDismiss = "esc to dismiss"

// agyStartNotice reports whether screen text shows an agy start notice waiting for Esc.
func agyStartNotice(text string) bool {
	return strings.Contains(strings.ToLower(text), agyNoticeDismiss)
}

// agyTrustKeys are the keys that answer agy's trust dialog by trusting the folder, nil when the screen does not show it.
// The dialog is known by its question ("Do you trust the contents of this project?") and the option by what it says
// ("Yes, I trust this folder"), not by its place.
func agyTrustKeys(text string) []string {
	if !strings.Contains(strings.ToLower(text), "do you trust") {
		return nil
	}
	d := screen.ParseDialog(text)
	if d == nil {
		return nil
	}
	for _, o := range d.Options {
		if l := strings.ToLower(o.Label); (strings.HasPrefix(l, "yes") || strings.HasPrefix(l, "trust")) && len(o.Keys) > 0 {
			return o.Keys
		}
	}
	return nil
}

// agyInputReady recognizes the default footer or the input box agy draws above a custom status line.
func agyInputReady(text string) bool {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	last := len(lines) - 1
	if strings.HasPrefix(strings.TrimSpace(lines[last]), agyReadyHint) {
		return true
	}
	// Look for the closing rule at the bottom of the frame or just above the status line. The two rules
	// span the same width, with a single prompt row between them.
	for bottom := last; bottom >= 2 && bottom >= last-1; bottom-- {
		rule := strings.TrimSpace(lines[bottom])
		if len(rule) < len("────────") || strings.Trim(rule, "─") != "" || strings.TrimSpace(lines[bottom-2]) != rule {
			continue
		}
		prompt := strings.TrimSpace(lines[bottom-1])
		if prompt == ">" || strings.HasPrefix(prompt, "> ") {
			return true
		}
	}
	return false
}

// agyTrustFolder answers agy's trust dialog in pane, if one shows, before the start types its first message. It returns
// once the input box is drawn with no dialog and herdr no longer reports status unknown, bounded by agyInitWait
// and agyTrustWait. herdr can report the process before agy takes the terminal, and agy can draw its input box before
// it finishes loading its model, mode and notices (#11437, #11345). A start notice over the box gets one Esc first.
func (n *Node) agyTrustFolder(ctx context.Context, pane string) error {
	deadline := time.Now().Add(agyTrustWait)
	var initDeadline time.Time
	answered := "" // the dialog screen last answered: the same screen is not answered twice
	dismissed := false
	for {
		text, err := n.readPane(ctx, pane, "visible", 40)
		if err == nil {
			if keys := agyTrustKeys(text); keys != nil {
				initDeadline = time.Time{}
				if text != answered {
					if err := n.sendKeys(ctx, pane, keys...); err != nil {
						return err
					}
					log.Printf("agy start in pane %s: trusted the folder the start chose (#11424)", pane)
					answered = text
				}
			} else if !screen.Choosing(text) && agyInputReady(text) {
				// A notice agy shows over its input box on start ("Auto is now the default agent mode … esc to
				// dismiss", 1.5.13+) swallows typed input until it is dismissed (#11437): Esc once, then the box.
				if !dismissed && agyStartNotice(text) {
					if err := n.sendKeys(ctx, pane, "escape"); err != nil {
						return err
					}
					log.Printf("agy start in pane %s: dismissed agy's start notice before the first message (#11437)", pane)
					dismissed = true
				} else {
					a, aerr := n.paneAgent(ctx, pane)
					if aerr != nil || a.Agent != agyAgent || a.AgentStatus != proto.StatusUnknown {
						return nil
					}
					if initDeadline.IsZero() {
						initDeadline = time.Now().Add(agyInitWait)
					}
					if !time.Now().Before(initDeadline) {
						return nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(agyTrustPoll):
		}
	}
}

// agyClearOverlay clears a late-arriving trust dialog or start notice before confirmWorkFirst retypes a dropped first
// message (#11345). It sends the needed keys once and polls until the overlay clears or agyTrustWait expires.
func (n *Node) agyClearOverlay(ctx context.Context, pane, text string) error {
	if keys := agyTrustKeys(text); keys != nil {
		if err := n.sendKeys(ctx, pane, keys...); err != nil {
			return err
		}
		log.Printf("agy retry in pane %s: trusted the folder before retrying the first message (#11345)", pane)
		return n.agyWaitOverlayGone(ctx, pane, true)
	}
	if agyStartNotice(text) {
		if err := n.sendKeys(ctx, pane, "escape"); err != nil {
			return err
		}
		log.Printf("agy retry in pane %s: dismissed late start notice before retrying the first message (#11345)", pane)
		return n.agyWaitOverlayGone(ctx, pane, false)
	}
	return nil
}

func (n *Node) agyWaitOverlayGone(ctx context.Context, pane string, waitReady bool) error {
	deadline := time.Now().Add(agyTrustWait)
	for {
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(agyTrustPoll):
		}
		text, err := n.readPane(ctx, pane, "visible", 40)
		if err != nil {
			continue
		}
		if agyTrustKeys(text) != nil || agyStartNotice(text) {
			continue
		}
		if !waitReady || (!screen.Choosing(text) && agyInputReady(text)) {
			return nil
		}
	}
}
