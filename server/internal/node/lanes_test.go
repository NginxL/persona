package node

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

// A node announces lanes, and frames its connection when a manager that knows them asks; with one that does not, it
// writes JSON lines as before (docs/protocol/protocol.md "Lanes").
func TestNodeServesLanes(t *testing.T) {
	for _, managerLanes := range []bool{true, false} {
		home := shortHome(t)
		t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
		file := filepath.Join(home, "big.txt")
		if err := os.WriteFile(file, bytes.Repeat([]byte("persona lanes\n"), 30_000), 0o600); err != nil {
			t.Fatal(err)
		}
		n, err := New("test")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(n.starts.close)
		n.logger.SetOutput(io.Discard)

		ctx, cancel := context.WithCancel(context.Background())
		n2hR, n2hW := io.Pipe()
		h2nR, h2nW := io.Pipe()
		seen := &firstBytes{r: n2hR}
		hub := rpc.New("h", h2nW)
		if managerLanes {
			hub.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
		}
		hub.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return map[string]any{}, nil }
		serveDone := make(chan error, 1)
		go func() { serveDone <- n.Serve(ctx, h2nR, n2hW) }()
		go func() { _ = hub.Serve(ctx, seen) }()

		cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
		var info proto.NodeInfo
		if err := hub.Call(cctx, "node.info", map[string]any{}, &info); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(info.Capabilities, nodeproto.NodeLanes) {
			t.Fatalf("node.info does not announce %s", nodeproto.NodeLanes)
		}
		if managerLanes {
			if err := hub.UpgradeLanes(cctx); err != nil {
				t.Fatal(err)
			}
		}
		var read FileContent
		if err := hub.Call(cctx, "file.read", map[string]any{"path": file}, &read); err != nil {
			t.Fatal(err)
		}
		if read.Size != 14*30_000 {
			t.Fatalf("file.read: %d bytes", read.Size)
		}
		if err := hub.Call(cctx, "ping", map[string]any{}, nil); err != nil {
			t.Fatal(err)
		}
		if hub.Framed() != managerLanes {
			t.Fatalf("manager lanes=%t, framed=%t", managerLanes, hub.Framed())
		}
		ccancel()
		if stats := hub.LaneStats(); managerLanes && stats.Lanes[rpc.LaneControl].SentFrames == 0 {
			t.Fatalf("the ping went on no control frame: %s", stats)
		}
		// Every line before the switch, and all of them without it, are JSON.
		if seen.b != '{' {
			t.Fatalf("the node's output began with %q", seen.b)
		}
		cancel()
		h2nW.Close()
		n2hW.Close()
		select {
		case <-serveDone:
		case <-time.After(10 * time.Second):
			t.Fatal("Serve did not return")
		}
	}
}

// firstBytes notes the first byte read through it.
type firstBytes struct {
	r    io.Reader
	b    byte
	seen bool
}

func (f *firstBytes) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if n > 0 && !f.seen {
		f.b, f.seen = p[0], true
	}
	return n, err
}

// The lane table names the node's own methods and events: a renamed method would quietly leave bulk.
func TestLaneTableNamesTheNodesMethods(t *testing.T) {
	for _, name := range []string{methodFileChunk, methodFileRead, methodFileUploadChunk, "node.update.chunk", streams.EventData} {
		if !slices.Contains(nodeproto.BulkLane, name) {
			t.Errorf("%s is not on the bulk lane", name)
		}
	}
	if !slices.Contains(nodeproto.ControlLane, streams.EventAck) || !slices.Contains(nodeproto.ControlLane, "ping") {
		t.Errorf("control lane: %v", nodeproto.ControlLane)
	}
	n := &Node{}
	if _, err := n.handle(context.Background(), "node.update.chunk", json.RawMessage(`{}`)); err == nil || strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("node.update.chunk is not a method of the node any more: %v", err)
	}
}
