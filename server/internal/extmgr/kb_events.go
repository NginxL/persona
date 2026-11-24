package extmgr

import (
	"encoding/json"
	"slices"
	"sync"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
)

func init() {
	onAssistantEvent(assistantwire.EventKBChanged, func(h *Manager, raw json.RawMessage) {
		var p assistantwire.KBChanged
		if json.Unmarshal(raw, &p) != nil || p.ID == "" || !slices.Contains([]string{kb.KindChat, kb.KindPerson, kb.KindProject, kb.KindDoc, kb.KindMeeting, kb.KindEvent, kb.KindRecording, kb.KindTopic}, p.Kind) ||
			!slices.Contains([]string{"metadata", "summary", "messages", "log"}, p.What) {
			return
		}
		h.extensions.queueEvent(assistantwire.EventKBChanged, map[string]any{"kind": p.Kind, "id": p.ID, "what": p.What})
	})
	onAssistantEvent(assistantwire.EventKBForgotten, func(h *Manager, raw json.RawMessage) {
		var p assistantwire.KBForgottenEvent
		if json.Unmarshal(raw, &p) != nil || p.Source == "" {
			return
		}
		h.extensions.queueEvent(assistantwire.EventKBForgotten, map[string]any{"source": p.Source})
	})
}

type extensionNotice struct {
	entryPersonal bool // internal ledger routing metadata, never delivered as payload
	topic         string
	payload       map[string]any
	// changed is the typed todo.changed behind payload, for merging (todo_events.go); nil for any other.
	changed *assistantwire.TodoChangedEvent
}
type extensionEvents struct {
	mu      sync.Mutex
	running bool
	dropped bool // the log has said events are dropped, and the queue has not been empty since
	pending []extensionNotice
}

const extensionEventLimit = 512

// queueEvent copies only the allowlisted fields parsed above. At most one worker sends these notifications;
// overload drops the oldest hint, so a slow extension cannot hold up the assistant's bus or grow an unbounded queue.
func (ps *extensions) queueEvent(topic string, payload map[string]any) {
	ps.queueOn(&ps.events, extensionNotice{topic: topic, payload: payload})
}

// queueOn is queueEvent on queue q: the knowledge base's and the todos' have one each, so a flood of one cannot push
// the other's out. A todo.changed for a todo that has one waiting is merged into it (assistantwire.MergeTodoChanged),
// so the backlog is at most one a todo. When the oldest is dropped the log says so, once until the queue is empty again.
func (ps *extensions) queueOn(q *extensionEvents, n extensionNotice) {
	if ps == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if n.changed != nil {
		for i, old := range q.pending {
			if old.changed == nil || old.changed.ID != n.changed.ID {
				continue
			}
			if merged, keep := assistantwire.MergeTodoChanged(*old.changed, *n.changed); keep {
				q.pending[i] = changedNotice(merged)
			} else {
				q.pending = slices.Delete(q.pending, i, i+1)
			}
			return
		}
	}
	if len(q.pending) >= extensionEventLimit {
		q.pending = q.pending[1:]
		if !q.dropped {
			q.dropped = true
			if ps.h != nil {
				ps.h.logf("extensions: events to them are behind: the oldest of %d waiting is dropped (%s)", extensionEventLimit, n.topic)
			}
		}
	}
	q.pending = append(q.pending, n)
	if !q.running {
		q.running = true
		go ps.sendEvents(q)
	}
}

func (ps *extensions) sendEvents(q *extensionEvents) {
	for {
		q.mu.Lock()
		if len(q.pending) == 0 {
			q.running = false
			q.dropped = false
			q.mu.Unlock()
			return
		}
		n := q.pending[0]
		q.pending = q.pending[1:]
		q.mu.Unlock()
		if n.entryPersonal {
			ps.emitPersonalEntries(n.payload)
		} else {
			ps.emit(n.topic, n.payload)
		}
	}
}
