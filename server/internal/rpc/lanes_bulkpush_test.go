package rpc

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/netshape"
)

// A slow link with deep buffers on the way, as a node behind Persona Link has: about 200 KB/s, a 300 ms round trip,
// and 512 KiB the path takes before a writer waits (the sender's socket, Link's WebSocket, the hub's relay). Scaled up
// from the 100 KB/s of #13677 so the test runs in seconds; what matters is the queue ahead of a pong.
var bulkPushLink = netshape.Config{Rate: 200_000, Delay: 150 * time.Millisecond, Queue: 512 << 10}

// bulkPushPeers is a framed pair over bulkPushLink: h asks, n answers and pushes. bulky's answer is large.
func bulkPushPeers(t *testing.T, creditReplies bool, big string) (h, n *Peer) {
	t.Helper()
	a, b := netshape.Pipe(bulkPushLink, bulkPushLink)
	h, n = New("h", a), New("n", b)
	h.EnableLanes(testControl, testBulk)
	n.EnableLanes(testControl, testBulk)
	if creditReplies {
		n.CreditReplies("bulky")
	}
	n.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "bulky" {
			return map[string]string{"data": big}, nil
		}
		return map[string]bool{"ok": true}, nil
	}
	go func() { _ = h.Serve(context.Background(), a) }()
	go func() { _ = n.Serve(context.Background(), b) }()
	t.Cleanup(func() { a.Close(); b.Close() })
	if err := h.UpgradeLanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	return h, n
}

// underway waits until n has written frames of its large lines.
func underway(t *testing.T, n *Peer) {
	t.Helper()
	eventually(t, func() bool {
		st := n.LaneStats()
		return st.Lanes[LaneInteractive].SentFrames+st.Lanes[LaneEvents].SentFrames+st.Lanes[LaneBulk].SentFrames >= 12
	})
}

// pingDuring is how long h's ping takes once start has put about a megabyte on its way from n.
func pingDuring(t *testing.T, h, n *Peer, start func()) time.Duration {
	t.Helper()
	go start()
	underway(t, n) // the large lines are under way
	began := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := h.Call(ctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatalf("ping: %v after %s", err, time.Since(began))
	}
	return time.Since(began)
}

// A node's pushes of a session's items (#13677): on the events lane they go to the transport at once, and a pong waits
// behind the path's whole buffer; NotifyBulk sends them with credit, and the pong passes them.
func TestNotifyBulkLeavesRoomForThePong(t *testing.T) {
	big := strings.Repeat("i", 120_000)
	push := func(n *Peer, bulk bool) func() {
		return func() {
			for range 10 { // ten pushes of 120 KB, as a catch-up of a session with screenshots made
				if bulk {
					_ = n.NotifyBulk(context.Background(), "session.items", map[string]string{"items": big})
				} else {
					_ = n.Notify("session.items", map[string]string{"items": big})
				}
			}
		}
	}
	h, n := bulkPushPeers(t, false, "")
	before := pingDuring(t, h, n, push(n, false))
	h, n = bulkPushPeers(t, false, "")
	after := pingDuring(t, h, n, push(n, true))
	t.Logf("ping behind 1.2 MB of pushes: events lane %s, bulk with credit %s", before, after)
	if before < 2*time.Second {
		t.Fatalf("the events lane's ping took only %s: the test proves nothing", before)
	}
	if after > 1500*time.Millisecond {
		t.Fatalf("ping took %s behind bulk pushes", after)
	}
	if got := n.LaneStats().Lanes[LaneBulk].SentFrames; got == 0 {
		t.Fatal("NotifyBulk sent nothing on the bulk lane")
	}
}

// An answer named by CreditReplies waits for credit; the same answer without it goes to the transport at once, ahead
// of the pong.
func TestCreditRepliesLeaveRoomForThePong(t *testing.T) {
	big := strings.Repeat("r", 1_200_000)
	ask := func(h *Peer) func() {
		return func() {
			var out map[string]string
			_ = h.Call(context.Background(), "bulky", map[string]any{}, &out)
		}
	}
	h, n := bulkPushPeers(t, false, big)
	before := pingDuring(t, h, n, ask(h))
	h, n = bulkPushPeers(t, true, big)
	after := pingDuring(t, h, n, ask(h))
	t.Logf("ping behind a 1.2 MB answer: without credit %s, with credit %s", before, after)
	if before < 2*time.Second {
		t.Fatalf("the interactive answer's ping took only %s: the test proves nothing", before)
	}
	if after > 1500*time.Millisecond {
		t.Fatalf("ping took %s behind an answer with credit", after)
	}
	if got := n.LaneStats().Lanes[LaneInteractive].SentFrames; got == 0 {
		t.Fatal("the answer did not stay on the interactive lane")
	}
}

// Without lanes both are what they were: NotifyBulk is Notify, CreditReplies changes nothing.
func TestBulkWithoutLanes(t *testing.T) {
	hc, nc := net.Pipe()
	h, n := New("h", hc), New("n", nc)
	n.CreditReplies("file.chunk")
	if n.creditReplies != nil {
		t.Fatal("CreditReplies without lanes recorded methods")
	}
	got := make(chan string, 1)
	h.OnEvent = func(event string, params json.RawMessage) { got <- event + string(params) }
	go func() { _ = h.Serve(context.Background(), hc) }()
	go func() { _ = n.Serve(context.Background(), nc) }()
	t.Cleanup(func() { hc.Close(); nc.Close() })
	if err := n.NotifyBulk(context.Background(), "session.items", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		if e != `session.items{"a":1}` {
			t.Fatalf("got %s", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
}

// A person's read (an answer with credit, interactive) is not held behind a catch-up's pushes (bulk with credit): it
// goes ahead of them, as it did when the pushes went to the transport at once and the read waited behind them all.
func TestCreditReplyPassesBulkPushes(t *testing.T) {
	page := strings.Repeat("p", 60_000)
	h, n := bulkPushPeers(t, true, page)
	pushed := make(chan struct{})
	go func() {
		defer close(pushed)
		big := strings.Repeat("i", 120_000)
		for range 10 { // 1.2 MB, about 6 s on this link
			_ = n.NotifyBulk(context.Background(), "session.items", map[string]string{"items": big})
		}
	}()
	underway(t, n)
	began := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out map[string]string
	if err := h.Call(ctx, "bulky", map[string]any{}, &out); err != nil || len(out["data"]) != len(page) {
		t.Fatalf("read: %v after %s", err, time.Since(began))
	}
	took := time.Since(began)
	select {
	case <-pushed:
		t.Fatal("the pushes finished first: the test proves nothing")
	default:
	}
	t.Logf("a 60 KB read behind 1.2 MB of pushes took %s", took)
	if took > 2*time.Second {
		t.Fatalf("the read took %s behind the pushes", took)
	}
}
