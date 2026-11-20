package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// withWebBot answers the feishu status with publicURL as the hub's web address (feishuHub leaves it empty).
func withWebBot(f *fakeHub, publicURL string) {
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), PublicURL: publicURL,
			Features: FeishuFeatures{Main: true, Cards: true}}, nil
	})
}

// A resolved interaction card carries the conversation's web link: /persona/<id> for a Persona conversation and
// /s/<machine>/<id> for a coding agent's; without a public URL it carries no link at all (#4073).
func TestAnsweredCardOpenButton(t *testing.T) {
	for _, tc := range []struct {
		name    string
		web     string
		session string
		wantURL string // "" means no open_url button
	}{
		{"persona conversation", "https://hub.example.com", "a1", "https://hub.example.com/persona/a1"},
		{"agent session", "https://hub.example.com", "g1", "https://hub.example.com/s/server/g1"},
		{"no web address", "", "a1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := feishuHub("ou_a", time.Unix(100, 0))
			withWebBot(f, tc.web)
			f.setSessions("server",
				proto.Session{SessionID: "a1", MachineID: "server", Assistant: true},
				proto.Session{SessionID: "g1", MachineID: "server", Agent: "claude"})
			s := newTestService(t, f)
			ix := proto.Interaction{ID: "ix_q", MachineID: "server", SessionID: tc.session, Kind: proto.IxQuestion,
				Questions: []proto.Question{{Question: "用哪个？"}}}
			card := s.answeredCard(context.Background(), ix, "已处理").JSON()
			// Over, it keeps no buttons: the conversation's page is a link in its small print.
			if strings.Contains(card, `"tag":"button"`) {
				t.Fatalf("an answered card with a button: %s", card)
			}
			hasLink := strings.Contains(card, "](http")
			if hasLink != (tc.wantURL != "") {
				t.Fatalf("link=%v, want %v: %s", hasLink, tc.wantURL != "", card)
			}
			if tc.wantURL != "" {
				if !strings.Contains(card, "[打开会话]("+tc.wantURL+")") {
					t.Fatalf("card missing %q: %s", tc.wantURL, card)
				}
			}
		})
	}
}

// answerCardsIn collects the cards a finished run posted (FeishuUpdateCard entries left out), in send order.
func answerCardsIn(posts *[]map[string]any) []string {
	var cards []string
	for _, p := range *posts {
		if p["update"] == true {
			continue
		}
		if card, ok := p["card"].(string); ok {
			cards = append(cards, card)
		}
	}
	return cards
}

// A long answer split over several cards carries the web link on its last card only; a one-card answer carries it on
// that one. Without a public URL no card has a link.
func TestFeishuAnswerOpenButtonOnLastPiece(t *testing.T) {
	for _, tc := range []struct {
		name   string
		web    string
		answer string
	}{
		{"long answer with web", "https://hub.example.com", strings.Repeat("第一段的内容很多。\n\n", 700) + strings.Repeat("第二段的内容很多。\n\n", 700) + "最后一句"},
		{"long answer without web", "", strings.Repeat("第一段的内容很多。\n\n", 700) + strings.Repeat("第二段的内容很多。\n\n", 700) + "最后一句"},
		{"short answer with web", "https://hub.example.com", "就一句"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, posts, _ := cotHub(t, false)
			f := s.hub.(*fakeHub)
			withWebBot(f, tc.web)
			f.setSessions("server", proto.Session{SessionID: "c1", MachineID: "server", Assistant: true})
			r, _ := cotRun(s)
			s.finishChannelRun(r, &channel.Result{Answer: tc.answer})
			cards := answerCardsIn(posts)
			if tc.name == "short answer with web" {
				if len(cards) != 1 {
					t.Fatalf("a short answer is %d cards", len(cards))
				}
			} else if len(cards) < 2 {
				t.Fatalf("a long answer is %d cards, want several", len(cards))
			}
			for i, card := range cards {
				hasLink := strings.Contains(card, "open_url")
				wantLink := tc.web != "" && i == len(cards)-1
				if hasLink != wantLink {
					t.Fatalf("card %d of %d: link=%v, want %v", i+1, len(cards), hasLink, wantLink)
				}
			}
			if tc.web != "" && !strings.Contains(cards[len(cards)-1], "https://hub.example.com/persona/c1") {
				t.Fatalf("the last card's link is wrong: %s", cards[len(cards)-1])
			}
		})
	}
}

// /list gives every conversation a link to its own page; without a public URL it only keeps the switch buttons, and
// more than eight conversations still leave the first eight whole and the card within its size budget.
func TestConversationsCardOpenButtons(t *testing.T) {
	t.Run("links per conversation", func(t *testing.T) {
		f, _ := feishuHub("ou_a", time.Unix(100, 0))
		withWebBot(f, "https://hub.example.com")
		s := newTestService(t, f)
		setSettings(t, s, func(v *Settings) { v.MachineID = "server"; v.Main = "main0" })
		now := time.Now().UTC()
		f.setSessions("server",
			proto.Session{SessionID: "main0", MachineID: "server", Assistant: true, Title: "主对话", UpdatedAt: now},
			proto.Session{SessionID: "c1", MachineID: "server", Assistant: true, Title: "修登录", UpdatedAt: now.Add(-time.Minute)})
		card, fallback := s.conversationsCard(context.Background())
		got := card.JSON()
		if n := strings.Count(got, "open_url"); n != 2 {
			t.Fatalf("open_url count=%d, want one button per row: %s", n, got)
		}
		for _, want := range []string{"https://hub.example.com/persona/main0", "https://hub.example.com/persona/c1"} {
			if !strings.Contains(got, want) {
				t.Fatalf("card missing %q: %s", want, got)
			}
		}
		if !strings.Contains(got, `"a":"switch"`) || !strings.Contains(fallback, "修登录") {
			t.Fatalf("the switch button or fallback changed: %s", got)
		}
	})

	t.Run("no web address", func(t *testing.T) {
		f, _ := feishuHub("ou_a", time.Unix(100, 0)) // no PublicURL
		s := newTestService(t, f)
		setSettings(t, s, func(v *Settings) { v.MachineID = "server"; v.Main = "main0" })
		f.setSessions("server", proto.Session{SessionID: "main0", MachineID: "server", Assistant: true, Title: "主对话"})
		card, _ := s.conversationsCard(context.Background())
		got := card.JSON()
		if strings.Contains(got, "open_url") || strings.Contains(got, `"default_url":""`) {
			t.Fatalf("a link without a web address: %s", got)
		}
		if !strings.Contains(got, `"a":"switch"`) {
			t.Fatalf("the switch button is gone: %s", got)
		}
	})

	t.Run("more than eight rows stays within budget", func(t *testing.T) {
		f, _ := feishuHub("ou_a", time.Unix(100, 0))
		withWebBot(f, "https://hub.example.com")
		s := newTestService(t, f)
		setSettings(t, s, func(v *Settings) { v.MachineID = "server"; v.Main = "main0" })
		now := time.Now().UTC()
		var cs []proto.Session
		for i := 0; i < 12; i++ {
			cs = append(cs, proto.Session{SessionID: fmt.Sprintf("c%02d", i), MachineID: "server", Assistant: true,
				Title: longListName(i), LastText: strings.Repeat("很长的最近消息。", 30), UpdatedAt: now.Add(-time.Duration(i) * time.Minute)})
		}
		f.setSessions("server", cs...)
		card, _ := s.conversationsCard(context.Background())
		got := card.JSON()
		if n := strings.Count(got, "open_url"); n != 8 {
			t.Fatalf("open_url count=%d, want exactly the eight rows kept", n)
		}
		if len(got) >= listCardMaxBytes {
			t.Fatalf("/list card %d bytes, want under %d", len(got), listCardMaxBytes)
		}
		if !strings.Contains(got, "/persona/c00") || strings.Contains(got, "/persona/c08") {
			t.Fatalf("the first rows must stay whole and later rows be dropped: %s", got)
		}
	})
}
