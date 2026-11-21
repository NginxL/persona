package node

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func startHerdrWorkspaceSession(t *testing.T, task bool) (*Node, *fakeHerdr, string, string) {
	t.Helper()
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	f.reply("workspace.list", map[string]any{"workspaces": []any{}})
	f.reply("workspace.create", map[string]any{
		"workspace": map[string]any{"workspace_id": "w-task", "label": "created"},
		"tab":       map[string]any{"tab_id": "w-task:t1"}, "root_pane": map[string]any{"pane_id": "w-task:p1"},
	})
	startHerdr(f, "w-task:p1", proto.StatusIdle)
	f.setScreen("w-task:p1", screenFixture(t, "claude-2.1.312-empty-162.txt"))
	params := map[string]any{"session_id": startID, "cwd": cwd, "tab_name": "Leader"}
	if task {
		params["task_id"] = "todo-1"
		params["workspace_label"] = "Ship the change"
	}
	if _, err := request(t, n, "session.start", params); err != nil {
		t.Fatal(err)
	}
	return n, f, home, cwd
}

func TestHerdrOnlyTaskLeaderForcesNamedWorkspace(t *testing.T) {
	for _, task := range []bool{false, true} {
		name := "ordinary directory placement"
		if task {
			name = "leader task placement"
		}
		t.Run(name, func(t *testing.T) {
			_, f, _, cwd := startHerdrWorkspaceSession(t, task)
			wantLabel := filepath.Base(cwd)
			if task {
				wantLabel = "Ship the change"
			}
			creates := f.trace("workspace.create")
			if len(creates) != 1 || !strings.Contains(creates[0], `"label":"`+wantLabel+`"`) {
				t.Fatalf("workspace.create = %v; want one labelled %q (all calls: %v)", creates, wantLabel, f.trace())
			}
			if task && len(f.trace("workspace.list")) != 0 {
				t.Fatalf("task leader fell back to directory-based placement: %v", f.trace())
			}
			if !task && len(f.trace("workspace.list")) != 1 {
				t.Fatalf("ordinary session lost its directory workspace lookup: %v", f.trace())
			}
			if got := f.trace("tab.create"); len(got) != 0 {
				t.Fatalf("leader's new workspace was not reused for its first tab: %v", got)
			}
		})
	}
}

func TestHerdrTaskStartRecoversMissingWorktreeWorkspace(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		name := "reopen workspace"
		if reuse {
			name = "reuse exact checkout"
		}
		t.Run(name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, filepath.Join(home, "worktrees", "project"))
			other := mkdir(t, filepath.Join(home, "other", "project"))
			spaces := []any{map[string]any{"workspace_id": "w-other", "cwd": other, "label": "project"}}
			if reuse {
				spaces = append(spaces, map[string]any{"workspace_id": "w-current", "cwd": cwd, "label": "renamed"})
			}
			f.reply("workspace.list", map[string]any{"workspaces": spaces})
			f.handle("tab.create", func(p map[string]any) any {
				switch p["workspace_id"] {
				case "w-old":
					return herdrFail{Code: "workspace_not_found", Message: "the old workspace closed"}
				case "w-current":
					if p["cwd"] == cwd && reuse {
						return map[string]any{"tab": map[string]any{"tab_id": "w-current:t1"}, "root_pane": map[string]any{"pane_id": "w-current:p1"}}
					}
				}
				return herdrFail{Code: "wrong_workspace", Message: "unexpected tab destination"}
			})
			f.handle("workspace.create", func(p map[string]any) any {
				if p["cwd"] != cwd || reuse {
					return herdrFail{Code: "wrong_workspace", Message: "unexpected workspace creation"}
				}
				return map[string]any{"workspace": map[string]any{"workspace_id": "w-current"},
					"tab": map[string]any{"tab_id": "w-current:t1"}, "root_pane": map[string]any{"pane_id": "w-current:p1"}}
			})
			startHerdr(f, "w-current:p1", proto.StatusIdle)
			f.setScreen("w-current:p1", screenFixture(t, "claude-2.1.312-empty-162.txt"))
			if _, err := request(t, n, "session.start", map[string]any{
				"session_id": startID, "cwd": cwd, "workspace_id": "w-old", "reopen_worktree_workspace": true,
			}); err != nil {
				t.Fatalf("start in the recovered workspace: %v (calls: %v)", err, f.trace())
			}
			wantCreates := 1
			if reuse {
				wantCreates = 0
			}
			if got := f.trace("workspace.create"); len(got) != wantCreates {
				t.Fatalf("workspace creates: got %v, want %d", got, wantCreates)
			}
			if typed := f.trace("pane.send_input"); len(typed) != 1 || !strings.Contains(typed[0], `"pane_id":"w-current:p1"`) {
				t.Fatalf("start command should run once in the recovered workspace, got %v", typed)
			}
		})
	}
}

func TestHerdrTaskWorktreeKeepsCheckoutWithoutOpeningWorkspace(t *testing.T) {
	w := worktreeNode(t)
	dest := filepath.Join(w.home, "task-checkout")
	w.f.handle("worktree.create", func(params map[string]any) any {
		if params["open_workspace"] != false {
			return herdrFail{Code: "task_workspace_opened", Message: "a task checkout must not open another workspace"}
		}
		runPullGit(t, w.repo, "worktree", "add", "-b", str(params["branch"]), "--", str(params["path"]))
		return map[string]any{"worktree": map[string]any{"path": dest, "branch": str(params["branch"])}}
	})
	res, err := request(t, w.n, "worktree.create", map[string]any{
		"workspace_id": "w1", "branch": "task/todo-1", "path": dest,
		"for_agent": true, "open_workspace": false,
	})
	if err != nil {
		t.Fatalf("task checkout failed: %v (calls: %v)", err, w.f.trace())
	}
	if res["workspace_id"] != nil && res["workspace_id"] != "" {
		t.Fatalf("checkout opened a separate workspace: %v", res)
	}
	if branchOf(t, dest) != "task/todo-1" {
		t.Fatal("worker lost its own git worktree and branch")
	}
	if got := w.f.trace("workspace.create", "tab.create"); len(got) != 0 {
		t.Fatalf("checkout opened herdr UI: %v", got)
	}
}

func TestHerdrRetiredTaskResumeUsesRecordedWorkspace(t *testing.T) {
	n, f, _, cwd := startHerdrWorkspaceSession(t, true)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	listSessions(t, n) // the old Agent and its shell are gone; a resume needs a new tab
	f.reset()
	f.reply("workspace.list", map[string]any{"workspaces": []any{
		map[string]any{"workspace_id": "w-task", "label": "Ship the change"},
		map[string]any{"workspace_id": "w-basename", "label": filepath.Base(cwd)},
	}})
	f.reply("tab.create", map[string]any{
		"tab": map[string]any{"tab_id": "w-task:t2"}, "root_pane": map[string]any{"pane_id": "w-task:p2"},
	})
	startHerdr(f, "w-task:p2", proto.StatusIdle)
	f.setScreen("w-task:p2", screenFixture(t, "claude-2.1.312-empty-162.txt"))
	if _, err := request(t, n, "session.start", map[string]any{"resume_session_id": startID}); err != nil {
		t.Fatal(err)
	}
	tabs := f.trace("tab.create")
	if len(tabs) != 1 || !strings.Contains(tabs[0], `"workspace_id":"w-task"`) {
		t.Fatalf("resume did not use the recorded task workspace: tabs=%v all=%v", tabs, f.trace())
	}
	if got := f.trace("workspace.create"); len(got) != 0 {
		t.Fatalf("resume created a basename-derived workspace: %v", got)
	}
}

func TestHerdrTaskWorkspaceCloseChecksOwnershipNotLabel(t *testing.T) {
	n, f, _, cwd := startHerdrWorkspaceSession(t, true)
	f.reset()
	f.reply("session.snapshot", snapshot([]any{
		map[string]any{"workspace_id": "w-task", "label": "Renamed by the user", "cwd": cwd},
		map[string]any{"workspace_id": "w-user", "label": "Ship the change", "cwd": cwd},
		map[string]any{"workspace_id": "w-other", "label": "Another task", "cwd": cwd},
	}, nil, nil, nil))
	missing := false
	f.handle("workspace.close", func(params map[string]any) any {
		if missing {
			return herdrFail{Code: "workspace_not_found", Message: "already closed"}
		}
		missing = true
		return map[string]any{}
	})
	for _, params := range []map[string]any{
		{"task_id": "todo-2", "workspace_id": "w-task"},  // another task's recorded workspace
		{"task_id": "todo-1", "workspace_id": "w-user"},  // user-opened, even with the old task label
		{"task_id": "todo-2", "workspace_id": "w-other"}, // never created by this task
		{"task_id": "", "workspace_id": "w-task"},        // no hub ownership record
	} {
		if _, err := request(t, n, "workspace.close", params); err == nil {
			t.Errorf("closed without matching task ownership: %v", params)
		}
	}
	if got := f.trace("workspace.close"); len(got) != 0 {
		t.Fatalf("an ownership mismatch reached herdr: %v", got)
	}

	closeTask := func() {
		t.Helper()
		if _, err := request(t, n, "workspace.close", map[string]any{"task_id": "todo-1", "workspace_id": "w-task"}); err != nil {
			t.Fatalf("close the recorded, renamed workspace: %v", err)
		}
	}
	closeTask()
	closeTask() // already gone is an idempotent success, not a reason to close anything else
	for _, call := range f.trace("workspace.close") {
		if !strings.Contains(call, `"workspace_id":"w-task"`) || strings.Contains(call, "w-user") || strings.Contains(call, "w-other") {
			t.Fatalf("unsafe workspace close: %v", call)
		}
	}
	if len(f.trace("workspace.close")) == 0 {
		t.Fatal("the exact recorded workspace was never closed")
	}
}

func TestHerdrTaskWorktreeWithoutSourceWorkspace(t *testing.T) {
	w := worktreeNode(t)
	w.f.reply("workspace.list", map[string]any{"workspaces": []any{}})
	w.f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	dest := filepath.Join(w.home, "checkout-without-source")
	res, err := request(t, w.n, "worktree.create", map[string]any{
		"cwd": w.repo, "branch": "task/no-source", "path": dest, "for_agent": true, "open_workspace": false,
	})
	if err != nil {
		t.Fatalf("checkout without a source workspace failed: %v", err)
	}
	if res["workspace_id"] != nil && res["workspace_id"] != "" || branchOf(t, dest) != "task/no-source" {
		t.Fatalf("checkout opened a workspace or lost its branch: %v", res)
	}
	if got := w.f.trace("workspace.create", "tab.create"); len(got) != 0 {
		t.Fatalf("checkout opened herdr UI: %v", got)
	}
}

func TestHerdrTaskResumeDoesNotAdoptAUserWorkspace(t *testing.T) {
	n, f, _, cwd := startHerdrWorkspaceSession(t, true)
	h := n.hosts.Get(host.Herdr).(*herdrHost)
	ctx, _ := withTaskSpace(context.Background(), "todo-1", "w-task", "Ship the change")
	ctx = withAdoptedPane(ctx, &host.Created{SpaceID: "w-user", SlotID: "w-user:t1", Pane: host.PaneRef{Kind: host.Herdr, ID: "w-user:p1"}}, nil)
	if _, err := n.newTab(ctx, h, cwd, "", "", startID); err != nil {
		t.Fatal(err)
	}
	state, _ := n.state.get(startID)
	if resumed := n.fillResume(startID, resumed{taskID: "todo-1", workspaceID: "w-user"}); resumed.workspaceID != "w-task" {
		t.Fatalf("conflicting resume request replaced the recorded task workspace: %+v", resumed)
	}
	n.putAgentState(ctx, h, startID, state)
	state, _ = n.state.get(startID)
	if state.TaskID != "todo-1" || state.WorkspaceID != "w-task" {
		t.Fatalf("adopted shell transferred task ownership to a user workspace: %+v", state)
	}
	if _, err := n.closeTaskSpace(ctx, "todo-1", "w-user"); err == nil {
		t.Fatal("closed the user workspace adopted by the shell")
	}
	for _, call := range f.trace("workspace.close") {
		if strings.Contains(call, "w-user") {
			t.Fatalf("user workspace reached herdr close: %v", call)
		}
	}
}

func TestHerdrTaskCloseUsesFreshHerdrSnapshot(t *testing.T) {
	n, f, _, cwd := startHerdrWorkspaceSession(t, true)
	h := n.hosts.Get(host.Herdr).(*herdrHost)
	n.hosts = host.NewRegistry(h, &launchHost{setHost: newSetHost(host.Orca)}) // an unrelated host that cannot refresh
	workspace := map[string]any{"workspace_id": "w-task", "label": "Ship the change", "cwd": cwd}
	f.reply("session.snapshot", snapshot([]any{workspace}, nil, []any{claudeAgent(startID, proto.StatusWorking, "w-task:p1", "w-task:t1", "w-task", cwd, "working")}, nil))
	if _, err := n.closeTaskSpace(context.Background(), "todo-1", "w-task"); err == nil {
		t.Fatal("closed while the fresh herdr snapshot had a live Agent")
	}
	if got := f.trace("workspace.close"); len(got) != 0 {
		t.Fatalf("live workspace reached herdr close: %v", got)
	}
	f.reply("session.snapshot", snapshot([]any{workspace}, nil, nil, nil))
	res, err := n.closeTaskSpace(context.Background(), "todo-1", "w-task")
	if err != nil || res["closed"] != true {
		t.Fatalf("close with no live Agent and an unrelated failed host = %v, %v", res, err)
	}
}
