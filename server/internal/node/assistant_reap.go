package node

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
)

const assistantIdleTimeout = 20 * time.Minute

// A process with background tasks or monitors running is kept: they are its children, and their notifications are
// what wakes the conversation up again. Past this long without a turn it is let go anyway (a forgotten `tail -f`).
const assistantTaskMaxIdle = 12 * time.Hour

// assistantInterrupt stops the current turn. claude -p has no interrupt over stdin, so the process is killed;
// the next message resumes the conversation from its transcript. Codex has turn/interrupt: the process stays.
func (n *Node) assistantInterrupt(id string) error {
	n.state.stopped(id, proto.StopUser)
	// An explicit Stop wins over any queued same-model or fallback continuation: cancelled before the session lock, a
	// recovery send that has not started gives up at once instead of holding Stop behind it (#3495).
	n.cancelExternalStartups(id)
	n.cancelAssistantRecovery(id)
	if n.stopAwaited(id) { // a message waits for the round's turn, holding the lock: it goes in now (assistant_round.go)
		return nil
	}
	defer n.typing.lock(id)()
	n.cancelAssistantRecovery(id)
	if n.runtimeAssistant(id) {
		return n.assistantInterruptRuntime(context.Background(), id)
	}
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	if p != nil && p.agent == "codex" && p.alive() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := p.cx.interrupt(ctx); err == nil {
			return nil
		}
	}
	n.stopAssistantProcess(id)
	n.rememberTasks(id, nil) // stopped on purpose, background tasks included
	return nil
}

// stopAssistant cancels recovery before replacing a process. Explicit user stops additionally hold the session send
// lock (assistantInterrupt and session.close); internal callers may already hold settings locks and must not take it.
func (n *Node) stopAssistant(id string) {
	n.cancelAssistantRecovery(id)
	n.stopAssistantProcess(id)
}

func (n *Node) stopAssistantProcess(id string) {
	if p := n.takeAssistant(id); p != nil {
		p.stop()
	}
}

// takeAssistant removes id's process from this Node's and returns it (nil for none).
func (n *Node) takeAssistant(id string) *assistantProc {
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.procs[id]
	delete(n.procs, id)
	return p
}

// stopAtConnectionEnd ends the conversations ids and the warm spare of a node without a resident keeper, whose
// process exits once its connection has (`node --stdio`), and returns when they have exited (stopAndWait). Each
// process leads its own group, so nothing else takes them down with the node: a node the manager killed left them
// running beside the processes the next node started for the same conversations, the old ones refused the
// assistant's tools by that node (mcpSession) and their background tasks reported lost while still running (#4259).
// What they were running is still remembered (rememberTasks), so the next node tells each conversation.
func (n *Node) stopAtConnectionEnd(ids []string, spare *assistantProc) {
	var procs []*assistantProc
	for _, id := range ids {
		n.cancelAssistantRecovery(id)
		if p := n.takeAssistant(id); p != nil {
			procs = append(procs, p)
		}
	}
	if spare != nil {
		procs = append(procs, spare)
	}
	if left := stopAndWait(procs); left > 0 {
		n.logger.Printf("assistant: %d conversations still running after SIGKILL as the connection ended", left)
	}
}

// reaperStopWait bounds how long Serve waits for reapAssistants to stop the conversations as the connection ends:
// stopAndWait's own bounds, and a margin.
func reaperStopWait() time.Duration { return keeperStopGrace + keeperKillWait + time.Second }

func (p *assistantProc) stop() {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = osproc.StopGroup(p.cmd.Process.Pid, false)
		go func() {
			select {
			case <-p.exited:
			case <-time.After(5 * time.Second):
				_ = osproc.StopGroup(p.cmd.Process.Pid, true)
			}
		}()
	}
}

// reapable: idle long enough to end; p.mu is held. Background tasks and monitors are children of the process
// (killed with it) and wake it when they report, so a process waiting on them stays (2026-10-30: a 23-minute
// background command was killed at minute 20 and the conversation never heard back).
func (p *assistantProc) reapable(now time.Time) bool {
	if p.compacting {
		return now.Sub(p.lastActive) > 10*time.Minute // a compaction that never reported back
	}
	if p.busy {
		return false
	}
	if len(p.tasks) > 0 {
		return now.Sub(p.lastActive) > assistantTaskMaxIdle
	}
	return now.Sub(p.lastActive) > assistantIdleTimeout
}

// idleCompactable: about to be let go with a context worth compacting first (assistantIdleCompact); p.mu is held. A
// supervision round's process never is (assistantProc.round): its next round starts from the first turn anyway.
func (p *assistantProc) idleCompactable() bool {
	threshold := p.compactIdleAt
	if threshold == 0 {
		threshold = assistantIdleCompact
	}
	return p.agent == "claude" && !p.round && !p.compacting && !p.compacted && p.context >= threshold
}

// compactIdle sends /compact to an idle process. It is not a turn: the conversation stays as it was in the app (the
// transcript shows a notice), and the process is let go when idle again.
func (n *Node) compactIdle(p *assistantProc) {
	// Not while a send, a restore or the user's compaction has this conversation (assistantLife), and only on the same
	// process, still idle: the next sweep tries again.
	unlock, ok := n.assistantLife.tryLock(p.id)
	if !ok {
		return
	}
	defer unlock()
	n.mu.Lock()
	same := n.procs[p.id] == p
	n.mu.Unlock()
	p.mu.Lock()
	idle := !p.busy && !p.compacting && !p.manual
	p.mu.Unlock()
	if !same || !idle {
		return
	}
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "/compact"}})
	p.mu.Lock()
	tokens := p.context
	p.compacting, p.compacted = true, true
	p.lastActive = time.Now()
	p.mu.Unlock()
	if err := p.write(line); err != nil {
		n.logger.Printf("assistant %s: compact: %v", p.id[:8], err)
		n.stopAssistant(p.id)
		return
	}
	n.logger.Printf("assistant %s: idle with %d tokens in context, compacting before letting it go", p.id[:8], tokens)
}

// reapAssistants ends processes idle for a while; their conversations resume on the next message. When ctx ends (the
// connection did) a resident node hands its conversations over; any other node stops them and returns once they have
// exited (stopAtConnectionEnd).
func (n *Node) reapAssistants(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			n.mu.Lock()
			ids := make([]string, 0, len(n.procs))
			for id := range n.procs {
				ids = append(ids, id)
			}
			spare := n.spare
			n.spare = nil
			n.mu.Unlock()
			if n.keeper != nil { // a resident node: the next connection takes them over (assistant_keep.go)
				if spare != nil {
					spare.stop()
				}
				n.handOverAssistants()
				return
			}
			n.stopAtConnectionEnd(ids, spare)
			return
		case <-t.C:
		}
		n.mu.Lock()
		var stale *assistantProc
		if n.spare != nil && time.Since(n.spare.born) > assistantSpareMaxAge {
			stale, n.spare = n.spare, nil
		}
		var idle []string
		var compact []*assistantProc
		var timedOut []string
		for id, p := range n.procs {
			p.mu.Lock()
			if p.manual && time.Since(p.lastActive) > assistantCompactTimeout {
				p.manual, p.compacting = false, false
				timedOut = append(timedOut, id)
			}
			if p.reapable(time.Now()) {
				if p.idleCompactable() {
					compact = append(compact, p)
				} else {
					idle = append(idle, id)
				}
			}
			p.mu.Unlock()
		}
		n.mu.Unlock()
		for _, id := range timedOut {
			n.compactionEnded(id, "压缩超过 10 分钟没有结果")
		}
		n.sweepCompactions()
		for _, p := range compact {
			n.compactIdle(p)
		}
		for _, id := range idle {
			n.stopAssistant(id)
			n.rememberTasks(id, nil) // let go on purpose: nothing to tell it after a restart
		}
		if stale != nil {
			stale.stop()
		}
	}
}
