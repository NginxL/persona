package devices

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

type runtimeUsageStore struct {
	*usageMemStore
	saved []db.UsageWrite
}

func (s *runtimeUsageStore) AddUsage(_ context.Context, m string, b []proto.UsageBucket) error {
	s.saved = append(s.saved, db.UsageWrite{Machine: m, Buckets: b, Add: true})
	return nil
}

type runtimeUsageBatchStore struct{ *runtimeUsageStore }

func (s *runtimeUsageBatchStore) PutUsages(_ context.Context, w []db.UsageWrite) ([]error, error) {
	s.saved = append(s.saved, w...)
	return nil, nil
}

func TestRuntimeUsageCrossesProcessingMinute(t *testing.T) {
	store := &runtimeUsageStore{usageMemStore: &usageMemStore{memStore: newMemStore(), done: make(chan struct{}, 2)}}
	d := New(Options{Hub: &fakeHost{}, Store: store})
	now := time.Now().Truncate(time.Minute)
	bucket := proto.UsageBucket{Minute: now.Add(-usageNodeWindow).Unix(), Model: "m", Agent: "grok", Output: 5, Requests: 1}
	data, err := json.Marshal(map[string]any{"add": true, "buckets": []proto.UsageBucket{bucket}})
	if err != nil {
		t.Fatal(err)
	}
	d.onUsage("box", data, now.Add(55*time.Second))
	d.onUsage("box", data, now.Add(time.Minute))
	d.saveUsage(context.Background(), store, []usageWrite{<-d.usageWrites, <-d.usageWrites})
	live := d.usage.buckets("box")
	if len(live) != 1 || live[0].Output != 10 || live[0].Requests != 2 {
		t.Fatalf("live boundary totals: %+v", live)
	}
	if len(store.saved) != 2 || store.saved[0].Buckets[0] != bucket || store.saved[1].Buckets[0] != bucket {
		t.Fatalf("stored boundary writes: %+v", store.saved)
	}
}

func TestRuntimeUsageAddSurvivesFull(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	for _, batch := range []bool{false, true} {
		s := &runtimeUsageStore{usageMemStore: &usageMemStore{memStore: newMemStore(), done: make(chan struct{}, 8)}}
		var st Store = s
		if batch {
			st = &runtimeUsageBatchStore{s}
		}
		d := New(Options{Hub: &fakeHost{}, Store: st})
		b := func(agent string, out int64) proto.UsageBucket {
			return proto.UsageBucket{Minute: now.Unix(), Model: "m", Agent: agent, Output: out, Requests: 1}
		}
		send := func(add, full bool, bs ...proto.UsageBucket) {
			p, _ := json.Marshal(map[string]any{"add": add, "full": full, "buckets": bs, "load": 4, "cpus": 2, "launchers": true})
			d.onUsage("box", p, now)
		}
		send(false, true, b("claude", 50), b("", 100))
		send(true, false, b("grok", 10))
		send(true, false, b("grok", 20))
		send(false, true, b("claude", 5))
		got := d.usage.buckets("box")
		var total int64
		for _, x := range got {
			total += x.Output
		}
		if total != 35 || len(got) != 2 {
			t.Fatalf("full erased runtime deltas: %+v", got)
		}
		send(true, true, b("grok", 1000))
		if len(d.usageWrites) != 4 {
			t.Fatal("accepted mixed full/add")
		}
		var writes []usageWrite
		for len(d.usageWrites) > 0 {
			writes = append(writes, <-d.usageWrites)
		}
		d.saveUsage(context.Background(), d.usageStore(), writes)
		if batch {
			if len(s.saved) != 4 || !s.saved[1].Add || s.saved[0].Add || s.saved[3].ReplaceFrom.IsZero() {
				t.Fatalf("batch flags lost: %+v", s.saved)
			}
		} else {
			if len(s.saved) != 2 || !s.saved[0].Add || !s.saved[1].Add {
				t.Fatalf("fallback lost deltas: %+v", s.saved)
			}
		}
	}
}
