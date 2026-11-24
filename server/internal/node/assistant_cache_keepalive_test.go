package node

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

const cacheFixtureBody = `{"model":"example-model","system":[{"type":"text","text":"fixture prompt","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"fixture"}],"max_tokens":100,"stream":true}`

// The isolated fake CLI uses the last --settings env over its launcher's process
// env, as Claude does. It calls a fake provider, then emits that provider message
// id on the root stream. It never invokes a real CLI or reads real configuration.
const cacheWirePython = `import json, os, sys, urllib.request
args=sys.argv[1:]
base=os.environ.get('ANTHROPIC_BASE_URL','')
for i, arg in enumerate(args):
 if arg=='--settings' and i+1<len(args):
  try:
   with open(args[i+1]) as f: base=json.load(f).get('env',{}).get('ANTHROPIC_BASE_URL',base)
  except FileNotFoundError: pass
body=os.environ['CACHE_FIXTURE_BODY'].encode()
req=urllib.request.Request(base.rstrip('/')+'/v1/messages',body,{'Content-Type':'application/json','X-Api-Key':'fixture-key'})
with urllib.request.urlopen(req,timeout=5) as r: raw=r.read().decode()
for line in raw.splitlines():
 if line.startswith('data: '):
  e=json.loads(line[6:])
  if e.get('type')=='message_start':
   print(json.dumps({'type':'stream_event','parent_tool_use_id':('tool_child' if os.environ.get('CACHE_FIXTURE_CHILD') else None),'event':e}),flush=True)
`

func TestAssistantCacheProxyScopeAndLauncherPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		allow, managed, override bool
	}{{"main", true, false, false}, {"lead", true, true, false}, {"background", false, false, false}, {"worker", false, false, false}, {"child", false, false, false}, {"override", true, false, true}, {"inherited-child", true, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTestBash(t)
			t.Setenv("CACHE_FIXTURE_BODY", cacheFixtureBody)
			if tc.name == "inherited-child" {
				t.Setenv("CACHE_FIXTURE_CHILD", "1")
			}
			writeFile(t, filepath.Join(f.dir, "cache-wire.py"), cacheWirePython)
			script := strings.Replace(fakeCLI, `*'"type":"user"'*)`, `*'"type":"user"'*)`+"\n      python3 \"$CLI_FAKE_DIR/cache-wire.py\" \"$@\"", 1)
			writeScript(t, filepath.Join(os.Getenv("HOME"), "bin", "claude"), "#!/bin/sh\n"+script, 0o700)
			var requests atomic.Int32
			var marked atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/" && slices.Contains(portProbeAgents, r.Header.Get("User-Agent")) {
					// A Persona node on the machine running these tests (they run under its agents) finds this server
					// among an agent's ports and probes it (ports.go). It is not a provider request: counted, it let
					// the wait below pass before the CLI's own request and failed the byte check.
					return
				}
				b, _ := io.ReadAll(r.Body)
				requests.Add(1)
				if r.Header.Get("X-Persona-Request-Kind") == "cache-keepalive" {
					marked.Add(1)
					_, _ = io.WriteString(w, `{"usage":{"cache_read_input_tokens":1234,"cache_creation_input_tokens":0,"output_tokens":1}}`)
					return
				}
				if string(b) != cacheFixtureBody {
					t.Error("normal provider bytes changed")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"model\":\"example-model\",\"usage\":{\"input_tokens\":1,\"cache_creation_input_tokens\":1234}}}\n\n")
			}))
			defer upstream.Close()
			launcher := "gw"
			if tc.managed {
				f.fakeLaunch(t)
				f.n.setManaged([]proto.ManagedLauncher{{Name: launcher, Kind: "api", BaseURL: upstream.URL, Model: "example-model"}})
			} else {
				rc := fmt.Sprintf("gw() { export ANTHROPIC_BASE_URL=%q; command claude \"$@\"; }\n", upstream.URL)
				if tc.override {
					file := filepath.Join(os.Getenv("HOME"), "override.json")
					writeFile(t, file, fmt.Sprintf(`{"env":{"ANTHROPIC_BASE_URL":%q}}`, upstream.URL))
					rc = fmt.Sprintf("gw() { command claude \"$@\" --settings %q; }\n", file)
				}
				writeFile(t, filepath.Join(os.Getenv("HOME"), ".zshrc"), rc)
				writeFile(t, filepath.Join(os.Getenv("HOME"), ".bashrc"), rc)
			}
			e := cliExec()
			e.Model = "example-model"
			e.IsMain = tc.name == "main"
			e.FollowsSetting = tc.name == "lead" || tc.name == "background"
			policy := &assistantwire.CacheKeepalive{Enabled: true, IntervalMinutes: 55, MaxRequests: 24}
			if tc.allow {
				e.CacheKeepalive = policy
			}
			p, err := f.n.spawnClaude(newUUID(), t.TempDir(), launcher, false, false, e)
			if err != nil {
				t.Fatal(err)
			}
			defer f.n.stopAssistant(p.id)
			if (p.cacheProxy != nil) != tc.allow {
				t.Fatal("wrong process got a proxy")
			}
			// A send marks the conversation busy before it writes (writeQueuedAgain); the turn's result clears it. Without
			// that, f.idle below could pass before the node read the provider's message_start, and Tick found nothing yet.
			p.mu.Lock()
			p.busy = true
			p.mu.Unlock()
			if err := p.write([]byte(`{"type":"user","message":{"role":"user","content":"fixture"}}`)); err != nil {
				t.Fatal(err)
			}
			cliWait(t, "fake provider request", func() bool { return requests.Load() == 1 })
			f.idle(t, p.id)
			// The result clears busy before it sends the turn's last delta, the clear of its live text: wait for that too,
			// or it can land between the snapshots below and read as the refresh's output.
			cliWait(t, "the turn's live text cleared", func() bool {
				f.fh.mu.Lock()
				defer f.fh.mu.Unlock()
				return len(f.fh.deltas) > 0 && f.fh.deltas[len(f.fh.deltas)-1] == ""
			})
			var barrier any
			if err := f.n.peer.Call(context.Background(), "assistant.lark", map[string]any{}, &barrier); err != nil {
				t.Fatal(err)
			}
			f.fh.mu.Lock()
			deltas, interactions := len(f.fh.deltas), len(f.fh.asked)
			f.fh.mu.Unlock()
			before := f.file("stdin.jsonl")
			if p.cacheProxy != nil {
				p.cacheProxy.Tick(context.Background(), time.Now().Add(55*time.Minute), policy, true)
			}
			want := int32(0)
			if tc.allow && !tc.override && tc.name != "inherited-child" {
				want = 1
			}
			if marked.Load() != want {
				t.Fatalf("refreshes %d want %d", marked.Load(), want)
			}
			if f.file("stdin.jsonl") != before {
				t.Fatal("keepalive wrote a conversation input")
			}
			if err := f.n.peer.Call(context.Background(), "assistant.lark", map[string]any{}, &barrier); err != nil {
				t.Fatal(err)
			}
			f.fh.mu.Lock()
			afterDeltas, afterInteractions := len(f.fh.deltas), len(f.fh.asked)
			newDeltas := slices.Clone(f.fh.deltas[deltas:])
			f.fh.mu.Unlock()
			if afterDeltas != deltas || afterInteractions != interactions {
				t.Fatalf("refresh emitted UI output or interactions: deltas %q, interactions %d -> %d", newDeltas, interactions, afterInteractions)
			}
			if want == 1 {
				f.fh.mu.Lock()
				f.fh.model = map[string]any{"launcher": launcher, "model": e.Model, "model_fingerprint": e.Fingerprint, "is_main": e.IsMain, "cache_keepalive": policy}
				f.fh.mu.Unlock()
				f.n.sweepCacheWarm(context.Background(), time.Now().Add(110*time.Minute))
				waitCacheCheck(t, f.n, p.id)
				if marked.Load() != 2 {
					t.Fatal("authorized reaper did not refresh")
				}
				f.fh.mu.Lock()
				delete(f.fh.model, "cache_keepalive")
				f.fh.mu.Unlock()
				f.n.sweepCacheWarm(context.Background(), time.Now().Add(165*time.Minute))
				waitCacheCheck(t, f.n, p.id)
				if marked.Load() != 2 {
					t.Fatal("removed role kept refreshing")
				}
			}
			if strings.Contains(f.log.String(), "fixture-key") || strings.Contains(f.log.String(), "fixture prompt") {
				t.Fatal("private log")
			}
		})
	}
}
func TestAssistantCacheProxyExcludesUnsupportedAndSpare(t *testing.T) {
	f := cliTestBash(t)
	c := assistantCompaction{endpoint: "https://api.example.com", endpointKnown: true}
	on := &assistantwire.CacheKeepalive{Enabled: true, IntervalMinutes: 55, MaxRequests: 24}
	f.n.setManaged([]proto.ManagedLauncher{{Name: "codex-provider", Agent: "codex"}, {Name: "other-provider", Agent: "agy"}})
	for _, launcher := range []string{"codex-provider", "other-provider"} {
		t.Run(launcher, func(t *testing.T) {
			if p := f.n.startCacheWarm("example", launcher, c, modelExecution{CacheKeepalive: on}, false); p != nil {
				p.Close()
				t.Fatal("non-Claude launcher mounted a proxy")
			}
		})
	}
	for _, e := range []modelExecution{{IsMain: true}, {FollowsSetting: true}, {CacheKeepalive: on, Strict: true}} {
		if p := f.n.startCacheWarm("example", "claude", c, e, false); p != nil {
			p.Close()
			t.Fatal("excluded execution mounted proxy")
		}
	}
	if p := f.n.startCacheWarm("example", "claude", c, modelExecution{CacheKeepalive: on}, true); p != nil {
		p.Close()
		t.Fatal("spare/guest mounted proxy")
	}
	if p := f.n.startCacheWarm("example", proto.ExternalLauncherPrefix+strings.Repeat("a", 32), c, modelExecution{CacheKeepalive: on}, false); p != nil {
		p.Close()
		t.Fatal("external runtime mounted proxy")
	}
}

func waitCacheCheck(t *testing.T, n *Node, id string) {
	t.Helper()
	b := n.cacheWarmPool()
	cliWait(t, "cache policy check", func() bool { b.mu.Lock(); defer b.mu.Unlock(); e := b.entries[id]; return e == nil || !e.checking })
}

// A main or lead conversation on a Persona-managed Claude launcher whose gateway is an HTTPS address (#12786): the
// start mounts the proxy in its settings, which win over the provider's own ANTHROPIC_BASE_URL that `personad launch`
// puts in the process environment; the proxy forwards to that HTTPS gateway. A start the manager gave no role (as a
// session.send without one did) runs straight on the gateway.
func TestAssistantCacheProxyMountsForHTTPSManagedLauncher(t *testing.T) {
	const gateway = "https://gateway.example/anthropic"
	for _, tc := range []struct {
		name  string
		allow bool
	}{{"attested", true}, {"no-role", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTestBash(t)
			f.fakeLaunch(t)
			f.n.setManaged([]proto.ManagedLauncher{{Name: "gw", Kind: "api", BaseURL: gateway, Model: "example-model"}})
			e := cliExec()
			e.Model = "example-model"
			e.IsMain = true
			if tc.allow {
				e.CacheKeepalive = &assistantwire.CacheKeepalive{Enabled: true, IntervalMinutes: 55, MaxRequests: 24}
			}
			c := f.n.assistantCompactionPolicy("gw", t.TempDir(), "", e)
			if !c.endpointKnown || c.endpoint != gateway {
				t.Fatalf("managed endpoint %q known=%v", c.endpoint, c.endpointKnown)
			}
			id := newUUID()
			p, err := f.n.spawnClaude(id, t.TempDir(), "gw", false, false, e)
			if err != nil {
				t.Fatal(err)
			}
			defer f.n.stopAssistant(p.id)
			if (p.cacheProxy != nil) != tc.allow {
				t.Fatalf("proxy mounted: %v, want %v", p.cacheProxy != nil, tc.allow)
			}
			dir := f.n.dir
			if dir == "" {
				dir = filepath.Join(f.n.home, ".persona")
			}
			b, err := os.ReadFile(filepath.Join(dir, "launcher-settings", "assistant-compact-"+id+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var s struct {
				Env map[string]any `json:"env"`
			}
			if err := json.Unmarshal(b, &s); err != nil {
				t.Fatal(err)
			}
			base, _ := s.Env["ANTHROPIC_BASE_URL"].(string)
			if tc.allow {
				if base != p.cacheProxy.BaseURL() || !strings.HasPrefix(base, "http://127.0.0.1:") {
					t.Fatalf("settings base %q, want the proxy", base)
				}
				if s.Env[promptCaching1hEnv] != "1" {
					t.Fatalf("settings %s = %v: Claude Code would declare only a five-minute cache, which Replay refuses", promptCaching1hEnv, s.Env[promptCaching1hEnv])
				}
				b := f.n.cacheWarmPool()
				b.mu.Lock()
				_, pooled := b.entries[id]
				b.mu.Unlock()
				if !pooled {
					t.Fatal("proxy not registered for refreshes")
				}
			} else if strings.HasPrefix(base, "http://127.0.0.1:") {
				t.Fatalf("unattested start points at a proxy: %q", base)
			} else if _, set := s.Env[promptCaching1hEnv]; set {
				t.Fatalf("unattested start asks for one-hour cache writes")
			}
		})
	}
}
