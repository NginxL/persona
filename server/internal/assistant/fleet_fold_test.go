package assistant

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func foldRow(id, state string, set func(*proto.Session)) proto.Session {
	s := proto.Session{SessionID: id, State: state}
	if set != nil {
		set(&s)
	}
	return s
}

func foldOf(list []proto.Session) map[string]string {
	f := foldSessions(list, nil)
	f.apply(list)
	out := map[string]string{}
	for i := range list {
		if u := list[i].FoldedUnder(); u != "" {
			out[list[i].SessionID] = u
		}
	}
	return out
}

func rowNamed(list []proto.Session, id string) *proto.Session {
	for i := range list {
		if list[i].SessionID == id {
			return &list[i]
		}
	}
	return nil
}

// Every child folds under its topmost listed ancestor, however deep, and that ancestor alone carries the rollup of all
// of them by their own states (#5120).
func TestFoldSessionsTwoLevels(t *testing.T) {
	list := []proto.Session{
		foldRow("lead", proto.StateIdle, nil),
		foldRow("mid", proto.StateWorking, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "lead" }),
		foldRow("leaf", proto.StateNeedsYou, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID, s.RootSessionID = true, "mid", "lead" }),
		foldRow("leaf2", proto.StateUnread, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "mid" }),
		foldRow("alone", proto.StateWorking, nil),
	}
	got := foldOf(list)
	if len(got) != 3 || got["mid"] != "lead" || got["leaf"] != "lead" || got["leaf2"] != "lead" {
		t.Fatalf("folded %v", got)
	}
	if c := rowNamed(list, "lead").Children(); c != (proto.SessionChildren{Total: 3, Working: 1, NeedsYou: 1}) {
		t.Fatalf("rollup %+v", c)
	}
	for _, id := range []string{"mid", "leaf", "leaf2", "alone"} {
		if c := rowNamed(list, id).Hierarchy; c != nil && c.Children != nil {
			t.Fatalf("%s carries a rollup: %+v", id, c.Children)
		}
	}
	if s := rowNamed(list, "lead"); s.FoldedState() != proto.StateNeedsYou {
		t.Fatalf("the parent shows %s", s.FoldedState())
	}
}

// A child whose parent is not listed (archived, never listed) is an orphan: it stays a row of its own, unless its
// root is listed. Rows that go round in a cycle fold under nothing; an archived row is in no fold.
func TestFoldSessionsOrphansCyclesArchived(t *testing.T) {
	list := []proto.Session{
		foldRow("archived-lead", proto.StateIdle, func(s *proto.Session) { s.Archived = true }),
		foldRow("orphan", proto.StateWorking, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "archived-lead" }),
		foldRow("gone", proto.StateWorking, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "nobody" }),
		foldRow("rooted", proto.StateIdle, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID, s.RootSessionID = true, "nobody", "top" }),
		foldRow("top", proto.StateIdle, nil),
		foldRow("a", proto.StateIdle, func(s *proto.Session) { s.ParentSessionID = "b" }),
		foldRow("b", proto.StateIdle, func(s *proto.Session) { s.ParentSessionID = "a" }),
		foldRow("into-cycle", proto.StateIdle, func(s *proto.Session) { s.ParentSessionID = "a" }),
		foldRow("self", proto.StateIdle, func(s *proto.Session) { s.ParentSessionID, s.RootSessionID = "self", "self" }),
		foldRow("archived-kid", proto.StateWorking, func(s *proto.Session) { s.Archived, s.IsSubagent, s.ParentSessionID = true, true, "top" }),
	}
	got := foldOf(list)
	if len(got) != 1 || got["rooted"] != "top" {
		t.Fatalf("folded %v", got)
	}
	if c := rowNamed(list, "top").Children(); c != (proto.SessionChildren{Total: 1}) {
		t.Fatalf("an archived child is counted: %+v", c)
	}
}

// A runtime child that waits for the user names its parent in asking_parent_session_id only (the node clears the
// rest for the apps that fold every child away, #4494): it folds under its parent all the same, and its parent shows
// that it needs the user.
func TestFoldSessionsBlockedRuntimeChild(t *testing.T) {
	list := []proto.Session{
		foldRow("lead", proto.StateIdle, func(s *proto.Session) { s.SourceKind = proto.SourceRuntime }),
		foldRow("mid", proto.StateWorking, func(s *proto.Session) {
			s.SourceKind, s.IsSubagent, s.ParentSessionID, s.RootSessionID = proto.SourceRuntime, true, "lead", "lead"
		}),
		foldRow("asks", proto.StateNeedsYou, func(s *proto.Session) {
			s.SourceKind = proto.SourceRuntime
			s.SetAskingParentSessionID("mid")
		}),
	}
	if got := foldOf(list); got["asks"] != "lead" || got["mid"] != "lead" {
		t.Fatalf("folded %v", got)
	}
	lead := rowNamed(list, "lead")
	if c := lead.Children(); c != (proto.SessionChildren{Total: 2, Working: 1, NeedsYou: 1}) || lead.FoldedState() != proto.StateNeedsYou {
		t.Fatalf("rollup %+v, state %s", c, lead.FoldedState())
	}
	if asks := rowNamed(list, "asks"); asks.IsSubtask() {
		t.Fatal("folding changed what counts as a subtask")
	}
}

// A row decorated on its own (a push of one session) keeps what the machine's last complete list folded it as, and a
// card sent to the apps names the session its agent folds under.
func TestFoldSubsetAndInteractions(t *testing.T) {
	s := &Service{}
	list := []proto.Session{
		foldRow("lead", proto.StateIdle, nil),
		foldRow("kid", proto.StateWorking, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "lead" }),
	}
	s.foldInventory("m1", list)
	one := []proto.Session{foldRow("kid", proto.StateNeedsYou, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "lead" })}
	s.foldSubset("m1", one)
	if one[0].FoldedUnder() != "lead" {
		t.Fatalf("a row on its own lost its fold: %+v", one[0].Hierarchy)
	}
	parent := []proto.Session{foldRow("lead", proto.StateIdle, nil)}
	s.foldSubset("m1", parent)
	if parent[0].Children() != (proto.SessionChildren{Total: 1, Working: 1}) {
		t.Fatalf("a parent on its own lost its rollup: %+v", parent[0].Hierarchy)
	}
	other := []proto.Session{{SessionID: "kid"}}
	if s.foldSubset("m2", other); other[0].Hierarchy != nil {
		t.Fatal("another machine's fold applied")
	}
	if ix := s.withTask(proto.Interaction{MachineID: "m1", SessionID: "kid"}); ix.FoldedUnder != "lead" {
		t.Fatalf("card folded under %q", ix.FoldedUnder)
	}
	if ix := s.withTask(proto.Interaction{MachineID: "m1", SessionID: "lead"}); ix.FoldedUnder != "" {
		t.Fatalf("a parent's card folded under %q", ix.FoldedUnder)
	}
	// The next complete list says otherwise: the kid is gone.
	s.foldInventory("m1", []proto.Session{foldRow("lead", proto.StateIdle, nil)})
	if ix := s.withTask(proto.Interaction{MachineID: "m1", SessionID: "kid"}); ix.FoldedUnder != "" {
		t.Fatalf("a stale fold: %q", ix.FoldedUnder)
	}
}

// A row folded the same way twice is not given a new hierarchy: the list's bytes, and the rows the hub keeps, stay.
func TestFoldSessionsStable(t *testing.T) {
	list := []proto.Session{
		foldRow("lead", proto.StateIdle, nil),
		foldRow("kid", proto.StateWorking, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "lead" }),
	}
	foldSessions(list, nil).apply(list)
	lead, kid := list[0].Hierarchy, list[1].Hierarchy
	foldSessions(list, nil).apply(list)
	if list[0].Hierarchy != lead || list[1].Hierarchy != kid {
		t.Fatal("an unchanged fold made new hierarchies")
	}
}

// The list the hub is told carries the fold: the child names the session it folds under, its parent the rollup, and
// both say so again in the next list once the child's state changed (#5120).
func TestPublishedListFoldsChildren(t *testing.T) {
	ft := newFleetTestNoStore(t)
	lead := proto.Session{SessionID: "lead", Status: proto.StatusIdle, Cwd: "/w/app", Agent: "codex"}
	kid := proto.Session{SessionID: "kid", Status: proto.StatusWorking, Cwd: "/w/app", Agent: "codex", IsSubagent: true,
		ParentSessionID: "lead", RootSessionID: "lead"}
	ft.sessions("mac", lead, kid)
	var published []proto.Session
	waitFolded := func(want proto.SessionChildren) []proto.Session {
		t.Helper()
		waitFor(t, "the published fold", func() bool {
			published = ft.lastList("mac")
			l, k := rowNamed(published, "lead"), rowNamed(published, "kid")
			return l != nil && k != nil && k.FoldedUnder() == "lead" && l.Children() == want
		})
		return published
	}
	list := waitFolded(proto.SessionChildren{Total: 1, Working: 1})
	if l := rowNamed(list, "lead"); l.FoldedUnder() != "" || l.FoldedState() != proto.StateWorking {
		t.Fatalf("parent %+v shows %s", l.Hierarchy, l.FoldedState())
	}
	kid.Status = proto.StatusDone
	ft.sessions("mac", lead, kid)
	waitFolded(proto.SessionChildren{Total: 1})
}

// A review's reviewer is no row the apps are shown (publishList leaves it out): it is no parent, and it is not counted in
// a parent's children or the state the parent shows.
func TestFoldLeavesReviewersOut(t *testing.T) {
	list := []proto.Session{
		foldRow("lead", proto.StateIdle, nil),
		foldRow("kid", proto.StateWorking, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "lead" }),
		foldRow("reviewer", proto.StateNeedsYou, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "lead" }),
		foldRow("under-reviewer", proto.StateWorking, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "reviewer" }),
	}
	f := foldSessions(list, map[string]bool{"reviewer": true})
	f.apply(list)
	if got := rowNamed(list, "lead").Children(); got != (proto.SessionChildren{Total: 1, Working: 1}) {
		t.Fatalf("rollup %+v", got)
	}
	if lead := rowNamed(list, "lead"); lead.FoldedState() != proto.StateWorking {
		t.Fatalf("the parent shows %s", lead.FoldedState())
	}
	if r, u := rowNamed(list, "reviewer"), rowNamed(list, "under-reviewer"); r.Hierarchy != nil || u.FoldedUnder() != "" {
		t.Fatalf("reviewer %+v, its child under %q", r.Hierarchy, u.FoldedUnder())
	}

	// The manager reads its reviewers off the to-dos, for the machine it folds only.
	s := &Service{}
	s.todos.todos = map[string]*Todo{"todo_r": {ID: "todo_r", Title: "x",
		Agents: []TodoAgent{{MachineID: "m1", SessionID: "reviewer", Source: sourceReview}}}}
	again := []proto.Session{
		foldRow("lead", proto.StateIdle, nil),
		foldRow("reviewer", proto.StateNeedsYou, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "lead" }),
	}
	s.foldInventory("m1", again)
	if under, _, _ := s.foldedUnder("m1", "reviewer"); again[0].Hierarchy != nil || under != "" {
		t.Fatalf("a reviewer folded: %+v", again[0].Hierarchy)
	}
	other := []proto.Session{again[0], foldRow("reviewer", proto.StateNeedsYou, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "lead" })}
	s.foldInventory("m2", other)
	if under, _, _ := s.foldedUnder("m2", "reviewer"); under != "lead" {
		t.Fatal("another machine's session of the reviewer's id was left out")
	}
}

// inTab places a row in pane n of a herdr tab (space, slot); a negative n leaves where it sits unsaid.
func inTab(space, slot string, n int, more ...func(*proto.Session)) func(*proto.Session) {
	return func(s *proto.Session) {
		s.SpaceKind, s.SpaceID, s.SlotID = "herdr", space, slot
		if n >= 0 {
			s.PaneIndex = &n
		}
		for _, f := range more {
			f(s)
		}
	}
}

func foldedAs(list []proto.Session) map[string]string {
	out := map[string]string{}
	for i := range list {
		if a := list[i].FoldedAs(); a != "" {
			out[list[i].SessionID] = a
		}
	}
	return out
}

// A tab's lead is the Agent in its first pane; every other running Agent of the tab is its worker, folded under it and
// marked so, and the lead counts them, by their own states, in its rollup (#5180). Another tab, a row with no tab and
// a row in a tab of another space kind fold nowhere.
func TestFoldSessionsTabTeam(t *testing.T) {
	list := []proto.Session{
		foldRow("lead", proto.StateIdle, inTab("w1", "t1", 0)),
		foldRow("busy", proto.StateWorking, inTab("w1", "t1", 1)),
		foldRow("asks", proto.StateNeedsYou, inTab("w1", "t1", 2)),
		foldRow("next-tab", proto.StateWorking, inTab("w1", "t2", 1)),
		foldRow("other-kind", proto.StateWorking, inTab("w1", "t1", 3, func(s *proto.Session) { s.SpaceKind = "orca" })),
		foldRow("no-tab", proto.StateWorking, nil),
	}
	if got := foldOf(list); len(got) != 2 || got["busy"] != "lead" || got["asks"] != "lead" {
		t.Fatalf("folded %v", got)
	}
	if got := foldedAs(list); len(got) != 2 || got["busy"] != proto.FoldedAsWorker || got["asks"] != proto.FoldedAsWorker {
		t.Fatalf("folded as %v", got)
	}
	lead := rowNamed(list, "lead")
	if c := lead.Children(); c != (proto.SessionChildren{Total: 2, Working: 1, NeedsYou: 1, Workers: 2}) || lead.FoldedState() != proto.StateNeedsYou {
		t.Fatalf("rollup %+v, state %s", c, lead.FoldedState())
	}
	if lead.FoldedUnder() != "" || lead.FoldedAs() != "" {
		t.Fatalf("the lead folds: %+v", lead.Hierarchy)
	}
	if busy := rowNamed(list, "busy"); busy.IsSubtask() || busy.Children() != (proto.SessionChildren{}) {
		t.Fatalf("a worker is a subtask or carries a rollup: %+v", busy.Hierarchy)
	}
}

// A tab folds only with exactly one lead: its first pane a shell (no Agent there), two rows claiming the first pane,
// an offline or hidden Agent there, or a first pane that is a child's, leave its Agents peers (#5180).
func TestFoldSessionsTabWithoutLead(t *testing.T) {
	for name, lead := range map[string][]proto.Session{
		"shell first": nil,
		"two firsts": {
			foldRow("one", proto.StateIdle, inTab("w1", "t1", 0)),
			foldRow("two", proto.StateIdle, inTab("w1", "t1", 0)),
		},
		"offline lead":  {foldRow("lead", proto.StateOffline, inTab("w1", "t1", 0))},
		"reviewer lead": {foldRow("reviewer", proto.StateIdle, inTab("w1", "t1", 0))},
		"child first": {foldRow("kid", proto.StateWorking, inTab("w1", "t1", 0, func(s *proto.Session) {
			s.IsSubagent, s.ParentSessionID = true, "elsewhere"
		}))},
		"assistant first": {foldRow("chat", proto.StateIdle, inTab("w1", "t1", 0, func(s *proto.Session) { s.Assistant = true }))},
		"pane unknown":    {foldRow("lead", proto.StateIdle, inTab("w1", "t1", -1))},
	} {
		list := append(lead,
			foldRow("a", proto.StateWorking, inTab("w1", "t1", 1)),
			foldRow("b", proto.StateNeedsYou, inTab("w1", "t1", 2)),
		)
		f := foldSessions(list, map[string]bool{"reviewer": true})
		f.apply(list)
		for i := range list {
			if list[i].Hierarchy != nil {
				t.Fatalf("%s: %s folds %+v", name, list[i].SessionID, list[i].Hierarchy)
			}
		}
	}
}

// A stopped pane has left the team: it stays a row of its own and is not counted. An assistant row in the tab is in
// no team either (#5180).
func TestFoldSessionsTabLeavesOfflineAndAssistantOut(t *testing.T) {
	list := []proto.Session{
		foldRow("lead", proto.StateWorking, inTab("w1", "t1", 0)),
		foldRow("stopped", proto.StateOffline, inTab("w1", "t1", 1)),
		foldRow("chat", proto.StateNeedsYou, inTab("w1", "t1", 2, func(s *proto.Session) { s.Assistant = true })),
		foldRow("worker", proto.StateIdle, inTab("w1", "t1", 3)),
	}
	if got := foldOf(list); len(got) != 1 || got["worker"] != "lead" {
		t.Fatalf("folded %v", got)
	}
	if c := rowNamed(list, "lead").Children(); c != (proto.SessionChildren{Total: 1, Workers: 1}) {
		t.Fatalf("rollup %+v", c)
	}
}

// A worker's child (a Codex subagent it started) folds under the worker's lead, its topmost listed ancestor, with no
// folded_as of its own, and the lead counts it beside its workers. A child sitting in the tab is no worker: it keeps
// folding by its parent (#5180).
func TestFoldSessionsTabWorkerChild(t *testing.T) {
	list := []proto.Session{
		foldRow("lead", proto.StateIdle, inTab("w1", "t1", 0)),
		foldRow("worker", proto.StateIdle, inTab("w1", "t1", 1)),
		foldRow("other", proto.StateWorking, inTab("w1", "t1", 2)),
		foldRow("sub", proto.StateWorking, func(s *proto.Session) {
			s.Agent, s.IsSubagent, s.ParentSessionID, s.RootSessionID = "codex", true, "worker", "worker"
		}),
		foldRow("solo", proto.StateIdle, nil),
		foldRow("kid-in-tab", proto.StateNeedsYou, inTab("w1", "t1", 3, func(s *proto.Session) {
			s.IsSubagent, s.ParentSessionID = true, "solo"
		})),
	}
	got := foldOf(list)
	if len(got) != 4 || got["worker"] != "lead" || got["other"] != "lead" || got["sub"] != "lead" || got["kid-in-tab"] != "solo" {
		t.Fatalf("folded %v", got)
	}
	if as := foldedAs(list); len(as) != 2 || as["worker"] != proto.FoldedAsWorker || as["other"] != proto.FoldedAsWorker {
		t.Fatalf("folded as %v", as)
	}
	if c := rowNamed(list, "lead").Children(); c != (proto.SessionChildren{Total: 3, Working: 2, Workers: 2}) {
		t.Fatalf("lead rollup %+v", c)
	}
	if c := rowNamed(list, "solo").Children(); c != (proto.SessionChildren{Total: 1, NeedsYou: 1}) {
		t.Fatalf("solo rollup %+v", c)
	}
	if rowNamed(list, "worker").Children() != (proto.SessionChildren{}) {
		t.Fatal("a worker carries its child's rollup")
	}
}

// A worker pushed on its own keeps folded_as, its lead the workers count, and a card a worker asks names its lead and
// that it comes from a worker; the next complete list without the team drops both (#5180).
func TestFoldSubsetAndInteractionsTabTeam(t *testing.T) {
	s := &Service{}
	team := []proto.Session{
		foldRow("lead", proto.StateIdle, inTab("w1", "t1", 0)),
		foldRow("worker", proto.StateWorking, inTab("w1", "t1", 1)),
	}
	s.foldInventory("m1", team)
	one := []proto.Session{foldRow("worker", proto.StateNeedsYou, inTab("w1", "t1", 1))}
	s.foldSubset("m1", one)
	if one[0].FoldedUnder() != "lead" || one[0].FoldedAs() != proto.FoldedAsWorker {
		t.Fatalf("a worker on its own lost its fold: %+v", one[0].Hierarchy)
	}
	lead := []proto.Session{foldRow("lead", proto.StateIdle, inTab("w1", "t1", 0))}
	if s.foldSubset("m1", lead); lead[0].Children() != (proto.SessionChildren{Total: 1, Working: 1, Workers: 1}) {
		t.Fatalf("a lead on its own lost its rollup: %+v", lead[0].Hierarchy)
	}
	if ix := s.withTask(proto.Interaction{MachineID: "m1", SessionID: "worker"}); ix.FoldedUnder != "lead" || ix.FoldedAs != proto.FoldedAsWorker {
		t.Fatalf("a worker's card: under %q as %q", ix.FoldedUnder, ix.FoldedAs)
	}
	if ix := s.withTask(proto.Interaction{MachineID: "m1", SessionID: "lead"}); ix.FoldedUnder != "" || ix.FoldedAs != "" {
		t.Fatalf("the lead's card: under %q as %q", ix.FoldedUnder, ix.FoldedAs)
	}
	s.foldInventory("m1", []proto.Session{
		foldRow("lead", proto.StateIdle, inTab("w1", "t1", 0)),
		foldRow("worker", proto.StateOffline, inTab("w1", "t1", 1)),
	})
	if ix := s.withTask(proto.Interaction{MachineID: "m1", SessionID: "worker"}); ix.FoldedUnder != "" || ix.FoldedAs != "" {
		t.Fatalf("a stopped worker's card: under %q as %q", ix.FoldedUnder, ix.FoldedAs)
	}
}

// The list the hub is told folds a tab's workers under its lead, from the rows the node sends (#5180).
func TestPublishedListFoldsTabTeam(t *testing.T) {
	ft := newFleetTestNoStore(t)
	zero, one := 0, 1
	lead := proto.Session{SessionID: "lead", Status: proto.StatusIdle, Cwd: "/w/app", Agent: "claude",
		SpaceKind: "herdr", SpaceID: "w1", SlotID: "t1", PaneIndex: &zero}
	worker := proto.Session{SessionID: "worker", Status: proto.StatusWorking, Cwd: "/w/app", Agent: "codex",
		SpaceKind: "herdr", SpaceID: "w1", SlotID: "t1", PaneIndex: &one}
	ft.sessions("mac", lead, worker)
	var l *proto.Session
	waitFor(t, "the team folded", func() bool {
		list := ft.lastList("mac")
		w := rowNamed(list, "worker")
		l = rowNamed(list, "lead")
		return l != nil && w != nil && w.FoldedUnder() == "lead" && w.FoldedAs() == proto.FoldedAsWorker &&
			l.Children() == (proto.SessionChildren{Total: 1, Working: 1, Workers: 1})
	})
	if l.FoldedState() != proto.StateWorking {
		t.Fatalf("the lead shows %s", l.FoldedState())
	}
}

// foldSessions skips its walk for a list where no row can fold (mayFold) and says what the walk says of it: nothing.
// A list whose only fold is a side session (#7944) is walked and folds. An orphan child (its parent is not listed) makes the walk run and folds nothing, so the fold of every list, with the
// orphan or without, is the same, whether a list is walked or not.
func TestFoldSessionsWithoutWalk(t *testing.T) {
	orphan := foldRow("orphan", proto.StateWorking, func(s *proto.Session) { s.ParentSessionID = "not-listed" })
	same := func(t *testing.T, name string, list []proto.Session, hidden map[string]bool) sessionFold {
		t.Helper()
		got := foldSessions(list, hidden)
		if walked := foldSessions(append(slices.Clone(list), orphan), hidden); !reflect.DeepEqual(got, walked) {
			t.Fatalf("%s: folded\n%+v\nwalked\n%+v", name, got, walked)
		}
		return got
	}
	for _, c := range []struct {
		name   string
		list   []proto.Session
		hidden map[string]bool
		may    bool
		folds  bool
	}{
		{name: "no tabs, no children", list: []proto.Session{foldRow("a", proto.StateWorking, nil), foldRow("b", proto.StateIdle, nil)}},
		{name: "one pane a tab", list: []proto.Session{
			foldRow("a", proto.StateWorking, inTab("w1", "t1", 0)),
			foldRow("b", proto.StateIdle, inTab("w1", "t2", 0)),
			foldRow("c", proto.StateIdle, inTab("w2", "t1", 0)),
		}},
		{name: "a lead and a worker in other tabs", may: true, list: []proto.Session{
			foldRow("a", proto.StateWorking, inTab("w1", "t1", 0)),
			foldRow("b", proto.StateIdle, inTab("w1", "t2", 1)),
		}},
		{name: "a squad", may: true, folds: true, list: []proto.Session{
			foldRow("lead", proto.StateIdle, inTab("w1", "t1", 0)),
			foldRow("worker", proto.StateWorking, inTab("w1", "t1", 1)),
		}},
		{name: "a squad without a pane index", list: []proto.Session{
			foldRow("a", proto.StateIdle, inTab("w1", "t1", -1)),
			foldRow("b", proto.StateWorking, inTab("w1", "t1", -1)),
		}},
		{name: "a squad whose lead is offline", list: []proto.Session{
			foldRow("lead", proto.StateOffline, inTab("w1", "t1", 0)),
			foldRow("worker", proto.StateWorking, inTab("w1", "t1", 1)),
		}},
		{name: "a squad whose lead is hidden", hidden: map[string]bool{"lead": true}, list: []proto.Session{
			foldRow("lead", proto.StateIdle, inTab("w1", "t1", 0)),
			foldRow("worker", proto.StateWorking, inTab("w1", "t1", 1)),
		}},
		{name: "a child", may: true, folds: true, list: []proto.Session{
			foldRow("parent", proto.StateIdle, nil),
			foldRow("kid", proto.StateWorking, func(s *proto.Session) { s.ParentSessionID = "parent" }),
		}},
		{name: "an archived child", list: []proto.Session{
			foldRow("parent", proto.StateIdle, nil),
			foldRow("kid", proto.StateWorking, func(s *proto.Session) { s.ParentSessionID, s.Archived = "parent", true }),
		}},
		// A side session (#7944) is no child and in no tab: it alone has to make the walk run.
		{name: "a side, no tabs, no children", may: true, folds: true, list: []proto.Session{
			foldRow("main", proto.StateIdle, nil),
			foldRow("side", proto.StateWorking, func(s *proto.Session) { s.SetParentAgentID("main") }),
		}},
		{name: "a side of an unlisted row", may: true, list: []proto.Session{
			foldRow("main", proto.StateIdle, nil),
			foldRow("side", proto.StateWorking, func(s *proto.Session) { s.SetParentAgentID("gone") }),
		}},
		{name: "a row naming itself its side parent", list: []proto.Session{
			foldRow("main", proto.StateIdle, nil),
			foldRow("side", proto.StateWorking, func(s *proto.Session) { s.SetParentAgentID("side") }),
		}},
		{name: "an archived side", list: []proto.Session{
			foldRow("main", proto.StateIdle, nil),
			foldRow("side", proto.StateWorking, func(s *proto.Session) { s.SetParentAgentID("main"); s.Archived = true }),
		}},
		{name: "an assistant row naming a side parent", list: []proto.Session{
			foldRow("main", proto.StateIdle, nil),
			foldRow("side", proto.StateWorking, func(s *proto.Session) { s.SetParentAgentID("main"); s.Assistant = true }),
		}},
	} {
		listed := func(s *proto.Session) bool { return (!s.Archived || s.Assistant) && !c.hidden[s.SessionID] }
		if may := mayFold(c.list, listed); may != c.may {
			t.Errorf("%s: mayFold %v, want %v", c.name, may, c.may)
		}
		f := same(t, c.name, c.list, c.hidden)
		if (f.under != nil) != c.folds {
			t.Errorf("%s: folded %+v", c.name, f)
		}
		if c.name == "a side, no tabs, no children" && (f.under["side"] != "main" || f.as["side"] != proto.FoldedAsSide) {
			t.Errorf("%s: the side folds %+v", c.name, f)
		}
	}

	// Lists of every shape: ids listed twice, tabs, panes, children, archived, offline, the assistant's, hidden rows.
	r := rand.New(rand.NewPCG(5180, 5120))
	ids := []string{"a", "b", "c", "d", "e", "f"}
	states := []string{proto.StateWorking, proto.StateIdle, proto.StateNeedsYou, proto.StateOffline}
	pick := func(of []string) string { return of[r.IntN(len(of))] }
	var walks, skips int
	for n := range 4000 {
		list := make([]proto.Session, r.IntN(7))
		for i := range list {
			s := proto.Session{SessionID: pick(ids), State: pick(states), Archived: r.IntN(8) == 0, Assistant: r.IntN(10) == 0}
			if r.IntN(3) > 0 {
				s.SpaceKind, s.SpaceID, s.SlotID = "herdr", pick([]string{"w1", "w2"}), pick([]string{"t1", "t2"})
				if p := r.IntN(4) - 1; p >= 0 {
					s.PaneIndex = &p
				}
			}
			switch r.IntN(6) {
			case 0:
				s.ParentSessionID = pick(append(ids, "gone"))
			case 1:
				s.IsSubagent, s.RootSessionID = true, pick(ids)
			case 2:
				s.SetParentAgentID(pick(append(ids, "gone")))
			}
			list[i] = s
		}
		var hidden map[string]bool
		if r.IntN(4) == 0 {
			hidden = map[string]bool{pick(ids): true}
		}
		if mayFold(list, func(s *proto.Session) bool { return (!s.Archived || s.Assistant) && !hidden[s.SessionID] }) {
			walks++
		} else {
			skips++
		}
		same(t, fmt.Sprintf("list %d %+v hidden %v", n, list, hidden), list, hidden)
	}
	if walks < 500 || skips < 500 {
		t.Fatalf("%d lists walked, %d not: the lists do not try both ways", walks, skips)
	}
}

// Cross-machine A2A subagents fold under their parent on another computer, roll up working/needs_you/total
// into the parent's SessionChildren, carry folded_under_machine_id on both sessions and interactions, and
// handle multi-hop chains, cycles, and missing/archived parents cleanly.
func TestFoldFleetCrossMachine(t *testing.T) {
	lists := map[string][]proto.Session{
		"mac": {
			foldRow("lead", proto.StateIdle, func(s *proto.Session) { s.MachineID = "mac" }),
			foldRow("local-kid", proto.StateWorking, func(s *proto.Session) {
				s.MachineID, s.IsSubagent, s.ParentSessionID = "mac", true, "lead"
			}),
		},
		"box": {
			foldRow("remote-working", proto.StateWorking, func(s *proto.Session) {
				s.MachineID, s.IsSubagent, s.ParentSessionID = "box", true, "lead"
				s.SetSubagentProvenance("mac", "task-1", proto.SubagentOriginA2A)
			}),
			foldRow("remote-needs", proto.StateNeedsYou, func(s *proto.Session) {
				s.MachineID, s.IsSubagent, s.ParentSessionID = "box", true, "remote-working"
				s.SetSubagentProvenance("", "", proto.SubagentOriginNative)
			}),
			foldRow("remote-orphan", proto.StateWorking, func(s *proto.Session) {
				s.MachineID, s.IsSubagent, s.ParentSessionID = "box", true, "missing-on-mac"
				s.SetSubagentProvenance("mac", "task-orphan", proto.SubagentOriginA2A)
			}),
			// Cross-machine cycle must fold nothing.
			foldRow("cyc-b", proto.StateIdle, func(s *proto.Session) {
				s.MachineID, s.IsSubagent, s.ParentSessionID = "box", true, "cyc-a"
				s.SetParentMachineID("mac")
			}),
		},
	}
	lists["mac"] = append(lists["mac"], foldRow("cyc-a", proto.StateIdle, func(s *proto.Session) {
		s.MachineID, s.IsSubagent, s.ParentSessionID = "mac", true, "cyc-b"
		s.SetParentMachineID("box")
	}))

	folds := foldFleet(lists, nil)
	folds["mac"].apply(lists["mac"])
	folds["box"].apply(lists["box"])

	lead := rowNamed(lists["mac"], "lead")
	if got := lead.Children(); got != (proto.SessionChildren{Total: 3, Working: 2, NeedsYou: 1}) {
		t.Fatalf("lead cross-machine rollup = %+v, want {Total:3 Working:2 NeedsYou:1}", got)
	}
	if lead.FoldedState() != proto.StateNeedsYou {
		t.Fatalf("lead folded state = %s, want needs_you", lead.FoldedState())
	}
	if local := rowNamed(lists["mac"], "local-kid"); local.FoldedUnder() != "lead" || local.FoldedUnderMachineID() != "" {
		t.Fatalf("same-machine child must omit folded_under_machine_id: %+v", local.Hierarchy)
	}
	for _, id := range []string{"remote-working", "remote-needs"} {
		kid := rowNamed(lists["box"], id)
		if kid.FoldedUnder() != "lead" || kid.FoldedUnderMachineID() != "mac" {
			t.Fatalf("%s: under=%q underMachine=%q, want lead/mac", id, kid.FoldedUnder(), kid.FoldedUnderMachineID())
		}
	}
	if orphan := rowNamed(lists["box"], "remote-orphan"); orphan.FoldedUnder() != "" || orphan.FoldedUnderMachineID() != "" {
		t.Fatalf("orphan folded unexpectedly: %+v", orphan.Hierarchy)
	}
	for _, pair := range [][2]string{{"mac", "cyc-a"}, {"box", "cyc-b"}} {
		if cyc := rowNamed(lists[pair[0]], pair[1]); cyc.FoldedUnder() != "" || cyc.FoldedUnderMachineID() != "" {
			t.Fatalf("cycle %s/%s folded unexpectedly: %+v", pair[0], pair[1], cyc.Hierarchy)
		}
	}
}

// End-to-end fleet publishing: when an A2A subagent starts on box for a parent on mac,
// foldInventory folds box's child under mac/lead, updates mac's published parent rollup
// asynchronously, and stamps folded_under_machine_id on box's pending interactions.
func TestPublishedListFoldsCrossMachineSubagent(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ft.d.add("box", "Box")
	lead := proto.Session{SessionID: "lead", Status: proto.StatusIdle, Cwd: "/w/app", Agent: "claude"}
	ft.sessions("mac", lead)
	waitFor(t, "mac initial list", func() bool {
		return rowNamed(ft.lastList("mac"), "lead") != nil
	})

	// Register durable subagent link in manager index (as a2aStart / a2aLaunched does).
	ft.s.recordA2ASubagent(&a2aTask{
		A2ARow:  db.A2ARow{ID: "task-cross-1", SourceMachine: "mac", SourceSession: "lead", TargetMachine: "box", TargetSession: "sub-box"},
		a2aBody: a2aBody{Launch: &a2aLaunch{CWD: "/code/app", Subagent: true}},
	})
	child := proto.Session{SessionID: "sub-box", Status: proto.StatusWorking, Cwd: "/code/app", Agent: "codex"}
	ft.sessions("box", child)

	waitFor(t, "cross-machine child tagged and folded on box", func() bool {
		k := rowNamed(ft.lastList("box"), "sub-box")
		return k != nil && k.IsSubagent && k.ParentSessionID == "lead" && k.ParentMachineID() == "mac" &&
			k.A2ATaskID() == "task-cross-1" && k.SubagentOrigin() == proto.SubagentOriginA2A &&
			k.FoldedUnder() == "lead" && k.FoldedUnderMachineID() == "mac"
	})
	waitFor(t, "parent rollup updated on mac", func() bool {
		l := rowNamed(ft.lastList("mac"), "lead")
		return l != nil && l.Children() == (proto.SessionChildren{Total: 1, Working: 1}) && l.FoldedState() == proto.StateWorking
	})
	if ix := ft.s.withTask(proto.Interaction{MachineID: "box", SessionID: "sub-box"}); ix.FoldedUnder != "lead" || ix.FoldedUnderMachineID != "mac" {
		t.Fatalf("cross-machine interaction: under=%q underMachine=%q, want lead/mac", ix.FoldedUnder, ix.FoldedUnderMachineID)
	}

	// Child becomes blocked -> parent rollup flips to needs_you.
	child.Status = proto.StatusBlocked
	ft.sessions("box", child)
	waitFor(t, "parent rollup flips to needs_you", func() bool {
		l := rowNamed(ft.lastList("mac"), "lead")
		return l != nil && l.Children() == (proto.SessionChildren{Total: 1, NeedsYou: 1}) && l.FoldedState() == proto.StateNeedsYou
	})

	// Child disappears from box -> mac's parent rollup clears cleanly without leaving stale counts.
	ft.sessions("box")
	waitFor(t, "parent rollup clears when cross-machine child is gone", func() bool {
		l := rowNamed(ft.lastList("mac"), "lead")
		return l != nil && l.Children() == (proto.SessionChildren{}) && l.FoldedState() == proto.StateIdle
	})
}

// Parity: foldFleet on any single-machine list (including tab workers, side sessions, children of sides,
// cycles, archived and hidden rows) produces the exact same sessionFold as foldSessions, never losing folded_as.
func TestFoldFleetMatchesFoldSessionsParity(t *testing.T) {
	r := rand.New(rand.NewPCG(14215, 7944))
	ids := []string{"a", "b", "c", "d", "e", "f"}
	states := []string{proto.StateWorking, proto.StateIdle, proto.StateNeedsYou, proto.StateUnread, proto.StateOffline}
	pick := func(of []string) string { return of[r.IntN(len(of))] }
	sawWorker, sawSide := false, false
	for n := range 1000 {
		list := make([]proto.Session, r.IntN(7))
		for i := range list {
			s := proto.Session{MachineID: "m", SessionID: pick(ids), State: pick(states), Archived: r.IntN(8) == 0, Assistant: r.IntN(10) == 0}
			if r.IntN(3) > 0 {
				s.SpaceKind, s.SpaceID, s.SlotID = "herdr", pick([]string{"w1", "w2"}), pick([]string{"t1", "t2"})
				if p := r.IntN(4) - 1; p >= 0 {
					s.PaneIndex = &p
				}
			}
			switch r.IntN(6) {
			case 0:
				s.ParentSessionID = pick(append(ids, "gone"))
			case 1:
				s.IsSubagent, s.RootSessionID = true, pick(ids)
			case 2:
				s.SetParentAgentID(pick(append(ids, "gone")))
			}
			list[i] = s
		}
		var hidden map[string]bool
		if r.IntN(4) == 0 {
			hidden = map[string]bool{pick(ids): true}
		}
		want := foldSessions(list, hidden)
		got := foldFleet(map[string][]proto.Session{"m": list}, map[string]map[string]bool{"m": hidden})["m"]
		if !sameSessionFold(want, got) {
			t.Fatalf("case %d: foldFleet != foldSessions\n got %+v\nwant %+v", n, got, want)
		}
		for _, as := range got.as {
			if as == proto.FoldedAsWorker {
				sawWorker = true
			}
			if as == proto.FoldedAsSide {
				sawSide = true
			}
		}
	}
	if !sawWorker || !sawSide {
		t.Fatalf("parity fuzz did not cover both worker (%v) and side (%v) folds", sawWorker, sawSide)
	}
}

// Removing a machine hosting cross-machine subagents prunes foldMeta and clears stale rollups on remaining parents.
func TestFoldInventoryPrunesRemovedMachine(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ft.d.add("box", "Box")
	ft.sessions("mac", proto.Session{SessionID: "lead", Status: proto.StatusIdle, Cwd: "/w/app", Agent: "claude"})
	ft.s.recordA2ASubagent(&a2aTask{
		A2ARow:  db.A2ARow{ID: "task-rm-1", SourceMachine: "mac", SourceSession: "lead", TargetMachine: "box", TargetSession: "sub-box"},
		a2aBody: a2aBody{Launch: &a2aLaunch{CWD: "/code/app", Subagent: true}},
	})
	ft.sessions("box", proto.Session{SessionID: "sub-box", Status: proto.StatusWorking, Cwd: "/code/app", Agent: "codex"})
	waitFor(t, "mac parent rolls up box subagent", func() bool {
		l := rowNamed(ft.lastList("mac"), "lead")
		return l != nil && l.Children() == (proto.SessionChildren{Total: 1, Working: 1})
	})

	// Remove "box" from devices and prune removed fold state.
	ft.d.mu.Lock()
	delete(ft.d.machines, "box")
	ft.d.order = slices.DeleteFunc(ft.d.order, func(id string) bool { return id == "box" })
	ft.d.mu.Unlock()
	ft.s.pruneRemovedFoldMachines()

	waitFor(t, "mac parent rollup clears after box removed", func() bool {
		l := rowNamed(ft.lastList("mac"), "lead")
		return l != nil && l.Children() == (proto.SessionChildren{}) && l.FoldedState() == proto.StateIdle
	})
	ft.s.fleet.foldMu.Lock()
	_, boxKept := ft.s.fleet.foldMeta["box"]
	ft.s.fleet.foldMu.Unlock()
	if boxKept {
		t.Fatal("removed machine stayed in foldMeta")
	}
}

// Unrelated machines in a multi-machine fleet stay on the local fast path even when two other machines have
// an active cross-machine subagent, never entering foldMeta or triggering full-fleet recomputation.
func TestFoldInventoryUnrelatedMachineTakesLocalFastPath(t *testing.T) {
	s := &Service{}
	box00 := []proto.Session{foldRow("lead", proto.StateIdle, func(r *proto.Session) { r.MachineID = "box00" })}
	box01 := []proto.Session{foldRow("sub", proto.StateWorking, func(r *proto.Session) {
		r.MachineID, r.IsSubagent, r.ParentSessionID = "box01", true, "lead"
		r.SetParentMachineID("box00")
	})}
	s.foldInventory("box01", box01)
	s.foldInventory("box00", box00)

	unrelated := make([]proto.Session, 200)
	for i := range unrelated {
		unrelated[i] = foldRow(fmt.Sprintf("s%03d", i), proto.StateIdle, func(r *proto.Session) { r.MachineID = "box07" })
	}
	s.foldInventory("box07", unrelated)
	s.fleet.foldMu.Lock()
	_, inMeta := s.fleet.foldMeta["box07"]
	metaCount := len(s.fleet.foldMeta)
	s.fleet.foldMu.Unlock()
	if inMeta || metaCount > 2 {
		t.Fatalf("unrelated machine entered foldMeta (inMeta=%v, metaCount=%d)", inMeta, metaCount)
	}
	if !raceEnabled {
		if n := testing.AllocsPerRun(20, func() { s.foldInventory("box07", unrelated) }); n > 2 {
			t.Fatalf("unrelated 200-row machine allocated %v times during foldInventory", n)
		}
	}
}

// When a cross-machine parent arrives while its child already holds a pending interaction, foldInventory
// refreshes the pending interaction on the hub with the newly resolved folded_under / folded_under_machine_id.
func TestFoldInventoryRefreshesPendingInteractionOnFoldChange(t *testing.T) {
	ft := newFleetTestNoStore(t)
	ft.d.add("box", "Box")
	ft.s.recordA2ASubagent(&a2aTask{
		A2ARow:  db.A2ARow{ID: "task-ix-1", SourceMachine: "mac", SourceSession: "lead", TargetMachine: "box", TargetSession: "sub-box"},
		a2aBody: a2aBody{Launch: &a2aLaunch{CWD: "/code/app", Subagent: true}},
	})
	// Child arrives BEFORE parent is listed, with an active pending interaction.
	ft.sessions("box", proto.Session{SessionID: "sub-box", Status: proto.StatusBlocked, Cwd: "/code/app", Agent: "codex"})
	ft.s.fleet.mu.Lock()
	ft.s.ixMapLocked()["ix-cross"] = &pending{
		ix:   proto.Interaction{ID: "ix-cross", MachineID: "box", SessionID: "sub-box", Kind: proto.IxPermission, ToolName: "Bash"},
		done: make(chan proto.Resolution, 1),
	}
	ft.s.fleet.mu.Unlock()

	// Parent arrives on mac -> fold changes for box/sub-box -> pending card is republished with folded_under_machine_id="mac".
	ft.sessions("mac", proto.Session{SessionID: "lead", Status: proto.StatusIdle, Cwd: "/w/app", Agent: "claude"})
	waitFor(t, "pending interaction refreshed with cross-machine fold", func() bool {
		for _, raw := range ft.toldOf(assistantwire.EventFleetInteraction) {
			var p assistantwire.FleetInteraction
			if json.Unmarshal(raw, &p) == nil && p.Interaction.ID == "ix-cross" && p.Interaction.FoldedUnder == "lead" && p.Interaction.FoldedUnderMachineID == "mac" {
				return true
			}
		}
		return false
	})
}

// Adding an outgoing cross-machine edge to an already-cached parent machine is never treated as a
// cache hit even when foldMeta[machine] was already pre-populated with identical entries (e.g. seeded).
func TestFoldInventoryAddedOutgoingEdgeIsCacheMiss(t *testing.T) {
	s := &Service{}
	macList := []proto.Session{foldRow("lead", proto.StateIdle, func(r *proto.Session) { r.MachineID = "mac" })}
	relayList := []proto.Session{foldRow("mid", proto.StateWorking, func(r *proto.Session) {
		r.MachineID, r.IsSubagent, r.ParentSessionID = "relay", true, "lead"
		r.SetParentMachineID("mac")
	})}
	// Simulate both machines already cached in foldMeta (e.g. via seeding) without the relay -> mac edge recorded yet.
	s.fleet.foldMu.Lock()
	s.fleet.foldMeta = map[string][]foldEntry{
		"mac":   toFoldEntries(macList, nil),
		"relay": toFoldEntries(relayList, nil),
	}
	s.fleet.folds = map[string]sessionFold{}
	s.fleet.foldMu.Unlock()

	s.foldInventory("relay", relayList)
	if relayList[0].FoldedUnder() != "lead" || relayList[0].FoldedUnderMachineID() != "mac" {
		t.Fatalf("added outgoing edge was short-circuited: folded=%q machine=%q", relayList[0].FoldedUnder(), relayList[0].FoldedUnderMachineID())
	}
	s.fleet.foldMu.Lock()
	macFold := s.fleet.folds["mac"]
	s.fleet.foldMu.Unlock()
	if got := macFold.children["lead"]; got == nil || *got != (proto.SessionChildren{Total: 1, Working: 1}) {
		t.Fatalf("parent rollup not computed after added outgoing edge: %+v", got)
	}
}

// Concurrent local parent folding and cross-machine child arrival must never overwrite the parent's
// cross-machine rollup when rechecking cross-machine membership under foldMu.
func TestFoldInventoryLocalRecheckDoesNotClobberConcurrentCrossChild(t *testing.T) {
	for range 32 {
		s := &Service{}
		macRows := func() []proto.Session {
			return []proto.Session{foldRow("lead", proto.StateIdle, func(r *proto.Session) { r.MachineID = "mac" })}
		}
		boxRows := func() []proto.Session {
			return []proto.Session{foldRow("sub", proto.StateWorking, func(r *proto.Session) {
				r.MachineID, r.IsSubagent, r.ParentSessionID = "box", true, "lead"
				r.SetParentMachineID("mac")
			})}
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.foldInventory("box", boxRows())
		}()
		s.foldInventory("mac", macRows())
		<-done

		// Final settled fold for mac (after both goroutines finish, regardless of arrival order) MUST include box/sub.
		s.foldInventory("mac", macRows())
		s.fleet.foldMu.Lock()
		got := s.fleet.folds["mac"].children["lead"]
		s.fleet.foldMu.Unlock()
		if got == nil || *got != (proto.SessionChildren{Total: 1, Working: 1}) {
			t.Fatalf("concurrent local fold clobbered cross-machine rollup: %+v", got)
		}
	}
}
