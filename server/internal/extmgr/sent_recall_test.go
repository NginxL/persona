package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/db"
)

// eventLog notes what happened in what order.
type eventLog struct {
	mu   sync.Mutex
	list []string
}

func (l *eventLog) add(s string) {
	l.mu.Lock()
	l.list = append(l.list, s)
	l.mu.Unlock()
}

func (l *eventLog) got() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.list, ",")
}

// recallingThreads is refThreads that can take a message back (connector.Recaller): it notes what it is asked, and
// answers as the test says. It can take back example_send only, and nothing older than limit when that is set.
type recallingThreads struct {
	*refThreads
	events *eventLog
	limit  time.Duration
	recall func(ctx context.Context, m connector.SentMessage) error

	mu    sync.Mutex
	asked []connector.SentMessage
}

func (c *recallingThreads) CanRecall(action string, age time.Duration) bool {
	return action == "example_send" && (c.limit == 0 || age <= c.limit)
}

func (c *recallingThreads) Recall(ctx context.Context, m connector.SentMessage) error {
	c.mu.Lock()
	c.asked = append(c.asked, m)
	recall := c.recall
	c.mu.Unlock()
	c.events.add("recall")
	if recall != nil {
		return recall(ctx, m)
	}
	return nil
}

func (c *recallingThreads) askedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.asked)
}

// recallEnv is a manager with the ledger in memory and a connector that can recall, on a clock the test moves.
type recallEnv struct {
	t      *testing.T
	h      *Manager
	st     *sentLedgerStore
	c      *recallingThreads
	events *eventLog
	now    time.Time
}

func newRecallEnv(t *testing.T) *recallEnv {
	t.Helper()
	h := newTestManager(t)
	st := newSentLedgerStore()
	h.store = st
	h.connectors = h.newConnectors([]connector.Rule{{Connector: "*", Action: "*", Decision: connector.Allow}})
	ev := &eventLog{}
	c := &recallingThreads{refThreads: &refThreads{threadsConnector: newThreadsConnector(), ref: "41"}, events: ev}
	if err := h.addConnector(c); err != nil {
		t.Fatal(err)
	}
	e := &recallEnv{t: t, h: h, st: st, c: c, events: ev, now: time.Now()}
	sentLedgerNow = func() time.Time { return e.now }
	t.Cleanup(func() { sentLedgerNow = time.Now })
	return e
}

// send writes through the gate (so the row is the one a real write makes) and puts the clock age after it.
func (e *recallEnv) send(age time.Duration) int64 {
	e.t.Helper()
	if r := e.h.callConnector(context.Background(), asker{}, "example_send", json.RawMessage(`{"chat":"u1","text":"hi"}`)); r.IsError {
		e.t.Fatalf("send: %+v", r)
	}
	rows := e.st.all()
	row := rows[len(rows)-1]
	e.now = row.At.Add(age)
	return row.ID
}

// put writes a row of its own, as if the gate had.
func (e *recallEnv) put(row db.SentAsUser) int64 {
	e.t.Helper()
	row.At = time.Now()
	id, err := e.st.RecordSentAsUser(context.Background(), row)
	if err != nil {
		e.t.Fatal(err)
	}
	e.now = row.At
	return id
}

func (e *recallEnv) confirm(_ context.Context, _ db.SentAsUser) error {
	e.events.add("confirm")
	return nil
}

func (e *recallEnv) recall(id int64) (db.SentAsUser, error) {
	return e.h.RecallSent(context.Background(), id, e.confirm)
}

func (e *recallEnv) recalledAt(id int64) *time.Time {
	row, _, _ := e.st.SentAsUserByID(context.Background(), id)
	return row.RecalledAt
}

func codeOf(err error) string {
	var re *RecallError
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// A row a write made is taken back through its connector, which is given what the row says; the user is asked after
// the checks and before the connector, and only then is the row marked, at the time of the recall. Asked again, it
// is already taken back, and neither the user nor the connector is bothered.
func TestRecallSentTakesTheMessageBack(t *testing.T) {
	needsLarkModule(t)
	e := newRecallEnv(t)
	id := e.send(time.Hour)
	row, err := e.recall(id)
	if err != nil {
		t.Fatal(err)
	}
	asked := e.c.asked
	if len(asked) != 1 || asked[0].Action != "example_send" || asked[0].Ref != "41" || len(objectOf(t, asked[0].Target)) != 1 || objectOf(t, asked[0].Target)["chat"] != "u1" {
		t.Fatalf("the connector was asked %+v", asked)
	}
	if got := e.events.got(); got != "confirm,recall" {
		t.Fatalf("order %s", got)
	}
	if row.ID != id || row.RecalledAt == nil || !row.RecalledAt.Equal(e.now) || e.recalledAt(id) == nil || !e.recalledAt(id).Equal(e.now) {
		t.Fatalf("returned %+v, stored %v, clock %v", row, e.recalledAt(id), e.now)
	}

	later := e.now.Add(time.Minute)
	e.now = later
	row, err = e.recall(id)
	if codeOf(err) != RecallAlreadyRecalled || row.ID != id || row.RecalledAt == nil || row.RecalledAt.Equal(later) {
		t.Fatalf("asked again: %+v %v", row, err)
	}
	if got := e.events.got(); got != "confirm,recall" || e.c.askedCount() != 1 {
		t.Fatalf("asked again, and the user or the connector heard of it: %s, %d", got, e.c.askedCount())
	}
}

// Whatever cannot be taken back is refused with its own code, before the user is asked and before the connector is
// told, and the row is left as it was.
func TestRecallSentRefuses(t *testing.T) {
	needsLarkModule(t)
	for _, tc := range []struct {
		name  string
		setup func(e *recallEnv) int64
		code  string
	}{
		{"a row that is not there", func(e *recallEnv) int64 { return 99 }, RecallNotFound},
		{"a ledger that is not there", func(e *recallEnv) int64 { e.h.store = newKeepStore(); return 1 }, RecallNoDatabase},
		{"a message taken back before", func(e *recallEnv) int64 {
			id := e.send(0)
			if _, err := e.recall(id); err != nil {
				e.t.Fatal(err)
			}
			return id
		}, RecallAlreadyRecalled},
		{"a connector that cannot recall", func(e *recallEnv) int64 {
			id := e.send(0)
			e.h.connectors.Remove("example")
			if err := e.h.addConnector(&refThreads{threadsConnector: newThreadsConnector(), ref: "41"}); err != nil {
				e.t.Fatal(err)
			}
			return id
		}, RecallNotRecallable},
		{"a connector that is gone", func(e *recallEnv) int64 { id := e.send(0); e.h.connectors.Remove("example"); return id }, RecallNotRecallable},
		{"a write that gave no ref", func(e *recallEnv) int64 { e.c.ref = ""; return e.send(0) }, RecallNotRecallable},
		{"a kind of message it cannot take back", func(e *recallEnv) int64 {
			return e.put(db.SentAsUser{Connector: "example", Action: "example_note", MessageRef: "5"})
		}, RecallNotRecallable},
		{"a send an extension recorded", func(e *recallEnv) int64 {
			return e.put(db.SentAsUser{Connector: "example", Action: sentRecordAction, MessageRef: "5"})
		}, RecallNotRecallable},
		{"a message older than the window", func(e *recallEnv) int64 { return e.send(24*time.Hour + time.Second) }, RecallTooOld},
		{"a message older than the connector allows", func(e *recallEnv) int64 { e.c.limit = 2 * time.Hour; return e.send(3 * time.Hour) }, RecallTooOld},
		{"a connector not signed in", func(e *recallEnv) int64 {
			id := e.send(0)
			e.c.state.Store(connector.StateNotSignedIn)
			return id
		}, RecallUnavailable},
		{"a connector switched off", func(e *recallEnv) int64 {
			id := e.send(0)
			if err := e.h.connectors.SetEnabled("example", false); err != nil {
				e.t.Fatal(err)
			}
			return id
		}, RecallUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecallEnv(t)
			id := tc.setup(e)
			was := e.recalledAt(id)
			asked, heard := e.c.askedCount(), e.events.got()
			row, err := e.recall(id)
			if codeOf(err) != tc.code {
				t.Fatalf("%v (code %q), want %s", err, codeOf(err), tc.code)
			}
			if e.c.askedCount() != asked || e.events.got() != heard {
				t.Errorf("the user or the connector was told: %s, asked %d", e.events.got(), e.c.askedCount())
			}
			if now := e.recalledAt(id); (now == nil) != (was == nil) || now != nil && !now.Equal(*was) {
				t.Errorf("recalled_at %v, was %v", now, was)
			}
			if tc.code != RecallNotFound && tc.code != RecallNoDatabase && row.ID != id {
				t.Errorf("no row came back: %+v", row)
			}
		})
	}
}

// A message can be taken back until sent_recall_hours after it was sent (24 hours when it is 0, never too late below
// 0, and not a number of hours that wraps the clock around), and until the connector's own limit when that is shorter: the stricter of the two, to the second.
func TestRecallSentWindow(t *testing.T) {
	needsLarkModule(t)
	const h = time.Hour
	for _, tc := range []struct {
		name  string
		hours int
		limit time.Duration // the connector's
		age   time.Duration
		late  bool
	}{
		{"the default, at the limit", 0, 0, 24 * h, false},
		{"the default, a second past it", 0, 0, 24*h + time.Second, true},
		{"one hour, at the limit", 1, 0, h, false},
		{"one hour, a second past it", 1, 0, h + time.Second, true},
		{"two days, within it", 48, 0, 47 * h, false},
		{"two days, past it", 48, 0, 48*h + time.Second, true},
		{"no limit", -1, 0, 100 * 24 * h, false},
		{"hours that wrap a duration around to a negative one", 10_000_000, 0, 100 * 24 * h, false},
		{"the most hours a number holds", math.MaxInt, 0, 100 * 24 * h, false},
		{"the connector is stricter, at its limit", 0, 2 * h, 2 * h, false},
		{"the connector is stricter, past its limit", 0, 2 * h, 2*h + time.Second, true},
		{"the window is stricter", 1, 48 * h, h + time.Second, true},
		{"no limit but the connector's", -1, 2 * h, 3 * h, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecallEnv(t)
			e.h.cfg.SentRecallHours = tc.hours
			e.c.limit = tc.limit
			id := e.send(tc.age)
			_, err := e.recall(id)
			switch {
			case tc.late && codeOf(err) != RecallTooOld:
				t.Fatalf("%v", err)
			case !tc.late && err != nil:
				t.Fatalf("%v", err)
			}
			if got := e.recalledAt(id) != nil; got == tc.late {
				t.Fatalf("marked: %v", got)
			}
		})
	}
}

// A recall needs the caller to say how the user agreed: without that, nothing happens. What the caller's confirm says
// is the answer: its error stops the recall and comes back as it is, with nothing told to the connector or marked.
func TestRecallSentNeedsTheUsersWord(t *testing.T) {
	needsLarkModule(t)
	e := newRecallEnv(t)
	id := e.send(0)

	_, err := e.h.RecallSent(context.Background(), id, nil)
	if err == nil || codeOf(err) != "" || e.c.askedCount() != 0 || e.recalledAt(id) != nil {
		t.Fatalf("without a way to confirm: %v, asked %d, marked %v", err, e.c.askedCount(), e.recalledAt(id))
	}

	declined := errors.New("the user said no")
	var saw db.SentAsUser
	row, err := e.h.RecallSent(context.Background(), id, func(_ context.Context, r db.SentAsUser) error { saw = r; return declined })
	if !errors.Is(err, declined) || codeOf(err) != "" || saw.ID != id || e.c.askedCount() != 0 || e.recalledAt(id) != nil || row.RecalledAt != nil {
		t.Fatalf("declined: %v, saw %+v, asked %d, marked %v", err, saw, e.c.askedCount(), e.recalledAt(id))
	}

	// A request that went away while the user was asked has not tried anything.
	ctx, cancel := context.WithCancel(context.Background())
	_, err = e.h.RecallSent(ctx, id, func(context.Context, db.SentAsUser) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || e.c.askedCount() != 0 || e.recalledAt(id) != nil {
		t.Fatalf("gone while asking: %v, asked %d", err, e.c.askedCount())
	}

	// After all that it can still be taken back.
	if _, err := e.recall(id); err != nil || e.recalledAt(id) == nil {
		t.Fatalf("%v", err)
	}
}

// markFailing is the ledger that cannot mark while fail is set.
type markFailing struct {
	*sentLedgerStore
	fail *atomic.Bool
}

func (m markFailing) MarkSentAsUserRecalled(ctx context.Context, id int64, at time.Time) (bool, error) {
	if m.fail.Load() {
		return false, errors.New("connection reset by peer")
	}
	return m.sentLedgerStore.MarkSentAsUserRecalled(ctx, id, at)
}

// What the connector says decides what is written: a refusal and an outcome nobody can tell leave the row as it was
// (and each can be asked again), the message being gone marks it, whether or not the request is still there to hear
// it, and a mark that fails after that says so without pretending the message is still there.
func TestRecallSentOutcomes(t *testing.T) {
	needsLarkModule(t)
	retry := func(e *recallEnv, id int64) {
		t.Helper()
		e.c.recall = nil
		if _, err := e.recall(id); err != nil || e.recalledAt(id) == nil {
			t.Fatalf("asked again: %v, marked %v", err, e.recalledAt(id))
		}
	}

	t.Run("refused", func(t *testing.T) {
		e := newRecallEnv(t)
		e.c.recall = func(context.Context, connector.SentMessage) error {
			return errors.New("GitHub DELETE: 403 not allowed")
		}
		id := e.send(0)
		row, err := e.recall(id)
		if codeOf(err) != RecallRefused || !strings.Contains(err.Error(), "403 not allowed") || e.recalledAt(id) != nil || row.RecalledAt != nil {
			t.Fatalf("%v (code %q), marked %v", err, codeOf(err), e.recalledAt(id))
		}
		retry(e, id)
	})
	t.Run("found it could not, before sending anything", func(t *testing.T) {
		e := newRecallEnv(t)
		e.c.recall = func(context.Context, connector.SentMessage) error {
			return fmt.Errorf("%w: the row has no comment id", connector.ErrCannotRecall)
		}
		id := e.send(0)
		if _, err := e.recall(id); codeOf(err) != RecallNotRecallable || e.recalledAt(id) != nil {
			t.Fatalf("%v (code %q), marked %v", err, codeOf(err), e.recalledAt(id))
		}
	})
	t.Run("not available, before sending anything", func(t *testing.T) {
		e := newRecallEnv(t)
		e.c.recall = func(context.Context, connector.SentMessage) error {
			return fmt.Errorf("%w: the computer is offline", connector.ErrRecallUnavailable)
		}
		id := e.send(0)
		if _, err := e.recall(id); codeOf(err) != RecallUnavailable || e.recalledAt(id) != nil {
			t.Fatalf("%v (code %q), marked %v", err, codeOf(err), e.recalledAt(id))
		}
		retry(e, id)
	})
	t.Run("an outcome nobody can tell", func(t *testing.T) {
		e := newRecallEnv(t)
		e.c.recall = func(context.Context, connector.SentMessage) error {
			return fmt.Errorf("%w: the connection broke", connector.ErrRecallUnknown)
		}
		id := e.send(0)
		if _, err := e.recall(id); codeOf(err) != RecallUnknown || e.recalledAt(id) != nil {
			t.Fatalf("%v (code %q), marked %v", err, codeOf(err), e.recalledAt(id))
		}
		retry(e, id)
	})
	t.Run("no answer before the request gives up", func(t *testing.T) {
		e := newRecallEnv(t)
		e.c.recall = func(ctx context.Context, _ connector.SentMessage) error {
			<-ctx.Done()
			return errors.New("timed out") // a connector's own words: nothing says it is a deadline
		}
		id := e.send(0)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := e.h.RecallSent(ctx, id, e.confirm); codeOf(err) != RecallUnknown || e.recalledAt(id) != nil {
			t.Fatalf("%v (code %q), marked %v", err, codeOf(err), e.recalledAt(id))
		}
		retry(e, id)
	})
	t.Run("gone, and the request went away meanwhile", func(t *testing.T) {
		e := newRecallEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		e.c.recall = func(context.Context, connector.SentMessage) error { cancel(); return nil }
		id := e.send(0)
		row, err := e.h.RecallSent(ctx, id, e.confirm)
		if err != nil || row.RecalledAt == nil || e.recalledAt(id) == nil {
			t.Fatalf("%v, returned %v, marked %v", err, row.RecalledAt, e.recalledAt(id))
		}
	})
	t.Run("gone, and the ledger cannot say so", func(t *testing.T) {
		e := newRecallEnv(t)
		fail := &atomic.Bool{}
		fail.Store(true)
		e.h.store = markFailing{e.st, fail}
		id := e.send(0)
		_, err := e.recall(id)
		if err == nil || codeOf(err) != "" || strings.Contains(err.Error(), "connection reset") || e.recalledAt(id) != nil || e.c.askedCount() != 1 {
			t.Fatalf("%v, marked %v, asked %d", err, e.recalledAt(id), e.c.askedCount())
		}
		fail.Store(false)
		if _, err := e.recall(id); err != nil || e.recalledAt(id) == nil || e.c.askedCount() != 2 {
			t.Fatalf("asked again: %v, marked %v, asked %d", err, e.recalledAt(id), e.c.askedCount())
		}
	})
}

// A connector that never answers is given sentRecallTimeout and no more, whatever the request's own time: the recall
// ends as an outcome nobody can tell (the connector's words are its own, a plain error that says nothing about a
// deadline), nothing is marked, and asking again goes through.
func TestRecallSentEndsWhenTheConnectorNeverAnswers(t *testing.T) {
	needsLarkModule(t)
	was := sentRecallTimeout
	sentRecallTimeout = 100 * time.Millisecond
	t.Cleanup(func() { sentRecallTimeout = was })
	e := newRecallEnv(t)
	e.c.recall = func(ctx context.Context, _ connector.SentMessage) error {
		select {
		case <-ctx.Done():
			return errors.New("the connection was closed")
		case <-time.After(3 * time.Second): // a recall that was given no time limit would have been left to wait this long
			return nil
		}
	}
	id := e.send(0)
	start := time.Now()
	_, err := e.recall(id)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("it took %v", took)
	}
	if codeOf(err) != RecallUnknown || e.recalledAt(id) != nil {
		t.Fatalf("%v (code %q), marked %v", err, codeOf(err), e.recalledAt(id))
	}
	e.c.recall = nil
	if _, err := e.recall(id); err != nil || e.recalledAt(id) == nil {
		t.Fatalf("asked again: %v, marked %v", err, e.recalledAt(id))
	}
}

// Two recalls of one row at once both reach the connector, which allows repeats, but the row is marked once: one of
// them is the recall, the other finds it taken back already.
func TestRecallSentTwiceAtOnce(t *testing.T) {
	needsLarkModule(t)
	e := newRecallEnv(t)
	var inside atomic.Int32
	release := make(chan struct{})
	e.c.recall = func(context.Context, connector.SentMessage) error {
		if inside.Add(1) == 2 {
			close(release)
		}
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
		return nil
	}
	id := e.send(0)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.recall(id)
		}()
	}
	wg.Wait()
	done, already := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			done++
		case codeOf(err) == RecallAlreadyRecalled:
			already++
		default:
			t.Errorf("%v", err)
		}
	}
	if done != 1 || already != 1 || e.recalledAt(id) == nil {
		t.Fatalf("%d recalled, %d found it taken back (%v), marked %v", done, already, errs, e.recalledAt(id))
	}
}

// Mail cannot be taken back (a sent mail is out of the connector's hands), and neither can a send an extension recorded
// under the id of a connector that can recall other things (GitHub's, registered here: it is the kind of message that
// is refused, not a connector that is missing): no connector but one that says it can offers a recall.
func TestRecallSentNeverMailsNorRecordedSends(t *testing.T) {
	needsGitHubModule(t)
	needsLarkModule(t)
	h := newTestManager(t)
	st := newSentLedgerStore()
	h.store = st
	h.mail = &sendingMail{replyMail: *newReplyMail(t), id: "1A2B.3C4D@hub.example.com"}
	h.connectors = h.newConnectors([]connector.Rule{{Connector: "mail", Action: "mail_send", Decision: connector.Allow}})
	h.registerConnectors()
	if _, ok := connector.RecallerOf(h.connectors.Get("github")); !ok {
		t.Fatal("GitHub is not registered as a connector that can recall")
	}
	if h.connectors.Get("mail") == nil {
		t.Fatal("mail is not registered")
	}
	args := json.RawMessage(`{"account_id":"acc1","to":[{"email":"someone@example.com"}],"subject":"示例","text":"附件已更新"}`)
	if r := h.callConnector(context.Background(), asker{}, "mail_send", args); r.IsError {
		t.Fatalf("send: %+v", r)
	}
	if _, err := st.RecordSentAsUser(context.Background(), db.SentAsUser{At: time.Now(), Connector: "github", Action: sentRecordAction, MessageRef: "5"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sentLedgerNow = time.Now })
	for _, tc := range []struct{ connector, why string }{
		{"mail", "cannot take back what it sent"},
		{"github", "cannot take back this kind of message"},
	} {
		var row db.SentAsUser
		for _, r := range st.all() {
			if r.Connector == tc.connector {
				row = r
			}
		}
		if row.ID == 0 || row.MessageRef == "" {
			t.Fatalf("%s: no row with a message ref: %+v", tc.connector, row)
		}
		sentLedgerNow = func() time.Time { return row.At }
		_, err := h.RecallSent(context.Background(), row.ID, func(context.Context, db.SentAsUser) error {
			t.Errorf("%s: the user was asked", row.Connector)
			return nil
		})
		if codeOf(err) != RecallNotRecallable || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("%s %s: %v", row.Connector, row.Action, err)
		}
	}
}
