package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/proto"
)

// windowsNodeTaskService is a Windows computer: its node reports its directories as C:\ paths. Whatever system the
// manager runs on, a task there must not read them as relative paths ("Workspace C:\… is not an available absolute
// directory", #7106).
func windowsNodeTaskService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f := workspaceTaskService(t)
	f.nodes["worktree.status"] = func(string, json.RawMessage) (any, error) {
		return proto.WorktreeStatus{Path: `C:\code\app`, Branch: "main"}, nil
	}
	f.nodes["workspace.resolve"] = func(_ string, raw json.RawMessage) (any, error) {
		var in struct{ Path string }
		_ = json.Unmarshal(raw, &in)
		return proto.Workspace{Cwd: strings.TrimRight(strings.ReplaceAll(in.Path, "/", `\`), `\`), Available: true}, nil
	}
	f.nodes[DeviceWorktreePrepare] = func(string, json.RawMessage) (any, error) {
		return proto.WorktreePrepare{Root: `C:\code\app`, RepoName: "app", Branch: "main", Path: `C:\wt\app`}, nil
	}
	return s, f
}

func windowsNodePlan(t *testing.T, s *Service, choice string) *TaskPlan {
	t.Helper()
	args := `{"tasks":[{"title":"Fix login","prompt":"Fix login and add tests","machine":"box","cwd":"C:\\code\\app","permission":"bypassPermissions"`
	if choice != "" {
		args += `,"workspace":` + choice
	}
	if _, err := s.toolTaskPlan(context.Background(), "workspace-chat", json.RawMessage(args+`}]}`)); err != nil {
		t.Fatal(err)
	}
	return readonlyFeedPlan(t, s, "workspace-chat")
}

func TestTaskWorkspaceWindowsNodeStartsInItsDirectory(t *testing.T) {
	for _, choice := range []string{"", `{"existing":"C:\\code\\app"}`, `{"existing":"c:/code/app/"}`} {
		t.Run(choice, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f := windowsNodeTaskService(t)
				p := windowsNodePlan(t, s, choice)
				if got := p.Tasks[0].WorkspaceResolved; got == nil || got.Path != `C:\code\app` {
					t.Fatalf("preview = %+v, want C:\\code\\app", got)
				}
				if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				saved, _ := s.loadPlan(context.Background(), p.ID)
				if saved.Tasks[0].Error != "" || len(f.starts) != 1 || f.starts[0]["cwd"] != `C:\code\app` || len(f.worktrees) != 0 {
					t.Fatalf("task did not start in its directory: plan=%+v starts=%v worktrees=%v", saved, f.starts, f.worktrees)
				}
			})
		})
	}
}

func TestTaskWorkspaceWindowsNodeNewCheckout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := windowsNodeTaskService(t)
		p := windowsNodePlan(t, s, `{"new":{"branch":"fix/Login"}}`)
		if got := p.Tasks[0].WorkspaceResolved.Path; got != `C:\wt\app\fix-login` {
			t.Fatalf("preview = %q, want C:\\wt\\app\\fix-login", got)
		}
		if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(f.worktrees) != 1 || f.worktrees[0]["path"] != `C:\wt\app\fix-login` ||
			len(f.starts) != 1 || f.starts[0]["cwd"] != `C:\wt\app\fix-login` {
			t.Fatalf("checkout = %v, starts = %v; want both at C:\\wt\\app\\fix-login", f.worktrees, f.starts)
		}
	})
}

func TestTaskWorkspaceWindowsNodeBusyDirectory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := windowsNodeTaskService(t)
		ctx := context.Background()
		owner, err := s.createTodo(ctx, "Existing writer", "", "other-chat", "")
		if err != nil {
			t.Fatal(err)
		}
		// The agent's row has the directory as its terminal wrote it, in another case: Windows ignores it.
		if err := s.linkTodoAgent(ctx, owner.ID, "box", "writer", owner.Title, `c:\Code\APP\`, ""); err != nil {
			t.Fatal(err)
		}
		p := windowsNodePlan(t, s, `{"existing":"C:\\code\\app"}`)
		_, err = s.startPlan(ctx, p.ID, nil, false)
		synctest.Wait()
		saved, _ := s.loadPlan(ctx, p.ID)
		reason := saved.Tasks[0].Error
		if err != nil {
			reason = err.Error()
		}
		if len(f.starts) != 0 || !strings.Contains(reason, owner.ID) {
			t.Fatalf("busy directory was not refused with its owner: err=%v plan=%+v starts=%v", err, saved, f.starts)
		}
	})
}

// Two writers admitted together claim one Windows directory however each wrote it.
func TestTaskWorkspaceWindowsNodeConcurrentClaimsIgnoreCase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := windowsNodeTaskService(t)
		args := `{"tasks":[
			{"title":"Writer one","prompt":"Write one","machine":"box","cwd":"C:\\code\\app","permission":"bypassPermissions","workspace":{"existing":"C:\\code\\app"}},
			{"title":"Writer two","prompt":"Write two","machine":"box","cwd":"C:\\code\\app","permission":"bypassPermissions","workspace":{"existing":"c:\\Code\\App"}}
		]}`
		if _, err := s.toolTaskPlan(context.Background(), "workspace-chat", json.RawMessage(args)); err != nil {
			t.Fatal(err)
		}
		p := readonlyFeedPlan(t, s, "workspace-chat")
		if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		saved, _ := s.loadPlan(context.Background(), p.ID)
		failures := 0
		for _, task := range saved.Tasks {
			if strings.Contains(task.Error, "occupied by task") {
				failures++
			}
		}
		if failures != 1 || len(f.starts) != 1 {
			t.Fatalf("writers were not serialized: starts=%v plan=%+v", f.starts, saved)
		}
	})
}
