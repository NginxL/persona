package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bare "~" is the machine's home, like "~/x" is x under it; the hub may not know the home yet and leave it to us.
func TestCheckStartExpandsHome(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	abs := t.TempDir()
	n := &Node{home: home}
	for in, want := range map[string]string{
		"~":     home,
		"~/":    home,
		"~/src": filepath.Join(home, "src"),
		abs:     abs,
		"":      home,
	} {
		got, launcher, err := n.checkStart(in, "", "", "", "", "")
		if err != nil {
			t.Fatalf("checkStart(%q): %v", in, err)
		}
		if got != want || launcher != "claude" {
			t.Errorf("checkStart(%q) = %q %q, want %q claude", in, got, launcher, want)
		}
	}
	if _, _, err := n.checkStart("~/missing", "", "", "", "", ""); err == nil {
		t.Error("checkStart accepted a directory that does not exist")
	}
}

// A rejected directory has no pane, persistent owner record or inventory row, including for the Web asynchronous route.
func TestMissingDirectoryStartLeavesNoSession(t *testing.T) {
	for name, async := range map[string]bool{"sync": false, "async": true} {
		t.Run(name, func(t *testing.T) {
			n, host, home := herdrNode(t)
			missing := filepath.Join(home, "missing-project")
			out, err := request(t, n, "session.start", map[string]any{
				"session_id": startID, "cwd": missing, "async": async,
			})
			if err == nil || !strings.Contains(err.Error(), "does not exist") || out["session"] != nil {
				t.Fatalf("start: %v, %v", out, err)
			}
			if _, recorded := n.state.get(startID); recorded || n.starts.get(startID) != nil {
				t.Fatal("refused session recorded as started")
			}
			if len(host.trace("tab.create", "workspace.create")) != 0 {
				t.Fatal("refused start created a terminal")
			}
			if rows := listSessions(t, n); len(rows) != 0 {
				t.Fatalf("inventory after refused start: %+v", rows)
			}
		})
	}
}
