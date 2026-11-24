package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestInboxNoOutputEscalationDedupPersistsPerSource(t *testing.T) {
	for _, delivery := range []supervisionDelivery{digestDelivery, leaderDelivery} {
		t.Run(fmt.Sprint(delivery), func(t *testing.T) {
			st := &memStore{}
			ctx, now := context.Background(), time.Now()
			for i, tc := range []struct {
				todo, source, route string
				want                int
			}{
				{"todo_example", "review-a@v1", "unhandled", 1},
				{"todo_example", "review-b@v1", "unhandled", 1},
				{"todo_example", "ordinary-message", "sent", 1},
				{"todo_example", "review-a@v1", "unhandled", 0},
				{"todo_example", "review-b@v1", "unhandled", 0},
				{"todo_example", "ordinary-message", "sent", 1},
				{"todo_example", "review-a@v2", "unhandled", 1},
				{"todo_other", "review-a@v1", "unhandled", 1},
			} {
				// Each pass restarts the manager after ordinary deduplication expires.
				s, _ := newQuietServiceOn(t, st)
				e := prompts.SuperviseEntry{ID: tc.todo, Kind: sigInbox,
					Machine: fmt.Sprintf("machine-%d", i), AgentID: fmt.Sprintf("agent-%d", i),
					Last: fmt.Sprintf("A later task reply %d.", i)}
				g := inboxRetryEvent(tc.todo, tc.source)
				g.Inbox.ToAgent = tc.route
				g.Detail = fmt.Sprintf("External event unhandled: failure detail %d.", i)
				g.To = fmt.Sprintf("supervisor-%d", i)
				out, _, _, err := s.reserveSupervisionDelivery(ctx, []prompts.SuperviseEntry{e},
					[]superviseSignal{g}, now.Add(time.Duration(i)*time.Hour), delivery)
				if err != nil || len(out) != tc.want {
					t.Errorf("pass %d (%+v): %d entries, %v; want %d", i, tc, len(out), err, tc.want)
				}
			}
			var history map[string]superviseFingerprint
			if ok, err := st.Setting(ctx, superviseDedupKey, &history); err != nil || !ok {
				t.Fatalf("read durable event fingerprints: %v, %v", ok, err)
			}
			if len(history) != 4 {
				t.Errorf("want four independent event fingerprints, got %+v", history)
			}
			for key, seen := range history {
				if seen.Todo == "" {
					t.Errorf("event fingerprint is not bounded by its open task: %s = %+v", key, seen)
				}
			}
		})
	}
}

func TestInboxNoOutputEscalationReplayAfterRestartDoesNotNotifyTwice(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	st := todoTestDB(t)
	s, f, todo := inboxRetryService(t, st)
	s.inboxToAgent(ctx, inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt := sent[len(sent)-1]
	inboxRetryTurn(s, f, "empty-1", prompt, "", 10)
	inboxRetryTurn(s, f, "empty-2", prompt, "", 20)
	failure := unhandledInboxSignals(s)[0]
	if failure.urgent() || failure.level() != supervisionTriage {
		t.Fatal("an unhandled external event must retain default supervision urgency")
	}
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{failure}, "", now.Add(-2*time.Hour), time.Time{}) {
		t.Fatal("first escalation was not delivered")
	}
	if got := f.notices(); len(got) != 1 || !strings.Contains(got[0], failure.Inbox.URL) ||
		!strings.Contains(got[0], failure.Inbox.Sender) {
		t.Fatalf("first escalation lost its source: %q", got)
	}
	ordinary := inboxRetryEvent(todo.ID, "ordinary-message")
	s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[ordinary.id()] = ordinary })
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{ordinary}, "", now.Add(-time.Hour), time.Time{}) {
		t.Fatal("ordinary Inbox delivery was blocked")
	}

	restarted, again := newQuietServiceOn(t, st)
	turnInboxSignalsOn(t, restarted)
	restarted.superviseTimes.gather = time.Hour
	if err := restarted.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	again.setSessions("box", turnRow(proto.StatusDone, 100, "A later, unrelated reply."))
	restarted.recoverTaskInboxEvents(ctx)
	recovered := unhandledInboxSignals(restarted)
	if len(recovered) != 1 || recovered[0].Key != failure.Key {
		t.Fatalf("failed event was not recovered from Postgres: %+v", recovered)
	}
	restarted.deliverDigest(ctx, restarted.settings(ctx), "conv1", recovered, "", now, time.Time{})
	if got := again.notices(); len(got) != 0 {
		t.Fatalf("restart and ordinary Inbox delivery repeated an escalation: %q", got)
	}
	if got := sentToAgent(again); len(got) != 0 {
		t.Fatalf("recovery spent another automatic retry: %q", got)
	}
}

func TestInboxNoOutputEscalationDoesNotBlameReplacementAgent(t *testing.T) {
	s, f, todo := inboxRetryService(t, nil)
	ctx, now := context.Background(), time.Now()
	s.inboxToAgent(ctx, inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt := sent[len(sent)-1]
	inboxRetryTurn(s, f, "empty-1", prompt, "", 10)
	inboxRetryTurn(s, f, "empty-2", prompt, "", 20)
	failure := unhandledInboxSignals(s)[0]
	if failure.Machine != "box" || failure.Session != "s1" {
		t.Errorf("unhandled source lost its original recipient: %+v", failure)
	}
	inboxRetryPeer(t, s, todo, "replacement")
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Agents[0].ReleasedAt = &now
	s.todos.mu.Unlock()
	replacement := turnRow(proto.StatusDone, 100, "I handled the review after a manual handoff.")
	replacement.SessionID = "replacement"
	f.setSessions("box", replacement)
	e, ok := s.digestEntry(ctx, failure, "")
	if !ok || e.AgentID != "s1" || e.Machine != "box" || e.Last != "" {
		t.Fatalf("old failed source was attributed to its replacement or a different turn's reply: %+v, %v", e, ok)
	}
}

type inboxEscalationStore struct {
	Store
	queueDown atomic.Bool
}

func (s *inboxEscalationStore) Setting(ctx context.Context, key string, value any) (bool, error) {
	if key == relayKey && s.queueDown.Load() {
		return false, errors.New("synthetic queue admission failure")
	}
	return s.Store.Setting(ctx, key, value)
}

func (s *inboxEscalationStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == relayKey && s.queueDown.Load() {
		return errors.New("synthetic queue admission failure")
	}
	return s.Store.PutSetting(ctx, key, value)
}

func TestInboxNoOutputEscalationFailedDispatchDoesNotConsumeNotice(t *testing.T) {
	st := &inboxEscalationStore{Store: todoTestDB(t)}
	s, f, todo := inboxRetryService(t, st)
	ctx, now := context.Background(), time.Now()
	s.inboxToAgent(ctx, inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt := sent[len(sent)-1]
	inboxRetryTurn(s, f, "empty-1", prompt, "", 10)
	inboxRetryTurn(s, f, "empty-2", prompt, "", 20)
	failure := unhandledInboxSignals(s)[0]
	st.queueDown.Store(true)
	f.mu.Lock()
	f.failAssistant = errors.New("synthetic supervisor dispatch failure")
	f.mu.Unlock()
	if s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{failure}, "", now, time.Time{}) {
		t.Fatal("failed dispatch and queue admission consumed the escalation")
	}
	st.queueDown.Store(false)
	f.mu.Lock()
	f.failAssistant = nil
	f.mu.Unlock()
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{failure}, "", now.Add(time.Hour), time.Time{}) ||
		len(f.notices()) != 1 {
		t.Fatalf("the failed escalation could not be delivered later: %q", f.notices())
	}
	e, ok := s.digestEntry(ctx, failure, "")
	if !ok {
		t.Fatal("failed event disappeared before acknowledgment")
	}
	s.supervise.dedup = nil
	out, _, _, err := s.reserveSupervision(ctx, []prompts.SuperviseEntry{e}, []superviseSignal{failure}, now.Add(2*time.Hour))
	if err != nil || len(out) != 0 {
		t.Fatalf("accepted retry of the supervisor notice was not durable: %d, %v", len(out), err)
	}
}

func TestInboxNoOutputEscalationRechecksSourceInsteadOfQueuingStaleNotice(t *testing.T) {
	ctx := context.Background()
	st := todoTestDB(t)
	s, f, todo := inboxRetryService(t, st)
	s.inboxToAgent(ctx, inboxRetryEvent(todo.ID, "review-1"))
	sent := sentToAgent(f)
	prompt := sent[len(sent)-1]
	inboxRetryTurn(s, f, "empty-1", prompt, "", 10)
	inboxRetryTurn(s, f, "empty-2", prompt, "", 20)
	failure := unhandledInboxSignals(s)[0]
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server",
		Assistant: true, Status: proto.StatusWorking})
	ordinary := inboxRetryEvent(todo.ID, "ordinary-message")
	s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[ordinary.id()] = ordinary })
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{ordinary}, "", time.Now(), time.Time{}) ||
		s.queuedFor("conv1") != 1 {
		t.Fatal("ordinary Inbox notice did not queue behind the supervisor's turn")
	}
	if s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{failure}, "", time.Now(), time.Time{}) ||
		s.queuedFor("conv1") != 1 {
		t.Fatal("an unhandled source was consumed by a rendered queue instead of awaiting revalidation")
	}
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server", idle)
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the ordinary notice to drain", func() bool { return s.queuedFor("conv1") == 0 })
	restarted, again := newQuietServiceOn(t, st)
	turnInboxSignalsOn(t, restarted)
	restarted.superviseTimes.gather = time.Hour
	// Match sendDigest's serialization while this test forces an immediate delivery. A restart timer may win the race,
	// but it cannot run beside the explicit call and produce a second notice.
	restarted.supervise.digestMu.Lock()
	defer restarted.supervise.digestMu.Unlock()
	restarted.supervise.dedupMu.Lock()
	defer restarted.supervise.dedupMu.Unlock()
	if err := restarted.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.recoverTaskInboxEvents(ctx)
	recovered := unhandledInboxSignals(restarted)
	delivered := true
	if len(again.notices()) == 0 {
		if len(recovered) != 1 {
			t.Fatalf("a supervisor becoming available after restart lost its source-bearing notice: recovered=%d", len(recovered))
		}
		delivered = restarted.deliverDigest(ctx, restarted.settings(ctx), "conv1", recovered, "", time.Now(), time.Time{})
	}
	if !delivered || len(again.notices()) != 1 {
		t.Fatalf("a supervisor becoming available after restart did not deliver exactly one source-bearing notice: recovered=%d delivered=%v notices=%d queued=%d", len(recovered), delivered, len(again.notices()), restarted.queuedFor("conv1"))
	}
}

func TestInboxNoOutputEscalationDedupPrunesClosedTasks(t *testing.T) {
	s, _ := newQuietServiceOn(t, &memStore{})
	ctx, now := context.Background(), time.Now()
	failure := inboxRetryEvent("todo_closed", "review-1")
	failure.Inbox.ToAgent = "unhandled"
	e := prompts.SuperviseEntry{ID: failure.Todo, Kind: sigInbox}
	if out, _, _, err := s.reserveSupervision(ctx, []prompts.SuperviseEntry{e}, []superviseSignal{failure}, now); err != nil || len(out) != 1 {
		t.Fatalf("initial escalation reservation = %d, %v", len(out), err)
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	s.todos.todos[failure.Todo] = &Todo{ID: failure.Todo, DoneAt: &now}
	s.todos.mu.Unlock()
	s.pruneAllStoppedDedup(ctx)
	var history map[string]superviseFingerprint
	if _, err := s.store().Setting(ctx, superviseDedupKey, &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("closed task retained its event fingerprint: %+v", history)
	}
}
