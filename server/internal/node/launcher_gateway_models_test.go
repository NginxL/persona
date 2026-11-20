package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

// gatewayFixture is a gateway that lists models and records what it was sent.
func gatewayFixture(t *testing.T) (*httptest.Server, *http.Header, *int) {
	t.Helper()
	var got http.Header
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		got = r.Header.Clone()
		if r.URL.Path != "/v1/models" || r.URL.Query().Get("limit") != "1000" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"data":[`+
			`{"id":"claude-opus-4-8","display_name":"Claude Opus 4.8"},`+
			`{"id":"anthropic-compat/glm-5.3"},`+
			`{"id":"glm-5.3"},`+
			`{"id":"claude-opus-5-5"},`+
			`{"id":"vendor/claude-x","description":"a vendor's Claude"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &got, &calls
}

func gatewayNode(t *testing.T, settings string) *Node {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "profile"))
	t.Setenv(gatewayDiscoveryVar, "")
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy",
		"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS"} {
		t.Setenv(k, "")
	}
	if settings != "" {
		writeFile(t, filepath.Join(home, "profile", "settings.json"), settings)
	}
	return &Node{home: home, dir: filepath.Join(home, ".persona"), logger: log.New(io.Discard, "", 0)}
}

func modelIDs(models []proto.AgentModel) []string {
	var out []string
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}

// With discovery on in the user's settings, the node lists the gateway's models as Claude Code would: the launch's
// credential and headers, only ids with claude or anthropic in them, the name and description it gives.
func TestGatewayModelsFollowsClaudeCodeDiscovery(t *testing.T) {
	srv, got, _ := gatewayFixture(t)
	n := gatewayNode(t, `{"env":{"`+gatewayDiscoveryVar+`":"1"}}`)
	l := proto.ManagedLauncher{Name: "gw", BaseURL: srv.URL + "/v1", APIKey: "sk-test", Headers: "x-team: blue"}
	models := n.gatewayModels(context.Background(), l)
	if want := []string{"claude-opus-4-8", "anthropic-compat/glm-5.3", "claude-opus-5-5", "vendor/claude-x"}; !slices.Equal(modelIDs(models), want) {
		t.Fatalf("models %v, want %v", modelIDs(models), want)
	}
	if models[0].Name != "Claude Opus 4.8" || models[0].Description != "From gateway" || models[3].Description != "a vendor's Claude" {
		t.Fatalf("names: %+v", models)
	}
	if got.Get("x-api-key") != "sk-test" || got.Get("Authorization") != "" || got.Get("anthropic-version") == "" || got.Get("x-team") != "blue" {
		t.Fatalf("headers: %v", *got)
	}
	l.Auth = "bearer"
	n.gatewayModels(context.Background(), l)
	if got.Get("Authorization") != "Bearer sk-test" || got.Get("x-api-key") != "" {
		t.Fatalf("bearer headers: %v", *got)
	}
}

func TestGatewayModelsUserAgent(t *testing.T) {
	srv, got, _ := gatewayFixture(t)
	n := gatewayNode(t, `{"env":{"`+gatewayDiscoveryVar+`":"1"}}`)
	for _, tc := range []struct{ headers, want string }{
		{"", modelhttp.UserAgent()},
		{"user-agent: provider/1", "provider/1"},
		{"user-agent:", ""},
	} {
		l := proto.ManagedLauncher{Name: "gw", BaseURL: srv.URL, APIKey: "sk-test", Headers: tc.headers}
		if models := n.gatewayModels(context.Background(), l); len(models) == 0 {
			t.Fatal("gateway returned no models")
		}
		if ua := got.Get("User-Agent"); ua != tc.want {
			t.Errorf("headers %q: User-Agent = %q, want %q", tc.headers, ua, tc.want)
		}
	}
}

// Off (not set, or set off by the provider over the user's settings), without a key, or for another agent: the
// gateway is not asked.
func TestGatewayModelsOnlyWhenDiscoveryIsOn(t *testing.T) {
	srv, _, calls := gatewayFixture(t)
	l := proto.ManagedLauncher{Name: "gw", BaseURL: srv.URL, APIKey: "sk-test"}
	for _, tc := range []struct {
		name     string
		settings string
		l        proto.ManagedLauncher
	}{
		{"not set", ``, l},
		{"set off", `{"env":{"` + gatewayDiscoveryVar + `":"0"}}`, l},
		{"provider env turns it off", `{"env":{"` + gatewayDiscoveryVar + `":"1"}}`,
			proto.ManagedLauncher{Name: "gw", BaseURL: srv.URL, APIKey: "sk-test", Env: gatewayDiscoveryVar + "=0"}},
		{"no key", `{"env":{"` + gatewayDiscoveryVar + `":"1"}}`, proto.ManagedLauncher{Name: "gw", BaseURL: srv.URL}},
		// The user's settings carry another gateway's token: never sent to this one.
		{"only another gateway's key", `{"env":{"` + gatewayDiscoveryVar + `":"1","ANTHROPIC_AUTH_TOKEN":"theirs","ANTHROPIC_API_KEY":"theirs"}}`,
			proto.ManagedLauncher{Name: "gw", BaseURL: srv.URL}},
		{"codex", `{"env":{"` + gatewayDiscoveryVar + `":"1"}}`,
			proto.ManagedLauncher{Name: "gw", BaseURL: srv.URL, APIKey: "sk-test", Agent: "codex"}},
	} {
		n := gatewayNode(t, tc.settings)
		if got := n.gatewayModels(context.Background(), tc.l); got != nil {
			t.Errorf("%s: %v", tc.name, modelIDs(got))
		}
	}
	if *calls != 0 {
		t.Fatalf("the gateway was asked %d times", *calls)
	}
	// The provider's own env turns it on too.
	n := gatewayNode(t, ``)
	on := proto.ManagedLauncher{Name: "gw", BaseURL: srv.URL, APIKey: "sk-test", Env: gatewayDiscoveryVar + "=true"}
	if got := n.gatewayModels(context.Background(), on); len(got) == 0 {
		t.Fatal("provider env on: no models")
	}
}

// A gateway that does not answer with a list adds nothing.
func TestGatewayModelsIgnoresAFailedList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()
	n := gatewayNode(t, `{"env":{"`+gatewayDiscoveryVar+`":"1"}}`)
	if got := n.gatewayModels(context.Background(), proto.ManagedLauncher{Name: "gw", BaseURL: srv.URL, APIKey: "k"}); got != nil {
		t.Fatalf("got %v", modelIDs(got))
	}
}

// The gateway's models go after the CLI's list, without those it has already (by id or as an alias resolves, with or
// without [1m]), so a list the CLI made from its cache of this gateway is not doubled.
func TestWithGatewayModelsAddsWhatIsMissing(t *testing.T) {
	cli := []proto.AgentModel{
		{ID: "default", Default: true},
		{ID: "opus", Resolved: "claude-opus-5-5[1m]"},
		{ID: "claude-opus-4-8", Description: "From gateway"},
	}
	gw := []proto.AgentModel{{ID: "claude-opus-5-5"}, {ID: "claude-opus-4-8"}, {ID: "anthropic-compat/glm-5.3"}, {ID: "anthropic-compat/glm-5.3"}}
	if got, want := modelIDs(withGatewayModels(cli, gw)), []string{"default", "opus", "claude-opus-4-8", "anthropic-compat/glm-5.3"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := withGatewayModels(cli, nil); len(got) != len(cli) {
		t.Fatalf("nothing from the gateway: %v", modelIDs(got))
	}
}

func TestLauncherModelsCarriesOnlyExactGatewayEvidence(t *testing.T) {
	home := fakeCLIHome(t)
	n, _ := testNode(t)
	n.home, n.dir, n.bin = home, filepath.Join(home, ".persona"), filepath.Join(home, "bin", "personad")
	writeFile(t, n.bin, `#!/bin/sh
read request
echo '{"type":"control_response","response":{"subtype":"success","request_id":"persona-models","response":{"models":[{"value":"relay/model-x-1m","resolvedModel":"claude-opus-5-5"},{"value":"relay/window","resolvedModel":"claude-opus-4-8[1m]"}]}}}'
`)
	if err := os.Chmod(n.bin, 0o700); err != nil {
		t.Fatal(err)
	}
	gateway, _, calls := gatewayFixture(t)
	for _, tc := range []struct {
		name, key string
		exact     bool
	}{
		{name: "with-key", key: "test-key", exact: true},
		{name: "no-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n.setManaged([]proto.ManagedLauncher{{Name: tc.name, BaseURL: gateway.URL, APIKey: tc.key, Env: gatewayDiscoveryVar + "=1"}})
			before := *calls
			for range 2 { // the second read reuses this launcher's catalog
				got, err := n.launcherModels(t.Context(), tc.name, "claude", false)
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"relay/model-x-1m", "relay/window"}
				if tc.exact {
					want = append(want, "anthropic-compat/glm-5.3", "vendor/claude-x")
				}
				if !slices.Equal(modelIDs(got.Models), want) {
					t.Fatalf("model ids = %v, want %v", modelIDs(got.Models), want)
				}
				wire, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("launchers.models %s", wire)
				var response struct {
					Models []struct {
						ID              string
						ResolvedOffered *bool `json:"resolved_offered"`
					}
				}
				if err := json.Unmarshal(wire, &response); err != nil {
					t.Fatal(err)
				}
				for _, row := range response.Models {
					wantMark := tc.exact && row.ID == "relay/model-x-1m"
					if mark := row.ResolvedOffered; (mark != nil) != wantMark || mark != nil && !*mark {
						t.Fatalf("gateway evidence for %s: %s", row.ID, wire)
					}
				}
			}
			wantCalls := 0
			if tc.exact {
				wantCalls = 1
			}
			if *calls-before != wantCalls {
				t.Fatalf("gateway GETs = %d, want %d", *calls-before, wantCalls)
			}
		})
	}
}

// Anthropic's own API is not a gateway: Claude Code asks it for no list, and neither does the node.
func TestGatewayModelsSkipsAnthropicsOwnAPI(t *testing.T) {
	for base, want := range map[string]bool{"https://api.anthropic.com": true, "https://API.Anthropic.com/v1": true,
		"https://gw.example.com": false, "https://anthropic.example.com": false} {
		if got := firstPartyHost(base); got != want {
			t.Errorf("%s: %v", base, got)
		}
	}
}
