package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestRuntimeItemsKeepUpdateInternal(t *testing.T) {
	for _, lazy := range []bool{false, true} {
		t.Run(fmt.Sprintf("lazy=%v", lazy), func(t *testing.T) {
			c := &client{out: make(chan []byte, 2), lazy: lazy, subs: map[[2]string]bool{{"m1", "s1"}: true}}
			m := newMachine(&Hub{clients: map[*client]struct{}{c: {}}}, MachineConfig{ID: "m1"})
			for _, update := range []bool{true, false} {
				payload, err := json.Marshal(map[string]any{
					"session_id": "s1", "items": []proto.Item{
						{ID: "i5", Seq: 5, Kind: proto.KindText, Text: "revised"},
						{ID: "i6", Seq: 6, Kind: proto.KindText, Text: "appended"},
					},
					"history": "h1", "from_seq": 5, "to_seq": 6, "update": update,
				})
				if err != nil {
					t.Fatal(err)
				}
				m.onEvent("session.items", payload)
				var got map[string]any
				if err := json.Unmarshal(wireReceive(t, c), &got); err != nil {
					t.Fatal(err)
				}
				if _, present := got["update"]; present {
					t.Fatalf("internal update flag conflicts with the App envelope: %v", got)
				}
				if got["type"] != "items" || got["from_seq"] != float64(5) || got["to_seq"] != float64(6) {
					t.Fatalf("item span changed: %v", got)
				}
				items, ok := got["items"].([]any)
				if !ok || len(items) != 2 {
					t.Fatalf("mixed batch lost items: %v", got)
				}
				for i, want := range []struct{ id, text string }{{"i5", "revised"}, {"i6", "appended"}} {
					item, ok := items[i].(map[string]any)
					if !ok || item["id"] != want.id || item["text"] != want.text {
						t.Fatalf("mixed batch item %d changed: %v", i, items[i])
					}
				}
			}
		})
	}
}

func TestRuntimeItemsResetWatchCursor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    int
	}{
		{"new history", `{"history":"h2","from_seq":4,"to_seq":5}`, 5},
		{"zero origin", `{"history":"h1","from_seq":0,"to_seq":5}`, 5},
		{"zero origin without history", `{"from_seq":0,"to_seq":5}`, 5},
		{"empty rewrite", `{"history":"h2","from_seq":0,"to_seq":0}`, 0},
		{"new history without origin", `{"history":"h2","to_seq":5}`, 5},
		{"same history", `{"history":"h1","from_seq":4,"to_seq":5}`, 20},
		{"missing history", `{"from_seq":4,"to_seq":5}`, 20},
		{"missing origin", `{"history":"h1","to_seq":5}`, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, m := newWatchNode(t)
			m.addWatcher(context.Background(), "s1")
			m.onEvent("session.items", json.RawMessage(`{"session_id":"s1","items":[],"history":"h1","from_seq":0,"to_seq":20}`))
			var event map[string]any
			if err := json.Unmarshal([]byte(tc.payload), &event); err != nil {
				t.Fatal(err)
			}
			event["session_id"], event["items"] = "s1", []proto.Item{}
			payload, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			m.onEvent("session.items", payload)
			m.removeWatcher("s1")
			m.addWatcher(context.Background(), "s1")
			n.mu.Lock()
			got := n.since["s1"]
			n.mu.Unlock()
			if got != tc.want {
				t.Fatalf("rewatch since_seq = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRuntimeItemsHistoryPrunedWithCursor(t *testing.T) {
	for _, settle := range []bool{false, true} {
		t.Run(fmt.Sprintf("settle=%v", settle), func(t *testing.T) {
			m := newMachine(&Hub{}, MachineConfig{ID: "m1"})
			m.onEvent("session.items", json.RawMessage(`{"session_id":"s1","items":[],"history":"h1","to_seq":20}`))
			m.setSessionList([]proto.Session{{SessionID: "s1"}})
			if m.lastHistory["s1"] != "h1" || m.lastSeq["s1"] != 20 {
				t.Fatal("listed session lost its history or cursor")
			}
			if settle {
				m.mu.Lock()
				m.sessions = nil
				m.mu.Unlock()
				m.settleEnded("s1")
			} else {
				m.setSessionList(nil)
			}
			if len(m.lastSeq) != 0 || len(m.lastHistory) != 0 {
				t.Fatalf("ended session retained cursors %v or histories %v", m.lastSeq, m.lastHistory)
			}
		})
	}
}

func TestRuntimeWatchSendsKnownHistory(t *testing.T) {
	n, m := newWatchNode(t)
	m.addWatcher(context.Background(), "s1")
	n.mu.Lock()
	initial := n.history["s1"]
	n.mu.Unlock()
	if initial != nil {
		t.Fatal("initial watch includes an unknown history")
	}
	m.onEvent("session.items", json.RawMessage(`{"session_id":"s1","items":[],"history":"h1","from_seq":0,"to_seq":20}`))
	m.removeWatcher("s1")
	m.addWatcher(context.Background(), "s1")
	n.mu.Lock()
	seq, history := n.since["s1"], n.history["s1"]
	n.mu.Unlock()
	if seq != 20 || history == nil || *history != "h1" {
		t.Fatalf("rewatch lost its cursor identity: seq=%d history=%v", seq, history)
	}
}
