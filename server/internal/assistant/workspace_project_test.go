package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/workspaceboard"
)

func TestWorkspaceProjectCreateRoutesOnlyExplicitRequests(t *testing.T) {
	d := newBoardDevs()
	s, _ := newBoardService(t, newMemoryBoardStore(), d)
	var requests []map[string]any
	d.add("computer", func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		if method != "workspace.create" {
			t.Fatalf("unexpected method %s", method)
		}
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, p)
		if len(requests) == 1 {
			return nil, errors.New("connection interrupted")
		}
		return proto.Workspace{ID: "native-project", Source: "codex_app", SourceID: "native-project", Cwd: `C:\Projects\Example`, Title: "Example", Available: true}, nil
	})
	body := `{"revision":0,"action":"add_card","machine_id":"computer","cwd":"C:\\Projects\\Example","title":"Example","column_id":"todo","create_directory":true,"desktop_app":"codex_app"}`
	if w := boardCall(s, "POST", "/api/v1/workspace-board", body, roleFull); w.Code != 400 {
		t.Fatalf("failed create: %d %s", w.Code, w.Body)
	}
	st := boardState(t, boardCall(s, "POST", "/api/v1/workspace-board", body, roleFull))
	if len(st.Cards) != 1 || st.Cards[0].Source != "codex_app" || st.Cards[0].SourceID != "native-project" {
		t.Fatalf("project binding lost: %+v", st)
	}
	if len(requests) != 2 || requests[0]["idempotency_key"] == "" || requests[0]["idempotency_key"] != requests[1]["idempotency_key"] {
		t.Fatalf("retry lost project identity: %v", requests)
	}
	if requests[1]["create_directory"] != true || requests[1]["desktop_app"] != "codex_app" || requests[1]["name"] != "Example" {
		t.Fatalf("creation options lost: %v", requests[1])
	}
}

func TestWorkspaceProjectConcurrentCreationChecksRevisionBeforeNativeCall(t *testing.T) {
	d := newBoardDevs()
	s, _ := newBoardService(t, newMemoryBoardStore(), d)
	var calls atomic.Int32
	entered, release := make(chan struct{}, 12), make(chan struct{})
	d.add("computer", func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method != "workspace.create" {
			t.Errorf("unexpected method %s", method)
		}
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return proto.Workspace{ID: "native", Source: "codex_app", SourceID: "native", Cwd: `C:\Projects\Example`, Available: true}, nil
	})
	start, results := make(chan struct{}), make(chan int, 12)
	var ready sync.WaitGroup
	ready.Add(12)
	for i := range 12 {
		go func() {
			<-start
			ready.Done()
			ready.Wait()
			body := fmt.Sprintf(`{"revision":0,"action":"add_card","machine_id":"computer","cwd":"C:\\Projects\\Example","title":"Project %d","column_id":"todo","create_directory":true,"desktop_app":"codex_app"}`, i)
			results <- boardCall(s, "POST", "/api/v1/workspace-board", body, roleFull).Code
		}()
	}
	close(start)
	ready.Wait()
	<-entered
	close(release)
	succeeded := 0
	for range 12 {
		switch code := <-results; code {
		case 200:
			succeeded++
		case 409:
		default:
			t.Errorf("unexpected creation response %d", code)
		}
	}
	if succeeded != 1 || calls.Load() != 1 {
		t.Fatalf("concurrent creation: %d successful, %d native calls", succeeded, calls.Load())
	}
}

func TestWorkspaceProjectCanonicalSnapshotWinsClockTie(t *testing.T) {
	store, d := newMemoryBoardStore(), newBoardDevs()
	s, _ := newBoardService(t, store, d)
	d.add("computer", nil)
	d.absorbed["alias"] = "computer"
	now := time.Now().UTC()
	store.snapshots["alias"] = db.WorkspaceSnapshotRow{MachineID: "alias", UpdatedAt: now, Data: json.RawMessage(`[{"id":"stale","source":"directory","source_id":"/old","cwd":"/old"}]`)}
	store.snapshots["computer"] = db.WorkspaceSnapshotRow{MachineID: "computer", UpdatedAt: now, Data: json.RawMessage(`[]`)}
	for range 10 {
		w := boardCall(s, "GET", "/api/v1/workspaces", "", roleFull)
		var list proto.WorkspacesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Workspaces) != 0 {
			t.Fatalf("clock tie restored an old alias catalog: %s, %v", w.Body, err)
		}
	}
}

func TestWorkspaceProjectPassesOnlySameComputerBindings(t *testing.T) {
	d := newBoardDevs()
	store := newMemoryBoardStore()
	store.state.Cards = []workspaceboard.Card{
		{ID: "existing", MachineID: "old-computer", Cwd: `C:\Projects\Example`, Title: "Old name", ColumnID: "todo"},
		{ID: "other", MachineID: "other-computer", Cwd: `C:\Projects\Other`, Title: "Other", ColumnID: "todo"},
	}
	d.absorbed["old-computer"] = "computer"
	s, _ := newBoardService(t, store, d)
	d.add("computer", func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		var p struct {
			BoundDirectories []string `json:"bound_directories"`
		}
		if method != "workspace.create" || json.Unmarshal(raw, &p) != nil || len(p.BoundDirectories) != 1 || p.BoundDirectories[0] != `C:\Projects\Example` {
			t.Fatalf("node must check canonical bindings before creation: %s %s", method, raw)
		}
		return nil, errors.New("project directory is already bound")
	})
	body := `{"revision":0,"action":"add_card","machine_id":"computer","cwd":"c:\\projects\\example","title":"New name","column_id":"todo","create_directory":true,"desktop_app":"codex_app"}`
	if w := boardCall(s, "POST", "/api/v1/workspace-board", body, roleFull); w.Code != 400 {
		t.Fatalf("duplicate create: %d %s", w.Code, w.Body)
	}
}

func TestWorkspaceProjectNativeRootsHaveDistinctStableIdentities(t *testing.T) {
	for _, source := range []string{"codex_app", proto.SourceClaudeDesktop} {
		t.Run(source, func(t *testing.T) {
			first := proto.Workspace{Source: source, SourceID: source + ":profile:one", Cwd: `C:\Projects\One`}
			second := first
			second.Cwd = `C:\Projects\Two`
			a, b := qualifyWorkspace("computer", first), qualifyWorkspace("computer", second)
			if a.ID == b.ID || a.SourceID != b.SourceID || qualifyWorkspace("computer", a).ID != a.ID {
				t.Fatalf("native multi-root project identities: %+v %+v", a, b)
			}
			second.Cwd, second.SourceID = first.Cwd, source+":profile:two"
			if qualifyWorkspace("computer", second).ID == a.ID {
				t.Fatal("projects sharing a directory collapsed")
			}
		})
	}
}

func TestWorkspaceProjectInvalidOrReadonlyRequestsNeverReachNode(t *testing.T) {
	d := newBoardDevs()
	s, _ := newBoardService(t, newMemoryBoardStore(), d)
	d.add("computer", func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("invalid creation reached node")
		return nil, nil
	})
	for _, body := range []string{
		`{"revision":0,"action":"add_card","machine_id":"computer","cwd":"/projects/example","title":"","column_id":"todo","create_directory":true}`,
		`{"revision":0,"action":"add_card","machine_id":"computer","cwd":"/projects/example","title":"Example","column_id":"missing","create_directory":true}`,
		`{"revision":0,"action":"add_card","machine_id":"computer","cwd":"/projects/example","title":"Example","column_id":"todo","desktop_app":"unknown"}`,
		`{"revision":0,"action":"add_column","title":"Example","create_directory":true}`,
	} {
		if w := boardCall(s, "POST", "/api/v1/workspace-board", body, roleFull); w.Code != 400 {
			t.Fatalf("accepted %s: %d", body, w.Code)
		}
	}
	if w := boardCall(s, "POST", "/api/v1/workspace-board", `{"revision":0,"action":"add_card","machine_id":"computer","cwd":"/projects/example","title":"Example","column_id":"todo","create_directory":true}`, roleAnswer); w.Code != 403 {
		t.Fatalf("read-only creation: %d", w.Code)
	}
}
