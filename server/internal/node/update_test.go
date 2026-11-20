package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The accepted commit's answer signals the restart once the whole of it reached the transport, never when its write
// failed or fell short: a line on its own, or the last frame of a framed connection.
func TestUpdateRestartsOnlyAfterSuccessfulRPCResponseWrite(t *testing.T) {
	for _, framed := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			writer io.Writer
			want   int
		}{
			{"written", new(bytes.Buffer), 1},
			{"failed", updateFailWriter{}, 0},
			{"short", updateShortWriter{}, 0},
		} {
			if framed && tc.want == 0 {
				continue // a writer that fails cannot take the switch: framed, it is the line case again
			}
			t.Run(fmt.Sprintf("%s framed=%t", tc.name, framed), func(t *testing.T) {
				var calls atomic.Int32
				p := rpc.New("n", tc.writer)
				p.LineWritten = updateResponseWritten(func(id string) {
					calls.Add(1)
					if id != "upload-1" {
						t.Fatal("wrong transaction acknowledged")
					}
					if b, ok := tc.writer.(*bytes.Buffer); ok && !bytes.Contains(b.Bytes(), []byte(`"accepted":true,"id":"upload-1"`)) {
						t.Fatal("restart signaled before complete reply reached transport")
					}
				})
				p.TrackReplies = true
				replied := make(chan struct{})
				p.OnRequest = func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
					rpc.AfterReply(ctx, func() { close(replied) }) // once the answer's write returned, LineWritten told or not
					return proto.NodeUpdateCommitResult{Accepted: true, ID: "upload-1"}, nil
				}
				send, end := servePeer(t, p, framed)
				send(`{"id":"h1","method":"node.update.commit","params":{}}`)
				defer end()
				select {
				case <-replied:
				case <-time.After(5 * time.Second):
					t.Fatal("no answer")
				}
				if int(calls.Load()) != tc.want {
					t.Fatalf("restart calls=%d, want %d", calls.Load(), tc.want)
				}
			})
		}
	}
}
func TestUpdateResponseWrittenIgnoresEventsErrorsAndOrdinaryResults(t *testing.T) {
	for _, reply := range []string{
		`{"event":"event","params":{"accepted":true,"id":"upload-1"}}`,
		`{"id":"h1","error":{"message":"bad"},"result":{"accepted":true,"id":"upload-1"}}`,
		`{"id":"h1","result":{"accepted":false,"id":"upload-1"}}`,
		`{"id":"h1","result":{"ok":true}}`,
	} {
		updateResponseWritten(func(string) { t.Fatalf("nonacceptance signaled resident restart: %s", reply) })([]byte(reply + "\n"))
	}
}
func TestStdioNodeRefusesRemoteUpdateWithoutResidentManager(t *testing.T) {
	n := &Node{}
	for _, method := range []string{"node.update.begin", "node.update.chunk", "node.update.commit", "node.update.abort", "node.update.confirm"} {
		_, err := n.handle(context.Background(), method, json.RawMessage(`{}`))
		if err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("%s: %v", method, err)
		}
	}
}

type updateFailWriter struct{}

func (updateFailWriter) Write([]byte) (int, error) { return 0, errors.New("disconnected") }

type updateShortWriter struct{}

func (updateShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

// A terminal Agent at work, with background tasks and subagents, does not hold an update back: the restart leaves it
// running. A busy headless conversation or a launch still starting does.
func TestUpdateWaitsOnlyForWhatTheRestartCuts(t *testing.T) {
	terminal := proto.Session{SessionID: "terminal", Status: proto.StatusWorking, State: proto.StateWorking, Host: proto.EngineHerdr,
		Background: []proto.BackgroundTask{{ID: "workflow", Kind: "workflow"}}, Subagents: []proto.Subagent{{Running: true}}}
	if updateWouldCut(false, []proto.Session{terminal, {SessionID: "outside", Status: proto.StatusBlocked}}) {
		t.Fatal("terminal Agents held the update back")
	}
	if !updateWouldCut(true, []proto.Session{terminal}) {
		t.Fatal("a busy headless conversation did not hold the update back")
	}
	if !updateWouldCut(false, []proto.Session{{SessionID: "new", Starting: true}}) {
		t.Fatal("a launch still starting did not hold the update back")
	}
}

// The host can list an agent before its asynchronous start finishes. The start record still holds the update back.
func TestUpdateWouldCutIncludesHostVisibleStarts(t *testing.T) {
	visible := []proto.Session{{SessionID: "new", Status: proto.StatusWorking, State: proto.StateWorking}}
	if !updateWouldCut(false, visible, []proto.Session{{SessionID: "new", Starting: true}}) {
		t.Fatal("a host-visible unfinished start did not hold the update back")
	}
	if updateWouldCut(false, visible, []proto.Session{{SessionID: "new"}}) {
		t.Fatal("a finished start held the update back")
	}
}

// A host-visible unfinished start must report the same busy state commit refuses (#7377).
func TestSessionsListReportsHostVisibleStartBusy(t *testing.T) {
	n := assistantNode(t)
	const id = "new"
	n.agents[id] = host.Agent{Agent: "claude", AgentStatus: proto.StatusWorking}
	if err := n.starts.add(id, &startRec{done: make(chan struct{}), session: proto.Session{SessionID: id, Starting: true}}); err != nil {
		t.Fatal(err)
	}
	out, err := request(t, n, "sessions.list", map[string]any{})
	if err != nil || out["update_busy"] != true {
		t.Fatalf("a host-visible unfinished start reported update_busy %v, want true (sessions %v, err %v)", out["update_busy"], out, err)
	}
	n.starts.finish(id, nil)
	out, _ = request(t, n, "sessions.list", map[string]any{})
	if out["update_busy"] != false {
		t.Fatalf("a finished start still reports update_busy: %v", out["update_busy"])
	}
}

// sessions.list carries the commit check as update_busy: the manager waits on it, since a headless conversation's row
// does not say it is busy (#6364).
func TestSessionsListReportsUpdateBusy(t *testing.T) {
	n := assistantNode(t)
	p := &assistantProc{id: "chat", busy: true, exited: make(chan struct{})}
	liveAssistants.Store(p, struct{}{})
	defer liveAssistants.Delete(p)
	out, err := request(t, n, "sessions.list", map[string]any{})
	if err != nil || out["update_busy"] != true {
		t.Fatalf("an answering conversation did not report update_busy: %v %v", out["update_busy"], err)
	}
	p.mu.Lock()
	p.busy = false
	p.mu.Unlock()
	if out, _ := request(t, n, "sessions.list", map[string]any{}); out["update_busy"] != false {
		t.Fatalf("an idle conversation reported update_busy: %v", out["update_busy"])
	}
}

// servePeer serves p on a pipe the test feeds lines into (send); framed, it first asks p for lanes as a manager does
// (rpc.Peer.EnableLanes) and waits until p writes frames. end closes the input and waits for Serve.
func servePeer(t *testing.T, p *rpc.Peer, framed bool) (send func(line string), end func()) {
	t.Helper()
	r, w := io.Pipe()
	done := make(chan struct{})
	go func() { _ = p.Serve(context.Background(), r); close(done) }()
	send = func(line string) {
		if _, err := w.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if framed {
		p.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
		send(`{"id":"h0","method":"rpc.lanes","params":{"versions":[1]}}`)
		waitFor(t, "the output framed", p.Framed)
	}
	return send, func() { w.Close(); <-done }
}
