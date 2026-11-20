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
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// endHub is the Feishu fake with a run that already shows its progress card, and the fake hub to set its
// conversation on.
func endHub(t *testing.T) (*Service, *fakeHub, *channel.Run, *[]map[string]any) {
	t.Helper()
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerApp(AppFeishu, FeishuUnreact, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	s := newTestService(t, f)
	r, _ := cotRun(s)
	feishuSink{s}.progress(context.Background(), r, time.Second, []string{"运行 `ls`"}, "")
	return s, f, r, posts
}

// lastCard is the card the run's progress card was last rewritten to.
func lastCard(t *testing.T, r *channel.Run, posts []map[string]any) string {
	t.Helper()
	last := posts[len(posts)-1]
	if last["update"] != true || last["message_id"] != r.Ext["card"] {
		t.Fatalf("the progress card was not rewritten: %v", last)
	}
	card, _ := last["card"].(string)
	return card
}

// A normal end: the answer replaces the card, its last line says done, the time and the tools called.
func TestFeishuEndDoneFooter(t *testing.T) {
	s, _, r, posts := endHub(t)
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	card := lastCard(t, r, *posts)
	if len(*posts) != 2 || !strings.Contains(card, "完成 · Persona · 用时") || !strings.Contains(card, "调用了 1 次工具") ||
		!strings.Contains(card, "已调用 1 次工具") || strings.Contains(card, "处理中") || strings.Contains(card, `"template"`) {
		t.Fatalf("card %s (posts %d)", card, len(*posts))
	}
}

// Ends that are not normal leave the card in a final state with a header saying so, never "Working".
func TestFeishuEndStates(t *testing.T) {
	for _, tc := range []struct {
		res  *channel.Result
		want string
	}{
		{&channel.Result{End: channel.EndStopped, Answer: "做了一半"}, "已中断"},
		{&channel.Result{End: channel.EndError, Stopped: "API 错误：overloaded"}, "出错了"},
		{&channel.Result{End: channel.EndOffline, Answer: "写了一半"}, "电脑离线"},
		{&channel.Result{End: channel.EndGone, Note: "这段对话不见了（被关掉或删掉了）。"}, "对话已关闭"},
	} {
		s, _, r, posts := endHub(t)
		s.finishChannelRun(r, tc.res)
		card := lastCard(t, r, *posts)
		if !strings.Contains(card, `"template"`) || !strings.Contains(card, tc.want) || strings.Contains(card, "处理中") || strings.Contains(card, "完成 ·") {
			t.Fatalf("%s: card %s", tc.want, card)
		}
	}
}

// checkEnd sets the run's conversation to sess and items, looks at it once, and returns the card it ended on.
func checkEnd(t *testing.T, s *Service, f *fakeHub, r *channel.Run, posts *[]map[string]any, sess proto.Session, items ...proto.Item) string {
	t.Helper()
	sess.MachineID, sess.SessionID = "server", "c1"
	f.setSessions("server", sess)
	f.setItems(items...)
	s.checkChannelRun(r.ID)
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Fatalf("the run did not end")
	}
	return lastCard(t, r, *posts)
}

// The conversation's turn error makes the end "Error"; an answer that only talks about an error is still "Done".
func TestFeishuCheckEndFailed(t *testing.T) {
	answer := proto.Item{Seq: 5, Role: "assistant", Kind: proto.KindText, Text: "修好了 API Error: 503 时的崩溃"}
	s, f, r, posts := endHub(t)
	s.setRunSeen(r)
	card := checkEnd(t, s, f, r, posts, proto.Session{Status: proto.StatusDone, State: proto.StateIdle, TurnError: "unexpected status 503"}, answer)
	if !strings.Contains(card, `"template"`) || !strings.Contains(card, "出错了") {
		t.Fatalf("failed: card %s", card)
	}
	s, f, r, posts = endHub(t)
	s.setRunSeen(r)
	card = checkEnd(t, s, f, r, posts, proto.Session{Status: proto.StatusDone, State: proto.StateIdle, LastText: answer.Text}, answer)
	if strings.Contains(card, `"template"`) || strings.Contains(card, "出错了") || !strings.Contains(card, "完成 · ") {
		t.Fatalf("mentions an error: card %s", card)
	}
}

// A held run whose background work never reported back ends as timed out in the card it still has: the card is
// rewritten with the note, not left saying it is working.
func TestFeishuHeldRunGivesUpOnTheCard(t *testing.T) {
	s, f, r, posts := endHub(t)
	s.chans.mu.Lock()
	r.Held, r.Started = true, time.Now().Add(-channel.HeldMost-time.Minute)
	s.chans.mu.Unlock()
	card := checkEnd(t, s, f, r, posts, proto.Session{Status: proto.StatusDone, State: proto.StateIdle})
	if !strings.Contains(card, `"template"`) || !strings.Contains(card, "已超时，去 App 里看") || !strings.Contains(card, "已调用 1 次工具") ||
		strings.Contains(card, "处理中") {
		t.Fatalf("card %s", card)
	}
}

// setRunSeen marks r as seen working, as the watch would.
func (s *Service) setRunSeen(r *channel.Run) {
	s.chans.mu.Lock()
	r.Seen = true
	s.chans.mu.Unlock()
}

// A run held after answering has no progress card any more (its card went back with the message). When the background
// work it waits for never reports back, the wait runs out — and the chat has to hear about it: the ending posts a card
// of its own, rather than saying nothing and leaving the owner to wonder.
func TestFeishuHeldRunGivesUpWithoutItsCard(t *testing.T) {
	s, f, r, posts := endHub(t)
	s.finishChannelRun(r, &channel.Result{Answer: "先答一部分", Held: true})
	s.chans.mu.Lock()
	left := r.Ext["card"]
	r.Started = time.Now().Add(-channel.HeldMost - time.Minute)
	s.chans.mu.Unlock()
	if left != "" {
		t.Fatalf("the held run kept its card: %q", left)
	}
	*posts = nil
	sess := proto.Session{Status: proto.StatusDone, State: proto.StateIdle, MachineID: "server", SessionID: "c1"}
	f.setSessions("server", sess)
	f.setItems()
	s.checkChannelRun(r.ID)
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Fatalf("the run did not end")
	}
	if len(*posts) != 1 {
		t.Fatalf("the run ended without telling the owner: %v", *posts)
	}
	card, _ := (*posts)[0]["card"].(string)
	if (*posts)[0]["update"] == true || !strings.Contains(card, "已超时，去 App 里看") || !strings.Contains(card, "先不等了") {
		t.Fatalf("card %v", (*posts)[0])
	}
}

// A held run's ending can also be quiet: its background work reported back in a turn that said nothing. The run has no
// card left to rewrite, but the chat was told it would answer again — so its ending line goes out as a card of its own,
// rather than nowhere.
func TestFeishuHeldRunQuietEndWithoutItsCard(t *testing.T) {
	s, f, r, posts := endHub(t)
	s.finishChannelRun(r, &channel.Result{Answer: "先答一部分", Held: true})
	s.chans.mu.Lock()
	left := r.Ext["card"]
	r.HeldAt = time.Now().Add(-channel.HeldIdleMost - time.Minute)
	s.chans.mu.Unlock()
	if left != "" {
		t.Fatalf("the held run kept its card: %q", left)
	}
	*posts = nil
	sess := proto.Session{Status: proto.StatusDone, State: proto.StateIdle, MachineID: "server", SessionID: "c1"}
	f.setSessions("server", sess)
	f.setItems()
	s.checkChannelRun(r.ID)
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Fatalf("the run did not end")
	}
	if len(*posts) != 1 {
		t.Fatalf("the run ended without telling the owner: %v", *posts)
	}
	post := (*posts)[0]
	card, _ := post["card"].(string)
	if post["update"] == true || post["reply_to"] != "om_m1" || !strings.Contains(card, "完成 · Persona · 用时") ||
		strings.Contains(card, "处理中") || cardFooters(t, card) != 1 {
		t.Fatalf("post %v", post)
	}
}

// An unrelated user turn ends the held wait without an answer or a quiet-ending card, even while background work
// remains. Reaction cleanup still uses the normal sink; no conversation content is posted back to the chat.
func TestFeishuHeldRunUnrelatedUserTurnSaysNothing(t *testing.T) {
	s, f, r, posts := heldRunWithoutCard(t)
	sess := proto.Session{Status: proto.StatusDone, State: proto.StateIdle, MachineID: "server", SessionID: "c1",
		Background: []proto.BackgroundTask{{ID: "background-work", Kind: "shell"}}}
	f.setSessions("server", sess)
	f.setItems(
		proto.Item{Seq: 5, Role: "user", Kind: proto.KindText, Text: "an unrelated private question"},
		proto.Item{Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "the unrelated private answer"},
	)
	s.checkChannelRun(r.ID)
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Fatalf("the unrelated turn did not end the held run: %+v", run)
	}
	if len(*posts) != 0 {
		t.Fatalf("the unrelated turn posted to the original chat: %v", *posts)
	}
}

// Live deltas cannot stream an unrelated turn into a held run's new progress card. Its user item ends the run and
// closes any card already opened, without posting the private text or a new quiet-ending card.
func TestFeishuHeldRunStopsUnrelatedProgress(t *testing.T) {
	s, f, r, posts := heldRunWithoutCard(t)
	sess := proto.Session{Status: proto.StatusWorking, State: proto.StateWorking, MachineID: "server", SessionID: "c1",
		Background: []proto.BackgroundTask{{ID: "background-work", Kind: "shell"}}}
	f.setSessions("server", sess)
	s.chans.mu.Lock()
	r.Ext["card"] = "om_unrelated_progress"
	prog := s.chans.live[r.ID]
	prog.updated = time.Now().Add(-channel.ProgressIdle - time.Second)
	prog.looked = time.Now()
	s.chans.mu.Unlock()
	s.SessionDelta(context.Background(), SessionDelta{MachineID: "server", SessionID: "c1", Text: "the unrelated private answer"})
	s.channelProgressTick(r.ID)
	s.SessionItems(context.Background(), SessionItems{MachineID: "server", SessionID: "c1", Items: []proto.Item{
		{Seq: 5, Role: "user", Kind: proto.KindText, Text: "an unrelated private question"},
	}})
	s.channelProgressTick(r.ID)
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Errorf("the unrelated user item did not end the held run")
	}
	for _, post := range *posts {
		raw, err := json.Marshal(post)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "unrelated private") || strings.Contains(string(raw), "处理中") {
			t.Errorf("unrelated progress leaked or stayed open: %s", raw)
		}
		if post["update"] != true {
			t.Errorf("the unrelated end posted a new card: %v", post)
		}
	}
}

// heldRunWithoutCard is a run answered once (held) whose card went back with its message, and whose background wait is
// older than HeldIdleMost: its next end is a quiet one.
func heldRunWithoutCard(t *testing.T) (*Service, *fakeHub, *channel.Run, *[]map[string]any) {
	t.Helper()
	s, f, r, posts := endHub(t)
	s.finishChannelRun(r, &channel.Result{Answer: "先答一部分", Held: true})
	s.chans.mu.Lock()
	left := r.Ext["card"]
	r.HeldAt = time.Now().Add(-channel.HeldIdleMost - time.Minute)
	s.chans.mu.Unlock()
	if left != "" {
		t.Fatalf("the held run kept its card: %q", left)
	}
	*posts = nil
	return s, f, r, posts
}

// A held run's quiet ending says how it ended. Its background work's turn can be interrupted, hit a model API error or
// lose its computer: the chat hears that instead of "Done", as a card of its own (the run has none left) (#4228).
func TestFeishuHeldRunQuietEndSaysHowItEnded(t *testing.T) {
	interrupted := []proto.Item{{Seq: 5, Kind: proto.KindNotice, Role: "system", Text: transcript.InterruptedNotice}}
	for _, tc := range []struct {
		name  string
		sess  proto.Session
		items []proto.Item
		want  string
	}{
		{"stopped", proto.Session{Status: proto.StatusDone, State: proto.StateIdle}, interrupted, "已中断"},
		{"failed", proto.Session{Status: proto.StatusDone, State: proto.StateIdle, TurnError: "unexpected status 503"}, nil, "出错了"},
		{"offline", proto.Session{Status: proto.StatusOffline, State: proto.StateIdle}, nil, "电脑离线"},
	} {
		s, f, r, posts := heldRunWithoutCard(t)
		sess := tc.sess
		sess.MachineID, sess.SessionID = "server", "c1"
		f.setSessions("server", sess)
		f.setItems(tc.items...)
		s.checkChannelRun(r.ID)
		if run, _ := s.channelRunByID(r.ID); run != nil {
			t.Fatalf("%s: the run did not end", tc.name)
		}
		if len(*posts) != 1 {
			t.Fatalf("%s: the run ended without telling the owner: %v", tc.name, *posts)
		}
		post := (*posts)[0]
		card, _ := post["card"].(string)
		if post["update"] == true || !strings.Contains(card, tc.want) || !strings.Contains(card, "· Persona · 用时") ||
			strings.Contains(card, "完成 ·") {
			t.Fatalf("%s: post %v", tc.name, post)
		}
	}
}

// cardFooters is how many of a card's body elements carry its ending line. Its chat-list summary repeats the line by
// design, so the whole JSON has it twice even when the card shows it once.
func cardFooters(t *testing.T, card string) int {
	t.Helper()
	var got struct {
		Body struct {
			Elements []struct {
				Content string
				Text    struct{ Content string }
			}
		}
	}
	if err := json.Unmarshal([]byte(card), &got); err != nil {
		t.Fatalf("card: %v", err)
	}
	n := 0
	for _, e := range got.Body.Elements {
		if strings.Contains(e.Content+e.Text.Content, "完成 · Persona · 用时") {
			n++
		}
	}
	return n
}

// A quiet ending is not always one to report: a message the owner recalled says nothing, with a card or without one.
func TestFeishuRecalledEndWithoutItsCardSaysNothing(t *testing.T) {
	s, _, r, posts := endHub(t)
	s.chans.mu.Lock()
	delete(r.Ext, "card") // recalled before its first progress card
	r.Recalled = true
	s.chans.mu.Unlock()
	*posts = nil
	s.finishChannelRun(r, recalledResult())
	if len(*posts) != 0 {
		t.Fatalf("a recalled message said something: %v", *posts)
	}
}

// A recalled message whose progress card cannot be written any more says nothing too: the ending is not posted as a
// card of its own.
func TestFeishuRecalledEndWithARefusedCardSaysNothing(t *testing.T) {
	s, f, r, posts := endHub(t)
	f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) {
		return nil, errors.New("feishu update card: 230025 card is not a card")
	})
	*posts = nil
	s.finishChannelRun(r, recalledResult())
	if len(*posts) != 0 {
		t.Fatalf("a recalled message said something: %v", *posts)
	}
}

// A quiet ending's progress card can be refused for good (recalled, no longer a card): the ending still has to reach
// the owner, so it goes out as a card of its own, as a normal answer does — and, like the card it replaces, it says how
// the wait ended (#4228) rather than always "Done".
func TestFeishuQuietEndPostsWhenItsCardIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  channel.Result
		want string
	}{
		{"done", channel.Result{Quiet: true}, "完成 · Persona · 用时"},
		{"failed", channel.Result{Quiet: true, End: channel.EndError, Stopped: "API 错误：overloaded"}, "出错了"},
	} {
		s, f, r, posts := endHub(t)
		f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) {
			return nil, errors.New("feishu update card: 230025 card is not a card")
		})
		*posts = nil
		res := tc.res
		s.finishChannelRun(r, &res)
		if run, _ := s.channelRunByID(r.ID); run != nil {
			t.Fatalf("%s: the run did not end", tc.name)
		}
		if len(*posts) != 1 {
			t.Fatalf("%s: the run ended without telling the owner: %v", tc.name, *posts)
		}
		post := (*posts)[0]
		card, _ := post["card"].(string)
		if post["update"] == true || post["reply_to"] != "om_m1" || !strings.Contains(card, tc.want) {
			t.Fatalf("%s: post %v", tc.name, post)
		}
	}
}

// A quiet ending that never got out because the hub could not be reached is not an ending: the run is kept and tried
// again, and the retry posts it under the same key, so one that did arrive lands once.
func TestFeishuQuietEndTriesAgainWhenUnreachable(t *testing.T) {
	s, f, r, posts := endHub(t)
	s.finishChannelRun(r, &channel.Result{Answer: "先答一部分", Held: true}) // it gave its card back
	*posts = nil
	tries := 0
	keys := []string{}
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		tries++
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		key, _ := m["uuid"].(string)
		keys = append(keys, key)
		if tries == 1 {
			return nil, bus.ErrOffline
		}
		*posts = append(*posts, m)
		return map[string]any{"message_id": "om_card"}, nil
	})
	s.finishChannelRun(r, &channel.Result{Quiet: true})
	if run, _ := s.channelRunByID(r.ID); run == nil {
		t.Fatal("the run was marked finished although nothing got out")
	}
	s.finishChannelRun(r, &channel.Result{Quiet: true})
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Fatal("the run did not end after the retry")
	}
	if tries != 2 || len(*posts) != 1 {
		t.Fatalf("tries %d, posts %v", tries, *posts)
	}
	if keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("the ending went out under keys %q, want one key both times", keys)
	}
	if card, _ := (*posts)[0]["card"].(string); !strings.Contains(card, "完成 · Persona · 用时") {
		t.Fatalf("card %s", card)
	}
}

// The same retry applies when the progress card was refused and the card of its own is what could not reach the hub.
func TestFeishuQuietEndTriesAgainWhenItsFallbackIsUnreachable(t *testing.T) {
	s, f, r, posts := endHub(t)
	f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) {
		return nil, errors.New("feishu update card: 230025 card is not a card")
	})
	*posts = nil
	tries := 0
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		tries++
		if tries == 1 {
			return nil, bus.ErrOffline
		}
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		*posts = append(*posts, m)
		return map[string]any{"message_id": "om_card"}, nil
	})
	s.finishChannelRun(r, &channel.Result{Quiet: true})
	if run, _ := s.channelRunByID(r.ID); run == nil {
		t.Fatal("the run was marked finished although nothing got out")
	}
	s.finishChannelRun(r, &channel.Result{Quiet: true})
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Fatal("the run did not end after the retry")
	}
	if tries != 2 || len(*posts) != 1 {
		t.Fatalf("tries %d, posts %v", tries, *posts)
	}
}
