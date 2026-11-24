package assistantwire

const (
	// EventTodoChanged travels from the assistant to the hub; extensions receive event.todo.changed with todos.read.
	EventTodoChanged = "todo.changed"
	// EventTodoDecision is sent for every decision written on a todo, whatever its source; extensions receive
	// event.todo.decision with todos.read.
	EventTodoDecision = "todo.decision"
)

// TodoEventThread is the chat thread a todo is bound to, as the extensions are told of it.
type TodoEventThread struct {
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	ThreadID string `json:"thread_id"`
	Title    string `json:"title"`
}

// TodoEventResponsible is the external responsible party on a to-do, as extensions are told of it (#12712).
type TodoEventResponsible struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// The kinds of change a TodoChangedEvent says.
const (
	TodoCreated = "created"
	TodoUpdated = "updated"
	TodoDeleted = "deleted"
)

// TodoChangedEvent: a todo was created, deleted, or its title, whether it is done, its supervisor, the thread it is
// bound to, or its external responsible party changed. Thread is the binding after the change (the last one it had,
// for a deleted todo); nil when it has none. Done is true while the todo is done (closed, or it failed to start) and
// left out otherwise. Supervisor is the conversation that supervises it (the main conversation or a leader, #3484):
// it changes too when the task is moved to another; left out for a todo nobody supervises. Kind is "concern" for
// something the owner raised in the main conversation (#3561), which is done only once its outcome was reported to
// them (a decision of source reported); left out for a to-do or a task. It never changes. Responsible is the external
// responsible party when set (#12712); nil when none is set.
type TodoChangedEvent struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Thread      *TodoEventThread      `json:"thread,omitempty"`
	Done        bool                  `json:"done,omitempty"`
	Supervisor  string                `json:"supervisor,omitempty"`
	Kind        string                `json:"kind,omitempty"`
	Responsible *TodoEventResponsible `json:"responsible,omitempty"`
	What        string                `json:"what"`
}

// MergeTodoChanged is what a queue that still holds old for a todo keeps when cur, a later change of the same todo,
// arrives: one event with the latest state, that says what the receiver would have to know (a todo it was not yet told
// of is still "created"; one created and deleted before it was told is nothing: keep false). The queues of the
// assistant and of the extension manager both merge by it, so a backlog is at most one event a todo.
func MergeTodoChanged(old, cur TodoChangedEvent) (merged TodoChangedEvent, keep bool) {
	switch {
	case old.What == TodoCreated && cur.What == TodoDeleted:
		return TodoChangedEvent{}, false
	case old.What == TodoCreated && cur.What == TodoUpdated:
		cur.What = TodoCreated
	case cur.What == TodoCreated:
		cur.What = TodoUpdated // the same id again: it existed
	}
	return cur, true
}

// TodoEventDecision is the decision a TodoDecisionEvent carries: what was asked and chosen, and where it came from
// (manual, card, outcome, agent, thread, reported).
type TodoEventDecision struct {
	ID           string `json:"id"`
	Question     string `json:"question"`
	Choice       string `json:"choice"`
	Reason       string `json:"reason,omitempty"`
	Source       string `json:"source"`
	SupersedesID string `json:"supersedes_id,omitempty"`
}

// TodoDecisionEvent: a decision was written on a todo.
type TodoDecisionEvent struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Thread   *TodoEventThread  `json:"thread,omitempty"`
	Decision TodoEventDecision `json:"decision"`
}
