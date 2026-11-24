package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
)

// unknownActionError names an action id the minutes (or the recap) don't have; the module checks the ids first.
type unknownActionError string

func (u unknownActionError) Error() string { return "no action item " + string(u) }

// actionTodos makes a to-do of each of the actions named (ids in list; one that has one already keeps it): the to-dos
// (never nil without an error) and what to record (action id → to-do, MinutesTaskKey → to-do). An error while making
// them still says what to record of those made before it; an unknown action or to-dos that can't be read make none
// (made nil). todoID is the to-do an action without one becomes, note its notes.
func (s *Service) actionTodos(ctx context.Context, list []kb.MinutesAction, actions []string, todoID func(aid string) string,
	note func(kb.MinutesAction) string) (out []Todo, made, byTask map[string]string, err error) {
	byID := map[string]kb.MinutesAction{}
	for _, a := range list {
		byID[a.ID] = a
	}
	for _, a := range actions {
		if _, ok := byID[a]; !ok {
			return nil, nil, nil, unknownActionError(a)
		}
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, nil, nil, err
	}
	made, byTask = map[string]string{}, map[string]string{}
	for _, aid := range dedupe(actions) {
		a := byID[aid]
		tid := a.TodoID
		if tid == "" {
			tid = todoID(aid)
		}
		if t, ok := s.todoNow(tid); ok {
			out, made[aid], byTask[kb.MinutesTaskKey(a)] = append(out, t), tid, tid
			continue
		}
		if a.TodoID != "" { // made before and deleted since: the user's choice stands
			continue
		}
		t, err := s.insertTodo(ctx, minutesTodoRow(tid, a, note(a)))
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" { // made a moment ago by another manager on the same store
			t, err = Todo{ID: tid, Title: minutesTodoTitle(a)}, nil
		}
		if err != nil {
			return out, made, byTask, err
		}
		out, made[aid], byTask[kb.MinutesTaskKey(a)] = append(out, t), tid, tid
	}
	if out == nil {
		out = []Todo{}
	}
	return out, made, byTask, nil
}

// minutesTodoID is the to-do an action of a version of a recording's minutes becomes.
func minutesTodoID(recording string, version int, action string) string {
	return actionTodoID("todo_mtg_", recording, version, action)
}

// actionTodoID is the to-do (prefix, then a hash) an action of a version of a recording's minutes or recap becomes.
func actionTodoID(prefix, recording string, version int, action string) string {
	h := sha256.Sum256([]byte(recording + "\x00" + strconv.Itoa(version) + "\x00" + action))
	return prefix + hex.EncodeToString(h[:8])
}

func minutesTodoTitle(a kb.MinutesAction) string {
	if a.Who != "" {
		return a.Who + ": " + a.What
	}
	return a.What
}

// minutesActionLines is what kb_get adds to minutes m of recording id: each action item (who, what, the deadline as
// said) with what became of it, a suggestion the user has not confirmed or its to-do as it is now (open and when due,
// done, cancelled, deleted), so "what is still open from that meeting" needs no other tool; "" without action items.
func (s *Service) minutesActionLines(ctx context.Context, id string, m *kb.Minutes) string {
	return s.actionLines(ctx, "\nAction items as they stand (one becomes a to-do only when the user confirms it):\n", m.Actions,
		func(aid string) string { return minutesTodoID(id, m.Version, aid) })
}

// actionLines is head, then a line per action of list with what became of it (minutesActionLines); todoID is the
// to-do an action without one would have become. "" without actions.
func (s *Service) actionLines(ctx context.Context, head string, list []kb.MinutesAction, todoID func(aid string) string) string {
	if len(list) == 0 {
		return ""
	}
	loaded := s.ensureTodosLoaded(ctx) == nil
	now, loc := time.Now(), s.location(ctx)
	var b strings.Builder
	b.WriteString(head)
	for _, a := range list {
		b.WriteString("- " + a.ID + " " + minutesTodoTitle(a))
		switch {
		case a.By != "" && a.DueDate != "" && a.By != a.DueDate:
			b.WriteString(" (by " + a.By + ", " + a.DueDate + ")")
		case a.By != "" || a.DueDate != "":
			b.WriteString(" (by " + cmp.Or(a.By, a.DueDate) + ")")
		}
		tid := a.TodoID
		if tid == "" {
			tid = todoID(a.ID) // made, though writing it into the minutes failed
		}
		t, ok := s.todoNow(tid)
		b.WriteString(" → ")
		switch {
		case !loaded:
			b.WriteString("to-do state unknown (the to-dos can't be read now)")
		case ok:
			b.WriteString("to-do " + t.ID)
			if t.Title != minutesTodoTitle(a) {
				fmt.Fprintf(&b, " %q", clip(oneLine(t.Title), 120))
			}
			switch {
			case t.Cancelled():
				b.WriteString(" cancelled")
			case t.DoneAt != nil:
				b.WriteString(" done " + t.DoneAt.In(loc).Format("2006-01-02"))
			default:
				b.WriteString(" open" + todoDue(t, now, loc))
			}
		case a.TodoID != "":
			b.WriteString("its to-do " + a.TodoID + " was deleted")
		default:
			b.WriteString("suggested only, not a to-do")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// minutesTodoRow is the plain to-do (no conversation, no task) action a becomes: due on its date when it has one.
func minutesTodoRow(id string, a kb.MinutesAction, notes string) db.TodoRow {
	now := time.Now().UTC()
	row := db.TodoRow{ID: id, Title: minutesTodoTitle(a), Notes: strings.TrimSpace(notes), CreatedAt: now, UpdatedAt: now}
	if d, err := time.Parse("2006-01-02", a.DueDate); err == nil {
		db.TodoEdit{SetDue: true, DueDate: &d}.Apply(&row, now)
	}
	return row
}

// dedupe is list without repeats, in order.
func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range list {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// dayRecapTodoID is the to-do an action of a version of a day recording's recap becomes.
func dayRecapTodoID(recording string, version int, action string) string {
	return actionTodoID("todo_day_", recording, version, action)
}

// recapActionLines is what kb_get adds to recap rc of day recording id: each suggested to-do with what became of it,
// as minutesActionLines says a meeting's.
func (s *Service) recapActionLines(ctx context.Context, id string, rc *kb.DayRecap) string {
	return s.actionLines(ctx, "\nSuggested to-dos as they stand (one becomes a to-do only when the user confirms it):\n", rc.Actions,
		func(aid string) string { return dayRecapTodoID(id, rc.Version, aid) })
}

// actionTodo is whether the to-do made of an action item is open or done: only that of it, never its title, notes or
// thread. suggestion: the action was never made a to-do; "": its to-do is not known (deleted, or unreadable now).
func (s *Service) actionTodo(ctx context.Context, todoID string) string {
	if todoID == "" {
		return "suggestion"
	}
	t, ok, err := s.todo(ctx, todoID)
	switch {
	case err != nil || !ok:
		return ""
	case t.DoneAt != nil:
		return "done"
	}
	return "open"
}
