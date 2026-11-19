package node

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"sync"
	"time"
)

// The background supervisor's rounds (#7315, docs/assistant/unified-entry.md "Each round starts clean"). The manager
// marks a supervision message to the background supervisor as a round (assistant.send supervise_round). The node then
// stops the conversation's idle process and resumes it at the end of its first turn, `claude -p --resume <id>
// --resume-session-at <uuid>`, so the model reads the start brief and this round, not every round before it. The
// conversation id, its file and its row stay the same: Claude Code appends the round to the same file as a branch from
// that line, which the transcript reader keeps in view (transcript/branch.go), and a later plain --resume (the owner
// writing there) continues the latest round. The round's process gets only the tools supervision uses: Persona's own
// MCP server, told PERSONA_TOOL_SET=supervise (the manager narrows the list it gives), and the built-ins
// roundBuiltinTools.
//
// Every assistant.send says which tool set it needs: supervise_round asks for the round's, and its absence (the owner's
// message, a supervision message once assistant.background_supervisor_keeps_context is on, a manager from before
// #7315) for every tool. A round's process that has settled (not working, workingLocked, and silent for
// assistantSettle) when a message needing every tool comes is replaced by a plain --resume with every tool and no round
// state, so it is compacted when idle again (stopSettled; a fresh round stops the process the same way). Replacing one
// still working (a turn, one Claude Code started, background tasks, a compaction, a message it queued or has not
// reported on yet) would lose that work, so there is no second queue at the node: a round's process reports itself
// working until it settled (assistantSessions), and the manager keeps its supervision messages in its persisted relay
// queue meanwhile. A message that needs every tool and comes anyway (the owner's, a relay racing the row) while the
// process runs a turn waits for that turn, up to roundTurnWait (30 s), inside its send and holding the conversation's
// locks, then goes into a plain resume with every tool (awaitRoundTurn); a Stop meanwhile ends the round at once and the
// message goes in once that process exited (stopAwaited). Past the wait, with only background tasks running, or as Claude Code begins a turn of
// its own after the wait looked, the message goes into the working process as Claude Code queues it (the owner's
// preempting a background round, as assistant/owner_preempt.go says) and runs once on the round's tools; the process is
// then marked to retire and is let go once it settled (roundSettled), so the next message starts a process with every
// tool.
//
// --resume-session-at is a print-mode flag Claude Code does not document. It is used only where it was measured: Claude
// Code (not Codex, an agent runtime or a guest), roundMinClaude or later, an idle
// process with no background tasks, and an anchor found on the transcript's latest chain (roundAnchor). Anything else
// sends as before and says why in the answer (context "kept"); a round whose process does not start is sent once more
// as before. A round's process has started when it prints its init (readAssistant): one that refuses the anchor prints
// only a result with no turn and exits 1.

// The answer's context: the round started from the first turn (fresh), or went in as before (kept), and why.
const (
	roundFresh          = "fresh"
	roundKept           = "kept"
	roundWhyBusy        = "busy"         // the process works, or began to before it settled (workingLocked, stopSettled)
	roundWhyUnsupported = "unsupported"  // not Claude Code on the CLI, or one older than roundMinClaude
	roundWhyNoAnchor    = "no_anchor"    // no first turn on the latest chain (a compaction cut it off, or none ended)
	roundWhyStartFailed = "start_failed" // the round's process did not start: resumed as before
)

// roundMinClaude is the first Claude Code --resume-session-at was measured on (2.1.322); older ones keep the context.
var roundMinClaude = [3]int{2, 1, 322}

// roundBuiltinTools are a round's built-in tools: what supervision uses (reading evidence, gh in Bash, a skill), none of
// the others the assistant gets (assistantBuiltinTools), and ToolSearch, which --tools filters too: with nothing
// deferred Claude Code lists no ToolSearch (measured on 2.1.322, where leaving it out added a DeferredToolPlaceholder
// tool instead), and a Persona tool the manager defers can still be loaded (#5839).
const roundBuiltinTools = "Bash,Read,Write,Skill,ToolSearch"

// roundToolSet names the round's tool set to the manager (PERSONA_TOOL_SET, mcp.tools tool_set).
const roundToolSet = "supervise"

// spawnRound is how a process starts for a round: resumed at anchor, with tool set toolSet. The zero value is an
// ordinary start, with every tool: what any message that is no round starting from the first turn needs.
type spawnRound struct{ anchor, toolSet string }

// roundResult is what became of a round asked for by one assistant.send, for its answer.
type roundResult struct {
	mu      sync.Mutex
	context string // roundFresh or roundKept
	why     string
	bytes   int64 // the transcript's size when the round was decided (the manager replaces an outgrown one)
	decided bool  // false while nothing was decided (the send did not run)
}

type roundKey struct{}

// withSuperviseRound marks ctx as an assistant.send of a round, with what becomes of it.
func withSuperviseRound(ctx context.Context) (context.Context, *roundResult) {
	r := &roundResult{}
	return context.WithValue(ctx, roundKey{}, r), r
}

// superviseRoundFrom is the round ctx's send was asked for, nil for any other send.
func superviseRoundFrom(ctx context.Context) *roundResult {
	r, _ := ctx.Value(roundKey{}).(*roundResult)
	return r
}

func (r *roundResult) set(context, why string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.context, r.why, r.decided = context, why, true
	r.mu.Unlock()
}

// keptBusy: the round goes in as before because the conversation's process works (roundWhyBusy), so it joins that
// process (assistantSend).
func (r *roundResult) keptBusy() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.decided && r.context == roundKept && r.why == roundWhyBusy
}

func (r *roundResult) sized(n int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.bytes = n
	r.mu.Unlock()
}

// answer puts the round's outcome on the send's answer: context, why when it was kept, the transcript's size. Nothing
// when no round was asked for or the send did not run (the same once_id asked again).
func (r *roundResult) answer(out map[string]any) map[string]any {
	if r == nil {
		return out
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.decided {
		return out
	}
	out["context"] = r.context
	if r.why != "" {
		out["why"] = r.why
	}
	if r.bytes > 0 {
		out["transcript_bytes"] = r.bytes
	}
	return out
}

// superviseRound decides how the round r for conversation id goes in: from its first turn (a spawnRound with the anchor,
// after which the caller stops p, if any, once it settled, and starts anew) or as before (the zero spawnRound, r says why). p is the
// conversation's process (nil for none), agent the launcher's kind and guest whether it is a guest conversation.
func (n *Node) superviseRound(ctx context.Context, r *roundResult, id string, p *assistantProc, agent string, guest bool) spawnRound {
	kept := func(why string) spawnRound {
		r.set(roundKept, why)
		n.logf("assistant %s: supervision round goes in as before (%s)", short(id), why)
		return spawnRound{}
	}
	if id == "" || agent != "claude" || guest {
		return kept(roundWhyUnsupported)
	}
	version := ""
	if p != nil && p.alive() {
		p.mu.Lock()
		busy := p.workingLocked()
		other := p.agent != "claude"
		version = p.claudeVersion
		p.mu.Unlock()
		if other {
			return kept(roundWhyUnsupported)
		}
		if busy {
			return kept(roundWhyBusy)
		}
	}
	path := n.transcriptPath(id)
	if path == "" {
		return kept(roundWhyNoAnchor)
	}
	tr, err := roundAnchor(path)
	if err != nil {
		n.logf("assistant %s: read its transcript for the round: %v", short(id), err)
		return kept(roundWhyNoAnchor)
	}
	r.sized(tr.size)
	if version = cmp.Or(version, tr.version); version == "" {
		version = n.claudeV.get(ctx)
	}
	if !versionAtLeast(version, roundMinClaude) {
		return kept(roundWhyUnsupported)
	}
	if tr.anchor == "" {
		return kept(roundWhyNoAnchor)
	}
	r.set(roundFresh, "")
	return spawnRound{anchor: tr.anchor, toolSet: roundToolSet}
}

// workingLocked: stopping the process now would lose something it runs or holds (p.mu held): a turn, a background
// task, a compaction (an idle one, or the user's, which sets no busy flag, assistant_compact.go), or a message Claude
// Code queued and has not started, or was written and Claude Code has said nothing of yet (its lifecycle lines may
// still wait in the pipe behind a turn's result, or Claude Code may not have read it). A turn's result clears busy
// before the queued message's turn starts (assistant_queue.go), and a new process would never get that message; once
// Claude Code says it started, the turn is busy (commandLifecycle), as is a turn Claude Code starts on its own from its
// init (readAssistant). A round goes in as before then (superviseRound), and so does a message that needs every tool
// (assistantSend).
func (p *assistantProc) workingLocked() bool {
	return p.busy || len(p.tasks) > 0 || p.compacting || p.manual || slices.ContainsFunc(p.written, p.holdsLocked)
}

// holdsLocked: Claude Code may hold message w, written to p (p.mu held): it said it queued it, or says what became of
// every message (lifecycle) and has said nothing of this one yet, for up to assistantInputKeep. One that never says
// anything (no command_lifecycle) holds none it did not queue.
func (p *assistantProc) holdsLocked(w assistantInput) bool {
	return w.queued || p.lifecycle && time.Since(w.at) <= assistantInputKeep
}

// assistantSettle is how long a process that is not working (workingLocked) prints nothing before the node takes it for
// settled. Claude Code starts a turn of its own for a background task's notification right after a turn's result or
// its task list emptying (measured on 2.1.322: its init 7 ms after the result, 26 ms after the list emptied), and
// nothing shows that turn before its init: stopping the process in between would cut it off.
const assistantSettle = time.Second

// settledLocked: p is not working and printed nothing for assistantSettle (p.mu held). A round's process reports itself
// working until then (assistantSessions), so the manager keeps supervision messages for it in its relay queue
// (assistant/conversations.go drainRelays) until it settled.
func (p *assistantProc) settledLocked() bool {
	return !p.workingLocked() && p.quiet() >= assistantSettle
}

// printed notes that p printed a line now (readAssistant).
func (p *assistantProc) printed() {
	p.lastOut.Store(int64(time.Since(p.born)))
}

// lineRead notes that the reader is done with a line and back at the pipe (readAssistant): the quiet counts from now.
func (p *assistantProc) lineRead() {
	p.printed()
	p.reading.Add(-1)
}

// quiet is how long ago p printed its last line (printed) and the reader was done with it (lineRead), or was born
// when it printed none; none while the reader is on a line, as a line behind it (a turn's init) is not read yet. Both
// are offsets from born on the monotonic clock: a step of the wall clock (an NTP correction, a suspended machine's
// clock catching up) neither takes a process for settled while it prints nor keeps one unsettled.
func (p *assistantProc) quiet() time.Duration {
	if p.reading.Load() > 0 {
		return 0
	}
	return time.Since(p.born) - time.Duration(p.lastOut.Load())
}

// stopSettled stops p, the conversation's process, once it settled, waiting up to assistantSettle for it; the caller
// holds the conversation's assistantLife lock, so no send starts a turn meanwhile. The last look and the stop are taken
// under p.mu, which the reader takes to mark a turn Claude Code starts on its own (readAssistant). True when p is
// stopped or had exited; false, with p left running, when it works by then, printed all along, is no longer the
// conversation's process, or ctx ended.
func (n *Node) stopSettled(ctx context.Context, p *assistantProc) bool {
	deadline := time.Now().Add(assistantSettle)
	for {
		n.mu.Lock()
		p.mu.Lock()
		alive, current := p.alive(), n.procs[p.id] == p
		working, wait := alive && p.workingLocked(), assistantSettle-p.quiet()
		stop := !alive || current && !working && wait <= 0
		if stop && current {
			delete(n.procs, p.id)
		}
		n.mu.Unlock()
		if stop && alive {
			p.stop()
		}
		p.mu.Unlock()
		if wait = min(wait, time.Until(deadline)); stop || !current || working || wait <= 0 {
			return stop
		}
		select {
		case <-time.After(wait):
		case <-p.exited:
		case <-ctx.Done():
			return false
		}
	}
}

// settleSoon has p, a round's process that just printed a line, looked at once it may have settled (roundSettled).
func (n *Node) settleSoon(p *assistantProc) {
	p.mu.Lock()
	armed := p.settleArmed
	p.settleArmed = true
	p.mu.Unlock()
	if !armed {
		time.AfterFunc(assistantSettle, func() { p.ownerOr(n).roundSettled(p) })
	}
}

// roundSettled looks at p, a round's process, until it settled: then its row goes to the manager as no longer working
// (assistantSessions), so a supervision message it kept back goes in now, and p, marked to retire, is let go, so the
// conversation's next message starts a process with every tool (assistantSend). Under the conversation's assistantLife
// lock, as a send replaces a process; stopSettled looks again.
func (n *Node) roundSettled(p *assistantProc) {
	p.mu.Lock()
	if !p.alive() {
		p.settleArmed = false
		p.mu.Unlock()
		return
	}
	if !p.settledLocked() {
		wait := assistantSettle // working: what ends it may print nothing (a recovery giving up, a compaction timing out)
		if !p.workingLocked() {
			wait -= p.quiet()
		}
		p.mu.Unlock()
		time.AfterFunc(wait, func() { p.ownerOr(n).roundSettled(p) })
		return
	}
	p.settleArmed = false
	retire := p.retire
	p.mu.Unlock()
	if retire {
		unlock := n.assistantLife.lock(p.id)
		// A send may have replaced p while this waited for the lock: stopSettled says true for p having exited then
		// too, and the background tasks remembered for the conversation are its new process's (recoverAssistantTasks
		// tells it of them after a node restart). Every process of the conversation starts under this lock, so p is
		// still its process when stopSettled looks.
		n.mu.Lock()
		current := n.procs[p.id] == p
		n.mu.Unlock()
		if current && n.stopSettled(context.Background(), p) {
			n.rememberTasks(p.id, nil) // settled: no background task
			n.logf("assistant %s: the supervision round's process that took a message needing every tool settled: let go, the next message starts with every tool", short(p.id))
		}
		unlock()
	}
	n.notifySessionsSoon()
}

// roundTurnWait bounds how long a message that needs every tool waits for a round's process to end its turn
// (awaitRoundTurn) before it joins that process on the round's tools, as it did without the wait. The node's RPC carries
// no deadline: the hub waits 90 s for an app's message to a working row (hub/send.go sendWait) and the manager's drain
// 90 s (assistant/conversations.go drainRelays); after the wait come the settle second and the start (assistantStartWait),
// and a message sent right behind this one waits for all of it on the typing lock. A variable so tests can shorten it.
var roundTurnWait = 30 * time.Second

// roundTurnPoll is how often awaitRoundTurn looks again.
const roundTurnPoll = 50 * time.Millisecond

// roundStopExitWait bounds how long a message waits for a round's process a Stop ended while it waited (stopAwaited)
// to exit: stop() kills it 5 s on, and a margin. One still there by then outlived its kill, and the wait ends anyway
// rather than hold the conversation's locks for good.
const roundStopExitWait = 10 * time.Second

// awaitRoundTurn: a message that needs every tool came for p, a round's process: wait while p runs a turn (busy, a
// compaction, a message Claude Code holds), up to roundTurnWait, so the message goes into a plain resume with every tool
// once p settled (assistantSend, stopSettled) rather than into p on the round's tools. The caller holds the
// conversation's typing and assistantLife locks: messages sent after it wait behind it, in their order, and nothing
// starts a turn in p meanwhile. Not for background tasks alone (they may run for hours), nor once an earlier message
// went into p (retire): this one joins it behind that one. A recovery of p's turn that begins meanwhile is cancelled,
// as the message would have cancelled it had it come a moment later (assistantSend); a Stop ends p at once
// (assistantInterrupt), and the message goes in once p exited. A wait says in one log line how it ended and how long it
// took.
func (n *Node) awaitRoundTurn(ctx context.Context, p *assistantProc) {
	start := time.Now()
	deadline := start.Add(roundTurnWait)
	p.mu.Lock()
	if p.retire {
		p.mu.Unlock()
		return
	}
	p.awaited = true
	p.mu.Unlock()
	waited, outcome := false, ""
	var stopExit time.Time // set once a Stop ended p: until when the wait goes on for p to exit
	defer func() {
		p.mu.Lock()
		p.awaited, p.awaitStopped = false, false
		p.mu.Unlock()
		if waited {
			n.logf("assistant %s: a message that needs every tool waited %s for a supervision round's turn: %s", short(p.id),
				time.Since(start).Round(time.Millisecond), outcome)
		}
	}()
	tick := time.NewTicker(roundTurnPoll)
	defer tick.Stop()
	for {
		n.mu.Lock()
		current, recovering := n.procs[p.id] == p, n.recoveries[p.id] != nil
		n.mu.Unlock()
		if recovering {
			n.cancelAssistantRecovery(p.id)
		}
		// The look and the end of the wait are one step under p's lock, as stopAwaited takes it: a Stop either finds the
		// message waiting and ends p, or finds it gone on and takes the usual way.
		p.mu.Lock()
		if p.awaitStopped && stopExit.IsZero() {
			stopExit = time.Now().Add(roundStopExitWait)
		}
		switch {
		case !p.alive():
			outcome = "exited"
		case p.awaitStopped && time.Now().Before(stopExit):
			// stopAwaited stopped it: wait until it exited, past the bound and whatever it prints on its way out
			// (its turn's result), so the message never goes into a process that is going away (its input closed, a
			// message written there is lost).
		case !current:
			outcome = "replaced"
		case !(p.busy || p.compacting || p.manual || slices.ContainsFunc(p.written, p.holdsLocked)):
			outcome = "turn_ended"
		case !time.Now().Before(deadline):
			outcome = "bound"
		}
		if outcome != "" {
			if p.awaitStopped { // stopAwaited ended it, whatever the process did on its way out
				outcome = "stopped"
			}
			p.awaited, p.awaitStopped = false, false
		}
		p.mu.Unlock()
		if outcome != "" {
			return
		}
		waited = true
		select {
		case <-tick.C:
		case <-p.exited:
		case <-ctx.Done():
			outcome = "ctx"
			return
		}
	}
}

// stopAwaited is a Stop for conversation id while a message waits for its round's process to end its turn
// (awaitRoundTurn): that process stops now, under its lock so the wait cannot end in between, and the message goes into
// a plain resume with every tool. True when it did; false leaves the Stop to go on as ever.
func (n *Node) stopAwaited(id string) bool {
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.awaited {
		return false
	}
	p.awaitStopped = true
	p.stop()
	return true
}

// roundTranscript is what roundAnchor read in a transcript: the anchor ("" for none), the Claude Code version that wrote
// its last line, and its size.
type roundTranscript struct {
	anchor, version string
	size            int64
}

// roundLine is what roundAnchor reads of a line.
type roundLine struct {
	Type      string  `json:"type"`
	UUID      string  `json:"uuid"`
	Parent    *string `json:"parentUuid"`
	Sidechain bool    `json:"isSidechain"`
	Meta      bool    `json:"isMeta"`
	Summary   bool    `json:"isCompactSummary"`
	Version   string  `json:"version"`
	Message   *struct {
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
}

// prompt says the line is a message someone wrote to the conversation: a user line that is no tool result, meta line
// or compaction summary.
func (l roundLine) prompt() bool {
	if l.Type != "user" || l.Sidechain || l.Meta || l.Summary || l.Message == nil {
		return false
	}
	c := bytes.TrimSpace(l.Message.Content)
	if len(c) > 0 && c[0] == '"' {
		return true
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(c, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return false
		}
	}
	return len(blocks) > 0
}

// roundAnchor finds where a round resumes in the transcript at path: the last assistant line that ended a turn
// (stop_reason end_turn) before the second message, so the round reads the start message and its answer, not the start
// message merged into its own prompt. It counts only when it lies on the chain Claude Code resumes, walked back from
// the last user or assistant line: a compaction starts a new chain (its first turn is no longer an ancestor, and
// Claude Code refuses the anchor), and so does a file whose chain is broken. Sidechain lines (subagents) are not the
// conversation's.
func roundAnchor(path string) (roundTranscript, error) {
	f, err := os.Open(path)
	if err != nil {
		return roundTranscript{}, err
	}
	defer f.Close()
	var out roundTranscript
	if st, err := f.Stat(); err == nil {
		out.size = st.Size()
	}
	up := map[string]string{} // line → its parent ("" for a chain's start)
	messages := 0
	candidate, leaf := "", ""
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var l roundLine
			if json.Unmarshal(line, &l) == nil {
				if l.Version != "" {
					out.version = l.Version
				}
				if l.UUID != "" && !l.Sidechain {
					if _, seen := up[l.UUID]; !seen {
						parent := ""
						if l.Parent != nil {
							parent = *l.Parent
						}
						up[l.UUID] = parent
					}
					if l.prompt() {
						messages++
					}
					if l.Type == "user" || l.Type == "assistant" {
						leaf = l.UUID
					}
					if messages == 1 && l.Type == "assistant" && l.Message != nil && l.Message.StopReason == "end_turn" {
						candidate = l.UUID
					}
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
	}
	if candidate == "" || leaf == "" {
		return out, nil
	}
	seen := map[string]bool{}
	for u := leaf; u != "" && !seen[u]; u = up[u] {
		if u == candidate {
			out.anchor = candidate
			break
		}
		seen[u] = true
	}
	return out, nil
}
