package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// queuedRunHub builds a channel run whose own message never reached the conversation: the transcript
// only holds the previous turn (its question and its closing words). status is the conversation's
// status at check time; the run was seen working and has waited past QueuedMost.
func queuedRunHub(t *testing.T, status string) (*Service, *[]map[string]any, *channel.Run) {
	t.Helper()
	f := newFakeHub().withMachine()
	h := &pagedHub{fakeHub: f, page: 4, full: map[string]string{}, items: []proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "上一轮的问题"},
		{ID: "a2", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "上一轮的收尾话"},
	}}
	var sent []map[string]any
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
			Installed: true, State: channel.StateConnected, Capabilities: channel.Capabilities{MaxText: 200}}}}, nil
	})
	f.answerApp(AppChannels, ChannelsSend, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		sent = append(sent, m)
		return map[string]any{}, nil
	})
	s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return &memStore{} }, Logf: testLogf(t)}))
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	s.chans.now = func() time.Time { return now }
	f.setSessions("server", proto.Session{SessionID: "s1", MachineID: "server", Status: status})
	r := &channel.Run{ID: "examplechat_m9", Channel: "examplechat", MachineID: "server", SessionID: "s1", ReplyTo: "m9",
		MsgIDs: []string{"wechat-9"}, Probe: "全新的排队问题", Started: now.Add(-channel.QueuedMost - time.Minute), Seen: true}
	prog := &chanLive{mu: newChanMutex(), checking: newChanMutex(), done: make(chan struct{})}
	s.chans.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	s.chans.st.Runs = append(s.chans.st.Runs, r)
	s.chans.live = map[string]*chanLive{r.ID: prog}
	s.chans.mu.Unlock()
	return s, &sent, r
}

// The previous turn ended leaving its closing words, and the queued message still never showed up
// past QueuedMost: the channel gets a neutral "please resend" note, never the previous turn's text.
func TestCheckChannelRunQueuedPastMostAsksToResend(t *testing.T) {
	s, sent, r := queuedRunHub(t, "")
	s.checkChannelRun(r.ID)
	if len(*sent) != 1 {
		t.Fatalf("got %d messages sent, want 1: %v", len(*sent), *sent)
	}
	text, _ := (*sent)[0]["text"].(string)
	if strings.Contains(text, "上一轮的收尾话") {
		t.Fatalf("the previous turn's words were answered with: %q", text)
	}
	if !strings.Contains(text, i18n.T("", "assistant-channels_runs.not_delivered")) {
		t.Fatalf("the channel got %q, want the resend note", text)
	}
	if n := len(s.channelRuns("examplechat")); n != 0 {
		t.Fatalf("%d runs left after the give-up; want 0", n)
	}
}

// While the conversation is still working the run waits on: a not-found read never ends it.
func TestCheckChannelRunQueuedWhileWorkingKeepsWaiting(t *testing.T) {
	s, sent, r := queuedRunHub(t, proto.StatusWorking)
	s.checkChannelRun(r.ID)
	if len(*sent) != 0 {
		t.Fatalf("a working conversation answered the channel: %v", *sent)
	}
	if n := len(s.channelRuns("examplechat")); n != 1 {
		t.Fatalf("%d runs left; want the run still waiting", n)
	}
}

// heldRunHub builds a run that already answered its channel message and was held with background work still running.
// Its base is the original answer; continuation is the next turn after that answer. The session's WokeByTask and
// Background fields reflect what the hub reports for that next turn.
func heldRunHub(t *testing.T, continuation []proto.Item, background int, pageSize ...int) (*Service, *[]map[string]any, *channel.Run) {
	t.Helper()
	page := 4
	if len(pageSize) > 0 {
		page = pageSize[0]
	}
	f := newFakeHub().withMachine()
	items := append([]proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "original channel question", ClientMsgID: "examplechat-m9"},
		{ID: "a2", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "original channel answer"},
	}, continuation...)
	h := &pagedHub{fakeHub: f, page: page, full: map[string]string{}, items: items}
	var sent []map[string]any
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
			Installed: true, State: channel.StateConnected, Capabilities: channel.Capabilities{MaxText: 200}}}}, nil
	})
	f.answerApp(AppChannels, ChannelsSend, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		sent = append(sent, m)
		return map[string]any{}, nil
	})
	s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return &memStore{} }, Logf: testLogf(t)}))
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	s.chans.now = func() time.Time { return now }
	sess := proto.Session{SessionID: "s1", MachineID: "server", Status: proto.StatusDone,
		WokeByTask: len(continuation) > 0 && continuation[0].Wake}
	for range background {
		sess.Background = append(sess.Background, proto.BackgroundTask{ID: "background-work", Kind: "shell"})
	}
	f.setSessions("server", sess)
	r := &channel.Run{ID: "examplechat_m9", Channel: "examplechat", MachineID: "server", SessionID: "s1", ReplyTo: "m9",
		Base: 2, Started: now.Add(-time.Minute), Held: true}
	prog := &chanLive{mu: newChanMutex(), checking: newChanMutex(), done: make(chan struct{})}
	s.chans.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	s.chans.st.Runs = append(s.chans.st.Runs, r)
	s.chans.live = map[string]*chanLive{r.ID: prog}
	s.chans.mu.Unlock()
	return s, &sent, r
}

func assertHeldRunFinishedQuietly(t *testing.T, s *Service, sent *[]map[string]any) {
	t.Helper()
	if len(*sent) != 0 {
		t.Errorf("an unrelated user turn sent its answer to the original channel: %v", *sent)
	}
	if runs := s.channelRuns("examplechat"); len(runs) != 0 {
		t.Errorf("the unrelated turn left %d held runs, want the run removed so it cannot claim later turns: %+v", len(runs), runs)
	}
}

// An unnamed held run must not treat an ordinary user turn from the app (or an older client without a client_msg_id)
// as its background continuation, even though other background work is still running.
func TestCheckChannelRunHeldIgnoresUnrelatedUserTurn(t *testing.T) {
	for _, tc := range []struct {
		name        string
		clientMsgID string
	}{
		{name: "Persona app", clientMsgID: "3f2a9c1e-1111-4222-8333-444455556666"},
		{name: "no client message id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sent, r := heldRunHub(t, []proto.Item{
				{ID: "u3", Seq: 3, Role: "user", Kind: proto.KindText, Text: "an unrelated question from another surface", ClientMsgID: tc.clientMsgID},
				{ID: "a4", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "the unrelated private answer"},
			}, 1)
			s.checkChannelRun(r.ID)
			assertHeldRunFinishedQuietly(t, s, sent)
		})
	}
}

// Restoration preserves the same unnamed Held shape; the unrelated-turn guard applies after that JSON round-trip too.
func TestCheckChannelRunRestoredHeldIgnoresUnrelatedUserTurn(t *testing.T) {
	s, sent, r := heldRunHub(t, []proto.Item{
		{ID: "u3", Seq: 3, Role: "user", Kind: proto.KindText, Text: "an unrelated question after a restart"},
		{ID: "a4", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "the unrelated private answer"},
	}, 1)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	restored := new(channel.Run)
	if err := json.Unmarshal(raw, restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Held || restored.Base != 2 {
		t.Fatalf("the persisted run did not restore as held: %+v", restored)
	}
	s.chans.mu.Lock()
	for i, existing := range s.chans.st.Runs {
		if existing == r {
			s.chans.st.Runs[i] = restored
		}
	}
	s.chans.mu.Unlock()
	s.checkChannelRun(restored.ID)
	assertHeldRunFinishedQuietly(t, s, sent)
}

// A real task notification is a wake notice, not user text. Its continuation is still read and answered: final when
// no background work remains, or sent with the background note and held again when more work remains.
func TestCheckChannelRunHeldAnswersBackgroundContinuation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		background int
	}{
		{name: "final"},
		{name: "still held", background: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sent, r := heldRunHub(t, []proto.Item{
				{ID: "n3", Seq: 3, Role: "user", Kind: proto.KindNotice, Text: "Background command finished", Wake: true,
					Task: &proto.TaskNote{Kind: "shell", TaskID: "background-work", ToolUseID: "call-1", Status: "completed"}},
				{ID: "a4", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "the background continuation answer"},
			}, tc.background)
			s.checkChannelRun(r.ID)
			if len(*sent) != 1 {
				t.Fatalf("got %d channel sends, want the continuation answer: %v", len(*sent), *sent)
			}
			wantText := "the background continuation answer"
			if tc.background > 0 {
				wantText += "\n\n" + i18n.T("", "channel-turns.background", i18n.Args{"count": tc.background})
			}
			if text, _ := (*sent)[0]["text"].(string); text != wantText || (*sent)[0]["reply_to"] != "m9" {
				t.Fatalf("continuation send = %v, want text %q replying to m9", (*sent)[0], wantText)
			}
			runs := s.channelRuns("examplechat")
			if tc.background == 0 {
				if len(runs) != 0 {
					t.Fatalf("final continuation left runs behind: %+v", runs)
				}
			} else if len(runs) != 1 || !runs[0].Held || runs[0].Base != 4 {
				t.Fatalf("continuation with background work was not held from the new answer: %+v", runs)
			}
		})
	}
}

// A genuine continuation followed by another user turn before one check must not supply the later private answer.
func TestCheckChannelRunHeldIgnoresUserTurnAfterWake(t *testing.T) {
	s, sent, r := heldRunHub(t, []proto.Item{
		{ID: "n3", Seq: 3, Role: "user", Kind: proto.KindNotice, Text: "Background command finished", Wake: true,
			Task: &proto.TaskNote{Kind: "shell", TaskID: "background-work", Status: "completed"}},
		{ID: "a4", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "the background continuation answer"},
		{ID: "u5", Seq: 5, Role: "user", Kind: proto.KindText, Text: "an unrelated private question"},
		{ID: "a6", Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "the unrelated private answer"},
	}, 1)
	s.checkChannelRun(r.ID)
	assertHeldRunFinishedQuietly(t, s, sent)
}

// The unrelated user item remains decisive even when a long turn pushes it out of ReadTurn's retained item buffer.
func TestCheckChannelRunHeldIgnoresUserTurnBeyondReadBuffer(t *testing.T) {
	items := []proto.Item{{ID: "u3", Seq: 3, Role: "user", Kind: proto.KindText, Text: "an unrelated private question"}}
	for seq := 4; seq <= 1005; seq++ {
		items = append(items, proto.Item{Seq: seq, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash"})
	}
	items = append(items, proto.Item{ID: "a1006", Seq: 1006, Role: "assistant", Kind: proto.KindText, Text: "the unrelated private answer"})
	s, sent, r := heldRunHub(t, items, 1, 2000)
	s.checkChannelRun(r.ID)
	assertHeldRunFinishedQuietly(t, s, sent)
}

// Preparing an owner's own send ends the held run before any live progress can belong to that new turn. The manager's
// own answer-again message takes the other path and leaves the run waiting.
func TestSendParamsEndsHeldRunBeforeOwnerSend(t *testing.T) {
	s, sent, r := heldRunHub(t, nil, 1)
	_, err := s.SendParams(context.Background(), SendParams{MachineID: r.MachineID, SessionID: r.SessionID,
		Params: map[string]any{"text": "an unrelated owner question"}})
	if err != nil {
		t.Fatal(err)
	}
	assertHeldRunFinishedQuietly(t, s, sent)

	s, _, r = heldRunHub(t, nil, 1)
	_, err = s.messageParams(context.Background(), SendParams{MachineID: r.MachineID, SessionID: r.SessionID,
		Params: map[string]any{"text": "write out this turn's answer"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if runs := s.channelRuns("examplechat"); len(runs) != 1 || !runs[0].Held {
		t.Fatalf("the manager's own message ended its held run: %+v", runs)
	}
}

// Tool-result images use the user role but belong to the background continuation. Both live
// session.items and polling (including restored held state) must retain and route its answer.
func TestHeldToolImageContinuation(t *testing.T) {
	for _, mode := range []string{"poll", "live", "restored live"} {
		for _, background := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/background=%d", mode, background), func(t *testing.T) {
				items := []proto.Item{
					{ID: "n3", Seq: 3, Role: "user", Kind: proto.KindNotice, Wake: true, Text: "Background command finished"},
					{ID: "t4", Seq: 4, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "read-1", ToolName: "Read"},
					{ID: "i5", Seq: 5, Role: "user", Kind: proto.KindImage, ToolUseID: "read-1"},
					{ID: "a6", Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "The screenshot confirms the result."},
				}
				s, sent, r := heldRunHub(t, items, background, 1)
				if mode == "restored live" {
					raw, err := json.Marshal(r)
					if err != nil {
						t.Fatal(err)
					}
					restored := new(channel.Run)
					if err := json.Unmarshal(raw, restored); err != nil {
						t.Fatal(err)
					}
					s.chans.mu.Lock()
					s.chans.st.Runs[0] = restored
					s.chans.mu.Unlock()
					r = restored
				}
				if mode != "poll" {
					s.SessionItems(context.Background(), SessionItems{MachineID: r.MachineID, SessionID: r.SessionID, Items: items[:3]})
					if runs := s.channelRuns("examplechat"); len(runs) != 1 || !runs[0].Held {
						t.Fatalf("tool image ended the live held run: %+v", runs)
					}
				}
				s.checkChannelRun(r.ID)
				if len(*sent) != 1 {
					t.Fatalf("sends = %v, want one continuation answer", *sent)
				}
				want := "The screenshot confirms the result."
				if background > 0 {
					want += "\n\n" + i18n.T("", "channel-turns.background", i18n.Args{"count": background})
				}
				if (*sent)[0]["text"] != want || (*sent)[0]["reply_to"] != "m9" {
					t.Fatalf("continuation routed incorrectly: %v", *sent)
				}
				runs := s.channelRuns("examplechat")
				if background == 0 && len(runs) != 0 || background > 0 && (len(runs) != 1 || !runs[0].Held || runs[0].Base != 6) {
					t.Fatalf("unexpected final/held state: %+v", runs)
				}
				s.checkChannelRun(r.ID)
				if len(*sent) != 1 {
					t.Fatalf("repeated check duplicated answer: %v", *sent)
				}
			})
		}
	}
}

func TestHeldToolImageThenLiveOwnerInput(t *testing.T) {
	for _, kind := range []string{proto.KindText, proto.KindImage} {
		t.Run(kind, func(t *testing.T) {
			s, sent, r := heldRunHub(t, nil, 1)
			s.SessionItems(context.Background(), SessionItems{MachineID: r.MachineID, SessionID: r.SessionID, Items: []proto.Item{
				{ID: "i3", Seq: 3, Role: "user", Kind: proto.KindImage, ToolUseID: "read-1"},
				{ID: "u4", Seq: 4, Role: "user", Kind: kind, Text: "An unrelated question"},
				{ID: "a5", Seq: 5, Role: "assistant", Kind: proto.KindText, Text: "A private answer."},
			}})
			assertHeldRunFinishedQuietly(t, s, sent)
		})
	}
}

// A repeated live image notification can race the polling completion. It must neither
// supersede the run nor produce another final answer.
func TestHeldToolImageConcurrentCompletion(t *testing.T) {
	items := []proto.Item{
		{ID: "i3", Seq: 3, Role: "user", Kind: proto.KindImage, ToolUseID: "read-1"},
		{ID: "a4", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "The screenshot confirms the result."},
	}
	s, sent, r := heldRunHub(t, items, 0)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			s.SessionItems(context.Background(), SessionItems{MachineID: r.MachineID, SessionID: r.SessionID, Items: items[:1]})
		})
		wg.Go(func() { <-start; s.checkChannelRun(r.ID) })
	}
	close(start)
	wg.Wait()
	if len(*sent) != 1 || (*sent)[0]["text"] != "The screenshot confirms the result." || (*sent)[0]["reply_to"] != "m9" {
		t.Fatalf("concurrent completion sent %v, want one routed answer", *sent)
	}
	if runs := s.channelRuns("examplechat"); len(runs) != 0 {
		t.Fatalf("completed run retained: %+v", runs)
	}
}

func TestHeldToolImageAnswerRetriesFailedDelivery(t *testing.T) {
	s, sent, r := heldRunHub(t, []proto.Item{
		{ID: "i3", Seq: 3, Role: "user", Kind: proto.KindImage, ToolUseID: "read-1"},
		{ID: "a4", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "The screenshot confirms the result."},
	}, 0)
	f := s.hub.(*pagedHub).fakeHub
	attempts := 0
	f.answerApp(AppChannels, ChannelsSend, func(p json.RawMessage) (any, error) {
		attempts++
		if attempts == 1 {
			return nil, fmt.Errorf("synthetic send failure")
		}
		var m map[string]any
		if err := json.Unmarshal(p, &m); err != nil {
			return nil, err
		}
		*sent = append(*sent, m)
		return map[string]any{}, nil
	})
	s.checkChannelRun(r.ID)
	if len(*sent) != 0 || len(s.channelRuns("examplechat")) != 1 {
		t.Fatalf("failed delivery lost the held run or sent an answer: %v", *sent)
	}
	s.checkChannelRun(r.ID)
	s.checkChannelRun(r.ID)
	if attempts != 2 || len(*sent) != 1 || (*sent)[0]["text"] != "The screenshot confirms the result." || (*sent)[0]["reply_to"] != "m9" {
		t.Fatalf("delivery retries = %d attempts, sent %v", attempts, *sent)
	}
	if runs := s.channelRuns("examplechat"); len(runs) != 0 {
		t.Fatalf("delivered run retained: %+v", runs)
	}
}
