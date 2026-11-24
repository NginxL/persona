package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// A message the owner recalled before it went into its conversation is not sent, and its OnIt reaction goes.
func TestFeishuRecalledMessageIsNotSent(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true})
	unreacted := 0
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { return map[string]any{"reaction_id": "r1"}, nil })
	f.answerApp(AppFeishu, FeishuUnreact, func(json.RawMessage) (any, error) { unreacted++; return map[string]any{}, nil })
	s := newTestService(t, f)
	var ev botcard.MessageEvent
	ev.Message.MessageID, ev.Message.ChatType, ev.Message.MessageType, ev.Message.Content = "om_x", "p2p", "text", `{"text":"发错了"}`

	s.FeishuRecalled(FeishuRecalled{MessageID: "om_x"})
	s.feishuMessage(ev, FeishuStatus{Ready: true, Features: FeishuFeatures{Main: true}}, "")
	if text := sendOnceText(f); text != "" {
		t.Fatalf("a recalled message was sent: %q", text)
	}
	// Recalled while it was being read (its downloads): botSend stops before it goes in.
	ev.Message.MessageID = "om_y"
	content := botcard.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
	s.FeishuRecalled(FeishuRecalled{MessageID: "om_y"})
	if err := s.botSend(context.Background(), &ev, content, botTarget{Kind: targetMain}); err != nil {
		t.Fatal(err)
	}
	if text := sendOnceText(f); text != "" || unreacted != 1 {
		t.Fatalf("sent %q, reactions taken off %d", text, unreacted)
	}
	// Another message goes as before.
	ev.Message.MessageID = "om_z"
	if err := s.botSend(context.Background(), &ev, content, botTarget{Kind: targetMain}); err != nil || sendOnceText(f) == "" {
		t.Fatalf("an ordinary message: %v", err)
	}
}

// recallHub is the Feishu fake with a run being answered (its progress card out, an OnIt reaction on its message),
// counting the reactions put on and taken off.
func recallHub(t *testing.T) (s *Service, f *fakeHub, r *channel.Run, prog *chanLive, posts *[]map[string]any, reacts *[]map[string]any, unreacts *[]map[string]any) {
	t.Helper()
	f, posts = feishuHub("ou_a", time.Unix(100, 0))
	reacts, unreacts = &[]map[string]any{}, &[]map[string]any{}
	record := func(into *[]map[string]any) appAnswer {
		return func(p json.RawMessage) (any, error) {
			var m map[string]any
			_ = json.Unmarshal(p, &m)
			*into = append(*into, m)
			return map[string]any{"reaction_id": "r_done"}, nil
		}
	}
	f.answerApp(AppFeishu, FeishuReact, record(reacts))
	f.answerApp(AppFeishu, FeishuUnreact, record(unreacts))
	f.setSessions("server", proto.Session{SessionID: "c1", Assistant: true, Status: proto.StatusWorking})
	s = newTestService(t, f)
	r, prog = cotRun(s)
	s.setRunExt(r, "reaction", "r_onit")
	r.MsgIDs = []string{channel.ClientMsgID(channel.Feishu, "om_m1")}
	feishuSink{s}.progress(context.Background(), r, time.Second, []string{"运行 `ls`"}, "")
	return
}

// The owner recalls the one message a turn is answering (#5248): the turn is interrupted as /stop does, the progress
// card ends as recalled with no answer, and the OnIt reaction goes without a DONE.
func TestFeishuRecallStopsTheTurnAnsweringIt(t *testing.T) {
	needsLarkLayer(t)
	s, f, r, prog, posts, reacts, unreacts := recallHub(t)
	prog.from = 3 // its message is in the conversation
	s.FeishuRecalled(FeishuRecalled{MessageID: "om_m1"})
	waitFor(t, "the run ended", func() bool { return len(s.channelRuns(channel.Feishu)) == 0 })
	waitFor(t, "the turn interrupted", func() bool { return slices.Equal(f.interrupted(), []string{"server/c1"}) })
	card := lastCard(t, r, *posts)
	if !strings.Contains(card, "撤回了，这一轮停了") || strings.Contains(card, "完成 ·") || strings.Contains(card, "处理中") {
		t.Fatalf("card %s", card)
	}
	if len(*reacts) != 0 || len(*unreacts) != 1 || (*unreacts)[0]["reaction_id"] != "r_onit" {
		t.Fatalf("reactions put on %v, taken off %v", *reacts, *unreacts)
	}
	// The turn's end comes after: nothing more goes out.
	n := len(*posts)
	f.setItems(proto.Item{Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "答案"})
	s.channelTurnEnded(context.Background(), "server", "c1")
	if len(*posts) != n {
		t.Fatalf("something went out after the recall: %v", (*posts)[n:])
	}
}

// An answer read just before the recall, finished after it, does not go out either.
func TestFeishuRecalledRunSendsNoAnswer(t *testing.T) {
	needsLarkLayer(t)
	s, _, r, _, posts, _, _ := recallHub(t)
	s.chans.mu.Lock()
	r.Recalled = true
	s.chans.mu.Unlock()
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	if card := lastCard(t, r, *posts); strings.Contains(card, "结论在这里") || !strings.Contains(card, "撤回了") {
		t.Fatalf("card %s", card)
	}
}

// Recalled while its message waits behind another turn of the conversation: the run ends with no answer, but nothing
// is interrupted (that would stop the other turn).
func TestFeishuRecallOfAQueuedMessageInterruptsNothing(t *testing.T) {
	needsLarkLayer(t)
	s, f, r, _, posts, _, unreacts := recallHub(t)
	f.setItems(proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, Text: "上一条", ClientMsgID: "feishu-om_before"})
	s.recallChannelRun(channel.Feishu, "om_m1", channel.ClientMsgID(channel.Feishu, "om_m1"))
	if len(s.channelRuns(channel.Feishu)) != 0 || len(f.interrupted()) != 0 || len(*unreacts) != 1 {
		t.Fatalf("runs %v, interrupted %v, unreacted %v", s.channelRuns(channel.Feishu), f.interrupted(), *unreacts)
	}
	if card := lastCard(t, r, *posts); !strings.Contains(card, "撤回了，这一轮停了") {
		t.Fatalf("card %s", card)
	}
}

// Answered together with another message (folded): the turn goes on, only the recalled one is taken out; the answer
// then marks just the other one done.
func TestFeishuRecallOfAFoldedMessageKeepsTheTurn(t *testing.T) {
	needsLarkLayer(t)
	s, f, r, prog, posts, reacts, unreacts := recallHub(t)
	prog.from = 3
	s.chans.mu.Lock()
	r.Folded = []channel.Folded{{ReplyTo: "om_m0", Ext: map[string]string{"reaction": "r_m0", "card": "om_card0"}}}
	r.MsgIDs = []string{channel.ClientMsgID(channel.Feishu, "om_m0"), channel.ClientMsgID(channel.Feishu, "om_m1")}
	s.chans.mu.Unlock()
	n := len(*posts)
	s.recallChannelRun(channel.Feishu, "om_m0", channel.ClientMsgID(channel.Feishu, "om_m0"))
	runs := s.channelRuns(channel.Feishu)
	if len(runs) != 1 || len(runs[0].Folded) != 0 || !slices.Equal(runs[0].MsgIDs, []string{"feishu-om_m1"}) || runs[0].Recalled {
		t.Fatalf("runs %+v", runs)
	}
	if len(f.interrupted()) != 0 || len(*unreacts) != 1 || (*unreacts)[0]["reaction_id"] != "r_m0" {
		t.Fatalf("interrupted %v, unreacted %v", f.interrupted(), *unreacts)
	}
	if len(*posts) != n+1 || (*posts)[n]["message_id"] != "om_card0" || !strings.Contains((*posts)[n]["card"].(string), "撤回了") {
		t.Fatalf("the recalled one's card: %v", (*posts)[n:])
	}
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	if card := lastCard(t, r, *posts); !strings.Contains(card, "结论在这里") {
		t.Fatalf("card %s", card)
	}
	if len(*reacts) != 1 || (*reacts)[0]["message_id"] != "om_m1" {
		t.Fatalf("marked done %v", *reacts)
	}
}

// Recalled after its answer went out: nothing changes.
func TestFeishuRecallAfterTheAnswerChangesNothing(t *testing.T) {
	s, f, r, prog, posts, reacts, unreacts := recallHub(t)
	prog.from = 3
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	n, nr, nu := len(*posts), len(*reacts), len(*unreacts)
	s.recallChannelRun(channel.Feishu, "om_m1", channel.ClientMsgID(channel.Feishu, "om_m1"))
	if len(*posts) != n || len(*reacts) != nr || len(*unreacts) != nu || len(f.interrupted()) != 0 {
		t.Fatalf("posts %v, reacts %v, unreacts %v, interrupted %v", (*posts)[n:], *reacts, *unreacts, f.interrupted())
	}
}
