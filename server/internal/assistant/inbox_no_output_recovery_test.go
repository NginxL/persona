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

func TestInboxNoOutputRecoveryContinuesPastAnUnreadableRecipient(t *testing.T) {
	for _, answer := range []string{"", "Handled the review."} {
		name := "retry"
		if answer != "" {
			name = "acknowledge"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, f, todo := inboxRetryService(t, todoTestDB(t))
			s.inboxToAgent(ctx, inboxRetryEvent(todo.ID, "unreadable-review"))
			todo = inboxRetryPeer(t, s, todo, "s2")
			first, second := turnRow(proto.StatusIdle, 10, ""), turnRow(proto.StatusIdle, 20, answer)
			second.SessionID, second.UpdatedAt = "s2", first.UpdatedAt.Add(time.Minute)
			f.setSessions("box", first, second)
			s.inboxToAgent(ctx, inboxRetryEvent(todo.ID, "later-review"))
			sent := sentToAgent(f)
			prompt, before := sent[len(sent)-1], len(sent)
			if inboxRetryLastSession(f) != "s2" {
				t.Fatal("the later event did not reach its recorded recipient")
			}
			todo = inboxRetryPeer(t, s, todo, "s3")
			peer := turnRow(proto.StatusWorking, 30, "")
			peer.SessionID, peer.UpdatedAt = "s3", second.UpdatedAt.Add(time.Minute)
			var firstReads, secondReads atomic.Int32
			var retryEnded atomic.Bool
			f.answerNode(DeviceSessionItems, func(_ string, params json.RawMessage) (any, error) {
				var p struct {
					Session string `json:"session_id"`
				}
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, err
				}
				if p.Session == "s1" {
					firstReads.Add(1)
					return nil, errors.New("the first recipient's transcript is unavailable")
				}
				if p.Session != "s2" {
					return nil, errors.New("an unrelated working peer must not be read")
				}
				secondReads.Add(1)
				turn := "later-empty"
				if retryEnded.Load() {
					turn = "later-retry-empty"
				}
				page := turnPage(turn, answer, true)
				page[0].Text = prompt
				return map[string]any{"items": page}, nil
			})
			f.setSessions("box", first, second, peer)
			s.tasks.turns.mu.Lock()
			s.tasks.turns.loaded = false
			s.tasks.turns.mu.Unlock()
			s.watchTurn(ctx, todo, []proto.Session{first, second, peer})
			turnsQuiet(t, s, todo.ID)
			if firstReads.Load() == 0 || secondReads.Load() == 0 {
				t.Fatalf("one failed read starved a later recipient: first=%d, later=%d", firstReads.Load(), secondReads.Load())
			}
			if answer != "" {
				if s.taskInboxRecipient(ctx, todo.ID, "box", "s2") || !s.taskInboxRecipient(ctx, todo.ID, "box", "s1") {
					t.Fatal("the later reply did not acknowledge only its own event")
				}
				if len(sentToAgent(f)) != before {
					t.Fatal("acknowledging the later event spent another send")
				}
				return
			}
			if len(sentToAgent(f)) != before+1 || inboxRetryLastSession(f) != "s2" {
				t.Fatal("the later empty turn did not retry its original recipient")
			}
			reads := firstReads.Load()
			s.tasks.turns.mu.Lock()
			for key, retry := range s.tasks.turns.retry {
				if retry.look.Session == "s1" {
					retry.at = time.Now().Add(time.Hour)
					s.tasks.turns.retry[key] = retry
				}
			}
			s.tasks.turns.mu.Unlock()
			retryEnded.Store(true)
			second.Rev++
			f.setSessions("box", first, second, peer)
			s.watchTurn(ctx, todo, []proto.Session{first, second, peer})
			turnsQuiet(t, s, todo.ID)
			failures := unhandledInboxSignals(s)
			if firstReads.Load() != reads || len(failures) != 1 || failures[0].Session != "s2" {
				t.Fatalf("one recipient's backoff hid another's completion: first reads=%d, failures=%+v", firstReads.Load(), failures)
			}
			if len(sentToAgent(f)) != before+1 {
				t.Fatal("recovery spent more than the one automatic retry")
			}
		})
	}
}

func flushInboxRecoveryDigest(s *Service) {
	b := &s.supervise
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	for id, g := range b.pending {
		g.At = time.Now().Add(-2 * time.Hour)
		b.pending[id] = g
	}
	b.last = time.Time{}
	b.mu.Unlock()
	s.sendDigest(context.Background())
}

func TestInboxNoOutputUnconfirmedNoticeSurvivesAnOwnerWait(t *testing.T) {
	ctx := context.Background()
	s, f, todo := inboxRetryService(t, todoTestDB(t))
	s.inboxToAgent(ctx, inboxRetryEvent(todo.ID, "review-owner-wait"))
	sent := sentToAgent(f)
	inboxRetryTurn(s, f, "empty-before-wait", sent[len(sent)-1], "", 10)
	sends := len(sentToAgent(f))
	// Model a manager stopped after reserving the retry but before saving its outcome.
	if err := s.updateTaskInboxEvents(ctx, func(events map[string]taskInboxEvent) bool {
		for id, event := range events {
			if event.Todo == todo.ID && event.Retried {
				event.Failure = inboxRetryUnconfirmed
				event.RetryAt = time.Now().Add(-6 * time.Minute)
				events[id] = event
				return true
			}
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}
	s.recoverTaskInboxEvents(ctx)
	pending := unhandledInboxSignals(s)
	if len(pending) != 1 || pending[0].urgent() {
		t.Fatalf("the expired unconfirmed send lost its default-urgency source: %+v", pending)
	}
	failure := pending[0]
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewNeedsUser
		return true
	}); err != nil {
		t.Fatal(err)
	}
	flushInboxRecoveryDigest(s)
	if len(f.notices()) != 0 {
		t.Fatal("the unconfirmed event bypassed the owner's decision wait")
	}
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = ""
		return true
	}); err != nil {
		t.Fatal(err)
	}
	s.recoverTaskInboxEvents(ctx)
	pending = unhandledInboxSignals(s)
	if len(pending) != 1 || pending[0].Key != failure.Key {
		t.Fatalf("the early sent marker permanently consumed an owner-held source: %+v", pending)
	}
	flushInboxRecoveryDigest(s)
	notices := f.notices()
	if len(notices) != 1 || !strings.Contains(notices[0], failure.Inbox.URL) ||
		!strings.Contains(notices[0], failure.Inbox.Sender) {
		t.Fatalf("the resolved owner wait lost the source-bearing escalation: %q", notices)
	}
	s.recoverTaskInboxEvents(ctx)
	flushInboxRecoveryDigest(s)
	if len(f.notices()) != 1 || len(sentToAgent(f)) != sends {
		t.Fatal("restoring the source repeated its escalation or spent another retry")
	}
}
