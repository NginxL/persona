package workspacenaming

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repository"
)

// Preparation must keep a saved provider's source even when its extension is no longer available.
func TestWorkspaceNamingProviderSnapshotPersists(t *testing.T) {
	stored := repository.SourceSnapshot{
		Identity: repository.Identity{Provider: "ext:acme-code/acme", InstanceURL: "https://code.acme.test", Path: "team/app", CanonicalURL: "https://code.acme.test/team/app"},
		CloneURL: "git@ssh.acme.test:team/app.git", TransportOrigin: "provider", ProviderName: "Acme Code",
	}
	for _, layout := range []repository.Layout{repository.LayoutRoot, repository.LayoutContainer} {
		for _, ref := range []repository.RequestedRef{{Kind: "default"}, {Kind: "branch", Value: "feature/login"}, {Kind: "tag", Value: "v1"}, {Kind: "ref", Value: "refs/merge-requests/12/head"}} {
			t.Run(string(layout)+"/"+ref.Kind, func(t *testing.T) {
				s := source()
				s.Layout = layout
				var err error
				s.Repositories, err = repository.Snapshot(s.Manager, s.TodoID, 1, []repository.TaskRepository{{RepoID: "web", RequestedRef: ref, SourceSnapshot: stored}}, []string{"web"})
				if err != nil {
					t.Fatal(err)
				}
				preview := Resolve(s, NodeContext{Home: "/home/example"})
				if preview.Plan == nil {
					t.Fatal(preview.Diagnostics)
				}
				raw, err := json.Marshal(preview.Plan)
				if err != nil {
					t.Fatal(err)
				}
				var saved Plan
				if err := json.Unmarshal(raw, &saved); err != nil {
					t.Fatal(err)
				}
				// Later catalog/provider changes cannot rewrite the persisted operation's source.
				s.Repositories.Repositories[0].SourceSnapshot.ProviderName = "Renamed source"
				s.Repositories.Repositories[0].SourceSnapshot.CloneURL = "https://code.acme.test/another/repo"
				if got := saved.Source.Repositories.Repositories[0]; got.SourceSnapshot != stored || got.RequestedRef != ref || saved.Repositories[0].RequestedRef != ref {
					t.Fatalf("provider source or typed ref changed: %+v", saved)
				}
				wantPath := "app"
				if layout == repository.LayoutRoot {
					wantPath = "."
				}
				if saved.Repositories[0].RelativePath != wantPath {
					t.Fatalf("%s layout mapped to %q", layout, saved.Repositories[0].RelativePath)
				}
				if err := CheckPlan(saved); err != nil {
					t.Fatal("persisted plan no longer verifies", err)
				}
			})
		}
	}
}
