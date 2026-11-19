package transcript

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The Workflow tool's launch, as Claude Code 2.1.308 writes it (#2935), and a Monitor's.
func TestWorkflowAndMonitorBackground(t *testing.T) {
	meta := &Meta{}
	launch := "Workflow launched in background. Task ID: wdp5zxh37\nSummary: Audit parity per domain\n" +
		"Transcript dir: /home/u/.claude/projects/p/s1/subagents/workflows/wf_c1d9daf8-cd2\n" +
		"Script file: /home/u/.claude/projects/p/s1/workflows/scripts/parity-audit-wf_1fb00b8c-9e9.js\n" +
		"(Edit this file …)\nRun ID: wf_c1d9daf8-cd2\n\nYou will be notified when it completes."
	result, _ := json.Marshal(launch)
	lines := []string{
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-05T13:46:56Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Workflow","input":{"scriptPath":"/home/u/.claude/projects/p/s1/workflows/scripts/parity-audit-wf_1fb00b8c-9e9.js"}}]}}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-11-05T13:46:57Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":` + string(result) + `}]}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-05T13:47:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu2","name":"Monitor","input":{"command":"tail -f x.log","description":"watch the deploy"}}]}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-11-05T13:47:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu2","content":"Monitor started (task bqke1prjm, expires in 30m unless the source ends first; you get one notice at expiry). Keep working."}]}}`,
		`{"type":"assistant","uuid":"a3","timestamp":"2026-11-05T13:47:02Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu3","name":"Workflow","input":{"script":"export const meta = {\n  name: 'review-changes',\n  phases: [{ title: 'Review' }],\n}\n"}}]}}`,
		`{"type":"user","uuid":"u3","timestamp":"2026-11-05T13:47:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu3","content":"Workflow launched in background. Task ID: wq1\nSummary: Review\nRun ID: wf_aa11"}]}}`,
	}
	for i, l := range lines {
		ParseLine(i+1, []byte(l), meta)
	}
	if len(meta.Background) != 3 {
		t.Fatalf("want 3 running, got %+v", meta.Background)
	}
	wf := meta.Background[0]
	if wf.ID != "wdp5zxh37" || wf.Kind != "workflow" || wf.RunID != "wf_c1d9daf8-cd2" || wf.Description != "parity-audit" {
		t.Fatalf("workflow task: %+v", wf)
	}
	mon := meta.Background[1]
	want := time.Date(2026, 11, 5, 14, 17, 1, 0, time.UTC)
	if mon.ID != "bqke1prjm" || mon.Kind != "monitor" || mon.ExpiresAt == nil || !mon.ExpiresAt.Equal(want) {
		t.Fatalf("monitor: %+v", mon)
	}
	if len(meta.Workflows) != 2 {
		t.Fatalf("workflows: %+v", meta.Workflows)
	}
	w := meta.Workflows[0]
	if w.TaskID != "wdp5zxh37" || w.RunID != "wf_c1d9daf8-cd2" || w.Name != "parity-audit" || w.Summary != "Audit parity per domain" ||
		!w.Running || w.Status != proto.WorkflowRunning || w.ToolUseID != "tu1" {
		t.Fatalf("run: %+v", w)
	}
	if meta.WorkflowScripts["wf_c1d9daf8-cd2"] != "/home/u/.claude/projects/p/s1/workflows/scripts/parity-audit-wf_1fb00b8c-9e9.js" {
		t.Fatalf("scripts: %v", meta.WorkflowScripts)
	}
	if meta.Workflows[1].Name != "review-changes" { // from the inline script's meta
		t.Fatalf("inline name: %+v", meta.Workflows[1])
	}

	// It reports back: out of Background, ended with the notification's status.
	note := "<task-notification>\n<task-id>wdp5zxh37</task-id>\n<status>completed</status>\n<summary>Workflow done</summary>\n</task-notification>"
	q, _ := json.Marshal(map[string]any{"type": "queue-operation", "operation": "enqueue", "content": note, "timestamp": "2026-11-05T14:30:00Z"})
	ParseLine(10, q, meta)
	if len(meta.Background) != 2 || meta.Background[0].ID != "bqke1prjm" {
		t.Fatalf("background after the notification: %+v", meta.Background)
	}
	if w := meta.Workflows[0]; w.Running || w.Status != proto.WorkflowCompleted || w.EndedAt == nil {
		t.Fatalf("ended run: %+v", w)
	}
	// A TaskStop kills the other one.
	ParseLine(11, []byte(`{"type":"assistant","uuid":"a4","timestamp":"2026-11-05T14:31:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu4","name":"TaskStop","input":{"task_id":"wq1"}}]}}`), meta)
	ParseLine(12, []byte(`{"type":"user","uuid":"u4","timestamp":"2026-11-05T14:31:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu4","content":"Stopped task wq1"}]}}`), meta)
	if w := meta.Workflows[1]; w.Running || w.Status != proto.WorkflowKilled {
		t.Fatalf("stopped run: %+v", w)
	}
}

func TestScriptName(t *testing.T) {
	for in, want := range map[string]string{"/a/b/parity-audit-wf_1fb00b8c-9e9.js": "parity-audit", "/a/review.js": "review", "": ""} {
		if got := scriptName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// A launch result whose call was not read (the node's background scan skips the call's line when an inline script
// makes it longer than 64 KiB) still starts the run: the result names the task, the run and the script (#6003).
func TestWorkflowLaunchWithoutItsCall(t *testing.T) {
	result := func(id, text string, isError bool) []byte {
		b, _ := json.Marshal(map[string]any{"type": "user", "uuid": "u-" + id, "timestamp": "2026-11-05T13:46:57Z", "message": map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": text, "is_error": isError}}}})
		return b
	}
	launch := "Workflow launched in background. Task ID: wdp5zxh37\nSummary: Audit parity per domain\n" +
		"Transcript dir: /home/u/.claude/projects/p/s1/subagents/workflows/wf_c1d9daf8-cd2\n" +
		"Script file: /home/u/.claude/projects/p/s1/workflows/scripts/parity-audit-wf_c1d9daf8-cd2.js\n" +
		"(Edit this file …)\nRun ID: wf_c1d9daf8-cd2\n\nYou will be notified when it completes."
	meta := &Meta{}
	ParseLine(1, result("tu1", launch, false), meta)
	if len(meta.Workflows) != 1 || len(meta.Background) != 1 {
		t.Fatalf("workflows %+v background %+v", meta.Workflows, meta.Background)
	}
	w, bg := meta.Workflows[0], meta.Background[0]
	if w.TaskID != "wdp5zxh37" || w.RunID != "wf_c1d9daf8-cd2" || w.Name != "parity-audit" || w.Summary != "Audit parity per domain" ||
		!w.Running || w.ToolUseID != "tu1" || !w.StartedAt.Equal(time.Date(2026, 11, 5, 13, 46, 57, 0, time.UTC)) {
		t.Fatalf("run: %+v", w)
	}
	if bg.ID != "wdp5zxh37" || bg.Kind != "workflow" || bg.RunID != "wf_c1d9daf8-cd2" || bg.Description != "parity-audit" {
		t.Fatalf("task: %+v", bg)
	}
	if meta.WorkflowScripts["wf_c1d9daf8-cd2"] == "" {
		t.Fatalf("scripts: %v", meta.WorkflowScripts)
	}

	// Not a launch: an error, the words quoted inside other output, or no run named.
	for name, line := range map[string][]byte{
		"error":     result("tu2", launch, true),
		"quoted":    result("tu3", "notes.txt:1:"+launch, false),
		"no run id": result("tu4", "Workflow launched in background. Task ID: wq1\nSummary: s", false),
	} {
		m := &Meta{}
		ParseLine(1, line, m)
		if len(m.Workflows) != 0 || len(m.Background) != 0 {
			t.Errorf("%s: workflows %+v background %+v", name, m.Workflows, m.Background)
		}
	}
}
