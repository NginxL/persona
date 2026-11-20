package node

import (
	"encoding/json"
	"testing"
)

func TestRuntimeWatchCarriesHistoryAcrossReconnect(t *testing.T) {
	n, _ := runtimeNode(t)
	first, _ := listedRuntime(t, n, "watch-history")
	if _, err := nodeCall(t, n, "session.watch", map[string]any{"session_id": "watch-history", "since_seq": 20, "history": "old"}); err != nil {
		t.Fatal(err)
	}
	check := func(rt *fakeRuntime) {
		t.Helper()
		waitFor(t, "watch received", func() bool { return len(rt.got("session.watch")) == 1 })
		var p struct {
			SinceSeq int    `json:"since_seq"`
			History  string `json:"history"`
		}
		if err := json.Unmarshal(rt.got("session.watch")[0], &p); err != nil {
			t.Fatal(err)
		}
		if p.SinceSeq != 20 || p.History != "old" {
			t.Fatalf("watch lost cursor identity: %+v", p)
		}
	}
	check(first)
	_ = first.conn.Close()
	waitFor(t, "runtime gone", func() bool {
		n.runtimes.mu.Lock()
		defer n.runtimes.mu.Unlock()
		return n.runtimes.owner["watch-history"] == nil
	})
	next, _ := listedRuntime(t, n, "watch-history")
	check(next)
}
