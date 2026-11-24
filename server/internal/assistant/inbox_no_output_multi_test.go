package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func inboxRetryPeer(t *testing.T, s *Service, todo Todo, sid string) Todo {
	t.Helper()
	if err := s.linkTodoAgent(context.Background(), todo.ID, "box", sid, "Peer", "/code/app", "codex"); err != nil {
		t.Fatal(err)
	}
	return todoNow(s, todo.ID)
}

func inboxRetryPages(f *fakeHub, pages map[string][]proto.Item) {
	f.answerNode(DeviceSessionItems, func(_ string, params json.RawMessage) (any, error) {
		var p struct {
			Session string `json:"session_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return map[string]any{"items": pages[p.Session]}, nil
	})
}

func inboxRetryLastSession(f *fakeHub) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.agentSent) > 0 {
		sid, _ := f.agentSent[len(f.agentSent)-1]["session_id"].(string)
		return sid
	}
	return ""
}

func TestInboxNoOutputRetryPinsTheOriginalRecipient(t *testing.T) {
	s, f, todo := inboxRetryService(t, nil)
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt, before := sent[len(sent)-1], len(sent)
	todo = inboxRetryPeer(t, s, todo, "s2")
	peer := turnRow(proto.StatusDone, 2, "Unrelated work.")
	peer.SessionID, peer.UpdatedAt = "s2", time.Now().Add(-time.Minute)
	first := turnRow(proto.StatusDone, 11, "")
	first.State = proto.StateIdle // the older unread peer wins pickTaskAgent's ordinary priority
	page := turnPage("empty-1", "", true)
	page[0].Text = prompt
	inboxRetryPages(f, map[string][]proto.Item{"s1": page, "s2": turnPage("peer", "Unrelated work.", true)})
	f.setSessions("box", first, peer)
	s.taskWorking(context.Background(), "box", "s1", startRow{})
	s.taskTurnSettled(context.Background(), "box", "s1")
	if len(sentToAgent(f)) != before+1 || inboxRetryLastSession(f) != "s1" {
		t.Fatalf("retry changed recipients: sends=%d, last recipient=%q", len(sentToAgent(f)), inboxRetryLastSession(f))
	}
}

func TestInboxNoOutputUnavailableRecipientDoesNotRerouteToPeer(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "stopped"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			s, f, todo := inboxRetryService(t, nil)
			s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
			sent := sentToAgent(f)
			prompt, before := sent[len(sent)-1], len(sent)
			inboxRetryPeer(t, s, todo, "s2")
			first := turnRow(proto.StatusDone, 11, "")
			first.State = proto.StateIdle
			peer := turnRow(proto.StatusDone, 2, "Unrelated work.")
			peer.SessionID, peer.UpdatedAt = "s2", first.UpdatedAt.Add(-time.Minute)
			page := turnPage("empty-1", "", true)
			page[0].Text = prompt
			f.setItems(page...)
			f.setSessions("box", first, peer)
			stopped := first
			stopped.Status, stopped.State = proto.StatusOffline, proto.StateOffline
			f.readHooks = receiptHooks{after: func(int) {
				if missing {
					f.setSessions("box", peer)
				} else {
					f.setSessions("box", stopped, peer)
				}
			}}
			s.taskWorking(context.Background(), "box", "s1", startRow{})
			s.taskTurnSettled(context.Background(), "box", "s1")
			failures := unhandledInboxSignals(s)
			if len(sentToAgent(f)) != before || len(failures) != 1 ||
				failures[0].Inbox.URL != linkedPRURL+"#discussion_r123" {
				t.Fatalf("unavailable recipient rerouted to a peer instead of reporting its event: sends=%d, failures=%+v",
					len(sentToAgent(f)), failures)
			}
		})
	}
}

func TestInboxNoOutputSettlementReadsTheTriggeringRecipient(t *testing.T) {
	for _, peerStatus := range []string{proto.StatusIdle, proto.StatusWorking} {
		t.Run(peerStatus, func(t *testing.T) {
			s, f, todo := inboxRetryService(t, nil)
			s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
			sent := sentToAgent(f)
			prompt, before := sent[len(sent)-1], len(sent)
			inboxRetryPeer(t, s, todo, "s2")
			first := turnRow(proto.StatusDone, 11, "")
			peer := turnRow(peerStatus, 20, "Unrelated work.")
			peer.SessionID, peer.UpdatedAt = "s2", first.UpdatedAt.Add(time.Minute)
			page := turnPage("empty-1", "", true)
			page[0].Text = prompt
			inboxRetryPages(f, map[string][]proto.Item{"s1": page, "s2": turnPage("peer", "Unrelated work.", true)})
			f.setSessions("box", first, peer)
			s.taskWorking(context.Background(), "box", "s1", startRow{})
			s.taskTurnSettled(context.Background(), "box", "s1")
			if len(sentToAgent(f)) != before+1 || inboxRetryLastSession(f) != "s1" {
				t.Fatalf("the peer's newer transcript hid the empty event turn: sends=%d, recipient=%q",
					len(sentToAgent(f)), inboxRetryLastSession(f))
			}
		})
	}
}

func TestInboxNoOutputRecoveryVisitsEachRecordedRecipient(t *testing.T) {
	for _, peerStatus := range []string{proto.StatusIdle, proto.StatusWorking} {
		t.Run(peerStatus, func(t *testing.T) {
			s, f, todo := inboxRetryService(t, todoTestDB(t))
			s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
			sent := sentToAgent(f)
			prompt1 := sent[len(sent)-1]
			todo = inboxRetryPeer(t, s, todo, "s2")
			first := turnRow(proto.StatusIdle, 10, "")
			second := turnRow(proto.StatusIdle, 20, "")
			second.SessionID, second.UpdatedAt = "s2", first.UpdatedAt.Add(time.Minute)
			f.setSessions("box", first, second)
			s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-2"))
			sent = sentToAgent(f)
			prompt2, before := sent[len(sent)-1], len(sent)
			if inboxRetryLastSession(f) != "s2" {
				t.Fatal("second event fixture did not reach its distinct recipient")
			}
			todo = inboxRetryPeer(t, s, todo, "s3")
			peer := turnRow(peerStatus, 30, "Unrelated work.")
			peer.SessionID, peer.UpdatedAt = "s3", second.UpdatedAt.Add(time.Minute)
			pages := map[string][]proto.Item{
				"s1": turnPage("empty-1", "", true),
				"s2": turnPage("empty-2", "", true),
				"s3": turnPage("peer", "Unrelated work.", true),
			}
			pages["s1"][0].Text, pages["s2"][0].Text = prompt1, prompt2
			inboxRetryPages(f, pages)
			f.setSessions("box", first, second, peer)
			s.tasks.turns.mu.Lock()
			s.tasks.turns.loaded = false
			s.tasks.turns.mu.Unlock()
			for range 3 {
				s.watchTurn(context.Background(), todo, []proto.Session{first, second, peer})
				waitFor(t, "the recovery pass", func() bool {
					s.tasks.turns.mu.Lock()
					defer s.tasks.turns.mu.Unlock()
					return !s.tasks.turns.recovering[todo.ID]
				})
			}
			if got := len(sentToAgent(f)); got != before+2 {
				t.Fatalf("recovery looked only at the newest peer: sends=%d, want %d", got, before+2)
			}
		})
	}
}

func TestInboxNoOutputEarlierStopDoesNotHideTheRetryResult(t *testing.T) {
	for _, answer := range []string{"I handled the review.", ""} {
		name := "answered"
		if answer == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			s, f, todo := inboxRetryService(t, nil)
			s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
			sent := sentToAgent(f)
			prompt, before := sent[len(sent)-1], len(sent)
			inboxRetryTurn(s, f, "empty-1", prompt, "", 10)
			earlier := turnPage("stopped", "", false)
			earlier[0].Text = prompt
			earlier = append(earlier, proto.Item{ID: "old-stop", Kind: proto.KindNotice, Text: userStoppedNotice})
			page := turnPage("retry-result", answer, true)
			page[0].Text = prompt
			f.setItems(append(earlier, page...)...)
			f.setSessions("box", turnRow(proto.StatusWorking, 20, ""))
			s.taskWorking(context.Background(), "box", "s1", startRow{})
			f.setSessions("box", turnRow(proto.StatusDone, 21, answer))
			s.taskTurnSettled(context.Background(), "box", "s1")
			if len(sentToAgent(f)) != before+1 {
				t.Fatal("an earlier stop caused another redelivery")
			}
			if answer == "" {
				if got := unhandledInboxSignals(s); len(got) != 1 || !strings.Contains(got[0].Detail, "produced no output") {
					t.Fatalf("an earlier stop hid the empty retry: %+v", got)
				}
			} else {
				s.tasks.turns.mu.Lock()
				raw, err := json.Marshal(s.tasks.turns.doc)
				s.tasks.turns.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), `"inbox_events"`) {
					t.Fatal("an earlier stop prevented the answer from acknowledging its event")
				}
			}
		})
	}
}

func TestInboxNoOutputRecoveryRetriesTheRecipientAfterAReadFailure(t *testing.T) {
	s, f, todo := inboxRetryService(t, nil)
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt, before := sent[len(sent)-1], len(sent)
	todo = inboxRetryPeer(t, s, todo, "s2")
	first := turnRow(proto.StatusIdle, 10, "")
	peer := turnRow(proto.StatusIdle, 20, "")
	peer.SessionID, peer.UpdatedAt = "s2", first.UpdatedAt.Add(time.Minute)
	page := turnPage("empty-1", "", true)
	page[0].Text = prompt
	var reads atomic.Int32
	f.answerNode(DeviceSessionItems, func(_ string, params json.RawMessage) (any, error) {
		var p struct {
			Session string `json:"session_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Session == "s1" {
			if reads.Add(1) == 2 {
				return nil, errors.New("the transcript's settlement read failed")
			}
			return map[string]any{"items": page}, nil
		}
		return map[string]any{"items": turnPage("peer", "Unrelated work.", true)}, nil
	})
	f.setSessions("box", first, peer)
	key := turnRecoveryKey{Todo: todo.ID, Machine: first.MachineID, Session: first.SessionID}
	for pass := range 2 {
		if pass == 1 {
			s.tasks.turns.mu.Lock()
			retry := s.tasks.turns.retry[key]
			retry.at = time.Now().Add(-time.Second)
			s.tasks.turns.retry[key] = retry
			s.tasks.turns.mu.Unlock()
		}
		s.watchTurn(context.Background(), todo, []proto.Session{first, peer})
		waitFor(t, "the recovery pass", func() bool {
			s.tasks.turns.mu.Lock()
			defer s.tasks.turns.mu.Unlock()
			return !s.tasks.turns.recovering[todo.ID]
		})
		if pass == 0 && len(sentToAgent(f)) != before {
			t.Fatal("a refused settlement used the sweep's stale read to spend the retry")
		}
	}
	if len(sentToAgent(f)) != before+1 || reads.Load() < 4 {
		t.Fatalf("closed-row cache hid the recipient after a transient read failure: sends=%d, reads=%d",
			len(sentToAgent(f)), reads.Load())
	}
}

func TestInboxNoOutputFailureStaysSeparateFromOrdinaryInboxThroughRecovery(t *testing.T) {
	ctx := context.Background()
	s, f, todo := inboxRetryService(t, todoTestDB(t))
	f.mu.Lock()
	f.failSend = errors.New("initial event could not be delivered")
	f.mu.Unlock()
	s.inboxToAgent(ctx, inboxRetryEvent(todo.ID, "initial-failure"))
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	s.inboxToAgent(ctx, inboxRetryEvent(todo.ID, "later-review"))
	sent := sentToAgent(f)
	prompt := sent[len(sent)-1]
	inboxRetryTurn(s, f, "empty-1", prompt, "", 10)
	inboxRetryTurn(s, f, "empty-2", prompt, "", 20)
	failures := unhandledInboxSignals(s)
	if len(failures) != 1 {
		t.Fatalf("initial-delivery failure absorbed the later unhandled event: %+v", failures)
	}
	failure := failures[0]
	ordinary := inboxRetryEvent(todo.ID, "ordinary-message")
	ordinary.Inbox.URL, ordinary.Inbox.Sender = linkedPRURL+"#issuecomment_456", "another-sender"
	s.signal(ordinary)
	for _, merged := range [][]superviseSignal{
		mergeSignals([]superviseSignal{failure}, []superviseSignal{ordinary}),
		mergeSignals([]superviseSignal{ordinary}, []superviseSignal{failure}),
	} {
		if len(merged) != 2 || merged[0].id() == merged[1].id() {
			t.Fatalf("queued signal merge folded the unhandled event into ordinary Inbox: %+v", merged)
		}
	}
	s.restoreDigest([]superviseSignal{failure, ordinary}, time.Now(), time.Time{})
	s.supervise.mu.Lock()
	delete(s.supervise.pending, failure.id()) // lose the undelivered failure across recovery
	s.supervise.sent[ordinary.id()] = failure.Key
	s.supervise.mu.Unlock()
	s.tasks.turns.mu.Lock()
	s.tasks.turns.loaded = false
	s.tasks.turns.mu.Unlock()
	s.superviseWatch(ctx, time.Now())
	if got := unhandledInboxSignals(s); len(got) != 1 || got[0].id() == ordinary.id() ||
		got[0].Inbox.URL != failure.Inbox.URL || got[0].Inbox.Sender != failure.Inbox.Sender {
		t.Fatalf("ordinary sent state suppressed or replaced recovered failure: %+v", got)
	}
	s.superviseTimes.gather = time.Nanosecond
	s.sendDigest(ctx)
	waitFor(t, "the recovered source-bearing notice", func() bool {
		for _, notice := range f.notices() {
			if strings.Contains(notice, "unhandled") && strings.Contains(notice, "later-review") &&
				strings.Contains(notice, failure.Inbox.URL) && strings.Contains(notice, failure.Inbox.Sender) {
				return true
			}
		}
		return false
	})
}
