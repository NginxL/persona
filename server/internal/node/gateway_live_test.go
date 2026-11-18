//go:build gatewaylive

package node

// A live check of a managed provider behind a gateway (#10365) with the machine's real claude: tool search off, where
// Claude Code would otherwise send deferred tools the gateway's edge cannot carry, and no tier alias sent as a model's
// name. An in-process edge emulation refuses what such an edge refuses (a defer_loading tool, a tool_search_* tool, a
// tool_reference block) with the 400 one answered, keeps each request's model, and answers the rest itself, or, with
// GATEWAYLIVE_LAUNCHER naming a managed claude provider of this computer's node, passes them on to that provider's
// gateway with its key (asked of the local node as `personad launch` asks; kept in memory, never printed). Claude gets
// a made-up home and config directory in a scratch directory, removed after. Never run by CI: it needs the gatewaylive
// build tag and GATEWAYLIVE=1 (GATEWAYLIVE_CLAUDE: another claude than the one on PATH; GATEWAYLIVE_MODEL: the
// provider's model to run, else the launcher's own; GATEWAYLIVE_MCP: a --mcp-config for a tool server, which Claude
// Code defers like the user's own servers).
//
//	cd server && GATEWAYLIVE=1 go test -tags gatewaylive -run TestGatewayToolSearchLive -v -count=1 ./internal/node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// edgeRefusal is what the gateway edge answered (#10365).
const edgeRefusal = "request is not representable on this edge: ToolSearch is not supported on this endpoint (defer_loading:true, tool_search_*, tool_reference); resend with all tools loaded"

// liveEdge is the edge emulation: upstream "" answers each message request with a short reply.
type liveEdge struct {
	upstream *url.URL
	mu       sync.Mutex
	seen     []edgeRequest
}

type edgeRequest struct {
	model    string
	deferred int
	refused  bool
}

func (e *liveEdge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/messages") && !strings.Contains(r.URL.Path, "count_tokens") {
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
			Tools  []struct {
				Name         string `json:"name"`
				Type         string `json:"type"`
				DeferLoading bool   `json:"defer_loading"`
			} `json:"tools"`
			Messages json.RawMessage `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		seen := edgeRequest{model: req.Model}
		for _, t := range req.Tools {
			if t.DeferLoading || strings.HasPrefix(t.Name, "tool_search") || strings.HasPrefix(t.Type, "tool_search") {
				seen.deferred++
			}
		}
		seen.refused = seen.deferred > 0 || bytes.Contains(req.Messages, []byte(`"tool_reference"`))
		e.mu.Lock()
		e.seen = append(e.seen, seen)
		e.mu.Unlock()
		if seen.refused {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": edgeRefusal}})
			return
		}
		if e.upstream == nil {
			gatewayLiveReply(w, req.Model, req.Stream)
			return
		}
	} else if e.upstream == nil {
		if strings.Contains(r.URL.Path, "count_tokens") {
			_ = json.NewEncoder(w).Encode(map[string]any{"input_tokens": 1})
			return
		}
		http.NotFound(w, r)
		return
	}
	e.forward(w, r, body)
}

// forward passes the request on to the upstream gateway as it came, its headers (the key among them) included.
func (e *liveEdge) forward(w http.ResponseWriter, r *http.Request, body []byte) {
	target := *e.upstream
	target.Path = strings.TrimSuffix(target.Path, "/") + r.URL.Path
	target.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, v := range r.Header {
		if k != "Host" && k != "Content-Length" && k != "Connection" && k != "Accept-Encoding" {
			req.Header[k] = v
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		if k != "Content-Length" && k != "Transfer-Encoding" && k != "Connection" {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// gatewayLiveReply is a model's short answer, streamed when the request asks for it.
func gatewayLiveReply(w http.ResponseWriter, model string, stream bool) {
	message := map[string]any{"id": "msg_live", "type": "message", "role": "assistant", "model": model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}
	if !stream {
		message["content"] = []any{map[string]any{"type": "text", "text": "EDGE-OK"}}
		message["stop_reason"] = "end_turn"
		_ = json.NewEncoder(w).Encode(message)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range []map[string]any{
		{"type": "message_start", "message": message},
		{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
		{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "EDGE-OK"}},
		{"type": "content_block_stop", "index": 0},
		{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}},
		{"type": "message_stop"},
	} {
		raw, _ := json.Marshal(e)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], raw)
	}
}

// liveProvider is the provider the check runs: the edge as its Base URL, and, forwarding, the named launcher's key and
// model as the local node gives them (never printed); its Opus field holds the alias that asked a gateway for "opus".
func liveProvider(t *testing.T) (proto.ManagedLauncher, *url.URL) {
	t.Helper()
	l := proto.ManagedLauncher{Name: "gateway-live", Kind: "api", APIKey: "live-key-not-a-secret", Auth: "bearer", Model: "gateway-model", OpusModel: "opus"}
	name := os.Getenv("GATEWAYLIVE_LAUNCHER")
	if name == "" {
		return l, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	line, err := nodeclient.Dial(ctx, launcherEnvRequest(name))
	if err != nil {
		t.Skipf("no node to ask for %s: %v", name, err)
	}
	var resp struct {
		Agent string   `json:"agent"`
		Env   []string `json:"env"`
		Error string   `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil || resp.Error != "" || (resp.Agent != "" && resp.Agent != "claude") {
		t.Skipf("launcher %s: not a managed claude provider here", name)
	}
	up, err := url.Parse(envValue(resp.Env, "ANTHROPIC_BASE_URL"))
	if err != nil || up.Host == "" || anthropicEndpoint(up.String()) {
		t.Skipf("launcher %s: no gateway Base URL", name)
	}
	if token := envValue(resp.Env, "ANTHROPIC_AUTH_TOKEN"); token != "" {
		l.APIKey, l.Auth = token, "bearer"
	} else {
		l.APIKey, l.Auth = envValue(resp.Env, "ANTHROPIC_API_KEY"), "api_key"
	}
	l.Headers = envValue(resp.Env, "ANTHROPIC_CUSTOM_HEADERS")
	l.Model = os.Getenv("GATEWAYLIVE_MODEL")
	if l.Model == "" {
		l.Model = strings.TrimSuffix(envValue(resp.Env, "ANTHROPIC_MODEL"), oneMillionSuffix)
	}
	if l.Model == "" || isTierAlias(l.Model) {
		t.Skipf("launcher %s names no model of its gateway: set GATEWAYLIVE_MODEL", name)
	}
	return l, up
}

type gatewayLive struct {
	t        *testing.T
	claude   string
	scratch  string
	cwd      string
	base     []string // the made-up environment claude inherits (the assistant's process: tool search on)
	edge     *liveEdge
	provider proto.ManagedLauncher
}

func gatewayLiveSetup(t *testing.T) *gatewayLive {
	t.Helper()
	if os.Getenv("GATEWAYLIVE") != "1" {
		t.Skip("set GATEWAYLIVE=1 to run the machine's claude against a gateway edge emulation")
	}
	claude := os.Getenv("GATEWAYLIVE_CLAUDE")
	if claude == "" {
		var err error
		if claude, err = exec.LookPath("claude"); err != nil {
			t.Skip("no claude on PATH")
		}
	}
	scratch, err := os.MkdirTemp("", "gatewaylive-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	l, upstream := liveProvider(t)
	g := &gatewayLive{t: t, claude: claude, scratch: scratch, cwd: filepath.Join(scratch, "work"), edge: &liveEdge{upstream: upstream}}
	home, config := filepath.Join(scratch, "home"), filepath.Join(scratch, "config")
	for _, d := range []string{g.cwd, home, config} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(config, ".claude.json"), []byte(`{"hasCompletedOnboarding":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: g.edge}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	l.BaseURL = "http://" + ln.Addr().String()
	l.NoProxy = "127.0.0.1,localhost"
	g.provider = l
	// What the assistant's process gives `personad launch`: tool search on (#7371), and nothing of this machine's
	// gateway, token or settings. The directory of claude (and of the node it may run on) stays on PATH.
	path := filepath.Dir(claude) + ":/usr/local/bin:/usr/bin:/bin"
	if node, err := exec.LookPath("node"); err == nil {
		path = filepath.Dir(node) + ":" + path
	}
	g.base = []string{"PATH=" + path, "HOME=" + home, "CLAUDE_CONFIG_DIR=" + config, "TMPDIR=" + scratch, "ENABLE_TOOL_SEARCH=true",
		"DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "DISABLE_AUTOUPDATER=1"}
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} { // the edge's way out is its own
		if v := os.Getenv(k); v != "" {
			g.base = append(g.base, k+"="+v)
		}
	}
	return g
}

// run is `personad launch` of l with args as Launch builds it, without the node: the provider's environment over the
// inherited one (its clear list, ENABLE_TOOL_SEARCH among it when the provider sets one), its settings env as the
// --settings a start passes, and, with guard, a tier alias on the command line mapped (gatewayModelArgs). It answers
// claude's result and the requests the edge saw.
func (g *gatewayLive) run(l proto.ManagedLauncher, env map[string]string, guard bool, args ...string) (map[string]any, []edgeRequest) {
	g.t.Helper()
	list := envList(env)
	settings, _ := json.Marshal(map[string]any{"env": unpinnedOutput(pinnedEnv(env)), "enableArtifact": false})
	file := filepath.Join(g.scratch, "settings.json")
	if err := os.WriteFile(file, settings, 0o600); err != nil {
		g.t.Fatal(err)
	}
	if guard {
		args = gatewayModelArgs(args, list, func(msg string) { g.t.Log("personad launch: " + msg) })
	}
	argv := append([]string{"-p", "Reply with OK only.", "--output-format", "json", "--settings", file}, args...)
	if mcp := os.Getenv("GATEWAYLIVE_MCP"); mcp != "" {
		argv = append(argv, "--mcp-config", mcp)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.claude, argv...)
	cmd.Dir = g.cwd
	cmd.Env = append(withoutEnv(g.base, withProviderToolSearch(envClearFor(l), list)), list...)
	g.edge.mu.Lock()
	before := len(g.edge.seen)
	g.edge.mu.Unlock()
	out, _ := cmd.Output()
	var result map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		g.t.Fatalf("claude printed no result (%d bytes)", len(out))
	}
	g.edge.mu.Lock()
	defer g.edge.mu.Unlock()
	return result, append([]edgeRequest(nil), g.edge.seen[before:]...)
}

func TestGatewayToolSearchLive(t *testing.T) {
	g := gatewayLiveSetup(t)
	l := g.provider
	text := func(r map[string]any) string { s, _ := r["result"].(string); return s }
	failed := func(r map[string]any) bool { b, _ := r["is_error"].(bool); return b }

	// As before #10365: the process carries tool search on, and so does its settings env. The edge refuses, and the
	// words are what the node resends once and the hub gives tool_search_unsupported.
	before := providerEnv(l, "", "", false)
	before[toolSearchEnv] = "true"
	r, seen := g.run(l, before, false)
	refused := 0
	for _, s := range seen {
		if s.refused {
			refused++
		}
	}
	t.Logf("tool search on: is_error=%v, %d requests, %d refused", failed(r), len(seen), refused)
	if refused == 0 {
		t.Log("claude sent no deferred tool here (no tool server: set GATEWAYLIVE_MCP); the refusal words are not checked")
	} else if !failed(r) || !assistantwire.ToolSearchRefused(text(r)) {
		t.Fatalf("tool search on: is_error=%v, result %q is not a tool-search refusal", failed(r), text(r))
	}

	// The provider's environment now: tool search off over the inherited true, every tool up front, nothing refused.
	env := providerEnv(l, "", "", false)
	if env[toolSearchEnv] != "false" {
		t.Fatalf("providerEnv %s=%q", toolSearchEnv, env[toolSearchEnv])
	}
	r, seen = g.run(l, env, true)
	for _, s := range seen {
		if s.deferred > 0 || s.refused {
			t.Fatalf("tool search off: a request deferred %d tools (refused %v)", s.deferred, s.refused)
		}
	}
	if failed(r) || len(seen) == 0 {
		t.Fatalf("tool search off: is_error=%v after %d requests: %s", failed(r), len(seen), text(r))
	}
	t.Logf("tool search off: answered after %d requests, none deferred", len(seen))

	// --model opus: before, the Opus tier variable held the provider's Opus field ("opus") and the gateway was asked for
	// "opus" (404 on a real one); now the provider's own model.
	old := providerEnv(l, "", "", false)
	old["ANTHROPIC_DEFAULT_OPUS_MODEL"] = "opus"
	if _, seen = g.run(l, old, false, "--model", "opus"); len(seen) == 0 || seen[0].model != "opus" {
		t.Fatalf("as before: the gateway was asked for %+v, want \"opus\"", seen)
	}
	t.Logf("as before: the gateway was asked for %q", seen[0].model)
	_, seen = g.run(l, env, true, "--model", "opus")
	if len(seen) == 0 {
		t.Fatal("--model opus: no request reached the edge")
	}
	for _, s := range seen {
		if isTierAlias(strings.TrimSuffix(s.model, oneMillionSuffix)) {
			t.Fatalf("--model opus: the gateway was asked for %q", s.model)
		}
	}
	t.Logf("--model opus: the gateway was asked for %q", seen[len(seen)-1].model)
}
