package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// unrelatedAnswerService has a held Lark run and an ordinary main-conversation turn, with both mirror transports
// enabled. The old run may still have a progress card, but it never owns the new question or its answer.
func unrelatedAnswerService(t *testing.T, card bool) (*Service, *fakeHub, *channel.Run, *chanLive) {
	t.Helper()
	s, f := channelPushService(t, true)
	enableChannelPushFeishu(f)
	for _, method := range []string{FeishuReact, FeishuUnreact, FeishuUpdateCard} {
		f.answerApp(AppFeishu, method, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	}
	r, p := cotRun(s)
	s.chans.mu.Lock()
	r.SessionID, r.Held, r.Base, r.HeldAt = "s1", true, 2, time.Now().Add(-time.Minute)
	r.Ext = map[string]string{}
	if card {
		r.Ext["card"] = "om_old_progress"
	}
	p.items, p.steps = nil, nil
	s.chans.mu.Unlock()
	f.setSessions("server", proto.Session{SessionID: "s1", Assistant: true, Status: proto.StatusDone,
		State: proto.StateIdle, LastText: "The new answer"})
	f.setItems(
		proto.Item{ID: "u3", Seq: 3, Role: "user", Kind: proto.KindText, Text: "The new question"},
		proto.Item{ID: "a4", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "The new answer"},
	)
	return s, f, r, p
}

func endUnrelatedAnswerRun(t *testing.T, s *Service, f *fakeHub, r *channel.Run, p *chanLive, via string, card bool) {
	t.Helper()
	ctx := context.Background()
	switch via {
	case "poll":
		s.checkChannelRun(r.ID)
	case "live items":
		s.SessionItems(ctx, SessionItems{MachineID: r.MachineID, SessionID: r.SessionID, Items: []proto.Item{
			{ID: "u3", Seq: 3, Role: "user", Kind: proto.KindText, Text: "The new question"},
			{ID: "a4", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "The new answer"},
		}})
	case "owner send":
		s.endHeldChannelRuns(r.MachineID, r.SessionID)
	default:
		t.Fatalf("unknown ending path %q", via)
	}
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Fatal("the superseded held run is still live")
	}
	select {
	case <-p.done:
	default:
		t.Fatal("the superseded held run did not finish")
	}
	if posts := f.appParams(AppFeishu, FeishuPost); len(posts) != 0 {
		t.Fatalf("the old run posted a new answer or ending card: %v", posts)
	}
	updates := f.appParams(AppFeishu, FeishuUpdateCard)
	want := 0
	if card {
		want = 1
	}
	if len(updates) != want {
		t.Fatalf("old-card updates = %v, want %d", updates, want)
	}
	for _, update := range updates {
		if update["message_id"] != "om_old_progress" || strings.Contains(fmt.Sprint(update), "The new") {
			t.Fatalf("the old card changed routing or exposed the new turn: %v", update)
		}
	}
}

func TestUnrelatedChannelEndKeepsNextTurnMirrors(t *testing.T) {
	needsLarkLayer(t)
	for _, via := range []string{"poll", "live items", "owner send"} {
		for _, card := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/card=%v", via, card), func(t *testing.T) {
				s, f, r, p := unrelatedAnswerService(t, card)
				endUnrelatedAnswerRun(t, s, f, r, p, via, card)
				// No elapsed wait: this is inside the 30-second answered window, through the real completion entry.
				s.channelTurnEnded(context.Background(), r.MachineID, r.SessionID)
				if posts := f.appParams(AppFeishu, FeishuPost); len(posts) != 1 || !strings.Contains(fmt.Sprint(posts), "The new answer") {
					t.Errorf("new turn's Lark mirror = %v, want its answer once", posts)
				}
				if sends := f.appParams(AppChannels, ChannelsSend); len(sends) != 2 || !strings.Contains(fmt.Sprint(sends), "The new answer") {
					t.Errorf("new turn's extension mirrors = %v, want its answer in both channels", sends)
				}
			})
		}
	}
}

// A completion can arrive before either the item event or the polling watch ended the old wait.
func TestUnrelatedChannelEndDiscoveredAtCompletion(t *testing.T) {
	needsLarkLayer(t)
	for _, tc := range []struct {
		name    string
		card    bool
		pending bool
	}{
		{name: "no card"},
		{name: "existing card", card: true},
		{name: "another pending run", card: true, pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, r, p := unrelatedAnswerService(t, tc.card)
			if tc.pending {
				s.chans.mu.Lock()
				s.chans.st.Runs = append(s.chans.st.Runs, &channel.Run{ID: "pending", Channel: "examplechat",
					MachineID: r.MachineID, SessionID: r.SessionID, Base: r.Base, Started: time.Now(), MsgIDs: []string{"not-delivered-yet"}})
				s.chans.live["pending"] = &chanLive{mu: newChanMutex(), checking: newChanMutex(), done: make(chan struct{})}
				s.chans.mu.Unlock()
			}
			s.channelTurnEnded(context.Background(), r.MachineID, r.SessionID)
			if run, _ := s.channelRunByID(r.ID); run != nil {
				t.Fatal("completion did not end the old held run")
			}
			select {
			case <-p.done:
			default:
				t.Fatal("the old held run did not finish")
			}
			wantPosts, wantSends := 1, 2
			if tc.pending {
				wantPosts, wantSends = 0, 0
				if run, _ := s.channelRunByID("pending"); run == nil {
					t.Fatal("completion removed the valid pending run")
				}
			}
			if posts := f.appParams(AppFeishu, FeishuPost); len(posts) != wantPosts || wantPosts > 0 &&
				(!strings.Contains(fmt.Sprint(posts), "The new answer") || posts[0]["reply_to"] == r.ReplyTo) {
				t.Errorf("Lark mirrors after checking ownership = %v, want %d separate new-turn mirrors", posts, wantPosts)
			}
			if sends := f.appParams(AppChannels, ChannelsSend); len(sends) != wantSends || wantSends > 0 && !strings.Contains(fmt.Sprint(sends), "The new answer") {
				t.Errorf("extension mirrors after checking ownership = %v, want %d", sends, wantSends)
			}
			updates := f.appParams(AppFeishu, FeishuUpdateCard)
			wantUpdates := 0
			if tc.card {
				wantUpdates = 1
			}
			if len(updates) != wantUpdates {
				t.Fatalf("old-card updates = %v", updates)
			}
			for _, update := range updates {
				if update["message_id"] != "om_old_progress" || strings.Contains(fmt.Sprint(update), "The new") {
					t.Errorf("old card exposed the new turn: %v", update)
				}
			}
		})
	}
}

func TestChannelCompletionKeepsOtherRunEndings(t *testing.T) {
	needsLarkLayer(t)
	for _, ending := range []string{"answer", "pending", "quiet", "recalled", "timeout"} {
		t.Run(ending, func(t *testing.T) {
			s, f, r, _ := unrelatedAnswerService(t, false)
			s.chans.mu.Lock()
			r.Held = false
			switch ending {
			case "pending":
				r.MsgIDs = []string{"not-delivered-yet"}
			case "quiet":
				r.Held, r.HeldAt = true, time.Now().Add(-channel.HeldIdleMost-time.Minute)
			case "recalled":
				r.Recalled = true
			case "timeout":
				r.Started = time.Now().Add(-channel.RunMost - time.Minute)
			}
			s.chans.mu.Unlock()
			if ending == "quiet" {
				f.setItems()
			}
			s.channelTurnEnded(context.Background(), r.MachineID, r.SessionID)
			wantPosts := 1
			if ending == "pending" || ending == "recalled" {
				wantPosts = 0
			}
			if posts := f.appParams(AppFeishu, FeishuPost); len(posts) != wantPosts {
				t.Errorf("owned ending posted %v, want %d run answers/endings", posts, wantPosts)
			}
			if sends := f.appParams(AppChannels, ChannelsSend); len(sends) != 0 {
				t.Errorf("an owned ending leaked an extra extension mirror: %v", sends)
			}
			if run, _ := s.channelRunByID(r.ID); (run != nil) != (ending == "pending") {
				t.Errorf("ending %s left run %+v", ending, run)
			}
		})
	}
}

func TestUnrelatedChannelEndCompletionRetriesCardCleanup(t *testing.T) {
	needsLarkLayer(t)
	s, f, r, _ := unrelatedAnswerService(t, true)
	f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) { return nil, context.DeadlineExceeded })
	s.channelTurnEnded(context.Background(), r.MachineID, r.SessionID)
	if run, _ := s.channelRunByID(r.ID); run == nil {
		t.Fatal("a failed old-card update did not keep its retry")
	}
	if len(f.appParams(AppFeishu, FeishuPost)) != 0 || len(f.appParams(AppChannels, ChannelsSend)) != 0 {
		t.Fatal("a pending cleanup retry was mirrored")
	}
	f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	s.channelTurnEnded(context.Background(), r.MachineID, r.SessionID)
	if run, _ := s.channelRunByID(r.ID); run != nil {
		t.Fatal("the successful old-card retry did not end its run")
	}
	if len(f.appParams(AppFeishu, FeishuPost)) != 1 || len(f.appParams(AppChannels, ChannelsSend)) != 2 {
		t.Fatal("the completed cleanup retry suppressed the new turn's mirrors")
	}
}

func TestUnrelatedChannelEndPreservesMirrorScope(t *testing.T) {
	for _, scope := range []string{"sync disabled", "health", "ordinary non-main conversation"} {
		t.Run(scope, func(t *testing.T) {
			s, f, r, _ := unrelatedAnswerService(t, true)
			switch scope {
			case "sync disabled":
				f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
					return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_owner"}, nil
				})
				s.chans.mu.Lock()
				for _, id := range []string{"examplechat", "otherchat"} {
					s.channelPrefLocked(id).Features[channel.FeatureSync] = false
				}
				s.chans.mu.Unlock()
			case "health":
				f.setSessions("server", proto.Session{SessionID: "s1", Assistant: true, Status: proto.StatusDone,
					Cwd: "/home/example/.persona/health", LastText: "The new answer"})
			case "ordinary non-main conversation":
				setSettings(t, s, func(v *Settings) { v.Main = "another-main" })
			}
			s.channelTurnEnded(context.Background(), r.MachineID, r.SessionID)
			if posts := f.appParams(AppFeishu, FeishuPost); len(posts) != 0 {
				t.Errorf("Lark mirrored outside its scope: %v", posts)
			}
			if sends := f.appParams(AppChannels, ChannelsSend); len(sends) != 0 {
				t.Errorf("extensions mirrored outside their scope: %v", sends)
			}
		})
	}
}

func TestUnrelatedChannelEndPreservesAnsweredMarkers(t *testing.T) {
	needsLarkLayer(t)
	s, f, r, p := unrelatedAnswerService(t, false)
	now := time.Now()
	prior := now.Add(-time.Minute)
	s.chans.mu.Lock()
	s.chans.now = func() time.Time { return now }
	s.chans.answered = map[[2]string]time.Time{
		{r.MachineID, r.SessionID}:     prior,
		{"server", "other-session"}:    now,
		{"other-machine", r.SessionID}: now,
	}
	s.chans.mu.Unlock()
	endUnrelatedAnswerRun(t, s, f, r, p, "owner send", false)
	s.chans.mu.Lock()
	got := s.chans.answered[[2]string{r.MachineID, r.SessionID}]
	s.chans.mu.Unlock()
	if !got.Equal(prior) {
		t.Errorf("silent end refreshed an earlier answer: got %v, want %v", got, prior)
	}
	ctx := context.Background()
	if s.channelAnswered(ctx, r.MachineID, r.SessionID) {
		t.Error("an expired answer now suppresses the new turn")
	}
	if !s.channelAnswered(ctx, "server", "other-session") || !s.channelAnswered(ctx, "other-machine", r.SessionID) {
		t.Error("ending a held run erased another conversation's real answer")
	}
	// A real answer still contributes the marker and keeps its normal completion/mirror deduplication.
	r, _ = cotRun(s)
	s.finishChannelRun(r, &channel.Result{Answer: "An answer delivered to Lark"})
	if !s.channelAnswered(ctx, r.MachineID, r.SessionID) {
		t.Fatal("a delivered answer lost its suppression marker")
	}
	before := len(f.appParams(AppFeishu, FeishuPost))
	if before != 1 || !strings.Contains(fmt.Sprint(f.appParams(AppFeishu, FeishuPost)), "An answer delivered to Lark") {
		t.Fatal("the control answer did not reach the fake Lark transport")
	}
	s.channelTurnEnded(ctx, r.MachineID, r.SessionID)
	if len(f.appParams(AppFeishu, FeishuPost)) != before || len(f.appParams(AppChannels, ChannelsSend)) != 0 {
		t.Fatal("a delivered answer was mirrored again")
	}
}

func TestUnrelatedChannelEndKeepsCompletionPush(t *testing.T) {
	needsLarkLayer(t)
	for _, delivered := range []bool{false, true} {
		t.Run(fmt.Sprintf("delivered=%v", delivered), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f, r, p := unrelatedAnswerService(t, false)
				d := newFleetDevs()
				d.add("server", "Example computer")
				s.devs = d
				ft := &fleetTest{t: t, s: s, d: d, f: f}
				t.Cleanup(func() {
					if q := s.fleet.q.ch; q != nil {
						close(q)
					}
				})
				f.mu.Lock()
				f.hello.Notify = assistantwire.NotifyTimings{DoneDelayMS: 100, LongTurnMS: 60_000}
				f.mu.Unlock()
				if delivered {
					s.finishChannelRun(r, &channel.Result{Answer: "An answer delivered to Lark"})
					if posts := f.appParams(AppFeishu, FeishuPost); len(posts) != 1 || !strings.Contains(fmt.Sprint(posts), "An answer delivered to Lark") {
						t.Fatalf("the control answer was not delivered: %v", posts)
					}
				} else {
					endUnrelatedAnswerRun(t, s, f, r, p, "owner send", false)
				}
				synctest.Wait() // the real channelsChanged publication carries ChannelsReport to the fleet
				work := proto.Session{SessionID: "s1", Title: "Example task", Agent: "claude", Status: proto.StatusWorking, UpdatedAt: time.Now()}
				ft.event("server", "hook.event", map[string]any{"session_id": "s1", "event": "UserPromptSubmit"})
				ft.sessions("server", work)
				synctest.Wait()
				done := work
				done.Status, done.LastText = proto.StatusDone, "The new answer"
				ft.sessions("server", done)
				synctest.Wait()
				<-time.After(100 * time.Millisecond)
				synctest.Wait()
				got := ft.pushesOf(assistantwire.NotifyDone, 0, 0)
				want := 1
				if delivered {
					want = 0
				}
				if len(got) != want {
					t.Fatalf("completion pushes = %+v, want %d", got, want)
				}
				if want == 1 && (got[0].SessionID != "s1" || got[0].Body != "The new answer") {
					t.Fatalf("wrong turn's completion: %+v", got)
				}
			})
		})
	}
}
