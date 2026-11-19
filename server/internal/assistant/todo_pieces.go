package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/db"
)

// A request in pieces (docs/assistant/todos.md "Requests in pieces"). When one thing the owner asked for is split into
// several pieces of work, each piece is a to-do with `part_of`, the to-do that stands for the request (its root), and
// `after`, the pieces of the same request it waits for. A piece that cannot start yet is a to-do with no Agent; when its
// turn comes, task_plan's todo_id runs an Agent on that same to-do, so its id, and every `after` naming it, stay.
//
// The links are the manager's, checked whenever they are written (todo_create, todo_update, task_plan, the apps'
// PATCH): the to-dos named exist, a to-do names neither itself nor a to-do that is itself a piece (one level: a request's
// root is no piece, and a piece has no pieces), `after` names pieces of the same request only, and no `after` closes a
// cycle. The same-request rule holds from both ends: a piece another piece waits for keeps its part_of (moving it to
// another request or clearing it is refused, naming who waits) until that wait is taken out of their after. Refused
// rather than taken out for them: dropping the wait would let them start early without anyone deciding so, and the
// write stays one row. Neither is a foreign key: a deleted to-do leaves an id behind, which the readers skip and the next write of
// that list drops.
//
// Each writer checks a change first, to answer with what is wrong before asking or writing anything, and the write
// itself checks it again while holding todoBook.writing (editTodo, insertTodo), against the to-dos as every earlier
// write left them: two writes that each pass against the graph the other is about to change (A after B and B after A at
// once) can't both be stored.
//
// Nothing here decides when work is split or started: that is the conversation's (its instructions, an extension).

// pieceEdit is a change to a to-do's place in a request; nil leaves that part as it is. Changing part_of without
// giving after empties after: the pieces it waited for belong to the request it left.
type pieceEdit struct {
	PartOf *string
	After  *[]string
}

func (p pieceEdit) empty() bool { return p.PartOf == nil && p.After == nil }

// set reports whether the edit names a request or prerequisites (an edit that only clears them is not "set").
func (p pieceEdit) set() bool {
	return p.PartOf != nil && strings.TrimSpace(*p.PartOf) != "" || p.After != nil && len(*p.After) > 0
}

// pieceError is a change to part_of or after the manager refuses (checkPieces): the caller's to fix, not the store's.
type pieceError struct{ error }

// checkPieces checks edit p of to-do id (id "" for a to-do not made yet, which nothing can name yet) against the to-dos
// as they are, and returns the change to write: trimmed, without repeats, nil fields left as they are. The error, a
// *pieceError, says what is wrong in words a model and a person can act on.
func (s *Service) checkPieces(id string, p pieceEdit) (db.TodoEdit, error) {
	if p.empty() {
		return db.TodoEdit{}, nil
	}
	out, err := s.checkPiecesNow(id, p)
	if errors.Is(err, db.ErrTodoNotFound) {
		return out, err // deleted since the caller read it: not a refusal of the links
	}
	if err != nil {
		return out, &pieceError{err}
	}
	return out, nil
}

func (s *Service) checkPiecesNow(id string, p pieceEdit) (db.TodoEdit, error) {
	var out db.TodoEdit
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	book := s.todos.todos
	var cur Todo // its links now, as the book has them (a write may have changed them since the caller read it)
	if id != "" {
		t, ok := book[id]
		if !ok {
			return out, db.ErrTodoNotFound // deleted since the caller read it: nothing to check, nothing to write
		}
		cur = *t
	}
	partOf := cur.PartOf
	if p.PartOf != nil {
		partOf = strings.TrimSpace(*p.PartOf)
	}
	after, afterGiven := cur.After, p.After != nil
	if afterGiven {
		after = cleanIDs(*p.After)
	} else if partOf != cur.PartOf {
		after, afterGiven = []string{}, true
	}

	if p.PartOf != nil && partOf != "" {
		root, ok := book[partOf]
		switch {
		case partOf == id:
			return out, errors.New("part_of can't name the to-do itself")
		case !ok:
			return out, fmt.Errorf("part_of: no to-do %q (todo_list has their ids)", partOf)
		case root.PartOf != "":
			return out, fmt.Errorf("part_of: %q is itself a piece of %q; name the request's own to-do, %q", partOf, root.PartOf, root.PartOf)
		}
		if id != "" {
			for _, t := range book {
				if t.PartOf == id {
					return out, fmt.Errorf("part_of: this to-do is a request with pieces (%q among them), so it can't be a piece itself", t.ID)
				}
			}
		}
	}
	if id != "" && partOf != cur.PartOf {
		if waiting := waitingFor(book, id, partOf); len(waiting) == 1 {
			return out, fmt.Errorf("part_of: %q waits for this to-do (its after names it), so this to-do can't leave the request; "+
				"first take it out of %q's after, then change part_of", waiting[0], waiting[0])
		} else if len(waiting) > 1 {
			return out, fmt.Errorf("part_of: %s wait for this to-do (their after names it), so this to-do can't leave the request; "+
				"first take it out of their after, then change part_of", quotedIDs(waiting))
		}
	}
	if afterGiven && len(after) > 0 {
		if partOf == "" {
			return out, errors.New("after names pieces of the same request: give part_of too")
		}
		for _, a := range after {
			t, ok := book[a]
			switch {
			case a == id:
				return out, errors.New("after can't name the to-do itself")
			case !ok:
				return out, fmt.Errorf("after: no to-do %q (todo_list has their ids)", a)
			case t.PartOf != partOf:
				return out, fmt.Errorf("after: %q is not a piece of the same request (%q)", a, partOf)
			}
		}
		if id != "" {
			if loop := afterCycle(book, id, after); loop != "" {
				return out, fmt.Errorf("after: %q already waits for this to-do, so waiting for it would never end", loop)
			}
		}
	}
	if p.PartOf != nil {
		out.PartOf = &partOf
	}
	if afterGiven {
		out.After = &after
	}
	return out, nil
}

// afterCycle is the prerequisite in after from which the after links lead back to id ("" when none does): id waiting
// for it would close a cycle. Called with the book read-locked.
func afterCycle(book map[string]*Todo, id string, after []string) string {
	for _, start := range after {
		seen := map[string]bool{}
		next := []string{start}
		for len(next) > 0 {
			at := next[len(next)-1]
			next = next[:len(next)-1]
			if at == id {
				return start
			}
			if seen[at] {
				continue
			}
			seen[at] = true
			if t, ok := book[at]; ok {
				next = append(next, t.After...)
			}
		}
	}
	return ""
}

// waitingFor is the to-dos whose after names id and that would no longer be its siblings were id a piece of partOf
// ("" no piece), sorted: id changing its request leaves them waiting for a piece of another one (or of none), which
// after never names. Called with the book read-locked.
func waitingFor(book map[string]*Todo, id, partOf string) []string {
	var out []string
	for _, t := range book {
		if t.ID != id && slices.Contains(t.After, id) && (partOf == "" || t.PartOf != partOf) {
			out = append(out, t.ID)
		}
	}
	slices.Sort(out)
	return out
}

// quotedIDs is ids as an error names them: "a", "b".
func quotedIDs(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = fmt.Sprintf("%q", id)
	}
	return strings.Join(q, ", ")
}

// cleanIDs is ids trimmed, without empty ones or repeats, in their order; never nil.
func cleanIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// pieceArgs reads part_of and after from a tool's or a PATCH's JSON: absent leaves them, null or "" / [] clears them.
func pieceArgs(partOf, after json.RawMessage) (pieceEdit, error) {
	var p pieceEdit
	if len(partOf) > 0 {
		var v *string
		if err := json.Unmarshal(partOf, &v); err != nil {
			return p, errors.New("part_of: a to-do id, or \"\" to clear it")
		}
		s := ""
		if v != nil {
			s = *v
		}
		p.PartOf = &s
	}
	if len(after) > 0 {
		var v []string
		if err := json.Unmarshal(after, &v); err != nil {
			return p, errors.New("after: a list of to-do ids, or [] to clear it")
		}
		if v == nil {
			v = []string{}
		}
		p.After = &v
	}
	return p, nil
}

// pieceEdit is task t's links as an edit of its to-do: what it gives, nothing for what it leaves out.
func (t PlanTask) pieceEdit() pieceEdit {
	var p pieceEdit
	if strings.TrimSpace(t.PartOf) != "" {
		partOf := t.PartOf
		p.PartOf = &partOf
	}
	if len(cleanIDs(t.After)) > 0 {
		after := slices.Clone(t.After)
		p.After = &after
	}
	return p
}

// recheckPieces is checkPieces again for edit e of to-do id (id "" for a new to-do) as it is written, with
// todoBook.writing held: every earlier link write is in the book by now, and no other can land before this one. It
// returns the links to write, which may differ from e's (part_of changed since: after is emptied with it).
func (s *Service) recheckPieces(id string, e db.TodoEdit) (db.TodoEdit, error) {
	links, err := s.checkPieces(id, pieceEdit{PartOf: e.PartOf, After: e.After})
	if err != nil {
		return links, err
	}
	cur, _ := s.todoNow(id)
	partOf, enforced := cur.PartOf, cur.RequireAcceptedAfter
	if links.PartOf != nil {
		partOf = *links.PartOf
	}
	if e.RequireAcceptedAfter != nil {
		enforced = *e.RequireAcceptedAfter
	}
	if enforced && (partOf != "" || cur.isConcern()) {
		return links, &pieceError{errRequestGateRoot}
	}
	return links, nil
}

// planPieces checks task t's links when its plan is made, against the to-do it will run on (todo_id) or, for a new
// one, as a to-do nothing names yet, and keeps them cleaned.
func (s *Service) planPieces(t *PlanTask) error {
	p := t.pieceEdit()
	if p.empty() {
		t.PartOf, t.After = "", nil
		return nil
	}
	links, err := s.checkPieces(t.ReuseTodo, p)
	if err != nil {
		return err
	}
	if links.PartOf != nil {
		t.PartOf = *links.PartOf
	}
	if links.After != nil {
		t.After = *links.After
	}
	return nil
}

// startPieces writes task t's links on its to-do as it starts. Checked again (and once more by editTodo as it writes):
// the request may have changed since the plan was made; a link that no longer holds is left out (and logged) rather
// than failing a start the owner approved.
func (s *Service) startPieces(ctx context.Context, t PlanTask, todo Todo) (Todo, error) {
	p := t.pieceEdit()
	if p.empty() {
		return todo, nil
	}
	links, err := s.checkPieces(todo.ID, p)
	if err != nil {
		if t.RequestGate != "" {
			return todo, err
		}
		s.logf("task %s: part_of/after left out: %v", todo.ID, err)
		return todo, nil
	}
	out, err := s.editTodo(ctx, todo.ID, links)
	if err != nil {
		if t.RequestGate != "" {
			return todo, err
		}
		s.logf("task %s: part_of/after: %v", todo.ID, err) // a *pieceError when a write since changed the request
		return todo, nil
	}
	return out, nil
}

// pieceLine is how todoLine shows a to-do's place in a request: " (piece of todo_x, after todo_a, todo_b)"; "" for
// none.
func pieceLine(t Todo) string {
	if t.PartOf == "" {
		return ""
	}
	if len(t.After) == 0 {
		return " (piece of " + t.PartOf + ")"
	}
	return " (piece of " + t.PartOf + ", after " + strings.Join(t.After, ", ") + ")"
}
