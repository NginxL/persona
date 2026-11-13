package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestDecisionChatAnswerSettlesOnlyItsOriginalRound(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo := newSupervisedServiceOn(t, database)
	waitsForReview(t, s, f, todo.ID, "Choose a rollout plan.")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	asked := askOf(s, todo.ID).AskedAt
	// A cached Web tab can answer immediately after the service restarts, before it lists to-dos again.
	s = newServiceOn(t, f, database)
	reply := func(round string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]string{"todo_id": todo.ID, "text": "Use the alternative with a smaller limit.", "asked_at": round})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/assistant/conversations/conv1/route", strings.NewReader(string(raw))))
		return w
	}
	sent := len(sentToAgent(f))
	if _, err := s.routeDecisionAnswer(context.Background(), "other-conversation", todo.ID, asked.Format(timeKeyLayout), "Wrong task."); err == nil {
		t.Fatal("a different conversation answered this task's decision")
	}
	if w := reply(asked.Add(-time.Second).Format(timeKeyLayout)); w.Code != http.StatusConflict {
		t.Fatalf("stale round: %d %s", w.Code, w.Body.String())
	}
	if len(sentToAgent(f)) != sent || listedAsk(t, s, todo.ID) == nil {
		t.Fatal("a stale round sent an answer or removed the card")
	}
	if w := reply(asked.Format(timeKeyLayout)); w.Code != http.StatusOK {
		t.Fatalf("chat answer: %d %s", w.Code, w.Body.String())
	}
	rows, err := database.ListTodoDecisions(context.Background(), todo.ID, false)
	if err != nil || len(rows) != 1 || rows[0].Source != db.DecisionManual ||
		rows[0].Question != rolloutDecision()["reason"] ||
		rows[0].Choice != "Use the alternative with a smaller limit." || rows[0].SourceMessage == "" {
		t.Fatalf("task decision: %+v, %v", rows, err)
	}
	if listedAsk(t, s, todo.ID) != nil || todoNow(s, todo.ID).Supervision.State == reviewNeedsUser {
		t.Fatal("the answered decision is still waiting for the owner")
	}
	if len(sentToAgent(f)) != sent+1 {
		t.Fatal("the answer was not delivered exactly once")
	}
	if w := reply(asked.Format(timeKeyLayout)); w.Code != http.StatusConflict || len(sentToAgent(f)) != sent+1 {
		t.Fatalf("a replay answered twice: %d", w.Code)
	}
}

func TestOrdinaryTaskChatDoesNotSettleADecision(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo := newSupervisedServiceOn(t, database)
	waitsForReview(t, s, f, todo.ID, "Choose a rollout plan.")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.routeToTask(context.Background(), "conv1", todo.ID, "Please explain the trade-offs first."); err != nil {
		t.Fatal(err)
	}
	rows, err := database.ListTodoDecisions(context.Background(), todo.ID, false)
	if err != nil || len(rows) != 0 || listedAsk(t, s, todo.ID) == nil {
		t.Fatalf("ordinary discussion was treated as a decision: %+v, %v", rows, err)
	}
}

func TestDecisionChatFailedDeliveryKeepsTheCard(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo := newSupervisedServiceOn(t, database)
	waitsForReview(t, s, f, todo.ID, "Choose a rollout plan.")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.failSend = errors.New("test delivery refused")
	f.mu.Unlock()
	_, err := s.routeDecisionAnswer(context.Background(), "conv1", todo.ID, askOf(s, todo.ID).AskedAt.Format(timeKeyLayout), "Use the alternative.")
	if err == nil {
		t.Fatal("failed delivery was reported as answered")
	}
	rows, err := database.ListTodoDecisions(context.Background(), todo.ID, false)
	if err != nil || len(rows) != 0 || listedAsk(t, s, todo.ID) == nil ||
		todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatalf("failed delivery settled the decision: %+v, %v", rows, err)
	}
}
