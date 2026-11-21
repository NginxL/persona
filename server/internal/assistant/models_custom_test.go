package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// customIDs are the ids of a /model list's custom entries, in order.
func customIDs(res proto.LauncherModels) []string {
	var out []string
	for _, m := range res.Models {
		if m.Custom {
			out = append(out, m.ID)
		}
	}
	return out
}

func machineModels(t *testing.T, s *Service, machine, launcher string) proto.LauncherModels {
	t.Helper()
	r := request(s, "GET", "/api/v1/machines/"+machine+"/launchers/"+launcher+"/models", "")
	if r.Code != http.StatusOK {
		t.Fatalf("models: %d %s", r.Code, r.Body.String())
	}
	var res proto.LauncherModels
	if err := json.Unmarshal(r.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res
}

// A typed model id that ran is remembered for its machine and launcher (#1644): not one the launcher lists itself (an
// alias, an id, what an alias resolves to), not one that is no model id, most recent first, at most maxCustomModels; the
// /model list offers them after its own as custom entries, and Forget drops one.
func TestCustomModelsRememberedPerMachineAndLauncher(t *testing.T) {
	ctx := context.Background()
	s, f, _ := modelService(t)
	f.agentModels = map[string][]proto.AgentModel{"work": {{ID: "default", Default: true, Resolved: "claude-x-1"}, {ID: "opus"}}}
	used := func(machine, launcher, model string) {
		s.ModelUsed(ctx, assistantwire.ModelUsed{MachineID: machine, Launcher: launcher, Model: model})
	}
	used("server", "work", "opus")       // listed
	used("server", "work", "claude-x-1") // what default resolves to
	used("server", "work", "a b")        // no model id
	used("server", "work", "")
	used("server", "", "gw/x")
	if got := s.customModels(ctx, "server", "work"); len(got) != 0 {
		t.Fatalf("remembered what needs no typing or is no id: %v", got)
	}
	used("server", "work", " gw/model-x ")
	used("server", "work", "gw/model-y")
	used("server", "work", "gw/model-x") // again: first again, once
	used("server", "other", "gw/model-z")
	used("box", "work", "gw/model-w")
	if got := s.customModels(ctx, "server", "work"); fmt.Sprint(got) != "[gw/model-x gw/model-y]" {
		t.Fatalf("server/work: %v", got)
	}
	if got := s.customModels(ctx, "server", "other"); fmt.Sprint(got) != "[gw/model-z]" {
		t.Fatalf("server/other: %v", got)
	}

	res := machineModels(t, s, "server", "work")
	if ids := customIDs(res); fmt.Sprint(ids) != "[gw/model-x gw/model-y]" || len(res.Models) != 4 || res.Models[0].ID != "default" || res.Models[1].ID != "opus" {
		t.Fatalf("list: %+v", res.Models)
	}
	if m := res.Models[2]; m.Name != "gw/model-x" || m.Description == "" || m.Default || len(m.Efforts) != 0 {
		t.Fatalf("a custom entry: %+v", m)
	}
	// It counts as the launcher's for a message that carries it over (sendModel).
	if !s.launcherHasModel(ctx, "server", "work", "gw/model-y") || s.launcherHasModel(ctx, "server", "work", "gw/never") {
		t.Fatal("launcherHasModel and the remembered ids")
	}

	// At most maxCustomModels per machine and launcher: the oldest goes; the others' stay.
	for i := range maxCustomModels + 2 {
		used("server", "work", fmt.Sprintf("gw/m-%d", i))
	}
	got := s.customModels(ctx, "server", "work")
	if len(got) != maxCustomModels || got[0] != fmt.Sprintf("gw/m-%d", maxCustomModels+1) || slices.Contains(got, "gw/model-x") {
		t.Fatalf("capped: %v", got)
	}
	if fmt.Sprint(s.customModels(ctx, "server", "other")) != "[gw/model-z]" || fmt.Sprint(s.customModels(ctx, "box", "work")) != "[gw/model-w]" {
		t.Fatal("another machine's or launcher's went too")
	}

	// Forget: that one, there; forgetting again or one never typed is no error; a bad id is refused.
	forget := func(machine, launcher, model string) int {
		return request(s, "DELETE", "/api/v1/machines/"+machine+"/launchers/"+launcher+"/models/custom?model="+url.QueryEscape(model), "").Code
	}
	if c := forget("server", "work", got[0]); c != http.StatusOK || slices.Contains(s.customModels(ctx, "server", "work"), got[0]) ||
		len(s.customModels(ctx, "server", "work")) != maxCustomModels-1 {
		t.Fatalf("forget: %d %v", c, s.customModels(ctx, "server", "work"))
	}
	if c := forget("server", "work", got[0]); c != http.StatusOK {
		t.Fatalf("forget again: %d", c)
	}
	if c := forget("server", "work", "a b"); c != http.StatusBadRequest {
		t.Fatalf("forget a bad id: %d", c)
	}
	if c := forget("box", "work", "gw/model-w"); c != http.StatusOK || len(s.customModels(ctx, "box", "work")) != 0 {
		t.Fatalf("forget on box: %d", c)
	}
	r := request(s, "DELETE", "/api/v1/machines/server/launchers/other/models/custom?model=gw/model-z", "")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"ok":true`) {
		t.Fatalf("forget answer: %d %s", r.Code, r.Body.String())
	}
}

// The ids remembered on one machine are not another's, and a list that already has one shows it once, as its own.
func TestCustomModelsMergedOnce(t *testing.T) {
	ctx := context.Background()
	s, f, _ := modelService(t)
	f.agentModels = map[string][]proto.AgentModel{"work": {{ID: "opus"}}}
	s.ModelUsed(ctx, assistantwire.ModelUsed{MachineID: "server", Launcher: "work", Model: "gw/new"})
	f.agentModels["work"] = []proto.AgentModel{{ID: "opus"}, {ID: "gw/new", Name: "New"}} // the gateway lists it now
	res := s.withCustomModels(ctx, "server", "work", proto.LauncherModels{Agent: "claude", Models: f.agentModels["work"]})
	if len(res.Models) != 2 || res.Models[1].Custom || res.Models[1].Name != "New" {
		t.Fatalf("listed twice: %+v", res.Models)
	}
	if res := s.withCustomModels(ctx, "elsewhere", "work", proto.LauncherModels{Agent: "claude", Models: []proto.AgentModel{}}); len(res.Models) != 0 {
		t.Fatalf("another machine's: %+v", res.Models)
	}
}

// A typed id is only for a launcher whose agent would start with it (#1644): Claude Code or Codex, with no --model of
// the launcher's own over it. Another agent's list (empty: the apps show no model row) and a locked one get no custom
// entries, and an id used with them is not remembered.
func TestCustomModelsOnlyWhereATypedModelReachesTheAgent(t *testing.T) {
	ctx := context.Background()
	s, f, _ := modelService(t)
	f.agentModels = map[string][]proto.AgentModel{"work": {{ID: "opus"}}, "codex-w": {{ID: "gpt-x"}}}
	f.modelsLocked = map[string]string{"fixed": "the launcher sets its own --model"}
	for _, launcher := range []string{"gemini-w", "fixed", "work", "codex-w"} {
		s.ModelUsed(ctx, assistantwire.ModelUsed{MachineID: "server", Launcher: launcher, Model: "gw/typed"})
	}
	for launcher, want := range map[string]string{"gemini-w": "[]", "fixed": "[]", "work": "[gw/typed]", "codex-w": "[gw/typed]"} {
		if got := s.customModels(ctx, "server", launcher); fmt.Sprint(got) != want {
			t.Errorf("%s remembered %v, want %s", launcher, got, want)
		}
	}
	// Remembered before (an older hub, a launcher changed since): not offered where it would not reach the agent.
	s.ModelUsed(ctx, assistantwire.ModelUsed{MachineID: "server", Launcher: "work", Model: "gw/other"})
	for _, res := range []proto.LauncherModels{{Agent: "gemini", Models: []proto.AgentModel{}}, {Agent: "agy", Models: []proto.AgentModel{}},
		{Agent: "claude", Models: []proto.AgentModel{{ID: "opus"}}, Locked: "fixed"}} {
		if got := s.withCustomModels(ctx, "server", "work", res); len(got.Models) != len(res.Models) {
			t.Errorf("%+v got custom entries: %+v", res, got.Models)
		}
	}
	if got := customIDs(machineModels(t, s, "server", "fixed")); len(got) != 0 {
		t.Fatalf("the locked launcher's list: %v", got)
	}
	if got := customIDs(machineModels(t, s, "server", "codex-w")); fmt.Sprint(got) != "[gw/typed]" {
		t.Fatalf("codex's list: %v", got)
	}
}

// A launcher purpose configured before the rule with a model that is no id (#1644) runs on the launcher's default
// instead of failing every run, the main chat's included. An edit that keeps it is refused naming it (the node would
// refuse it too), one that clears it or sets an id is taken; a gateway's ~-alias is an id.
func TestOldConfiguredModelThatIsNoID(t *testing.T) {
	ctx := context.Background()
	s, f, _ := modelService(t)
	f.launcherList = []proto.Launcher{{Name: "claude"}}
	f.agentModels = map[string][]proto.AgentModel{"claude": {{ID: "opus"}}}
	for _, id := range []string{"agent.new", "assistant"} {
		if err := s.saveModelRecord(ctx, id, 0, ModelBinding{Provider: "launcher.claude", Model: "old model+x"}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "server", Params: map[string]any{}})
	if err != nil || p["launcher"] != "claude" || p["model"] != nil {
		t.Fatalf("New Agent on the old record: %v %v", p, err)
	}
	params := map[string]any{}
	if err := s.applyModel(ctx, "assistant", params); err != nil || params["model"] != nil {
		t.Fatalf("the main chat on the old record: %v %v", params, err)
	}
	// This start's own pick is still held to the rule.
	if _, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "server", Params: map[string]any{"model": "a b"}}); err == nil {
		t.Fatal("a pick that is no id went through")
	}
	rev := purposeOf(t, s, "agent.new").Revision
	if r := modelRequest(s, "PATCH", "agent.new", fmt.Sprintf(`{"revision":%d,"effort":"high"}`, rev)); r.Code != http.StatusBadRequest ||
		!strings.Contains(r.Body.String(), "old model+x") {
		t.Fatalf("an edit that keeps it: %d %s", r.Code, r.Body.String())
	}
	if r := modelRequest(s, "PATCH", "agent.new", fmt.Sprintf(`{"revision":%d,"model":"new model"}`, rev)); r.Code != http.StatusBadRequest {
		t.Fatalf("setting a model that is no id: %d %s", r.Code, r.Body.String())
	}
	if r := modelRequest(s, "PATCH", "agent.new", fmt.Sprintf(`{"revision":%d,"model":null,"effort":"high"}`, rev)); r.Code != http.StatusOK {
		t.Fatalf("clearing it: %d %s", r.Code, r.Body.String())
	}
	rev = purposeOf(t, s, "agent.new").Revision
	if r := modelRequest(s, "PATCH", "agent.new", fmt.Sprintf(`{"revision":%d,"model":"~anthropic/claude-opus-latest"}`, rev)); r.Code != http.StatusOK {
		t.Fatalf("a gateway's ~-alias: %d %s", r.Code, r.Body.String())
	}
	if p, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "server", Params: map[string]any{}}); err != nil || p["model"] != "~anthropic/claude-opus-latest" {
		t.Fatalf("New Agent on the ~-alias: %v %v", p, err)
	}
}

// A typed model that is no id is refused in the language of the app that sent it (the bus call's lang), not the hub's.
func TestModelRefusalInTheAppsLanguage(t *testing.T) {
	ctx := context.Background()
	s, f, _ := modelService(t)
	f.launcherList = []proto.Launcher{{Name: "claude"}}
	f.setSessions("server", proto.Session{SessionID: "side", Launcher: "claude", Assistant: true})
	for _, l := range []i18n.Lang{i18n.En, i18n.Ja, i18n.ZhHans} {
		want := i18n.T(l, "assistant-models.err_model_invalid", i18n.Args{"model": `"a b"`})
		_, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "server", Params: map[string]any{"launcher": "claude", "model": "a b"}, Lang: string(l)})
		var re *rpc.Error
		if !errors.As(err, &re) || re.Message != want {
			t.Errorf("%s: New Agent's refusal %v, want %q", l, err, want)
		}
		_, err = s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": "hi", "model": "a b"}, Lang: string(l)})
		if !errors.As(err, &re) || re.Message != want {
			t.Errorf("%s: the chip's refusal %v, want %q", l, err, want)
		}
	}
}

// A message to one of the assistant's conversations with a model picked in its chip keeps it though the launcher's
// list does not have it (#1644); a pick that is no model id is refused; a model only carried over from what the
// conversation ran is still dropped when the list has it no more, unless the user typed it there before.
func TestSendParamsKeepsATypedModel(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	f.setSessions("server", proto.Session{SessionID: "side", Launcher: "side-l", Model: "gw/dropped", Assistant: true},
		proto.Session{SessionID: "synth", Launcher: "side-l", Model: "<synthetic>", Assistant: true})
	f.launcherList = []proto.Launcher{{Name: "claude"}, {Name: "side-l"}}
	f.agentModels = map[string][]proto.AgentModel{"side-l": {{ID: "m"}}}
	s := newTestService(t, f)

	p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": "hi", "launcher": "side-l", "model": " gw/typed "}})
	if err != nil || p["launcher"] != "side-l" || p["model"] != "gw/typed" || p["model_fingerprint"] == nil {
		t.Fatalf("a typed pick: %v %v", p, err)
	}
	p, err = s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": "hi", "model": "gw/typed"}})
	if err != nil || p["launcher"] != "side-l" || p["model"] != "gw/typed" {
		t.Fatalf("a typed pick on the conversation's launcher: %v %v", p, err)
	}
	for _, bad := range []string{"x --dangerously-skip-permissions", "$(id)", "-p"} {
		if p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": "hi", "model": bad}}); err == nil {
			t.Fatalf("%q went through: %v", bad, p)
		}
	}
	p, err = s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": "hi"}})
	if err != nil || p["model"] != nil {
		t.Fatalf("a carried-over model the list dropped: %v %v", p, err)
	}
	p, err = s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "synth", Params: map[string]any{"text": "hi"}})
	if err != nil || p["model"] != nil {
		t.Fatalf("a carried-over model that is no id: %v %v", p, err)
	}
	s.ModelUsed(ctx, assistantwire.ModelUsed{MachineID: "server", Launcher: "side-l", Model: "gw/dropped"}) // typed there before
	p, err = s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": "hi"}})
	if err != nil || p["model"] != "gw/dropped" {
		t.Fatalf("a carried-over model the user typed: %v %v", p, err)
	}
}

// Every way a model reaches a start is held to the same rule (#1644): New Agent's pick (agents.start_params), Model
// configuration's launcher purposes, agent_start and task_plan from the assistant, a plan card's edit. A typed id the
// list does not have goes through as it is.
func TestModelIDCheckedAtEveryEntry(t *testing.T) {
	ctx := context.Background()
	s, f, _ := modelService(t)
	f.launcherList = []proto.Launcher{{Name: "claude"}}
	f.agentModels = map[string][]proto.AgentModel{"claude": {{ID: "opus"}}}

	p, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "server", Params: map[string]any{"launcher": "claude", "model": "gw/model-x"}})
	if err != nil || p["model"] != "gw/model-x" || p["launcher"] != "claude" {
		t.Fatalf("a typed id: %v %v", p, err)
	}
	if p, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "server", Params: map[string]any{"launcher": "claude", "model": "  "}}); err != nil || p["model"] != nil {
		t.Fatalf("a blank model is none: %v %v", p, err)
	}
	for _, bad := range []string{"a b", "--dangerously-skip-permissions", "`id`", "x;y", strings.Repeat("m", proto.MaxModelIDLen+1)} {
		if _, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "server", Params: map[string]any{"launcher": "claude", "model": bad}}); err == nil {
			t.Errorf("start with %q", bad)
		}
	}

	// Model configuration: a launcher purpose takes a typed id, not one that is no id; an API purpose is not an argument.
	if r := modelRequest(s, "PATCH", "agent.new", `{"revision":0,"provider":"launcher.claude","model":"gw/model-x"}`); r.Code != http.StatusOK {
		t.Fatalf("typed id in Model configuration: %d %s", r.Code, r.Body.String())
	}
	rev := purposeOf(t, s, "agent.new").Revision
	if r := modelRequest(s, "PATCH", "agent.new", fmt.Sprintf(`{"revision":%d,"model":"gw model"}`, rev)); r.Code != http.StatusBadRequest ||
		!strings.Contains(r.Body.String(), "gw model") {
		t.Fatalf("a model with a space: %d %s", r.Code, r.Body.String())
	}
	if r := modelRequest(s, "PATCH", "agent.new", fmt.Sprintf(`{"revision":%d,"model":"-p"}`, rev)); r.Code != http.StatusBadRequest {
		t.Fatalf("a flag for a model: %d %s", r.Code, r.Body.String())
	}

	// The assistant's tools: refused before anything is asked or started.
	tool := func(name, args string) (string, bool) {
		res := s.Tool(ctx, ToolCall{MachineID: "server", SessionID: "c1", Name: name, Arguments: json.RawMessage(args)})
		return res.Content[0].Text, res.IsError
	}
	if out, isErr := tool("agent_start", `{"machine":"server","cwd":"/code","prompt":"x","model":"x; rm -rf ~"}`); !isErr || !strings.Contains(out, "not a model id") || len(f.starts) != 0 {
		t.Fatalf("agent_start: %s, starts %v", out, f.starts)
	}
	if out, isErr := tool("task_plan", `{"tasks":[{"title":"t","permission":"bypassPermissions","prompt":"p","role":"worker","machine":"server","cwd":"/code","readonly":false,"user_requested":false,"why":"w","model":"$(id)"}]}`); !isErr ||
		!strings.Contains(out, "task 1: model") {
		t.Fatalf("task_plan: %s", out)
	}
}

// A plan card's edit with a model that is no model id is the user's to fix (400), and nothing starts; a typed id
// starts as it is.
func TestPlanEditModelChecked(t *testing.T) {
	s, f, st := newProfileTaskService(t)
	p := dispatchProfilePlan(t, s, st, profilePlanTask())
	if w := request(s, http.MethodPost, "/api/v1/task-plans/"+p.ID+"/start", `{"tasks":[{"model":"a b"}]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an edit with a bad model: %d %s", w.Code, w.Body)
	}
	if cur, err := s.loadPlan(context.Background(), p.ID); err != nil || cur.State != planOpen || len(profileNodeStarts(f)) != 0 {
		t.Fatalf("the plan moved or started: %+v %v", cur, err)
	}
	if w := request(s, http.MethodPost, "/api/v1/task-plans/"+p.ID+"/start", `{"tasks":[{"model":"gw/typed"}]}`); w.Code != http.StatusOK {
		t.Fatalf("a typed id: %d %s", w.Code, w.Body)
	}
	waitFor(t, "the task's start", func() bool { return len(profileNodeStarts(f)) == 1 })
	var params map[string]any
	if err := json.Unmarshal(profileNodeStarts(f)[0].Params, &params); err != nil || params["model"] != "gw/typed" {
		t.Fatalf("session.start: %v %v", params, err)
	}
}

// agent_start's confirmation on the phone (a health conversation's hand-off, health.go) names the model the agent
// would run on, so a model the assistant picked is seen before the user says yes.
func TestAgentStartConfirmationShowsTheModel(t *testing.T) {
	f := healthHub()
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	says := i18n.T(s.lang(context.Background()), "assistant-tools.with_model", i18n.Args{"model": "gw/model-x"})
	answerOutbound(t, s, "sleep-000-0000", "agent_start", says, false)
	out, _ := spaceTool(t, s, "sleep-000-0000", "agent_start", `{"machine":"server","cwd":"/home/me/src/app","prompt":"整理体检报告","model":"gw/model-x"}`)
	if !strings.Contains(out, "declined") || len(f.starts) != 0 {
		t.Fatalf("agent_start: %s, starts %v", out, f.starts)
	}
}
