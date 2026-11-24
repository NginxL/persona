package voice

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// One WebSocket from the extension to the hub (/api/v1/ws, docs/assistant/voice.md "Extension calls to hub"), shared by every conversation:
// their runs learn at once that a turn ended, a card came or went or the conversation changed, rather than on their
// 5 s look. Subscriptions are counted per conversation (machine, session). The read loop never waits on anyone: the
// hub drops a client whose queue fills up (hub/http.go), so each listener gets its events on a bounded channel and one
// that is full is marked dirty instead; a run's check reads everything again, so a lost event loses nothing.
//
// The hub only subscribes its own machines (a mesh member's are not followed here): a run on one of those is followed
// by its 5 s look alone.

type linkKey struct{ machine, session string }

// linkEvent is what a listener is told: something about key changed (zero key: anything may have, an interaction
// resolved or the machines changed); ended: a turn ended there (a turn_duration line, the headless main conversation's
// empty delta, a Stop hook).
type linkEvent struct {
	key   linkKey
	ended bool
}

// linkListener is one conversation's end of the link.
type linkListener struct {
	link   *hubLink
	events chan linkEvent
	dirty  atomic.Bool     // events were dropped: check everything
	keys   map[linkKey]int // under link.mu
	closed bool            // under link.mu: a watch after close is none
}

const listenerEvents = 64

// Reconnecting after the link drops: 1, 2, 5, then every 10 s.
var linkBackoff = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second}

type hubLink struct {
	hub  *hubClient
	logf func(string, ...any)

	mu         sync.Mutex
	listeners  map[*linkListener]struct{}
	subs       map[linkKey]int // listeners' watches
	since      map[linkKey]int // what a subscription catches up from (a run's base, moved on by the lines seen: saw)
	seen       map[linkKey]string
	conn       *websocket.Conn
	cancel     context.CancelFunc
	lang       i18n.Lang
	hubID      string
	machineHub map[string]string
	elsewhere  map[string]bool // machines already logged as not this hub's
	down       bool            // the last dial failed (logged once until it works again)
}

func newHubLink(hub *hubClient, logf func(string, ...any)) *hubLink {
	return &hubLink{hub: hub, logf: logf, listeners: map[*linkListener]struct{}{}, subs: map[linkKey]int{}, since: map[linkKey]int{},
		seen: map[linkKey]string{}, machineHub: map[string]string{}, elsewhere: map[string]bool{}}
}

// language is the user's language as the hub's hello said ("" before it did).
func (h *hubLink) language() i18n.Lang {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lang
}

// listen adds a listener; the link connects with the first one and closes with the last.
func (h *hubLink) listen() *linkListener {
	l := &linkListener{link: h, events: make(chan linkEvent, listenerEvents), keys: map[linkKey]int{}}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listeners[l] = struct{}{}
	if h.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		go h.run(ctx, linkBackoff) // read here: a test sets it back while a closed link's loop is still on its way out
	}
	return l
}

// close drops the listener and its watches.
func (l *linkListener) close() {
	h := l.link
	h.mu.Lock()
	keys := l.keys
	l.keys, l.closed = map[linkKey]int{}, true
	for k, n := range keys {
		for range n {
			h.unwatchLocked(k)
		}
	}
	delete(h.listeners, l)
	var stop context.CancelFunc
	if len(h.listeners) == 0 && h.cancel != nil {
		stop, h.cancel = h.cancel, nil
	}
	h.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// watch follows a conversation for this listener until unwatch; since is where a new subscription catches up from.
func (l *linkListener) watch(k linkKey, since int) {
	h := l.link
	h.mu.Lock()
	if l.closed {
		h.mu.Unlock()
		return
	}
	l.keys[k]++
	h.subs[k]++
	first := h.subs[k] == 1
	if first || since < h.since[k] {
		h.since[k] = since
	}
	conn := h.conn
	if hub, ok := h.machineHub[k.machine]; ok && hub != "" && h.hubID != "" && hub != h.hubID && !h.elsewhere[k.machine] {
		h.elsewhere[k.machine] = true
		h.logf("hub link: %s is another hub's machine: its conversations are followed by the 5 s look only", k.machine)
	}
	h.mu.Unlock()
	if first && conn != nil {
		h.subscribe(conn, "subscribe", k, since)
	}
}

func (l *linkListener) unwatch(k linkKey) {
	h := l.link
	h.mu.Lock()
	if l.keys[k] == 0 {
		h.mu.Unlock()
		return
	}
	if l.keys[k]--; l.keys[k] == 0 {
		delete(l.keys, k)
	}
	h.unwatchLocked(k)
	h.mu.Unlock()
}

// unwatchLocked drops one watch of k (mu held), unsubscribing the last.
func (h *hubLink) unwatchLocked(k linkKey) {
	if h.subs[k]--; h.subs[k] > 0 {
		return
	}
	delete(h.subs, k)
	delete(h.since, k)
	delete(h.seen, k)
	if conn := h.conn; conn != nil {
		go h.subscribe(conn, "unsubscribe", k, 0)
	}
}

func (h *hubLink) subscribe(conn *websocket.Conn, typ string, k linkKey, since int) {
	f := map[string]any{"type": typ, "machine_id": k.machine, "session_id": k.session}
	if typ == "subscribe" {
		f["since_seq"], f["lazy_images"] = since, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = writeJSON(ctx, conn, f)
}

// run keeps the link up while there are listeners, waiting backoff between attempts.
func (h *hubLink) run(ctx context.Context, backoff []time.Duration) {
	for attempt := 0; ctx.Err() == nil; attempt++ {
		connected, err := h.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			attempt = 0 // it was up: the backoff starts over
		}
		h.mu.Lock()
		if !h.down {
			h.logf("hub link: %v (reconnecting; the runs look every 5 s meanwhile)", err)
		}
		h.down = true
		h.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff[min(attempt, len(backoff)-1)]):
		}
	}
}

// session is one connection: dial, subscribe what is watched, read until it drops (connected: the dial went through).
func (h *hubLink) session(ctx context.Context) (connected bool, err error) {
	u := "ws" + strings.TrimPrefix(h.hub.url, "http") + "/api/v1/ws"
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + h.hub.token}}})
	cancel()
	if err != nil {
		return false, err
	}
	conn.SetReadLimit(64 << 20)
	defer conn.CloseNow()
	h.mu.Lock()
	h.conn = conn
	if h.down {
		h.logf("hub link: connected again")
	}
	h.down = false
	subs := make(map[linkKey]int, len(h.subs))
	for k := range h.subs {
		subs[k] = h.since[k]
	}
	clear(h.seen)
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.conn == conn {
			h.conn = nil
		}
		h.mu.Unlock()
	}()
	for k, since := range subs {
		h.subscribe(conn, "subscribe", k, since)
	}
	// Whatever happened while the link was down: everyone checks.
	h.dispatch(linkEvent{})
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			return true, err
		}
		h.handle(b)
	}
}

// handle reads one of the hub's events; it never blocks.
func (h *hubLink) handle(b []byte) {
	var head struct {
		Type      string `json:"type"`
		MachineID string `json:"machine_id"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(b, &head) != nil {
		return
	}
	key := linkKey{head.MachineID, head.SessionID}
	switch head.Type {
	case "hello":
		var f struct {
			Locale string `json:"locale"`
			HubID  string `json:"hub_id"`
		}
		_ = json.Unmarshal(b, &f)
		h.mu.Lock()
		h.lang, h.hubID = i18n.Resolve(f.Locale), f.HubID
		h.mu.Unlock()
	case "machines":
		var f struct {
			Machines []proto.Machine `json:"machines"`
		}
		_ = json.Unmarshal(b, &f)
		h.mu.Lock()
		for _, m := range f.Machines {
			h.machineHub[m.ID] = m.HubID
		}
		h.mu.Unlock()
		h.dispatch(linkEvent{})
	case "sessions":
		h.sessions(head.MachineID, b)
	case "items":
		if !h.watched(key) {
			return
		}
		var f struct {
			Items []struct {
				Seq          int             `json:"seq"`
				TurnDuration json.RawMessage `json:"turn_duration"`
			} `json:"items"`
			ToSeq *int `json:"to_seq"`
		}
		_ = json.Unmarshal(b, &f)
		seen := 0
		if f.ToSeq != nil {
			seen = *f.ToSeq
		}
		for _, it := range f.Items {
			seen = max(seen, it.Seq)
		}
		h.saw(key, seen)
		for _, it := range f.Items {
			if len(it.TurnDuration) > 0 && string(it.TurnDuration) != "null" {
				h.dispatch(linkEvent{key: key, ended: true})
				return
			}
		}
	case "delta":
		var f struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(b, &f) == nil && f.Text == "" && h.watched(key) {
			h.dispatch(linkEvent{key: key, ended: true}) // the headless main conversation wrote its last words
		}
	case "hook_event":
		var f struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(b, &f) == nil && f.Event == "Stop" && h.watched(key) {
			h.dispatch(linkEvent{key: key, ended: true})
		}
	case "interaction":
		var f struct {
			Interaction struct {
				MachineID string `json:"machine_id"`
				SessionID string `json:"session_id"`
			} `json:"interaction"`
		}
		if json.Unmarshal(b, &f) == nil {
			if k := (linkKey{f.Interaction.MachineID, f.Interaction.SessionID}); h.watched(k) {
				h.dispatch(linkEvent{key: k})
			}
		}
	case "interaction_resolved":
		h.dispatch(linkEvent{}) // it names only the card
	}
}

// sessions tells the listeners of a machine's watched conversations whose state changed (not every step: the
// state, not the transcript's size).
func (h *hubLink) sessions(machine string, b []byte) {
	h.mu.Lock()
	var keys []linkKey
	for k := range h.subs {
		if k.machine == machine {
			keys = append(keys, k)
		}
	}
	h.mu.Unlock()
	if len(keys) == 0 {
		return
	}
	var f struct {
		Sessions []proto.Session `json:"sessions"`
	}
	if json.Unmarshal(b, &f) != nil {
		return
	}
	for _, k := range keys {
		fp := "gone"
		for i := range f.Sessions {
			if f.Sessions[i].SessionID == k.session {
				fp = fingerprint(&f.Sessions[i])
				break
			}
		}
		h.mu.Lock()
		changed := h.seen[k] != fp
		if _, ok := h.subs[k]; ok {
			h.seen[k] = fp
		}
		h.mu.Unlock()
		if changed {
			h.dispatch(linkEvent{key: k})
		}
	}
}

// fingerprint is what of a conversation's state a run's check looks at.
func fingerprint(s *proto.Session) string {
	work := ""
	if s.Work != nil {
		work = s.Work.Phase
	}
	b, _ := json.Marshal([]any{s.Status, s.State, s.Starting, s.PendingInteractions, len(s.Background), s.Subagents, s.TurnError, work})
	return string(b)
}

// saw moves k's catch-up point on to seq, the last line of a batch the link got (it only moves on).
func (h *hubLink) saw(k linkKey, seq int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[k] > 0 && seq > h.since[k] {
		h.since[k] = seq
	}
}

func (h *hubLink) watched(k linkKey) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.subs[k] > 0
}

// dispatch hands ev to the listeners it concerns without waiting: a full one is marked dirty.
func (h *hubLink) dispatch(ev linkEvent) {
	h.mu.Lock()
	var to []*linkListener
	for l := range h.listeners {
		if ev.key == (linkKey{}) || l.keys[ev.key] > 0 {
			to = append(to, l)
		}
	}
	h.mu.Unlock()
	for _, l := range to {
		select {
		case l.events <- ev:
		default:
			l.dirty.Store(true)
		}
	}
}
