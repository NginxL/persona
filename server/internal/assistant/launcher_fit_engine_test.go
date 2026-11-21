package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A new Agent on a computer whose engine is Paseo, which runs only its own providers (#8140, launcher_fit_engine.go).

// paseoFitHub: pbox's engine is Paseo, whose providers are claude-sub, codex-cpa and krcc (a name its login shell has
// too); its own list (Settings, the main chat) is the shell's codex, krcc and traex. Model configuration › Agents runs
// the shell's codex.
func paseoFitHub(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.sessions["pbox"] = nil
	f.devices = []Device{
		{ID: "server", Name: "server", Home: "/home/me", Online: true, Reachable: true},
		{ID: "pbox", Name: "pbox", Home: "/home/me", Online: true, Reachable: true, ClaudeVersion: "2.1.0", Engine: proto.EnginePaseo},
	}
	f.zshrc = map[string][]proto.Launcher{"pbox": {{Name: "codex", Agent: "codex", Source: "zshrc"}, {Name: "krcc", Source: "zshrc"}, {Name: "traex", Agent: "traex", Source: "zshrc"}}}
	f.hostLaunchers = map[string][]proto.Launcher{"pbox": {
		{Name: "claude-sub", Source: "paseo"}, {Name: "codex-cpa", Source: "paseo", Agent: "codex", PromptOptional: true}, {Name: "krcc", Source: "paseo"},
	}}
	f.agentModels = map[string][]proto.AgentModel{"codex-cpa": {{ID: "gpt-x", Default: true}}}
	s := newServiceOn(t, f, &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}})
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.codex", Model: "gpt-6", Effort: "xhigh"})
	return s, f
}

// A task on pbox whose launcher Paseo does not have runs on Paseo's provider of the same kind: the card names it and its
// why says so before anything starts; the model the default named is the shell codex's, which codex-cpa does not offer,
// so it runs codex-cpa's own, while the effort goes with it (Paseo lowers one its model lacks, and says so).
func TestTaskOnPaseoGetsAProviderOfTheSameKind(t *testing.T) {
	s, f := paseoFitHub(t)
	ctx := context.Background()
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"pbox","cwd":"~/code/app","why":"A quick check"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	task := p.Tasks[0]
	if task.Launcher != "codex-cpa" || task.Model != "" || task.Effort != "xhigh" || !strings.HasPrefix(task.Why, "A quick check (") ||
		!containsAll(task.Why, "Paseo", "pbox", "“codex”", "“codex-cpa”", "gpt-6") {
		t.Fatalf("the card does not show what runs: %+v", task)
	}
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start", func() bool { return len(startsOf(f)) == 1 })
	if st := startsOf(f)[0]; st["launcher"] != "codex-cpa" || st["model"] != nil || st["effort"] != "xhigh" {
		t.Fatalf("started with %v", st)
	}

	// agent_start says so on its answer; one Paseo has (krcc, its own provider of the shell's name) runs as named.
	out, err := s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"pbox","cwd":"/home/me/code","prompt":"Reply with pong"}`))
	if err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[1]; st["launcher"] != "codex-cpa" || !strings.Contains(out, "Note: ") || !strings.Contains(out, "codex-cpa") {
		t.Fatalf("agent_start: %v\n%s", st, out)
	}
	out, err = s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"pbox","cwd":"/home/me/code","prompt":"Reply with pong","launcher":"krcc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[2]; st["launcher"] != "krcc" || strings.Contains(out, "Note: ") {
		t.Fatalf("krcc: %v\n%s", st, out)
	}

	// Plain claude (Claude Code's default): Paseo's first Claude Code provider, said.
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.claude"})
	params, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "pbox", Params: map[string]any{"cwd": "/home/me/code"}})
	if err != nil || params["launcher"] != "claude-sub" {
		t.Fatalf("new agent with claude: %v %v", params, err)
	}
}

// None of that kind on pbox: the task, agent_start and New Agent are refused with the reason, nothing starts, and no
// other kind of agent runs in its place.
func TestNoProviderOfTheKindOnPaseoIsRefused(t *testing.T) {
	s, f := paseoFitHub(t)
	ctx := context.Background()
	_, err := s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"pbox","cwd":"/home/me/code","prompt":"Reply","launcher":"traex"}`))
	if err == nil || !containsAll(err.Error(), "Paseo", "pbox", "traex", "claude-sub, codex-cpa, krcc", "no agent was started") {
		t.Fatalf("agent_start: %v", err)
	}
	_, err = s.AgentStartParams(ctx, AgentStartParams{MachineID: "pbox", Params: map[string]any{"cwd": "/home/me/code", "launcher": "traex"}})
	if err == nil {
		t.Fatal("New Agent with traex was not refused")
	}
	if len(startsOf(f)) != 0 {
		t.Fatalf("started %v", startsOf(f))
	}
}

// What the engine ran in place of what was asked (a race the card could not see, or an effort the model lacks) comes
// back with the session: the task names what runs and says so, agent_start's answer too, and the health and use of
// the launcher that ran are recorded under it.
func TestEngineSaysWhatItRanInstead(t *testing.T) {
	s, f := paseoFitHub(t)
	ctx := context.Background()
	f.startReply = func(params map[string]any, sess *proto.Session) *proto.StartInstead {
		sess.Launcher, sess.Effort = "codex-cpa", "high"
		return &proto.StartInstead{Effort: "xhigh"}
	}
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"pbox","cwd":"~/code/app","why":"A quick check"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the why updated", func() bool {
		p, err := s.loadPlan(ctx, id)
		return err == nil && strings.Contains(p.Tasks[0].Why, "xhigh") && p.Tasks[0].Effort == "high"
	})
	out, err := s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"pbox","cwd":"/home/me/code","prompt":"Reply","launcher":"codex-cpa","effort":"xhigh"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(out, "Note: ", "“codex-cpa”", "xhigh", " high") {
		t.Fatalf("agent_start's answer: %s", out)
	}
}

// New Agent's list on pbox is Paseo's providers alone: no provider kept in Persona, no plain claude, no launcher of the
// login shell's. The main chat's menu lists the computer's own, which the assistant runs on its command line.
func TestNewAgentListOnPaseoIsItsProviders(t *testing.T) {
	s, f := paseoFitHub(t)
	ctx := context.Background()
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "deepseek-gw", BaseURL: "https://gw.example", Model: "m"}})
	names := func(query string) []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/machines/pbox/launchers"+query, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req.WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body)
		}
		var body struct {
			Launchers []struct {
				Name string `json:"name"`
			} `json:"launchers"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		var out []string
		for _, l := range body.Launchers {
			out = append(out, l.Name)
		}
		return out
	}
	if got := names(""); !slices.Equal(got, []string{"claude-sub", "codex-cpa", "krcc"}) {
		t.Fatalf("New Agent on pbox lists %v", got)
	}
	query := "?purpose=assistant&host=paseo"
	live := names(query)
	if slices.Contains(live, "claude-sub") || !slices.Contains(live, "krcc") ||
		!slices.Contains(live, "claude") || !slices.Contains(live, "deepseek-gw") {
		t.Fatalf("the main chat's machine-wide menu lists %v", live)
	}
	_ = s.zshrcLaunchers(ctx, true)
	old := launcherSyncTimeout
	launcherSyncTimeout = 50 * time.Millisecond
	t.Cleanup(func() { launcherSyncTimeout = old })
	f.mu.Lock()
	f.slowLaunchers = map[string]bool{"pbox": true}
	f.mu.Unlock()
	if got := names(query); !slices.Equal(got, live) {
		t.Fatalf("the main chat's menu during a shell timeout lists %v, want %v", got, live)
	}
	if f.agents != 0 {
		t.Fatal("listing started something")
	}
}

// Where the node does not answer for the engine host (an older node, or Paseo away), the start goes on as before: the
// computer's list holds it, and the engine itself refuses what it cannot run.
func TestPaseoListUnansweredKeepsTheOldRule(t *testing.T) {
	s, f := paseoFitHub(t)
	f.hostLaunchers = nil
	params, err := s.AgentStartParams(context.Background(), AgentStartParams{MachineID: "pbox", Params: map[string]any{"cwd": "/home/me/code", "launcher": "krcc"}})
	if err != nil || params["launcher"] != "krcc" {
		t.Fatalf("%v %v", params, err)
	}
	// The engine's own refusal of a launcher it has no provider of the kind of is the request's, not a broken launcher.
	refused := &rpc.Error{Code: proto.CodeLauncherKindMissing, Message: "Paseo has no provider traex"}
	if !startRefused(refused) {
		t.Fatal("a launcher_kind_missing refusal would count against the launcher's health")
	}
	if !errors.As(error(refused), new(*rpc.Error)) {
		t.Fatal("not an rpc error")
	}
}

// containsAll: every one of words is in text (the notes are in the user's language; the names and values are not).
func containsAll(text string, words ...string) bool {
	for _, w := range words {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}
