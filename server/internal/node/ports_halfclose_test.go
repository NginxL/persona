package node

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

func TestPortDialNegotiatesWriteEOF(t *testing.T) {
	n, _ := testNode(t)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	hubPeer, nodePeer := rpc.New("h", a), rpc.New("n", b)
	hubMux := streams.New(hubPeer.Notify)
	n.streams = streams.New(nodePeer.Notify)
	defer hubMux.CloseAll()
	defer n.streams.CloseAll()
	type streamEvent struct{ event, id string }
	events := make(chan streamEvent, 8)
	hubPeer.OnEvent = func(event string, p json.RawMessage) {
		hubMux.Event(event, p)
		if event == streams.EventEOF || event == streams.EventClose {
			var id struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(p, &id)
			events <- streamEvent{event, id.ID}
		}
	}
	nodePeer.OnEvent = func(event string, p json.RawMessage) { n.streams.Event(event, p) }
	nodePeer.OnRequest = n.handle
	go func() { _ = hubPeer.Serve(t.Context(), a) }()
	go func() { _ = nodePeer.Serve(t.Context(), b) }()

	for _, version := range []int{0, devicewire.PortStreamHalfCloseV1, 99} {
		ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		_ = ln.SetDeadline(time.Now().Add(5 * time.Second))
		dial := devicewire.PortDial{ID: ln.Addr().String(), Port: ln.Addr().(*net.TCPAddr).Port, StreamVersion: version}
		if err := hubMux.Open(dial.ID); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		var res devicewire.PortDialReply
		err = hubPeer.Call(ctx, devicewire.MethodPortDial, dial, &res)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if version == devicewire.PortStreamHalfCloseV1 {
			want = version
		}
		if res.StreamVersion != want {
			t.Fatalf("offered %d, node selected %d, want %d", version, res.StreamVersion, want)
		}
		server, err := ln.AcceptTCP()
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(5 * time.Second))
		if err := server.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		wantEvent := streams.EventClose
		if want != 0 {
			wantEvent = streams.EventEOF
		}
		eventTimeout := time.After(5 * time.Second)
		var gotEvent string
		for gotEvent == "" {
			select {
			case msg := <-events:
				if msg.id == dial.ID {
					gotEvent = msg.event
				}
			case <-eventTimeout:
				t.Fatalf("offer %d: timed out waiting for %s", version, wantEvent)
			}
		}
		if gotEvent != wantEvent {
			t.Fatalf("offer %d: got %s, want %s", version, gotEvent, wantEvent)
		}
		if want == 0 {
			if got, err := io.ReadAll(server); err != nil || len(got) != 0 {
				t.Fatalf("legacy offer %d: %q, %v", version, got, err)
			}
		} else {
			// The hub is still able to send after the node delivered its EOF.
			if err := hubPeer.Notify(streams.EventData, map[string]any{"id": dial.ID, "data": []byte("response")}); err != nil {
				t.Fatal(err)
			}
			if err := hubPeer.Notify(streams.EventEOF, map[string]any{"id": dial.ID}); err != nil {
				t.Fatal(err)
			}
			if got, err := io.ReadAll(server); err != nil || string(got) != "response" {
				t.Fatalf("negotiated reverse direction: %q, %v", got, err)
			}
		}
	}
	waitFor(t, "all port dials should retire", func() bool { return n.streams.Len() == 0 })
}
