package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

const traexOwnerPane = "w1:p2"

func TestTraexPaneOwnersWaitForPaginatedMetadata(t *testing.T) {
	n, _, _ := startNode(t)
	traexHistoryFixture(t, n, nil)
	writeOwner(t, n, threadNew, time.Now())
	if got := n.traexOwners(); len(got) != 0 {
		t.Fatalf("an owner absent from paginated metadata was guessed to be a conversation: %v", got)
	}
}

func TestTraexPaginatedConversationDirectoryDiffersFromContainer(t *testing.T) {
	n, _, _ := startNode(t)
	traexHistoryFixture(t, n, nil)
	writeOwner(t, n, threadMain, time.Now())
	cacheTraexOwnerPane(t, os.Getpid())
	panes := []host.Agent{{PaneID: traexOwnerPane, Cwd: "/work/unrelated-project", WorkspaceID: "container", RawTitle: "⠸ running"}}
	got := n.traexAgents(context.Background(), panes, nil)
	if len(got) != 1 || got[0].Cwd != "/work/app" || got[0].WorkspaceID != "container" {
		t.Fatalf("paginated conversation directory or terminal placement is wrong: %+v", got)
	}
}

// Two live processes with valid main threads inherit the same terminal pane.
func traexPaneOwnerFixture(t *testing.T) *Node {
	t.Helper()
	if !alive(os.Getppid()) {
		t.Skip("the test's parent is not this user's live process")
	}
	n, _, _ := startNode(t)
	traexHistoryFixture(t, n, nil)
	writeCodexApp(t, n.traexHome(), "/work/app", `UPDATE threads SET id = '`+threadNew+
		`', archived = 0, source = 'cli', name = 'Nested task' WHERE id = '`+threadOld+`';`)
	invalidateCodexPaginated(n.traexHome())
	writeTraexOwner(t, n.home, threadMain, os.Getppid())
	writeTraexOwner(t, n.home, threadNew, os.Getpid())
	cacheTraexOwnerPane(t, os.Getppid(), os.Getpid())
	return n
}

func cacheTraexOwnerPane(t *testing.T, pids ...int) {
	t.Helper()
	traexCache.Lock()
	previous, present := map[int]string{}, map[int]bool{}
	for _, pid := range pids {
		previous[pid], present[pid] = traexCache.panes[pid]
		traexCache.panes[pid] = traexOwnerPane
	}
	traexCache.Unlock()
	t.Cleanup(func() {
		traexCache.Lock()
		defer traexCache.Unlock()
		for pid, pane := range previous {
			if present[pid] {
				traexCache.panes[pid] = pane
			} else {
				delete(traexCache.panes, pid)
			}
		}
	})
}

func TestTraexPaneOwnersKeepConfirmedHostSession(t *testing.T) {
	n := traexPaneOwnerFixture(t)
	panes := []host.Agent{{PaneID: traexOwnerPane, RawTitle: "⠸ running"}}
	for range 64 {
		agents := []host.Agent{{Agent: traexAgent, PaneID: traexOwnerPane,
			AgentSession: &host.AgentSession{Kind: "id", Value: threadMain}}}
		if added := n.traexAgents(context.Background(), panes, agents); len(added) != 0 {
			t.Fatalf("the same pane was added again: %+v", added)
		}
		if got := agents[0]; got.SessionID() != threadMain || got.AgentStatus != proto.StatusWorking {
			t.Fatalf("a nested writer replaced the terminal's confirmed session: %+v", got)
		}
	}
}

func TestTraexPaneOwnersChooseAncestorOncePerPane(t *testing.T) {
	if p, err := parentPID(os.Getpid()); err != nil || p != os.Getppid() {
		t.Skip("process ancestry is unavailable")
	}
	for _, tc := range []struct {
		name     string
		reported bool
		id       string
	}{
		{name: "undetected terminal"},
		{name: "sessionless host", reported: true},
		{name: "stale host", reported: true, id: threadOld},
		{name: "nested host claim", reported: true, id: threadNew},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := traexPaneOwnerFixture(t)
			panes := []host.Agent{{PaneID: traexOwnerPane, RawTitle: "⠸ running"}}
			for range 32 {
				var agents []host.Agent
				if tc.reported {
					agents = []host.Agent{{Agent: traexAgent, PaneID: traexOwnerPane,
						AgentSession: &host.AgentSession{Kind: "id", Value: tc.id}}}
				}
				added := n.traexAgents(context.Background(), panes, agents)
				all := append(agents, added...)
				if len(all) != 1 || all[0].SessionID() != threadMain || all[0].AgentStatus != proto.StatusWorking {
					t.Fatalf("expected only the ancestor's terminal conversation, got %+v", all)
				}
			}
		})
	}
}

func TestTraexPaneOwnersKeepOlderMainWriterForSameProcess(t *testing.T) {
	n := traexPaneOwnerFixture(t)
	writeTraexOwner(t, n.home, threadNew, os.Getppid())
	old := time.Now().Add(-time.Hour)
	p := filepath.Join(n.traexHome(), "thread-writer-locks", threadMain+".owner.json")
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	agents := []host.Agent{{Agent: traexAgent, PaneID: traexOwnerPane,
		AgentSession: &host.AgentSession{Kind: "id", Value: threadMain}}}
	added := n.traexAgents(context.Background(), []host.Agent{{PaneID: traexOwnerPane, RawTitle: "⠸ running"}}, agents)
	if len(added) != 0 || agents[0].SessionID() != threadMain {
		t.Fatalf("the newer main thread displaced a still-owned host session: %+v, %+v", agents, added)
	}
}

func TestTraexPaneOwnersDoNotPromotePaginatedGuardian(t *testing.T) {
	n, _, _ := startNode(t)
	traexHistoryFixture(t, n, nil)
	writeOwner(t, n, threadGuardian, time.Now())
	cacheTraexOwnerPane(t, os.Getpid())
	if got := n.traexOwners(); len(got) != 0 {
		t.Fatalf("a guardian without rollout ancestry became a main owner: %v", got)
	}
	for _, reported := range []bool{false, true} {
		var agents []host.Agent
		if reported {
			agents = []host.Agent{{Agent: traexAgent, PaneID: traexOwnerPane,
				AgentSession: &host.AgentSession{Kind: "id", Value: threadGuardian}}}
		}
		added := n.traexAgents(context.Background(), []host.Agent{{PaneID: traexOwnerPane}}, agents)
		if len(added) != 0 || reported && agents[0].SessionID() != "" {
			t.Fatalf("a known guardian surfaced as a root conversation: %+v, %+v", agents, added)
		}
	}
}

func TestTraexPaginatedGuardianRolloutIdentifiesPane(t *testing.T) {
	n, _, _ := startNode(t)
	traexHistoryFixture(t, n, nil)
	writeMetaRollout(t, n.traexHome(), threadGuardian, `{"id":"`+threadGuardian+`","session_id":"`+threadMain+
		`","thread_source":"subagent"}`, time.Now())
	writeOwner(t, n, threadGuardian, time.Now())
	cacheTraexOwnerPane(t, os.Getpid())
	got := n.traexAgents(context.Background(), []host.Agent{{PaneID: traexOwnerPane}}, nil)
	if len(got) != 1 || got[0].SessionID() != threadMain {
		t.Fatalf("pane with only the guardian's lock: %+v, want its parent", got)
	}
}

func TestTraexPaneOwnersProcessAncestry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		parents   map[int]int
		preferred string
		want      string
	}{
		{name: "nested writer has smaller pid", parents: map[int]int{10: 30, 30: 90, 90: 1}, want: threadMain},
		{name: "nested host is superseded", parents: map[int]int{10: 30, 30: 90, 90: 1}, preferred: threadNew, want: threadMain},
		{name: "independent host wins", parents: map[int]int{10: 1, 90: 1}, preferred: threadMain, want: threadMain},
		{name: "independent roots are stable", parents: map[int]int{10: 1, 90: 1}, want: threadNew},
		{name: "unknown ancestry preserves owned host", preferred: threadMain, want: threadMain},
		{name: "unknown ancestry is not guessed"},
		{name: "cyclic ancestry is not guessed", parents: map[int]int{10: 90, 90: 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owners := map[int][]string{90: {threadMain}, 10: {threadNew}}
			parent := func(pid int) (int, error) {
				if next, ok := tc.parents[pid]; ok {
					return next, nil
				}
				return 0, os.ErrPermission
			}
			for range 32 {
				if got := traexPaneThread(owners, tc.preferred, parent); got != tc.want {
					t.Fatalf("terminal conversation = %q, want %q", got, tc.want)
				}
			}
		})
	}
}
