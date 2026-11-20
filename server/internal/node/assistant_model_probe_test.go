package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestLauncherModelProbeUserAgent(t *testing.T) {
	oldModels := assistantRecoveryModels
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{}, nil
	}
	t.Cleanup(func() { assistantRecoveryModels = oldModels })

	for _, tc := range []struct {
		name, launcherHeaders, userHeaders, flagHeaders, want string
		userHeadersSet, flagHeadersSet                        bool
	}{
		{name: "default", want: modelhttp.UserAgent()},
		{name: "launcher", launcherHeaders: "User-Agent: launcher", want: "launcher"},
		{name: "user settings override launcher", launcherHeaders: "User-Agent: launcher", userHeaders: "user-agent: user", want: "user"},
		{name: "flag overrides user", launcherHeaders: "User-Agent: launcher", userHeaders: "User-Agent: user", flagHeaders: "X-Unrelated: private\nUSER-AGENT: flag\nx-api-key: wrong", want: "flag"},
		{name: "empty user headers override launcher", launcherHeaders: "User-Agent: launcher", userHeadersSet: true, want: modelhttp.UserAgent()},
		{name: "empty flag headers override user and launcher", launcherHeaders: "User-Agent: launcher", userHeaders: "User-Agent: user", flagHeadersSet: true, want: modelhttp.UserAgent()},
		{name: "empty user agent suppresses default", flagHeaders: "uSeR-aGeNt:   ", want: ""},
		{name: "no user agent in selected headers", launcherHeaders: "User-Agent: launcher", flagHeaders: "X-Unrelated: private", want: modelhttp.UserAgent()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got http.Header
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer gw.Close()

			home := t.TempDir()
			writeSettings := func(path, headers string, set bool) {
				t.Helper()
				if headers == "" && !set {
					return
				}
				data, err := json.Marshal(map[string]any{"env": map[string]string{"ANTHROPIC_CUSTOM_HEADERS": headers}})
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, path, string(data))
			}
			writeSettings(filepath.Join(home, ".claude", "settings.json"), tc.userHeaders, tc.userHeadersSet)
			flagPath := filepath.Join(home, "launcher-settings.json")
			writeSettings(flagPath, tc.flagHeaders, tc.flagHeadersSet)
			scriptPath := filepath.Join(home, "launcher")
			script := fmt.Sprintf("#!/bin/sh\nexport ANTHROPIC_BASE_URL=%q\nexport ANTHROPIC_API_KEY=probe-key\nexport ANTHROPIC_CUSTOM_HEADERS=%q\nexec claude --settings %q \"$@\"\n", gw.URL, tc.launcherHeaders, flagPath)
			writeScript(t, scriptPath, script, 0o755)
			withScripts(t, map[string]string{"probe-launcher": scriptPath})
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			if !assistantModelProbe(context.Background(), n, "probe-launcher", "primary-model") {
				t.Fatal("probe failed to reach the gateway")
			}
			if got.Get("User-Agent") != tc.want {
				t.Errorf("User-Agent = %q, want %q", got.Get("User-Agent"), tc.want)
			}
			if tc.want == "" && len(got.Values("User-Agent")) != 0 {
				t.Errorf("empty User-Agent should not be sent, got %q", got.Values("User-Agent"))
			}
			wantUnrelated := ""
			for _, configured := range []string{tc.launcherHeaders, tc.userHeaders, tc.flagHeaders} {
				if strings.Contains(configured, "X-Unrelated:") {
					wantUnrelated = "private"
				}
			}
			if got.Get("X-Unrelated") != wantUnrelated || got.Get("X-Api-Key") != "probe-key" {
				t.Errorf("custom headers or credential: %v", got)
			}
		})
	}
}

// The configured-model availability probe talks to the launcher's real gateway. A 429 or 5xx keeps the conversation on
// its fallback; once the gateway answers 2xx the next request may return to the configured model (#7824).
func TestProbeGatewayReflectsRecovery(t *testing.T) {
	var requests atomic.Int32
	var healthy atomic.Bool
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/messages" {
			t.Errorf("probe path %q, want /v1/messages", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "probe-key" && r.Header.Get("Authorization") != "Bearer probe-key" {
			t.Error("the probe did not carry the gateway key")
		}
		if !healthy.Load() {
			w.Header().Set("x-should-retry", "false")
			http.Error(w, `{"error":{"code":"quota_exhausted","message":"an account becomes available again at 23:59 UTC (in 1m)"}}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","model":"primary-model","content":[],"stop_reason":"end_turn","usage":{}}`))
	}))
	defer gw.Close()

	// The shared status rule the real probe uses.
	if probeStatusOK(http.StatusTooManyRequests) || probeStatusOK(http.StatusBadGateway) {
		t.Fatal("a 429/5xx must not count as available")
	}
	if !probeStatusOK(http.StatusOK) {
		t.Fatal("a 2xx must count as available")
	}

	ctx := context.Background()
	if probeGateway(ctx, gw.URL, "probe-key", "primary-model", "") {
		t.Fatal("the probe said available while the gateway answers 429")
	}
	if requests.Load() == 0 {
		t.Fatal("the probe never reached the gateway")
	}

	healthy.Store(true)
	if !probeGateway(ctx, gw.URL, "probe-key", "primary-model", "") {
		t.Fatal("the probe did not see the recovered gateway")
	}

	// A gateway without a key, or a bad address, never guesses.
	if probeGateway(ctx, gw.URL, "", "primary-model", "") {
		t.Fatal("a probe without a key must fail closed")
	}
	if probeGateway(ctx, "://not a url", "probe-key", "primary-model", "") {
		t.Fatal("a bad gateway URL must fail closed")
	}
}
