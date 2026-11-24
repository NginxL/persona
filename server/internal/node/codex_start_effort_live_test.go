//go:build !windows

package node

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/nwverse/persona/server/internal/proto"
)

// Opt in with PERSONA_CODEX_EFFORT_BIN=<absolute path to codex>. Both the app
// server and TUI use a disposable home, socket and loopback model provider.
func TestLiveCodexPreparedEffort(t *testing.T) {
	bin := os.Getenv("PERSONA_CODEX_EFFORT_BIN")
	if !filepath.IsAbs(bin) {
		t.Skip("set PERSONA_CODEX_EFFORT_BIN to an absolute Codex CLI path")
	}
	root, err := os.MkdirTemp("/tmp", "cx-effort-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	cwd := mkdir(t, filepath.Join(root, "work"))
	home := filepath.Join(root, ".codex")
	t.Setenv("HOME", root)
	t.Setenv("CODEX_HOME", home)
	t.Setenv("ZDOTDIR", root)
	env := []string{"HOME=" + root, "CODEX_HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"}
	requests := make(chan string, 32)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Reasoning struct{ Effort string }
			Input     json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if strings.Contains(string(body.Input), "persona initial effort probe") {
			requests <- body.Reasoning.Effort
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"+
			"data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	t.Cleanup(provider.Close)
	writeFile(t, filepath.Join(home, "config.toml"), fmt.Sprintf(`
model = "gpt-6.2-sol"
model_reasoning_effort = "xhigh"
model_provider = "fixture"
check_for_update_on_startup = false
[model_providers.fixture]
name = "Fixture"
base_url = %q
wire_api = "responses"
[projects.%q]
trust_level = "trusted"
`, provider.URL+"/v1", cwd))

	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	socket := filepath.Join(root, "s")
	endpoint := "unix://" + socket
	cmd := exec.CommandContext(ctx, bin, "app-server", "--listen", endpoint)
	cmd.Env, cmd.Dir = env, cwd
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	waitFor(t, "isolated app-server socket", func() bool {
		_, err := os.Stat(socket)
		return err == nil
	})
	n := remoteTestNode(t)
	n.home = root
	n.configureCodex(proto.CodexConfig{})
	c := connectFixtureEndpoint(t, n, home, "effort-test", endpoint)
	cli := []string{bin, "--remote", endpoint}

	startTUI := func(t *testing.T, id, want string) {
		t.Helper()
		tui := exec.CommandContext(ctx, bin, "--remote", endpoint, "--no-alt-screen", "resume", id)
		tui.Env, tui.Dir = env, cwd
		terminal, err := pty.StartWithSize(tui, &pty.Winsize{Rows: 40, Cols: 160})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tui.Process.Kill(); _ = terminal.Close(); _ = tui.Wait() })
		result := make(chan string, 1)
		go func() {
			var shown strings.Builder
			reader := bufio.NewReader(terminal)
			for {
				b := make([]byte, 8192)
				n, err := reader.Read(b)
				shown.Write(b[:n])
				if err != nil || strings.Contains(shown.String(), want) ||
					(want == "for shortcuts" && strings.Contains(shown.String(), "Failed to resume session")) {
					result <- shown.String()
					return
				}
			}
		}()
		select {
		case seen := <-result:
			if !strings.Contains(seen, want) {
				t.Fatalf("Codex did not show %q: %q", want, seen)
			}
		case <-time.After(8 * time.Second):
			t.Fatalf("Codex did not show %q", want)
		}
	}

	t.Run("reproduce zero-turn resume", func(t *testing.T) {
		var reply struct{ Thread remoteThreadData }
		if err := c.wire.call(ctx, "thread/start", map[string]any{"cwd": cwd, "config": map[string]any{"model_reasoning_effort": "high"}}, &reply); err != nil {
			t.Fatal(err)
		}
		thread := &codexPreparedThread{server: c, raw: reply.Thread.ID, path: reply.Thread.Path}
		t.Cleanup(thread.discard)
		if err := c.wire.call(ctx, "thread/settings/update", map[string]any{"threadId": thread.raw, "effort": "high"}, nil); err != nil {
			t.Fatal(err)
		}
		if codexRolloutExists(reply.Thread.Path) {
			t.Fatal("fixture unexpectedly persisted a rollout before the first turn")
		}
		startTUI(t, thread.raw, "no rollout found for thread id")
	})

	t.Run("unsupported effort", func(t *testing.T) {
		thread, err := n.prepareCodexEffort(ctx, nil, cli, cwd, modelExecution{Model: "gpt-5.5", Effort: "max"})
		if thread != nil || err == nil || !strings.Contains(err.Error(), `does not support effort "max"`) {
			t.Fatalf("want a model capability refusal, got %v", err)
		}
	})

	for _, effort := range []string{"max", "high", "low"} {
		t.Run(effort, func(t *testing.T) {
			thread, err := n.prepareCodexEffort(ctx, nil, cli, cwd, modelExecution{Effort: effort})
			if err != nil || thread == nil {
				t.Fatalf("shared start preparation failed: %v", err)
			}
			t.Cleanup(thread.discard)
			if route, note := n.codexRoute(thread.public); route != codexRouteAppServer {
				t.Fatalf("route %q: %s", route, note)
			}
			startTUI(t, thread.raw, "for shortcuts")
			if err := n.sendCodex(ctx, thread.public, "persona initial effort probe", nil); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-requests:
				if got != effort {
					t.Fatalf("first model request used %q, want %q", got, effort)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("the provider did not receive the first turn")
			}
		})
	}
}
