package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const reviewArtifactURL = "https://github.com/example/project/pull/42"

// i18n-ignore: anonymized shape of a pre-rollout stopped turn; its waiting field has no URL.
const reviewArtifactCloseout = `<closeout status="blocked" merged="no" deployed="yes">
did: Added one-click translation and verified the local change.
artifact: pr https://github.com/example/project/pull/42
artifact: commit 0123456789abcdef
verified: Local tests pass; PR is MERGEABLE.
missing: Maintainer re-review and upstream merge.
waiting: maintainer-a/上游维护者复审
</closeout>`

func backfillInventory(agents ...proto.Session) machineSessions {
	lists := machineSessions{}
	for _, c := range agents {
		l := lists[c.MachineID]
		l.list = append(l.list, c)
		lists[c.MachineID] = l
	}
	return lists
}

func reviewArtifactFixture(t *testing.T) (*Service, *fakeHub, Todo, proto.Session, time.Time) {
	t.Helper()
	s, f, task, c, now := automaticFixture(t, reviewArtifactCloseout)
	s.born = now
	c.Rev = 7
	f.setSessions(c.MachineID, c)
	s.todos.mu.Lock()
	task = copyTodoLocked(s.todos.todos[task.ID])
	task.PRURL = reviewArtifactURL
	task.PRStatus = &TodoPRStatus{URL: task.PRURL, State: "open", HeadOID: "head-1", Review: "pending", CheckedAt: now}
	*s.todos.todos[task.ID] = copyTodoLocked(&task)
	s.todos.mu.Unlock()
	recordToldCloseout(t, s, task, c, now)
	return s, f, task, c, now
}

func recordToldCloseout(t *testing.T, s *Service, task Todo, c proto.Session, now time.Time) {
	t.Helper()
	s.turnOf(context.Background(), "")
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	told := now.Add(-time.Minute)
	b.doc.Turns[task.ID] = taskTurn{Machine: c.MachineID, Session: c.SessionID, Rev: c.Rev,
		State: turnDone, Receipt: true, Judged: &turnJudged{At: c.UpdatedAt},
		Signal: &turnSignal{Kind: sigIdle, Key: "old-stop", Told: &told}}
	s.saveTurnsLocked(context.Background())
}

func TestAutomaticCloseoutReviewArtifactBinding(t *testing.T) {
	for _, tc := range []struct {
		name, reply, source string
		want                bool
	}{
		{"maintainer re-review", reviewArtifactCloseout, reviewArtifactURL, true},
		// i18n-ignore: literal reviewer/merge variants of the incident closeout.
		{"reviewer re-review", strings.ReplaceAll(reviewArtifactCloseout, "maintainer-a/上游维护者复审", "等评审人复审"), reviewArtifactURL, true},
		{"upstream merge", strings.ReplaceAll(reviewArtifactCloseout, "maintainer-a/上游维护者复审", "等上游合并"), reviewArtifactURL, true},
		{"MR artifact", strings.ReplaceAll(reviewArtifactCloseout, reviewArtifactURL, "https://gitlab.com/example/project/-/merge_requests/42"), "https://gitlab.com/example/project/-/merge_requests/42", true},
		{"no external link", strings.ReplaceAll(reviewArtifactCloseout, "artifact: pr "+reviewArtifactURL+"\n", ""), reviewArtifactURL, false},
		{"link only outside closeout", "PR " + reviewArtifactURL + "\n" + strings.ReplaceAll(reviewArtifactCloseout, "artifact: pr "+reviewArtifactURL+"\n", ""), reviewArtifactURL, false},
		{"link only in earlier closeout", reviewArtifactCloseout + "\n" + automaticCloseout("waiting for maintainer re-review", "", nil), reviewArtifactURL, false},
		{"unbound source", strings.ReplaceAll(reviewArtifactCloseout, reviewArtifactURL, "https://github.com/example/project/pull/43"), reviewArtifactURL, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, task, c, now := reviewArtifactFixture(t)
			c.LastText = tc.reply
			f.setSessions(c.MachineID, c)
			s.todos.mu.Lock()
			s.todos.todos[task.ID].PRURL = tc.source
			s.todos.todos[task.ID].PRStatus.URL = tc.source
			s.todos.mu.Unlock()
			kept := s.screenMechanicalSignals(context.Background(), []superviseSignal{{Todo: task.ID, Kind: sigIdle}}, now, now.Add(-time.Hour))
			cur := todoNow(s, task.ID)
			if parked := cur.Supervision.State == reviewParked; parked != tc.want || (len(kept) == 0) != tc.want {
				t.Fatalf("parked=%v idle=%d want=%v", parked, len(kept), tc.want)
			}
			if tc.want && cur.Supervision.parking().Evidence.URL != tc.source {
				t.Fatalf("wrong bound artifact: %+v", cur.Supervision.parking())
			}
			if cur.DoneAt != nil {
				t.Fatal("an external wait was accepted as complete")
			}
		})
	}
}

func TestAutomaticCloseoutPassivePRRequiresExternalActor(t *testing.T) {
	// i18n-ignore: literal Agent closeouts, including first-person and named-reviewer variants.
	for _, tc := range []struct {
		name, waiting string
		want          bool
	}{
		{"pending own final review", "pending my final review before merging", false},
		{"waiting for own review", "waiting for my final review before merging", false},
		{"pending without actor", "pending final review before merging", false},
		{"waiting without actor", "waiting for final review before merging", false},
		{"merge without actor", "await merge", false},
		{"own upstream review", "pending my final upstream review before merging", false},
		{"review by me", "pending upstream review by me before merging", false},
		{"internal work before review", "waiting for maintainer review after I finish tests", false},
		{"own review before upstream merge", "等我复审后上游合并", false},
		{"own Japanese review", "メンテナーのレビュー待ち；自分で最終レビュー", false},
		{"named reviewer", "pending reviewer-a final review", true},
		{"my reviewer is external", "waiting for my reviewer-a final review", true},
		{"maintainer merge", "pending maintainer-a merge", true},
		{"Chinese reviewer", "等评审人复审", true},
		{"Japanese maintainer", "メンテナーのレビュー待ち", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, task, c, now := reviewArtifactFixture(t)
			c.LastText = strings.Replace(reviewArtifactCloseout, "maintainer-a/上游维护者复审", tc.waiting, 1)
			f.setSessions(c.MachineID, c)
			kept := s.screenMechanicalSignals(context.Background(), []superviseSignal{{Todo: task.ID, Kind: sigIdle}}, now, now.Add(-time.Hour))
			if parked := todoNow(s, task.ID).Supervision.State == reviewParked; parked != tc.want || (len(kept) == 0) != tc.want {
				t.Fatalf("external actor admission parked=%v idle=%d want=%v", parked, len(kept), tc.want)
			}
		})
	}
}

func TestAutomaticCloseoutStartupBackfill(t *testing.T) {
	s, _, task, c, now := reviewArtifactFixture(t)
	ctx := context.Background()
	if !s.tasks.turns.doc.Turns[task.ID].settled() {
		t.Fatal("fixture needs an already-told stop, not a new completion edge")
	}
	s.superviseWatch(ctx, now)
	cur := todoNow(s, task.ID)
	if cur.Supervision.State != reviewParked || cur.Supervision.parking().Evidence.URL != reviewArtifactURL {
		t.Fatalf("startup ignored the already-told external wait: %+v", cur.Supervision)
	}
	if a := automaticMetadata(t, cur.Supervision); a["count"] != float64(1) {
		t.Fatalf("first backfill spent more than one attempt: %+v", a)
	}
	s.supervise.mu.Lock()
	for _, g := range s.supervise.pending {
		if g.Kind == sigIdle || g.Kind == sigAllStopped {
			t.Errorf("backfilled task still queued %s", g.Kind)
		}
	}
	s.supervise.mu.Unlock()
	for range 2 {
		s.superviseWatch(ctx, now)
	}
	// Reconstruct the persisted ledger and forget startup memory, as after a manager restart.
	v := supervisionFromRow(todoNow(s, task.ID).Supervision.row(task.ID))
	s.todos.mu.Lock()
	s.todos.todos[task.ID].Supervision = v
	s.todos.mu.Unlock()
	s.supervise.mu.Lock()
	s.supervise.closeoutScanned = nil
	s.supervise.mu.Unlock()
	s.superviseWatch(ctx, now)
	cur = todoNow(s, task.ID)
	if len(cur.Supervision.Reviews) != 1 || automaticMetadata(t, cur.Supervision)["count"] != float64(1) {
		t.Fatalf("sweeps/reload repeated admission: %+v", cur.Supervision)
	}
	// After an event/deadline wake, a restart must not silently park that same stopped turn again.
	s.todos.mu.Lock()
	s.todos.todos[task.ID].Supervision.State = ""
	s.todos.mu.Unlock()
	s.supervise.mu.Lock()
	s.supervise.closeoutScanned = nil
	s.supervise.mu.Unlock()
	cur, _ = s.backfillCloseout(ctx, todoNow(s, task.ID), backfillInventory(c), now)
	if cur.Supervision.State == reviewParked || len(cur.Supervision.Reviews) != 1 ||
		automaticMetadata(t, cur.Supervision)["count"] != float64(1) {
		t.Fatalf("same-turn startup hid an existing wake or spent another attempt: %+v", cur.Supervision)
	}
}

func TestAutomaticCloseoutStartupBackfillReadinessAndGuards(t *testing.T) {
	for _, guard := range []string{"missing inventory", "stale PR", "missing PR", "working", "queued", "interaction", "owner wait", "scheduled wake", "post-start turn", "no external link"} {
		t.Run(guard, func(t *testing.T) {
			s, f, task, c, now := reviewArtifactFixture(t)
			inventory := []proto.Session{c}
			switch guard {
			case "missing inventory":
				inventory = nil
			case "stale PR":
				task.PRStatus.CheckedAt = now.Add(-triagePRStatusFreshFor - time.Second)
			case "missing PR":
				task.PRStatus = nil
			case "working":
				c.State = proto.StateWorking
			case "queued":
				c.Queued = []string{"continue"}
			case "interaction":
				c.PendingInteractions = 1
			case "owner wait":
				s.putWork(context.Background(), db.SessionWork{MachineID: c.MachineID, SessionID: c.SessionID, TurnAt: c.UpdatedAt,
					Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser}})
			case "scheduled wake":
				c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}
			case "post-start turn":
				c.UpdatedAt = s.born.Add(time.Minute)
			case "no external link":
				c.LastText = strings.ReplaceAll(c.LastText, "artifact: pr "+reviewArtifactURL+"\n", "")
			}
			f.setSessions(c.MachineID, c)
			if len(inventory) > 0 {
				inventory[0] = c
			}
			s.todos.mu.Lock()
			*s.todos.todos[task.ID] = copyTodoLocked(&task)
			s.todos.mu.Unlock()
			lists := backfillInventory(inventory...)
			if guard == "missing inventory" {
				lists[c.MachineID] = machineList{err: errors.New("node inventory unavailable")}
			}
			cur, _ := s.backfillCloseout(context.Background(), task, lists, now)
			if cur.Supervision.State == reviewParked {
				t.Fatalf("%s was hidden by startup parking", guard)
			}
			if guard == "missing inventory" || guard == "stale PR" || guard == "missing PR" {
				s.todos.mu.Lock()
				s.todos.todos[task.ID].PRStatus = &TodoPRStatus{URL: reviewArtifactURL, State: "open", HeadOID: "head-1", CheckedAt: now}
				s.todos.mu.Unlock()
				cur, _ = s.backfillCloseout(context.Background(), todoNow(s, task.ID), backfillInventory(c), now)
				if cur.Supervision.State != reviewParked {
					t.Fatalf("early %s consumed the one startup inspection", guard)
				}
			}
		})
	}
}

func TestAutomaticCloseoutStartupBackfillConsumesOldIdle(t *testing.T) {
	s, _, task, _, now := reviewArtifactFixture(t)
	ctx := context.Background()
	b := &s.tasks.turns
	b.mu.Lock()
	e := b.doc.Turns[task.ID]
	signal := *e.Signal
	signal.Told = nil
	e.Signal = &signal
	b.doc.Turns[task.ID] = e
	s.saveTurnsLocked(ctx)
	b.mu.Unlock()
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.pending[task.ID+"|idle"] = superviseSignal{Todo: task.ID, Kind: sigIdle, Key: signal.Key}
	s.supervise.pending[task.ID+"|report"] = superviseSignal{Todo: task.ID, Kind: "report", Key: "new-report"}
	s.supervise.mu.Unlock()
	s.superviseWatch(ctx, now)
	if todoNow(s, task.ID).Supervision.State == reviewParked {
		t.Fatal("startup hid an independently actionable report")
	}
	s.supervise.mu.Lock()
	delete(s.supervise.pending, task.ID+"|report")
	s.supervise.mu.Unlock()
	s.superviseWatch(ctx, now)
	cur := todoNow(s, task.ID)
	if cur.Supervision.State != reviewParked {
		t.Fatal("old untold idle prevented startup parking")
	}
	stopped, _ := s.turnOf(ctx, task.ID)
	if stopped.Signal == nil || stopped.Signal.Told == nil {
		t.Fatal("startup parking left the durable idle receipt retryable")
	}
	s.supervise.mu.Lock()
	defer s.supervise.mu.Unlock()
	for _, g := range s.supervise.pending {
		if g.Todo == task.ID && (g.Kind == sigIdle || g.Kind == sigAllStopped) {
			t.Fatalf("startup parking left an old %s queued", g.Kind)
		}
	}
}

func TestAutomaticCloseoutStartupBackfillChecksOnlyItsSource(t *testing.T) {
	for _, deployment := range []bool{false, true} {
		t.Run(fmt.Sprintf("deployment=%v", deployment), func(t *testing.T) {
			s, f, task, c, now := reviewArtifactFixture(t)
			source := reviewArtifactURL
			if deployment {
				source = "https://example.org/deployments/rollout-42"
				c.LastText = automaticCloseout("deployment rollout-42", "build "+source, nil)
				f.setSessions(c.MachineID, c)
			}
			s.todos.mu.Lock()
			cur := s.todos.todos[task.ID]
			cur.PRs = []string{"https://github.com/example/project/pull/41"}
			if deployment {
				cur.PRStatus = nil
			}
			s.todos.mu.Unlock()
			current, _ := s.backfillCloseout(context.Background(), todoNow(s, task.ID), backfillInventory(c), now)
			if current.Supervision.State != reviewParked || current.Supervision.parking().Evidence.URL != source {
				t.Fatalf("unrelated missing PR facts blocked the evidenced wait: %+v", current.Supervision)
			}
		})
	}
}

func TestAutomaticCloseoutStartupBackfillInspectsOnce(t *testing.T) {
	s, f, task, c, now := reviewArtifactFixture(t)
	c.LastText = "No structured closeout."
	f.setSessions(c.MachineID, c)
	var reads atomic.Int32
	f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
		reads.Add(1)
		return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: c.LastText}}}, nil
	})
	for range 3 {
		s.backfillCloseout(context.Background(), task, backfillInventory(c), now)
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("startup read an ineligible stopped turn %d times, want 1", n)
	}
}

func TestAutomaticCloseoutStartupBackfillWriteFailureAndRecovery(t *testing.T) {
	database := &ledgerDown{DB: taskRecoveryDatabase(t)}
	s, f, task := newSupervisedServiceOn(t, database)
	holdDigest(s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	s.born = now
	a := task.Agents[0]
	c := stopped(a.SessionID, reviewArtifactCloseout)
	c.MachineID, c.UpdatedAt = a.MachineID, now.Add(-time.Hour)
	f.setSessions(a.MachineID, c)
	s.todos.mu.Lock()
	cur := s.todos.todos[task.ID]
	cur.PRURL = reviewArtifactURL
	cur.PRStatus = &TodoPRStatus{URL: reviewArtifactURL, State: "open", HeadOID: "head-1", CheckedAt: now}
	s.todos.mu.Unlock()
	before, _ := json.Marshal(todoNow(s, task.ID).Supervision)
	database.down.Store(true)
	s.backfillCloseout(ctx, todoNow(s, task.ID), backfillInventory(c), now)
	after, _ := json.Marshal(todoNow(s, task.ID).Supervision)
	if string(before) != string(after) {
		t.Fatal("failed startup write leaked parking or consumed an attempt")
	}
	database.down.Store(false)
	current, _ := s.backfillCloseout(ctx, todoNow(s, task.ID), backfillInventory(c), now)
	if current.Supervision.State != reviewParked || automaticMetadata(t, current.Supervision)["count"] != float64(1) {
		t.Fatalf("failed startup write consumed its inspection: %+v", current.Supervision)
	}
	again, _ := restartedOn(t, database)
	holdDigest(again)
	if err := again.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	reloaded := todoNow(again, task.ID)
	if reloaded.Supervision.State != reviewParked || len(reloaded.Supervision.Reviews) != 1 ||
		automaticMetadata(t, reloaded.Supervision)["count"] != float64(1) {
		t.Fatalf("startup admission was not durable: %+v", reloaded.Supervision)
	}
}

func TestAutomaticCloseoutStartupBackfillBudget(t *testing.T) {
	s, f, task, c, now := reviewArtifactFixture(t)
	sessions := []proto.Session{c}
	ids := []string{task.ID}
	for i := 1; i <= closeoutBackfillMost; i++ {
		clone, agent := copyTodoLocked(&task), c
		clone.ID, agent.SessionID = fmt.Sprintf("todo-backfill-%d", i), fmt.Sprintf("backfill-agent-%d", i)
		clone.Agents[0].SessionID = agent.SessionID
		s.todos.mu.Lock()
		s.todos.todos[clone.ID] = &clone
		s.todos.mu.Unlock()
		recordToldCloseout(t, s, clone, agent, now)
		sessions, ids = append(sessions, agent), append(ids, clone.ID)
	}
	f.setSessions(c.MachineID, sessions...)
	for pass, want := range []int{closeoutBackfillMost, len(ids)} {
		s.superviseWatch(context.Background(), now)
		parked := 0
		for _, id := range ids {
			if todoNow(s, id).Supervision.State == reviewParked {
				parked++
			}
		}
		if parked != want {
			t.Fatalf("pass %d parked=%d want=%d", pass+1, parked, want)
		}
	}
}
