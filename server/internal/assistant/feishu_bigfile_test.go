package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// bigFileHub is a paired bot whose hub says every file is too large to save.
func bigFileHub(t *testing.T) (*fakeHub, *Service, *[]string) {
	t.Helper()
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true})
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { return map[string]any{"reaction_id": "r1"}, nil })
	f.answerApp(AppFeishu, FeishuUnreact, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerApp(AppFeishu, FeishuSave, func(json.RawMessage) (any, error) { return map[string]any{"too_large": true}, nil })
	var notes []string
	f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		notes = append(notes, m["text"].(string))
		return map[string]any{}, nil
	})
	return f, newTestService(t, f), &notes
}

// A message with words and a file too large to pass on: the words go into the conversation, and a line says which
// file was left out.
func TestFeishuTooLargeFileLeavesTheRest(t *testing.T) {
	f, s, notes := bigFileHub(t)
	var ev botcard.MessageEvent
	ev.Message.MessageID, ev.Message.ChatType, ev.Message.MessageType = "om_big", "p2p", "post"
	ev.Message.Content = `{"title":"","content":[[{"tag":"text","text":"帮我看下这个录屏"}],[{"tag":"media","file_key":"file_big"}]]}`
	content := botcard.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
	if err := s.botSend(context.Background(), &ev, content, botTarget{Kind: targetMain}); err != nil {
		t.Fatalf("the whole message refused: %v", err)
	}
	if text := sendOnceText(f); !strings.Contains(text, "帮我看下这个录屏") || strings.Contains(text, "附件") {
		t.Fatalf("text %q", text)
	}
	if len(*notes) != 1 || !strings.Contains((*notes)[0], "太大") || !strings.Contains((*notes)[0], "视频.mp4") {
		t.Fatalf("notes %v", *notes)
	}
}

// Only a file too large and nothing else: nothing to send, and the answer says it is too large.
func TestFeishuOnlyATooLargeFile(t *testing.T) {
	f, s, _ := bigFileHub(t)
	var ev botcard.MessageEvent
	ev.Message.MessageID, ev.Message.ChatType, ev.Message.MessageType = "om_big", "p2p", "file"
	ev.Message.Content = `{"file_key":"file_big","file_name":"录屏.mov"}`
	content := botcard.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
	err := s.botSend(context.Background(), &ev, content, botTarget{Kind: targetMain})
	if err == nil || !strings.Contains(err.Error(), "太大") || !strings.Contains(err.Error(), "录屏.mov") {
		t.Fatalf("err %v", err)
	}
	// It is said under the message wrapped in not_sent: "Could not send" once, not twice.
	note := i18n.T("zh-Hans", "assistant-feishu.not_sent", i18n.Args{"error": err.Error()})
	if strings.Count(note, "没能发出去") != 1 {
		t.Fatalf("note %q", note)
	}
	if text := sendOnceText(f); text != "" {
		t.Fatalf("sent %q", text)
	}
}
