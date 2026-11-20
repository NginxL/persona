package node

import (
	"path/filepath"
	"testing"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestBackendWorkspaceProviderSourcesPrepareWithoutProviderCalls(t *testing.T) {
	for _, layout := range []repository.Layout{repository.LayoutRoot, repository.LayoutContainer} {
		t.Run(string(layout), func(t *testing.T) {
			home := workspaceTemp(t)
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			sources := []string{workspaceRemote(t, "trunk")}
			if layout == repository.LayoutContainer {
				sources = append(sources, workspaceRemote(t, "develop"))
			}
			plan := workspacePlan(t, home, layout, sources...)
			for i := range plan.Source.Repositories.Repositories {
				row := &plan.Source.Repositories.Repositories[i]
				row.SourceSnapshot.Identity = repository.Identity{Provider: "ext:example/source", InstanceURL: "https://code.example.com", Path: "example/" + row.RepoID, CanonicalURL: "https://code.example.com/example/" + row.RepoID}
				row.SourceSnapshot.ProviderName = "Example source"
				row.SourceSnapshot.TransportOrigin = "provider"
			}
			// Only the isolated Git transport is substituted; preparation still fetches and creates real worktrees.
			preview := workspacenaming.Resolve(plan.Source, plan.Node)
			if preview.Plan == nil {
				t.Fatal(preview)
			}
			req := taskworkspace.Request{WorkspaceID: "ws_provider", OperationID: "op_provider", Plan: *preview.Plan}
			result := workspaceCall(t, n, "prepare", req)
			if result.State != "ready" || len(result.Repositories) != len(sources) {
				t.Fatal(result)
			}
			for _, row := range result.Repositories {
				if row.Input.SourceSnapshot.ProviderName != "Example source" || row.Input.SourceSnapshot.Identity.Provider != "ext:example/source" || row.ResolvedCommit == "" {
					t.Fatal(row)
				}
			}
			removed := workspaceCall(t, n, "cleanup", req)
			if removed.Cleanup == nil || !removed.Cleanup.Removed {
				t.Fatal(removed)
			}
			for _, row := range removed.Repositories {
				if row.BranchDeleted || workspaceGit(t, row.CommonDir, "rev-parse", "refs/heads/"+row.TaskBranch) != row.ResolvedCommit+"\n" {
					t.Fatal("default cleanup lost the provider repository's branch", row)
				}
			}
		})
	}
}

func TestBackendWorkspaceProviderIdentityIsRechecked(t *testing.T) {
	valid := repository.SourceSnapshot{Identity: repository.Identity{Provider: "ext:example/source", InstanceURL: "https://code.example.com", Path: "example/web", CanonicalURL: "https://code.example.com/example/web"}, CloneURL: "https://code.example.com/example/web.git", ProviderName: "Example source"}
	for _, tc := range []struct {
		name   string
		change func(*repository.SourceSnapshot)
		want   bool
	}{
		{"valid", func(*repository.SourceSnapshot) {}, true},
		{"canonical mismatch", func(s *repository.SourceSnapshot) { s.Identity.CanonicalURL += "-other" }, false},
		{"path traversal", func(s *repository.SourceSnapshot) {
			s.Identity.Path = "../web"
			s.Identity.CanonicalURL = s.Identity.InstanceURL + "/" + s.Identity.Path
		}, false},
		{"instance credentials", func(s *repository.SourceSnapshot) {
			s.Identity.InstanceURL = "https://example:example@code.example.com"
			s.Identity.CanonicalURL = s.Identity.InstanceURL + "/" + s.Identity.Path
		}, false},
		{"noncanonical port", func(s *repository.SourceSnapshot) {
			s.Identity.InstanceURL = "https://code.example.com:443"
			s.Identity.CanonicalURL = s.Identity.InstanceURL + "/" + s.Identity.Path
		}, false},
		{"clone credentials", func(s *repository.SourceSnapshot) {
			s.CloneURL = "https://example:example@code.example.com/example/web.git"
		}, false},
		{"empty host", func(s *repository.SourceSnapshot) {
			s.Identity.InstanceURL = "https://"
			s.Identity.CanonicalURL = s.Identity.InstanceURL + "/" + s.Identity.Path
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid
			tc.change(&s)
			if got := (&Node{}).workspaceSourceAllowed(repository.PreparationRepository{SourceSnapshot: s}); got != tc.want {
				t.Fatalf("source allowed=%v, want %v", got, tc.want)
			}
		})
	}
}
