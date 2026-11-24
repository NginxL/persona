package assistant

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// Card rules, rule 2's choice A+B: a task a conversation handed out that nothing works on does not count toward the
// owner's badge (the web's ownerNeeds and the iOS badge leave it out: assigning it is the supervisors' work, not the
// owner's), and once a day the main conversation is given the list to assign (B), with the card ledger's report
// (reportCardLedger). The owner's own to-dos (no origin) are theirs and are never listed here.

// unattendedListed bounds the tasks the report names; the rest are counted.
const unattendedListed = 10

// unattendedHandedOut are the open tasks a conversation handed out that nothing works on: no Agent linked and not
// released or dismissed, no open piece (PartOf) carrying it, not a concern, not a failed start (its supervisor retries
// it), not put off to Someday and not quiet. Oldest first.
func (s *Service) unattendedHandedOut() []Todo {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	carried := map[string]bool{}
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && t.PartOf != "" {
			carried[t.PartOf] = true
		}
	}
	var out []Todo
	for _, t := range s.todos.todos {
		if t.DoneAt != nil || t.Origin == "" || t.StartError != "" || t.Quiet || t.isConcern() || t.SomedayUntil != nil ||
			carried[t.ID] || slices.ContainsFunc(t.Agents, TodoAgent.active) {
			continue
		}
		out = append(out, Todo{ID: t.ID, Title: t.Title, Origin: t.Origin, CreatedAt: t.CreatedAt})
	}
	slices.SortFunc(out, func(a, b Todo) int { return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID)) })
	return out
}

// unattendedReport is the main conversation's lines on list (unattendedHandedOut); "" for none.
func unattendedReport(list []Todo) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Handed-out tasks nothing works on: %d. They are not counted for the owner; assign each to a supervisor, start it again, or close it:", len(list))
	for i, t := range list {
		if i == unattendedListed {
			fmt.Fprintf(&b, "\n- and %d more", len(list)-unattendedListed)
			break
		}
		fmt.Fprintf(&b, "\n- %s %q (from %s)", t.ID, clip(oneLine(t.Title), 60), t.Origin)
	}
	return b.String()
}
