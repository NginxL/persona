package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

func strp(s string) *string { return &s }

func TestUsageSeries(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).Truncate(time.Hour).Add(25 * time.Minute) // 25 min into an hour
	from := now.Add(-time.Hour).Truncate(10 * time.Minute).Add(10 * time.Minute)
	m := func(ago time.Duration, model, launcher string, in, cw, cr, out int64) proto.UsageBucket {
		return proto.UsageBucket{Minute: now.Add(-ago).Unix() / 60 * 60, Model: model, Launcher: launcher, Input: in,
			CacheWrite: cw, CacheRead: cr, Output: out, Requests: 1}
	}
	rows := []proto.UsageBucket{
		m(time.Minute, "opus", "claude", 10, 100, 1000, 5),
		m(2*time.Minute, "opus", "claude", 10, 0, 1000, 5), // the same 10 minutes
		m(40*time.Minute, "gpt", "codex", 50, 0, 500, 20),
		m(40*time.Minute, "opus", "", 1, 0, 0, 1),    // typed in a terminal
		m(3*time.Hour, "opus", "claude", 9, 9, 9, 9), // before the range
	}
	points, total, models, launchers := usageSeries(rows, from, now, 600, usageFilter{}, nil)
	if len(points) != 6 || points[0].T != from.Unix() || points[5].T != now.Unix()/600*600 {
		t.Fatalf("points = %d from %d to %d", len(points), points[0].T, points[len(points)-1].T)
	}
	if p := points[5]; p.Input != 20 || p.CacheRead != 2000 || p.Requests != 2 {
		t.Fatalf("the last step = %+v", p)
	}
	if total.Total() != 20+100+2000+10+50+500+20+1+1 || total.Requests != 4 {
		t.Fatalf("total = %+v", total)
	}
	if len(models) != 2 || models[0].Name != "opus" || len(launchers) != 3 || launchers[0].Name != "claude" {
		t.Fatalf("models %+v launchers %+v", models, launchers)
	}

	// A launcher filter: the chart and total keep only it, the models list what it ran, the launchers stay all
	// (under no model filter) so the menu lists them.
	points, total, models, launchers = usageSeries(rows, from, now, 600, usageFilter{launcher: strp("codex")}, nil)
	if total.Requests != 1 || total.Output != 20 || len(models) != 1 || models[0].Name != "gpt" || len(launchers) != 3 {
		t.Fatalf("codex: total %+v models %+v launchers %+v", total, models, launchers)
	}
	// "" is the unknown launcher, not "all".
	if _, total, _, _ = usageSeries(rows, from, now, 600, usageFilter{launcher: strp("")}, nil); total.Requests != 1 || total.Output != 1 {
		t.Fatalf("unknown launcher: %+v", total)
	}
	// A model filter narrows the launchers' list the same way.
	if _, _, _, launchers = usageSeries(rows, from, now, 600, usageFilter{model: strp("gpt")}, nil); len(launchers) != 1 {
		t.Fatalf("gpt's launchers = %+v", launchers)
	}

	// Offline: a step without a monitoring sample and nothing counted is a break; a quiet step with a sample is 0.
	present := map[int64]bool{points[0].T: true, points[5].T: true}
	points, _, _, _ = usageSeries(rows, from, now, 600, usageFilter{}, present)
	var gaps []int
	for i, p := range points {
		if p.Gap {
			gaps = append(gaps, i)
		}
	}
	// Step 1 has the 40-minute-ago usage (counted, so not a break although no sample is in it).
	if len(gaps) != 3 || gaps[0] != 2 || gaps[2] != 4 {
		t.Fatalf("breaks at %v, want [2 3 4]", gaps)
	}
}

func TestUsageCard(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	now := time.Date(2026, 10, 29, 0, 1, 30, 0, loc) // a minute and a half into the day
	minute := now.Unix() / 60 * 60
	var book usageBook
	b, _ := json.Marshal(map[string]any{"full": true, "launchers": true, "buckets": []proto.UsageBucket{
		{Minute: minute, Model: "m", Launcher: "claude", Input: 10, CacheRead: 90, Output: 50, Requests: 1},
		{Minute: minute - 60, Model: "m", Launcher: "claude", Output: 100, Requests: 1},
		{Minute: minute - 120, Model: "m", Output: 1000, Requests: 1}, // yesterday here, and 2 minutes ago
		{Minute: minute - 3600, Model: "m", Output: 7, Requests: 1},   // an hour ago: off the sparkline
	}})
	book.apply("a", b, now)
	c := book.card("a", now, loc)
	if c == nil || c.Today.Total() != 250 || c.Today.Requests != 2 || c.Today.CacheRead != 90 {
		t.Fatalf("card = %+v", c)
	}
	if len(c.Recent) != 60 || c.Recent[59] != 150 || c.Recent[58] != 100 || c.Recent[57] != 1000 || c.Recent[0] != 0 {
		t.Fatalf("recent = %v", c.Recent)
	}
	// 1250 tokens over the 2.5 minutes from two minutes before this one.
	if c.PerMinute != 500 {
		t.Fatalf("per minute = %d", c.PerMinute)
	}
	if !book.knowsLaunchers("a") || book.card("nobody", now, loc) != nil {
		t.Fatal("launchers known / an unknown machine")
	}
}

// usageMemStore is a usageStore in memory: what was written, in order.
type usageMemStore struct {
	*memStore
	mu     sync.Mutex
	writes []usageWrite
	done   chan struct{}
}

func (s *usageMemStore) PutUsage(_ context.Context, machine string, buckets []proto.UsageBucket, replaceFrom time.Time) error {
	s.mu.Lock()
	s.writes = append(s.writes, usageWrite{machine: machine, buckets: buckets, replaceFrom: replaceFrom})
	s.mu.Unlock()
	s.done <- struct{}{}
	return nil
}
func (s *usageMemStore) AddUsage(_ context.Context, machine string, buckets []proto.UsageBucket) error {
	s.mu.Lock()
	s.writes = append(s.writes, usageWrite{machine: machine, buckets: buckets, add: true})
	s.mu.Unlock()
	s.done <- struct{}{}
	return nil
}
func (s *usageMemStore) RollupUsage(context.Context, time.Time) (int64, error) { return 0, nil }
func (s *usageMemStore) PruneUsage(context.Context, time.Time) (int64, error)  { return 0, nil }
func (s *usageMemStore) UsageSeries(_ context.Context, machine string, from, to time.Time, step int) ([]db.UsageRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.UsageRow
	for _, w := range s.writes {
		for _, b := range w.buckets {
			if w.machine == machine && b.Minute >= from.Unix() && b.Minute < to.Unix() {
				b.Minute = b.Minute / int64(step) * int64(step)
				out = append(out, b)
			}
		}
	}
	return out, nil
}

// usageBatchMemStore stores several writes at once (db.PutUsages); calls: how many each time.
type usageBatchMemStore struct {
	*usageMemStore
	calls []int
}

func (s *usageBatchMemStore) PutUsages(ctx context.Context, writes []db.UsageWrite) ([]error, error) {
	s.mu.Lock()
	s.calls = append(s.calls, len(writes))
	s.mu.Unlock()
	for _, w := range writes {
		_ = s.PutUsage(ctx, w.Machine, w.Buckets, w.ReplaceFrom)
	}
	return nil, nil
}

// A node's usage goes to Postgres in the order it came: its whole window first (replacing the window's minutes),
// then what changed; the machine's page reads its range from there. While another machine writes too, writes that
// come within usageGather of each other go in one transaction when the store can (PutUsages), else one PutUsage each.
func TestUsageIsStoredInOrder(t *testing.T) {
	for _, batched := range []bool{false, true} {
		t.Run(fmt.Sprint("batched=", batched), func(t *testing.T) {
			mem := &usageMemStore{memStore: newMemStore(), done: make(chan struct{}, 8)}
			var st Store = mem
			batch := &usageBatchMemStore{usageMemStore: mem}
			if batched {
				st = batch
			}
			d := New(Options{Hub: &fakeHost{}, Store: st})
			d.usageWait = time.Second
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go d.usageLoop(ctx)
			now := time.Now()
			minute := now.Unix() / 60 * 60
			send := func(machine string, full bool, out int64) {
				b, _ := json.Marshal(map[string]any{"full": full, "launchers": true,
					"buckets": []proto.UsageBucket{{Minute: minute, Model: "m", Launcher: "claude", Output: out, Requests: 1}}})
				d.onUsage(machine, b, now)
			}
			stored := func(n int) {
				t.Helper()
				for range n {
					select {
					case <-mem.done:
					case <-time.After(5 * time.Second):
						t.Fatal("usage not written")
					}
				}
			}
			send("b", false, 5) // another machine that writes
			stored(1)
			send("a", true, 10)
			send("a", false, 30)
			stored(2)
			mem.mu.Lock()
			defer mem.mu.Unlock()
			if len(mem.writes) != 3 || mem.writes[0].machine != "b" {
				t.Fatalf("writes = %+v", mem.writes)
			}
			if a := mem.writes[1:]; a[0].machine != "a" || a[1].machine != "a" || a[0].replaceFrom.IsZero() ||
				!a[1].replaceFrom.IsZero() || a[1].buckets[0].Output != 30 {
				t.Fatalf("writes = %+v", mem.writes)
			}
			if from := mem.writes[1].replaceFrom; now.Sub(from) > usageNodeWindow || now.Sub(from) < usageNodeWindow-2*time.Minute {
				t.Fatalf("a full window replaces from %v (%v ago), want the node's window", from, now.Sub(from))
			}
			if batched && !slices.Equal(batch.calls, []int{1, 2}) {
				t.Fatalf("PutUsages calls = %v, want b's write alone, then a's two in one", batch.calls)
			}
		})
	}
}

// gatherUsage takes what is queued, and waits for more only while another machine wrote in the last usageActive: one
// busy machine's writes are not held back.
func TestUsageGatherWaitsOnlyForOthers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := New(Options{Hub: &fakeHost{}})
		d.usageWait = time.Minute
		w := func(machine string) usageWrite { return usageWrite{machine: machine} }
		// gather starts gatherUsage with a's write queued already, and returns once it has ended or is blocked on its
		// wait (synctest.Wait); done is when it ended (the bubble's clock moves only while everything waits) and what
		// it took.
		gather := func(active map[string]time.Time) (done func() (time.Duration, []string)) {
			t.Helper()
			d.usageWrites <- w("a")
			start := time.Now()
			got := make(chan []usageWrite, 1)
			go func() { got <- d.gatherUsage(context.Background(), w("a"), active) }()
			synctest.Wait()
			return func() (time.Duration, []string) {
				var machines []string
				for _, w := range <-got {
					machines = append(machines, w.machine)
				}
				return time.Since(start), machines
			}
		}

		// Alone (b wrote long ago): it takes what is queued and does not wait.
		active := map[string]time.Time{"a": time.Now(), "b": time.Now().Add(-usageActive - time.Second)}
		if took, got := gather(active)(); took != 0 || !slices.Equal(got, []string{"a", "a"}) {
			t.Fatalf("alone: %v after %v, want a's two at once", got, took)
		}
		if _, ok := active["b"]; ok {
			t.Fatal("a machine quiet for longer than usageActive is still active")
		}

		// b wrote lately: it waits usageWait, and takes b's write that comes meanwhile.
		active["b"] = time.Now()
		done := gather(active)
		d.usageWrites <- w("b")
		if took, got := done(); took != d.usageWait || !slices.Equal(got, []string{"a", "a", "b"}) {
			t.Fatalf("with b: %v after %v, want a's two and b's after %v", got, took, d.usageWait)
		}
	})
}

// GET /api/v1/machines/{id}/usage: the range, the filters, an unknown machine and range.
func TestUsageRoute(t *testing.T) {
	d := newTestDevices(t)
	entry, _ := json.Marshal(MachineConfig{ID: "a", Name: "a", Kind: kindLink})
	d.Configure(devicewire.Config{Machines: []json.RawMessage{entry}})
	if d.machineByID("a") == nil {
		t.Fatal("machine not started")
	}
	now := time.Now()
	b, _ := json.Marshal(map[string]any{"full": true, "launchers": true, "buckets": []proto.UsageBucket{
		{Minute: now.Unix()/60*60 - 60, Model: "opus", Launcher: "claude", Input: 1, CacheRead: 9, Output: 2, Requests: 1},
		{Minute: now.Unix()/60*60 - 120, Model: "gpt", Launcher: "codex", Output: 4, Requests: 1},
	}})
	d.onUsage("a", b, now)
	mux := http.NewServeMux()
	d.usageRoutes(mux)
	get := func(url string) (int, MachineUsageSeries) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		var body struct {
			Usage MachineUsageSeries `json:"usage"`
		}
		json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.Usage
	}
	code, u := get("/api/v1/machines/a/usage?range=1h")
	if code != 200 || u.Step != 60 || len(u.Points) != 60 || u.Total.Total() != 16 || !u.LaunchersKnown || u.Range != "1h" {
		t.Fatalf("1h: %d %+v", code, u)
	}
	if _, u = get("/api/v1/machines/a/usage?range=1h&launcher=codex"); u.Total.Output != 4 || len(u.Launchers) != 2 {
		t.Fatalf("codex: %+v", u)
	}
	if _, u = get("/api/v1/machines/a/usage?range=1h&launcher=-"); u.Total.Requests != 0 {
		t.Fatalf("the unknown launcher: %+v", u)
	}
	if _, u = get("/api/v1/machines/a/usage"); u.Range != "24h" || u.Step != 600 {
		t.Fatalf("default range: %+v", u)
	}
	if code, _ = get("/api/v1/machines/a/usage?range=6h"); code != http.StatusBadRequest {
		t.Fatalf("6h: %d", code)
	}
	if code, _ = get("/api/v1/machines/nope/usage"); code != http.StatusNotFound {
		t.Fatalf("unknown machine: %d", code)
	}
}
