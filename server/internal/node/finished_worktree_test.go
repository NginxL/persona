package node

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// worktreeShell is a herdr pane entry for the first shell of a worktree the assistant made for an Agent.
func worktreeShell(pane, tab, ws, cwd string) map[string]any {
	return map[string]any{"pane_id": pane, "tab_id": tab, "workspace_id": ws, "cwd": cwd,
		"tokens": map[string]any{personaWorktreeToken: "1"}}
}

// The first shell of an Agent's worktree closes with the Agent's finished terminal once the grace period is over, so
// the whole workspace goes (#10624).
func TestWorktreeShellClosesWithItsFinishedAgent(t *testing.T) {
	n, f, cwd := keptAgent(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, []any{
		worktreeShell("w5:p1", "w5:t1", "w5", cwd), shellPane("w5:p2", "w5:t2", "w5", cwd, sidA)}))
	t0 := time.Now()
	got := rounds(t, n, t0, 0, testGrace-time.Second, testGrace)
	if len(got[0]) != 0 || len(got[1]) != 0 {
		t.Fatalf("closed within the grace period: %q", got)
	}
	slices.Sort(got[2])
	if !slices.Equal(got[2], []string{"w5:p1", "w5:p2"}) {
		t.Fatalf("after the grace period closed %q", got[2])
	}
}

// The worktree's first shell stays while anything else is in its workspace: a working, idle or waiting Agent, or a
// pane the user opened there. A worktree the user made (no mark) is never closed, even alone.
func TestWorktreeShellStaysWhileItsWorkspaceIsInUse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		panes func(cwd string) (agents, panes []any)
		want  []string // what the round after the grace period closes
	}{
		{"working agent", func(cwd string) ([]any, []any) {
			return []any{claudeAgent(sidA, proto.StatusWorking, "w5:p2", "w5:t2", "w5", cwd, "")},
				[]any{worktreeShell("w5:p1", "w5:t1", "w5", cwd), claudePane(sidA, "w5:p2", "w5:t2", "w5", cwd)}
		}, nil},
		{"waiting agent", func(cwd string) ([]any, []any) {
			return []any{claudeAgent(sidA, proto.StatusBlocked, "w5:p2", "w5:t2", "w5", cwd, "")},
				[]any{worktreeShell("w5:p1", "w5:t1", "w5", cwd), claudePane(sidA, "w5:p2", "w5:t2", "w5", cwd)}
		}, nil},
		{"user's pane beside it", func(cwd string) ([]any, []any) {
			return nil, []any{worktreeShell("w5:p1", "w5:t1", "w5", cwd), shellPane("w5:p2", "w5:t2", "w5", cwd, sidA),
				shellPane("w5:p3", "w5:t3", "w5", cwd, "")}
		}, []string{"w5:p2"}},
		{"user's own worktree", func(cwd string) ([]any, []any) {
			return nil, []any{shellPane("w6:p1", "w6:t1", "w6", cwd, "")}
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, cwd := keptAgent(t)
			agents, panes := tc.panes(cwd)
			f.reply("session.snapshot", snapshot(nil, nil, agents, panes))
			got := rounds(t, n, time.Now(), 0, testGrace, 3*testGrace)
			if len(got[0]) != 0 || !slices.Equal(got[1], tc.want) || len(got[2]) != 0 {
				t.Fatalf("closed %q, want %q after the grace period", got, tc.want)
			}
		})
	}
}

// Only the assistant's worktree.create (for_agent) marks the first shell; the user's New worktree marks nothing.
func TestMarkWorktreeShellOnlyForAgents(t *testing.T) {
	n, f, _ := herdrNode(t)
	h := n.hosts.Get(host.Herdr)
	created := &host.Created{SpaceID: "w5", SlotID: "w5:t1", Pane: host.PaneRef{Kind: host.Herdr, ID: "w5:p1"}}
	n.markWorktreeShell(context.Background(), h, created)
	if got := f.trace("pane.report_metadata"); len(got) != 0 {
		t.Fatalf("the user's worktree was marked: %q", got)
	}
	n.markWorktreeShell(withAgentWorktree(context.Background(), true), h, created)
	got := f.trace("pane.report_metadata")
	if len(got) != 1 || !strings.Contains(got[0], `"pane_id":"w5:p1"`) || !strings.Contains(got[0], personaWorktreeToken) {
		t.Fatalf("report_metadata %q", got)
	}
}

// A task's Agent the hub stopped as task_completed keeps that stop cause when its finished terminal is closed, so the
// assistant can still resume it, in a new tab (the live check after #10111 stopped its Agent by hand, which records no
// cause).
func TestSupervisorResumeAfterFinishedTerminalClosed(t *testing.T) {
	n, f, cwd := keptAgent(t)
	n.state.stopped(sidA, proto.StopTaskCompleted)
	f.after(func(c herdrCall) {
		if c.Method == "tab.close" || c.Method == "pane.close" {
			f.reply("session.snapshot", snapshot([]any{map[string]any{"workspace_id": "w1", "label": "proj", "cwd": cwd}}, nil, nil, nil))
		}
	})
	if got := rounds(t, n, time.Now(), 0, testGrace); !slices.Equal(got[1], []string{"w1:p1"}) {
		t.Fatalf("closed %q", got)
	}
	if c := n.stopCause(sidA); c != proto.StopTaskCompleted {
		t.Fatalf("stop cause after the close = %q", c)
	}
	if err := n.supervisorResumeAllowed(sidA, "", ""); err != nil {
		t.Fatalf("supervisor resume refused after the close: %v", err)
	}
	f.reset()
	f.reply("session.snapshot", snapshot([]any{map[string]any{"workspace_id": "w1", "label": "proj", "cwd": cwd}}, nil, nil, nil))
	zshInFront(f)
	n.claudeStart, n.claudeReady = 200*time.Millisecond, 200*time.Millisecond // the fake shell never runs claude
	_, _ = request(t, n, "session.start", map[string]any{"resume_session_id": sidA, "resume_automatic": true})
	if got := f.trace("tab.create", "workspace.create"); len(got) == 0 {
		t.Fatalf("the supervisor's resume opened no new tab: %q", f.trace())
	}
	var typed string
	for _, c := range f.calls() {
		if c.Method == "pane.send_input" {
			typed += str(c.Params["text"])
		}
	}
	if !strings.Contains(typed, "--resume "+sidA) {
		t.Errorf("typed %q", typed)
	}
}
