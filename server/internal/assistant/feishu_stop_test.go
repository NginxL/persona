package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// stopCardHub is a paired bot on the assistant's machine "server", which holds conversation conv0001; its status
// reports features.
func stopCardHub(t *testing.T, features FeishuFeatures) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.setSessions("server", proto.Session{SessionID: "conv0001", MachineID: "server", Assistant: true, Title: "修 bug"})
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), Features: features}, nil
	})
	f.answerApp(AppFeishu, FeishuText, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID = "server"; v.Main = "main0000" })
	return s, f
}

func stopCardAction(t botTarget) *botcard.CardAction {
	var a botcard.CardAction
	a.Action.Value = map[string]any{"a": "stop", "k": t.Kind, "m": t.MachineID, "s": t.SessionID}
	return &a
}

// A run's progress card carries the Stop button with the run's own conversation, in the card's language; a card with
// no live run carries no button (the answer and idle cards never do).
func TestProgressCardStopButton(t *testing.T) {
	target := botTarget{Kind: targetPersona, MachineID: "server", SessionID: "conv0001"}
	b, _ := json.Marshal(progressCard("", "Persona", time.Second, nil, "", "", liveView{}, target))
	card := string(b)
	for _, want := range []string{`"a":"stop"`, `"k":"persona"`, `"m":"server"`, `"s":"conv0001"`, `"type":"danger"`, "停止"} {
		if !strings.Contains(card, want) {
			t.Fatalf("running card missing %q: %s", want, card)
		}
	}
	b, _ = json.Marshal(progressCard(i18n.En, "Persona", time.Second, nil, "", "", liveView{}, target))
	if en := string(b); !strings.Contains(en, "Stop") || strings.Contains(en, "停止") {
		t.Fatalf("English card button not translated: %s", en)
	}
	b, _ = json.Marshal(progressCard("", "Persona", time.Second, nil, "", "", liveView{}, botTarget{}))
	if strings.Contains(string(b), `"a":"stop"`) {
		t.Fatalf("a card with no live run carries a stop button: %s", b)
	}
}

// Pressing the progress card's Stop button interrupts the same conversation with the same device call as /stop in
// that conversation's thread: both use the same interrupt call after their own checks.
func TestStopCardPressMakesTheSameInterruptCallAsStopCommand(t *testing.T) {
	s, f := stopCardHub(t, FeishuFeatures{Main: true})
	ctx := context.Background()
	target := botTarget{Kind: targetPersona, MachineID: "server", SessionID: "conv0001"}

	r := s.botCardAction(ctx, currentStopCard(s, target), "")
	want := i18n.T("", "assistant-feishu_cards.stopped", i18n.Args{"label": s.targetLabel(ctx, target)})
	if r.Toast == nil || r.Toast.Type != "success" || r.Toast.Content != want {
		t.Fatalf("press toast %+v, want success %q", r.Toast, want)
	}
	if got := f.interrupted(); len(got) != 1 || got[0] != "server/conv0001" {
		t.Fatalf("press interrupted %v, want [server/conv0001]", got)
	}

	s.rememberBotThread("om_topic", target, "修 bug")
	if h, _ := s.botCommand(ctx, FeishuFeatures{Main: true}, "om", "/stop", "om_topic", ""); !h {
		t.Fatal("/stop was not handled")
	}
	got := f.interrupted()
	if len(got) != 2 || got[0] != got[1] {
		t.Fatalf("interrupts %v, want two identical calls", got)
	}
	var calls []DeviceCall
	for _, c := range f.calls {
		if c.Method == "session.interrupt" {
			calls = append(calls, c)
		}
	}
	if len(calls) != 2 || calls[0].Device != calls[1].Device || string(calls[0].Params) != string(calls[1].Params) {
		t.Fatalf("button call %+v != command call %+v", calls[0], calls[1])
	}
}

// When the node cannot stop the turn, the press answers with an error toast that carries the reason, rather than
// staying silent.
func TestStopCardPressInterruptFailureSaysWhy(t *testing.T) {
	s, f := stopCardHub(t, FeishuFeatures{Main: true})
	f.failInterrupt = errors.New("node is down")
	ctx := context.Background()
	target := botTarget{Kind: targetPersona, MachineID: "server", SessionID: "conv0001"}

	r := s.botCardAction(ctx, currentStopCard(s, target), "")
	want := i18n.T("", "assistant-feishu_cards.not_stopped", i18n.Args{"error": "node is down"})
	if r.Toast == nil || r.Toast.Type != "error" || r.Toast.Content != want {
		t.Fatalf("toast %+v, want error %q", r.Toast, want)
	}
}

// With nothing to stop, the press answers with an explicit toast and interrupts nothing: the target conversation is
// gone (an agent no longer listed), no conversation exists yet, or the Agents feature gates agent targets exactly as
// it does for /stop.
func TestStopCardPressWithNothingToStopSaysSo(t *testing.T) {
	feats := FeishuFeatures{Main: true, Agents: true}
	f := newFakeHub().withMachine()
	f.setSessions("server")
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), Features: feats}, nil
	})
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID = "server" })
	ctx := context.Background()

	for _, c := range []struct {
		name      string
		target    botTarget
		agents    bool // the Agents feature on this press
		wantToast string
		wantKind  string
	}{
		{"agent is gone", botTarget{Kind: targetAgent, MachineID: "box", SessionID: "agone01"}, true,
			i18n.T("", "assistant-feishu_cards.card_expired"), "info"},
		{"no conversation yet", botTarget{Kind: targetPersona}, true,
			i18n.T("", "assistant-feishu_cards.card_expired"), "info"},
		{"agents feature off", botTarget{Kind: targetAgent, MachineID: "box", SessionID: "agone01"}, false,
			i18n.T("", "assistant-feishu.agents_off"), "error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			feats.Agents = c.agents
			before := len(f.interrupted())
			r := s.botCardAction(ctx, stopCardAction(c.target), "")
			if r.Toast == nil || r.Toast.Type != c.wantKind || r.Toast.Content != c.wantToast {
				t.Fatalf("toast %+v, want %s %q", r.Toast, c.wantKind, c.wantToast)
			}
			if got := f.interrupted()[before:]; len(got) != 0 {
				t.Fatalf("interrupted %v, want none", got)
			}
		})
	}
}

// A conversation whose last turn is over has nothing to stop: /stop and the progress card's Stop button answer that
// nothing is running and send no interrupt, which for an ordinary agent presses Escape in its terminal (a keystroke in
// an idle window) and tells the owner a turn was stopped that had already finished. One on a turn, waiting on the owner
// or just being opened still stops, and so does one whose state cannot be told at all, or that this chat just sent a
// message into that was not seen working yet (its state may just not have caught up): the owner asked, so it is tried.
func TestStopWithNoTurnRunningSaysSo(t *testing.T) {
	for _, c := range []struct {
		name string
		sess proto.Session
		sent bool // this chat sent a message into it that was not seen working yet
		runs bool // there is a turn to stop
	}{
		{"finished, result unseen", proto.Session{State: proto.StateUnread}, false, false},
		{"finished, result seen", proto.Session{State: proto.StateIdle}, false, false},
		{"finished, this chat's message not seen working yet", proto.Session{State: proto.StateIdle}, true, true},
		{"on a turn", proto.Session{State: proto.StateWorking, Status: proto.StatusWorking}, false, true},
		{"waiting on the owner", proto.Session{State: proto.StateNeedsYou, Status: proto.StatusBlocked}, false, true},
		{"being opened", proto.Session{State: proto.StateIdle, Starting: true}, false, true},
		{"state cannot be told", proto.Session{}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := stopCardHub(t, FeishuFeatures{Main: true})
			var notes []string
			f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
				var m map[string]any
				_ = json.Unmarshal(p, &m)
				if text, ok := m["text"].(string); ok {
					notes = append(notes, text)
				}
				return map[string]any{}, nil
			})
			sess := c.sess
			sess.SessionID, sess.MachineID, sess.Assistant = "main0000", "server", true
			f.setSessions("server", sess)
			setSettings(t, s, func(v *Settings) { v.MachineID = "server"; v.Main = "main0000" })
			if c.sent {
				s.chans.st.Runs = append(s.chans.st.Runs, &channel.Run{ID: "fb_om", Channel: channel.Feishu,
					MachineID: "server", SessionID: "main0000", Started: time.Now().UTC()})
			}
			ctx := context.Background()
			target := botTarget{Kind: targetPersona, MachineID: "server", SessionID: "main0000"}
			stopped := i18n.T("", "assistant-feishu_cards.stopped", i18n.Args{"label": "主对话"})
			none := i18n.T("", "assistant-feishu_cards.nothing_running")

			if h, _ := s.botCommand(ctx, FeishuFeatures{Main: true}, "om", "/stop", "", ""); !h {
				t.Fatal("/stop was not handled")
			}
			a := currentStopCard(s, target)
			if !c.sent {
				s.chans.st.Runs[len(s.chans.st.Runs)-1].Seen = true
			}
			r := s.botCardAction(ctx, a, "")
			want := map[bool]int{true: 2, false: 0}[c.runs] // /stop and the press, each with its own call
			if got := f.interrupted(); len(got) != want {
				t.Fatalf("interrupts %v, want %d", got, want)
			}
			wantNote, wantToast, wantKind := stopped, stopped, "success"
			if !c.runs {
				wantNote, wantToast, wantKind = none, i18n.T("", "assistant-feishu_cards.card_expired"), "info"
			}
			if len(notes) != 1 || notes[0] != wantNote {
				t.Fatalf("/stop answered %v, want [%q]", notes, wantNote)
			}
			if r.Toast == nil || r.Toast.Content != wantToast || r.Toast.Type != wantKind {
				t.Fatalf("press toast %+v, want %s %q", r.Toast, wantKind, wantToast)
			}
		})
	}
}
