package link

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
)

// sshReady also requires the ordered endpoints and node messages, so lifecycle actions cannot close the socket while
// those initial writes are still pending.
func sshReady(s Status, connected func(Status) bool) bool {
	return connected(s) && s.HubName == "home" && s.Node == "online"
}

// waitForSSHReady is the shared readiness gate for SSH lifecycle actions. The
// connected match keeps each lifecycle step's connection and mode requirements.
func waitForSSHReady(t *testing.T, ch chan Status, connected func(Status) bool) Status {
	t.Helper()
	return waitFor(t, ch, func(s Status) bool { return sshReady(s, connected) })
}

func TestSSHLifecycleReadinessObservesInitialHubMessages(t *testing.T) {
	connected := func(s Status) bool {
		return s.State == "connected" && s.Kind == KindSSH && s.Conn == 1
	}

	t.Run("connected before initial hub messages", func(t *testing.T) {
		ch := make(chan Status, 3)
		ch <- Status{State: "connected", Kind: KindSSH, Conn: 1, Node: "unknown"}
		ch <- Status{State: "connected", Kind: KindSSH, Conn: 1, HubName: "home", Node: "unknown"}
		ch <- Status{State: "connected", Kind: KindSSH, Conn: 1, HubName: "home", Node: "online"}

		st := waitForSSHReady(t, ch, connected)
		if st.HubName != "home" || st.Node != "online" {
			t.Fatalf("SSH lifecycle became ready before the hub's initial endpoints and node messages were observed: %+v", st)
		}
	})

	t.Run("normal ready control", func(t *testing.T) {
		ch := make(chan Status, 1)
		want := Status{State: "connected", Kind: KindSSH, Conn: 1, HubName: "home", Node: "online"}
		ch <- want

		if got := waitForSSHReady(t, ch, connected); !reflect.DeepEqual(got, want) {
			t.Fatalf("ready SSH status = %+v, want %+v", got, want)
		}
	})
}

// TestSSHReconnectInitialWriteBoundary runs the real lifecycle with an abortive tunnel close and the second hub
// connection held immediately before its initial writes. The old readiness gate kills that connection; the corrected
// gate recognizes that its messages are still pending and lets the normal reconnect, mode-switch and unpair checks run.
func TestSSHReconnectInitialWriteBoundary(t *testing.T) {
	t.Setenv(standInSSHAbortiveCloseEnv, "1")
	var firstConn atomic.Uint64
	secondConnected := make(chan Status, 1)
	var secondOnce sync.Once

	beforeInitialWrites := func(ssh *sshHarness, connection int) {
		if connection != 2 {
			return
		}
		var st Status
		select {
		case st = <-secondConnected:
		case <-time.After(5 * time.Second):
			t.Error("reconnect status did not reach the initial-write boundary")
			return
		}
		runs := ssh.runs()
		if len(runs) < 2 {
			t.Errorf("reconnect starts = %d, want at least 2", len(runs))
			return
		}
		connected := func(s Status) bool {
			return s.State == "connected" && s.Kind == KindSSH && s.Conn > firstConn.Load()
		}
		if sshReady(st, connected) {
			pid := runs[len(runs)-1].PID
			if err := osproc.Kill(pid); err != nil {
				t.Errorf("kill premature-ready reconnect: %v", err)
			} else {
				waitCondition(t, 3*time.Second, "premature reconnect SSH cleanup", func() bool {
					return !osproc.AliveMine(pid)
				})
			}
		}
	}
	onStatus := func(s Status) {
		if s.State != "connected" || s.Kind != KindSSH {
			return
		}
		if firstConn.CompareAndSwap(0, s.Conn) {
			return
		}
		if s.Conn > firstConn.Load() {
			secondOnce.Do(func() { secondConnected <- s })
		}
	}
	testSSHReconnectLifecycle(t, beforeInitialWrites, onStatus)
}
