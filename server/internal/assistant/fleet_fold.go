package assistant

import (
	"slices"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- children folded into their parent (#5120), a tab's workers into its lead (#5180), side sessions into the
// session they were started from (#7944) ----
// docs/protocol/protocol.md "Session hierarchy"

// sessionFold is what folding says of one machine's list: the session each folded row folds under (its topmost
// listed ancestor), why for a row that is no child (as: proto.FoldedAsWorker or proto.FoldedAsSide), the side session
// a row folded through one belongs to (side, not set on that side itself), and the rollup each such ancestor carries
// of all the rows folded under it.
type sessionFold struct {
	under    map[string]string
	as       map[string]string
	side     map[string]string
	children map[string]*proto.SessionChildren
}

// tabKey is a host tab of one machine's list (a herdr tab): its space kind, space and slot.
type tabKey struct{ kind, space, slot string }

// foldSessions folds a machine's complete list (its rows as decorated, their states computed). Only the rows the apps
// are shown are listed: not an archived Agent (the hub leaves those out, deleted_sessions.go), nor one in hidden (a
// review's reviewer, which publishList leaves out). A child (a subagent, or a session that names a parent or another
// root, proto.Session.IsChild) has as its parent the listed row its parent_session_id names, else the one its
// asking_parent_session_id names, else its root_session_id's; it folds under the topmost listed ancestor that parent
// leads to. A side session (sideParents, #7944) has as its parent the row its parent_agent_id names and is marked
// proto.FoldedAsSide; a row that folds through a side further up names the nearest one (folded_side). A tab's worker
// (tabLeads, #5180) has its tab's lead as its parent and is marked proto.FoldedAsWorker; a side is in no tab team. An
// orphan (no listed parent) and a row whose ancestors go round in a cycle fold under nothing. Each ancestor that rows
// fold under counts them all, whatever their depth, by their own states, how many of them are its workers, and how
// many are side sessions, by their own states.
func foldSessions(list []proto.Session, hidden map[string]bool) sessionFold {
	listed := func(s *proto.Session) bool { return (!s.Archived || s.Assistant) && !hidden[s.SessionID] }
	at := make(map[string]int, len(list))
	for i := range list {
		if _, dup := at[list[i].SessionID]; !dup && listed(&list[i]) {
			at[list[i].SessionID] = i
		}
	}
	counted := func(i int) bool { j, ok := at[list[i].SessionID]; return ok && j == i }
	sides := sideParents(list, at, counted)
	lead := tabLeads(list, func(i int) bool { _, side := sides[i]; return counted(i) && !side })
	parent := func(i int) int {
		s := &list[i]
		if !s.IsChild() {
			if j, ok := sides[i]; ok {
				return j
			}
			if j, ok := lead[i]; ok {
				return j
			}
			return -1
		}
		root := s.RootSessionID
		if root == s.SessionID {
			root = ""
		}
		for _, id := range [...]string{s.ParentSessionID, s.AskingParentSessionID(), root} {
			if j, ok := at[id]; ok && id != "" && j != i {
				return j
			}
		}
		return -1
	}
	var f sessionFold
	var path []int // the rows walked up from one child, to find a cycle
	for i := range list {
		if at[list[i].SessionID] != i || !listed(&list[i]) {
			continue
		}
		top := i
		path = append(path[:0], i)
		for {
			p := parent(top)
			if p < 0 {
				break
			}
			if slices.Contains(path, p) {
				top = i // a cycle: no topmost ancestor
				break
			}
			path = append(path, p)
			top = p
		}
		if top == i {
			continue
		}
		if f.under == nil {
			f.under, f.children = map[string]string{}, map[string]*proto.SessionChildren{}
		}
		id, root := list[i].SessionID, list[top].SessionID
		f.under[id] = root
		c := f.children[root]
		if c == nil {
			c = &proto.SessionChildren{}
			f.children[root] = c
		}
		c.Total++
		switch list[i].State {
		case proto.StateWorking:
			c.Working++
		case proto.StateNeedsYou:
			c.NeedsYou++
		}
		_, side := sides[i]
		if _, worker := lead[i]; worker || side { // a worker or a side itself, not a row folded through one
			if f.as == nil {
				f.as = map[string]string{}
			}
			if side {
				f.as[id] = proto.FoldedAsSide
				c.Sides++
				switch list[i].State {
				case proto.StateWorking:
					c.SidesWorking++
				case proto.StateNeedsYou:
					c.SidesNeedsYou++
				case proto.StateUnread:
					c.SidesUnread++
				}
			} else {
				f.as[id] = proto.FoldedAsWorker
				c.Workers++
			}
			continue
		}
		// The nearest side on the way up, the topmost row aside (it folds under nothing, so it is no side here).
		for _, p := range path[1 : len(path)-1] {
			if _, ok := sides[p]; ok {
				if f.side == nil {
					f.side = map[string]string{}
				}
				f.side[id] = list[p].SessionID
				break
			}
		}
	}
	return f
}

// sideParents finds the side sessions of a machine's list (#7944): for each, by its index, the index of the row its
// parent_agent_id names. A side is a counted row (the listed rows, each id once) that is no child and no assistant row
// and names another counted row that is no assistant row either; that row need not run. A child keeps folding by its
// own parent fields.
func sideParents(list []proto.Session, at map[string]int, counted func(int) bool) map[int]int {
	var out map[int]int
	for i := range list {
		s := &list[i]
		id := s.ParentAgentID()
		if id == "" || id == s.SessionID || s.Assistant || s.IsChild() || !counted(i) {
			continue
		}
		if j, ok := at[id]; ok && j != i && !list[j].Assistant {
			if out == nil {
				out = map[int]int{}
			}
			out[i] = j
		}
	}
	return out
}

// tabLeads finds the tab teams of a machine's list (#5180): for each worker, by its index, its lead's index. A tab is
// the rows of one space kind, space and slot, both named; a row of the assistant's is in none. Its lead is the one row
// counted (the listed rows, each id once) in its first pane (pane_index 0) that is no child and not offline; a tab with
// none or more than one such row has no lead, its Agents peers. Every other counted row of a tab with a lead that is
// no child (a child folds by its own parent) and not offline (a stopped pane has left the team) is its worker.
func tabLeads(list []proto.Session, counted func(int) bool) map[int]int {
	member := func(i int) (tabKey, bool) {
		s := &list[i]
		if s.Assistant || s.SessionID == "" || s.SpaceID == "" || s.SlotID == "" || !counted(i) || s.IsChild() ||
			s.State == proto.StateOffline {
			return tabKey{}, false
		}
		return tabKey{s.SpaceKind, s.SpaceID, s.SlotID}, true
	}
	var leads map[tabKey]int // the lead's index; -1 for a tab with more than one row in its first pane
	for i := range list {
		if p := list[i].PaneIndex; p == nil || *p != 0 {
			continue
		}
		if k, ok := member(i); ok {
			if leads == nil {
				leads = map[tabKey]int{}
			}
			if _, two := leads[k]; two {
				leads[k] = -1
			} else {
				leads[k] = i
			}
		}
	}
	var out map[int]int
	for i := range list {
		k, ok := member(i)
		if !ok {
			continue
		}
		if j, has := leads[k]; has && j >= 0 && j != i {
			if out == nil {
				out = map[int]int{}
			}
			out[i] = j
		}
	}
	return out
}

// apply puts the fold on the rows: what each folds under, why, and the rollup it carries, nothing on the others. A row
// is given a new hierarchy only where it changes.
func (f sessionFold) apply(list []proto.Session) {
	for i := range list {
		id := list[i].SessionID
		list[i].SetFold(f.under[id], f.as[id], f.side[id], f.children[id])
	}
}

// foldInventory folds machine's complete list (foldSessions, without the reviewers publishList hides), puts the fold on
// its rows and keeps it for the rows decorated on their own (foldSubset) and the cards sent to the apps (withTask)
// until the next complete list.
func (s *Service) foldInventory(machine string, list []proto.Session) {
	var hidden map[string]bool
	for k := range s.reviewerSessions() {
		if k[0] == machine {
			if hidden == nil {
				hidden = map[string]bool{}
			}
			hidden[k[1]] = true
		}
	}
	f := foldSessions(list, hidden)
	f.apply(list)
	s.fleet.foldMu.Lock()
	defer s.fleet.foldMu.Unlock()
	if s.fleet.folds == nil {
		s.fleet.folds = map[string]sessionFold{}
	}
	s.fleet.folds[machine] = f
}

// foldSubset puts on rows decorated without the rest of their machine's list what its last complete list folded them
// as: a single row pushed on its own keeps its folded_under, folded_as, folded_side and children.
func (s *Service) foldSubset(machine string, list []proto.Session) {
	s.fleet.foldMu.Lock()
	f := s.fleet.folds[machine]
	s.fleet.foldMu.Unlock()
	f.apply(list)
}

// foldedUnder is the session machine's session sid folded under in its last complete list, why when it is no child
// (proto.FoldedAsWorker, proto.FoldedAsSide) and the side session it belongs to when it folds through one; "" for
// none.
func (s *Service) foldedUnder(machine, sid string) (under, as, side string) {
	s.fleet.foldMu.Lock()
	defer s.fleet.foldMu.Unlock()
	f := s.fleet.folds[machine]
	return f.under[sid], f.as[sid], f.side[sid]
}
