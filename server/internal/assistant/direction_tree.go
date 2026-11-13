package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/proto"
)

// The direction tree (#4352, docs/assistant/unified-entry.md "The direction tree"): each current direction (a leader
// conversation) with the open tasks it supervises, each task with its state and its Agents, each Agent with the
// Agents under it; then the secretary group (the main conversation's) with every other open task. The sidebar, the
// To-dos page's direction grouping and the Agents page's direction tree on every client read this one tree
// (GET /api/v1/assistant/directions/tree): no client works out the grouping or a task's state itself.
//
//   - Which tasks: the ones the To-dos page counts open (triageCountsAsOpen, the hub's copy of the SOP, quiet tasks
//     included as the page includes them), so the four counts of every group add up to the page's open count; a
//     worker task nested under its leader's Agent is the one exception (it is not listed or counted on its own).
//   - Whose: the task's supervisor. A current leader's tasks are its direction's; the main conversation's, no
//     supervisor's, an ended or unknown conversation's are the secretary's. A worker task (its supervisor a task
//     leader's Agent, task_leader.go) puts its Agents under that leader Agent inside the leader's task; when the
//     leader's task is not listed it is a task of the conversation above the leader (supervisorAboveTask).
//   - Its state, first match wins: needs_you (the SOP's Waiting on you), review (waits for acceptance, nothing of it
//     running), planned (no agent has ever worked on it and it is not a concern: a piece of a direction a leader
//     recorded with todo_create, a Lark topic's to-do, a to-do the user made; #5846), stuck (taskStuck, the leader
//     reports' rule), working. A direction lists them needs_you, review, stuck, working, planned.
//   - Running: whether anything under a direction is at work right now, an Agent of one of its listed tasks or one
//     under it whose session is working (the state its row shows), whatever the task's state. Not the working count:
//     a working task may have every Agent stopped, and one that needs the user may still have another Agent at work.
//     It is the one rule for a direction row's activity mark on every client (#6626); the leader conversation's own
//     turn is its own row's state, which the clients have.
//
// The apps are told {"type":"directions"} whenever the tree's content changes (directionTreeSync), and read the tree
// again then.

// The tree's task states, in the order a direction lists them.
const (
	treeNeedsYou = "needs_you"
	treeReview   = "review"
	treeStuck    = "stuck"
	treeWorking  = "working"
	treePlanned  = "planned"
)

var treeStates = []string{treeNeedsYou, treeReview, treeStuck, treeWorking, treePlanned}

// sourceSpawned: a to-do Agent linked because another Agent of the to-do spawned it (todo_sessions.go); its via is
// "<machine>/<session>" of that Agent.
const sourceSpawned = "spawned"

// directionTreeSettle is how long a change waits for the rest before the tree is built again and compared.
const directionTreeSettle = time.Second

// directionTree is GET /api/v1/assistant/directions/tree.
type directionTree struct {
	Directions []treeDirection `json:"directions"`
}

// treeDirection is one direction, or the secretary group (Secretary; ID the main conversation's, Name "").
type treeDirection struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Secretary bool       `json:"secretary,omitempty"`
	Counts    treeCounts `json:"counts"`
	Running   bool       `json:"running"` // sent false too, so a client tells a hub without it (it reads counts.working then)
	Tasks     []treeTask `json:"tasks"`
}

// treeCounts are how many of a direction's listed tasks are in each state.
type treeCounts struct {
	Working  int `json:"working"`
	NeedsYou int `json:"needs_you"`
	Stuck    int `json:"stuck"`
	Review   int `json:"review"`
	Planned  int `json:"planned"`
}

func (c *treeCounts) add(state string) {
	switch state {
	case treeNeedsYou:
		c.NeedsYou++
	case treeReview:
		c.Review++
	case treeStuck:
		c.Stuck++
	case treePlanned:
		c.Planned++
	default:
		c.Working++
	}
}

// treeTask is one open task of a direction.
type treeTask struct {
	ID     string      `json:"id"`
	Title  string      `json:"title"`
	State  string      `json:"state"`
	Agents []treeAgent `json:"agents"`
	last   time.Time   // its latest activity (the SOP's), for the order
}

// treeAgent is one Agent of a task, or one under another Agent: State is the session's state as the apps get it on
// its row ("" when the session was not found).
type treeAgent struct {
	MachineID string      `json:"machine_id"`
	SessionID string      `json:"session_id"`
	Title     string      `json:"title"`
	Agent     string      `json:"agent"`
	State     string      `json:"state"`
	Children  []treeAgent `json:"children,omitempty"`
}

// directionTreeBook is what the apps were last told the tree is (its digest), and the scheduled compare.
type directionTreeBook struct {
	mu     sync.Mutex
	armed  bool
	told   bool
	digest [sha256.Size]byte
	after  func(d time.Duration, f func()) // tests: a fake clock
	run    sync.Mutex                      // one build and compare at a time
}

// directionTreeRoutes: GET /api/v1/assistant/directions/tree is the tree as it is now.
func (s *Service) directionTreeRoutes() {
	s.handle("GET /api/v1/assistant/directions/tree", func(w http.ResponseWriter, r *http.Request) {
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		apierr.WriteJSON(w, s.directionTree(r.Context(), time.Now()))
	})
}

// directionTreeSoon schedules building the tree again and telling the apps when it changed, once the changes of the
// next directionTreeSettle are in. Called when the snapshot is published (todos, titles, settings, the leaders), when a
// to-do changes, and when the sessions of a machine a task's Agent is on change.
func (s *Service) directionTreeSoon() {
	b := &s.dtree
	b.mu.Lock()
	if b.armed {
		b.mu.Unlock()
		return
	}
	b.armed = true
	after := b.after
	b.mu.Unlock()
	f := func() { s.directionTreeSync(context.Background()) }
	if after != nil {
		after(directionTreeSettle, f)
		return
	}
	time.AfterFunc(directionTreeSettle, f)
}

// directionTreeSessions: the sessions of machine d changed; the tree is built again when an open task's Agent is there.
func (s *Service) directionTreeSessions(d DeviceSessions) {
	s.todos.mu.RLock()
	on := false
	for k := range s.todos.activeBySession {
		if k[0] == d.MachineID {
			on = true
			break
		}
	}
	s.todos.mu.RUnlock()
	if on {
		s.directionTreeSoon()
	}
}

// directionTreeSync builds the tree and tells the apps {"type":"directions"} when it is not the one they were last told
// of. Nothing is told before the to-dos are loaded.
func (s *Service) directionTreeSync(ctx context.Context) {
	b := &s.dtree
	b.mu.Lock()
	b.armed = false
	b.mu.Unlock()
	if !s.todosLoaded() {
		return
	}
	b.run.Lock()
	defer b.run.Unlock()
	raw, err := json.Marshal(s.directionTree(ctx, time.Now()))
	if err != nil {
		return
	}
	sum := sha256.Sum256(raw)
	b.mu.Lock()
	same := b.told && b.digest == sum
	b.digest, b.told = sum, true
	b.mu.Unlock()
	if !same {
		s.event(ctx, map[string]any{"type": "directions"})
	}
}

// currentLeaders are the current leaders (assistant.leaders without the main conversation) in the order
// GET /api/v1/assistant/leaders lists them (by when each became a direction, then id; one not recorded yet last), and
// their names as it gives them.
func (s *Service) currentLeaders(ctx context.Context, cfg Settings) ([]string, map[string]string) {
	titles, _ := s.conversationTitles()
	pnames, pok := s.projectNames(ctx)
	type lead struct {
		id   string
		rec  bool
		at   time.Time
		name string
	}
	var list []lead
	b := &s.directions
	b.mu.Lock()
	loaded := s.directionsLocked(ctx)
	for _, id := range cfg.Leaders {
		if id == "" || id == cfg.Main || slices.ContainsFunc(list, func(l lead) bool { return l.id == id }) {
			continue
		}
		l := lead{id: id, name: directionName(nil, titles[id].title, "")}
		if d := b.m[id]; loaded && d != nil {
			l.rec, l.at, l.name = true, d.At, nameOf(d, titles[id].title, pnames, pok)
		}
		list = append(list, l)
	}
	b.mu.Unlock()
	slices.SortFunc(list, func(a, b lead) int {
		if a.rec != b.rec {
			return cond(a.rec, -1, 1)
		}
		return cmp.Or(a.at.Compare(b.at), cmp.Compare(a.id, b.id))
	})
	ids := make([]string, 0, len(list))
	names := make(map[string]string, len(list))
	for _, l := range list {
		ids = append(ids, l.id)
		names[l.id] = l.name
	}
	return ids, names
}

// directionTree builds the tree from the to-dos and their Agents' sessions now (each machine asked once).
func (s *Service) directionTree(ctx context.Context, now time.Time) directionTree {
	cfg := s.settings(ctx)
	leads, names := s.currentLeaders(ctx, cfg)

	s.todos.mu.RLock()
	all := make([]listedTodo, 0, len(s.todos.todos))
	for _, t := range s.todos.todos {
		all = append(all, listedTodo{Todo: formatTodo(copyTodoLocked(t))}) // as GET /api/v1/todos lists it
	}
	s.todos.mu.RUnlock()
	slices.SortFunc(all, func(a, b listedTodo) int { return cmp.Compare(a.ID, b.ID) })
	asks := s.todoAsks(ctx) // once the to-dos are let go: it reads them itself
	var undone []Todo
	for i := range all {
		if a, ok := asks[all[i].ID]; ok {
			all[i].Ask = &a
		}
		if all[i].DoneAt == nil {
			undone = append(undone, all[i].Todo)
		}
	}
	lists, reached := s.agentSessionLists(ctx, undone)
	tb := newTreeBuild(s, cfg.Main, leads, all, lists, reached, now, s.location(ctx))
	return tb.tree(names)
}

// treeBuild is one build of the tree: the to-dos placed, the sessions by machine and id, and what is nested where.
type treeBuild struct {
	s       *Service
	main    string
	leads   []string
	todos   map[string]listedTodo
	placed  map[string]todoTriage
	open    []string // the ids the To-dos page counts open, by id
	reached map[string]bool
	now     time.Time

	sessions map[[2]string]proto.Session
	codexKid map[[2]string][]proto.Session // a machine's Codex sub-agent sessions still going, by parent session
	// leaderAgent: every task leader's Agent (machine, session) and the to-dos it is the leader Agent of.
	leaderAgent map[[2]string][]string
	// workerAt: an open worker task listed under a leader Agent instead of on its own: the leader task and that Agent.
	workerAt map[string]treeAttach
	// workersUnder: the worker tasks under each (leader task, leader Agent).
	workersUnder map[treeAttach][]string
}

type treeAttach struct {
	task  string
	agent [2]string
}

func newTreeBuild(s *Service, main string, leads []string, all []listedTodo, lists map[string][]proto.Session,
	reached map[string]bool, now time.Time, loc *time.Location) *treeBuild {
	tb := &treeBuild{s: s, main: main, leads: leads, todos: map[string]listedTodo{}, reached: reached, now: now,
		sessions: map[[2]string]proto.Session{}, codexKid: map[[2]string][]proto.Session{},
		leaderAgent: map[[2]string][]string{}, workerAt: map[string]treeAttach{}, workersUnder: map[treeAttach][]string{}}
	for machine, list := range lists {
		for _, c := range list {
			tb.sessions[[2]string{machine, c.SessionID}] = c
			if c.ParentSessionID != "" && treeGoing(c) {
				k := [2]string{machine, c.ParentSessionID}
				tb.codexKid[k] = append(tb.codexKid[k], c)
			}
		}
	}
	for k := range tb.codexKid {
		slices.SortFunc(tb.codexKid[k], func(a, b proto.Session) int { return cmp.Compare(a.SessionID, b.SessionID) })
	}
	look := func(machine, id string) (proto.Session, bool) {
		c, ok := tb.sessions[[2]string{machine, id}]
		return c, ok
	}
	tb.placed = triagePlace(all, look, now, loc)
	for _, t := range all {
		tb.todos[t.ID] = t
		if triageCountsAsOpen(t.Todo, tb.placed[t.ID]) {
			tb.open = append(tb.open, t.ID)
		}
		for _, a := range t.Agents {
			if a.Source == sourceLeader {
				k := [2]string{a.MachineID, a.SessionID}
				tb.leaderAgent[k] = append(tb.leaderAgent[k], t.ID)
			}
		}
	}
	for _, id := range tb.open {
		if at, ok := tb.leaderOf(tb.todos[id].Todo); ok {
			tb.workerAt[id] = at
		}
	}
	// A worker whose chain of leaders comes back to it is listed on its own, so nothing is lost in a loop. Each chain
	// is walked over the nesting as it was before any is undone, so which tasks come out does not depend on the order
	// they are looked at: every task in a loop, and only those (one leading into a loop stays nested).
	var loop []string
	for _, id := range tb.open {
		seen := map[string]bool{}
		for at, ok := tb.workerAt[id]; ok && !seen[at.task]; at, ok = tb.workerAt[at.task] {
			if at.task == id {
				loop = append(loop, id)
				break
			}
			seen[at.task] = true
		}
	}
	for _, id := range loop {
		delete(tb.workerAt, id)
	}
	for _, id := range tb.open {
		if at, ok := tb.workerAt[id]; ok {
			tb.workersUnder[at] = append(tb.workersUnder[at], id)
		}
	}
	return tb
}

// treeGoing: a Codex sub-agent session not finished (working, or waiting for the owner).
func treeGoing(c proto.Session) bool {
	st := triageStateOf(c)
	return st == proto.StateWorking || st == proto.StateNeedsYou
}

func (tb *treeBuild) isOpen(id string) bool {
	_, ok := slices.BinarySearch(tb.open, id)
	return ok
}

// leaderOf is where worker task t is listed under a leader Agent: the open leader task whose active leader Agent its
// supervisor names, else (a leader started again) the current leader Agent of the open leader task its worker row
// names (leaderSupervisor's rule). False for any other task, and for a worker whose leader's task is not open.
func (tb *treeBuild) leaderOf(t Todo) (treeAttach, bool) {
	if t.Supervision == nil {
		return treeAttach{}, false
	}
	machine, sid, ok := parseLeaderRef(t.Supervision.Supervisor)
	if !ok {
		return treeAttach{}, false
	}
	k := [2]string{tb.s.canonical(machine), sid}
	for _, id := range tb.leaderAgent[k] {
		if id != t.ID && tb.isOpen(id) && treeActiveLeader(tb.todos[id].Todo, k) {
			return treeAttach{task: id, agent: k}, true
		}
	}
	var best TodoAgent
	var at treeAttach
	found := false
	for _, a := range t.Agents {
		if a.Source != sourceWorker || a.Via == "" || a.Via == t.ID || !tb.isOpen(a.Via) {
			continue
		}
		for _, l := range tb.todos[a.Via].Agents {
			if l.Source == sourceLeader && l.active() && (!found || l.LinkedAt.After(best.LinkedAt)) {
				best, found = l, true
				at = treeAttach{task: a.Via, agent: [2]string{l.MachineID, l.SessionID}}
			}
		}
	}
	return at, found
}

// treeActiveLeader: k is an active leader Agent of t.
func treeActiveLeader(t Todo, k [2]string) bool {
	return slices.ContainsFunc(t.Agents, func(a TodoAgent) bool {
		return a.Source == sourceLeader && a.active() && a.MachineID == k[0] && a.SessionID == k[1]
	})
}

// directionOf is the group a listed task is in: its supervisor when that is a current leader, the leader's own
// supervisor for a worker listed on its own (supervisorAboveTask), else the secretary ("").
func (tb *treeBuild) directionOf(t Todo) string {
	if t.Supervision == nil {
		return ""
	}
	sup := t.Supervision.Supervisor
	if machine, sid, ok := parseLeaderRef(sup); ok {
		sup = ""
		for _, id := range tb.leaderAgent[[2]string{tb.s.canonical(machine), sid}] {
			if l := tb.todos[id].Todo; l.Supervision != nil {
				if _, _, ref := parseLeaderRef(l.Supervision.Supervisor); !ref {
					sup = l.Supervision.Supervisor
					break
				}
			}
		}
	}
	if sup != "" && slices.Contains(tb.leads, sup) {
		return sup
	}
	return ""
}

// state is listed task t's state in the tree (first match wins).
func (tb *treeBuild) state(t listedTodo) string {
	tr := tb.placed[t.ID]
	switch {
	case tr.Section == triageWaiting:
		return treeNeedsYou
	case tr.Section != triageWorking && (t.Supervision.state(t.Todo) == reviewPending || triageOpenAsk(t.Ask) != nil):
		return treeReview
	case notStarted(t.Todo):
		return treePlanned
	case taskStuck(t.Todo, tr, tb.reached[t.ID], tb.now):
		return treeStuck
	}
	return treeWorking
}

// tree is the directions in order with the secretary group last.
func (tb *treeBuild) tree(names map[string]string) directionTree {
	groups := make(map[string]*treeDirection, len(tb.leads)+1)
	out := directionTree{Directions: make([]treeDirection, 0, len(tb.leads)+1)}
	for _, id := range tb.leads {
		groups[id] = &treeDirection{ID: id, Name: names[id], Tasks: []treeTask{}}
	}
	sec := &treeDirection{ID: tb.main, Secretary: true, Tasks: []treeTask{}}
	for _, id := range tb.open {
		if _, nested := tb.workerAt[id]; nested {
			continue
		}
		t := tb.todos[id]
		g := sec
		if lead := tb.directionOf(t.Todo); lead != "" {
			g = groups[lead]
		}
		state := tb.state(t)
		g.Counts.add(state)
		agents := tb.agents(t.ID, map[[2]string]bool{})
		g.Running = g.Running || treeRunning(agents)
		g.Tasks = append(g.Tasks, treeTask{ID: t.ID, Title: t.Title, State: state, Agents: agents, last: tb.placed[id].Last})
	}
	for _, g := range append(slicesOf(groups, tb.leads), sec) {
		slices.SortStableFunc(g.Tasks, func(a, b treeTask) int {
			return cmp.Or(cmp.Compare(slices.Index(treeStates, a.State), slices.Index(treeStates, b.State)),
				b.last.Compare(a.last), cmp.Compare(a.ID, b.ID))
		})
		out.Directions = append(out.Directions, *g)
	}
	return out
}

// treeRunning: one of these Agents, or one under them, is at work (its session working as its row shows it).
func treeRunning(agents []treeAgent) bool {
	return slices.ContainsFunc(agents, func(a treeAgent) bool { return a.State == proto.StateWorking || treeRunning(a.Children) })
}

func slicesOf(m map[string]*treeDirection, ids []string) []*treeDirection {
	out := make([]*treeDirection, 0, len(ids))
	for _, id := range ids {
		out = append(out, m[id])
	}
	return out
}

// agents are task id's active Agents as nodes: one spawned by another Agent of the task under it, the rest at the top
// (in the order the task has them); seen keeps each Agent to one place in the task's subtree.
func (tb *treeBuild) agents(id string, seen map[[2]string]bool) []treeAgent {
	t := tb.todos[id].Todo
	var active []TodoAgent
	mine := map[[2]string]bool{}
	for _, a := range t.Agents {
		if a.active() {
			active = append(active, a)
			mine[[2]string{a.MachineID, a.SessionID}] = true
		}
	}
	under := func(a TodoAgent) ([2]string, bool) { // the Agent of this task that spawned a
		if a.Source != sourceSpawned {
			return [2]string{}, false
		}
		m, sid, ok := parseLeaderRef(a.Via)
		k := [2]string{tb.s.canonical(m), sid}
		return k, ok && mine[k] && k != [2]string{a.MachineID, a.SessionID}
	}
	var node func(a TodoAgent) treeAgent
	node = func(a TodoAgent) treeAgent {
		k := [2]string{a.MachineID, a.SessionID}
		seen[k] = true
		n := tb.sessionNode(a.MachineID, a.SessionID, a.Title, a.Agent)
		var kids []treeAgent
		for _, c := range active {
			if p, ok := under(c); ok && p == k && !seen[[2]string{c.MachineID, c.SessionID}] {
				kids = append(kids, node(c))
			}
		}
		for _, w := range tb.workersUnder[treeAttach{task: id, agent: k}] {
			kids = append(kids, tb.agents(w, seen)...)
		}
		n.Children = append(kids, tb.sessionKids(a.MachineID, a.SessionID, a.Agent, seen)...)
		return n
	}
	out := []treeAgent{}
	for _, a := range active {
		if _, ok := under(a); !ok && !seen[[2]string{a.MachineID, a.SessionID}] {
			out = append(out, node(a))
		}
	}
	for _, a := range active { // spawned by one another in a loop: at the top, so none is lost
		if !seen[[2]string{a.MachineID, a.SessionID}] {
			out = append(out, node(a))
		}
	}
	return out
}

// sessionNode is the node of session machine/id: its title and state as the apps get them (title the to-do's own
// when the session has none or was not found).
func (tb *treeBuild) sessionNode(machine, id, title, agent string) treeAgent {
	n := treeAgent{MachineID: machine, SessionID: id, Title: title, Agent: agent}
	if c, ok := tb.sessions[[2]string{machine, id}]; ok {
		n.Title, n.Agent, n.State = cmp.Or(c.Title, title), cmp.Or(agent, c.Agent), triageStateOf(c)
	}
	return n
}

// sessionKids are the Agents under session machine/id that are not to-do Agents: its running in-session sub-agents
// (nested by the sub-agent that launched each) and its Codex sub-agent sessions still going.
func (tb *treeBuild) sessionKids(machine, id, agent string, seen map[[2]string]bool) []treeAgent {
	var out []treeAgent
	if c, ok := tb.sessions[[2]string{machine, id}]; ok {
		running := map[string]bool{}
		for _, a := range c.Subagents {
			if a.Running {
				running[a.ID] = true
			}
		}
		launcher := func(a proto.Subagent) string { // "": the session itself
			return cond(a.ParentID != a.ID && running[a.ParentID], a.ParentID, "")
		}
		var under func(parent string, all bool) []treeAgent
		under = func(parent string, all bool) []treeAgent {
			var kids []treeAgent
			for _, a := range c.Subagents {
				sid := cmp.Or(a.SessionID, id+"@"+a.ID)
				if !a.Running || seen[[2]string{machine, sid}] || !all && launcher(a) != parent {
					continue
				}
				seen[[2]string{machine, sid}] = true
				kids = append(kids, treeAgent{MachineID: machine, SessionID: sid, Title: cmp.Or(a.Description, a.Type),
					Agent: cmp.Or(agent, c.Agent), State: proto.StateWorking, Children: under(a.ID, false)})
			}
			return kids
		}
		out = append(out, under("", false)...)
		out = append(out, under("", true)...) // launched by one another in a loop: at the top, so none is lost
	}
	for _, kid := range tb.codexKid[[2]string{machine, id}] {
		k := [2]string{machine, kid.SessionID}
		if seen[k] {
			continue
		}
		seen[k] = true
		n := tb.sessionNode(machine, kid.SessionID, "", kid.Agent)
		n.Children = tb.sessionKids(machine, kid.SessionID, kid.Agent, seen)
		out = append(out, n)
	}
	return out
}
