package node

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestDesktopCreatedCodexDoesNotBypassSwitchNowRefusal(t *testing.T) {
	n, terminal, _, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	const id = "0f1e2d3c-4b5a-4968-8776-655443322110"
	n.state.put(id, sessionState{Launcher: codexAppLauncher, Cwd: cwd})
	n.state.setPendingSwitch(id, &proto.PendingSwitch{Launcher: "work", At: time.Now()})
	err := tellNow(t, n, id, "go on")
	if code, data := refusalOf(t, err); code != proto.CodeSwitchUnsupported || data["delivered"] != deliveredNone {
		t.Fatalf("desktop creation recovery bypassed launcher-switch refusal: %v (%v)", err, data)
	}
	if n.pendingSwitch(id) != nil {
		t.Fatal("unsupported desktop session kept its pending switch")
	}
	if trace := drove(terminal); len(trace) != 0 {
		t.Fatalf("refused switch drove a terminal: %v", trace)
	}
}
