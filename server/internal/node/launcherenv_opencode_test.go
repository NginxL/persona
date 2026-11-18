package node

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

type nodeRoundTrip func(*http.Request) (*http.Response, error)

func (f nodeRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderEnvOpenCodeGoCustomHeaders(t *testing.T) {
	l := proto.ManagedLauncher{
		Name:    "opencode-claude",
		BaseURL: "https://opencode.ai/inference/go/anthropic/v1/messages",
		APIKey:  "sk-test",
		Model:   "claude-sonnet-5-5",
	}
	envA := providerEnv(l, "", "", false)
	envB := providerEnv(l, "", "", false)
	if envA["ANTHROPIC_BASE_URL"] != "https://opencode.ai/inference/go/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", envA["ANTHROPIC_BASE_URL"])
	}
	want := modelhttp.OpenCodeSessionHeader + ": " + modelhttp.OpenCodeSessionID("launcher:opencode-claude")
	if envA["ANTHROPIC_CUSTOM_HEADERS"] != want || envB["ANTHROPIC_CUSTOM_HEADERS"] != want {
		t.Fatalf("ANTHROPIC_CUSTOM_HEADERS = %q / %q, want %q", envA["ANTHROPIC_CUSTOM_HEADERS"], envB["ANTHROPIC_CUSTOM_HEADERS"], want)
	}

	// Appends cleanly when other custom headers exist without x-opencode-session.
	withTeam := l
	withTeam.Headers = "X-Team: alpha"
	if got := providerEnv(withTeam, "", "", false)["ANTHROPIC_CUSTOM_HEADERS"]; got != "X-Team: alpha\n"+want {
		t.Fatalf("merged headers = %q", got)
	}

	// Preserves user-supplied x-opencode-session.
	withExplicit := l
	withExplicit.Headers = "X-OpenCode-Session: ses_custom"
	if got := providerEnv(withExplicit, "", "", false)["ANTHROPIC_CUSTOM_HEADERS"]; got != "X-OpenCode-Session: ses_custom" {
		t.Fatalf("explicit header overwritten: %q", got)
	}

	// Non-OpenCode gateways stay untouched.
	other := proto.ManagedLauncher{Name: "other", BaseURL: "https://notopencode.ai/inference/go/anthropic", APIKey: "sk-test", Model: "m"}
	if got := providerEnv(other, "", "", false)["ANTHROPIC_CUSTOM_HEADERS"]; strings.Contains(got, modelhttp.OpenCodeSessionHeader) {
		t.Fatalf("unexpected header on non-opencode gateway: %q", got)
	}

	// ANTHROPIC_BASE_URL in l.Env determines whether OpenCode Go session headers are injected.
	envOverrideIn := other
	envOverrideIn.Env = "ANTHROPIC_BASE_URL=https://opencode.ai/zen/go/anthropic"
	wantOverride := modelhttp.OpenCodeSessionHeader + ": " + modelhttp.OpenCodeSessionID("launcher:other")
	if got := providerEnv(envOverrideIn, "", "", false)["ANTHROPIC_CUSTOM_HEADERS"]; got != wantOverride {
		t.Fatalf("env override into OpenCode Go: got %q, want %q", got, wantOverride)
	}
	envOverrideOut := l
	envOverrideOut.Env = "ANTHROPIC_BASE_URL=https://api.anthropic.com"
	if got := providerEnv(envOverrideOut, "", "", false)["ANTHROPIC_CUSTOM_HEADERS"]; strings.Contains(got, modelhttp.OpenCodeSessionHeader) {
		t.Fatalf("env override away from OpenCode Go kept header: %q", got)
	}
}

func TestCodexManagedEnvAndLaunchArgsStripLegacyResponsesSuffix(t *testing.T) {
	legacy := proto.ManagedLauncher{
		Name:    "legacy-codex",
		Agent:   "codex",
		BaseURL: "https://gw.example/v1/responses",
		APIKey:  "sk-codex",
		Model:   "gpt-5.4",
	}
	env := managedEnv(legacy)
	wantEnv := []string{"OPENAI_BASE_URL=https://gw.example/v1", "OPENAI_API_KEY=sk-codex"}
	if !slices.Equal(env, wantEnv) {
		t.Fatalf("managedEnv = %v, want %v", env, wantEnv)
	}

	argv := launchArgv("codex", []string{"exec", "hi"}, nil, "", "gpt-5.4", env, "")
	wantArgv := []string{
		"codex",
		"-c", `model_provider="persona-launcher"`,
		"-c", `model_providers.persona-launcher={name="Persona launcher",base_url="https://gw.example/v1",wire_api="responses",env_key="OPENAI_API_KEY"}`,
		"-m", "gpt-5.4",
		"exec", "hi",
	}
	if !slices.Equal(argv, wantArgv) {
		t.Fatalf("launchArgv = %v, want %v", argv, wantArgv)
	}

	catIn, ok := codexCatalogInputOf(legacy)
	if !ok || catIn.base != "https://gw.example/v1" {
		t.Fatalf("codexCatalogInputOf = %+v, %v", catIn, ok)
	}

	piProv, ok := piProviderOf(legacy)
	if !ok || piProv.BaseURL != "https://gw.example/v1" {
		t.Fatalf("piProviderOf = %+v, %v", piProv, ok)
	}
}

func TestCodexOpenCodeGoSessionHeaderInLaunchCatalogAndPi(t *testing.T) {
	oc := proto.ManagedLauncher{
		Name:    "opencode-codex",
		Agent:   "codex",
		BaseURL: "https://opencode.ai/inference/go/openai/v1/responses",
		APIKey:  "sk-codex",
		Model:   "gpt-5.4",
	}
	wantSession := modelhttp.OpenCodeSessionID("launcher:opencode-codex")
	env := managedEnv(oc)
	wantEnv := []string{
		"OPENAI_BASE_URL=https://opencode.ai/inference/go/openai/v1",
		"OPENAI_API_KEY=sk-codex",
		openCodeSessionEnv + "=" + wantSession,
	}
	if !slices.Equal(env, wantEnv) {
		t.Fatalf("managedEnv = %v, want %v", env, wantEnv)
	}
	if !slices.Contains(envClearFor(oc), openCodeSessionEnv) {
		t.Fatalf("envClearFor missing %s: %v", openCodeSessionEnv, envClearFor(oc))
	}

	argv := launchArgv("codex", []string{"exec", "hi"}, nil, "", "gpt-5.4", env, "")
	wantArgv := []string{
		"codex",
		"-c", `model_provider="persona-launcher"`,
		"-c", `model_providers.persona-launcher={name="Persona launcher",base_url="https://opencode.ai/inference/go/openai/v1",wire_api="responses",env_key="OPENAI_API_KEY",env_http_headers={"x-opencode-session"="PERSONA_OPENCODE_SESSION"}}`,
		"-m", "gpt-5.4",
		"exec", "hi",
	}
	if !slices.Equal(argv, wantArgv) {
		t.Fatalf("launchArgv = %v, want %v", argv, wantArgv)
	}

	// Explicit x-opencode-session in custom Headers overrides the derived ID.
	withHdr := oc
	withHdr.Headers = "X-OpenCode-Session: ses-explicit"
	if got := envValue(managedEnv(withHdr), openCodeSessionEnv); got != "ses-explicit" {
		t.Fatalf("explicit header session = %q, want ses-explicit", got)
	}

	// Catalog probe attaches the same per-provider x-opencode-session header.
	catIn, ok := codexCatalogInputOf(oc)
	if !ok {
		t.Fatal("codexCatalogInputOf failed")
	}
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	var mu sync.Mutex
	var gotHeaders []string
	http.DefaultTransport = nodeRoundTrip(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		gotHeaders = append(gotHeaders, r.URL.Path+"="+r.Header.Get(modelhttp.OpenCodeSessionHeader))
		mu.Unlock()
		if r.Header.Get(modelhttp.OpenCodeSessionHeader) == "" {
			return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"type":"error"}`)), Request: r}, nil
		}
		if strings.HasSuffix(r.URL.Path, "/models") {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(codexCatalogBody)), Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})
	catalogURL, sure := probeCodexCatalog(context.Background(), catIn.base, catIn.key, "0.169.0", catIn.env)
	if catalogURL != "https://opencode.ai/inference/go/openai/v1/models" || !sure {
		t.Fatalf("probeCodexCatalog = (%q, %v)", catalogURL, sure)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, entry := range gotHeaders {
		if !strings.HasSuffix(entry, "="+wantSession) {
			t.Fatalf("catalog probe header mismatch: %v", gotHeaders)
		}
	}

	// pi provider registration also carries x-opencode-session on OpenCode Go Codex and Claude entries.
	piProv, ok := piProviderOf(oc)
	if !ok || piProv.Headers[modelhttp.OpenCodeSessionHeader] != wantSession {
		t.Fatalf("piProviderOf = %+v, %v", piProv, ok)
	}
}
