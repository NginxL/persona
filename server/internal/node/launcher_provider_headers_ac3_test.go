package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

const ac3HeaderValue = "example-value"

// Supported managed launchers must hand every provider header to the mechanism their real model calls use. pi is
// covered at its provider-registration boundary; it does not have a separate managed-launcher agent type.
func TestManagedProviderHeadersReachSupportedLaunches(t *testing.T) {
	raw := "X-Custom-Header: " + ac3HeaderValue + "\nX-Team: blue"
	want := map[string]string{"X-Custom-Header": ac3HeaderValue, "X-Team": "blue"}

	for _, agent := range []string{"claude", "omp"} {
		t.Run(agent, func(t *testing.T) {
			l := proto.ManagedLauncher{
				Name: "provider", Agent: agent, Kind: "launcher", API: "messages",
				BaseURL: "https://gw.example.com", APIKey: "sk-test", Model: "claude-x", Headers: raw,
			}
			if got := envValue(managedEnv(l), "ANTHROPIC_CUSTOM_HEADERS"); got != raw {
				t.Fatalf("launch custom headers = %q, want every configured line", got)
			}
			if agent == "claude" {
				if got := managedSettingsEnv(l)["ANTHROPIC_CUSTOM_HEADERS"]; got != raw {
					t.Fatalf("assistant settings custom headers = %q, want every configured line", got)
				}
			}
		})
	}

	t.Run("codex", func(t *testing.T) {
		l := proto.ManagedLauncher{
			Name: "provider", Agent: "codex", Kind: "launcher", API: "responses",
			BaseURL: "https://gw.example.com/v1", APIKey: "sk-test", Model: "gpt-x", Headers: raw,
		}
		bindings, env := ac3CodexHeaderBindings(t, l)
		ac3AssertCodexHeaderBindings(t, bindings, env, want)
	})

	for _, agent := range []string{"claude", "codex"} {
		t.Run("pi registration for "+agent, func(t *testing.T) {
			l := proto.ManagedLauncher{
				Name: "provider", Agent: agent, Kind: "launcher", BaseURL: "https://gw.example.com/v1",
				APIKey: "sk-test", Model: "model-x", Headers: raw,
			}
			if agent == "codex" {
				l.API = "responses"
			} else {
				l.API = "messages"
			}
			provider, ok := piProviderOf(l)
			if !ok {
				t.Fatal("pi rejected a compatible provider")
			}
			for name, value := range want {
				if got := provider.Headers[name]; got != value {
					t.Errorf("pi header %q = %q, want %q", name, got, value)
				}
			}
			if len(provider.Headers) != len(want) {
				t.Errorf("pi registered %d headers, want %d", len(provider.Headers), len(want))
			}
		})
	}
}

// An old provider with no headers keeps its launch behavior. OpenCode Go is the deliberate exception: its derived
// session header is still required, but no other header or header-value environment appears by default.
func TestManagedProviderHeadersNoHeaderDefault(t *testing.T) {
	for _, agent := range []string{"claude", "omp", "agy"} {
		t.Run(agent, func(t *testing.T) {
			l := proto.ManagedLauncher{
				Name: "provider", Agent: agent, Kind: "launcher", API: "messages",
				BaseURL: "https://gw.example.com", APIKey: "sk-test", Model: "model-x",
			}
			n := &Node{}
			n.setManaged([]proto.ManagedLauncher{l})
			answer := n.launcherEnv(l.Name)
			if answer["error"] != nil {
				t.Fatalf("a provider without headers was refused: %v", answer["error"])
			}
			env, ok := answer["env"].([]string)
			if !ok {
				t.Fatal("launch returned no environment")
			}
			if got := envValue(env, "ANTHROPIC_CUSTOM_HEADERS"); got != "" {
				t.Fatalf("unexpected custom headers in the default launch: %q", got)
			}
		})
	}

	t.Run("codex", func(t *testing.T) {
		l := proto.ManagedLauncher{
			Name: "provider", Agent: "codex", Kind: "launcher", API: "responses",
			BaseURL: "https://gw.example.com/v1", APIKey: "sk-test", Model: "gpt-x",
		}
		bindings, env := ac3CodexHeaderBindings(t, l)
		if len(bindings) != 0 {
			t.Fatal("a provider without headers gained env_http_headers")
		}
		wantEnv := []string{"OPENAI_BASE_URL=https://gw.example.com/v1", "OPENAI_API_KEY=sk-test"}
		if !slices.Equal(env, wantEnv) {
			t.Fatal("the no-header Codex environment changed")
		}
	})

	t.Run("pi registration", func(t *testing.T) {
		provider, ok := piProviderOf(proto.ManagedLauncher{
			Name: "provider", Kind: "launcher", API: "messages", BaseURL: "https://gw.example.com",
			APIKey: "sk-test", Model: "model-x",
		})
		if !ok || len(provider.Headers) != 0 {
			t.Fatal("pi gained headers for a provider that has none")
		}
	})
}

// Codex takes header values through environment variables named by env_http_headers. Those generated names must not
// collapse distinct legal HTTP field names or overwrite a variable from the provider's own environment.
func TestCodexManagedHeaderEnvironmentNamesDoNotCollide(t *testing.T) {
	const existingName = "PERSONA_HEADER_X_TEAM_ID"
	l := proto.ManagedLauncher{
		Name: "provider", Agent: "codex", Kind: "launcher", API: "responses",
		BaseURL: "https://gw.example.com/v1", APIKey: "sk-test", Model: "gpt-x",
		Headers: "X-Team-ID: hyphen-value\nX_Team_ID: underscore-value",
		Env:     existingName + "=provider-environment-value",
	}
	bindings, env := ac3CodexHeaderBindings(t, l)
	ac3AssertCodexHeaderBindings(t, bindings, env, map[string]string{
		"X-Team-ID": "hyphen-value", "X_Team_ID": "underscore-value",
	})
	if got := envValue(env, existingName); got != "provider-environment-value" {
		t.Fatal("a generated header variable replaced the provider's own environment")
	}
	for header, variable := range bindings {
		if variable == existingName {
			t.Errorf("%q reused the provider environment variable %q", header, existingName)
		}
	}
}

// Arbitrary Codex headers extend, rather than replace, OpenCode Go's separate session-header path. An explicit session
// still wins, and a provider with no custom headers gets only the derived session.
func TestCodexManagedHeadersIncludeOpenCodeGoSession(t *testing.T) {
	base := proto.ManagedLauncher{
		Name: "provider", Agent: "codex", Kind: "launcher", API: "responses",
		BaseURL: "https://opencode.ai/inference/go/openai/v1/responses", APIKey: "sk-test", Model: "gpt-x",
	}
	derived := modelhttp.OpenCodeSessionID("launcher:" + base.Name)
	for _, tc := range []struct {
		name    string
		headers string
		want    map[string]string
	}{
		{
			name: "no custom headers",
			want: map[string]string{modelhttp.OpenCodeSessionHeader: derived},
		},
		{
			name:    "arbitrary header and derived session",
			headers: "X-Custom-Header: " + ac3HeaderValue,
			want: map[string]string{
				"X-Custom-Header": ac3HeaderValue, modelhttp.OpenCodeSessionHeader: derived,
			},
		},
		{
			name:    "arbitrary header and explicit session",
			headers: "X-Custom-Header: " + ac3HeaderValue + "\nX-OpenCode-Session: explicit-session",
			want: map[string]string{
				"X-Custom-Header": ac3HeaderValue, modelhttp.OpenCodeSessionHeader: "explicit-session",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := base
			l.Headers = tc.headers
			bindings, env := ac3CodexHeaderBindings(t, l)
			ac3AssertCodexHeaderBindings(t, bindings, env, tc.want)
		})
	}
}

// Antigravity has no arbitrary-header launch channel. omp's Anthropic variable does not make headers valid on its
// OpenAI Responses model path. Both must refuse the launch instead of silently dropping the configured values.
func TestManagedProviderHeadersRejectUnsupportedLaunch(t *testing.T) {
	for _, tc := range []struct {
		name string
		l    proto.ManagedLauncher
	}{
		{
			name: "antigravity",
			l: proto.ManagedLauncher{
				Name: "provider", Agent: "agy", Kind: "launcher", API: "messages",
				BaseURL: "https://gw.example.com", APIKey: "sk-test", Model: "gemini-x",
			},
		},
		{
			name: "omp openai responses model path",
			l: proto.ManagedLauncher{
				Name: "provider", Agent: "omp", Kind: "launcher", API: "responses",
				BaseURL: "https://gw.example.com/v1", APIKey: "sk-test", Model: "gpt-x",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := tc.l
			l.Headers = "X-Custom-Header: " + ac3HeaderValue
			n := &Node{}
			n.setManaged([]proto.ManagedLauncher{l})
			answer := n.launcherEnv(l.Name)
			message, _ := answer["error"].(string)
			if message == "" {
				t.Fatal("an unsupported runtime/model path accepted custom headers")
			}
			if !strings.Contains(strings.ToLower(message), "header") {
				t.Fatalf("launch refusal does not explain the header limitation: %q", message)
			}
			if strings.Contains(message, ac3HeaderValue) {
				t.Fatal("launch refusal exposed a provider header value")
			}
		})
	}
}

// The configured-model recovery probe must authenticate exactly like the launch it is trying to restore, not merely
// copy User-Agent and OpenCode's session header.
func TestProbeGatewaySendsEveryConfiguredHeader(t *testing.T) {
	calls := 0
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Custom-Header") != ac3HeaderValue || r.Header.Get("X-Team") != "blue" {
			http.Error(w, "required provider headers missing", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","model":"primary-model","content":[],"stop_reason":"end_turn","usage":{}}`))
	}))
	defer gw.Close()

	headers := "X-Custom-Header: " + ac3HeaderValue + "\nX-Team: blue"
	if !probeGateway(context.Background(), gw.URL, "probe-key", "primary-model", headers) {
		t.Fatal("recovery probe did not carry every configured provider header")
	}
	if probeGateway(context.Background(), gw.URL, "probe-key", "primary-model", "") {
		t.Fatal("header-dependent recovery probe passed without the configured headers")
	}
	if calls != 2 {
		t.Fatalf("gateway calls = %d, want 2", calls)
	}
}

// Header names can remain visible for configuration diagnostics, but arbitrary values are secrets too: a name without
// "key", "token", or "auth" must not make its value safe to expose through launcher details.
func TestLauncherDetailRedactsArbitraryProviderHeaderValues(t *testing.T) {
	detail := LauncherDetail(Probe{Agent: "claude", Env: map[string]string{
		"ANTHROPIC_CUSTOM_HEADERS": "X-Custom-Header: " + ac3HeaderValue + "\nX-Team: team-secret-value",
	}})
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{ac3HeaderValue, "team-secret-value"} {
		if strings.Contains(text, secret) {
			t.Errorf("launcher details exposed the value of an arbitrary provider header")
		}
	}
	for _, name := range []string{"X-Custom-Header", "X-Team"} {
		if !strings.Contains(text, name) {
			t.Errorf("launcher details hid the configured header name %q", name)
		}
	}
}

var ac3CodexHeaderPair = regexp.MustCompile(`("(?:[^"\\]|\\.)*")=("(?:[^"\\]|\\.)*")`)

// ac3CodexHeaderBindings reads the actual Codex provider argument and its launch environment together. Header names
// map to environment variable names; the assertions below then resolve those variables to the original values.
func ac3CodexHeaderBindings(t *testing.T, l proto.ManagedLauncher) (map[string]string, []string) {
	t.Helper()
	env := managedEnv(l)
	argv := launchArgv("codex", []string{"exec", "hi"}, nil, "", l.Model, env, "")
	bindings := map[string]string{}
	for _, arg := range argv {
		const marker = "env_http_headers={"
		start := strings.Index(arg, marker)
		if start < 0 {
			continue
		}
		body := strings.TrimPrefix(arg[start:], marker)
		end := strings.IndexByte(body, '}')
		if end < 0 {
			t.Fatal("unterminated env_http_headers in Codex launch arguments")
		}
		body = body[:end]
		for _, match := range ac3CodexHeaderPair.FindAllStringSubmatch(body, -1) {
			header, err1 := strconv.Unquote(match[1])
			variable, err2 := strconv.Unquote(match[2])
			if err1 != nil || err2 != nil {
				t.Fatal("invalid quoted env_http_headers entry")
			}
			bindings[header] = variable
		}
	}
	return bindings, env
}

func ac3AssertCodexHeaderBindings(t *testing.T, bindings map[string]string, env []string, want map[string]string) {
	t.Helper()
	if len(bindings) != len(want) {
		t.Errorf("Codex has %d env_http_headers entries, want %d", len(bindings), len(want))
	}
	used := map[string]string{}
	for header, value := range want {
		variable := ""
		for candidate, name := range bindings {
			if strings.EqualFold(candidate, header) {
				variable = name
				break
			}
		}
		if variable == "" {
			t.Errorf("Codex launch dropped provider header %q", header)
			continue
		}
		if previous, duplicate := used[variable]; duplicate {
			t.Errorf("provider headers %q and %q share environment variable %q", previous, header, variable)
		}
		used[variable] = header
		if got := envValue(env, variable); got != value {
			t.Errorf("Codex header %q resolves to %q, want %q", header, got, value)
		}
	}
}
