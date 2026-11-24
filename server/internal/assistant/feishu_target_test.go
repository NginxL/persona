package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
)

// The wrappers' target adds chat_id / in_thread to feishu.post and feishu.text only when set: the usual calls carry
// exactly the fields they always did.
func TestFeishuTargetParams(t *testing.T) {
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	var notes []map[string]any
	f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		notes = append(notes, m)
		return map[string]any{}, nil
	})
	s := newTestService(t, f)
	ctx := context.Background()
	card := botcard.NewCard("t", "green", "a").Markdown("target card")

	if _, err := s.botPost(ctx, "om_1", card, "fb"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.botPostTo(ctx, feishuTarget{ChatID: "oc_test", InThread: true}, "om_2", card, "fb", ""); err != nil {
		t.Fatal(err)
	}
	s.botNote(ctx, "om_3", "plain note")
	s.botNoteTo(ctx, feishuTarget{InThread: true}, "om_4", "thread note")

	for i, want := range []map[string]any{{"reply_to": "om_1"}, {"reply_to": "om_2", "chat_id": "oc_test", "in_thread": true}} {
		got := (*posts)[i]
		for _, k := range []string{"chat_id", "in_thread"} {
			if got[k] != want[k] {
				t.Errorf("post %d %s = %v, want %v", i, k, got[k], want[k])
			}
		}
	}
	if _, has := notes[0]["chat_id"]; has || notes[0]["in_thread"] != nil {
		t.Errorf("a plain note carries target fields: %v", notes[0])
	}
	if _, has := notes[1]["chat_id"]; has || notes[1]["in_thread"] != true {
		t.Errorf("a thread note: %v", notes[1])
	}
}
