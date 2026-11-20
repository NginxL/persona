package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Feed the real discovery parser into the node's normal launcher and assistant paths: installing the plain CLI
// must not turn it into an explicit shell override of its assistant-capable companion (#9181).
func TestAgyCommandOnPathUsesConnectedAssistantRuntime(t *testing.T) {
	for _, override := range []string{"", "function", "alias", "helper"} {
		t.Run("override="+override, func(t *testing.T) {
			parts := []string{"", "", "", "", "", "", "", "/opt/agent/bin/agy", "", ""}
			switch override {
			case "function":
				parts[8] = "agy\n"
			case "alias":
				parts[1] = "agy\x1f/opt/custom/bin/agy\x1f\x1e"
			case "helper":
				parts[0] = "agy https://gateway.example.com custom-model\n"
			}
			ls, _, _, _ := mergeLaunchers(launcherBegin + strings.Join(parts, "\x1d") + launcherEnd)
			withShellLaunchers(t) // owns and restores the global discovery cache
			launcherCache.Lock()
			launcherCache.list = ls
			launcherCache.Unlock()
			n, hub := runtimeNode(t)
			assistantHub(hub)
			if n.runtimeOf("agy") != nil {
				t.Fatal("no connected runtime: keep the terminal fallback")
			}
			hello := assistantRuntimeHello()
			hello["runtime"], hello["agent"], hello["launcher"] = "agy", "agy", "agy"
			hello["launchers"] = []map[string]any{{"name": "agy", "agent": "agy", "models": []map[string]any{{"id": "runtime-model"}}}}
			host := dialRuntime(t, n, hello)
			host.answer = func(method string, _ json.RawMessage) (any, error) {
				if method == "session.start" {
					return map[string]any{"session": map[string]any{"session_id": "agy-assistant-test", "cwd": n.home, "status": "idle"}}, nil
				}
				return map[string]any{"ok": true}, nil
			}
			raw, err := nodeCall(t, n, "launchers.list", map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			var listed struct{ Launchers []proto.Launcher }
			if err := json.Unmarshal(raw, &listed); err != nil {
				t.Fatal(err)
			}
			var picks []proto.Launcher
			for _, l := range listed.Launchers {
				if l.Name == "agy" {
					picks = append(picks, l)
				}
			}
			if len(picks) != 1 {
				t.Fatalf("ambiguous AGY launcher: %+v", picks)
			}
			models, err := nodeCall(t, n, "launchers.models", map[string]any{"name": "agy", "host": "agy"})
			if err != nil || !strings.Contains(string(models), `"id":"runtime-model"`) {
				t.Fatalf("explicit companion model catalog: %s %v", models, err)
			}
			if override != "" {
				if picks[0].Assistant || n.runtimeOf("agy") != nil || n.assistantRuntime("agy") != nil {
					t.Fatal("the runtime took an explicit shell override")
				}
				return
			}
			if !picks[0].Assistant || picks[0].Source != "agy" {
				t.Fatalf("plain CLI shadowed the assistant runtime: %+v", picks[0])
			}
			if agent, err := n.assistantAgent(context.Background(), "agy"); err != nil || agent != assistantOnRuntime {
				t.Fatalf("assistant agent: %s %v", agent, err)
			}
			models, err = nodeCall(t, n, "launchers.models", map[string]any{"name": "agy", "purpose": "assistant"})
			if err != nil || !strings.Contains(string(models), `"id":"runtime-model"`) {
				t.Fatalf("runtime model catalog: %s %v", models, err)
			}
			if _, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": "agy",
				"cwd": filepath.Join(n.home, ".persona", "assistant"), "text": "test task", "model": "runtime-model"}); err != nil {
				t.Fatal(err)
			}
			if len(host.got("session.start")) != 1 || len(host.got("session.send")) != 1 {
				t.Fatal("assistant task did not use the runtime protocol")
			}
			var start map[string]any
			_ = json.Unmarshal(host.got("session.start")[0], &start)
			if start["assistant"] != true || start["model"] != "runtime-model" {
				t.Fatalf("assistant settings changed: %+v", start)
			}
			_ = host.conn.Close()
			waitFor(t, "terminal fallback after disconnect", func() bool { return n.runtimeOf("agy") == nil })
			if n.assistantRuntime("agy") != nil {
				t.Fatal("disconnected runtime remained an assistant launcher")
			}
			raw, err = nodeCall(t, n, "launchers.list", map[string]any{})
			listed.Launchers = nil
			if err != nil || json.Unmarshal(raw, &listed) != nil {
				t.Fatalf("disconnected launcher list: %s %v", raw, err)
			}
			if l := byName(listed.Launchers)["agy"]; l.Name == "" || l.Assistant || l.Source != "zshrc" {
				t.Fatalf("terminal fallback was lost: %+v", l)
			}
		})
	}
}

func TestAgyStartRoutesHerdrAndCompanion(t *testing.T) {
	for _, tc := range []struct {
		name, startHost, model, effort string
		companion                      bool
	}{
		{name: "explicit herdr with companion", startHost: proto.EngineHerdr, model: "example-low", effort: "low", companion: true},
		{name: "default herdr with companion", model: "example-medium", effort: "medium", companion: true},
		{name: "herdr without companion", startHost: proto.EngineHerdr, model: "example-high", effort: "high"},
		{name: "extra high", startHost: proto.EngineHerdr, model: "example-xhigh", effort: "xhigh"},
		{name: "max", startHost: proto.EngineHerdr, model: "example-max", effort: "max"},
		{name: "native defaults", startHost: proto.EngineHerdr, companion: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			catalog, calls := fakeAgyModels(t, home)
			writeFile(t, catalog, "local-model\tLocal Model\n")
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
			f.after(func(c herdrCall) {
				if c.Method == "pane.send_input" && strings.Contains(c.String(), "HERDR_AGENT=agy") {
					f.reply("session.snapshot", snapshot(nil, nil, []any{
						agentOf(agyAgent, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, agyAgent),
					}, nil))
					f.setScreen("w1:p2", agyIdleScreen) // agy's input box is up: its first message may go in (agytrust.go)
				}
			})
			var companion *fakeRuntime
			if tc.companion {
				n.logger = log.New(io.Discard, "", 0)
				n.runtimeSock = filepath.Join(filepath.Dir(f.sock), "runtime.sock")
				ctx, cancel := context.WithCancel(t.Context())
				t.Cleanup(cancel)
				if _, err := n.listenRuntimeSocket(ctx); err != nil {
					t.Fatal(err)
				}
				hello := assistantRuntimeHello()
				hello["runtime"], hello["agent"], hello["launcher"] = agyAgent, agyAgent, agyAgent
				hello["launchers"] = []map[string]any{{"name": agyAgent, "agent": agyAgent, "models": []proto.AgentModel{{ID: "companion-model"}}}}
				companion = dialRuntime(t, n, hello)
				companion.mu.Lock()
				companion.answer = func(method string, _ json.RawMessage) (any, error) {
					if method == "session.start" {
						return map[string]any{"session": proto.Session{SessionID: "agy-companion", Cwd: home, Status: proto.StatusIdle}}, nil
					}
					return map[string]any{"ok": true}, nil
				}
				companion.mu.Unlock()
			}

			models, err := nodeCall(t, n, "launchers.models", map[string]any{"name": agyAgent, "host": tc.startHost})
			if err != nil || !strings.Contains(string(models), "\"id\":\"local-model\"") {
				t.Fatalf("herdr catalog = %s, %v", models, err)
			}
			res, err := nodeCall(t, n, "session.start", map[string]any{
				"cwd": home, "launcher": agyAgent, "host": tc.startHost, "workspace_id": "w1",
				"model": tc.model, "effort": tc.effort, "async": true,
			})
			var started struct{ Session proto.Session }
			if err != nil || json.Unmarshal(res, &started) != nil || started.Session.PaneID != "w1:p2" {
				t.Fatalf("herdr session = %s, %v", res, err)
			}
			command := "HERDR_AGENT=agy agy"
			if tc.model != "" {
				command += " --model " + tc.model + " --effort " + tc.effort
			}
			want := call("pane.send_input", map[string]any{"pane_id": "w1:p2", "keys": []string{"Enter"}, "text": inAgentEnv(n.dir, command, shellQuote)})
			var matching []string
			for _, c := range f.trace("pane.send_input") {
				if strings.Contains(c, "HERDR_AGENT=") {
					matching = append(matching, c)
				}
			}
			if len(matching) != 1 || matching[0] != want {
				t.Fatalf("pane input = %v, want %s", matching, want)
			}
			if st, _ := n.state.get(started.Session.SessionID); st.Model != tc.model || st.ModelEffort != tc.effort {
				t.Fatalf("recorded model and effort = %q, %q", st.Model, st.ModelEffort)
			}
			if companion == nil {
				return
			}
			if len(companion.got("session.start")) != 0 {
				t.Fatal("herdr start was sent to the companion")
			}
			models, err = nodeCall(t, n, "launchers.models", map[string]any{"name": agyAgent, "host": agyAgent})
			if err != nil || !strings.Contains(string(models), "\"id\":\"companion-model\"") {
				t.Fatalf("terminal-less catalog = %s, %v", models, err)
			}
			res, err = nodeCall(t, n, "session.start", map[string]any{
				"cwd": home, "launcher": agyAgent, "host": agyAgent, "model": "companion-model", "effort": "high",
			})
			if err != nil || !strings.Contains(string(res), "agy-companion") {
				t.Fatalf("terminal-less session = %s, %v", res, err)
			}
			var args runtimeStartParams
			starts := companion.got("session.start")
			if len(starts) != 1 || json.Unmarshal(starts[0], &args) != nil || args.Model != "companion-model" || args.Effort != "high" {
				t.Fatalf("companion start = %s", starts)
			}
			if got, err := os.ReadFile(calls); err != nil || string(got) != "models\n" {
				t.Fatalf("agy calls = %q, %v", got, err)
			}
			if len(f.trace("tab.create")) != 1 {
				t.Fatal("companion start created a pane")
			}
		})
	}
}

func TestAgyStartRefusesInvalidEffortBeforeCreatingPane(t *testing.T) {
	n, f, home := herdrNode(t)
	for _, effort := range []string{"auto", "ultra", "bogus"} {
		_, err := nodeCall(t, n, "session.start", map[string]any{
			"cwd": home, "launcher": agyAgent, "host": host.Herdr, "model": "example-high", "effort": effort, "async": true,
		})
		if err == nil || !strings.Contains(err.Error(), "unknown effort level") {
			t.Fatalf("start with effort %q: %v", effort, err)
		}
	}
	if len(f.trace("tab.create")) != 0 || len(f.trace("pane.send_input")) != 0 {
		t.Fatal("invalid effort reached herdr")
	}
}
