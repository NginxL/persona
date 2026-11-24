package node

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// A turn whose end the node never saw (#14351). A conversation's row says working while its process works
// (assistantSessions: busy, or a round's process that has not settled), and everything that waits for the
// conversation reads that row: the manager holds the messages other conversations send it until the row stops saying
// working (assistant/conversations.go drainRelays), and a deployment waits for the main conversation to be idle. The
// stream's lines are what clear it (readAssistant: the turn's result; commandLifecycle), and a line that never comes or
// comes in an order the node does not expect leaves working behind for good: the main conversation once said working
// for hours with no turn running, and its next turn's result was what ended it.
//
// So the row is checked against what runs, where it is computed, rather than by everyone who reads it. A Claude Code
// process that has said it works and then printed nothing for assistantStaleTurn is looked at, at most every
// assistantStaleRecheck: when Claude Code's own record of the process (sessions/<pid>.json, which it rewrites as each
// turn starts and ends) has said idle for as long, and the transcript's last message ends a turn (an assistant line
// that stopped for anything but a tool call), the turn is over: busy is cleared, and so are the messages it still
// seemed to hold, as an idle Claude Code takes every message it queued at once. Both have to say so: a tool that runs
// for an hour prints nothing either, but its transcript ends on the tool call. Nothing is changed while it has
// background tasks, a compaction, or a model recovery in flight: those keep their own ends.

// assistantStaleTurn is how long a working Claude Code process prints nothing before its turn is checked against
// Claude Code's own record; assistantStaleRecheck how often it is looked at again. Variables so tests can shorten them.
var (
	assistantStaleTurn    = 2 * time.Minute
	assistantStaleRecheck = 30 * time.Second
)

// assistantStaleTail is how much of a transcript's end is read for its last message line.
const assistantStaleTail = 1 << 20

// claudeRuntimeTurn is what Claude Code's own record says of p's turn: busy, idle, shell (idle with background commands)
// or waiting, and since when; ok false when there is no record of this very process. A variable so tests can say.
var claudeRuntimeTurn = func(n *Node, p *assistantProc) (status string, since time.Time, ok bool) {
	return n.claudeSessionFileTurn(p)
}

// claudeSessionFileTurn reads p's sessions/<pid>.json in the config directories Claude Code writes to (claudebg.go),
// the one of this process only (sameProcess: a pid can be reused).
func (n *Node) claudeSessionFileTurn(p *assistantProc) (string, time.Time, bool) {
	p.mu.Lock()
	pid := 0
	if p.cmd != nil && p.cmd.Process != nil {
		pid = p.cmd.Process.Pid
	}
	p.mu.Unlock()
	if pid <= 0 {
		return "", time.Time{}, false
	}
	name := strconv.Itoa(pid) + ".json"
	for _, dir := range n.claudeConfigDirs() {
		f := filepath.Join(dir, "sessions", name)
		st, err := os.Stat(f)
		if err != nil || st.Size() > 64<<10 {
			continue
		}
		c, ok := n.cfiles.parse(f, st)
		if !ok || c.Status == "" || !sameProcess(pid, claudeFileStart(c.ProcStart)) {
			continue
		}
		var since time.Time
		if c.StatusUpdatedAt > 0 {
			since = time.UnixMilli(c.StatusUpdatedAt)
		}
		return c.Status, since, true
	}
	return "", time.Time{}, false
}

// transcriptTurnOver says whether the last message line in the end of the transcript at path ends a turn: an assistant
// line that stopped for anything but a tool call. A user line (a prompt, a tool's result) or an assistant line still
// being written says the turn goes on. known is false when the end read holds no message line.
func transcriptTurnOver(path string) (over, known bool) {
	f, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, false
	}
	from := max(st.Size()-assistantStaleTail, 0)
	buf := make([]byte, st.Size()-from)
	if _, err := f.ReadAt(buf, from); err != nil && err != io.EOF {
		return false, false
	}
	lines := bytes.Split(buf, []byte{'\n'})
	if from > 0 && len(lines) > 0 {
		lines = lines[1:] // cut in the middle
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var l roundLine
		if json.Unmarshal(line, &l) != nil || l.Sidechain || l.Meta || l.Summary || l.Message == nil {
			continue
		}
		switch l.Type {
		case "assistant":
			return l.Message.StopReason != "" && l.Message.StopReason != "tool_use", true
		case "user":
			return false, true
		}
	}
	return false, false
}

// staleWorkingLocked: p says it works only for a turn or a held message, nothing that ends on its own line (p.mu held).
func (p *assistantProc) staleWorkingLocked() bool {
	held := p.busy || slices.ContainsFunc(p.written, p.holdsLocked)
	return held && !p.compacting && !p.manual && len(p.tasks) == 0
}

// reconcileStaleTurn ends p's turn when the node missed its end (see above): true when it did.
func (n *Node) reconcileStaleTurn(p *assistantProc) bool {
	if p == nil || p.agent != "claude" || !p.alive() {
		return false
	}
	n.mu.Lock()
	_, recovering := n.recoveries[p.id]
	n.mu.Unlock()
	if recovering {
		return false
	}
	now := time.Now()
	p.mu.Lock()
	due := p.staleWorkingLocked() && p.quiet() >= assistantStaleTurn && now.Sub(p.staleLooked) >= assistantStaleRecheck
	if due {
		p.staleLooked = now
	}
	out := p.lastOut.Load()
	p.mu.Unlock()
	if !due {
		return false
	}
	status, since, ok := claudeRuntimeTurn(n, p)
	if !ok || status != "idle" && status != "shell" || since.IsZero() || now.Sub(since) < assistantStaleTurn {
		return false
	}
	path := n.transcriptPath(p.id)
	if path == "" {
		return false
	}
	if over, known := transcriptTurnOver(path); !known || !over {
		return false
	}
	p.mu.Lock()
	if !p.alive() || p.lastOut.Load() != out || p.reading.Load() > 0 || !p.staleWorkingLocked() {
		p.mu.Unlock() // it printed meanwhile, or something of its own began
		return false
	}
	busy, held := p.busy, len(p.written)
	p.busy, p.written = false, nil
	quiet := p.quiet()
	p.mu.Unlock()
	n.logf("assistant %s: working (busy %v, %d messages held) with nothing printed for %s, but Claude Code says %s since %s and the transcript's turn ended: the turn is over",
		short(p.id), busy, held, quiet.Round(time.Second), status, since.UTC().Format(time.RFC3339))
	return true
}
