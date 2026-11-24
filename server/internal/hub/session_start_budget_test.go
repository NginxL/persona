package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Codex and compatible variants return their session only after startup even when a client asks for async: the node gets the agent
// type the launcher resolves to, and the start's budget (startBudget) follows it. What the start runs on is the
// manager's (agents.start_params), whose calls carry no deadline over the bus: the budget is checked on the decision.
func TestSessionStartBudgetMatchesNodeStartup(t *testing.T) {
	for _, tc := range []struct {
		name       string
		agentType  string
		configured string
		async      bool
		short      bool
	}{
		{name: "default Claude async", async: true, short: true},
		{name: "explicit Claude async", agentType: "claude", async: true, short: true},
		{name: "explicit Codex async", agentType: "codex", async: true},
		{name: "explicit TRAE CLI async", agentType: "traex", async: true},
		{name: "explicit AGY async", agentType: "agy", async: true},
		{name: "configured Codex async", configured: "codex", async: true},
		{name: "configured TRAE CLI async", configured: "traex", async: true},
		{name: "Claude sync"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			connectAssistant(t, h) // it says what a start runs on; without it nothing starts
			startReceiptNode(t, h, func(_ context.Context, params json.RawMessage) (any, error) {
				var in struct {
					AgentType string `json:"agent_type"`
				}
				if err := json.Unmarshal(params, &in); err != nil {
					return nil, err
				}
				want := tc.agentType
				if want == "" {
					want = tc.configured
				}
				if in.AgentType != want {
					t.Errorf("node agent_type = %q, want %q", in.AgentType, want)
				}
				return map[string]any{"session": proto.Session{SessionID: "created"}}, nil
			})
			h.byID["machine"].cfg.Launchers = []assistantwire.ConfiguredLauncher{{Launcher: proto.Launcher{Name: "wrapper"}, AgentType: tc.configured}}
			body, err := json.Marshal(map[string]any{
				"machine_id": "machine", "cwd": "/code", "launcher": "wrapper", "prompt": "hello", "tab_name": "test",
				"agent_type": tc.agentType, "async": tc.async,
			})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(string(body)))
			w := httptest.NewRecorder()
			h.startSession(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("start = %d %s", w.Code, w.Body)
			}
			want := tc.agentType
			if want == "" {
				want = tc.configured
			}
			budget := startBudget(startsAsync(tc.async, "", "wrapper", want))
			min, max := 60*time.Second, 101*time.Second
			if tc.short {
				min, max = 10*time.Second, 16*time.Second
			}
			if budget < min || budget > max {
				t.Fatalf("operation budget = %s, want between %s and %s", budget, min, max)
			}
		})
	}
}

func TestDesktopCreationBudgetIncludesImportAndFirstMessage(t *testing.T) {
	for _, launcher := range []string{"claude-desktop", "codex-app"} {
		for _, async := range []bool{true, false} {
			if budget := startBudgetForLauncher(async, launcher); budget < 165*time.Second {
				t.Fatalf("%s async=%v: budget %s cannot complete native creation", launcher, async, budget)
			}
		}
	}
	if got := startBudgetForLauncher(true, "claude"); got != startBudget(true) {
		t.Fatalf("ordinary async startup budget changed: %s", got)
	}
}

// Observe the hub's actual context before the RPC transport drops its deadline.
type sessionStartBudgetCaller struct {
	nodeCaller
	beforeStart func(context.Context, any) error
}

func (c *sessionStartBudgetCaller) Call(ctx context.Context, method string, params, out any) error {
	if method == "session.start" {
		if err := c.beforeStart(ctx, params); err != nil {
			return err
		}
	}
	return c.nodeCaller.Call(ctx, method, params, out)
}

func observeSessionStartBudget(t *testing.T, m *Machine, before func(context.Context, any) error) {
	t.Helper()
	m.mu.Lock()
	original := m.peer
	m.peer = &sessionStartBudgetCaller{nodeCaller: original, beforeStart: before}
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		m.peer = original
		m.mu.Unlock()
	})
}

func TestSessionStartUsesManagerResolvedBudget(t *testing.T) {
	for _, launcher := range []string{"claude-desktop", "codex-app", "configured-codex"} {
		for _, parentMode := range []string{"ordinary", "deadline", "canceled"} {
			t.Run(launcher+"/"+parentMode, func(t *testing.T) {
				h := newTestHub(t)
				h.store = &newAgentStore{memStore: newMemStore(), b: assistantwire.ModelBinding{Provider: "launcher." + launcher}}
				startReceiptNode(t, h, func(context.Context, json.RawMessage) (any, error) {
					return map[string]any{"session": proto.Session{SessionID: "created"}}, nil
				})
				m := h.byID["machine"]
				if launcher == "configured-codex" {
					m.cfg.Launchers = []assistantwire.ConfiguredLauncher{{Launcher: proto.Launcher{Name: launcher}, AgentType: "codex"}}
				}
				connectAssistant(t, h)
				type contextKey struct{}
				parent := context.WithValue(context.Background(), contextKey{}, "request")
				var parentDeadline time.Time
				if parentMode == "deadline" {
					parentDeadline = time.Now().Add(30 * time.Second)
					var cancel context.CancelFunc
					parent, cancel = context.WithDeadline(parent, parentDeadline)
					t.Cleanup(cancel)
				}
				parent, cancelParent := context.WithCancel(parent)
				t.Cleanup(cancelParent)
				observed := false
				observeSessionStartBudget(t, m, func(ctx context.Context, raw any) error {
					observed = true
					params := raw.(map[string]any)
					if params["launcher"] != launcher || params["async"] != false {
						t.Errorf("resolved start = %+v, want %s synchronous", params, launcher)
					}
					if launcher == "configured-codex" && params["agent_type"] != "codex" {
						t.Errorf("resolved agent_type = %v, want codex", params["agent_type"])
					}
					deadline, ok := ctx.Deadline()
					if !ok {
						t.Error("node dispatch has no deadline")
					} else if !parentDeadline.IsZero() {
						if !deadline.Equal(parentDeadline) {
							t.Errorf("dispatch deadline = %s, want parent %s", deadline, parentDeadline)
						}
					} else {
						budget := 3 * time.Minute
						if launcher == "configured-codex" {
							budget = 100 * time.Second
						}
						if remaining := time.Until(deadline); remaining < budget-15*time.Second || remaining > budget {
							t.Errorf("dispatch budget = %s, want up to %s", remaining, budget)
						}
					}
					if ctx.Value(contextKey{}) != "request" {
						t.Error("request context value was lost")
					}
					if parentMode == "canceled" {
						cancelParent()
						if !errors.Is(ctx.Err(), context.Canceled) {
							t.Errorf("dispatch ignored request cancellation: %v", ctx.Err())
						}
						return ctx.Err()
					}
					return nil
				})
				r := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"machine_id":"machine","cwd":"/code","async":true,"prompt":"hello","tab_name":"test"}`)).WithContext(parent)
				w := httptest.NewRecorder()
				h.startSession(w, r)
				if !observed {
					t.Fatalf("start never reached node: %d %s", w.Code, w.Body)
				}
				if parentMode == "canceled" {
					if w.Code == http.StatusOK {
						t.Fatalf("canceled start succeeded: %s", w.Body)
					}
				} else if w.Code != http.StatusOK {
					t.Fatalf("start = %d %s", w.Code, w.Body)
				}
			})
		}
	}
}

func TestSessionStartResolvedSyncKeepsNameAndCompletion(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "manager disables async", true: "fallback changes inferred type"}[fallback], func(t *testing.T) {
			h := newTestHub(t)
			h.bus.idents = &memIdents{}
			mux := http.NewServeMux()
			h.busRoutes(mux)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			startReceiptNode(t, h, func(context.Context, json.RawMessage) (any, error) {
				return map[string]any{"session": proto.Session{SessionID: "created"}}, nil
			})
			m := h.byID["machine"]
			m.owner = "m-budget"
			h.cfg.Machines = []MachineConfig{m.cfg}
			h.devices.assign = map[string]string{m.cfg.ID: m.owner}
			h.devices.assignRead = true
			m.cfg.Launchers = []assistantwire.ConfiguredLauncher{
				{Launcher: proto.Launcher{Name: "old"}, AgentType: "claude"},
				{Launcher: proto.Launcher{Name: "resolved"}, AgentType: "codex"},
			}
			resolvedAt := make(chan time.Time, 1)
			names := make(chan string, 1)
			manager := connectGuardManager(t, h, srv.URL, m.owner, func(method string, raw json.RawMessage) (any, error) {
				switch method {
				case assistantwire.MethodAgentStartParams:
					var in assistantwire.AgentStartParams
					if err := json.Unmarshal(raw, &in); err != nil {
						return nil, err
					}
					if in.NameFrom != "" {
						t.Errorf("initial async resolution asked for name %q", in.NameFrom)
					}
					if fallback {
						in.Params["launcher"] = "resolved"
					} else {
						in.Params["async"] = false
					}
					resolvedAt <- time.Now()
					return map[string]any{"params": in.Params}, nil
				case assistantwire.MethodAgentTabName:
					var in struct {
						Prompt string `json:"prompt"`
					}
					if err := json.Unmarshal(raw, &in); err != nil {
						return nil, err
					}
					names <- in.Prompt
					return map[string]any{"name": "resolved-name"}, nil
				}
				return map[string]any{}, nil
			})
			events := make(chan string, 4)
			manager.mu.Lock()
			manager.onEvent = func(method string, _ json.RawMessage) {
				if method == assistantwire.EventStartEnded || method == assistantwire.EventModelUsed {
					events <- method
				}
			}
			manager.mu.Unlock()
			observed := false
			observeSessionStartBudget(t, m, func(ctx context.Context, raw any) error {
				observed = true
				params := raw.(map[string]any)
				if params["async"] != false || params["tab_name"] != "resolved-name" {
					t.Errorf("synchronous start not normalized: %+v", params)
				}
				if fallback && params["agent_type"] != "codex" {
					t.Errorf("fallback retained old inferred type: %+v", params)
				}
				deadline, ok := ctx.Deadline()
				if resolved := <-resolvedAt; !ok || deadline.After(resolved.Add(100*time.Second)) || time.Until(deadline) < 85*time.Second {
					t.Errorf("deadline %s should use the original start time and resolved synchronous budget", deadline)
				}
				return nil
			})
			r := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(`{"machine_id":"machine","cwd":"/code","launcher":"old","model":"chosen","async":true,"prompt":"hello"}`))
			w := httptest.NewRecorder()
			h.startSession(w, r)
			if w.Code != http.StatusOK || !observed {
				t.Fatalf("start = %d %s; observed=%v", w.Code, w.Body, observed)
			}
			select {
			case name := <-names:
				if name != "hello" {
					t.Errorf("name prompt = %q, want hello", name)
				}
			default:
				t.Error("owning manager was not asked to name the synchronous start")
			}
			seen := map[string]bool{}
			for len(seen) < 2 {
				select {
				case event := <-events:
					seen[event] = true
				case <-time.After(time.Second):
					t.Fatalf("synchronous completion events missing: %v", seen)
				}
			}
		})
	}
}
func TestSessionStartAgyUsesSynchronousBudget(t *testing.T) {
	for _, tc := range []struct {
		name, requested, resolved, agent string
		replyAt                          time.Duration
		wantOK                           bool
	}{
		{"named launcher", "agy", "agy", "", 20 * time.Second, true},
		{"manager resolved launcher", "", "agy-wrapper", "agy", 99 * time.Second, true},
		{"manager resolved launcher times out", "", "agy-wrapper", "agy", 101 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newTestHub(t)
				startReceiptNode(t, h, func(ctx context.Context, raw json.RawMessage) (any, error) {
					var params struct{ Launcher string }
					if err := json.Unmarshal(raw, &params); err != nil {
						return nil, err
					}
					if params.Launcher != tc.resolved {
						t.Errorf("node launcher = %q, want %q", params.Launcher, tc.resolved)
					}
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(tc.replyAt):
						return map[string]any{"session": proto.Session{SessionID: "created", Agent: "agy"}}, nil
					}
				})
				hubIn, managerOut := io.Pipe()
				managerIn, hubOut := io.Pipe()
				hubPeer, managerPeer := rpc.New("h", hubOut), rpc.New("m", managerOut)
				managerPeer.OnRequest = func(_ context.Context, method string, raw json.RawMessage) (any, error) {
					if method != assistantwire.MethodAgentStartParams {
						return nil, nil
					}
					var p assistantwire.AgentStartParams
					if err := json.Unmarshal(raw, &p); err != nil {
						return nil, err
					}
					p.Params["launcher"] = tc.resolved
					if tc.agent != "" {
						p.Params["agent_type"] = tc.agent
					}
					return map[string]any{"params": p.Params}, nil
				}
				t.Cleanup(func() {
					hubPeer.Close(io.EOF)
					managerPeer.Close(io.EOF)
					_ = hubIn.Close()
					_ = managerIn.Close()
				})
				go hubPeer.Serve(t.Context(), hubIn)
				go managerPeer.Serve(t.Context(), managerIn)
				h.bus.mu.Lock()
				h.bus.current = map[string]*busConn{assistantwire.AssistantID: {peer: hubPeer}}
				h.bus.mu.Unlock()

				body, err := json.Marshal(map[string]any{
					"machine_id": "machine", "cwd": "/code", "launcher": tc.requested, "tab_name": "test", "async": true,
				})
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(string(body)))
				w := httptest.NewRecorder()
				startedAt := time.Now()
				h.startSession(w, r)
				if (w.Code == http.StatusOK) != tc.wantOK {
					t.Fatalf("start after %s = %d %s", time.Since(startedAt), w.Code, w.Body)
				}
				if elapsed := time.Since(startedAt); !tc.wantOK && elapsed != 100*time.Second {
					t.Fatalf("start timed out after %s, want 100s", elapsed)
				}
			})
		})
	}
}

// A start's goal (POST /api/v1/sessions) reaches the node's session.start, where the node sets it after the first
// message: the route does not drop the documented field.
func TestSessionStartForwardsTheGoal(t *testing.T) {
	h := newTestHub(t)
	connectAssistant(t, h)
	var gotGoal string
	startReceiptNode(t, h, func(_ context.Context, params json.RawMessage) (any, error) {
		var in struct {
			Goal string `json:"goal"`
		}
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, err
		}
		gotGoal = in.Goal
		return map[string]any{"session": proto.Session{SessionID: "created"}}, nil
	})
	body, err := json.Marshal(map[string]any{
		"machine_id": "machine", "cwd": "/code", "launcher": "claude", "prompt": "fix the build", "tab_name": "t",
		"goal": "CI is green",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/sessions", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.startSession(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	if gotGoal != "CI is green" {
		t.Fatalf("the node got goal %q", gotGoal)
	}
}
