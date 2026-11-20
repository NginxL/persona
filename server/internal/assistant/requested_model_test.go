package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSendKeepsRequestedModelAfterGatewayEcho(t *testing.T) {
	for _, tc := range []struct {
		name, requested, reported, explicit, want string
		legacy                                    bool
	}{
		{name: "gateway alias", requested: "auto/large", reported: "group/auto-large", want: "auto/large"},
		{name: "launcher default", reported: "group/auto-large"},
		{name: "message pick wins", requested: "auto/large", reported: "group/auto-large", explicit: "typed/model", want: "typed/model"},
		{name: "older node", reported: "auto/large", want: "auto/large", legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := mainChatFixture(t)
			f.agentModels = map[string][]proto.AgentModel{"gw": {{ID: "auto/large"}}}
			c := proto.Session{SessionID: "side", Assistant: true, Launcher: "gw", Model: tc.reported}
			if !tc.legacy {
				c.RequestedModel = &tc.requested
			}
			f.setSessions("server", c)
			params := map[string]any{"text": "next turn"}
			if tc.explicit != "" {
				params["model"] = tc.explicit
			}
			ctx, note := withLauncherNote(context.Background())
			p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side", Lang: "en", Params: params})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := p["model"].(string)
			if got != tc.want || p["launcher"] != "gw" || *note != "" {
				t.Fatalf("params=%v note=%q; want model %q", p, *note, tc.want)
			}
		})
	}
}

func TestMissingRequestedModelExplainsFallback(t *testing.T) {
	for _, lang := range []i18n.Lang{"en", "zh-Hans", "ja"} {
		t.Run(string(lang), func(t *testing.T) {
			s, f := mainChatFixture(t)
			requested := "removed/model"
			f.agentModels = map[string][]proto.AgentModel{"gw": {{ID: "m1"}}}
			f.setSessions("server", proto.Session{SessionID: "side", Assistant: true, Launcher: "gw", Model: "gateway/echo", RequestedModel: &requested})
			ctx, note := withLauncherNote(context.Background())
			p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side", Lang: string(lang), Params: map[string]any{"text": "next turn"}})
			if err != nil {
				t.Fatal(err)
			}
			want := i18n.T(lang, "assistant-start.model_missing", i18n.Args{"model": requested, "launcher": "gw"})
			if p["model"] != nil || p["launcher"] != "gw" || *note != want {
				t.Fatalf("params=%v note=%q; want %q", p, *note, want)
			}
		})
	}
}

func TestRequestedModelDoesNotOverrideConfigurationOrConversationPick(t *testing.T) {
	for _, picked := range []bool{false, true} {
		t.Run(map[bool]string{false: "main configuration", true: "conversation pick"}[picked], func(t *testing.T) {
			s, f := mainChatFixture(t)
			requested := "old/request"
			f.setSessions("server", proto.Session{SessionID: "main", Assistant: true, Launcher: "gw", Model: "echo/old", RequestedModel: &requested})
			setSettings(t, s, func(v *Settings) { v.Main = "main" })
			setModel(t, s, "assistant", ModelBinding{Provider: "launcher.gw", Model: "configured/model"})
			want := "configured/model"
			if picked {
				want = "picked/model"
				if err := s.setModelPick(context.Background(), "main", &modelPick{MachineID: "server", Launcher: "gw", Model: want}); err != nil {
					t.Fatal(err)
				}
			}
			p, err := s.SendParams(context.Background(), SendParams{MachineID: "server", SessionID: "main", Lang: "en", Params: map[string]any{"text": "next turn"}})
			if err != nil || p["model"] != want {
				t.Fatalf("params=%v error=%v; want model %s", p, err, want)
			}
		})
	}
}

// A gateway may echo a model routed under a prefix, or the other spelling of its 1M marker: the send folds it back to
// the launcher's declared id before anything runs.
func TestSendFoldsAGatewayEchoToTheDeclaredModel(t *testing.T) {
	const carbon = "gemini-4-carbon"
	for _, tc := range []struct{ name, requested, declared, want string }{
		{name: "a routed echo", requested: "passthrough/route-gemini-4-carbon/" + carbon, declared: carbon, want: carbon},
		{name: "a plain echo of the 1m variant", requested: carbon, declared: carbon + "[1m]", want: carbon + "[1m]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := mainChatFixture(t)
			f.agentModels = map[string][]proto.AgentModel{"gw": {{ID: tc.declared}}}
			f.setSessions("server", proto.Session{SessionID: "side", Assistant: true, Launcher: "gw", Model: tc.requested, RequestedModel: &tc.requested})
			ctx, note := withLauncherNote(context.Background())
			p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side", Lang: "en", Params: map[string]any{"text": "next turn"}})
			if err != nil {
				t.Fatal(err)
			}
			if p["model"] != tc.want || p["launcher"] != "gw" || *note != "" {
				t.Fatalf("params=%v note=%q; want model %q", p, *note, tc.want)
			}
		})
	}
}

func TestResolveLauncherModel(t *testing.T) {
	s, f := mainChatFixture(t)
	ctx := context.Background()
	f.agentModels = map[string][]proto.AgentModel{"gw": {{ID: "gemini-4-carbon"}, {ID: "gw-small", Resolved: "gemini-4-carbon[1m]"}}}
	f.agentModels["gw1m"] = []proto.AgentModel{{ID: "gemini-4-carbon[1m]"}}
	f.agentModels["gwroute"] = []proto.AgentModel{{ID: "route-a/gemini-4-carbon"}}
	for _, tc := range []struct {
		name, launcher, model, want string
		ok                          bool
	}{
		{name: "an exact id", launcher: "gw", model: "gemini-4-carbon", want: "gemini-4-carbon", ok: true},
		{name: "a routed echo", launcher: "gw", model: "passthrough/route-gemini-4-carbon/gemini-4-carbon", want: "gemini-4-carbon", ok: true},
		{name: "a bare echo of a route-prefixed listing", launcher: "gwroute", model: "gemini-4-carbon", want: "route-a/gemini-4-carbon", ok: true},
		{name: "the 1m variant of a plain listing", launcher: "gw1m", model: "gemini-4-carbon", want: "gemini-4-carbon[1m]", ok: true},
		{name: "the plain form of a 1m listing", launcher: "gw1m", model: "gemini-4-carbon[1m]", want: "gemini-4-carbon[1m]", ok: true},
		{name: "a removed model", launcher: "gw", model: "removed/model", ok: false},
		{name: "a gone small model", launcher: "gw", model: "gw-gone", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := s.resolveLauncherModel(ctx, "server", tc.launcher, tc.model)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("resolveLauncherModel(%q, %q) = %q, %v; want %q, %v", tc.launcher, tc.model, got, ok, tc.want, tc.ok)
			}
		})
	}
	// A typed id is the user's own: it is offered even when the list lacks it, and comes back as typed.
	s.rememberCustomModel(ctx, "server", "gw", "vendor/typed-model")
	if got, ok := s.resolveLauncherModel(ctx, "server", "gw", "vendor/typed-model"); !ok || got != "vendor/typed-model" {
		t.Fatalf("a remembered typed id: %q, %v", got, ok)
	}
}

// A completed reply that echoes a routed name still validates a main-chat binding for the clean id, either spelling.
func TestReadinessReplyAcceptsARoutedEcho(t *testing.T) {
	s, f := mainChatFixture(t)
	f.agentModels = map[string][]proto.AgentModel{"gw": {{ID: "gemini-4-carbon[1m]"}}}
	ctx := context.Background()
	sess := proto.Session{Launcher: "gw", Model: "passthrough/route-x/gemini-4-carbon", Status: proto.StatusDone, State: proto.StateIdle, LastText: "Completed", UpdatedAt: time.Now()}
	for _, want := range []string{"gemini-4-carbon", "gemini-4-carbon[1m]"} {
		if !s.readinessReplySucceeded(ctx, "server", "gw", sess, ModelBinding{Provider: "launcher.gw", Model: want}, "binding", nil) {
			t.Fatalf("a routed echo did not validate %q", want)
		}
	}
	sess.Model = "gemini-4-argon"
	if s.readinessReplySucceeded(ctx, "server", "gw", sess, ModelBinding{Provider: "launcher.gw", Model: "gemini-4-carbon"}, "binding", nil) {
		t.Fatal("another model validated the binding")
	}
}
