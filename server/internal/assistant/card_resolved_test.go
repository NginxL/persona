package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// toldCardOver reports whether the manager told the hub card id is over.
func (f *fakeHub) toldCardOver(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.told {
		var r assistantwire.FleetResolved
		if e.event == assistantwire.EventFleetResolved && json.Unmarshal(e.params, &r) == nil && r.ID == id {
			return true
		}
	}
	return false
}

// A failed start's push stands for its card: once the task is started again (a retry reopens it), the card is over
// and the hub takes the push off the phones it went to, as it does a plan's or a decision's.
func TestAFailedStartsPushLeavesOnceTheTaskStartsAgain(t *testing.T) {
	e := newTodoEnv(t, nil)
	ctx := context.Background()
	id := e.create("Rebuild the index").ID
	if err := e.s.failTodo(ctx, id, "did not start"); err != nil {
		t.Fatal(err)
	}
	before := len(e.f.pushed())
	e.s.notifyTaskFailed(ctx, Todo{ID: id, Title: "Rebuild the index", Origin: "conv9"}, "box", "did not start")
	pushes := e.f.pushed()
	if len(pushes) != before+1 || pushes[before].CardID != taskFailedCardID(id) {
		t.Fatalf("the failed start's push does not stand for its card: %+v", pushes[before:])
	}
	if e.f.toldCardOver(taskFailedCardID(id)) {
		t.Fatal("the card was over before the task started again")
	}
	if _, ok, err := e.s.reopenFailedTodo(ctx, id); err != nil || !ok {
		t.Fatalf("retry: %v %v", ok, err)
	}
	if !e.f.toldCardOver(taskFailedCardID(id)) {
		t.Fatal("the failed start's card was not over once the task started again")
	}
}

// A decision's card is over the one way every card is (resolveCard): the hub then takes its push back too.
func TestADecisionsCardIsOverTheOneWay(t *testing.T) {
	e := newTodoEnv(t, nil)
	e.s.todoPushOver("t1")
	if !e.f.toldCardOver(assistantwire.TodoAskCardID("t1")) {
		t.Fatal("the decision's card was not said to be over")
	}
	e.s.todoPushOver("")
	if e.f.toldCardOver(assistantwire.TodoAskCardID("")) {
		t.Fatal("a card without a to-do was said to be over")
	}
	if taskFailedCardID("") != "" {
		t.Fatal("a to-do without an id names a card")
	}
}
