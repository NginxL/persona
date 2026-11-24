package devices

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/netshape"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestNodeUpdateChunkSizer(t *testing.T) {
	t.Run("default ping timeout starts conservatively and ramps smoothly", func(t *testing.T) {
		s := newNodeUpdateChunkSizer(link.PingTimeout)
		if got := s.next(); got != nodeUpdateInitChunk {
			t.Fatalf("initial chunk = %d, want %d", got, nodeUpdateInitChunk)
		}
		// Fast link: grows at most 2x per chunk up to nodeUpdateMaxChunk.
		s.observe(s.next(), 2*time.Millisecond, rpc.LaneStats{Framed: true, RTT: 2 * time.Millisecond})
		if got := s.next(); got != 2*nodeUpdateInitChunk {
			t.Fatalf("after fast chunk 1: %d, want %d", got, 2*nodeUpdateInitChunk)
		}
		s.observe(s.next(), 2*time.Millisecond, rpc.LaneStats{Framed: true, RTT: 2 * time.Millisecond})
		if got := s.next(); got != nodeUpdateMaxChunk {
			t.Fatalf("after fast chunk 2: %d, want %d", got, nodeUpdateMaxChunk)
		}
		// Slow link: shrinks immediately to fit within target (~1.25 s).
		s.observe(s.next(), 4*time.Second, rpc.LaneStats{Framed: true, RTT: 500 * time.Millisecond})
		if got := s.next(); got > 96<<10 || got < 64<<10 {
			t.Fatalf("after 4s chunk: %d, want ~80 KiB", got)
		}
		// Very slow link: clamps to nodeUpdateMinChunk.
		s.observe(s.next(), 30*time.Second, rpc.LaneStats{Framed: true, RTT: 500 * time.Millisecond})
		if got := s.next(); got != nodeUpdateMinChunk {
			t.Fatalf("after 30s chunk: %d, want %d", got, nodeUpdateMinChunk)
		}
	})

	t.Run("high lane RTT prevents growth and shrinks when severe", func(t *testing.T) {
		s := newNodeUpdateChunkSizer(link.PingTimeout) // target = 1.25s
		s.cur = 128 << 10
		// Fast elapsed (e.g. compressible chunk), but RTT > 2*target: must not grow.
		s.observe(128<<10, 200*time.Millisecond, rpc.LaneStats{Framed: true, RTT: 3 * time.Second})
		if got := s.next(); got != 128<<10 {
			t.Fatalf("grew under high RTT: %d", got)
		}
		// Severe RTT > 3*target: shrinks by half.
		s.observe(128<<10, 200*time.Millisecond, rpc.LaneStats{Framed: true, RTT: 4 * time.Second})
		if got := s.next(); got != 64<<10 {
			t.Fatalf("did not shrink under severe RTT: %d, want %d", got, 64<<10)
		}
	})

	t.Run("shorter ping timeout scales initial and max chunk down", func(t *testing.T) {
		s := newNodeUpdateChunkSizer(time.Second)
		if got := s.next(); got != nodeUpdateMinChunk {
			t.Fatalf("initial chunk for 1s ping timeout = %d, want %d", got, nodeUpdateMinChunk)
		}
		if s.max >= nodeUpdateMaxChunk {
			t.Fatalf("max chunk for 1s ping timeout = %d, expected scaled below %d", s.max, nodeUpdateMaxChunk)
		}
	})
}

// pipeListener accepts in-process net.Conn pairs fed by DialContext without touching OS TCP ports or socket buffers.
type pipeListener struct {
	ch     chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{ch: make(chan net.Conn, 4), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr("pipe") }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

// TestLinkedDeploySlowLinkKeepsStreamPingAlive reproduces #13761: a node update transferred over a slow, high-RTT
// Persona Link WebSocket stream (~100 KB/s, 260 ms RTT, 256 KiB buffer, permessage-deflate enabled) with a realistic
// compressible-then-incompressible binary payload while the hub's WebSocket ping (and concurrent RPC control pings)
// run throughout the transfer. Both framed (lanes=on) and unframed (lanes=off) connections must finish cleanly without
// starving pings.
func TestLinkedDeploySlowLinkKeepsStreamPingAlive(t *testing.T) {
	// Compressible prefix (as ELF headers/padding compress under permessage-deflate, inflating the initial credit window)
	// followed by incompressible bytes so the full payload equals one legacy 1 MiB UploadChunkBytes chunk.
	const prefixBytes = 512 << 10
	payload := make([]byte, proto.UploadChunkBytes)
	rng := rand.New(rand.NewPCG(13761, 1))
	for i := prefixBytes; i < len(payload); i++ {
		payload[i] = byte(rng.Uint32())
	}

	for _, framed := range []bool{true, false} {
		t.Run(fmt.Sprintf("framed=%t", framed), func(t *testing.T) {
			t.Parallel()
			const (
				pingEvery   = 700 * time.Millisecond
				pingTimeout = 2500 * time.Millisecond
			)
			shape := netshape.Config{Rate: 100_000, Delay: 130 * time.Millisecond, Queue: 256 << 10}

			h := newTestDevices(t)
			h.cfg.HubVersion = "new-build"
			h.cfg.AutoUpdateNodes = true
			h.cfg.LinkPingMS = int(pingEvery.Milliseconds())
			h.cfg.LinkPingTimeoutMS = int(pingTimeout.Milliseconds())
			m := newMachine(h, MachineConfig{ID: "slow-link-node", Name: "Slow Link", Kind: kindLink})
			m.deployRetry = 10 * time.Millisecond
			m.online = true
			m.info = proto.NodeInfo{OS: "linux", Arch: "amd64", Version: "old-build", NodeUpdate: true}
			h.addEntry(m)

			var (
				wsPingOK    atomic.Int64
				wsPingErrMu sync.Mutex
				wsPingErr   error
				maxChunk    atomic.Int64
				chunkCount  atomic.Int64
			)

			// Set up an in-process WebSocket server simulating hub serveLinkStream (permessage-deflate + stream ping).
			ln := newPipeListener()
			mgrReady := make(chan net.Conn, 1)
			pingStop := make(chan struct{})
			var pingWG sync.WaitGroup
			srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
					InsecureSkipVerify: true,
					CompressionMode:    link.StreamCompression,
				})
				if err != nil {
					return
				}
				conn.SetReadLimit(64 << 20)
				sctx, scancel := context.WithCancel(context.Background())
				defer scancel()
				nc := websocket.NetConn(sctx, conn, websocket.MessageBinary)
				pingWG.Add(1)
				go func() {
					defer pingWG.Done()
					tk := time.NewTicker(pingEvery)
					defer tk.Stop()
					for {
						select {
						case <-pingStop:
							return
						case <-sctx.Done():
							return
						case <-tk.C:
							pctx, pcancel := context.WithTimeout(sctx, pingTimeout)
							err := conn.Ping(pctx)
							pcancel()
							if err != nil {
								select {
								case <-pingStop:
									return
								default:
								}
								if sctx.Err() == nil {
									wsPingErrMu.Lock()
									if wsPingErr == nil {
										wsPingErr = err
									}
									wsPingErrMu.Unlock()
									_ = conn.CloseNow()
								}
								return
							}
							wsPingOK.Add(1)
						}
					}
				}()
				mgrReady <- nc
				<-pingStop
				_ = nc.Close()
			})}
			go func() { _ = srv.Serve(ln) }()

			httpClient := &http.Client{Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					clientSide, serverSide := netshape.Pipe(shape, shape)
					select {
					case ln.ch <- serverSide:
						return clientSide, nil
					case <-ctx.Done():
						clientSide.Close()
						serverSide.Close()
						return nil, ctx.Err()
					}
				},
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			nodeWS, _, err := websocket.Dial(ctx, "ws://pipe"+link.StreamPath, &websocket.DialOptions{
				HTTPClient:      httpClient,
				CompressionMode: link.StreamCompression,
			})
			if err != nil {
				t.Fatalf("dial slow link stream: %v", err)
			}
			nodeWS.SetReadLimit(64 << 20)
			nodeConn := websocket.NetConn(ctx, nodeWS, websocket.MessageBinary)
			mgrConn := <-mgrReady

			mgrPeer := rpc.New("h", mgrConn)
			nodePeer := rpc.New("n", nodeConn)
			if framed {
				mgrPeer.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
				nodePeer.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
			}

			n := &updatingNode{machine: m, expected: payload}
			nodePeer.OnRequest = func(rctx context.Context, method string, params json.RawMessage) (any, error) {
				if method == "ping" {
					return map[string]any{}, nil
				}
				if method == "node.update.chunk" {
					var p struct {
						Data []byte `json:"data"`
					}
					if json.Unmarshal(params, &p) == nil {
						chunkCount.Add(1)
						for {
							prev := maxChunk.Load()
							if int64(len(p.Data)) <= prev || maxChunk.CompareAndSwap(prev, int64(len(p.Data))) {
								break
							}
						}
					}
				}
				return n.handle(rctx, method, params)
			}
			m.peer = mgrPeer
			m.peer.OnEvent = m.onEvent

			var served sync.WaitGroup
			served.Add(2)
			go func() { defer served.Done(); _ = mgrPeer.Serve(ctx, mgrConn) }()
			go func() { defer served.Done(); _ = nodePeer.Serve(ctx, nodeConn) }()
			t.Cleanup(func() {
				close(pingStop)
				cancel()
				_ = mgrConn.Close()
				_ = nodeConn.Close()
				_ = srv.Close()
				_ = ln.Close()
				pingWG.Wait()
				served.Wait()
			})

			if framed {
				if err := mgrPeer.UpgradeLanes(ctx); err != nil {
					t.Fatalf("upgrade lanes: %v", err)
				}
			}

			bin := filepath.Join(t.TempDir(), "persona-node-linux-amd64")
			if err := os.WriteFile(bin, payload, 0o755); err != nil {
				t.Fatal(err)
			}

			// Also verify RPC control pings succeed while node.update.chunk is in flight.
			rpcPingDone := make(chan error, 1)
			go func() {
				// Wait until transfer has sent at least one chunk, then send a control ping.
				deadline := time.Now().Add(15 * time.Second)
				for chunkCount.Load() == 0 {
					if time.Now().After(deadline) || ctx.Err() != nil {
						rpcPingDone <- errors.New("timed out waiting for first chunk")
						return
					}
					select {
					case <-ctx.Done():
						rpcPingDone <- ctx.Err()
						return
					case <-time.After(20 * time.Millisecond):
					}
				}
				pctx, pcancel := context.WithTimeout(ctx, pingTimeout)
				defer pcancel()
				rpcPingDone <- mgrPeer.Call(pctx, "ping", map[string]any{}, nil)
			}()

			record := nodeDeployRecord{Target: "new-build", State: "queued"}
			if err := m.performRPCDeployment(ctx, bin, &record); err != nil {
				wsPingErrMu.Lock()
				pe := wsPingErr
				wsPingErrMu.Unlock()
				t.Fatalf("performRPCDeployment failed: %v (wsPingOK=%d wsPingErr=%v stats=%s)", err, wsPingOK.Load(), pe, mgrPeer.LaneStats())
			}

			if err := <-rpcPingDone; err != nil {
				t.Fatalf("concurrent RPC ping during transfer failed: %v", err)
			}
			wsPingErrMu.Lock()
			pe := wsPingErr
			wsPingErrMu.Unlock()
			if pe != nil {
				t.Fatalf("WebSocket stream ping failed during transfer: %v", pe)
			}
			if wsPingOK.Load() < 2 {
				t.Fatalf("only %d WebSocket pings ran during transfer, want >= 2", wsPingOK.Load())
			}
			if got := maxChunk.Load(); got > nodeUpdateMaxChunk {
				t.Fatalf("max chunk size = %d, expected adaptive chunks <= %d", got, nodeUpdateMaxChunk)
			}

			n.mu.Lock()
			defer n.mu.Unlock()
			wantSum := sha256.Sum256(payload)
			gotSum := sha256.Sum256(n.data)
			if n.confirms != 1 || !bytes.Equal(n.data, payload) {
				t.Fatalf("confirms=%d sha256 got=%s want=%s", n.confirms, hex.EncodeToString(gotSum[:]), hex.EncodeToString(wantSum[:]))
			}
			t.Logf("framed=%t chunks=%d maxChunk=%dKiB wsPingsOK=%d stats=%s",
				framed, chunkCount.Load(), maxChunk.Load()>>10, wsPingOK.Load(), mgrPeer.LaneStats())
		})
	}
}
