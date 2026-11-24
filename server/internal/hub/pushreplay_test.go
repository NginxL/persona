package hub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// recorded is one message an app got from a real hub (scripts/pushrec).
type recorded struct {
	at      time.Time
	raw     []byte
	typ     string
	machine string
	list    []proto.Session
	fleet   *FleetState
}

func readRecording(t *testing.T, path string) []recorded {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []recorded
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var line struct {
			T   int64           `json:"t"`
			Msg json.RawMessage `json:"msg"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		var m struct {
			Type     string          `json:"type"`
			Machine  string          `json:"machine_id"`
			Sessions []proto.Session `json:"sessions"`
			Fleet    *FleetState     `json:"fleet"`
		}
		_ = json.Unmarshal(line.Msg, &m)
		out = append(out, recorded{at: time.UnixMilli(line.T), raw: line.Msg, typ: m.Type, machine: m.Machine, list: m.Sessions, fleet: m.Fleet})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPushReplay replays what an app got from a real hub, recorded with scripts/pushrec, through this hub's push logic,
// and reports per minute: the messages and bytes an open app receives, and the Live Activity pushes a phone gets
// while the app is closed, as recorded (the hub before) and as this code sends them (docs/operations/testing.md "Push replay").
// The recording holds real sessions and stays on the machine it was made on: PERSONA_PUSH_RECORDING is its path.
func TestPushReplay(t *testing.T) {
	path := os.Getenv("PERSONA_PUSH_RECORDING")
	if path == "" {
		t.Skip("PERSONA_PUSH_RECORDING: a recording made with scripts/pushrec")
	}
	recs := readRecording(t, path)
	if len(recs) < 10 {
		t.Fatalf("only %d messages in the recording", len(recs))
	}
	// The first seconds are the connection's snapshot (every machine's list): what the app has to begin with.
	steady := recs[0].at.Add(3 * time.Second)
	first := sort.Search(len(recs), func(i int) bool { return !recs[i].at.Before(steady) })
	span := recs[len(recs)-1].at.Sub(recs[first].at).Minutes()
	t.Logf("recording: %d messages over %.1f min after the snapshot", len(recs)-first, span)

	// What an open app receives: every message as recorded; and with this hub, the session lists through
	// broadcastSessions (the same list again is not sent).
	h := newTestHub(t)
	app := &client{out: make(chan []byte, 1<<16), subs: map[[2]string]bool{}, role: roleFull}
	// An app on the delta protocol (sessions_delta): its first message per machine stands for its snapshot.
	deltaApp := &client{out: make(chan []byte, 1<<16), subs: map[[2]string]bool{}, role: roleFull, delta: true, have: map[string]map[string][]byte{}}
	addTestClient(h, app, deltaApp)
	machines := map[string]*Machine{}
	machine := func(id string) *Machine {
		if machines[id] == nil {
			machines[id] = &Machine{cfg: MachineConfig{ID: id}}
		}
		return machines[id]
	}
	type tally struct{ n, bytes int }
	before, after, delta := map[string]*tally{}, map[string]*tally{}, map[string]*tally{}
	add := func(m map[string]*tally, typ string, size int) {
		if m[typ] == nil {
			m[typ] = &tally{}
		}
		m[typ].n++
		m[typ].bytes += size
	}
	lastRaw := map[string][]byte{}
	for i, r := range recs {
		if r.typ == "sessions" {
			h.broadcastSessions(machine(r.machine), r.list)
			sent := false
			for len(app.out) > 0 {
				<-app.out
				sent = true
			}
			for len(deltaApp.out) > 0 {
				if b := <-deltaApp.out; i >= first {
					add(delta, "sessions", len(b))
				} else {
					add(delta, "snapshot", len(b))
				}
			}
			if i < first {
				add(before, "snapshot", len(r.raw))
			}
			// The decision on the decoded list must be the one on the recorded bytes: nothing lost in decoding.
			if same := bytes.Equal(lastRaw[r.machine], r.raw); same == sent && i >= first {
				t.Fatalf("message %d: sent=%v but recorded bytes same=%v", i, sent, same)
			}
			lastRaw[r.machine] = r.raw
			if i >= first {
				add(before, r.typ, len(r.raw))
				if sent {
					add(after, r.typ, len(r.raw))
				}
			}
			continue
		}
		if i >= first {
			add(before, r.typ, len(r.raw))
			add(after, r.typ, len(r.raw))
			add(delta, r.typ, len(r.raw))
		}
	}
	total := func(m map[string]*tally) (n, b int) {
		for _, v := range m {
			n, b = n+v.n, b+v.bytes
		}
		return
	}
	rate := func(v *tally) string {
		if v == nil {
			v = &tally{}
		}
		return fmt.Sprintf("%6.1f/min %7.1f KB/min", float64(v.n)/span, float64(v.bytes)/1024/span)
	}
	for _, typ := range []string{"sessions", "revs", "fleet", "lark", "inbox", "hook_event"} {
		if before[typ] == nil {
			continue
		}
		t.Logf("%-10s before %s | whole lists %s | delta app %s", typ, rate(before[typ]), rate(after[typ]), rate(delta[typ]))
	}
	t.Logf("connecting: whole lists %.0f KB, delta snapshot (no Codex history) %.0f KB", float64(before["snapshot"].bytes)/1024,
		float64(delta["snapshot"].bytes)/1024)
	delete(before, "snapshot")
	delete(delta, "snapshot")
	bn, bb := total(before)
	an, ab := total(after)
	dn, db := total(delta)
	t.Logf("all        before %s | whole lists %s | delta app %s", rate(&tally{bn, bb}), rate(&tally{an, ab}), rate(&tally{dn, db}))

	// The Live Activity of a phone whose app is closed: the recorded fleet changes through the coalescer and flush,
	// on a clock that follows the recording; before: no gap; after: fleetMinGap. (An open app gets none: it draws
	// the activity itself.) The numbers on the island are left out: their pushes did not change.
	for _, gap := range []time.Duration{0, fleetMinGap, 2 * fleetMinGap} {
		pushes, critical, worst := replayFleet(t, recs[first:], gap)
		t.Logf("activity   gap %4v: %3d pushes (%5.1f/h), %d at once for a request; longest behind the hub %v", gap, pushes,
			float64(pushes)/span*60, critical, worst)
		if gap == fleetMinGap && worst > fleetMinGap+fleetCoalesce {
			t.Fatalf("a change reached the activity after %v", worst)
		}
	}
}

// replayFleet runs the recorded fleet changes through the push logic with the given gap and returns the pushes made,
// how many went at once for a request, and the longest the activity showed something other than the hub's fleet.
func replayFleet(t *testing.T, recs []recorded, gap time.Duration) (pushes, critical int, worst time.Duration) {
	h, _ := testLive(t)
	la := h.live
	la.minGap = gap
	var clock time.Time
	la.now = func() time.Time { return clock }
	clock = recs[0].at
	la.update("phone", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act1", "acttok", clock })
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true, LockDetails: true,
		Island: IslandPrefs{Preset: "custom", Left: SlotStatus, Right: SlotTokens}}); err != nil {
		t.Fatal(err)
	}
	tailored := func(st FleetState) []byte {
		b, _ := json.Marshal(forDevice(st, h.push.prefs("phone"), FleetStats{}))
		return b
	}
	var truth FleetState
	var due time.Time        // the run loop's timer
	var staleSince time.Time // since when the activity has shown something else than the hub (zero: the same)
	var lastPush time.Time
	observe := func() {
		la.mu.Lock()
		shown, at := la.sent["phone"], la.sentAt["phone"]
		la.mu.Unlock()
		if at.Equal(clock) && !at.Equal(lastPush) {
			pushes++
			lastPush = at
		}
		// An empty fleet is not pushed: the activity ends after fleetEndAfter (as before).
		if fleetEmpty(truth) || bytes.Equal(shown, tailored(truth)) {
			if !staleSince.IsZero() {
				worst = max(worst, clock.Sub(staleSince))
				staleSince = time.Time{}
			}
		} else if staleSince.IsZero() {
			staleSince = clock
		}
	}
	flush := func(urgent bool) {
		if next := la.flush(truth, urgent, false); !next.IsZero() && (due.IsZero() || next.Before(due)) {
			due = next
		}
		observe()
	}
	advance := func(to time.Time) {
		for !due.IsZero() && !due.After(to) {
			clock, due = due, time.Time{}
			flush(false)
		}
		clock = to
	}
	var lastState []byte
	for _, r := range recs {
		if r.typ != "fleet" || r.fleet == nil {
			continue
		}
		advance(r.at)
		st := *r.fleet
		st.Stats = nil
		if b := tailored(st); bytes.Equal(b, lastState) {
			continue // numbers only, or nothing this phone's activity shows
		} else {
			lastState = b
		}
		truth = st
		observe()
		now, schedule := la.coal.offer(truth)
		switch {
		case now:
			critical++
			flush(true)
			if !staleSince.IsZero() {
				t.Fatalf("a request change did not go out at once")
			}
		case schedule:
			if at := clock.Add(fleetCoalesce); due.IsZero() || at.Before(due) {
				due = at
			}
		}
	}
	advance(clock.Add(time.Hour))
	return pushes, critical, worst
}
