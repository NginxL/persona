package assistant

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/proto"
)

// A sticker carries no request: it is accepted silently, with no reaction, no run and no reply.
func TestFeishuStickerIsAcceptedSilently(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true})
	reactions := 0
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { reactions++; return map[string]any{"reaction_id": "r1"}, nil })
	s := newTestService(t, f)

	var ev botcard.MessageEvent
	ev.Message.MessageID, ev.Message.ChatType, ev.Message.MessageType, ev.Message.Content = "om_s", "p2p", "sticker", `{"file_key":"f1"}`
	s.feishuMessage(ev, FeishuStatus{Ready: true, Features: FeishuFeatures{Main: true}}, "")
	if reactions != 0 {
		t.Fatalf("a sticker got a reaction: %d", reactions)
	}
	if text := sendOnceText(f); text != "" {
		t.Fatalf("a sticker started a turn: %q", text)
	}

	// An ordinary text message still goes through.
	ev.Message.MessageID, ev.Message.MessageType, ev.Message.Content = "om_t", "text", `{"text":"你好"}`
	s.feishuMessage(ev, FeishuStatus{Ready: true, Features: FeishuFeatures{Main: true}}, "")
	if text := sendOnceText(f); text == "" {
		t.Fatal("an ordinary text message was not sent")
	}
}
