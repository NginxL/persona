package node

import (
	"testing"
)

// A stdio node stops, as it ends, the herdr server it started itself when nothing is open in it (#10114); a server
// with a pane, or one it did not start, is left running.
func TestStopUnusedHerdr(t *testing.T) {
	for _, tc := range []struct {
		name    string
		started bool
		panes   []any
		stop    bool
	}{
		{"own and empty", true, []any{}, true},
		{"own with a pane", true, []any{map[string]any{"pane_id": "w1:p1"}}, false},
		{"not started here", false, []any{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHerdr(t)
			f.reply("pane.list", map[string]any{"panes": tc.panes})
			f.reply("server.stop", map[string]any{"type": "ok"})
			n, _ := testNode(t)
			if tc.started {
				n.hstart.started = f.sock
			}
			n.StopUnusedHerdr(t.Context())
			if got := len(f.trace("server.stop")); (got == 1) != tc.stop {
				t.Fatalf("server.stop calls = %d, want stop %v (trace %q)", got, tc.stop, f.trace())
			}
			if tc.stop && n.hstart.started != "" {
				t.Errorf("still recorded as started: %q", n.hstart.started)
			}
		})
	}
}

// A server that does not answer is left alone.
func TestStopUnusedHerdrLeavesSilentServer(t *testing.T) {
	n, _ := testNode(t)
	n.hstart.started = "/tmp/persona-no-such-herdr.sock"
	n.StopUnusedHerdr(t.Context())
	if n.hstart.started == "" {
		t.Fatal("forgot a server it could not check")
	}
}
