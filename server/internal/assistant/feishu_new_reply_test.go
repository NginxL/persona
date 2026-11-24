package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise the public DM entrance, including commands, card decisions, target lookup and Start.
func TestFeishuNewFirstMessageIgnoresOldReply(t *testing.T) {
	for _, kind := range []string{targetPersona, targetAgent} {
		for _, binding := range []string{"parent", "root", "both", "question", "unbound question"} {
			t.Run(kind+"/"+binding, func(t *testing.T) {
				f, _ := feishuHub("ou_owner", time.Unix(100, 0))
				f.settings = &Settings{MachineID: "server", Main: "m1", DefaultPermissionMode: proto.ModeDefault}
				f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
					return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_owner", PairedAt: time.Unix(100, 0), Features: FeishuFeatures{Main: true, Agents: true, Cards: true}}, nil
				})
				old := botTarget{Kind: kind, MachineID: "server", SessionID: "oldchat01"}
				if kind == targetAgent {
					old.MachineID = "box"
					f.sessions["box"] = []proto.Session{{SessionID: "oldchat01"}}
				}
				f.sessions["server"] = []proto.Session{{SessionID: "m1", Assistant: true}, {SessionID: "oldchat01", Assistant: kind == targetPersona}}
				ix := proto.Interaction{ID: "ix_old", MachineID: "server", SessionID: "oldchat01", Kind: proto.IxQuestion,
					Questions: []proto.Question{{Question: "Which option?", Options: []proto.QuestionOption{{Label: "First"}, {Label: "Second"}}}}}
				f.withQueue(ix)
				f.answerApp(AppInteractions, InteractionsResolve, func(json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil })
				empty := func(json.RawMessage) (any, error) { return map[string]any{}, nil }
				for _, m := range []string{FeishuText, FeishuReact, FeishuUnreact, FeishuMessages} {
					f.answerApp(AppFeishu, m, empty)
				}
				f.answerApp(AppFeishu, FeishuMessages, func(json.RawMessage) (any, error) {
					return map[string]any{"items": []map[string]any{{"message_id": "om_old", "msg_type": "text", "sender": map[string]any{"id": "ou_previous", "sender_type": "user"}, "body": map[string]any{"content": `{"text":"Previous synthetic quote"}`}}}}, nil
				})
				f.answerApp(AppFeishu, FeishuUserName, func(json.RawMessage) (any, error) { return map[string]any{"name": "Previous author"}, nil })
				s := newTestService(t, f)
				if _, err := s.feishuStatus(context.Background()); err != nil {
					t.Fatal(err)
				}
				s.setBotCurrent(old)
				s.rememberBotThread("om_old", old, "Previous conversation")
				ev := privateEvent("om_new", "/new 2")
				switch binding {
				case "parent":
					ev.Message.ParentID = "om_old"
				case "root":
					ev.Message.RootID = "om_old"
				case "both", "question":
					ev.Message.ParentID, ev.Message.RootID = "om_old", "om_old"
				}
				if strings.Contains(binding, "question") {
					s.fbot.mu.Lock()
					s.fbot.cards = map[string]botCard{ix.ID: {MessageID: "om_old", Ix: ix, At: time.Now()}}
					s.fbot.mu.Unlock()
				}
				raw, err := json.Marshal(ev)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					if err := s.FeishuMessage(context.Background(), FeishuMessage{Event: raw, Action: "oa_test"}); err != nil {
						t.Fatal(err)
					}
				}
				s.fbot.mu.Lock()
				done := s.fbot.line
				s.fbot.mu.Unlock()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatal("DM did not finish")
				}
				runs := s.channelRuns(channel.Feishu)
				if len(runs) != 1 || runs[0].SessionID == "oldchat01" || runs[0].ReplyTo != "om_new" {
					t.Fatalf("new run: %+v", runs)
				}
				cur := s.botTargetFor("", "")
				if cur.SessionID != runs[0].SessionID || cur.Kind != targetPersona {
					t.Fatalf("new current: %+v", cur)
				}
				s.origins.mu.Lock()
				origin := *s.origins.m[cur.SessionID]
				s.origins.mu.Unlock()
				if origin.Origin != OriginChannel || origin.Ref != channel.Feishu {
					t.Errorf("origin: %+v", origin)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				var starts, oldSends int
				for _, c := range f.calls {
					var p map[string]any
					_ = json.Unmarshal(c.Params, &p)
					if c.Method == DeviceAssistantSend && p["session_id"] == nil {
						starts++
						if p["cwd"] != "/home/me/.persona/assistant" || p["is_main"] != false || p["permission_mode"] != proto.ModeDefault || p["origin"] != OriginChannel {
							t.Errorf("start policy changed: %+v", p)
						}
						if ev.Message.ParentID != "" && !strings.Contains(p["text"].(string), "Previous synthetic quote") {
							t.Error("original quote context lost")
						}
						if c.Device != "server" || !strings.HasSuffix(strings.TrimSpace(p["text"].(string)), "2") || strings.Contains(p["text"].(string), "/new") {
							t.Errorf("new first message: %s %+v", c.Device, p)
						}
					}
					if c.Method == DeviceSessionSendOnce || (c.Method == DeviceAssistantSend && p["session_id"] == "oldchat01") {
						oldSends++
					}
				}
				if starts != 1 || oldSends != 0 {
					t.Fatalf("new starts = %d, old sends = %d; want 1, 0", starts, oldSends)
				}
				for _, c := range f.appCalls {
					if c.AppID == AppInteractions && c.Method == InteractionsResolve {
						t.Fatalf("old question resolved: %s", c.Params)
					}
				}
				if runs := s.channelRuns(channel.Feishu); len(runs) != 1 || runs[0].SessionID == "oldchat01" || runs[0].ReplyTo != "om_new" {
					t.Fatalf("new run: %+v", runs)
				}
			})
		}
	}
}

// Wait on the entrance's own turn barrier instead of sleeping or calling the private handler.
func deliverNewReplyDM(t *testing.T, s *Service, id, text, parent, root, action string) {
	t.Helper()
	ev := privateEvent(id, text)
	ev.Message.ParentID, ev.Message.RootID = parent, root
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FeishuMessage(context.Background(), FeishuMessage{Event: raw, Action: action}); err != nil {
		t.Fatal(err)
	}
	s.fbot.mu.Lock()
	done := s.fbot.line
	s.fbot.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("DM did not finish")
	}
}

func newCommandReplyBot(t *testing.T, features FeishuFeatures) (*Service, *fakeHub) {
	t.Helper()
	f, _ := feishuHub("ou_owner", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1", DefaultPermissionMode: proto.ModeDefault}
	f.sessions["server"] = []proto.Session{{SessionID: "m1", Assistant: true}, {SessionID: "oldchat01", Assistant: true}}
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_owner", PairedAt: time.Unix(100, 0), Features: features}, nil
	})
	for _, m := range []string{FeishuText, FeishuReact, FeishuUnreact, FeishuMessages} {
		f.answerApp(AppFeishu, m, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	}
	s := newTestService(t, f)
	if _, err := s.feishuStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := botTarget{Kind: targetPersona, MachineID: "server", SessionID: "oldchat01"}
	s.setBotCurrent(old)
	s.rememberBotThread("om_old", old, "Previous conversation")
	return s, f
}

func TestFeishuNewBareCommandDefersStart(t *testing.T) {
	s, f := newCommandReplyBot(t, FeishuFeatures{Main: true, Cards: true})
	deliverNewReplyDM(t, s, "om_bare", "/new \n", "om_old", "om_old", "oa_test")
	f.mu.Lock()
	starts := len(f.sends)
	f.mu.Unlock()
	if starts != 0 || len(handoffs(f)) != 0 || len(f.appParams(AppFeishu, FeishuText)) != 1 {
		t.Fatal("bare /new did not only confirm")
	}
	if got := s.botTargetFor("", ""); got.Kind != targetPersona || got.SessionID != "" {
		t.Fatalf("next unbound message target: %+v", got)
	}
	if got := s.botTargetFor("om_old", "om_old"); got.SessionID != "oldchat01" {
		t.Fatalf("bare /new erased the old binding: %+v", got)
	}
	deliverNewReplyDM(t, s, "om_followup", "First request", "", "", "oa_test")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) != 1 || f.made != 1 || !strings.HasSuffix(strings.TrimSpace(f.sends[0]["text"].(string)), "First request") {
		t.Fatalf("next unbound message: made %d, sends %+v", f.made, f.sends)
	}
}

func TestFeishuNewReplyControls(t *testing.T) {
	for _, c := range []struct {
		name, text string
		disabled   bool
		wantSends  int
	}{
		{"ordinary reply", "Continue here", false, 1},
		{"unknown command", "/compact", false, 1},
		{"empty input", "", false, 0},
		{"disabled chat", "/new First request", true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := newCommandReplyBot(t, FeishuFeatures{Main: !c.disabled, Agents: true, Cards: true})
			deliverNewReplyDM(t, s, "om_control", c.text, "om_old", "om_old", "oa_test")
			got := handoffs(f)
			if len(got) != c.wantSends || len(got) > 0 && (got[0].session != "oldchat01" || !strings.HasSuffix(strings.TrimSpace(got[0].text), c.text)) {
				t.Fatalf("ordinary routing: %+v", got)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.sends) != 0 || f.made != 0 {
				t.Fatalf("control started a conversation: %+v", f.sends)
			}
			if got := s.botTargetFor("", ""); got.SessionID != "oldchat01" {
				t.Fatalf("control changed current: %+v", got)
			}
		})
	}
}

func TestFeishuNewReplyCommandSpellings(t *testing.T) {
	for _, text := range []string{"/NEW\tFirst\n\n  second", "／new\u3000First\n\n  second", "/新对话 First\n\n  second"} {
		t.Run(text, func(t *testing.T) {
			s, f := newCommandReplyBot(t, FeishuFeatures{Main: true})
			deliverNewReplyDM(t, s, "om_spelling", text, "om_old", "om_old", "oa_test")
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.sends) != 1 || f.made != 1 || !strings.HasSuffix(strings.TrimSpace(f.sends[0]["text"].(string)), "First\n\n  second") {
				t.Fatalf("first message: made %d, sends %+v", f.made, f.sends)
			}
		})
	}
}

func TestFeishuNewOrdinaryCardReplyStillAnswers(t *testing.T) {
	s, f := newCommandReplyBot(t, FeishuFeatures{Main: true, Cards: true})
	ix := proto.Interaction{ID: "ix_control", MachineID: "server", SessionID: "oldchat01", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "Which option?", Options: []proto.QuestionOption{{Label: "First"}, {Label: "Second"}}}}}
	for _, m := range []string{InteractionsGet, InteractionsResolve} {
		f.answerApp(AppInteractions, m, func(json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil })
	}
	s.fbot.mu.Lock()
	s.fbot.cards = map[string]botCard{ix.ID: {MessageID: "om_old", Ix: ix, At: time.Now()}}
	s.fbot.mu.Unlock()
	deliverNewReplyDM(t, s, "om_answer", "2", "om_old", "om_old", "oa_test")
	resolves := f.appParams(AppInteractions, InteractionsResolve)
	if len(resolves) != 1 || resolves[0]["interaction_id"] != ix.ID {
		t.Fatalf("ordinary card reply did not resolve once: %+v", resolves)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) != 0 || f.made != 0 {
		t.Fatalf("ordinary card reply started a conversation: %+v", f.sends)
	}
}
