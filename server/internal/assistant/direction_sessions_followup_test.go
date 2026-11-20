package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// What #11391 left to do (#11458). Like the tests beside them these read the routes' answers, the stored book and the
// tree, so a manager without the fixes fails them on behavior, not on a missing symbol. The standing tests run on the
// manager's own fleet, which answers a disconnected computer's list from what it last listed.

// directionFleetComputer adds computer id to the fleet env's devices, its node listing list.
func directionFleetComputer(t *testing.T, ft *fleetTest, id string, list ...proto.Session) {
	t.Helper()
	ft.d.add(id, id)
	ft.sessions(id, list...)
	directionQueueDone(t, ft.s)
}

// directionFleetAway: computer id's node is not connected now; the devices keep its last list.
func directionFleetAway(ft *fleetTest, id string) {
	ft.d.mu.Lock()
	defer ft.d.mu.Unlock()
	ft.d.machines[id].away = true
}

// directionFleetRemove: computer id is no longer one of the manager's computers.
func directionFleetRemove(ft *fleetTest, id string) {
	ft.d.mu.Lock()
	defer ft.d.mu.Unlock()
	delete(ft.d.machines, id)
	ft.d.order = slices.DeleteFunc(ft.d.order, func(m string) bool { return m == id })
}

// directionQueueDone waits until what the fleet queued so far (a list's SessionsChanged) has run.
func directionQueueDone(t *testing.T, s *Service) {
	t.Helper()
	done := make(chan struct{})
	s.inOrder(func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the fleet's queue did not drain")
	}
}

// directionTreeArmed reports whether a tree rebuild is scheduled and clears the mark for the next step. The env's
// rebuild never runs by itself (dtree.after does nothing), so the mark stays until it is read.
func directionTreeArmed(s *Service) bool {
	s.dtree.mu.Lock()
	defer s.dtree.mu.Unlock()
	armed := s.dtree.armed
	s.dtree.armed = false
	return armed
}

func directionTreeManual(s *Service) {
	s.dtree.mu.Lock()
	defer s.dtree.mu.Unlock()
	s.dtree.after = func(time.Duration, func()) {}
	s.dtree.armed = false
}

func directionHubEvents(f *fakeHub, kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, ev := range f.events {
		if ev == kind {
			n++
		}
	}
	return n
}

func directionStoppedAgent(id string) proto.Session {
	return proto.Session{SessionID: id, Agent: "claude", Cwd: "/home/u/code", Status: proto.StatusOffline, State: proto.StateOffline,
		Title: "Agent " + id, UpdatedAt: time.Now().Add(-time.Hour)}
}

// A filed Agent on a computer that disconnects reads as its computer offline, in the read and in the tree, although the
// fleet still lists it as working (point 1); the disconnect itself rebuilds the tree.
func TestDirectionStandingComputerOfflineWithTheFleet(t *testing.T) {
	ft := directionFilingFleetEnv(t, newFleetMem())
	directionTreeManual(ft.s)
	ctx := context.Background()
	directionFleetComputer(t, ft, "laptop", proto.Session{SessionID: "agent-laptop", Agent: "claude", Cwd: "/home/u/code",
		Status: proto.StatusWorking, Title: "Laptop agent"})
	directionFilingMustPut(t, ft.s, "laptop", "agent-laptop", "conv_lead_a")
	if got := directionFindSession(t, directionMustRead(t, ft.s, "", "conv_lead_a").Sessions, "agent-laptop"); got.State != proto.StateWorking {
		t.Fatalf("on a connected computer the Agent reads its own state: %+v", got)
	}
	if tree, _ := directionFilingTreeGet(t, ft.s); !slices.Contains(directionSessionIDs(tree[0].Sessions), "agent-laptop") {
		t.Fatalf("a working filed Agent is in its direction's tree sessions: %+v", tree[0])
	}
	ft.s.directionTreeSync(ctx)
	told := directionHubEvents(ft.f, "directions")
	directionTreeArmed(ft.s)

	directionFleetAway(ft, "laptop")
	list, err := ft.s.sessions(ctx, "laptop")
	if err != nil || !slices.ContainsFunc(list, func(c proto.Session) bool {
		return c.SessionID == "agent-laptop" && triageStateOf(c) == proto.StateWorking
	}) {
		t.Fatalf("precondition: the fleet answers the disconnected computer from its last list: %v %+v", err, list)
	}
	got := directionFindSession(t, directionMustRead(t, ft.s, "", "conv_lead_a").Sessions, "agent-laptop")
	if got.State != "computer_offline" {
		t.Fatalf("filed Agent on a disconnected computer: state %q, want computer_offline", got.State)
	}
	tree, raw := directionFilingTreeGet(t, ft.s)
	if strings.Contains(raw, "agent-laptop") {
		t.Fatalf("the tree still lists the Agent of a disconnected computer: %+v", tree[0])
	}
	ft.s.NodeDetached("laptop")
	if !directionTreeArmed(ft.s) {
		t.Fatal("a disconnect of a filed session's computer did not schedule a tree rebuild")
	}
	ft.s.directionTreeSync(ctx)
	if got := directionHubEvents(ft.f, "directions"); got != told+1 {
		t.Fatalf("directions events: %d, want %d", got, told+1)
	}
}

// An archived Agent reads not_listed: archived through the archive book, its row still in its computer's list (point 2).
func TestDirectionStandingArchivedAgentThroughTheArchiveBook(t *testing.T) {
	ft := directionFilingFleetEnv(t, newFleetMem())
	ctx := context.Background()
	directionFleetComputer(t, ft, "box", directionStoppedAgent("agent-stopped"))
	directionFilingMustPut(t, ft.s, "box", "agent-stopped", "conv_lead_a")
	if got := directionFindSession(t, directionMustRead(t, ft.s, "", "conv_lead_a").Sessions, "agent-stopped"); got.State != proto.StateOffline {
		t.Fatalf("a stopped Agent on a connected computer reads its own state: %+v", got)
	}
	if w := archiveRequest(ft.s, "box", "agent-stopped", roleFull, true); w.Code != http.StatusOK {
		t.Fatalf("archive: %d %s", w.Code, w.Body)
	}
	list, err := ft.s.sessions(ctx, "box")
	if err != nil || !slices.ContainsFunc(list, func(c proto.Session) bool { return c.SessionID == "agent-stopped" && c.Archived }) {
		t.Fatalf("precondition: the computer's list keeps the archived row: %v %+v", err, list)
	}
	if got := directionFindSession(t, directionMustRead(t, ft.s, "", "conv_lead_a").Sessions, "agent-stopped"); got.State != "not_listed" {
		t.Fatalf("archived Agent: state %q, want not_listed", got.State)
	}
}

// A stopped session on a connected computer, one on a computer that is not connected and one on a computer that left
// read three different ways (point 3); the last one's entry can still be taken out (point 7).
func TestDirectionStandingTellsStoppedOfflineAndRemovedApart(t *testing.T) {
	ft := directionFilingFleetEnv(t, newFleetMem())
	for _, id := range []string{"box", "laptop", "spare"} {
		directionFleetComputer(t, ft, id, directionStoppedAgent("agent-"+id))
		directionFilingMustPut(t, ft.s, id, "agent-"+id, "conv_lead_a")
	}
	directionFleetAway(ft, "laptop")
	directionFleetRemove(ft, "spare")
	got := directionMustRead(t, ft.s, "", "conv_lead_a")
	want := map[string]string{"agent-box": proto.StateOffline, "agent-laptop": "computer_offline", "agent-spare": "computer_removed"}
	for id, state := range want {
		if row := directionFindSession(t, got.Sessions, id); row.State != state {
			t.Errorf("%s: state %q, want %q", id, row.State, state)
		}
	}
	out := directionFilingMustRemove(t, ft.s, "spare", "agent-spare")
	if directionValue(out.Previous) != "conv_lead_a" || !out.Changed {
		t.Fatalf("taking out the entry of a computer that left: %s", out.Raw)
	}
	if directionBookHas(t, ft.s, "spare", "agent-spare") {
		t.Fatalf("the entry stayed: %s", directionBookSnapshot(t, ft.s))
	}
	if after := directionMustRead(t, ft.s, "", "conv_lead_a"); slices.Contains(directionSessionIDs(after.Sessions), "agent-spare") {
		t.Fatalf("a taken-out entry is still placed: %+v", after.Sessions)
	}
}

// An Agent on a supervised task that is not done, but shelved or seen (the To-dos open count leaves both out), follows
// its task in the read, the write and the tree, and so does a sub-agent session under it (points 4 and 5).
func TestDirectionRuleOneHoldsShelvedAndSeenTasks(t *testing.T) {
	for _, state := range []string{"shelved", "seen"} {
		t.Run(state, func(t *testing.T) {
			e := directionFilingEnv(t, nil)
			ctx := context.Background()
			now := time.Now()
			old := now.Add(-72 * time.Hour)
			agent := proto.Session{MachineID: "box", SessionID: "agent-quiet", Agent: "codex", Status: proto.StatusIdle,
				State: proto.StateIdle, Title: "Quiet agent", UpdatedAt: old}
			if state == "seen" {
				agent.Status, agent.State, agent.UpdatedAt = proto.StatusDone, proto.StateUnread, now.Add(-time.Hour)
			}
			kid := proto.Session{MachineID: "box", SessionID: "agent-quiet-kid", Agent: "codex", ParentSessionID: "agent-quiet",
				Status: proto.StatusWorking, State: proto.StateWorking, Title: "Its sub-agent", UpdatedAt: now}
			e.f.setSessions("box", agent, kid)
			// Both were filed under B before the Agent joined A's task.
			directionFilingMustPut(t, e.s, "box", "agent-quiet", "conv_lead_b")
			directionFilingMustPut(t, e.s, "box", "agent-quiet-kid", "conv_lead_b")
			taskID := e.task("Quiet supervised task", func(todo *Todo) {
				todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
				todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "agent-quiet", Agent: "codex", LinkedAt: old}}
				todo.CreatedAt, todo.UpdatedAt = old, old
				if state == "seen" {
					todo.SeenAt = &now
				}
			})
			e.s.todos.mu.Lock()
			e.s.todos.activeBySession[[2]string{"box", "agent-quiet"}] = taskID
			e.s.todos.mu.Unlock()

			tb, _, _ := e.s.directionTreeParts(ctx, time.Now())
			placed := tb.placed[taskID]
			wantSection := map[string]string{"shelved": triageShelved, "seen": triageDone}[state]
			if tb.isOpen(taskID) || todoNow(e.s, taskID).DoneAt != nil || placed.Section != wantSection {
				t.Fatalf("precondition: a task not done that the open count leaves out (%s): open=%v placed=%+v", state, tb.isOpen(taskID), placed)
			}
			if got := directionMustRead(t, e.s, "", "conv_lead_b"); len(got.Sessions) != 0 {
				t.Fatalf("read: the task's Agent stayed under its old filing: %+v", got.Sessions)
			}
			for _, sid := range []string{"agent-quiet", "agent-quiet-kid"} {
				if out := directionFilingPut(e.s, roleFull, "", "box", sid, ""); out.Status != http.StatusConflict || out.Code != "direction_supervised_task" {
					t.Fatalf("write for %s: %d %s", sid, out.Status, out.Raw)
				}
			}
			tree := e.tree()
			for _, d := range tree.Directions {
				if len(d.Sessions) != 0 {
					t.Fatalf("tree: %s lists sessions of a task's Agent: %+v", d.ID, d.Sessions)
				}
			}
		})
	}
}

// directionTreeCount is how many times the tree names session sid: under its tasks' Agents and in sessions.
func directionTreeCount(tree directionTree, sid string) int {
	n := 0
	var walk func(agents []treeAgent)
	walk = func(agents []treeAgent) {
		for _, a := range agents {
			if a.SessionID == sid {
				n++
			}
			walk(a.Children)
		}
	}
	for _, d := range tree.Directions {
		for _, t := range d.Tasks {
			walk(t.Agents)
		}
		for _, s := range d.Sessions {
			if s.SessionID == sid {
				n++
			}
		}
	}
	return n
}

// A Codex sub-agent session under an Agent of an open supervised task is listed once in the tree, under that Agent,
// even when the owner filed it elsewhere before (point 5).
func TestDirectionTreeListsATaskAgentsSubAgentOnce(t *testing.T) {
	e := directionFilingEnv(t, nil)
	lead := proto.Session{MachineID: "box", SessionID: "agent-lead", Agent: "codex", Status: proto.StatusWorking, State: proto.StateWorking, Title: "Lead"}
	kid := proto.Session{MachineID: "box", SessionID: "agent-kid", Agent: "codex", ParentSessionID: "agent-lead",
		Status: proto.StatusWorking, State: proto.StateWorking, Title: "Sub-agent"}
	e.f.setSessions("box", lead, kid)
	directionFilingMustPut(t, e.s, "box", "agent-kid", "conv_lead_b")
	taskID := e.task("Supervised work", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "agent-lead", Agent: "codex"}}
	})
	e.s.todos.mu.Lock()
	e.s.todos.activeBySession[[2]string{"box", "agent-lead"}] = taskID
	e.s.todos.mu.Unlock()

	tree := e.tree()
	if n := directionTreeCount(tree, "agent-kid"); n != 1 {
		t.Fatalf("the sub-agent session is in the tree %d times: %+v", n, tree.Directions)
	}
	if a := tree.Directions[0]; len(a.Tasks) != 1 || directionTreeCount(directionTree{Directions: []treeDirection{a}}, "agent-kid") != 1 {
		t.Fatalf("the sub-agent session is not under its Agent in A: %+v", a)
	}
	if got := directionMustRead(t, e.s, "", "conv_lead_b"); slices.Contains(directionSessionIDs(got.Sessions), "agent-kid") {
		t.Fatalf("read: the sub-agent session stayed under its old filing: %+v", got.Sessions)
	}
	if out := directionFilingPut(e.s, roleFull, "", "box", "agent-kid", "conv_lead_b"); out.Status != http.StatusConflict || out.Code != "direction_supervised_task" {
		t.Fatalf("write for the sub-agent session: %d %s", out.Status, out.Raw)
	}
}

// Undo of a first filing restores the placement before it exactly: no entry (point 6).
func TestDirectionUndoOfAFirstFilingRestoresNoEntry(t *testing.T) {
	ctx := context.Background()
	t.Run("opened by a leader", func(t *testing.T) {
		e := directionFilingEnv(t, nil)
		e.s.originSet(ctx, "server", "chat-opened", OriginSpawned, "conv_lead_a", "")
		beforeA, beforeB := directionMustRead(t, e.s, "", "conv_lead_a"), directionMustRead(t, e.s, "", "conv_lead_b")
		opened := directionFindSession(t, beforeA.Sessions, "chat-opened")
		filed := directionFilingMustPut(t, e.s, "server", "chat-opened", "conv_lead_b")
		if filed.Previous != nil || !strings.Contains(filed.Raw, `"previous":null`) {
			t.Fatalf("a first filing's previous is null: %s", filed.Raw)
		}
		undo := directionUndo(t, e.s, "server", "chat-opened", filed)
		if undo.Direction != nil || directionValue(undo.Previous) != "conv_lead_b" || !undo.Changed {
			t.Fatalf("undo: %s", undo.Raw)
		}
		afterA, afterB := directionMustRead(t, e.s, "", "conv_lead_a"), directionMustRead(t, e.s, "", "conv_lead_b")
		if restored := directionFindSession(t, afterA.Sessions, "chat-opened"); !directionSameRow(restored, opened) || restored.Source != "leader" {
			t.Fatalf("undo did not restore the leader's placement: before %+v, after %+v", opened, restored)
		}
		if len(afterA.Sessions) != len(beforeA.Sessions) || len(afterB.Sessions) != len(beforeB.Sessions) {
			t.Fatalf("undo changed the counts: A %d→%d, B %d→%d", len(beforeA.Sessions), len(afterA.Sessions), len(beforeB.Sessions), len(afterB.Sessions))
		}
		if directionBookHas(t, e.s, "server", "chat-opened") {
			t.Fatalf("undo left an entry: %s", directionBookSnapshot(t, e.s))
		}
	})
	t.Run("placed nowhere before", func(t *testing.T) {
		e := directionFilingEnv(t, nil)
		filed := directionFilingMustPut(t, e.s, "server", "chat-b", "conv_lead_a")
		if filed.Previous != nil {
			t.Fatalf("previous: %s", filed.Raw)
		}
		directionUndo(t, e.s, "server", "chat-b", filed)
		if directionBookHas(t, e.s, "server", "chat-b") {
			t.Fatalf("undo left an entry: %s", directionBookSnapshot(t, e.s))
		}
		if got := directionMustRead(t, e.s, "", "conv_lead_a"); slices.Contains(directionSessionIDs(got.Sessions), "chat-b") {
			t.Fatalf("undo left the chat filed: %+v", got.Sessions)
		}
	})
	t.Run("the earlier direction ended", func(t *testing.T) {
		e := directionFilingEnv(t, nil)
		e.s.originSet(ctx, "server", "chat-opened", OriginSpawned, "conv_lead_a", "")
		filed := directionFilingMustPut(t, e.s, "server", "chat-opened", "conv_lead_b")
		if code, _ := doRoute(t, e.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_a/end", "", roleFull); code != http.StatusOK {
			t.Fatalf("end: %d", code)
		}
		e.s.directionsSync(ctx)
		directionUndo(t, e.s, "server", "chat-opened", filed)
		ended := directionMustRead(t, e.s, "", "conv_lead_a")
		if row := directionFindSession(t, ended.Sessions, "chat-opened"); ended.EndedAt == nil || row.Source != "leader" {
			t.Fatalf("undo into an ended direction: %+v", ended)
		}
	})
	t.Run("the earlier direction is full", func(t *testing.T) {
		e := directionFilingEnv(t, nil)
		var extra []proto.Session
		for i := range directionSessionsPerDirection {
			extra = append(extra, proto.Session{MachineID: "server", SessionID: fmt.Sprintf("chat-full-%02d", i), Cwd: "/home/u/.persona/assistant",
				Assistant: true, Status: proto.StatusIdle, Title: "Filed"})
		}
		directionFilingSetSessions(e, extra...)
		for _, c := range extra {
			directionFilingMustPut(t, e.s, "server", c.SessionID, "conv_lead_a")
		}
		e.s.originSet(ctx, "server", "chat-opened", OriginSpawned, "conv_lead_a", "")
		filed := directionFilingMustPut(t, e.s, "server", "chat-opened", "conv_lead_b")
		directionUndo(t, e.s, "server", "chat-opened", filed)
		if row := directionFindSession(t, directionMustRead(t, e.s, "", "conv_lead_a").Sessions, "chat-opened"); row.Source != "leader" {
			t.Fatalf("undo into a full direction: %+v", row)
		}
	})
}

// A session its computer no longer lists can still be set on its own and taken out; moving it needs its row (point 7).
func TestDirectionEntryOfANotListedSessionCanBeTakenOut(t *testing.T) {
	e := directionFilingEnv(t, nil)
	e.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "agent-gone", Agent: "claude", Status: proto.StatusIdle, Title: "Gone"})
	directionFilingMustPut(t, e.s, "box", "agent-gone", "conv_lead_a")
	e.f.setSessions("box")
	if out := directionFilingPut(e.s, roleFull, "", "box", "agent-gone", "conv_lead_b"); out.Status != http.StatusNotFound || out.Code != "session_not_found" {
		t.Fatalf("moving a session that is not listed: %d %s", out.Status, out.Raw)
	}
	onOwn := directionFilingMustPut(t, e.s, "box", "agent-gone", "")
	if directionValue(onOwn.Previous) != "conv_lead_a" || !onOwn.Changed {
		t.Fatalf("on its own: %s", onOwn.Raw)
	}
	out := directionFilingMustRemove(t, e.s, "box", "agent-gone")
	if directionValue(out.Previous) != "" || !out.Changed || directionBookHas(t, e.s, "box", "agent-gone") {
		t.Fatalf("taking it out: %s; book %s", out.Raw, directionBookSnapshot(t, e.s))
	}
	if again := directionFilingMustRemove(t, e.s, "box", "agent-gone"); again.Changed || again.Previous != nil {
		t.Fatalf("taking out what is not there: %s", again.Raw)
	}
}

// The limit counts the entries the read places: not a filed chat that became a direction's own leader conversation,
// nor a filed Agent that now follows a supervised task (point 7).
func TestDirectionLimitCountsOnlyWhatTheReadPlaces(t *testing.T) {
	e := directionFilingEnv(t, nil)
	ctx := context.Background()
	var extra []proto.Session
	for i := range directionSessionsPerDirection + 2 {
		extra = append(extra, proto.Session{MachineID: "server", SessionID: fmt.Sprintf("chat-limit-%02d", i), Cwd: "/home/u/.persona/assistant",
			Assistant: true, Status: proto.StatusIdle, Title: "Filed"})
	}
	directionFilingSetSessions(e, extra...)
	e.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "agent-limit", Agent: "claude", Status: proto.StatusIdle, Title: "Agent"})
	directionFilingMustPut(t, e.s, "box", "agent-limit", "conv_lead_a")
	for _, c := range extra[:directionSessionsPerDirection-1] {
		directionFilingMustPut(t, e.s, "server", c.SessionID, "conv_lead_a")
	}
	next := extra[directionSessionsPerDirection-1].SessionID
	if out := directionFilingPut(e.s, roleFull, "", "server", next, "conv_lead_a"); out.Status != http.StatusConflict || out.Code != "direction_full" {
		t.Fatalf("51st: %d %s", out.Status, out.Raw)
	}
	setSettings(t, e.s, func(v *Settings) { v.Leaders = append(v.Leaders, "chat-limit-00") })
	e.s.directionsSync(ctx)
	taskID := e.task("B's supervised task", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_b"}
		todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "agent-limit", Agent: "claude"}}
	})
	e.s.todos.mu.Lock()
	e.s.todos.activeBySession[[2]string{"box", "agent-limit"}] = taskID
	e.s.todos.mu.Unlock()
	if got := directionMustRead(t, e.s, "", "conv_lead_a"); len(got.Sessions) != directionSessionsPerDirection-2 {
		t.Fatalf("A places %d sessions, want %d: %+v", len(got.Sessions), directionSessionsPerDirection-2, got.Sessions)
	}
	for _, c := range extra[directionSessionsPerDirection-1 : directionSessionsPerDirection+1] {
		directionFilingMustPut(t, e.s, "server", c.SessionID, "conv_lead_a")
	}
	last := extra[directionSessionsPerDirection+1].SessionID
	if out := directionFilingPut(e.s, roleFull, "", "server", last, "conv_lead_a"); out.Status != http.StatusConflict || out.Code != "direction_full" {
		t.Fatalf("A full again: %d %s", out.Status, out.Raw)
	}
}

// An entry made before its computer's id was absorbed by another is the session's one entry: a move replaces it and
// takes effect, and previous names it (point 8).
func TestDirectionEntryUnderAnAbsorbedComputerIDIsReplaced(t *testing.T) {
	ctx := context.Background()
	st := newFleetMem()
	filedAt := time.Now().Add(-time.Hour).UTC()
	if err := st.PutSetting(ctx, directionSessionsSettingKey, map[string]map[string]any{
		runtimeListID("a-mac", "chat-a"): {"direction": "conv_lead_a", "by": titleByUser, "at": filedAt, "title": "Original chat title"},
	}); err != nil {
		t.Fatal(err)
	}
	ft := directionFilingFleetEnv(t, st) // "a-mac" is an id "mac" absorbed
	if got := directionMustRead(t, ft.s, "", "conv_lead_a"); !slices.Contains(directionSessionIDs(got.Sessions), "chat-a") {
		t.Fatalf("precondition: the entry under the absorbed id places the chat: %+v", got.Sessions)
	}
	moved := directionFilingMustPut(t, ft.s, "mac", "chat-a", "conv_lead_b")
	if directionValue(moved.Previous) != "conv_lead_a" || !moved.Changed {
		t.Fatalf("move: %s", moved.Raw)
	}
	if got := directionMustRead(t, ft.s, "", "conv_lead_a"); slices.Contains(directionSessionIDs(got.Sessions), "chat-a") {
		t.Fatalf("the old entry still places the chat under A: %+v", got.Sessions)
	}
	if got := directionMustRead(t, ft.s, "", "conv_lead_b"); !slices.Contains(directionSessionIDs(got.Sessions), "chat-a") {
		t.Fatalf("the move did not take effect: %+v", got.Sessions)
	}
	var book map[string]json.RawMessage
	if _, err := st.Setting(ctx, directionSessionsSettingKey, &book); err != nil {
		t.Fatal(err)
	}
	if _, ok := book[runtimeListID("mac", "chat-a")]; len(book) != 1 || !ok {
		t.Fatalf("the session has more than its one canonical entry: %v", book)
	}
}

// A state change of a conversation a direction's leader opened, on a computer with no task Agent and no filing,
// rebuilds the tree and tells the apps (point 9).
func TestDirectionTreeLeaderOpenedStateChangePublishes(t *testing.T) {
	e := directionFilingEnv(t, nil)
	ctx := context.Background()
	e.s.originSet(ctx, "server", "chat-opened", OriginSpawned, "conv_lead_a", "")
	e.clock.advance(time.Minute)
	e.s.directionTreeSync(ctx)
	first := e.directionsEvents()

	e.f.mu.Lock()
	for i := range e.f.sessions["server"] {
		if e.f.sessions["server"][i].SessionID == "chat-opened" {
			e.f.sessions["server"][i].Status, e.f.sessions["server"][i].State = proto.StatusWorking, proto.StateWorking
		}
	}
	changed := slices.Clone(e.f.sessions["server"])
	e.f.mu.Unlock()
	e.s.SessionsChanged(ctx, DeviceSessions{MachineID: "server", Sessions: changed})
	e.s.dtree.mu.Lock()
	armed := e.s.dtree.armed
	e.s.dtree.mu.Unlock()
	if !armed {
		t.Fatal("a leader-opened conversation's state change did not schedule a tree rebuild")
	}
	e.clock.advance(directionTreeSettle)
	if got := e.directionsEvents(); got != first+1 {
		t.Fatalf("directions events: got %d, want %d", got, first+1)
	}
}

// A leader-opened conversation deleted on another of the manager's computers is left out (point 10).
func TestDirectionReadLeavesOutAConversationDeletedOnAnotherComputer(t *testing.T) {
	e := directionFilingEnv(t, nil)
	ctx := context.Background()
	e.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "chat-remote", Cwd: "/home/u/.persona/assistant", Assistant: true,
		Status: proto.StatusIdle, State: proto.StateIdle, Title: "Remote chat"})
	e.s.originSet(ctx, "box", "chat-remote", OriginSpawned, "conv_lead_a", "")
	if got := directionMustRead(t, e.s, "", "conv_lead_a"); !slices.Contains(directionSessionIDs(got.Sessions), "chat-remote") {
		t.Fatalf("precondition: the leader-opened chat is placed: %+v", got.Sessions)
	}
	if code, body := doRoute(t, e.s, http.MethodDelete, "/api/v1/assistant/conversations/chat-remote", `{"machine_id":"box"}`, roleFull); code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, body)
	}
	if got := directionMustRead(t, e.s, "", "conv_lead_a"); slices.Contains(directionSessionIDs(got.Sessions), "chat-remote") {
		t.Fatalf("a conversation deleted on another computer is still placed: %+v", got.Sessions)
	}
}

// directionCountingStore keeps conversation deletions as the database does (the deletion record's one settings value,
// read by key) and counts which conversations are looked up one by one; failing makes the deletion record unreadable.
type directionCountingStore struct {
	*memStore
	mu      sync.Mutex
	looked  []string
	failing bool
}

func (st *directionCountingStore) fail(on bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.failing = on
}

func (st *directionCountingStore) lookups() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return slices.Clone(st.looked)
}

func (st *directionCountingStore) reset() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.looked = nil
}

var errDirectionDeletionRecord = errors.New("synthetic deletion record failure")

func (st *directionCountingStore) Setting(ctx context.Context, key string, v any) (bool, error) {
	st.mu.Lock()
	failing := st.failing
	st.mu.Unlock()
	if failing && key == deletedConversationsKey {
		return false, errDirectionDeletionRecord
	}
	return st.memStore.Setting(ctx, key, v)
}

func (st *directionCountingStore) ledger(ctx context.Context) map[string]json.RawMessage {
	rows := map[string]json.RawMessage{}
	_, _ = st.memStore.Setting(ctx, deletedConversationsKey, &rows)
	return rows
}

func (st *directionCountingStore) PutConversationDeletion(ctx context.Context, machine, session string, row json.RawMessage) error {
	rows := st.ledger(ctx)
	rows[runtimeListID(machine, session)] = row
	return st.memStore.PutSetting(ctx, deletedConversationsKey, rows)
}

func (st *directionCountingStore) ConversationDeletion(ctx context.Context, machine, session string) (json.RawMessage, bool, error) {
	st.mu.Lock()
	st.looked = append(st.looked, session)
	failing := st.failing
	st.mu.Unlock()
	if failing {
		return nil, false, errDirectionDeletionRecord
	}
	raw, ok := st.ledger(ctx)[runtimeListID(machine, session)]
	return raw, ok, nil
}

func (st *directionCountingStore) ConversationDeletedID(ctx context.Context, session string) (bool, error) {
	for _, raw := range st.ledger(ctx) {
		var row deletedConversation
		if json.Unmarshal(raw, &row) == nil && row.ID == session {
			return true, nil
		}
	}
	return false, nil
}

// One page of one direction looks up nothing per conversation of another direction, however many it has (point 11).
func TestDirectionReadAsksNothingPerConversationOfOtherDirections(t *testing.T) {
	ctx := context.Background()
	read := func(others int) (otherLookups, lookups int) {
		st := &directionCountingStore{memStore: &memStore{}}
		e := directionFilingEnv(t, st)
		e.s.originSet(ctx, "server", "chat-opened", OriginSpawned, "conv_lead_a", "")
		var extra []proto.Session
		for i := range others {
			id := fmt.Sprintf("chat-of-b-%02d", i)
			extra = append(extra, proto.Session{MachineID: "server", SessionID: id, Cwd: "/home/u/.persona/assistant", Assistant: true,
				Status: proto.StatusIdle, Title: "B's chat"})
			e.s.originSet(ctx, "server", id, OriginSpawned, "conv_lead_b", "")
		}
		directionFilingSetSessions(e, extra...)
		st.reset()
		if got := directionMustRead(t, e.s, "", "conv_lead_a"); !slices.Contains(directionSessionIDs(got.Sessions), "chat-opened") {
			t.Fatalf("A's read lost its own chat: %+v", got.Sessions)
		}
		looked := st.lookups()
		return len(slices.DeleteFunc(slices.Clone(looked), func(id string) bool { return !strings.HasPrefix(id, "chat-of-b-") })), len(looked)
	}
	b1, n1 := read(1)
	b30, n30 := read(30)
	if b1 != 0 || b30 != 0 {
		t.Fatalf("A's read looked up B's conversations: %d with 1, %d with 30", b1, b30)
	}
	if n1 != n30 {
		t.Fatalf("A's lookups grow with B's conversations: %d with 1, %d with 30", n1, n30)
	}
}

// When the deletion record cannot be read, the read still answers with its ended tasks and says its sessions could
// not be checked; it never answers 503 for that (point 11).
func TestDirectionReadKeepsEndedTasksWhenSessionsCannotBeChecked(t *testing.T) {
	ctx := context.Background()
	st := &directionCountingStore{memStore: &memStore{}}
	e := directionFilingEnv(t, st)
	e.s.originSet(ctx, "server", "chat-opened", OriginSpawned, "conv_lead_a", "")
	now := time.Now().UTC()
	taskID := e.task("Ended under A", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		todo.DoneAt = &now
	})
	st.fail(true)
	code, got, raw := directionReadGet(t, e.s, "", "/api/v1/assistant/directions/conv_lead_a")
	if code != http.StatusOK {
		t.Fatalf("the read failed for its sessions: %d %s", code, raw)
	}
	if len(got.EndedTasks) != 1 || got.EndedTasks[0].ID != taskID || !got.SessionsUnavailable || len(got.Sessions) != 0 {
		t.Fatalf("read with sessions unavailable: %s", raw)
	}
	st.fail(false)
	again := directionMustRead(t, e.s, "", "conv_lead_a")
	if again.SessionsUnavailable || !slices.Contains(directionSessionIDs(again.Sessions), "chat-opened") {
		t.Fatalf("read once the record is readable: %+v", again)
	}
}

// A device without full access is refused with a sentence of the filing's own, in each language (point 13).
func TestDirectionFilingRefusesOtherDevicesWithItsOwnSentence(t *testing.T) {
	e := directionFilingEnv(t, nil)
	sentences := map[string]bool{}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		out := directionFilingPut(e.s, "answer", string(lang), "server", "chat-a", "conv_lead_a")
		if out.Status != http.StatusForbidden || out.Code != "full_access_required" {
			t.Fatalf("%s: %d %s", lang, out.Status, out.Raw)
		}
		if out.Error == "" || out.Error == i18n.T(lang, "assistant-model_pick.err_role") {
			t.Fatalf("%s: the refusal borrows another feature's sentence: %q", lang, out.Error)
		}
		sentences[out.Error] = true
	}
	if len(sentences) != 3 {
		t.Fatalf("the refusal is not localized in en, zh-Hans and ja: %v", sentences)
	}
}

// The read's own refusals keep their codes (point 12): an unknown direction, and a cursor that names none of its ended
// tasks. The write's body is a direction's id, "" or null, nothing else.
func TestDirectionReadAndWriteRefusalCodes(t *testing.T) {
	e := directionFilingEnv(t, nil)
	now := time.Now().UTC()
	e.task("Ended under A", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		todo.DoneAt = &now
	})
	for path, want := range map[string]struct {
		status int
		code   string
	}{
		"/api/v1/assistant/directions/direction-missing":               {http.StatusNotFound, "direction_not_found"},
		"/api/v1/assistant/directions/conv_lead_a?before=todo_missing": {http.StatusConflict, "ended_task_not_found"},
	} {
		code, _, raw := directionReadGet(t, e.s, "", path)
		var body struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal([]byte(raw), &body)
		if code != want.status || body.Code != want.code || body.Error == "" {
			t.Errorf("%s: %d %s, want %d %s", path, code, raw, want.status, want.code)
		}
	}
	before := directionBookSnapshot(t, e.s)
	if out := directionFilingPutBody(e.s, roleFull, "", "server", "chat-a", `{"direction":7}`); out.Status != http.StatusBadRequest || out.Code != "bad_request" {
		t.Fatalf("a direction that is not a string: %d %s", out.Status, out.Raw)
	}
	if got := directionBookSnapshot(t, e.s); got != before {
		t.Fatalf("a bad body changed the book: %s", got)
	}
}

// An ended direction recorded on a computer id another computer absorbed since: its leader conversation is still never
// filed or placed.
func TestDirectionLeaderRecordedUnderAnAbsorbedComputerIDStaysSpecial(t *testing.T) {
	ft := directionFilingFleetEnv(t, newFleetMem())
	ctx := context.Background()
	convs := []proto.Session{{MachineID: "mac", SessionID: "conv_old_lead", Cwd: "/home/u/.persona/assistant", Assistant: true,
		Status: proto.StatusIdle, Title: "An ended direction"}}
	ft.d.mu.Lock()
	convs = append(slices.Clone(ft.d.machines["mac"].sessions), convs...)
	ft.d.mu.Unlock()
	ft.sessions("mac", convs...)
	directionQueueDone(t, ft.s)
	ended := time.Now().Add(-time.Hour).UTC()
	b := &ft.s.directions
	b.mu.Lock()
	if !ft.s.directionsLocked(ctx) {
		b.mu.Unlock()
		t.Fatal("directions unavailable")
	}
	b.m["conv_old_lead"] = &direction{MachineID: "a-mac", By: "user", At: ended.Add(-time.Hour), EndedAt: &ended}
	b.mu.Unlock()
	if out := directionFilingPut(ft.s, roleFull, "", "mac", "conv_old_lead", "conv_lead_a"); out.Status != http.StatusConflict || out.Code != "direction_leader_conversation" {
		t.Fatalf("filing an ended direction's leader conversation: %d %s", out.Status, out.Raw)
	}
}

// directionFilingPutHooked is the write with a context the fake hub's afterList hooks answer to (listHooked): a hook
// armed for its computer runs after each of the write's own lists of it, never after another call's.
func directionFilingPutHooked(s *Service, machine, session, direction string) directionFilingAnswer {
	body, _ := json.Marshal(map[string]string{"direction": direction})
	r := httptest.NewRequest(http.MethodPut, "/api/v1/assistant/sessions/"+machine+"/"+session+"/direction", strings.NewReader(string(body)))
	r = r.WithContext(listHooked(r.Context()))
	r.Header.Set(RoleHeader, roleFull)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var out directionFilingAnswer
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	out.Status, out.Raw = w.Code, w.Body.String()
	return out
}

// directionArmAfterList runs hook after the next hooked list of machine, again for each of the following ones while it
// answers true.
func directionArmAfterList(f *fakeHub, machine string, hook func() bool) {
	var again func()
	again = func() {
		if hook() {
			f.mu.Lock()
			f.afterList[machine] = again
			f.mu.Unlock()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.afterList == nil {
		f.afterList = map[string]func(){}
	}
	f.afterList[machine] = again
}

// Filings that land while a write asks the computers never make it fail: it decides and saves under the book's lock
// from the book as it is then, and asks no computer under that lock (#11490 point 3).
func TestDirectionFilingSucceedsWhileOtherFilingsLand(t *testing.T) {
	e := directionFilingEnv(t, nil)
	var extra []proto.Session
	for i := range 8 {
		extra = append(extra, proto.Session{MachineID: "server", SessionID: fmt.Sprintf("chat-meanwhile-%d", i), Cwd: "/home/u/.persona/assistant",
			Assistant: true, Status: proto.StatusIdle, Title: "Filed meanwhile"})
	}
	directionFilingSetSessions(e, extra...)
	directionFilingMustPut(t, e.s, "server", "chat-b", "conv_lead_a") // A has a filing: its count lists the computer
	landed := 0
	directionArmAfterList(e.f, "server", func() bool {
		directionFilingMustPut(t, e.s, "server", extra[landed].SessionID, "conv_lead_b")
		landed++
		return landed < len(extra)
	})
	if out := directionFilingPutHooked(e.s, "server", "chat-a", "conv_lead_a"); out.Status != http.StatusOK || !out.Changed {
		t.Fatalf("a filing that fits, with %d others landing while it asked the computer: %d %s", landed, out.Status, out.Raw)
	}
	if landed < 2 {
		t.Fatalf("precondition: other filings landed while it asked the computer (%d)", landed)
	}
	for _, id := range []string{"chat-a", "chat-b"} {
		if !directionBookHas(t, e.s, "server", id) {
			t.Fatalf("%s is not filed: %s", id, directionBookSnapshot(t, e.s))
		}
	}
}

// Several filings sent together, each fitting, all succeed (#11490 point 3).
func TestDirectionFilingsSentTogetherAllSucceed(t *testing.T) {
	e := directionFilingEnv(t, nil)
	var extra []proto.Session
	for i := range 16 {
		extra = append(extra, proto.Session{MachineID: "server", SessionID: fmt.Sprintf("chat-together-%02d", i), Cwd: "/home/u/.persona/assistant",
			Assistant: true, Status: proto.StatusIdle, Title: "Filed together"})
	}
	directionFilingSetSessions(e, extra...)
	results := make([]directionFilingAnswer, len(extra))
	var wg sync.WaitGroup
	for i, c := range extra {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = directionFilingPut(e.s, roleFull, "", "server", c.SessionID, cond(i%2 == 0, "conv_lead_a", "conv_lead_b"))
		}()
	}
	wg.Wait()
	for i, out := range results {
		if out.Status != http.StatusOK || !out.Changed {
			t.Fatalf("%s: %d %s", extra[i].SessionID, out.Status, out.Raw)
		}
	}
	if a, b := directionMustRead(t, e.s, "", "conv_lead_a"), directionMustRead(t, e.s, "", "conv_lead_b"); len(a.Sessions)+len(b.Sessions) != len(extra) {
		t.Fatalf("filed %d of %d", len(a.Sessions)+len(b.Sessions), len(extra))
	}
}

// A tree built while what places sessions cannot be read keeps the sessions told last, instead of telling the apps
// every direction lost them.
func TestDirectionTreeKeepsItsSessionsWhenTheyCannotBePlaced(t *testing.T) {
	ctx := context.Background()
	st := &directionCountingStore{memStore: &memStore{}}
	e := directionFilingEnv(t, st)
	e.s.originSet(ctx, "server", "chat-opened", OriginSpawned, "conv_lead_a", "")
	e.f.mu.Lock()
	for i := range e.f.sessions["server"] {
		if e.f.sessions["server"][i].SessionID == "chat-opened" {
			e.f.sessions["server"][i].Status, e.f.sessions["server"][i].State = proto.StatusWorking, proto.StateWorking
		}
	}
	e.f.mu.Unlock()
	e.clock.advance(time.Minute)
	e.s.directionTreeSync(ctx)
	told := e.directionsEvents()
	if got := e.tree().Directions[0]; !slices.Contains(directionSessionViews(got.Sessions), "chat-opened") {
		t.Fatalf("precondition: the working leader-opened chat is in A's sessions: %+v", got.Sessions)
	}
	st.fail(true)
	if got := e.tree().Directions[0]; !slices.Contains(directionSessionViews(got.Sessions), "chat-opened") {
		t.Fatalf("a tree that could not place sessions dropped them: %+v", got.Sessions)
	}
	e.s.directionTreeSync(ctx)
	if got := e.directionsEvents(); got != told {
		t.Fatalf("the apps were told a changed tree: %d events, want %d", got, told)
	}
}

func directionSessionViews(list []directionSession) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.SessionID)
	}
	return out
}

// directionFleetShown: computer id's node is away while the devices still show it as before until until (its
// reconnect grace, or the warm start after a restart): online and reachable, its last list, no further word.
func directionFleetShown(ft *fleetTest, id string, until time.Time) {
	ft.d.mu.Lock()
	defer ft.d.mu.Unlock()
	ft.d.machines[id].shownUntil = until
}

// directionFleetGone: the window ended with its node still away: the devices show it offline and tell the manager
// nothing.
func directionFleetGone(ft *fleetTest, id string) {
	ft.d.mu.Lock()
	defer ft.d.mu.Unlock()
	ft.d.machines[id].away, ft.d.machines[id].shownUntil = true, time.Time{}
}

// The tree is built again when a computer stops being shown as before, with nothing telling the manager: at the end
// of the reconnect grace (the devices told it at the start, while their summary still said online) and at the end of
// the warm start after a restart (they tell it nothing at all). Point 1 of the review of #11463.
func TestDirectionTreeRebuiltWhenAComputerStopsBeingShownAsBefore(t *testing.T) {
	for _, tc := range []struct {
		name   string
		detach bool
	}{{"reconnect grace", true}, {"warm start", false}} {
		t.Run(tc.name, func(t *testing.T) {
			ft := directionFilingFleetEnv(t, newFleetMem())
			clock := newFakeClock()
			ft.s.dtree.mu.Lock()
			ft.s.dtree.after = clock.After
			ft.s.dtree.mu.Unlock()
			ctx := context.Background()
			directionFleetComputer(t, ft, "laptop", proto.Session{SessionID: "agent-laptop", Agent: "claude", Cwd: "/home/u/code",
				Status: proto.StatusWorking, Title: "Laptop agent"})
			directionFilingMustPut(t, ft.s, "laptop", "agent-laptop", "conv_lead_a")
			directionFleetShown(ft, "laptop", time.Now().Add(30*time.Second))
			if tc.detach {
				ft.s.NodeDetached("laptop")
			}
			clock.advance(directionTreeSettle)
			ft.s.directionTreeSync(ctx)
			told := directionHubEvents(ft.f, "directions")
			if tree, _ := directionFilingTreeGet(t, ft.s); !slices.Contains(directionSessionIDs(tree[0].Sessions), "agent-laptop") {
				t.Fatalf("precondition: while shown as before the Agent keeps its last state: %+v", tree[0])
			}

			directionFleetGone(ft, "laptop")
			clock.advance(31 * time.Second)
			if got := directionHubEvents(ft.f, "directions"); got != told+1 {
				t.Fatalf("the end of the window told the apps %d times, want once", got-told)
			}
			if tree, raw := directionFilingTreeGet(t, ft.s); strings.Contains(raw, "agent-laptop") {
				t.Fatalf("the tree still lists the Agent of a computer no longer shown: %+v", tree[0])
			}
			if got := directionFindSession(t, directionMustRead(t, ft.s, "", "conv_lead_a").Sessions, "agent-laptop"); got.State != "computer_offline" {
				t.Fatalf("state %q, want computer_offline", got.State)
			}
		})
	}
}

// A connected computer that has not given its list on this connection (a cold start, or its list call failed) has a
// list that is not known, not an empty one (point 3 of the review of #11463).
func TestDirectionStandingListUnknownUntilTheComputerGivesItsList(t *testing.T) {
	ft := directionFilingFleetEnv(t, newFleetMem())
	directionFleetComputer(t, ft, "box", directionStoppedAgent("agent-box"))
	directionFilingMustPut(t, ft.s, "box", "agent-box", "conv_lead_a")
	ft.d.mu.Lock()
	ft.d.machines["box"].unlisted, ft.d.machines["box"].sessions = true, nil
	ft.d.mu.Unlock()
	if got := directionFindSession(t, directionMustRead(t, ft.s, "", "conv_lead_a").Sessions, "agent-box"); got.State != "list_unknown" {
		t.Fatalf("a computer with no list yet: state %q, want list_unknown", got.State)
	}
	ft.d.mu.Lock()
	ft.d.machines["box"].unlisted = false
	ft.d.mu.Unlock()
	ft.sessions("box") // its list arrives, without the Agent
	directionQueueDone(t, ft.s)
	if got := directionFindSession(t, directionMustRead(t, ft.s, "", "conv_lead_a").Sessions, "agent-box"); got.State != "not_listed" {
		t.Fatalf("a list without the session: state %q, want not_listed", got.State)
	}
}

// Undo works right after the write it undoes: it puts back exactly the entry that write replaced or took out, without
// the session's row and into an ended or full direction (points 4 and 5 of the review of #11463).
func TestDirectionUndoRightAfterItsWrite(t *testing.T) {
	ctx := context.Background()
	notListed := func(t *testing.T, write string) {
		e := directionFilingEnv(t, nil)
		e.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "agent-gone", Agent: "claude", Status: proto.StatusIdle, Title: "Filed title"})
		directionFilingMustPut(t, e.s, "box", "agent-gone", "conv_lead_a")
		filed := directionFindSession(t, directionMustRead(t, e.s, "", "conv_lead_a").Sessions, "agent-gone")
		e.f.setSessions("box")
		out := directionFilingPutBody(e.s, roleFull, "", "box", "agent-gone", write)
		if out.Status != http.StatusOK || directionValue(out.Previous) != "conv_lead_a" {
			t.Fatalf("write %s: %d %s", write, out.Status, out.Raw)
		}
		undo := directionFilingPut(e.s, roleFull, "", "box", "agent-gone", *out.Previous)
		if undo.Status != http.StatusOK || directionValue(undo.Direction) != "conv_lead_a" || !undo.Changed {
			t.Fatalf("undo: %d %s", undo.Status, undo.Raw)
		}
		back := directionFindSession(t, directionMustRead(t, e.s, "", "conv_lead_a").Sessions, "agent-gone")
		filed.State = "not_listed"
		if !directionSameRow(back, filed) {
			t.Fatalf("undo did not put back the entry as it was: before %+v, after %+v", filed, back)
		}
		if moved := directionFilingPut(e.s, roleFull, "", "box", "agent-gone", "conv_lead_b"); moved.Status != http.StatusNotFound {
			t.Fatalf("a write that is no Undo still needs the row: %d %s", moved.Status, moved.Raw)
		}
	}
	t.Run("on its own, not listed", func(t *testing.T) { notListed(t, `{"direction":""}`) })
	t.Run("taken out, not listed", func(t *testing.T) { notListed(t, `{"direction":null}`) })
	t.Run("taken out, its computer left", func(t *testing.T) {
		ft := directionFilingFleetEnv(t, newFleetMem())
		directionFleetComputer(t, ft, "spare", directionStoppedAgent("agent-spare"))
		directionFilingMustPut(t, ft.s, "spare", "agent-spare", "conv_lead_a")
		directionFleetRemove(ft, "spare")
		out := directionFilingMustRemove(t, ft.s, "spare", "agent-spare")
		if undo := directionFilingPut(ft.s, roleFull, "", "spare", "agent-spare", *out.Previous); undo.Status != http.StatusOK {
			t.Fatalf("undo: %d %s", undo.Status, undo.Raw)
		}
		if got := directionFindSession(t, directionMustRead(t, ft.s, "", "conv_lead_a").Sessions, "agent-spare"); got.State != "computer_removed" {
			t.Fatalf("restored filing: %+v", got)
		}
	})
	t.Run("into an ended direction", func(t *testing.T) {
		e := directionFilingEnv(t, nil)
		directionFilingMustPut(t, e.s, "server", "chat-b", "conv_lead_a")
		filed := directionFindSession(t, directionMustRead(t, e.s, "", "conv_lead_a").Sessions, "chat-b")
		moved := directionFilingMustPut(t, e.s, "server", "chat-b", "conv_lead_b")
		if code, _ := doRoute(t, e.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_a/end", "", roleFull); code != http.StatusOK {
			t.Fatalf("end: %d", code)
		}
		e.s.directionsSync(ctx)
		undo := directionFilingPut(e.s, roleFull, "", "server", "chat-b", *moved.Previous)
		if undo.Status != http.StatusOK || directionValue(undo.Previous) != "conv_lead_b" {
			t.Fatalf("undo into an ended direction: %d %s", undo.Status, undo.Raw)
		}
		ended := directionMustRead(t, e.s, "", "conv_lead_a")
		if back := directionFindSession(t, ended.Sessions, "chat-b"); ended.EndedAt == nil || !directionSameRow(back, filed) {
			t.Fatalf("undo did not put the entry back as it was: before %+v, after %+v", filed, back)
		}
		if again := directionFilingPut(e.s, roleFull, "", "server", "chat-a", "conv_lead_a"); again.Status != http.StatusConflict || again.Code != "direction_ended" {
			t.Fatalf("a filing that is no Undo into the ended direction: %d %s", again.Status, again.Raw)
		}
	})
	t.Run("into a full direction", func(t *testing.T) {
		e := directionFilingEnv(t, nil)
		var extra []proto.Session
		for i := range directionSessionsPerDirection + 1 {
			extra = append(extra, proto.Session{MachineID: "server", SessionID: fmt.Sprintf("chat-full-%02d", i), Cwd: "/home/u/.persona/assistant",
				Assistant: true, Status: proto.StatusIdle, Title: "Filed"})
		}
		directionFilingSetSessions(e, extra...)
		for _, c := range extra[:directionSessionsPerDirection] {
			directionFilingMustPut(t, e.s, "server", c.SessionID, "conv_lead_a")
		}
		moved := directionFilingMustPut(t, e.s, "server", "chat-full-00", "conv_lead_b")
		directionFilingMustPut(t, e.s, "server", extra[directionSessionsPerDirection].SessionID, "conv_lead_a") // A is full again
		if undo := directionFilingPut(e.s, roleFull, "", "server", "chat-full-00", *moved.Previous); undo.Status != http.StatusOK {
			t.Fatalf("undo into a full direction: %d %s", undo.Status, undo.Raw)
		}
		if got := directionMustRead(t, e.s, "", "conv_lead_a"); len(got.Sessions) != directionSessionsPerDirection+1 {
			t.Fatalf("A holds %d after the Undo, want %d", len(got.Sessions), directionSessionsPerDirection+1)
		}
		if out := directionFilingPut(e.s, roleFull, "", "server", "chat-a", "conv_lead_a"); out.Status != http.StatusConflict || out.Code != "direction_full" {
			t.Fatalf("a new filing into A: %d %s", out.Status, out.Raw)
		}
	})
}

// A removal marker past its seven days is no Undo any more, also while nothing has been written since to drop it; one
// within them still is (#11490 point 1).
func TestDirectionRemovalMarkerPastItsWeekIsNoUndo(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		age  time.Duration
		want int
	}{{"past its week", directionRemovedKeep + time.Hour, http.StatusNotFound}, {"within it", directionRemovedKeep - time.Hour, http.StatusOK}} {
		t.Run(tc.name, func(t *testing.T) {
			e := directionFilingEnv(t, nil)
			removed := time.Now().Add(-tc.age).UTC()
			filed := removed.Add(-time.Hour)
			if err := e.s.store().PutSetting(ctx, directionSessionsSettingKey, map[string]map[string]any{
				runtimeListID("box", "agent-gone"): {"direction": "conv_lead_a", "by": titleByUser, "at": filed, "title": "Filed title", "removed_at": removed},
			}); err != nil {
				t.Fatal(err)
			}
			e.s.directionFilings.mu.Lock()
			e.s.directionFilings.loaded = false // read the book as stored
			e.s.directionFilings.mu.Unlock()
			e.f.setSessions("box") // its computer does not list it: only an Undo needs no row
			if out := directionFilingPut(e.s, roleFull, "", "box", "agent-gone", "conv_lead_a"); out.Status != tc.want {
				t.Fatalf("a write naming the marker's direction: %d %s, want %d", out.Status, out.Raw, tc.want)
			}
		})
	}
}

// A write decided as the Undo from the book its handler read first is decided again from the book as it is when it
// is saved: a write to the same session in between makes it an ordinary move, which the ended direction refuses
// (#11490 point 2).
func TestDirectionUndoDecidedFromTheBookItSaves(t *testing.T) {
	e := directionFilingEnv(t, nil)
	ctx := context.Background()
	directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_a")
	moved := directionFilingMustPut(t, e.s, "server", "chat-a", "conv_lead_b")
	if code, _ := doRoute(t, e.s, http.MethodPost, "/api/v1/assistant/directions/conv_lead_a/end", "", roleFull); code != http.StatusOK {
		t.Fatalf("end: %d", code)
	}
	e.s.directionsSync(ctx)
	directionArmAfterList(e.f, "server", func() bool {
		directionFilingMustPut(t, e.s, "server", "chat-a", "") // another device sets it on its own meanwhile
		return false
	})
	undo := directionFilingPutHooked(e.s, "server", "chat-a", *moved.Previous)
	if undo.Status != http.StatusConflict || undo.Code != "direction_ended" {
		t.Fatalf("the Undo of a write that is no longer the last one: %d %s", undo.Status, undo.Raw)
	}
	if got := directionMustRead(t, e.s, "", "conv_lead_a"); slices.Contains(directionSessionIDs(got.Sessions), "chat-a") {
		t.Fatalf("the ended direction got the chat back: %+v", got.Sessions)
	}
}

// A tree built while what places sessions cannot be read lists a session that joined a task since only under that
// task, not again in its old direction's sessions (#11490 point 4).
func TestDirectionTreeFallbackLeavesOutASessionNowUnderATask(t *testing.T) {
	ctx := context.Background()
	st := &directionCountingStore{memStore: &memStore{}}
	e := directionFilingEnv(t, st)
	e.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "agent-joins", Agent: "claude", Status: proto.StatusWorking,
		State: proto.StateWorking, Title: "Working agent"})
	directionFilingMustPut(t, e.s, "box", "agent-joins", "conv_lead_a")
	e.clock.advance(time.Minute)
	e.s.directionTreeSync(ctx)
	if got := e.tree().Directions[0]; !slices.Contains(directionSessionViews(got.Sessions), "agent-joins") {
		t.Fatalf("precondition: the working filed Agent is in A's sessions: %+v", got.Sessions)
	}
	taskID := e.task("B's supervised task", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_b"}
		todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "agent-joins", Agent: "claude"}}
	})
	e.s.todos.mu.Lock()
	e.s.todos.activeBySession[[2]string{"box", "agent-joins"}] = taskID
	e.s.todos.mu.Unlock()
	st.fail(true)
	tree := e.tree()
	if n := directionTreeCount(tree, "agent-joins"); n != 1 {
		t.Fatalf("the Agent is in the tree %d times: %+v", n, tree.Directions)
	}
	if slices.Contains(directionSessionViews(tree.Directions[0].Sessions), "agent-joins") {
		t.Fatalf("the fallback kept the Agent in its old direction's sessions: %+v", tree.Directions[0].Sessions)
	}
}
