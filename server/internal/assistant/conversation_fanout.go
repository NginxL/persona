package assistant

import (
	"sync"
	"time"
)

// A broadcast by conversation_send (11-23): the main conversation sent the same round to 10 leader conversations three
// times in 12 minutes, and all 30 woke. A sender that wakes fanoutWide or more conversations within fanoutWindow has
// the ones past that kept for their receivers instead (wake: false, relay_later.go): each reads it with its next
// message, none is woken for it. A conversation it already woke in the window, and a quote of the owner's request
// (which must wake), still go in. The answer says so, and that a standing rule belongs in LEAD-COMMON, which every
// leader reads, rather than in a message to each.
const (
	fanoutWindow = 10 * time.Minute
	fanoutWide   = 3
)

// sendFanout is, per sender, when it last woke each conversation by conversation_send.
type sendFanout struct {
	mu    sync.Mutex
	woken map[string]map[string]time.Time
}

// admit says whether sender may wake target at now: yes when target is one it woke within the window, or when the
// conversations it woke within the window, with target, are fewer than fanoutWide. A yes is recorded.
func (f *sendFanout) admit(sender, target string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.woken == nil {
		f.woken = map[string]map[string]time.Time{}
	}
	seen := f.woken[sender]
	for id, at := range seen {
		if now.Sub(at) >= fanoutWindow {
			delete(seen, id)
		}
	}
	if _, again := seen[target]; !again && len(seen)+1 >= fanoutWide {
		return false
	}
	if seen == nil {
		seen = map[string]time.Time{}
		f.woken[sender] = seen
	}
	seen[target] = now
	return true
}

// fanoutNote is what conversation_send answers when admit kept a message for its receiver instead of waking it.
const fanoutNote = " It was not woken: this conversation woke %d or more conversations in the last %d minutes, so the rest are kept " +
	// i18n-ignore: a tool answer to the model; the Chinese is how leaders name that rule, kept beside the English.
	"for their next message instead. A rule for every leader belongs in LEAD-COMMON (规矩请写进 LEAD-COMMON), not in a message to each; " +
	"to wake one now, send it again after the window or quote the owner's request."
