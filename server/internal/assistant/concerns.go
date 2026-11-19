package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
)

// Concerns (#3561, docs/assistant/todos.md "Concerns", docs/design/proposals/owner-at-a-glance.md G5): something the owner
// raised in the main conversation and wants an answer to is a to-do of kind concern, made by the main conversation
// (todo_create kind concern) or by hand (POST /api/v1/todos {kind: "concern"}). It needs no agent and nothing closes it
// on its own: not the hub's judge, a review, a failed start, nor completing it by hand. It closes only when the outcome
// was told to the owner, written as a decision of source reported (concern_report, or POST …/decisions {source:
// "reported"}): the report and the close are one write (db.ReportConcern). Reopened, it needs another report to close.
// todo.changed carries kind, so an extension tells a concern from a task; the report reaches it as todo.decision.

// errConcernMain: only the main conversation records concerns and reports on them.
var errConcernMain = errors.New("concerns are the main conversation's: only it records them and reports their outcome")

// errConcernNoStore: without the hub's database there is no decision to report a concern's outcome with.
var errConcernNoStore = errors.New("concerns need the hub's database")

// errEmptyReport: a report must say what the owner was told.
var errEmptyReport = errors.New("the report cannot be empty: say what the owner was told")

// isConcern: t is a concern.
func (t Todo) isConcern() bool { return t.Kind == db.KindConcern }

// todoKind reads a kind given by a device or a tool: "" (a to-do) or concern.
func todoKind(s string) (string, error) {
	switch k := strings.ToLower(strings.TrimSpace(s)); k {
	case "", "todo":
		return "", nil
	case db.KindConcern:
		return k, nil
	}
	return "", fmt.Errorf("kind must be todo or concern, not %q", s)
}

// createConcern records a concern of the owner's: origin the main conversation, no agent; plan sets its due date,
// priority and project as for any to-do. It needs the hub's database, or it could never be reported and closed.
func (s *Service) createConcern(ctx context.Context, title, notes string, plan db.TodoEdit, bindings ...*repository.CreateBindings) (Todo, error) {
	return s.createConcernWithImages(ctx, title, notes, plan, nil, bindings...)
}

func (s *Service) createConcernWithImages(ctx context.Context, title, notes string, plan db.TodoEdit, images []db.TodoImage, bindings ...*repository.CreateBindings) (Todo, error) {
	if s.todoDB() == nil {
		return Todo{}, errConcernNoStore
	}
	now := time.Now().UTC()
	row := db.TodoRow{ID: "todo_" + randomHex(8), Title: title, Notes: notes, CreatedAt: now, UpdatedAt: now,
		Origin: s.settings(ctx).Main, Kind: db.KindConcern, Images: images}
	plan.Title, plan.Notes, plan.Done = nil, nil, nil
	plan.Apply(&row, now)
	return s.insertTodo(ctx, row, bindings...)
}

// reportConcern writes report, what the owner was told of concern id's outcome, as its reported decision and closes it
// with it (db.ReportConcern); machine and session are the conversation that told them ("" by hand). closed is false
// when it was closed already (the report is kept). db.ErrTodoNotFound, db.ErrNotConcern as the store says.
func (s *Service) reportConcern(ctx context.Context, id, report, machine, session string) (Todo, bool, error) {
	st := s.todoDB()
	if st == nil {
		return Todo{}, false, errConcernNoStore
	}
	cur, ok := s.todoNow(id)
	if !ok {
		return Todo{}, false, db.ErrTodoNotFound
	}
	if !cur.isConcern() {
		return Todo{}, false, db.ErrNotConcern
	}
	text := decisionText(report)
	if text == "" {
		return Todo{}, false, errEmptyReport
	}
	row := db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: id, Question: decisionText(cur.Title), Choice: text,
		Source: db.DecisionReported, SourceMachine: machine, SourceSession: session, CreatedAt: decisionNow()}
	s.todos.writing.Lock() // memory takes the rows in the order the store committed them (todoBook.writing)
	stored, closed, err := st.ReportConcern(ctx, row, clip(oneLine(text), 300))
	if err != nil {
		s.todos.writing.Unlock()
		return Todo{}, false, err
	}
	out, doneKeys, applied := s.setTodoRow(id, *stored, nil)
	s.todos.writing.Unlock()
	if !applied {
		return Todo{}, false, db.ErrTodoNotFound // deleted meanwhile
	}
	s.todoRowSet(ctx, doneKeys)
	s.todoDecisionEvent(row)
	if !closed && postable(row) {
		// A close already posts the report as its done line (setTodoRow): only a report on a closed one is posted here.
		s.threadPost(id, threadEvent{kind: threadDecision, text: decisionLine(row)})
	}
	s.logf("todo %s: concern reported (closed %v)", id, closed)
	return out, closed, nil
}

// toolConcernReport runs concern_report for conversation self: the main conversation tells the tool what it told the
// owner of a concern's outcome, which closes it.
func (s *Service) toolConcernReport(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	if !s.isMain(ctx, self) {
		return "", errConcernMain
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", fmt.Errorf("the todos can't be read now (%v): try again later", err)
	}
	var a struct {
		ID     string `json:"id"`
		Report string `json:"report"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	t, ok := s.todoNow(strings.TrimSpace(a.ID))
	if !ok {
		return "", fmt.Errorf("no to-do %q: todo_list kind concern has the open concerns", a.ID)
	}
	if !t.isConcern() {
		return "", fmt.Errorf("%s is not a concern: complete a to-do with todo_complete", t.ID)
	}
	out, closed, err := s.reportConcern(ctx, t.ID, a.Report, machine, self)
	if err != nil {
		if errors.Is(err, db.ErrTodoNotFound) {
			return "It was deleted meanwhile: nothing to report on.", nil
		}
		return "", fmt.Errorf("the report could not be saved, the concern stays open: %w", err)
	}
	now, loc := time.Now(), s.location(ctx)
	if !closed {
		return "Report noted; it was already closed: " + todoLine(out, now, loc), nil
	}
	return "Reported and closed: " + todoLine(out, now, loc), nil
}
