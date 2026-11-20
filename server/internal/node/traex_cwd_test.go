package node

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

// An Orca terminal can use an unrelated workspace as a container, then cd into
// the requested directory. The conversation directory is not that container.
func TestTraexConversationDirectoryDiffersFromTerminalContainer(t *testing.T) {
	n, _, _ := startNode(t)
	const pane = "w1:p2"
	const cwd = "/work/isolated-probe"
	writeMetaRollout(t, n.traexHome(), threadMain,
		`{"id":"`+threadMain+`","cwd":"`+cwd+`","source":"cli"}`, time.Now())
	writeOwner(t, n, threadMain, time.Now())
	traexCache.Lock()
	previous, had := traexCache.panes[os.Getpid()]
	traexCache.panes[os.Getpid()] = pane
	traexCache.Unlock()
	t.Cleanup(func() {
		traexCache.Lock()
		defer traexCache.Unlock()
		if had {
			traexCache.panes[os.Getpid()] = previous
		} else {
			delete(traexCache.panes, os.Getpid())
		}
	})
	panes := []host.Agent{{PaneID: pane, Cwd: "/work/unrelated-project", WorkspaceID: "container", RawTitle: "⠸ running"}}
	agents := n.traexAgents(context.Background(), panes, nil)
	if len(agents) != 1 || agents[0].SessionID() != threadMain {
		t.Fatalf("conversation identity = %+v", agents)
	}
	if got := agents[0].Cwd; got != cwd {
		t.Fatalf("conversation directory = %q, want %q (terminal container is %q)", got, cwd, panes[0].Cwd)
	}
	if agents[0].WorkspaceID != "container" {
		t.Fatalf("the real terminal container was lost: %+v", agents[0])
	}
}
