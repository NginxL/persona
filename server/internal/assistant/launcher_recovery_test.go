package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func fastLauncherRecovery(t *testing.T) {
	t.Helper()
	old := launcherRecoveryPoll
	launcherRecoveryPoll = 2 * time.Millisecond
	t.Cleanup(func() { launcherRecoveryPoll = old })
}

func setLauncherRecovery(f *fakeHub, waitSeconds int, fallbacks map[string][]string) {
	f.hello.Models.LauncherRecovery = map[string]LauncherRecovery{
		"server": {WaitSeconds: waitSeconds, Fallbacks: fallbacks},
	}
}

func launcherListCalls(f *fakeHub) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.launcherListParams...)
}

func TestMainChatLauncherRecoversWithFreshDiscovery(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	setLauncherRecovery(f, 1, nil)
	f.launcherList = []proto.Launcher{{Name: "cx", Agent: "codex"}}
	params := map[string]any{"launcher": "wrap", "model": "claude-opus-5-5"}
	type result struct {
		note string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		note, err := s.finishMainChatModel(context.Background(), "server", "assistant", params, false)
		done <- result{note: note, err: err}
	}()
	waitFor(t, "a fresh launcher-discovery retry", func() bool {
		for _, call := range launcherListCalls(f) {
			if call["refresh"] == true {
				return true
			}
		}
		return false
	})
	f.mu.Lock()
	f.launcherList = []proto.Launcher{{Name: "wrap"}, {Name: "cx", Agent: "codex"}}
	f.mu.Unlock()
	select {
	case got := <-done:
		if got.err != nil || got.note != "" || params["launcher"] != "wrap" || params["model"] != "claude-opus-5-5" {
			t.Fatalf("recovery: params %v, note %q, err %v", params, got.note, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovery did not finish after the launcher returned")
	}
	calls := launcherListCalls(f)
	if len(calls) < 2 || calls[0]["refresh"] == true {
		t.Fatalf("discovery calls = %v, want an ordinary initial read followed by fresh retries", calls)
	}
}

func TestMainChatLauncherRefreshFailureDoesNotCountAsRecovery(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	setLauncherRecovery(f, 1, nil)
	f.launcherList = []proto.Launcher{{Name: "cx", Agent: "codex"}}
	f.failLauncherRefresh = true
	params := map[string]any{"launcher": "wrap", "model": "claude-opus-5-5"}
	note, err := s.finishMainChatModel(context.Background(), "server", "assistant", params, false)
	if err == nil || !strings.Contains(err.Error(), "launcher refresh failed") || note != "" {
		t.Fatalf("failed refresh was accepted as recovery: params %v, note %q, err %v", params, note, err)
	}
	if params["launcher"] != "wrap" || params["model"] != "claude-opus-5-5" {
		t.Fatalf("failed refresh changed the binding: %v", params)
	}
	refreshes := 0
	for _, call := range launcherListCalls(f) {
		if call["refresh"] == true {
			refreshes++
		}
	}
	if refreshes < 2 {
		t.Fatalf("recovery stopped after the first failed refresh: %v", launcherListCalls(f))
	}
}

func TestMainChatLauncherIncompleteRefreshDoesNotCountAsRecovery(t *testing.T) {
	s, f := kindFixture(t)
	// An older node, or a response whose shell entries are all hidden, may contain only managed/built-in entries.
	// Those entries can be explicit substitutes, but they do not prove that the requested shell alias is back.
	f.launcherList = []proto.Launcher{{Name: "managed", Source: "persona"}}
	r := s.mainChatFitState(context.Background(), "server", "wrap", true)
	if r.fit != mainChatGone || !r.transient {
		t.Fatalf("incomplete forced catalog released the original send: %+v", r)
	}
}

func TestMainChatLauncherUsesCatalogFromWithinRecoveryBudget(t *testing.T) {
	old := launcherRecoveryPoll
	launcherRecoveryPoll = 2 * time.Second
	t.Cleanup(func() { launcherRecoveryPoll = old })
	s, f := kindFixture(t)
	setLauncherRecovery(f, 2, map[string][]string{"wrap": {"claude"}})
	f.launcherList = []proto.Launcher{{Name: "cx", Agent: "codex"}}
	params := map[string]any{"launcher": "wrap", "model": "claude-opus-5-5"}
	type result struct {
		note string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		note, err := s.finishMainChatModel(context.Background(), "server", "assistant", params, false)
		done <- result{note: note, err: err}
	}()
	fresh := func() bool {
		for _, call := range launcherListCalls(f) {
			if call["refresh"] == true {
				return true
			}
		}
		return false
	}
	deadline := time.NewTimer(500 * time.Millisecond)
	poll := time.NewTicker(2 * time.Millisecond)
	defer deadline.Stop()
	defer poll.Stop()
	for !fresh() {
		select {
		case <-deadline.C:
			t.Fatal("recovery did not make a fresh discovery at the start of its budget")
		case <-poll.C:
		}
	}
	// Any discovery after the recovery budget now fails. The fresh catalog obtained inside the budget must still be
	// enough to select the explicit fallback; an additional post-deadline read would instead return this error.
	f.mu.Lock()
	f.failLauncherRefresh = true
	f.mu.Unlock()
	select {
	case got := <-done:
		if got.err != nil || params["launcher"] != "claude" || !strings.Contains(got.note, "claude") {
			t.Fatalf("post-budget discovery displaced the in-budget catalog: params %v, note %q, err %v", params, got.note, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("recovery did not finish within its budget")
	}
}

func TestMainChatLauncherUsesOnlyConfiguredOrderedFallbacks(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	setLauncherRecovery(f, 1, map[string][]string{"wrap": {"cx", "unhealthy", "custom-b", "claude"}})
	f.launcherList = []proto.Launcher{
		{Name: "cx", Agent: "codex"},
		{Name: "unhealthy"},
		{Name: "custom-b"},
	}
	f.agentModels["custom-b"] = []proto.AgentModel{{ID: "claude-opus-5-5"}}
	s.recordHealth(context.Background(), "server", "unhealthy", healthResult{err: "signed out", source: healthSourceCheck})
	params := map[string]any{"launcher": "wrap", "model": "claude-opus-5-5"}
	note, err := s.finishMainChatModel(context.Background(), "server", "assistant", params, false)
	if err != nil || params["launcher"] != "custom-b" || params["model"] != "claude-opus-5-5" || !strings.Contains(note, "custom-b") {
		t.Fatalf("ordered fallback: params %v, note %q, err %v", params, note, err)
	}
}

func TestMainChatLauncherWithoutRecoveryConfigRefusesInsteadOfUsingDefault(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	setLauncherRecovery(f, 1, nil)
	// Both another Claude launcher and built-in claude are usable, but neither was approved as a substitute.
	f.launcherList = []proto.Launcher{{Name: "custom-b"}}
	params := map[string]any{"launcher": "wrap", "model": "claude-opus-5-5"}
	note, err := s.finishMainChatModel(context.Background(), "server", "assistant", params, false)
	if err == nil || !strings.Contains(err.Error(), "wrap") || note != "" {
		t.Fatalf("missing terminal refusal: params %v, note %q, err %v", params, note, err)
	}
	if params["launcher"] != "wrap" || params["model"] != "claude-opus-5-5" {
		t.Fatalf("refusal changed the resolved binding: %v", params)
	}
}

func TestMainChatIncompatibleLauncherDoesNotWaitForDiscovery(t *testing.T) {
	s, _ := mainChatFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	params := map[string]any{"launcher": "cc-relay"}
	_, err := s.finishMainChatModel(ctx, "server", "assistant", params, false)
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "cc-relay") {
		t.Fatalf("incompatible launcher waited or lost its refusal: %v", err)
	}
}

func TestMainChatLauncherRecoveryHonorsCancellation(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	setLauncherRecovery(f, 30, map[string][]string{"wrap": {"claude"}})
	f.launcherList = []proto.Launcher{{Name: "cx", Agent: "codex"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	params := map[string]any{"launcher": "wrap", "model": "claude-opus-5-5"}
	done := make(chan error, 1)
	go func() {
		_, err := s.finishMainChatModel(ctx, "server", "assistant", params, false)
		done <- err
	}()
	waitFor(t, "the first fresh discovery", func() bool {
		for _, call := range launcherListCalls(f) {
			if call["refresh"] == true {
				return true
			}
		}
		return false
	})
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation left launcher recovery blocked")
	}
	if params["launcher"] != "wrap" {
		t.Fatalf("cancellation selected a substitute: %v", params)
	}
}

func TestMainChatLauncherFallbackDiscoveryHonorsCancellation(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	setLauncherRecovery(f, 1, map[string][]string{"wrap": {"claude"}})
	f.launcherList = []proto.Launcher{{Name: "cx", Agent: "codex"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	params := map[string]any{"launcher": "wrap", "model": "claude-opus-5-5"}
	done := make(chan error, 1)
	go func() {
		_, err := s.finishMainChatModel(ctx, "server", "assistant", params, false)
		done <- err
	}()
	waitFor(t, "the first fresh discovery", func() bool {
		for _, call := range launcherListCalls(f) {
			if call["refresh"] == true {
				return true
			}
		}
		return false
	})
	gate := make(chan struct{})
	f.mu.Lock()
	f.launcherGate = gate
	f.mu.Unlock()
	t.Cleanup(func() { close(gate) })
	waitFor(t, "the fallback discovery", func() bool { return len(launcherListCalls(f)) >= 4 })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("fallback discovery cancellation returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation left fallback discovery blocked")
	}
}

func TestLauncherRecoveryWaitDefaultsAndIsBounded(t *testing.T) {
	s, f := kindFixture(t)
	if wait, _ := s.launcherRecovery(context.Background(), "server", "wrap"); wait != 30*time.Second {
		t.Fatalf("default wait = %s", wait)
	}
	setLauncherRecovery(f, 999, nil)
	if wait, _ := s.launcherRecovery(context.Background(), "server", "wrap"); wait != 2*time.Minute {
		t.Fatalf("configured wait was not capped: %s", wait)
	}
}

func TestMainChatLauncherAllowsExplicitDefaultFallback(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	setLauncherRecovery(f, 1, map[string][]string{"wrap": {"claude"}})
	f.launcherList = []proto.Launcher{{Name: "cx", Agent: "codex"}}
	params := map[string]any{"launcher": "wrap", "model": "claude-opus-5-5"}
	note, err := s.finishMainChatModel(context.Background(), "server", "assistant", params, false)
	if err != nil || params["launcher"] != "claude" || params["model"] != "claude-opus-5-5" || !strings.Contains(note, "claude") {
		t.Fatalf("explicit default fallback: params %v, note %q, err %v", params, note, err)
	}
}

func TestTaskBaselineUsesOnlyConfiguredRemoteFallback(t *testing.T) {
	fastLauncherRecovery(t)
	f := newFakeHub().withMachine()
	f.sessions["remote"] = nil
	f.zshrc = map[string][]proto.Launcher{
		"server": {{Name: "main", Source: "zshrc"}},
		"remote": {{Name: "remote", Source: "zshrc"}},
	}
	s := newTestService(t, f)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.main", Model: "selected-model"})
	f.hello.Models.LauncherRecovery = map[string]LauncherRecovery{
		"remote": {WaitSeconds: 1},
	}
	if got, err := s.taskModelBaseline(context.Background(), "remote"); err != nil || got.Provider != "launcher.main" {
		t.Fatalf("unconfigured remote fallback: %+v, err %v", got, err)
	}
	f.hello.Models.LauncherRecovery = map[string]LauncherRecovery{
		"remote": {WaitSeconds: 1, Fallbacks: map[string][]string{"main": {"remote"}}},
	}
	if got, err := s.taskModelBaseline(context.Background(), "remote"); err != nil || got.Provider != "launcher.remote" {
		t.Fatalf("configured remote fallback: %+v, err %v", got, err)
	}
	refreshes := 0
	for _, call := range launcherListCalls(f) {
		if call["refresh"] == true {
			refreshes++
		}
	}
	if refreshes == 0 {
		t.Fatal("a remote fallback was selected without fresh discovery")
	}
}

func TestTaskBaselineWaitsForRemoteLauncherRecovery(t *testing.T) {
	fastLauncherRecovery(t)
	f := newFakeHub().withMachine()
	f.sessions["remote"] = nil
	f.zshrc = map[string][]proto.Launcher{
		"server": {{Name: "main", Source: "zshrc"}},
		"remote": {{Name: "remote", Source: "zshrc"}},
	}
	s := newTestService(t, f)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.main", Model: "selected-model"})
	f.hello.Models.LauncherRecovery = map[string]LauncherRecovery{
		"remote": {WaitSeconds: 1, Fallbacks: map[string][]string{"main": {"remote"}}},
	}
	type result struct {
		binding ModelBinding
		err     error
	}
	done := make(chan result, 1)
	go func() {
		binding, err := s.taskModelBaseline(context.Background(), "remote")
		done <- result{binding: binding, err: err}
	}()
	waitFor(t, "a fresh remote launcher read", func() bool {
		for _, call := range launcherListCalls(f) {
			if call["refresh"] == true {
				return true
			}
		}
		return false
	})
	f.mu.Lock()
	f.zshrc["remote"] = []proto.Launcher{{Name: "main", Source: "zshrc"}, {Name: "remote", Source: "zshrc"}}
	f.mu.Unlock()
	select {
	case got := <-done:
		if got.err != nil || got.binding.Provider != "launcher.main" {
			t.Fatalf("remote recovery was replaced by its configured fallback: %+v, err %v", got.binding, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote launcher recovery did not finish")
	}
}

func TestStartOnMissingLauncherUsesRecoveryInsteadOfDefault(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "claude-opus-5-5"})
	setLauncherRecovery(f, 1, map[string][]string{"wrap": {"custom-b"}})
	f.launcherList = []proto.Launcher{{Name: "cx", Agent: "codex"}, {Name: "custom-b"}}
	f.agentModels["custom-b"] = []proto.AgentModel{{ID: "claude-opus-5-5"}}
	_, err := s.startOn(context.Background(), "server", StartParams{Text: "hi", Launcher: "wrap", Model: "claude-opus-5-5"}, false)
	if err != nil {
		t.Fatalf("start with configured recovery: %v", err)
	}
	if f.sent() != 1 || f.sends[0]["launcher"] != "custom-b" || f.sends[0]["model"] != "claude-opus-5-5" {
		t.Fatalf("start bypassed the original launcher's recovery: %v", f.sends)
	}
}

func TestSideConversationMissingLauncherUsesRecoveryInsteadOfDefault(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "claude-opus-5-5"})
	setLauncherRecovery(f, 1, map[string][]string{"wrap": {"custom-b"}})
	f.launcherList = []proto.Launcher{{Name: "cx", Agent: "codex"}, {Name: "custom-b"}}
	f.agentModels["custom-b"] = []proto.AgentModel{{ID: "claude-opus-5-5"}}
	f.setSessions("server", proto.Session{SessionID: "side", Launcher: "wrap", Model: "claude-opus-5-5", Assistant: true})
	p, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": "hi"}})
	if err != nil || p["launcher"] != "custom-b" || p["model"] != "claude-opus-5-5" {
		t.Fatalf("side conversation bypassed recovery: %v, %v", p, err)
	}
}

func TestSideConversationMissingLauncherWithoutFallbackDoesNotSend(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "claude-opus-5-5"})
	setLauncherRecovery(f, 1, nil)
	f.launcherList = []proto.Launcher{{Name: "cx", Agent: "codex"}, {Name: "custom-b"}}
	f.setSessions("server", proto.Session{SessionID: "side", Launcher: "wrap", Model: "claude-opus-5-5", Assistant: true})
	_, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": "hi"}})
	if err == nil || !strings.Contains(err.Error(), "wrap") || f.sent() != 0 {
		t.Fatalf("unapproved default was used: err %v, sends %v", err, f.sends)
	}
}

func TestMissingShellCodexUsesLastSeenAgentKindForFallback(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	setLauncherRecovery(f, 1, map[string][]string{"shell-codex": {"custom-b", "cx"}})
	f.launcherList = []proto.Launcher{{Name: "custom-b"}, {Name: "cx", Agent: "codex"}}
	s.models.zshrcMu.Lock()
	s.models.zshrc = map[string][]proto.Launcher{"server": {{Name: "shell-codex", Agent: "codex", Source: "zshrc"}}}
	s.models.zshrcSync = map[string]LauncherSync{}
	s.models.zshrcMu.Unlock()
	params := map[string]any{"launcher": "shell-codex", "model": "gpt-5"}
	note, err := s.finishMainChatModel(context.Background(), "server", "assistant", params, false)
	if err != nil || params["launcher"] != "cx" || !strings.Contains(note, "cx") {
		t.Fatalf("missing shell Codex was treated as Claude: params %v, note %q, err %v", params, note, err)
	}
}

func TestMissingLauncherWithUnknownKindDoesNotUseFallback(t *testing.T) {
	fastLauncherRecovery(t)
	s, f := kindFixture(t)
	s.models.zshrcMu.Lock()
	s.models.zshrc = nil // no successful discovery has established the disappeared launcher's agent
	s.models.zshrcSync = nil
	s.models.zshrcMu.Unlock()
	setLauncherRecovery(f, 1, map[string][]string{"wrap": {"claude", "cx"}})
	f.launcherList = []proto.Launcher{{Name: "custom-b"}, {Name: "cx", Agent: "codex"}}
	params := map[string]any{"launcher": "wrap", "model": "claude-opus-5-5"}
	note, err := s.finishMainChatModel(context.Background(), "server", "assistant", params, false)
	if err == nil || note != "" || params["launcher"] != "wrap" || params["model"] != "claude-opus-5-5" {
		t.Fatalf("unknown original kind selected a fallback: params %v, note %q, err %v", params, note, err)
	}
}
