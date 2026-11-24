package assistant

import (
	"context"
	"github.com/nwverse/persona/server/internal/compat"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A manager other than the built-in one keeps its assistant and health directories of its own on a computer
// (.persona/assistant-<id>, .persona/health-<id>); the built-in one's are as they were. Another manager's directory,
// left on the computer when it moved, is not its conversations'.
func TestAManagersOwnDirectories(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		id, assistant, health string
	}{
		{"", ".persona/assistant", ".persona/health"},
		{"m-0b0b0b", ".persona/assistant-m-0b0b0b", ".persona/health-m-0b0b0b"},
	} {
		f := healthHub()
		s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return &memStore{} }, Logf: testLogf(t), ID: c.id}))
		for _, space := range []string{"", SpaceHealth} {
			if _, err := s.prepareSpace(ctx, "server", space); err != nil {
				t.Fatal(err)
			}
		}
		if len(f.prepared) != 2 || f.prepared[0]["dir"] != compat.StateDir+"/"+c.assistant[len(".persona/"):] || f.prepared[1]["dir"] != compat.StateDir+"/"+c.health[len(".persona/"):] {
			t.Fatalf("%q prepared %v", c.id, f.prepared)
		}
		// Its health CLAUDE.md names its own space, where its reports are.
		if md, _ := f.prepared[1]["claude_md"].(string); !strings.Contains(md, "/"+c.health[len(".persona/"):]+"/reports/") {
			t.Fatalf("%q: the health CLAUDE.md does not name its space: %s", c.id, md)
		}
		d := Device{ID: "server", Home: "/home/me"}
		if got := s.assistantCwd(d); got != "/home/me/"+c.assistant {
			t.Fatalf("%q: assistant directory %q", c.id, got)
		}
		d.DataDir = "/data"
		if got := s.spaceCwd(context.Background(), d, SpaceHealth); got != "/data/"+c.health[len(".persona/"):] {
			t.Fatalf("%q: health directory under a data directory %q", c.id, got)
		}
	}
	builtin := fleetMachine{ID: "server", Home: "/home/me", manager: ""}
	other := fleetMachine{ID: "server", Home: "/home/me", manager: "m-0b0b0b"}
	for cwd, want := range map[string][2]bool{
		"/home/me/.persona/assistant":          {true, false},
		"/home/me/.persona/health":             {true, false},
		"/home/me/.persona/assistant-m-0b0b0b": {false, true},
		"/home/me/.persona/health-m-0b0b0b":    {false, true},
		"/home/me/code/app":                    {false, false},
	} {
		sess := proto.Session{SessionID: "s", Cwd: cwd}
		if got := [2]bool{builtin.isAssistant(sess), other.isAssistant(sess)}; got != want {
			t.Errorf("%s: the built-in's %v, m-0b0b0b's %v, want %v", cwd, got[0], got[1], want)
		}
	}
}
