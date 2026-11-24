package subscriptions

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// A things recipe hands the Inbox its latest answer as it is (#13637), on made-up data.

// groupedRows are a chat connector's answer that relates new messages to events: a direct chat, and a group chat whose
// new messages wait to be related while two of its events were read and replied to elsewhere, all under its thread.
func groupedRows() []connector.InboxThing {
	at := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	return []connector.InboxThing{
		{Thread: "chat:a1", Kind: "dm", Title: "Ann", Sender: "Ann", Preview: "Made-up: lunch?", At: at, Last: "a5", CanReply: true},
		{Thread: "chat:g1", Kind: "group", Group: &connector.ThingGroup{Batch: "chat:g1@7", New: []string{"m6", "m7"}},
			Context: "Bo: m6\nCy: m7", At: at.Add(-time.Hour), Last: "7"},
		{Thread: "chat:g1", Kind: "group", ID: "ev1", Group: &connector.ThingGroup{Batch: "", New: []string{}}, Read: true,
			At: at.Add(-2 * time.Hour), Last: "5"},
		{Thread: "chat:g1", Kind: "group", ID: "ev2", Group: &connector.ThingGroup{Batch: "", New: []string{}},
			Done: connector.DoneReplied, At: at.Add(-3 * time.Hour), Last: "4"},
		{Thread: "chat:b1", Kind: "dm", Title: "Bo", Sender: "Bo", Preview: "Made-up: thanks", At: at.Add(-4 * time.Hour), Last: "b2"},
	}
}

// movedOn is the next answer: the batch related (not handed any more), a's newer move, a new chat c1, and b1 no longer
// handed (it fell out of the connector's window).
func movedOn() []connector.InboxThing {
	rows := groupedRows()
	a := rows[0]
	a.Preview, a.Last, a.At = "Made-up: at one?", "a6", a.At.Add(time.Minute)
	c := connector.InboxThing{Thread: "chat:c1", Kind: "dm", Title: "Cy", Sender: "Cy", Preview: "Made-up: hi", At: a.At, Last: "c1"}
	return []connector.InboxThing{a, rows[2], rows[3], c}
}

// switched is the env with its connector's Collect answering rows, switched to its default subscription and run once.
func switched(t *testing.T, rows []connector.InboxThing) (*env, *collectConn, Subscription) {
	t.Helper()
	e := newEnv(t)
	c := &collectConn{fakeConn: e.conn, rows: rows}
	e.conns.infos["example"].Conn = c
	sub, err := e.svc.Migrate(e.ctx, Migration{Connector: "example", Source: "example", Label: "Example", Recipe: genericRecipe()}, true, Actor{})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.RunDue(e.ctx)
	return e, c, sub
}

// runAgain runs the subscription once more, its interval later.
func (e *env) runAgain() {
	e.clock.advance(time.Hour)
	e.svc.RunDue(e.ctx)
}

// asBefore are the rows the Inbox gets without a kept answer: each thread's newest stored, as a build before #13637
// hands them (and this one until a subscription's next run after an upgrade). The kept answer is put back after.
func (e *env) asBefore(id string) []connector.InboxThing {
	e.t.Helper()
	cur, _ := e.store.GetSubscription(e.ctx, id)
	if cur == nil || cur.AnswerAt == nil {
		e.t.Fatalf("no kept answer: %+v", cur)
	}
	old := *cur
	old.Answer, old.AnswerAt = nil, nil
	if ok, err := e.store.UpdateSubscription(e.ctx, old, cur.Revision); !ok || err != nil {
		e.t.Fatalf("clear the answer: %v %v", ok, err)
	}
	rows := e.connectorsRows("example")
	again, _ := e.store.GetSubscription(e.ctx, id)
	back := *again
	back.Answer, back.AnswerAt = cur.Answer, cur.AnswerAt
	if ok, err := e.store.UpdateSubscription(e.ctx, back, again.Revision); !ok || err != nil {
		e.t.Fatalf("put the answer back: %v %v", ok, err)
	}
	return rows
}

// The Inbox gets the connector's latest answer, every row as it is in its order: a conversation's batch and its events'
// state stay apart under one thread, and once the connector moves on, a batch it related and a chat it no longer hands
// are not handed again. Each thread's newest stored row, as before, lost the batch and handed the related one again.
func TestAThingsRecipeHandsItsLatestAnswer(t *testing.T) {
	e, c, sub := switched(t, groupedRows())
	sameRows(t, e.connectorsRows("example"), groupedRows())
	if before := e.asBefore(sub.ID); len(before) != 3 {
		t.Fatalf("each thread's newest stored keeps one row of g1: %+v", before)
	}
	c.rows = movedOn()
	e.runAgain()
	sameRows(t, e.connectorsRows("example"), movedOn())
	before := e.asBefore(sub.ID)
	handed := map[string]bool{}
	for _, r := range before {
		handed[r.Thread+"/"+r.Last] = true
	}
	if !handed["chat:b1/b2"] {
		t.Fatalf("as before, b1's stored row would come again: %+v", before)
	}
	// What the Inbox gets is the same move for every thread the connector still hands (the Inbox only acts on rows it
	// is handed: b1's item stays as it is).
	for _, r := range movedOn() {
		if r.ID == "" && !handed[r.Thread+"/"+r.Last] {
			t.Fatalf("a row the connector hands, not as before: %+v", r)
		}
	}
}

// The upgrade: a subscription a build before #13637 ran has no kept answer, and the Inbox gets what it got before,
// each thread's newest stored, until its next run; that run's answer is then handed whole, its rows stored before the
// upgrade among them (seen already, so not stored again), and nothing else.
func TestTheUpgradeRoundHandsWhatItHandedBeforeThenTheAnswer(t *testing.T) {
	rows := groupedRows()[:1]
	rows = append(rows, groupedRows()[4])
	e, c, sub := switched(t, rows)
	before := e.asBefore(sub.ID)
	cur, _ := e.store.GetSubscription(e.ctx, sub.ID)
	old := *cur
	old.Answer, old.AnswerAt = nil, nil // as the older build left it
	if ok, _ := e.store.UpdateSubscription(e.ctx, old, cur.Revision); !ok {
		t.Fatal("clear the answer")
	}
	e.svc = e.service() // the upgraded manager starts
	sameRows(t, e.connectorsRows("example"), before)
	c.rows = append(append([]connector.InboxThing{}, rows...), groupedRows()[1:4]...) // the group chat came meanwhile
	items := len(e.store.Items())
	e.runAgain()
	sameRows(t, e.connectorsRows("example"), c.rows)
	if got := len(e.store.Items()); got != items+3 {
		t.Fatalf("the rows stored before the upgrade were stored again: %d items, %d before", got, items)
	}
}

// A restarted manager hands the kept answer at once, before any run.
func TestARestartHandsTheKeptAnswer(t *testing.T) {
	e, _, _ := switched(t, groupedRows())
	e.svc = e.service()
	sameRows(t, e.connectorsRows("example"), groupedRows())
}

// The answer's rows are kept from pruning while they are in it, however old (the connector hands them each round);
// a row it no longer has is pruned as any.
func TestTheAnswersRowsAreKeptFromPruning(t *testing.T) {
	e, c, _ := switched(t, groupedRows())
	c.rows = groupedRows()[:4] // b1 falls out
	e.clock.advance(inboxKeep + 24*time.Hour)
	e.svc.RunDue(e.ctx)
	sameRows(t, e.connectorsRows("example"), groupedRows()[:4])
	for _, it := range e.store.Items() {
		var row connector.InboxThing
		if json.Unmarshal(it.Thing, &row) == nil && row.Thread == "chat:b1" {
			t.Fatal("a row out of the answer and past the Inbox's keep was not pruned")
		}
	}
}

// Switched off and on again, the answer taken before is not handed: the Inbox gets nothing until the next run, as
// before, then its answer.
func TestASwitchTurnedOnAgainWaitsForItsNextAnswer(t *testing.T) {
	e, c, sub := switched(t, groupedRows())
	for _, on := range []bool{false, true} {
		e.clock.advance(time.Minute)
		if _, err := e.svc.Migrate(e.ctx, Migration{Connector: "example", Source: "example", Label: "Example", Recipe: genericRecipe()}, on, Actor{}); err != nil {
			t.Fatal(err)
		}
	}
	if rows := e.connectorsRows("example"); len(rows) != 0 {
		t.Fatalf("an answer from before the switch: %+v", rows)
	}
	c.rows = movedOn()
	e.svc.RunDue(e.ctx)
	sameRows(t, e.connectorsRows("example"), movedOn())
	if cur, _ := e.store.GetSubscription(e.ctx, sub.ID); cur == nil || len(cur.Answer) != len(movedOn()) {
		t.Fatalf("its answer: %+v", cur)
	}
}

// Mixed versions: the answer is kept in the subscription's run state (keep), which an older manager reads past and
// drops when it writes the subscription back; this one then hands each thread's newest stored until the next run. An
// answer with no rows stays one.
func TestTheAnswerIsKeptWithTheRunState(t *testing.T) {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for _, answer := range [][]string{{"t:1", "t:2"}, {}} {
		data, err := encode(Subscription{ID: "sub_a", Answer: answer, AnswerAt: &at})
		if err != nil {
			t.Fatal(err)
		}
		got, err := decode(data)
		if err != nil || got.AnswerAt == nil || !got.AnswerAt.Equal(at) || got.Answer == nil || len(got.Answer) != len(answer) {
			t.Fatalf("kept %v: %+v %v (%s)", answer, got, err, data)
		}
		var raw map[string]map[string]any
		_ = json.Unmarshal(data, &raw)
		delete(raw["keep"], "answer")
		delete(raw["keep"], "answer_at") // an older manager wrote it back
		older, _ := json.Marshal(raw)
		if got, err := decode(older); err != nil || got.AnswerAt != nil || got.Answer != nil {
			t.Fatalf("written back by an older manager: %+v %v", got, err)
		}
	}
}
