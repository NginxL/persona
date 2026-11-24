package node

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func prepareRuntimeAssistantEnvironment(t *testing.T, n *Node, proxied bool) string {
	t.Helper()
	skills := filepath.Join(n.home, ".persona", "skills")
	t.Setenv("PERSONA_SKILLS_DIR", skills)
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n.dir = filepath.Join(n.home, ".persona")
	script := filepath.Join(skills, "example", "scripts", "ex.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "example", "SKILL.md"), []byte("---\nname: example\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'fake extension command\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := prepareAssistantForTest(t, n, ".persona/assistant", nil, nil)
	g := exampleGate("")
	g.Env["EXAMPLE_ACCOUNT"] = "2"
	g.Env["EXAMPLE_TOKEN"] = "fake-credential-handle=with-equals"
	if proxied {
		g.Env["EXAMPLE_BASE"] = "https://upstream.example.test"
		g.Auth = &proto.GateAuth{Header: "Authorization", Value: "Bearer ${EXAMPLE_TOKEN}"}
	}
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{g}}, time.Now().Add(time.Hour)
	n.mu.Unlock()
	return cwd
}

func runtimeAssistantEnvironment(t *testing.T, raw json.RawMessage) map[string]string {
	t.Helper()
	var params struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatal(err)
	}
	return params.Env
}

func TestRuntimeAssistantEnvironmentCapable(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	cwd := prepareRuntimeAssistantEnvironment(t, n, false)
	hello := assistantRuntimeHello()
	hello["caps"].(map[string]any)["assistant_environment"] = true
	host := dialRuntime(t, n, hello)
	startedAssistant(t, n, host)

	starts, sends := host.got("session.start"), host.got("session.send")
	if len(starts) != 1 || len(sends) != 1 {
		t.Fatalf("start/send counts: %d/%d", len(starts), len(sends))
	}
	env := runtimeAssistantEnvironment(t, starts[0])
	if len(env) == 0 {
		t.Fatalf("runtime assistant start has no extension environment: %s", starts[0])
	}
	if got := runtimeAssistantEnvironment(t, sends[0]); !maps.Equal(got, env) {
		t.Fatalf("start and send environments differ:\nstart %q\nsend  %q", env, got)
	}
	bin := n.assistantBin(cwd)
	if path := env["PATH"]; path != bin+string(os.PathListSeparator)+os.Getenv("PATH") {
		t.Fatalf("PATH %q does not begin with the generated command directory %q", path, bin)
	}
	for name, want := range map[string]string{
		"PERSONA_ASSISTANT_BIN": bin,
		"PERSONA_SPACE":         spaceMain,
		"EXAMPLE_ACCOUNT":       "2",
		"EXAMPLE_TOKEN":         "fake-credential-handle=with-equals",
	} {
		if got := env[name]; got != want {
			t.Fatalf("%s = %q, want %q; environment %q", name, got, want, env)
		}
	}
	if _, err := os.Stat(filepath.Join(bin, "ex")); err != nil {
		t.Fatalf("generated extension CLI is missing: %v", err)
	}

	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": "session-a1", "permission_mode": "plan"}); err != nil {
		t.Fatal(err)
	}
	modes := host.got("session.modes")
	if len(modes) != 1 || !maps.Equal(runtimeAssistantEnvironment(t, modes[0]), env) {
		t.Fatalf("mode-triggered worker creation lost the environment: %s", modes)
	}
}

func TestRuntimeAssistantEnvironmentWithholdsProxyCredentials(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	prepareRuntimeAssistantEnvironment(t, n, true)
	hello := assistantRuntimeHello()
	hello["caps"].(map[string]any)["assistant_environment"] = true
	host := dialRuntime(t, n, hello)
	startedAssistant(t, n, host)

	env := runtimeAssistantEnvironment(t, host.got("session.start")[0])
	if got := env["EXAMPLE_ACCOUNT"]; got != "2" {
		t.Fatalf("approved non-secret environment is missing: %q", env)
	}
	for _, name := range []string{"EXAMPLE_TOKEN", "EXAMPLE_BASE"} {
		if value, ok := env[name]; ok {
			t.Fatalf("proxied credential material escaped through %s=%q", name, value)
		}
	}
	encoded, _ := json.Marshal(env)
	if joined := string(encoded); strings.Contains(joined, "fake-credential-handle") || strings.Contains(joined, "upstream.example.test") {
		t.Fatalf("raw credential or direct upstream reached the runtime: %s", joined)
	}
}

func TestRuntimeAssistantEnvironmentCapabilityBoundary(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	prepareRuntimeAssistantEnvironment(t, n, false)
	host := dialRuntime(t, n, assistantRuntimeHello())
	startedAssistant(t, n, host)
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": "session-a1", "permission_mode": "plan"}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"session.start", "session.send", "session.modes"} {
		for _, raw := range host.got(method) {
			if strings.Contains(string(raw), `"env":`) {
				t.Fatalf("runtime without assistant_environment received an environment: %s %s", method, raw)
			}
		}
	}
}
