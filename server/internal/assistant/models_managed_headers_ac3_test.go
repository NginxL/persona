package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const ac3ManagerHeaderValue = "example-value"

// A compatible managed launcher's manager-side HTTP fallback is an inference path too. It must carry the same complete
// header set as the launch, while an existing provider with no headers retains its old wire behavior.
func TestManagedLauncherHTTPFallbackSendsAllProviderHeaders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers string
		want    map[string]string
	}{
		{
			name: "no headers",
			want: map[string]string{"X-Custom-Header": "", "X-Team": ""},
		},
		{
			name:    "arbitrary headers",
			headers: "X-Custom-Header: " + ac3ManagerHeaderValue + "\nX-Team: blue",
			want:    map[string]string{"X-Custom-Header": ac3ManagerHeaderValue, "X-Team": "blue"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan http.Header, 1)
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got <- r.Header.Clone()
				_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
			}))
			defer gw.Close()

			s, _, _ := modelService(t)
			key, err := s.sealKey("synthetic-key")
			if err != nil {
				t.Fatal(err)
			}
			l := storedLauncher{ManagedLauncher: proto.ManagedLauncher{
				Name: "stored", Kind: "launcher", API: "messages", BaseURL: gw.URL, Model: "m",
			}, KeySealed: key}
			if tc.headers != "" {
				l.HeadersSealed, err = s.sealKey(tc.headers)
				if err != nil {
					t.Fatal(err)
				}
			}
			putLaunchers(t, s, l)
			cfg, err := s.apiConfig(context.Background(), ModelBinding{Provider: "launcher.stored"})
			if err != nil {
				t.Fatal(err)
			}
			answer, err := s.aiClient(cfg).Complete(context.Background(), "system", "hello", 8)
			if err != nil || answer != "ok" {
				t.Fatalf("answer = %q, error = %v", answer, err)
			}
			headers := <-got
			for name, want := range tc.want {
				if value := headers.Get(name); value != want {
					t.Errorf("%s = %q, want %q", name, value, want)
				}
			}
		})
	}
}

// Once arbitrary values are inference credentials, an upstream error that echoes one must not expose it through the
// manager's returned error (and any log built from that error).
func TestManagedLauncherHTTPFallbackRedactsProviderHeaderValues(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"rejected ` + ac3ManagerHeaderValue + `"}}`))
	}))
	defer gw.Close()

	s, _, _ := modelService(t)
	key, err := s.sealKey("synthetic-key")
	if err != nil {
		t.Fatal(err)
	}
	headers, err := s.sealKey("X-Custom-Header: " + ac3ManagerHeaderValue)
	if err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, s, storedLauncher{
		ManagedLauncher: proto.ManagedLauncher{Name: "stored", Kind: "launcher", API: "messages", BaseURL: gw.URL, Model: "m"},
		KeySealed:       key,
		HeadersSealed:   headers,
	})
	cfg, err := s.apiConfig(context.Background(), ModelBinding{Provider: "launcher.stored"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.aiClient(cfg).Complete(context.Background(), "system", "hello", 8)
	if err == nil {
		t.Fatal("the synthetic gateway failure was ignored")
	}
	if strings.Contains(err.Error(), ac3ManagerHeaderValue) {
		t.Fatal("inference error exposed an arbitrary provider header value")
	}
}

// The gateway's successful answer proves only that the probe carried a header. It must not be presented as proof that
// an unsupported selected runtime/model path will carry that header on a real launch.
func TestProviderTestManagedHeaderSupport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agent     string
		api       string
		model     string
		headers   bool
		wantOK    bool
		wantCalls int32
	}{
		{name: "claude messages", agent: "claude", api: "messages", model: "claude-x", headers: true, wantOK: true, wantCalls: 1},
		{name: "codex responses", agent: "codex", api: "responses", model: "gpt-x", headers: true, wantOK: true, wantCalls: 1},
		{name: "omp anthropic compatible gateway", agent: "omp", api: "messages", model: "claude-x", headers: true, wantOK: true, wantCalls: 1},
		{name: "antigravity rejects arbitrary headers", agent: "agy", api: "messages", model: "gemini-x", headers: true, wantOK: false, wantCalls: 0},
		{name: "omp responses rejects anthropic-only headers", agent: "omp", api: "responses", model: "gpt-x", headers: true, wantOK: false, wantCalls: 0},
		{name: "antigravity no headers remains testable", agent: "agy", api: "messages", model: "gemini-x", wantOK: true, wantCalls: 1},
		{name: "omp responses no headers remains testable", agent: "omp", api: "responses", model: "gpt-x", wantOK: true, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.headers && r.Header.Get("X-Custom-Header") != ac3ManagerHeaderValue {
					http.Error(w, "required provider header missing", http.StatusUnauthorized)
					return
				}
				if tc.api == "responses" {
					_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
					return
				}
				_, _ = w.Write([]byte(`{"type":"message","content":[]}`))
			}))
			defer gw.Close()

			s, _, _ := modelService(t)
			key, err := s.sealKey("synthetic-key")
			if err != nil {
				t.Fatal(err)
			}
			l := storedLauncher{ManagedLauncher: proto.ManagedLauncher{
				Name: "stored", Kind: "launcher", Agent: tc.agent, API: tc.api, BaseURL: gw.URL + "/v1", Model: tc.model,
			}, KeySealed: key}
			if tc.headers {
				l.HeadersSealed, err = s.sealKey("X-Custom-Header: " + ac3ManagerHeaderValue)
				if err != nil {
					t.Fatal(err)
				}
			}
			putLaunchers(t, s, l)
			out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{Name: "stored"})
			if got, _ := out["ok"].(bool); got != tc.wantOK {
				t.Fatalf("connection test ok = %v, want %v (error: %v)", got, tc.wantOK, out["error"])
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("gateway calls = %d, want %d", got, tc.wantCalls)
			}
			if !tc.wantOK {
				message, _ := out["error"].(string)
				if !strings.Contains(strings.ToLower(message), "header") {
					t.Fatalf("connection test did not explain its header limitation: %v", out["error"])
				}
				if strings.Contains(message, ac3ManagerHeaderValue) {
					t.Fatal("connection test exposed a provider header value")
				}
			}
		})
	}
}
