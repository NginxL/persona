package proto

import (
	"slices"
	"strings"
	"time"
)

// IsCodexSubagent identifies delegated tasks from source metadata, even when
// their parent is not in the current session list. Titles never imply ancestry.
func (s Session) IsCodexSubagent() bool {
	if s.Agent != "codex" && !strings.HasPrefix(s.SourceKind, "codex_") {
		return false
	}
	return s.IsSubagent || s.ParentSessionID != "" || s.ParentThreadID != "" ||
		(s.RootSessionID != "" && s.RootSessionID != s.SessionID)
}

// IsSubtask says whether a session is listed and counted under the session that started it rather than on its own:
// a Codex subagent, or a runtime's own session that names its parent, whichever agent it runs (SourceRuntime: an
// agent another Paseo agent created, #4494). Unlike a Codex subagent, which its parent drives, such a runtime session
// can need the user. While it does it is no subtask: every place that counts sessions has it, and none folds it
// away. (The node lists it without its parent then; a card can still reach the hub before that row does.)
func (s Session) IsSubtask() bool {
	if s.SourceKind == SourceRuntime {
		return (s.IsSubagent || s.ParentSessionID != "") && s.State != StateNeedsYou
	}
	return s.IsCodexSubagent()
}

// SessionHierarchy is a Session's place among the sessions that started one another (docs/protocol/protocol.md
// "Session hierarchy"), when it began, and its completed task ownership. Each member is a key of the row itself on
// the wire. A Session holds it by pointer (Session.Hierarchy, nil when every member is empty): these sparse fields
// inline would take every row past its size class (TestSessionFitsItsSizeClass).
type SessionHierarchy struct {
	// CompletedTodo is display-only task ownership. Keep this sparse reference out of every Session's hot row.
	CompletedTodo *TodoRef `json:"completed_todo,omitempty"`
	// ParentAgentID is the session on the same machine a side session was started from (docs/protocol/protocol.md
	// "Session hierarchy"). Unlike parent_session_id it makes no child: the session keeps its own state, sends and
	// unread, and is no subagent. The manager folds it under that session's topmost listed row as a side session
	// (FoldedAsSide, #7944). Set by the node: a Claude desktop app's side session names the session it was started from,
	// while that one is listed too (claudedesktop.go, #5935).
	ParentAgentID string `json:"parent_agent_id,omitempty"`
	// StartedAt is when the session began: its transcript's first line, a runtime's created time. Set by the node where
	// it knows it (Codex rollouts, runtime rows); zero when it does not.
	StartedAt time.Time `json:"started_at,omitzero"`
	// AskingParentSessionID names the parent of a runtime child that waits for the user (status blocked). Such a row
	// has is_subagent, parent_session_id and root_session_id cleared, so that apps which fold every child under its
	// parent still list it while it needs the user (#4494); this keeps its parent known to those that fold it as a card
	// in the parent's chat. Set by the node.
	AskingParentSessionID string `json:"asking_parent_session_id,omitempty"`
	// FoldedUnder is the listed session on the same machine this child folds under: its topmost listed ancestor. Set by
	// the manager (foldSessions, assistant/fleet_fold.go); empty for a session that is not a child, an orphan and a
	// member of a cycle.
	FoldedUnder string `json:"folded_under,omitempty"`
	// FoldedAs says why a row that is no child folds under FoldedUnder: FoldedAsWorker for a worker of a tab's lead
	// (#5180), FoldedAsSide for a side session (#7944). Set by the manager beside FoldedUnder; empty for a child, whose
	// own fields say why.
	FoldedAs string `json:"folded_as,omitempty"`
	// FoldedSide is the nearest side session a folded row belongs to when it folds through one and is not itself that
	// side (a side's own Codex children): its card and child card belong to that side's chat, not FoldedUnder's (#7944).
	// Set by the manager beside FoldedUnder.
	FoldedSide string `json:"folded_side,omitempty"`
	// Children counts the sessions folded under this one, all depths, by their own states. Set by the manager on the
	// topmost listed ancestor only.
	Children *SessionChildren `json:"children,omitempty"`
	// EngineHistory is the conversation's earlier stretches, oldest first, when it moved between Claude Code and Codex
	// (#5054, docs/protocol/protocol.md "Switching a running Agent to another engine"): the session that ran each and
	// when it was switched away from. Only the session running the conversation now carries it; the sessions it names
	// are left out of the machine's list (one conversation, one row), and their transcripts stay readable and resumable.
	// A session may be named more than once (switched back to and away again), each time for another stretch. Set by
	// the node.
	EngineHistory []EngineLeg `json:"engine_history,omitempty"`
}

// EngineLeg is one earlier stretch of a conversation (SessionHierarchy.EngineHistory): the session that ran it, its
// engine ("claude" or "codex"), and when the conversation was switched away from it. The stretch began when the one
// before it ended (the first: when its session began).
type EngineLeg struct {
	SessionID string    `json:"session_id"`
	Agent     string    `json:"agent"`
	Until     time.Time `json:"until"`
}

// SameEngineHistory: a and b name the same stretches.
func SameEngineHistory(a, b []EngineLeg) bool {
	return slices.EqualFunc(a, b, func(x, y EngineLeg) bool {
		return x.SessionID == y.SessionID && x.Agent == y.Agent && x.Until.Equal(y.Until)
	})
}

// FoldedAsWorker is SessionHierarchy.FoldedAs for a worker of a tab team: another running Agent in the tab whose first
// pane runs the lead it folds under (docs/protocol/protocol.md "Session hierarchy", #5180).
const FoldedAsWorker = "worker"

// FoldedAsSide is SessionHierarchy.FoldedAs for a side session (a Claude desktop app's side chat, /btw question or
// spawned session, parent_agent_id): its own step up is a side link, and it lives in the chat of the row it folds under
// (docs/protocol/protocol.md "Session hierarchy", #7944).
const FoldedAsSide = "side"

// SessionChildren is the rollup a parent row carries of the children folded under it (SessionHierarchy.Children).
type SessionChildren struct {
	Total    int `json:"total"`
	Working  int `json:"working,omitempty"`
	NeedsYou int `json:"needs_you,omitempty"`
	// Workers is how many of them are the row's tab workers (folded_as "worker"), counted in Total too.
	Workers int `json:"workers,omitempty"`
	// Sides is how many of them are side sessions (folded_as "side"), counted in Total too; SidesWorking,
	// SidesNeedsYou and SidesUnread count those sides by their own states (#7944).
	Sides         int `json:"sides,omitempty"`
	SidesWorking  int `json:"sides_working,omitempty"`
	SidesNeedsYou int `json:"sides_needs_you,omitempty"`
	SidesUnread   int `json:"sides_unread,omitempty"`
}

func (h *SessionHierarchy) empty() bool {
	return h.ParentAgentID == "" && h.StartedAt.IsZero() && h.AskingParentSessionID == "" && h.FoldedUnder == "" && h.FoldedAs == "" &&
		h.FoldedSide == "" &&
		h.Children == nil && len(h.EngineHistory) == 0 && h.CompletedTodo == nil
}

// CompletedTodo is the last completed association for navigation, never active work.
func (s *Session) CompletedTodo() *TodoRef {
	if s.Hierarchy == nil {
		return nil
	}
	return s.Hierarchy.CompletedTodo
}

// SetCompletedTodo preserves the copy-on-write sparse metadata shared by session copies.
func (s *Session) SetCompletedTodo(todo *TodoRef) {
	if s.CompletedTodo() == todo {
		return
	}
	h := s.hierarchy()
	h.CompletedTodo = todo
	s.setHierarchy(h)
}

// hierarchy is a copy of s's hierarchy to change, its children copied too: the one s holds is shared.
func (s *Session) hierarchy() SessionHierarchy {
	if s.Hierarchy == nil {
		return SessionHierarchy{}
	}
	h := *s.Hierarchy
	if h.Children != nil {
		c := *h.Children
		h.Children = &c
	}
	h.EngineHistory = slices.Clone(h.EngineHistory)
	return h
}

// setHierarchy makes h the session's, nil when it is empty.
func (s *Session) setHierarchy(h SessionHierarchy) {
	if h.empty() {
		s.Hierarchy = nil
		return
	}
	s.Hierarchy = &h
}

// ParentAgentID is the session this one is drawn under while staying its own (SessionHierarchy.ParentAgentID); "" when
// none.
func (s *Session) ParentAgentID() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.ParentAgentID
}

// SetParentAgentID sets the session this one is drawn under ("" for none), with no new hierarchy when it already is.
func (s *Session) SetParentAgentID(id string) {
	if s.ParentAgentID() == id {
		return
	}
	h := s.hierarchy()
	h.ParentAgentID = id
	s.setHierarchy(h)
}

// StartedAt is when the session began (SessionHierarchy.StartedAt); zero when not known.
func (s *Session) StartedAt() time.Time {
	if s.Hierarchy == nil {
		return time.Time{}
	}
	return s.Hierarchy.StartedAt
}

// AskingParentSessionID is the parent of a blocked runtime child (SessionHierarchy.AskingParentSessionID).
func (s *Session) AskingParentSessionID() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.AskingParentSessionID
}

// FoldedUnder is the listed session this one folds under (SessionHierarchy.FoldedUnder); "" when none.
func (s *Session) FoldedUnder() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.FoldedUnder
}

// FoldedAs says why this row folds under FoldedUnder when it is no child (SessionHierarchy.FoldedAs); "" otherwise.
func (s *Session) FoldedAs() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.FoldedAs
}

// FoldedSide is the side session this folded row belongs to when it is not that side (SessionHierarchy.FoldedSide);
// "" when none.
func (s *Session) FoldedSide() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.FoldedSide
}

// Children is the rollup of the sessions folded under this one; the zero value when none are.
func (s *Session) Children() SessionChildren {
	if s.Hierarchy == nil || s.Hierarchy.Children == nil {
		return SessionChildren{}
	}
	return *s.Hierarchy.Children
}

// EngineHistory is the conversation's earlier stretches this session continues (SessionHierarchy.EngineHistory),
// oldest first; nil when it never moved between engines. Shared with the row's other copies: not to be changed.
func (s *Session) EngineHistory() []EngineLeg {
	if s.Hierarchy == nil {
		return nil
	}
	return s.Hierarchy.EngineHistory
}

// SetEngineHistory sets the conversation's earlier stretches (nil: none), with no new hierarchy when it already has
// them.
func (s *Session) SetEngineHistory(legs []EngineLeg) {
	if SameEngineHistory(s.EngineHistory(), legs) {
		return
	}
	h := s.hierarchy()
	h.EngineHistory = slices.Clone(legs)
	s.setHierarchy(h)
}

// SetStartedAt sets when the session began (zero: not known), with no new hierarchy when it is already so.
func (s *Session) SetStartedAt(t time.Time) {
	if s.StartedAt().Equal(t) && s.StartedAt().Location() == t.Location() {
		return
	}
	h := s.hierarchy()
	h.StartedAt = t
	s.setHierarchy(h)
}

// SetAskingParentSessionID sets the parent a blocked runtime child names ("" for none).
func (s *Session) SetAskingParentSessionID(id string) {
	if s.AskingParentSessionID() == id {
		return
	}
	h := s.hierarchy()
	h.AskingParentSessionID = id
	s.setHierarchy(h)
}

// SetFold sets what folding says of the session: the session it folds under ("" for none), why when it is no child
// (as: FoldedAsWorker or FoldedAsSide, "" for none), the side session it belongs to when it is not that side (side, ""
// for none) and the rollup of those folded under it (nil for none). Nothing new is made when the session already says
// so.
func (s *Session) SetFold(under, as, side string, children *SessionChildren) {
	cur := s.Hierarchy
	if cur == nil && under == "" && as == "" && side == "" && children == nil {
		return
	}
	if cur != nil && cur.FoldedUnder == under && cur.FoldedAs == as && cur.FoldedSide == side &&
		(cur.Children == nil) == (children == nil) && (children == nil || *cur.Children == *children) {
		return
	}
	h := s.hierarchy()
	h.FoldedUnder, h.FoldedAs, h.FoldedSide = under, as, side
	h.Children = nil
	if children != nil {
		c := *children
		h.Children = &c
	}
	s.setHierarchy(h)
}

// IsChild says whether the session was started by another one, as the hierarchy reads it: a subagent, a session that
// names its parent (also while it waits for the user, AskingParentSessionID) or a root other than itself.
func (s *Session) IsChild() bool {
	return s.IsSubagent || s.ParentSessionID != "" || s.AskingParentSessionID() != "" ||
		(s.RootSessionID != "" && s.RootSessionID != s.SessionID)
}

// FoldedState is the state a row shows with its folded children (docs/protocol/protocol.md "Session hierarchy"): it
// needs the user when it or one of them does; it works when one of them works, unless it has a result not looked at
// yet or awaits the user's reply; it is unread when one of its side sessions is and it does not work itself (sides
// only: a Codex child's or a worker's result is its parent's to report, #7944); otherwise its own.
func (s *Session) FoldedState() string {
	c := s.Children()
	switch {
	case s.State == StateNeedsYou || c.NeedsYou > 0:
		return StateNeedsYou
	case c.Working > 0 && s.State != StateUnread && !(s.State == StateIdle && s.AwaitsReply):
		return StateWorking
	case c.SidesUnread > 0 && s.State != StateWorking:
		return StateUnread
	}
	return s.State
}
