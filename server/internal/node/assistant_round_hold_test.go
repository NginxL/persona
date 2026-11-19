package node

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"testing"
	"time"
)

// What a process holds that is no turn, and that stopping it would lose (#7315, assistant_round.go workingLocked,
// stopSettled, roundSettled): a message Claude Code has said nothing of yet, a line the reader has not got to, and the
// messages a queue edit cancelled and has yet to write again.

// holdTestProc is queueTestProc's process as one that can be stopped, its turn running, quiet for long enough to
// settle once it stops working.
func holdTestProc(t *testing.T, texts ...string) (*Node, *assistantProc, *fakeClaudeIn) {
	t.Helper()
	n, p, f := queueTestProc(t, texts...)
	p.mu.Lock()
	p.cmd, p.born, p.initSeen = &exec.Cmd{}, time.Now().Add(-time.Minute), true
	p.mu.Unlock()
	return n, p, f
}

// quietNow has p print its last line a settle ago, as a process that printed nothing since (printed).
func quietNow(p *assistantProc) {
	p.lastOut.Store(int64(time.Since(p.born) - assistantSettle))
}

// stoppedNow is whether the node stops p, the conversation's process, at one look now (stopSettled with a context
// already ended: no wait for it to settle).
func stoppedNow(n *Node, p *assistantProc) bool {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return n.stopSettled(ctx, p)
}

func writeText(t *testing.T, p *assistantProc, text string) string {
	t.Helper()
	if err := p.writeInput(assistantInput{text: text, content: []map[string]any{{"type": "text", "text": text}}}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.written[len(p.written)-1].id
}

// Claude Code says it queued a message within a millisecond of its write (measured on 2.1.322), which the reader may
// get before the writer goes on: the message is remembered before it is written, so that line is not dropped and the
// message shows queued. One whose write failed is not remembered.
func TestALifecycleLineBeforeTheWriteReturnsIsKept(t *testing.T) {
	_, p, f := holdTestProc(t)
	f.queueAtOnce = true
	writeText(t, p, "fast")
	if got := queuedNow(p); !slices.Equal(got, []string{"fast"}) {
		t.Fatalf("queued %q, want the message Claude Code queued as it was written", got)
	}
	f.failUsers = errors.New("broken pipe")
	if err := p.writeInput(assistantInput{text: "lost", content: []map[string]any{{"type": "text", "text": "lost"}}}); err == nil {
		t.Fatal("the failed write went through")
	}
	p.mu.Lock()
	left := len(p.written)
	p.mu.Unlock()
	if left != 1 {
		t.Fatalf("%d messages remembered, want only the one written", left)
	}
}

// A message written to a Claude Code that says what became of each one is held until it says so: its lines may wait
// in the pipe behind a turn's result, or Claude Code may not have read it yet, and a process stopped then never runs
// it. A started line starts a turn also for a message the node no longer knows. A Claude Code that never said anything
// of its messages holds only what it queued.
func TestAMessageClaudeCodeHasNotSpokenOfHoldsTheProcess(t *testing.T) {
	t.Run("held until its lifecycle comes", func(t *testing.T) {
		n, p, _ := holdTestProc(t, "the round") // its lifecycle lines say what became of every message
		p.mu.Lock()
		round := p.written[0].id
		p.mu.Unlock()
		feedAssistant(n, p, fmt.Sprintf(lifecycleShape, round, "started"))
		id := writeText(t, p, "the owner's question") // on its way in as the round's turn ends
		feedAssistant(n, p, turnResult)
		quietNow(p)
		if stoppedNow(n, p) {
			t.Fatal("stopped with a message Claude Code had said nothing of")
		}
		feedAssistant(n, p, fmt.Sprintf(lifecycleShape, id, "queued"))
		quietNow(p)
		if stoppedNow(n, p) {
			t.Fatal("stopped with a message queued")
		}
		feedAssistant(n, p, fmt.Sprintf(lifecycleShape, id, "started"), turnResult, fmt.Sprintf(lifecycleShape, id, "completed"))
		quietNow(p)
		if !stoppedNow(n, p) {
			t.Fatal("not stopped once its message's turn ended")
		}
	})
	t.Run("a started line of a message it no longer knows", func(t *testing.T) {
		n, p, _ := holdTestProc(t)
		feedAssistant(n, p, turnResult, fmt.Sprintf(lifecycleShape, "written-long-ago", "started"))
		if working, _ := workingNow(n, p); !working {
			t.Fatal("the turn Claude Code started is not under way")
		}
	})
	t.Run("a Claude Code without lifecycle lines", func(t *testing.T) {
		n, p, _ := holdTestProc(t)
		writeText(t, p, "the owner's question")
		feedAssistant(n, p, turnResult)
		quietNow(p)
		if !stoppedNow(n, p) {
			t.Fatal("a Claude Code that never says what became of its messages is held by one")
		}
	})
}

// While the reader is on a line (a turn's result, whose transcript refresh and pushes can take a while), the lines
// behind it wait in the pipe, a turn's init among them: the process is not quiet, however long that line took, until
// the reader is back at the pipe.
func TestALineNotReadYetKeepsTheProcess(t *testing.T) {
	n, p, _ := holdTestProc(t)
	n.deltaMu.Lock() // the result's live-text clear waits: the reader stays on that line
	read := make(chan struct{})
	go func() {
		defer close(read)
		feedAssistant(n, p, turnResult)
	}()
	waitFor(t, "the result to end the turn", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return !p.busy
	})
	quietNow(p) // as if the reader had been on that line for longer than a settle
	stopped := stoppedNow(n, p)
	n.deltaMu.Unlock()
	<-read
	if stopped {
		t.Fatal("stopped while a line behind the result was not read yet")
	}
	if stoppedNow(n, p) { // the quiet counts from when the reader was done with the line
		t.Fatal("stopped as the reader got back to the pipe, before the line behind the result")
	}
	feedAssistant(n, p, turnInitLine) // the turn Claude Code started on its own, read now
	if working, _ := workingNow(n, p); !working {
		t.Fatal("the turn Claude Code started is not under way")
	}
	n.mu.Lock()
	current := n.procs[queueTestID] == p
	n.mu.Unlock()
	if !current {
		t.Fatal("the process was let go")
	}
}

// A round's process marked to retire is not let go in the middle of a queue edit: once the last cancel went through,
// nothing is queued and it may look settled, but the edit has yet to write the cancelled messages again, and they
// would go into a process let go. The edit holds the conversation (assistantLife) as retiring takes it.
func TestARetiringRoundProcessWaitsForAQueueEdit(t *testing.T) {
	n, p, f := holdTestProc(t, "a", "b")
	p.mu.Lock()
	ids := []string{p.written[0].id, p.written[1].id}
	p.retire, p.settleArmed = true, true // a message joined it while it worked, and the look is on its way
	p.mu.Unlock()
	feedAssistant(n, p, turnResult) // the turn they waited behind ended; both still queued
	paused, resume := make(chan struct{}), make(chan struct{})
	f.onControl = func(request map[string]any) {
		if request["message_uuid"] == ids[1] {
			// The second cancel went through (its own line removed b), its answer not back yet: nothing is queued,
			// and the process printed nothing for a settle.
			p.commandLifecycle(ids[1], "cancelled")
			quietNow(p)
			close(paused)
			<-resume
		}
	}
	edited := make(chan error, 1)
	go func() {
		_, err := n.assistantEditQueue(context.Background(), queueTestID, []int{1, 0})
		edited <- err
	}()
	<-paused
	looked := make(chan struct{}) // the retiring look comes now
	if unlock, free := n.assistantLife.tryLock(queueTestID); free {
		unlock()
		n.roundSettled(p)
		close(looked)
	} else {
		go func() {
			defer close(looked)
			n.roundSettled(p)
		}()
		waitFor(t, "the look to find the process settled", func() bool { // it waits for the lock now
			p.mu.Lock()
			defer p.mu.Unlock()
			return !p.settleArmed
		})
	}
	close(resume)
	if err := <-edited; err != nil {
		t.Fatal(err)
	}
	<-looked
	n.mu.Lock()
	current := n.procs[queueTestID] == p
	n.mu.Unlock()
	if !current {
		t.Fatal("the round's process was let go in the middle of the queue edit")
	}
	again := f.userLines()[2:]
	if len(again) != 2 || again[0].text != "b" || again[1].text != "a" {
		t.Fatalf("written again %+v, want b then a", again)
	}
}
