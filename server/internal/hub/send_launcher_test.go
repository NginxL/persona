package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// assistantStore answers the "assistant" setting with cfg, and keeps the others written to it (the assistant's).
type assistantStore struct {
	*memStore
	cfg  assistant.Settings
	mu   sync.Mutex
	vals map[string][]byte
}

func (s *assistantStore) Setting(_ context.Context, key string, out any) (bool, error) {
	if key != "assistant" {
		s.mu.Lock()
		b, ok := s.vals[key]
		s.mu.Unlock()
		if !ok {
			return false, nil
		}
		return true, json.Unmarshal(b, out)
	}
	b, _ := json.Marshal(s.cfg)
	return true, json.Unmarshal(b, out)
}

func (s *assistantStore) PutSetting(_ context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vals == nil {
		s.vals = map[string][]byte{}
	}
	s.vals[key] = b
	return nil
}

// A message to Persona' main conversation without a launcher goes out on "Settings › Assistant"'s launcher (it used to keep the
// one it started with, so switching seemed to do nothing), also when it last ran on another one (#1518: a follow-up
// had moved it onto plain claude, and every message after stayed there); a launcher the app picked wins; other
// conversations are left alone.
func TestMainConversationFollowsTheAssistantLauncher(t *testing.T) {
	h := newTestHub(t)
	h.store = &assistantStore{memStore: newMemStore(), cfg: assistant.Settings{MachineID: "box", Launcher: "ccc", Main: "main-1"}}
	m := newMachine(h, MachineConfig{ID: "box", Name: "box",
		LauncherRecovery: &assistantwire.LauncherRecovery{WaitSeconds: 1}})
	h.byID["box"] = m
	h.machines = append(h.machines, m)
	hubIn, nodeOut := io.Pipe()
	nodeIn, hubOut := io.Pipe()
	hubPeer, nodePeer := rpc.New("h", hubOut), rpc.New("n", nodeOut)
	var mu sync.Mutex
	var got map[string]any
	nodePeer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "session.send":
			mu.Lock()
			got = map[string]any{}
			_ = json.Unmarshal(params, &got)
			mu.Unlock()
		case "launchers.list":
			return map[string]any{"launchers": []any{map[string]any{"name": "sss"}, map[string]any{"name": "other"},
				map[string]any{"name": "ccc"}}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = hubPeer.Serve(ctx, hubIn) }()
	go func() { _ = nodePeer.Serve(ctx, nodeIn) }()
	m.mu.Lock()
	m.peer, m.online = hubPeer, true
	m.info.Home = "/home/u" // its assistant conversations are told apart by their directory (the manager decorates the lists)
	m.mu.Unlock()

	m.mu.Lock()
	m.sessions = []proto.Session{{SessionID: "main-1", Launcher: "claude", Model: "stale", Assistant: true, Cwd: "/home/u/.persona/assistant"},
		{SessionID: "side-2", Launcher: "sss", Assistant: true, Cwd: "/home/u/.persona/assistant"},
		{SessionID: "side-3", Launcher: "gone", Assistant: true, Cwd: "/home/u/.persona/assistant"},
		{SessionID: "daily-4", Launcher: "sss", Assistant: true, Cwd: "/home/u/.persona/assistant"}}
	m.mu.Unlock()
	seedTasks(t, h, assistant.Task{ID: "r", Title: "早报", Repeat: "daily", Time: "09:00", Enabled: true, RunMachineID: "box", RunSessionID: "daily-4"})
	connectAssistant(t, h) // the hub knows a task's own conversation from what the assistant reports

	mux := h.routes()
	for _, c := range []struct{ sid, body, want, wantModel string }{
		{"main-1", `{"text":"hi"}`, "ccc", ""},
		{"main-1", `{"text":"hi","launcher":"other"}`, "other", ""},
		{"main-1", `{"text":"hi","model":"m3"}`, "ccc", "m3"},
		{"side-2", `{"text":"hi"}`, "sss", ""},
		// The chat's menu: the app sends both; a model alone keeps the conversation's launcher.
		{"side-2", `{"text":"hi","launcher":"other","model":"m2"}`, "other", "m2"},
		{"side-2", `{"text":"hi","model":"m9"}`, "sss", "m9"},
		// a scheduled task's own conversation follows the setting like the main one (#1544); a pick still wins
		{"daily-4", `{"text":"hi"}`, "ccc", ""},
		{"daily-4", `{"text":"hi","launcher":"other"}`, "other", ""},
	} {
		if code, out := call(t, mux, "POST", "/api/v1/sessions/box/"+c.sid+"/messages", h.cfg.Token, c.body); code != 200 {
			t.Fatalf("%s: %d %v", c.sid, code, out)
		}
		mu.Lock()
		l, _ := got["launcher"].(string)
		model, _ := got["model"].(string)
		fp, _ := got["model_fingerprint"].(string)
		mu.Unlock()
		if l != c.want || model != c.wantModel {
			t.Errorf("%s %s: launcher %q model %q, want %q %q", c.sid, c.body, l, model, c.want, c.wantModel)
		}
		if (l != "" || model != "") && fp == "" {
			t.Errorf("%s %s: no model fingerprint", c.sid, c.body)
		}
	}

	// A launcher deleted since the conversation started is refused after bounded rediscovery, not silently replaced.
	mu.Lock()
	got = nil
	mu.Unlock()
	if code, out := call(t, mux, "POST", "/api/v1/sessions/box/side-3/messages", h.cfg.Token, `{"text":"hi"}`); code != http.StatusBadRequest {
		t.Fatalf("gone side launcher: %d %v", code, out)
	}
	mu.Lock()
	sent := got["launcher"]
	mu.Unlock()
	if sent != nil {
		t.Fatalf("gone side launcher sent on %q", sent)
	}
}

// An answer-only device says something to a conversation it can see, no more: not one nobody has (that would start an
// agent), not which program runs it or what else a send can shape, and not keys for the terminal. A full device keeps
// all of those.
func TestAnswerRoleSendsOnlyText(t *testing.T) {
	h := newTestHub(t)
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
	m := newMachine(h, MachineConfig{ID: "box", Name: "box"})
	h.byID["box"] = m
	h.machines = append(h.machines, m)
	hubIn, nodeOut := io.Pipe()
	nodeIn, hubOut := io.Pipe()
	hubPeer, nodePeer := rpc.New("h", hubOut), rpc.New("n", nodeOut)
	var mu sync.Mutex
	var got map[string]any
	nodePeer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == "session.send" {
			mu.Lock()
			got = map[string]any{}
			_ = json.Unmarshal(params, &got)
			mu.Unlock()
		}
		return map[string]any{"ok": true}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = hubPeer.Serve(ctx, hubIn) }()
	go func() { _ = nodePeer.Serve(ctx, nodeIn) }()
	m.mu.Lock()
	m.peer, m.online = hubPeer, true
	m.sessions = []proto.Session{{SessionID: "s1"}, {SessionID: "s2", Status: proto.StatusOffline}}
	m.mu.Unlock()
	connectAssistant(t, h) // a session resumed without a launcher runs on "New Agent"'s, the assistant's to say

	mux := h.routes()
	_, phone := call(t, mux, "POST", "/api/v1/auth/tokens", h.cfg.Token, `{"name":"iPhone","kind":"phone"}`)
	_, watch := call(t, mux, "POST", "/api/v1/auth/tokens", phone["token"].(string), `{"name":"Watch","kind":"watch"}`)
	const body = `{"text":"\u0003\u0003hi\r\nthere\t\u001b[201~","launcher":"picked","model":"m","model_fingerprint":"f","images":[{"data":"x"}],"clear_draft":"d","permission_mode":"bypassPermissions"}`
	if code, b := call(t, mux, "POST", "/api/v1/sessions/box/s1/messages", watch["token"].(string), body); code != 200 {
		t.Fatalf("%d %v", code, b)
	}
	mu.Lock()
	sent := got
	got = nil
	mu.Unlock()
	if sent["text"] != "hi\nthere\t[201~" || sent["session_id"] != "s1" {
		t.Fatalf("the watch's message: %q", sent)
	}
	for _, k := range []string{"launcher", "model", "model_fingerprint", "images", "clear_draft", "permission_mode"} {
		if _, ok := sent[k]; ok {
			t.Errorf("an answer-only device's send carries %s: %v", k, sent)
		}
	}
	if code, _ := call(t, mux, "POST", "/api/v1/sessions/box/deadbeef-0000-4000-8000-000000000001/messages", watch["token"].(string), `{"text":"hi"}`); code != http.StatusNotFound {
		t.Fatalf("an answer-only device's message to an id nobody has: %d", code)
	}
	for _, empty := range []string{`{}`, `{"text":"  "}`, `{"text":"\u0003\r"}`, `{"text":5}`} {
		if code, _ := call(t, mux, "POST", "/api/v1/sessions/box/s1/messages", watch["token"].(string), empty); code != http.StatusBadRequest {
			t.Fatalf("an answer-only device's message %s: %d", empty, code)
		}
	}
	if code, b := call(t, mux, "POST", "/api/v1/sessions/box/s2/messages", watch["token"].(string), `{"text":"hi"}`); code != 200 {
		t.Fatalf("an answer-only device's message to a conversation not running: %d %v", code, b)
	}
	if code, b := call(t, mux, "DELETE", "/api/v1/sessions/box/s2", phone["token"].(string), ""); code != 200 {
		t.Fatalf("delete: %d %v", code, b)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/sessions/box/s2/messages", watch["token"].(string), `{"text":"hi"}`); code != http.StatusNotFound {
		t.Fatalf("an answer-only device's message to a deleted conversation: %d", code)
	}
	mu.Lock()
	got = nil
	mu.Unlock()
	mu.Lock()
	sent = got
	mu.Unlock()
	if sent != nil {
		t.Fatalf("reached the node: %v", sent)
	}
	if code, b := call(t, mux, "POST", "/api/v1/sessions/box/deadbeef-0000-4000-8000-000000000001/messages", phone["token"].(string), `{"text":"\u0003hi","launcher":"picked"}`); code != 200 {
		t.Fatalf("%d %v", code, b)
	}
	mu.Lock()
	sent = got
	mu.Unlock()
	if sent["launcher"] != "picked" || sent["text"] != "\u0003hi" {
		t.Fatalf("a full device's send: %q", sent)
	}
}

// A conversation whose model the gateway dropped (claude-opus-5) falls back to the launcher's default instead of
// failing every turn with "There's an issue with the selected model".
func TestSendDropsAModelTheLauncherNoLongerOffers(t *testing.T) {
	for _, c := range []struct{ model, want string }{{"claude-opus-5", ""}, {"claude-opus-5-5", "claude-opus-5-5"}} {
		t.Run(c.model, func(t *testing.T) {
			h := newTestHub(t)
			m := newMachine(h, MachineConfig{ID: "box-" + c.model, Name: "box"})
			h.byID[m.cfg.ID] = m
			h.machines = append(h.machines, m)
			hubIn, nodeOut := io.Pipe()
			nodeIn, hubOut := io.Pipe()
			hubPeer, nodePeer := rpc.New("h", hubOut), rpc.New("n", nodeOut)
			var mu sync.Mutex
			var got map[string]any
			nodePeer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
				switch method {
				case "launchers.models":
					return map[string]any{"agent": "claude", "models": []any{map[string]any{"id": "claude-opus-5-5"}}}, nil
				case "launchers.list": // the launcher is still there; only the model is gone
					return map[string]any{"launchers": []any{map[string]any{"name": "l"}}}, nil
				case "session.send":
					mu.Lock()
					got = map[string]any{}
					_ = json.Unmarshal(params, &got)
					mu.Unlock()
				}
				return map[string]any{"ok": true}, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			go func() { _ = hubPeer.Serve(ctx, hubIn) }()
			go func() { _ = nodePeer.Serve(ctx, nodeIn) }()
			m.mu.Lock()
			m.peer, m.online = hubPeer, true
			m.info.Home = "/home/u"
			m.sessions = []proto.Session{{SessionID: "side", Launcher: "l", Model: c.model, Assistant: true, Cwd: "/home/u/.persona/assistant"}}
			m.mu.Unlock()
			connectAssistant(t, h) // what a message to its conversation runs on is its decision
			if code, out := call(t, h.routes(), "POST", "/api/v1/sessions/"+m.cfg.ID+"/side/messages", h.cfg.Token, `{"text":"hi"}`); code != 200 {
				t.Fatalf("%d %v", code, out)
			}
			mu.Lock()
			model, _ := got["model"].(string)
			mu.Unlock()
			if model != c.want {
				t.Errorf("model %q, want %q", model, c.want)
			}
		})
	}
}

// newAgentStore answers "New Agent"'s model setting (the assistant's to read) with b.
type newAgentStore struct {
	*memStore
	b assistantwire.ModelBinding
}

func (s *newAgentStore) Setting(_ context.Context, key string, out any) (bool, error) {
	if key != "model_config.agent.new" {
		return false, nil
	}
	raw, _ := json.Marshal(map[string]any{"revision": 1, "binding": s.b})
	return true, json.Unmarshal(raw, out)
}

// A message to a session that is not running and whose launcher nobody knows (started outside Persona) carries "New
// Agent's launcher and model, so the node resumes it on them instead of plain claude; a known launcher, a running
// session and a launcher the app picked are left alone. Which launcher and model that is is the manager's to say
// (agents.resume_params); while it is not connected the hub adds nothing.
func TestOfflineSessionWithoutLauncherResumesOnNewAgents(t *testing.T) {
	h := newTestHub(t)
	h.store = &newAgentStore{memStore: newMemStore(), b: assistantwire.ModelBinding{Provider: "launcher.work", Model: "m-1"}}
	m := newMachine(h, MachineConfig{ID: "box", Name: "box"})
	h.byID["box"] = m
	h.machines = append(h.machines, m)
	hubIn, nodeOut := io.Pipe()
	nodeIn, hubOut := io.Pipe()
	hubPeer, nodePeer := rpc.New("h", hubOut), rpc.New("n", nodeOut)
	var mu sync.Mutex
	var got map[string]any
	nodePeer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == "session.send" {
			mu.Lock()
			got = map[string]any{}
			_ = json.Unmarshal(params, &got)
			mu.Unlock()
		}
		return map[string]any{"ok": true}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = hubPeer.Serve(ctx, hubIn) }()
	go func() { _ = nodePeer.Serve(ctx, nodeIn) }()
	m.mu.Lock()
	m.peer, m.online = hubPeer, true
	m.sessions = []proto.Session{
		{SessionID: "gone-1", Status: proto.StatusOffline},
		{SessionID: "known-2", Status: proto.StatusOffline, Launcher: "mine", StartedByPersona: true},
		{SessionID: "live-3", Status: proto.StatusIdle},
		{SessionID: "codex-4", Status: proto.StatusOffline, Agent: "codex"},
	}
	m.mu.Unlock()

	mux := h.routes()
	send := func(sid, body string) (string, string) {
		t.Helper()
		if code, out := call(t, mux, "POST", "/api/v1/sessions/box/"+sid+"/messages", h.cfg.Token, body); code != 200 {
			t.Fatalf("%s: %d %v", sid, code, out)
		}
		mu.Lock()
		defer mu.Unlock()
		l, _ := got["launcher"].(string)
		model, _ := got["model"].(string)
		return l, model
	}
	// Without the manager the hub adds nothing (it does not reach the machine without it: devices_link.go).
	if l, model := send("gone-1", `{"text":"hi"}`); l != "" || model != "" {
		t.Errorf("without the manager: launcher %q model %q, want nothing added", l, model)
	}
	connectAssistant(t, h)
	for _, c := range []struct{ sid, body, launcher, model string }{
		{"gone-1", `{"text":"hi"}`, "work", "m-1"},
		{"gone-1", `{"text":"hi","launcher":"other"}`, "other", ""},
		{"known-2", `{"text":"hi"}`, "", ""},
		{"live-3", `{"text":"hi"}`, "", ""},
		{"codex-4", `{"text":"hi"}`, "", ""},
	} {
		if l, model := send(c.sid, c.body); l != c.launcher || model != c.model {
			t.Errorf("%s %s: launcher %q model %q, want %q %q", c.sid, c.body, l, model, c.launcher, c.model)
		}
	}
}
