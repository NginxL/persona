package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAssistantLauncherModelsAreScopedAndCachedSeparately(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	f.answerNode(assistantwire.DeviceLaunchersModels, func(_ string, raw json.RawMessage) (any, error) {
		var p struct{ Purpose string }
		_ = json.Unmarshal(raw, &p)
		model := "provider-model"
		if p.Purpose == "assistant" {
			model = "shell-model"
		}
		return proto.LauncherModels{Agent: "claude", Models: []proto.AgentModel{{ID: model}}}, nil
	})
	for _, purpose := range []string{"", "assistant", ""} {
		want := "provider-model"
		if purpose == "assistant" {
			want = "shell-model"
		}
		r := request(s, "GET", "/api/v1/machines/server/launchers/claude/models?purpose="+purpose, "")
		if r.Code != 200 || !strings.Contains(r.Body.String(), want) {
			t.Fatalf("%s: %d %s", purpose, r.Code, r.Body.String())
		}
		ctx := withLauncherPurpose(context.Background(), purpose)
		set := s.launcherModelSet(ctx, "server", "claude")
		if !set[want] || len(set) != 1 {
			t.Fatalf("%s cached models: %v", purpose, set)
		}
	}
}

func TestAgentLauncherModelsPassExplicitHost(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	f.answerNode(assistantwire.DeviceLaunchersModels, func(_ string, raw json.RawMessage) (any, error) {
		var params map[string]any
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		model := "companion-model"
		if params["host"] == proto.EngineHerdr {
			model = "local-model"
		}
		if params["purpose"] == "assistant" && params["host"] != nil {
			t.Error("assistant catalog has an engine host")
		}
		return proto.LauncherModels{Agent: "agy", Models: []proto.AgentModel{{ID: model}}}, nil
	})
	for _, tc := range []struct{ query, want string }{
		{"", "companion-model"},
		{"?host=herdr", "local-model"},
		{"", "companion-model"},
		{"?purpose=assistant&host=herdr", "companion-model"},
	} {
		r := request(s, "GET", "/api/v1/machines/server/launchers/agy/models"+tc.query, "")
		if r.Code != 200 || !strings.Contains(r.Body.String(), tc.want) {
			t.Fatalf("%q: %d %s, want %s", tc.query, r.Code, r.Body.String(), tc.want)
		}
	}
}

func TestAssistantLauncherCheckPassesPurposeToNode(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	f.answerNode(assistantwire.DeviceLaunchersCheck, func(_ string, raw json.RawMessage) (any, error) {
		var p struct{ Purpose string }
		_ = json.Unmarshal(raw, &p)
		return proto.LauncherCheck{OK: true, Answer: "checked:" + p.Purpose}, nil
	})
	for _, purpose := range []string{"assistant", ""} {
		r := request(s, "POST", "/api/v1/launchers/claude/check?machine=server&purpose="+purpose, "")
		if r.Code != 200 || !strings.Contains(r.Body.String(), `"answer":"checked:`+purpose+`"`) {
			t.Fatalf("%s: %d %s", purpose, r.Code, r.Body.String())
		}
	}
}

func TestAssistantFollowupKeepsModelFromAssistantCatalog(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{{Name: "claude"}, {Name: "shared", Source: "zshrc"}}
	f.setSessions("server", proto.Session{SessionID: "side", Launcher: "shared", Model: "shell-model", Assistant: true})
	s := newTestService(t, f)
	f.answerNode(assistantwire.DeviceLaunchersModels, func(_ string, raw json.RawMessage) (any, error) {
		var p struct{ Purpose string }
		_ = json.Unmarshal(raw, &p)
		model := "engine-model"
		if p.Purpose == "assistant" {
			model = "shell-model"
		}
		return proto.LauncherModels{Agent: "claude", Models: []proto.AgentModel{{ID: model}}}, nil
	})
	// Visiting New Agent first must not poison a conversation's carried model on its next ordinary message.
	if !s.launcherModelSet(ctx, "server", "shared")["engine-model"] {
		t.Fatal("Agent catalog was not primed")
	}
	p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "side", Params: map[string]any{"text": "Continue"}})
	if err != nil || p["launcher"] != "shared" || p["model"] != "shell-model" {
		t.Fatalf("follow-up lost its assistant model: %v (%v)", p, err)
	}
}

func TestAssistantBotModelMenuUsesAssistantCatalog(t *testing.T) {
	s, f := pickFixture(t)
	f.answerNode(assistantwire.DeviceLaunchersModels, func(_ string, raw json.RawMessage) (any, error) {
		var p struct{ Purpose string }
		_ = json.Unmarshal(raw, &p)
		model := "engine-model"
		if p.Purpose == "assistant" {
			model = "shell-model"
		}
		return proto.LauncherModels{Agent: "claude", Models: []proto.AgentModel{{ID: model}}}, nil
	})
	var note string
	f.answerApp(AppFeishu, FeishuText, func(raw json.RawMessage) (any, error) {
		var p struct{ Text string }
		_ = json.Unmarshal(raw, &p)
		note = p.Text
		return map[string]any{}, nil
	})
	s.setBotCurrent(botTarget{Kind: targetMain})
	s.botModels(context.Background(), FeishuFeatures{Main: true}, "message", "/modellist", "", "", "")
	if !strings.Contains(note, "shell-model") || strings.Contains(note, "engine-model") {
		t.Fatalf("assistant bot listed the Agent catalog: %q", note)
	}
}

func TestAssistantLauncherResolutionUsesScopedCatalog(t *testing.T) {
	for _, primeAgent := range []bool{false, true} {
		name := "without_agent_cache"
		if primeAgent {
			name = "with_agent_cache"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			s := newTestService(t, f)
			f.answerNode(assistantwire.DeviceLaunchersModels, func(_ string, raw json.RawMessage) (any, error) {
				var p struct{ Purpose string }
				_ = json.Unmarshal(raw, &p)
				model := "engine-model"
				if p.Purpose == "assistant" {
					model = "shell-model"
				}
				return proto.LauncherModels{Agent: "claude", Models: []proto.AgentModel{{ID: "default", Resolved: model, Default: true}}}, nil
			})
			if primeAgent {
				s.launcherModelSet(context.Background(), "server", "shared")
			}
			aliases, model := s.launcherModelResolution(withLauncherPurpose(context.Background(), "assistant"), "server", "shared")
			if model != "shell-model" || aliases["default"] != "shell-model" {
				t.Fatalf("assistant resolution reused another purpose: aliases=%v default=%q", aliases, model)
			}
		})
	}
}

func TestAssistantReadinessChecksAssistantExecution(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	f.answerNode(assistantwire.DeviceLaunchersCheck, func(_ string, raw json.RawMessage) (any, error) {
		var p struct{ Purpose string }
		_ = json.Unmarshal(raw, &p)
		model := "engine-model"
		if p.Purpose == "assistant" {
			model = "shell-model"
		}
		return proto.LauncherCheck{OK: true, Model: model}, nil
	})
	got := s.readinessProbe(context.Background(), "server", s.settings(context.Background()))
	if got == nil || got.Model != "shell-model" {
		t.Fatalf("assistant readiness tested a different process: %+v", got)
	}
}

func TestAssistantReadinessReplyUsesAssistantAliases(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	f.answerNode(assistantwire.DeviceLaunchersModels, func(_ string, raw json.RawMessage) (any, error) {
		var p struct{ Purpose string }
		_ = json.Unmarshal(raw, &p)
		model := "engine-model"
		if p.Purpose == "assistant" {
			model = "shell-model"
		}
		return proto.LauncherModels{Agent: "claude", Models: []proto.AgentModel{{ID: "default", Resolved: model, Default: true}}}, nil
	})
	ctx := context.Background()
	s.launcherModelSet(ctx, "server", "shared")
	sess := proto.Session{Launcher: "shared", Model: "shell-model", Status: proto.StatusDone, State: proto.StateIdle, LastText: "Completed", UpdatedAt: time.Now()}
	binding := ModelBinding{Provider: "launcher.shared", Model: "default"}
	if !s.readinessReplySucceeded(ctx, "server", "shared", sess, binding, "binding", nil) {
		t.Fatal("the assistant's default alias did not recognize its own completed reply")
	}
	sess.Model = "engine-model"
	if s.readinessReplySucceeded(ctx, "server", "shared", sess, binding, "binding", nil) {
		t.Fatal("another execution catalog's reply validated the assistant")
	}
}
