package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func dbTodoDoneEdit(done bool) db.TodoEdit {
	return db.TodoEdit{Done: &done}
}

func pendingParentCloseout(s *Service, parentID string) (superviseSignal, bool) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.pending[(superviseSignal{Todo: parentID, Kind: sigParentCloseout}).id()]
	return g, ok
}

func TestGoldenParentCloseoutWakeLanguages(t *testing.T) {
	children := []prompts.ParentCloseoutChild{
		{ID: "todo_child_1", Title: "Implement API", PRs: []string{"https://github.com/example/app/pull/101"}},
		{ID: "todo_child_2", Title: "Add UI tests", PRs: []string{"https://github.com/example/app/pull/102"}},
	}
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		container := prompts.RenderParentCloseoutWake(prompts.ParentCloseoutWake{
			Language: answerLanguage(locale),
			Personal: false,
			Children: children,
			Notes:    "Track API + UI rollout",
		})
		for _, want := range []string{"todo_child_1", "todo_child_2", "pull/101", "pull/102", "todo_complete"} {
			if !strings.Contains(container, want) {
				t.Fatalf("%s container wake missing %q:\n%s", locale, want, container)
			}
		}
		golden(t, "parent_closeout_wake.container."+locale, container)

		personal := prompts.RenderParentCloseoutWake(prompts.ParentCloseoutWake{
			Language: answerLanguage(locale),
			Personal: true,
			Children: children[:1],
		})
		if !strings.Contains(personal, "todo_child_1") || !strings.Contains(personal, "pull/101") {
			t.Fatalf("%s personal wake missing child details:\n%s", locale, personal)
		}
		golden(t, "parent_closeout_wake.personal."+locale, personal)
	}
}

func TestParentCloseoutHooksAndPartialCompletion(t *testing.T) {
	for _, hook := range []string{"todo_complete", "task_review_accepted", "auto_done"} {
		t.Run(hook, func(t *testing.T) {
			st := &memStore{}
			s, f := newTodoReadyService(t, st)
			s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			ctx := context.Background()

			parent := createRequestAndPiece(t, s, "conv1", "Ship auth refresh", "Parent container", "", nil)
			child1 := createRequestAndPiece(t, s, "conv1", "Backend token refresh", "", parent.ID, nil)
			child2 := createRequestAndPiece(t, s, "conv1", "Client retry wiring", "", parent.ID, nil)
			const pr1 = "https://github.com/example/app/pull/201"
			const pr2 = "https://github.com/example/app/pull/202"
			s.setTaskStage(ctx, child1.ID, "merge", pr1)
			s.setTaskStage(ctx, child2.ID, "merge", pr2)
			if err := s.applyPRStatus(ctx, todoNow(s, child1.ID), TodoPRStatus{URL: pr1, State: "merged", CheckedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := s.applyPRStatus(ctx, todoNow(s, child2.ID), TodoPRStatus{URL: pr2, State: "merged", CheckedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}

			complete := func(id string) {
				t.Helper()
				switch hook {
				case "todo_complete":
					if _, err := s.editTodo(ctx, id, dbTodoDoneEdit(true)); err != nil {
						t.Fatal(err)
					}
				case "task_review_accepted":
					if closed, err := s.reviewDoneTodo(ctx, id, "Verified", true); err != nil || !closed {
						t.Fatalf("reviewDoneTodo(%s): closed=%v err=%v", id, closed, err)
					}
				case "auto_done":
					if ok, err := s.autoDoneTodo(ctx, id, "Done", time.Now().Add(-time.Minute)); err != nil || !ok {
						t.Fatalf("autoDoneTodo(%s): ok=%v err=%v", id, ok, err)
					}
				}
			}

			// Completing only child 1 (partial completion) must NOT wake the parent.
			complete(child1.ID)
			if _, ok := pendingParentCloseout(s, parent.ID); ok {
				t.Fatal("partial completion woke the parent prematurely")
			}

			// Completing child 2 completes all part_of children -> queues exactly one parent_closeout wake.
			complete(child2.ID)
			g, ok := pendingParentCloseout(s, parent.ID)
			if !ok {
				t.Fatal("completing last part_of child did not queue sigParentCloseout")
			}
			if !g.urgent() || !supervisionBatchImmediate(g) {
				t.Fatalf("sigParentCloseout should be urgent and batch-immediate: %+v", g)
			}

			// Parent itself remains open (never silently auto-closed).
			if got := todoNow(s, parent.ID); got.DoneAt != nil {
				t.Fatal("parent to-do was silently auto-completed")
			}

			sendPendingPRConflicts(s)
			notices := sendsToConversation(f, "conv1")
			if len(notices) != 1 {
				t.Fatalf("got %d notices to main, want 1: %q", len(notices), notices)
			}
			for _, want := range []string{"signal=" + sigParentCloseout, parent.ID, child1.Title, child2.Title, pr1, pr2, "todo_complete"} {
				if !strings.Contains(notices[0], want) {
					t.Errorf("parent closeout notice missing %q:\n%s", want, notices[0])
				}
			}
		})
	}
}

func TestParentCloseoutSkipsActiveAgentAndCancelledOrFailedChildren(t *testing.T) {
	t.Run("parent with active agent is skipped", func(t *testing.T) {
		s, _ := newTodoReadyService(t, &memStore{})
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		ctx := context.Background()

		parent := createRequestAndPiece(t, s, "conv1", "Parent with active agent", "", "", nil)
		child := createRequestAndPiece(t, s, "conv1", "Subtask", "", parent.ID, nil)
		s.todos.mu.Lock()
		s.todos.todos[parent.ID].Agents = []TodoAgent{{MachineID: "box", SessionID: "sess-1", LinkedAt: time.Now()}}
		s.todos.mu.Unlock()

		if _, err := s.editTodo(ctx, child.ID, dbTodoDoneEdit(true)); err != nil {
			t.Fatal(err)
		}
		if _, ok := pendingParentCloseout(s, parent.ID); ok {
			t.Fatal("parent with an active Agent unexpectedly queued sigParentCloseout")
		}
	})

	t.Run("cancelled or failed-start child does not trigger parent closeout", func(t *testing.T) {
		for _, mode := range []string{"cancelled", "failed_start"} {
			t.Run(mode, func(t *testing.T) {
				s, _ := newTodoReadyService(t, &memStore{})
				s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
				ctx := context.Background()

				parent := createRequestAndPiece(t, s, "conv1", "Parent", "", "", nil)
				c1 := createRequestAndPiece(t, s, "conv1", "Done child", "", parent.ID, nil)
				c2 := createRequestAndPiece(t, s, "conv1", "Bad child", "", parent.ID, nil)

				if _, err := s.editTodo(ctx, c1.ID, dbTodoDoneEdit(true)); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "cancelled":
					if closed, err := s.reviewDoneTodo(ctx, c2.ID, "Abandoned", false); err != nil || !closed {
						t.Fatalf("cancel child: closed=%v err=%v", closed, err)
					}
				case "failed_start":
					now := time.Now().UTC()
					s.todos.mu.Lock()
					s.todos.todos[c2.ID].DoneAt = &now
					s.todos.todos[c2.ID].StartError = "machine offline"
					s.todos.mu.Unlock()
					s.sweepParentCloseout(ctx)
				}
				if _, ok := pendingParentCloseout(s, parent.ID); ok {
					t.Fatalf("%s child unexpectedly triggered sigParentCloseout", mode)
				}
			})
		}
	})

	t.Run("synthetic ask or outside-decision rows do not block or trigger parent closeout", func(t *testing.T) {
		s, _ := newTodoReadyService(t, &memStore{})
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		ctx := context.Background()

		parent := createRequestAndPiece(t, s, "conv1", "Parent with ask child", "", "", nil)
		child := createRequestAndPiece(t, s, "conv1", "Real child", "", parent.ID, nil)
		s.todos.mu.Lock()
		s.todos.todos[askTodoPrefix+"1"] = &Todo{ID: askTodoPrefix + "1", Title: "Pending question", PartOf: parent.ID}
		s.todos.todos["todo_od_1"] = &Todo{ID: "todo_od_1", Title: "Outside decision", PartOf: parent.ID}
		s.todos.mu.Unlock()

		if _, err := s.editTodo(ctx, child.ID, dbTodoDoneEdit(true)); err != nil {
			t.Fatal(err)
		}
		if _, ok := pendingParentCloseout(s, parent.ID); !ok {
			t.Fatal("synthetic ask/outside-decision rows blocked parent closeout wake")
		}
	})
}

func TestParentCloseoutRoutingAndPersonalTodoBoundary(t *testing.T) {
	t.Run("leader-owned parent wakes leader and never background supervisor", func(t *testing.T) {
		s, f := newTodoReadyService(t, &memStore{})
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		ctx := context.Background()
		f.setSessions("server",
			proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		)
		setSettings(t, s, func(v *Settings) {
			v.Leaders = []string{"lead1"}
			v.BackgroundSupervisor = "bg1"
			v.BackgroundSupervisorMachineID = "server"
		})

		parent := createRequestAndPiece(t, s, "lead1", "Leader milestone", "", "", nil)
		child := createRequestAndPiece(t, s, "lead1", "Leader child", "", parent.ID, nil)
		if _, err := s.editTodo(ctx, child.ID, dbTodoDoneEdit(true)); err != nil {
			t.Fatal(err)
		}
		sendPendingPRConflicts(s)
		if got := sendsToConversation(f, "lead1"); len(got) != 1 || !strings.Contains(got[0], "signal="+sigParentCloseout) {
			t.Fatalf("leader did not receive parent_closeout wake: %q", got)
		}
		if got := sendsToConversation(f, "bg1"); len(got) != 0 {
			t.Fatalf("background supervisor unexpectedly received parent_closeout wake: %q", got)
		}
	})

	t.Run("plain personal parent stays open and warns against silent auto-complete", func(t *testing.T) {
		s, f := newTodoReadyService(t, &memStore{})
		s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
		ctx := context.Background()

		// Plain personal to-do has empty Origin, empty PlanID, nil Supervision.
		parent := createRequestAndPiece(t, s, "", "Prepare release checklist for my review", "", "", nil)
		if !isPlainPersonalTodo(parent) {
			t.Fatalf("expected plain personal todo, got %+v", parent)
		}
		child := createRequestAndPiece(t, s, "conv1", "Draft release checklist", "", parent.ID, nil)
		if _, err := s.editTodo(ctx, child.ID, dbTodoDoneEdit(true)); err != nil {
			t.Fatal(err)
		}

		// Must NOT auto-close the plain personal parent.
		if got := todoNow(s, parent.ID); got.DoneAt != nil {
			t.Fatal("plain personal parent to-do was silently auto-completed")
		}

		sendPendingPRConflicts(s)
		notices := sendsToConversation(f, "conv1")
		if len(notices) != 1 {
			t.Fatalf("main did not receive personal parent closeout wake: %q", notices)
		}
		if !strings.Contains(notices[0], "用户个人普通待办") || !strings.Contains(notices[0], "切勿静默自动关闭") {
			t.Fatalf("personal parent closeout wake lost delegate.md boundary guidance:\n%s", notices[0])
		}
		if got := todoNow(s, parent.ID); got.DoneAt != nil {
			t.Fatal("plain personal parent was closed during wake delivery")
		}
	})
}

func TestParentCloseoutIdempotentAcrossSweepsAndRestart(t *testing.T) {
	st := &memStore{}
	s1, f1 := newTodoReadyService(t, st)
	s1.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()

	parent := createRequestAndPiece(t, s1, "conv1", "Container parent", "", "", nil)
	c1 := createRequestAndPiece(t, s1, "conv1", "Child 1", "", parent.ID, nil)
	if _, err := s1.editTodo(ctx, c1.ID, dbTodoDoneEdit(true)); err != nil {
		t.Fatal(err)
	}
	sendPendingPRConflicts(s1)
	if got := sendsToConversation(f1, "conv1"); len(got) != 1 || !strings.Contains(got[0], "signal="+sigParentCloseout) {
		t.Fatalf("expected 1 parent_closeout wake, got %q", got)
	}

	// Verify durable wake marker was persisted to the store.
	markers := map[string]parentCloseoutWakeMarker{}
	if found, err := st.Setting(ctx, parentCloseoutWakesKey, &markers); err != nil || !found || markers[parent.ID].Key == "" {
		t.Fatalf("expected persisted parent_closeout_wakes marker: found=%v err=%v markers=%+v", found, err, markers)
	}

	// Repeated sweep on the same service must not duplicate.
	s1.sweepParentCloseout(ctx)
	sendPendingPRConflicts(s1)
	if got := sendsToConversation(f1, "conv1"); len(got) != 1 {
		t.Fatalf("repeated sweep duplicated parent_closeout wake: %d", len(got))
	}

	// Restarted service sharing the same store and reloaded todos must not re-emit for the same child set.
	s2, f2 := newTodoReadyService(t, st)
	s2.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	parentCopy, c1Copy := todoNow(s1, parent.ID), todoNow(s1, c1.ID)
	s2.todos.mu.Lock()
	s2.todos.loaded = true
	s2.todos.todos = map[string]*Todo{
		parent.ID: &parentCopy,
		c1.ID:     &c1Copy,
	}
	s2.todos.mu.Unlock()
	s2.sweepParentCloseout(ctx)
	sendPendingPRConflicts(s2)
	if got := sendsToConversation(f2, "conv1"); len(got) != 0 {
		t.Fatalf("restarted service re-emitted duplicate parent_closeout wake: %q", got)
	}

	// Adding a new child c2 and completing it produces a new canonical child-set key and wakes once more.
	c2 := createRequestAndPiece(t, s2, "conv1", "Child 2", "", parent.ID, nil)
	if _, err := s2.editTodo(ctx, c2.ID, dbTodoDoneEdit(true)); err != nil {
		t.Fatal(err)
	}
	sendPendingPRConflicts(s2)
	if got := sendsToConversation(f2, "conv1"); len(got) != 1 || !strings.Contains(got[0], c2.Title) {
		t.Fatalf("new completed child set did not trigger fresh parent_closeout wake: %q", got)
	}

	// Closing and reopening parent resets the persisted marker so a subsequent sweep can wake if needed.
	if _, err := s2.editTodo(ctx, parent.ID, dbTodoDoneEdit(true)); err != nil {
		t.Fatal(err)
	}
	s2.pruneParentCloseoutWakes(ctx)
	markers = map[string]parentCloseoutWakeMarker{}
	if _, err := st.Setting(ctx, parentCloseoutWakesKey, &markers); err != nil {
		t.Fatal(err)
	}
	if _, ok := markers[parent.ID]; ok {
		t.Fatalf("closed parent marker was not pruned: %+v", markers)
	}
}
