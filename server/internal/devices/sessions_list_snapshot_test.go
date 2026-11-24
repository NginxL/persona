package devices

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The node computed a Working sessions.list response, then the session finished.
// Its newer notification reaches the manager before that older response.
func TestExplicitSessionsListResponseCannotOverwriteNewerNotification(t *testing.T) {
	d, m := explicitSessionsListMachine(t, func(nodePeer *rpc.Peer) (any, error) {
		if err := nodePeer.Notify("sessions.changed", explicitListWithStatus(proto.StatusDone)); err != nil {
			return nil, err
		}
		return explicitListWithStatus(proto.StatusWorking), nil
	})
	forwarded := recordSessionEventStatuses(d)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := d.Call(ctx, devicewire.NodeCall{MachineID: "computer", Method: "sessions.list"})
	if err != nil {
		t.Fatal(err)
	}
	got := m.sessionList()
	if len(got) != 1 || got[0].Status != proto.StatusDone {
		t.Fatalf("older sessions.list response replaced the newer notification: %+v", got)
	}
	if got := forwarded(); !slices.Equal(got, []string{proto.StatusDone}) {
		t.Fatalf("forwarded session statuses = %v, want only the newer Done notification", got)
	}
}

func TestExplicitSessionsListResponseUpdatesCacheAndForwards(t *testing.T) {
	d, m := explicitSessionsListMachine(t, func(*rpc.Peer) (any, error) {
		return explicitListWithStatus(proto.StatusWorking), nil
	})
	forwarded := recordSessionEventStatuses(d)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := d.Call(ctx, devicewire.NodeCall{MachineID: "computer", Method: "sessions.list"})
	if err != nil {
		t.Fatal(err)
	}
	got := m.sessionList()
	if len(got) != 1 || got[0].Status != proto.StatusWorking || got[0].MachineID != "computer" {
		t.Fatalf("uncontended sessions.list response was not cached: %+v", got)
	}
	if got := forwarded(); !slices.Equal(got, []string{proto.StatusWorking}) {
		t.Fatalf("forwarded session statuses = %v, want Working", got)
	}
}

func TestConcurrentSameStartSessionsListResponsesAreAcceptedInResponseOrder(t *testing.T) {
	workingStarted, doneStarted := make(chan struct{}), make(chan struct{})
	releaseWorking, releaseDone := make(chan struct{}), make(chan struct{})
	var workingOnce, doneOnce sync.Once
	allowWorking := func() { workingOnce.Do(func() { close(releaseWorking) }) }
	allowDone := func() { doneOnce.Do(func() { close(releaseDone) }) }
	t.Cleanup(allowWorking)
	t.Cleanup(allowDone)
	var requestMu sync.Mutex
	requests := 0
	d, m := explicitSessionsListMachine(t, func(*rpc.Peer) (any, error) {
		requestMu.Lock()
		requests++
		request := requests
		requestMu.Unlock()
		if request == 1 {
			close(workingStarted)
			<-releaseWorking
			return explicitListWithStatus(proto.StatusWorking), nil
		}
		close(doneStarted)
		<-releaseDone
		return explicitListWithStatus(proto.StatusDone), nil
	})
	forwarded := recordSessionEventStatuses(d)

	results := make(chan error, 2)
	call := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := d.Call(ctx, devicewire.NodeCall{MachineID: "computer", Method: "sessions.list"})
		results <- err
	}
	go call()
	select {
	case <-workingStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the Working sessions.list request did not start")
	}
	go call()
	select {
	case <-doneStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the concurrent Done sessions.list request did not start")
	}

	allowWorking()
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	allowDone()
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if got := m.sessionList(); len(got) != 1 || got[0].Status != proto.StatusDone {
		t.Errorf("the later same-start response was rejected: sessions = %+v, want Done", got)
	}
	if got := forwarded(); !slices.Equal(got, []string{proto.StatusWorking, proto.StatusDone}) {
		t.Fatalf("forwarded session statuses = %v, want Working then Done", got)
	}
}

func TestSessionsChangedCannotOvertakeSessionsListForward(t *testing.T) {
	d, m := explicitSessionsListMachine(t, func(*rpc.Peer) (any, error) {
		return explicitListWithStatus(proto.StatusWorking), nil
	})
	workingForward, doneForwarded := make(chan struct{}), make(chan struct{})
	releaseWorking := make(chan struct{})
	var releaseOnce sync.Once
	allowWorking := func() { releaseOnce.Do(func() { close(releaseWorking) }) }
	t.Cleanup(allowWorking)
	var mu sync.Mutex
	var statuses []string
	d.opts.Event = func(_ string, event string, params json.RawMessage) bool {
		if event != "sessions.changed" {
			return true
		}
		var p struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if json.Unmarshal(params, &p) != nil || len(p.Sessions) != 1 {
			return true
		}
		status := p.Sessions[0].Status
		if status == proto.StatusWorking {
			close(workingForward)
			<-releaseWorking
		}
		mu.Lock()
		statuses = append(statuses, status)
		mu.Unlock()
		if status == proto.StatusDone {
			close(doneForwarded)
		}
		return true
	}

	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := d.Call(ctx, devicewire.NodeCall{MachineID: "computer", Method: "sessions.list"})
		result <- err
	}()
	select {
	case <-workingForward:
	case <-time.After(5 * time.Second):
		t.Fatal("the Working sessions.list response was not forwarded")
	}
	params, _ := json.Marshal(explicitListWithStatus(proto.StatusDone))
	go m.onEvent("sessions.changed", params)

	overtook := false
	select {
	case <-doneForwarded:
		overtook = true
	case <-time.After(100 * time.Millisecond):
	}
	allowWorking()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	select {
	case <-doneForwarded:
	case <-time.After(5 * time.Second):
		t.Fatal("the Done notification was not forwarded")
	}
	mu.Lock()
	gotStatuses := slices.Clone(statuses)
	mu.Unlock()
	if overtook {
		t.Fatalf("the Done notification overtook the paused Working response: forwarded statuses = %v", gotStatuses)
	}
	if !slices.Equal(gotStatuses, []string{proto.StatusWorking, proto.StatusDone}) {
		t.Fatalf("forwarded session statuses = %v, want Working then Done", gotStatuses)
	}
	if got := m.sessionList(); len(got) != 1 || got[0].Status != proto.StatusDone {
		t.Fatalf("sessions = %+v, want the final Done notification", got)
	}
}

func TestExplicitSessionsListResponseAfterFirstConnectionWaitUpdatesCache(t *testing.T) {
	old := firstWait
	firstWait = 5 * time.Second
	t.Cleanup(func() { firstWait = old })
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "computer", Name: "computer", Kind: "ssh"})
	dialStarted, releaseDial := make(chan struct{}), make(chan struct{})
	var dialOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseDial) }) }
	t.Cleanup(release)
	responses := make(chan any, 2)
	responses <- map[string]any{"sessions": []proto.Session{}}
	responses <- explicitListWithStatus(proto.StatusWorking)
	m.dialNode = func(ctx context.Context) (net.Conn, error) {
		dialOnce.Do(func() { close(dialStarted) })
		select {
		case <-releaseDial:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		managerSide, nodeSide := net.Pipe()
		nodePeer := rpc.New("n", nodeSide)
		nodePeer.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			switch method {
			case "node.info":
				return proto.NodeInfo{Version: "test"}, nil
			case "sessions.list":
				return <-responses, nil
			default:
				return map[string]any{"ok": true}, nil
			}
		}
		go func() { _ = nodePeer.Serve(ctx, nodeSide); _ = nodeSide.Close() }()
		return managerSide, nil
	}
	d.addEntry(m)
	runMachine(t, m)
	select {
	case <-dialStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the first connection did not start")
	}
	forwarded := recordSessionEventStatuses(d)
	timer := time.AfterFunc(200*time.Millisecond, release)
	t.Cleanup(func() {
		if timer.Stop() {
			release()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := d.Call(ctx, devicewire.NodeCall{MachineID: "computer", Method: "sessions.list"})
	if err != nil {
		t.Fatal(err)
	}
	got := m.sessionList()
	if len(got) != 1 || got[0].Status != proto.StatusWorking {
		t.Fatalf("sessions.list response after the first-connection wait was not cached: %+v", got)
	}
	if got := forwarded(); !slices.Equal(got, []string{proto.StatusWorking}) {
		t.Fatalf("forwarded session statuses = %v, want the explicit Working response", got)
	}
}

func TestExplicitSessionsListResponseAfterReplacementPeerRetryUpdatesCache(t *testing.T) {
	firstAttempt, allowRefusal := make(chan struct{}), make(chan struct{})
	var refusalOnce sync.Once
	releaseRefusal := func() { refusalOnce.Do(func() { close(allowRefusal) }) }
	t.Cleanup(releaseRefusal)
	d, m := explicitSessionsListMachine(t, func(*rpc.Peer) (any, error) {
		close(firstAttempt)
		<-allowRefusal
		return nil, &rpc.Error{Code: nodeproto.CodeRestarting, Message: nodeproto.CodeRestarting}
	})
	forwarded := recordSessionEventStatuses(d)

	type callResult struct {
		out json.RawMessage
		err error
	}
	result := make(chan callResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := d.Call(ctx, devicewire.NodeCall{MachineID: "computer", Method: "sessions.list"})
		result <- callResult{out: out, err: err}
	}()
	select {
	case <-firstAttempt:
	case <-time.After(5 * time.Second):
		t.Fatal("sessions.list did not reach the first peer")
	}
	retried := make(chan struct{})
	attachExplicitSessionsListPeer(t, m, func(*rpc.Peer) (any, error) {
		close(retried)
		return explicitListWithStatus(proto.StatusWorking), nil
	})
	releaseRefusal()

	select {
	case answered := <-result:
		if answered.err != nil {
			t.Fatal(answered.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sessions.list was not retried on the replacement peer")
	}
	select {
	case <-retried:
	default:
		t.Fatal("sessions.list did not use the replacement peer")
	}
	got := m.sessionList()
	if len(got) != 1 || got[0].Status != proto.StatusWorking {
		t.Fatalf("sessions.list response after the replacement-peer retry was not cached: %+v", got)
	}
	if got := forwarded(); !slices.Equal(got, []string{proto.StatusWorking}) {
		t.Fatalf("forwarded session statuses = %v, want the retried Working response", got)
	}
}

func TestExplicitSessionsListResponseCannotOverwriteReplacementPeer(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseResponse := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseResponse)
	d, m := explicitSessionsListMachine(t, func(*rpc.Peer) (any, error) {
		close(started)
		<-release
		return explicitListWithStatus(proto.StatusWorking), nil
	})
	forwarded := recordSessionEventStatuses(d)

	type callResult struct {
		out json.RawMessage
		err error
	}
	result := make(chan callResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := d.Call(ctx, devicewire.NodeCall{MachineID: "computer", Method: "sessions.list"})
		result <- callResult{out: out, err: err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		releaseResponse()
		t.Fatal("sessions.list did not reach the old peer")
	}

	m.mu.Lock()
	m.peer, m.online = new(rpc.Peer), true
	m.mu.Unlock()
	m.setSessions([]proto.Session{{SessionID: "agent", Status: proto.StatusDone}})
	releaseResponse()

	var answered callResult
	select {
	case answered = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("old peer's sessions.list response was not released")
	}
	if answered.err != nil {
		t.Fatal(answered.err)
	}
	var response struct {
		Sessions []proto.Session `json:"sessions"`
	}
	if json.Unmarshal(answered.out, &response) != nil || len(response.Sessions) != 1 || response.Sessions[0].Status != proto.StatusWorking {
		t.Fatalf("the caller did not receive the old peer's response: %s", answered.out)
	}
	if got := m.sessionList(); len(got) != 1 || got[0].Status != proto.StatusDone {
		t.Fatalf("old peer's response replaced the replacement's sessions: %+v", got)
	}
	if got := forwarded(); len(got) != 0 {
		t.Fatalf("old peer's response was forwarded with statuses %v", got)
	}
}

func TestRelistResponseCannotOverwriteNewerNotification(t *testing.T) {
	d, m := explicitSessionsListMachine(t, func(nodePeer *rpc.Peer) (any, error) {
		if err := nodePeer.Notify("sessions.changed", explicitListWithStatus(proto.StatusDone)); err != nil {
			return nil, err
		}
		return explicitListWithStatus(proto.StatusWorking), nil
	})
	forwarded := recordSessionEventStatuses(d)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Relist(ctx, "computer"); err != nil {
		t.Fatal(err)
	}
	got := m.sessionList()
	if len(got) != 1 || got[0].Status != proto.StatusDone {
		t.Fatalf("older Relist response replaced the newer notification: %+v", got)
	}
	if got := forwarded(); !slices.Equal(got, []string{proto.StatusDone}) {
		t.Fatalf("forwarded session statuses = %v, want only the newer Done notification", got)
	}
}

func TestRelistResponseUpdatesCacheAndForwards(t *testing.T) {
	d, m := explicitSessionsListMachine(t, func(*rpc.Peer) (any, error) {
		return explicitListWithStatus(proto.StatusWorking), nil
	})
	forwarded := recordSessionEventStatuses(d)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Relist(ctx, "computer"); err != nil {
		t.Fatal(err)
	}
	got := m.sessionList()
	if len(got) != 1 || got[0].Status != proto.StatusWorking || got[0].MachineID != "computer" {
		t.Fatalf("uncontended Relist response was not cached: %+v", got)
	}
	if got := forwarded(); !slices.Equal(got, []string{proto.StatusWorking}) {
		t.Fatalf("forwarded session statuses = %v, want Working", got)
	}
}

func TestReconfigureClaudeDirsResponseCannotOverwriteNewerNotification(t *testing.T) {
	d, m := explicitSessionsListMachine(t, func(nodePeer *rpc.Peer) (any, error) {
		if err := nodePeer.Notify("sessions.changed", explicitListWithStatus(proto.StatusDone)); err != nil {
			return nil, err
		}
		return explicitListWithStatus(proto.StatusWorking), nil
	})
	forwarded := recordSessionEventStatuses(d)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.reconfigureClaudeDirs(ctx)
	got := m.sessionList()
	if len(got) != 1 || got[0].Status != proto.StatusDone {
		t.Fatalf("older reconfigureClaudeDirs response replaced the newer notification: %+v", got)
	}
	if got := forwarded(); !slices.Equal(got, []string{proto.StatusDone}) {
		t.Fatalf("forwarded session statuses = %v, want only the newer Done notification", got)
	}
}

func TestReconfigureClaudeDirsResponseUpdatesCacheAndForwards(t *testing.T) {
	d, m := explicitSessionsListMachine(t, func(*rpc.Peer) (any, error) {
		return explicitListWithStatus(proto.StatusWorking), nil
	})
	forwarded := recordSessionEventStatuses(d)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.reconfigureClaudeDirs(ctx)
	got := m.sessionList()
	if len(got) != 1 || got[0].Status != proto.StatusWorking || got[0].MachineID != "computer" {
		t.Fatalf("uncontended reconfigureClaudeDirs response was not cached: %+v", got)
	}
	if got := forwarded(); !slices.Equal(got, []string{proto.StatusWorking}) {
		t.Fatalf("forwarded session statuses = %v, want Working", got)
	}
}

func TestWaitNodeIdleResponseCannotOverwriteNewerNotification(t *testing.T) {
	_, m := explicitSessionsListMachine(t, func(nodePeer *rpc.Peer) (any, error) {
		if err := nodePeer.Notify("sessions.changed", explicitListWithStatus(proto.StatusDone)); err != nil {
			return nil, err
		}
		return explicitListWithStatus(proto.StatusWorking), nil
	})
	m.deployRetry = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := m.waitNodeIdle(ctx, time.Time{}); err != nil {
		t.Fatalf("waitNodeIdle did not continue from the accepted idle state: %v", err)
	}
	got := m.sessionList()
	if len(got) != 1 || got[0].Status != proto.StatusDone {
		t.Fatalf("older waitNodeIdle response replaced the newer notification: %+v", got)
	}
}

func TestWaitNodeIdleResponseUpdatesCache(t *testing.T) {
	_, m := explicitSessionsListMachine(t, func(*rpc.Peer) (any, error) {
		return explicitListWithStatus(proto.StatusIdle), nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.waitNodeIdle(ctx, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got := m.sessionList()
	if len(got) != 1 || got[0].Status != proto.StatusIdle || got[0].MachineID != "computer" {
		t.Fatalf("uncontended waitNodeIdle response was not cached: %+v", got)
	}
}

func explicitSessionsListMachine(t *testing.T, respond func(*rpc.Peer) (any, error)) (*Devices, *Machine) {
	t.Helper()
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "computer", Name: "computer", Kind: kindLink})
	d.addEntry(m)
	attachExplicitSessionsListPeer(t, m, respond)
	return d, m
}

func attachExplicitSessionsListPeer(t *testing.T, m *Machine, respond func(*rpc.Peer) (any, error)) {
	t.Helper()
	managerSide, nodeSide := net.Pipe()
	managerPeer := rpc.New("m", managerSide)
	nodePeer := rpc.New("n", nodeSide)
	m.mu.Lock()
	m.peer, m.online = managerPeer, true
	m.mu.Unlock()
	managerPeer.OnEvent = m.onEvent
	nodePeer.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method != "sessions.list" {
			return map[string]any{"ok": true}, nil
		}
		return respond(nodePeer)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = managerPeer.Serve(ctx, managerSide) }()
	go func() { _ = nodePeer.Serve(ctx, nodeSide) }()
	t.Cleanup(func() {
		cancel()
		managerPeer.Close(context.Canceled)
		nodePeer.Close(context.Canceled)
		_ = managerSide.Close()
		_ = nodeSide.Close()
	})
}

func explicitListWithStatus(status string) map[string]any {
	return map[string]any{"sessions": []proto.Session{{SessionID: "agent", Status: status}}}
}

func recordSessionEventStatuses(d *Devices) func() []string {
	var mu sync.Mutex
	var statuses []string
	d.opts.Event = func(_ string, event string, params json.RawMessage) bool {
		if event == "sessions.changed" {
			var p struct {
				Sessions []proto.Session `json:"sessions"`
			}
			if json.Unmarshal(params, &p) == nil {
				mu.Lock()
				for _, session := range p.Sessions {
					statuses = append(statuses, session.Status)
				}
				mu.Unlock()
			}
		}
		return true
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(statuses)
	}
}
