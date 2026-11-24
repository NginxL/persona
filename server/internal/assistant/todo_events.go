package assistant

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// What the extensions are told of the todos (#5279, docs/extensions/extensions.md "Events"): todo.changed when a todo is created,
// deleted, or its title, whether it is done, its supervisor (#3484) or its chat thread changes (each carries its kind:
// concern for one of the owner's concerns, #3561), and todo.decision for every decision written on
// one. The hub hands them only to the extensions that subscribed and were granted todos.read (extmgr/todo_events.go);
// this side only says what happened, and nothing is sent while the hub carries no extensions (todoEventPublisher), nor
// of a todo an extension is not shown (todoChangedFlush, todoSendEvents, #10941). One whose verdict cannot be told yet
// (todoUntold) is not dropped: it is judged again a while later (todoRetryAfter), and told once it can be.

// todoChangedSettle is how long a change waits for the rest: what happens to todos within it is told once, as the
// todos are at its end.
const todoChangedSettle = time.Second

// todoEventLimit bounds what waits to be handed to the hub and the todo.decision events that wait for their verdict
// (past it the oldest is dropped), and the todos whose todo.changed waits for its verdict (past it, by id, the rest is
// told nothing of what changed so far).
const todoEventLimit = 512

// todoRetryAfter is how long what waits for its verdict waits before it is judged again, doubled each time it still
// cannot be told, up to todoRetryMost.
const (
	todoRetryAfter = 30 * time.Second
	todoRetryMost  = 10 * time.Minute
)

// todoRetryDelay is the wait before the tries-th judgement again (tries from 0).
func todoRetryDelay(tries int) time.Duration {
	d := todoRetryAfter
	for range min(tries, 8) {
		d *= 2
	}
	return min(d, todoRetryMost)
}

// todoEventPublisher is optional for in-process embedders. Production's BusHub publishes these two typed events; they
// never enter the public ws.broadcast path.
type todoEventPublisher interface {
	TodoEvent(event string, params any) error
}

func (b *BusHub) TodoEvent(event string, params any) error { return b.notifyHub(event, params) }

// todoMark is what a change of a todo is told by: what todo.changed carries and whether it is done.
type todoMark struct {
	title          string
	done           bool
	failed         bool
	bound          bool
	thread         TodoThread
	supervisor     string
	kind           string
	hasResponsible bool
	responsible    TodoResponsible
}

func markOf(t *Todo) todoMark {
	m := todoMark{title: t.Title, done: t.DoneAt != nil, failed: t.StartError != "", kind: t.Kind}
	if t.Supervision != nil {
		m.supervisor = t.Supervision.Supervisor
	}
	if t.Thread != nil {
		m.bound, m.thread = true, *t.Thread
	}
	if t.Responsible != nil {
		m.hasResponsible, m.responsible = true, *t.Responsible
	}
	return m
}

func (m todoMark) eventThread() *assistantwire.TodoEventThread {
	if !m.bound {
		return nil
	}
	return &assistantwire.TodoEventThread{Channel: m.thread.Channel, ChatID: m.thread.ChatID, ThreadID: m.thread.ThreadID, Title: m.thread.Title}
}

func (m todoMark) eventResponsible() *assistantwire.TodoEventResponsible {
	if !m.hasResponsible {
		return nil
	}
	return &assistantwire.TodoEventResponsible{
		Kind:      m.responsible.Kind,
		Name:      m.responsible.Name,
		MachineID: m.responsible.MachineID,
		SessionID: m.responsible.SessionID,
	}
}

type todoNotice struct {
	event  string
	params any
}

// todoEvents is what the todos have waiting to be told to the hub. marks is the todos as the extensions were last told
// of them (nil until the todos are loaded: nothing is told of the todos as they were found).
type todoEvents struct {
	mu       sync.Mutex // marks, armed, gone, retryArmed, retries
	marks    map[string]todoMark
	armed    bool            // a flush is scheduled
	gone     map[string]Todo // the todos deleted since the last flush read the todos, as they last were (todoGone)
	flushing sync.Mutex
	after    func(d time.Duration, f func()) // tests replace the timer

	// retryArmed: a flush is scheduled for the todos left untold (todoRetried), retries the ones in a row so far
	// (todoRetryDelay); a change in between is still told after todoChangedSettle.
	retryArmed bool
	retries    int

	qmu     sync.Mutex // pending, running, dropped, held, heldArmed, heldTries
	pending []todoNotice
	running bool
	dropped bool // the log has said events are dropped, and the queue has not been empty since
	// held are the todo.decision events whose todo's verdict could not be told, in order, judged again once
	// heldArmed's wait is over (todoHold); heldTries how many times in a row they were (todoRetryDelay).
	held      []todoNotice
	heldArmed bool
	heldTries int
}

func (e *todoEvents) later(d time.Duration, f func()) {
	if e.after != nil {
		e.after(d, f)
		return
	}
	time.AfterFunc(d, f)
}

func (s *Service) todoEventsOn() bool {
	_, ok := s.hub.(todoEventPublisher)
	return ok
}

// todoMarksBaseline takes the todos as they were loaded for what the extensions are told of them; the loader holds the
// todos' lock.
func (s *Service) todoMarksBaseline(todos map[string]*Todo) {
	marks := make(map[string]todoMark, len(todos))
	for id, t := range todos {
		marks[id] = markOf(t)
	}
	e := &s.tevents
	e.mu.Lock()
	e.marks = marks
	e.mu.Unlock()
}

// todoGone keeps todo t, which is being deleted, for the todo.changed that tells of it: todoChangedFlush judges it as
// it last was. The caller holds the todos' lock.
func (s *Service) todoGone(t *Todo) {
	if !s.todoEventsOn() {
		return
	}
	e := &s.tevents
	e.mu.Lock()
	if e.marks != nil {
		if e.gone == nil {
			e.gone = map[string]Todo{}
		}
		e.gone[t.ID] = copyTodoLocked(t)
	}
	e.mu.Unlock()
}

// todoChangedSoon is called whenever the todos changed ({"type":"todos"}): it schedules telling the extensions what
// differs from what they were told, once the changes of the next todoChangedSettle are in. Reading the differences from
// the todos themselves means no writer of a todo has to remember it.
func (s *Service) todoChangedSoon() {
	if !s.todoEventsOn() {
		return
	}
	e := &s.tevents
	e.mu.Lock()
	if e.armed || e.marks == nil {
		e.mu.Unlock()
		return
	}
	e.armed = true
	e.mu.Unlock()
	e.later(todoChangedSettle, s.todoChangedFlush)
}

func (s *Service) todoChangedFlush() {
	e := &s.tevents
	e.mu.Lock()
	e.armed = false // before the todos are read: a change from here on schedules the next flush
	e.mu.Unlock()
	s.todoFlush()
}

// todoFlush tells the extensions what differs from what they were told (todoChangedFlush, and todoRetried's retry).
func (s *Service) todoFlush() {
	e := &s.tevents
	e.flushing.Lock()
	defer e.flushing.Unlock()
	s.todos.mu.RLock()
	cur := make(map[string]todoMark, len(s.todos.todos))
	for id, t := range s.todos.todos {
		cur[id] = markOf(t)
	}
	s.todos.mu.RUnlock()
	e.mu.Lock()
	prev := e.marks
	e.marks = cur
	gone := map[string]Todo{}
	for id, t := range e.gone {
		if _, there := cur[id]; !there { // deleted before the todos were read: told now (or never known)
			gone[id] = t
			delete(e.gone, id)
		}
	}
	e.mu.Unlock()
	if prev == nil {
		return
	}
	ids := make([]string, 0, len(cur)+len(prev))
	for id := range cur {
		ids = append(ids, id)
	}
	for id := range prev {
		if _, ok := cur[id]; !ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	// What is told is judged once, a deleted todo as it last was: nothing of a todo an extension is not shown. One whose
	// verdict cannot be told yet keeps what it was last told as its mark (a deleted one is kept to be judged as it last
	// was), so the next flush, which a retry brings, tells what changed since.
	var tell []string
	for _, id := range ids {
		if now, there := cur[id]; !there || now != prev[id] {
			tell = append(tell, id)
		}
	}
	if len(tell) == 0 {
		s.todoRetried(false)
		return
	}
	ctx, cancel := context.WithTimeout(s.runCtx(), todoNoticeTimeout)
	verdicts := s.todoVerdicts(ctx, tell, gone, nil)
	cancel()
	untold, dropped := 0, 0
	for _, id := range ids {
		now, there := cur[id]
		was, known := prev[id]
		switch verdicts[id] {
		case todoHidden:
			continue
		case todoUntold:
			if untold++; untold > todoEventLimit {
				dropped++ // its mark is what it is now: nothing waits of it
				continue
			}
			e.mu.Lock()
			if known {
				e.marks[id] = was
			} else {
				delete(e.marks, id)
			}
			if t, ok := gone[id]; ok && !there {
				if e.gone == nil {
					e.gone = map[string]Todo{}
				}
				e.gone[id] = t
			}
			e.mu.Unlock()
			continue
		}
		switch {
		case there && !known:
			s.todoQueue(todoNotice{assistantwire.EventTodoChanged, assistantwire.TodoChangedEvent{
				ID: id, Title: now.title, Thread: now.eventThread(), Done: now.done, Supervisor: now.supervisor, Kind: now.kind, Responsible: now.eventResponsible(), What: assistantwire.TodoCreated}})
		case there && now != was:
			s.todoQueue(todoNotice{assistantwire.EventTodoChanged, assistantwire.TodoChangedEvent{
				ID: id, Title: now.title, Thread: now.eventThread(), Done: now.done, Supervisor: now.supervisor, Kind: now.kind, Responsible: now.eventResponsible(), What: assistantwire.TodoUpdated}})
		case !there:
			s.todoQueue(todoNotice{assistantwire.EventTodoChanged, assistantwire.TodoChangedEvent{
				ID: id, Title: was.title, Thread: was.eventThread(), Done: was.done, Supervisor: was.supervisor, Kind: was.kind, Responsible: was.eventResponsible(), What: assistantwire.TodoDeleted}})
		}
	}
	if dropped > 0 {
		s.logf("todos: %d todos wait for their verdict: what changed of %d more is dropped", todoEventLimit, dropped)
	}
	s.todoRetried(untold > 0)
}

// todoRetried says whether a flush left a todo untold: if so, another flush judges it again after todoRetryDelay
// (a change's flush in between judges it too); if not, the next wait starts from todoRetryAfter again.
func (s *Service) todoRetried(untold bool) {
	e := &s.tevents
	e.mu.Lock()
	if !untold {
		e.retries = 0
		e.mu.Unlock()
		return
	}
	if e.retryArmed {
		e.mu.Unlock()
		return
	}
	delay := todoRetryDelay(e.retries)
	e.retries++
	e.retryArmed = true
	e.mu.Unlock()
	e.later(delay, func() {
		e.mu.Lock()
		e.retryArmed = false
		e.mu.Unlock()
		if s.runCtx().Err() == nil { // a manager that stopped judges nothing again
			s.todoFlush()
		}
	})
}

// todoDecisionEvent tells the extensions of a decision just written (every source: one that came from the todo's own
// thread carries source "thread", for an extension that would otherwise post it back to where it was said).
func (s *Service) todoDecisionEvent(row db.TodoDecisionRow) {
	if !s.todoEventsOn() {
		return
	}
	s.todos.mu.RLock()
	t, ok := s.todos.todos[row.TodoID]
	var m todoMark
	if ok {
		m = markOf(t)
	}
	s.todos.mu.RUnlock()
	if !ok {
		return
	}
	s.todoQueue(todoNotice{assistantwire.EventTodoDecision, assistantwire.TodoDecisionEvent{
		ID: row.TodoID, Title: m.title, Thread: m.eventThread(),
		Decision: assistantwire.TodoEventDecision{ID: row.ID, Question: row.Question, Choice: row.Choice, Reason: row.Reason,
			Source: row.Source, SupersedesID: row.SupersedesID}}})
}

// todoQueue keeps the todos' writes independent of a slow or disconnected bus: one worker hands the hub what waits, in
// order. A todo.changed for a todo that has one waiting is merged into it (assistantwire.MergeTodoChanged), so a backlog
// is at most one a todo; past todoEventLimit the oldest is dropped, and the log says so (once, until the queue is empty
// again). Like the other extension events these are best effort.
func (s *Service) todoQueue(n todoNotice) {
	e := &s.tevents
	e.qmu.Lock()
	defer e.qmu.Unlock()
	if c, ok := n.params.(assistantwire.TodoChangedEvent); ok {
		for i, old := range e.pending {
			o, isChange := old.params.(assistantwire.TodoChangedEvent)
			if !isChange || o.ID != c.ID {
				continue
			}
			if merged, keep := assistantwire.MergeTodoChanged(o, c); keep {
				e.pending[i].params = merged
			} else {
				e.pending = slices.Delete(e.pending, i, i+1)
			}
			return
		}
	}
	if len(e.pending) >= todoEventLimit {
		e.pending = e.pending[1:]
		if !e.dropped {
			e.dropped = true
			s.logf("todos: the extensions are behind: the oldest of %d waiting todo events is dropped", todoEventLimit)
		}
	}
	e.pending = append(e.pending, n)
	if !e.running {
		e.running = true
		go s.todoSendEvents()
	}
}

func (s *Service) todoSendEvents() {
	publisher := s.hub.(todoEventPublisher)
	e := &s.tevents
	for {
		e.qmu.Lock()
		if len(e.pending) == 0 {
			e.running = false
			e.dropped = false
			e.qmu.Unlock()
			return
		}
		n := e.pending[0]
		e.pending = e.pending[1:]
		e.qmu.Unlock()
		switch s.todoDecisionVerdict(n) {
		case todoHidden:
			continue
		case todoUntold:
			s.todoHold(n)
			continue
		}
		if _, decision := n.params.(assistantwire.TodoDecisionEvent); decision {
			e.qmu.Lock()
			if len(e.held) == 0 {
				e.heldTries = 0 // told: the next one held waits from todoRetryAfter again
			}
			e.qmu.Unlock()
		}
		if publisher.TodoEvent(n.event, n.params) != nil {
			s.logf("todos: %s could not be told to the extensions", n.event)
		}
	}
}

// todoHold keeps todo.decision n, whose todo's verdict could not be told, to be judged again after todoRetryDelay:
// back at the head of the queue, in the order they were held. Past todoEventLimit the oldest held is dropped.
func (s *Service) todoHold(n todoNotice) {
	e := &s.tevents
	e.qmu.Lock()
	if len(e.held) >= todoEventLimit {
		e.held = e.held[1:]
		s.logf("todos: %d todo events wait for their verdict: the oldest is dropped", todoEventLimit)
	}
	e.held = append(e.held, n)
	arm := !e.heldArmed
	e.heldArmed = true
	delay := todoRetryDelay(e.heldTries)
	e.qmu.Unlock()
	if arm {
		e.later(delay, s.todoReleaseHeld)
	}
}

// todoReleaseHeld puts the held todo.decision events back at the head of the queue, to be judged again.
func (s *Service) todoReleaseHeld() {
	e := &s.tevents
	e.qmu.Lock()
	e.heldArmed = false
	if len(e.held) == 0 || s.runCtx().Err() != nil {
		e.qmu.Unlock()
		return
	}
	e.pending = append(e.held, e.pending...)
	e.held = nil
	e.heldTries++
	start := !e.running
	e.running = true
	e.qmu.Unlock()
	if start {
		go s.todoSendEvents()
	}
}
