package feishubot

import (
	"encoding/json"
	"testing"
)

// AC3: the native AG-UI payload gives Feishu an icon and useful row subject, then output it can expand.
func TestCotToolEventsExposeIconSubjectAndOutput(t *testing.T) {
	events := CotToolCall("tu1", "p1", "Bash", "运行 · ls", "bash")
	if len(events) != 2 || events[0].Type != "TOOL_CALL_START" || events[1].Type != "TOOL_CALL_END" {
		t.Fatalf("tool-call events = %v", events)
	}
	var start map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &start); err != nil {
		t.Fatal(err)
	}
	if start["toolCallId"] != "tu1" || start["parentMessageId"] != "p1" || start["toolCallName"] != "Bash" || start["title"] != "运行 · ls" || start["icon"] != "bash" {
		t.Fatalf("TOOL_CALL_START content = %v", start)
	}

	result := CotToolResult("tu1", "a.txt\n", true)
	if result.Type != "TOOL_CALL_RESULT" {
		t.Fatalf("result event = %v", result)
	}
	var body struct {
		ToolCallID string `json:"toolCallId"`
		Content    string `json:"content"`
	}
	if err := json.Unmarshal([]byte(result.Content), &body); err != nil {
		t.Fatal(err)
	}
	var output struct {
		Type string `json:"type"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(body.Content), &output); err != nil {
		t.Fatal(err)
	}
	if body.ToolCallID != "tu1" || output.Type != "code" || output.Code != "a.txt\n" {
		t.Fatalf("TOOL_CALL_RESULT = %+v, output = %+v", body, output)
	}
}
