package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExternalLauncherIdentity(t *testing.T) {
	first := ExternalLauncherName("source-a", "target-a")
	if !IsExternalLauncher(first) || first != ExternalLauncherName("source-a", "target-a") {
		t.Fatalf("unstable external identity: %q", first)
	}
	for _, other := range []string{
		ExternalLauncherName("source-b", "target-a"),
		ExternalLauncherName("source-a", "target-b"),
		ExternalLauncherName("source-a\x00target-a", ""),
	} {
		if first == other {
			t.Fatal("distinct external targets collided")
		}
	}
	if strings.ContainsAny(first, " \t\n/;:$()'\"") {
		t.Fatalf("identity is not a safe launcher name: %q", first)
	}
	for _, ordinary := range []string{"claude", "external_tools", "persona_ext_helper", "persona_ext_" + strings.Repeat("g", 32)} {
		if IsExternalLauncher(ordinary) {
			t.Fatalf("ordinary launcher %q was reserved by the adapter", ordinary)
		}
	}
}

func TestExternalAdvertisementRoundTrip(t *testing.T) {
	target := ExternalTarget{
		ID: "target-a", Name: "Example connection", Engine: "claude-code", Revision: "revision-a",
		Capabilities:    ExternalCapabilities{Contexts: []string{"main-chat"}, Runtimes: []string{"cli"}, Platforms: []string{"posix"}},
		CredentialState: "unknown", Models: []AgentModel{{ID: "example-model", Name: "Example model"}},
	}
	launcher := Launcher{
		Name: ExternalLauncherName("source-a", target.ID), Source: ExternalLauncherSource,
		External: &ExternalLauncher{AdapterID: "source-a", ExternalTarget: target},
	}
	data, err := json.Marshal(LauncherEnvelope(launcher))
	if err != nil {
		t.Fatal(err)
	}
	var got LauncherEnvelope
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.External == nil || got.External.ID != target.ID || got.External.Models[0].ID != "example-model" {
		t.Fatalf("external metadata lost in the compatibility envelope: %s", data)
	}
	ordinary, err := json.Marshal(Launcher{Name: "claude"})
	if err != nil || strings.Contains(string(ordinary), "external") {
		t.Fatalf("ordinary launcher wire changed: %s (%v)", ordinary, err)
	}
}
