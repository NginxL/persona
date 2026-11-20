package assistant

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

type inboxRetryFailStore struct {
	Store
	fail         atomic.Bool
	failFeed     atomic.Bool
	feedFailures atomic.Int32
}

func (s *inboxRetryFailStore) PutSetting(ctx context.Context, key string, value any) error {
	if s.fail.Load() && key == taskTurnsKey {
		return errors.New("retry ledger unavailable")
	}
	return s.Store.PutSetting(ctx, key, value)
}

func (s *inboxRetryFailStore) AppendFeed(ctx context.Context, row db.FeedRow) error {
	if s.failFeed.Load() && row.Kind == feedRouted {
		s.feedFailures.Add(1)
		return errors.New("routed feed unavailable")
	}
	return s.Store.AppendFeed(ctx, row)
}

func inboxRetryEvent(todo, key string) superviseSignal {
	return superviseSignal{Todo: todo, Kind: sigInbox, Key: key, Inbox: &prompts.SuperviseInbox{
		Item: "github:pr12", Title: "Review", Sender: "reviewer", Preview: "Please fix the interleaved batch.",
		Count: 1, URL: linkedPRURL + "#discussion_r123",
	}}
}

func inboxRetryService(t *testing.T, st Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, st)
	turnInboxSignalsOn(t, s)
	s.superviseTimes.gather = time.Hour
	todo := startedTask(t, s, f)
	f.setSessions("box", turnRow(proto.StatusIdle, 1, ""))
	return s, f, todo
}

func inboxRetryTurn(s *Service, f *fakeHub, turn, prompt, answer string, rev int64, extra ...proto.Item) {
	ctx := context.Background()
	page := turnPage(turn, answer, true)
	page[0].Text = prompt
	end := page[len(page)-1]
	page = append(page[:len(page)-1], extra...)
	page = append(page, end)
	f.setItems(page...)
	f.setSessions("box", turnRow(proto.StatusWorking, rev, ""))
	s.taskWorking(ctx, "box", "s1")
	f.setSessions("box", turnRow(proto.StatusDone, rev+1, answer))
	s.taskTurnSettled(ctx, "box", "s1")
}

func unhandledInboxSignals(s *Service) []superviseSignal {
	s.supervise.mu.Lock()
	defer s.supervise.mu.Unlock()
	var out []superviseSignal
	for _, g := range s.supervise.pending {
		if g.Kind == sigInbox && strings.Contains(g.Detail, "unhandled") {
			out = append(out, g)
		}
	}
	return out
}

func TestInboxNoOutputRetriesOnceThenNamesUnhandledEvent(t *testing.T) {
	s, f, todo := inboxRetryService(t, nil)
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt := sent[len(sent)-1]
	before := len(sent)

	inboxRetryTurn(s, f, "empty-1", prompt, "", 10)
	if got := len(sentToAgent(f)); got != before+1 {
		t.Fatalf("empty external turn: sends = %d, want %d (one redelivery)", got, before+1)
	}
	if got := unhandledInboxSignals(s); len(got) != 0 {
		t.Fatalf("escalated before the retry ended: %+v", got)
	}
	// A duplicate end, including a stale working edge, cannot spend the retry twice.
	inboxRetryTurn(s, f, "empty-1", prompt, "", 10)
	if got := len(sentToAgent(f)); got != before+1 || len(unhandledInboxSignals(s)) != 0 {
		t.Fatalf("duplicate end retried or escalated: sends=%d", got)
	}

	inboxRetryTurn(s, f, "empty-2", prompt, "", 20)
	if got := len(sentToAgent(f)); got != before+1 {
		t.Fatalf("a third delivery was made: sends=%d", got)
	}
	signals := unhandledInboxSignals(s)
	if len(signals) != 1 {
		t.Fatalf("want one unhandled-event signal, got %+v", signals)
	}
	g := signals[0]
	if g.urgent() || g.Inbox == nil || g.Inbox.URL != linkedPRURL+"#discussion_r123" ||
		g.Inbox.Sender != "reviewer" || g.Inbox.ToAgent == "sent" {
		t.Fatalf("lost source or raised urgency on the failure signal: %+v", g)
	}
	s.superviseTimes.gather = time.Nanosecond
	s.sendDigest(context.Background())
	waitFor(t, "the source-bearing failure notice", func() bool { return len(f.notices()) > 0 })
	notice := strings.Join(f.notices(), "\n")
	for _, want := range []string{"unhandled", "review-1", "reviewer", linkedPRURL + "#discussion_r123"} {
		if !strings.Contains(notice, want) {
			t.Errorf("supervisor notice lacks %q: %s", want, notice)
		}
	}
	if todoNow(s, todo.ID).DoneAt != nil {
		t.Fatal("an empty external turn completed the task")
	}
}

func TestInboxNoOutputDoesNotRetryUnrelatedOwnerInput(t *testing.T) {
	s, f, todo := inboxRetryService(t, nil)
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt, before := sent[len(sent)-1], len(sent)
	inboxRetryTurn(s, f, "owner-stop", "Stop now; do not reply.", "", 10)
	if got := len(sentToAgent(f)); got != before {
		t.Fatalf("owner's silent stop was retried: sends=%d, want %d", got, before)
	}
	inboxRetryTurn(s, f, "owner-quotes-event", prompt+"\nStop now; do not reply.", "", 20)
	if got := len(sentToAgent(f)); got != before {
		t.Fatalf("owner's quoted event was mistaken for an external delivery: sends=%d", got)
	}
	// The unrelated turn must not acknowledge the event that is still pending.
	inboxRetryTurn(s, f, "external-empty", prompt, "", 30)
	if got := len(sentToAgent(f)); got != before+1 {
		t.Fatalf("unrelated input consumed the event: sends=%d, want %d", got, before+1)
	}
}

func TestInboxNoOutputSuccessfulTurnAcknowledgesOnlyItsEvent(t *testing.T) {
	s, f, todo := inboxRetryService(t, nil)
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
	first := sentToAgent(f)
	prompt1 := first[len(first)-1]
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-2"))
	sent := sentToAgent(f)
	prompt2, before := sent[len(sent)-1], len(sent)
	if prompt1 == prompt2 {
		t.Fatal("distinct source events have indistinguishable dispatch prompts")
	}
	inboxRetryTurn(s, f, "answered", prompt1, "I read the review and am fixing it.", 10)
	inboxRetryTurn(s, f, "second-empty", prompt2, "", 20)
	if got := len(sentToAgent(f)); got != before+1 {
		t.Fatalf("one answer acknowledged another event: sends=%d, want %d", got, before+1)
	}
	// Replaying the successful event cannot resurrect its retry.
	inboxRetryTurn(s, f, "answered-again", prompt1, "", 30)
	if got := len(sentToAgent(f)); got != before+1 {
		t.Fatalf("an acknowledged event was retried: sends=%d", got)
	}
}

func TestInboxNoOutputFailedRedeliveryNamesEvent(t *testing.T) {
	s, f, todo := inboxRetryService(t, nil)
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt, before := sent[len(sent)-1], len(sent)
	f.mu.Lock()
	f.failSend = errors.New("node refused redelivery")
	f.mu.Unlock()
	inboxRetryTurn(s, f, "empty-1", prompt, "", 10)
	signals := unhandledInboxSignals(s)
	if len(signals) != 1 || signals[0].Inbox.URL != linkedPRURL+"#discussion_r123" ||
		!strings.Contains(signals[0].Detail, "node refused redelivery") {
		t.Fatalf("redelivery failure lost its event: %+v", signals)
	}
	if got := len(sentToAgent(f)); got > before+1 {
		t.Fatalf("more than one redelivery was attempted: %d", got-before)
	}
}

func TestInboxNoOutputRedeliveryFeedFailureWaitsForActualTurn(t *testing.T) {
	for _, answer := range []string{"I read the review and am fixing it.", ""} {
		name := "answered"
		if answer == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st := &inboxRetryFailStore{Store: repositorystore.New(todoTestDB(t))}
			s, f, todo := inboxRetryService(t, st)
			g := inboxRetryEvent(todo.ID, "review-1")
			s.inboxToAgent(ctx, g)
			sent := sentToAgent(f)
			prompt, before := sent[len(sent)-1], len(sent)
			id := namedClientID("task inbox\n" + todo.ID + "\n" + g.Key)
			st.failFeed.Store(true)
			inboxRetryTurn(s, f, "empty-1", prompt, "", 10)
			if got := len(sentToAgent(f)); got != before+1 || st.feedFailures.Load() != 1 {
				t.Fatalf("redelivery not accepted before the feed failed: sends=%d, feed failures=%d",
					got, st.feedFailures.Load())
			}
			if got := unhandledInboxSignals(s); len(got) != 0 {
				t.Fatalf("accepted redelivery was reported as failed before its turn ended: %+v", got)
			}
			// Reload the pending event to check the persisted outcome, not just a queued notice.
			s.tasks.turns.mu.Lock()
			s.tasks.turns.loaded = false
			s.tasks.turns.mu.Unlock()
			s.superviseWatch(ctx, time.Now())
			var ledger struct {
				Events map[string]struct {
					Retried bool   `json:"retried"`
					Failure string `json:"failure"`
				} `json:"inbox_events"`
			}
			if _, err := st.Setting(ctx, taskTurnsKey, &ledger); err != nil {
				t.Fatal(err)
			}
			event, pending := ledger.Events[id]
			if !pending || !event.Retried || event.Failure != "" || len(unhandledInboxSignals(s)) != 0 {
				t.Fatal("feed failure discarded the event or persisted a false delivery failure")
			}
			inboxRetryTurn(s, f, "retry-result", prompt, answer, 20)
			if got := len(sentToAgent(f)); got != before+1 {
				t.Fatalf("feed failure spent another retry: sends=%d, want %d", got, before+1)
			}
			signals := unhandledInboxSignals(s)
			if answer != "" {
				ledger.Events = nil
				if _, err := st.Setting(ctx, taskTurnsKey, &ledger); err != nil {
					t.Fatal(err)
				}
				if len(signals) != 0 || len(ledger.Events) != 0 {
					t.Fatalf("the retry's answer did not acknowledge its event: %+v", signals)
				}
			} else if len(signals) != 1 || signals[0].Inbox.URL != g.Inbox.URL ||
				signals[0].Inbox.Sender != g.Inbox.Sender ||
				!strings.Contains(signals[0].Detail, "produced no output") {
				t.Fatalf("the retry's empty result did not name the unhandled event: %+v", signals)
			}
		})
	}
}

func TestInboxNoOutputRetryBudgetSurvivesLedgerReload(t *testing.T) {
	s, f, todo := inboxRetryService(t, todoTestDB(t))
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt, before := sent[len(sent)-1], len(sent)
	inboxRetryTurn(s, f, "empty-1", prompt, "", 10)
	// Force the next completion to read its provenance and attempt from Postgres.
	s.tasks.turns.mu.Lock()
	s.tasks.turns.loaded = false
	s.tasks.turns.mu.Unlock()
	inboxRetryTurn(s, f, "empty-2", prompt, "", 20)
	if got := len(sentToAgent(f)); got != before+1 || len(unhandledInboxSignals(s)) != 1 {
		t.Fatalf("reload forgot the retry budget: sends=%d, failures=%d", got, len(unhandledInboxSignals(s)))
	}
}

func TestInboxNoOutputExplicitStopDoesNotAcknowledgeEvent(t *testing.T) {
	s, f, todo := inboxRetryService(t, nil)
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt, before := sent[len(sent)-1], len(sent)
	inboxRetryTurn(s, f, "stopped", prompt, "", 10,
		proto.Item{Kind: proto.KindNotice, Role: "system", Text: userStoppedNotice})
	if got := len(sentToAgent(f)); got != before {
		t.Fatalf("explicit stop was retried: sends=%d", got)
	}
	inboxRetryTurn(s, f, "resumed-empty", prompt, "", 20)
	if got := len(sentToAgent(f)); got != before+1 {
		t.Fatalf("explicit stop acknowledged an unhandled event: sends=%d", got)
	}
}

func TestInboxNoOutputFailuresRemainDistinctAndRecoverable(t *testing.T) {
	s, f, todo := inboxRetryService(t, todoTestDB(t))
	for i, key := range []string{"review-1", "review-2"} {
		s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, key))
		sent := sentToAgent(f)
		prompt := sent[len(sent)-1]
		inboxRetryTurn(s, f, key+"-empty-1", prompt, "", int64(i*20+10))
		inboxRetryTurn(s, f, key+"-empty-2", prompt, "", int64(i*20+20))
	}
	if got := unhandledInboxSignals(s); len(got) != 2 {
		t.Fatalf("one unhandled source absorbed another: %+v", got)
	}
	s.supervise.mu.Lock()
	clear(s.supervise.pending)
	s.supervise.mu.Unlock()
	s.tasks.turns.mu.Lock()
	s.tasks.turns.loaded = false
	s.tasks.turns.mu.Unlock()
	s.superviseWatch(context.Background(), time.Now())
	if got := unhandledInboxSignals(s); len(got) != 2 {
		t.Fatalf("reload lost the source-bearing failures: %+v", got)
	}
}

func TestInboxNoOutputModelErrorStillRetries(t *testing.T) {
	s, f, todo := inboxRetryService(t, nil)
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt, before := sent[len(sent)-1], len(sent)
	page := turnPage("api-error", "", true)
	page[0].Text = prompt
	f.setItems(page...)
	f.setSessions("box", turnRow(proto.StatusWorking, 10, ""))
	s.taskWorking(context.Background(), "box", "s1")
	row := turnRow(proto.StatusDone, 11, "")
	row.TurnError = "API Error: upstream returned no output"
	f.setSessions("box", row)
	s.taskTurnSettled(context.Background(), "box", "s1")
	if got := len(sentToAgent(f)); got != before+1 {
		t.Fatalf("an empty API-error turn was not redelivered once: sends=%d", got)
	}
}

func TestInboxNoOutputDuplicateDispatchDoesNotResend(t *testing.T) {
	s, f, todo := inboxRetryService(t, nil)
	g := inboxRetryEvent(todo.ID, "review-1")
	s.inboxToAgent(context.Background(), g)
	before := len(sentToAgent(f))
	s.inboxToAgent(context.Background(), g)
	if got := len(sentToAgent(f)); got != before {
		t.Fatalf("duplicate source was delivered again: sends=%d, want %d", got, before)
	}
}

func TestInboxNoOutputRetryRespectsAnOwnerWait(t *testing.T) {
	s, f, todo := inboxRetryService(t, nil)
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt, before := sent[len(sent)-1], len(sent)
	page := turnPage("empty-owner-wait", "", true)
	page[0].Text = prompt
	f.setItems(page...)
	f.setSessions("box", turnRow(proto.StatusWorking, 10, ""))
	s.taskWorking(context.Background(), "box", "s1")
	if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
		v.State = reviewNeedsUser
		return true
	}); err != nil {
		t.Fatal(err)
	}
	f.setSessions("box", turnRow(proto.StatusDone, 11, ""))
	s.taskTurnSettled(context.Background(), "box", "s1")
	if got := len(sentToAgent(f)); got != before {
		t.Fatalf("automatic retry bypassed an owner wait: sends=%d, want %d", got, before)
	}
}

func TestInboxNoOutputPersistenceFailureStillNamesEvent(t *testing.T) {
	st := &inboxRetryFailStore{Store: repositorystore.New(todoTestDB(t))}
	s, f, todo := inboxRetryService(t, st)
	s.inboxToAgent(context.Background(), inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt, before := sent[len(sent)-1], len(sent)
	st.fail.Store(true)
	inboxRetryTurn(s, f, "empty-write-failed", prompt, "", 10)
	if got := len(sentToAgent(f)); got != before {
		t.Fatalf("redelivered without a persisted retry budget: sends=%d", got)
	}
	signals := unhandledInboxSignals(s)
	if len(signals) != 1 || signals[0].Inbox.Sender != "reviewer" || !strings.Contains(signals[0].Detail, "could not be saved") {
		t.Fatalf("ledger failure lost its source-bearing fallback: %+v", signals)
	}
	st.fail.Store(false)
}
