package node

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// An omp launcher is omp, and only a launcher named exactly omp is (a wrapper of it is not guessed).
func TestLauncherAgentTypeOmp(t *testing.T) {
	for name, want := range map[string]string{"omp": ompAgent, "claude": "claude", "codex": "codex",
		"traex": traexAgent, "omp-wrapper": "claude", "my-omp": "claude"} {
		if got := launcherAgentType(name); got != want {
			t.Errorf("launcherAgentType(%s) = %q, want %q", name, got, want)
		}
	}
	list := typedLaunchers([]proto.Launcher{{Name: "omp"}, {Name: "claude"}})
	if list[0].AgentType != ompAgent || list[1].AgentType != "claude" {
		t.Fatalf("typed launchers %+v", list)
	}
}

// omp starts with its own command, never with Claude Code's flags, and its first message is one literal argument.
func TestOmpStartCommand(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" {
			cancel() // what was typed is all this test looks at
		}
	})
	const prompt = `a 'quoted' prompt; $(printf unexpected)`
	_, _ = n.startOtherAgent(ctx, n.hosts.Get(host.Herdr).(*herdrHost), home, "omp", ompAgent, prompt, "", "w1", "")
	want := call("pane.send_input", map[string]any{"pane_id": "w1:p2", "keys": []string{"Enter"},
		"text": inAgentEnv(n.dir, "omp "+shellQuote(prompt), shellQuote)})
	if got := f.trace("pane.send_input"); len(got) != 1 || got[0] != want {
		t.Fatalf("typed %v, want %s", got, want)
	}
	for _, flag := range []string{"--session-id", "--settings", "--mcp-config", "--plugin-dir", "--allowedTools"} {
		if strings.Contains(f.trace("pane.send_input")[0], flag) {
			t.Fatalf("omp was given %s", flag)
		}
	}

	// The same through a shell: the prompt is one argument, its quotes and command substitution left as text.
	cmd := otherAgentCommand("omp", "", prompt)
	out, err := exec.Command("sh", "-c", `omp() { printf '%s\n' "$#" "$@"; }; `+cmd).Output()
	if err != nil || string(out) != "1\n"+prompt+"\n" {
		t.Fatalf("omp arguments = %q (%v)", out, err)
	}
}

// A resume model override or launcher options unsupported by omp are refused before anything runs.
func TestOmpStartRefusesOptions(t *testing.T) {
	cases := map[string]map[string]any{
		"resume model": {"model": "some-model", "resume_session_id": "00000000-0000-4000-8000-000000000001"},
		"mode":         {"permission_mode": "plan"},
		"effort":       {"effort": "high"},
		"ultracode":    {"ultracode": true},
		"model+mode":   {"model": "some-model", "permission_mode": "plan"},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			params := map[string]any{"cwd": home, "launcher": "omp", "agent_type": "omp"}
			for k, v := range extra {
				params[k] = v
			}
			_, err := request(t, n, "session.start", params)
			if code, _ := refusalOf(t, err); code != "omp_options" {
				t.Fatalf("code %q (%v)", code, err)
			}
			if got := f.trace(); len(got) != 0 {
				t.Fatalf("herdr was asked %v", got)
			}
		})
	}
}

// An omp start naming another launcher (Codex, an omp provider with no omp command) is refused before a tab opens:
// that launcher would get the first message, not omp.
func TestOmpStartRefusesOtherLaunchers(t *testing.T) {
	for _, launcher := range []string{"codex", "my-omp-provider"} {
		for _, model := range []string{"", "opus"} {
			n, f, home := herdrNode(t)
			_, err := request(t, n, "session.start", map[string]any{"cwd": home, "launcher": launcher, "agent_type": "omp", "model": model})
			if err == nil || !strings.Contains(err.Error(), "launcher named omp") {
				t.Fatalf("%s (model %q): err = %v", launcher, model, err)
			}
			if got := f.trace(); len(got) != 0 {
				t.Fatalf("%s (model %q): herdr was asked %v", launcher, model, got)
			}
		}
	}
}

// Resuming an omp that is not running is refused until the node knows its session file; nothing is started and
// the message is not sent.
func TestOmpResumeRefusedWithoutSessionFile(t *testing.T) {
	n, f, home := herdrNode(t)
	_, err := request(t, n, "session.start", map[string]any{"cwd": home, "launcher": "omp",
		"resume_session_id": "00000000-0000-4000-8000-000000000001"})
	if code, _ := refusalOf(t, err); code != "omp_resume_unknown" {
		t.Fatalf("code %q (%v)", code, err)
	}
	if got := f.trace(); len(got) != 0 {
		t.Fatalf("herdr was asked %v", got)
	}
}
