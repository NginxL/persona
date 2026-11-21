package assistant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestUserCheckClosedTaskReopensAsOngoing(t *testing.T) {
	ctx := context.Background()
	for _, via := range []string{"verification feedback", "PATCH", "successor"} {
		t.Run(via, func(t *testing.T) {
			database := todoTestDB(t)
			s, f, todo := acceptedForUserCheckOn(t, database)
			a := askOf(s, todo.ID)
			done := true
			closed, err := s.editTodo(ctx, todo.ID, db.TodoEdit{Done: &done})
			if err != nil || closed.DoneAt == nil || closed.Supervision.State != reviewUserCheck {
				t.Fatalf("close pending verification: %+v, %v", closed, err)
			}
			switch via {
			case "verification feedback":
				if !s.acceptReply(ctx, "om_feedback", "Please fix the spacing.", false, a.MessageID) {
					t.Fatal("feedback was not recognized")
				}
			case "PATCH":
				rec := httptest.NewRecorder()
				s.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/v1/todos/"+todo.ID,
					strings.NewReader(`{"done":false}`)))
				if rec.Code != http.StatusOK {
					t.Fatalf("reopen PATCH: %d %s", rec.Code, rec.Body.String())
				}
			case "successor":
				s.successorOpens(ctx, closed, proto.Session{})
			}
			for _, service := range []*Service{s, newServiceOn(t, f, database)} {
				if err := service.ensureTodosLoaded(ctx); err != nil {
					t.Fatal(err)
				}
				got := todoNow(service, todo.ID)
				if got.DoneAt != nil || got.ReopenedAt == nil || !got.ReopenedAt.After(a.AskedAt) ||
					got.Supervision.State != "" || triageReviewState(got) != "" {
					t.Fatalf("reopened task still waits on stale personal verification: %+v", got)
				}
				if _, _, userCheck := service.openTaskOf([2]string{"box", "s1"}); userCheck {
					t.Fatal("reopened work still has the personal-verification idle exemption")
				}
				if result, err := service.acceptTry(ctx, todo.ID, a.MessageID); err != nil || result.taken {
					t.Fatalf("stale Pass accepted reopened work: %+v, %v", result, err)
				}
			}
		})
	}
}

func TestUserCheckAcceptsAClosedTaskInANewRound(t *testing.T) {
	ctx := context.Background()
	s, _, todo := acceptedForUserCheck(t)
	old := askOf(s, todo.ID)
	done := true
	if _, err := s.editTodo(ctx, todo.ID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatal(err)
	}
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict": "accepted", "user_check": true, "checks": completeReviewChecks(todoNow(s, todo.ID)),
	})
	got := todoNow(s, todo.ID)
	a := askOf(s, todo.ID)
	if got.DoneAt != nil || got.Supervision.State != reviewUserCheck || a == nil ||
		!a.AskedAt.After(old.AskedAt) || got.ReopenedAt == nil || a.AskedAt.Before(*got.ReopenedAt) {
		t.Fatalf("a fresh review lost its new personal-verification round: todo=%+v ask=%+v", got, a)
	}
	if result, err := s.acceptTry(ctx, todo.ID, a.MessageID); err != nil || !result.taken {
		t.Fatalf("the fresh owner Pass did not complete: %+v, %v", result, err)
	}
}

func TestUserCheckStartsNewRoundAfterTerminalCleanupWasSkipped(t *testing.T) {
	s, f, todo := dispatchTerminalTask(t, true, true)
	f.mu.Lock()
	f.sessions["box"][0].Status = proto.StatusWorking
	f.mu.Unlock()
	result := reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted"})
	closed := todoNow(s, todo.ID)
	if closed.DoneAt == nil || closed.DoneBy != db.DoneByReview ||
		!strings.Contains(result, "Terminal cleanup: skipped") {
		t.Fatalf("the prior review did not complete while keeping the terminal: %q, %+v", result, closed)
	}

	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "user_check": true})
	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.Supervision.state(got) != reviewUserCheck || askOf(s, todo.ID) == nil {
		t.Fatalf("the fresh owner check did not reopen the task: %+v", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method == devicewire.MethodCloseTaskTerminal {
			t.Fatal("starting a fresh owner check attempted terminal cleanup")
		}
	}
}

func TestUserCheckOrdinaryOpenEditKeepsVerification(t *testing.T) {
	ctx := context.Background()
	database := todoTestDB(t)
	s, f, todo := acceptedForUserCheckOn(t, database)
	a := askOf(s, todo.ID)
	open, title := false, "Updated task title"
	if _, err := s.editTodo(ctx, todo.ID, db.TodoEdit{Done: &open, Title: &title}); err != nil {
		t.Fatal(err)
	}
	restarted := newServiceOn(t, f, database)
	if err := restarted.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	got := todoNow(restarted, todo.ID)
	if got.Supervision.State != reviewUserCheck || got.ReopenedAt != nil {
		t.Fatalf("an ordinary edit ended pending personal verification: %+v", got)
	}
	if result, err := restarted.acceptTry(ctx, todo.ID, a.MessageID); err != nil || !result.taken {
		t.Fatalf("an ordinary edit invalidated the owner's Pass: %+v, %v", result, err)
	}
}

func TestUserCheckFeedbackDoesNotHoldActionLockWhileSending(t *testing.T) {
	ctx := context.Background()
	s, h, todo := stoppedTaskOnGate(t)
	// Old writable offline rows resume on send; genuinely stopped Agents remain read-only.
	h.setSessions("box", proto.Session{MachineID: "box", SessionID: "sec00001", Status: proto.StatusOffline,
		Capabilities: &proto.SessionCapabilities{Read: true, Send: true, Terminal: true}})
	arrived, gate := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}()
	h.fakeHub.mu.Lock()
	h.beforeSend = func() { close(arrived); <-gate }
	h.fakeHub.mu.Unlock()
	at := time.Now().UTC()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewUserCheck
		v.Reviews = []TaskReview{{At: at, Verdict: reviewAccepted, UserCheck: true}}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	a := &acceptAsk{AskedAt: at, MessageID: "om_verify", Messages: []string{"om_verify"}}
	s.accept.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		s.accept.mu.Unlock()
		t.Fatal("acceptance book could not be loaded")
	}
	s.accept.asks[todo.ID] = a
	s.accept.mu.Unlock()
	replied := make(chan bool, 1)
	go func() {
		replied <- s.acceptReply(ctx, "om_feedback", "Please fix the spacing.", false, a.MessageID)
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("feedback never reached the delivery gate")
	}
	type passReply struct {
		result acceptResult
		err    error
	}
	passed := make(chan passReply, 1)
	go func() {
		result, err := s.acceptTry(ctx, todo.ID, a.MessageID)
		passed <- passReply{result, err}
	}()
	passReturned := false
	select {
	case reply := <-passed:
		passReturned = true
		if reply.err != nil || reply.result.taken {
			t.Errorf("the old Pass was not refused after feedback: %+v, %v", reply.result, reply.err)
		}
	case <-time.After(time.Second):
		t.Error("feedback kept Pass waiting for Agent delivery")
	}
	// A review can post a newer ask while delivery waits; the old reply must not consume that ask.
	fresh := &acceptAsk{AskedAt: at.Add(time.Second), MessageID: "om_fresh", Messages: []string{"om_fresh"}}
	s.accept.mu.Lock()
	s.accept.asks[todo.ID] = fresh
	s.accept.mu.Unlock()
	close(gate)
	select {
	case recognized := <-replied:
		if !recognized {
			t.Fatal("feedback was not recognized")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("feedback did not finish after delivery")
	}
	if !passReturned {
		select {
		case <-passed:
		case <-time.After(5 * time.Second):
			t.Fatal("Pass remained blocked after delivery")
		}
	}
	if a := askOf(s, todo.ID); a == nil || !a.AskedAt.Equal(fresh.AskedAt) {
		t.Fatalf("an old feedback delivery consumed a newer verification round: %+v", a)
	}
}

type userCheckReviewFailure struct {
	*db.DB
	fail      atomic.Bool
	failRound atomic.Bool
}

func (d *userCheckReviewFailure) EditTodo(ctx context.Context, id string, edit db.TodoEdit, at time.Time) (*db.TodoRow, error) {
	if edit.RestartWork && d.failRound.Load() {
		return nil, errors.New("work round store unavailable")
	}
	return d.DB.EditTodo(ctx, id, edit, at)
}

func (d *userCheckReviewFailure) PutTodoSupervision(ctx context.Context, row db.TodoSupervisionRow) error {
	if d.fail.Load() {
		return errors.New("review store unavailable")
	}
	return d.DB.PutTodoSupervision(ctx, row)
}

func TestUserCheckSavedReworkInvalidatesPassIfRoundWriteFails(t *testing.T) {
	ctx := context.Background()
	store := &userCheckReviewFailure{DB: todoTestDB(t)}
	s, f, todo := acceptedForUserCheckOn(t, store)
	a := askOf(s, todo.ID)
	store.failRound.Store(true)
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": "rework", "note": "Please fix the spacing."}); err == nil || !strings.Contains(err.Error(), "work round store unavailable") {
		t.Fatalf("work-round failure was not reported: %v", err)
	}
	for _, service := range []*Service{s, newServiceOn(t, f, store)} {
		if err := service.ensureTodosLoaded(ctx); err != nil {
			t.Fatal(err)
		}
		got := todoNow(service, todo.ID)
		if got.Supervision.State != reviewRework || got.ReopenedAt != nil {
			t.Fatalf("saved rework was not left for recovery: %+v", got)
		}
		if result, err := service.acceptTry(ctx, todo.ID, a.MessageID); err != nil || result.taken {
			t.Fatalf("a superseded Pass verified rework after its round write failed: %+v, %v", result, err)
		}
	}
}

func TestUserCheckRejectedReviewPreservesVerification(t *testing.T) {
	ctx := context.Background()
	for _, reason := range []string{"leader parking", "ledger write"} {
		t.Run(reason, func(t *testing.T) {
			store := &userCheckReviewFailure{DB: todoTestDB(t)}
			s, f, todo := acceptedForUserCheckOn(t, store)
			a := askOf(s, todo.ID)
			args := map[string]any{"verdict": "rework", "note": "Please fix the spacing."}
			if reason == "leader parking" {
				s.todos.mu.Lock()
				s.todos.todos[todo.ID].Agents = append(s.todos.todos[todo.ID].Agents,
					TodoAgent{MachineID: "box", SessionID: "leader", Source: sourceLeader})
				s.todos.mu.Unlock()
				if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
					v.Supervisor = leaderRef("box", "leader")
					return true
				}); err != nil {
					t.Fatal(err)
				}
				args = waitingPark()
			}
			before := todoNow(s, todo.ID)
			store.fail.Store(reason == "ledger write")
			_, err := reviewErr(s, todo.ID, args)
			store.fail.Store(false)
			if err == nil {
				t.Fatal("the invalid review was accepted")
			}
			if reason == "leader parking" && !strings.Contains(err.Error(), "task leader agent") ||
				reason == "ledger write" && !strings.Contains(err.Error(), "review store unavailable") {
				t.Fatalf("review failed for the wrong reason: %v", err)
			}
			for _, service := range []*Service{s, newServiceOn(t, f, store)} {
				if err := service.ensureTodosLoaded(ctx); err != nil {
					t.Fatal(err)
				}
				got := todoNow(service, todo.ID)
				if !reflect.DeepEqual(got.ReopenedAt, before.ReopenedAt) ||
					got.Supervision.State != before.Supervision.State || got.Supervision.Round != before.Supervision.Round ||
					!reflect.DeepEqual(got.Supervision.Reviews, before.Supervision.Reviews) || got.Stage != before.Stage {
					t.Fatalf("a rejected review invalidated personal verification: before=%+v after=%+v", before, got)
				}
			}
			if result, err := s.acceptTry(ctx, todo.ID, a.MessageID); err != nil || !result.taken {
				t.Fatalf("rejected review destroyed the owner's Pass: %+v, %v", result, err)
			}
		})
	}
}
