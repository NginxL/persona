package hub

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// A to-do's pushes to the owner are keyed by the to-do and their reason (push_todo_dedup.go): one to-do rang four
// times in an hour while its key carried the round.

// todoDecision is the assistant's decision push of to-do id, round asked, keyed by the to-do and reason.
func todoDecision(id string, asked time.Time, reason string) assistantwire.Notification {
	return assistantwire.Notification{ID: "ntf_" + randomHex(8), Kind: assistantwire.NotifyDecide, Subtitle: "Persona",
		Title: "结论：修登录崩溃", Body: "🤔 要你拍板：先灰度还是直接全量", Level: assistantwire.LevelP0,
		RelatedKey: assistantwire.TodoPushKey(id, reason),
		Todo:       &assistantwire.NotifyTodo{ID: id, AskedAt: asked, Recommend: "先灰度 10%"}}
}

// todoNeedsUser is the assistant's needs-user push of to-do id.
func todoNeedsUser(id string) assistantwire.Notification {
	return gradingNotification(assistantwire.LevelP0, assistantwire.TodoPushKey(id, assistantwire.TodoPushNeedsUser), "任务需要你")
}

// Four pushes of one to-do about one reason, each round asked anew, ring once; another reason of the same to-do rings
// once too, and so does the same reason of another to-do. A push held as a repeat says so.
func TestTodoPushRingsOncePerTodoAndReason(t *testing.T) {
	h, f := gradingHub(t)
	attempts := gradingAttempts(h)
	asked := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	for i := range 4 {
		if err := h.deliverPush(todoDecision("todo_1", asked.Add(time.Duration(i)*15*time.Minute), assistantwire.TodoPushDecide)); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 1 {
		t.Fatalf("%d pushes for one to-do's decision asked four times, want 1", *attempts)
	}
	for i := range 4 {
		out, err := h.deliverPushFrom(assistantwire.AssistantID, todoNeedsUser("todo_1"))
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && (out == nil || out.held != interceptReasonDedup || out.devices != 0) {
			t.Fatalf("repeat needs-user push %d = %+v, want held as %s", i, out, interceptReasonDedup)
		}
	}
	if *attempts != 2 {
		t.Fatalf("%d pushes after the needs-user ones, want 2 (one more reason)", *attempts)
	}
	if err := h.deliverPush(todoDecision("todo_1", asked, assistantwire.TodoPushDecideOverdue)); err != nil {
		t.Fatal(err)
	}
	if err := h.deliverPush(todoDecision("todo_2", asked, assistantwire.TodoPushDecide)); err != nil {
		t.Fatal(err)
	}
	if *attempts != 4 {
		t.Fatalf("%d pushes, want 4: the overdue reason and another to-do ring once each", *attempts)
	}
	// A day later the same reason rings again (a daily reminder), once.
	moveGradingClock(h, h.push.now().Add(gradingDedupWindow))
	for range 2 {
		if err := h.deliverPush(todoDecision("todo_1", asked, assistantwire.TodoPushDecide)); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 5 {
		t.Fatalf("%d pushes, want 5 after the window", *attempts)
	}
	f.calls(t, 1)
}

// A push whose key carries the round (an older manager's, todo:<id>@<round>) is not held by this rule: each round
// rings as it always did.
func TestLegacyTodoKeysRingEveryRound(t *testing.T) {
	h, f := gradingHub(t)
	attempts := gradingAttempts(h)
	asked := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	for i := range 3 {
		n := todoDecision("todo_1", asked.Add(time.Duration(i)*time.Minute), assistantwire.TodoPushDecide)
		n.RelatedKey = ""
		if err := h.deliverPush(n); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 3 {
		t.Fatalf("%d pushes, want 3", *attempts)
	}
	f.calls(t, 1)
}

// Once the manager says the to-do no longer waits on the owner (EventFleetResolved naming its ask), the next push
// about it rings. The decision push carries no interaction_id: the iOS app would clear it as a stale card, unanswered,
// and nothing is pushed to take it back.
func TestTodoPushOverRingsAgain(t *testing.T) {
	h, f, open := withdrawHub(t, featureWithdraw)
	asked := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	if err := h.deliverPush(todoDecision("todo_1", asked, assistantwire.TodoPushDecide)); err != nil {
		t.Fatal(err)
	}
	first := f.wait(t, 1)[0]
	if p := open(first); p["interaction_id"] != nil {
		t.Fatalf("decision push payload %v carries an interaction_id", p)
	}
	if err := h.deliverPush(todoDecision("todo_1", asked.Add(time.Minute), assistantwire.TodoPushDecide)); err != nil {
		t.Fatal(err)
	}
	reportResolved(t, h, assistantwire.TodoAskCardID("todo_1"), "")
	if err := h.deliverPush(todoDecision("todo_1", asked.Add(time.Hour), assistantwire.TodoPushDecide)); err != nil {
		t.Fatal(err)
	}
	got := f.wait(t, 2)
	if got[1].PushType != "alert" {
		t.Fatalf("after the to-do was over: %+v, want the next decision's alert (the repeat before it was held)", got[1])
	}
	for _, r := range got {
		if r.PushType == "background" {
			t.Fatalf("a withdrawal went out for a decision push: %+v", r)
		}
	}
}

// The to-do a related key names, in every form a client follows: the legacy round, the reason, the bare to-do.
func TestRelatedTodoKeysStillNameTheirTodo(t *testing.T) {
	for _, key := range []string{"todo:todo_1", "todo:todo_1@2026-11-14T10:00:00Z", "todo:todo_1:needs_user", " todo:todo_1:decide "} {
		if got := relatedTodoID(key, false); got != "todo_1" {
			t.Errorf("relatedTodoID(%q) = %q", key, got)
		}
		if got := relatedDestination(key); got != "todo:todo_1" {
			t.Errorf("relatedDestination(%q) = %q", key, got)
		}
	}
	for _, key := range []string{"todo:", "todo::needs_user", "ask:todo_1", "reply:s1@1"} {
		if got := relatedTodoID(key, false); got != "" {
			t.Errorf("relatedTodoID(%q) = %q, want none", key, got)
		}
	}
}
