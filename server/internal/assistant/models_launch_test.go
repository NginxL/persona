package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestModelPurposeLaunch(t *testing.T) {
	for _, tc := range []struct {
		id, parent, want string
	}{
		{"assistant", "", "headless"},
		{"schedule", "assistant", "headless"},
		{"task.example", "assistant", "headless"},
		{"agent.new", "", "engine"},
		{"inbox.agent", "agent.new", "headless"},
		{"lark.agent", "agent.new", "headless"},
		{purposeMeetingRoom, "assistant", "headless"},
		{"child", "assistant", "headless"},
		{"child", "agent.new", "engine"},
		{"unknown", "", ""},
		{"kb", "", ""},
		{"inbox.triage", "", ""},
		{"agent.delegate", "", ""},
		{"plugin.voice", "", ""},
		{"extension.example", "kb", ""},
		{purposeMeetingMinutes, "kb", ""},
	} {
		for _, kind := range []string{"launcher", "api", "plugin", ""} {
			p := ModelPurpose{ID: tc.id, Parent: tc.parent, Kind: kind}
			want := tc.want
			if kind != "launcher" {
				want = ""
			}
			if got := p.launch(); got != want {
				t.Errorf("%s (%s, parent %s): launch %q, want %q", tc.id, kind, tc.parent, got, want)
			}
		}
	}
}

func TestModelLaunchJSONIsDerivedAndReadOnly(t *testing.T) {
	s, _, _ := modelService(t)
	r := modelRequest(s, "GET", "", "")
	var list struct {
		Purposes []map[string]any `json:"purposes"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &list) != nil {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	var sample []map[string]any
	for _, p := range list.Purposes {
		item := map[string]any{"id": p["id"], "kind": p["kind"]}
		if launch, ok := p["launch"]; ok {
			item["launch"] = launch
		}
		sample = append(sample, item)
		want := map[string]string{"assistant": "headless", "agent.new": "engine", "inbox.agent": "headless", "lark.agent": "headless", purposeSupervise: "headless", purposeMeetingRoom: "headless"}[p["id"].(string)]
		if want == "" {
			if _, ok := p["launch"]; ok {
				t.Fatalf("non-launcher serialized launch: %v", p)
			}
		} else if p["launch"] != want {
			t.Fatalf("catalog: %v, want %s", p, want)
		}
		detail := purposeOf(t, s, p["id"].(string))
		if detail.Launch != want {
			t.Fatalf("detail: %+v, want %s", detail, want)
		}
	}
	raw, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("GET /api/v1/model-config: purposes=%s", raw)
	for _, verb := range []string{"PATCH", "PUT"} {
		if r := modelRequest(s, verb, "assistant", `{"revision":0,"launch":"engine"}`); r.Code != 400 {
			t.Fatalf("%s accepted a derived field: %d %s", verb, r.Code, r.Body)
		}
	}
}

func TestApplyModelOverwritesCallerLaunchWithoutChangingPicks(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	for id, want := range map[string]string{"assistant": "headless", "schedule": "headless", "inbox.agent": "headless", "agent.new": "engine", "lark.agent": "headless"} {
		params := map[string]any{"launch": "caller", "launcher": "picked", "model": "picked-model", "effort": "high"}
		if err := s.applyModel(ctx, id, params); err != nil {
			t.Fatal(err)
		}
		if params["launch"] != want || params["launcher"] != "picked" || params["model"] != "picked-model" || params["effort"] != "high" {
			t.Fatalf("%s: %v", id, params)
		}
	}
	f.voice = []assistantwire.VoicePlugin{{ID: "voice", Name: "Voice", Default: "v1", Models: []ModelChoice{{ID: "v1"}}}}
	setModel(t, s, "plugin.voice", ModelBinding{Model: "v1"})
	params := map[string]any{"launch": "engine"}
	if err := s.applyModel(ctx, "plugin.voice", params); err != nil || params["launch"] != "" || params["model"] != "v1" {
		t.Fatalf("plugin took caller launch: %v %v", params, err)
	}
}

func TestConversationStartRequiresHeadless(t *testing.T) {
	s, f, _ := modelService(t)
	f.voice = []assistantwire.VoicePlugin{{ID: "voice", Name: "Voice", Default: "v1", Models: []ModelChoice{{ID: "v1"}}}}
	for _, id := range []string{"agent.new", "kb", "plugin.voice"} {
		setModel(t, s, modelCategoryID(id), ModelBinding{Provider: "launcher.claude"})
		if _, err := s.startOn(context.Background(), "server", StartParams{Purpose: id}, false); err == nil || !strings.Contains(err.Error(), "headless") {
			t.Fatalf("%s entered assistant.send: %v", id, err)
		}
	}
	if f.sent() != 0 {
		t.Fatal("a rejected purpose reached the node")
	}
	for _, id := range []string{"", "inbox.agent", "lark.agent", "schedule"} {
		if _, err := s.startOn(context.Background(), "server", StartParams{Purpose: id}, false); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if send := f.lastSend(t); send["launch"] != "headless" {
			t.Fatalf("%s: headless launch missing: %v", id, send)
		}
	}
}

func TestAgentStartRequiresEngine(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	params, err := s.AgentStartParams(ctx, AgentStartParams{Params: map[string]any{"launch": "headless"}})
	if err != nil || params["launch"] != "engine" {
		t.Fatalf("engine launch missing: %v %v", params, err)
	}
	// A catalog regression must not send an API/plugin purpose into session.start,
	// even with a launcher binding that finishModel can otherwise apply.
	before := modelPurposes
	modelPurposes = append([]ModelPurpose(nil), modelPurposes...)
	t.Cleanup(func() { modelPurposes = before })
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.claude"})
	for _, kind := range []string{"api", "plugin"} {
		for i := range modelPurposes {
			if modelPurposes[i].ID == "agent.new" {
				modelPurposes[i].Kind = kind
			}
		}
		if _, err := s.AgentStartParams(ctx, AgentStartParams{}); err == nil || !strings.Contains(err.Error(), "engine") {
			t.Fatalf("%s entered session.start: %v", kind, err)
		}
	}
}
