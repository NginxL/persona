package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// skip_project_memory (#13538) is a per-start opt-out of the automatic project and direction handoff. The
// assignment, its criteria and the framework rules are untouched; omitted and false keep today's first prompt.
// All names and notes here are placeholders.

const (
	skipMemorySummary   = "Skip-switch project summary"
	skipMemoryOutcome   = "Skip-switch recent outcome"
	skipMemoryDecision  = "Skip-switch effective decision"
	skipMemoryPitfall   = "Skip-switch pitfall"
	skipMemoryDirection = "Skip-switch direction note"
	skipMemoryLesson    = "Skip-switch learned lesson"
	skipMemoryExtension = "Skip-switch extension note"
)

// skipMemoryEnv is notesEnv with every kind of automatic project/direction memory present, so a test cannot pass
// merely because its fixture had nothing to omit.
func skipMemoryEnv(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f := notesEnv(t)
	ctx := context.Background()
	if _, _, err := s.setDirectionNotes(ctx, "conv_lead", skipMemoryDirection, nil); err != nil {
		t.Fatal(err)
	}
	setSettings(t, s, func(v *Settings) { v.TaskLessons = new(true) })
	e := kbEng(t, s)
	if err := e.Store.PutSummary(kb.KindProject, "天气-app", skipMemorySummary); err != nil {
		t.Fatal(err)
	}
	for _, record := range []kb.ProjectMemoryRecord{
		{ID: "skip-outcome", Type: kb.ProjectMemoryOutcome, Conclusion: skipMemoryOutcome, RecordedAt: "2026-11-20T02:00:00Z"},
		{ID: "skip-decision", Type: kb.ProjectMemoryDecision, Question: "Which layout?", Decision: skipMemoryDecision,
			Reason: "The fixed width did not fit.", RecordedAt: "2026-11-20T03:00:00Z"},
		{ID: "skip-pitfall", Type: kb.ProjectMemoryPitfall, Conclusion: skipMemoryPitfall, RecordedAt: "2026-11-20T04:00:00Z"},
	} {
		record.Verification = kb.ProjectMemoryAgentReported
		if err := e.AppendProjectMemory("天气-app", record); err != nil {
			t.Fatal(err)
		}
	}
	appendTestLesson(t, e, "天气-app", "skip-lesson", kb.LessonImprovement, skipMemoryLesson, time.Now())
	f.answerApp(AppPlugins, PluginsProjectContext, func(json.RawMessage) (any, error) {
		return assistantwire.ExtensionContextResult{Blocks: []assistantwire.ExtensionContextBlock{
			{Extension: "notes", Name: "Notes", Project: "天气-app", Text: skipMemoryExtension},
		}}, nil
	})
	return s, f
}

func skipMemoryMarkers() []string {
	return []string{skipMemorySummary, skipMemoryOutcome, skipMemoryDecision, skipMemoryPitfall,
		skipMemoryDirection, skipMemoryLesson, skipMemoryExtension}
}

// agent_start keeps its project/direction handoff by default and when the option is explicitly false. True omits
// every kind of automatic memory while leaving the supplied prompt and the ordinary start rules in place.
func TestAgentStartSkipProjectMemory(t *testing.T) {
	for _, mode := range []string{"omitted", "false", "true"} {
		t.Run(mode, func(t *testing.T) {
			s, f := skipMemoryEnv(t)
			f.tabReply = "Dark mode"
			args := map[string]any{"machine": "box", "cwd": "/code/weather", "prompt": "Add dark mode"}
			if mode != "omitted" {
				args["skip_project_memory"] = mode == "true"
			}
			raw, _ := json.Marshal(args)
			if _, err := s.runTool(context.Background(), "server", "conv9", "agent_start", raw); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.starts) != 1 {
				t.Fatalf("starts = %+v", f.starts)
			}
			prompt, _ := f.starts[0]["prompt"].(string)
			for _, marker := range skipMemoryMarkers() {
				if got := strings.Contains(prompt, marker); got != (mode != "true") {
					t.Errorf("memory marker %q present = %v, want %v:\n%s", marker, got, mode != "true", prompt)
				}
			}
			assignment := "Add dark mode\n\nPersona task handoffs:"
			if !strings.Contains(prompt, assignment) || (mode == "true" && !strings.HasPrefix(prompt, assignment)) {
				t.Errorf("the assignment or its start rules changed:\n%s", prompt)
			}
		})
	}
}

// task_plan has the same per-task switch. It survives the saved plan's JSON round trip (what a queued start, a retry
// and a reload read), and startTaskAgent honours it. The criteria and read-only constraint still reach the Agent.
// Decode the tool-shaped JSON rather than naming the new Go field: the unmodified implementation compiles this test
// and fails behaviourally, rather than failing to build.
func TestTaskPlanSkipProjectMemory(t *testing.T) {
	for _, mode := range []string{"omitted", "false", "true"} {
		t.Run(mode, func(t *testing.T) {
			s, f := skipMemoryEnv(t)
			task := map[string]any{
				"title": "Inspect the layout", "prompt": "Read the current implementation only.", "machine": "box",
				"cwd": "/code/weather", "readonly": true, "why": "the repository is on box", "supervisor": "conv_lead",
				"done_when": []string{"Report the layout findings"}, "expect_minutes": 30,
			}
			if mode != "omitted" {
				task["skip_project_memory"] = mode == "true"
			}
			raw, _ := json.Marshal(task)
			var proposed PlanTask
			if err := json.Unmarshal(raw, &proposed); err != nil {
				t.Fatal(err)
			}
			saved, err := json.Marshal(proposed)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(saved), `"skip_project_memory":true`); got != (mode == "true") {
				t.Errorf("saved plan skip_project_memory=true present = %v, want %v: %s", got, mode == "true", saved)
			}
			var reloaded PlanTask
			if err := json.Unmarshal(saved, &reloaded); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.startTaskAgent(context.Background(), &reloaded, false); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			if len(f.starts) != 1 {
				f.mu.Unlock()
				t.Fatalf("starts = %+v", f.starts)
			}
			prompt, _ := f.starts[0]["prompt"].(string)
			f.mu.Unlock()
			for _, marker := range skipMemoryMarkers() {
				if got := strings.Contains(prompt, marker); got != (mode != "true") {
					t.Errorf("memory marker %q present = %v, want %v:\n%s", marker, got, mode != "true", prompt)
				}
			}
			if !strings.HasPrefix(prompt, "Read the current implementation only.") ||
				!strings.Contains(prompt, "Report the layout findings") {
				t.Errorf("the assignment or its criteria changed:\n%s", prompt)
			}
		})
	}
}
