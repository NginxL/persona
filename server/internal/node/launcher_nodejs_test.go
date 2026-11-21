package node

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLauncherNodeDirUsesResolvedNPMPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix npm launcher")
	}
	home := t.TempDir()
	prefix := filepath.Join(home, "node-install")
	script := writeExecutable(t, filepath.Join(prefix, "lib", "node_modules", "fixture", "cli.js"), "#!/usr/bin/env node\n")
	matched := filepath.Dir(writeExecutable(t, filepath.Join(prefix, "bin", "node"), "#!/bin/sh\n"))
	other := filepath.Dir(writeExecutable(t, filepath.Join(home, ".local", "bin", "node"), "#!/bin/sh\n"))
	launcher := filepath.Join(other, "claude")
	if err := os.Symlink(script, launcher); err != nil {
		t.Fatal(err)
	}
	matched, err := filepath.EvalSymlinks(matched)
	if err != nil {
		t.Fatal(err)
	}
	if dir, needsNode := launcherNodeDir(launcher); !needsNode || dir != matched {
		t.Fatalf("launcher needs Node: %t, matching bin: %q, want %q", needsNode, dir, matched)
	}
}
