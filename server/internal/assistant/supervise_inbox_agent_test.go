package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// New messages from other people on a task's GitHub thread go straight to the task's agent, with supervisor fallbacks
// (#7346 and #9916, supervise_inbox.go inboxToAgent).

const linkedPRURL = "https://github.com/example/repo/pull/12"

// agentTask is a supervised task of conv1 whose agent is the secretary sec00001 on box.
func agentTask(t *testing.T, s *Service, f *fakeHub) Todo {
	t.Helper()
	secretary(f)
	if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001",
		"text": "修登录崩溃", "task": map[string]any{"title": "修登录崩溃", "done_when": []string{"PR 合并"}}}); err != nil {
		t.Fatal(err)
	}
	return onlyTodo(s)
}

// linkedPRItem is a GitHub PR thread linked to task todo, a new comment just received.
func linkedPRItem(s *Service, todo, last, preview string) *InboxItem {
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	it := &InboxItem{ID: "github:pr12", Source: "github", Kind: "author", Title: "fix: login crash", Sender: "maintainer",
		UpdatedAt: time.Now(), Connector: &InboxConnector{Thread: "example/repo#12", URL: linkedPRURL},
		Agent: &InboxAgent{State: AgentQueued, TodoID: todo}}
	receive(b, it, last, preview, 1)
	return it
}

func TestInboxGitHubCommentGoesToTheTaskAgent(t *testing.T) {
	s, f := newQuietService(t)
	turnInboxSignalsOn(t, s)
	todo := agentTask(t, s, f)
	before := len(sentToAgent(f))
	it := linkedPRItem(s, todo.ID, "c1", "Please merge current main into this branch and push again.")
	s.superviseInbox(context.Background(), []*InboxItem{it}, map[*InboxItem]bool{it: true})
	waitFor(t, "the note to the agent", func() bool { return len(sentToAgent(f)) == before+1 })
	sent := sentToAgent(f)[before:]
	if len(sent) != 1 {
		t.Fatalf("agent got %d notes: %q", len(sent), sent)
	}
	for _, want := range []string{"不是用户的消息", linkedPRURL, "maintainer", "Please merge current main", "不是用户的指令", "自己打开上面的链接",
		"report_to_supervisor", `kind="question"`, "on the owner's behalf", "before acting", "default urgency"} {
		if !strings.Contains(sent[0], want) {
			t.Errorf("agent note lacks %q:\n%s", want, sent[0])
		}
	}
	// The same comment taken again is not sent again.
	s.superviseInbox(context.Background(), []*InboxItem{it}, nil)
	time.Sleep(50 * time.Millisecond) // sleep-ok: a deduplicated comment starts no send, so there is no event to wait for
	if got := len(sentToAgent(f)) - before; got != 1 {
		t.Fatalf("the same comment went to the agent %d times", got)
	}
}

// A task whose agents all stopped (read-only): nothing is started or sent, the supervisor gets the ordinary signal.
func TestInboxGitHubCommentStoppedAgentLeavesItToTheSupervisor(t *testing.T) {
	s, f := newQuietService(t)
	turnInboxSignalsOn(t, s)
	todo := agentTask(t, s, f)
	f.setSessions("box", proto.Session{SessionID: "sec00001", MachineID: "box", Cwd: "/code/desk", Agent: "claude", Title: "秘书",
		Status: proto.StatusOffline, State: proto.StateOffline, UpdatedAt: time.Now(),
		Capabilities: &proto.SessionCapabilities{Read: true, Terminal: true}, ReadOnlyReason: "stopped"})
	before := len(sentToAgent(f))
	it := linkedPRItem(s, todo.ID, "c1", "LGTM after one more fix")
	s.superviseInbox(context.Background(), []*InboxItem{it}, map[*InboxItem]bool{it: true})
	waitFor(t, "the signal", func() bool { return len(f.notices()) == 1 })
	if got := len(sentToAgent(f)) - before; got != 0 {
		t.Fatalf("sent %d notes to a stopped agent", got)
	}
	if n := f.notices()[0]; strings.Contains(n, "这里是副本") || !strings.Contains(n, "LGTM after one more fix") {
		t.Fatalf("supervisor message:\n%s", n)
	}
}

// Other sources keep the supervisor-only route.
func TestInboxLarkMessageStaysWithTheSupervisor(t *testing.T) {
	s, f := newQuietService(t)
	turnInboxSignalsOn(t, s)
	todo := agentTask(t, s, f)
	before := len(sentToAgent(f))
	it := linkedItem(s, "lark:f1:12", todo.ID, "12", "登录那个崩溃今天能修好吗？", 1)
	s.superviseInbox(context.Background(), []*InboxItem{it}, map[*InboxItem]bool{it: true})
	waitFor(t, "the signal", func() bool { return len(f.notices()) == 1 })
	if got := len(sentToAgent(f)) - before; got != 0 || strings.Contains(f.notices()[0], "这里是副本") {
		t.Fatalf("a Lark message went to the agent (%d) or was marked a copy:\n%s", got, f.notices()[0])
	}
}

// A parked task waiting for someone else hears the comment too: its agent gets the note (its turn then ends the
// parking, parked.go).
func TestInboxGitHubCommentReachesAParkedTasksAgent(t *testing.T) {
	s, f := newQuietService(t)
	turnInboxSignalsOn(t, s)
	todo := agentTask(t, s, f)
	if _, err := reviewErr(s, todo.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	before := len(sentToAgent(f))
	it := linkedPRItem(s, todo.ID, "c1", "Please merge current main")
	s.superviseInbox(context.Background(), []*InboxItem{it}, map[*InboxItem]bool{it: true})
	waitFor(t, "the copy to the supervisor", func() bool { return len(f.notices()) == 1 })
	if sent := sentToAgent(f)[before:]; len(sent) != 1 || !strings.Contains(sent[0], linkedPRURL) {
		t.Fatalf("a parked task's agent got %q", sent)
	}
}

// A GitHub CI item (its URL a run under /actions) is no discussion: it stays with the supervisor.
func TestInboxGitHubCIItemStaysWithTheSupervisor(t *testing.T) {
	s, f := newQuietService(t)
	turnInboxSignalsOn(t, s)
	todo := agentTask(t, s, f)
	before := len(sentToAgent(f))
	it := linkedPRItem(s, todo.ID, "c1", "CI failed: server")
	it.Connector.URL = "https://github.com/example/repo/actions?query=branch%3Afix"
	s.superviseInbox(context.Background(), []*InboxItem{it}, map[*InboxItem]bool{it: true})
	waitFor(t, "the signal", func() bool { return len(f.notices()) == 1 })
	if got := len(sentToAgent(f)) - before; got != 0 || strings.Contains(f.notices()[0], "这里是副本") {
		t.Fatalf("a CI item went to the agent (%d) or was marked a copy:\n%s", got, f.notices()[0])
	}
}

// An unknown stopped agent is not resumed by an Inbox comment, and the undelivered comment is not marked as a copy.
func TestInboxGitHubCommentToAClosedAgent(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s, f := newQuietService(t)
		turnInboxSignalsOn(t, s)
		todo := agentTask(t, s, f)
		f.setSessions("box", proto.Session{SessionID: "sec00001", MachineID: "box", Cwd: "/code/desk", Agent: "claude", Title: "秘书",
			Status: proto.StatusOffline, State: proto.StateOffline, UpdatedAt: time.Now()})
		before := len(sentToAgent(f))
		if fail {
			f.mu.Lock()
			f.failSend = errors.New("resume failed")
			f.mu.Unlock()
		}
		it := linkedPRItem(s, todo.ID, "c1", "Please merge current main")
		s.superviseInbox(context.Background(), []*InboxItem{it}, map[*InboxItem]bool{it: true})
		waitFor(t, "the signal", func() bool { return len(f.notices()) == 1 })
		if marked := strings.Contains(f.notices()[0], "这里是副本"); marked || len(sentToAgent(f)) != before {
			t.Fatalf("unknown stop was sent to or marked as delivered (send failure=%v)", fail)
		}
	}
}

func TestInboxDeliveredCommentDoesNotHideFailedAgentDelivery(t *testing.T) {
	s, f := newQuietService(t)
	turnInboxSignalsOn(t, s)
	s.superviseTimes.gather = time.Hour
	todo := agentTask(t, s, f)
	f.mu.Lock()
	f.failSend = errors.New("send failed")
	f.mu.Unlock()
	first := linkedPRItem(s, todo.ID, "c1", "the first comment was not delivered")
	s.superviseInbox(context.Background(), []*InboxItem{first}, map[*InboxItem]bool{first: true})
	waitFor(t, "the failed comment to remain unmarked", func() bool {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		g, ok := s.supervise.pending[todo.ID+"|inbox"]
		return ok && g.Inbox.ToAgent == "" && g.Inbox.Item == "github:pr12"
	})
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	second := linkedPRItem(s, todo.ID, "c2", "the second comment was delivered")
	second.ID = "github:pr12:comment2"
	before := len(sentToAgent(f))
	g, _ := inboxSignalOf(second, true)
	s.inboxToAgent(context.Background(), g)

	s.supervise.mu.Lock()
	failed := s.supervise.pending[todo.ID+"|inbox"]
	_, copyPending := s.supervise.pending[todo.ID+"|inbox:agent_copy"]
	s.supervise.mu.Unlock()
	if failed.Inbox == nil || failed.Inbox.ToAgent != "" || failed.Inbox.Item != "github:pr12" {
		t.Fatalf("the failed comment lost its unmarked supervisor entry: %+v", failed.Inbox)
	}
	if copyPending {
		t.Fatal("the successful delivery queued an unnecessary supervisor copy")
	}
	if got := sentToAgent(f)[before:]; len(got) != 1 || !strings.Contains(got[0], "the second comment was delivered") || strings.Contains(got[0], "the first comment was not delivered") {
		t.Fatalf("only the second comment should reach the agent: %q", got)
	}
}

func TestInboxReviewsKeepReachingSupervisorAfterAcceptance(t *testing.T) {
	s, f := newQuietService(t)
	turnInboxSignalsOn(t, s)
	ctx := t.Context()
	task := agentTask(t, s, f)
	s.setTaskStage(ctx, task.ID, "review", linkedPRURL)
	now := time.Now().UTC()
	s.todos.mu.Lock()
	current := s.todos.todos[task.ID]
	current.DoneAt = &now
	current.Supervision.State = reviewAccepted
	current.Supervision.Reviews = []TaskReview{{Verdict: reviewAccepted, At: now}}
	s.todos.mu.Unlock()

	it := linkedPRItem(s, "", "review:1", "Add a regression case for an expired request.")
	it.Connector.ThreadState = "open"
	b := &s.inbox
	b.mu.Lock()
	b.items[it.ID] = it
	b.mu.Unlock()
	before := len(sentToAgent(f))
	s.linkInboxTaskURLs(ctx)
	waitFor(t, "first post-acceptance review", func() bool { return len(f.notices()) == 1 })
	s.linkInboxTaskURLs(ctx) // delivery is deduplicated across repeated collections of the same review

	b.mu.Lock()
	receive(b, it, "review:2", "Check the empty-state wording as well.", 1)
	b.mu.Unlock()
	s.linkInboxTaskURLs(ctx)
	waitFor(t, "second post-acceptance review", func() bool { return len(f.notices()) == 2 })
	notices := f.notices()
	if !strings.Contains(notices[0], "Add a regression case") || !strings.Contains(notices[1], "Check the empty-state wording") ||
		!strings.Contains(notices[1], "待办已验收") || !strings.Contains(notices[1], linkedPRURL) {
		t.Fatalf("notices: %q", notices)
	}
	if after := todoNow(s, task.ID); after.DoneAt == nil || !acceptanceStands(after) || len(sentToAgent(f)) != before {
		t.Fatalf("post-acceptance notifications changed the work: %+v, sends=%v", after.Supervision, sentToAgent(f))
	}
}

func TestAcceptedTaskFeedbackDoesNotFollowClosedOrUnrelatedPRs(t *testing.T) {
	for _, state := range []string{"closed", "merged", "other PR", "cancelled", "unknown state"} {
		t.Run(state, func(t *testing.T) {
			s, _ := newQuietService(t)
			turnInboxSignalsOn(t, s)
			task := supervisedTodo(t, s, "Fix the timer")
			ctx := t.Context()
			s.setTaskStage(ctx, task.ID, "review", linkedPRURL)
			now := time.Now().UTC()
			s.todos.mu.Lock()
			current := s.todos.todos[task.ID]
			current.DoneAt = &now
			current.Supervision.State = reviewAccepted
			current.Supervision.Reviews = []TaskReview{{Verdict: reviewAccepted, At: now}}
			if state == "cancelled" {
				current.CancelledAt = &now
			}
			s.todos.mu.Unlock()

			it := linkedPRItem(s, task.ID, "review:1", "A new comment on the patch.")
			switch state {
			case "closed", "merged":
				it.Connector.ThreadState = state
			case "other PR":
				it.Connector.URL, it.Connector.ThreadState = "https://github.com/example/other/pull/12", "open"
			case "cancelled":
				it.Connector.ThreadState = "open"
			}
			s.superviseInbox(ctx, []*InboxItem{it}, nil)
			if _, ok := pendingInbox(s, task.ID); ok || it.Agent.signaledOf != "" {
				t.Fatal("this feedback was treated as routable")
			}
		})
	}
}
