package assistant

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/proto"
)

// A computer whose home is a link (/home/me -> /data/home/me). Its node names the folder an Agent starts in by the
// home's own spelling when asked for a terminal (#10365); an Agent started before that, or by an older node, has the
// resolved one. Both are one folder, so the to-do writing there is found under either spelling.
func linkedHomeTaskService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f := workspaceTaskService(t)
	f.devices = []Device{{ID: "box", Name: "box", Home: "/home/me", HomeReal: "/data/home/me", Online: true, Reachable: true}}
	f.nodes["worktree.status"] = func(string, json.RawMessage) (any, error) {
		return proto.WorktreeStatus{Path: "/data/home/me/code/app", Branch: "main"}, nil
	}
	f.nodes["workspace.resolve"] = func(_ string, raw json.RawMessage) (any, error) {
		var in struct {
			Path     string
			Terminal bool
		}
		_ = json.Unmarshal(raw, &in)
		real := path.Clean(in.Path)
		if rest, ok := strings.CutPrefix(real, "/home/me/"); ok {
			real = "/data/home/me/" + rest
		}
		cwd := real
		if rest, ok := strings.CutPrefix(real, "/data/home/me/"); ok && in.Terminal {
			cwd = "/home/me/" + rest
		}
		return proto.Workspace{ID: real, Cwd: cwd, Available: true}, nil
	}
	return s, f
}

// physicalWriter is an open to-do whose Agent was started in /data/home/me/code/app before the node spelled it
// through the home.
func physicalWriter(t *testing.T, s *Service) Todo {
	t.Helper()
	ctx := context.Background()
	owner, err := s.createTodo(ctx, "Earlier writer", "", "other-chat", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, owner.ID, "box", "writer", owner.Title, "/data/home/me/code/app", ""); err != nil {
		t.Fatal(err)
	}
	return owner
}

func TestTaskWorkspaceOwnerFoldsLinkedHomeSpellings(t *testing.T) {
	s, _ := linkedHomeTaskService(t)
	ctx := context.Background()
	owner := physicalWriter(t, s)
	for _, cwd := range []string{"/home/me/code/app", "/home/me/code/app/", "/data/home/me/code/app"} {
		if got := s.taskWorkspaceOwner(ctx, "box", cwd, "", false); got == nil || got.ID != owner.ID {
			t.Errorf("%s: owner = %v, want %s", cwd, got, owner.ID)
		}
	}
	if got := s.taskWorkspaceOwner(ctx, "box", "/home/me/code", "", true); got == nil || got.ID != owner.ID {
		t.Errorf("a checkout root above the writer under the other spelling: owner = %v", got)
	}
	if got := s.taskWorkspaceOwner(ctx, "box", "/home/me/code/other", "", false); got != nil {
		t.Errorf("another folder is busy: %v", got)
	}
	// A claim made under the home's spelling (admission before the Agent is linked) holds the resolved one too.
	s.tasks.mu.Lock()
	s.tasks.workspaceClaims = map[string]string{"box\x00" + s.taskDirKey(ctx, "box")("/home/me/code/claimed"): owner.ID}
	s.tasks.mu.Unlock()
	if got := s.taskWorkspaceOwner(ctx, "box", "/data/home/me/code/claimed", "", false); got == nil || got.ID != owner.ID {
		t.Errorf("claim under the home's spelling not found from the resolved one: %v", got)
	}
}

// {existing} on the folder an earlier to-do still writes in, asked for through the home: refused with that to-do,
// not started beside it.
func TestTaskWorkspaceExistingBusyUnderTheOtherHomeSpelling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := linkedHomeTaskService(t)
		ctx := context.Background()
		owner := physicalWriter(t, s)
		p := workspacePlan(t, s, `{"existing":"~/code/app"}`)
		if got := p.Tasks[0].WorkspaceResolved; got == nil || got.Path != "/home/me/code/app" {
			t.Fatalf("preview = %+v, want the terminal's /home/me/code/app", got)
		}
		_, err := s.startPlan(ctx, p.ID, nil, false)
		synctest.Wait()
		saved, _ := s.loadPlan(ctx, p.ID)
		reason := saved.Tasks[0].Error
		if err != nil {
			reason = err.Error()
		}
		if len(f.starts) != 0 || !strings.Contains(reason, owner.ID) {
			t.Fatalf("busy folder not refused: err=%v plan=%+v starts=%v", err, saved, f.starts)
		}
	})
}

// A task planned in that folder without a workspace gets its own checkout, as it does when the spellings agree.
func TestTaskWorkspaceInferredBusyUnderTheOtherHomeSpelling(t *testing.T) {
	s, _ := linkedHomeTaskService(t)
	physicalWriter(t, s)
	task := PlanTask{Machine: "box", Cwd: "/home/me/code/app"}
	if err := s.resolveTaskWorkspace(context.Background(), &task); err != nil {
		t.Fatal(err)
	}
	if task.Workspace == nil || task.Workspace.Mode != workspaceNew {
		t.Fatalf("workspace = %+v, want a new checkout beside the busy folder", task.Workspace)
	}
}

func TestHomeDirKey(t *testing.T) {
	for _, c := range []struct{ dir, home, real, want string }{
		{"/data/home/me/src", "/home/me", "/data/home/me", "/home/me/src"},
		{"/data/home/me/", "/home/me", "/data/home/me", "/home/me"},
		{"/home/me/src/", "/home/me", "/data/home/me", "/home/me/src"},
		{"/data/home/meow/src", "/home/me", "/data/home/me", "/data/home/meow/src"},
		{"/data/home/me/src", "/home/me", "", "/data/home/me/src"},
		{"/data/home/me/src", "", "/data/home/me", "/data/home/me/src"},
		{`d:/profiles/me/trees/x`, `C:\Users\me`, `D:\Profiles\me`, `C:\USERS\ME\TREES\X`},
		{`C:\Users\me\trees\x\`, `C:\Users\me`, `D:\Profiles\me`, `C:\USERS\ME\TREES\X`},
		{`D:\Profiles\me\x`, "/home/me", `D:\Profiles\me`, `D:\PROFILES\ME\X`},
	} {
		if got := homeDirKey(c.dir, c.home, c.real); got != c.want {
			t.Errorf("homeDirKey(%q, %q, %q) = %q, want %q", c.dir, c.home, c.real, got, c.want)
		}
	}
}
