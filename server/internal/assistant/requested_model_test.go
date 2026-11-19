package assistant

import (
	"context"
	"testing"

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
