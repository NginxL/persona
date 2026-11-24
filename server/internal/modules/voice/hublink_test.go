package voice

import (
	"io"
	"log"
	"testing"
	"time"
)

// The link's read loop never waits on a listener: one whose events are full is marked dirty (its next check reads
// everything again), and the rest still get theirs.
func TestHubLinkDispatchNeverBlocks(t *testing.T) {
	h := newHubLink(newHubClient("http://127.0.0.1:1", "t"), log.New(io.Discard, "", 0).Printf)
	slow, quick := h.listen(), h.listen()
	defer slow.close()
	defer quick.close()
	k := linkKey{"m", "s"}
	slow.watch(k, 0)
	quick.watch(k, 0)
	done := make(chan struct{})
	go func() {
		for range listenerEvents + 10 {
			h.dispatch(linkEvent{key: k, ended: true})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch waited on a full listener")
	}
	if !slow.dirty.Load() {
		t.Fatal("the full listener was not marked dirty")
	}
	if ev := <-quick.events; ev.key != k || !ev.ended {
		t.Fatalf("%+v", ev)
	}
	// Only the listeners watching a conversation hear of it.
	other := h.listen()
	defer other.close()
	h.dispatch(linkEvent{key: k})
	select {
	case ev := <-other.events:
		t.Fatalf("an unwatched conversation's event: %+v", ev)
	default:
	}
}

// What the hub pushes: a turn's end (turn_duration, the main conversation's empty delta, a Stop hook), a changed
// state; not every step, not what nobody watches.
func TestHubLinkReadsTheHubsEvents(t *testing.T) {
	h := newHubLink(newHubClient("http://127.0.0.1:1", "t"), log.New(io.Discard, "", 0).Printf)
	l := h.listen()
	defer l.close()
	l.watch(linkKey{"m", "s"}, 0)
	next := func() (linkEvent, bool) {
		select {
		case ev := <-l.events:
			return ev, true
		default:
			return linkEvent{}, false
		}
	}
	h.handle([]byte(`{"type":"hello","locale":"ja","hub_id":"h1"}`))
	if h.language() != "ja" {
		t.Fatalf("locale %q", h.language())
	}
	for _, raw := range []string{
		`{"type":"items","machine_id":"m","session_id":"s","items":[{"seq":3,"kind":"text"},{"seq":4,"kind":"turn_duration","turn_duration":{"duration_ms":5}}]}`,
		`{"type":"delta","machine_id":"m","session_id":"s","text":""}`,
		`{"type":"hook_event","machine_id":"m","session_id":"s","event":"Stop"}`,
	} {
		h.handle([]byte(raw))
		if ev, ok := next(); !ok || !ev.ended {
			t.Errorf("%s: %+v %v", raw, ev, ok)
		}
	}
	for _, raw := range []string{
		`{"type":"items","machine_id":"m","session_id":"s","items":[{"seq":5,"kind":"text"}]}`,
		`{"type":"delta","machine_id":"m","session_id":"s","text":"写到一半"}`,
		`{"type":"items","machine_id":"m","session_id":"other","items":[{"seq":4,"turn_duration":{"duration_ms":5}}]}`,
	} {
		h.handle([]byte(raw))
		if ev, ok := next(); ok {
			t.Errorf("%s: %+v", raw, ev)
		}
	}
	working := `{"type":"sessions","machine_id":"m","sessions":[{"session_id":"s","status":"working","updated_at":"2026-11-06T00:00:01Z"}]}`
	h.handle([]byte(working))
	if _, ok := next(); !ok {
		t.Fatal("a changed state was not told")
	}
	h.handle([]byte(`{"type":"sessions","machine_id":"m","sessions":[{"session_id":"s","status":"working","updated_at":"2026-11-06T00:00:09Z"}]}`))
	if ev, ok := next(); ok {
		t.Fatalf("a step that changed nothing a run looks at: %+v", ev)
	}
}

// A subscription catches up, after a reconnect, from the last line the link saw of the conversation (a batch's to_seq
// or its last item), not from where the call or a run began: what is replayed is only what it missed.
func TestHubLinkCatchesUpFromWhatItSaw(t *testing.T) {
	h := newHubLink(newHubClient("http://127.0.0.1:1", "t"), log.New(io.Discard, "", 0).Printf)
	l := h.listen()
	defer l.close()
	k := linkKey{"m", "s"}
	l.watch(k, 5)
	since := func() int {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.since[k]
	}
	for _, c := range []struct {
		raw  string
		want int
	}{
		{`{"type":"items","machine_id":"m","session_id":"s","items":[{"seq":6},{"seq":7,"turn_duration":{"duration_ms":5}}]}`, 7},
		{`{"type":"items","machine_id":"m","session_id":"s","items":[],"to_seq":9}`, 9},
		{`{"type":"items","machine_id":"m","session_id":"s","items":[{"seq":8}],"backlog":true}`, 9}, // never back
		{`{"type":"items","machine_id":"m","session_id":"other","items":[{"seq":40}]}`, 9},
	} {
		h.handle([]byte(c.raw))
		if got := since(); got != c.want {
			t.Fatalf("%s: since %d, want %d", c.raw, got, c.want)
		}
	}
	h.mu.Lock()
	_, other := h.since[linkKey{"m", "other"}]
	h.mu.Unlock()
	if other {
		t.Fatal("an unwatched conversation got a catch-up point")
	}
	l.watch(k, 3) // a run's base before what was seen: its turn's end is caught up too
	if got := since(); got != 3 {
		t.Fatalf("since %d after a watch from 3", got)
	}
}
