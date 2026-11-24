package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Feishu refuses an update of the progress card that comes too soon after the last one. The answer goes into that card
// at the end of the run, when no later update writes it again: if it does not wait the card-update window out, the
// answer becomes a card of its own and the progress card keeps saying it is still working for good (#3915).
func TestFeishuAnswerWaitsOutACardUpdateThrottle(t *testing.T) {
	needsLarkLayer(t)
	s, posts, _ := cotHub(t, false)
	r, _ := cotRun(s)
	feishuSink{s}.progress(context.Background(), r, time.Second, []string{"运行 `ls`"}, "")
	card := r.Ext["card"]
	if card == "" || len(*posts) != 1 {
		t.Fatalf("progress: posts %v, ext %v", *posts, r.Ext)
	}
	s.setLarkTiming("answer_card_wait", time.Millisecond)
	updates := 0
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuUpdateCard, func(p json.RawMessage) (any, error) {
		updates++
		if updates == 1 {
			return nil, errors.New("feishu update card: 230020 card is updated too often")
		}
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		m["update"] = true
		*posts = append(*posts, m)
		return map[string]any{}, nil
	})
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	if updates != 2 {
		t.Fatalf("updates %d, want the answer to try the card again", updates)
	}
	if len(*posts) != 2 {
		t.Fatalf("posts %v, want the answer in the progress card and no card of its own", *posts)
	}
	last := (*posts)[1]
	if last["message_id"] != card || last["update"] != true {
		t.Fatalf("the answer went into %v, want the progress card %q", last, card)
	}
	if c, _ := last["card"].(string); !strings.Contains(c, "结论在这里") {
		t.Fatalf("answer card %q", c)
	}
}

// A quiet end has nothing to send but its last line, which replaces the progress card: the same wait applies, or the
// card keeps saying the run is still going with nothing to come.
func TestFeishuQuietEndWaitsOutACardUpdateThrottle(t *testing.T) {
	needsLarkLayer(t)
	s, posts, _ := cotHub(t, false)
	r, _ := cotRun(s)
	feishuSink{s}.progress(context.Background(), r, time.Second, []string{"运行 `ls`"}, "")
	if r.Ext["card"] == "" || len(*posts) != 1 {
		t.Fatalf("progress: posts %v, ext %v", *posts, r.Ext)
	}
	progress, _ := (*posts)[0]["card"].(string)
	s.setLarkTiming("answer_card_wait", time.Millisecond)
	updates := 0
	cards := []string{}
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuUpdateCard, func(p json.RawMessage) (any, error) {
		updates++
		if updates == 1 {
			return nil, errors.New("feishu update card: 230020 card is updated too often")
		}
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		c, _ := m["card"].(string)
		cards = append(cards, c)
		return map[string]any{}, nil
	})
	s.finishChannelRun(r, &channel.Result{Quiet: true})
	if updates != 2 || len(*posts) != 1 {
		t.Fatalf("updates %d, posts %v, want the card tried again and nothing else sent", updates, *posts)
	}
	if len(cards) != 1 || cards[0] == progress || !strings.Contains(cards[0], i18n.T("", "assistant-feishu.end_done")) {
		t.Fatalf("the card ended as %q (was %q)", cards, progress)
	}
}

// An update refused for another reason is Feishu's word on that card: the answer does not wait it out, it becomes a
// card of its own at once.
func TestFeishuAnswerDoesNotWaitForARefusedCard(t *testing.T) {
	needsLarkLayer(t)
	s, posts, _ := cotHub(t, false)
	r, _ := cotRun(s)
	feishuSink{s}.progress(context.Background(), r, time.Second, []string{"运行 `ls`"}, "")
	if r.Ext["card"] == "" || len(*posts) != 1 {
		t.Fatalf("progress: posts %v, ext %v", *posts, r.Ext)
	}
	s.setLarkTiming("answer_card_wait", time.Hour)
	updates := 0
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) {
		updates++
		return nil, errors.New("feishu update card: 230025 card is not a card")
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the answer waited on a refusal that is not a rate limit")
	}
	if updates != 1 || len(*posts) != 2 {
		t.Fatalf("updates %d, posts %v, want one refused update then a card of its own", updates, *posts)
	}
	if c, _ := (*posts)[1]["card"].(string); !strings.Contains(c, "结论在这里") {
		t.Fatalf("answer card %q", c)
	}
}
