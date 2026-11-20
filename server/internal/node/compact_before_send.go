package node

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// Compacting a nearly full Agent before a message goes in (#11137). The assistant's agent_send asks for it
// (sendOpts.CompactIfFull): rather than starting a replacement that knows nothing of the Agent's work, the Agent's own
// runtime compacts its context (Claude Code's and Codex's /compact; a joined Codex's thread/compact/start, which
// sendLocked picks for /compact, codex_remote_compact.go) and the message goes in after it.
//
//   - Decided at delivery: an Agent mid-turn whose context is already nearly full keeps the message here, not in the
//     runtime's own queue (where nothing could come between the turn and the message), and the node looks again once
//     the turn has ended. One that is not nearly full gets the message as before.
//   - Done when the transcript gains a compaction record after the /compact (Claude Code's compact_boundary, a Codex
//     rollout's compacted / context_compacted) and the Agent no longer works: the runtime's own signals, no fixed
//     wait. Failed when the Agent went back to rest without one, wrote an error, never began (compactStartGrace), its
//     terminal went away, or no record came within compactBeforeSendDeadline; the message is then not sent and the
//     refusal says why.
//   - Only Claude Code and Codex: any other runtime (pi, agy, opencode, cursor, a runtime such as Paseo, a Claude
//     outside Persona's terminals) has no compaction Persona can run, and its message goes in as before.

var (
	// compactBeforeSendDeadline bounds one compaction: a long conversation's summary takes a few minutes at most.
	compactBeforeSendDeadline = 10 * time.Minute
	// compactStartGrace: how long a /compact (whose submission sendLocked has confirmed) may take to show as work or
	// write its record before it counts as not taken. A failure the runtime wrote ends the wait at once.
	compactStartGrace = 3 * time.Minute
	// compactPoll: how often the transcript and the Agent's status are read while it compacts or finishes a turn.
	compactPoll = 500 * time.Millisecond
	// codexComposerWait bounds how long a resumed Codex may take to show its input box before /compact goes in.
	codexComposerWait = 3 * time.Minute
	// codexStartupLook bounds how long a Codex just resumed is watched for its update offer.
	codexStartupLook = 8 * time.Second
	// compactTurnWait bounds how long a message held for a compaction waits for the Agent's turn to end.
	compactTurnWait = 6 * time.Hour
)

// compactRestTicks: the reads in a row an Agent must be at rest without a compaction record for its /compact to count
// as finished without compacting (its record may land just after its status flips).
const compactRestTicks = 10

// compactNote is what a send asking for compaction did, for session.send's answer: held behind the turn for a
// compaction (held), or compacted before it went in (done).
type compactNote struct{ held, done bool }

type compactNoteKey struct{}

func withCompactNote(ctx context.Context) (context.Context, *compactNote) {
	c := new(compactNote)
	return context.WithValue(ctx, compactNoteKey{}, c), c
}

func noteCompact(ctx context.Context, held, done bool) {
	if c, ok := ctx.Value(compactNoteKey{}).(*compactNote); ok {
		c.held, c.done = c.held || held, c.done || done
	}
}

// compactPlan is what a send asking for compaction does.
type compactPlan int

const (
	compactNo       compactPlan = iota // send as without the option
	compactThenSend                    // compact now, then send
	compactHold                        // nearly full mid-turn: hold the message, decide again once the turn ends
)

// planCompaction decides a send's compaction: only one that asked, to an Agent Persona can compact, nearly full.
func planCompaction(asked, compactable, busy, full bool) compactPlan {
	switch {
	case !asked || !compactable || !full:
		return compactNo
	case busy:
		return compactHold
	default:
		return compactThenSend
	}
}

// compactableRuntime says whether Persona can compact session id with the runtime's own command: Codex on its
// app-server, and Claude Code or Codex in one of Persona's terminals. Otherwise why not, for the log and refusals.
func (n *Node) compactableRuntime(id string) (bool, string) {
	if n.externalCodex(id) || n.codexJoined(id) {
		if _, _, err := n.codexOwner(id); errors.Is(err, errCodexNoOwner) {
			if src, ok := n.codexSource(id); ok && codexDesktopChat(src) {
				// The Codex App's chat: its messages go in as the App's turns (codex_desktop_queue.go), which run no
				// slash command, and the App offers no compaction call: the App compacts on its own.
				return false, "the Codex App runs it, which takes no /compact from Persona"
			}
		}
		return true, ""
	}
	a, ok := n.agent(id)
	if !ok {
		return false, "it does not run in one of Persona's terminals"
	}
	if host.ParsePane(a.PaneID).Kind == host.Botmux {
		return false, "botmux runs it"
	}
	switch a.Agent {
	case "claude", "codex":
		return true, ""
	case "":
		return false, "its runtime is not known"
	}
	return false, a.Agent + " has no compaction Persona can run"
}

// compactBusy reports whether session id is in a turn (working, or held by a dialog inside it) and whether it still
// runs at all.
func (n *Node) compactBusy(id string) (busy, live bool) {
	if n.externalCodex(id) || n.codexJoined(id) {
		if _, t, err := n.codexOwner(id); err == nil {
			return t.status == proto.StatusWorking || t.status == proto.StatusBlocked, true
		}
	}
	a, ok := n.agent(id)
	if !ok {
		return false, false
	}
	return a.AgentStatus == proto.StatusWorking || a.AgentStatus == proto.StatusBlocked, true
}

// contextUse is session id's context as its transcript says it now: what the latest reply saw and the model's window
// (0 when either is unknown, as after a compaction until the next reply).
func (n *Node) contextUse(id string) (tokens, window int) {
	path := n.transcriptPath(id)
	if path == "" {
		return 0, 0
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0, 0
	}
	m := n.metaNow(path, st)
	if m.contextTokens <= 0 {
		return 0, 0
	}
	return m.contextTokens, n.sessionContextWindow(id, "", nil, m)
}

// sendCompacting is session.send with CompactIfFull: compacted first when nearly full (planCompaction), else n.send.
func (n *Node) sendCompacting(ctx context.Context, id, text string, images []proto.Image, opts sendOpts) error {
	compactable, why := n.compactableRuntime(id)
	busy, _ := n.compactBusy(id)
	tokens, window := n.contextUse(id)
	full := proto.ContextNearlyFull(tokens, window)
	switch planCompaction(true, compactable, busy, full) {
	case compactHold:
		n.logf("session %s: context %d of %d, mid-turn: the message waits for the turn, then a compaction", short(id), tokens, window)
		markSendQueued(ctx)
		noteCompact(ctx, true, false)
		go n.sendAfterTurn(id, text, images, opts)
		return nil
	case compactThenSend:
		unlock, err := n.inputTurn(ctx, id)
		if err != nil {
			return err
		}
		defer unlock()
		return n.compactThenSendLocked(ctx, id, text, images, opts)
	}
	if full && !compactable {
		n.logf("session %s: context %d of %d, but %s: sent without compacting", short(id), tokens, window, why)
	}
	return n.send(ctx, id, text, images, opts)
}

// compactThenSendLocked compacts session id when it is still nearly full, then sends; with its input lock held.
func (n *Node) compactThenSendLocked(ctx context.Context, id, text string, images []proto.Image, opts sendOpts) error {
	if tokens, window := n.contextUse(id); proto.ContextNearlyFull(tokens, window) {
		if ok, _ := n.compactableRuntime(id); ok {
			began := time.Now()
			n.logf("session %s: context %d of %d tokens: compacting before the message goes in", short(id), tokens, window)
			if err := n.compactLocked(ctx, id); err != nil {
				n.logf("session %s: compaction before the message failed after %s: %v", short(id), time.Since(began).Round(time.Second), err)
				data := map[string]any{}
				if _, live := n.agent(id); !live {
					// It quit on a step of Persona's, not the user's: the assistant may resume it again (#11441).
					n.state.stopped(id, proto.StopCompactFailed)
					data["agent_quit"] = true
				}
				return refusal(proto.CodeCompactFailed, fmt.Sprintf("its context is nearly full (%d of %d tokens) and compacting it "+
					"failed: %v. The message was not sent", tokens, window, err), data)
			}
			n.logf("session %s: compacted in %s; the message goes in now", short(id), time.Since(began).Round(time.Second))
			noteCompact(ctx, false, true)
		}
	}
	return n.sendLocked(ctx, id, text, images, opts)
}

// sendAfterTurn is a held message (compactHold): once the Agent's turn has ended, compacted if still nearly full, then
// sent. One that cannot go in leaves the session waiting for the user with why (lostFirst), as a first message that
// did not go in does: nobody waits on this call any more.
func (n *Node) sendAfterTurn(id, text string, images []proto.Image, opts sendOpts) {
	ctx, cancel := context.WithTimeout(context.Background(), compactTurnWait+compactBeforeSendDeadline)
	defer cancel()
	fail := func(err error) {
		n.logf("session %s: the message held for a compaction did not go in: %v", short(id), err)
		n.lostFirst.set(id, fmt.Sprintf(lostCompact, err), time.Now())
	}
	if err := n.waitTurnEnd(ctx, id); err != nil {
		fail(err)
		return
	}
	unlock, err := n.inputTurn(ctx, id)
	if err != nil {
		fail(err)
		return
	}
	defer unlock()
	if err := n.compactThenSendLocked(ctx, id, text, images, opts); err != nil {
		fail(err)
	}
}

// lostCompact is why a session waits for the user when a message held for a compaction did not go in.
const lostCompact = "上下文快满了，消息要等压缩完再送，但没有送进去（%v）：看看电脑上的终端，再把消息重发一遍。" // i18n-ignore: node status text, as the other lostFirst reasons

// waitTurnEnd waits until session id is no longer in a turn (compactTurnWait at most).
func (n *Node) waitTurnEnd(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, compactTurnWait)
	defer cancel()
	tick := time.NewTicker(compactPoll)
	defer tick.Stop()
	for {
		busy, live := n.compactBusy(id)
		switch {
		case !live:
			return fmt.Errorf("the agent stopped before its turn ended")
		case !busy:
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("its turn did not end within %s", compactTurnWait)
		case <-tick.C:
		}
	}
}

// compactLocked types /compact into session id (sendLocked: a joined Codex's thread/compact/start) and waits for it
// to finish, as compactProgress reads it; with its input lock held.
func (n *Node) compactLocked(ctx context.Context, id string) error {
	path := n.transcriptPath(id)
	var off int64
	if st, err := os.Stat(path); err == nil {
		off = st.Size()
	}
	before, _ := n.contextUse(id)
	if err := n.codexComposerReady(ctx, id); err != nil {
		return err
	}
	pane := ""
	if a, ok := n.agent(id); ok {
		pane = a.PaneID
	}
	if err := n.sendLocked(ctx, id, "/compact", nil, sendOpts{}); err != nil {
		if !maybeDelivered(err) {
			return fmt.Errorf("/compact was not taken: %w", err)
		}
		// Typed, but its screen did not confirm it in time (a Codex busy compacting a large context shows no empty box):
		// what the transcript and the agent's status say decides, as for any compaction.
		n.logf("session %s: /compact typed, not confirmed on screen (%v): waiting for its record", short(id), err)
	}
	err := waitCompaction(ctx, func() compactProgress {
		busy, live := n.compactBusy(id)
		p := compactProgress{busy: busy, live: live}
		if path == "" {
			path = n.transcriptPath(id) // a session whose record appeared since
		}
		p.compacted, p.grew, p.reason = compactionSince(path, off)
		if !p.compacted && before > 0 {
			// A reply after the compaction saw far less than before it: the context did shrink, whatever the record.
			if now, _ := n.contextUse(id); now > 0 && contextShrank(before, now) {
				p.compacted = true
			}
		}
		return p
	}, compactPoll, compactStartGrace, compactBeforeSendDeadline)
	if errors.Is(err, errCompactAgentGone) && pane != "" {
		return n.agentExited(ctx, pane, "after /compact was typed, before any compaction was recorded")
	}
	return err
}

// errCompactAgentGone: the agent left its terminal while it was to compact.
var errCompactAgentGone = errors.New("the agent stopped while compacting")

// contextShrank: now is at most compactShrinkShare of before, as only a compaction makes a context.
func contextShrank(before, now int) bool {
	return before > 0 && now > 0 && float64(now) <= compactShrinkShare*float64(before)
}

// compactShrinkShare: the share of the context before a /compact that a reply after it may see for the drop to count
// as the compaction.
const compactShrinkShare = 0.6

// codexComposerReady waits, for a Codex in one of Persona's terminals that does not go through its app-server, until
// its TUI shows an empty input box: a Codex just resumed (codex resume <id>) reads its whole rollout first, and a
// /compact typed before the box is there is lost. Codex's own update offer (default: Update now, which installs and
// quits) is skipped with Esc as a start skips it (codexSkipUpdateKeys, #11316); any other dialog with numbered options
// is never taken for the box, and refuses with its first line, as does a draft.
func (n *Node) codexComposerReady(ctx context.Context, id string) error {
	if n.externalCodex(id) || n.codexJoined(id) {
		return nil
	}
	a, ok := n.agent(id)
	if !ok || a.Agent != "codex" {
		return nil
	}
	wait, cancel := context.WithTimeout(ctx, codexComposerWait)
	defer cancel()
	tick := time.NewTicker(compactPoll)
	defer tick.Stop()
	for {
		if _, live := n.agent(id); !live {
			return n.agentExited(ctx, a.PaneID, "after it was resumed, before it showed its input box")
		}
		if text, err := n.readPane(wait, a.PaneID, "visible", screen.DialogLines); err == nil {
			switch skip, dialog := codexStartupScreen(text); {
			case skip != nil:
				if err := n.sendKeys(wait, a.PaneID, skip...); err != nil {
					return fmt.Errorf("could not skip Codex's update offer: %w", err)
				}
				n.logf("session %s: skipped Codex's update offer before /compact", short(id))
				sleepCtx(wait, codexDialogPause)
				continue
			case dialog != "":
				return fmt.Errorf("/compact was not typed: a dialog is open in its terminal (%s)", dialog)
			}
		}
		if sc, err := n.codexScreen(wait, a.PaneID); err == nil && sc.View == screen.CodexComposer {
			if sc.Draft != "" {
				return fmt.Errorf("/compact was not typed: %w", refusal(screen.InputDraft, codexDraftMessage(sc.Draft), nil))
			}
			return nil
		} else if err == nil && (sc.View == screen.CodexApproval || sc.View == screen.CodexQuestion || sc.View == screen.CodexDialog) {
			return fmt.Errorf("/compact was not typed: a dialog is open in its terminal")
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("its Codex did not show its input box within %s", codexComposerWait)
		case <-tick.C:
		}
	}
}

// codexStartupScreen reads a Codex pane's visible text for what stands before its input box: the keys that skip its
// update offer (codexSkipUpdateKeys), else the first line of any other dialog with numbered options, else neither.
func codexStartupScreen(text string) (skip []string, dialog string) {
	if keys := codexUpdateOfferKeys(text); keys != nil {
		return keys, ""
	}
	if d := screen.ParseDialog(text); d != nil && len(d.Options) > 0 {
		head := d.Options[0].Label
		if q := strings.TrimSpace(d.Question); q != "" {
			head = q
		}
		return nil, clipLine(head, 120)
	}
	return nil, ""
}

// codexUpdateOfferKeys are the keys that skip Codex's update offer on screen text: codexSkipUpdateKeys, and the same
// three choices however the screen reads otherwise. Its "› 1. Update now" row can pass for the input box's prompt, and
// that is how a /compact once picked Update now and quit Codex (#11316).
func codexUpdateOfferKeys(text string) []string {
	if keys := codexSkipUpdateKeys(text); keys != nil {
		return keys
	}
	if !strings.Contains(screen.Plain(text), "Update available") {
		return nil
	}
	d := screen.ParseDialog(text)
	if d == nil || len(d.Options) != 3 {
		return nil
	}
	if first := d.Options[0].Label; (first == "Update now" || strings.HasPrefix(first, "Update now (")) &&
		d.Options[1].Label == "Skip" && d.Options[2].Label == "Skip until next version" {
		return []string{"escape"}
	}
	return nil
}

// clipLine is s on one line, at most n runes.
func clipLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// settleCodexStartup is what a Codex just started or resumed in pane may show before its input box: its update offer
// is skipped (Esc) when it shows within codexStartupLook; anything else is left for the next send's checks.
func (n *Node) settleCodexStartup(ctx context.Context, pane string) {
	look, cancel := context.WithTimeout(ctx, codexStartupLook)
	defer cancel()
	for {
		if text, err := n.readPane(look, pane, "visible", screen.DialogLines); err == nil {
			if keys := codexUpdateOfferKeys(text); keys != nil {
				if err := n.sendKeys(look, pane, keys...); err == nil {
					n.logf("pane %s: skipped Codex's update offer after its start", pane)
				}
				return
			}
			if screen.ReadCodex(text).View == screen.CodexComposer {
				return
			}
		}
		if !sleepCtx(look, codexStep) {
			return
		}
	}
}

// agentExited is the error for an agent that left its pane at step, with the pane's last lines (an update that
// ran, a crash) so the refusal says where and why it quit.
func (n *Node) agentExited(ctx context.Context, pane, step string) error {
	shown, _ := n.readPane(context.WithoutCancel(ctx), pane, "recent-unwrapped", 30)
	if last := strings.TrimSpace(lastLines(shown, 6)); last != "" {
		return fmt.Errorf("the agent quit %s; its terminal shows:\n%s", step, last)
	}
	return fmt.Errorf("the agent quit %s", step)
}

// compactProgress is one read of a compaction under way.
type compactProgress struct {
	compacted  bool   // the transcript has a compaction record after the /compact, or the context shrank since
	grew       bool   // the transcript was written to after the /compact: it is at work on it
	busy, live bool   // the Agent works; it still runs
	reason     string // an error the runtime wrote about it, "" when none
}

// waitCompaction reads probe every poll until the compaction is over: nil once a record is there and the Agent rests,
// else why it failed (the Agent stopped, came back to rest without a record, never began within grace, or deadline).
func waitCompaction(ctx context.Context, probe func() compactProgress, poll, grace, deadline time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	began := time.Now()
	started, rest := false, 0
	for {
		p := probe()
		switch {
		case !p.live:
			return errCompactAgentGone
		case p.compacted && !p.busy:
			return nil
		case p.busy || p.compacted:
			started, rest = true, 0
		case !started && p.reason != "":
			return fmt.Errorf("it did not compact: %s", p.reason)
		case started:
			if rest++; rest >= compactRestTicks {
				if p.reason != "" {
					return fmt.Errorf("it finished /compact without compacting: %s", p.reason)
				}
				return fmt.Errorf("it finished /compact without compacting")
			}
		case p.grew:
			// Written to, never seen at work: a Codex whose status lags its rollout. Its record or the deadline decides.
		case time.Since(began) >= grace:
			if p.reason != "" {
				return fmt.Errorf("it did not start compacting: %s", p.reason)
			}
			return fmt.Errorf("it did not start compacting within %s", grace)
		}
		select {
		case <-ctx.Done():
			if p.reason != "" {
				return fmt.Errorf("no compaction record within %s: %s", deadline, p.reason)
			}
			return fmt.Errorf("no compaction record within %s", deadline)
		case <-tick.C:
		}
	}
}

// compactMarks are the transcript lines a compaction writes: Claude Code's compact_boundary, a Codex rollout's
// compacted item and its context_compacted event.
var compactMarks = [][]byte{[]byte(`"subtype":"compact_boundary"`), []byte(`"type":"compacted"`), []byte(`"type":"context_compacted"`)}

// compactErrorRE: what Claude Code writes when its /compact fails ("Error during compaction: …").
var compactErrorRE = lazyre.New(`(?i)error (?:during|while) compact[^"\\]{0,200}`)

// compactionSince reads transcript path from offset off: whether a compaction record is there, whether anything was
// written since, and the error the runtime wrote about a failed one.
func compactionSince(path string, off int64) (compacted, grew bool, reason string) {
	if path == "" {
		return false, false, ""
	}
	f, err := os.Open(path)
	if err != nil {
		return false, false, ""
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return false, false, ""
	}
	b, err := io.ReadAll(io.LimitReader(f, 64<<20))
	if err != nil {
		return false, false, ""
	}
	grew = len(bytes.TrimSpace(b)) > 0
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		compact := bytes.ReplaceAll(line, []byte(`": "`), []byte(`":"`))
		for _, m := range compactMarks {
			if bytes.Contains(compact, m) {
				return true, grew, ""
			}
		}
	}
	if all := compactErrorRE.FindAll(b, -1); len(all) > 0 {
		reason = strings.TrimSpace(string(all[len(all)-1]))
	}
	return false, grew, reason
}
