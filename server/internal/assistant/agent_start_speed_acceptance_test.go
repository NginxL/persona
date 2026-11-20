package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// speedTier11141 adds speed through JSON, as the settings and MCP callers do. Keeping the construction on the wire
// shape lets the acceptance test compile on a hub whose ModelTier does not have the field yet.
func speedTier11141(t *testing.T, tier ModelTier, speed string) ModelTier {
	t.Helper()
	raw, err := json.Marshal(tier)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if speed != "" {
		fields["speed"] = speed
	}
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &tier); err != nil {
		t.Fatal(err)
	}
	return tier
}

func speedTask11141(t *testing.T, task PlanTask, speed string) PlanTask {
	t.Helper()
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if speed != "" {
		fields["speed"] = speed
	}
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	return task
}

func speedField11141(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	speed, _ := fields["speed"].(string)
	return speed
}

func newSpeedAgentService11141(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := healthHub()
	f.launcherList = []proto.Launcher{{Name: "codex", Agent: "codex"}, {Name: "claude", Agent: "claude"}, {Name: "agy", Agent: "agy"}}
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	return s, f
}

type speedToolOutcome11141 struct {
	result             proto.ToolResult
	card               *proto.Interaction
	startsBeforeAnswer int
}

// runSpeedTool11141 answers a card if the call makes one, but also returns a call refused before a card. It never
// leaves a watcher behind waiting for a card that a correct refusal did not create.
func runSpeedTool11141(t *testing.T, s *Service, f *fakeHub, call ToolCall, allow bool) speedToolOutcome11141 {
	t.Helper()
	call.Arguments = json.RawMessage(strings.TrimSpace(string(call.Arguments)))
	result := make(chan proto.ToolResult, 1)
	go func() { result <- s.Tool(context.Background(), call) }()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		if questions := s.pendingQuestions(); len(questions) == 1 {
			q := questions[0]
			out := speedToolOutcome11141{card: &q, startsBeforeAnswer: len(startsOf(f))}
			raw, err := json.Marshal(proto.Resolution{Allow: &allow})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: q.ID, Answer: raw}); err != nil {
				t.Fatal(err)
			}
			select {
			case out.result = <-result:
				return out
			case <-timeout.C:
				t.Fatal("tool did not return after its card was answered")
			}
		}
		select {
		case res := <-result:
			return speedToolOutcome11141{result: res}
		case <-tick.C:
		case <-timeout.C:
			t.Fatal("tool neither returned nor showed its approval card")
		}
	}
}

func TestAgentStartSpeedResolution11141(t *testing.T) {
	const base = `"machine":"server","cwd":"/home/me/src/app","prompt":"check the build"`
	for _, tc := range []struct {
		name, args, tierSpeed, wantLauncher, wantModel, wantSpeed string
	}{
		{name: "preset", args: `"tier":"standard"`, tierSpeed: "fast", wantLauncher: "codex", wantModel: "gpt-tier", wantSpeed: "fast"},
		{name: "explicit wins over preset", args: `"tier":"standard","speed":"ultrafast"`, tierSpeed: "fast", wantLauncher: "codex", wantModel: "gpt-tier", wantSpeed: "ultrafast"},
		{name: "standard overrides preset without an override", args: `"tier":"standard","speed":"standard"`, tierSpeed: "fast", wantLauncher: "codex", wantModel: "gpt-tier"},
		{name: "another model leaves preset speed behind", args: `"tier":"standard","model":"gpt-own"`, tierSpeed: "fast", wantLauncher: "codex", wantModel: "gpt-own"},
		{name: "another launcher leaves preset speed behind", args: `"tier":"standard","launcher":"claude"`, tierSpeed: "fast", wantLauncher: "claude"},
		{name: "explicit speed follows another launcher", args: `"tier":"standard","launcher":"claude","speed":"fast"`, tierSpeed: "ultrafast", wantLauncher: "claude", wantSpeed: "fast"},
		{name: "omitted preserves launcher default", args: `"launcher":"codex"`, wantLauncher: "codex"},
		{name: "standard preserves launcher default", args: `"launcher":"codex","speed":"standard"`, wantLauncher: "codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newSpeedAgentService11141(t)
			putTierProfiles(t, s, "economy", map[string]ModelTier{"standard": speedTier11141(t,
				ModelTier{Launcher: "codex", Model: "gpt-tier", Effort: "high"}, tc.tierSpeed)}, nil)
			out := runSpeedTool11141(t, s, f, ToolCall{MachineID: "server", SessionID: "sleep-000-0000", Name: "agent_start",
				Arguments: json.RawMessage(`{` + base + `,` + tc.args + `}`)}, true)
			if out.result.IsError {
				t.Fatalf("agent_start: %+v", out.result)
			}
			if out.card == nil || out.startsBeforeAnswer != 0 {
				t.Fatalf("approval before start: card %+v, starts before answer %d", out.card, out.startsBeforeAnswer)
			}
			starts := startsOf(f)
			if len(starts) != 1 {
				t.Fatalf("starts: %v", starts)
			}
			st := starts[0]
			if st["launcher"] != tc.wantLauncher || unset(st["model"]) && tc.wantModel != "" || st["model"] != tc.wantModel && tc.wantModel != "" {
				t.Fatalf("launcher/model: %v", st)
			}
			if got, _ := st["speed"].(string); got != tc.wantSpeed {
				t.Errorf("session.start speed = %q, want %q: %v", got, tc.wantSpeed, st)
			}
			if tc.wantSpeed != "" && !strings.Contains(out.card.Summary, tc.wantSpeed) {
				t.Errorf("approval card does not disclose %q: %s", tc.wantSpeed, out.card.Summary)
			}
		})
	}
}

func TestAgentStartSpeedRefusedBeforeApproval11141(t *testing.T) {
	for _, tc := range []struct{ name, launcher, speed string }{
		{name: "unsupported agent", launcher: "agy", speed: "fast"},
		{name: "Claude ultrafast", launcher: "claude", speed: "ultrafast"},
		{name: "invalid speed", launcher: "codex", speed: "ludicrous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newSpeedAgentService11141(t)
			args, err := json.Marshal(map[string]any{"machine": "server", "cwd": "/home/me/src/app", "prompt": "check the build",
				"launcher": tc.launcher, "speed": tc.speed})
			if err != nil {
				t.Fatal(err)
			}
			out := runSpeedTool11141(t, s, f, ToolCall{MachineID: "server", SessionID: "sleep-000-0000", Name: "agent_start", Arguments: args}, false)
			if !out.result.IsError || out.card != nil {
				t.Fatalf("faster speed was not refused before approval: result %+v card %+v", out.result, out.card)
			}
			if len(startsOf(f)) != 0 || len(s.pendingQuestions()) != 0 {
				t.Fatalf("refusal started or asked: starts %v cards %v", startsOf(f), s.pendingQuestions())
			}
		})
	}
}

func TestAgentResumeSpeedRefused11141(t *testing.T) {
	for _, method := range []string{"agents.start_params", "agents.resume_params"} {
		t.Run(method, func(t *testing.T) {
			f := newFakeHub()
			f.launcherList = []proto.Launcher{{Name: "codex", Agent: "codex"}}
			s := newTestService(t, f)
			setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.codex"})
			var err error
			switch method {
			case "agents.start_params":
				_, err = s.AgentStartParams(context.Background(), AgentStartParams{MachineID: "box",
					Params: map[string]any{"resume_session_id": "old-session", "speed": "fast"}})
			case "agents.resume_params":
				_, err = s.AgentResumeParams(context.Background(), AgentResumeParams{MachineID: "box", SessionID: "old-session",
					Params: map[string]any{"text": "continue", "speed": "fast"}})
			}
			if err == nil {
				t.Fatal("a resume accepted faster speed")
			}
			if len(startsOf(f)) != 0 || len(s.pendingQuestions()) != 0 {
				t.Fatalf("resume refusal started or asked: starts %v cards %v", startsOf(f), s.pendingQuestions())
			}
		})
	}
}
