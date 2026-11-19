package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/link"
)

// What Persona Link is told of the computer's resident node (`node`, persona-link status's "Local node"): a link
// session starts at "unknown" and shows what the manager tells it (#11262).

// overLink is Persona Link's side of the streams the manager asks for on machine's link: each one is a fresh transport
// to n, as persona-link connects the node's socket for every stream, its bytes forwarded both ways until either ends.
func (n *residentNode) overLink(t *testing.T, d *Devices, machine string) func(*testLinkClient, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		d.acceptLinkStream(conn, devicewire.Accept{ID: "relay-stream", Kind: kindLink, MachineID: machine, TokenID: "token-1",
			StreamID: r.URL.Query().Get("id")})
	}))
	t.Cleanup(srv.Close)
	return func(_ *testLinkClient, id string) {
		ctx := context.Background()
		node, err := n.dial(ctx)
		if err != nil {
			return
		}
		ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/?id="+id, nil)
		if err != nil {
			_ = node.Close()
			return
		}
		ws.SetReadLimit(64 << 20)
		stream := websocket.NetConn(ctx, ws, websocket.MessageBinary)
		go func() {
			_, _ = io.Copy(stream, node)
			_ = stream.Close()
		}()
		_, _ = io.Copy(node, stream)
		_ = node.Close()
	}
}

// loggedDevices is newTestDevices whose log lines are kept.
func loggedDevices(t *testing.T) (*Devices, *benchLog) {
	t.Helper()
	d := newTestDevices(t)
	logs := &benchLog{}
	quiet := d.logf
	d.logf = func(format string, args ...any) {
		logs.add(fmt.Sprintf(format, args...))
		quiet(format, args...)
	}
	return d, logs
}

// The link persona-link restarted with gets no new node connection: the one it had is resumed over it (#10663). The
// new link is told the node is online once that is done, not while the connection still waits (#11262).
func TestLinkHearsTheNodeOnlineAfterAResume(t *testing.T) {
	d, logs := loggedDevices(t)
	n := &residentNode{release: make(chan struct{}), slowSeen: make(chan struct{})}
	d.Configure(devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{linkEntry("Laptop")}})
	first, _ := dialTestLinkOpening(t, d, link.Message{Type: "hello", Machine: "lap", Version: "first"}, n.overLink(t, d, "lap"))
	if msg := first.next(t, "node"); !msg.Online {
		t.Fatalf("the first link: %+v", msg)
	}
	eventually(t, 10*time.Second, "lap connected, resumable", func() bool { return logs.count("lanes=on resume=on") > 0 })

	_ = first.conn.CloseNow() // persona-link restarts
	eventually(t, 10*time.Second, "the connection waits to be resumed", func() bool { return logs.count("resuming within") > 0 })
	again, _ := dialTestLinkOpening(t, d, link.Message{Type: "hello", Machine: "lap", Version: "again"}, n.overLink(t, d, "lap"))
	msg := again.next(t, "node")
	if !msg.Online || msg.Error != "" {
		t.Fatalf("the link that came back: %+v", msg)
	}
	if logs.count("resumed after") == 0 {
		t.Fatalf("told online before the connection was resumed: %q", logs.since(0))
	}
	if transports, fresh := n.counts(); transports != 2 || fresh != 1 {
		t.Fatalf("%d transports, %d served from the start: want 2 and 1", transports, fresh)
	}
	if c := logs.count("] connected over "); c != 1 {
		t.Fatalf("connected %d times", c)
	}
}

// A link that comes while the computer's node is connected through another of its paths hears that at once: nothing
// about the node changes for it to be told otherwise (#11262).
func TestLinkThatComesHearsTheNodeIsConnected(t *testing.T) {
	d, _ := loggedDevices(t)
	nd := &testNode{}
	box := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	box.dialNode = nd.dial
	lap := newMachine(d, MachineConfig{ID: "lap", Name: "Laptop", Kind: kindLink})
	d.addEntry(box)
	d.addEntry(lap)
	f := &d.fleet
	f.mu.Lock()
	f.init()
	f.paths[box] = []*nodePath{cfgPath(box), cfgPath(lap)}
	f.absorbed[lap] = box // the same computer: box's entry has it
	f.mu.Unlock()
	runMachine(t, box)
	eventually(t, 5*time.Second, "box connected", func() bool { return box.reachable() && len(nd.seen("sessions.list")) > 0 })

	for _, version := range []string{"first", "again"} {
		c, _ := dialTestLinkOpening(t, d, link.Message{Type: "hello", Machine: "lap", Version: version}, nil)
		if msg := c.next(t, "node"); !msg.Online || msg.Error != "" {
			t.Fatalf("link %s: %+v", version, msg)
		}
		_ = c.conn.CloseNow()
		eventually(t, 5*time.Second, "link "+version+" gone", func() bool { return lap.currentLink() == nil })
	}
}

// A computer whose resident node does not answer stays offline for every link that comes: a new one is never told
// online by what the manager knows, only offline, by the try its coming starts.
func TestLinkHearsTheNodeOfflineWhenItIs(t *testing.T) {
	d, _ := loggedDevices(t)
	d.Configure(devicewire.Config{HubID: "hub-test", HubName: "test", Machines: []json.RawMessage{linkEntry("Laptop")}})
	noNode := func(c *testLinkClient, id string) { // persona-link cannot reach the node's socket
		b, _ := json.Marshal(link.Message{Type: "open_failed", ID: id, Error: "no resident node"})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.conn.Write(ctx, websocket.MessageText, b)
	}
	for _, version := range []string{"first", "again"} {
		c, _ := dialTestLinkOpening(t, d, link.Message{Type: "hello", Machine: "lap", Version: version}, noNode)
		if msg := c.next(t, "node"); msg.Online || !strings.Contains(msg.Error, "no resident node") {
			t.Fatalf("link %s: %+v", version, msg)
		}
		_ = c.conn.CloseNow()
		eventually(t, 5*time.Second, "link "+version+" gone", func() bool { return d.cfgMachine("lap").currentLink() == nil })
	}
}

// What a link is told of the node goes out newest last, however the goroutines sending it run: a node that went
// offline right after it came online is never left showing online.
func TestLinkKeepsTheNewestNodeState(t *testing.T) {
	d := newTestDevices(t)
	d.addEntry(newMachine(d, MachineConfig{ID: "lap", Name: "Laptop", Kind: kindLink}))
	c, l := dialTestLink(t, d, "lap")
	const tells = 20
	for i := range tells {
		l.tellNode(true, "", nil)
		l.tellNode(false, fmt.Sprintf("try %d", i), nil) // the newest: offline, "try 19"
	}
	var last link.Message
	sent := 0
	for quiet := false; !quiet; {
		select {
		case m := <-c.msgs:
			if m.Type == "node" {
				last = m
				sent++
			}
		case <-time.After(500 * time.Millisecond):
			quiet = true
		}
	}
	if sent == 0 || sent > 2*tells || last.Online || last.Error != "try 19" {
		t.Fatalf("%d sent, the last %+v: want offline, try 19", sent, last)
	}
}
