package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/channel"
)

// newCardHub is endHub with answer_new_card on: a run that shows its progress card, what is posted from then on.
func newCardHub(t *testing.T) (*Service, *fakeHub, *channel.Run, *[]map[string]any) {
	t.Helper()
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0),
			Features: FeishuFeatures{Main: true, Cards: true, AnswerNewCard: true}}, nil
	})
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerApp(AppFeishu, FeishuUnreact, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	s := newTestService(t, f)
	r, _ := cotRun(s)
	feishuSink{s}.progress(context.Background(), r, time.Second, []string{"运行 `ls`"}, "")
	*posts = nil
	return s, f, r, posts
}

// With answer_new_card the answer is a new card under the owner's message, and the progress card folds into the
// process with a line pointing below (#5396).
func TestFeishuAnswerNewCard(t *testing.T) {
	needsLarkLayer(t)
	s, _, r, posts := newCardHub(t)
	progress := r.Ext["card"]
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	if len(*posts) != 2 {
		t.Fatalf("posts %v", *posts)
	}
	answer, fold := (*posts)[0], (*posts)[1]
	card, _ := answer["card"].(string)
	if answer["update"] == true || answer["reply_to"] != "om_m1" || answer["uuid"] == "" ||
		!strings.Contains(card, "结论在这里") || !strings.Contains(card, "完成 · Persona · 用时") || strings.Contains(card, "已调用 1 次工具") {
		t.Fatalf("answer %v", answer)
	}
	folded, _ := fold["card"].(string)
	if fold["update"] != true || fold["message_id"] != progress || !strings.Contains(folded, "回复见下方") ||
		!strings.Contains(folded, "已调用 1 次工具") || strings.Contains(folded, "结论在这里") || strings.Contains(folded, "处理中") {
		t.Fatalf("folded %v", fold)
	}
}

// A quiet ending has no answer to send: it stays in the progress card, as with the switch off.
func TestFeishuAnswerNewCardQuietEnd(t *testing.T) {
	needsLarkLayer(t)
	s, _, r, posts := newCardHub(t)
	s.finishChannelRun(r, &channel.Result{Quiet: true})
	if len(*posts) != 1 || (*posts)[0]["update"] != true {
		t.Fatalf("posts %v", *posts)
	}
}

// A new card Feishu refuses is written into the progress card instead: the answer still lands.
func TestFeishuAnswerNewCardRefusedFallsBack(t *testing.T) {
	needsLarkLayer(t)
	s, f, r, posts := newCardHub(t)
	f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		return nil, errors.New("feishu post: 230002 bot is not in the chat")
	})
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	if len(*posts) != 1 {
		t.Fatalf("posts %v", *posts)
	}
	if card := lastCard(t, r, *posts); !strings.Contains(card, "结论在这里") || strings.Contains(card, "回复见下方") {
		t.Fatalf("card %s", card)
	}
}

// An answer that did not reach the hub is tried again under the same key, so one that did arrive lands once, and
// the progress card is folded only once it is out.
func TestFeishuAnswerNewCardTriesAgainWhenUnreachable(t *testing.T) {
	needsLarkLayer(t)
	s, f, r, posts := newCardHub(t)
	var keys []string
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		key, _ := m["uuid"].(string)
		keys = append(keys, key)
		if len(keys) == 1 {
			return nil, bus.ErrOffline
		}
		*posts = append(*posts, m)
		return map[string]any{"message_id": "om_answer"}, nil
	})
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	if run, _ := s.channelRunByID(r.ID); run == nil || len(*posts) != 0 {
		t.Fatalf("the run ended or wrote something although nothing got out: %v", *posts)
	}
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Fatal("the run did not end after the retry")
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys %q", keys)
	}
	if len(*posts) != 2 || (*posts)[1]["update"] != true {
		t.Fatalf("posts %v", *posts)
	}
}
