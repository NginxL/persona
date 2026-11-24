package assistant

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// feishuCardsOn has ft's Feishu bot take every card, and records the cards it posts and updates (in order).
func feishuCardsOn(ft *fleetTest) func() (posts int, updates []string) {
	var mu sync.Mutex
	posts := 0
	var updates []string
	ft.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0),
			Features: FeishuFeatures{Main: true, Cards: true, AllCards: true}}, nil
	})
	ft.f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		mu.Lock()
		posts++
		mu.Unlock()
		return map[string]any{"message_id": "om_card"}, nil
	})
	ft.f.answerApp(AppFeishu, FeishuUpdateCard, func(p json.RawMessage) (any, error) {
		var m struct {
			Card string `json:"card"`
		}
		_ = json.Unmarshal(p, &m)
		mu.Lock()
		updates = append(updates, m.Card)
		mu.Unlock()
		return map[string]any{}, nil
	})
	return func() (int, []string) {
		mu.Lock()
		defer mu.Unlock()
		return posts, append([]string(nil), updates...)
	}
}

// waitFeishu waits up to d for ok to hold on what the bot posted and updated.
func waitFeishu(seen func() (int, []string), d time.Duration, ok func(int, []string) bool) (int, []string) {
	deadline := time.Now().Add(d)
	for {
		p, u := seen()
		if ok(p, u) || time.Now().After(deadline) {
			return p, u
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitCardKept waits until a card of ft's Feishu bot has its message id: the post is seen by the fake before that, and a
// node that detaches earlier finds a card with no message to close, which is then closed as answered ("Handled")
// when its post returns, not as "Ended".
func waitCardKept(t *testing.T, ft *fleetTest) {
	t.Helper()
	waitFor(t, "the Feishu card's message id kept", func() bool {
		ft.s.fbot.mu.Lock()
		defer ft.s.fbot.mu.Unlock()
		for _, c := range ft.s.fbot.cards {
			if c.MessageID != "" {
				return true
			}
		}
		return false
	})
}

// The node's connection drops and comes back while the agent still waits on the same dialog: the Feishu card closed
// as "Ended" asks it again, in the same message, not as a second card (#4583).
func TestFeishuCardBackAfterTheNodeReconnects(t *testing.T) {
	ft, n := newCardFleet(t, false)
	seen := feishuCardsOn(ft)
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	if p, _ := waitFeishu(seen, 2*time.Second, func(p int, _ []string) bool { return p == 1 }); p != 1 {
		t.Fatalf("first card: %d posts", p)
	}
	waitCardKept(t, ft)
	s := ft.s
	s.NodeDetached("mac") // the session still shows blocked: the card comes back at once
	if back, _ := waitCards(s, 1, 2*time.Second); len(back) != 1 {
		t.Fatalf("the card did not come back: %v", back)
	}
	posts, updates := waitFeishu(seen, 2*time.Second, func(_ int, u []string) bool { return len(u) >= 2 })
	if len(updates) < 2 || !strings.Contains(updates[0], "已结束") {
		t.Fatalf("the Feishu card was not closed, then asked again: %d posts, updates %q", posts, updates)
	}
	if last := updates[len(updates)-1]; strings.Contains(last, "已结束") || !strings.Contains(last, "gpt-6") {
		t.Fatalf("the Feishu card does not ask again: %s", last)
	}
	if posts != 1 {
		t.Fatalf("a second Feishu card: %d posts", posts)
	}
}

// The agent moved on while its node was away: the card does not come back, and its Feishu card stays "Ended".
func TestFeishuCardThatEndedWhileTheNodeWasAwayStaysClosed(t *testing.T) {
	ft, n := newCardFleet(t, false)
	seen := feishuCardsOn(ft)
	n.setScreen(pickModel)
	blockSession(ft, proto.Session{SessionID: "s1", Agent: "claude"})
	if p, _ := waitFeishu(seen, 2*time.Second, func(p int, _ []string) bool { return p == 1 }); p != 1 {
		t.Fatalf("first card: %d posts", p)
	}
	waitCardKept(t, ft)
	ft.d.setSessions("mac", []proto.Session{{SessionID: "s1", Agent: "claude", Status: proto.StatusUnknown}})
	ft.s.NodeDetached("mac")
	ft.sessions("mac", proto.Session{SessionID: "s1", Agent: "claude", Status: proto.StatusWorking}) // back, moved on
	posts, updates := waitFeishu(seen, 500*time.Millisecond, func(_ int, u []string) bool { return len(u) >= 2 })
	if posts != 1 || len(updates) != 1 || !strings.Contains(updates[0], "已结束") {
		t.Fatalf("an ended card was asked again: %d posts, updates %q", posts, updates)
	}
}
