package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// #7539, the explicit reopens of an accepted task: whoever may ask it for more (its conversation, its supervisor, the
// background supervisor acting for the main conversation) opens it again with what it sends the task's agent, and that
// holds across a manager restart between the send and the turn it queued, also one before the send's answer came back.
// Everyone else's sends (the background supervisor's for a task a side conversation supervises included), a bare /exit
// or /quit, a card message for another task, a send that definitely failed or never went out (even when a turn of the
// agent started while it was out), one whose answer never came with no turn of the agent soon after, and a card
// message whose new agent did not start leave it closed.

// longAgent is the session id of the agent a plannedTask runs on: long enough for the agent tools to find it.
const longAgent = "s100001"

// plannedTask is newSupervisedServiceOn with its agent named longAgent (on box), st as its database (nil: none).
func plannedTask(t *testing.T, st Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, st)
	f.mu.Lock()
	f.judge = `{"verdict":"done","reason":"merged"}`
	f.agents = 100000
	f.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "Fix the login crash", "prompt": "The login page crashes on an iPad in landscape: fix it and add tests",
		"machine": "box", "cwd": "/code/app", "readonly": true, "why": "the repository is on box",
		"done_when": []string{"PR merged", "light and dark screenshots in the PR"}, "expect_minutes": 30,
	}}})
	if _, err := s.toolTaskPlan(context.Background(), "conv1", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	if todo.Agents[0].SessionID != longAgent || todo.PlanID == "" || todo.Supervision == nil || todo.Supervision.Supervisor != "conv1" {
		t.Fatalf("not a plan task of conv1 on %s: %+v %+v", longAgent, todo, todo.Supervision)
	}
	return s, f, todo
}

// longRow lists the agent longAgent on box with status and its last words text, as its node does.
func longRow(f *fakeHub, status, text string) {
	st := proto.StateUnread
	if status == proto.StatusWorking {
		st = proto.StateWorking
	}
	f.setSessions("box", proto.Session{SessionID: longAgent, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: status,
		State: st, LastText: text, UpdatedAt: time.Now()})
}

// longTurn is agentTurn for the agent longAgent.
func longTurn(s *Service, f *fakeHub, status, text string) {
	ctx := context.Background()
	longRow(f, status, text)
	if status == proto.StatusWorking {
		s.taskWorking(ctx, "box", longAgent)
	} else {
		s.taskTurnEnded(ctx, "box", longAgent)
	}
}

// sendTo sends agent longAgent on box text from conversation from, with tool (agent_send, or agent_interrupt with
// then).
func sendTo(t *testing.T, s *Service, tool, from, text string) error {
	t.Helper()
	args := map[string]any{"machine": "box", "session_id": longAgent, "text": text}
	if tool == "agent_interrupt" {
		args = map[string]any{"machine": "box", "session_id": longAgent, "then": text}
	}
	_, err := runJSON(t, s, from, tool, args)
	return err
}

// sendVia sends agent longAgent text from conversation from: with agent_send, agent_interrupt's then, or as the
// owner's message on task id's card ("card").
func sendVia(t *testing.T, s *Service, via, from, id, text string) error {
	t.Helper()
	if via == "card" {
		_, err := s.routeToTask(context.Background(), from, id, text)
		return err
	}
	return sendTo(t, s, via, from, text)
}

// acceptedPlanTask is plannedTask (st its database, nil: none) with its agent's work done and accepted by conv1, the
// clock past the acceptance.
func acceptedPlanTask(t *testing.T, st Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f, todo := plannedTask(t, st)
	longTurn(s, f, proto.StatusWorking, "")
	longTurn(s, f, proto.StatusDone, "Fixed it: PR https://github.com/o/r/pull/77 is merged")
	waitFor(t, "the review message", func() bool { return supervisionOf(s, f, todo.ID).notices == 1 })
	accepted := reviewAndWait(t, s, todo.ID, reviewAccepted)
	waitFor(t, "the clock to pass the acceptance", func() bool { return time.Now().After(*accepted.DoneAt) })
	return s, f, accepted
}

// acceptedByBackground is plannedTask with the background supervisor on (as by default), its agent's work done and
// accepted by the background supervisor bg for the main conversation (the ledger still names conv1), the clock past
// the acceptance.
func acceptedByBackground(t *testing.T) (s *Service, f *fakeHub, accepted Todo, bg string) {
	t.Helper()
	s, f, todo := plannedTask(t, nil)
	s.noBackground = false // on, as by default (newBackgroundService)
	f.mu.Lock()
	f.made = 100
	f.mu.Unlock()
	longTurn(s, f, proto.StatusWorking, "")
	longTurn(s, f, proto.StatusDone, "Fixed it: PR https://github.com/o/r/pull/77 is merged")
	bg = backgroundID(t, s)
	listBackground(t, s, f, bg)
	waitFor(t, "the background supervisor's review message", func() bool { return len(f.noticesTo(bg)) == 1 })
	if _, err := runJSON(t, s, bg, "task_review", map[string]any{"todo_id": todo.ID, "verdict": reviewAccepted,
		"note": "Checked the PR and the screenshots", "checks": completeReviewChecks(todoNow(s, todo.ID))}); err != nil {
		t.Fatalf("the background supervisor's accept: %v", err)
	}
	settleService(t, s)
	accepted = todoNow(s, todo.ID)
	if accepted.DoneAt == nil || accepted.DoneBy != db.DoneByReview || accepted.Supervision.Supervisor != "conv1" {
		t.Fatalf("not accepted for the main conversation: %+v %+v", accepted, accepted.Supervision)
	}
	waitFor(t, "the clock to pass the acceptance", func() bool { return time.Now().After(*accepted.DoneAt) })
	return s, f, accepted, bg
}

// beforeSend has f run fn, once, when the manager next calls session.send, before the node answers.
func beforeSend(f *fakeHub, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beforeSend = fn
}

// failSends has every session.send f gets next fail with err (nil: answered).
func failSends(f *fakeHub, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failSend = err
}

// shortSends makes a live send wait at most wait, for this test.
func shortSends(t *testing.T, wait time.Duration) {
	t.Helper()
	old := liveSendWait
	liveSendWait = wait
	t.Cleanup(func() { liveSendWait = old })
}

// ageSends makes the sends of more for accepted tasks that s keeps, in memory and in its database st (nil: none), by
// older, as if they had been sent that much earlier: the agent reads the text that long after the send.
func ageSends(t *testing.T, s *Service, st Store, by time.Duration) {
	t.Helper()
	if by == 0 {
		return
	}
	s.tasks.mu.Lock()
	for _, o := range s.tasks.sendsOut {
		for _, n := range o.notes {
			n.at, n.until = n.at.Add(-by), n.until.Add(-by)
		}
	}
	s.tasks.mu.Unlock()
	if st == nil {
		return
	}
	list := keptSends(t, st)
	for i := range list {
		for j := range list[i].Sends {
			e := &list[i].Sends[j]
			e.At, e.Until = e.At.Add(-by), e.Until.Add(-by)
		}
	}
	if err := st.PutSetting(context.Background(), sendsOutKey, list); err != nil {
		t.Fatal(err)
	}
}

// keptSends is what database st keeps of the sends of more for accepted tasks, by agent.
func keptSends(t *testing.T, st Store) []sendsOutRecord {
	t.Helper()
	var list []sendsOutRecord
	if _, err := st.Setting(context.Background(), sendsOutKey, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

// clockPast waits until the clock is past at.
func clockPast(t *testing.T, at time.Time) {
	t.Helper()
	waitFor(t, "the clock to pass "+at.Format(time.RFC3339Nano), func() bool { return time.Now().After(at) })
}

// The node's errors for a send, as it words them (node/sendsafe.go, node/claudebg.go): a Claude's inbox read the text
// but the session had not taken it in time (held for the person at the computer, say), and typing that failed part way,
// both delivered unknown; a refusal with nothing typed, delivered none.
var (
	nodeUnconfirmed = &rpc.Error{Code: "peer_unconfirmed", Message: "the session has not taken the message yet",
		Data: json.RawMessage(`{"delivered":"unknown"}`)}
	nodeTypingFailed = &rpc.Error{Message: "the pane closed while typing", Data: json.RawMessage(`{"delivered":"unknown"}`)}
	relayedDeadline  = &rpc.Error{Message: context.DeadlineExceeded.Error()} // the bus only carries the words
	nodeAfterWrite   = &rpc.Error{Code: "internal", Message: "could not save the receipt", Data: json.RawMessage(`{"delivered":"yes"}`)}
	nodeRefused      = &rpc.Error{Code: "not_delivered", Message: "the terminal took nothing", Data: json.RawMessage(`{"delivered":"none"}`)}
)

// The node acknowledged its write but returned a receipt of the wrong shape.
func unreadableWriteReceipt(string, json.RawMessage) (any, error) { return 42, nil }

// sessionSends counts the session.send calls f got, answered or not.
func sessionSends(f *fakeHub) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == DeviceSessionSend {
			n++
		}
	}
	return n
}

// The background supervisor accepts the main conversation's plan-started task (its ledger keeps the main conversation
// as the supervisor), then sends its agent more: the task opens again, and the work it asked for is judged and waits
// for its review again. Another conversation's send before it leaves the task closed.
func TestBackgroundSupervisorReopensATaskItAccepted(t *testing.T) {
	for _, tool := range []string{"agent_send", "agent_interrupt"} {
		t.Run(tool, func(t *testing.T) {
			s, f, todo, bg := acceptedByBackground(t)
			accepted := todo

			if err := sendTo(t, s, tool, "conv7", "Also look at the other dashboard"); err != nil {
				t.Fatalf("%s from another conversation: %v", tool, err)
			}
			longTurn(s, f, proto.StatusWorking, "")
			closedUnchanged(t, s, accepted)
			longTurn(s, f, proto.StatusDone, "The other dashboard looks fine")
			closedUnchanged(t, s, accepted)

			told := len(f.noticesTo(bg))
			if err := sendTo(t, s, tool, bg, "Add the landscape case to the tests"); err != nil {
				t.Fatalf("%s from the background supervisor: %v", tool, err)
			}
			longTurn(s, f, proto.StatusWorking, "")
			if got := todoNow(s, todo.ID); got.DoneAt != nil || got.ReopenedAt == nil {
				t.Fatalf("the background supervisor's %s did not reopen it: %+v", tool, got)
			}
			longTurn(s, f, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
			waitFor(t, "the review again", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
			waitFor(t, "the background supervisor's next review message", func() bool { return len(f.noticesTo(bg)) > told })
		})
	}
}

// More for an accepted task reaches its live agent (the owner's message on its card, or agent_send from its
// conversation), which only queues it; the manager restarts before the agent takes it up. The turn it starts then is
// the task's: open, followed, judged, waiting for review, also a day later (a Claude's inbox held the text until the
// person at the computer let it in). A send that definitely failed asked nothing: the task stays closed through the
// restart and the agent's next turn, also when a turn of the agent started (other work) while that send was out and
// ends after the restart.
func TestMoreForAnAcceptedTaskSurvivesARestart(t *testing.T) {
	failed := errors.New("no such session")
	for _, c := range []struct {
		name, via string
		err       error // the send's (nil: taken)
		fails     bool  // it definitely failed
		// meanwhile: a turn of the agent starts while the send is out
		meanwhile bool
		later     time.Duration // how long after the send the agent takes the text up
	}{
		{"card", "card", nil, false, false, 0},
		{"agent_send", "agent_send", nil, false, false, 0},
		{"card, failed", "card", failed, true, false, 0},
		{"agent_send, failed", "agent_send", failed, true, false, 0},
		{"card, failed while a turn started", "card", failed, true, true, 0},
		{"agent_send, failed while a turn started", "agent_send", failed, true, true, 0},
		// The node's inbox holds the text for the person at the computer, who lets it in after the restart.
		{"agent_send, the node could not confirm it", "agent_send", nodeUnconfirmed, false, false, 0},
		{"agent_interrupt, the node could not confirm it", "agent_interrupt", nodeUnconfirmed, false, false, 0},
		{"card, the node could not confirm it", "card", nodeUnconfirmed, false, false, 0},
		{"agent_send, the node could not confirm it, let in a day later", "agent_send", nodeUnconfirmed, false, false, 24 * time.Hour},
		{"card, the node could not confirm it, let in a day later", "card", nodeUnconfirmed, false, false, 24 * time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			s, f, accepted := acceptedPlanTask(t, database)
			failSends(f, c.err)
			if c.meanwhile {
				beforeSend(f, func() { longTurn(s, f, proto.StatusWorking, "") })
			}
			if err := sendVia(t, s, c.via, "conv1", accepted.ID, "Please cover the landscape case too"); (c.err != nil) != (err != nil) {
				t.Fatalf("the send: %v, want %v", err, c.err)
			}
			if c.err != nil && !c.fails {
				closedUnchanged(t, s, accepted) // nothing arrived yet
			}
			if c.fails {
				closedUnchanged(t, s, accepted)
			}
			ageSends(t, s, database, c.later)

			// The manager restarts before the agent takes up the text it queued.
			restarted, hub := newQuietServiceOn(t, database)
			hub.judge = `{"verdict":"done","reason":"merged"}`
			if c.meanwhile { // the turn that started while the send was out ends
				longTurn(restarted, hub, proto.StatusDone, "The other dashboard looks fine")
				closedUnchanged(t, restarted, accepted)
			}
			longTurn(restarted, hub, proto.StatusWorking, "")
			if c.fails {
				closedUnchanged(t, restarted, accepted)
				return
			}
			if got := todoNow(restarted, accepted.ID); got.DoneAt != nil || got.ReopenedAt == nil {
				t.Fatalf("after the restart, the turn its %s queued left the task closed: %+v", c.via, got)
			}
			longTurn(restarted, hub, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
			waitFor(t, "the review again", func() bool { return todoNow(restarted, accepted.ID).Supervision.State == reviewPending })
			waitFor(t, "the review message after the restart", func() bool { return supervisionOf(restarted, hub, accepted.ID).notices == 1 })
		})
	}
}

// A turn of the agent starts while a send of more for its accepted task is out (its own wakeup, say, or the send's
// own turn coming in before the node's answer). The turn waits for that answer. When the send definitely failed (the
// node refused it, or it never went out: cancelled, or its wait ran out while the node refused it as restarting), it
// was other work, and the task stays closed through it and its end, with nothing judged, receipted or told. When the
// node took the send, its answer never came, or the node's error says the text may have arrived (delivered unknown),
// the task opens and the turn is the task's: its done end waits for review, also when that end came before the answer.
func TestATurnDuringASendWaitsForItsAnswer(t *testing.T) {
	outcomes := []struct {
		name string
		err  error // the send's (nil: taken)
		// failed: the send definitely asked for nothing; ended: the turn's end comes before the answer
		failed, ended bool
	}{
		{"failed", errors.New("the input box holds a draft; nothing was typed"), true, false},
		{"refused by the node, nothing typed", nodeRefused, true, false},
		{"never went out, cancelled", rpc.NotSent(context.Canceled), true, false},
		{"never went out, its wait ran out", rpc.NotSent(context.DeadlineExceeded), true, false},
		{"no answer", context.DeadlineExceeded, false, false},
		{"no answer, ended first", context.DeadlineExceeded, false, true},
		{"the node could not confirm it", nodeUnconfirmed, false, false},
		{"typing failed part way, ended first", nodeTypingFailed, false, true},
		{"sent", nil, false, false},
		{"sent, ended first", nil, false, true},
	}
	for _, via := range []string{"agent_send", "agent_interrupt", "card"} {
		for _, o := range outcomes {
			t.Run(via+", "+o.name, func(t *testing.T) {
				s, f, accepted := acceptedPlanTask(t, nil)
				before, judged, receipted := supervisionOf(s, f, accepted.ID), judgeCalls(f), receiptCount(t, s)
				failSends(f, o.err)
				beforeSend(f, func() {
					longTurn(s, f, proto.StatusWorking, "")
					if got := todoNow(s, accepted.ID); got.DoneAt == nil {
						t.Errorf("reopened by a turn before the send's answer: %+v", got)
					}
					if o.ended {
						longTurn(s, f, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
					}
				})
				err := sendVia(t, s, via, "conv1", accepted.ID, "Please cover the landscape case too")
				if (err != nil) != (o.err != nil) {
					t.Fatalf("the send: %v, want %v", err, o.err)
				}
				if o.failed {
					closedUnchanged(t, s, accepted)
					longTurn(s, f, proto.StatusDone, "The other dashboard looks fine")
					closedUnchanged(t, s, accepted)
					if n := judgeCalls(f); n != judged {
						t.Fatalf("the turn was judged: %d judgments, %d before", n, judged)
					}
					if n := receiptCount(t, s); n != receipted {
						t.Fatalf("the turn got a receipt: %d, %d before", n, receipted)
					}
					nothingToldAfter(t, s, f, accepted.ID, before)
					return
				}
				if got := todoNow(s, accepted.ID); got.DoneAt != nil || got.ReopenedAt == nil {
					t.Fatalf("the send was taken or may have been, its turn started, and the task stayed closed: %+v", got)
				}
				if !o.ended {
					longTurn(s, f, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
				}
				waitFor(t, "the review again", func() bool { return todoNow(s, accepted.ID).Supervision.State == reviewPending })
				waitFor(t, "the next review message", func() bool { return supervisionOf(s, f, accepted.ID).notices > before.notices })
			})
		}
	}
}

// A send of more whose answer never came (the wait ran out, the link dropped), or that the node could not confirm (a
// Claude's inbox holds the text for the person at the computer), may still reach the agent, however long after: queued
// behind a long turn, or let in once that person looks. The accepted task stays closed until the agent's next turn,
// which opens it and is the task's, whether it starts soon after the send or an hour later, long past the send's own
// wait. Only an agent that starts no turn for longer than sendKeep (a week) leaves the task closed through a later
// turn, and its supervisor hears nothing.
func TestAnUnknownSendReopensOnTheAgentsNextTurn(t *testing.T) {
	for _, c := range []struct {
		via, why string
		err      error
		later    time.Duration // how long after the send the agent's turn starts
	}{
		{"agent_send", "no answer", context.DeadlineExceeded, 0},
		{"agent_send", "deadline across the bus", relayedDeadline, 0},
		{"agent_send", "node failed after writing", nodeAfterWrite, 0},
		{"agent_send", "unreadable write receipt", nil, 0},
		{"card", "no answer", context.DeadlineExceeded, 0},
		{"agent_send", "no answer", context.DeadlineExceeded, time.Hour},
		{"card", "no answer", context.DeadlineExceeded, time.Hour},
		{"agent_send", "no answer", context.DeadlineExceeded, sendKeep + time.Minute},
		{"card", "no answer", context.DeadlineExceeded, sendKeep + time.Minute},
		{"agent_send", "the node could not confirm it", nodeUnconfirmed, 0},
		{"agent_interrupt", "the node could not confirm it", nodeUnconfirmed, 0},
		{"card", "the node could not confirm it", nodeUnconfirmed, 0},
		{"agent_send", "the node could not confirm it", nodeUnconfirmed, time.Hour},
		{"agent_interrupt", "the node could not confirm it", nodeUnconfirmed, time.Hour},
		{"card", "the node could not confirm it", nodeUnconfirmed, sendKeep + time.Minute},
	} {
		late := c.later > sendKeep
		name := fmt.Sprintf("%s, %s, its turn starts %v after", c.via, c.why, c.later)
		t.Run(name, func(t *testing.T) {
			shortSends(t, 200*time.Millisecond)
			s, f, accepted := acceptedPlanTask(t, nil)
			before := supervisionOf(s, f, accepted.ID)
			failSends(f, c.err)
			if c.err == nil {
				f.answerNode(DeviceSessionSend, unreadableWriteReceipt)
			}
			if err := sendVia(t, s, c.via, "conv1", accepted.ID, "Please cover the landscape case too"); err == nil {
				t.Fatal("the send's answer came")
			}
			closedUnchanged(t, s, accepted) // nothing arrived yet
			ageSends(t, s, nil, c.later)
			longTurn(s, f, proto.StatusWorking, "")
			if late {
				closedUnchanged(t, s, accepted)
				longTurn(s, f, proto.StatusDone, "The other dashboard looks fine")
				closedUnchanged(t, s, accepted)
				nothingToldAfter(t, s, f, accepted.ID, before)
				return
			}
			if got := todoNow(s, accepted.ID); got.DoneAt != nil || got.ReopenedAt == nil {
				t.Fatalf("the turn %v after a send that may have arrived left the task closed: %+v", c.later, got)
			}
			longTurn(s, f, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
			waitFor(t, "the review again", func() bool { return todoNow(s, accepted.ID).Supervision.State == reviewPending })
			waitFor(t, "the next review message", func() bool { return supervisionOf(s, f, accepted.ID).notices > before.notices })

			// That turn was the one the send asked for: the next one, after the task is accepted again, is other work.
			again := reviewAndWait(t, s, accepted.ID, reviewAccepted)
			longTurn(s, f, proto.StatusWorking, "")
			if got := todoNow(s, accepted.ID); got.DoneAt == nil || !got.DoneAt.Equal(*again.DoneAt) || got.DoneBy != db.DoneByReview {
				t.Fatalf("the turn after the task was accepted again moved it: %+v", got)
			}
		})
	}
}

// A live agent's node never answers agent_send while its link stays up (a runtime's tool call has no deadline of its
// own). The send still ends with its own wait, as one whose answer never came, and a turn of the agent held for that
// answer meanwhile is the task's: the task opens, and the turn's done end waits for review, whether it came before the
// wait ran out or after.
func TestALiveSendNeverAnsweredEndsWithItsWait(t *testing.T) {
	for _, ended := range []bool{false, true} {
		name := "the turn ends after the wait"
		if ended {
			name = "the turn ends during the wait"
		}
		t.Run(name, func(t *testing.T) {
			shortSends(t, 300*time.Millisecond)
			s, f, accepted := acceptedPlanTask(t, nil)
			before := supervisionOf(s, f, accepted.ID)
			f.mu.Lock()
			f.sendHangs = true
			f.mu.Unlock()
			beforeSend(f, func() {
				longTurn(s, f, proto.StatusWorking, "")
				if ended {
					longTurn(s, f, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
				}
			})
			done := make(chan error, 1)
			go func() { done <- sendTo(t, s, "agent_send", "conv1", "Please cover the landscape case too") }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("the send no node answered: %v, want its wait run out", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the send no node answered never ended")
			}
			if got := todoNow(s, accepted.ID); got.DoneAt != nil || got.ReopenedAt == nil {
				t.Fatalf("the turn held for the send left the task closed: %+v", got)
			}
			if !ended {
				longTurn(s, f, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
			}
			waitFor(t, "the review again", func() bool { return todoNow(s, accepted.ID).Supervision.State == reviewPending })
			waitFor(t, "the next review message", func() bool { return supervisionOf(s, f, accepted.ID).notices > before.notices })
		})
	}
}

// A live agent's node never answers agent_send while its link stays up. Only a send of more for an accepted task holds
// a turn of the agent for its answer, so only it ends with its own wait, and tells the model the text may still
// arrive. Any other send to a running agent (to one with no task, or another conversation's to the accepted task's
// agent) waits as long as its caller, as before #7539, and ends with it.
func TestOnlyASendOfMoreForAnAcceptedTaskEndsWithItsOwnWait(t *testing.T) {
	for _, c := range []struct {
		name, from string
		accepted   bool // the agent works for an accepted task of conv1 (else for no task)
		bounded    bool // the send ends with its own wait
	}{
		{"more for the accepted task", "conv1", true, true},
		{"another conversation's send to the accepted task's agent", "conv7", true, false},
		{"an agent with no task", "conv1", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			shortSends(t, 200*time.Millisecond)
			var s *Service
			var f *fakeHub
			if c.accepted {
				s, f, _ = acceptedPlanTask(t, nil)
			} else {
				s, f = newQuietService(t)
				longRow(f, proto.StatusIdle, "Fixed it")
			}
			out := make(chan struct{})
			f.mu.Lock()
			f.sendHangs = true
			f.mu.Unlock()
			beforeSend(f, func() { close(out) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw, _ := json.Marshal(map[string]any{"machine": "box", "session_id": longAgent, "text": "Please cover the landscape case too"})
			done := make(chan error, 1)
			go func() {
				_, err := s.runTool(ctx, "server", c.from, "agent_send", raw)
				done <- err
			}()
			select {
			case <-out:
			case err := <-done:
				t.Fatalf("the send ended before it went out: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("the send never went out")
			}
			wentOut := time.Now()
			if c.bounded {
				select {
				case err := <-done:
					if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "check with agent_read before sending again") {
						t.Fatalf("the send of more no node answered: %v, want its own wait run out, saying it may still arrive", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("the send of more no node answered never ended")
				}
				return
			}
			clockPast(t, wentOut.Add(5*liveSendWait))
			select {
			case err := <-done:
				t.Fatalf("the send ended (%v) while its caller still waited", err)
			default:
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("the send its caller stopped waiting for: %v, want cancelled", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the send did not end with its caller")
			}
		})
	}
}

// Two sends to the agent overlap: a turn of the agent starts while the first, of more for the accepted task, is out,
// and is held for its answer; a second send is made before the turn ends (another conversation's, or more for the task
// again); the turn ends before either answer. The second send does not hide the held turn: once the task opens, the
// turn and its end are the task's, and the task waits for review again. When the first send fails, the second one,
// for the task too, takes the turn up.
func TestOverlappingSendsKeepTheHeldTurn(t *testing.T) {
	failed := errors.New("the input box holds a draft; nothing was typed")
	for _, c := range []struct {
		name, second  string // the conversation the second send is from
		first, latter error  // how each went
	}{
		{"another conversation's send meanwhile, which fails", "conv7", nil, failed},
		{"more for the task again meanwhile", "conv1", nil, nil},
		{"the first fails, the second for the task is taken", "conv1", failed, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, f, accepted := acceptedPlanTask(t, nil)
			before := supervisionOf(s, f, accepted.ID)
			first, err := s.noteAgentSend(ctx, "conv1", "box", longAgent, "", "Please cover the landscape case too")
			if err != nil {
				t.Fatal(err)
			}
			longTurn(s, f, proto.StatusWorking, "") // held for the first send's answer
			closedUnchanged(t, s, accepted)
			second, err := s.noteAgentSend(ctx, c.second, "box", longAgent, "", "And the portrait case")
			if err != nil {
				t.Fatal(err)
			}
			longTurn(s, f, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged") // put off
			closedUnchanged(t, s, accepted)
			first.sent(ctx, c.first)
			second.sent(ctx, c.latter)
			if got := todoNow(s, accepted.ID); got.DoneAt != nil || got.ReopenedAt == nil {
				t.Fatalf("a send for the task was taken, and the task stayed closed: %+v", got)
			}
			waitFor(t, "the review again", func() bool { return todoNow(s, accepted.ID).Supervision.State == reviewPending })
			waitFor(t, "the next review message", func() bool { return supervisionOf(s, f, accepted.ID).notices > before.notices })
		})
	}
}

// More for an accepted task reaches its live agent, and the manager restarts before the node's answer to the send came
// back (the node had queued the text). The restarted manager takes the kept send as one whose answer is unknown: the
// agent's next turn is the task's (open, followed, judged, waiting for review), also when the agent takes the queued
// text up only an hour later, behind a long turn, long past the send's own wait. A turn that started while the send
// was out, before the restart, is the task's too once the manager starts (recoverSendsOut, as Run does), also when it
// ended before.
func TestMoreForAnAcceptedTaskSurvivesARestartBeforeItsAnswer(t *testing.T) {
	for _, c := range []struct {
		name, via string
		// before: what the agent did while the send was out, before the restart: "" nothing, "started" a turn,
		// "ended" a turn that started and ended
		before string
		later  time.Duration // how long after the send the agent takes the text up
	}{
		{"card", "card", "", 0},
		{"agent_send", "agent_send", "", 0},
		{"agent_interrupt", "agent_interrupt", "", 0},
		{"agent_send, a turn started before the restart", "agent_send", "started", 0},
		{"card, a turn started and ended before the restart", "card", "ended", 0},
		{"agent_send, its turn starts an hour later", "agent_send", "", time.Hour},
		{"card, its turn starts an hour later", "card", "", time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			database := taskRecoveryDatabase(t)
			s, f, accepted := acceptedPlanTask(t, database)
			out, gone, answered := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			beforeSend(f, func() {
				if c.before != "" {
					longTurn(s, f, proto.StatusWorking, "")
				}
				if c.before == "ended" {
					longTurn(s, f, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
				}
				close(out)
				<-gone // the manager that sent it stopped: it never reads the answer
			})
			go func() { answered <- sendVia(t, s, c.via, "conv1", accepted.ID, "Please cover the landscape case too") }()
			defer func() { // the old manager's call ends, as one that failed, once this test is done with it
				failSends(f, errors.New("the manager that sent it stopped"))
				close(gone)
				<-answered
			}()
			<-out
			closedUnchanged(t, s, accepted)
			ageSends(t, s, database, c.later)

			// The manager restarts while the send is out.
			restarted, hub := newQuietServiceOn(t, database)
			hub.judge = `{"verdict":"done","reason":"merged"}`
			switch c.before {
			case "started":
				longRow(hub, proto.StatusWorking, "")
			case "ended":
				longRow(hub, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
			}
			restarted.recoverSendsOut(ctx) // as Run does when the manager starts
			if c.before == "" {
				closedUnchanged(t, restarted, accepted) // the restart alone opens nothing
				longTurn(restarted, hub, proto.StatusWorking, "")
			}
			if got := todoNow(restarted, accepted.ID); got.DoneAt != nil || got.ReopenedAt == nil {
				t.Fatalf("after the restart, the turn its %s queued left the task closed: %+v", c.via, got)
			}
			if c.before != "ended" {
				longTurn(restarted, hub, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
			}
			waitFor(t, "the review again", func() bool { return todoNow(restarted, accepted.ID).Supervision.State == reviewPending })
			waitFor(t, "the review message after the restart", func() bool { return supervisionOf(restarted, hub, accepted.ID).notices == 1 })
		})
	}
}

// sendsKeepFails is a database whose next read, or next write, of the kept sends (sendsOutKey) fails once when armed;
// everything else works as the database does.
type sendsKeepFails struct {
	*db.DB
	mu                  sync.Mutex
	failRead, failWrite bool
}

func (d *sendsKeepFails) arm(read, write bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failRead, d.failWrite = read, write
}

// writeArmed says whether the next write of the kept sends is still to fail.
func (d *sendsKeepFails) writeArmed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.failWrite
}

// take says whether *armed was set, and clears it.
func (d *sendsKeepFails) take(armed *bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	was := *armed
	*armed = false
	return was
}

func (d *sendsKeepFails) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == sendsOutKey && d.take(&d.failRead) {
		return false, errors.New("connection reset by peer")
	}
	return d.DB.Setting(ctx, key, out)
}

func (d *sendsKeepFails) PutSetting(ctx context.Context, key string, v any) error {
	if key == sendsOutKey && d.take(&d.failWrite) {
		return errors.New("connection reset by peer")
	}
	return d.DB.PutSetting(ctx, key, v)
}

// More for an accepted task whose note cannot be kept (the database fails, once, to write it or to read the notes kept
// before) would be lost with a manager restart while the node holds the text, so it is not sent: the send says so,
// nothing reaches the node, and the task stays closed, also through a turn of the agent after it. Sent again once the
// database works, with its answer lost, it survives a restart: the turn it queued opens the task, which waits for
// review again.
func TestASendOfMoreThatCannotBeKeptIsNotSent(t *testing.T) {
	for _, via := range []string{"agent_send", "agent_interrupt", "card"} {
		for _, read := range []bool{false, true} {
			name := via + ", its write fails"
			if read {
				name = via + ", the read of the notes kept before fails"
			}
			t.Run(name, func(t *testing.T) {
				database := &sendsKeepFails{DB: taskRecoveryDatabase(t)}
				s, f, accepted := acceptedPlanTask(t, database)
				before := sessionSends(f)
				database.arm(read, !read)
				err := sendVia(t, s, via, "conv1", accepted.ID, "Please cover the landscape case too")
				var msg *i18n.Message
				switch {
				case err == nil:
					t.Fatal("sent, with its note not kept")
				case via == "card" && !(errors.As(err, &msg) && msg.Key == "assistant-tasks.route_not_kept"):
					t.Fatalf("the card message: %v, want route_not_kept", err)
				case via != "card" && !strings.Contains(err.Error(), "nothing was sent"):
					t.Fatalf("the %s: %v, want it to say nothing was sent", via, err)
				}
				if n := sessionSends(f); n != before {
					t.Fatalf("%d session.send went out with the note not kept", n-before)
				}
				closedUnchanged(t, s, accepted)
				longTurn(s, f, proto.StatusWorking, "") // other work: nothing was sent
				closedUnchanged(t, s, accepted)
				longTurn(s, f, proto.StatusDone, "The other dashboard looks fine")
				closedUnchanged(t, s, accepted)

				failSends(f, context.DeadlineExceeded) // the node queues it, and its answer is lost
				if err := sendVia(t, s, via, "conv1", accepted.ID, "Please cover the landscape case too"); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("sent again: %v, want its answer lost", err)
				}
				if n := sessionSends(f); n != before+1 {
					t.Fatalf("sent again: %d session.send, want 1", n-before)
				}
				closedUnchanged(t, s, accepted) // nothing arrived yet, and the turn before was other work

				// The manager restarts before the agent takes up the text it queued.
				restarted, hub := newQuietServiceOn(t, database.DB)
				hub.judge = `{"verdict":"done","reason":"merged"}`
				longTurn(restarted, hub, proto.StatusWorking, "")
				if got := todoNow(restarted, accepted.ID); got.DoneAt != nil || got.ReopenedAt == nil {
					t.Fatalf("after the restart, the turn the send queued left the task closed: %+v", got)
				}
				longTurn(restarted, hub, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
				waitFor(t, "the review again", func() bool { return todoNow(restarted, accepted.ID).Supervision.State == reviewPending })
			})
		}
	}
}

// A send of more the node refused asked for nothing, and its note goes from what is kept as well as from memory. When
// that write fails (the database refuses it once, after it took the note itself), it is written again until it goes
// through: a manager that restarts afterwards finds nothing kept for the send, and the agent's turns after the restart
// leave the task closed, also one that started while the send was out and ends after the restart, with nothing judged
// or told.
func TestARefusedSendIsForgottenWhenItsCleanupWriteFails(t *testing.T) {
	every := sendsRetryEvery
	sendsRetryEvery = 10 * time.Millisecond
	t.Cleanup(func() { sendsRetryEvery = every })
	for _, via := range []string{"agent_send", "agent_interrupt", "card"} {
		for _, meanwhile := range []bool{false, true} {
			name := via
			if meanwhile {
				name += ", a turn started while it was out"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				database := &sendsKeepFails{DB: taskRecoveryDatabase(t)}
				s, f, accepted := acceptedPlanTask(t, database)
				failSends(f, nodeRefused)
				beforeSend(f, func() {
					if meanwhile {
						longTurn(s, f, proto.StatusWorking, "") // held for the answer
					}
					if n := len(keptSends(t, database.DB)); n != 1 {
						t.Errorf("%d agents with sends kept while the send is out, want 1", n)
					}
					database.arm(false, true) // the write after the refusal fails
				})
				if err := sendVia(t, s, via, "conv1", accepted.ID, "Please cover the landscape case too"); err == nil {
					t.Fatal("the send the node refused succeeded")
				}
				if database.writeArmed() {
					t.Fatal("nothing was written after the refusal")
				}
				closedUnchanged(t, s, accepted)
				waitFor(t, "the refused send to go from what is kept", func() bool { return len(keptSends(t, database.DB)) == 0 })

				// The manager restarts.
				restarted, hub := newQuietServiceOn(t, database.DB)
				hub.judge = `{"verdict":"done","reason":"merged"}`
				before := supervisionOf(restarted, hub, accepted.ID)
				if meanwhile {
					longRow(hub, proto.StatusWorking, "")
				}
				restarted.recoverSendsOut(ctx) // as Run does when the manager starts
				closedUnchanged(t, restarted, accepted)
				if meanwhile { // the turn that started while the send was out ends
					longTurn(restarted, hub, proto.StatusDone, "The other dashboard looks fine")
					closedUnchanged(t, restarted, accepted)
				}
				judged := judgeCalls(hub)
				longTurn(restarted, hub, proto.StatusWorking, "")
				closedUnchanged(t, restarted, accepted)
				longTurn(restarted, hub, proto.StatusDone, "The other dashboard looks fine")
				closedUnchanged(t, restarted, accepted)
				if n := judgeCalls(hub); n != judged {
					t.Fatalf("a turn after the restart was judged: %d judgments, %d before", n, judged)
				}
				nothingToldAfter(t, restarted, hub, accepted.ID, before)
			})
		}
	}
}

// A turn of the agent starts while a send of more for its accepted task is out, and is held for the send's answer;
// before the turn ends, the agent goes to another task, an open one. The turn's end is that task's (taken and judged:
// done), not put off for the answer, and nothing stays kept for the accepted task; the answer, unknown, leaves the
// accepted task closed.
func TestAHeldTurnOfAnAgentThatWentToAnotherTaskIsThatTasks(t *testing.T) {
	ctx := context.Background()
	database := taskRecoveryDatabase(t)
	s, f, accepted := acceptedPlanTask(t, database)
	note, err := s.noteAgentSend(ctx, "conv1", "box", longAgent, "", "Please cover the landscape case too")
	if err != nil {
		t.Fatal(err)
	}
	longTurn(s, f, proto.StatusWorking, "") // held for the send's answer
	closedUnchanged(t, s, accepted)
	other, err := s.createTaskTodo(ctx, "Tidy the release notes", "Group the entries by area.", "conv1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, other.ID, "box", longAgent, "", "/code/app", "claude"); err != nil {
		t.Fatalf("hand the agent to the other task: %v", err)
	}
	longRow(f, proto.StatusDone, "Release notes tidied")
	if got := s.settleTurn(ctx, "box", longAgent, ""); got != settleTaken {
		t.Fatalf("the end of the other task's turn: %v, want taken (settleLater: put off for the accepted task's send)", got)
	}
	if kept := keptSends(t, database); len(kept) != 0 {
		t.Fatalf("still kept for the accepted task: %+v", kept)
	}
	note.sent(ctx, context.DeadlineExceeded)
	closedUnchanged(t, s, accepted)
	if got := todoNow(s, other.ID); got.DoneAt == nil || got.DoneBy != db.DoneByAuto {
		t.Fatalf("the other task's turn end was not judged: %+v", got)
	}
}

// A send of more for an accepted task whose answer is unknown is kept for the agent's next turn. When the task opens
// by other means first (the owner reopens it), the send asks nothing of it any more: the next write of the kept sends
// (here, as the manager restarts) forgets it, and once the task is accepted again, the agent's next turn leaves it
// closed.
func TestASendForATaskReopenedMeanwhileIsForgotten(t *testing.T) {
	ctx := context.Background()
	database := taskRecoveryDatabase(t)
	s, f, accepted := acceptedPlanTask(t, database)
	failSends(f, context.DeadlineExceeded)
	if err := sendTo(t, s, "agent_send", "conv1", "Please cover the landscape case too"); err == nil {
		t.Fatal("the send's answer came")
	}
	if n := len(keptSends(t, database)); n != 1 {
		t.Fatalf("%d agents with sends kept, want 1", n)
	}
	if err := s.reopenTodo(ctx, accepted.ID); err != nil {
		t.Fatal(err)
	}

	restarted, hub := newQuietServiceOn(t, database)
	restarted.recoverSendsOut(ctx) // as Run does when the manager starts
	if kept := keptSends(t, database); len(kept) != 0 {
		t.Fatalf("still kept for a task that opened again: %+v", kept)
	}
	again := reviewAndWait(t, restarted, accepted.ID, reviewAccepted)
	longTurn(restarted, hub, proto.StatusWorking, "")
	if got := todoNow(restarted, accepted.ID); got.DoneAt == nil || !got.DoneAt.Equal(*again.DoneAt) || got.DoneBy != db.DoneByReview {
		t.Fatalf("the turn after the task was accepted again moved it: %+v", got)
	}
}

// The background supervisor stands in for the main conversation only. A task a side conversation supervises sends that
// conversation its review signals; once accepted, the background supervisor's agent_send or agent_interrupt to its
// agent, and the agent's turn after it, leave it closed.
func TestBackgroundSupervisorLeavesASideConversationsTaskClosed(t *testing.T) {
	for _, tool := range []string{"agent_send", "agent_interrupt"} {
		t.Run(tool, func(t *testing.T) {
			s, f, todo, bg := acceptedByBackground(t)
			side := todo
			ledger := *todo.Supervision
			side.Origin, ledger.Supervisor, side.Supervision = "side1", "side1", &ledger
			putTodo(s, side)
			if !s.backgroundSupervises(context.Background(), bg, side) {
				t.Fatal("the background supervisor may not review the side conversation's task")
			}
			if err := sendTo(t, s, tool, bg, "Add the landscape case to the tests"); err != nil {
				t.Fatalf("%s from the background supervisor: %v", tool, err)
			}
			closedUnchanged(t, s, side)
			longTurn(s, f, proto.StatusWorking, "")
			closedUnchanged(t, s, side)
		})
	}
}

// The owner's message on the card of an accepted task whose agents have all stopped starts a new agent for it. When
// that start definitely fails (the agent's computer is unreachable, its launcher is refused, the start never went out),
// nothing was asked of the task: it stays exactly as accepted, and its supervisor hears nothing of it.
func TestACardMessageWhoseNewAgentFailsLeavesTheTaskClosed(t *testing.T) {
	for _, why := range []string{"computer unreachable", "launcher refused", "start never went out"} {
		t.Run(why, func(t *testing.T) {
			ctx := context.Background()
			s, f, todo := doneAndReviewed(t)
			accepted := reviewAndWait(t, s, todo.ID, reviewAccepted)
			before := supervisionOf(s, f, todo.ID)
			f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
				LastText: "Fixed it: PR https://github.com/o/r/pull/77 is merged", Status: proto.StatusOffline, State: proto.StateOffline,
				UpdatedAt: time.Now(), Capabilities: &proto.SessionCapabilities{Read: true, Terminal: true}, ReadOnlyReason: "stopped"})
			f.mu.Lock()
			switch why {
			case "computer unreachable":
				f.devices = []Device{{ID: "box", Name: "box", Online: true, Reachable: false}}
			case "launcher refused":
				f.failStart = errors.New("launcher not found")
			default: // the node refused it as restarting until the manager's retries ran out (devices.Machine.call)
				f.failStart = rpc.NotSent(context.DeadlineExceeded)
			}
			f.mu.Unlock()
			if _, err := s.routeToTask(ctx, "conv1", todo.ID, "Please cover the landscape case too"); err == nil {
				t.Fatal("the new agent started")
			}
			closedUnchanged(t, s, accepted)
			nothingToldAfter(t, s, f, todo.ID, before)
		})
	}
}

// A bare /exit or /quit closes the agent (the node runs it as the agents' own quit command): it asks the accepted
// task for nothing, from its conversation as from the background supervisor, with agent_send, agent_interrupt's then
// or the task's card, and leaves the task closed, also for a turn of the agent after it.
func TestAQuitCommandLeavesAnAcceptedTaskClosed(t *testing.T) {
	for _, c := range []struct{ from, via, text string }{
		{"conv1", "agent_send", "/exit"},
		{"conv1", "agent_interrupt", "/quit"},
		{"conv1", "card", " /exit "},
		{"background", "agent_send", "/quit"},
		{"background", "agent_interrupt", "/exit"},
	} {
		t.Run(c.from+", "+c.via+" "+c.text, func(t *testing.T) {
			s, f, accepted, bg := acceptedByBackground(t)
			from := c.from
			if from == "background" {
				from = bg
			}
			if err := sendVia(t, s, c.via, from, accepted.ID, c.text); err != nil {
				t.Fatalf("the send: %v", err)
			}
			closedUnchanged(t, s, accepted)
			longTurn(s, f, proto.StatusWorking, "")
			closedUnchanged(t, s, accepted)
		})
	}
}

// A message on an accepted task's card reopens that task only. Its agent is handed to another task of the same
// conversation, accepted too, while the card's message looks its agents up: the message goes to the agent as before,
// and neither task opens again, not even on the agent's next turn.
func TestACardMessageReopensOnlyItsOwnTask(t *testing.T) {
	ctx := context.Background()
	s, f, accepted := acceptedPlanTask(t, nil)
	now := time.Now()
	putTodo(s, Todo{ID: "todo_b", Title: "Tidy the release notes", Origin: "conv1", CreatedAt: now, UpdatedAt: now,
		Supervision: &Supervision{Supervisor: "conv1", State: reviewAccepted, Reviews: []TaskReview{{At: now, Verdict: reviewAccepted}}}})
	var other Todo
	f.mu.Lock()
	f.afterList = map[string]func(){"box": func() { // the agents of the card's task were just read
		if err := s.linkTodoAgent(ctx, "todo_b", "box", longAgent, "", "/code/app", "claude"); err != nil {
			t.Errorf("hand the agent to the other task: %v", err)
			return
		}
		done := time.Now()
		s.applyTodoRow(ctx, "todo_b", db.TodoRow{ID: "todo_b", Title: "Tidy the release notes", Origin: "conv1", CreatedAt: now,
			UpdatedAt: done, DoneAt: &done, DoneBy: db.DoneByReview}, nil)
		other = todoNow(s, "todo_b")
	}}
	f.mu.Unlock()
	// Only the card message's own look at the agents runs the hook: the token sync the acceptance started may still be
	// listing box.
	if _, err := s.routeToTask(listHooked(ctx), "conv1", accepted.ID, "Please cover the landscape case too"); err != nil {
		t.Fatalf("the card message: %v", err)
	}
	if other.ID == "" || other.DoneAt == nil || other.DoneBy != db.DoneByReview || !slices.ContainsFunc(other.Agents, func(a TodoAgent) bool {
		return a.SessionID == longAgent && a.ReleasedAt == nil
	}) {
		t.Fatalf("the other task did not take the agent and close: %+v", other)
	}
	closedUnchanged(t, s, other)
	if got := todoNow(s, accepted.ID); got.DoneAt == nil {
		t.Fatalf("the card's task opened again without its agent: %+v", got)
	}
	longTurn(s, f, proto.StatusWorking, "")
	closedUnchanged(t, s, other)
}

// A turn of the agent starts while a send of more for its accepted task is out, and is held for its answer; the answer
// (taken, or unknown) opens the task and takes the turn up. In between, the task is open, so the agent is no longer its
// closed task's: a write of the kept sends then (one for the turn's own end that came before the answer, another
// send's, a retry) and the turn's end coming then both leave the turn and its end held for that answer. The end is the
// task's either way: the task waits for review again.
func TestAHeldTurnStaysWhileItsAnswerOpensTheTask(t *testing.T) {
	const done = "Landscape covered: PR https://github.com/o/r/pull/78 is merged"
	for _, c := range []struct {
		name   string
		answer error // the send's (nil: taken)
		// before: the turn ended before the answer, and a write of the kept sends runs as the answer opens the task;
		// else the turn ends then
		before bool
	}{
		{"taken, ended before, a write meanwhile", nil, true},
		{"unknown, ended before, a write meanwhile", context.DeadlineExceeded, true},
		{"taken, it ends meanwhile", nil, false},
		{"the node could not confirm it, it ends meanwhile", nodeUnconfirmed, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, f, accepted := acceptedPlanTask(t, nil)
			before := supervisionOf(s, f, accepted.ID)
			note, err := s.noteAgentSend(ctx, "conv1", "box", longAgent, "", "Please cover the landscape case too")
			if err != nil {
				t.Fatal(err)
			}
			longTurn(s, f, proto.StatusWorking, "") // held for the answer
			closedUnchanged(t, s, accepted)
			if c.before {
				longTurn(s, f, proto.StatusDone, done) // put off
				closedUnchanged(t, s, accepted)
			}
			between := false
			s.sendsSettleHook = func() { // the answer opened the task, and has not taken the turn up yet
				between = true
				if got := todoNow(s, accepted.ID); got.DoneAt != nil {
					t.Errorf("not open as its answer takes the turn up: %+v", got)
				}
				if c.before {
					s.saveSendsOut(ctx)
					return
				}
				longRow(f, proto.StatusDone, done)
				end := make(chan turnSettle, 1)
				go func() { end <- s.settleTurn(ctx, "box", longAgent, "") }()
				select {
				case got := <-end:
					if got != settleLater {
						t.Errorf("the turn's end as its answer takes the turn up: %v, want put off (settleLater)", got)
					}
				case <-time.After(10 * time.Second): // it waits for the answer's lock: it was not put off
					t.Error("the turn's end as its answer takes the turn up was not put off within 10s")
				}
			}
			note.sent(ctx, c.answer)
			s.sendsSettleHook = nil
			if !between {
				t.Fatal("the answer did not open the task")
			}
			waitFor(t, "the review again", func() bool { return todoNow(s, accepted.ID).Supervision.State == reviewPending })
			waitFor(t, "the next review message", func() bool { return supervisionOf(s, f, accepted.ID).notices > before.notices })
		})
	}
}

// More for an accepted task is sent its agent, and before the agent's next turn the task is accepted again as it
// stands (still closed, on the same done_at). That acceptance answers the send: whether its answer was unknown, or it
// is still out and taken after (a turn held for it meanwhile included, or a later send out then), the task stays as
// accepted through the agent's next turns, across a manager restart too (also one before the acceptance again that
// could not read the kept sends yet), and its supervisor hears nothing. A send after the acceptance again opens the
// task as ever.
func TestAnAcceptanceAgainEndsTheSendsBeforeIt(t *testing.T) {
	for _, c := range []struct {
		name    string
		out     bool // the send is still out when the task is accepted again (its answer, taken, comes after)
		held    bool // a turn of the agent started while it was out
		restart bool // the manager restarts after the acceptance again
		// readFails: the manager restarts before the acceptance again, and its read of the kept sends as it starts fails
		readFails bool
	}{
		{"its answer unknown", false, false, false, false},
		{"its answer unknown, then a restart", false, false, true, false},
		{"its answer unknown, a restart that cannot read the kept sends yet", false, false, false, true},
		{"still out, taken after", true, false, false, false},
		{"still out with a turn held, taken after", true, true, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			var st Store
			var keep *sendsKeepFails
			if c.restart || c.readFails {
				keep = &sendsKeepFails{DB: taskRecoveryDatabase(t)}
				st = keep
			}
			s, f, accepted := acceptedPlanTask(t, st)
			note, err := s.noteAgentSend(ctx, "conv1", "box", longAgent, "", "Please cover the landscape case too")
			if err != nil {
				t.Fatal(err)
			}
			if !c.out {
				note.sent(ctx, context.DeadlineExceeded) // kept, for the agent's next turn
			}
			if c.held {
				longTurn(s, f, proto.StatusWorking, "") // held for the answer
			}
			closedUnchanged(t, s, accepted)
			if c.readFails {
				s, f = newQuietServiceOn(t, keep)
				f.judge = `{"verdict":"done","reason":"merged"}`
				keep.arm(true, false)
				s.recoverSendsOut(ctx) // as Run does when the manager starts: the read fails
				if keep.take(&keep.failRead) {
					t.Fatal("the restarted manager did not read the kept sends")
				}
			}
			again := reviewAndWait(t, s, accepted.ID, reviewAccepted)
			if !again.DoneAt.Equal(*accepted.DoneAt) || again.DoneBy != db.DoneByReview || again.ReopenedAt != nil {
				t.Fatalf("not accepted again as it stood: %+v", again)
			}
			before := supervisionOf(s, f, accepted.ID)
			if c.held { // the turn held for the send ends
				longTurn(s, f, proto.StatusDone, "Landscape covered: PR https://github.com/o/r/pull/78 is merged")
				closedUnchanged(t, s, again)
			}
			if c.out { // the answer comes while a send after the acceptance again is out, which then fails
				later, err := s.noteAgentSend(ctx, "conv1", "box", longAgent, "", "And the portrait case")
				if err != nil {
					t.Fatal(err)
				}
				note.sent(ctx, nil)
				closedUnchanged(t, s, again)
				later.sent(ctx, errors.New("the input box holds a draft; nothing was typed"))
				closedUnchanged(t, s, again)
			}
			if c.restart {
				if kept := keptSends(t, keep.DB); len(kept) != 0 {
					t.Fatalf("still kept after the acceptance again: %+v", kept)
				}
				s, f = newQuietServiceOn(t, keep)
				f.judge = `{"verdict":"done","reason":"merged"}`
				s.recoverSendsOut(ctx) // as Run does when the manager starts
				before = supervisionOf(s, f, accepted.ID)
			}
			judged := judgeCalls(f)
			longTurn(s, f, proto.StatusWorking, "")
			closedUnchanged(t, s, again)
			longTurn(s, f, proto.StatusDone, "The other dashboard looks fine")
			closedUnchanged(t, s, again)
			if n := judgeCalls(f); n != judged {
				t.Fatalf("a turn after the acceptance again was judged: %d judgments, %d before", n, judged)
			}
			nothingToldAfter(t, s, f, accepted.ID, before)

			if err := sendTo(t, s, "agent_send", "conv1", "And the portrait case"); err != nil {
				t.Fatalf("the send after the acceptance again: %v", err)
			}
			if got := todoNow(s, accepted.ID); got.DoneAt != nil || got.ReopenedAt == nil {
				t.Fatalf("the send after the acceptance again, taken, left the task closed: %+v", got)
			}
		})
	}
}

// A bare /exit or /quit on the card of an accepted task whose agents have all stopped asks the task for nothing, and
// what it would close is closed already: no new agent starts, the card says so, and the task stays exactly as accepted,
// its supervisor told nothing.
func TestAQuitCommandToAStoppedTaskStartsNothing(t *testing.T) {
	for _, text := range []string{"/exit", " /quit\n"} {
		t.Run(strings.TrimSpace(text), func(t *testing.T) {
			ctx := context.Background()
			s, f, todo := doneAndReviewed(t)
			accepted := reviewAndWait(t, s, todo.ID, reviewAccepted)
			before := supervisionOf(s, f, todo.ID)
			f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
				LastText: "Fixed it: PR https://github.com/o/r/pull/77 is merged", Status: proto.StatusOffline, State: proto.StateOffline,
				UpdatedAt: time.Now(), Capabilities: &proto.SessionCapabilities{Read: true, Terminal: true}, ReadOnlyReason: "stopped"})
			starts := func() int {
				f.mu.Lock()
				defer f.mu.Unlock()
				n := 0
				for _, c := range f.calls {
					if c.Method == DeviceSessionStart {
						n++
					}
				}
				return n
			}
			started := starts()
			_, err := s.routeToTask(ctx, "conv1", todo.ID, text)
			if msg := (*i18n.Message)(nil); !errors.As(err, &msg) || msg.Key != "assistant-tasks.route_agents_exited" {
				t.Fatalf("%q on the card of a task whose agents stopped: %v, want route_agents_exited", text, err)
			}
			if n := starts() - started; n != 0 {
				t.Fatalf("%q started %d new agents", text, n)
			}
			closedUnchanged(t, s, accepted)
			nothingToldAfter(t, s, f, todo.ID, before)
		})
	}
}
