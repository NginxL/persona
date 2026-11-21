package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestClaudeDesktopConfigLibraryModelsAndFallback(t *testing.T) {
	n, _ := testNode(t)
	_, second := useDesktopDirs(t)
	lib := filepath.Join(second, "configLibrary")
	if err := os.MkdirAll(lib, 0o700); err != nil {
		t.Fatal(err)
	}

	var seenAuth, seenKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		seenAuth = r.Header.Get("Authorization")
		seenKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": "claude-sonnet-5-5",
					"display_name": "Sonnet 5.5",
					"supported_reasoning_levels": [{"effort": "low"}, {"effort": "medium"}, {"effort": "high"}]
				},
				{
					"id": "gateway-opus-5-5[1m]",
					"display_name": "Gateway Opus [1m]",
					"description": "1M context",
					"is_family_default": true,
					"anthropic_family_tier": "opus",
					"supported_reasoning_levels": [{"effort": "low"}, {"effort": "medium"}, {"effort": "high"}, {"effort": "xhigh"}, {"effort": "max"}]
				},
				{
					"id": "unconfigured-model",
					"display_name": "Should Filter Out"
				}
			]
		}`))
	}))
	defer srv.Close()

	profileID := "11111111-2222-3333-4444-555555555555"
	if err := os.WriteFile(filepath.Join(lib, "_meta.json"), []byte(`{"appliedId":"`+profileID+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgJSON := `{
		"inferenceGatewayBaseUrl": "` + srv.URL + `",
		"inferenceGatewayApiKey": "sk-test-secret",
		"inferenceModels": [
			{"id": "claude-sonnet-5-5", "name": "claude-sonnet-5-5"},
			{"id": "gateway-opus-5-5[1m]", "name": "gateway-opus-5-5[1m]", "labelOverride": "Gateway Opus (1M)"}
		]
	}`
	if err := os.WriteFile(filepath.Join(lib, profileID+".json"), []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := n.launcherModels(context.Background(), "claude-desktop", "", true)
	if err != nil {
		t.Fatalf("launcherModels: %v", err)
	}
	if seenAuth != "Bearer sk-test-secret" || seenKey != "sk-test-secret" {
		t.Fatalf("unexpected headers: auth=%q key=%q", seenAuth, seenKey)
	}
	if got.Locked != "" || len(got.Models) != 2 {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got.Models[0].ID != "claude-sonnet-5-5" || got.Models[0].Default {
		t.Fatalf("unexpected first model: %+v", got.Models[0])
	}
	if got.Models[1].ID != "gateway-opus-5-5[1m]" || got.Models[1].Name != "Gateway Opus (1M)" || !got.Models[1].Default || got.Models[1].DefaultEffort != "high" {
		t.Fatalf("unexpected second model: %+v", got.Models[1])
	}
	if !reflect.DeepEqual(got.Models[1].Efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("unexpected efforts: %v", got.Models[1].Efforts)
	}

	// Gateway unreachable -> falls back to static inferenceModels entries.
	srv.Close()
	fallback, err := n.launcherModels(context.Background(), "claude-desktop", "", true)
	if err != nil {
		t.Fatalf("fallback launcherModels: %v", err)
	}
	if len(fallback.Models) != 2 || fallback.Models[0].ID != "claude-sonnet-5-5" || !fallback.Models[0].Default || fallback.Models[1].Name != "Gateway Opus (1M)" {
		t.Fatalf("unexpected static fallback: %+v", fallback)
	}
}

func TestClaudeDesktopMacOSBundledCLIDiscovery(t *testing.T) {
	root := t.TempDir()
	cli := filepath.Join(root, "Claude-3p", "claude-code", "2.1.326", "c4434d650cb3", "claude.app", "Contents", "MacOS", "claude")
	if err := os.MkdirAll(filepath.Dir(cli), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := claudeDesktopCLIsIn("darwin", []string{filepath.Join(root, "Claude"), filepath.Join(root, "Claude-3p")})
	if !reflect.DeepEqual(got, []string{cli}) {
		t.Fatalf("claudeDesktopCLIsIn = %v, want [%s]", got, cli)
	}
}

func TestCCPluginSettingsSwitchOnOutsideClaudeSession(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 2*desktopGatewayModelsTimeout)
	shortPluginWaits(t)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	// Start a settings-capable plugin poll loop over the real Unix socket.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	var delivered *ccPluginSettings
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			b, _ := json.Marshal(map[string]any{
				"session_id":     sidA,
				"plugin_version": "0.2.0",
				"engine_version": "2.1.326",
				"capabilities":   []string{"settings"},
				"wait_ms":        1000,
			})
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://persona/claude-plugin/v1/poll", strings.NewReader(string(b)))
			res, err := c.Do(req)
			if err != nil {
				continue
			}
			var out ccPluginDelivery
			_ = json.NewDecoder(res.Body).Decode(&out)
			_ = res.Body.Close()
			if out.Settings != nil {
				cp := *out.Settings
				delivered = &cp
				pluginPost(t, c, "ack", map[string]any{"session_id": sidA, "id": cp.ID, "submitted": true})
			}
		}
	}()
	waitPolling(t, n, sidA)

	// Verify listSessions advertises Capabilities.Model and Efforts without Modes.
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	s := byID(listSessions(t, n))[sidA]
	if s.Capabilities == nil || s.Capabilities.Model == nil || !*s.Capabilities.Model {
		t.Fatalf("expected Capabilities.Model = true, got %+v", s.Capabilities)
	}
	if s.Capabilities.Modes != nil && *s.Capabilities.Modes {
		t.Fatalf("expected Capabilities.Modes to stay false, got %+v", s.Capabilities)
	}
	if !reflect.DeepEqual(s.Efforts, proto.EffortLevels) {
		t.Fatalf("expected Efforts = %v, got %v", proto.EffortLevels, s.Efforts)
	}

	if _, err := request(t, n, "session.modes", map[string]any{
		"session_id": sidA,
		"model":      "gateway-opus-5-5[1m]",
		"effort":     "xhigh",
	}); err != nil {
		t.Fatalf("session.modes: %v", err)
	}
	if delivered == nil || delivered.Model != "gateway-opus-5-5[1m]" || delivered.Effort != "xhigh" {
		t.Fatalf("delivered = %+v", delivered)
	}

	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	after := byID(listSessions(t, n))[sidA]
	if after.Model != "gateway-opus-5-5[1m]" || after.Effort != "xhigh" {
		t.Fatalf("session after switch = %+v", after)
	}
}
