package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// The first message of a Cursor or agy start (startWorkAgent) goes in through herdr's agent.prompt, or on Windows
// through a paste and its Enter (hostherdr.go). Both only type. herdr reports the agent as soon as it sees its process,
// and says it is idle as soon as its input box shows, but agy is still signing in then (model list, slash commands,
// skills and hooks reloaded): a message typed in that window can be dropped. agy then waited at an empty box while the
// start said it had begun, until someone sent the task again (#11345). So, as for Claude (firstprompt.go), the message
// counts as taken only once the agent is seen taking it: herdr calls it working, blocked or done, or herdr's
// Antigravity integration reports the agy conversation, which its hook does only once a message was submitted. Until
// then the message is typed again only when it is provably gone, up to firstPromptTries times in all. When it cannot be
// confirmed, the session is listed as needing the user (lostFirst) instead of idle.

// workFirstWait is how long herdr has to see the agent take its first message after it was typed, and workFirstPoll
// how often the node looks. Tests shorten both.
var workFirstWait, workFirstPoll = 10 * time.Second, 250 * time.Millisecond

// workFirst is what the node saw of a typed first message.
type workFirst int

const (
	workFirstUnseen workFirst = iota // not taken by the deadline, the agent idle at its last look
	workFirstTaken                   // working, blocked, done, or an agy conversation reported
	workFirstUnsure                  // not taken, and the agent not provably idle (unknown status, read failed)
	workFirstGone                    // the agent left the pane, or another one took it
)

// workAgentFirst types the first message into the new agent of kind in pane and, in the background, checks that the
// agent takes it (confirmWorkFirst). The start answers as before; messages sent to the session meanwhile wait for the
// check (the typing lock, workAgentCalls), so one typed again cannot meet them. An error: the host refused the message.
func (n *Node) workAgentFirst(ctx context.Context, pane, id, kind, prompt string) error {
	unlock := n.typing.lock(id)
	since := time.Now()
	if err := n.hostPrompt(ctx, pane, prompt); err != nil {
		unlock()
		return err
	}
	parent := n.firstPromptParent
	if parent == nil {
		parent = context.Background()
	}
	n.firstWait.Add(1)
	go func() {
		defer n.firstWait.Done()
		defer unlock()
		bctx, cancel := context.WithTimeout(parent, firstPromptTries*(workFirstWait+15*time.Second))
		defer cancel()
		if why := n.confirmWorkFirst(bctx, pane, kind, prompt); why != "" {
			log.Printf("start %s: %s", id, why)
			n.lostFirst.set(id, why, since)
			n.notifySessions()
		}
	}()
	return nil
}

// confirmWorkFirst waits for the agent in pane to take the first message, typing it again while it is provably gone:
// the agent stayed idle and the screen shows nothing of it. It returns "" once the agent took it or left the pane,
// else why it did not go in, for the user.
func (n *Node) confirmWorkFirst(ctx context.Context, pane, kind, prompt string) string {
	for try := 1; ; try++ {
		switch n.workFirstSeen(ctx, pane, kind) {
		case workFirstTaken:
			if try > 1 {
				log.Printf("start in pane %s: %s took its first message on try %d", pane, kind, try)
			}
			return ""
		case workFirstGone:
			log.Printf("start in pane %s: %s left the pane before taking its first message", pane, kind)
			return ""
		case workFirstUnsure:
			return lostTimeout
		}
		// agy keeps a message it did not submit in its input box, a long one as "[Pasted text #N +M lines]", and shows
		// one it took above the box: typing it again is safe only when none of that is on the screen.
		s, err := n.readPane(ctx, pane, "visible", 80)
		if err != nil || promptShown(s, prompt) {
			return lostTimeout
		}
		if try == firstPromptTries {
			return fmt.Sprintf(lostTries, firstPromptTries)
		}
		if kind == agyAgent {
			if err := n.agyClearOverlay(ctx, pane, s); err != nil {
				log.Printf("start in pane %s: clear agy overlay before retry: %v", pane, err)
				return lostTimeout
			}
		}
		log.Printf("start in pane %s: %s did not take its first message (try %d), typing it again", pane, kind, try)
		err = n.hostPrompt(ctx, pane, prompt)
		if errors.Is(err, host.ErrAgentBlocked) {
			return lostDialog
		}
		if err != nil {
			log.Printf("start in pane %s: type the first message again: %v", pane, err)
			return lostTimeout
		}
	}
}

// workFirstSeen looks at the agent in pane every workFirstPoll for workFirstWait, until it is seen taking the message.
func (n *Node) workFirstSeen(ctx context.Context, pane, kind string) workFirst {
	deadline := time.Now().Add(workFirstWait)
	for {
		select {
		case <-ctx.Done():
			return workFirstUnsure
		case <-time.After(workFirstPoll):
		}
		idle := false
		a, err := n.paneAgent(ctx, pane)
		switch {
		case errors.Is(err, host.ErrNoAgent):
			return workFirstGone
		case err != nil:
			// not read: no proof either way
		case a.Agent != kind:
			return workFirstGone
		case a.AgentStatus == proto.StatusWorking || a.AgentStatus == proto.StatusBlocked ||
			a.AgentStatus == proto.StatusDone || n.agyReported(*a) != "":
			return workFirstTaken
		default:
			idle = a.AgentStatus == proto.StatusIdle
		}
		if time.Now().After(deadline) {
			if idle {
				return workFirstUnseen
			}
			return workFirstUnsure
		}
	}
}

// workShownRunes is how many characters of the message in a row count as it being on the screen.
const workShownRunes = 12

// pasteChip is how agy (and Claude Code, Codex) draw a long paste in their input box, anywhere on the screen.
var pasteChip = lazyre.New(`(?i)\[pasted (text|content)`)

// promptShown: the screen shows a paste placeholder, or workShownRunes characters of prompt in a row anywhere (the
// whole prompt when it is shorter). Spaces and box drawing are left out of both, so wrapping, borders and indentation
// do not matter; the runs overlap, so a part the agent renders differently (Markdown) does not hide the rest.
func promptShown(screen, prompt string) bool {
	if pasteChip.MatchString(screen) {
		return true
	}
	s, p := shownText(screen), []rune(shownText(prompt))
	if len(p) == 0 {
		return false
	}
	if len(p) <= workShownRunes {
		return strings.Contains(s, string(p))
	}
	for i := 0; ; i += workShownRunes / 2 {
		if i+workShownRunes >= len(p) {
			return strings.Contains(s, string(p[len(p)-workShownRunes:]))
		}
		if strings.Contains(s, string(p[i:i+workShownRunes])) {
			return true
		}
	}
}

// shownText is s without spaces, control characters and box drawing (U+2500 to U+259F).
func shownText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r >= 0x2500 && r <= 0x259F {
			return -1
		}
		return r
	}, s)
}
