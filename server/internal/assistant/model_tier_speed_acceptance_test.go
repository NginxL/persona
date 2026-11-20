package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestModelTierSpeedSummaryKeepsStandardModel11141(t *testing.T) {
	got := (ModelTier{Launcher: "codex", Model: "standard", Speed: "fast"}).runs()
	if !strings.Contains(got, "standard") || !strings.Contains(got, "fast") {
		t.Fatalf("tier summary dropped a model named standard: %q", got)
	}
}

func TestModelTierSpeedSettingRoundTrip11141(t *testing.T) {
	for _, speed := range []string{"", "standard", "fast", "ultrafast"} {
		tiers := map[string]ModelTier{"standard": speedTier11141(t, ModelTier{Launcher: "codex", Model: "gpt-tier"}, speed)}
		if err := checkModelTiers(tiers); err != nil {
			t.Errorf("valid speed %q: %v", speed, err)
		}
	}
	if tiers := map[string]ModelTier{"standard": speedTier11141(t, ModelTier{Launcher: "codex"}, "ludicrous")}; checkModelTiers(tiers) == nil {
		t.Error("an invalid preset speed passed validation")
	}

	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{{Name: "codex", Agent: "codex"}, {Name: "claude", Agent: "claude"}}
	s := newTestService(t, f)
	putTierProfiles(t, s, "economy", map[string]ModelTier{"standard": {Launcher: "codex", Model: "gpt-tier"}}, nil)
	call := ToolCall{MachineID: "server", SessionID: "c1", Name: "persona_settings",
		Arguments: json.RawMessage(`{"action":"set","key":"model_profiles.tiers","value":{"tier":"standard","speed":"fast","profiles":["economy"]}}`)}
	out := runSpeedTool11141(t, s, f, call, true)
	if out.result.IsError {
		t.Fatalf("persona_settings refused a valid speed: %+v", out.result)
	}
	if out.card == nil || !strings.Contains(out.card.Summary, "fast") {
		t.Fatalf("tier approval did not show speed: %+v", out.card)
	}
	var saved struct {
		Profiles []struct {
			ID    string `json:"id"`
			Tiers map[string]struct {
				Speed string `json:"speed"`
			} `json:"tiers"`
		} `json:"profiles"`
	}
	if ok, err := s.store().Setting(context.Background(), modelProfilesKey, &saved); err != nil || !ok {
		t.Fatalf("read saved tiers: ok %v err %v", ok, err)
	}
	if len(saved.Profiles) == 0 || saved.Profiles[0].Tiers["standard"].Speed != "fast" {
		t.Fatalf("speed did not persist through persona_settings: %+v", saved)
	}
	got := s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: "c1", Name: "persona_settings",
		Arguments: json.RawMessage(`{"action":"get","key":"model_profiles.tiers"}`)})
	if got.IsError || len(got.Content) != 1 || !strings.Contains(got.Content[0].Text, "fast") {
		t.Fatalf("persona_settings get omitted speed: %+v", got)
	}
}

func TestModelTierSpeedRefusedBeforeTierApproval11141(t *testing.T) {
	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{{Name: "codex", Agent: "codex"}}
	s := newTestService(t, f)
	putTierProfiles(t, s, "economy", map[string]ModelTier{"standard": {Launcher: "codex"}}, nil)
	out := runSpeedTool11141(t, s, f, ToolCall{MachineID: "server", SessionID: "c1", Name: "persona_settings",
		Arguments: json.RawMessage(`{"action":"set","key":"model_profiles.tiers","value":{"tier":"standard","speed":"ludicrous"}}`)}, false)
	if !out.result.IsError || out.card != nil || len(startsOf(f)) != 0 {
		t.Fatalf("invalid speed reached approval: %+v card %+v", out.result, out.card)
	}
}

func newSpeedPresetService11141(t *testing.T, speed string) *Service {
	t.Helper()
	s, _ := defaultsHub(t)
	saveTiers(t, s, map[string]ModelTier{
		"deep":  speedTier11141(t, ModelTier{Launcher: "gateway", Model: "gw-deep", Effort: "high", Description: "deep work"}, speed),
		"plain": {Launcher: "claude"},
	})
	return s
}

func TestSpeedInPresetRoutes11141(t *testing.T) {
	s := newSpeedPresetService11141(t, "ultrafast")
	for _, path := range []string{"/api/v1/machines/box/launchers", "/api/v1/assistant/worker-presets?machine=box"} {
		t.Run(path, func(t *testing.T) {
			w := request(s, http.MethodGet, path, "")
			if w.Code != http.StatusOK {
				t.Fatalf("GET: %d %s", w.Code, w.Body)
			}
			var body struct {
				Tiers []struct {
					Name  string `json:"name"`
					Speed string `json:"speed"`
				} `json:"tiers"`
				Presets []struct {
					Name  string `json:"name"`
					Speed string `json:"speed"`
				} `json:"presets"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			rows := body.Tiers
			if rows == nil {
				rows = body.Presets
			}
			for _, row := range rows {
				if row.Name == "deep" && row.Speed == "ultrafast" {
					return
				}
			}
			t.Fatalf("deep preset has no ultrafast speed: %s", w.Body)
		})
	}
}

func TestSpeedInSnapshotPresetLine11141(t *testing.T) {
	s := newSpeedPresetService11141(t, "fast")
	snapshot := s.contextSnapshot(context.Background(), "", false)
	if !strings.Contains(snapshot, "deep") || !strings.Contains(snapshot, "fast") {
		t.Fatalf("snapshot preset line omitted speed:\n%s", snapshot)
	}
}

func speedSchemaFields11141(t *testing.T, tool proto.Tool, nested bool) (speed, tier map[string]any) {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("%s schema: %v", tool.Name, err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s schema properties: %s", tool.Name, tool.InputSchema)
	}
	if nested {
		tasks, ok := properties["tasks"].(map[string]any)
		if !ok {
			t.Fatalf("%s tasks schema: %s", tool.Name, tool.InputSchema)
		}
		items, ok := tasks["items"].(map[string]any)
		if !ok {
			t.Fatalf("%s task items schema: %s", tool.Name, tool.InputSchema)
		}
		properties, ok = items["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s task properties: %s", tool.Name, tool.InputSchema)
		}
	}
	speed, speedOK := properties["speed"].(map[string]any)
	tier, tierOK := properties["tier"].(map[string]any)
	if !speedOK || !tierOK {
		t.Fatalf("%s schema lacks speed or tier: %s", tool.Name, tool.InputSchema)
	}
	return speed, tier
}

func TestSpeedToolSchemas11141(t *testing.T) {
	s, _, _ := newProfileTaskService(t)
	raw, err := json.Marshal(map[string]any{"session_id": "main", "assistant": true})
	if err != nil {
		t.Fatal(err)
	}
	out, handled, err := s.NodeRequest(context.Background(), "server", "mcp.tools", raw)
	if err != nil || !handled {
		t.Fatalf("mcp.tools: handled %v err %v", handled, err)
	}
	listed, ok := out.(map[string]any)["tools"].([]proto.Tool)
	if !ok {
		t.Fatalf("mcp.tools result: %+v", out)
	}
	tools := map[string]proto.Tool{}
	for _, tool := range listed {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"agent_start", "task_plan"} {
		if _, ok := tools[name]; !ok {
			t.Fatalf("MCP did not list %s", name)
		}
	}
	tools["leader_task_plan"] = leaderPlanTool()
	for _, name := range []string{"agent_start", "task_plan", "leader_task_plan"} {
		t.Run(name, func(t *testing.T) {
			tool := tools[name]
			speed, tier := speedSchemaFields11141(t, tool, name != "agent_start")
			rawEnum, err := json.Marshal(speed["enum"])
			if err != nil {
				t.Fatal(err)
			}
			var enum []string
			if err := json.Unmarshal(rawEnum, &enum); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(enum, []string{"standard", "fast", "ultrafast"}) {
				t.Errorf("speed enum = %v", enum)
			}
			description, _ := speed["description"].(string)
			description = strings.ToLower(tool.Description + " " + description)
			for _, word := range []string{"speed", "standard", "fast", "ultrafast"} {
				if !strings.Contains(description, word) {
					t.Errorf("%s speed description lacks %q", name, word)
				}
			}
			tierDescription, _ := tier["description"].(string)
			if !strings.Contains(strings.ToLower(tierDescription), "speed") {
				t.Errorf("%s tier description does not say explicit speed overrides the preset", name)
			}
		})
	}
}
