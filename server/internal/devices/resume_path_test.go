package devices

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type betterPathResumeFixture struct {
	d             *Devices
	m             *Machine
	betterSource  *Machine
	n             *residentNode
	logs          *benchLog
	mine, better  *nodePath
	oldPathFailed atomic.Bool
}

func newBetterPathResumeFixture(t *testing.T) *betterPathResumeFixture {
	t.Helper()
	r := &betterPathResumeFixture{
		d:    newTestDevices(t),
		n:    &residentNode{release: make(chan struct{}), slowSeen: make(chan struct{})},
		logs: &benchLog{},
	}
	quiet := r.d.logf
	r.d.logf = func(format string, args ...any) {
		r.logs.add(fmt.Sprintf(format, args...))
		quiet(format, args...)
	}
	r.m = newMachine(r.d, MachineConfig{ID: "box", Name: "box", Kind: "ssh"})
	r.m.dialNode = func(ctx context.Context) (net.Conn, error) {
		if r.oldPathFailed.Load() {
			return nil, errors.New("the old path is down")
		}
		return r.n.dial(ctx)
	}
	r.betterSource = newMachine(r.d, MachineConfig{ID: "box-local", Name: "box-local", Kind: "local"})
	r.betterSource.dialNode = r.n.dial
	r.d.addEntry(r.m)
	r.mine, r.better = cfgPath(r.m), cfgPath(r.betterSource)
	f := &r.d.fleet
	f.mu.Lock()
	f.init()
	f.paths[r.m] = []*nodePath{r.better, r.mine}
	f.absorbed[r.betterSource] = r.m
	// Hold the better path back until the original connection is up and the test is ready to drop it.
	f.health[r.better.key] = &pathHealth{failUntil: time.Now().Add(time.Hour)}
	f.mu.Unlock()
	runMachine(t, r.m)
	eventually(t, 10*time.Second, "connected, resumable through the original path", func() bool {
		return r.m.reachable() && r.m.activePathKey() == r.mine.key && r.logs.count("lanes=on resume=on") > 0
	})
	return r
}

func (r *betterPathResumeFixture) failOldPathAndCut() {
	r.oldPathFailed.Store(true)
	f := &r.d.fleet
	f.mu.Lock()
	f.health[r.better.key] = &pathHealth{}
	f.mu.Unlock()
	r.n.cut()
}

// A connection whose path failed resumes over a better path to the same resident node: its calls and connection live,
// and the machine's route moves with it (#10944).
func TestMachineResumesOverABetterPath(t *testing.T) {
	r := newBetterPathResumeFixture(t)
	d, m, n, logs, better := r.d, r.m, r.n, r.logs, r.better

	call := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		call <- m.call(ctx, "slow", map[string]any{}, nil)
	}()
	select {
	case <-n.slowSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not reach the node")
	}

	r.failOldPathAndCut()
	eventually(t, 10*time.Second, "the connection on the better path", func() bool {
		return m.reachable() && m.activePathKey() == better.key
	})
	close(n.release)
	if err := <-call; err != nil {
		t.Errorf("the call in flight: %v", err)
	}
	if transports, fresh := n.counts(); transports != 2 || fresh != 1 {
		t.Errorf("%d transports, %d served from the start: want 2 and 1 (a resume, not a full reconnect)", transports, fresh)
	}
	if got := logs.count("] connected over "); got != 1 {
		t.Errorf("connected from the start %d times: want 1", got)
	}
	m.mu.Lock()
	counts := m.resumes
	m.mu.Unlock()
	if counts.drops != 1 || counts.resumed != 1 || counts.fellBack != 0 {
		t.Errorf("resume counts: %s; want drops=1 resumed=1 fell_back=0", counts)
	}
	destination := pathName(d, better)
	loggedDestination := false
	for _, line := range logs.since(0) {
		if strings.Contains(line, "] connection over ") && strings.Contains(line, " resumed after ") && strings.Contains(line, destination) {
			loggedDestination = true
		}
	}
	if !loggedDestination {
		t.Errorf("no successful resume log names the destination path %q: %q", destination, logs.since(0))
	}
	if got := m.transportNow(); got != "local" {
		t.Errorf("the machine's route uses %q: want local", got)
	}
}

// A refused resume on the better path still ends in a full reconnect there. The extra transport before the fresh
// connection proves that the manager tried rpc.resume on the better path rather than reconnecting immediately.
func TestMachineFallsBackWhenBetterPathResumeIsRefused(t *testing.T) {
	r := newBetterPathResumeFixture(t)
	r.n.mu.Lock()
	r.n.restarted = true
	r.n.mu.Unlock()
	r.failOldPathAndCut()
	eventually(t, 10*time.Second, "connected anew over the better path", func() bool {
		return r.m.reachable() && r.m.activePathKey() == r.better.key && r.logs.count("] connected over ") == 2
	})
	if transports, fresh := r.n.counts(); transports != 3 || fresh != 2 {
		t.Errorf("%d transports, %d served from the start: want 3 and 2 (a refused better-path resume, then a full reconnect)", transports, fresh)
	}
	r.m.mu.Lock()
	counts := r.m.resumes
	r.m.mu.Unlock()
	if counts.drops != 1 || counts.resumed != 0 || counts.fellBack != 1 {
		t.Errorf("resume counts: %s; want drops=1 resumed=0 fell_back=1", counts)
	}
	if r.logs.count("drops=1 resumed=0 fell_back=1") == 0 {
		t.Errorf("the fallback counts are not logged: %q", r.logs.since(0))
	}
}

// A transport error trying the better path is also a fallback, not a reason to skip that resume attempt. Its error is
// carried into the fallback log, and the next dial makes the fresh connection there.
func TestMachineFallsBackWhenBetterPathResumeTransportFails(t *testing.T) {
	r := newBetterPathResumeFixture(t)
	const dialError = "better-path resume transport failed deliberately"
	var betterDials atomic.Int32
	r.betterSource.dialNode = func(ctx context.Context) (net.Conn, error) {
		if betterDials.Add(1) == 1 {
			return nil, errors.New(dialError)
		}
		return r.n.dial(ctx)
	}
	r.failOldPathAndCut()
	eventually(t, 10*time.Second, "connected anew after the better-path resume transport failed", func() bool {
		return r.m.reachable() && r.m.activePathKey() == r.better.key && r.logs.count("] connected over ") == 2
	})
	if got := betterDials.Load(); got != 2 {
		t.Errorf("the better path was dialed %d times: want a failed resume dial, then a fresh connection", got)
	}
	if transports, fresh := r.n.counts(); transports != 2 || fresh != 2 {
		t.Errorf("%d transports, %d served from the start: want 2 and 2 (the failed dial made no transport)", transports, fresh)
	}
	r.m.mu.Lock()
	counts := r.m.resumes
	r.m.mu.Unlock()
	if counts.drops != 1 || counts.resumed != 0 || counts.fellBack != 1 {
		t.Errorf("resume counts: %s; want drops=1 resumed=0 fell_back=1", counts)
	}
	loggedFailure := false
	for _, line := range r.logs.since(0) {
		if strings.Contains(line, "connection not resumed") && strings.Contains(line, dialError) {
			loggedFailure = true
		}
	}
	if !loggedFailure {
		t.Errorf("the fallback log does not contain the better-path resume error %q: %q", dialError, r.logs.since(0))
	}
}
