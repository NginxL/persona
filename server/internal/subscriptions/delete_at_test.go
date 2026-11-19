package subscriptions

import (
	"testing"
)

func TestDeleteAtRejectsStaleRevisionWithoutForgettingOrDeleting(t *testing.T) {
	e := newEnv(t)
	sub := e.turnOn(e.create(itemsRecipe()))
	if err := e.svc.DeleteAt(e.ctx, sub.ID, sub.Revision-1, true, Actor{ID: "device:phone"}); codeOf(err) != CodeConflict {
		t.Fatalf("stale DeleteAt error = %v", err)
	}
	if len(e.forgot) != 0 {
		t.Fatal("a stale DeleteAt forgot destination data")
	}
	if got := e.get(sub.ID); got.Label != sub.Label || got.Revision != sub.Revision {
		t.Fatalf("stale DeleteAt changed current state: %+v", got)
	}
	if err := e.svc.DeleteAt(e.ctx, sub.ID, sub.Revision, true, Actor{ID: "device:phone"}); err != nil {
		t.Fatalf("fresh DeleteAt: %v", err)
	}
	if len(e.forgot) != 1 || e.forgot[0].ToInbox || e.forgot[0].ToKB {
		t.Fatalf("DeleteAt did not switch off before forgetting: %+v", e.forgot)
	}
	if _, _, err := e.svc.Get(e.ctx, sub.ID); codeOf(err) != CodeNotFound {
		t.Fatalf("fresh DeleteAt left the subscription: %v", err)
	}
}
