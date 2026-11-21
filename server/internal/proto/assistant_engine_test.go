package proto

import "testing"

func TestAssistantExecutionIdentityIncludesAdapterAndRejectsUnknownKinds(t *testing.T) {
	native := AssistantExecutionIdentity("claude", "", "", "alias")
	runtime := AssistantExecutionIdentity("claude", SourceRuntime, "engine-one", "alias")
	other := AssistantExecutionIdentity("claude", SourceRuntime, "engine-two", "alias")
	if native == "" || runtime == "" || other == "" || native == runtime || runtime == other {
		t.Fatalf("collapsed execution identities: %q %q %q", native, runtime, other)
	}
	for _, identity := range []string{native, runtime, other} {
		if AssistantEngineKind(identity) != "claude" {
			t.Fatalf("kind lost: %s", identity)
		}
	}
	for _, identity := range []string{"future", "future|runtime:engine", "claude|runtime:", "claude|runtime:bad/id", "claude|unknown:engine", "claude|external:unregistered"} {
		if AssistantEngineKind(identity) != "" {
			t.Fatalf("unknown execution accepted: %s", identity)
		}
	}
	if got := AssistantExecutionIdentity("opencode", SourceRuntime, "opencode", "opencode"); got == "" {
		t.Fatal("supported runtime rejected")
	}
}
