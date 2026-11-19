package node

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A message that needs every tool and comes while a round's process runs its turn (#7315, assistant_round.go
// awaitRoundTurn) waits, within roundTurnWait, for that turn to end, and then goes into a plain resume with every
// tool, not into the round's process on the round's tools. Later messages wait behind it, in order. A Stop while it
// waits ends the round's process at once and lets the message go in. Each wait logs one line saying how it ended.

// waitLogged is the line awaitRoundTurn logs for a wait that ended with outcome.
func waitLogged(t *testing.T, logs *syncBuffer, outcome string) {
	t.Helper()
	if got := strings.Count(logs.String(), "for a supervision round's turn: "); got != 1 ||
		!strings.Contains(logs.String(), "for a supervision round's turn: "+outcome+"\n") {
		t.Fatalf("want one wait logged as %s, got %d:\n%s", outcome, got, logs.String())
	}
}

// ownerSendsAsync is ownerSends without waiting for the answer: the channel gets the send's error.
func ownerSendsAsync(n *Node, text string) <-chan error {
	sent := make(chan error, 1)
	raw, _ := json.Marshal(map[string]any{"session_id": roundConversation, "text": text})
	go func() {
		_, err := n.handle(context.Background(), "session.send", raw)
		sent <- err
	}()
	return sent
}

// holdsTheConversation waits until a send holds the conversation's assistantLife lock (it reached its process) or
// answered; true when it holds the lock.
func holdsTheConversation(t *testing.T, n *Node, sent <-chan error) bool {
	t.Helper()
	held := false
	waitFor(t, "the send to reach the conversation's process", func() bool {
		if len(sent) > 0 {
			return true
		}
		unlock, free := n.assistantLife.tryLock(roundConversation)
		if free {
			unlock()
		}
		held = !free
		return held
	})
	return held
}

// stillWaiting fails t when the send answered within a moment, or wrote anything into the round's process: the round's
// own message is all the fake claude read.
func stillWaiting(t *testing.T, sent <-chan error, input string) {
	t.Helper()
	select {
	case err := <-sent:
		t.Fatalf("answered (%v) while the round's turn runs; read %q", err, roundInputs(t, input))
	case <-time.After(300 * time.Millisecond):
	}
	if msgs := roundInputs(t, input); len(msgs) != 1 {
		t.Fatalf("written into the round's process: %q", msgs)
	}
}

func TestAMessageWaitsForTheRoundsTurnAndGetsEveryTool(t *testing.T) {
	n, argv, input := roundTestNode(t, roundStarted("2.1.322", 1), false)
	var logs syncBuffer
	n.logger = log.New(&logs, "", 0)
	if out := sendRound(t, n, "once-round"); out["context"] != roundFresh {
		t.Fatalf("the round: %v", out)
	}
	round := roundProc(n) // answering: busy since the round's message went in
	sent := ownerSendsAsync(n, "the owner's question")
	holdsTheConversation(t, n, sent)
	stillWaiting(t, sent, input)
	stillRound(t, n, round, argv, 1)
	feedAssistant(n, round, messageStartLine, turnResult) // the round's turn ends
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	round.mu.Lock()
	retire := round.retire
	round.mu.Unlock()
	if retire {
		t.Fatal("the round's process was joined")
	}
	replacedWithEveryTool(t, n, round, argv, input, 2, "一个任务要你处理", "the owner's question")
	waitLogged(t, &logs, "turn_ended")
}

func TestMessagesWaitingForTheRoundGoInInOrder(t *testing.T) {
	n, argv, input := roundTestNode(t, roundStarted("2.1.322", 1), false)
	if out := sendRound(t, n, "once-round"); out["context"] != roundFresh {
		t.Fatalf("the round: %v", out)
	}
	round := roundProc(n)
	first := ownerSendsAsync(n, "the owner's first")
	holdsTheConversation(t, n, first)
	second := ownerSendsAsync(n, "the owner's second") // behind the first, on the conversation's typing lock
	stillWaiting(t, first, input)
	feedAssistant(n, round, messageStartLine, turnResult)
	for _, sent := range []<-chan error{first, second} {
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
	}
	replacedWithEveryTool(t, n, round, argv, input, 2, "一个任务要你处理", "the owner's first", "the owner's second")
}

func TestStopWhileAMessageWaitsForTheRound(t *testing.T) {
	n, argv, input := roundTestNode(t, roundStarted("2.1.322", 1), false)
	var logs syncBuffer
	n.logger = log.New(&logs, "", 0)
	if out := sendRound(t, n, "once-round"); out["context"] != roundFresh {
		t.Fatalf("the round: %v", out)
	}
	round := roundProc(n)
	sent := ownerSendsAsync(n, "the owner's question")
	holdsTheConversation(t, n, sent)
	stillWaiting(t, sent, input)
	stopped := make(chan error, 1)
	go func() { stopped <- n.assistantInterrupt(roundConversation) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop waited for the message waiting for the round's turn")
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the round's process to end", func() bool { return !round.alive() })
	all := roundStarts(t, argv)
	if len(all) != 2 {
		t.Fatalf("%d starts", len(all))
	}
	everyToolStart(t, all[1])
	np := roundProc(n)
	if np == nil || np == round || np.round || !np.alive() {
		t.Fatalf("the message's process: %+v", np)
	}
	waitFor(t, "the message in the new process", func() bool {
		return strings.Contains(strings.Join(roundInputs(t, input), "\n"), "the owner's question")
	})
	readOnce(t, input, "一个任务要你处理", "the owner's question")
	waitLogged(t, &logs, "stopped")
}

// slowToStop makes the fake claude roundTestNode put on PATH ignore SIGTERM: a process that takes a while to exit once
// stopped, until stop() kills it 5 s on.
func slowToStop(t *testing.T) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), "bin", "claude")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "#!/bin/sh\n", "#!/bin/sh\ntrap '' TERM\n", 1)), 0o700); err != nil {
		t.Fatal(err)
	}
}

// A Stop ends the round's process, which may take a while to exit (stop() kills it 5 s on). The message waits until it
// exited, also past the wait's bound and whatever the process prints on its way out, and then goes into a new process
// with every tool: written into the one going away (its input closed), it was lost.
func TestAMessageWaitsForAStoppedRoundToExit(t *testing.T) {
	for _, c := range []struct {
		name string
		task bool // the turn started a background task and goes on, and prints its result as it stops
	}{
		{"past the bound", false},
		{"its result on the way out, with a background task", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			wait := roundTurnWait
			roundTurnWait = time.Second // well before the stopped process exits
			t.Cleanup(func() { roundTurnWait = wait })
			n, argv, input := roundTestNode(t, roundStarted("2.1.322", 1), false)
			slowToStop(t)
			var logs syncBuffer
			n.logger = log.New(&logs, "", 0)
			if out := sendRound(t, n, "once-round"); out["context"] != roundFresh {
				t.Fatalf("the round: %v", out)
			}
			round := roundProc(n)
			if c.task {
				feedAssistant(n, round, oneTaskLine)
			}
			sent := ownerSendsAsync(n, "the owner's question")
			waitFor(t, "the message to wait for the round's turn", func() bool {
				round.mu.Lock()
				defer round.mu.Unlock()
				return round.awaited
			})
			if err := n.assistantInterrupt(roundConversation); err != nil {
				t.Fatal(err)
			}
			if c.task {
				feedAssistant(n, round, turnResult)
			}
			if err := <-sent; err != nil {
				t.Fatalf("the message after a Stop: %v", err)
			}
			if round.alive() {
				t.Fatal("the message went on while the stopped round's process was still there")
			}
			round.mu.Lock()
			retire := round.retire
			round.mu.Unlock()
			if retire {
				t.Fatal("the stopped round's process was joined")
			}
			all := roundStarts(t, argv)
			if len(all) != 2 {
				t.Fatalf("%d starts", len(all))
			}
			everyToolStart(t, all[1])
			waitFor(t, "the message in the new process", func() bool {
				return strings.Contains(strings.Join(roundInputs(t, input), "\n"), "the owner's question")
			})
			readOnce(t, input, "一个任务要你处理", "the owner's question")
			waitLogged(t, &logs, "stopped")
		})
	}
}

// A round's process with only a background task running (its turn ended) is not waited for: the task may run for
// hours. The message goes in at once, on the round's tools, and the process is let go once it settled, as before.
func TestAMessageDoesNotWaitForARoundsBackgroundTask(t *testing.T) {
	n, argv, input := roundTestNode(t, roundStarted("2.1.322", 1), false)
	var logs syncBuffer
	n.logger = log.New(&logs, "", 0)
	if out := sendRound(t, n, "once-round"); out["context"] != roundFresh {
		t.Fatalf("the round: %v", out)
	}
	round := roundProc(n)
	feedAssistant(n, round, oneTaskLine, turnResult)
	start := time.Now()
	ownerSends(t, n, "the owner's question")
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("waited %s for a background task", took)
	}
	stillRound(t, n, round, argv, 1)
	waitFor(t, "the message in the round's process", func() bool {
		return strings.Contains(strings.Join(roundInputs(t, input), "\n"), "the owner's question")
	})
	round.mu.Lock()
	retire := round.retire
	round.mu.Unlock()
	if !retire {
		t.Fatal("the round's process that took the message is not marked to retire")
	}
	if strings.Contains(logs.String(), "for a supervision round's turn") {
		t.Fatalf("a wait is logged where none happened:\n%s", logs.String())
	}
}
