package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestExpiredQuestionIrreversibleDeadlineRemindsButStillNeedsOwnerDecision(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	ix, _ := e.expireWith(strings.Replace(strings.Replace(askDeadlineArgsJSON, "%s", "6h", 1), "%s", "false", 1))
	todo := e.onlyAskTodo()
	e.s.askTodoDue(context.Background(), todo.ID, false, time.Now().Add(7*time.Hour))
	a, _ := e.s.askTodoOf(context.Background(), todo.ID)
	if a.DueAt.IsZero() || a.ByDefault || !a.AnsweredAt.IsZero() {
		t.Fatalf("an irreversible deadline must only remind, not answer: %+v", a)
	}
	again := newAskTodoEnv(t, database)
	view := expiredQuestionPayload(again, todo.ID)
	if view.Ask == nil || view.DoneAt != nil || len(view.Ask.Questions[0].Options) != 2 {
		t.Fatalf("the reminded question must still offer its original choices after reload: %+v", view)
	}
	if code := answerExpiredQuestion(again, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != 200 {
		t.Fatalf("the owner must still decide an overdue irreversible question: HTTP %d", code)
	}
	waitFor(t, "the owner's complete answer is delivered", func() bool { return len(again.told("c1x1x1")) == 1 })
	rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil || len(rows) != 1 || rows[0].Source != db.DecisionManual || rows[0].Choice != "Wait" ||
		len(again.told("c1x1x1")) != 1 || again.get(todo.ID).DoneAt == nil {
		t.Fatalf("only the owner's decision may finish this question: rows=%+v err=%v", rows, err)
	}
	t.Log("irreversible_due=true ask_present=true owner_http=200 manual_decisions=1 asker_messages=1")
}

func TestExpiredQuestionReversibleDeadlineDefaultIsNotAnotherOwnerDecision(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	ix, _ := e.expireWith(strings.Replace(strings.Replace(askDeadlineArgsJSON, "%s", "6h", 1), "%s", "true", 1))
	todo := e.onlyAskTodo()
	e.s.askTodoDue(context.Background(), todo.ID, false, time.Now().Add(7*time.Hour))
	waitFor(t, "the default closes its question", func() bool { return e.get(todo.ID).DoneAt != nil })
	if view := expiredQuestionPayload(e, todo.ID); view.Ask != nil {
		t.Fatalf("a default already delivered must not ask for another decision: %+v", view.Ask)
	}
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != 409 {
		t.Fatalf("the default already answered the question: HTTP %d", code)
	}
	rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil || len(rows) != 1 || rows[0].Source != db.DecisionAgent || len(e.told("c1x1x1")) != 1 {
		t.Fatalf("only the default must be recorded and relayed: rows=%+v err=%v", rows, err)
	}
	t.Log("reversible_due=true ask_present=false owner_http=409 default_decisions=1 asker_messages=1")
}

func TestExpiredQuestionDefaultInFlightRemainsReservedAfterClaimExpires(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	ix, _ := e.expireWith(strings.Replace(strings.Replace(askDeadlineArgsJSON, "%s", "6h", 1), "%s", "true", 1))
	todo := e.onlyAskTodo()
	// A restart can lose the sending goroutine and outlive its claim, but the persisted default still owns the answer.
	e.s.askTodos.mu.Lock()
	a := &e.s.askTodos.list[0]
	a.Deadline, a.DueAt = time.Now().Add(-time.Minute), time.Now().UTC()
	a.ByDefault, a.ClaimedAt = true, time.Now().Add(-askClaimHold-time.Second)
	err := e.s.saveAskTodosLocked(context.Background())
	e.s.askTodos.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	again := newAskTodoEnv(t, database)
	if view := expiredQuestionPayload(again, todo.ID); view.Ask != nil {
		t.Fatalf("a reserved default must not become a second owner decision after its claim expires: %+v", view.Ask)
	}
	if code := answerExpiredQuestion(again, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != 409 {
		t.Fatalf("a persisted default must keep its answer reservation: HTTP %d", code)
	}
	if _, claimed := again.s.claimAskTodo(context.Background(), todo.ID, true); claimed {
		t.Fatal("the manual decision path must not take over a reserved default either")
	}
	t.Log("default_reserved=true claim_expired=true ask_present=false owner_http=409")
}

func TestExpiredQuestionFailedDefaultReturnsToOwnerDespiteNonzeroDueAt(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	ix, _ := e.expireWith(strings.Replace(strings.Replace(askDeadlineArgsJSON, "%s", "6h", 1), "%s", "true", 1))
	todo := e.onlyAskTodo()
	setSettings(t, e.s, func(v *Settings) { v.Main = "" })
	e.sessions("server")
	e.s.askTodoDue(context.Background(), todo.ID, false, time.Now().Add(7*time.Hour))
	waitFor(t, "the failed default returns its claim to the owner", func() bool {
		a, _ := e.s.askTodoOf(context.Background(), todo.ID)
		return !a.DueAt.IsZero() && !a.ByDefault && !a.held(time.Now())
	})
	again := newAskTodoEnv(t, database)
	if view := expiredQuestionPayload(again, todo.ID); view.Ask == nil {
		t.Fatal("a failed default must leave the owner's question answerable despite DueAt")
	}
	if code := answerExpiredQuestion(again, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != 200 {
		t.Fatalf("the owner must be able to answer after the default failed: HTTP %d", code)
	}
	waitFor(t, "the owner's answer after the failed default", func() bool { return len(again.told("c1x1x1")) == 1 })
	rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil || len(rows) != 1 || rows[0].Source != db.DecisionManual || len(again.told("c1x1x1")) != 1 {
		t.Fatalf("the failed default must not record a second decision: rows=%+v err=%v", rows, err)
	}
	t.Log("default_failed=true deadline_due=true ask_present=true owner_http=200 manual_decisions=1")
}

func TestExpiredQuestionUnacceptedAnswerCanRetryAfterDestinationRecovers(t *testing.T) {
	for _, choice := range []string{"Wait", "Merge (Recommended)"} {
		t.Run(choice, func(t *testing.T) {
			database := todoTestDB(t)
			e := newAskTodoEnv(t, database)
			ix, _ := e.expire()
			todo := e.onlyAskTodo()
			setSettings(t, e.s, func(v *Settings) { v.Main = "" })
			e.sessions("server")
			if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != 502 {
				t.Fatalf("an answer with nowhere to relay must fail, not complete: HTTP %d", code)
			}
			rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
			if err != nil || len(rows) != 0 || e.get(todo.ID).DoneAt != nil || len(e.told("c1x1x1")) != 0 {
				t.Fatalf("an unaccepted answer must leave no decisions or relay: rows=%+v err=%v", rows, err)
			}
			again := newAskTodoEnv(t, database)
			if code := answerExpiredQuestion(again, todo.ID, ix.CreatedAt, map[string]any{"option": choice}); code != 200 {
				t.Fatalf("retrying an unaccepted answer after recovery must succeed: HTTP %d", code)
			}
			waitFor(t, "the recovered answer is delivered", func() bool { return len(again.told("c1x1x1")) == 1 })
			rows, err = database.ListTodoDecisions(context.Background(), todo.ID, true)
			messages := again.told("c1x1x1")
			if err != nil || len(rows) != 1 || rows[0].Choice != choice || len(messages) != 1 || !strings.Contains(messages[0], choice) ||
				again.get(todo.ID).DoneAt == nil {
				t.Fatalf("recovery must record the chosen answer and send it once: rows=%+v messages=%q err=%v", rows, messages, err)
			}
			t.Logf("first_http=502 retry_http=200 choice=%q decisions=%d asker_messages=1 closed=true", choice, len(rows))
		})
	}
}

func TestExpiredQuestionClaimedAnswerSurvivesOwnerCloseOrDelete(t *testing.T) {
	for _, when := range []string{"before acceptance commit", "after acceptance commit"} {
		for _, action := range []string{"close", "delete"} {
			t.Run(when+"/"+action, func(t *testing.T) {
				database := todoTestDB(t)
				e := newAskTodoEnv(t, database)
				ix, _ := e.expire()
				todo := e.onlyAskTodo()
				interrupt := func() {
					a, _ := e.s.askTodoOf(context.Background(), todo.ID)
					if when == "before acceptance commit" && !a.held(time.Now()) ||
						when == "after acceptance commit" && a.AnsweredAt.IsZero() {
						t.Fatalf("the owner action must interleave at %s: %+v", when, a)
					}
					if action == "close" {
						e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
					} else {
						e.must("DELETE", "/api/v1/todos/"+todo.ID, "")
					}
					if len(e.told("c1x1x1")) != 0 {
						t.Fatal("closing a claimed question must leave its terminal message to the answer")
					}
				}
				e.f.mu.Lock()
				if when == "before acceptance commit" {
					e.f.beforeSessionGet = interrupt
				} else {
					e.f.beforeAssistantSend = interrupt
				}
				e.f.mu.Unlock()
				if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != 200 {
					t.Fatalf("a claimed answer must not be abandoned when the owner %ss its to-do: HTTP %d", action, code)
				}
				waitFor(t, "the claimed answer is delivered once", func() bool { return len(e.told("c1x1x1")) == 1 })
				messages := e.told("c1x1x1")
				a, _ := e.s.askTodoOf(context.Background(), todo.ID)
				if len(messages) != 1 || !strings.Contains(messages[0], "Wait") || a.AnsweredAt.IsZero() ||
					!a.DroppedAt.IsZero() || !a.ClaimedAt.IsZero() {
					t.Fatalf("the asking conversation needs exactly one terminal answer: messages=%q record=%+v", messages, a)
				}
				cur, exists := e.s.todoNow(todo.ID)
				if action == "delete" && exists || action == "close" && (!exists || cur.DoneAt == nil) {
					t.Fatal("delivering the claimed answer must not resurrect or reopen the to-do")
				}
				if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != 409 {
					t.Fatalf("the terminal answer must not be replayed: HTTP %d", code)
				}
				t.Logf("interleave=%q action=%s answer_http=200 asker_messages=1 answered=true dropped=false resurrected=false", when, action)
			})
		}
	}
}

type beforeExpiredQuestionHistoryRead struct{}

type expiredQuestionHistoryStore struct{ *db.DB }

func (s *expiredQuestionHistoryStore) InteractionRecord(ctx context.Context, id string) (*db.InteractionRecord, error) {
	if beforeRead, ok := ctx.Value(beforeExpiredQuestionHistoryRead{}).(func()); ok {
		beforeRead()
	}
	return s.DB.InteractionRecord(ctx, id)
}

func TestExpiredQuestionDropWinsBeforeInlineAnswerClaim(t *testing.T) {
	for _, action := range []struct{ name, method, body string }{
		{"close", http.MethodPatch, `{"done":true}`},
		{"delete", http.MethodDelete, ""},
	} {
		t.Run(action.name, func(t *testing.T) {
			ctx := context.Background()
			database := todoTestDB(t)
			e := newAskTodoEnv(t, database)
			ix, _ := expireRecorded(e, database, askTodoArgs)
			todo := e.onlyAskTodo()
			a, _ := e.s.askTodoOf(ctx, todo.ID)
			// A legacy question reads its card after initial validation and before claiming the answer.
			a.QuestionDetails = nil
			if err := database.PutSetting(ctx, askTodosKey, []askTodo{a}); err != nil {
				t.Fatal(err)
			}
			e = newAskTodoEnv(t, &expiredQuestionHistoryStore{DB: database})
			path := "/api/v1/todos/" + todo.ID
			dropClaimed := false
			closeFirst := func() {
				e.must(action.method, path, action.body)
				a, ok := e.s.askTodoOf(ctx, todo.ID)
				if !ok || a.DroppedAt.IsZero() || !a.ClaimedAt.IsZero() {
					t.Fatalf("the drop must claim the unanswered question first: %+v", a)
				}
				waitFor(t, "the drop notice", func() bool { return len(e.told("c1x1x1")) == 1 })
				dropClaimed = true
			}
			body := `{"asked_at":"` + ix.CreatedAt.Format(time.RFC3339Nano) + `","option":"Wait"}`
			req := httptest.NewRequest(http.MethodPost, path+"/accept", strings.NewReader(body))
			req = req.WithContext(context.WithValue(req.Context(), beforeExpiredQuestionHistoryRead{}, closeFirst))
			reply := httptest.NewRecorder()
			e.s.ServeHTTP(reply, req)
			if !dropClaimed {
				t.Fatal("the answer did not reach the interleaving point")
			}
			if reply.Code != http.StatusConflict {
				t.Errorf("an inline answer after the drop: HTTP %d, want 409", reply.Code)
			}
			a, _ = e.s.askTodoOf(ctx, todo.ID)
			if a.DroppedAt.IsZero() || !a.AnsweredAt.IsZero() || !a.ClaimedAt.IsZero() {
				t.Errorf("the drop must be the only terminal outcome: %+v", a)
			}
			if messages := e.told("c1x1x1"); len(messages) != 1 || !strings.Contains(messages[0], "没有在待办里给答案") {
				t.Errorf("the asking conversation needs one drop notice: %q", messages)
			}
			if rows, err := database.ListTodoDecisions(ctx, todo.ID, true); err != nil || len(rows) != 0 {
				t.Errorf("a losing inline answer must not record decisions: %+v, %v", rows, err)
			}
		})
	}
}

func TestReopenedAskTodoManualDecisionStillReturns(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	e.expire()
	todo := e.onlyAskTodo()
	path := "/api/v1/todos/" + todo.ID
	e.must(http.MethodPatch, path, `{"done":true}`)
	waitFor(t, "the drop notice", func() bool { return len(e.told("c1x1x1")) == 1 })
	e.must(http.MethodPatch, path, `{"done":false}`)
	e.must(http.MethodPost, path+"/decisions", `{"question":"Merge the release PR now?","choice":"Wait"}`)
	waitFor(t, "the reopened question to close after its manual answer", func() bool {
		return e.get(todo.ID).DoneAt != nil
	})
	if messages := e.told("c1x1x1"); len(messages) != 2 || !strings.Contains(messages[1], "Wait") {
		t.Fatalf("the explicit decision after reopening must reach the asker once: %q", messages)
	}
}
