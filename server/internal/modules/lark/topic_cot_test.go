package lark

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// topicCotRun is a main-conversation run answering the message that originated in an opted-in topic.
func topicCotRun(s *Service) (*channel.Run, *chanLive) {
	r, prog := cotRun(s)
	r.SessionID = "m1"
	s.setRunExt(r, "chat", testChat)
	s.setRunExt(r, "thread", testThread)
	return r, prog
}

// AC1: creation stays under the originating message, in its chat and topic, rather than taking the DM's road.
func TestFeishuTopicRunCreatesAgentMessageInOriginTopic(t *testing.T) {
	s, posts, calls := cotHub(t, false)
	r, _ := topicCotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")

	creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate)
	if len(creates) != 1 {
		t.Fatalf("native agent-message creations = %v, want one; progress posts = %v; native calls = %v", creates, *posts, *calls)
	}
	got := creates[0]
	if got["reply_to"] != "om_m1" || got["chat_id"] != testChat || got["in_thread"] != true {
		t.Fatalf("native agent-message creation = %v, want reply_to om_m1, chat_id %s and in_thread true", got, testChat)
	}
}

// AC2: the first process item opens the native message, whether it is thinking or a tool call. An answer alone does not.
func TestFeishuTopicAgentMessageOpensOnFirstProcessItem(t *testing.T) {
	for _, tc := range []struct {
		name       string
		items      []proto.Item
		steps      []string
		wantCreate bool
	}{
		{name: "thinking", items: []proto.Item{{ID: "think", Role: "assistant", Kind: proto.KindThinking, Text: "想一想"}}, wantCreate: true},
		{name: "tool", items: []proto.Item{{ID: "use", Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tu1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)}}, steps: []string{"运行 `ls`"}, wantCreate: true},
		{name: "text only", items: []proto.Item{{ID: "answer", Role: "assistant", Kind: proto.KindText, Text: "好的"}}, wantCreate: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, posts, _ := cotHub(t, false)
			r, prog := topicCotRun(s)
			prog.items, prog.steps = tc.items, tc.steps
			topicCotProgress(t, s, r, time.Second, tc.steps, "")
			creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate)
			if got := len(creates); (got == 1) != tc.wantCreate {
				t.Fatalf("native agent-message creations = %d, want create %v; posts = %v", got, tc.wantCreate, *posts)
			}
			if tc.wantCreate {
				return
			}
			finishTopicCot(t, s, r, &channel.Result{Answer: "好的"})
			if creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate); len(creates) != 0 {
				t.Fatalf("a text-only turn created an empty native agent message: %v", creates)
			}
			card := lastCard(t, r, *posts)
			if !strings.Contains(card, "好的") || strings.Contains(card, "collapsible_panel") {
				t.Fatalf("text-only answer card = %s", card)
			}
		})
	}
}

// AC3: the topic's native run carries thinking, an icon and useful tool subject, expandable output, and its finish.
func TestFeishuTopicAgentMessageStreamsNativeEvents(t *testing.T) {
	s, _, _ := cotHub(t, false)
	r, prog := topicCotRun(s)
	prog.items[2].Text = "a.txt"
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})

	var events []botcard.CotEvent
	for _, p := range s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotAppend) {
		raw, _ := json.Marshal(p["events"])
		var batch []botcard.CotEvent
		_ = json.Unmarshal(raw, &batch)
		events = append(events, batch...)
	}
	var types []string
	contents := map[string]map[string]any{}
	for _, e := range events {
		types = append(types, e.Type)
		if _, seen := contents[e.Type]; !seen {
			var body map[string]any
			_ = json.Unmarshal([]byte(e.Content), &body)
			contents[e.Type] = body
		}
	}
	want := "RUN_STARTED REASONING_START REASONING_MESSAGE_START REASONING_MESSAGE_CONTENT REASONING_MESSAGE_END TOOL_CALL_START TOOL_CALL_END TOOL_CALL_RESULT REASONING_END RUN_FINISHED"
	if strings.Join(types, " ") != want {
		t.Fatalf("native events = %v, want %s", types, want)
	}
	if body := contents["REASONING_MESSAGE_CONTENT"]; body["delta"] != "想一想" {
		t.Fatalf("thinking event = %v", body)
	}
	if body := contents["TOOL_CALL_START"]; body["icon"] != "bash" || body["toolCallName"] != "Bash" || !strings.Contains(body["title"].(string), "ls") || body["parentMessageId"] == "" {
		t.Fatalf("tool-call event = %v", body)
	}
	var output struct {
		Type string `json:"type"`
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(contents["TOOL_CALL_RESULT"]["content"].(string)), &output)
	if output.Type != "code" || output.Code != "a.txt" {
		t.Fatalf("expandable tool output = %+v", output)
	}
	if body := contents["RUN_FINISHED"]; body["status"] != "done" {
		t.Fatalf("run finish = %v", body)
	}
}

// AC4: native process display supplements the progress card; it neither replaces it nor duplicates the final answer.
func TestFeishuTopicNativeProcessKeepsProgressAndSingleAnswerCard(t *testing.T) {
	s, posts, calls := cotHub(t, false)
	withWebBot(s.hub.(*fakeHub), "https://hub.example.com")
	r, _ := topicCotRun(s)
	withCot(s, r) // isolate the card/native coexistence from creation, covered above
	topicCotProgress(t, s, r, 4*time.Second, []string{"运行 `ls`"}, "")
	if len(*posts) != 1 {
		t.Fatalf("progress posts = %v, want the one progress card beside the native agent message", *posts)
	}
	progress, _ := (*posts)[0]["card"].(string)
	if !strings.Contains(progress, "4 秒") || !strings.Contains(progress, "ls") || (*posts)[0]["reply_to"] != "om_m1" || (*posts)[0]["chat_id"] != testChat || (*posts)[0]["in_thread"] != true {
		t.Fatalf("progress card beside native process = %v", (*posts)[0])
	}

	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	if len(*posts) != 2 {
		t.Fatalf("posts after finish = %v, want one progress post and its answer update", *posts)
	}
	answerCards := 0
	for _, p := range *posts {
		if card, _ := p["card"].(string); strings.Contains(card, "结论在这里") {
			answerCards++
		}
	}
	card := lastCard(t, r, *posts)
	if answerCards != 1 || strings.Contains(card, "collapsible_panel") || !strings.Contains(card, "https://hub.example.com/persona/m1") {
		t.Fatalf("answer cards = %d, final card = %s; want one answer with Open web app and no fallback process panel", answerCards, card)
	}
	if got := strings.Join(*calls, " "); !strings.HasSuffix(got, "REASONING_END RUN_FINISHED") {
		t.Fatalf("native run did not finish with the turn: %s", got)
	}
}

// AC5: the visible setting can restore today's card and folded process for new topic runs.
func TestFeishuTopicNativeProcessSettingOffKeepsCardPanel(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		// A generic status payload keeps this red-first test compilable before the feature exists in the wire type.
		return map[string]any{"ready": true, "enabled": true, "owner": "ou_a", "paired_at": time.Unix(100, 0),
			"features": map[string]any{"main": true, "cards": true, "topic_cot": false}}, nil
	})
	r, _ := topicCotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	if creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate); len(creates) != 0 || len(*posts) != 1 {
		t.Fatalf("topic_cot off: native creations = %v, progress posts = %v", creates, *posts)
	}
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	card := lastCard(t, r, *posts)
	if !strings.Contains(card, "结论在这里") || !strings.Contains(card, "collapsible_panel") || !strings.Contains(card, "已调用 1 次工具") {
		t.Fatalf("topic_cot off answer card = %s", card)
	}
}

// AC6: a refused creation is recorded and leaves the complete card/panel fallback, including the answer.
func TestFeishuTopicNativeCreationRefusalFallsBack(t *testing.T) {
	s, posts, calls := cotHub(t, true)
	r, _ := topicCotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate)
	if len(creates) != 1 {
		t.Fatalf("refused native creation attempts = %v, want one", creates)
	}
	if r.Ext["cot_off"] == "" || len(*posts) != 1 || r.Ext["card"] == "" {
		t.Fatalf("fallback was not recorded with a progress card: ext = %v, posts = %v", r.Ext, *posts)
	}
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	if len(*posts) != 2 {
		t.Fatalf("fallback posts = %v, want the progress card and one answer update", *posts)
	}
	card := lastCard(t, r, *posts)
	if !strings.Contains(card, "结论在这里") || !strings.Contains(card, "collapsible_panel") || !strings.Contains(card, "已调用 1 次工具") {
		t.Fatalf("fallback answer card = %s", card)
	}
	if got := strings.Join(*calls, " "); strings.Count(got, "create") != 1 || strings.Contains(got, "RUN_STARTED") {
		t.Fatalf("native calls after creation was refused = %s", got)
	}
}

// measureTopicCotProgressTiming measures send-to-feedback (the Processing card reached the fake API) separately from
// send-to-execution (the sink's whole progress pass completed). No model call is involved; that boundary is not measured.
func measureTopicCotProgressTiming(t *testing.T, native bool, nativeDelay time.Duration) (feedback, execution time.Duration) {
	s, posts, _ := cotHub(t, false)
	f := s.hub.(*fakeHub)
	if !native {
		f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
			return map[string]any{"ready": true, "enabled": true, "owner": "ou_a", "paired_at": time.Unix(100, 0),
				"features": map[string]any{"main": true, "cards": true, "topic_cot": false}}, nil
		})
	}
	r, prog := topicCotRun(s)
	prog.from = 1 // the run's own message is in the conversation: the card says Processing, not Queued

	start := time.Now()
	feedbackSent := make(chan time.Duration, 1)
	var feedbackOnce sync.Once
	f.answerApp(AppFeishu, FeishuPost, func(raw json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		*posts = append(*posts, m)
		feedbackOnce.Do(func() { feedbackSent <- time.Since(start) })
		return map[string]any{"message_id": "om_card"}, nil
	})

	entered, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	releaseCreate := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseCreate)
	if nativeDelay > 0 {
		f.answerAppBlocking(AppFeishu, FeishuCotCreate, func(json.RawMessage) (any, error) {
			entered <- struct{}{}
			select {
			case <-release:
				return map[string]any{"cot_id": "cot1", "message_id": "om_cot"}, nil
			case <-time.After(2 * time.Second):
				return nil, context.DeadlineExceeded
			}
		})
	}

	done := make(chan struct{})
	go func() {
		s.sink().progress(context.Background(), r, time.Second, []string{"运行 `ls`"}, "")
		close(done)
	}()
	if nativeDelay > 0 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("native creation was not entered within one second")
		}
	}
	select {
	case feedback = <-feedbackSent:
	case <-time.After(time.Second):
		releaseCreate()
		t.Fatalf("no Processing card within one second (native creation delay %s)", nativeDelay)
	}
	if feedback >= time.Second {
		t.Fatalf("send-to-feedback = %s, must be under one second", feedback)
	}
	if len(*posts) != 1 || !strings.Contains((*posts)[0]["card"].(string), "处理中") {
		t.Fatalf("first feedback is not the Processing card: %v", *posts)
	}
	if nativeDelay > 0 {
		timer := time.NewTimer(nativeDelay)
		<-timer.C // hold the fake native API after feedback arrived, proving it was not waited on
		releaseCreate()
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("progress pass did not complete")
	}
	waitTopicCot(t, s, r)
	execution = time.Since(start)
	if execution < feedback || execution < nativeDelay {
		t.Fatalf("feedback %s, execution %s, native delay %s", feedback, execution, nativeDelay)
	}
	creates := len(f.appParams(AppFeishu, FeishuCotCreate))
	if native && creates != 1 || !native && creates != 0 {
		t.Fatalf("native=%t: creation calls = %d", native, creates)
	}
	t.Logf("native=%t native_delay=%s: send-to-feedback=%s; send-to-execution=%s", native, nativeDelay, feedback, execution)
	return feedback, execution
}

// Principle 7: the opt-out is the before path and native processing the after path. Processing arrives within one
// second in both, and remains prompt when the native API is deliberately held for 300 ms. Execution is measured apart.
func TestFeishuTopicCotFeedbackAndExecutionTiming(t *testing.T) {
	t.Run("before card only", func(t *testing.T) { measureTopicCotProgressTiming(t, false, 0) })
	t.Run("after native process", func(t *testing.T) { measureTopicCotProgressTiming(t, true, 0) })
	t.Run("delayed native process", func(t *testing.T) { measureTopicCotProgressTiming(t, true, 300*time.Millisecond) })
}

// waitTopicCot observes the native worker, without making card delivery wait on it in production.
func waitTopicCot(t *testing.T, s *Service, r *channel.Run) {
	t.Helper()
	s.chans.mu.Lock()
	p := s.chans.live[r.ID]
	var w *cotWorker
	if p != nil {
		w = p.native()
	}
	s.chans.mu.Unlock()
	if w == nil {
		return
	}
	waitCotWorker(t, w)
}

func waitCotWorker(t *testing.T, w *cotWorker) {
	t.Helper()
	waitFor(t, "native worker to become idle", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return !w.queued && w.cancel == nil
	})
}

func topicCotProgress(t *testing.T, s *Service, r *channel.Run, took time.Duration, steps []string, live string) {
	t.Helper()
	s.sink().progress(context.Background(), r, took, steps, live)
	waitTopicCot(t, s, r)
}

func finishTopicCot(t *testing.T, s *Service, r *channel.Run, result *channel.Result) {
	t.Helper()
	// Keep the worker reference after finish removes the run.
	s.chans.mu.Lock()
	p := s.chans.live[r.ID]
	s.chans.mu.Unlock()
	s.finishChannelRun(r, result)
	if p != nil {
		s.chans.mu.Lock()
		w := p.native()
		s.chans.mu.Unlock()
		if w != nil {
			select {
			case <-w.done:
			case <-time.After(2 * time.Second):
				t.Fatal("native finish did not end")
			}
		}
	}
}
