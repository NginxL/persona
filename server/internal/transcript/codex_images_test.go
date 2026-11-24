package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A Codex MCP call's pictures (a connector's, computer use's) become image items on the call's card, as a Claude tool
// result's do (#8484); a picture without data or type, or over what the apps keep, is left out.
func TestCodexMcpToolCallImages(t *testing.T) {
	big := strings.Repeat("A", maxImageLen+4)
	line := `{"timestamp":"2026-10-26T05:06:05.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"McpToolCall","id":"c2","server":"persona","tool":"example_frames","arguments":{"note":"n1"},"status":"completed","result":{"content":[` +
		`{"type":"text","text":"a video of 0:42"},{"type":"text","text":"[image 1 of 2] 00:01"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"},` +
		`{"type":"text","text":"[image 2 of 2] 00:30"},{"type":"image","data":"/9j/4AAQ","mimeType":"image/jpeg"},` +
		`{"type":"image","data":"","mimeType":"image/png"},{"type":"image","data":"AAAA","mimeType":"text/plain"},{"type":"image","data":"` + big + `","mimeType":"image/png"}],"isError":false}}}}`
	items := ParseLine(7, []byte(line), &Meta{Codex: true})
	var got []string
	for _, it := range items {
		got = append(got, it.Role+":"+it.Kind)
	}
	if want := "assistant:tool_use user:tool_result user:image user:image"; strings.Join(got, " ") != want {
		t.Fatalf("items %v, want %s", got, want)
	}
	res, a, b := items[1], items[2], items[3]
	if res.Text != "a video of 0:42\n[image 1 of 2] 00:01\n[image 2 of 2] 00:30" {
		t.Fatalf("result text %q", res.Text)
	}
	for _, im := range []proto.Item{a, b} {
		if im.ToolUseID != "c2" || im.Image == nil || im.ID == res.ID {
			t.Fatalf("image item %+v", im)
		}
	}
	if a.Image.Mime != "image/png" || a.Image.Data != "iVBORw0KGgo=" || b.Image.Mime != "image/jpeg" || a.ID == b.ID {
		t.Fatalf("images %+v %+v", a.Image, b.Image)
	}
}

func TestCodexMcpResultTypes(t *testing.T) {
	completed := func(item string) []byte {
		return []byte(`{"timestamp":"2026-11-01T00:00:00Z","type":"event_msg","payload":{"type":"item_completed","item":` + item + `}}`)
	}
	base := `{"type":"McpToolCall","id":"same","server":"example","tool":"lookup","status":"completed"`
	valid := base + `,"result":{"content":[{"type":"text","text":"found"}]}}`
	formats := []struct {
		name string
		wrap func(string) []byte
	}{
		{"event", completed},
		{"history mutation", func(item string) []byte {
			return []byte(`{"type":"history_mutation","payload":{"display_completions":[{"item":` + item + `}]}}`)
		}},
	}
	for _, test := range []struct {
		name   string
		result string
	}{
		{"string", `,"result":"opaque"`},
		{"array", `,"result":[]`},
		{"wrong content shape", `,"result":{"content":"bad"}`},
		{"wrong error shape", `,"result":{"isError":"false"}`},
	} {
		for _, format := range formats {
			t.Run(format.name+"/"+test.name, func(t *testing.T) {
				meta := &Meta{Codex: true}
				if items := ParseLine(1, format.wrap(base+test.result+`}`), meta); len(items) != 0 {
					t.Fatalf("malformed result emitted items: %+v", items)
				}
				items := ParseLine(2, format.wrap(valid), meta)
				if len(items) != 2 || items[0].Kind != proto.KindToolUse || items[1].Kind != proto.KindToolResult || items[1].Text != "found" || items[1].IsError {
					t.Fatalf("valid same-ID result was lost: %+v", items)
				}
			})
		}
	}
	for _, test := range []struct {
		name   string
		result string
	}{
		{"absent", ""},
		{"null", `,"result":null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			items := ParseLine(1, completed(base+test.result+`}`), &Meta{Codex: true})
			if len(items) != 2 || items[1].Kind != proto.KindToolResult || items[1].Text != "" || items[1].IsError {
				t.Fatalf("empty MCP result: %+v", items)
			}
		})
	}
	items := ParseLine(1, completed(base+`,"result":{"content":[{"type":"text","text":"failure"}],"isError":true}}`), &Meta{Codex: true})
	if len(items) != 2 || items[1].Text != "failure" || !items[1].IsError || items[1].Outcome == nil || items[1].Outcome.Status != proto.OutcomeFailed {
		t.Fatalf("typed MCP error: %+v", items)
	}
	for _, test := range []struct {
		name string
		item string
	}{
		{"other extension", `{"type":"Extension","kind":"other","id":"image","result":"opaque"}`},
		{"malformed other field", `{"type":"Extension","kind":"image_gen.generation","id":"image","content":"bad","result":"opaque"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if items := ParseLine(1, completed(test.item), &Meta{Codex: true}); len(items) != 0 {
				t.Fatalf("invalid extension emitted items: %+v", items)
			}
		})
	}
}

func TestCodexHistoryMutationGeneratedAndMcp(t *testing.T) {
	meta := &Meta{Codex: true}
	generated := `{"type":"Extension","kind":"image_gen.generation","id":"generated","status":"completed","savedPath":"/home/example/picture.png","result":"<base64>"}`
	native := []byte(`{"type":"event_msg","payload":{"type":"item_completed","item":` + generated + `}}`)
	if items := ParseLine(1, native, meta); len(items) != 2 || items[0].ToolName != "Read" {
		t.Fatalf("native generated image: %+v", items)
	}
	other := `{"type":"Extension","kind":"image_gen.generation","id":"other","status":"completed","savedPath":"/home/example/other.png","result":"<other bytes>"}`
	mcp := `{"type":"McpToolCall","id":"mcp","server":"example","tool":"lookup","result":{"content":[{"type":"text","text":"found"}]}}`
	mutation := []byte(`{"type":"history_mutation","payload":{"display_completions":[{"item":` + generated + `},{"item":` + other + `},{"item":` + mcp + `}]}}`)
	items := ParseLine(2, mutation, meta)
	if len(items) != 4 || items[0].ToolName != "Read" || string(items[0].ToolInput) != `{"file_path":"/home/example/other.png"}` ||
		items[1].Kind != proto.KindToolResult || items[2].ToolName != "mcp__example__lookup" || items[3].Text != "found" {
		t.Fatalf("history mutation items: %+v", items)
	}
	for _, item := range items {
		if item.Kind == proto.KindImage || strings.Contains(item.Text, "<other bytes>") {
			t.Fatalf("image bytes entered history projection: %+v", item)
		}
	}
	encoded, err := json.Marshal(items)
	if err != nil || strings.Contains(string(encoded), "<other bytes>") {
		t.Fatalf("image result entered history items: %s %v", encoded, err)
	}
	if duplicate := ParseLine(3, []byte(`{"type":"event_msg","payload":{"type":"item_completed","item":`+mcp+`}}`), meta); len(duplicate) != 0 {
		t.Fatalf("native duplicate of history item: %+v", duplicate)
	}
}

func TestCodexGeneratedImageRollout(t *testing.T) {
	const generated = `{"type":"Extension","kind":"image_gen.generation","id":"call_a","status":"completed","savedPath":"/home/example/.codex/generated_images/example/call_a.png","result":"<base64>"}`
	lines := []string{
		`{"timestamp":"2026-11-01T00:00:00Z","type":"session_meta","payload":{"id":"synthetic","cwd":"/home/example"}}`,
		`{"timestamp":"2026-11-01T00:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"Make a picture"}]}}}`,
		`{"timestamp":"2026-11-01T00:00:02Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"Reasoning","id":"r1","summary_text":["Making a picture"]}}}`,
		`{"timestamp":"2026-11-01T00:00:03Z","type":"event_msg","payload":{"type":"item_completed","item":` + generated + `}}`,
		`{"timestamp":"2026-11-01T00:00:04Z","type":"event_msg","payload":{"type":"item_completed","item":` + generated + `}}`,
		`{"timestamp":"2026-11-01T00:00:05Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"Extension","kind":"image_gen.generation","id":"failed","status":"failed","savedPath":"/home/example/failed.png"}}}`,
		`{"timestamp":"2026-11-01T00:00:06Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"Extension","kind":"image_gen.generation","id":"missing","status":"completed"}}}`,
		`{"timestamp":"2026-11-01T00:00:07Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"reply","phase":"final_answer","content":[{"type":"text","text":"Here is your picture."}]}}}`,
	}
	path := filepath.Join(t.TempDir(), "rollout-synthetic.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := NewFile(path)
	items, span, err := file.RefreshSpan()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(span.History, ".items1") {
		t.Fatalf("Codex history revision = %q", span.History)
	}
	if len(items) != 7 || items[2].Kind != proto.KindToolUse || items[2].ToolName != "Read" ||
		string(items[2].ToolInput) != `{"file_path":"/home/example/.codex/generated_images/example/call_a.png"}` ||
		items[3].Kind != proto.KindToolResult || items[3].ToolUseID != items[2].ToolUseID || items[3].IsError {
		t.Fatalf("generated image items = %+v", items)
	}
	for _, item := range items {
		if item.Kind == proto.KindImage || strings.Contains(item.Text, "<base64>") {
			t.Fatalf("image bytes entered the projection: %+v", item)
		}
	}
	if items[4].Kind != proto.KindNotice || items[5].Kind != proto.KindNotice ||
		!strings.Contains(items[4].Text, "image_gen.generation") || !strings.Contains(items[5].Text, "image_gen.generation") ||
		items[6].Kind != proto.KindText || items[6].Text != "Here is your picture." {
		t.Fatalf("fallback notices or final reply = %+v", items)
	}
	folded := FoldWork(items)
	if len(folded) != 5 || folded[1].Kind != proto.KindFold || folded[1].Fold == nil || len(folded[1].Fold.Images) != 1 ||
		folded[1].Fold.Images[0].Path != "/home/example/.codex/generated_images/example/call_a.png" ||
		folded[1].Fold.Images[0].ToolUseID != "call_a" || folded[4].Text != "Here is your picture." {
		t.Fatalf("folded image and reply = %+v", folded)
	}
}
