package node

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestBackendWorkspaceOutOfOrderTerminalRequest(t *testing.T) {
	for _, first := range []string{"cancel then adopt", "cancel then cleanup", "cleanup"} {
		t.Run(first, func(t *testing.T) {
			home := workspaceTemp(t)
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			a := taskworkspace.Request{WorkspaceID: "ws_before", OperationID: "op_a", Plan: workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "main"))}
			method, want := "cancel", "cancelled"
			if first == "cleanup" {
				method, want = "cleanup", "removed"
			}

			settled := workspaceCall(t, n, method, a)
			if settled.Error != nil || settled.State != want || settled.NodeOperationID != a.OperationID || settled.InputDigest != taskworkspace.InputKey(a.Plan) {
				t.Fatal(settled)
			}
			n = &Node{home: home, dir: n.dir}
			for _, action := range []string{"inspect", "prepare", method} {
				result := workspaceCall(t, n, action, a)
				if result.Error != nil || result.State != want || len(result.Repositories) != 0 {
					t.Fatal(action, result)
				}
			}
			if _, err := os.Lstat(a.Plan.RootPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("late prepare created a checkout", err)
			}

			b := a
			b.OperationID = "op_b"
			if first == "cancel then adopt" {
				b.PreviousOperationID = a.OperationID
			} else {
				result := workspaceCall(t, n, "cleanup", a)
				if result.Error != nil || result.State != "removed" || result.Cleanup == nil || !result.Cleanup.Removed {
					t.Fatal(result)
				}
				b.WorkspaceID = "ws_after_terminal"
			}
			target := n
			if first == "cancel then cleanup" {
				otherHome := workspaceTemp(t)
				target = &Node{home: otherHome, dir: filepath.Join(otherHome, ".persona")}
				source := a.Plan.Source
				source.MachineID = "other-computer"
				b.Plan = *workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: target.home, PersonaHome: target.dir}).Plan
			}
			ready := workspaceCall(t, target, "prepare", b)
			if ready.State != "ready" || ready.Error != nil {
				t.Fatal(ready)
			}
			late := workspaceCall(t, n, "prepare", a)
			if late.State != "removed" && late.State != "cancelled" {
				t.Fatal("old prepare revived", late)
			}
			current := workspaceCall(t, target, "inspect", b)
			if current.State != "ready" || current.Error != nil || current.Repositories[0].CurrentHead != ready.Repositories[0].CurrentHead {
				t.Fatal("current operation changed", current)
			}
		})
	}
}

func TestBackendWorkspaceMissingReceiptWithPhysicalRemains(t *testing.T) {
	for _, existing := range []string{"checkout", "private Git", "unexpected control file", "nonempty hooks"} {
		t.Run(existing, func(t *testing.T) {
			home := workspaceTemp(t)
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			req := taskworkspace.Request{WorkspaceID: "ws_ambiguous", OperationID: "op_ambiguous", Plan: workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "main"))}
			storage := filepath.Join(n.dir, "task-workspaces", taskworkspace.Hash(req.WorkspaceID))
			path := map[string]string{
				"checkout": req.Plan.RootPath, "private Git": filepath.Join(storage, "git"),
				"unexpected control file": storage, "nonempty hooks": filepath.Join(storage, "hooks"),
			}[existing]
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(path, "existing-data")
			if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{"cancel", "cleanup"} {
				r := workspaceCall(t, n, method, req)
				if r.Error == nil || r.Error.Code != "operation_unknown" || r.State == "removed" || r.State == "cancelled" {
					t.Fatal(method, r)
				}
			}
			if _, err := os.Lstat(filepath.Join(storage, "receipt.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invented ownership", err)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
				t.Fatal("existing resource changed", string(data), err)
			}
		})
	}
}
