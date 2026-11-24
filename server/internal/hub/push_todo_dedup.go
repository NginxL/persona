package hub

import (
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// A to-do's push to the owner rings once per reason while the to-do waits on them for it (assistantwire.TodoPushKey,
// "todo:<id>:<reason>"). The legacy key carried the round ("todo:<id>@<asked at>"), so a decision asked again, or a
// needs-user action after each stretch of work, rang every time: one to-do rang four times in an hour. Now a push
// whose key names the to-do and a reason is held while an earlier one with the same key rang less than
// gradingDedupWindow ago and the manager has not said the to-do is over for the owner (EventFleetResolved naming
// assistantwire.TodoAskCardID, todoPushOver). A reminder a day later still rings. Keys of any other form, the legacy
// one of older managers included, are not affected. Whatever the push level: a P0 is not exempt from this one.

// todoPushBook is when each todo push key last rang.
type todoPushBook struct {
	mu sync.Mutex
	at map[string]time.Time
}

func (p *Pusher) todoPushNow() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// claimTodoPush says whether a push with related key may ring: true for any key that is not a todo push key, and for
// one that did not ring within the window since the to-do was last over; it then notes it rang now.
func (p *Pusher) claimTodoPush(key string) bool {
	if p == nil {
		return true
	}
	if _, _, ok := assistantwire.TodoPushKeyParts(key); !ok {
		return true
	}
	key = strings.TrimSpace(key)
	now := p.todoPushNow()
	b := &p.todoPushes
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, at := range b.at {
		if now.Sub(at) >= gradingDedupWindow {
			delete(b.at, k)
		}
	}
	if at, seen := b.at[key]; seen && now.Sub(at) < gradingDedupWindow {
		return false
	}
	if b.at == nil {
		b.at = map[string]time.Time{}
	}
	b.at[key] = now
	return true
}

// todoPushOver: to-do id no longer waits on the owner (answered, withdrawn by its supervisor, closed): its next push
// about any reason rings again.
func (p *Pusher) todoPushOver(id string) {
	if p == nil || strings.TrimSpace(id) == "" {
		return
	}
	prefix := "todo:" + strings.TrimSpace(id) + ":"
	b := &p.todoPushes
	b.mu.Lock()
	defer b.mu.Unlock()
	for k := range b.at {
		if strings.HasPrefix(k, prefix) {
			delete(b.at, k)
		}
	}
}
