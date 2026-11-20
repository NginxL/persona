package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

const (
	agyTestID      = "11111111-2222-4333-8444-555555555555"
	agyOtherTestID = "22222222-3333-4444-8555-666666666666"
	agyTestPane    = "w1:p1"
	agyTestRow     = "agy-w1-p1"
)

func agyTestAgent(pane, cwd, id string) map[string]any {
	a := agentOf(agyAgent, "", proto.StatusIdle, pane, "w1:t1", "w1", cwd, "agy")
	if id != "" {
		a["agent_session"] = map[string]any{"kind": "id", "value": id, "source": "herdr:antigravity_cli", "agent": agyAgent}
	}
	return a
}

func agyTestFile(t *testing.T, root, id, contents string) string {
	t.Helper()
	file := filepath.Join(root, "brain", id, agyTranscriptPath)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	agyTestWrite(t, file, contents)
	return file
}

func agyTestWrite(t *testing.T, file, contents string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func agyTestAppend(t *testing.T, file, contents string) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(contents); err != nil {
		t.Fatal(err)
	}
}

func agyTestCall(t *testing.T, n *Node, method, id string, params map[string]any) any {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["session_id"] = id
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.handle(i18n.With(context.Background(), i18n.En), method, raw)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

func agyTestItems(t *testing.T, n *Node, id string, params map[string]any) ([]proto.Item, map[string]any) {
	t.Helper()
	page := agyTestCall(t, n, "session.items", id, params).(map[string]any)
	return page["items"].([]proto.Item), page
}

func TestAgyPaneUserImagesAndCheckpoints(t *testing.T) {
	node, herdr, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, node, herdr, agyTestAgent(agyTestPane, home, agyTestID))
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jB9sAAAAASUVORK5CYII="
	agyTestFile(t, node.agyDataRoot(), agyTestID, fmt.Sprintf(`{"step_index":0,"type":"USER_INPUT","content":"Look\n<attached_images>\nuser.png\n</attached_images>","media":[{"mime_type":"image/png","uri":"./user.png"},{"mime_type":"image/png","data":%q}]}
{"step_index":1,"type":"PLANNER_RESPONSE","content":"Done"}
{"step_index":2,"type":"USER_INPUT","client_msg_id":"photo-only","media":[{"mime_type":"image/png","data":%q},{"mime_type":"image/png","data":%q}]}
{"step_index":3,"type":"CHECKPOINT"}
`, png, png, png))
	index := agyTestCall(t, node, "session.items", agyTestRow, map[string]any{"user_inputs": true}).(proto.UserInputIndex)
	if index.Total != 2 || index.Inputs[0].Seq != 1 || index.Inputs[1].Seq != 3 {
		t.Fatalf("user inputs: %+v", index)
	}
	recent, page := agyTestItems(t, node, agyTestRow, map[string]any{"turns": 1})
	if page["has_more"] != true || len(recent) != 3 || index.Inputs[1].ID != recent[0].ID {
		t.Fatalf("latest turn: %+v", page)
	}
	for _, image := range recent[:2] {
		if image.Role != "user" || image.Kind != proto.KindImage || image.ToolUseID != "" || image.ClientMsgID != "photo-only" ||
			image.Image == nil || !image.Image.Lazy || image.Image.Data != "" {
			t.Fatalf("user image: %+v", image)
		}
		fetched := agyTestCall(t, node, "session.image", agyTestRow, map[string]any{"item_id": image.ID}).(*proto.Image)
		if fetched.Mime != "image/png" || fetched.Data != png {
			t.Fatalf("fetched image: %+v", fetched)
		}
	}
	if notice := recent[2]; notice.Role != "system" || notice.Kind != proto.KindNotice || notice.Text != transcript.CompactedNotice {
		t.Fatalf("checkpoint: %+v", notice)
	}
	older, page := agyTestItems(t, node, agyTestRow, map[string]any{"turns": 1, "before_seq": 3})
	if page["has_more"] != false || len(older) != 3 || older[0].Kind != proto.KindText || older[1].Kind != proto.KindImage ||
		older[0].Seq != older[1].Seq || !strings.HasSuffix(older[1].ID, "#media1") {
		t.Fatalf("earlier turn did not suppress the duplicate path: %+v", page)
	}
}

func TestAgyPaneSessionImage(t *testing.T) {
	node, herdr, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	const otherRow = "agy-w1-p2"
	seedAgents(t, node, herdr, agyTestAgent(agyTestPane, home, agyTestID), agyTestAgent("w1:p2", home, agyOtherTestID))
	root := node.agyDataRoot()
	brain := filepath.Join(root, "brain", agyTestID)
	if err := os.MkdirAll(brain, 0o700); err != nil {
		t.Fatal(err)
	}
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jB9sAAAAASUVORK5CYII="
	data, err := base64.StdEncoding.DecodeString(png)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(brain, "screen shot.png")
	agyTestWrite(t, file, string(data))
	uriPath := filepath.ToSlash(file)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := (&url.URL{Scheme: "file", Path: uriPath}).String()
	oversize := filepath.Join(brain, "large.png")
	agyTestWrite(t, oversize, string(data)+strings.Repeat("0", agyMaxImageBytes))
	unknown := filepath.Join(brain, "unknown.png")
	agyTestWrite(t, unknown, "\x00\x01\x02\x03")
	svg := filepath.Join(brain, "markup.png")
	agyTestWrite(t, svg, `<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`)
	link := filepath.Join(brain, "link.png")
	linkErr := os.Symlink(file, link)
	outside := t.TempDir()
	agyTestWrite(t, filepath.Join(outside, "outside.png"), string(data))
	parentLinkErr := os.Symlink(outside, filepath.Join(brain, "external"))
	fifo := filepath.Join(brain, "pipe.png")
	fifoErr := exec.Command("mkfifo", fifo).Run()
	internal := filepath.Join(brain, "subdir")
	if err := os.Mkdir(internal, 0o700); err != nil {
		t.Fatal(err)
	}
	entries := []struct {
		name           string
		media          agyMedia
		valid, omitted bool
		setupErr       error
	}{
		{name: "file URI", media: agyMedia{Mime: "image/png", URI: uri}, valid: true},
		{name: "plain path", media: agyMedia{Mime: "image/png", URI: file}, valid: true},
		{name: "relative path", media: agyMedia{Mime: "image/png", URI: filepath.Base(file)}, valid: true},
		{name: "inline", media: agyMedia{Mime: "image/png", Data: png}, valid: true},
		{name: "missing", media: agyMedia{Mime: "image/png", URI: "missing.png"}},
		{name: "over size", media: agyMedia{Mime: "image/png", URI: oversize}},
		{name: "over size inline", media: agyMedia{Mime: "image/png", Data: strings.Repeat("A", base64.StdEncoding.EncodedLen(agyMaxImageBytes)+4)}},
		{name: "invalid inline", media: agyMedia{Mime: "image/png", Data: "!invalid"}},
		{name: "bad WebP signature", media: agyMedia{Mime: "image/webp", Data: base64.StdEncoding.EncodeToString([]byte("RIFF\x00\x00\x00\x00WEBPVP"))}},
		{name: "unsupported network URI", media: agyMedia{Mime: "image/png", URI: "https://example.com/screenshot.png"}},
		{name: "remote file URI", media: agyMedia{Mime: "image/png", URI: "file://example.com" + uriPath}},
		{name: "parent traversal", media: agyMedia{Mime: "image/png", URI: "../somewhere.png"}},
		{name: "symlink", media: agyMedia{Mime: "image/png", URI: link}, setupErr: linkErr},
		{name: "parent symlink escape", media: agyMedia{Mime: "image/png", URI: "external/outside.png"}, setupErr: parentLinkErr},
		{name: "FIFO", media: agyMedia{Mime: "image/png", URI: fifo}, setupErr: fifoErr},
		{name: "directory", media: agyMedia{Mime: "image/png", URI: internal}},
		{name: "MIME mismatch", media: agyMedia{Mime: "image/jpeg", URI: file}},
		{name: "SVG disguised as PNG", media: agyMedia{Mime: "image/png", URI: svg}},
		{name: "unknown bytes", media: agyMedia{Mime: "image/png", URI: unknown}},
		{name: "SVG", media: agyMedia{Mime: "image/svg+xml", URI: svg}, omitted: true},
		{name: "no source", media: agyMedia{Mime: "image/png"}, omitted: true},
	}
	var media []agyMedia
	for _, entry := range entries {
		media = append(media, entry.media)
	}
	encoded, err := json.Marshal(media)
	if err != nil {
		t.Fatal(err)
	}
	const fixture = `{"step_index":0,"type":"USER_INPUT","content":"<attached_images>\nuser.png\n</attached_images>","media":[{"mime_type":"image/png","uri":"user.png"}]}
{"step_index":1,"type":"PLANNER_RESPONSE","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"/example/view.png"}},{"name":"run_command","args":{"CommandLine":"capture"}}]}
{"step_index":2,"type":"GENERIC","content":"Looked at a picture"}
{"step_index":3,"type":"GENERIC","content":"Captured screenshots","media":%s}
{"step_index":4,"type":"PLANNER_RESPONSE","content":"Done"}
`
	agyTestFile(t, root, agyTestID, fmt.Sprintf(fixture, encoded))
	agyTestFile(t, root, agyOtherTestID, "{\"type\":\"USER_INPUT\",\"content\":\"Other session\"}\n")
	items, page := agyTestItems(t, node, agyTestRow, map[string]any{"turns": 1})
	if page["has_more"] != false || items[0].Text != "<attached_images>\nuser.png\n</attached_images>" || page["to_seq"] != 5 {
		t.Fatalf("tool images changed the turn or attachment text: %+v", page)
	}
	result := items[4]
	if result.Kind != proto.KindToolResult || result.Seq != 4 {
		t.Fatalf("capture result: %+v", result)
	}
	baseID := strings.TrimSuffix(result.ID, "#0")
	imageItems := make(map[string]proto.Item)
	for _, item := range items {
		if item.Kind == proto.KindImage {
			imageItems[item.ID] = item
		}
	}
	if len(imageItems) != len(entries)-2 {
		t.Fatalf("got %d eligible image items", len(imageItems))
	}
	_, folded := agyTestItems(t, node, agyTestRow, map[string]any{"turns": 1, "fold_work": true})
	foldedItems := folded["items"].([]proto.Item)
	if len(foldedItems) != 3 || foldedItems[1].Fold == nil || !slices.ContainsFunc(foldedItems[1].Fold.Images, func(image proto.FoldImage) bool {
		return image.Path == "/example/view.png" && image.Tool == "Read"
	}) || !slices.ContainsFunc(foldedItems[1].Fold.Images, func(image proto.FoldImage) bool {
		return image.ItemID == baseID+"#media0" && image.ToolUseID == result.ToolUseID
	}) {
		t.Fatalf("fold lost Read or lazy image metadata: %+v", foldedItems)
	}
	for index, entry := range entries {
		t.Run(entry.name, func(t *testing.T) {
			if entry.setupErr != nil {
				t.Skipf("could not set up test file: %v", entry.setupErr)
			}
			id := fmt.Sprintf("%s#media%d", baseID, index)
			item, found := imageItems[id]
			if found == entry.omitted || found && (item.Role != "assistant" || item.Seq != result.Seq || item.ToolUseID != result.ToolUseID ||
				item.Image == nil || *item.Image != (proto.Image{Mime: entry.media.Mime, Lazy: true})) {
				t.Fatalf("unexpected lazy image item: %+v", item)
			}
			if entry.valid {
				image := agyTestCall(t, node, "session.image", agyTestRow, map[string]any{"item_id": id, "path": unknown}).(*proto.Image)
				if *image != (proto.Image{Mime: "image/png", Data: png}) {
					t.Fatalf("unexpected image: %+v", image)
				}
				full := agyTestCall(t, node, "session.item", agyTestRow, map[string]any{"item_id": id}).(proto.Item)
				if full.Image == nil || *full.Image != *item.Image {
					t.Fatalf("session.item lost the lazy placeholder: %+v", full)
				}
				return
			}
			agyAssertNoImage(t, node, agyTestRow, id)
		})
	}
	agyAssertNoImage(t, node, agyTestRow, file)
	agyAssertNoImage(t, node, agyTestRow, result.ID)
	agyAssertNoImage(t, node, otherRow, baseID+"#media0")
}

func agyAssertNoImage(t *testing.T, node *Node, session, item string) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"session_id": session, "item_id": item})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := node.handle(context.Background(), "session.image", raw); err == nil {
		t.Fatal("session.image accepted an unavailable reference")
	}
}

func TestAgyPaneSessionList(t *testing.T) {
	n, herdr, home := herdrNode(t)
	const (
		acpRow     = "agy-" + agyTestID
		otherRow   = "agy-" + agyOtherTestID
		desktopRow = "desktop-agy-" + agyTestID
		secondPane = "w1:p2"
		secondRow  = "agy-w1-p2"
		model      = "gemini-test"
	)
	n.runtimes = newRuntimeRegistry()
	runtime := n.runtimes.add(nil)
	runtime.ready, runtime.hello.Runtime = true, agyAgent
	n.runtimes.setSessions(runtime, []proto.Session{
		{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusIdle, Title: "ACP conversation", Model: model},
		{SessionID: otherRow, Agent: agyAgent, Cwd: home, Status: proto.StatusIdle, Model: "other-model"},
		{SessionID: desktopRow, Agent: agyAgent, Cwd: home, Status: proto.StatusOffline},
	})
	for _, tc := range []struct {
		name, status, conversation string
		ambiguous                  bool
		wantACP                    bool
	}{
		{name: "live", status: proto.StatusIdle, conversation: agyTestID},
		{name: "unresolved", status: proto.StatusIdle, wantACP: true},
		{name: "ambiguous", status: proto.StatusIdle, conversation: agyTestID, ambiguous: true, wantACP: true},
		{name: "offline", status: proto.StatusOffline, conversation: agyTestID, wantACP: true},
		{name: "interrupted", status: proto.StatusInterrupted, conversation: agyTestID, wantACP: true},
		{name: "closed", wantACP: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := map[string]string{otherRow: "other-model", desktopRow: ""}
			var agents []any
			if tc.status != "" {
				pane := agyTestAgent(agyTestPane, home, tc.conversation)
				pane["agent_status"] = tc.status
				agents = append(agents, pane)
				want[agyTestRow] = ""
			}
			if tc.ambiguous {
				agents = append(agents, agyTestAgent(secondPane, home, tc.conversation))
				want[secondRow] = ""
			}
			if tc.wantACP {
				want[acpRow] = model
			} else {
				want[agyTestRow] = model
			}
			herdr.reply("session.snapshot", snapshot(nil, nil, agents, nil))
			rows := listSessions(t, n)
			got := make(map[string]string, len(rows))
			for _, row := range rows {
				got[row.SessionID] = row.Model
				if row.SessionID == agyTestRow && (row.PaneID != agyTestPane || row.Host != host.Herdr || row.Status != tc.status || row.Title != "agy") {
					t.Errorf("pane metadata changed: %+v", row)
				}
			}
			if len(rows) != len(want) || !maps.Equal(got, want) {
				t.Errorf("got %d rows with models %v, want %v", len(rows), got, want)
			}
		})
	}
}

func TestAgyPanePagesAndFullItems(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	const fixture = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-01T00:00:00Z","content":"<USER_REQUEST>\n<persona-context>hidden</persona-context>\nCheck the files\n</USER_REQUEST><ADDITIONAL_METADATA>hidden</ADDITIONAL_METADATA>"}
{"step_index":1,"type":"USER_INPUT","source":"SYSTEM","content":"hidden system input"}
{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-01T00:00:01Z","thinking":"  First find the file. ","content":" I will check. ","tool_calls":[{"name":"run_command","args":{"CommandLine":"ls","toolSummary":"List files"}},{"name":"view_file","args":{"AbsolutePath":"sample.txt"}}]}
{"step_index":3,"type":"GENERIC","content":"Created At: 2026-11-01\nOutput:\nsample.txt\n","truncated_fields":["content"]}
{"step_index":4,"type":"GENERIC","status":"ERROR","error":"Read failed"}
{"step_index":5,"type":"PLANNER_RESPONSE","content":%q}
{"step_index":6,"type":"UNKNOWN"}
`
	long := strings.Repeat("result ", 5000)
	agyTestFile(t, n.agyDataRoot(), agyTestID, fmt.Sprintf(fixture, long))
	output := filepath.Join(n.agyDataRoot(), "brain", agyTestID, ".system_generated", "steps", "3", "output.txt")
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		t.Fatal(err)
	}
	agyTestWrite(t, output, "Created At: 2026-11-01\nOutput:\nsample.txt\nsecond.txt\n")
	items, page := agyTestItems(t, n, agyTestRow, map[string]any{"limit": 1})
	if len(items) != 1 || items[0].Seq != 6 || items[0].FullSize != len(long)-1 || page["to_seq"] != 7 || page["has_more"] != true {
		t.Fatalf("latest page: %+v", page)
	}
	full := agyTestCall(t, n, "session.item", agyTestRow, map[string]any{"item_id": items[0].ID}).(proto.Item)
	if full.ID != items[0].ID || full.Text != strings.TrimSpace(long) {
		t.Fatalf("full item %s has %d bytes", full.ID, len(full.Text))
	}
	items, page = agyTestItems(t, n, agyTestRow, map[string]any{"before_seq": 4, "limit": 1})
	if page["has_more"] != true || page["to_seq"] != 3 || len(items) != 4 ||
		items[0].Kind != "thinking" || items[0].Text != "First find the file." || items[1].Text != "I will check." ||
		items[2].ToolName != "Bash" || string(items[2].ToolInput) != `{"command":"ls","description":"List files"}` ||
		items[3].ToolName != "Read" || string(items[3].ToolInput) != `{"file_path":"sample.txt"}` {
		t.Fatalf("planner page: %+v", page)
	}
	results, _ := agyTestItems(t, n, agyTestRow, map[string]any{"from_seq": 4, "through_seq": 5})
	if len(results) != 2 || results[0].Kind != "tool_result" || results[0].Text != "sample.txt\n" ||
		results[0].ToolUseID != items[2].ToolUseID || results[1].ToolUseID != items[3].ToolUseID ||
		results[1].Text != "Read failed" || !results[1].IsError {
		t.Fatalf("tool results: %+v", results)
	}
	expanded := agyTestCall(t, n, "session.item", agyTestRow, map[string]any{"item_id": results[0].ID}).(proto.Item)
	if expanded.ID != results[0].ID || expanded.ToolUseID != items[2].ToolUseID || expanded.Text != "sample.txt\nsecond.txt\n" {
		t.Fatalf("expanded tool result: %+v", expanded)
	}
	items, page = agyTestItems(t, n, agyTestRow, map[string]any{"before_seq": 3})
	if len(items) != 1 || items[0].Text != "Check the files" || items[0].Role != "user" || page["has_more"] != false {
		t.Fatalf("oldest page: %+v", page)
	}
	items, page = agyTestItems(t, n, agyTestRow, map[string]any{"after_seq": 5})
	if len(items) != 1 || items[0].ID != full.ID || page["from_seq"] != 5 || page["to_seq"] != 7 {
		t.Fatalf("catch-up page: %+v", page)
	}
}

func TestAgyPaneConversationIsolation(t *testing.T) {
	n, f, home := herdrNode(t)
	root := t.TempDir()
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", root)
	const user = `{"step_index":0,"type":"USER_INPUT","content":%q}` + "\n"
	agyTestFile(t, root, agyTestID, fmt.Sprintf(user, "first conversation"))
	agyTestFile(t, root, agyOtherTestID, fmt.Sprintf(user, "second conversation"))
	first := agyTestAgent(agyTestPane, home, agyTestID)
	const pane2, row2 = "w1:p2", "agy-w1-p2"
	valid := agyTestAgent(pane2, home, agyOtherTestID)
	seedAgents(t, n, f, first, valid)
	items, _ := agyTestItems(t, n, agyTestRow, nil)
	other, _ := agyTestItems(t, n, row2, nil)
	if len(items) != 1 || len(other) != 1 || items[0].Text != "first conversation" || other[0].Text != "second conversation" || items[0].ID == other[0].ID {
		t.Fatalf("same-directory panes: first %+v, second %+v", items, other)
	}
	assertMissingItem := func(id, itemID string) {
		t.Helper()
		if _, err := request(t, n, "session.item", map[string]any{"session_id": id, "item_id": itemID}); err == nil {
			t.Fatal("expanded an item outside the verified conversation")
		}
	}
	assertMissingItem(row2, items[0].ID)
	t.Run("snapshot_mismatch", func(t *testing.T) {
		n.mu.Lock()
		row := n.agents[row2]
		stale := *row.AgentSession
		stale.Value = "33333333-4444-4555-8666-777777777777"
		row.AgentSession = &stale
		// Leave the raw snapshot untouched so the merged row disagrees with its pane's report.
		n.agents[row2] = row
		n.mu.Unlock()
		got, _ := agyTestItems(t, n, row2, nil)
		if len(got) != 1 || got[0].Kind != "notice" {
			t.Fatalf("snapshot mismatch: %+v", got)
		}
		assertMissingItem(row2, items[0].ID)
		assertMissingItem(row2, other[0].ID)
	})
	for _, tc := range []struct{ name, kind, source, agent, id string }{
		{name: "missing"},
		{"path kind", "path", "herdr:antigravity_cli", "agy", agyOtherTestID},
		{"traversal", "id", "herdr:antigravity_cli", "agy", "../" + agyTestID},
		{"invalid UUID", "id", "herdr:antigravity_cli", "agy", "11111111"},
		{"other agent", "id", "herdr:antigravity_cli", "cursor", agyOtherTestID},
		{"other source", "id", "custom:persona", "agy", agyOtherTestID},
		{"duplicate ID", "id", "herdr:antigravity_cli", "agy", agyTestID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			second := agyTestAgent(pane2, home, "")
			if tc.id != "" {
				second["agent_session"] = map[string]any{"kind": tc.kind, "agent": tc.agent, "source": tc.source, "value": tc.id}
			}
			seedAgents(t, n, f, first, second)
			got, _ := agyTestItems(t, n, row2, nil)
			if len(got) != 1 || got[0].Kind != "notice" || got[0].TS.IsZero() || !strings.Contains(got[0].Text, "herdr integration install antigravity-cli") {
				t.Fatalf("terminal notice: %+v", got)
			}
			assertMissingItem(row2, items[0].ID)
		})
	}

	// A conversation directory symlink cannot redirect even to a sibling inside the data root.
	const linked = "33333333-4444-4555-8666-777777777777"
	if err := os.Symlink(agyTestID, filepath.Join(root, "brain", linked)); err != nil {
		t.Fatal(err)
	}
	seedAgents(t, n, f, first, agyTestAgent(pane2, home, linked))
	if got, _ := agyTestItems(t, n, row2, nil); len(got) != 0 {
		t.Fatalf("redirected conversation was read: %+v", got)
	}
}

// A watch from the end of the history, then a step that maps to no item, then a visible one: the empty step moves the
// cursor once, and the visible one comes as its own batch, nothing sent twice (review on #10364).
func TestAgyPaneWatchFromTheEndPastAnInvisibleStep(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"Check the files"}`+"\n"+
		`{"step_index":1,"type":"PLANNER_RESPONSE","content":"Looking"}`+"\n")
	_, page := agyTestItems(t, n, agyTestRow, nil)
	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": page["to_seq"]})
	t.Cleanup(func() { n.unwatch(agyTestRow) })
	n.mu.Lock()
	w := n.watches[agyTestRow]
	n.mu.Unlock()

	agyTestAppend(t, file, `{"step_index":2,"type":"USER_INPUT","source":"SYSTEM","content":"hidden"}`+"\n")
	for range 3 {
		n.pollAgyPane(w)
	}
	waitFor(t, "the invisible step's batch reaches the hub", func() bool { return len(hub.spans()) >= 1 })
	if got := hub.spans(); len(got) != 1 || got[0].n != 0 || got[0].fromSeq != 2 || got[0].toSeq != 3 {
		t.Fatalf("after the invisible step: %+v", got)
	}
	agyTestAppend(t, file, `{"step_index":3,"type":"PLANNER_RESPONSE","content":"Visible"}`+"\n")
	waitFor(t, "the visible step", func() bool { return len(hub.spans()) >= 2 })
	if got := hub.spans(); len(got) != 2 || got[1].n != 1 || got[1].fromSeq != 3 || got[1].toSeq != 4 || hub.got()[0].Text != "Visible" {
		t.Fatalf("after the visible step: %+v, items %+v", got, hub.got())
	}
}

func TestAgyPaneWakeKeepsDeliveredReplyAndCatchUp(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	raw, err := os.ReadFile("../../../integrations/antigravity/test/fixtures/autonomous-wake.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, strings.Join(lines[:3], ""))
	prior, page := agyTestItems(t, n, agyTestRow, nil)
	if len(prior) != 2 || !strings.Contains(prior[1].Text, "file: /synthetic/chart.png") {
		t.Fatalf("foreground answer: %+v", prior)
	}
	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": page["to_seq"]})
	t.Cleanup(func() { n.unwatch(agyTestRow) })
	agyTestAppend(t, file, strings.Join(lines[3:5], ""))
	waitFor(t, "the private notifications advance the cursor", func() bool { return len(hub.spans()) >= 1 })
	if got := hub.spans()[0]; got.n != 0 || got.toSeq != 5 {
		t.Fatalf("notifications before anything resumed: %+v", got)
	}
	agyTestAppend(t, file, strings.Join(lines[5:], ""))
	waitFor(t, "background work reaches the watcher", func() bool { return len(hub.spans()) >= 2 })
	rows, _ := agyTestItems(t, n, agyTestRow, map[string]any{"after_seq": 5})
	watched := hub.got()
	if len(rows) != 4 || len(watched) != len(rows) || !rows[0].Wake || rows[0].Role != "system" || rows[0].Kind != proto.KindNotice ||
		rows[0].Seq != 6 || rows[0].ID != strings.Split(prior[1].ID, "#")[0]+"#-1" || rows[0].Text != transcript.BackgroundTaskUpdate ||
		rows[1].Text != "The check is complete." || rows[2].Role != "user" || rows[3].Text != "Another answer." {
		t.Fatalf("native catch-up: %+v (watched %+v)", rows, watched)
	}
	full, _ := agyTestItems(t, n, agyTestRow, nil)
	if len(full) != len(rows)+len(prior) || full[1].ID != prior[1].ID || full[1].Text != prior[1].Text {
		t.Fatalf("foreground answer across the wake: %+v", full)
	}
	for i, row := range rows {
		if watched[i].ID != row.ID || full[i+len(prior)].ID != row.ID {
			t.Fatalf("unstable row %d: page %s, live %s, cold %s", i, row.ID, watched[i].ID, full[i+len(prior)].ID)
		}
	}
}

func TestAgyPaneReplaysOnlyTheMatchingObservedWake(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	const fixtures = "../../../integrations/antigravity/test/fixtures/"
	raw, err := os.ReadFile(fixtures + "autonomous-wake.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	background := strings.Replace(lines[5], `"step_index":5`, `"step_index":3`, 1)
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, strings.Join(lines[:3], "")+background)
	prior, page := agyTestItems(t, n, agyTestRow, nil)
	if len(prior) != 3 || prior[2].Wake {
		t.Fatalf("ordinary adjacent replies: %+v", prior)
	}
	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": page["to_seq"]})
	t.Cleanup(func() { n.unwatch(agyTestRow) })

	// This is the file the registered companion produces in the markerless ACP test.
	receipt, err := os.ReadFile(fixtures + "observed-wake.txt")
	if err != nil {
		t.Fatal(err)
	}
	agyTestWrite(t, filepath.Join(n.agyDataRoot(), "brain", agyTestID, agyWakeReceipts), string(receipt))
	waitFor(t, "the same native export gains its observed wake", func() bool { return len(hub.spans()) > 0 })
	rows, matchedPage := agyTestItems(t, n, agyTestRow, nil)
	if len(rows) != 4 || rows[1].ID != prior[1].ID || rows[3].ID != prior[2].ID || !rows[2].Wake ||
		rows[2].Seq != rows[3].Seq || matchedPage["history"] == page["history"] || hub.spans()[0].fromSeq != 0 {
		t.Fatalf("observed wake: %+v, span %+v", rows, hub.spans())
	}
	agyTestAppend(t, file, lines[2]+background)
	other, _ := agyTestItems(t, n, agyTestRow, nil)
	if len(other) != 3 || other[1].ID == prior[1].ID {
		t.Fatalf("receipt crossed a rewind: %+v", other)
	}
}

func TestAgyPaneWatchAppendPartialAndRewrites(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	const first = `{"step_index":0,"type":"USER_INPUT","content":"Check the files"}` + "\n"
	const planner = `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-01T00:00:01Z","content":%q}` + "\n"
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, first)
	_, page := agyTestItems(t, n, agyTestRow, nil)
	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": page["to_seq"]})
	t.Cleanup(func() { n.unwatch(agyTestRow) })

	line := fmt.Sprintf(planner, "Draft")
	record := strings.TrimSuffix(line, "\n")
	agyTestAppend(t, file, record[:len(record)-1])
	if got, _ := agyTestItems(t, n, agyTestRow, nil); len(got) != 1 {
		t.Fatalf("truncated JSON was visible: %+v", got)
	}
	agyTestAppend(t, file, record[len(record)-1:]) // Valid JSON is still incomplete until the newline.
	n.mu.Lock()
	w := n.watches[agyTestRow]
	n.mu.Unlock()
	n.pollAgyPane(w) // one read after the append, as the pump's next tick would do
	if got, _ := agyTestItems(t, n, agyTestRow, nil); len(got) != 1 || len(hub.spans()) != 0 {
		t.Fatalf("partial record was visible: page %+v, batches %+v", got, hub.spans())
	}
	agyTestAppend(t, file, "\n")
	waitFor(t, "the complete record", func() bool { return len(hub.spans()) >= 1 })
	draft := hub.got()[0]
	if batch := hub.spans()[0]; draft.Text != "Draft" || batch.fromSeq != 1 || batch.toSeq != 2 || batch.history != page["history"] {
		t.Fatalf("append: %+v, %+v", draft, batch)
	}

	agyTestAppend(t, file, fmt.Sprintf(planner, "Final"))
	waitFor(t, "the appended step completion", func() bool { return len(hub.spans()) >= 2 })
	items, page := agyTestItems(t, n, agyTestRow, nil)
	if batch := hub.spans()[1]; len(items) != 2 || items[1].ID != draft.ID || items[1].Text != "Final" ||
		batch.fromSeq != 0 || batch.toSeq != 2 || batch.n != 2 || batch.history == hub.spans()[0].history || batch.history != page["history"] ||
		hub.got()[2].ID != draft.ID || hub.got()[2].Text != "Final" {
		t.Fatalf("same-step completion: page %+v, batches %+v, items %+v", page, hub.spans(), hub.got())
	}

	agyTestWrite(t, file, first+line+fmt.Sprintf(planner, "Fixed")) // same length and sequence
	waitFor(t, "the in-place same-size rewrite", func() bool { return len(hub.spans()) >= 3 })
	full := agyTestCall(t, n, "session.item", agyTestRow, map[string]any{"item_id": draft.ID}).(proto.Item)
	if batch := hub.spans()[2]; full.Text != "Fixed" || batch.history == page["history"] || batch.fromSeq != 0 || batch.toSeq != 2 || hub.got()[4].Text != "Fixed" {
		t.Fatalf("same-size rewrite: %+v, %+v", full, batch)
	}
	last := hub.spans()[2]
	agyTestAppend(t, file, `{"step_index":2,"type":"PLANNER_RESPONSE","content":"Next"}`+"\n")
	waitFor(t, "an append after rewriting", func() bool { return len(hub.spans()) >= 4 })
	if batch := hub.spans()[3]; batch.history != last.history || batch.fromSeq != 2 || batch.toSeq != 3 || batch.n != 1 {
		t.Fatalf("append after rewrite: %+v", batch)
	}

	agyTestAppend(t, file, `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-02T00:00:01Z","content":"Replacement"}`+"\n")
	waitFor(t, "the replacement branch", func() bool { return len(hub.spans()) >= 5 })
	items, page = agyTestItems(t, n, agyTestRow, nil)
	if batch := hub.spans()[4]; len(items) != 2 || items[1].Text != "Replacement" || items[1].ID == draft.ID ||
		batch.history == last.history || batch.history != page["history"] || batch.fromSeq != 0 || batch.toSeq != 2 {
		t.Fatalf("replacement branch: page %+v, batch %+v", page, batch)
	}
	if _, err := request(t, n, "session.item", map[string]any{"session_id": agyTestRow, "item_id": draft.ID}); err == nil {
		t.Fatal("expanded an item from the abandoned branch")
	}

	agyTestCall(t, n, "session.unwatch", agyTestRow, nil)
	agyTestAppend(t, file, `{"step_index":3,"type":"PLANNER_RESPONSE","content":"After unwatch"}`+"\n")
	n.mu.Lock()
	_, watched := n.watches[agyTestRow]
	n.mu.Unlock()
	n.pollAgyPane(w) // a read the pump had already started for the ended watch pushes nothing
	if got := hub.spans(); watched || len(got) != 5 {
		t.Fatalf("watch pushed after unwatch: %+v", got)
	}
}

func TestAgyPaneWatchConversationIsolation(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	agent := func(pane, id string) host.Agent {
		a := host.Agent{PaneID: pane, Agent: agyAgent, Cwd: home}
		if id != "" {
			a.AgentSession = &host.AgentSession{Kind: "id", Value: id, Agent: agyAgent, Source: "herdr:antigravity_cli"}
		}
		return a
	}
	setSnapshot := func(pane host.Agent, others ...host.Agent) {
		n.mu.Lock()
		n.hostAgents = map[string][]host.Agent{host.Herdr: append([]host.Agent{pane}, others...)}
		n.agents[agyTestRow] = pane
		n.mu.Unlock()
	}
	const user = `{"step_index":0,"type":"USER_INPUT","content":%q}` + "\n"
	agyTestFile(t, n.agyDataRoot(), agyTestID, fmt.Sprintf(user, "first conversation"))
	agyTestFile(t, n.agyDataRoot(), agyOtherTestID, fmt.Sprintf(user, "second conversation"))
	first := agent(agyTestPane, agyTestID)
	setSnapshot(first)
	items, page := agyTestItems(t, n, agyTestRow, nil)
	if len(items) != 1 || items[0].Text != "first conversation" {
		t.Fatalf("initial page: %+v", page)
	}
	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": page["to_seq"]})
	t.Cleanup(func() { n.unwatch(agyTestRow) })
	const noticeKey = "node-engine.agy_pane_integration"
	noticeText := i18n.T(i18n.Default, noticeKey) // the node's own language; the hub says it in each client's
	noticeToken := "agy-pane:" + agyTestRow
	firstToken := page["history"].(string)
	for i, tc := range []struct {
		name                string
		pane                host.Agent
		others              []host.Agent
		kind, text, history string
	}{
		{"ambiguous", first, []host.Agent{agent("w1:p2", agyTestID)}, "notice", noticeText, noticeToken},
		{"verified again", first, nil, "text", "first conversation", firstToken},
		{"ID disappeared", agent(agyTestPane, ""), nil, "notice", noticeText, noticeToken},
		{"ID returned", first, nil, "text", "first conversation", firstToken},
		{"switched conversation", agent(agyTestPane, agyOtherTestID), nil, "text", "second conversation", agyStepsHistory(agyOtherTestID).token},
	} {
		setSnapshot(tc.pane, tc.others...)
		waitFor(t, tc.name, func() bool { return len(hub.spans()) >= i+1 })
		batches, got := hub.spans(), hub.got()
		want := pushed{n: 1, history: tc.history, fromSeq: 0, toSeq: 1}
		if len(batches) != i+1 || batches[i] != want || len(got) != i+1 || got[i].Kind != tc.kind || got[i].Text != tc.text || got[i].TS.IsZero() {
			t.Fatalf("%s: batches %+v, items %+v", tc.name, batches, got)
		}
	}
	if got := hub.got(); got[len(got)-1].ID == items[0].ID {
		t.Fatal("new conversation reused an item ID from the previous conversation")
	}
}

func assertAgyResumeReport(t *testing.T, f *fakeHerdr, index int, model, effort, conversation string) {
	t.Helper()
	wantID, wantCommand := agyTestRow, "agy --model "+model
	if effort != "" {
		wantCommand += " --effort " + effort
	}
	if conversation != "" {
		wantID = conversation
		wantCommand += " --conversation " + conversation
	}
	waitFor(t, "the agy resume report", func() bool { return len(f.trace("pane.report_agent_session")) > index })
	got := f.trace("pane.report_agent_session")
	if len(got) != index+1 || !strings.Contains(got[index], `"agent_session_id":"`+wantID+`"`) ||
		!strings.Contains(got[index], wantCommand+`"`) || (conversation == "" && strings.Contains(got[index], "--conversation")) {
		t.Fatalf("want report %d with session ID %s and command %q, got %v", index, wantID, wantCommand, got)
	}
}

func TestAgyPaneResumeAndTerminalControls(t *testing.T) {
	n, f, home := herdrNode(t)
	without := agyTestAgent(agyTestPane, home, "")
	seedAgents(t, n, f, without)
	const model = "test-model"
	n.noteWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), agyTestPane, agyTestRow, home, agyAgent, agyAgent, model, "")
	assertAgyResumeReport(t, f, 0, model, "", "")
	agyTestCall(t, n, "session.send", agyTestRow, map[string]any{"text": "first\nsecond"})
	agyTestCall(t, n, "session.interrupt", agyTestRow, nil)
	assertTrace(t, f.trace("agent.prompt", "pane.send_keys", "pane.send_input"), []string{
		call("agent.prompt", map[string]any{"target": agyTestPane, "text": "first\nsecond"}),
		sendKeys(agyTestPane, "escape"),
	})
	f.reply("session.snapshot", snapshot(nil, nil, []any{agyTestAgent(agyTestPane, home, agyTestID)}, nil))
	listSessions(t, n)
	assertAgyResumeReport(t, f, 1, model, "", agyTestID)
}

func TestAgyPaneResumeBackfillTracksConversation(t *testing.T) {
	n, f, home := herdrNode(t)
	first := agyTestAgent(agyTestPane, home, agyTestID)
	seedAgents(t, n, f, first)
	const model = "test-model"
	const effort = "high"
	n.noteWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), agyTestPane, agyTestRow, home, agyAgent, agyAgent, model, effort)
	assertAgyResumeReport(t, f, 0, model, effort, agyTestID)
	n.mu.Lock()
	kept := n.launches[agyTestPane].AgyConversation
	n.mu.Unlock()
	if kept != agyTestID { // what was reported is what the launch keeps (review on #10364)
		t.Fatalf("launch keeps conversation %q, reported %q", kept, agyTestID)
	}

	second := agyTestAgent(agyTestPane, home, agyOtherTestID)
	f.reply("session.snapshot", snapshot(nil, nil, []any{second}, nil))
	listSessions(t, n)
	assertAgyResumeReport(t, f, 1, model, effort, agyOtherTestID)

	duplicate := agyTestAgent("w1:p2", home, agyOtherTestID)
	f.reply("session.snapshot", snapshot(nil, nil, []any{second, duplicate}, nil))
	listSessions(t, n)
	assertAgyResumeReport(t, f, 2, model, effort, "")
}

func TestAgyPaneResumeRejectsAmbiguousReports(t *testing.T) {
	for _, tc := range []struct{ name, pane, id string }{
		{"conflicting_reports", agyTestPane, agyOtherTestID},
		{"duplicate_id", "w1:p2", agyTestID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID), agyTestAgent(tc.pane, home, tc.id))
			const model = "test-model"
			n.noteWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), agyTestPane, agyTestRow, home, agyAgent, agyAgent, model, "")
			assertAgyResumeReport(t, f, 0, model, "", "")
		})
	}
}

func TestAgyPaneKeepsCursorTerminalBehavior(t *testing.T) {
	n, f, home := herdrNode(t)
	cursor := agentOf(cursorAgent, agyTestID, proto.StatusIdle, agyTestPane, "w1:t1", "w1", home, "cursor")
	seedAgents(t, n, f, cursor)
	const cursorRow = "cursor-w1-p1"
	items, _ := agyTestItems(t, n, cursorRow, nil)
	if len(items) != 1 || items[0].ID != "cursor#0" || items[0].Kind != "notice" || strings.Contains(items[0].Text, "antigravity-cli") {
		t.Fatalf("cursor notice: %+v", items)
	}
	if _, err := request(t, n, "session.item", map[string]any{"session_id": cursorRow, "item_id": items[0].ID}); err == nil {
		t.Fatal("Cursor unexpectedly expanded a transcript item")
	}
	agyTestCall(t, n, "session.watch", cursorRow, nil)
	n.mu.Lock()
	w := n.watches[cursorRow]
	n.mu.Unlock()
	if w != nil {
		t.Fatal("Cursor unexpectedly started a transcript watch")
	}
	agyTestCall(t, n, "session.unwatch", cursorRow, nil)
	agyTestCall(t, n, "session.send", cursorRow, map[string]any{"text": "check the file"})
	agyTestCall(t, n, "session.interrupt", cursorRow, nil)
	assertTrace(t, f.trace("agent.prompt", "pane.send_keys", "pane.send_input"), []string{
		call("agent.prompt", map[string]any{"target": agyTestPane, "text": "check the file"}),
		sendKeys(agyTestPane, "escape"),
	})
	if got := workAgentResume(cursorAgent, "cursor-agent", agyTestID); got != "cursor-agent" {
		t.Fatalf("Cursor resume: %s", got)
	}
}
