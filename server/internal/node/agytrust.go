package node

import (
	"context"
	"log"
	"strings"
	"time"

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

// agyTrustWait bounds how long a start watches agy's pane for the dialog before the first message goes in; agyTrustPoll
// is how often it looks (variables for tests). Past the bound the start goes on: the first-message check
// (workagent_first.go) flags a message it cannot confirm.
var (
	agyTrustWait = 15 * time.Second
	agyTrustPoll = 300 * time.Millisecond
)

// agyReadyHint is the hint under agy's input box: the box is up, so no trust dialog is coming.
const agyReadyHint = "? for shortcuts"

// agyNoticeDismiss is the line under a notice agy draws above its input box on start, which holds the keyboard.
const agyNoticeDismiss = "esc to dismiss"

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

// agyTrustFolder answers agy's trust dialog in pane, if one shows, before the start types its first message. It returns
// once agy's input box is drawn (its hint) with no dialog, or after agyTrustWait; errors only when ctx ends or keys cannot
// be sent. herdr reports agy by its process before agy's screen takes the terminal, and calls it idle then: a message
// typed in that window is echoed by the terminal and dropped by agy (#11437), so herdr's status alone never lets the
// first message go. A start notice over the box is dismissed with one Esc first.
func (n *Node) agyTrustFolder(ctx context.Context, pane string) error {
	deadline := time.Now().Add(agyTrustWait)
	answered := "" // the dialog screen last answered: the same screen is not answered twice
	dismissed := false
	for {
		text, err := n.readPane(ctx, pane, "visible", 40)
		if err == nil {
			if keys := agyTrustKeys(text); keys != nil {
				if text != answered {
					if err := n.sendKeys(ctx, pane, keys...); err != nil {
						return err
					}
					log.Printf("agy start in pane %s: trusted the folder the start chose (#11424)", pane)
					answered = text
				}
			} else if !screen.Choosing(text) && strings.Contains(text, agyReadyHint) {
				// A notice agy shows over its input box on start ("Auto is now the default agent mode … esc to
				// dismiss", 1.5.13) swallows typed input until it is dismissed (#11437): Esc once, then the box.
				if dismissed || !strings.Contains(strings.ToLower(text), agyNoticeDismiss) {
					return nil
				}
				if err := n.sendKeys(ctx, pane, "escape"); err != nil {
					return err
				}
				log.Printf("agy start in pane %s: dismissed agy's start notice before the first message (#11437)", pane)
				dismissed = true
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
