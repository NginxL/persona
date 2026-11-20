package workspacenaming

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repository"
)

func TestWorkspaceNamingSharedFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "spec", "fixtures", "workspace-naming", "contract.json")
	if os.Getenv("UPDATE_WORKSPACE_NAMING_FIXTURES") == "1" {
		s := source()
		s.Rules.TaskDirectoryTemplate = "{yyyy}{mmdd}/{task-id}-{task-slug}"
		row := s.Repositories.Repositories[0]
		row.SourceSnapshot.CloneURL = row.SourceSnapshot.Identity.CanonicalURL
		row.SourceSnapshot.TransportOrigin = "canonical_default"
		row.SourceSnapshot.CatalogRevision = 1
		s.Repositories, _ = repository.Snapshot(s.Manager, s.TodoID, 1, []repository.TaskRepository{{RepoID: row.RepoID, RequestedRef: row.RequestedRef, SourceSnapshot: row.SourceSnapshot}}, []string{row.RepoID})
		p := Resolve(s, NodeContext{Home: "/home/example", PersonaHome: "/home/example/.persona"})
		if p.Plan == nil {
			t.Fatal(p.Diagnostics)
		}
		raw, err := json.MarshalIndent(p.Plan, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, append(raw, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want Plan
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got := Resolve(want.Source, want.Node)
	if got.Plan == nil || !reflect.DeepEqual(*got.Plan, want) {
		t.Fatal("shared naming contract changed", got)
	}
}
