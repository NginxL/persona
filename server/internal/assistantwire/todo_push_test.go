package assistantwire

import "testing"

// A to-do push key names the to-do and the reason, and every form of related key still names its to-do.
func TestTodoPushKeys(t *testing.T) {
	key := TodoPushKey("todo_1", TodoPushNeedsUser)
	if key != "todo:todo_1:needs_user" {
		t.Fatalf("key %q", key)
	}
	if id, reason, ok := TodoPushKeyParts(key); !ok || id != "todo_1" || reason != TodoPushNeedsUser {
		t.Fatalf("parts of %q: %q %q %v", key, id, reason, ok)
	}
	for _, other := range []string{"todo:todo_1@2026-11-14T10:00:00Z", "todo:todo_1", "todo::decide", "todo:todo_1:", "ask:todo_1", ""} {
		if _, _, ok := TodoPushKeyParts(other); ok {
			t.Errorf("%q taken for a to-do push key", other)
		}
	}
	for _, k := range []string{key, "todo:todo_1@2026-11-14T10:00:00Z", "todo:todo_1"} {
		if got := RelatedTodoID(k); got != "todo_1" {
			t.Errorf("RelatedTodoID(%q) = %q", k, got)
		}
	}
	if card := TodoAskCardID("todo_1"); TodoOfAskCard(card) != "todo_1" || TodoOfAskCard("plan_1") != "" {
		t.Fatalf("ask card %q", card)
	}
}
