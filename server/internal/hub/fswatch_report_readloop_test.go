package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// heldFSNode answers fs.watch like a node, but keeps the first answer back until the test releases it.
type heldFSNode struct {
	mu        sync.Mutex
	calls     []fsWatchParam
	entered   chan fsWatchParam
	held      chan struct{}
	once      sync.Once
	failFirst bool
}

func newHeldFSNode() *heldFSNode {
	return &heldFSNode{entered: make(chan fsWatchParam, 16), held: make(chan struct{})}
}

func (n *heldFSNode) handle(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method != "fs.watch" {
		return nil, fmt.Errorf("unexpected node method %s", method)
	}
	var p fsWatchParam
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	n.mu.Lock()
	call := len(n.calls)
	n.calls = append(n.calls, p)
	n.mu.Unlock()
	n.entered <- p
	if call == 0 {
		select {
		case <-n.held:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if call == 0 && n.failFirst {
		return nil, errors.New("test fs watch failure")
	}
	return map[string]any{"ok": true}, nil
}

func (n *heldFSNode) release() { n.once.Do(func() { close(n.held) }) }

func (n *heldFSNode) next(t *testing.T) fsWatchParam {
	t.Helper()
	select {
	case p := <-n.entered:
		return p
	case <-time.After(readLoopLimit):
		t.Fatal("the node did not get fs.watch")
		return fsWatchParam{}
	}
}

func (n *heldFSNode) snapshot() []fsWatchParam {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]fsWatchParam(nil), n.calls...)
}

type fsReadLoopFixture struct {
	hub     *Hub
	machine *Machine
	app     *app
	node    *heldFSNode
}

func newFSReadLoopFixture(t *testing.T) *fsReadLoopFixture {
	t.Helper()
	h := newTestHub(t)
	n := newHeldFSNode()
	m := boardNode(t, h, "m1", rpc.Handler(n.handle))
	srv := httptest.NewTLSServer(h.routes())
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	mh := &testHub{h: h, srv: srv}
	a := dialApp(t, mh, h.cfg.Token)
	f := &fsReadLoopFixture{hub: h, machine: m, app: a, node: n}
	// This cleanup runs before the socket, server and pipe cleanups. In particular, a failed assertion must release
	// the node before closing the app and joining the workers, or a correct worker would be held by the fixture itself.
	t.Cleanup(func() {
		n.release()
		_ = a.conn.CloseNow()
		waitHubHandlers(t, h)
		waitHubWorkers(t, h)
	})
	return f
}

func (f *fsReadLoopFixture) watch(t *testing.T, paths ...string) {
	t.Helper()
	f.app.send(t, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/work", "paths": paths})
}

func waitHubHandlers(t *testing.T, h *Hub) {
	t.Helper()
	done := make(chan struct{})
	go func() { h.wsHandlers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(readLoopLimit):
		t.Fatal("hub socket handlers did not finish")
	}
}

func waitHubWorkers(t *testing.T, h *Hub) {
	t.Helper()
	done := make(chan struct{})
	go func() { h.nodeWorkers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(readLoopLimit):
		t.Error("hub workers did not finish")
	}
}

func onlyPath(p fsWatchParam) string {
	if len(p.Dirs) != 1 || len(p.Git) != 0 {
		return fmt.Sprintf("%+v", p)
	}
	return p.Dirs[0].Path
}

// A slow node must not stop the app's socket being read. While the first call is held, later sets coalesce into one
// trailing call with the latest union; a separate goroutine per message would neither serialize nor coalesce them.
func TestFSWatchHeldNodeLeavesReadLoopAndCoalesces(t *testing.T) {
	f := newFSReadLoopFixture(t)
	defer f.node.release()
	f.watch(t, "/a")
	if got := onlyPath(f.node.next(t)); got != "/a" {
		t.Fatalf("first fs.watch = %s, want /a", got)
	}
	f.watch(t, "/b")
	f.watch(t, "/c")
	f.app.pongWithin(t, "fs-read", "behind two fs_watch messages while fs.watch is held")

	select {
	case p := <-f.node.entered:
		t.Fatalf("a trailing fs.watch started before the first answered: %+v", p)
	case <-time.After(100 * time.Millisecond):
	}
	f.node.release()
	if got := onlyPath(f.node.next(t)); got != "/c" {
		t.Fatalf("trailing fs.watch = %s, want the latest set /c", got)
	}
	waitHubWorkers(t, f.hub)
	if calls := f.node.snapshot(); len(calls) != 2 {
		t.Fatalf("the node got %d fs.watch calls, want the held one and one coalesced call: %+v", len(calls), calls)
	}
}

// Disconnect cleanup uses the same ordered queue as an explicit set. An empty trailing set after the held open must
// therefore release the node's folders rather than racing it or being lost.
func TestFSWatchCloseWhileHeldReleases(t *testing.T) {
	f := newFSReadLoopFixture(t)
	defer f.node.release()
	f.watch(t, "/a")
	f.node.next(t)
	_ = f.app.conn.CloseNow()
	eventually(t, readLoopLimit, "the closing app's fs watches forgotten", func() bool {
		return len(f.hub.fsWatchClients()) == 0
	})
	f.node.release()
	p := f.node.next(t)
	if len(p.Dirs) != 0 || len(p.Git) != 0 {
		t.Fatalf("after the app closed the node kept %+v", p)
	}
	waitHubWorkers(t, f.hub)
	if calls := f.node.snapshot(); len(calls) != 2 {
		t.Fatalf("the node got %d fs.watch calls after close, want open then release: %+v", len(calls), calls)
	}
}

// Every reconnect resend is forced, even when an earlier empty resend completed while this one waited: fswSent alone
// cannot represent that requirement because a successful empty call clears it again.
func TestFSWatchReconnectsCoalesceWithoutLosingTheForcedEmptySet(t *testing.T) {
	f := newFSReadLoopFixture(t)
	defer f.node.release()
	f.hub.fsWatchReconnected(f.machine)
	if p := f.node.next(t); len(p.Dirs) != 0 || len(p.Git) != 0 {
		t.Fatalf("first reconnect sent %+v, want an empty set", p)
	}
	f.hub.fsWatchReconnected(f.machine)
	f.node.release()
	if p := f.node.next(t); len(p.Dirs) != 0 || len(p.Git) != 0 {
		t.Fatalf("second reconnect sent %+v, want an empty set", p)
	}
	waitHubWorkers(t, f.hub)
	if calls := f.node.snapshot(); len(calls) != 2 {
		t.Fatalf("two reconnects produced %d calls, want the held one and the trailing forced one", len(calls))
	}
}

// Before the queue, a failed reconnect left fswSent set, so the next empty set tried the release again. Keep that
// recovery: consuming a one-shot force flag must not turn a failed resend into a watch the node keeps forever.
func TestFSWatchReconnectFailureIsRetriedByTheNextEmptySet(t *testing.T) {
	f := newFSReadLoopFixture(t)
	defer f.node.release()
	f.node.failFirst = true
	f.hub.fsWatchReconnected(f.machine)
	f.node.next(t)
	f.node.release()
	waitHubWorkers(t, f.hub)

	f.hub.queueFSWatchSync(f.machine)
	if p := f.node.next(t); len(p.Dirs) != 0 || len(p.Git) != 0 {
		t.Fatalf("the retry sent %+v, want an empty set", p)
	}
	waitHubWorkers(t, f.hub)
	if calls := f.node.snapshot(); len(calls) != 2 {
		t.Fatalf("a failed reconnect and an empty set made %d calls, want the failure and one retry", len(calls))
	}
}

type reportWrite struct {
	report   extwire.Report
	deadline bool
}

// reportStore implements only the setting write this test exercises; the embedded Store keeps the fixture small.
type reportStore struct {
	Store
	mu        sync.Mutex
	writes    []reportWrite
	saved     *extwire.Report
	entered   chan struct{}
	held      chan struct{}
	once      sync.Once
	holdFirst bool
	failFirst bool
}

func newReportStore(holdFirst bool) *reportStore {
	return &reportStore{entered: make(chan struct{}, 32), held: make(chan struct{}), holdFirst: holdFirst}
}

func (s *reportStore) PutSetting(ctx context.Context, key string, value any) error {
	if key != extReportKey {
		return nil
	}
	r, ok := value.(extwire.Report)
	if !ok {
		return fmt.Errorf("report setting has type %T", value)
	}
	_, bounded := ctx.Deadline()
	s.mu.Lock()
	call := len(s.writes)
	s.writes = append(s.writes, reportWrite{report: r, deadline: bounded})
	s.mu.Unlock()
	s.entered <- struct{}{}
	if call == 0 && s.holdFirst {
		select {
		case <-s.held:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if call == 0 && s.failFirst {
		return errors.New("test report write failure")
	}
	s.mu.Lock()
	copy := r
	s.saved = &copy
	s.mu.Unlock()
	return nil
}

func (s *reportStore) release() { s.once.Do(func() { close(s.held) }) }

func (s *reportStore) next(t *testing.T) {
	t.Helper()
	select {
	case <-s.entered:
	case <-time.After(readLoopLimit):
		t.Fatal("the report write did not start")
	}
}

func (s *reportStore) snapshot() ([]reportWrite, *extwire.Report) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writes := append([]reportWrite(nil), s.writes...)
	if s.saved == nil {
		return writes, nil
	}
	copy := *s.saved
	return writes, &copy
}

func extensionReports(h *Hub, reports ...extwire.Report) <-chan struct{} {
	c := &busConn{ident: db.BusIdentity{ID: assistantwire.AssistantID}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, r := range reports {
			raw, _ := json.Marshal(r)
			busEvents[extwire.EventReport](h, c, raw)
		}
	}()
	return done
}

func waitExtensionReports(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(readLoopLimit):
		t.Error("the manager's later bus events were not handled")
	}
}

func features(name string) extwire.Report { return extwire.Report{Features: []string{name}} }

// Report memory and broadcasts stay in bus order; only persistence moves out. A burst while the first write is held
// produces one trailing write of the last report, every write has a deadline, and the bus keeps handling events.
func TestExtensionReportsHeldStoreLeavesBusReadAndCoalesces(t *testing.T) {
	h := newTestHub(t)
	s := newReportStore(true)
	h.store = s
	t.Cleanup(func() { waitHubWorkers(t, h) })
	first := extensionReports(h, features("r1"))
	defer func() {
		s.release()
		waitExtensionReports(t, first)
	}()
	s.next(t)
	waitExtensionReports(t, first)

	rest := extensionReports(h, features("r2"), features("r3"), features("r4"), features("r5"))
	defer func() {
		s.release()
		waitExtensionReports(t, rest)
	}()
	waitExtensionReports(t, rest)
	if got := h.extReport().Features; !slices.Equal(got, []string{"r5"}) {
		t.Fatalf("the in-memory report is %v while the first write is held, want the latest report", got)
	}
	s.release()
	waitHubWorkers(t, h)
	writes, saved := s.snapshot()
	if len(writes) != 2 {
		t.Fatalf("five reports caused %d writes, want exactly the held r1 write and one trailing r5 write: %+v", len(writes), writes)
	}
	for i, want := range []string{"r1", "r5"} {
		if got := writes[i].report.Features; !slices.Equal(got, []string{want}) {
			t.Errorf("report write %d = %v, want %s", i+1, got, want)
		}
	}
	if saved == nil || !slices.Equal(saved.Features, []string{"r5"}) {
		t.Fatalf("stored report = %+v, want r5", saved)
	}
	for i, w := range writes {
		if !w.deadline {
			t.Errorf("report write %d has no context deadline", i+1)
		}
	}
}

// A failed write is not retried in a loop and does not leave the queue stuck: the next report makes the next attempt.
func TestExtensionReportWriteFailureDoesNotStopTheNextReport(t *testing.T) {
	h := newTestHub(t)
	s := newReportStore(false)
	s.failFirst = true
	h.store = s
	t.Cleanup(func() { waitHubWorkers(t, h) })
	first := extensionReports(h, features("first"))
	waitExtensionReports(t, first)
	s.next(t)
	waitHubWorkers(t, h)
	writes, saved := s.snapshot()
	if len(writes) != 1 || saved != nil {
		t.Fatalf("after the failed write: writes=%d saved=%+v, want one attempt and no saved report", len(writes), saved)
	}
	second := extensionReports(h, features("second"))
	defer waitExtensionReports(t, second)
	waitExtensionReports(t, second)
	s.next(t)
	waitHubWorkers(t, h)
	writes, saved = s.snapshot()
	if len(writes) != 2 || saved == nil || !slices.Equal(saved.Features, []string{"second"}) {
		t.Fatalf("after the next report: writes=%d saved=%+v, want a successful second attempt", len(writes), saved)
	}
}

// The hub's existing node-worker shutdown step joins both kinds of asynchronous work. Keep both operations held while
// that wait starts, then release them and require the last values to have completed before shutdown can finish.
func TestHubShutdownJoinsFSWatchAndTheLastReportSave(t *testing.T) {
	h := newTestHub(t)
	s := newReportStore(true)
	h.store = s
	n := newHeldFSNode()
	boardNode(t, h, "m1", rpc.Handler(n.handle))
	c := fsClient(h, roleFull)
	defer func() {
		h.dropClient(c)
		waitHubWorkers(t, h)
	}()
	defer n.release()
	defer s.release()
	say(h, c, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/work", "paths": []string{"/a"}})
	n.next(t)
	reported := extensionReports(h, features("last"))
	waitExtensionReports(t, reported)
	s.next(t)

	budget := newShutdownBudget(h.cfg)
	defer budget.close()
	stopped := make(chan struct{})
	go func() {
		budget.step(h, "node connections", func(context.Context) error {
			h.nodeWorkers.Wait()
			return nil
		})
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("shutdown finished while the node call and report write were held")
	case <-time.After(100 * time.Millisecond):
	}
	s.release()
	n.release()
	select {
	case <-stopped:
	case <-time.After(readLoopLimit):
		t.Fatal("shutdown did not finish after the held workers were released")
	}
	_, saved := s.snapshot()
	if saved == nil || !slices.Equal(saved.Features, []string{"last"}) {
		t.Fatalf("shutdown finished without the last report saved: %+v", saved)
	}
}
