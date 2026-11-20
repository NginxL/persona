package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func traexIdentityNode(t *testing.T, id string, confirmed ...bool) (*Node, *fakeHerdr, string) {
	t.Helper()
	n, f, home := herdrNode(t)
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("ZDOTDIR", home)
	writeFile(t, filepath.Join(home, ".bashrc"), "\n")
	withCodexLaunchers(t, map[string][]string{})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" {
			if len(confirmed) == 0 || confirmed[0] {
				writeTraexOwner(t, n.home, id, os.Getpid())
				cacheTraexOwnerPane(t, os.Getpid())
			}
			f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf(traexAgent, id, proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, "")}, nil))
		}
	})
	return n, f, home
}

func TestTraexFreshStartRequiresCurrentWriterEvidence(t *testing.T) {
	for _, source := range []string{"unindexed", "archived", "outside rollout window"} {
		t.Run(source, func(t *testing.T) {
			n, _, home := traexIdentityNode(t, threadOld, false)
			switch source {
			case "archived":
				traexHistoryFixture(t, n, nil) // threadOld is archived and excluded from the list.
			case "outside rollout window":
				writeMetaRollout(t, n.traexHome(), threadOld, `{"id":"`+threadOld+`","cwd":"`+home+`","source":"cli"}`, time.Now().Add(-90*24*time.Hour))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 1300*time.Millisecond)
			defer cancel()
			s, err := n.start(ctx, home, traexAgent, "Inspect the export", "", "w1", "", "", "", "", "")
			if s != nil || err == nil || !strings.Contains(err.Error(), "wait for traex session") {
				t.Fatalf("an unconfirmed host claim was adopted: %+v (%v)", s, err)
			}
		})
	}
}

func TestTraexAsyncStartWaitsForActualThread(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "managed"}[managed], func(t *testing.T) {
			n, f, home := traexIdentityNode(t, threadNew)
			launcher := traexAgent
			if managed {
				launcher = "managed-wrapper"
				n.setManaged([]proto.ManagedLauncher{{Name: launcher, Agent: traexAgent}})
			}
			res, err := request(t, n, "session.start", map[string]any{
				"cwd": home, "workspace_id": "w1", "launcher": launcher, "prompt": "Inspect the export", "async": true,
			})
			if err != nil {
				t.Fatal(err)
			}
			got := decodeSessions(t, []any{res["session"]})[0]
			if got.SessionID != threadNew || got.Agent != traexAgent {
				t.Fatalf("returned a preallocated or historical identity: %+v", got)
			}
			var commands []string
			for _, call := range f.calls() {
				if call.Method == "pane.send_input" {
					commands = append(commands, str(call.Params["text"]))
				}
			}
			if !strings.Contains(strings.Join(commands, "\n"), "Inspect the export") {
				t.Fatalf("the first prompt was lost: %q", commands)
			}
		})
	}
}

func TestTraexFreshStartExcludesHistoricalThreads(t *testing.T) {
	for _, paginated := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollout", true: "paginated"}[paginated], func(t *testing.T) {
			id := threadOld
			if paginated {
				id = threadMain
			}
			n, _, home := traexIdentityNode(t, id)
			if paginated {
				traexHistoryFixture(t, n, nil)
				if _, ok := n.traexThreads()[id]; !ok {
					t.Fatal("fixture has no historical paginated thread")
				}
			} else {
				writeMetaRollout(t, n.traexHome(), id, `{"id":"`+id+`","cwd":"`+home+`","source":"cli"}`, time.Now().Add(-time.Hour))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 1300*time.Millisecond)
			defer cancel()
			s, err := n.start(ctx, home, traexAgent, "Inspect the export", "", "w1", "", "", "", "", "")
			if s != nil || err == nil || !strings.Contains(err.Error(), "wait for traex session") {
				t.Fatalf("a fresh start adopted old history: %+v (%v)", s, err)
			}
		})
	}
}
