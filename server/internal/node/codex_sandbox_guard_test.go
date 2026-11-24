package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexWorkspaceWriteRefusesProtectedSymlinks(t *testing.T) {
	for _, name := range []string{".aws", ".codex", ".agents", ".git"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			link := filepath.Join(home, name)
			// A missing external target must still be rejected without dereferencing it.
			if err := os.Symlink(filepath.Join(t.TempDir(), "external"), link); err != nil {
				t.Fatal(err)
			}
			err := guardCodexWritableSymlinks(context.Background(), []string{"codex", "--sandbox", "workspace-write"}, nil, home, home)
			if rpcCode(err) != "codex_writable_symlink" {
				t.Fatalf("expected workspace-write symlink refusal, got %v", err)
			}
			if target, err := os.Readlink(link); err != nil || target == "" {
				t.Fatalf("the user's link was changed: target=%q err=%v", target, err)
			}
		})
	}
}

func TestCodexWorkspaceWriteGuardUsesResolvedModeAndRoots(t *testing.T) {
	home := t.TempDir()
	configHome := t.TempDir()
	writeFile(t, filepath.Join(configHome, "config.toml"), "sandbox_mode = \"workspace-write\"\n[profiles.safe]\nsandbox_mode = \"read-only\"\n")
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(home, ".aws")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(project, ".aws"), 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(project, ".git"), "gitdir: ../common")
	env := []string{"CODEX_HOME=" + configHome}
	for _, c := range []struct {
		name    string
		args    []string
		cwd     string
		refused bool
	}{
		{"configured default at home", []string{"codex"}, home, true},
		{"explicit config", []string{"codex", "-c", `sandbox_mode="workspace-write"`}, home, true},
		{"full auto", []string{"codex", "--full-auto"}, home, true},
		{"read only", []string{"codex", "--sandbox=read-only"}, home, false},
		{"profile read only", []string{"codex", "-p", "safe"}, home, false},
		{"unrestricted", []string{"codex", "--yolo"}, home, false},
		{"project cwd", []string{"codex"}, project, false},
		{"extra writable root", []string{"codex", "--add-dir", home}, project, true},
		{"cd changes writable root", []string{"codex", "-C", home}, project, true},
		{"prompt is not flags", []string{"codex", "--", "--add-dir", home}, project, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := guardCodexWritableSymlinks(context.Background(), c.args, env, c.cwd, home)
			if (rpcCode(err) == "codex_writable_symlink") != c.refused {
				t.Fatalf("refused=%v, error=%v", c.refused, err)
			}
		})
	}
}

func TestCodexSymlinkRefusalPrecedesOpeningTheAgent(t *testing.T) {
	home := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(home, ".aws")); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: home, dir: t.TempDir()}
	// A nil host would panic if startup reached a terminal operation.
	_, err := n.startCodex(context.Background(), nil, home, "codex", []string{"codex", "--sandbox", "workspace-write"}, modelExecution{}, "Inspect the source", "", "", "default")
	if rpcCode(err) != "codex_writable_symlink" {
		t.Fatalf("got %v", err)
	}
}

func TestCodexGuardChecksConfiguredWritableRoots(t *testing.T) {
	home, configHome, project := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(home, ".aws")); err != nil {
		t.Fatal(err)
	}
	body := "sandbox_mode = \"workspace-write\"\n[sandbox_workspace_write]\nwritable_roots = [\n# additional working tree\n'" + home + "',\n]\n"
	writeFile(t, filepath.Join(configHome, "config.toml"), body)
	env := []string{"CODEX_HOME=" + configHome}
	if err := guardCodexWritableSymlinks(context.Background(), []string{"codex"}, env, project, home); rpcCode(err) != "codex_writable_symlink" {
		t.Fatalf("configured root: %v", err)
	}
	if err := guardCodexWritableSymlinks(context.Background(), []string{"codex", "-c", "sandbox_workspace_write.writable_roots=[]"}, env, project, home); err != nil {
		t.Fatalf("empty CLI roots override the configured roots: %v", err)
	}
	writeFile(t, filepath.Join(configHome, "config.toml"), `sandbox_mode = "workspace-write"`)
	args := []string{"codex", "-c", "sandbox_workspace_write.writable_roots=['" + home + "']"}
	if err := guardCodexWritableSymlinks(context.Background(), args, env, project, home); rpcCode(err) != "codex_writable_symlink" {
		t.Fatalf("config override root: %v", err)
	}
	writeFile(t, filepath.Join(project, ".codex", "config.toml"), `sandbox_mode = "read-only"`)
	if err := guardCodexWritableSymlinks(context.Background(), []string{"codex", "--add-dir", home}, env, project, home); err != nil {
		t.Fatalf("project's read-only mode should allow the link: %v", err)
	}
}
