package prompts

import (
	"strings"
	"testing"
)

func TestA2AMessageLanguage(t *testing.T) {
	for language, firstLine := range map[string]string{
		"en":      "Persona A2A task task-id from machine/session is submitted.",
		"zh-Hans": "Persona A2A 任务 task-id 来自 machine/session，状态为 submitted。",
		"ja":      "machine/session からの Persona A2A タスク task-id。現在の状態は submitted です。",
	} {
		req := A2AMessage{ID: "task-id", Sender: "machine/session", State: "submitted", Text: "Original input.", Language: language, Request: true}
		got := RenderA2AMessage(req)
		if !strings.HasPrefix(got, firstLine+"\n") || !strings.Contains(got, "a2a_report") || !strings.HasSuffix(got, "\n\n"+req.Text) {
			t.Fatalf("request in %s: %q", language, got)
		}
		req.Request = false
		got = RenderA2AMessage(req)
		if !strings.Contains(got, "a2a_send") || strings.Contains(got, "a2a_report") {
			t.Fatalf("reply in %s: %q", language, got)
		}
	}
}
