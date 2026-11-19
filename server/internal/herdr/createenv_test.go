package herdr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// An Agent's terminal starts with its marker variables: tab.create and workspace.create carry them as herdr's `env`
// object (#10111).
func TestCreateCarriesEnv(t *testing.T) {
	var mu sync.Mutex
	got := map[string]map[string]any{}
	sock := fakeServer(t, func(method string, params map[string]any) string {
		mu.Lock()
		got[method] = params
		mu.Unlock()
		return `"result":{"type":"tab_created","tab":{"tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}`
	})
	c := &Client{Bin: "/nonexistent/herdr", Socket: sock}
	if _, err := c.TabCreate(context.Background(), "w1", "/tmp", "agent", "PERSONA_AGENT=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WorkspaceCreate(context.Background(), "/tmp", "tmp", "PERSONA_AGENT=1"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"tab.create", "workspace.create"} {
		env, _ := got[m]["env"].(map[string]any)
		if env["PERSONA_AGENT"] != "1" {
			t.Fatalf("%s params %v: no PERSONA_AGENT in env", m, got[m])
		}
	}
	// Without variables nothing changes in the request.
	if _, err := c.TabCreate(context.Background(), "w1", "/tmp", "shell"); err != nil {
		t.Fatal(err)
	}
	if _, has := got["tab.create"]["env"]; has {
		t.Fatalf("a plain tab got env: %v", got["tab.create"])
	}
}

// Through the CLI (no server socket) the variables go as --env KEY=VALUE each, in the order given (#10365: a start
// tab's several); without any, no --env.
func TestCreateCarriesEnvOnTheCLI(t *testing.T) {
	bin, args := cliScript(t, `{"result":{"tab":{"tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}}`)
	c := &Client{Bin: bin, Socket: filepath.Join(t.TempDir(), "missing.sock")}
	ctx := context.Background()
	if _, err := c.TabCreate(ctx, "w1", "/tmp", "agent-1", "PERSONA_AGENT=1", "DISABLE_AUTO_UPDATE=true"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(args); strings.TrimSpace(string(b)) !=
		"tab create --workspace w1 --cwd /tmp --label agent-1 --no-focus --env PERSONA_AGENT=1 --env DISABLE_AUTO_UPDATE=true" {
		t.Errorf("CLI args %q", b)
	}
	if _, err := c.WorkspaceCreate(ctx, "/tmp", "tmp"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(args); strings.Contains(string(b), "--env") {
		t.Errorf("CLI args without env %q", b)
	}
}

// A herdr without environment support refuses the field; the tab still opens, without the variables.
func TestCreateRetriesWithoutEnvWhenRefused(t *testing.T) {
	var mu sync.Mutex
	var calls []map[string]any
	sock := fakeServer(t, func(method string, params map[string]any) string {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, params)
		if _, has := params["env"]; has {
			return `"error":{"code":"invalid_request","message":"unknown field ` + "`env`" + `"}`
		}
		return `"result":{"type":"tab_created","tab":{"tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}`
	})
	c := &Client{Bin: "/nonexistent/herdr", Socket: sock}
	created, err := c.TabCreate(context.Background(), "w1", "/tmp", "agent", "PERSONA_AGENT=1")
	if err != nil || created.RootPane.ID != "w1:p2" || len(calls) != 2 {
		t.Fatalf("created %+v err %v after %d calls", created, err, len(calls))
	}
}
