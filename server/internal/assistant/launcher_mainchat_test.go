package assistant

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The machine of these tests lists plain claude, a ~/.zshrc Codex alias, a Paseo provider, a runtime that runs the
// assistant, TRAEX, and the managed launchers: one Claude, one Codex.
func mainChatFixture(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{
		{Name: "claude"},
		{Name: "cx", Agent: "codex"},
		{Name: "cc-relay", Source: "paseo"},
		{Name: "agy-rt", Source: "agy", Agent: "agy", Assistant: true},
		{Name: "traex", Agent: "traex", BaseURL: "(traex)"},
	}
	s := newTestService(t, f)
	putLaunchers(t, s,
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", BaseURL: "https://gw.example", Model: "m1"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "codex-gw", Agent: "codex", BaseURL: "https://gw.example/v1", Model: "gpt"}})
	return s, f
}

func pickerNames(t *testing.T, s *Service, purpose string) []string {
	t.Helper()
	r := request(s, "GET", "/api/v1/machines/server/launchers?purpose="+purpose, "")
	if r.Code != 200 {
		t.Fatalf("%s: %d %s", purpose, r.Code, r.Body.String())
	}
	var out struct {
		Launchers []ConfiguredLauncher `json:"launchers"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range out.Launchers {
		names = append(names, l.Name)
	}
	return names
}

// The main chat's menu offers only what a message can run on; New Agent's keeps every launcher the machine starts.
func TestMainChatPickerListsOnlyWhatRunsTheAssistant(t *testing.T) {
	s, _ := mainChatFixture(t)
	if got := strings.Join(pickerNames(t, s, "assistant"), ","); got != "claude,cx,agy-rt,gw" {
		t.Fatalf("assistant: %s", got)
	}
	if got := strings.Join(pickerNames(t, s, "agent.new"), ","); got != "claude,cx,cc-relay,agy-rt,traex,gw,codex-gw" {
		t.Fatalf("agent.new: %s", got)
	}
}

// Every client replays these candidates: an assistant-purpose read adds the eligible Messages API provider and keeps
// the declared runtime, but not managed Codex. New Agent keeps its generic launcher answer (Direct APIs are not Agents).
func TestMainChatPickerReplaysSharedLauncherFixture(t *testing.T) {
	var fixture struct {
		Version int `json:"version"`
		Cases   []struct {
			Name       string `json:"name"`
			Candidates []struct {
				Name      string `json:"name"`
				Kind      string `json:"kind"`
				API       string `json:"api"`
				Agent     string `json:"agent"`
				Source    string `json:"source"`
				BaseURL   string `json:"base_url"`
				Model     string `json:"model"`
				Assistant bool   `json:"assistant"`
			} `json:"candidates"`
			Assistant []string `json:"assistant"`
			AgentNew  []string `json:"agent_new"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile("../../../spec/fixtures/assistant-launchers/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || len(fixture.Cases) != 1 {
		t.Fatalf("fixture version/cases: %d/%d", fixture.Version, len(fixture.Cases))
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			s := newTestService(t, f)
			var managed []storedLauncher
			for _, c := range tc.Candidates {
				switch c.Kind {
				case "runtime":
					f.launcherList = append(f.launcherList, proto.Launcher{Name: c.Name, Source: c.Source, Agent: c.Agent, Model: c.Model, Assistant: c.Assistant})
				case "api", "launcher":
					managed = append(managed, storedLauncher{ManagedLauncher: proto.ManagedLauncher{
						Kind: c.Kind, API: c.API, Name: c.Name, Agent: c.Agent, BaseURL: c.BaseURL, Model: c.Model,
					}})
				default:
					t.Fatalf("candidate %s has unknown kind %q", c.Name, c.Kind)
				}
			}
			putLaunchers(t, s, managed...)
			if got := pickerNames(t, s, "assistant"); !slices.Equal(got, tc.Assistant) {
				t.Fatalf("assistant choices = %v, want %v", got, tc.Assistant)
			}
			if got := pickerNames(t, s, "agent.new"); !slices.Equal(got, tc.AgentNew) {
				t.Fatalf("generic New Agent choices = %v, want %v", got, tc.AgentNew)
			}
		})
	}
}

// A message for one of the assistant's conversations is held to the same rule: a launcher picked for it that can't
// run the assistant is refused, saying why; one a conversation was left on falls back to the main chat's.
func TestSendRefusesOrReplacesALauncherThatCannotRunTheAssistant(t *testing.T) {
	ctx := context.Background()
	s, f := mainChatFixture(t)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.gw"})
	f.setSessions("server", proto.Session{SessionID: "side", Launcher: "cc-relay", Model: "kimi", Assistant: true},
		proto.Session{SessionID: "side-codex", Launcher: "codex-gw", Assistant: true},
		proto.Session{SessionID: "ok", Launcher: "cx", Assistant: true})
	for _, name := range []string{"cc-relay", "codex-gw", "traex"} {
		_, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "ok", Lang: "zh-Hans", Params: map[string]any{"text": "hi", "launcher": name}})
		if err == nil || !strings.Contains(err.Error(), "「"+name+"」") || !strings.Contains(err.Error(), "跑不了助手对话") {
			t.Fatalf("picked %s: %v", name, err)
		}
	}
	for _, sid := range []string{"side", "side-codex"} {
		p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: sid, Params: map[string]any{"text": "hi"}})
		if err != nil || p["launcher"] != "gw" || p["model"] != nil {
			t.Fatalf("%s left on a launcher that can't run it: %v %v", sid, p, err)
		}
	}
	p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "ok", Params: map[string]any{"text": "hi", "launcher": "agy-rt"}})
	if err != nil || p["launcher"] != "agy-rt" {
		t.Fatalf("a runtime that runs the assistant: %v %v", p, err)
	}
	f.launcherList = nil // the list can't be read: nothing is refused on a guess
	if p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "ok", Params: map[string]any{"text": "hi", "launcher": "cc-relay"}}); err != nil || p["launcher"] != "cc-relay" {
		t.Fatalf("unknown list: %v %v", p, err)
	}
	if _, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "ok", Params: map[string]any{"text": "hi", "launcher": "codex-gw"}}); err == nil {
		t.Fatal("a managed Codex launcher is the same everywhere: refused without the list")
	}
}

func TestSendValidatesTheResolvedAssistantLauncher(t *testing.T) {
	ctx := context.Background()
	t.Run("old managed Codex default on the main conversation", func(t *testing.T) { // gives way to a Codex launcher, never to Claude (#4659)
		s, f := mainChatFixture(t)
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.codex-gw", Model: "old-model"})
		setSettings(t, s, func(v *Settings) { v.Main = "main" })
		f.setSessions("server", proto.Session{SessionID: "main", Assistant: true})
		p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "main", Params: map[string]any{"text": "hi"}})
		if err != nil || p["launcher"] != "cx" || p["model"] != "old-model" { // cx's list can't be read: kept (launcherHasModel)
			t.Fatalf("resolved default was not replaced with a verified compatible launcher of its kind: %v %v", p, err)
		}
	})
	t.Run("legacy side falls back to the same incompatible default", func(t *testing.T) {
		s, f := mainChatFixture(t)
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.cc-relay"})
		f.setSessions("server", proto.Session{SessionID: "side", Launcher: "cc-relay", Assistant: true})
		p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": "hi"}})
		if err != nil || p["launcher"] != "claude" || p["model"] != nil {
			t.Fatalf("fallback resolved to the same incompatible launcher: %v %v", p, err)
		}
	})
}

func TestStartOnRefusesALauncherThatCannotRunTheAssistant(t *testing.T) {
	for _, name := range []string{"codex-gw", "cc-relay"} {
		t.Run(name, func(t *testing.T) {
			s, f := mainChatFixture(t)
			_, err := s.startOn(context.Background(), "server", StartParams{Text: "hi", Launcher: name}, false)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("first message used an incompatible launcher: %v", err)
			}
			if len(f.sends) != 0 {
				t.Fatalf("first message reached the node: %v", f.sends)
			}
		})
	}
}

func TestMainChatPickerUsesCurrentLauncherFacts(t *testing.T) {
	ctx := context.Background()
	t.Run("shell added after the global discovery cache", func(t *testing.T) {
		s, f := mainChatFixture(t)
		_ = s.modelProviders(ctx) // Prime the global discovery snapshot before the node's current list changes.
		f.launcherList = append(f.launcherList, proto.Launcher{Name: "fresh-shell", Source: "zshrc", Agent: "claude"})
		if names := pickerNames(t, s, "assistant"); !slices.Contains(names, "fresh-shell") {
			t.Fatalf("current shell launcher missing from assistant picker: %v", names)
		}
	})
	t.Run("configured wrapper", func(t *testing.T) {
		s, f := mainChatFixture(t)
		f.hello.Models.Launchers = map[string][]ConfiguredLauncher{
			"server": {{Launcher: proto.Launcher{Name: "configured-claude"}, AgentType: "claude"}},
		}
		f.setSessions("server", proto.Session{SessionID: "ok", Assistant: true})
		if names := pickerNames(t, s, "assistant"); !slices.Contains(names, "configured-claude") {
			t.Fatalf("configured wrapper missing from assistant picker: %v", names)
		}
		p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "ok", Params: map[string]any{"text": "hi", "launcher": "configured-claude"}})
		if err != nil || p["launcher"] != "configured-claude" {
			t.Fatalf("configured wrapper could not run the assistant: %v %v", p, err)
		}
	})
	t.Run("runtime declares assistant capability", func(t *testing.T) {
		s, f := mainChatFixture(t)
		f.launcherList = append(f.launcherList, proto.Launcher{Name: "dsh-rt", Source: "dsh", Agent: "dsh", Assistant: true})
		if names := pickerNames(t, s, "assistant"); !slices.Contains(names, "dsh-rt") {
			t.Fatalf("assistant-capable runtime missing from assistant picker: %v", names)
		}
	})
}

// A Paseo provider only starts Agents; a runtime set up to run the assistant for it (integrations/grok's grok-chat,
// declared with assistant_for) is what the main chat's picker points it at, by the name people know it by. Nothing is
// matched on a likeness of names or agents, and nothing runs on one in the other's place.
func TestMainChatPointsAnAgentLauncherAtTheAssistantDeclaredForIt(t *testing.T) {
	ctx := context.Background()
	s, f := mainChatFixture(t)
	f.launcherList = append(f.launcherList,
		proto.Launcher{Name: "grok", Source: "paseo", Agent: "paseo", DisplayName: "Grok · Official"},
		proto.Launcher{Name: "grok-chat", Source: "grok-chat", Agent: "grok", Assistant: true, AssistantFor: "grok", DisplayName: "Grok"},
		proto.Launcher{Name: "grok-other", Source: "paseo", Agent: "paseo", DisplayName: "Grok · Work"}, // the same agent, another account: not pointed
	)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.gw"})
	f.setSessions("server", proto.Session{SessionID: "ok", Launcher: "cx", Assistant: true})

	r := appRequest(t, s, "GET", "/api/v1/machines/server/launchers?purpose=assistant", "", "zh-Hans")
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	var out struct {
		Launchers   []ConfiguredLauncher  `json:"launchers"`
		Unavailable []unavailableLauncher `json:"unavailable"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(out.Launchers, func(l ConfiguredLauncher) bool { return l.Name == "grok-chat" && l.DisplayName == "Grok" }) {
		t.Fatalf("the assistant runtime is not offered by its name: %s", r.Body.String())
	}
	reasons := map[string]unavailableLauncher{}
	for _, u := range out.Unavailable {
		reasons[u.Name] = u
	}
	if u := reasons["grok"]; u.Code != unavailableNotMainChat || u.Instead != "grok-chat" || u.DisplayName != "Grok · Official" ||
		!strings.Contains(u.Reason, "助手对话请选「Grok」") {
		t.Fatalf("grok: %+v", u)
	}
	for _, name := range []string{"grok-other", "cc-relay"} {
		if u := reasons[name]; u.Code != unavailableNotMainChat || u.Instead != "" || strings.Contains(u.Reason, "请选") {
			t.Fatalf("%s pointed at an assistant nobody declared for it: %+v", name, u)
		}
	}

	// A message or a pick on it is refused and names the one to choose; it never runs on grok-chat in its place.
	_, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "ok", Lang: "en", Params: map[string]any{"text": "hi", "launcher": "grok"}})
	if err == nil || !strings.Contains(err.Error(), "Grok · Official can start Agents") || !strings.Contains(err.Error(), "Choose Grok in the menu") {
		t.Fatalf("send on grok: %v", err)
	}
	if len(f.sends) != 0 {
		t.Fatalf("reached the node: %v", f.sends)
	}
	setSettings(t, s, func(v *Settings) { v.Main = "ok" })
	w := putPick(t, s, "ok", `{"machine_id":"server","launcher":"grok"}`)
	if code, msg := errorBody(t, w); w.Code != 400 || !strings.Contains(msg, "Choose Grok for assistant chats") {
		t.Fatalf("pick grok: %d %s %s", w.Code, code, msg)
	}
	if w := putPick(t, s, "ok", `{"machine_id":"server","launcher":"grok-other"}`); w.Code != 400 || strings.Contains(w.Body.String(), "for assistant chats") {
		t.Fatalf("pick grok-other: %d %s", w.Code, w.Body.String())
	}
	if p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "ok", Params: map[string]any{"text": "hi", "launcher": "grok-chat"}}); err != nil || p["launcher"] != "grok-chat" {
		t.Fatalf("the declared assistant runtime: %v %v", p, err)
	}
}

// Plain CLI fallbacks for agy and omp (Source "zshrc", Assistant false when their companion runtime is away) stay out
// of the assistant picker and in unavailable; once the companion connects with Assistant true, agy is offered.
func TestPlainAgyAndOmpRequireConnectedAssistantRuntime(t *testing.T) {
	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{
		{Name: "claude"},
		{Name: "agy", Source: "zshrc", Agent: "agy"},
		{Name: "omp", Source: "zshrc", Agent: "omp"},
	}
	s := newTestService(t, f)
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "agy-gw", Agent: "agy", BaseURL: "https://gw.example", Model: "m1"}})
	if got := strings.Join(pickerNames(t, s, "assistant"), ","); got != "claude" {
		t.Fatalf("assistant without runtime: %s", got)
	}
	if got := strings.Join(pickerNames(t, s, "agent.new"), ","); got != "claude,agy,omp,agy-gw" {
		t.Fatalf("agent.new without runtime: %s", got)
	}

	f.launcherList = []proto.Launcher{
		{Name: "claude"},
		{Name: "agy", Source: "agy", Agent: "agy", Assistant: true},
		{Name: "omp", Source: "zshrc", Agent: "omp"},
	}
	if got := strings.Join(pickerNames(t, s, "assistant"), ","); got != "claude,agy,agy-gw" {
		t.Fatalf("assistant with agy runtime: %s", got)
	}
}
