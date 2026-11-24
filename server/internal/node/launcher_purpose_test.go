package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAssistantLauncherDiscoveryAndCheckUseShellOnPaseoHost(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := runtimeNode(t)
	n.home = home
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	hello := sameKindPaseoHello()
	hello["caps"].(map[string]any)["check"] = true
	hello["launchers"] = []map[string]any{{"name": "mine", "agent": "claude", "models": []map[string]any{{"id": "provider-model"}}}}
	host := dialRuntime(t, n, hello)
	host.answer = func(method string, _ json.RawMessage) (any, error) {
		return proto.LauncherCheck{OK: true, Model: "provider-model", Answer: "provider answer"}, nil
	}
	waitFor(t, "provider", func() bool { return n.runtimes.launcherRuntime("mine") != nil })

	for _, purpose := range []string{"", "assistant", ""} {
		raw, err := nodeCall(t, n, "launchers.models", map[string]any{"name": "mine", "purpose": purpose})
		if err != nil {
			t.Fatal(err)
		}
		var got proto.LauncherModels
		if err := json.Unmarshal(raw, &got); err != nil || len(got.Models) != 1 {
			t.Fatalf("%s: %s (%v)", purpose, raw, err)
		}
		if purpose == "assistant" {
			if got.Models[0].Resolved != "model-of-mine" {
				t.Fatalf("assistant got another process's models: %s", raw)
			}
		} else if got.Models[0].ID != "provider-model" {
			t.Fatalf("Agents lost their provider's models: %s", raw)
		}
	}
	for path, data := range map[string]string{
		".claude.json": `{"hasCompletedOnboarding":true}`,
		"bin/claude":   "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"result\":\"shell answer\",\"is_error\":false}'\n",
	} {
		if err := os.WriteFile(filepath.Join(home, path), []byte(data), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, purpose := range []string{"", "assistant"} {
		raw, err := nodeCall(t, n, "launchers.check", map[string]any{"name": "mine", "purpose": purpose})
		var got proto.LauncherCheck
		if err != nil || json.Unmarshal(raw, &got) != nil || !got.OK {
			t.Fatalf("%s: %s (%v)", purpose, raw, err)
		}
		want := "provider answer"
		if purpose == "assistant" {
			want = "shell answer"
		}
		if got.Answer != want {
			t.Fatalf("%s check answered from %q, want %q", purpose, got.Answer, want)
		}
	}
}

func TestAssistantModelForwardingCheckDoesNotUseSameNamedEngineProvider(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := runtimeNode(t)
	n.home = home
	if err := n.useEngine(t.Context(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	hello := sameKindPaseoHello()
	hello["launchers"] = []map[string]any{{"name": "fixed", "agent": "claude", "models": []map[string]any{{"id": "provider-model"}}}}
	dialRuntime(t, n, hello)
	waitFor(t, "provider", func() bool { return n.runtimes.launcherRuntime("fixed") != nil })
	for _, purpose := range []string{"", "assistant", ""} {
		_, err := nodeCall(t, n, "launchers.model.check", map[string]any{"name": "fixed", "model": "other-model", "purpose": purpose})
		if (err != nil) != (purpose == "assistant") {
			t.Fatalf("purpose %q: fixed shell model forwarding error=%v", purpose, err)
		}
	}
}
