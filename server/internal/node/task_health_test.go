package node

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
)

// A task's prepared cwd is consumed by the real node preparation and process-start environment. The fake Codex
// executable speaks only the test protocol: no model is called. Even a saved allow rule cannot bypass health gates.
func TestTaskHealthPreparationClassifiesOutboundGates(t *testing.T) {
	for _, manager := range []string{"", "m-ab12"} {
		t.Run(identity.HealthDirName(manager), func(t *testing.T) {
			const thread = "019a1930-7e92-7073-9bb8-d2ad3126f329"
			addrFile := fakeCodexHome(t, thread)
			r := newSpaceRig(t, sendAllowed)
			r.n.home = t.TempDir()
			r.n.dir = filepath.Join(r.n.home, compat.StateDir)
			if err := os.MkdirAll(r.n.dir, 0700); err != nil {
				t.Fatal(err)
			}
			const policy = "# Synthetic Health instructions\nKeep this space separate.\n"
			raw, _ := json.Marshal(map[string]any{"dir": compat.StateDir + "/" + identity.HealthDirName(manager), "claude_md": policy})
			result, err := r.n.handle(context.Background(), "assistant.prepare", raw)
			if err != nil {
				t.Fatal(err)
			}
			cwd := result.(map[string]any)["cwd"].(string)
			for _, file := range []string{"CLAUDE.md", "AGENTS.md"} {
				data, err := os.ReadFile(filepath.Join(cwd, file))
				if err != nil || string(data) != policy {
					t.Fatalf("prepared %s: %q %v", file, data, err)
				}
			}
			if err := r.n.checkTaskDirectory("", cwd, "codex"); err != nil {
				t.Fatal(err)
			}
			r.skillIn(cwd)
			r.n.mu.Lock()
			r.n.modes, r.n.akeys, r.n.metas, r.n.bgScans = map[string]sessionModes{}, map[string]*assistantProc{}, map[string]quickMeta{}, map[string]*bgScan{}
			r.n.mu.Unlock()
			p, err := r.n.spawnCodex("", cwd, "codex", false, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(p.stop)
			if space, _ := envOf(p.cmd.Env, "PERSONA_SPACE"); space != identity.SpaceHealth {
				t.Fatalf("node started a main-space process: %q", space)
			}
			base, err := os.ReadFile(addrFile)
			if err != nil || len(base) == 0 {
				t.Fatalf("fake process's gate address: %q %v", base, err)
			}
			url := string(base) + "/api/v1/conversations/7123/messages"
			if code, _ := send(t, "POST", url, `{"text":"synthetic"}`); code != http.StatusForbidden || len(r.api.requests()) != 0 || r.cards() != 0 {
				t.Fatalf("unconfirmed outbound write: %d", code)
			}
			// The model can cd away; the process's initial health classification still requires a new phone confirmation.
			command := `ex POST /conversations/7123/messages '{"text":"synthetic"}'`
			if decision := r.check(t.TempDir(), thread, command); decision != "allow" || r.cards() != 1 {
				t.Fatalf("missing phone confirmation: %q cards=%d", decision, r.cards())
			}
			if keys := r.hub.lastCard().AlwaysKeys; len(keys) != 0 {
				t.Fatalf("health offered Always Allow: %v", keys)
			}
			if code, body := send(t, "POST", url, `{"text":"synthetic"}`); code != http.StatusOK {
				t.Fatalf("confirmed write: %d %s", code, body)
			}
			if code, _ := send(t, "POST", url, `{"text":"synthetic"}`); code != http.StatusForbidden || len(r.api.requests()) != 1 {
				t.Fatalf("confirmation was reused: %d", code)
			}
		})
	}
}
