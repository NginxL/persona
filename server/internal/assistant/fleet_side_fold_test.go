package assistant

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// sideFoldFixtures is shared with the clients' tests (#7944): the manager's fold of side sessions, the rows a roots=1
// list has with their folded states, and where each card goes.
const sideFoldFixtures = "../../../spec/fixtures/side-fold/cases.json"

type sideFoldEntry struct {
	FoldedUnder string                 `json:"folded_under"`
	FoldedAs    string                 `json:"folded_as"`
	FoldedSide  string                 `json:"folded_side"`
	Children    *proto.SessionChildren `json:"children"`
}

type sideFoldCase struct {
	Name  string `json:"name"`
	Input struct {
		Sessions     []proto.Session     `json:"sessions"`
		Interactions []proto.Interaction `json:"interactions"`
		Paged        bool                `json:"paged"`
	} `json:"input"`
	Expected struct {
		Fold map[string]sideFoldEntry `json:"fold"`
		Rows []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"rows"`
		Cards []struct {
			ID   string `json:"id"`
			Home string `json:"home"`
			Chat string `json:"chat"`
		} `json:"cards"`
	} `json:"expected"`
}

// The manager folds the shared fixture's rows as it says, the rows a roots=1 list keeps show the folded states it says,
// and each card's home and chat are what the cards sent to the clients name (#7944).
func TestSideFoldFollowsTheSharedFixtures(t *testing.T) {
	b, err := os.ReadFile(sideFoldFixtures)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version int            `json:"version"`
		Cases   []sideFoldCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &file); err != nil || file.Version != 1 || len(file.Cases) == 0 {
		t.Fatalf("fixtures: version %d, %d cases, %v", file.Version, len(file.Cases), err)
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			s := &Service{}
			list := c.Input.Sessions
			if !c.Input.Paged {
				var machines []string
				byMachine := map[string][]int{}
				for i := range list {
					m := list[i].MachineID
					if _, ok := byMachine[m]; !ok {
						machines = append(machines, m)
					}
					byMachine[m] = append(byMachine[m], i)
				}
				for _, m := range machines {
					rows := make([]proto.Session, 0, len(byMachine[m]))
					for _, i := range byMachine[m] {
						rows = append(rows, list[i])
					}
					s.foldInventory(m, rows)
					for k, i := range byMachine[m] {
						list[i] = rows[k]
					}
				}
			}
			got := map[string]sideFoldEntry{}
			for i := range list {
				r := &list[i]
				e := sideFoldEntry{FoldedUnder: r.FoldedUnder(), FoldedAs: r.FoldedAs(), FoldedSide: r.FoldedSide()}
				if r.Hierarchy != nil && r.Hierarchy.Children != nil {
					kids := r.Children()
					e.Children = &kids
				}
				if e != (sideFoldEntry{}) && !c.Input.Paged {
					got[r.MachineID+"/"+r.SessionID] = e
				}
			}
			if !c.Input.Paged && !reflect.DeepEqual(got, c.Expected.Fold) {
				t.Fatalf("fold\n got %s\nwant %s", jsonOf(got), jsonOf(c.Expected.Fold))
			}
			type shown struct{ ID, State string }
			var rows, want []shown
			for i := range list {
				r := &list[i]
				if r.Archived && !r.Assistant || r.FoldedUnder() != "" {
					continue
				}
				rows = append(rows, shown{r.MachineID + "/" + r.SessionID, r.FoldedState()})
			}
			for _, r := range c.Expected.Rows {
				want = append(want, shown{r.ID, r.State})
			}
			if !reflect.DeepEqual(rows, want) {
				t.Fatalf("rows\n got %+v\nwant %+v", rows, want)
			}
			if len(c.Input.Interactions) != len(c.Expected.Cards) {
				t.Fatalf("%d cards, %d expected", len(c.Input.Interactions), len(c.Expected.Cards))
			}
			for i, ix := range c.Input.Interactions {
				ix = s.withTask(ix)
				home := ix.SessionID
				if ix.FoldedUnder != "" {
					home = ix.FoldedUnder
				}
				chat := home
				switch {
				case ix.FoldedSide != "":
					chat = ix.FoldedSide
				case ix.FoldedAs == proto.FoldedAsSide:
					chat = ix.SessionID
				}
				w := c.Expected.Cards[i]
				if w.ID != ix.ID || w.Home != ix.MachineID+"/"+home || w.Chat != ix.MachineID+"/"+chat {
					t.Fatalf("card %s: home %s chat %s, want %+v", ix.ID, home, chat, w)
				}
			}
		})
	}
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// A side pushed on its own keeps folded_as, a row folded through it keeps folded_side, its parent the sides rollup, and
// the cards sent to the apps name where they go; the next complete list without the parent makes the side a row
// again (#7944).
func TestFoldSubsetAndInteractionsSide(t *testing.T) {
	s := &Service{}
	side := func(state string) proto.Session {
		return foldRow("side", state, func(s *proto.Session) { s.SetParentAgentID("parent") })
	}
	kid := foldRow("kid", proto.StateWorking, func(s *proto.Session) { s.IsSubagent, s.ParentSessionID = true, "side" })
	s.foldInventory("m1", []proto.Session{foldRow("parent", proto.StateIdle, nil), side(proto.StateUnread), kid})
	one := []proto.Session{side(proto.StateWorking), kid}
	s.foldSubset("m1", one)
	if one[0].FoldedUnder() != "parent" || one[0].FoldedAs() != proto.FoldedAsSide || one[0].FoldedSide() != "" {
		t.Fatalf("a side on its own lost its fold: %+v", one[0].Hierarchy)
	}
	if one[1].FoldedUnder() != "parent" || one[1].FoldedAs() != "" || one[1].FoldedSide() != "side" {
		t.Fatalf("a side's child on its own lost its fold: %+v", one[1].Hierarchy)
	}
	parent := []proto.Session{foldRow("parent", proto.StateIdle, nil)}
	if s.foldSubset("m1", parent); parent[0].Children() != (proto.SessionChildren{Total: 2, Working: 1, Sides: 1, SidesUnread: 1}) ||
		parent[0].FoldedState() != proto.StateWorking {
		t.Fatalf("a parent on its own: %+v shows %s", parent[0].Hierarchy, parent[0].FoldedState())
	}
	if ix := s.withTask(proto.Interaction{MachineID: "m1", SessionID: "side"}); ix.FoldedUnder != "parent" || ix.FoldedAs != proto.FoldedAsSide || ix.FoldedSide != "" {
		t.Fatalf("a side's card: %+v", ix)
	}
	if ix := s.withTask(proto.Interaction{MachineID: "m1", SessionID: "kid"}); ix.FoldedUnder != "parent" || ix.FoldedAs != "" || ix.FoldedSide != "side" {
		t.Fatalf("a side's child's card: %+v", ix)
	}
	s.foldInventory("m1", []proto.Session{foldRow("parent", proto.StateIdle, func(s *proto.Session) { s.Archived = true }), side(proto.StateIdle)})
	if ix := s.withTask(proto.Interaction{MachineID: "m1", SessionID: "side"}); ix.FoldedUnder != "" || ix.FoldedAs != "" {
		t.Fatalf("a side of an archived parent: %+v", ix)
	}
}

// The list the hub is told folds a side session under the session it was started from, from the rows the node sends
// (#7944).
func TestPublishedListFoldsSides(t *testing.T) {
	ft := newFleetTestNoStore(t)
	parent := proto.Session{SessionID: "parent", Status: proto.StatusIdle, Cwd: "/w/app", Agent: "claude"}
	side := proto.Session{SessionID: "side", Status: proto.StatusWorking, Cwd: "/w/app", Agent: "claude"}
	side.SetParentAgentID("parent")
	ft.sessions("mac", parent, side)
	var p *proto.Session
	waitFor(t, "the side folded", func() bool {
		list := ft.lastList("mac")
		sd := rowNamed(list, "side")
		p = rowNamed(list, "parent")
		return p != nil && sd != nil && sd.FoldedUnder() == "parent" && sd.FoldedAs() == proto.FoldedAsSide &&
			p.Children() == (proto.SessionChildren{Total: 1, Working: 1, Sides: 1, SidesWorking: 1})
	})
	if p.FoldedState() != proto.StateWorking {
		t.Fatalf("the parent shows %s", p.FoldedState())
	}
}

// The Live Activity counts a folded side session under its parent, the parent once in the state it shows with its
// sides, as the lists do: a side's card stands for the parent, a working side makes it work, an unseen side makes it
// finished unseen. A side whose parent the fleet does not have counts on its own, and a muted side nowhere (#7944).
func TestFleetCountsASideUnderItsParent(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	agent := func(id, state string, mod func(*proto.Session)) fleetInput {
		s := proto.Session{MachineID: "m", SessionID: id, Title: "agent " + id, State: state, UpdatedAt: t0}
		if mod != nil {
			mod(&s)
		}
		return fleetInput{Machine: "box", Session: &s}
	}
	asSide := func(s *proto.Session) { s.SetParentAgentID("parent"); s.SetFold("parent", proto.FoldedAsSide, "", nil) }
	parent := agent("parent", proto.StateIdle, nil)

	// A side that needs you, by its card: the parent's item, the side's card on it.
	ixs := []proto.Interaction{{ID: "ix-side", MachineID: "m", SessionID: "side", Kind: proto.IxPermission, Summary: "npm test", CreatedAt: t0}}
	st := buildFleet([]fleetInput{parent, agent("side", proto.StateNeedsYou, asSide)}, ixs)
	if st.NeedsYou != 1 || st.Working+st.Unread != 0 || len(st.Items) != 1 || st.Items[0].ID != "m/parent" ||
		st.Items[0].Interaction != "ix-side" || st.Items[0].Title != "agent parent" {
		t.Fatalf("a side's card: %+v", st)
	}
	// A card that reached the hub before the side's row says so stands for the parent too.
	if st := buildFleet([]fleetInput{parent, agent("side", proto.StateWorking, asSide)}, ixs); st.NeedsYou != 1 || st.Working != 0 ||
		len(st.Items) != 1 || st.Items[0].ID != "m/parent" {
		t.Fatalf("a side's card before its row: %+v", st)
	}
	// Working, from when the side began.
	working := agent("side", proto.StateWorking, asSide)
	working.TurnStart = t0.Add(-3 * time.Minute)
	if st := buildFleet([]fleetInput{parent, working}, nil); st.Working != 1 || len(st.Items) != 1 || st.Items[0].ID != "m/parent" ||
		st.WorkingSince != float64(t0.Add(-3*time.Minute).Unix()) {
		t.Fatalf("a working side: %+v", st)
	}
	// Finished unseen.
	unseen := agent("side", proto.StateUnread, func(s *proto.Session) { asSide(s); s.UpdatedAt = t0.Add(time.Minute) })
	if st := buildFleet([]fleetInput{parent, unseen}, nil); st.Unread != 1 || st.Working+st.NeedsYou != 0 || st.Items[0].ID != "m/parent" ||
		st.Items[0].Since != float64(t0.Add(time.Minute).Unix()) {
		t.Fatalf("an unseen side: %+v", st)
	}
	// The parent awaiting the user's reply does not show its side working (the list's rule), and its row is not changed.
	awaiting := agent("parent", proto.StateIdle, func(s *proto.Session) { s.AwaitsReply = true })
	if st := buildFleet([]fleetInput{awaiting, working}, nil); st.Working+st.NeedsYou+st.Unread != 0 || awaiting.Session.State != proto.StateIdle {
		t.Fatalf("a parent awaiting a reply: %+v", st)
	}
	// A side's own child that needs you counts for the parent too.
	kid := agent("kid", proto.StateNeedsYou, func(s *proto.Session) {
		s.SourceKind, s.IsSubagent, s.ParentSessionID = proto.SourceRuntime, true, "side"
		s.SetFold("parent", "", "side", nil)
	})
	if st := buildFleet([]fleetInput{parent, agent("side", proto.StateIdle, asSide), kid}, nil); st.NeedsYou != 1 || len(st.Items) != 1 ||
		st.Items[0].ID != "m/parent" {
		t.Fatalf("a side's child: %+v", st)
	}
	// Without its parent (a quiet task's agent) the side counts on its own; muted, nowhere.
	if st := buildFleet([]fleetInput{agent("side", proto.StateWorking, asSide)}, nil); st.Working != 1 || st.Items[0].ID != "m/side" {
		t.Fatalf("a side with no parent in the fleet: %+v", st)
	}
	muted := agent("side", proto.StateNeedsYou, asSide)
	muted.Muted = true
	if st := buildFleet([]fleetInput{parent, muted}, ixs); st.NeedsYou+st.Working+st.Unread != 0 {
		t.Fatalf("a muted side: %+v", st)
	}
	// Nothing folds: the agents and cards are the caller's, as they were.
	plain := []fleetInput{parent, agent("other", proto.StateWorking, nil)}
	if a, x := foldSidesIn(plain, ixs); &a[0] != &plain[0] || &x[0] != &ixs[0] {
		t.Fatal("a fleet without sides was copied")
	}
}
