package assistant

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/proto"
)

// agy workers run in Allow all, as Claude Code's do: in a terminal pane nobody answers agy's own approval prompt, and
// the node starts it with --dangerously-skip-permissions (node/workagent.go). A mode the plan names stays.
func TestPlannedAgyTaskAllowsAll(t *testing.T) {
	for _, tc := range []struct{ name, permission, want string }{
		{"unset", "", "bypassPermissions"},
		{"named", "acceptEdits", "acceptEdits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f := readonlyTaskService(t)
				f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}, {Name: "agy", Agent: "agy"}}
				setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = "auto" })
				task := PlanTask{Title: "Fix login", Prompt: "Fix the login flow and add tests.", Machine: "box", Cwd: "/code/app", Launcher: "agy", Permission: tc.permission}
				if _, err := s.toolTaskPlan(context.Background(), "agy-chat", readonlyTaskArgs(t, task)); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				p := readonlyFeedPlan(t, s, "agy-chat")
				if p.Tasks[0].Permission != tc.want {
					t.Fatalf("card = %+v, want permission %s", p.Tasks[0], tc.want)
				}
				if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				f.mu.Lock()
				defer f.mu.Unlock()
				if len(f.starts) != 1 || f.starts[0]["permission_mode"] != tc.want {
					t.Fatalf("start = %v, want permission_mode %s", f.starts, tc.want)
				}
			})
		})
	}
}
