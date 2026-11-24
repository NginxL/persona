package node

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	ttftMaxSessions        = 256
	ttftMaxItemsPerSession = 64
	// ttftTimestampSlack tolerates small clock/write ordering jitter between in-memory send time
	// and a transcript line's recorded timestamp while rejecting historical items from prior turns.
	ttftTimestampSlack = 5 * time.Second
)

// turnTTFTTracker measures and caches per-turn Time-To-First-Token (ttft_ms) for live streaming
// turns on the node without any background polling or timers. Historical replays without an
// observed live streaming turn leave Item.TTFTMS nil (omitted on the wire).
type turnTTFTTracker struct {
	mu       sync.Mutex
	sessions map[string]*ttftSessionState
}

type ttftSessionState struct {
	armed        bool
	startedAt    time.Time
	promptID     string
	pendingReady bool
	pendingStart time.Time
	pendingTTFT  int64
	touchedAt    time.Time
	bound        map[string]int64
	boundOrder   []string
}

func (t *turnTTFTTracker) entryLocked(id string, now time.Time) *ttftSessionState {
	if t.sessions == nil {
		t.sessions = map[string]*ttftSessionState{}
	}
	st := t.sessions[id]
	if st == nil {
		if len(t.sessions) >= ttftMaxSessions {
			var oldestKey string
			var oldestTime time.Time
			for k, v := range t.sessions {
				if oldestKey == "" || v.touchedAt.Before(oldestTime) {
					oldestKey, oldestTime = k, v.touchedAt
				}
			}
			if oldestKey != "" {
				delete(t.sessions, oldestKey)
			}
		}
		st = &ttftSessionState{}
		t.sessions[id] = st
	}
	st.touchedAt = now
	return st
}

// arm records the start of a new live turn for session id.
func (t *turnTTFTTracker) arm(id string, startedAt time.Time, promptID string) {
	if t == nil || id == "" || startedAt.IsZero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.entryLocked(id, startedAt)
	if st.armed && promptID != "" && st.promptID == promptID {
		return
	}
	st.armed = true
	st.startedAt = startedAt
	st.promptID = promptID
	// A brand new turn supersedes any unattached pending TTFT from a prior turn.
	st.pendingReady = false
}

// armIfUnarmed arms the turn only when no send-time start is already armed.
func (t *turnTTFTTracker) armIfUnarmed(id string, startedAt time.Time, promptID string) {
	if t == nil || id == "" || startedAt.IsZero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if st := t.sessions[id]; st != nil && st.armed {
		if st.promptID == "" && promptID != "" {
			st.promptID = promptID
		}
		return
	}
	st := t.entryLocked(id, startedAt)
	st.armed = true
	st.startedAt = startedAt
	st.promptID = promptID
	st.pendingReady = false
}

// firstToken records the first streaming assistant token/chunk for an armed turn.
// Returns the observed ttft_ms and true once per turn; subsequent chunks in the same turn are ignored.
func (t *turnTTFTTracker) firstToken(id string, now time.Time, promptID string) (int64, bool) {
	if t == nil || id == "" || now.IsZero() {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.sessions[id]
	if st == nil || !st.armed {
		return 0, false
	}
	if promptID != "" && st.promptID != "" && promptID != st.promptID {
		return 0, false
	}
	st.armed = false
	ms := now.Sub(st.startedAt).Milliseconds()
	if ms < 0 {
		return 0, false
	}
	st.pendingReady = true
	st.pendingStart = st.startedAt
	st.pendingTTFT = ms
	st.touchedAt = now
	return ms, true
}

// clearPending disarms an unfinished turn that ended without producing any assistant token.
// Already-captured pending TTFT remains available for the turn's closing transcript push.
func (t *turnTTFTTracker) clearPending(id string) {
	if t == nil || id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if st := t.sessions[id]; st != nil {
		st.armed = false
		st.startedAt = time.Time{}
		st.promptID = ""
	}
}

// disarm cancels an armed turn (and any pending measurement captured for that exact send timestamp)
// when a send RPC definitively failed before typing/starting a turn or returned queued.
func (t *turnTTFTTracker) disarm(id string, startedAt time.Time) {
	if t == nil || id == "" || startedAt.IsZero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.sessions[id]
	if st == nil {
		return
	}
	if st.armed && st.startedAt.Equal(startedAt) {
		st.armed = false
		st.startedAt = time.Time{}
		st.promptID = ""
	}
	if st.pendingReady && st.pendingStart.Equal(startedAt) {
		st.pendingReady = false
		st.pendingStart = time.Time{}
		st.pendingTTFT = 0
	}
}

func (t *turnTTFTTracker) has(id string) bool {
	if t == nil || id == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.sessions[id]
	return st != nil && (st.pendingReady || len(st.bound) > 0)
}

func (st *ttftSessionState) bindLocked(itemID string, ms int64) {
	if itemID == "" || ms < 0 {
		return
	}
	if st.bound == nil {
		st.bound = map[string]int64{}
	}
	if _, exists := st.bound[itemID]; exists {
		return
	}
	if len(st.boundOrder) >= ttftMaxItemsPerSession {
		evict := st.boundOrder[0]
		st.boundOrder = st.boundOrder[1:]
		delete(st.bound, evict)
	}
	st.bound[itemID] = ms
	st.boundOrder = append(st.boundOrder, itemID)
}

func isTTFTAssistantItem(it proto.Item) bool {
	if it.Role != "assistant" {
		return false
	}
	switch it.Kind {
	case proto.KindText, proto.KindThinking, proto.KindToolUse, proto.KindFold:
		return true
	}
	return false
}

// annotate attaches observed TTFTMS onto items in place. Previously bound item IDs always retain
// their observed ttft_ms across reads; a newly observed pending TTFT binds only when allowBind is
// true (live pushes or latest-page reads) and never attaches to historical items written before the turn.
// Existing Item.TTFTMS values reported natively by an engine/runtime are preserved untouched.
func (t *turnTTFTTracker) annotate(id string, items []proto.Item, allowBind bool) bool {
	if t == nil || id == "" || len(items) == 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.sessions[id]
	if st == nil || (!st.pendingReady && len(st.bound) == 0) {
		return false
	}
	changed := false
	for i := range items {
		it := &items[i]
		if it.TTFTMS != nil {
			if *it.TTFTMS >= 0 {
				st.bindLocked(it.ID, *it.TTFTMS)
				if allowBind && st.pendingReady && isTTFTAssistantItem(*it) &&
					(it.TS.IsZero() || st.pendingStart.IsZero() || !it.TS.Before(st.pendingStart.Add(-ttftTimestampSlack))) {
					st.pendingReady = false
				}
			}
			continue
		}
		if ms, ok := st.bound[it.ID]; ok {
			it.TTFTMS = proto.Int64(ms)
			changed = true
			continue
		}
		if allowBind && st.pendingReady && isTTFTAssistantItem(*it) {
			if !it.TS.IsZero() && !st.pendingStart.IsZero() && it.TS.Before(st.pendingStart.Add(-ttftTimestampSlack)) {
				continue
			}
			it.TTFTMS = proto.Int64(st.pendingTTFT)
			st.bindLocked(it.ID, st.pendingTTFT)
			st.pendingReady = false
			changed = true
		}
	}
	return changed
}

// annotateRaw patches the items array in a raw runtime session.items payload only when session id
// has tracked TTFT state, keeping the fast path zero-overhead.
func (t *turnTTFTTracker) annotateRaw(id string, raw json.RawMessage, field string, allowBind bool) json.RawMessage {
	if !t.has(id) || len(raw) == 0 {
		return raw
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw
	}
	var items []proto.Item
	if json.Unmarshal(obj[field], &items) != nil || len(items) == 0 {
		return raw
	}
	if !t.annotate(id, items, allowBind) {
		return raw
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return raw
	}
	obj[field] = encoded
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}
