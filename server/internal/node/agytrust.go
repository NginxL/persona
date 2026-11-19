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

// agyTrustWait bounds how long a start watches agy's pane for the dialog before the first message goes in; agyTrustPoll
// is how often it looks (variables for tests). Past the bound the start goes on: the first-message check
// (workagent_first.go) flags a message it cannot confirm.
var (
	agyTrustWait = 15 * time.Second
	agyTrustPoll = 300 * time.Millisecond
)

// agyReadyHint is the hint under agy's input box: the box is up, so no trust dialog is coming.
const agyReadyHint = "? for shortcuts"

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

// agyPaneSettled: herdr reports the agy in pane as anything but blocked (it reads the trust dialog as blocked), so its
// input box is up. False when the hosts cannot be read or no agy is reported there.
func (n *Node) agyPaneSettled(ctx context.Context, pane string) bool {
	if err := n.refreshHosts(ctx); err != nil {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range n.agents {
		if a.PaneID == pane && a.Agent == agyAgent {
			return a.AgentStatus != "" && a.AgentStatus != proto.StatusBlocked && a.AgentStatus != proto.StatusUnknown
		}
	}
	return false
}

// agyTrustFolder answers agy's trust dialog in pane, if one shows, before the start types its first message. It returns
// once agy's input box shows (its hint, or herdr no longer reads agy as blocked) with no dialog, or after agyTrustWait;
// errors only when ctx ends or keys cannot be sent.
func (n *Node) agyTrustFolder(ctx context.Context, pane string) error {
	deadline := time.Now().Add(agyTrustWait)
	answered := "" // the dialog screen last answered: the same screen is not answered twice
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
			} else if !screen.Choosing(text) && (strings.Contains(text, agyReadyHint) || n.agyPaneSettled(ctx, pane)) {
				return nil
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
