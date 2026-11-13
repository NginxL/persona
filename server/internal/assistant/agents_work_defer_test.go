package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

type failingDeferredWorkStore struct {
	workMemStore
	fail bool
}

func (s *failingDeferredWorkStore) PutSessionWork(ctx context.Context, w db.SessionWork) error {
	if s.fail {
		return errors.New("database unavailable")
	}
	return s.workMemStore.PutSessionWork(ctx, w)
}

func deferredCardFixture(t *testing.T, store *failingDeferredWorkStore, options ...proto.QuestionOption) (*Service, *fakeHub, proto.Interaction) {
	t.Helper()
	f := newFakeHub()
	c := stopped("s1", "Continue now?")
	c.UpdatedAt = time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	f.setSessions("box", c)
	s := newServiceOn(t, f, store)
	if err := s.putTriageSwitches(context.Background(), TriageSwitches{Enabled: true, Decide: true}); err != nil {
		t.Fatal(err)
	}
	f.triage = `{"phase":"blocked","on":"user","line":"Choose when to continue","questions":[{"question":"Continue now?","options":[{"label":"Continue"},{"label":"Later","action":"defer"}]}]}`
	if len(options) > 0 {
		out := triageOut{Phase: proto.WorkBlocked, On: proto.WaitUser, Line: "Choose when to continue",
			Questions: []proto.Question{{Question: "Continue now?", Options: []proto.QuestionOption{{Label: "Continue"}, options[0]}}}}
		encoded, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		f.triage = string(encoded)
	}
	triage(t, s, "s1")
	return s, f, s.workCards()[0]
}

func TestWorkCardCancelAndDeferSurviveRestart(t *testing.T) {
	for _, tc := range []struct {
		name       string
		resolution proto.Resolution
	}{
		{"cancel", proto.Resolution{Cancel: true}},
		{"legacy dismiss", proto.Resolution{Dismiss: true}},
		{"structured defer", proto.Resolution{Answers: map[string]string{"Continue now?": "Later"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &failingDeferredWorkStore{}
			s, f, ix := deferredCardFixture(t, st)
			if !ix.CanCancel {
				t.Fatal("work card does not advertise cancellation")
			}
			answerCard(t, s, ix.ID, tc.resolution)
			if e := entryOf(t, s, "s1"); !e.Work.Deferred || e.InteractionID != "" || e.WakeAt != nil {
				t.Fatalf("not durably deferred: %+v", e)
			}
			if got := f.sentToAgents(); len(got) != 0 {
				t.Fatalf("sent an answer: %v", got)
			}
			s.waitWorkEvents()
			s = newServiceOn(t, f, st)
			s.workTick(context.Background(), time.Now())
			triage(t, s, "s1")
			if len(s.workCards()) != 0 {
				t.Fatal("completed card returned after restart")
			}
			if _, err := s.workAnswer(context.Background(), ix.ID, tc.resolution); err == nil {
				t.Fatal("old card was accepted again")
			}
			// A genuinely new turn still receives its own question, even with the same text.
			c := stopped("s1", "Continue now?")
			c.UpdatedAt = time.Now().UTC()
			f.setSessions("box", c)
			triage(t, s, "s1")
			cards := s.workCards()
			if len(cards) != 1 || cards[0].ID == ix.ID {
				t.Fatalf("new question suppressed: %+v", cards)
			}
			s.waitWorkEvents()
		})
	}
}

// A legacy custom pause used to be sent to the agent, starting a new turn that
// produced another reminder. It must finish locally just like structured defer.
func TestLegacyWorkCardPauseDoesNotStartAnotherTurn(t *testing.T) {
	st := &failingDeferredWorkStore{}
	label := "先不管，稍后再验收" // i18n-ignore: legacy persisted option fixture
	s, f, ix := deferredCardFixture(t, st, proto.QuestionOption{Label: label})
	answerCard(t, s, ix.ID, proto.Resolution{Answers: map[string]string{"Continue now?": label}})
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("pause restarted the agent: %v", got)
	}
	s.waitWorkEvents()
	s = newServiceOn(t, f, st)
	s.workTick(context.Background(), time.Now())
	triage(t, s, "s1")
	if len(s.workCards()) != 0 || !entryOf(t, s, "s1").Work.Deferred {
		t.Fatal("legacy pause was replayed")
	}
	s.waitWorkEvents()
}

func TestWorkCardCancelWriteFailureRemainsRetryable(t *testing.T) {
	st := &failingDeferredWorkStore{}
	s, f, ix := deferredCardFixture(t, st)
	st.fail = true
	if _, err := s.workAnswer(context.Background(), ix.ID, proto.Resolution{Cancel: true}); err == nil {
		t.Fatal("failed persistence acknowledged cancellation")
	}
	if e := entryOf(t, s, "s1"); e.InteractionID != ix.ID || e.Work.Deferred {
		t.Fatalf("failed write changed work: %+v", e)
	}
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != ix.ID {
		t.Fatalf("failed write lost card: %+v", cards)
	}
	st.fail = false
	answerCard(t, s, ix.ID, proto.Resolution{Cancel: true})
	if len(s.workCards()) != 0 || len(f.sentToAgents()) != 0 {
		t.Fatal("retry did not cancel without an answer")
	}
	s.waitWorkEvents()
}

func TestWorkCardRejectsCancelWithAnswerBeforeClaim(t *testing.T) {
	st := &failingDeferredWorkStore{}
	s, f, ix := deferredCardFixture(t, st)
	_, err := s.workAnswer(context.Background(), ix.ID, proto.Resolution{Cancel: true, Answers: map[string]string{"Continue now?": "Continue"}})
	if !errors.Is(err, errInvalidQuestionCancel) {
		t.Fatalf("mixed cancellation accepted: %v", err)
	}
	if len(s.workCards()) != 1 || entryOf(t, s, "s1").Work.Deferred || len(f.sentToAgents()) != 0 {
		t.Fatal("invalid cancellation changed the question")
	}
	s.waitWorkEvents()
}

func TestDeferredWorkOptionRequiresPresentedUnambiguousChoice(t *testing.T) {
	for _, tc := range []struct {
		label, action        string
		multi, offered, want bool
	}{
		{"Later", proto.QuestionOptionDefer, false, true, true},
		{"Later", proto.QuestionOptionDefer, true, true, false},
		{"Later", "", false, true, false},
		{"先不管，稍后再验收", "", false, true, true},
		{"先不管，稍后再处理", "", false, true, true},
		{"先不管，稍后再验收", "", false, false, false},
		{"先不管，稍后再验收，但先保存", "", false, true, false},
		{"先不管旧版本，稍后再验收", "", false, true, false},
		{"先不管，稍后再部署生产", "", false, true, false},
	} {
		q := proto.Question{MultiSelect: tc.multi}
		if tc.offered {
			q.Options = []proto.QuestionOption{{Label: tc.label, Action: tc.action}}
		}
		if got := deferredWorkOption(q, tc.label); got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
}

func TestRestoredWorkCardGainsCancellation(t *testing.T) {
	st := &failingDeferredWorkStore{}
	s, f, ix := deferredCardFixture(t, st)
	s.waitWorkEvents()
	ix.CanCancel = false
	ix.Questions[0].Options[1] = proto.QuestionOption{Label: answerSkip} // a built-in defer from before option actions
	f.hubCards = []proto.Interaction{ix}
	s = newServiceOn(t, f, st)
	if !s.loadWork(context.Background()) {
		t.Fatal("load failed")
	}
	if cards := s.workCards(); len(cards) != 1 || !cards[0].CanCancel || cards[0].Questions[0].Options[1].Action != proto.QuestionOptionDefer {
		t.Fatalf("legacy card not upgraded: %+v", cards)
	}
}

type workCardReadHookHub struct {
	*fakeHub
	beforeSessionRead func()
}

func (h *workCardReadHookHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == DeviceSessionsList && h.beforeSessionRead != nil {
		hook := h.beforeSessionRead
		h.beforeSessionRead = nil
		hook()
	}
	return h.fakeHub.DeviceCall(ctx, p)
}

func TestWorkCardTickCannotRestoreConcurrentlyCancelledCard(t *testing.T) {
	st := &failingDeferredWorkStore{}
	s, f, ix := deferredCardFixture(t, st)
	if !s.loadWork(context.Background()) {
		t.Fatal("load failed")
	}
	// Simulate an offline card whose recovery has begun. Cancellation completes
	// while the recovery checks the node's current session state.
	s.work.mu.Lock()
	delete(s.work.cards, ix.ID)
	s.work.mu.Unlock()
	setHub(s, &workCardReadHookHub{fakeHub: f, beforeSessionRead: func() {
		answerCard(t, s, ix.ID, proto.Resolution{Cancel: true})
	}})
	s.cardTick(context.Background(), s.newWorkView(context.Background()), [2]string{"box", "s1"}, time.Now())
	if len(s.workCards()) != 0 || entryOf(t, s, "s1").InteractionID != "" {
		t.Fatal("an in-flight recovery restored the cancelled card")
	}
	s.waitWorkEvents()
}

type blockedWorkWriteStore struct {
	*failingDeferredWorkStore
	once             sync.Once
	started, release chan struct{}
}

func (s *blockedWorkWriteStore) PutSessionWork(ctx context.Context, w db.SessionWork) error {
	s.once.Do(func() { close(s.started); <-s.release })
	return s.failingDeferredWorkStore.PutSessionWork(ctx, w)
}

func TestWorkCardCancelSerializesWithOlderWrites(t *testing.T) {
	st := &failingDeferredWorkStore{}
	s, _, ix := deferredCardFixture(t, st)
	s.waitWorkEvents()
	old := entryOf(t, s, "s1")
	blocked := &blockedWorkWriteStore{failingDeferredWorkStore: st, started: make(chan struct{}), release: make(chan struct{})}
	s.store = func() Store { return blocked }
	oldDone := make(chan struct{})
	go func() { s.saveWork(old); close(oldDone) }()
	<-blocked.started
	cancelDone := make(chan error, 1)
	go func() {
		_, err := s.workAnswer(context.Background(), ix.ID, proto.Resolution{Cancel: true})
		cancelDone <- err
	}()
	// A slow write for this session must not hold the work book's global lock.
	snapshotDone := make(chan struct{})
	go func() { s.workCards(); close(snapshotDone) }()
	select {
	case <-snapshotDone:
	case <-time.After(time.Second):
		close(blocked.release)
		t.Fatal("database write blocked all work snapshots")
	}
	close(blocked.release)
	<-oldDone
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	// A delayed caller can still hold the old copy after cancellation. Neither
	// its persistence nor its same-turn state update may resurrect the card.
	s.saveWork(old)
	s.putWork(context.Background(), old)
	rows, err := st.SessionWorks(context.Background(), time.Now().Add(-time.Hour))
	if err != nil || len(rows) != 1 || !rows[0].Work.Deferred || rows[0].InteractionID != "" {
		t.Fatalf("stale write overwrote cancellation: %+v, %v", rows, err)
	}
	if e := entryOf(t, s, "s1"); !e.Work.Deferred || e.InteractionID != "" {
		t.Fatalf("stale state resurrected card: %+v", e)
	}
	s.waitWorkEvents()
}

func TestWorkCardCancelMergesConcurrentSameTurnWithdrawal(t *testing.T) {
	st := &failingDeferredWorkStore{}
	s, _, ix := deferredCardFixture(t, st)
	s.waitWorkEvents()
	blocked := &blockedWorkWriteStore{failingDeferredWorkStore: st, started: make(chan struct{}), release: make(chan struct{})}
	s.store = func() Store { return blocked }
	cancelDone := make(chan error, 1)
	go func() {
		_, err := s.workAnswer(context.Background(), ix.ID, proto.Resolution{Cancel: true})
		cancelDone <- err
	}()
	<-blocked.started
	withdrawn := entryOf(t, s, "s1")
	withdrawn.InteractionID, withdrawn.Work.InteractionID = "", ""
	withdrawn.Work.Line = "Updated by the same turn"
	putDone := make(chan struct{})
	go func() { s.putWork(context.Background(), withdrawn); close(putDone) }()
	deadline := time.Now().Add(time.Second)
	for entryOf(t, s, "s1").InteractionID != "" {
		if time.Now().After(deadline) {
			close(blocked.release)
			t.Fatal("same-turn update did not reach memory")
		}
		time.Sleep(time.Millisecond)
	}
	close(blocked.release)
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	<-putDone
	rows, err := st.SessionWorks(context.Background(), time.Now().Add(-time.Hour))
	if err != nil || len(rows) != 1 || !rows[0].Work.Deferred || rows[0].InteractionID != "" || rows[0].Work.Line != withdrawn.Work.Line {
		t.Fatalf("same-turn withdrawal lost cancellation or latest fields: %+v, %v", rows, err)
	}
	s.waitWorkEvents()
}
