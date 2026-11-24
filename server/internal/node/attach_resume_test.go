package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// residentAttach is a resident node (Attach) and a manager's peer framed with it over a first transport, resuming
// on: what these tests take back, refuse or load.
type residentAttach struct {
	t     *testing.T
	home  string
	ctx   context.Context
	extra chan net.Conn
	made  atomic.Int32
	done  chan error
	hub   *rpc.Peer
	first net.Conn // the manager's end of the first transport
	lost  chan error
	stop  context.CancelFunc
	hooks hookEvents // the hook.event notifications the manager got
}

func startResidentAttach(t *testing.T) *residentAttach {
	home := shortHome(t)
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := &residentAttach{t: t, home: home, ctx: ctx, extra: make(chan net.Conn), done: make(chan error, 1),
		lost: make(chan error, 64), stop: cancel}
	go func() {
		r.done <- Attach(ctx, func() (*Node, error) {
			r.made.Add(1)
			n, err := New("test")
			if err == nil {
				n.logger.SetOutput(io.Discard)
				t.Cleanup(n.starts.close)
			}
			return n, err
		}, r.extra, nil)
	}()
	m1, n1 := net.Pipe()
	r.extra <- n1
	r.first = m1
	hub := rpc.New("h", m1)
	hub.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
	hub.EnableResume(5 * time.Second)
	hub.OnLost = func(err error) { r.lost <- err }
	hub.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return map[string]any{}, nil }
	hub.OnEvent = r.hooks.record
	go func() { _ = hub.Serve(ctx, slowReader{m1}) }()
	r.hub = hub
	cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
	defer ccancel()
	var info proto.NodeInfo
	if err := hub.Call(cctx, "node.info", map[string]any{}, &info); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(info.Capabilities, nodeproto.NodeResume) {
		t.Fatalf("a resident node does not announce %s", nodeproto.NodeResume)
	}
	if err := hub.UpgradeLanes(cctx); err != nil {
		t.Fatal(err)
	}
	if !hub.Resumable() {
		t.Fatal("not resumable after rpc.lanes with a resident node")
	}
	return r
}

// end stops Attach and waits for it.
func (r *residentAttach) end() {
	r.t.Helper()
	r.stop()
	select {
	case <-r.done:
	case <-time.After(15 * time.Second):
		r.t.Fatal("Attach did not end")
	}
}

// load keeps the node writing: listings of a large folder (answers no credit holds back) asked over and over by a
// few callers until stop is called, which says the first error any of them had. more waits for another answer.
func (r *residentAttach) load(ctx context.Context) (stop func() error, more func()) {
	r.t.Helper()
	dir := filepath.Join(r.home, "load")
	if err := os.Mkdir(dir, 0o700); err != nil {
		r.t.Fatal(err)
	}
	for i := range 400 {
		name := fmt.Sprintf("%03d-%s.txt", i, strings.Repeat("persona", 20))
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	lctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	var answers atomic.Int64
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for lctx.Err() == nil {
				var list json.RawMessage
				err := r.hub.Call(lctx, "fs.list", map[string]any{"path": dir}, &list)
				if lctx.Err() != nil {
					return
				}
				if err != nil || len(list) < 400*100 {
					errs <- fmt.Errorf("listing: %d bytes, %v", len(list), err)
					return
				}
				answers.Add(1)
			}
		}()
	}
	stop = func() error {
		cancel()
		wg.Wait()
		close(errs)
		if answers.Load() == 0 {
			return errors.New("no answer came")
		}
		return <-errs
	}
	more = func() {
		r.t.Helper()
		was := answers.Load()
		for deadline := time.Now().Add(20 * time.Second); answers.Load() == was; {
			if time.Now().After(deadline) {
				r.t.Fatal("no answer came")
			}
			time.Sleep(time.Millisecond) // sleep-ok: polls a counter the callers bump
		}
	}
	return stop, more
}

// slowReader is the manager's end of a slow link: what the node writes waits on it.
type slowReader struct{ r io.Reader }

func (s slowReader) Read(b []byte) (int, error) {
	time.Sleep(time.Millisecond) // sleep-ok: at most 16 KiB a millisecond
	return s.r.Read(b[:min(len(b), 16<<10)])
}

// A resident node keeps its connection when the transport under it ends: the manager comes back on a new one with its
// token and takes it back, the same Node serving it (#10663). A token the node does not have is refused on the spot,
// and the connection served is left as it was.
func TestAttachKeepsConnectionAfterLargeRequest(t *testing.T) {
	r := startResidentAttach(t)
	defer r.end()
	ctx, cancel := context.WithTimeout(r.ctx, 20*time.Second)
	defer cancel()
	const head, tail = `{"padding":"`, `"}`
	const size = 70 << 20
	params := rpc.Appended{
		Size: len(head) + size + len(tail),
		Append: func(dst []byte) []byte {
			dst = append(dst, head...)
			for range size {
				dst = append(dst, 'x')
			}
			return append(dst, tail...)
		},
	}
	if err := r.hub.Call(ctx, "ping", params, nil); err != nil {
		t.Fatalf("70 MiB request: %v", err)
	}
	if err := r.hub.Call(ctx, "ping", nil, nil); err != nil {
		t.Fatalf("ping after large request: %v", err)
	}
	if got := r.made.Load(); got != 1 {
		t.Fatalf("created %d nodes on the transport, want 1", got)
	}
}

func TestAttachResumesAConnection(t *testing.T) {
	r := startResidentAttach(t)
	hub := r.hub
	cctx, ccancel := context.WithTimeout(r.ctx, 20*time.Second)
	defer ccancel()
	r.first.Close() // the transport goes: a relay ended, a link dropped
	select {
	case <-r.lost:
	case <-time.After(5 * time.Second):
		t.Fatal("the manager's peer was not told its transport was lost")
	}
	m2, n2 := net.Pipe()
	r.extra <- n2
	if err := hub.Resume(cctx, m2, m2); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := hub.Call(cctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatalf("after resuming: %v", err)
	}
	if r.made.Load() != 1 {
		t.Fatalf("%d Nodes made: a resume made a new one", r.made.Load())
	}

	// A token this node does not have: refused at once, the connection served left alone.
	if line := refusedResume(t, r); !strings.Contains(line, rpc.CodeResumeRefused) {
		t.Fatalf("a made-up token: %q", line)
	}
	if err := hub.Call(cctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatalf("after a refused resume: %v", err)
	}
	if r.made.Load() != 1 {
		t.Fatalf("%d Nodes made: a refused resume made one", r.made.Load())
	}
	r.end()
}

// refusedResume asks the resident node to resume with a token it does not have, on a new transport, and says the line
// it answered.
func refusedResume(t *testing.T, r *residentAttach) string {
	t.Helper()
	m, n := net.Pipe()
	defer m.Close()
	r.extra <- n
	go func() {
		_, _ = m.Write([]byte(`{"id":"x1","method":"rpc.resume","params":{"token":"not-this-one","read":0}}` + "\n"))
	}()
	_ = m.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(m).ReadString('\n')
	if err != nil {
		t.Fatalf("a made-up token: %q, %v", line, err)
	}
	return line
}

// slowResumeDecisions holds every resume a moment before it is decided, for frames to be written meanwhile: one that
// went to the new transport would show.
func slowResumeDecisions(t *testing.T) {
	wait := func() { time.Sleep(50 * time.Millisecond) } // sleep-ok: a window for the writer to write in
	resumeDeciding.Store(&wait)
	t.Cleanup(func() { resumeDeciding.Store(nil) })
}

// Resumes refused while the connection is busy writing take none of its frames: a frame on the refused transport would
// be a gap on the one in use, which ends it and counts against framing (#10829's review). The manager never sees it
// lost.
func TestAttachRefusedResumeTakesNoFrames(t *testing.T) {
	slowResumeDecisions(t)
	r := startResidentAttach(t)
	stop, more := r.load(r.ctx)
	for range 40 {
		more()
		if line := refusedResume(t, r); !strings.HasPrefix(line, "{") || !strings.Contains(line, rpc.CodeResumeRefused) {
			t.Fatalf("a made-up token answered %q", line)
		}
	}
	if err := stop(); err != nil {
		t.Fatalf("the connection in use: %v", err)
	}
	select {
	case err := <-r.lost:
		t.Fatalf("the manager lost the connection in use: %v", err)
	default:
	}
	if r.made.Load() != 1 {
		t.Fatalf("%d Nodes made", r.made.Load())
	}
	r.end()
}

// The manager takes its connection back while the old transport still carries frames (its pings went unanswered, the
// node saw nothing wrong): the answer is the first line of the new transport, every time, and the calls under way go
// on (#10829's review).
func TestAttachResumesWhileTheOldTransportStillWorks(t *testing.T) {
	slowResumeDecisions(t)
	r := startResidentAttach(t)
	stop, more := r.load(r.ctx)
	cctx, ccancel := context.WithTimeout(r.ctx, 60*time.Second)
	defer ccancel()
	old := r.first
	for i := range 10 {
		more()
		if !r.hub.Lose(errors.New("two pings unanswered")) {
			t.Fatalf("resume %d: the connection cannot be lost", i)
		}
		m, n := net.Pipe()
		r.extra <- n
		if err := r.hub.Resume(cctx, slowReader{m}, m); err != nil {
			t.Fatalf("resume %d: %v", i, err)
		}
		old.Close() // the manager lets the old transport go (devices resumeConn)
		old = m
	}
	if err := stop(); err != nil {
		t.Fatalf("the calls under way: %v", err)
	}
	if r.made.Load() != 1 {
		t.Fatalf("%d Nodes made", r.made.Load())
	}
	r.end()
}

// A transport that ends before it said a whole first line (a manager's attempt to resume cut on its way) is let go:
// it does not replace the connection kept (#10829's review).
func TestAttachIgnoresATransportThatSaidNothing(t *testing.T) {
	home := shortHome(t)
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var made atomic.Int32
	extra := make(chan net.Conn)
	done := make(chan error, 1)
	go func() {
		done <- Attach(ctx, func() (*Node, error) {
			made.Add(1)
			n, err := New("test")
			if err == nil {
				n.logger.SetOutput(io.Discard)
				t.Cleanup(n.starts.close)
			}
			return n, err
		}, extra, nil)
	}()
	m1, n1 := net.Pipe()
	extra <- n1
	hub := rpc.New("h", m1)
	hub.EnableLanes(nodeproto.ControlLane, nodeproto.BulkLane)
	hub.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return map[string]any{}, nil }
	go func() { _ = hub.Serve(ctx, m1) }()
	cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
	defer ccancel()
	if err := hub.Call(cctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	for _, partial := range []string{"", `{"id":"h7","method":"rpc.res`} {
		m2, n2 := net.Pipe()
		extra <- n2
		go func() {
			_, _ = m2.Write([]byte(partial))
			m2.Close()
		}()
	}
	if err := hub.Call(cctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatalf("the connection served after two that said nothing: %v", err)
	}
	if made.Load() != 1 {
		t.Fatalf("%d Nodes made", made.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Attach did not end")
	}
}

// sendHook sends a report-only hook's event on node.sock, as `personad hook` does, and says how long its answer took.
func sendHook(t *testing.T, event, session string) time.Duration {
	t.Helper()
	start := time.Now()
	c, err := net.Dial("unix", nodeclient.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b, _ := json.Marshal(sockRequest{Kind: "hook", ReportOnly: true, Input: reportInput(event, session, "")})
	if _, err := c.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Minute))
	if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
		t.Fatalf("the hook's answer: %v", err)
	}
	return time.Since(start)
}

// hookSession is the session of the i-th hook these tests send.
func hookSession(i int) string { return fmt.Sprintf("%08d-2222-3333-4444-555555555555", i) }

// sessionsOf says the sessions of hook events, in the order they came.
func sessionsOf(events []map[string]any) []string {
	var got []string
	for _, e := range events {
		got = append(got, fmt.Sprint(e["session_id"]))
	}
	return got
}

// While the connection waits for the manager to resume it, a hook does not wait with it: its event goes out, in order,
// once the connection is resumed. One whose connection is never resumed is kept, and the next connection gets it
// (#10829's review).
func TestAttachHookEventsDoNotWaitForAResume(t *testing.T) {
	was := resumeWindow
	resumeWindow = 3 * time.Second
	t.Cleanup(func() { resumeWindow = was })
	r := startResidentAttach(t)
	cctx, ccancel := context.WithTimeout(r.ctx, time.Minute)
	defer ccancel()
	if took := sendHook(t, "Stop", hookSession(0)); took > hookEventWait {
		t.Fatalf("a hook on a working connection took %v", took)
	}
	waitFor(t, "the first event", func() bool { return len(r.hooks.all()) == 1 })

	r.first.Close()
	<-r.lost
	for i := 1; i <= 3; i++ {
		if took := sendHook(t, "Stop", hookSession(i)); took > hookEventWait+time.Second/2 {
			t.Fatalf("hook %d waited %v for the transport", i, took)
		}
	}
	m2, n2 := net.Pipe()
	r.extra <- n2
	if err := r.hub.Resume(cctx, m2, m2); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitFor(t, "the events after the resume", func() bool { return len(r.hooks.all()) == 4 })
	if got, want := sessionsOf(r.hooks.all()), []string{hookSession(0), hookSession(1), hookSession(2), hookSession(3)}; !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}

	// Lost again, and not resumed: what the hooks sent meanwhile goes to the next connection.
	m2.Close()
	<-r.lost
	for i := 4; i <= 5; i++ {
		if took := sendHook(t, "Stop", hookSession(i)); took > hookEventWait+time.Second/2 {
			t.Fatalf("hook %d waited %v for the transport", i, took)
		}
	}
	// The events are kept only once the window runs out: waitFor's time counts from there, not from the loss.
	waitWithin(t, resumeWindow+5*time.Second, "the kept events", func() bool { return len(openSpool(SpoolPath()).load()) == 2 })
	m3, n3 := net.Pipe()
	r.extra <- n3
	next := rpc.New("h2", m3)
	var replayed hookEvents
	next.OnEvent = replayed.record
	next.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return map[string]any{}, nil }
	go func() { _ = next.Serve(r.ctx, m3) }()
	if err := next.Call(cctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the kept events on the next connection", func() bool { return len(replayed.all()) == 2 })
	got := replayed.all()
	if !slices.Equal(sessionsOf(got), []string{hookSession(4), hookSession(5)}) || got[0]["spool_id"] == nil {
		t.Fatalf("replayed %v", got)
	}
	if r.made.Load() != 2 {
		t.Fatalf("%d Nodes made", r.made.Load())
	}
	r.end()
}

// Connections replace each other in the order they came, whatever order they speak in: one that came before the
// connection served and speaks only now (an attempt the manager gave up on) is let go, not served in its place
// (#10829's review).
func TestAttachKeepsTheOrderConnectionsCameIn(t *testing.T) {
	home := shortHome(t)
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var made atomic.Int32
	extra := make(chan net.Conn)
	done := make(chan error, 1)
	go func() {
		done <- Attach(ctx, func() (*Node, error) {
			made.Add(1)
			n, err := New("test")
			if err == nil {
				n.logger.SetOutput(io.Discard)
				t.Cleanup(n.starts.close)
			}
			return n, err
		}, extra, nil)
	}()
	manager := func(name string, c net.Conn) *rpc.Peer {
		p := rpc.New(name, c)
		p.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return map[string]any{}, nil }
		go func() { _ = p.Serve(ctx, c) }()
		return p
	}
	m1, n1 := net.Pipe()
	extra <- n1 // came first, says nothing yet
	m2, n2 := net.Pipe()
	extra <- n2
	newer := manager("h2", m2)
	cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
	defer ccancel()
	if err := newer.Call(cctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	older := manager("h1", m1)
	octx, ocancel := context.WithTimeout(ctx, 3*time.Second)
	defer ocancel()
	if err := older.Call(octx, "ping", map[string]any{}, nil); err == nil {
		t.Fatal("the connection that came first was served after the newer one")
	}
	if err := newer.Call(cctx, "ping", map[string]any{}, nil); err != nil {
		t.Fatalf("the newer connection: %v", err)
	}
	if made.Load() != 1 {
		t.Fatalf("%d Nodes made", made.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Attach did not end")
	}
}
