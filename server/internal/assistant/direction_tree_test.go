package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The direction tree (direction_tree.go, #4352): grouping by supervisor, the secretary group, the task states and
// counts, the Agents and the Agents under them, the route and the apps' event. All ids are placeholders.

const treeMain = "conv_main"

// treeEnv is directionEnv with a main conversation and two current leaders, A and B.
func treeEnv(t *testing.T) *todoEventEnv {
	t.Helper()
	e := directionEnv(t, &memStore{})
	setSettings(t, e.s, func(v *Settings) { v.Main = treeMain })
	e.title("conv_lead_a", "1110 | 功能 | Android parity")
	e.title("conv_lead_b", "1110 | 功能 | Push relay")
	e.leaders("conv_lead_a", "conv_lead_b")
	e.syncLeaders()
	return e
}

// task is a new to-do changed by fn.
func (e *todoEventEnv) task(title string, fn func(*Todo)) string {
	e.t.Helper()
	id := e.create(title).ID
	e.s.todos.mu.Lock()
	if fn != nil {
		fn(e.s.todos.todos[id])
	}
	e.s.todos.mu.Unlock()
	return id
}

func supervisedBy(sup string) func(*Todo) {
	return func(t *Todo) { t.Supervision = &Supervision{Supervisor: sup} }
}

func (e *todoEventEnv) tree() directionTree {
	e.t.Helper()
	return e.s.directionTree(context.Background(), time.Now())
}

func taskIDs(d treeDirection) []string {
	var out []string
	for _, t := range d.Tasks {
		out = append(out, t.ID)
	}
	return out
}

func sortedIDs(ids ...string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func TestDirectionTreeGroupsTasksBySupervisor(t *testing.T) {
	e := treeEnv(t)
	// A leader that ended: its open tasks are the secretary's again.
	e.title("conv_lead_old", "1110 | 功能 | Old line")
	e.leaders("conv_lead_a", "conv_lead_b", "conv_lead_old")
	e.syncLeaders()
	e.leaders("conv_lead_a", "conv_lead_b")
	e.syncLeaders()

	a := e.task("A's task", supervisedBy("conv_lead_a"))
	b := e.task("B's task", supervisedBy("conv_lead_b"))
	mainTask := e.task("Main's task", supervisedBy(treeMain))
	none := e.task("Nobody's task", nil)
	empty := e.task("Empty supervisor", supervisedBy(""))
	ended := e.task("Ended leader's task", supervisedBy("conv_lead_old"))
	unknown := e.task("Unknown conversation's task", supervisedBy("conv_nobody"))

	got := e.tree().Directions
	if len(got) != 3 {
		t.Fatalf("directions: %+v", got)
	}
	if got[0].ID != "conv_lead_a" || got[0].Name != "Android parity" || got[0].Secretary || !slices.Equal(taskIDs(got[0]), []string{a}) {
		t.Fatalf("A: %+v", got[0])
	}
	if got[1].ID != "conv_lead_b" || got[1].Name != "Push relay" || !slices.Equal(taskIDs(got[1]), []string{b}) {
		t.Fatalf("B: %+v", got[1])
	}
	sec := got[2]
	if !sec.Secretary || sec.ID != treeMain || sec.Name != "" {
		t.Fatalf("secretary: %+v", sec)
	}
	if want := sortedIDs(mainTask, none, empty, ended, unknown); !slices.Equal(sortedIDs(taskIDs(sec)...), want) {
		t.Fatalf("secretary's tasks: %v, want %v", taskIDs(sec), want)
	}
	// No agent has worked on any of them: not started.
	if sec.Counts != (treeCounts{Planned: 5}) || got[0].Counts != (treeCounts{Planned: 1}) {
		t.Fatalf("counts: %+v %+v", got[0].Counts, sec.Counts)
	}
}

func TestDirectionTreeWithoutLeadersIsTheSecretaryAlone(t *testing.T) {
	e := directionEnv(t, &memStore{})
	setSettings(t, e.s, func(v *Settings) { v.Main = treeMain })
	got := e.tree().Directions
	if len(got) != 1 || !got[0].Secretary || got[0].Tasks == nil || len(got[0].Tasks) != 0 {
		t.Fatalf("no leaders, no tasks: %+v", got)
	}
}

func TestDirectionTreeTaskStatesAndCounts(t *testing.T) {
	e := treeEnv(t)
	old := time.Now().Add(-30 * time.Hour)
	e.f.setSessions("mac",
		proto.Session{MachineID: "mac", SessionID: "s-ask", State: proto.StateNeedsYou},
		proto.Session{MachineID: "mac", SessionID: "s-ask2", State: proto.StateNeedsYou},
		proto.Session{MachineID: "mac", SessionID: "s-run", State: proto.StateWorking},
		proto.Session{MachineID: "mac", SessionID: "s-run2", State: proto.StateWorking},
		proto.Session{MachineID: "mac", SessionID: "s-idle", State: proto.StateIdle},
		proto.Session{MachineID: "mac", SessionID: "s-old", State: proto.StateIdle, UpdatedAt: old, StateSince: old})
	agent := func(sid string) func(*Todo) {
		return func(t *Todo) {
			t.Supervision = &Supervision{Supervisor: "conv_lead_a"}
			t.Agents = []TodoAgent{{MachineID: "mac", SessionID: sid, Title: sid}}
		}
	}
	both := func(fns ...func(*Todo)) func(*Todo) {
		return func(t *Todo) {
			for _, f := range fns {
				f(t)
			}
		}
	}
	pending := func(t *Todo) { t.Supervision.State = reviewPending }
	stale := func(t *Todo) { t.CreatedAt, t.UpdatedAt, t.PRURL = old, old, "https://example.com/pull/1" }

	asks := e.task("Agent asks", agent("s-ask"))
	// needs_you before review: pending review, but its agent asks.
	asksPending := e.task("Pending review but asks", both(agent("s-ask2"), pending))
	decides := e.task("Owner decides", both(agent("s-idle"), func(t *Todo) { t.Supervision.State = reviewNeedsUser }))
	review := e.task("Awaiting acceptance", both(agent("s-idle"), pending))
	// review needs nothing running: pending review with a working agent is doing.
	runningPending := e.task("Pending review, still running", both(agent("s-run2"), pending))
	stuck := e.task("PR to merge, nothing moved for a day", both(agent("s-old"), stale))
	// A machine that does not answer may be running it: not stuck.
	unreached := e.task("On a machine that does not answer", both(func(t *Todo) {
		t.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		t.Agents = []TodoAgent{{MachineID: "gone", SessionID: "s-x"}}
	}, stale))
	doing := e.task("Running", agent("s-run"))

	a := e.tree().Directions[0]
	state := map[string]string{}
	for _, tk := range a.Tasks {
		state[tk.ID] = tk.State
	}
	want := map[string]string{asks: treeNeedsYou, asksPending: treeNeedsYou, decides: treeNeedsYou, review: treeReview,
		runningPending: treeWorking, stuck: treeStuck, unreached: treeWorking, doing: treeWorking}
	for id, w := range want {
		if state[id] != w {
			t.Errorf("%s: %q, want %q", e.s.todos.todos[id].Title, state[id], w)
		}
	}
	if a.Counts != (treeCounts{NeedsYou: 3, Review: 1, Stuck: 1, Working: 3}) {
		t.Fatalf("counts: %+v", a.Counts)
	}
	// Listed needs_you, review, stuck, working.
	var order []int
	for _, tk := range a.Tasks {
		order = append(order, slices.Index(treeStates, tk.State))
	}
	if !slices.IsSorted(order) {
		t.Fatalf("order: %+v", a.Tasks)
	}
	for _, tk := range a.Tasks {
		if tk.ID == asks && (len(tk.Agents) != 1 || tk.Agents[0].State != proto.StateNeedsYou || tk.Agents[0].MachineID != "mac") {
			t.Fatalf("an asking task's agent: %+v", tk.Agents)
		}
	}
}

func TestDirectionTreeLeavesOutWhatIsNotOpen(t *testing.T) {
	e := treeEnv(t)
	long := time.Now().Add(-72 * time.Hour)
	now := time.Now().UTC()
	open := e.task("Open", supervisedBy("conv_lead_a"))
	e.task("Closed", func(t *Todo) { t.Supervision, t.DoneAt = &Supervision{Supervisor: "conv_lead_a"}, &now })
	e.task("Cancelled", func(t *Todo) {
		t.Supervision, t.DoneAt, t.CancelledAt = &Supervision{Supervisor: "conv_lead_a", State: reviewCancelled}, &now, &now
	})
	e.task("Accepted", func(t *Todo) {
		t.Supervision = &Supervision{Supervisor: "conv_lead_a", State: reviewAccepted,
			Reviews: []TaskReview{{At: now, Verdict: reviewAccepted}}}
	})
	e.task("Shelved", func(t *Todo) {
		t.Supervision, t.Origin, t.CreatedAt, t.UpdatedAt = &Supervision{Supervisor: "conv_lead_a"}, treeMain, long, long
	})
	if a := e.tree().Directions[0]; !slices.Equal(taskIDs(a), []string{open}) || a.Counts != (treeCounts{Planned: 1}) {
		t.Fatalf("A: %+v", a)
	}
}

func TestDirectionTreeNestsWorkersUnderTheirLeaderAgent(t *testing.T) {
	e := treeEnv(t)
	now := time.Now().UTC()
	e.f.setSessions("mac", proto.Session{MachineID: "mac", SessionID: "s-lead", State: proto.StateWorking, Title: "Leader"},
		proto.Session{MachineID: "mac", SessionID: "s-w1", State: proto.StateNeedsYou, Title: "Worker one"})
	lead := e.task("Leader's task", func(t *Todo) {
		t.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		t.Agents = []TodoAgent{{MachineID: "mac", SessionID: "s-lead", Source: sourceLeader, Agent: "claude"}}
	})
	worker := e.task("Worker's task", func(t *Todo) {
		t.Supervision = &Supervision{Supervisor: leaderRef("mac", "s-lead")}
		t.Agents = []TodoAgent{{MachineID: "mac", SessionID: "s-w1", Source: sourceWorker, Via: lead}}
	})
	// A worker of a leader whose task is closed: listed on its own under the conversation above that leader.
	closedLead := e.task("Closed leader's task", func(t *Todo) {
		t.Supervision, t.DoneAt = &Supervision{Supervisor: "conv_lead_b"}, &now
		t.Agents = []TodoAgent{{MachineID: "mac", SessionID: "s-lead-old", Source: sourceLeader}}
	})
	orphan := e.task("Worker of a closed leader", func(t *Todo) {
		t.Supervision = &Supervision{Supervisor: leaderRef("mac", "s-lead-old")}
		t.Agents = []TodoAgent{{MachineID: "mac", SessionID: "s-w2", Source: sourceWorker, Via: closedLead}}
	})

	ds := e.tree().Directions
	a, b := ds[0], ds[1]
	if !slices.Equal(taskIDs(a), []string{lead}) || a.Counts != (treeCounts{Working: 1}) {
		t.Fatalf("A lists the leader's task only, the worker not counted (it needs you, but under its leader): %+v", a)
	}
	agents := a.Tasks[0].Agents
	if len(agents) != 1 || agents[0].SessionID != "s-lead" || agents[0].Title != "Leader" || agents[0].Agent != "claude" ||
		agents[0].State != proto.StateWorking {
		t.Fatalf("leader agent: %+v", agents)
	}
	if kids := agents[0].Children; len(kids) != 1 || kids[0].SessionID != "s-w1" || kids[0].State != proto.StateNeedsYou {
		t.Fatalf("the worker's agent under the leader's: %+v", kids)
	}
	if slices.Contains(taskIDs(ds[2]), worker) {
		t.Fatalf("the worker is listed on its own: %+v", ds[2])
	}
	if !slices.Equal(taskIDs(b), []string{orphan}) || b.Counts != (treeCounts{Working: 1}) {
		t.Fatalf("B lists the closed leader's worker: %+v", b)
	}
}

// Running (#6626) is whether an Agent under the direction is at work, at any depth and whatever its task's state: not
// the working count, which also holds tasks whose Agents all stopped.
func TestDirectionTreeRunningIsAnAgentAtWork(t *testing.T) {
	e := treeEnv(t)
	e.f.setSessions("mac",
		proto.Session{MachineID: "mac", SessionID: "s-ask", State: proto.StateNeedsYou},
		proto.Session{MachineID: "mac", SessionID: "s-run", State: proto.StateWorking},
		proto.Session{MachineID: "mac", SessionID: "s-idle", State: proto.StateIdle},
		proto.Session{MachineID: "mac", SessionID: "s-p", State: proto.StateIdle,
			Subagents: []proto.Subagent{{ID: "a1", SessionID: "s-p@a1", Description: "Search", Running: true}}})
	on := func(lead string, sids ...string) func(*Todo) {
		return func(t *Todo) {
			t.Supervision = &Supervision{Supervisor: lead}
			for _, sid := range sids {
				t.Agents = append(t.Agents, TodoAgent{MachineID: "mac", SessionID: sid})
			}
		}
	}
	// A: its only task is in the working state, but its Agent stopped.
	e.task("Stopped", on("conv_lead_a", "s-idle"))
	// B: its task needs the user (one Agent asks) while another of its Agents still works.
	e.task("Asks while another works", on("conv_lead_b", "s-ask", "s-run"))
	tr := e.tree()
	a, b, sec := tr.Directions[0], tr.Directions[1], tr.Directions[2]
	if a.Counts.Working != 1 || a.Running {
		t.Fatalf("A, a working task with nothing at work: counts %+v, running %v", a.Counts, a.Running)
	}
	if b.Counts.Working != 0 || b.Counts.NeedsYou != 1 || !b.Running {
		t.Fatalf("B, a task that needs the user with an Agent at work: counts %+v, running %v", b.Counts, b.Running)
	}
	if sec.Running {
		t.Fatal("the secretary's group holds nothing")
	}

	// Deeper: an idle Agent whose sub-agent runs is work going on under the direction.
	e.task("Sub-agent at work", on("conv_lead_a", "s-p"))
	if a := e.tree().Directions[0]; !a.Running {
		t.Fatalf("a running sub-agent under an idle Agent: %+v", a)
	}
}

func TestDirectionTreeNestsSpawnedAndSubAgents(t *testing.T) {
	e := treeEnv(t)
	e.f.setSessions("mac",
		proto.Session{MachineID: "mac", SessionID: "s-p", State: proto.StateWorking, Title: "Parent",
			Subagents: []proto.Subagent{
				{ID: "a1", SessionID: "s-p@a1", Description: "Search the code", Running: true},
				{ID: "a2", SessionID: "s-p@a2", ParentID: "a1", Description: "Read one file", Running: true},
				{ID: "a3", SessionID: "s-p@a3", Description: "Done already", Running: false},
			}},
		proto.Session{MachineID: "mac", SessionID: "s-c", State: proto.StateIdle, Title: "Spawned"},
		proto.Session{MachineID: "mac", SessionID: "s-cx", State: proto.StateWorking, Title: "Codex child", Agent: "codex",
			ParentSessionID: "s-p"},
		proto.Session{MachineID: "mac", SessionID: "s-cx-done", State: proto.StateIdle, Agent: "codex", ParentSessionID: "s-p"})
	e.task("Parent and spawned", func(t *Todo) {
		t.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		t.Agents = []TodoAgent{
			{MachineID: "mac", SessionID: "s-c", Source: sourceSpawned, Via: "mac/s-p", Title: "spawned row"},
			{MachineID: "mac", SessionID: "s-p", Source: "manual", Agent: "claude"},
			{MachineID: "mac", SessionID: "s-gone", Title: "Not listed", Agent: "codex"},
		}
	})

	agents := e.tree().Directions[0].Tasks[0].Agents
	if len(agents) != 2 || agents[0].SessionID != "s-p" || agents[1].SessionID != "s-gone" {
		t.Fatalf("top agents: %+v", agents)
	}
	if g := agents[1]; g.State != "" || g.Title != "Not listed" || g.Agent != "codex" || len(g.Children) != 0 {
		t.Fatalf("a session not found: %+v", g)
	}
	kids := agents[0].Children
	var ids []string
	for _, k := range kids {
		ids = append(ids, k.SessionID)
	}
	if !slices.Equal(ids, []string{"s-c", "s-p@a1", "s-cx"}) {
		t.Fatalf("children: %+v", kids)
	}
	if kids[0].Title != "Spawned" || kids[0].State != proto.StateIdle {
		t.Fatalf("spawned child: %+v", kids[0])
	}
	if sub := kids[1]; sub.Title != "Search the code" || sub.State != proto.StateWorking || len(sub.Children) != 1 ||
		sub.Children[0].SessionID != "s-p@a2" || sub.Children[0].Title != "Read one file" {
		t.Fatalf("sub-agents: %+v", sub)
	}
	if cx := kids[2]; cx.Title != "Codex child" || cx.Agent != "codex" || cx.State != proto.StateWorking {
		t.Fatalf("codex child: %+v", cx)
	}
}

func TestDirectionTreeRouteShape(t *testing.T) {
	e := treeEnv(t)
	e.f.setSessions("mac", proto.Session{MachineID: "mac", SessionID: "s-run", State: proto.StateWorking, Title: "Run"})
	e.task("A's task", func(t *Todo) {
		t.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		t.Agents = []TodoAgent{{MachineID: "mac", SessionID: "s-run", Agent: "claude"}}
	})
	e.task("Main's task", supervisedBy(treeMain))

	rec := httptest.NewRecorder()
	e.s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/directions/tree", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		Directions []map[string]json.RawMessage `json:"directions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Directions) != 3 {
		t.Fatalf("%v %s", err, rec.Body)
	}
	keys := func(m map[string]json.RawMessage) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	if k := keys(got.Directions[0]); !slices.Equal(k, []string{"counts", "id", "name", "running", "tasks"}) {
		t.Fatalf("a direction's fields: %v", k)
	}
	// running is sent false too: a client tells it from a hub without it (#6626)
	if r, idle := string(got.Directions[0]["running"]), string(got.Directions[1]["running"]); r != "true" || idle != "false" {
		t.Fatalf("running: %s, idle direction %s", r, idle)
	}
	if k := keys(got.Directions[2]); !slices.Equal(k, []string{"counts", "id", "name", "running", "secretary", "tasks"}) ||
		string(got.Directions[2]["secretary"]) != "true" || string(got.Directions[2]["name"]) != `""` {
		t.Fatalf("the secretary's fields: %v %s", k, got.Directions[2]["secretary"])
	}
	if c := string(got.Directions[0]["counts"]); c != `{"working":1,"needs_you":0,"stuck":0,"review":0,"planned":0}` {
		t.Fatalf("counts: %s", c)
	}
	if ts := string(got.Directions[1]["tasks"]); ts != "[]" {
		t.Fatalf("an empty direction's tasks: %s", ts)
	}
	var tasks []map[string]json.RawMessage
	_ = json.Unmarshal(got.Directions[0]["tasks"], &tasks)
	if len(tasks) != 1 || !slices.Equal(keys(tasks[0]), []string{"agents", "id", "state", "title"}) || string(tasks[0]["state"]) != `"working"` {
		t.Fatalf("task: %v", tasks)
	}
	if a := string(tasks[0]["agents"]); a != `[{"machine_id":"mac","session_id":"s-run","title":"Run","agent":"claude","state":"working"}]` {
		t.Fatalf("agents: %s", a)
	}
}

func (e *todoEventEnv) directionsEvents() int {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	n := 0
	for _, ev := range e.f.events {
		if ev == "directions" {
			n++
		}
	}
	return n
}

func TestDirectionTreeEventOnlyWhenTheTreeChanged(t *testing.T) {
	e := treeEnv(t)
	e.s.dtree.after = e.clock.After
	ctx := context.Background()
	e.s.directionTreeSync(ctx)
	first := e.directionsEvents()
	if first != 1 {
		t.Fatalf("the first build tells the apps: %d", first)
	}
	e.s.directionTreeSync(ctx)
	e.s.directionTreeSoon()
	e.clock.advance(directionTreeSettle)
	if n := e.directionsEvents(); n != first {
		t.Fatalf("nothing changed, told again: %d", n)
	}

	// A new task of A: its change schedules a build, told once the second is over.
	id := e.task("A's task", supervisedBy("conv_lead_a"))
	e.s.directionTreeSoon()
	if n := e.directionsEvents(); n != first {
		t.Fatalf("told inside the second: %d", n)
	}
	e.clock.advance(directionTreeSettle)
	if n := e.directionsEvents(); n != first+1 {
		t.Fatalf("a change: %d", n)
	}

	// A leader's name changed: the tree changed.
	e.title("conv_lead_a", "1110 | 功能 | Android parity v2")
	e.s.directionTreeSync(ctx)
	if n := e.directionsEvents(); n != first+2 {
		t.Fatalf("a renamed direction: %d", n)
	}

	// An agent's session state changed.
	e.s.todos.mu.Lock()
	e.s.todos.todos[id].Agents = []TodoAgent{{MachineID: "mac", SessionID: "s-1"}}
	e.s.todos.mu.Unlock()
	e.f.setSessions("mac", proto.Session{MachineID: "mac", SessionID: "s-1", State: proto.StateWorking})
	e.s.directionTreeSync(ctx)
	e.f.setSessions("mac", proto.Session{MachineID: "mac", SessionID: "s-1", State: proto.StateNeedsYou})
	e.s.directionTreeSync(ctx)
	if n := e.directionsEvents(); n != first+4 {
		t.Fatalf("an agent's state: %d", n)
	}
	e.s.directionTreeSync(ctx)
	if n := e.directionsEvents(); n != first+4 {
		t.Fatalf("the same again: %d", n)
	}
}

func TestDirectionTreeCountsAddUpToTheOpenCount(t *testing.T) {
	e := treeEnv(t)
	now := time.Now().UTC()
	long := time.Now().Add(-72 * time.Hour)
	old := time.Now().Add(-30 * time.Hour)
	e.f.setSessions("mac",
		proto.Session{MachineID: "mac", SessionID: "s-ask", State: proto.StateNeedsYou},
		proto.Session{MachineID: "mac", SessionID: "s-run", State: proto.StateWorking},
		proto.Session{MachineID: "mac", SessionID: "s-idle", State: proto.StateIdle, UpdatedAt: old, StateSince: old})
	with := func(sup, sid string) func(*Todo) {
		return func(t *Todo) {
			t.Supervision = &Supervision{Supervisor: sup}
			if sid != "" {
				t.Agents = []TodoAgent{{MachineID: "mac", SessionID: sid}}
			}
		}
	}
	e.task("A asks", with("conv_lead_a", "s-ask"))
	e.task("A runs", with("conv_lead_a", "s-run"))
	e.task("B stuck", func(t *Todo) {
		with("conv_lead_b", "s-idle")(t)
		t.CreatedAt, t.UpdatedAt, t.PRURL = old, old, "https://example.com/pull/2"
	})
	e.task("Main pending", func(t *Todo) {
		with(treeMain, "s-idle")(t)
		t.Supervision.State = reviewPending
	})
	e.task("Quiet", func(t *Todo) { with(treeMain, "s-run")(t); t.Quiet = true })
	e.task("Failed start", func(t *Todo) { t.Origin, t.StartError = treeMain, "no such launcher" })
	concern := e.task("Concern", func(t *Todo) { t.Kind = "concern" })
	ask := e.askTask("A question that expired")
	e.task("Hand-made", nil)
	e.task("Closed", func(t *Todo) { with("conv_lead_a", "")(t); t.DoneAt = &now })
	e.task("Shelved", func(t *Todo) { t.Origin, t.CreatedAt, t.UpdatedAt = treeMain, long, long })

	ctx := context.Background()
	var all []listedTodo
	var undone []Todo
	e.s.todos.mu.RLock()
	for _, t := range e.s.todos.todos {
		all = append(all, listedTodo{Todo: formatTodo(copyTodoLocked(t))})
		if t.DoneAt == nil {
			undone = append(undone, copyTodoLocked(t))
		}
	}
	e.s.todos.mu.RUnlock()
	look, _ := e.s.leaderSessions(ctx, undone)
	open := triageTodos(all, look, time.Now(), e.s.location(ctx), true).Open

	sum := 0
	for _, d := range e.tree().Directions {
		c := d.Counts
		sum += c.Working + c.NeedsYou + c.Stuck + c.Review + c.Planned
		if n := c.Working + c.NeedsYou + c.Stuck + c.Review + c.Planned; n != len(d.Tasks) {
			t.Fatalf("%s: counts %+v for %d tasks", d.ID, c, len(d.Tasks))
		}
	}
	if sum != open || open != 9 {
		t.Fatalf("the tree counts %d, the To-dos page %d open", sum, open)
	}
	// The To-dos page lists ask to-dos and concerns as any other: so does the tree, in the secretary's group.
	ds := e.tree().Directions
	if sec := taskIDs(ds[len(ds)-1]); !slices.Contains(sec, ask) || !slices.Contains(sec, concern) {
		t.Fatalf("the secretary's tasks: %v, want the ask to-do %s and the concern %s", sec, ask, concern)
	}
}

// askTask is an open to-do as an expired question becomes one (ask_todo.go): its id starts with askTodoPrefix.
func (e *todoEventEnv) askTask(title string) string {
	e.t.Helper()
	id := e.task(title, nil)
	e.s.todos.mu.Lock()
	t := e.s.todos.todos[id]
	delete(e.s.todos.todos, id)
	t.ID = askTodoPrefix + "q1"
	e.s.todos.todos[t.ID] = t
	e.s.todos.mu.Unlock()
	return t.ID
}

// Inside a state the newest activity comes first; the same activity is ordered by id.
func TestDirectionTreeOrdersTasksByActivityThenID(t *testing.T) {
	e := treeEnv(t)
	at := func(when time.Time) func(*Todo) {
		return func(t *Todo) { supervisedBy("conv_lead_a")(t); t.CreatedAt, t.UpdatedAt = when, when }
	}
	base := time.Now().Add(-2 * time.Hour).UTC()
	older := e.task("Older", at(base))
	newer := e.task("Newer", at(base.Add(time.Hour)))
	tie1 := e.task("Tie one", at(base.Add(30*time.Minute)))
	tie2 := e.task("Tie two", at(base.Add(30*time.Minute)))
	ties := sortedIDs(tie1, tie2)
	want := []string{newer, ties[0], ties[1], older}
	for range 5 { // the same every build
		if got := taskIDs(e.tree().Directions[0]); !slices.Equal(got, want) {
			t.Fatalf("order: %v, want %v", got, want)
		}
	}
}

// Workers whose leaders lead one another in a loop are listed on their own, the same on every build; a worker that
// only leads into the loop stays under its leader's Agent.
func TestDirectionTreeWorkerLoopIsTheSameEveryBuild(t *testing.T) {
	e := treeEnv(t)
	var a, b string
	a = e.task("Loop A", nil)
	b = e.task("Loop B", nil)
	in := e.task("Into the loop", nil)
	set := func(id, sup, sid, via string) {
		e.s.todos.mu.Lock()
		t := e.s.todos.todos[id]
		t.Supervision = &Supervision{Supervisor: sup}
		t.Agents = []TodoAgent{{MachineID: "mac", SessionID: sid, Source: sourceLeader},
			{MachineID: "mac", SessionID: sid + "-w", Source: sourceWorker, Via: via}}
		e.s.todos.mu.Unlock()
	}
	set(a, leaderRef("mac", "s-b"), "s-a", b)
	set(b, leaderRef("mac", "s-a"), "s-b", a)
	set(in, leaderRef("mac", "s-a"), "s-in", a)
	var first []byte
	for i := range 20 {
		raw, _ := json.Marshal(e.tree())
		if i == 0 {
			first = raw
			sec := e.tree().Directions[2]
			if got, want := sortedIDs(taskIDs(sec)...), sortedIDs(a, b); !slices.Equal(got, want) {
				t.Fatalf("the loop listed on its own, the one into it nested: %v, want %v", got, want)
			}
			continue
		}
		if string(raw) != string(first) {
			t.Fatalf("build %d differs:\n%s\n%s", i, raw, first)
		}
	}
}

// Renaming a direction tells the apps the tree changed, also on a hub where the rename publishes nothing (no
// extension takes leader.changed): directionsSoon builds the tree again.
func TestDirectionTreeToldWhenADirectionIsRenamed(t *testing.T) {
	ctx := context.Background()
	te := newTodoEnv(t, &memStore{})
	if err := te.s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock()
	e := &todoEventEnv{todoEnv: te, clock: clock}
	e.s.directions.after, e.s.dtree.after = clock.After, clock.After
	setSettings(t, e.s, func(v *Settings) { v.Main = treeMain })
	e.title("conv_lead_a", "1110 | 功能 | Android parity")
	e.leaders("conv_lead_a")
	e.s.directionsSync(ctx)
	clock.advance(time.Minute) // whatever the setup scheduled
	e.s.directionTreeSync(ctx)
	first := e.directionsEvents()

	if err := e.s.setDirectionName(ctx, "conv_lead_a", "基建", directionByUser); err != nil {
		t.Fatal(err)
	}
	clock.advance(directionsSettle + directionTreeSettle)
	if n := e.directionsEvents(); n != first+1 {
		t.Fatalf("renamed: told %d times, want once", n-first)
	}
	if d := e.tree().Directions[0]; d.Name != "基建" {
		t.Fatalf("name: %+v", d)
	}
}

// The sessions of a machine an open task's Agent is on changing schedules a build; another machine's do not.
func TestDirectionTreeSessionsChangedSchedulesABuild(t *testing.T) {
	e := treeEnv(t)
	e.s.dtree.after = e.clock.After
	e.clock.advance(time.Minute)
	ctx := context.Background()
	id := e.task("A's task", supervisedBy("conv_lead_a"))
	e.s.todos.mu.Lock()
	e.s.todos.todos[id].Agents = []TodoAgent{{MachineID: "mac", SessionID: "s-1"}}
	e.s.todos.activeBySession[[2]string{"mac", "s-1"}] = id
	e.s.todos.mu.Unlock()
	e.f.setSessions("mac", proto.Session{MachineID: "mac", SessionID: "s-1", State: proto.StateWorking})
	e.s.directionTreeSync(ctx)
	first := e.directionsEvents()

	armed := func() bool {
		e.s.dtree.mu.Lock()
		defer e.s.dtree.mu.Unlock()
		return e.s.dtree.armed
	}
	e.s.SessionsChanged(ctx, DeviceSessions{MachineID: "other"})
	if armed() {
		t.Fatal("a machine with no task's Agent scheduled a build")
	}
	e.f.setSessions("mac", proto.Session{MachineID: "mac", SessionID: "s-1", State: proto.StateNeedsYou})
	e.s.SessionsChanged(ctx, DeviceSessions{MachineID: "mac", Sessions: []proto.Session{{MachineID: "mac", SessionID: "s-1",
		State: proto.StateNeedsYou}}})
	if !armed() {
		t.Fatal("no build scheduled")
	}
	e.clock.advance(directionTreeSettle)
	if n := e.directionsEvents(); n != first+1 {
		t.Fatalf("an agent's state through SessionsChanged: told %d times", n-first)
	}
}
