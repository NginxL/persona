package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/mcptoolgolden"
)

// Behaviour cases for supervision events (#6879, step P0 of docs/design/proposals/assistant-instructions.md). Each
// case hands a model the text a supervising conversation reads, taken from the goldens so a prompt change is tested
// as soon as its goldens are regenerated: the instructions (a CLAUDE.md golden) as the system prompt, the assistant's
// tool list (rebuilt from hub's mcp_tools goldens, plus a Bash tool standing in for the shell) and a supervision event
// golden as the message. Tool calls get the case's canned results, and the calls are checked, not the wording.
//
// TestSupervisionBehaviourCasesAreWellFormed runs with the other tests and needs no model. TestSupervisionBehaviourLive
// asks a real model and runs only when PERSONA_LIVE_AI is set, never in CI:
//
//	PERSONA_LIVE_AI='{"base_url":"…","api_key_file":"…","model":"…"}' go test -run SupervisionBehaviourLive -v -count=1 ./internal/assistant
//
// It runs every case PERSONA_LIVE_RUNS times (default 5, PERSONA_LIVE_PARALLEL at a time, default 4) and prints passes
// per case and what the runs spent. Run it before and after a change
// to supervision prompts or the instructions and put both tables in the PR; a case that passes fewer runs after the
// change blocks it (the pass bar in the proposal's section 6).
//
// Limits: the model is called directly, without Claude Code's own system prompt, the snapshot or earlier turns.

type behaviourCall struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// behaviourMatch is a call the model should (or should not) make: Args values are matched as substrings of the
// argument's JSON, so "feat/x" matches answers that contain it. After lists tools of which at least one must have
// been called earlier in the run (evidence before a verdict).
type behaviourMatch struct {
	Tool  string            `json:"tool"`
	Args  map[string]string `json:"args"`
	After []string          `json:"after"`
}

type behaviourCase struct {
	Name    string             `json:"name"`
	Why     string             `json:"why"`
	System  string             `json:"system"` // a golden under testdata/prompts
	Event   string             `json:"event"`  // a golden under testdata/prompts
	Results map[string]string  `json:"results"`
	Expect  []behaviourMatch   `json:"expect"`
	AnyOf   [][]behaviourMatch `json:"any_of"`
	Forbid  []behaviourMatch   `json:"forbid"`
}

// behaviourBash stands in for Claude Code's shell, which the instructions send the model to for gh and git.
var behaviourBash = map[string]any{
	"name":        "Bash",
	"description": "Run a shell command on this computer and return its output (gh, git, ls and other command line tools).",
	"input_schema": map[string]any{"type": "object", "required": []string{"command"},
		"properties": map[string]any{"command": map[string]any{"type": "string"}}},
}

func loadBehaviourCases(t *testing.T) []behaviourCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "behaviour", "supervision.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []behaviourCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func behaviourGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "prompts", name+".golden"))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(b)
}

// behaviourTools is the assistant's tool list as the hub offers it, plus Bash.
func behaviourTools(t *testing.T) []map[string]any {
	t.Helper()
	b, err := mcptoolgolden.Load(filepath.Join("..", "hub", mcptoolgolden.Dir), "assistant")
	if err != nil {
		t.Fatal(err)
	}
	var tools []map[string]any
	if err := json.Unmarshal(b, &tools); err != nil {
		t.Fatal(err)
	}
	return append(tools, behaviourBash)
}

func TestSupervisionBehaviourCasesAreWellFormed(t *testing.T) {
	props := map[string]map[string]any{}
	for _, tool := range behaviourTools(t) {
		schema, _ := tool["input_schema"].(map[string]any)
		p, _ := schema["properties"].(map[string]any)
		props[tool["name"].(string)] = p
	}
	check := func(c behaviourCase, m behaviourMatch) {
		p, ok := props[m.Tool]
		if !ok {
			t.Errorf("%s: no tool %q", c.Name, m.Tool)
			return
		}
		for k := range m.Args {
			if _, ok := p[k]; !ok {
				t.Errorf("%s: %s has no argument %q", c.Name, m.Tool, k)
			}
		}
		for _, a := range m.After {
			if _, ok := props[a]; !ok {
				t.Errorf("%s: after names no tool %q", c.Name, a)
			}
		}
	}
	names := map[string]bool{}
	for _, c := range loadBehaviourCases(t) {
		if c.Name == "" || names[c.Name] {
			t.Errorf("case name %q empty or repeated", c.Name)
		}
		names[c.Name] = true
		if c.Why == "" {
			t.Errorf("%s: say why", c.Name)
		}
		behaviourGolden(t, c.System)
		behaviourGolden(t, c.Event)
		if len(c.Expect)+len(c.AnyOf)+len(c.Forbid) == 0 {
			t.Errorf("%s: checks nothing", c.Name)
		}
		for _, m := range c.Expect {
			check(c, m)
		}
		for _, group := range c.AnyOf {
			if len(group) == 0 {
				t.Errorf("%s: empty any_of", c.Name)
			}
			for _, m := range group {
				check(c, m)
			}
		}
		for _, m := range c.Forbid {
			check(c, m)
		}
		for tool := range c.Results {
			if _, ok := props[tool]; !ok {
				t.Errorf("%s: a result for no tool %q", c.Name, tool)
			}
		}
	}
}

func TestSupervisionBehaviourChecks(t *testing.T) {
	c := behaviourCase{
		Expect: []behaviourMatch{{Tool: "task_review", Args: map[string]string{"verdict": "accepted"}, After: []string{"agent_read"}}},
		AnyOf:  [][]behaviourMatch{{{Tool: "interaction_answer"}, {Tool: "ask_escalate"}}},
		Forbid: []behaviourMatch{{Tool: "notify_user"}},
	}
	read := behaviourCall{Tool: "agent_read"}
	accept := behaviourCall{Tool: "task_review", Args: map[string]any{"todo_id": "t1", "verdict": "accepted"}}
	answer := behaviourCall{Tool: "interaction_answer", Args: map[string]any{"answers": map[string]any{"q": "feat/x"}}}
	for _, tc := range []struct {
		calls []behaviourCall
		miss  bool
	}{
		{[]behaviourCall{read, accept, answer}, false},
		{[]behaviourCall{accept, read, answer}, true},                        // verdict before evidence
		{[]behaviourCall{read, accept}, true},                                // no any_of
		{[]behaviourCall{read, accept, answer, {Tool: "notify_user"}}, true}, // forbidden
		{[]behaviourCall{read, {Tool: "task_review", Args: map[string]any{"verdict": "rework"}}, answer}, true},
	} {
		if got := c.misses(tc.calls); (len(got) > 0) != tc.miss {
			t.Errorf("%v: misses %v", tc.calls, got)
		}
	}
	if !(behaviourMatch{Tool: "interaction_answer", Args: map[string]string{"answers": "feat/x"}}).matches(answer) {
		t.Error("an argument's JSON should match by substring")
	}
	// The parked case's shape: a task out of parks must be decided or closed, and a further
	// parked verdict on it is itself a miss, not a satisfying task_review.
	parked := behaviourCase{
		Expect: []behaviourMatch{{Tool: "task_review", Args: map[string]string{"todo_id": "t1"}}},
		AnyOf: [][]behaviourMatch{{
			{Tool: "task_review", Args: map[string]string{"todo_id": "t2", "verdict": "accepted"}},
			{Tool: "task_review", Args: map[string]string{"todo_id": "t2", "verdict": "cancelled"}},
			{Tool: "task_review", Args: map[string]string{"todo_id": "t2", "verdict": "needs_user"}},
		}},
		Forbid: []behaviourMatch{{Tool: "task_review", Args: map[string]string{"todo_id": "t2", "verdict": "parked"}}},
	}
	first := behaviourCall{Tool: "task_review", Args: map[string]any{"todo_id": "t1", "verdict": "parked"}}
	decided := behaviourCall{Tool: "task_review", Args: map[string]any{"todo_id": "t2", "verdict": "accepted"}}
	reparked := behaviourCall{Tool: "task_review", Args: map[string]any{"todo_id": "t2", "verdict": "parked"}}
	for _, tc := range []struct {
		calls []behaviourCall
		miss  bool
	}{
		{[]behaviourCall{first, decided}, false},
		{[]behaviourCall{first, reparked}, true}, // an illegal fifth park passes no check
		{[]behaviourCall{first}, true},           // t2 left as is
	} {
		if got := parked.misses(tc.calls); (len(got) > 0) != tc.miss {
			t.Errorf("%v: misses %v", tc.calls, got)
		}
	}
}

func (m behaviourMatch) matches(c behaviourCall) bool {
	if c.Tool != m.Tool {
		return false
	}
	for k, want := range m.Args {
		v, ok := c.Args[k]
		if !ok {
			return false
		}
		got, _ := v.(string)
		if _, isString := v.(string); !isString {
			b, _ := json.Marshal(v)
			got = string(b)
		}
		if !strings.Contains(got, want) {
			return false
		}
	}
	return true
}

func (m behaviourMatch) String() string {
	b, _ := json.Marshal(m.Args)
	return m.Tool + string(b)
}

// misses lists what the calls got wrong against the case: an expected call missing (or made before its evidence),
// an any_of group with no match, a forbidden call made.
func (c behaviourCase) misses(calls []behaviourCall) []string {
	var out []string
	found := func(m behaviourMatch) (bool, bool) { // matched at all, matched after its evidence
		matched := false
		for i, call := range calls {
			if !m.matches(call) {
				continue
			}
			matched = true
			if len(m.After) == 0 || slices.ContainsFunc(calls[:i], func(e behaviourCall) bool { return slices.Contains(m.After, e.Tool) }) {
				return true, true
			}
		}
		return matched, false
	}
	for _, m := range c.Expect {
		switch matched, inOrder := found(m); {
		case !matched:
			out = append(out, "missing "+m.String())
		case !inOrder:
			out = append(out, "no evidence before "+m.String())
		}
	}
	for _, group := range c.AnyOf {
		if !slices.ContainsFunc(group, func(m behaviourMatch) bool { ok, inOrder := found(m); return ok && inOrder }) {
			names := make([]string, len(group))
			for i, m := range group {
				names[i] = m.String()
			}
			out = append(out, "none of "+strings.Join(names, " | "))
		}
	}
	for _, m := range c.Forbid {
		if slices.ContainsFunc(calls, m.matches) {
			out = append(out, "forbidden "+m.String())
		}
	}
	return out
}

func TestSupervisionBehaviourLive(t *testing.T) {
	raw := os.Getenv("PERSONA_LIVE_AI")
	if raw == "" {
		t.Skip("PERSONA_LIVE_AI not set")
	}
	var cfg ai.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.API != "" && cfg.API != "messages" {
		t.Skipf("the behaviour runner speaks the Messages API, not %q", cfg.API)
	}
	runs, parallel := 5, 4
	if n, err := strconv.Atoi(os.Getenv("PERSONA_LIVE_RUNS")); err == nil && n > 0 {
		runs = n
	}
	if n, err := strconv.Atoi(os.Getenv("PERSONA_LIVE_PARALLEL")); err == nil && n > 0 {
		parallel = n
	}
	only := os.Getenv("PERSONA_LIVE_CASE")
	tools := behaviourTools(t)
	var cases []behaviourCase
	for _, c := range loadBehaviourCases(t) {
		if only == "" || c.Name == only {
			cases = append(cases, c)
		}
	}
	type outcome struct {
		calls []behaviourCall
		miss  []string
		usage behaviourUsage
	}
	results := make([][]outcome, len(cases))
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, parallel)
	for ci, c := range cases {
		results[ci] = make([]outcome, runs)
		system, event := behaviourGolden(t, c.System), behaviourGolden(t, c.Event)
		for r := range runs {
			// Take the slot before launching, so PERSONA_LIVE_PARALLEL bounds goroutines and
			// their memory, not only the model calls they end up making.
			slots <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				calls, usage, err := runBehaviour(cfg, system, event, tools, c.Results)
				miss := c.misses(calls)
				if err != nil {
					miss = append(miss, "error: "+err.Error())
				}
				mu.Lock()
				results[ci][r] = outcome{calls, miss, usage}
				mu.Unlock()
			}()
		}
	}
	wg.Wait()
	var rows []string
	var spent behaviourUsage
	total, passed := 0, 0
	for ci, c := range cases {
		ok, last := 0, ""
		for _, o := range results[ci] {
			spent = spent.add(o.usage)
			names := make([]string, len(o.calls))
			for i, call := range o.calls {
				names[i] = call.Tool
			}
			if len(o.miss) == 0 {
				ok++
			} else {
				last = strings.Join(o.miss, "; ")
			}
			t.Logf("%s: %s %v", c.Name, map[bool]string{true: "ok  ", false: "MISS"}[len(o.miss) == 0], names)
		}
		total += runs
		passed += ok
		rows = append(rows, fmt.Sprintf("%-48s %d/%d  %s", c.Name, ok, runs, last))
	}
	t.Logf("supervision behaviour on %s: %d of %d runs as expected; %d model calls, input %d, cache write %d, cache read %d, output %d\n%s",
		cfg.Model, passed, total, spent.Calls, spent.Input, spent.CacheWrite, spent.CacheRead, spent.Output, strings.Join(rows, "\n"))
}

// behaviourUsage is what the runs spent, as the upstream reported it.
type behaviourUsage struct{ Calls, Input, CacheWrite, CacheRead, Output int }

func (u behaviourUsage) add(v behaviourUsage) behaviourUsage {
	return behaviourUsage{u.Calls + v.Calls, u.Input + v.Input, u.CacheWrite + v.CacheWrite, u.CacheRead + v.CacheRead, u.Output + v.Output}
}

// runBehaviour plays one case against the model: the event as the first message, canned results for its tool calls,
// until it stops calling tools or after 12 model calls. It returns every tool call in order. The system prompt and the
// tool list are marked for prompt caching, as Claude Code does, so repeated runs pay for them once.
func runBehaviour(cfg ai.Config, system, event string, tools []map[string]any, results map[string]string) ([]behaviourCall, behaviourUsage, error) {
	var spent behaviourUsage
	key := cfg.APIKey
	if key == "" {
		path := cfg.APIKeyFile
		if strings.HasPrefix(path, "~/") {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, path[2:])
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, spent, err
		}
		key = strings.TrimSpace(string(b))
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	cached := slices.Clone(tools)
	last := maps.Clone(cached[len(cached)-1])
	last["cache_control"] = map[string]any{"type": "ephemeral"}
	cached[len(cached)-1] = last
	systemBlocks := []map[string]any{{"type": "text", "text": system, "cache_control": map[string]any{"type": "ephemeral"}}}
	messages := []map[string]any{{"role": "user", "content": event}}
	var calls []behaviourCall
	for range 12 {
		body := map[string]any{"model": cfg.Model, "max_tokens": 4096, "system": systemBlocks, "tools": cached, "messages": messages}
		if cfg.ReasoningEffort != "" {
			body["output_config"] = map[string]any{"effort": cfg.ReasoningEffort}
		}
		b, _ := json.Marshal(body)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		req, _ := http.NewRequestWithContext(ctx, "POST", base+"/messages", bytes.NewReader(b))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("anthropic-version", "2023-06-01")
		if cfg.Bearer {
			req.Header.Set("authorization", "Bearer "+key)
		} else {
			req.Header.Set("x-api-key", key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			return calls, spent, err
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if resp.StatusCode != 200 {
			return calls, spent, fmt.Errorf("status %d: %.300s", resp.StatusCode, raw)
		}
		var reply struct {
			Content    []map[string]any `json:"content"`
			StopReason string           `json:"stop_reason"`
			Usage      struct {
				Input      int `json:"input_tokens"`
				CacheWrite int `json:"cache_creation_input_tokens"`
				CacheRead  int `json:"cache_read_input_tokens"`
				Output     int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			return calls, spent, err
		}
		spent = spent.add(behaviourUsage{1, reply.Usage.Input, reply.Usage.CacheWrite, reply.Usage.CacheRead, reply.Usage.Output})
		messages = append(messages, map[string]any{"role": "assistant", "content": reply.Content})
		var outs []map[string]any
		for _, block := range reply.Content {
			if block["type"] != "tool_use" {
				continue
			}
			name, _ := block["name"].(string)
			args, _ := block["input"].(map[string]any)
			calls = append(calls, behaviourCall{Tool: name, Args: args})
			result, ok := results[name]
			if !ok {
				result = `{"ok":true}`
			}
			outs = append(outs, map[string]any{"type": "tool_result", "tool_use_id": block["id"], "content": result})
		}
		if len(outs) == 0 {
			return calls, spent, nil
		}
		messages = append(messages, map[string]any{"role": "user", "content": outs})
	}
	return calls, spent, nil
}
